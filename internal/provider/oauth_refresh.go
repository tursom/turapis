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

// OAuthTokensJSON 统一解析 OAuth 凭证 JSON，兼容两种格式：
//
//	旧格式: {"tokens": {"access_token": "...", ...}}
//	新格式: {"email":"...", "credential": {"tokens": {"access_token": "...", ...}}, ...}
//
// 返回 tokens map 和从顶层或 credential 层解析后的 access_token。
func OAuthTokensJSON(apiKey string) (tokens map[string]interface{}, accessToken string) {
	var creds map[string]interface{}
	if json.Unmarshal([]byte(apiKey), &creds) != nil {
		return nil, ""
	}

	// 旧格式：tokens 在顶层
	if t, ok := creds["tokens"].(map[string]interface{}); ok {
		if at, ok := t["access_token"].(string); ok && at != "" {
			return t, at
		}
		return t, ""
	}

	// 新格式(codex-login)：credential.tokens
	if cr, ok := creds["credential"].(map[string]interface{}); ok {
		if t, ok := cr["tokens"].(map[string]interface{}); ok {
			if at, ok := t["access_token"].(string); ok && at != "" {
				return t, at
			}
			return t, ""
		}
	}

	return nil, ""
}

// ExtractOAuthAccessToken 从 OAuth 凭证 JSON 中提取 access_token，兼容新旧格式。
func ExtractOAuthAccessToken(apiKey string) string {
	_, at := OAuthTokensJSON(apiKey)
	return at
}

// NormalizeOAuthCredential 将任意 OAuth 凭证 JSON 规范化为旧格式 {"tokens":{...}}。
// 如果输入不是 JSON 或解析失败，返回空字符串。
func NormalizeOAuthCredential(apiKey string) string {
	var creds map[string]interface{}
	if json.Unmarshal([]byte(apiKey), &creds) != nil {
		return ""
	}

	// 已经是旧格式
	if _, ok := creds["tokens"]; ok {
		return apiKey
	}

	// 新格式：提取 credential
	if cr, ok := creds["credential"].(map[string]interface{}); ok {
		normalized, _ := json.Marshal(cr)
		return string(normalized)
	}

	return ""
}

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

func RefreshCodexToken(store TokenRefresherStore, providerID int, proxyURL string, updater ProviderKeyUpdater) error {
	return refreshCodexToken(store, providerID, proxyURL, updater)
}

type TokenRefresherStore interface {
	GetProviderAPIKey(id int) (string, error)
	UpdateProviderAPIKey(id int, apiKey string) error
}

func refreshCodexToken(store TokenRefresherStore, providerID int, proxyURL string, updater ProviderKeyUpdater) error {
	apiKeyRaw, err := store.GetProviderAPIKey(providerID)
	if err != nil {
		return fmt.Errorf("get provider api key: %w", err)
	}

	isNewFormat := strings.Contains(apiKeyRaw, `"credential"`)

	// 统一规范化为旧格式 {"tokens":{...}} 以便解析
	apiKey := NormalizeOAuthCredential(apiKeyRaw)
	if apiKey == "" {
		apiKey = apiKeyRaw
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

	newTokens := map[string]interface{}{
		"access_token":  result.AccessToken,
		"refresh_token": creds.Tokens.RefreshToken,
		"id_token":      creds.Tokens.IDToken,
		"client_id":     clientID,
		"expires_at":    time.Now().Add(time.Duration(result.ExpiresIn) * time.Second).UnixMilli(),
	}
	if result.RefreshToken != "" {
		newTokens["refresh_token"] = result.RefreshToken
	}
	if result.IDToken != "" {
		newTokens["id_token"] = result.IDToken
	}

	// 统一使用新版 codex-login 格式存储
	var newAPIKeyData map[string]interface{}
	if isNewFormat {
		// 保持原有 email/account_id/user_id/plan_type，只更新 credential
		var orig map[string]interface{}
		json.Unmarshal([]byte(apiKeyRaw), &orig)
		orig["credential"] = map[string]interface{}{"tokens": newTokens}
		newAPIKeyData = orig
	} else {
		// 旧格式迁移到新格式（email 等信息暂缺）
		newAPIKeyData = map[string]interface{}{
			"credential": map[string]interface{}{"tokens": newTokens},
		}
	}

	newAPIKey, _ := json.Marshal(newAPIKeyData)
	if err := store.UpdateProviderAPIKey(providerID, string(newAPIKey)); err != nil {
		return fmt.Errorf("update provider api key: %w", err)
	}

	if updater != nil {
		if err := updater.SetProviderAPIKey(providerID, result.AccessToken); err != nil {
			slog.Warn("set_provider_api_key_in_memory_failed", "provider_id", providerID, "error", err)
		}
	}

	slog.Info("oauth_token_refreshed", "provider_id", providerID,
		"expires_in", result.ExpiresIn)
	return nil
}
