package codexauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	codexTokenURL = "https://auth.openai.com/oauth/token"
	codexClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	codexIssuer   = "https://auth.openai.com"
)

type AutoLoginFlow struct {
	cfg FlowConfig
}

func NewAutoLoginFlow(cfg FlowConfig) *AutoLoginFlow {
	return &AutoLoginFlow{cfg: cfg}
}

func (f *AutoLoginFlow) redirectURI() string {
	return fmt.Sprintf("http://localhost:%d/auth/callback", f.cfg.CallbackPort)
}

func StartLogin(ctx context.Context, cfg FlowConfig) (authURL string, wait func(context.Context) (*FlowResult, error), err error) {
	verifier, challenge, err := generatePKCE()
	if err != nil {
		return "", nil, fmt.Errorf("generate pkce: %w", err)
	}
	state, err := generateState()
	if err != nil {
		return "", nil, fmt.Errorf("generate state: %w", err)
	}

	redisURI := fmt.Sprintf("http://localhost:%d/auth/callback", cfg.CallbackPort)
	cs, err := startCallbackServer(cfg.CallbackPort, state)
	if err != nil {
		return "", nil, fmt.Errorf("start callback server: %w", err)
	}

	authURL = buildAuthorizeURL(challenge, state, redisURI)

	wait = func(waitCtx context.Context) (*FlowResult, error) {
		defer cs.shutdown()

		code, wErr := cs.wait(waitCtx)
		if wErr != nil {
			return nil, fmt.Errorf("wait for callback: %w", wErr)
		}

		tokenURL := cfg.TokenURL
		if tokenURL == "" {
			tokenURL = codexTokenURL
		}

		tr, wErr := exchangeCodeForTokens(waitCtx, code, verifier, redisURI, tokenURL)
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

type callbackServer struct {
	codeCh chan string
	errCh  chan error
	srv    *http.Server
	state  string
	ln     net.Listener
}

func startCallbackServer(port int, expectedState string) (*callbackServer, error) {
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

		if errStr != "" {
			errDesc := q.Get("error_description")
			select {
			case cs.errCh <- fmt.Errorf("authorization error: %s - %s", errStr, errDesc):
			default:
			}
			http.Error(w, "Authorization failed", http.StatusBadRequest)
			return
		}

		callbackState := q.Get("state")
		if callbackState == "" {
			http.Error(w, "Missing state", http.StatusBadRequest)
			return
		}
		if callbackState != expectedState {
			http.Error(w, "State mismatch", http.StatusForbidden)
			return
		}

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
	cs.ln = listener

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

func (cs *callbackServer) shutdown() {
	if cs.srv != nil {
		_ = cs.srv.Close()
	}
}

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

func generateState() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate state random: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func buildAuthorizeURL(challenge, state, redirectURI string) string {
	params := url.Values{
		"client_id":                  {codexClientID},
		"redirect_uri":               {redirectURI},
		"response_type":              {"code"},
		"scope":                      {"openid profile email offline_access api.connectors.read api.connectors.invoke"},
		"state":                      {state},
		"code_challenge":             {challenge},
		"code_challenge_method":      {"S256"},
		"id_token_add_organizations": {"true"},
		"codex_cli_simplified_flow":  {"true"},
		"originator":                 {"codex_cli"},
	}
	return "https://auth.openai.com/oauth/authorize?" + params.Encode()
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	ExpiresIn    int    `json:"expires_in"`
}

func exchangeCodeForTokens(ctx context.Context, code, verifier, redirectURI, tokenURL string) (*tokenResponse, error) {
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

func tokenRespToTokenSet(tr *tokenResponse) *TokenSet {
	return &TokenSet{
		AccessToken:  tr.AccessToken,
		RefreshToken: tr.RefreshToken,
		IDToken:      tr.IDToken,
		AccountID:    "",
		ExpiresAt:    time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second).UnixMilli(),
	}
}

func extractAccountIdentity(idToken string) (*AccountIdentity, error) {
	identity, err := extractJWTIdentity(idToken)
	if err != nil {
		return nil, err
	}
	return identity, nil
}

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

func b64Decode(s string) ([]byte, error) {
	switch len(s) % 4 {
	case 2:
		s += "=="
	case 3:
		s += "="
	}
	return base64.URLEncoding.DecodeString(s)
}

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
