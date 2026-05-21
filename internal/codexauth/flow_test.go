package codexauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tursom/turapis/internal/email"
)

// TestGeneratePKCE 验证生成的 verifier 和 challenge 均为 43 字符的 base64url 编码。
func TestGeneratePKCE(t *testing.T) {
	verifier, challenge, err := generatePKCE()
	if err != nil {
		t.Fatalf("generatePKCE failed: %v", err)
	}
	if len(verifier) == 0 {
		t.Error("verifier should not be empty")
	}
	if len(challenge) == 0 {
		t.Error("challenge should not be empty")
	}

	// 32 bytes → 43 chars base64url without padding
	if len(verifier) != 43 {
		t.Errorf("verifier length = %d, want 43", len(verifier))
	}
	if len(challenge) != 43 {
		t.Errorf("challenge length = %d, want 43", len(challenge))
	}

	// PKCE is non-deterministic, each call should differ
	v2, c2, _ := generatePKCE()
	if verifier == v2 {
		t.Error("consecutive verifiers should differ")
	}
	if challenge == c2 {
		t.Error("consecutive challenges should differ")
	}
}

// TestBuildAuthorizeURL 验证授权 URL 包含正确的参数。
func TestBuildAuthorizeURL(t *testing.T) {
	challenge := "challenge123"
	state := "state456"
	redirectURI := "http://localhost:1455/auth/callback"

	authURL := buildAuthorizeURL(challenge, state, redirectURI)

	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("invalid URL: %v", err)
	}

	params := parsed.Query()
	if params.Get("client_id") != codexClientID {
		t.Errorf("client_id = %s, want %s", params.Get("client_id"), codexClientID)
	}
	if params.Get("redirect_uri") != redirectURI {
		t.Errorf("redirect_uri = %s, want %s", params.Get("redirect_uri"), redirectURI)
	}
	if params.Get("response_type") != "code" {
		t.Errorf("response_type = %s, want code", params.Get("response_type"))
	}
	if params.Get("scope") != "openid profile email offline_access" {
		t.Errorf("scope = %s", params.Get("scope"))
	}
	if params.Get("state") != state {
		t.Errorf("state = %s, want %s", params.Get("state"), state)
	}
	if params.Get("code_challenge") != challenge {
		t.Errorf("code_challenge = %s, want %s", params.Get("code_challenge"), challenge)
	}
	if params.Get("code_challenge_method") != "S256" {
		t.Errorf("code_challenge_method = %s, want S256", params.Get("code_challenge_method"))
	}

	if parsed.Scheme != "https" {
		t.Errorf("scheme = %s, want https", parsed.Scheme)
	}
	if parsed.Host != "auth.openai.com" {
		t.Errorf("host = %s, want auth.openai.com", parsed.Host)
	}
}

// TestExtractAccountIdentity 使用手工构造的 JWT 验证身份解析。
func TestExtractAccountIdentity(t *testing.T) {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))

	exp := time.Now().Add(1 * time.Hour).Unix()
	payloadJSON := fmt.Sprintf(
		`{"email":"user@example.com","sub":"acc_12345","user_id":"user_67890","https://api.openai.com/plan_type":"pro","exp":%d,"iss":"https://auth.openai.com","aud":"app_EMoamEEZ73f0CkXaXp7hrann"}`,
		exp,
	)
	payload := base64.RawURLEncoding.EncodeToString([]byte(payloadJSON))

	signature := base64.RawURLEncoding.EncodeToString([]byte("fake-signature"))

	idToken := strings.Join([]string{header, payload, signature}, ".")

	identity, err := extractAccountIdentity(idToken)
	if err != nil {
		t.Fatalf("extractAccountIdentity failed: %v", err)
	}

	if identity.Email != "user@example.com" {
		t.Errorf("Email = %s, want user@example.com", identity.Email)
	}
	if identity.AccountID != "acc_12345" {
		t.Errorf("AccountID = %s, want acc_12345", identity.AccountID)
	}
	if identity.UserID != "user_67890" {
		t.Errorf("UserID = %s, want user_67890", identity.UserID)
	}
	if identity.PlanType != "pro" {
		t.Errorf("PlanType = %s, want pro", identity.PlanType)
	}
}

// TestExtractAccountIdentity_InvalidJWT 验证对无效 JWT 返回错误。
func TestExtractAccountIdentity_InvalidJWT(t *testing.T) {
	futureExp := time.Now().Add(1 * time.Hour).Unix()
	pastExp := time.Now().Add(-1 * time.Hour).Unix()

	validPayload := fmt.Sprintf(`{"email":"x@x","sub":"s","user_id":"u","exp":%d,"iss":"https://auth.openai.com","aud":"app_EMoamEEZ73f0CkXaXp7hrann"}`, futureExp)
	validHeader := `{"alg":"RS256","typ":"JWT"}`

	makeJWT := func(header, payload string) string {
		h := base64.RawURLEncoding.EncodeToString([]byte(header))
		p := base64.RawURLEncoding.EncodeToString([]byte(payload))
		s := base64.RawURLEncoding.EncodeToString([]byte("sig"))
		return strings.Join([]string{h, p, s}, ".")
	}
	expiredPayload := fmt.Sprintf(`{"email":"x@x","sub":"s","user_id":"u","exp":%d,"iss":"https://auth.openai.com","aud":"app_EMoamEEZ73f0CkXaXp7hrann"}`, pastExp)
	wrongIssuerPayload := fmt.Sprintf(`{"email":"x@x","sub":"s","user_id":"u","exp":%d,"iss":"https://evil.com","aud":"app_EMoamEEZ73f0CkXaXp7hrann"}`, futureExp)
	wrongAudPayload := fmt.Sprintf(`{"email":"x@x","sub":"s","user_id":"u","exp":%d,"iss":"https://auth.openai.com","aud":"wrong_client"}`, futureExp)
	noExpPayload := `{"email":"x@x","sub":"s","user_id":"u","iss":"https://auth.openai.com","aud":"app_EMoamEEZ73f0CkXaXp7hrann"}`
	algNoneHeader := `{"alg":"none","typ":"JWT"}`

	tests := []struct {
		name    string
		jwt     string
		wantErr bool
	}{
		{"empty", "", true},
		{"no dots", "not-a-jwt", true},
		{"bad base64 payload", "header.!@#$.sig", true},
		{"expired", makeJWT(validHeader, expiredPayload), true},
		{"wrong issuer", makeJWT(validHeader, wrongIssuerPayload), true},
		{"wrong audience", makeJWT(validHeader, wrongAudPayload), true},
		{"missing exp", makeJWT(validHeader, noExpPayload), true},
		{"alg none", makeJWT(algNoneHeader, validPayload), true},
		{"valid", makeJWT(validHeader, validPayload), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := extractAccountIdentity(tt.jwt)
			if (err != nil) != tt.wantErr {
				t.Errorf("extractAccountIdentity(%q) error = %v, wantErr = %v", tt.name, err, tt.wantErr)
			}
		})
	}
}

// TestTokenSetToCredentialJSON 验证输出 JSON 格式匹配 oauth_refresh.go 的期望。
func TestTokenSetToCredentialJSON(t *testing.T) {
	accessJWT := makeAccessJWT(`{"sub":"s","client_id":"app_EMoamEEZ73f0CkXaXp7hrann","iat":1}`)
	ts := &TokenSet{
		AccessToken:  accessJWT,
		RefreshToken: "rt-def",
		IDToken:      "id-ghi",
		AccountID:    "acc_123",
		ExpiresAt:    1716249600000,
	}

	raw, err := TokenSetToCredentialJSON(ts)
	if err != nil {
		t.Fatalf("TokenSetToCredentialJSON failed: %v", err)
	}

	var result struct {
		Tokens struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			IDToken      string `json:"id_token"`
			AccountID    string `json:"account_id"`
			ClientID     string `json:"client_id"`
			ExpiresAt    int64  `json:"expires_at"`
		} `json:"tokens"`
	}

	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("unmarshal credential JSON: %v", err)
	}

	if result.Tokens.AccessToken != accessJWT {
		t.Errorf("access_token = %s", result.Tokens.AccessToken)
	}
	if result.Tokens.RefreshToken != "rt-def" {
		t.Errorf("refresh_token = %s", result.Tokens.RefreshToken)
	}
	if result.Tokens.IDToken != "id-ghi" {
		t.Errorf("id_token = %s", result.Tokens.IDToken)
	}
	if result.Tokens.AccountID != "acc_123" {
		t.Errorf("account_id = %s", result.Tokens.AccountID)
	}
	if result.Tokens.ClientID != "app_EMoamEEZ73f0CkXaXp7hrann" {
		t.Errorf("client_id = %s", result.Tokens.ClientID)
	}
	if result.Tokens.ExpiresAt != 1716249600000 {
		t.Errorf("expires_at = %d", result.Tokens.ExpiresAt)
	}
}

// makeAccessJWT 构造一个包含 claims 的无签 JWT，用于测试。
func makeAccessJWT(claimsJSON string) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	p := base64.RawURLEncoding.EncodeToString([]byte(claimsJSON))
	s := base64.RawURLEncoding.EncodeToString([]byte("sig"))
	return strings.Join([]string{h, p, s}, ".")
}

// TestEmailCredentialRoundTrip 验证序列化/反序列化一致性。
func TestEmailCredentialRoundTrip(t *testing.T) {
	original := &EmailCredential{
		Email:     "test@mock.test",
		Provider:  "mock",
		Token:     "token-abc",
		UpdatedAt: "2025-01-01T00:00:00Z",
	}

	raw, err := EmailCredentialToJSON(original)
	if err != nil {
		t.Fatalf("EmailCredentialToJSON failed: %v", err)
	}

	restored, err := EmailCredentialFromJSON(raw)
	if err != nil {
		t.Fatalf("EmailCredentialFromJSON failed: %v", err)
	}

	if restored.Email != original.Email {
		t.Errorf("Email = %s, want %s", restored.Email, original.Email)
	}
	if restored.Provider != original.Provider {
		t.Errorf("Provider = %s, want %s", restored.Provider, original.Provider)
	}
	if restored.Token != original.Token {
		t.Errorf("Token = %s, want %s", restored.Token, original.Token)
	}
	if restored.UpdatedAt != original.UpdatedAt {
		t.Errorf("UpdatedAt = %s, want %s", restored.UpdatedAt, original.UpdatedAt)
	}
}

// TestEmailCredentialFromJSON_Invalid 验证对无效 JSON 返回错误。
func TestEmailCredentialFromJSON_Invalid(t *testing.T) {
	_, err := EmailCredentialFromJSON([]byte(`{invalid}`))
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

// TestDefaultFlowConfig 验证默认配置的各项默认值。
func TestDefaultFlowConfig(t *testing.T) {
	cfg := DefaultFlowConfig()

	if cfg.CallbackPort != 1455 {
		t.Errorf("CallbackPort = %d, want 1455", cfg.CallbackPort)
	}
	if cfg.PollInterval != 5*time.Second {
		t.Errorf("PollInterval = %v, want 5s", cfg.PollInterval)
	}
	if cfg.PollTimeout != 120*time.Second {
		t.Errorf("PollTimeout = %v, want 120s", cfg.PollTimeout)
	}
	if cfg.BrowserTimeout != 120*time.Second {
		t.Errorf("BrowserTimeout = %v, want 120s", cfg.BrowserTimeout)
	}
}

// TestNewAutoLoginFlow 验证构造函数正确保存配置。
func TestNewAutoLoginFlow(t *testing.T) {
	cfg := FlowConfig{
		CallbackPort:   9999,
		PollInterval:   10 * time.Second,
		PollTimeout:    60 * time.Second,
		BrowserTimeout: 30 * time.Second,
	}

	flow := NewAutoLoginFlow(cfg)

	if flow.cfg.CallbackPort != 9999 {
		t.Errorf("CallbackPort = %d, want 9999", flow.cfg.CallbackPort)
	}
	if flow.cfg.PollInterval != 10*time.Second {
		t.Errorf("PollInterval = %v, want 10s", flow.cfg.PollInterval)
	}
}

// TestGenerateState 验证 state 生成的非空性和随机性。
func TestGenerateState(t *testing.T) {
	s1, err := generateState()
	if err != nil {
		t.Fatalf("generateState failed: %v", err)
	}
	if len(s1) == 0 {
		t.Error("state should not be empty")
	}

	s2, _ := generateState()
	if s1 == s2 {
		t.Error("consecutive states should differ")
	}
}

// TestTokenSetToCredentialJSON_Empty 验证空的 / 无效 access_token 返回错误。
func TestTokenSetToCredentialJSON_Empty(t *testing.T) {
	tests := []struct {
		name    string
		ts      *TokenSet
		wantErr bool
	}{
		{"empty tokens", &TokenSet{}, true},
		{"non-jwt access_token", &TokenSet{AccessToken: "not-a-jwt"}, true},
		{"jwt without client_id", &TokenSet{AccessToken: jwtWithoutClientID()}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := TokenSetToCredentialJSON(tt.ts)
			if (err != nil) != tt.wantErr {
				t.Errorf("TokenSetToCredentialJSON(%s) error = %v, wantErr = %v", tt.name, err, tt.wantErr)
			}
		})
	}
}

func jwtWithoutClientID() string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	p := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"s","email":"e","exp":9999999999}`))
	s := base64.RawURLEncoding.EncodeToString([]byte("sig"))
	return strings.Join([]string{h, p, s}, ".")
}

// ──────────────────── moq 辅助构造器 ────────────────────

// newBrowserClientMock 返回一个所有操作均为空操作的 BrowserClientMock。
func newBrowserClientMock() *BrowserClientMock {
	return &BrowserClientMock{
		NewContextFunc: func(ctx context.Context) (context.Context, context.CancelFunc) {
			return context.WithCancel(ctx)
		},
		NavigateFunc:        func(_ context.Context, _ string) error { return nil },
		WaitForSelectorFunc: func(_ context.Context, _ string) error { return nil },
		SendKeysFunc:        func(_ context.Context, _, _ string) error { return nil },
		ClickFunc:           func(_ context.Context, _ string) error { return nil },
	}
}

// newEmailProviderMock 返回一个预配置的 EmailProviderMock，
// 邮箱地址为 test@mock.test，含验证链接和 6 位验证码（123456）。
func newEmailProviderMock() *EmailProviderMock {
	providerName := "mock"
	mockEmail := &email.InboxInfo{
		Address:  "test@mock.test",
		Provider: providerName,
		Token:    "mock-token-123",
	}
	msg := email.EmailMessage{
		ID:      "msg-1",
		From:    "noreply@openai.com",
		To:      mockEmail.Address,
		Subject: "Verify your email",
		Body:    "Your verification link: https://auth.openai.com/verify?link=abc123\nYour verification code: 123456",
		HTML:    "<a href='https://auth.openai.com/verify?link=abc123'>Verify</a> <span>123456</span>",
		Date:    "2025-01-01T00:00:00Z",
	}

	return &EmailProviderMock{
		CreateInboxFunc: func(_ context.Context) (*email.InboxInfo, error) {
			return mockEmail, nil
		},
		CreateInboxWithAliasFunc: func(_ context.Context, alias, domain string) (*email.InboxInfo, error) {
			return &email.InboxInfo{Address: alias + "@" + domain, Provider: providerName}, nil
		},
		GetMessagesFunc: func(_ context.Context, _ *email.InboxInfo) ([]email.EmailMessage, error) {
			return []email.EmailMessage{msg}, nil
		},
		GetMessageFunc: func(_ context.Context, _ *email.InboxInfo, messageID string) (*email.EmailMessage, error) {
			return &email.EmailMessage{ID: messageID, From: "noreply@openai.com", Subject: "Verify", Body: "123456"}, nil
		},
		WaitForEmailFunc: func(_ context.Context, inbox *email.InboxInfo, _ time.Duration, predicate func(*email.EmailMessage) bool) (*email.EmailMessage, error) {
			// 委托给 GetMessages 的内联逻辑
			if predicate(&msg) {
				return &msg, nil
			}
			return nil, nil
		},
		SupportsReuseFunc: func() bool { return false },
		NameFunc:          func() string { return providerName },
	}
}

// ──────────────────── 集成测试（已移除冗余 mock 类型，改用 moq） ────────────────────

// newCaptureBrowser 返回一个 BrowserClientMock，其 NavigateFunc 会将
// 每次导航的 URL 追加到 captured 切片中。
func newCaptureBrowser(captured *[]string) *BrowserClientMock {
	mock := newBrowserClientMock()
	mock.NavigateFunc = func(_ context.Context, url string) error {
		*captured = append(*captured, url)
		return nil
	}
	return mock
}

// TestRunRelogin 集成测试：使用 mock EmailProvider + mock Browser + 真实 HTTP 回调服务器 +
// httptest 模拟的 token 端点，验证 RunRelogin 完整流程。
func TestRunRelogin(t *testing.T) {
	// 构造一个带有全部安全校验字段的 mock JWT id_token
	exp := time.Now().Add(1 * time.Hour).Unix()
	jwtHeader := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	jwtPayload := base64.RawURLEncoding.EncodeToString([]byte(
		fmt.Sprintf(`{"email":"relogin@test.local","sub":"acc_relogin","user_id":"user_xyz","https://api.openai.com/plan_type":"free","exp":%d,"iss":"https://auth.openai.com","aud":"app_EMoamEEZ73f0CkXaXp7hrann"}`, exp),
	))
	jwtSig := base64.RawURLEncoding.EncodeToString([]byte("fake-signature"))
	mockJWT := strings.Join([]string{jwtHeader, jwtPayload, jwtSig}, ".")

	// mock OAuth token 端点
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"access_token":  "mock_access_token",
			"refresh_token": "mock_refresh_token",
			"id_token":      mockJWT,
			"expires_in":    3600,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer tokenServer.Close()

	const testPort = 18455
	var capturedURLs []string

	cfg := FlowConfig{
		EmailProvider: newEmailProviderMock(),
		BrowserClient: newCaptureBrowser(&capturedURLs),
		CallbackPort:  testPort,
		TokenURL:      tokenServer.URL,
		PollTimeout:   3 * time.Second,
	}

	flow := NewAutoLoginFlow(cfg)
	ec := &EmailCredential{
		Email:     "test@mock.test",
		Provider:  "mock",
		Token:     "mock-token-123",
		UpdatedAt: "2025-01-01T00:00:00Z",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resultCh := make(chan *FlowResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := flow.RunRelogin(ctx, ec)
		if err != nil {
			errCh <- err
			return
		}
		resultCh <- result
	}()

	// 等待 mock browser 完成导航，从授权 URL 中提取 state 参数
	time.Sleep(200 * time.Millisecond)

	var callbackState string
	for _, u := range capturedURLs {
		if strings.Contains(u, "auth.openai.com/authorize") {
			parsed, _ := url.Parse(u)
			callbackState = parsed.Query().Get("state")
			break
		}
	}
	if callbackState == "" {
		t.Fatal("failed to capture state from authorize URL")
	}

	// 模拟 OAuth 重定向：带上正确的 state 和授权码
	callbackURL := fmt.Sprintf("http://127.0.0.1:%d/auth/callback?code=test_auth_code&state=%s", testPort, url.QueryEscape(callbackState))
	resp, err := http.Get(callbackURL)
	if err != nil {
		t.Fatalf("send callback request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("callback response: %d", resp.StatusCode)
	}

	select {
	case result := <-resultCh:
		if result.Tokens == nil {
			t.Fatal("Tokens should not be nil")
		}
		if result.Tokens.AccessToken != "mock_access_token" {
			t.Errorf("AccessToken = %s, want mock_access_token", result.Tokens.AccessToken)
		}
		if result.Tokens.RefreshToken != "mock_refresh_token" {
			t.Errorf("RefreshToken = %s, want mock_refresh_token", result.Tokens.RefreshToken)
		}
		if result.Tokens.IDToken != mockJWT {
			t.Errorf("IDToken mismatch")
		}
		if result.Tokens.AccountID != "acc_relogin" {
			t.Errorf("AccountID = %s, want acc_relogin", result.Tokens.AccountID)
		}
		if result.Identity == nil {
			t.Fatal("Identity should not be nil")
		}
		if result.Identity.Email != "relogin@test.local" {
			t.Errorf("Identity.Email = %s, want relogin@test.local", result.Identity.Email)
		}
		if result.Identity.AccountID != "acc_relogin" {
			t.Errorf("Identity.AccountID = %s, want acc_relogin", result.Identity.AccountID)
		}
		if result.Identity.UserID != "user_xyz" {
			t.Errorf("Identity.UserID = %s, want user_xyz", result.Identity.UserID)
		}
		if result.Identity.PlanType != "free" {
			t.Errorf("Identity.PlanType = %s, want free", result.Identity.PlanType)
		}

	case err := <-errCh:
		t.Fatalf("RunRelogin failed: %v", err)

	case <-ctx.Done():
		t.Fatal("timeout waiting for RunRelogin")
	}
}

// TestRunRelogin_MissingCode 验证当回调服务器收到无效请求时 RunRelogin 正确报错。
func TestRunRelogin_MissingCode(t *testing.T) {
	const testPort = 18456
	var capturedURLs []string

	cfg := FlowConfig{
		EmailProvider: newEmailProviderMock(),
		BrowserClient: newCaptureBrowser(&capturedURLs),
		CallbackPort:  testPort,
		PollTimeout:   3 * time.Second,
	}

	flow := NewAutoLoginFlow(cfg)
	ec := &EmailCredential{
		Email:     "test@mock.test",
		Provider:  "mock",
		Token:     "mock-token-123",
		UpdatedAt: "2025-01-01T00:00:00Z",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		_, err := flow.RunRelogin(ctx, ec)
		errCh <- err
	}()

	time.Sleep(200 * time.Millisecond)

	var callbackState string
	for _, u := range capturedURLs {
		if strings.Contains(u, "auth.openai.com/authorize") {
			parsed, _ := url.Parse(u)
			callbackState = parsed.Query().Get("state")
			break
		}
	}
	if callbackState == "" {
		t.Fatal("failed to capture state from authorize URL")
	}

	// 发送带有效 state 但不含 code 的回调
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/auth/callback?state=%s", testPort, url.QueryEscape(callbackState)))
	if err != nil {
		t.Fatalf("send callback request: %v", err)
	}
	resp.Body.Close()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected error for missing code")
		}
	case <-ctx.Done():
		t.Fatal("timeout waiting for RunRelogin")
	}
}

// ──────────────────── 注册流程 mock ────────────────────

// newEmailProviderMockForRegister 返回一个 EmailProviderMock，
// CreateInbox 返回 "register@mock.test"，WaitForEmail 返回包含验证链接的邮件。
func newEmailProviderMockForRegister() *EmailProviderMock {
	providerName := "mock"
	mockEmail := &email.InboxInfo{
		Address:  "register@mock.test",
		Provider: providerName,
		Token:    "mock-token",
	}
	msg := email.EmailMessage{
		ID:      "msg-1",
		From:    "noreply@openai.com",
		To:      mockEmail.Address,
		Subject: "Verify your email",
		Body:    "Your verification link: https://auth.openai.com/verify?link=abc123",
		HTML:    "<a href='https://auth.openai.com/verify?link=abc123'>Verify</a>",
		Date:    "2025-01-01T00:00:00Z",
	}

	return &EmailProviderMock{
		CreateInboxFunc: func(_ context.Context) (*email.InboxInfo, error) {
			return mockEmail, nil
		},
		CreateInboxWithAliasFunc: func(_ context.Context, alias, domain string) (*email.InboxInfo, error) {
			return &email.InboxInfo{Address: alias + "@" + domain, Provider: providerName}, nil
		},
		GetMessagesFunc: func(_ context.Context, _ *email.InboxInfo) ([]email.EmailMessage, error) {
			return []email.EmailMessage{msg}, nil
		},
		GetMessageFunc: func(_ context.Context, _ *email.InboxInfo, messageID string) (*email.EmailMessage, error) {
			return &email.EmailMessage{ID: messageID, From: "noreply@openai.com", Subject: "Verify", Body: "abc123"}, nil
		},
		WaitForEmailFunc: func(_ context.Context, inbox *email.InboxInfo, _ time.Duration, predicate func(*email.EmailMessage) bool) (*email.EmailMessage, error) {
			if predicate(&msg) {
				return &msg, nil
			}
			return nil, nil
		},
		SupportsReuseFunc: func() bool { return false },
		NameFunc:          func() string { return providerName },
	}
}

// TestRunRegister 集成测试：使用 mock EmailProvider + mock Browser + 真实 HTTP 回调服务器 +
// httptest 模拟的 token 端点，验证 RunRegister 完整流程（注册→验证邮件→OAuth→令牌交换）。
func TestRunRegister(t *testing.T) {
	// 构造 id_token，包含所有 extractAccountIdentity 校验字段
	exp := time.Now().Add(1 * time.Hour).Unix()
	jwtHeader := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	jwtPayload := base64.RawURLEncoding.EncodeToString([]byte(
		fmt.Sprintf(`{"email":"register@mock.test","sub":"acc_register","user_id":"user_reg","https://api.openai.com/plan_type":"pro","exp":%d,"iss":"https://auth.openai.com","aud":"app_EMoamEEZ73f0CkXaXp7hrann"}`, exp),
	))
	jwtSig := base64.RawURLEncoding.EncodeToString([]byte("fake-signature"))
	mockJWT := strings.Join([]string{jwtHeader, jwtPayload, jwtSig}, ".")

	// mock OAuth token 端点
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"access_token":  "mock_at",
			"refresh_token": "mock_rt",
			"id_token":      mockJWT,
			"expires_in":    3600,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer tokenServer.Close()

	const testPort = 19455
	var capturedURLs []string

	cfg := FlowConfig{
		EmailProvider: newEmailProviderMockForRegister(),
		BrowserClient: newCaptureBrowser(&capturedURLs),
		CallbackPort:  testPort,
		TokenURL:      tokenServer.URL,
		PollTimeout:   3 * time.Second,
	}

	flow := NewAutoLoginFlow(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resultCh := make(chan *FlowResult, 1)
	errCh := make(chan error, 1)
	ecCh := make(chan *EmailCredential, 1)
	go func() {
		result, ec, err := flow.RunRegister(ctx)
		if err != nil {
			errCh <- err
			return
		}
		resultCh <- result
		ecCh <- ec
	}()

	// 等待 mock browser 完成导航，从授权 URL 中提取 state 参数
	time.Sleep(200 * time.Millisecond)

	var callbackState string
	for _, u := range capturedURLs {
		if strings.Contains(u, "auth.openai.com/authorize") {
			parsed, _ := url.Parse(u)
			callbackState = parsed.Query().Get("state")
			break
		}
	}
	if callbackState == "" {
		t.Fatal("failed to capture state from authorize URL")
	}

	// 模拟 OAuth 重定向：带上 state 和授权码
	callbackURL := fmt.Sprintf("http://127.0.0.1:%d/auth/callback?code=test_code&state=%s", testPort, url.QueryEscape(callbackState))
	resp, err := http.Get(callbackURL)
	if err != nil {
		t.Fatalf("send callback request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("callback response: %d", resp.StatusCode)
	}

	select {
	case result := <-resultCh:
		// 验证 Tokens
		if result.Tokens == nil {
			t.Fatal("Tokens should not be nil")
		}
		if result.Tokens.AccessToken != "mock_at" {
			t.Errorf("AccessToken = %s, want mock_at", result.Tokens.AccessToken)
		}
		if result.Tokens.RefreshToken != "mock_rt" {
			t.Errorf("RefreshToken = %s, want mock_rt", result.Tokens.RefreshToken)
		}
		if result.Tokens.IDToken != mockJWT {
			t.Errorf("IDToken mismatch")
		}
		if result.Tokens.AccountID != "acc_register" {
			t.Errorf("AccountID = %s, want acc_register", result.Tokens.AccountID)
		}

		// 验证 Identity
		if result.Identity == nil {
			t.Fatal("Identity should not be nil")
		}
		if result.Identity.Email != "register@mock.test" {
			t.Errorf("Identity.Email = %s, want register@mock.test", result.Identity.Email)
		}
		if result.Identity.AccountID != "acc_register" {
			t.Errorf("Identity.AccountID = %s, want acc_register", result.Identity.AccountID)
		}
		if result.Identity.UserID != "user_reg" {
			t.Errorf("Identity.UserID = %s, want user_reg", result.Identity.UserID)
		}
		if result.Identity.PlanType != "pro" {
			t.Errorf("Identity.PlanType = %s, want pro", result.Identity.PlanType)
		}

		// 验证 EmailCredential
		ec := <-ecCh
		if ec == nil {
			t.Fatal("EmailCredential should not be nil")
		}
		if ec.Email != "register@mock.test" {
			t.Errorf("EmailCredential.Email = %s, want register@mock.test", ec.Email)
		}
		if ec.Provider != "mock" {
			t.Errorf("EmailCredential.Provider = %s, want mock", ec.Provider)
		}

	case err := <-errCh:
		t.Fatalf("RunRegister failed: %v", err)

	case <-ctx.Done():
		t.Fatal("timeout waiting for RunRegister")
	}
}

// ──────────────────── 登录流程（场景 B） ────────────────────

// TestRunLogin 集成测试：使用真实 HTTP 回调服务器（RunLogin 内部启动）
// + httptest mock OAuth token 端点，验证 RunLogin 完整流程。
//
// 注意：RunLogin 不依赖 EmailProvider 或 BrowserClient，仅使用
// CallbackPort 和 TokenURL。state 在函数内部随机生成，回调前不可见。
// 本测试分两阶段：
//  1. 通过 ?error=test 回调（绕过 state 校验）解阻塞 RunLogin 并捕获 authURL，
//     解析出 state 后立即手动启动同 state 的回调服务器，调用 exchangeCodeForTokens
//     模拟 OAuth 授权的令牌交换，最后验证令牌与身份。
//  2. 验证 authURL 包含完整的 OAuth 参数。
func TestRunLogin(t *testing.T) {
	// ── 构造 mock JWT id_token ──
	exp := time.Now().Add(1 * time.Hour).Unix()
	jwtHeader := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	jwtPayload := base64.RawURLEncoding.EncodeToString([]byte(
		fmt.Sprintf(`{"email":"login@test.local","sub":"acc_login","user_id":"user_lgn","https://api.openai.com/plan_type":"pro","exp":%d,"iss":"https://auth.openai.com","aud":"app_EMoamEEZ73f0CkXaXp7hrann"}`, exp),
	))
	jwtSig := base64.RawURLEncoding.EncodeToString([]byte("fake-signature"))
	mockJWT := strings.Join([]string{jwtHeader, jwtPayload, jwtSig}, ".")

	// ── mock OAuth token 端点 ──
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"access_token":  "mock_login_at",
			"refresh_token": "mock_login_rt",
			"id_token":      mockJWT,
			"expires_in":    3600,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer tokenServer.Close()

	const testPort = 20455

	cfg := FlowConfig{
		EmailProvider: newEmailProviderMock(),
		BrowserClient: newBrowserClientMock(),
		CallbackPort:  testPort,
		TokenURL:      tokenServer.URL,
	}

	flow := NewAutoLoginFlow(cfg)

	// ── 阶段 1：通过 error 回调获取 authURL 并解析 state ──
	ctx1, cancel1 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel1()

	type loginOut struct {
		authURL string
		result  *FlowResult
		err     error
	}
	outCh := make(chan loginOut, 1)

	go func() {
		authURL, result, err := flow.RunLogin(ctx1)
		outCh <- loginOut{authURL, result, err}
	}()

	// 等待回调服务器就绪
	time.Sleep(100 * time.Millisecond)

	// 发送 error 回调：error 参数在校验 state 之前触发，直接解阻塞 cs.wait
	errResp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/auth/callback?error=internal_test_hook", testPort))
	if err != nil {
		t.Fatalf("send error callback: %v", err)
	}
	errResp.Body.Close()

	// 收集 RunLogin 结果（authURL 作为命名返回值，即使 error 路径也会填充）
	var authURL string
	select {
	case out := <-outCh:
		if out.authURL == "" {
			t.Fatal("authURL should not be empty (named return must be set before cs.wait)")
		}
		if out.err == nil {
			t.Fatal("expected error from error callback path")
		}
		authURL = out.authURL
	case <-ctx1.Done():
		t.Fatal("timeout waiting for RunLogin error-path return")
	}

	// 解析 state
	parsedAuth, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("parse authURL: %v", err)
	}
	capturedState := parsedAuth.Query().Get("state")
	if capturedState == "" {
		t.Fatal("state should not be empty in authURL")
	}

	// ── 阶段 1a：手动启动同 state 的回调服务器并交换令牌 ──
	// 使用从 authURL 解析出的 state 启动一个新的回调服务器，
	// 模拟 OAuth 授权回调，验证令牌交换与身份解析。
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()

	cs, err := startCallbackServer(ctx2, testPort+1, capturedState)
	if err != nil {
		t.Fatalf("start manual callback server: %v", err)
	}
	defer cs.shutdown()

	// 模拟 OAuth 重定向：发送正确 state + code 的回调
	callbackURL := fmt.Sprintf("http://127.0.0.1:%d/auth/callback?code=test_login_code&state=%s", testPort+1, url.QueryEscape(capturedState))
	go func() {
		time.Sleep(50 * time.Millisecond)
		resp, err := http.Get(callbackURL)
		if err != nil {
			t.Errorf("send manual callback: %v", err)
			return
		}
		resp.Body.Close()
	}()

	code, err := cs.wait(ctx2)
	if err != nil {
		t.Fatalf("wait for manual callback: %v", err)
	}
	if code != "test_login_code" {
		t.Errorf("code = %s, want test_login_code", code)
	}

	// 令牌交换
	tr, err := exchangeCodeForTokens(ctx2, code, "", flow.redirectURI(), tokenServer.URL)
	if err != nil {
		t.Fatalf("exchange code for tokens: %v", err)
	}

	ts := tokenRespToTokenSet(tr)
	identity, err := extractAccountIdentity(tr.IDToken)
	if err != nil {
		t.Fatalf("extract identity: %v", err)
	}
	ts.AccountID = identity.AccountID

	// 验证结果
	if ts.AccessToken != "mock_login_at" {
		t.Errorf("AccessToken = %s, want mock_login_at", ts.AccessToken)
	}
	if ts.RefreshToken != "mock_login_rt" {
		t.Errorf("RefreshToken = %s, want mock_login_rt", ts.RefreshToken)
	}
	if ts.IDToken != mockJWT {
		t.Errorf("IDToken mismatch")
	}
	if ts.AccountID != "acc_login" {
		t.Errorf("AccountID = %s, want acc_login", ts.AccountID)
	}
	if identity.Email != "login@test.local" {
		t.Errorf("Identity.Email = %s, want login@test.local", identity.Email)
	}
	if identity.AccountID != "acc_login" {
		t.Errorf("Identity.AccountID = %s, want acc_login", identity.AccountID)
	}
	if identity.UserID != "user_lgn" {
		t.Errorf("Identity.UserID = %s, want user_lgn", identity.UserID)
	}
	if identity.PlanType != "pro" {
		t.Errorf("Identity.PlanType = %s, want pro", identity.PlanType)
	}

	// ── 阶段 2：验证 authURL 参数 ──
	params := parsedAuth.Query()
	if params.Get("client_id") != codexClientID {
		t.Errorf("client_id = %s, want %s", params.Get("client_id"), codexClientID)
	}
	expectedRedirect := fmt.Sprintf("http://localhost:%d/auth/callback", testPort)
	if params.Get("redirect_uri") != expectedRedirect {
		t.Errorf("redirect_uri = %s, want %s", params.Get("redirect_uri"), expectedRedirect)
	}
	if params.Get("response_type") != "code" {
		t.Errorf("response_type = %s, want code", params.Get("response_type"))
	}
	if params.Get("scope") != "openid profile email offline_access" {
		t.Errorf("scope = %s", params.Get("scope"))
	}
	if params.Get("code_challenge") == "" {
		t.Error("code_challenge should not be empty")
	}
	if params.Get("code_challenge_method") != "S256" {
		t.Errorf("code_challenge_method = %s, want S256", params.Get("code_challenge_method"))
	}
	if params.Get("state") == "" {
		t.Error("state should not be empty")
	}
	if parsedAuth.Scheme != "https" {
		t.Errorf("scheme = %s, want https", parsedAuth.Scheme)
	}
	if parsedAuth.Host != "auth.openai.com" {
		t.Errorf("host = %s, want auth.openai.com", parsedAuth.Host)
	}
}
