package codexauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tursom/turapis/internal/email"
	"github.com/tursom/turapis/internal/sms"
	"golang.org/x/net/proxy"
)

// OAuth PKCE 常量，与 oauth_refresh.go 保持一致。
const (
	codexTokenURL = "https://auth.openai.com/oauth/token"
	codexClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	codexIssuer   = "https://auth.openai.com"
)

// AutoLoginFlow 是 Codex 自动登录流程的核心编排器。
// 它协调浏览器自动化、临时邮箱和 OAuth PKCE 回调，
// 实现零人工介入的账号注册与登录。
type AutoLoginFlow struct {
	cfg FlowConfig
}

// NewAutoLoginFlow 使用给定的配置创建一个新的流程编排器。
func NewAutoLoginFlow(cfg FlowConfig) *AutoLoginFlow {
	return &AutoLoginFlow{cfg: cfg}
}

// SetBrowserClient 动态设置用于 OAuth 流程的浏览器客户端。
func (f *AutoLoginFlow) SetBrowserClient(bc BrowserClient) {
	f.cfg.BrowserClient = bc
}

// SetProgressFn sets the progress callback used by RunRegister and RunRelogin.
// Pass nil to disable progress reporting.
func (f *AutoLoginFlow) SetProgressFn(fn func(string)) {
	f.cfg.ProgressFn = fn
}

// SetEmailProvider sets the email provider used for creating temporary inboxes.
func (f *AutoLoginFlow) SetEmailProvider(ep email.EmailProvider) {
	f.cfg.EmailProvider = ep
}

// SetSMSProvider sets the SMS provider used for phone verification.
func (f *AutoLoginFlow) SetSMSProvider(sp sms.SMSProvider) {
	f.cfg.SMSProvider = sp
}

func (f *AutoLoginFlow) redirectURI() string {
	return fmt.Sprintf("http://localhost:%d/auth/callback", f.cfg.CallbackPort)
}

// ──────────────────── 回调服务器 ────────────────────

// callbackServer 在本地监听 OAuth 授权码回调。
type callbackServer struct {
	codeCh chan string
	errCh  chan error
	srv    *http.Server
	state  string
}

// startCallbackServer 在 127.0.0.1:port 启动一个 HTTP 服务器，
// 监听 /auth/callback 路径捕获 OAuth 授权码，并校验 state 防 CSRF。
func startCallbackServer(_ context.Context, port int, expectedState string) (*callbackServer, error) {
	mux := http.NewServeMux()
	cs := &callbackServer{
		codeCh: make(chan string, 1),
		errCh:  make(chan error, 1),
		state:  expectedState,
	}

	mux.HandleFunc("/auth/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		code := q.Get("code")
		errStr := q.Get("error")

		// OAuth 错误直接终止等待
		if errStr != "" {
			errDesc := q.Get("error_description")
			select {
			case cs.errCh <- fmt.Errorf("authorization error: %s - %s", errStr, errDesc):
			default:
			}
			http.Error(w, "Authorization failed", http.StatusBadRequest)
			return
		}

		// state 缺失或错误 → 仅返回 HTTP 错误，不终止等待
		// 防止端口扫描/浏览器预请求/噪声请求误杀流程
		callbackState := q.Get("state")
		if callbackState == "" {
			http.Error(w, "Missing state", http.StatusBadRequest)
			return
		}
		if callbackState != expectedState {
			http.Error(w, "State mismatch", http.StatusForbidden)
			return
		}

		// state 正确但缺 code → 真正的错误，终止等待
		if code == "" {
			select {
			case cs.errCh <- fmt.Errorf("missing authorization code in callback"):
			default:
			}
			http.Error(w, "Missing code", http.StatusBadRequest)
			return
		}

		select {
		case cs.codeCh <- code:
			fmt.Fprintf(w, "Authorization successful. You may close this window.")
		default:
			http.Error(w, "Already received code", http.StatusConflict)
		}
	})

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", addr, err)
	}

	cs.srv = &http.Server{Handler: mux}
	go func() {
		if err := cs.srv.Serve(listener); err != nil && err != http.ErrServerClosed {
			select {
			case cs.errCh <- fmt.Errorf("callback server: %w", err):
			default:
			}
		}
	}()

	return cs, nil
}

// wait 阻塞等待授权码到达，或上下文超时/取消。
func (cs *callbackServer) wait(ctx context.Context) (string, error) {
	select {
	case code := <-cs.codeCh:
		return code, nil
	case err := <-cs.errCh:
		return "", err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// shutdown 优雅关闭回调服务器。
func (cs *callbackServer) shutdown() {
	if cs.srv != nil {
		_ = cs.srv.Close()
	}
}

// ──────────────────── OAuth PKCE 辅助 ────────────────────

// generatePKCE 生成 PKCE code_verifier 和基于 S256 的 code_challenge。
// verifier 为 32 字节随机数据的 base64url 编码。
func generatePKCE() (verifier, challenge string, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", fmt.Errorf("generate pkce random: %w", err)
	}
	verifier = base64.RawURLEncoding.EncodeToString(raw)
	hash := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(hash[:])
	return verifier, challenge, nil
}

// generateState 生成一个随机 state 参数，用于防 CSRF。
func generateState() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate state random: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// buildAuthorizeURL 构造 OAuth 授权 URL。
func buildAuthorizeURL(challenge, state, redirectURI string) string {
	params := url.Values{
		"client_id":                   {codexClientID},
		"redirect_uri":                {redirectURI},
		"response_type":               {"code"},
		"scope":                       {"openid profile email offline_access api.connectors.read api.connectors.invoke"},
		"state":                       {state},
		"code_challenge":              {challenge},
		"code_challenge_method":       {"S256"},
		"id_token_add_organizations":  {"true"},
		"codex_cli_simplified_flow":   {"true"},
		"originator":                  {"codex_cli"},
	}
	return "https://auth.openai.com/oauth/authorize?" + params.Encode()
}

// tokenResponse 表示 OAuth token 端点的响应。
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	ExpiresIn    int    `json:"expires_in"`
}

// exchangeCodeForTokens 使用授权码和 PKCE verifier 交换令牌。
// tokenURL 为空时使用默认 codexTokenURL。
func exchangeCodeForTokens(ctx context.Context, code, verifier, redirectURI, tokenURL string) (*tokenResponse, error) {
	if tokenURL == "" {
		tokenURL = codexTokenURL
	}

	form := url.Values{
		"client_id":     {codexClientID},
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"code_verifier": {verifier},
	}

	req, err := http.NewRequestWithContext(ctx, "POST", tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("create token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 65536))
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("token exchange: http %d: %s", resp.StatusCode, string(body))
	}

	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, fmt.Errorf("parse token response: %w", err)
	}
	if tr.AccessToken == "" {
		return nil, fmt.Errorf("token response missing access_token")
	}
	return &tr, nil
}

// exchangeCodeForTokensClient is like exchangeCodeForTokens but allows
// specifying the OAuth client_id (needed when exchanging codes from the
// platform consent flow vs the Codex PKCE flow).
func exchangeCodeForTokensClient(ctx context.Context, code, verifier, redirectURI, tokenURL, clientID, proxyURL string) (*tokenResponse, error) {
	if tokenURL == "" {
		tokenURL = codexTokenURL
	}

	form := url.Values{
		"client_id":     {clientID},
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"code_verifier": {verifier},
	}

	req, err := http.NewRequestWithContext(ctx, "POST", tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("create token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	httpClient := &http.Client{Timeout: 15 * time.Second}
	if proxyURL != "" {
		httpClient.Transport = proxyTransport(proxyURL)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 65536))
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("token exchange: http %d: %s", resp.StatusCode, string(body))
	}

	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, fmt.Errorf("parse token response: %w", err)
	}
	if tr.AccessToken == "" {
		return nil, fmt.Errorf("token response missing access_token")
	}
	return &tr, nil
}

// extractAccountIdentity 从 id_token JWT 中解析账号身份信息。
// 注意：由于未接入 Auth0/OpenAI JWKS 验签，此函数仅做结构完整性校验
// （alg 非 none、exp/iss/aud 匹配），不提供密码学信任保证。
// 调用方不应将返回的身份信息用于超出展示层的安全决策。
func extractAccountIdentity(idToken string) (*AccountIdentity, error) {
	identity, err := extractJWTIdentity(idToken)
	if err != nil {
		return nil, err
	}
	return identity, nil
}

// extractJWTIdentity parses a JWT (id_token or access_token) and extracts
// account identity claims. plan_type uses the OpenAI-specific claim name.
func extractJWTIdentity(token string) (*AccountIdentity, error) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil, fmt.Errorf("invalid jwt: not enough parts")
	}

	headerJSON, err := b64Decode(parts[0])
	if err != nil {
		return nil, fmt.Errorf("decode jwt header: %w", err)
	}
	var header struct {
		Alg string `json:"alg"`
		Typ string `json:"typ"`
	}
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		return nil, fmt.Errorf("parse jwt header: %w", err)
	}
	if header.Alg == "" {
		return nil, fmt.Errorf("jwt missing alg claim")
	}
	if strings.EqualFold(header.Alg, "none") {
		return nil, fmt.Errorf("jwt alg is none — rejected")
	}

	payloadJSON, err := b64Decode(parts[1])
	if err != nil {
		return nil, fmt.Errorf("decode jwt payload: %w", err)
	}

	var claims struct {
		Email    string `json:"email"`
		Sub      string `json:"sub"`
		UserID   string `json:"user_id"`
		PlanType string `json:"https://api.openai.com/plan_type"`
		Auth     struct {
			UserID string `json:"user_id"`
		} `json:"https://api.openai.com/auth"`
		Exp int64  `json:"exp"`
		Iss string `json:"iss"`
		Aud any    `json:"aud"`
	}
	if err := json.Unmarshal(payloadJSON, &claims); err != nil {
		return nil, fmt.Errorf("parse jwt claims: %w", err)
	}

	// user_id may be at top level or nested in https://api.openai.com/auth
	userID := claims.UserID
	if userID == "" {
		userID = claims.Auth.UserID
	}

	if claims.Exp == 0 {
		return nil, fmt.Errorf("jwt missing exp claim")
	}
	if time.Unix(claims.Exp, 0).Before(time.Now().Add(-60 * time.Second)) {
		return nil, fmt.Errorf("jwt expired at %d (%s)", claims.Exp,
			time.Unix(claims.Exp, 0).Format(time.RFC3339))
	}

	if claims.Iss == "" {
		return nil, fmt.Errorf("jwt missing iss claim")
	}
	if claims.Iss != codexIssuer {
		return nil, fmt.Errorf("jwt iss %q, want %q", claims.Iss, codexIssuer)
	}

	if !audContains(claims.Aud, codexClientID) && !audContains(claims.Aud, PlatformClientID) {
		return nil, fmt.Errorf("jwt aud does not contain expected client_id")
	}

	return &AccountIdentity{
		Email:     claims.Email,
		AccountID: claims.Sub,
		UserID:    userID,
		PlanType:  claims.PlanType,
	}, nil
}

// mergeIdentities fills empty fields in primary with values from fallback.
// Used when id_token lacks some claims that access_token provides.
func mergeIdentities(primary, fallback *AccountIdentity) *AccountIdentity {
	if primary == nil {
		return fallback
	}
	if fallback == nil {
		return primary
	}
	if primary.Email == "" {
		primary.Email = fallback.Email
	}
	if primary.AccountID == "" {
		primary.AccountID = fallback.AccountID
	}
	if primary.UserID == "" {
		primary.UserID = fallback.UserID
	}
	if primary.PlanType == "" {
		primary.PlanType = fallback.PlanType
	}
	return primary
}

// b64Decode 将 base64url 字符串解码为原始字节，
// 自动补全缺失的填充字符。
func b64Decode(s string) ([]byte, error) {
	switch len(s) % 4 {
	case 2:
		s += "=="
	case 3:
		s += "="
	}
	return base64.URLEncoding.DecodeString(s)
}

// audContains 检查 aud（string 或 []interface{}）是否包含 target。
func audContains(aud any, target string) bool {
	switch v := aud.(type) {
	case string:
		return v == target
	case []any:
		for _, a := range v {
			if s, ok := a.(string); ok && s == target {
				return true
			}
		}
	}
	return false
}

// TokenSetToCredentialJSON 将 TokenSet 转换为 turapis OAuth 凭证 JSON 格式，
// 兼容 oauth_refresh.go 的解析逻辑。
// 同时从 access_token JWT 中提取 client_id，确保刷新逻辑可用。
func TokenSetToCredentialJSON(ts *TokenSet) (json.RawMessage, error) {
	clientID := extractClientIDFromAccessToken(ts.AccessToken)
	if clientID == "" {
		return nil, fmt.Errorf("cannot extract client_id from access_token — refresh will fail")
	}

	tokens := map[string]any{
		"access_token":  ts.AccessToken,
		"refresh_token": ts.RefreshToken,
		"id_token":      ts.IDToken,
		"account_id":    ts.AccountID,
		"client_id":     clientID,
		"expires_at":    ts.ExpiresAt,
	}
	creds := map[string]any{"tokens": tokens}
	data, err := json.Marshal(creds)
	if err != nil {
		return nil, fmt.Errorf("marshal credential json: %w", err)
	}
	return json.RawMessage(data), nil
}

// extractClientIDFromAccessToken 从 access_token JWT payload 中提取 client_id claim。
func extractClientIDFromAccessToken(accessToken string) string {
	parts := strings.Split(accessToken, ".")
	if len(parts) < 2 {
		return ""
	}
	payload, err := b64Decode(parts[1])
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

// tokenRespToTokenSet 将 token 响应转换为 TokenSet。
func tokenRespToTokenSet(tr *tokenResponse) *TokenSet {
	return &TokenSet{
		AccessToken:  tr.AccessToken,
		RefreshToken: tr.RefreshToken,
		IDToken:      tr.IDToken,
		AccountID:    "",
		ExpiresAt:    time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second).UnixMilli(),
	}
}

// ──────────────────── 场景 A：注册新账号 ────────────────────

// RunRegister 执行完整的自动注册流程（场景 A）。
// 返回令牌结果和邮箱凭证，可用于后续登录。
func (f *AutoLoginFlow) RunRegister(ctx context.Context) (*FlowResult, *EmailCredential, error) {
	cfg := f.cfg

	if cfg.EmailProvider == nil {
		return nil, nil, fmt.Errorf("email provider is not configured")
	}
	if cfg.BrowserClient == nil {
		return nil, nil, fmt.Errorf("browser client is not configured")
	}

	// ① 创建临时邮箱
	inbox, err := cfg.EmailProvider.CreateInbox(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("create inbox: %w", err)
	}

	if f.cfg.ProgressFn != nil {
		f.cfg.ProgressFn(fmt.Sprintf("inbox: %s (%s)", inbox.Address, inbox.Provider))
	}

	// ② 打开浏览器会话
	bCtx, cancel := cfg.BrowserClient.NewContext(ctx)
	defer cancel()

	if f.cfg.ProgressFn != nil {
		f.cfg.ProgressFn("opening_browser")
	}

	// ③ 导航到注册页并填写邮箱
	if err := cfg.BrowserClient.Navigate(bCtx, "https://auth.openai.com/signup"); err != nil {
		return nil, nil, fmt.Errorf("navigate signup: %w", err)
	}
	if err := cfg.BrowserClient.WaitForSelector(bCtx, "input[type=email]"); err != nil {
		return nil, nil, fmt.Errorf("wait for email input: %w", err)
	}
	if err := cfg.BrowserClient.SendKeys(bCtx, "input[type=email]", inbox.Address); err != nil {
		return nil, nil, fmt.Errorf("fill email: %w", err)
	}
	// 点击继续按钮
	if err := cfg.BrowserClient.Click(bCtx, "button[type=submit]"); err != nil {
		return nil, nil, fmt.Errorf("submit email: %w", err)
	}

	if f.cfg.ProgressFn != nil {
		f.cfg.ProgressFn("filling_signup_form")
	}

	// ④ 等待验证邮件
	msg, err := cfg.EmailProvider.WaitForEmail(ctx, inbox, cfg.PollTimeout, func(m *email.EmailMessage) bool {
		return strings.Contains(m.Subject, "Verify") || strings.Contains(m.Subject, "email")
	})
	if err != nil {
		return nil, nil, fmt.Errorf("wait for verification email: %w", err)
	}

	if f.cfg.ProgressFn != nil {
		f.cfg.ProgressFn(fmt.Sprintf("waiting_for_email to %s", inbox.Address))
	}
	// ⑤ 提取验证链接
	verifyLink := email.ExtractVerificationLink(msg)
	if verifyLink == "" {
		return nil, nil, fmt.Errorf("no verification link found in email")
	}

	// ⑥ 浏览器导航到验证链接完成验证
	if err := cfg.BrowserClient.Navigate(bCtx, verifyLink); err != nil {
		return nil, nil, fmt.Errorf("navigate verify link: %w", err)
	}

	if f.cfg.ProgressFn != nil {
		f.cfg.ProgressFn("verifying_email")
	}

	// ⑦ 等待密码设置页面并填写密码
	passwd := generatePassword()
	if err := cfg.BrowserClient.WaitForSelector(bCtx, "input[type=password]"); err != nil {
		return nil, nil, fmt.Errorf("wait for password input: %w", err)
	}
	if err := cfg.BrowserClient.SendKeys(bCtx, "input[type=password]", passwd); err != nil {
		return nil, nil, fmt.Errorf("fill password: %w", err)
	}
	if err := cfg.BrowserClient.Click(bCtx, "button[type=submit]"); err != nil {
		return nil, nil, fmt.Errorf("submit password: %w", err)
	}

	if f.cfg.ProgressFn != nil {
		f.cfg.ProgressFn("setting_password")
	}

	// ⑧ 生成 PKCE
	verifier, challenge, err := generatePKCE()
	if err != nil {
		return nil, nil, fmt.Errorf("generate pkce: %w", err)
	}
	state, err := generateState()
	if err != nil {
		return nil, nil, fmt.Errorf("generate state: %w", err)
	}

	if f.cfg.ProgressFn != nil {
		f.cfg.ProgressFn("generating_pkce")
	}

	// ⑨ 启动回调服务器（在导航之前）
	cs, err := startCallbackServer(ctx, cfg.CallbackPort, state)
	if err != nil {
		return nil, nil, fmt.Errorf("start callback server: %w", err)
	}
	defer cs.shutdown()

	if f.cfg.ProgressFn != nil {
		f.cfg.ProgressFn("starting_callback")
	}

	// ⑩ 浏览器导航到授权 URL
	authURL := buildAuthorizeURL(challenge, state, f.redirectURI())
	if err := cfg.BrowserClient.Navigate(bCtx, authURL); err != nil {
		return nil, nil, fmt.Errorf("navigate authorize url: %w", err)
	}

	if f.cfg.ProgressFn != nil {
		f.cfg.ProgressFn("authorizing")
	}

	// ⑪ 等待回调
	code, err := cs.wait(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("wait for callback: %w", err)
	}

	if f.cfg.ProgressFn != nil {
		f.cfg.ProgressFn("waiting_callback")
	}

	// ⑫ 交换令牌
	tr, err := exchangeCodeForTokens(ctx, code, verifier, f.redirectURI(), f.cfg.TokenURL)
	if err != nil {
		return nil, nil, fmt.Errorf("exchange code for tokens: %w", err)
	}

	if f.cfg.ProgressFn != nil {
		f.cfg.ProgressFn("exchanging_tokens")
	}

	// ⑬ 解析 JWT 获取身份信息
	ts := tokenRespToTokenSet(tr)
	identity, err := extractAccountIdentity(tr.IDToken)
	if err != nil {
		return nil, nil, fmt.Errorf("extract identity: %w", err)
	}
	ts.AccountID = identity.AccountID

	if f.cfg.ProgressFn != nil {
		f.cfg.ProgressFn("parsing_identity")
	}

	// ⑭ 构造并返回邮箱凭证
	// Save password with email credential so relogin can use it for VerifyPassword.
	ec := &EmailCredential{
		Email:     inbox.Address,
		Provider:  cfg.EmailProvider.Name(),
		Token:     inbox.Token,
		Password:  passwd,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}

	return &FlowResult{Tokens: ts, Identity: identity}, ec, nil
}

// ──────────────────── 场景 B：非阻塞登录 ────────────────────

// StartLogin 生成 OAuth 授权 URL 并启动回调服务器，
// 返回 URL 和一个等待函数。调用者在浏览器中打开 URL 后，
// 调用 wait 阻塞等待 OAuth 回调并交换令牌。
func (f *AutoLoginFlow) StartLogin(ctx context.Context) (authURL string, wait func(context.Context) (*FlowResult, error), err error) {
	cfg := f.cfg

	verifier, challenge, err := generatePKCE()
	if err != nil {
		return "", nil, fmt.Errorf("generate pkce: %w", err)
	}
	state, err := generateState()
	if err != nil {
		return "", nil, fmt.Errorf("generate state: %w", err)
	}

	cs, err := startCallbackServer(ctx, cfg.CallbackPort, state)
	if err != nil {
		return "", nil, fmt.Errorf("start callback server: %w", err)
	}

	authURL = buildAuthorizeURL(challenge, state, f.redirectURI())

	wait = func(waitCtx context.Context) (*FlowResult, error) {
		defer cs.shutdown()

		code, wErr := cs.wait(waitCtx)
		if wErr != nil {
			return nil, fmt.Errorf("wait for callback: %w", wErr)
		}

		tr, wErr := exchangeCodeForTokens(waitCtx, code, verifier, f.redirectURI(), f.cfg.TokenURL)
		if wErr != nil {
			return nil, fmt.Errorf("exchange code for tokens: %w", wErr)
		}

		ts := tokenRespToTokenSet(tr)
		identity, wErr := extractAccountIdentity(tr.IDToken)
		if wErr != nil {
			return nil, fmt.Errorf("extract identity: %w", wErr)
		}
		ts.AccountID = identity.AccountID
		return &FlowResult{Tokens: ts, Identity: identity}, nil
	}

	return authURL, wait, nil
}

// ──────────────────── 场景 B2/C：复用邮箱凭证重新登录 ────────────────────

// RunRelogin 使用已有的邮箱凭证，通过验证码完成重新登录（场景 B2/C）。
func (f *AutoLoginFlow) RunRelogin(ctx context.Context, ec *EmailCredential) (*FlowResult, error) {
	cfg := f.cfg

	inbox := &email.InboxInfo{
		Address:  ec.Email,
		Provider: ec.Provider,
		Token:    ec.Token,
	}

	bCtx, cancel := cfg.BrowserClient.NewContext(ctx)
	defer cancel()

	if err := cfg.BrowserClient.Navigate(bCtx, "https://auth.openai.com/login"); err != nil {
		return nil, fmt.Errorf("navigate login: %w", err)
	}
	if err := cfg.BrowserClient.WaitForSelector(bCtx, "input[type=email]"); err != nil {
		// bCtx may be canceled; use a fresh context for debug
		debugCtx, _ := cfg.BrowserClient.NewContext(ctx)
		if bc, ok := cfg.BrowserClient.(interface {
			Screenshot(context.Context, string) error
			TextContent(context.Context, string) (string, error)
		}); ok {
			slog.Info("cloudflare_debug_capturing")
			if sErr := bc.Screenshot(debugCtx, "/tmp/cloudflare_debug.png"); sErr != nil {
				slog.Warn("cloudflare_debug_screenshot_failed", "error", sErr)
			}
			if text, tErr := bc.TextContent(debugCtx, "body"); tErr == nil {
				slog.Info("cloudflare_debug_body", "text", text[:min(len(text), 500)])
			}
		}
		return nil, fmt.Errorf("wait for email input: %w", err)
	}
	if err := cfg.BrowserClient.SendKeys(bCtx, "input[type=email]", ec.Email); err != nil {
		return nil, fmt.Errorf("fill email: %w", err)
	}
	if err := cfg.BrowserClient.Click(bCtx, "button[type=submit]"); err != nil {
		return nil, fmt.Errorf("submit email: %w", err)
	}

	msg, err := cfg.EmailProvider.WaitForEmail(ctx, inbox, cfg.PollTimeout, func(m *email.EmailMessage) bool {
		return email.ExtractVerificationCode(m) != ""
	})
	if err != nil {
		return nil, fmt.Errorf("wait for code email: %w", err)
	}

	code := email.ExtractVerificationCode(msg)
	if code == "" {
		return nil, fmt.Errorf("no verification code found in email")
	}

	if err := cfg.BrowserClient.WaitForSelector(bCtx, "input[type=text]"); err != nil {
		return nil, fmt.Errorf("wait for code input: %w", err)
	}
	if err := cfg.BrowserClient.SendKeys(bCtx, "input[type=text]", code); err != nil {
		return nil, fmt.Errorf("fill code: %w", err)
	}
	if err := cfg.BrowserClient.Click(bCtx, "button[type=submit]"); err != nil {
		return nil, fmt.Errorf("submit code: %w", err)
	}

	verifier, challenge, err := generatePKCE()
	if err != nil {
		return nil, fmt.Errorf("generate pkce: %w", err)
	}
	state, err := generateState()
	if err != nil {
		return nil, fmt.Errorf("generate state: %w", err)
	}

	cs, err := startCallbackServer(ctx, cfg.CallbackPort, state)
	if err != nil {
		return nil, fmt.Errorf("start callback server: %w", err)
	}
	defer cs.shutdown()

	authURL := buildAuthorizeURL(challenge, state, f.redirectURI())
	if err := cfg.BrowserClient.Navigate(bCtx, authURL); err != nil {
		return nil, fmt.Errorf("navigate authorize url: %w", err)
	}

	authCode, err := cs.wait(ctx)
	if err != nil {
		return nil, fmt.Errorf("wait for callback: %w", err)
	}

	tr, err := exchangeCodeForTokens(ctx, authCode, verifier, f.redirectURI(), f.cfg.TokenURL)
	if err != nil {
		return nil, fmt.Errorf("exchange code for tokens: %w", err)
	}

	ts := tokenRespToTokenSet(tr)
	identity, err := extractAccountIdentity(tr.IDToken)
	if err != nil {
		return nil, fmt.Errorf("extract identity: %w", err)
	}

	// Merge identity from access_token for nested claims (user_id)
	if atIdent, atErr := extractJWTIdentity(tr.AccessToken); atErr == nil {
		identity = mergeIdentities(identity, atIdent)
	}
	ts.AccountID = identity.AccountID

	return &FlowResult{Tokens: ts, Identity: identity}, nil
}

// ──────────────────── HTTP-based 注册（chatgpt2api 方式，无需浏览器）────────────────────

// HTTPCodexBrowserLogin uses the browser (when available) to perform Codex OAuth
// PKCE login for an already-registered account. It delegates browser automation
// to a Python undetected-chromedriver script that bypasses Cloudflare.
// When email is non-empty, the script logs in via the browser (email + verification code)
// instead of using cookie injection. getCode is called to retrieve the verification code;
// when email is empty, cookies are used for authentication (legacy path).
// Go handles PKCE generation and token exchange; Python handles the browser.
func (f *AutoLoginFlow) HTTPCodexBrowserLogin(ctx context.Context, proxyURL, email, password string, getCode func() (string, error)) (*FlowResult, error) {
	cfg := f.cfg

	codexRedirectURI := fmt.Sprintf("http://localhost:%d/auth/callback", cfg.CallbackPort)

	verifier, challenge, err := generatePKCE()
	if err != nil {
		return nil, fmt.Errorf("generate pkce: %w", err)
	}
	state, err := generateState()
	if err != nil {
		return nil, fmt.Errorf("generate state: %w", err)
	}

	authURL := buildAuthorizeURL(challenge, state, codexRedirectURI)

	slog.Info("python_codex_oauth_starting", "port", cfg.CallbackPort, "email", email)

	var getPhone func() (string, error)
	var getSMSCode func(string) (string, error)
	if cfg.SMSProvider != nil {
		getPhone = func() (string, error) {
			num, err := cfg.SMSProvider.GetNumber(ctx, "openai")
			if err != nil {
				return "", err
			}
			slog.Info("sms_number_obtained", "number", num.Number)
			return num.Number, nil
		}
		getSMSCode = func(phone string) (string, error) {
			msg, err := cfg.SMSProvider.WaitForCode(ctx, &sms.NumberInfo{
				Number:       phone,
				ActivationID: phone,
				Provider:     cfg.SMSProvider.Name(),
			}, cfg.PollTimeout)
			if err != nil {
				return "", err
			}
			code := sms.ExtractVerificationCode(msg)
			if code == "" {
				return "", fmt.Errorf("no code in sms: %s", msg.Text)
			}
			return code, nil
		}
	}

	code, err := pythonCodexBrowserLogin(ctx, authURL, proxyURL, email, password, cfg.CallbackPort, getCode, getPhone, getSMSCode)
	if err != nil {
		return nil, fmt.Errorf("python codex oauth: %w", err)
	}

	slog.Info("python_codex_oauth_done", "code_len", len(code))

	tr, err := exchangeCodeForTokens(ctx, code, verifier, codexRedirectURI, cfg.TokenURL)
	if err != nil {
		return nil, fmt.Errorf("exchange codex tokens: %w", err)
	}

	ts := tokenRespToTokenSet(tr)
	identity, err := extractAccountIdentity(tr.IDToken)
	if err != nil {
		return nil, fmt.Errorf("extract identity: %w", err)
	}
	ts.AccountID = identity.AccountID

	return &FlowResult{Tokens: ts, Identity: identity}, nil
}

// HTTPRunRegister executes the full registration flow using HTTP API calls
// to auth.openai.com (chatgpt2api approach). No browser required.
// proxyURL is the SOCKS5 or HTTP proxy URL for TLS impersonation.
func (f *AutoLoginFlow) HTTPRunRegister(ctx context.Context, proxyURL string) (*FlowResult, *EmailCredential, error) {
	cfg := f.cfg

	if cfg.EmailProvider == nil {
		return nil, nil, fmt.Errorf("email provider is not configured")
	}

	if f.cfg.ProgressFn != nil {
		f.cfg.ProgressFn("creating_inbox")
	}
	inbox, err := cfg.EmailProvider.CreateInbox(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("create inbox: %w", err)
	}

	if f.cfg.ProgressFn != nil {
		f.cfg.ProgressFn(fmt.Sprintf("inbox: %s (%s)", inbox.Address, inbox.Provider))
	}

	passwd := generatePassword()
	firstName, lastName := randomName()
	birthdate := randomBirthdate()

	client, err := NewChromeClient(proxyURL)
	if err != nil {
		return nil, nil, fmt.Errorf("create chrome client: %w", err)
	}
	defer client.Close()

	gen := NewSentinelTokenGenerator(client.DeviceID)

	if f.cfg.ProgressFn != nil {
		f.cfg.ProgressFn("init_session")
	}
	if err := client.InitSession(ctx, inbox.Address, PlatformClientID, PlatformRedirectURI, "login_or_signup", PlatformAuth0Client); err != nil {
		return nil, nil, fmt.Errorf("init session: %w", err)
	}

	sentinelRegister, err := gen.BuildSentinelToken(ctx, client.HTTPClient, SentinelFlowRegister)
	if err != nil {
		return nil, nil, fmt.Errorf("sentinel register: %w", err)
	}

	if f.cfg.ProgressFn != nil {
		f.cfg.ProgressFn("registering_user")
	}
	if err := client.RegisterUser(ctx, inbox.Address, passwd, sentinelRegister); err != nil {
		return nil, nil, fmt.Errorf("register user: %w", err)
	}

	if f.cfg.ProgressFn != nil {
		f.cfg.ProgressFn("sending_otp")
	}
	if err := client.SendOTP(ctx); err != nil {
		return nil, nil, fmt.Errorf("send otp: %w", err)
	}

	if f.cfg.ProgressFn != nil {
		f.cfg.ProgressFn(fmt.Sprintf("waiting_for_email to %s", inbox.Address))
	}
	msg, err := cfg.EmailProvider.WaitForEmail(ctx, inbox, cfg.PollTimeout, func(m *email.EmailMessage) bool {
		code := email.ExtractVerificationCode(m)
		return code != ""
	})
	if err != nil {
		return nil, nil, fmt.Errorf("wait for otp email: %w", err)
	}

	code := email.ExtractVerificationCode(msg)
	if code == "" {
		return nil, nil, fmt.Errorf("no verification code found in email")
	}

	if f.cfg.ProgressFn != nil {
		f.cfg.ProgressFn("validating_otp")
	}
	sentinelAuth, _ := gen.BuildSentinelToken(ctx, client.HTTPClient, SentinelFlowAuthorizeContinue)
	if err := client.ValidateOTP(ctx, code, sentinelAuth); err != nil {
		return nil, nil, fmt.Errorf("validate otp: %w", err)
	}

	if f.cfg.ProgressFn != nil {
		f.cfg.ProgressFn("creating_account")
	}
	sentinelCreate, err := gen.BuildSentinelToken(ctx, client.HTTPClient, SentinelFlowCreateAccount)
	if err != nil {
		return nil, nil, fmt.Errorf("sentinel create account: %w", err)
	}
	if err := client.CreateAccount(ctx, firstName+" "+lastName, birthdate, sentinelCreate); err != nil {
		return nil, nil, fmt.Errorf("create account: %w", err)
	}

	// Login phase — use Codex OAuth PKCE via browser when available,
	// otherwise fall back to platform OAuth tokens.
	var ts *TokenSet
	var identity *AccountIdentity
	if f.cfg.ProgressFn != nil {
		f.cfg.ProgressFn("login")
	}
	if f.cfg.BrowserClient != nil {
		if f.cfg.ProgressFn != nil {
			f.cfg.ProgressFn("browser_codex_auth")
		}
		getCode := func() (string, error) {
			msg, err := cfg.EmailProvider.WaitForEmail(ctx, inbox, cfg.PollTimeout, func(m *email.EmailMessage) bool {
				return email.ExtractVerificationCode(m) != ""
			})
			if err != nil {
				all, _ := cfg.EmailProvider.GetMessages(ctx, inbox)
				var subjects []string
				for _, m := range all {
					subjects = append(subjects, fmt.Sprintf("%s: %s", m.Date, m.Subject))
				}
				return "", fmt.Errorf("wait for code: %w (inbox has %d msgs: %v)", err, len(all), subjects)
			}
			code := email.ExtractVerificationCode(msg)
			if code == "" {
				return "", fmt.Errorf("no code in email")
			}
			return code, nil
		}
		result, err := f.HTTPCodexBrowserLogin(ctx, proxyURL, inbox.Address, passwd, getCode)
		if err != nil {
			ec := &EmailCredential{
				Email:     inbox.Address,
				Provider:  cfg.EmailProvider.Name(),
				Token:     inbox.Token,
				Password:  passwd,
				UpdatedAt: time.Now().UTC().Format(time.RFC3339),
			}
			return nil, ec, fmt.Errorf("codex browser login: %w", err)
		}
		ts = result.Tokens
		identity = result.Identity
	} else {
		// Fallback: platform OAuth token exchange
		loginClient, err := NewChromeClient(proxyURL)
		if err != nil {
			return nil, nil, fmt.Errorf("create login client: %w", err)
		}
		defer loginClient.Close()

		loginGen := NewSentinelTokenGenerator(loginClient.DeviceID)

		if err := loginClient.InitSession(ctx, inbox.Address, PlatformClientID, PlatformRedirectURI, "login", PlatformAuth0Client); err != nil {
			return nil, nil, fmt.Errorf("login init session: %w", err)
		}

		sentinelCont, _ := loginGen.BuildSentinelToken(ctx, loginClient.HTTPClient, SentinelFlowAuthorizeContinue)
		if err := loginClient.ContinueLogin(ctx, inbox.Address, sentinelCont); err != nil {
			return nil, nil, fmt.Errorf("continue login: %w", err)
		}

		sentinelPw, _ := loginGen.BuildSentinelToken(ctx, loginClient.HTTPClient, SentinelFlowPasswordVerify)
		pwResp, err := loginClient.VerifyPassword(ctx, passwd, sentinelPw)
		if err != nil {
			return nil, nil, fmt.Errorf("verify password: %w", err)
		}

		var pwData struct {
			ContinueURL string `json:"continue_url"`
		}
		json.Unmarshal(pwResp, &pwData)
		continueURL := pwData.ContinueURL
		if continueURL == "" {
			continueURL = AuthBase + "/sign-in-with-chatgpt/codex/consent"
		}

		verifier := loginClient.PkceVerifier()
		if verifier == "" {
			return nil, nil, fmt.Errorf("no pkce verifier from login session")
		}
		authCode, err := loginClient.FollowConsent(ctx, continueURL)
		if err != nil {
			return nil, nil, fmt.Errorf("follow consent: %w", err)
		}

		tr, err := exchangeCodeForTokensClient(ctx, authCode, verifier, PlatformRedirectURI, f.cfg.TokenURL, PlatformClientID, proxyURL)
		if err != nil {
			return nil, nil, fmt.Errorf("exchange tokens: %w", err)
		}

		ts = tokenRespToTokenSet(tr)
		var identErr error
		identity, identErr = extractAccountIdentity(tr.IDToken)
		if identErr != nil {
			return nil, nil, fmt.Errorf("extract identity: %w", identErr)
		}
		ts.AccountID = identity.AccountID
	}

	if f.cfg.ProgressFn != nil {
		f.cfg.ProgressFn("saving_account")
	}

	ec := &EmailCredential{
		Email:     inbox.Address,
		Provider:  cfg.EmailProvider.Name(),
		Token:     inbox.Token,
		Password:  passwd,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}

	return &FlowResult{Tokens: ts, Identity: identity}, ec, nil
}

// HTTPRunRelogin executes the re-login flow using HTTP API calls (no browser).
func (f *AutoLoginFlow) HTTPRunRelogin(ctx context.Context, ec *EmailCredential, proxyURL string) (*FlowResult, error) {
	cfg := f.cfg

	if cfg.EmailProvider == nil {
		return nil, fmt.Errorf("email provider is not configured")
	}

	if f.cfg.BrowserClient != nil && ec.Email != "" {
		if f.cfg.ProgressFn != nil {
			f.cfg.ProgressFn("login")
		}

		inbox := &email.InboxInfo{
			Address:  ec.Email,
			Provider: ec.Provider,
			Token:    ec.Token,
		}
		getCode := func() (string, error) {
			msg, err := cfg.EmailProvider.WaitForEmail(ctx, inbox, cfg.PollTimeout, func(m *email.EmailMessage) bool {
				return email.ExtractVerificationCode(m) != ""
			})
			if err != nil {
				all, _ := cfg.EmailProvider.GetMessages(ctx, inbox)
				var subjects []string
				for _, m := range all {
					subjects = append(subjects, fmt.Sprintf("%s: %s", m.Date, m.Subject))
				}
				return "", fmt.Errorf("wait for code: %w (inbox has %d msgs: %v)", err, len(all), subjects)
			}
			code := email.ExtractVerificationCode(msg)
			if code == "" {
				return "", fmt.Errorf("no code in email")
			}
			return code, nil
		}

		result, err := f.HTTPCodexBrowserLogin(ctx, proxyURL, ec.Email, ec.Password, getCode)
		if err != nil {
			return nil, fmt.Errorf("browser login: %w", err)
		}
		return result, nil
	}

	// Fallback: HTTP-based login + platform OAuth
	client, err := NewChromeClient(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("create chrome client: %w", err)
	}
	defer client.Close()

	inbox := &email.InboxInfo{
		Address:  ec.Email,
		Provider: ec.Provider,
		Token:    ec.Token,
	}

	gen := NewSentinelTokenGenerator(client.DeviceID)

	if f.cfg.ProgressFn != nil {
		f.cfg.ProgressFn("init_session")
	}
	if err := client.InitSession(ctx, ec.Email, PlatformClientID, PlatformRedirectURI, "login", PlatformAuth0Client); err != nil {
		return nil, fmt.Errorf("init session: %w", err)
	}

	sentinelCont, err := gen.BuildSentinelToken(ctx, client.HTTPClient, SentinelFlowAuthorizeContinue)
	if err != nil {
		return nil, fmt.Errorf("sentinel login continue: %w", err)
	}

	if f.cfg.ProgressFn != nil {
		f.cfg.ProgressFn("filling_login_form")
	}
	if err := client.ContinueLogin(ctx, ec.Email, sentinelCont); err != nil {
		return nil, fmt.Errorf("continue login: %w", err)
	}

	if f.cfg.ProgressFn != nil {
		f.cfg.ProgressFn("platform_oauth")
	}

	// We need OTP verification for platform login flow.
	if f.cfg.ProgressFn != nil {
		f.cfg.ProgressFn("waiting_for_email_code")
	}
	msg, _ := cfg.EmailProvider.WaitForEmail(ctx, inbox, cfg.PollTimeout, func(m *email.EmailMessage) bool {
		return email.ExtractVerificationCode(m) != ""
	})
	code := ""
	if msg != nil {
		code = email.ExtractVerificationCode(msg)
	}
	if code != "" {
		if f.cfg.ProgressFn != nil {
			f.cfg.ProgressFn("validating_otp")
		}
		sentinelAuth, _ := gen.BuildSentinelToken(ctx, client.HTTPClient, SentinelFlowAuthorizeContinue)
		if err := client.ValidateOTP(ctx, code, sentinelAuth); err != nil {
			return nil, fmt.Errorf("validate otp: %w", err)
		}
	}

	// Continue with consent redirect chain + platform token exchange.
	verifier := client.PkceVerifier()
	if verifier == "" {
		return nil, fmt.Errorf("no pkce verifier from relogin session")
	}

	if ec.Password != "" {
		sentinelPw, _ := gen.BuildSentinelToken(ctx, client.HTTPClient, SentinelFlowPasswordVerify)
		pwResp, err := client.VerifyPassword(ctx, ec.Password, sentinelPw)
		if err != nil {
			return nil, fmt.Errorf("verify password: %w", err)
		}
		var pwData struct {
			ContinueURL string `json:"continue_url"`
		}
		json.Unmarshal(pwResp, &pwData)
		if pwData.ContinueURL != "" {
			// Use the continue_url from password verification for the consent flow.
			authCode, err := client.FollowConsent(ctx, pwData.ContinueURL)
			if err == nil {
				tr, err := exchangeCodeForTokensClient(ctx, authCode, verifier, PlatformRedirectURI, f.cfg.TokenURL, PlatformClientID, proxyURL)
				if err != nil {
					return nil, fmt.Errorf("exchange tokens: %w", err)
				}
				ts := tokenRespToTokenSet(tr)
				identity, err := extractAccountIdentity(tr.IDToken)
				if err != nil {
					return nil, fmt.Errorf("extract identity: %w", err)
				}
				ts.AccountID = identity.AccountID
				return &FlowResult{Tokens: ts, Identity: identity}, nil
			}
		}
	}

	// Fallback: consent without password (from OTP-only session).
	if f.cfg.ProgressFn != nil {
		f.cfg.ProgressFn("generating_pkce")
	}

	consentURL := AuthBase + "/sign-in-with-chatgpt/codex/consent"
	authCode, err := client.FollowConsent(ctx, consentURL)
	if err != nil {
		return nil, fmt.Errorf("follow consent: %w", err)
	}

	tr, err := exchangeCodeForTokensClient(ctx, authCode, verifier, PlatformRedirectURI, f.cfg.TokenURL, PlatformClientID, proxyURL)
	if err != nil {
		return nil, fmt.Errorf("exchange tokens: %w", err)
	}

	ts := tokenRespToTokenSet(tr)
	identity, err := extractAccountIdentity(tr.IDToken)
	if err != nil {
		return nil, fmt.Errorf("extract identity: %w", err)
	}
	ts.AccountID = identity.AccountID

	return &FlowResult{Tokens: ts, Identity: identity}, nil
}

// ──────────────────── 辅助 ────────────────────

// generatePassword 生成一个强随机密码，供自动注册使用。
func generatePassword() string {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789!@#$%^&*"
	const length = 24
	b := make([]byte, length)
	for i := range b {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
		b[i] = charset[n.Int64()]
	}
	return string(b)
}

// randomName returns a random first and last name for account creation.
func randomName() (string, string) {
	firstNames := []string{"James", "Robert", "John", "Michael", "David", "Mary", "Emma", "Olivia"}
	lastNames := []string{"Smith", "Johnson", "Williams", "Brown", "Jones", "Garcia", "Miller"}
	n1, _ := rand.Int(rand.Reader, big.NewInt(int64(len(firstNames))))
	n2, _ := rand.Int(rand.Reader, big.NewInt(int64(len(lastNames))))
	return firstNames[n1.Int64()], lastNames[n2.Int64()]
}

// randomBirthdate returns a random birthdate in YYYY-MM-DD format (1996-2006).
func randomBirthdate() string {
	year := 1996 + randomIntRange(0, 10)
	month := randomIntRange(1, 12)
	day := randomIntRange(1, 28)
	return fmt.Sprintf("%04d-%02d-%02d", year, month, day)
}

func randomIntRange(min, max int) int {
	n, _ := rand.Int(rand.Reader, big.NewInt(int64(max-min+1)))
	return min + int(n.Int64())
}

// proxyTransport creates an http.RoundTripper that routes through the given proxy.
// Supports socks5://, socks5h://, http://, and https:// proxy URLs.
func proxyTransport(proxyURL string) *http.Transport {
	transport := &http.Transport{}
	u, err := url.Parse(proxyURL)
	if err != nil {
		return transport
	}
	if strings.HasPrefix(proxyURL, "socks5://") || strings.HasPrefix(proxyURL, "socks5h://") {
		dialer, err := proxy.SOCKS5("tcp", u.Host, nil, proxy.Direct)
		if err != nil {
			return transport
		}
		if dc, ok := dialer.(interface {
			DialContext(context.Context, string, string) (net.Conn, error)
		}); ok {
			transport.DialContext = dc.DialContext
		}
	} else {
		transport.Proxy = http.ProxyURL(u)
	}
	return transport
}
