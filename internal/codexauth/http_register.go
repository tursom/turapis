package codexauth

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/proxy"
)

const (
	AuthBase            = "https://auth.openai.com"
	PlatformClientID    = "app_2SKx67EdpoN0G6j64rFvigXD"
	PlatformRedirectURI = "https://platform.openai.com/auth/callback"
	PlatformAuth0Client = "eyJuYW1lIjoiYXV0aDAtc3BhLWpzIiwidmVyc2lvbiI6IjEuMjEuMCJ9"
	PlatformAudience    = "https://api.openai.com/v1"
)

var chromeJSONHeaders = http.Header{
	"accept":             {"application/json"},
	"accept-language":    {"en-US,en;q=0.9"},
	"content-type":       {"application/json"},
	"sec-ch-ua":          {`"Chromium";v="145", "Not:A-Brand";v="24", "Google Chrome";v="145"`},
	"sec-ch-ua-mobile":   {"?0"},
	"sec-ch-ua-platform": {`"Windows"`},
	"sec-fetch-dest":     {"empty"},
	"sec-fetch-mode":     {"cors"},
	"sec-fetch-site":     {"same-origin"},
	"user-agent":         {UserAgent},
}

var chromeNavigateHeaders = http.Header{
	"accept":                    {"text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8"},
	"accept-language":           {"en-US,en;q=0.9"},
	"sec-ch-ua":                 {`"Chromium";v="145", "Not:A-Brand";v="24", "Google Chrome";v="145"`},
	"sec-ch-ua-mobile":          {"?0"},
	"sec-ch-ua-platform":        {`"Windows"`},
	"sec-fetch-dest":            {"document"},
	"sec-fetch-mode":            {"navigate"},
	"sec-fetch-site":            {"same-origin"},
	"sec-fetch-user":            {"?1"},
	"upgrade-insecure-requests": {"1"},
	"user-agent":                {UserAgent},
}

// ChromeClient is an HTTP client that mimics Chrome browser headers
// and maintains a cookie jar for session persistence across
// auth.openai.com registration API calls.
type ChromeClient struct {
	HTTPClient   *http.Client
	DeviceID     string
	UserAgent    string
	pkceVerifier string   // stored from InitSession for token exchange
	auth0State   string   // stored from InitSession for callback state validation
}

// NewChromeClient creates a ChromeClient with Chrome-mimicking headers.
// If proxyURL starts with "socks5://", a SOCKS5 dialer is used.
// If proxyURL starts with "http", the standard HTTP_PROXY mechanism applies.
func NewChromeClient(proxyURL string) (*ChromeClient, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("create cookie jar: %w", err)
	}

	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}

	if strings.HasPrefix(proxyURL, "socks5://") || strings.HasPrefix(proxyURL, "socks5h://") {
		u, err := url.Parse(proxyURL)
		if err != nil {
			return nil, fmt.Errorf("parse proxy url: %w", err)
		}
		dialer, err := proxy.SOCKS5("tcp", u.Host, nil, proxy.Direct)
		if err != nil {
			return nil, fmt.Errorf("create socks5 dialer: %w", err)
		}
		transport.DialContext = dialer.(interface {
			DialContext(ctx context.Context, network, addr string) (net.Conn, error)
		}).DialContext
		// Ensure DialContext is set
		if dc, ok := dialer.(interface{ DialContext(context.Context, string, string) (net.Conn, error) }); ok {
			transport.DialContext = dc.DialContext
		}
	} else if proxyURL != "" {
		transport.Proxy = func(req *http.Request) (*url.URL, error) {
			return url.Parse(proxyURL)
		}
	}

	return &ChromeClient{
		HTTPClient: &http.Client{
			Jar:       jar,
			Transport: transport,
			Timeout:   60 * time.Second,
		},
		DeviceID:  generateUUID(),
		UserAgent: UserAgent,
	}, nil
}

// Close closes idle HTTP connections.
func (c *ChromeClient) Close() {
	c.HTTPClient.CloseIdleConnections()
}

// setCommonHeaders applies Chrome JSON API headers and device ID to a request.
func (c *ChromeClient) setCommonHeaders(req *http.Request, referer string) {
	for k, vs := range chromeJSONHeaders {
		for _, v := range vs {
			req.Header.Set(k, v)
		}
	}
	req.Header.Set("oai-device-id", c.DeviceID)
	req.Header.Set("origin", AuthBase)
	if referer != "" {
		req.Header.Set("referer", referer)
	}
}

// setNavigateHeaders applies Chrome navigation headers to a request.
func (c *ChromeClient) setNavigateHeaders(req *http.Request) {
	for k, vs := range chromeNavigateHeaders {
		for _, v := range vs {
			req.Header.Set(k, v)
		}
	}
	req.Header.Set("oai-device-id", c.DeviceID)
}

// doJSON performs a JSON request with Chrome headers and reads the response body.
func (c *ChromeClient) doJSON(ctx context.Context, method, urlStr, referer string, body any) ([]byte, int, error) {
	var bodyReader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, 0, fmt.Errorf("marshal body: %w", err)
		}
		bodyReader = strings.NewReader(string(raw))
	}

	req, err := http.NewRequestWithContext(ctx, method, urlStr, bodyReader)
	if err != nil {
		return nil, 0, fmt.Errorf("create request: %w", err)
	}
	c.setCommonHeaders(req, referer)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("request %s %s: %w", method, urlStr, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 128*1024))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read response: %w", err)
	}

	return raw, resp.StatusCode, nil
}

// InitSession starts an auth session for registration or login.
// screenHint: "login_or_signup" for registration, "login" for login flows.
func (c *ChromeClient) InitSession(ctx context.Context, email, clientID, redirectURI, screenHint, auth0Client string) error {
	verifier, challenge, err := generatePKCE()
	if err != nil {
		return fmt.Errorf("init session pkce: %w", err)
	}
	state, err := generateState()
	if err != nil {
		return fmt.Errorf("init session state: %w", err)
	}
	nonce, err := generateState()
	if err != nil {
		return fmt.Errorf("init session nonce: %w", err)
	}

	params := url.Values{
		"issuer":                 {AuthBase},
		"client_id":              {clientID},
		"audience":               {PlatformAudience},
		"redirect_uri":           {redirectURI},
		"device_id":              {c.DeviceID},
		"screen_hint":            {screenHint},
		"max_age":                {"0"},
		"login_hint":             {email},
		"scope":                  {"openid profile email offline_access"},
		"response_type":          {"code"},
		"response_mode":          {"query"},
		"state":                  {state},
		"nonce":                  {nonce},
		"code_challenge":         {challenge},
		"code_challenge_method":  {"S256"},
	}
	if auth0Client != "" {
		params.Set("auth0Client", auth0Client)
	}

	// Store PKCE verifier on the client for later use by FollowAuthorize.
	// We keep the verifier because the authorize API sets session cookies
	// that allow us to later extract the OAuth authorization code.
	_ = verifier

	reqURL := fmt.Sprintf("%s/api/accounts/authorize?%s", AuthBase, params.Encode())
	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return fmt.Errorf("init session request: %w", err)
	}
	c.setNavigateHeaders(req)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("init session: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("init_session_http_%d: %s", resp.StatusCode, string(body))
	}

	// Set oai-did cookie explicitly (some endpoints check for it)
	c.HTTPClient.Jar.SetCookies(req.URL, []*http.Cookie{
		{Name: "oai-did", Value: c.DeviceID, Domain: ".auth.openai.com", Path: "/"},
		{Name: "oai-did", Value: c.DeviceID, Domain: "auth.openai.com", Path: "/"},
	})

	// Store PKCE verifier and state for later use
	c.pkceVerifier = verifier
	c.auth0State = state

	return nil
}

// PkceVerifier returns the PKCE code verifier set by the last InitSession call.
func (c *ChromeClient) PkceVerifier() string {
	return c.pkceVerifier
}

// Auth0State returns the authorization state set by the last InitSession call.
func (c *ChromeClient) Auth0State() string {
	return c.auth0State
}

// AuthCookies returns all cookies in the jar for the auth.openai.com domain.
// These can be injected into a browser session to skip Cloudflare and login.
func (c *ChromeClient) AuthCookies() []*http.Cookie {
	u, _ := url.Parse(AuthBase)
	return c.HTTPClient.Jar.Cookies(u)
}

// RegisterUser creates a new user account on auth.openai.com.
func (c *ChromeClient) RegisterUser(ctx context.Context, email, password, sentinelToken string) error {
	raw, status, err := c.doJSON(ctx, "POST", AuthBase+"/api/accounts/user/register",
		AuthBase+"/create-account/password",
		map[string]string{"username": email, "password": password},
	)
	if err != nil {
		return fmt.Errorf("register user: %w", err)
	}
	if status != 200 {
		return fmt.Errorf("register_user_http_%d: %s", status, string(raw))
	}
	return nil
}

// SendOTP requests an OTP code to the registered email.
func (c *ChromeClient) SendOTP(ctx context.Context) error {
	reqURL := fmt.Sprintf("%s/api/accounts/email-otp/send", AuthBase)
	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return fmt.Errorf("send otp request: %w", err)
	}
	c.setNavigateHeaders(req)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("send otp: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 && resp.StatusCode != 302 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("send_otp_http_%d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// ValidateOTP validates the OTP code. It first tries without sentinel token,
// and if that fails, retries with sentinel token (matching chatgpt2api's retry pattern).
func (c *ChromeClient) ValidateOTP(ctx context.Context, code, sentinelToken string) error {
	// First attempt without sentinel
	raw, status, err := c.doJSON(ctx, "POST", AuthBase+"/api/accounts/email-otp/validate",
		AuthBase+"/email-verification",
		map[string]string{"code": code},
	)
	if err != nil {
		return fmt.Errorf("validate otp: %w", err)
	}
	if status == 200 {
		return nil
	}

	// Retry with sentinel token if first attempt failed
	if sentinelToken != "" {
		raw, status, err = c.doJSONWithSentinel(ctx, "POST", AuthBase+"/api/accounts/email-otp/validate",
			AuthBase+"/email-verification",
			map[string]string{"code": code},
			sentinelToken,
		)
		if err != nil {
			return fmt.Errorf("validate otp retry: %w", err)
		}
		if status == 200 {
			return nil
		}
		return fmt.Errorf("validate_otp_http_%d: %s", status, string(raw))
	}
	return fmt.Errorf("validate_otp_http_%d: %s", status, string(raw))
}

// CreateAccount sets the user's name and birthdate.
func (c *ChromeClient) CreateAccount(ctx context.Context, name, birthdate, sentinelToken string) error {
	raw, status, err := c.doJSONWithSentinel(ctx, "POST", AuthBase+"/api/accounts/create_account",
		AuthBase+"/about-you",
		map[string]string{"name": name, "birthdate": birthdate},
		sentinelToken,
	)
	if err != nil {
		return fmt.Errorf("create account: %w", err)
	}
	if status != 200 && status != 302 {
		return fmt.Errorf("create_account_http_%d: %s", status, string(raw))
	}
	return nil
}

// ContinueLogin submits the email during the login flow.
func (c *ChromeClient) ContinueLogin(ctx context.Context, email, sentinelToken string) error {
	body := map[string]any{
		"username": map[string]string{
			"kind":  "email",
			"value": email,
		},
	}
	raw, status, err := c.doJSONWithSentinel(ctx, "POST", AuthBase+"/api/accounts/authorize/continue",
		AuthBase+"/log-in?usernameKind=email",
		body,
		sentinelToken,
	)
	if err != nil {
		return fmt.Errorf("continue login: %w", err)
	}
	if status != 200 {
		return fmt.Errorf("continue_login_http_%d: %s", status, string(raw))
	}
	return nil
}

// VerifyPassword verifies the password during the login flow.
// Returns the response body for extracting continue_url and page_type.
func (c *ChromeClient) VerifyPassword(ctx context.Context, password, sentinelToken string) ([]byte, error) {
	raw, status, err := c.doJSONWithSentinel(ctx, "POST", AuthBase+"/api/accounts/password/verify",
		AuthBase+"/log-in/password",
		map[string]string{"password": password},
		sentinelToken,
	)
	if err != nil {
		return nil, fmt.Errorf("verify password: %w", err)
	}
	if status != 200 {
		return nil, fmt.Errorf("password_verify_http_%d: %s", status, string(raw))
	}
	return raw, nil
}

// FollowConsent follows the consent URL returned by password verification
// through the redirect chain and extracts the OAuth authorization code.
func (c *ChromeClient) FollowConsent(ctx context.Context, consentURL string) (string, error) {
	currentURL := consentURL
	if strings.HasPrefix(currentURL, "/") {
		currentURL = AuthBase + currentURL
	}

	client := &http.Client{
		Jar: c.HTTPClient.Jar,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Timeout: 30 * time.Second,
	}

	for i := 0; i < 10; i++ {
		req, err := http.NewRequestWithContext(ctx, "GET", currentURL, nil)
		if err != nil {
			return "", fmt.Errorf("follow consent request: %w", err)
		}
		c.setNavigateHeaders(req)

		resp, err := client.Do(req)
		if err != nil {
			return "", fmt.Errorf("follow consent: %w", err)
		}

		if code := extractCodeFromLocation(resp); code != "" {
			resp.Body.Close()
			return code, nil
		}
		if code := extractCodeFromURL(resp.Request.URL); code != "" {
			resp.Body.Close()
			return code, nil
		}

		location := resp.Header.Get("Location")
		resp.Body.Close()

		if location == "" {
			break
		}
		if strings.HasPrefix(location, "/") {
			currentURL = AuthBase + location
		} else {
			currentURL = location
		}
	}

	return "", fmt.Errorf("follow_consent_no_code")
}

// doJSONWithSentinel performs a JSON request with Chrome headers AND sentinel token.
func (c *ChromeClient) doJSONWithSentinel(ctx context.Context, method, urlStr, referer string, body any, sentinelToken string) ([]byte, int, error) {
	var bodyReader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, 0, fmt.Errorf("marshal body: %w", err)
		}
		bodyReader = strings.NewReader(string(raw))
	}

	req, err := http.NewRequestWithContext(ctx, method, urlStr, bodyReader)
	if err != nil {
		return nil, 0, fmt.Errorf("create request: %w", err)
	}
	c.setCommonHeaders(req, referer)
	req.Header.Set("openai-sentinel-token", sentinelToken)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("request %s %s: %w", method, urlStr, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 128*1024))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read response: %w", err)
	}

	return raw, resp.StatusCode, nil
}

// extractCodeFromLocation extracts the OAuth authorization code from a response's Location header.
func extractCodeFromLocation(resp *http.Response) string {
	location := resp.Header.Get("Location")
	if location == "" {
		return ""
	}
	u, err := url.Parse(location)
	if err != nil {
		return ""
	}
	return u.Query().Get("code")
}

// extractCodeFromURL extracts the OAuth authorization code from a URL.
func extractCodeFromURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	return u.Query().Get("code")
}

// BuildCodexAuthorizeURL constructs the standard OAuth authorize URL for the Codex client.
func BuildCodexAuthorizeURL(clientID, redirectURI, challenge, state string) string {
	params := url.Values{
		"client_id":             {clientID},
		"redirect_uri":          {redirectURI},
		"response_type":         {"code"},
		"scope":                 {"openid profile email offline_access"},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	return AuthBase + "/oauth/authorize?" + params.Encode()
}


