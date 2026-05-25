package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tursom/turapis/internal/config"
	"github.com/tursom/turapis/internal/models"
	"github.com/tursom/turapis/internal/provider"
	"github.com/tursom/turapis/internal/router"
)

type gatewayRawProvider struct {
	name       string
	id         int
	body       string
	header     http.Header
	statusCode int
}

func (p *gatewayRawProvider) Name() string { return p.name }
func (p *gatewayRawProvider) ID() int      { return p.id }

func (p *gatewayRawProvider) ChatCompletion(context.Context, *models.UnifiedRequest) (*models.UnifiedResponse, error) {
	return &models.UnifiedResponse{}, nil
}

func (p *gatewayRawProvider) ChatCompletionStream(context.Context, *models.UnifiedRequest) (<-chan models.UnifiedStreamEvent, error) {
	ch := make(chan models.UnifiedStreamEvent)
	close(ch)
	return ch, nil
}

func (p *gatewayRawProvider) ListModels(context.Context) ([]models.ModelInfo, error) {
	return nil, nil
}

func (p *gatewayRawProvider) Protocol() models.ProtocolType { return models.ProtocolOpenAI }

func (p *gatewayRawProvider) SetAPIKey(string) {}

func (p *gatewayRawProvider) SupportsTool(string) bool { return true }

func (p *gatewayRawProvider) RawResponsesStream(context.Context, []byte) (*http.Response, error) {
	header := p.header
	if header == nil {
		header = http.Header{}
	}
	statusCode := p.statusCode
	if statusCode == 0 {
		statusCode = http.StatusOK
	}
	return &http.Response{
		StatusCode: statusCode,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(p.body)),
	}, nil
}

func rawQuotaHeader(used string) http.Header {
	h := http.Header{}
	h.Set("x-codex-primary-used-percent", used)
	h.Set("x-codex-primary-reset-after-seconds", "300")
	h.Set("x-codex-primary-window-minutes", "300")
	return h
}

func TestRawResponsesUsageParserCapturesCompletedUsage(t *testing.T) {
	collector := &AccessLogCollector{}
	parser := rawResponsesUsageParser{collector: collector}

	parser.Write([]byte("event: response.completed\n"))
	parser.Write([]byte(`data: {"type":"response.completed","response":{"model":"gpt-5.4","usage":{"input_tokens":123,"output_tokens":45}}}`))
	parser.Write([]byte("\n\n"))
	parser.Close()

	inTok, outTok := collector.Tokens()
	if inTok != 123 || outTok != 45 {
		t.Fatalf("tokens = %d/%d, want 123/45", inTok, outTok)
	}
	if got := collector.Model(); got != "gpt-5.4" {
		t.Fatalf("model = %q, want gpt-5.4", got)
	}
}

func TestRawResponsesProxyRecordsUsageInAccessLog(t *testing.T) {
	tmp := t.TempDir()
	store, err := config.NewStore(filepath.Join(tmp, "turapis.db"), filepath.Join(tmp, "logs"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer store.Close()

	registry := provider.NewRegistry()
	rawBody := "event: response.output_text.delta\n" +
		`data: {"type":"response.output_text.delta","delta":"hello"}` + "\n\n" +
		"event: response.completed\n" +
		`data: {"type":"response.completed","response":{"model":"gpt-5.4","usage":{"input_tokens":321,"output_tokens":54}}}` + "\n\n"
	p := &gatewayRawProvider{name: "codex-raw", body: rawBody, header: rawQuotaHeader("42")}
	stored := &config.Provider{
		Name:           p.name,
		BaseURL:        "https://example.test",
		APIKey:         `{"tokens":{"access_token":"test-token","quota":{"primary":{"used_percent":11,"reset_after_seconds":600,"window_minutes":300}}}}`,
		Protocol:       "openai",
		AuthMode:       "oauth",
		Priority:       10,
		Enabled:        true,
		SupportedTools: "[]",
	}
	if err := store.CreateProvider(stored); err != nil {
		t.Fatalf("create provider: %v", err)
	}
	p.id = stored.ID
	registry.Register(p)

	g := New(router.New(store, registry), http.NewServeMux(), store, store.LogStore, "", "")
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.4","stream":true}`))
	req.Header.Set("Authorization", "Bearer eyJ-test")
	rec := httptest.NewRecorder()

	g.SetupRoutes().ServeHTTP(rec, req)
	g.accessLogWriter.Shutdown(time.Second)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != rawBody {
		t.Fatalf("raw response body changed:\ngot  %q\nwant %q", rec.Body.String(), rawBody)
	}

	logs, total, err := store.QueryAccessLogs(config.AccessLogQuery{Page: 1, PerPage: 10})
	if err != nil {
		t.Fatalf("query access logs: %v", err)
	}
	if total != 1 || len(logs) != 1 {
		t.Fatalf("logs total/len = %d/%d, want 1/1", total, len(logs))
	}
	got := logs[0]
	if got.TokensIn != 321 || got.TokensOut != 54 {
		t.Fatalf("logged tokens = %d/%d, want 321/54", got.TokensIn, got.TokensOut)
	}
	if got.ProviderName != p.name {
		t.Fatalf("provider = %q, want %q", got.ProviderName, p.name)
	}
	if got.Model != "gpt-5.4" {
		t.Fatalf("model = %q, want gpt-5.4", got.Model)
	}
	assertQuotaUsed(t, got.QuotaBefore, 11)
	assertQuotaUsed(t, got.QuotaAfter, 42)

	fetched, err := store.GetProvider(p.id)
	if err != nil {
		t.Fatalf("get provider: %v", err)
	}
	rawQuota := config.ParseProviderQuota(fetched.APIKey)
	if rawQuota == nil {
		t.Fatalf("stored quota = %v, want refreshed quota", rawQuota)
	}
	assertQuotaUsed(t, string(*rawQuota), 42)
}

func TestRawResponsesProxyRecordsSuccessfulFailoverQuotaInAccessLog(t *testing.T) {
	tmp := t.TempDir()
	store, err := config.NewStore(filepath.Join(tmp, "turapis.db"), filepath.Join(tmp, "logs"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer store.Close()

	registry := provider.NewRegistry()
	failed := &gatewayRawProvider{
		name:       "codex-failed",
		body:       "quota exhausted",
		header:     rawQuotaHeader("99"),
		statusCode: http.StatusTooManyRequests,
	}
	rawBody := "event: response.completed\n" +
		`data: {"type":"response.completed","response":{"model":"gpt-5.4","usage":{"input_tokens":10,"output_tokens":5}}}` + "\n\n"
	success := &gatewayRawProvider{name: "codex-success", body: rawBody, header: rawQuotaHeader("42")}
	createRawProvider(t, store, failed, 10, `{"tokens":{"access_token":"failed-token","quota":{"primary":{"used_percent":90,"reset_after_seconds":600,"window_minutes":300}}}}`)
	createRawProvider(t, store, success, 20, `{"tokens":{"access_token":"success-token","quota":{"primary":{"used_percent":11,"reset_after_seconds":600,"window_minutes":300}}}}`)
	registry.Register(failed)
	registry.Register(success)

	g := New(router.New(store, registry), http.NewServeMux(), store, store.LogStore, "", "")
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.4","stream":true}`))
	req.Header.Set("Authorization", "Bearer eyJ-test")
	rec := httptest.NewRecorder()

	g.SetupRoutes().ServeHTTP(rec, req)
	g.accessLogWriter.Shutdown(time.Second)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	logs, total, err := store.QueryAccessLogs(config.AccessLogQuery{Page: 1, PerPage: 10})
	if err != nil {
		t.Fatalf("query access logs: %v", err)
	}
	if total != 1 || len(logs) != 1 {
		t.Fatalf("logs total/len = %d/%d, want 1/1", total, len(logs))
	}
	got := logs[0]
	assertQuotaUsed(t, got.QuotaBefore, 11)
	assertQuotaUsed(t, got.QuotaAfter, 42)

	var attempts []config.AttemptRecord
	if err := json.Unmarshal([]byte(got.AttemptsJSON), &attempts); err != nil {
		t.Fatalf("unmarshal attempts: %v", err)
	}
	if len(attempts) != 2 {
		t.Fatalf("attempts len = %d, want 2: %#v", len(attempts), attempts)
	}
	if attempts[0].Success {
		t.Fatalf("first attempt should fail: %#v", attempts[0])
	}
	assertQuotaUsed(t, attempts[0].QuotaAfter, 99)
	if !attempts[1].Success {
		t.Fatalf("second attempt should succeed: %#v", attempts[1])
	}
	assertQuotaUsed(t, attempts[1].QuotaBefore, 11)
	assertQuotaUsed(t, attempts[1].QuotaAfter, 42)
}

func TestRawResponsesProxyKeepsFailedAttemptQuotaWhenSuccessfulFailoverHasNoQuota(t *testing.T) {
	tmp := t.TempDir()
	store, err := config.NewStore(filepath.Join(tmp, "turapis.db"), filepath.Join(tmp, "logs"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer store.Close()

	registry := provider.NewRegistry()
	failed := &gatewayRawProvider{
		name:       "codex-failed-with-quota",
		body:       "quota exhausted",
		header:     rawQuotaHeader("99"),
		statusCode: http.StatusTooManyRequests,
	}
	rawBody := "event: response.completed\n" +
		`data: {"type":"response.completed","response":{"model":"gpt-5.4","usage":{"input_tokens":10,"output_tokens":5}}}` + "\n\n"
	success := &gatewayRawProvider{name: "codex-success-no-quota", body: rawBody}
	createRawProvider(t, store, failed, 10, `{"tokens":{"access_token":"failed-token","quota":{"primary":{"used_percent":90,"reset_after_seconds":600,"window_minutes":300}}}}`)
	createRawProvider(t, store, success, 20, `{"tokens":{"access_token":"success-token"}}`)
	registry.Register(failed)
	registry.Register(success)

	g := New(router.New(store, registry), http.NewServeMux(), store, store.LogStore, "", "")
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.4","stream":true}`))
	req.Header.Set("Authorization", "Bearer eyJ-test")
	rec := httptest.NewRecorder()

	g.SetupRoutes().ServeHTTP(rec, req)
	g.accessLogWriter.Shutdown(time.Second)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	logs, total, err := store.QueryAccessLogs(config.AccessLogQuery{Page: 1, PerPage: 10})
	if err != nil {
		t.Fatalf("query access logs: %v", err)
	}
	if total != 1 || len(logs) != 1 {
		t.Fatalf("logs total/len = %d/%d, want 1/1", total, len(logs))
	}
	got := logs[0]
	assertQuotaUsed(t, got.QuotaBefore, 90)
	assertQuotaUsed(t, got.QuotaAfter, 99)

	var attempts []config.AttemptRecord
	if err := json.Unmarshal([]byte(got.AttemptsJSON), &attempts); err != nil {
		t.Fatalf("unmarshal attempts: %v", err)
	}
	if len(attempts) != 2 {
		t.Fatalf("attempts len = %d, want 2: %#v", len(attempts), attempts)
	}
	assertQuotaUsed(t, attempts[0].QuotaAfter, 99)
	if !attempts[1].Success {
		t.Fatalf("second attempt should succeed: %#v", attempts[1])
	}
	if attempts[1].QuotaBefore != "" || attempts[1].QuotaAfter != "" {
		t.Fatalf("success attempt quota should be empty: %#v", attempts[1])
	}
}

func createRawProvider(t *testing.T, store *config.Store, p *gatewayRawProvider, priority int, apiKey string) {
	t.Helper()
	stored := &config.Provider{
		Name:           p.name,
		BaseURL:        "https://example.test",
		APIKey:         apiKey,
		Protocol:       "openai",
		AuthMode:       "oauth",
		Priority:       priority,
		Enabled:        true,
		SupportedTools: "[]",
	}
	if err := store.CreateProvider(stored); err != nil {
		t.Fatalf("create provider %s: %v", p.name, err)
	}
	p.id = stored.ID
}

func assertQuotaUsed(t *testing.T, raw string, want float64) {
	t.Helper()
	var quota map[string]map[string]float64
	if err := json.Unmarshal([]byte(raw), &quota); err != nil {
		t.Fatalf("unmarshal quota %q: %v", raw, err)
	}
	got, ok := quota["primary"]["used_percent"]
	if !ok {
		t.Fatalf("quota missing primary used_percent: %#v", quota)
	}
	if got != want {
		t.Fatalf("primary used_percent = %v, want %v", got, want)
	}
}
