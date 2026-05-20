package email

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// ============================================================================
// buildTransport
// ============================================================================

func TestBuildTransport_EmptyProxy(t *testing.T) {
	tr := buildTransport("")
	if tr == nil {
		t.Fatal("buildTransport returned nil")
	}
	if tr.Proxy != nil {
		t.Error("Proxy should be nil for empty proxy URL")
	}
	if tr.DialContext == nil {
		t.Error("DialContext should not be nil")
	}
}

func TestBuildTransport_HTTPProxy(t *testing.T) {
	tr := buildTransport("http://proxy.example.com:8080")
	if tr.Proxy == nil {
		t.Fatal("Proxy should be set for HTTP proxy URL")
	}

	req, _ := http.NewRequest("GET", "http://target.example.com", nil)
	proxyURL, err := tr.Proxy(req)
	if err != nil {
		t.Fatalf("Proxy returned error: %v", err)
	}
	if proxyURL == nil {
		t.Fatal("Proxy URL should not be nil")
	}
	if proxyURL.Host != "proxy.example.com:8080" {
		t.Errorf("unexpected proxy host: %s", proxyURL.Host)
	}
}

func TestBuildTransport_SOCKS5Proxy(t *testing.T) {
	tr := buildTransport("socks5://proxy.example.com:1080")
	// SOCKS5 uses DialContext, not Proxy.
	if tr.Proxy != nil {
		t.Error("Proxy should be nil for SOCKS5 (DialContext is used instead)")
	}
	if tr.DialContext == nil {
		t.Error("DialContext should not be nil")
	}
}

func TestBuildTransport_InvalidURL_NoHost(t *testing.T) {
	tr := buildTransport("://invalid")
	if tr == nil {
		t.Fatal("buildTransport returned nil for invalid URL")
	}
	if tr.Proxy != nil {
		t.Error("Proxy should be nil for URL with no host")
	}
	if tr.DialContext == nil {
		t.Error("DialContext should not be nil for invalid proxy URL")
	}
}

func TestBuildTransport_InvalidURL_EmptyHost(t *testing.T) {
	tr := buildTransport("http://")
	if tr == nil {
		t.Fatal("buildTransport returned nil for URL with empty host")
	}
	if tr.Proxy != nil {
		t.Error("Proxy should be nil when host is empty")
	}
}

func TestBuildTransport_Defaults(t *testing.T) {
	tr := buildTransport("")
	if tr.MaxIdleConns != 100 {
		t.Errorf("MaxIdleConns = %d, want 100", tr.MaxIdleConns)
	}
	if tr.MaxIdleConnsPerHost != 10 {
		t.Errorf("MaxIdleConnsPerHost = %d, want 10", tr.MaxIdleConnsPerHost)
	}
	if tr.IdleConnTimeout != 90*time.Second {
		t.Errorf("IdleConnTimeout = %v, want 90s", tr.IdleConnTimeout)
	}
	if tr.TLSHandshakeTimeout != 10*time.Second {
		t.Errorf("TLSHandshakeTimeout = %v, want 10s", tr.TLSHandshakeTimeout)
	}
	if tr.ForceAttemptHTTP2 {
		t.Error("ForceAttemptHTTP2 should be false")
	}
}

func TestBuildTransport_HTTPProxyRespected(t *testing.T) {
	t.Skip("requires network listener (httptest not available in this environment)")
}

// ============================================================================
// ExtractVerificationCode
// ============================================================================

func TestExtractVerificationCode_NilMessage(t *testing.T) {
	if got := ExtractVerificationCode(nil); got != "" {
		t.Errorf("expected empty for nil message, got %q", got)
	}
}

func TestExtractVerificationCode_FromBody(t *testing.T) {
	msg := &EmailMessage{
		Body: "Your verification code is 123456. Do not share it.",
	}
	if got := ExtractVerificationCode(msg); got != "123456" {
		t.Errorf("expected 123456, got %q", got)
	}
}

func TestExtractVerificationCode_FromHTML(t *testing.T) {
	msg := &EmailMessage{
		HTML: "<p>Code: <strong>654321</strong></p>",
	}
	if got := ExtractVerificationCode(msg); got != "654321" {
		t.Errorf("expected 654321, got %q", got)
	}
}

func TestExtractVerificationCode_FromSubject(t *testing.T) {
	msg := &EmailMessage{
		Subject: "999888 is your verification code",
	}
	if got := ExtractVerificationCode(msg); got != "999888" {
		t.Errorf("expected 999888, got %q", got)
	}
}

func TestExtractVerificationCode_PrefersBodyOverSubject(t *testing.T) {
	msg := &EmailMessage{
		Body:    "Code: 111111",
		Subject: "Code: 222222",
	}
	if got := ExtractVerificationCode(msg); got != "111111" {
		t.Errorf("expected body code 111111, got %q", got)
	}
}

func TestExtractVerificationCode_NoMatch(t *testing.T) {
	msg := &EmailMessage{
		Body:    "Hello, no code here.",
		Subject: "Welcome!",
	}
	if got := ExtractVerificationCode(msg); got != "" {
		t.Errorf("expected empty when no code present, got %q", got)
	}
}

func TestExtractVerificationCode_AllEmptySources(t *testing.T) {
	msg := &EmailMessage{}
	if got := ExtractVerificationCode(msg); got != "" {
		t.Errorf("expected empty for empty message, got %q", got)
	}
}

func TestExtractVerificationCode_ShortNumberIgnored(t *testing.T) {
	msg := &EmailMessage{Body: "Code: 12345."}
	if got := ExtractVerificationCode(msg); got != "" {
		t.Errorf("expected empty for 5-digit number, got %q", got)
	}
}

func TestExtractVerificationCode_LongNumberIgnored(t *testing.T) {
	msg := &EmailMessage{Body: "Code: 1234567."}
	if got := ExtractVerificationCode(msg); got != "" {
		t.Errorf("expected empty for 7-digit number, got %q", got)
	}
}

func TestExtractVerificationCode_MultipleCodes_ReturnsFirst(t *testing.T) {
	msg := &EmailMessage{Body: "Codes: 111111 and 222222."}
	if got := ExtractVerificationCode(msg); got != "111111" {
		t.Errorf("expected first code 111111, got %q", got)
	}
}

func TestExtractVerificationCode_CodeAtWordBoundary(t *testing.T) {
	// Must be word-bounded; digits inside a larger string should not match.
	msg := &EmailMessage{Body: "ID: abc123456def"}
	if got := ExtractVerificationCode(msg); got != "" {
		t.Errorf("expected empty for embedded digits, got %q", got)
	}
}

// ============================================================================
// ExtractVerificationLink
// ============================================================================

func TestExtractVerificationLink_NilMessage(t *testing.T) {
	if got := ExtractVerificationLink(nil); got != "" {
		t.Errorf("expected empty for nil message, got %q", got)
	}
}

func TestExtractVerificationLink_FromHTML_PrefersHTML(t *testing.T) {
	msg := &EmailMessage{
		HTML: `<a href="https://auth.openai.com/verify?code=abc123">Verify</a>`,
		Body: "https://auth.openai.com/verify?code=frombody",
	}
	if got := ExtractVerificationLink(msg); got != "https://auth.openai.com/verify?code=abc123" {
		t.Errorf("expected HTML link, got %q", got)
	}
}

func TestExtractVerificationLink_FromBody_Fallback(t *testing.T) {
	msg := &EmailMessage{
		Body: "Click here: https://auth.openai.com/verify?token=xyz",
	}
	if got := ExtractVerificationLink(msg); got != "https://auth.openai.com/verify?token=xyz" {
		t.Errorf("got %q", got)
	}
}

func TestExtractVerificationLink_NoMatch(t *testing.T) {
	msg := &EmailMessage{
		Body: "No link here.",
		HTML: "<p>Just text</p>",
	}
	if got := ExtractVerificationLink(msg); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestExtractVerificationLink_NonOpenAILink(t *testing.T) {
	msg := &EmailMessage{Body: "https://example.com/verify"}
	if got := ExtractVerificationLink(msg); got != "" {
		t.Errorf("expected empty for non-OpenAI link, got %q", got)
	}
}

func TestExtractVerificationLink_AllEmptySources(t *testing.T) {
	msg := &EmailMessage{}
	if got := ExtractVerificationLink(msg); got != "" {
		t.Errorf("expected empty for empty message, got %q", got)
	}
}

// ============================================================================
// WaitForEmailPolling
// ============================================================================

// mockEmailProvider implements EmailProvider for testing WaitForEmailPolling.
type mockEmailProvider struct {
	getMessagesFn func(ctx context.Context, inbox *InboxInfo) ([]EmailMessage, error)
}

func (m *mockEmailProvider) CreateInbox(ctx context.Context) (*InboxInfo, error) {
	return nil, errors.New("not implemented")
}
func (m *mockEmailProvider) CreateInboxWithAlias(ctx context.Context, alias, domain string) (*InboxInfo, error) {
	return nil, errors.New("not implemented")
}
func (m *mockEmailProvider) GetMessages(ctx context.Context, inbox *InboxInfo) ([]EmailMessage, error) {
	if m.getMessagesFn != nil {
		return m.getMessagesFn(ctx, inbox)
	}
	return nil, nil
}
func (m *mockEmailProvider) GetMessage(ctx context.Context, inbox *InboxInfo, messageID string) (*EmailMessage, error) {
	return nil, errors.New("not implemented")
}
func (m *mockEmailProvider) WaitForEmail(ctx context.Context, inbox *InboxInfo, timeout time.Duration, predicate func(*EmailMessage) bool) (*EmailMessage, error) {
	return nil, errors.New("not implemented")
}
func (m *mockEmailProvider) SupportsReuse() bool { return false }
func (m *mockEmailProvider) Name() string        { return "mock" }

func TestWaitForEmailPolling_ImmediateMatch(t *testing.T) {
	provider := &mockEmailProvider{
		getMessagesFn: func(ctx context.Context, inbox *InboxInfo) ([]EmailMessage, error) {
			return []EmailMessage{{From: "noreply@openai.com", Subject: "Verify your email"}}, nil
		},
	}
	inbox := &InboxInfo{Address: "test@example.com"}
	pred := func(msg *EmailMessage) bool { return msg.Subject == "Verify your email" }

	msg, err := WaitForEmailPolling(context.Background(), provider, inbox, 5*time.Second, 100*time.Millisecond, pred)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg.Subject != "Verify your email" {
		t.Errorf("unexpected subject: %q", msg.Subject)
	}
}

func TestWaitForEmailPolling_EventualMatch(t *testing.T) {
	callCount := 0
	provider := &mockEmailProvider{
		getMessagesFn: func(ctx context.Context, inbox *InboxInfo) ([]EmailMessage, error) {
			callCount++
			if callCount >= 3 {
				return []EmailMessage{{Subject: "target"}}, nil
			}
			return nil, nil
		},
	}
	inbox := &InboxInfo{Address: "test@example.com"}
	pred := func(msg *EmailMessage) bool { return msg.Subject == "target" }

	msg, err := WaitForEmailPolling(context.Background(), provider, inbox, 10*time.Second, 20*time.Millisecond, pred)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg.Subject != "target" {
		t.Errorf("unexpected subject: %q", msg.Subject)
	}
}

func TestWaitForEmailPolling_Timeout(t *testing.T) {
	provider := &mockEmailProvider{
		getMessagesFn: func(ctx context.Context, inbox *InboxInfo) ([]EmailMessage, error) {
			return nil, nil
		},
	}
	inbox := &InboxInfo{Address: "test@example.com"}
	pred := func(msg *EmailMessage) bool { return false }

	_, err := WaitForEmailPolling(context.Background(), provider, inbox, 100*time.Millisecond, 20*time.Millisecond, pred)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	t.Logf("timeout error (expected): %v", err)
}

func TestWaitForEmailPolling_ContextCanceled(t *testing.T) {
	provider := &mockEmailProvider{
		getMessagesFn: func(ctx context.Context, inbox *InboxInfo) ([]EmailMessage, error) {
			return nil, nil
		},
	}
	inbox := &InboxInfo{Address: "test@example.com"}
	pred := func(msg *EmailMessage) bool { return false }

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := WaitForEmailPolling(ctx, provider, inbox, 5*time.Second, 50*time.Millisecond, pred)
	if err == nil {
		t.Fatal("expected context canceled error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestWaitForEmailPolling_TransientErrorsRetried(t *testing.T) {
	// Retry only applies to polling ticks, not the initial fetch.
	// The initial fetch must succeed (return empty) for the test to enter the polling loop.
	callCount := 0
	provider := &mockEmailProvider{
		getMessagesFn: func(ctx context.Context, inbox *InboxInfo) ([]EmailMessage, error) {
			callCount++
			// Initial fetch (call 1): return empty, no error.
			// Poll tick 1 (call 2): transient error.
			// Poll tick 2 (call 3): transient error.
			// Poll tick 3 (call 4): success.
			if callCount == 1 {
				return nil, nil
			}
			if callCount <= 3 {
				return nil, fmt.Errorf("transient network error")
			}
			return []EmailMessage{{Subject: "finally"}}, nil
		},
	}
	inbox := &InboxInfo{Address: "test@example.com"}
	pred := func(msg *EmailMessage) bool { return msg.Subject == "finally" }

	msg, err := WaitForEmailPolling(context.Background(), provider, inbox, 5*time.Second, 20*time.Millisecond, pred)
	if err != nil {
		t.Fatalf("unexpected error after transient failures: %v", err)
	}
	if msg.Subject != "finally" {
		t.Errorf("unexpected subject: %q", msg.Subject)
	}
	if callCount < 4 {
		t.Errorf("expected at least 4 calls (1 initial + 2 transient + 1 success), got %d", callCount)
	}
}

func TestWaitForEmailPolling_InitialFetchError(t *testing.T) {
	provider := &mockEmailProvider{
		getMessagesFn: func(ctx context.Context, inbox *InboxInfo) ([]EmailMessage, error) {
			return nil, fmt.Errorf("fatal: server down")
		},
	}
	inbox := &InboxInfo{Address: "test@example.com"}
	pred := func(msg *EmailMessage) bool { return true }

	_, err := WaitForEmailPolling(context.Background(), provider, inbox, 5*time.Second, 50*time.Millisecond, pred)
	if err == nil {
		t.Fatal("expected error from initial fetch")
	}
}

func TestWaitForEmailPolling_DefaultsUsedWhenZero(t *testing.T) {
	provider := &mockEmailProvider{
		getMessagesFn: func(ctx context.Context, inbox *InboxInfo) ([]EmailMessage, error) {
			return []EmailMessage{{Subject: "ok"}}, nil
		},
	}
	inbox := &InboxInfo{Address: "test@example.com"}
	pred := func(msg *EmailMessage) bool { return true }

	// Both interval and timeout <= 0 should use defaults and still work.
	msg, err := WaitForEmailPolling(context.Background(), provider, inbox, 0, 0, pred)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg == nil || msg.Subject != "ok" {
		t.Fatal("expected message with subject 'ok'")
	}
}
