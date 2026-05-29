package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tursom/turapis/internal/config"
	"github.com/tursom/turapis/internal/models"
)

func setupGatewayAuthTest(t *testing.T) (*Gateway, *config.Store) {
	t.Helper()
	store, err := config.NewStore(":memory:")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return &Gateway{store: store}, store
}

func TestAPIKeyAuthRejectsUnauthorizedRequests(t *testing.T) {
	tests := []struct {
		name       string
		authHeader string
		wantState  string
	}{
		{name: "missing", wantState: "missing"},
		{name: "malformed", authHeader: "Basic abc", wantState: "malformed"},
		{name: "empty bearer", authHeader: "Bearer ", wantState: "empty"},
		{name: "jwt passthrough disabled", authHeader: "Bearer eyJ-test", wantState: "invalid"},
		{name: "unknown local key", authHeader: "Bearer sk-unknown", wantState: "invalid"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g, _ := setupGatewayAuthTest(t)
			called := false
			next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				called = true
			})

			req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}
			rec := httptest.NewRecorder()

			g.apiKeyAuth(next).ServeHTTP(rec, req)

			if called {
				t.Fatal("next handler should not be called")
			}
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401; body = %s", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("X-Api-Key-Auth"); got != tt.wantState {
				t.Fatalf("X-Api-Key-Auth = %q, want %q", got, tt.wantState)
			}
		})
	}
}

func TestAPIKeyAuthRejectsDisabledAPIKey(t *testing.T) {
	g, store := setupGatewayAuthTest(t)
	key, err := store.CreateAPIKey("disabled")
	if err != nil {
		t.Fatalf("create api key: %v", err)
	}
	if err := store.RevokeAPIKey(key.ID); err != nil {
		t.Fatalf("revoke api key: %v", err)
	}

	called := false
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	req.Header.Set("Authorization", "Bearer "+key.Key)
	rec := httptest.NewRecorder()

	g.apiKeyAuth(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	})).ServeHTTP(rec, req)

	if called {
		t.Fatal("next handler should not be called")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Api-Key-Auth"); got != "invalid" {
		t.Fatalf("X-Api-Key-Auth = %q, want invalid", got)
	}
}

func TestAPIKeyAuthAllowsValidAPIKeyAndInjectsContext(t *testing.T) {
	g, store := setupGatewayAuthTest(t)
	key, err := store.CreateAPIKey("valid")
	if err != nil {
		t.Fatalf("create api key: %v", err)
	}
	if err := store.UpdateAPIKey(key.ID, key.Name, true, `{"allowed_models":["gpt-test"],"allowed_providers":["provider-a"]}`); err != nil {
		t.Fatalf("update api key: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	req.Header.Set("Authorization", "Bearer "+key.Key)
	req.Header.Set("User-Agent", "codex_cli_rs/0.130.0")
	rec := httptest.NewRecorder()

	g.apiKeyAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if apiKeyFromContext(r.Context()) == nil {
			t.Fatal("api key missing from context")
		}
		perms := models.KeyPermissionsFromContext(r.Context())
		if perms == nil || !perms.ModelAllowed("gpt-test") || perms.ModelAllowed("other-model") {
			t.Fatalf("permissions not injected correctly: %#v", perms)
		}
		if got := models.CodexVersionFromContext(r.Context()); got != "0.130.0" {
			t.Fatalf("codex version = %q, want 0.130.0", got)
		}
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Api-Key-Auth"); got != "valid" {
		t.Fatalf("X-Api-Key-Auth = %q, want valid", got)
	}
}
