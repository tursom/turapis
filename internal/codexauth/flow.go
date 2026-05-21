package codexauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tursom/turapis/internal/email"
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

func (f *AutoLoginFlow) redirectURI() string {
	return fmt.Sprintf("http://localhost:%d/auth/callback", f.cfg.CallbackPort)
}

// ──────────────────── 回调服务器 ────────────────────

// callbackServer 在本地监听 OAuth 授权码回调。
type callbackServer struct {
	codeCh  chan string
	errCh   chan error
	srv     *http.Server
	state   string
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
		"client_id":             {codexClientID},
		"redirect_uri":          {redirectURI},
		"response_type":         {"code"},
		"scope":                 {"openid profile email offline_access"},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	return "https://auth.openai.com/authorize?" + params.Encode()
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

// extractAccountIdentity 从 id_token JWT 中解析账号身份信息。
// 注意：由于未接入 Auth0/OpenAI JWKS 验签，此函数仅做结构完整性校验
//（alg 非 none、exp/iss/aud 匹配），不提供密码学信任保证。
// 调用方不应将返回的身份信息用于超出展示层的安全决策。
func extractAccountIdentity(idToken string) (*AccountIdentity, error) {
	parts := strings.Split(idToken, ".")
	if len(parts) < 2 {
		return nil, fmt.Errorf("invalid jwt: not enough parts")
	}

	// 解码 header，校验 alg 非 none（防算法混淆攻击）
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
		Exp      int64  `json:"exp"`
		Iss      string `json:"iss"`
		Aud      any    `json:"aud"`
	}
	if err := json.Unmarshal(payloadJSON, &claims); err != nil {
		return nil, fmt.Errorf("parse jwt claims: %w", err)
	}

	// 校验 exp：必须存在且在将来（允许 60s 时钟偏差）
	if claims.Exp == 0 {
		return nil, fmt.Errorf("jwt missing exp claim")
	}
	if time.Unix(claims.Exp, 0).Before(time.Now().Add(-60 * time.Second)) {
		return nil, fmt.Errorf("jwt expired at %d (%s)", claims.Exp,
			time.Unix(claims.Exp, 0).Format(time.RFC3339))
	}

	// 校验 iss
	if claims.Iss == "" {
		return nil, fmt.Errorf("jwt missing iss claim")
	}
	if claims.Iss != codexIssuer {
		return nil, fmt.Errorf("jwt iss %q, want %q", claims.Iss, codexIssuer)
	}

	// 校验 aud：必须包含 codexClientID
	if !audContains(claims.Aud, codexClientID) {
		return nil, fmt.Errorf("jwt aud does not contain client_id %q", codexClientID)
	}

	return &AccountIdentity{
		Email:     claims.Email,
		AccountID: claims.Sub,
		UserID:    claims.UserID,
		PlanType:  claims.PlanType,
	}, nil
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

	// ① 创建临时邮箱
	inbox, err := cfg.EmailProvider.CreateInbox(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("create inbox: %w", err)
	}

	// ② 打开浏览器会话
	bCtx, cancel := cfg.BrowserClient.NewContext(ctx)
	defer cancel()

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

	// ④ 等待验证邮件
	msg, err := cfg.EmailProvider.WaitForEmail(ctx, inbox, cfg.PollTimeout, func(m *email.EmailMessage) bool {
		return strings.Contains(m.Subject, "Verify") || strings.Contains(m.Subject, "email")
	})
	if err != nil {
		return nil, nil, fmt.Errorf("wait for verification email: %w", err)
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

	// ⑧ 生成 PKCE
	verifier, challenge, err := generatePKCE()
	if err != nil {
		return nil, nil, fmt.Errorf("generate pkce: %w", err)
	}
	state, err := generateState()
	if err != nil {
		return nil, nil, fmt.Errorf("generate state: %w", err)
	}

	// ⑨ 启动回调服务器（在导航之前）
	cs, err := startCallbackServer(ctx, cfg.CallbackPort, state)
	if err != nil {
		return nil, nil, fmt.Errorf("start callback server: %w", err)
	}
	defer cs.shutdown()

	// ⑩ 浏览器导航到授权 URL
	authURL := buildAuthorizeURL(challenge, state, f.redirectURI())
	if err := cfg.BrowserClient.Navigate(bCtx, authURL); err != nil {
		return nil, nil, fmt.Errorf("navigate authorize url: %w", err)
	}

	// ⑪ 等待回调
	code, err := cs.wait(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("wait for callback: %w", err)
	}

	// ⑫ 交换令牌
	tr, err := exchangeCodeForTokens(ctx, code, verifier, f.redirectURI(), f.cfg.TokenURL)
	if err != nil {
		return nil, nil, fmt.Errorf("exchange code for tokens: %w", err)
	}

	// ⑬ 解析 JWT 获取身份信息
	ts := tokenRespToTokenSet(tr)
	identity, err := extractAccountIdentity(tr.IDToken)
	if err != nil {
		return nil, nil, fmt.Errorf("extract identity: %w", err)
	}
	ts.AccountID = identity.AccountID

	// ⑭ 构造并返回邮箱凭证
	ec := &EmailCredential{
		Email:     inbox.Address,
		Provider:  cfg.EmailProvider.Name(),
		Token:     inbox.Token,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}

	return &FlowResult{Tokens: ts, Identity: identity}, ec, nil
}

// ──────────────────── 场景 B：已有邮箱登录 ────────────────────

// RunLogin 启动登录流程（场景 B），返回授权 URL 并阻塞等待回调完成。
// 调用方负责在浏览器中打开 authURL。
func (f *AutoLoginFlow) RunLogin(ctx context.Context) (authURL string, result *FlowResult, err error) {
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
	defer cs.shutdown()

	authURL = buildAuthorizeURL(challenge, state, f.redirectURI())

	code, err := cs.wait(ctx)
	if err != nil {
		return authURL, nil, fmt.Errorf("wait for callback: %w", err)
	}

	tr, err := exchangeCodeForTokens(ctx, code, verifier, f.redirectURI(), f.cfg.TokenURL)
	if err != nil {
		return authURL, nil, fmt.Errorf("exchange code for tokens: %w", err)
	}

	ts := tokenRespToTokenSet(tr)
	identity, err := extractAccountIdentity(tr.IDToken)
	if err != nil {
		return authURL, nil, fmt.Errorf("extract identity: %w", err)
	}
	ts.AccountID = identity.AccountID

	return authURL, &FlowResult{Tokens: ts, Identity: identity}, nil
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

	// ① 打开浏览器，导航到登录页
	bCtx, cancel := cfg.BrowserClient.NewContext(ctx)
	defer cancel()

	if err := cfg.BrowserClient.Navigate(bCtx, "https://auth.openai.com/login"); err != nil {
		return nil, fmt.Errorf("navigate login: %w", err)
	}
	if err := cfg.BrowserClient.WaitForSelector(bCtx, "input[type=email]"); err != nil {
		return nil, fmt.Errorf("wait for email input: %w", err)
	}
	if err := cfg.BrowserClient.SendKeys(bCtx, "input[type=email]", ec.Email); err != nil {
		return nil, fmt.Errorf("fill email: %w", err)
	}
	if err := cfg.BrowserClient.Click(bCtx, "button[type=submit]"); err != nil {
		return nil, fmt.Errorf("submit email: %w", err)
	}

	// ② 等待验证码邮件
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

	// ③ 在浏览器中填写验证码
	if err := cfg.BrowserClient.WaitForSelector(bCtx, "input[type=text]"); err != nil {
		return nil, fmt.Errorf("wait for code input: %w", err)
	}
	if err := cfg.BrowserClient.SendKeys(bCtx, "input[type=text]", code); err != nil {
		return nil, fmt.Errorf("fill code: %w", err)
	}
	if err := cfg.BrowserClient.Click(bCtx, "button[type=submit]"); err != nil {
		return nil, fmt.Errorf("submit code: %w", err)
	}

	// ④ 生成 PKCE 并获取 token
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
