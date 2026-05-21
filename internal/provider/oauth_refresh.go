package provider

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const codexTokenURL = "https://auth.openai.com/oauth/token"

type tokenRefreshResult struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	ExpiresIn    int    `json:"expires_in"`
}

func extractClientID(accessToken string) string {
	parts := strings.Split(accessToken, ".")
	if len(parts) < 2 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		ClientID string `json:"client_id"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return ""
	}
	return claims.ClientID
}

func RefreshCodexToken(store TokenRefresherStore, providerID int, proxyURL string) error {
	return refreshCodexToken(store, providerID, proxyURL)
}

type TokenRefresherStore interface {
	GetProviderAPIKey(id int) (string, error)
	UpdateProviderAPIKey(id int, apiKey string) error
}

func refreshCodexToken(store TokenRefresherStore, providerID int, proxyURL string) error {
	apiKey, err := store.GetProviderAPIKey(providerID)
	if err != nil {
		return fmt.Errorf("get provider api key: %w", err)
	}

	var creds struct {
		Tokens struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			IDToken      string `json:"id_token"`
			ClientID     string `json:"client_id"`
			ExpiresAt    int64  `json:"expires_at"`
		} `json:"tokens"`
	}
	if json.Unmarshal([]byte(apiKey), &creds) != nil {
		return fmt.Errorf("invalid oauth credential json")
	}

	if creds.Tokens.RefreshToken == "" {
		return fmt.Errorf("no refresh_token in credential")
	}

	clientID := creds.Tokens.ClientID
	if clientID == "" {
		clientID = extractClientID(creds.Tokens.AccessToken)
	}
	if clientID == "" {
		return fmt.Errorf("cannot extract client_id from access_token")
	}

	form := url.Values{
		"client_id":     {clientID},
		"grant_type":    {"refresh_token"},
		"refresh_token": {creds.Tokens.RefreshToken},
		"scope":         {"openid profile email"},
	}
	req, err := http.NewRequest("POST", codexTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("create refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	transport := SharedTransport()
	if proxyURL != "" {
		transport = NewTransportWithProxy(proxyURL)
	}
	client := &http.Client{Timeout: 15 * time.Second, Transport: transport}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("refresh token request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 65536))
	if resp.StatusCode != 200 {
		return fmt.Errorf("refresh token: http %d: %s", resp.StatusCode, string(body))
	}

	var result tokenRefreshResult
	if json.Unmarshal(body, &result) != nil {
		return fmt.Errorf("parse refresh response: %s", string(body))
	}
	if result.AccessToken == "" {
		return fmt.Errorf("refresh response missing access_token")
	}

	newCreds := map[string]interface{}{
		"tokens": map[string]interface{}{
			"access_token":  result.AccessToken,
			"refresh_token": creds.Tokens.RefreshToken,
			"id_token":      creds.Tokens.IDToken,
			"client_id":     clientID,
			"expires_at":    time.Now().Add(time.Duration(result.ExpiresIn) * time.Second).UnixMilli(),
		},
	}
	if result.RefreshToken != "" {
		newCreds["tokens"].(map[string]interface{})["refresh_token"] = result.RefreshToken
	}
	if result.IDToken != "" {
		newCreds["tokens"].(map[string]interface{})["id_token"] = result.IDToken
	}

	newAPIKey, _ := json.Marshal(newCreds)
	if err := store.UpdateProviderAPIKey(providerID, string(newAPIKey)); err != nil {
		return fmt.Errorf("update provider api key: %w", err)
	}

	slog.Info("oauth_token_refreshed", "provider_id", providerID,
		"expires_in", result.ExpiresIn)
	return nil
}
