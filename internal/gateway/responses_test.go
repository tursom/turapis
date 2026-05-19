package gateway

import (
	"context"
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
	name string
	body string
}

func (p *gatewayRawProvider) Name() string { return p.name }

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

func (p *gatewayRawProvider) SupportsTool(string) bool { return true }

func (p *gatewayRawProvider) RawResponsesStream(context.Context, []byte) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(p.body)),
	}, nil
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
	p := &gatewayRawProvider{name: "codex-raw", body: rawBody}
	if err := store.CreateProvider(&config.Provider{
		Name:           p.name,
		BaseURL:        "https://example.test",
		APIKey:         `{"tokens":{"access_token":"test-token"}}`,
		Protocol:       "openai",
		AuthMode:       "oauth",
		Priority:       10,
		Enabled:        true,
		SupportedTools: "[]",
	}); err != nil {
		t.Fatalf("create provider: %v", err)
	}
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
}
