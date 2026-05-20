package router

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/tursom/turapis/internal/config"
	"github.com/tursom/turapis/internal/models"
	providerpkg "github.com/tursom/turapis/internal/provider"
)

type testProvider struct {
	name string

	quota          map[string]interface{}
	completionResp *models.UnifiedResponse
	completionErr  error
	streamEvents   <-chan models.UnifiedStreamEvent
	streamErr      error
	rawResp        *http.Response
	rawErr         error
}

func (p *testProvider) Name() string { return p.name }

func (p *testProvider) ChatCompletion(context.Context, *models.UnifiedRequest) (*models.UnifiedResponse, error) {
	if p.completionErr != nil {
		return nil, p.completionErr
	}
	if p.completionResp != nil {
		return p.completionResp, nil
	}
	return &models.UnifiedResponse{ID: "resp_test", Model: "gpt-test", Role: "assistant", Content: "ok"}, nil
}

func (p *testProvider) ChatCompletionStream(context.Context, *models.UnifiedRequest) (<-chan models.UnifiedStreamEvent, error) {
	if p.streamErr != nil {
		return nil, p.streamErr
	}
	if p.streamEvents != nil {
		return p.streamEvents, nil
	}
	ch := make(chan models.UnifiedStreamEvent)
	close(ch)
	return ch, nil
}

func (p *testProvider) ListModels(context.Context) ([]models.ModelInfo, error) {
	return nil, nil
}

func (p *testProvider) Protocol() models.ProtocolType { return models.ProtocolOpenAI }

func (p *testProvider) SupportsTool(string) bool { return true }

func (p *testProvider) LastQuota() map[string]interface{} { return p.quota }

func (p *testProvider) RawResponsesStream(context.Context, []byte) (*http.Response, error) {
	if p.rawErr != nil {
		return nil, p.rawErr
	}
	return p.rawResp, nil
}

func setupRouterTest(t *testing.T) (*config.Store, *providerpkg.Registry, *Router) {
	t.Helper()
	store, err := config.NewStore(":memory:")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	registry := providerpkg.NewRegistry()
	return store, registry, New(store, registry)
}

func registerTestProvider(t *testing.T, store *config.Store, registry *providerpkg.Registry, p *testProvider, priority int) {
	t.Helper()
	stored := &config.Provider{
		Name:           p.name,
		BaseURL:        "https://example.test",
		APIKey:         `{"tokens":{"access_token":"test-token"}}`,
		Protocol:       "openai",
		AuthMode:       "oauth",
		Priority:       priority,
		Enabled:        true,
		SupportedTools: "[]",
	}
	if err := store.CreateProvider(stored); err != nil {
		t.Fatalf("create provider %s: %v", p.name, err)
	}
	registry.Register(p)
}

func storedQuota(t *testing.T, store *config.Store, providerName string) map[string]interface{} {
	t.Helper()
	p, err := store.GetProviderByName(providerName)
	if err != nil {
		t.Fatalf("get provider %s: %v", providerName, err)
	}
	raw := config.ParseProviderQuota(p.APIKey)
	if raw == nil {
		t.Fatalf("expected quota for provider %s", providerName)
	}
	var quota map[string]interface{}
	if err := json.Unmarshal(*raw, &quota); err != nil {
		t.Fatalf("unmarshal quota for %s: %v", providerName, err)
	}
	return quota
}

func quotaHeader(used string) http.Header {
	h := http.Header{}
	h.Set("x-codex-primary-used-percent", used)
	h.Set("x-codex-primary-reset-after-seconds", "300")
	h.Set("x-codex-primary-window-minutes", "300")
	return h
}

func primaryUsed(t *testing.T, quota map[string]interface{}) float64 {
	t.Helper()
	primary, ok := quota["primary"].(map[string]interface{})
	if !ok {
		t.Fatalf("quota missing primary entry: %#v", quota)
	}
	used, ok := primary["used_percent"].(float64)
	if !ok {
		t.Fatalf("quota missing primary used_percent: %#v", quota)
	}
	return used
}

func seedStoredQuota(t *testing.T, store *config.Store, providerName string, used float64) {
	t.Helper()
	p, err := store.GetProviderByName(providerName)
	if err != nil {
		t.Fatalf("get provider %s: %v", providerName, err)
	}
	q, err := json.Marshal(map[string]interface{}{
		"primary": map[string]interface{}{
			"used_percent":        used,
			"reset_after_seconds": 600,
			"window_minutes":      300,
		},
	})
	if err != nil {
		t.Fatalf("marshal quota: %v", err)
	}
	if err := store.SaveProviderQuota(p.ID, q); err != nil {
		t.Fatalf("save quota for %s: %v", providerName, err)
	}
}

func TestRouteNonStreamSavesQuotaOnSuccess(t *testing.T) {
	store, registry, r := setupRouterTest(t)
	p := &testProvider{
		name: "provider-success",
		quota: map[string]interface{}{
			"primary": map[string]interface{}{
				"used_percent":        12.5,
				"reset_after_seconds": 300,
				"window_minutes":      300,
			},
		},
	}
	registerTestProvider(t, store, registry, p, 10)

	result, err := r.Route(context.Background(), &models.UnifiedRequest{Model: "gpt-test"})
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if result.UsedProvider != p.name {
		t.Fatalf("used provider = %s, want %s", result.UsedProvider, p.name)
	}

	if got := primaryUsed(t, storedQuota(t, store, p.name)); got != 12.5 {
		t.Fatalf("stored used_percent = %v, want 12.5", got)
	}
}

func TestRouteNonStreamSavesQuotaAcrossFailover(t *testing.T) {
	store, registry, r := setupRouterTest(t)
	failed := &testProvider{
		name: "provider-quota-failed",
		quota: map[string]interface{}{
			"primary": map[string]interface{}{"used_percent": 99.0},
		},
		completionErr: &models.UpstreamError{StatusCode: http.StatusTooManyRequests, Body: []byte("quota exhausted")},
	}
	success := &testProvider{
		name: "provider-after-failover",
		quota: map[string]interface{}{
			"primary": map[string]interface{}{"used_percent": 8.0},
		},
	}
	registerTestProvider(t, store, registry, failed, 10)
	registerTestProvider(t, store, registry, success, 20)

	result, err := r.Route(context.Background(), &models.UnifiedRequest{Model: "gpt-test"})
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if result.UsedProvider != success.name {
		t.Fatalf("used provider = %s, want %s", result.UsedProvider, success.name)
	}
	if got := primaryUsed(t, storedQuota(t, store, failed.name)); got != 99.0 {
		t.Fatalf("failed provider used_percent = %v, want 99", got)
	}
	if got := primaryUsed(t, storedQuota(t, store, success.name)); got != 8.0 {
		t.Fatalf("success provider used_percent = %v, want 8", got)
	}
}

func TestRouteStreamSavesQuotaOnSuccess(t *testing.T) {
	store, registry, r := setupRouterTest(t)
	p := &testProvider{
		name: "stream-provider-success",
		quota: map[string]interface{}{
			"primary": map[string]interface{}{"used_percent": 23.0},
		},
	}
	registerTestProvider(t, store, registry, p, 10)

	result, err := r.RouteStream(context.Background(), &models.UnifiedRequest{Model: "gpt-test", Stream: true})
	if err != nil {
		t.Fatalf("route stream: %v", err)
	}
	if result.ProviderName != p.name {
		t.Fatalf("provider name = %s, want %s", result.ProviderName, p.name)
	}
	if got := primaryUsed(t, storedQuota(t, store, p.name)); got != 23.0 {
		t.Fatalf("stream provider used_percent = %v, want 23", got)
	}
}

func TestRouteRawStreamSavesQuotaAcrossFailover(t *testing.T) {
	store, registry, r := setupRouterTest(t)
	failed := &testProvider{
		name: "raw-provider-failed",
		rawResp: &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     quotaHeader("100"),
			Body:       io.NopCloser(strings.NewReader("quota exhausted")),
		},
	}
	success := &testProvider{
		name: "raw-provider-success",
		rawResp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     quotaHeader("7"),
			Body:       io.NopCloser(strings.NewReader("data: ok\n\n")),
		},
	}
	registerTestProvider(t, store, registry, failed, 10)
	registerTestProvider(t, store, registry, success, 20)

	result, err := r.RouteRawStream(context.Background(), "gpt-test", []byte(`{"model":"gpt-test"}`))
	if err != nil {
		t.Fatalf("route raw stream: %v", err)
	}
	defer result.Body.Close()
	if result.ProviderName != success.name {
		t.Fatalf("provider name = %s, want %s", result.ProviderName, success.name)
	}
	data, err := io.ReadAll(result.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(data) != "data: ok\n\n" {
		t.Fatalf("body = %q, want raw stream body", string(data))
	}

	if got := primaryUsed(t, storedQuota(t, store, failed.name)); got != 100.0 {
		t.Fatalf("failed raw provider used_percent = %v, want 100", got)
	}
	if got := primaryUsed(t, storedQuota(t, store, success.name)); got != 7.0 {
		t.Fatalf("success raw provider used_percent = %v, want 7", got)
	}
}

func TestRouteRawStreamKeepsStoredQuotaWhenHeadersMissing(t *testing.T) {
	store, registry, r := setupRouterTest(t)
	p := &testProvider{
		name: "raw-provider-no-quota",
		rawResp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader("data: ok\n\n")),
		},
	}
	registerTestProvider(t, store, registry, p, 10)
	seedStoredQuota(t, store, p.name, 33.0)

	result, err := r.RouteRawStream(context.Background(), "gpt-test", []byte(`{"model":"gpt-test"}`))
	if err != nil {
		t.Fatalf("route raw stream: %v", err)
	}
	defer result.Body.Close()

	if got := primaryUsed(t, storedQuota(t, store, p.name)); got != 33.0 {
		t.Fatalf("stored used_percent = %v, want existing quota 33", got)
	}
	if !strings.Contains(result.QuotaBefore, `"used_percent":33`) {
		t.Fatalf("quota before = %q, want existing quota", result.QuotaBefore)
	}
	if result.QuotaAfter != result.QuotaBefore {
		t.Fatalf("quota after = %q, want unchanged quota %q", result.QuotaAfter, result.QuotaBefore)
	}
}
