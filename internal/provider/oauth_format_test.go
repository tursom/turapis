package provider

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestOAuthTokensJSON_NewFormat(t *testing.T) {
	newFmt := `{"email":"t@test.com","credential":{"tokens":{"access_token":"eyJtest","refresh_token":"rt"}}}`
	tokens, at := OAuthTokensJSON(newFmt)
	if at != "eyJtest" {
		t.Fatalf("new format access_token = %q, want eyJtest", at)
	}
	if tokens["refresh_token"] != "rt" {
		t.Fatalf("new format refresh_token = %v", tokens["refresh_token"])
	}
}

func TestOAuthTokensJSON_OldFormat(t *testing.T) {
	oldFmt := `{"tokens":{"access_token":"eyJold","refresh_token":"rt_old"}}`
	tokens, at := OAuthTokensJSON(oldFmt)
	if at != "eyJold" {
		t.Fatalf("old format access_token = %q, want eyJold", at)
	}
	if tokens["refresh_token"] != "rt_old" {
		t.Fatalf("old format refresh_token = %v", tokens["refresh_token"])
	}
}

func TestExtractOAuthAccessToken(t *testing.T) {
	if at := ExtractOAuthAccessToken(`{"credential":{"tokens":{"access_token":"eyJ"}}}`); at != "eyJ" {
		t.Fatalf("got %q", at)
	}
	if at := ExtractOAuthAccessToken(`{"tokens":{"access_token":"eyJ"}}`); at != "eyJ" {
		t.Fatalf("got %q", at)
	}
	if at := ExtractOAuthAccessToken(`not json`); at != "" {
		t.Fatalf("got %q, want empty", at)
	}
}

func TestNormalizeOAuthCredential(t *testing.T) {
	newFmt := `{"email":"t@test.com","credential":{"tokens":{"access_token":"eyJtest"}}}`
	norm := NormalizeOAuthCredential(newFmt)
	if norm != `{"tokens":{"access_token":"eyJtest"}}` {
		t.Fatalf("got %s", norm)
	}

	oldFmt := `{"tokens":{"access_token":"eyJold"}}`
	norm = NormalizeOAuthCredential(oldFmt)
	if norm != oldFmt {
		t.Fatalf("old format should pass through, got %s", norm)
	}
}

func TestRefreshCodexTokenPreservesTokenMetadata(t *testing.T) {
	store := &refreshTestStore{
		apiKey: `{"email":"t@test.com","credential":{"tokens":{"access_token":"` + testAccessToken("client-test") + `","refresh_token":"rt-old","id_token":"id-old","scope":"openid","quota":{"primary":{"used_percent":42}},"last_discovered_models":["gpt-test"]}}}`,
	}
	updater := &refreshTestUpdater{}
	withTestCodexTokenServer(t, func(values url.Values) map[string]interface{} {
		if got := values.Get("client_id"); got != "client-test" {
			t.Fatalf("client_id = %q, want client-test", got)
		}
		if got := values.Get("refresh_token"); got != "rt-old" {
			t.Fatalf("refresh_token = %q, want rt-old", got)
		}
		return map[string]interface{}{
			"access_token": "new-access-token",
			"expires_in":   3600,
		}
	})

	if err := RefreshCodexToken(store, 7, "", updater); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if updater.accessToken != "new-access-token" {
		t.Fatalf("updater access token = %q", updater.accessToken)
	}
	tokens := decodeStoredTokens(t, store.apiKey)
	if tokens["access_token"] != "new-access-token" {
		t.Fatalf("access_token = %v", tokens["access_token"])
	}
	if tokens["refresh_token"] != "rt-old" {
		t.Fatalf("refresh_token = %v", tokens["refresh_token"])
	}
	if tokens["id_token"] != "id-old" {
		t.Fatalf("id_token = %v", tokens["id_token"])
	}
	if tokens["scope"] != "openid" {
		t.Fatalf("scope = %v", tokens["scope"])
	}
	if _, ok := tokens["quota"].(map[string]interface{}); !ok {
		t.Fatalf("quota was not preserved: %#v", tokens)
	}
	if _, ok := tokens["last_discovered_models"].([]interface{}); !ok {
		t.Fatalf("last_discovered_models was not preserved: %#v", tokens)
	}
}

func TestRefreshCodexTokenCASRetryPreservesConcurrentQuota(t *testing.T) {
	store := &refreshTestStore{
		apiKey: `{"credential":{"tokens":{"access_token":"` + testAccessToken("client-test") + `","refresh_token":"rt-old"}}}`,
		onFirstCAS: func(s *refreshTestStore) {
			s.apiKey = `{"credential":{"tokens":{"access_token":"` + testAccessToken("client-test") + `","refresh_token":"rt-old","quota":{"primary":{"used_percent":99}}}}}`
		},
	}
	withTestCodexTokenServer(t, func(url.Values) map[string]interface{} {
		return map[string]interface{}{
			"access_token":  "new-access-token",
			"refresh_token": "rt-new",
			"expires_in":    3600,
		}
	})

	if err := RefreshCodexToken(store, 8, "", nil); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	tokens := decodeStoredTokens(t, store.apiKey)
	if tokens["access_token"] != "new-access-token" {
		t.Fatalf("access_token = %v", tokens["access_token"])
	}
	if tokens["refresh_token"] != "rt-new" {
		t.Fatalf("refresh_token = %v", tokens["refresh_token"])
	}
	quota, ok := tokens["quota"].(map[string]interface{})
	if !ok {
		t.Fatalf("quota was not preserved: %#v", tokens)
	}
	primary := quota["primary"].(map[string]interface{})
	if primary["used_percent"] != float64(99) {
		t.Fatalf("quota used_percent = %v", primary["used_percent"])
	}
}

func TestOAuthAccessTokenExpiresAt(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	tests := []struct {
		name   string
		apiKey string
		want   time.Time
		wantOK bool
	}{
		{
			name:   "unix_millis",
			apiKey: `{"tokens":{"access_token":"eyJ","expires_at":1712345678901}}`,
			want:   time.UnixMilli(1712345678901),
			wantOK: true,
		},
		{
			name:   "unix_seconds_string",
			apiKey: `{"tokens":{"access_token":"eyJ","expires_at":"1712345678"}}`,
			want:   time.Unix(1712345678, 0),
			wantOK: true,
		},
		{
			name:   "rfc3339",
			apiKey: `{"tokens":{"access_token":"eyJ","expires_at":"` + now.Format(time.RFC3339) + `"}}`,
			want:   now,
			wantOK: true,
		},
		{
			name:   "missing",
			apiKey: `{"tokens":{"access_token":"eyJ"}}`,
			wantOK: false,
		},
		{
			name:   "invalid",
			apiKey: `{"tokens":{"access_token":"eyJ","expires_at":"bad"}}`,
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := OAuthAccessTokenExpiresAt(tt.apiKey)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if tt.wantOK && !got.Equal(tt.want) {
				t.Fatalf("expires_at = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestOAuthAccessTokenExpiresSoon(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	soon := now.Add(2 * time.Minute).UnixMilli()
	later := now.Add(20 * time.Minute).UnixMilli()

	if !OAuthAccessTokenExpiresSoon(`{"tokens":{"access_token":"eyJ","expires_at":`+jsonNumber(soon)+`}}`, now, 5*time.Minute) {
		t.Fatal("expected near-expiry token to be considered expiring soon")
	}
	if OAuthAccessTokenExpiresSoon(`{"tokens":{"access_token":"eyJ","expires_at":`+jsonNumber(later)+`}}`, now, 5*time.Minute) {
		t.Fatal("expected far-expiry token to not be considered expiring soon")
	}
	if OAuthAccessTokenExpiresSoon(`{"tokens":{"access_token":"eyJ"}}`, now, 5*time.Minute) {
		t.Fatal("missing expires_at should not be considered expiring soon")
	}
}

func TestRefreshCodexTokenReturnsErrorWhenUpdaterFails(t *testing.T) {
	store := &refreshTestStore{
		apiKey: `{"credential":{"tokens":{"access_token":"` + testAccessToken("client-test") + `","refresh_token":"rt-old"}}}`,
	}
	withTestCodexTokenServer(t, func(url.Values) map[string]interface{} {
		return map[string]interface{}{
			"access_token": "new-access-token",
			"expires_in":   3600,
		}
	})

	err := RefreshCodexToken(store, 9, "", refreshTestUpdaterFunc(func(int, string) error {
		return io.ErrUnexpectedEOF
	}))
	if err == nil {
		t.Fatal("expected updater failure to be returned")
	}
	if err.Error() == "" || !strings.Contains(err.Error(), "set provider api key in memory") {
		t.Fatalf("unexpected error: %v", err)
	}
}

type refreshTestStore struct {
	mu         sync.Mutex
	apiKey     string
	casCalls   int
	onFirstCAS func(*refreshTestStore)
}

func (s *refreshTestStore) GetProviderAPIKey(int) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.apiKey, nil
}

func (s *refreshTestStore) UpdateProviderAPIKey(_ int, apiKey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.apiKey = apiKey
	return nil
}

func (s *refreshTestStore) UpdateProviderAPIKeyIfCurrent(_ int, currentAPIKey string, newAPIKey string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.casCalls == 0 && s.onFirstCAS != nil {
		s.casCalls++
		s.onFirstCAS(s)
		return false, nil
	}
	s.casCalls++
	if s.apiKey != currentAPIKey {
		return false, nil
	}
	s.apiKey = newAPIKey
	return true, nil
}

type refreshTestUpdater struct {
	accessToken string
}

func (u *refreshTestUpdater) SetProviderAPIKey(_ int, accessToken string) error {
	u.accessToken = accessToken
	return nil
}

type refreshTestUpdaterFunc func(int, string) error

func (f refreshTestUpdaterFunc) SetProviderAPIKey(providerID int, accessToken string) error {
	return f(providerID, accessToken)
}

func withTestCodexTokenServer(t *testing.T, handler func(url.Values) map[string]interface{}) {
	t.Helper()
	oldURL := codexTokenURL
	oldClient := newCodexTokenHTTPClient
	newCodexTokenHTTPClient = func(string) *http.Client {
		return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.String() != codexTokenURL {
				t.Fatalf("request URL = %s, want %s", r.URL.String(), codexTokenURL)
			}
			if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
				t.Fatalf("content type = %q", r.Header.Get("Content-Type"))
			}
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse form: %v", err)
			}
			body, _ := json.Marshal(handler(r.PostForm))
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(bytes.NewReader(body)),
				Request:    r,
			}, nil
		})}
	}
	t.Cleanup(func() {
		codexTokenURL = oldURL
		newCodexTokenHTTPClient = oldClient
	})
	codexTokenURL = "https://auth.test/oauth/token"
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func decodeStoredTokens(t *testing.T, apiKey string) map[string]interface{} {
	t.Helper()
	tokens, _ := OAuthTokensJSON(apiKey)
	if tokens == nil {
		t.Fatalf("stored credential has no tokens: %s", apiKey)
	}
	return tokens
}

func testAccessToken(clientID string) string {
	payload, _ := json.Marshal(map[string]string{"client_id": clientID})
	return "eyJ." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func jsonNumber(v int64) string {
	return strconv.FormatInt(v, 10)
}
