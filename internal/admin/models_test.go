package admin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/tursom/turapis/internal/config"
	"github.com/tursom/turapis/internal/models"
	"github.com/tursom/turapis/internal/provider"
)

type adminTestProvider struct {
	id              int
	name            string
	apiKey          string
	listModelsFn    func(context.Context) ([]models.ModelInfo, error)
	listModelsCalls int
}

func (p *adminTestProvider) Name() string { return p.name }
func (p *adminTestProvider) ID() int      { return p.id }

func (p *adminTestProvider) ChatCompletion(context.Context, *models.UnifiedRequest) (*models.UnifiedResponse, error) {
	return nil, nil
}

func (p *adminTestProvider) ChatCompletionStream(context.Context, *models.UnifiedRequest) (<-chan models.UnifiedStreamEvent, error) {
	ch := make(chan models.UnifiedStreamEvent)
	close(ch)
	return ch, nil
}

func (p *adminTestProvider) ListModels(ctx context.Context) ([]models.ModelInfo, error) {
	p.listModelsCalls++
	if p.listModelsFn != nil {
		return p.listModelsFn(ctx)
	}
	return nil, nil
}

func (p *adminTestProvider) Protocol() models.ProtocolType { return models.ProtocolOpenAI }
func (p *adminTestProvider) SupportsTool(string) bool      { return true }
func (p *adminTestProvider) SetAPIKey(key string)          { p.apiKey = key }

func setupAdminModelsTest(t *testing.T) (*Admin, *config.Store, *provider.Registry) {
	t.Helper()
	store, err := config.NewStore(":memory:")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	registry := provider.NewRegistry()
	return &Admin{store: store, registry: registry}, store, registry
}

func registerAdminTestProvider(t *testing.T, store *config.Store, registry *provider.Registry, p *adminTestProvider, authMode string, apiKey string) {
	t.Helper()
	stored := &config.Provider{
		Name:           p.name,
		BaseURL:        "https://example.test",
		APIKey:         apiKey,
		Protocol:       "openai",
		AuthMode:       authMode,
		Priority:       10,
		Enabled:        true,
		SupportedTools: "[]",
	}
	if err := store.CreateProvider(stored); err != nil {
		t.Fatalf("create provider: %v", err)
	}
	p.id = stored.ID
	if authMode == "oauth" {
		p.SetAPIKey(provider.ExtractOAuthAccessToken(apiKey))
	} else {
		p.SetAPIKey(apiKey)
	}
	registry.Register(p)
}

func requestWithRouteParam(method, target, body string, key, value string) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add(key, value)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
}

func TestDiscoverModelsRefreshesOAuthProviderOnAuthError(t *testing.T) {
	adminAPI, store, registry := setupAdminModelsTest(t)
	p := &adminTestProvider{name: "oauth-discover"}
	p.listModelsFn = func(context.Context) ([]models.ModelInfo, error) {
		if p.apiKey != "fresh-token" {
			return nil, &models.UpstreamError{StatusCode: http.StatusUnauthorized, Body: []byte("expired")}
		}
		return []models.ModelInfo{{ID: "gpt-test", Name: "gpt-test", Provider: p.name}}, nil
	}
	registerAdminTestProvider(t, store, registry, p, "oauth", `{"tokens":{"access_token":"stale-token","refresh_token":"rt"}}`)

	oldRefresh := refreshOAuthToken
	refreshCalls := 0
	refreshOAuthToken = func(store provider.TokenRefresherStore, providerID int, proxyURL string, updater provider.ProviderKeyUpdater) error {
		refreshCalls++
		return updater.SetProviderAPIKey(providerID, "fresh-token")
	}
	t.Cleanup(func() { refreshOAuthToken = oldRefresh })

	rec := httptest.NewRecorder()
	req := requestWithRouteParam(http.MethodPost, "/admin/providers/1/discover", "", "id", strconv.Itoa(p.id))
	adminAPI.discoverModels(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if refreshCalls != 1 {
		t.Fatalf("refresh calls = %d, want 1", refreshCalls)
	}
	if p.listModelsCalls != 2 {
		t.Fatalf("listModels calls = %d, want 2", p.listModelsCalls)
	}
	models, err := store.GetProviderModels(p.id)
	if err != nil {
		t.Fatalf("get provider models: %v", err)
	}
	if len(models) != 1 || models[0].ModelName != "gpt-test" {
		t.Fatalf("models = %#v, want one discovered model", models)
	}
}

func TestDiscoverModelsReturnsUnauthorizedWhenRefreshFails(t *testing.T) {
	adminAPI, store, registry := setupAdminModelsTest(t)
	p := &adminTestProvider{name: "oauth-discover-fail"}
	p.listModelsFn = func(context.Context) ([]models.ModelInfo, error) {
		return nil, &models.UpstreamError{StatusCode: http.StatusForbidden, Body: []byte("expired")}
	}
	registerAdminTestProvider(t, store, registry, p, "oauth", `{"tokens":{"access_token":"stale-token","refresh_token":"rt"}}`)

	oldRefresh := refreshOAuthToken
	refreshOAuthToken = func(provider.TokenRefresherStore, int, string, provider.ProviderKeyUpdater) error {
		return io.ErrUnexpectedEOF
	}
	t.Cleanup(func() { refreshOAuthToken = oldRefresh })

	rec := httptest.NewRecorder()
	req := requestWithRouteParam(http.MethodPost, "/admin/providers/1/discover", "", "id", strconv.Itoa(p.id))
	adminAPI.discoverModels(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if p.listModelsCalls != 1 {
		t.Fatalf("listModels calls = %d, want 1", p.listModelsCalls)
	}
	if !strings.Contains(rec.Body.String(), "refresh oauth token") {
		t.Fatalf("body = %s, want refresh error", rec.Body.String())
	}
}

func TestDiscoverAllModelsRefreshesOAuthProvidersOnAuthError(t *testing.T) {
	adminAPI, store, registry := setupAdminModelsTest(t)
	p := &adminTestProvider{name: "oauth-batch-discover"}
	p.listModelsFn = func(context.Context) ([]models.ModelInfo, error) {
		if p.apiKey != "fresh-token" {
			return nil, &models.UpstreamError{StatusCode: http.StatusUnauthorized, Body: []byte("expired")}
		}
		return []models.ModelInfo{{ID: "gpt-batch", Name: "gpt-batch", Provider: p.name}}, nil
	}
	registerAdminTestProvider(t, store, registry, p, "oauth", `{"tokens":{"access_token":"stale-token","refresh_token":"rt"}}`)

	oldRefresh := refreshOAuthToken
	refreshCalls := 0
	refreshOAuthToken = func(store provider.TokenRefresherStore, providerID int, proxyURL string, updater provider.ProviderKeyUpdater) error {
		refreshCalls++
		return updater.SetProviderAPIKey(providerID, "fresh-token")
	}
	t.Cleanup(func() { refreshOAuthToken = oldRefresh })

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/providers/batch-discover", strings.NewReader(`{"provider_ids":[`+strconv.Itoa(p.id)+`]}`))
	adminAPI.discoverAllModels(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if refreshCalls != 1 {
		t.Fatalf("refresh calls = %d, want 1", refreshCalls)
	}
	if p.listModelsCalls != 2 {
		t.Fatalf("listModels calls = %d, want 2", p.listModelsCalls)
	}

	var body struct {
		Results []struct {
			Provider string `json:"provider"`
			Count    int    `json:"count"`
			Error    string `json:"error"`
		} `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if len(body.Results) != 1 || body.Results[0].Count != 1 || body.Results[0].Error != "" {
		t.Fatalf("results = %#v, want one successful refreshed discovery", body.Results)
	}
}
