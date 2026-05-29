package provider

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

var codexTokenURL = "https://auth.openai.com/oauth/token"

var codexRefreshLocks sync.Map

var newCodexTokenHTTPClient = func(proxyURL string) *http.Client {
	transport := SharedTransport()
	if proxyURL != "" {
		transport = NewTransportWithProxy(proxyURL)
	}
	return &http.Client{Timeout: 15 * time.Second, Transport: transport}
}

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

// ExtractOAuthAccountID extracts the ChatGPT account id from OAuth credentials.
// It supports both codex-login output and the stored credential formats.
func ExtractOAuthAccountID(apiKey string) string {
	var creds map[string]interface{}
	if json.Unmarshal([]byte(apiKey), &creds) != nil {
		return ""
	}
	if accountID := stringValue(creds["account_id"]); accountID != "" {
		return accountID
	}
	if cr, ok := creds["credential"].(map[string]interface{}); ok {
		if t, ok := cr["tokens"].(map[string]interface{}); ok {
			if accountID := stringValue(t["account_id"]); accountID != "" {
				return accountID
			}
		}
	}
	if t, ok := creds["tokens"].(map[string]interface{}); ok {
		return stringValue(t["account_id"])
	}
	return ""
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

func getProviderRefreshLock(providerID int) *sync.Mutex {
	v, _ := codexRefreshLocks.LoadOrStore(providerID, &sync.Mutex{})
	return v.(*sync.Mutex)
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

type tokenRefresherCASStore interface {
	TokenRefresherStore
	UpdateProviderAPIKeyIfCurrent(id int, currentAPIKey string, newAPIKey string) (bool, error)
}

func refreshCodexToken(store TokenRefresherStore, providerID int, proxyURL string, updater ProviderKeyUpdater) error {
	mu := getProviderRefreshLock(providerID)
	mu.Lock()
	defer mu.Unlock()

	apiKeyRaw, err := store.GetProviderAPIKey(providerID)
	if err != nil {
		return fmt.Errorf("get provider api key: %w", err)
	}

	_, tokens, _, err := parseOAuthCredential(apiKeyRaw)
	if err != nil {
		return err
	}

	accessToken := stringValue(tokens["access_token"])
	refreshToken := stringValue(tokens["refresh_token"])
	idToken := stringValue(tokens["id_token"])
	clientID := stringValue(tokens["client_id"])
	if refreshToken == "" {
		return fmt.Errorf("no refresh_token in credential")
	}

	if clientID == "" {
		clientID = extractClientID(accessToken)
	}
	if clientID == "" {
		return fmt.Errorf("cannot extract client_id from access_token")
	}

	form := url.Values{
		"client_id":     {clientID},
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"scope":         {"openid profile email"},
	}
	req, err := http.NewRequest("POST", codexTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("create refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	client := newCodexTokenHTTPClient(proxyURL)
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

	newRefreshToken := refreshToken
	if result.RefreshToken != "" {
		newRefreshToken = result.RefreshToken
	}
	newIDToken := idToken
	if result.IDToken != "" {
		newIDToken = result.IDToken
	}

	newAPIKey, err := buildRefreshedOAuthCredential(store, providerID, refreshToken, result.AccessToken, newRefreshToken, newIDToken, clientID, result.ExpiresIn)
	if err != nil {
		return err
	}
	if err := updateProviderAPIKeyCAS(store, providerID, newAPIKey, refreshToken); err != nil {
		return fmt.Errorf("update provider api key: %w", err)
	}

	if updater != nil {
		if err := updater.SetProviderAPIKey(providerID, result.AccessToken); err != nil {
			return fmt.Errorf("set provider api key in memory: %w", err)
		}
	}

	slog.Info("oauth_token_refreshed", "provider_id", providerID,
		"expires_in", result.ExpiresIn)
	return nil
}

func buildRefreshedOAuthCredential(store TokenRefresherStore, providerID int, usedRefreshToken, accessToken, refreshToken, idToken, clientID string, expiresIn int) (string, error) {
	apiKeyRaw, err := store.GetProviderAPIKey(providerID)
	if err != nil {
		return "", fmt.Errorf("get provider api key: %w", err)
	}
	root, tokens, isNewFormat, err := parseOAuthCredential(apiKeyRaw)
	if err != nil {
		return "", err
	}
	currentRefreshToken := stringValue(tokens["refresh_token"])
	if currentRefreshToken != "" && currentRefreshToken != usedRefreshToken {
		return "", fmt.Errorf("oauth credential changed during refresh")
	}

	mergedTokens := cloneMap(tokens)
	mergedTokens["access_token"] = accessToken
	mergedTokens["refresh_token"] = refreshToken
	if idToken != "" {
		mergedTokens["id_token"] = idToken
	}
	mergedTokens["client_id"] = clientID
	mergedTokens["expires_at"] = time.Now().Add(time.Duration(expiresIn) * time.Second).UnixMilli()

	if isNewFormat {
		credential, _ := root["credential"].(map[string]interface{})
		if credential == nil {
			credential = map[string]interface{}{}
		}
		credential["tokens"] = mergedTokens
		root["credential"] = credential
	} else {
		delete(root, "tokens")
		root["credential"] = map[string]interface{}{"tokens": mergedTokens}
	}

	newAPIKey, err := json.Marshal(root)
	if err != nil {
		return "", fmt.Errorf("marshal credential: %w", err)
	}
	return string(newAPIKey), nil
}

func updateProviderAPIKeyCAS(store TokenRefresherStore, providerID int, newAPIKey string, usedRefreshToken string) error {
	casStore, ok := store.(tokenRefresherCASStore)
	if !ok {
		return store.UpdateProviderAPIKey(providerID, newAPIKey)
	}
	for attempt := 0; attempt < 5; attempt++ {
		current, err := casStore.GetProviderAPIKey(providerID)
		if err != nil {
			return err
		}
		rebuilt, err := mergeRefreshedCredentialIntoCurrent(current, newAPIKey, usedRefreshToken)
		if err != nil {
			return err
		}
		updated, err := casStore.UpdateProviderAPIKeyIfCurrent(providerID, current, rebuilt)
		if err != nil {
			return err
		}
		if updated {
			return nil
		}
	}
	return fmt.Errorf("provider api key changed too many times")
}

func mergeRefreshedCredentialIntoCurrent(currentAPIKey, refreshedAPIKey string, usedRefreshToken string) (string, error) {
	currentRoot, currentTokens, currentIsNew, err := parseOAuthCredential(currentAPIKey)
	if err != nil {
		return "", err
	}
	currentRefreshToken := stringValue(currentTokens["refresh_token"])
	if currentRefreshToken != "" && currentRefreshToken != usedRefreshToken {
		return "", fmt.Errorf("oauth credential changed during refresh")
	}
	_, refreshedTokens, _, err := parseOAuthCredential(refreshedAPIKey)
	if err != nil {
		return "", err
	}
	mergedTokens := cloneMap(currentTokens)
	for _, key := range []string{"access_token", "refresh_token", "id_token", "client_id", "expires_at"} {
		if value, ok := refreshedTokens[key]; ok {
			mergedTokens[key] = value
		}
	}
	if currentIsNew {
		credential, _ := currentRoot["credential"].(map[string]interface{})
		if credential == nil {
			credential = map[string]interface{}{}
		}
		credential["tokens"] = mergedTokens
		currentRoot["credential"] = credential
	} else {
		delete(currentRoot, "tokens")
		currentRoot["credential"] = map[string]interface{}{"tokens": mergedTokens}
	}
	b, err := json.Marshal(currentRoot)
	if err != nil {
		return "", fmt.Errorf("marshal credential: %w", err)
	}
	return string(b), nil
}

func parseOAuthCredential(apiKey string) (root map[string]interface{}, tokens map[string]interface{}, isNewFormat bool, err error) {
	if json.Unmarshal([]byte(apiKey), &root) != nil {
		return nil, nil, false, fmt.Errorf("invalid oauth credential json")
	}
	if t, ok := root["tokens"].(map[string]interface{}); ok {
		return root, t, false, nil
	}
	if cr, ok := root["credential"].(map[string]interface{}); ok {
		if t, ok := cr["tokens"].(map[string]interface{}); ok {
			return root, t, true, nil
		}
		return nil, nil, true, fmt.Errorf("invalid oauth credential json")
	}
	return nil, nil, false, fmt.Errorf("invalid oauth credential json")
}

func cloneMap(in map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func stringValue(v interface{}) string {
	s, _ := v.(string)
	return s
}

func OAuthAccessTokenExpiresAt(apiKey string) (time.Time, bool) {
	tokens, _ := OAuthTokensJSON(apiKey)
	if tokens == nil {
		return time.Time{}, false
	}
	raw, ok := tokens["expires_at"]
	if !ok {
		return time.Time{}, false
	}

	var n int64
	switch v := raw.(type) {
	case float64:
		n = int64(v)
	case int64:
		n = v
	case int:
		n = int64(v)
	case json.Number:
		parsed, err := v.Int64()
		if err != nil {
			return time.Time{}, false
		}
		n = parsed
	case string:
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			return t, true
		}
		parsed, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return time.Time{}, false
		}
		n = parsed
	default:
		return time.Time{}, false
	}
	if n <= 0 {
		return time.Time{}, false
	}
	if n > 1_000_000_000_000 {
		return time.UnixMilli(n), true
	}
	return time.Unix(n, 0), true
}

func OAuthAccessTokenExpiresSoon(apiKey string, now time.Time, skew time.Duration) bool {
	expiresAt, ok := OAuthAccessTokenExpiresAt(apiKey)
	return ok && !expiresAt.After(now.Add(skew))
}
