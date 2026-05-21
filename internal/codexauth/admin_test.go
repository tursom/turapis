package codexauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/tursom/turapis/internal/config"
)

type loginFlowRunnerMock struct {
	authURL string
	result  *FlowResult
	err     error
}

func (m *loginFlowRunnerMock) StartLogin(ctx context.Context) (string, func(context.Context) (*FlowResult, error), error) {
	return m.authURL, func(_ context.Context) (*FlowResult, error) { return m.result, m.err }, m.err
}

func setupTestAdmin(t *testing.T) (*CodexAdmin, *config.Store, *AccountRegistry) {
	t.Helper()
	store := setupTestRegistry(t)

	fr := testFlowResult()
	flowMock := &RegFlowRunnerMock{
		RunRegisterFunc: func(ctx context.Context) (*FlowResult, *EmailCredential, error) {
			return fr, testEmailCredential(), nil
		},
		RunReloginFunc: func(ctx context.Context, ec *EmailCredential) (*FlowResult, error) {
			return fr, nil
		},
	}
	reg := NewRegistry(store, flowMock)
	loginFlow := &loginFlowRunnerMock{
		authURL: "https://auth.openai.com/authorize?test=1",
		result:  fr,
	}

	lm := NewLifecycleManager(
		DefaultCodexAuthConfig(), reg, store, nil, &healthProberMock{
			result: &HealthProbeResult{StatusCode: 200},
		},
	)

	ca := NewCodexAdmin(reg, nil, lm, loginFlow, newBrowserClientMock())
	return ca, store, reg
}

func setupTestAdminWithAccount(t *testing.T) (*CodexAdmin, *config.Store, *AccountRegistry) {
	t.Helper()
	ca, store, reg := setupTestAdmin(t)
	if err := reg.Register(context.Background()); err != nil {
		t.Fatalf("Register: %v", err)
	}
	return ca, store, reg
}

func serveRequest(handler http.HandlerFunc, method, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

func serveRequestWithBody(handler http.HandlerFunc, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

func serveChiRequest(router http.Handler, method, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func serveChiRequestWithBody(router http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestListAccounts_Empty(t *testing.T) {
	ca, _, _ := setupTestAdmin(t)
	rec := serveRequest(ca.listAccounts, "GET", "/accounts")

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}

	var accounts []config.CodexAccount
	json.NewDecoder(rec.Body).Decode(&accounts)
	if len(accounts) != 0 {
		t.Errorf("expected empty list, got %d accounts", len(accounts))
	}
}

func TestListAccounts_WithData(t *testing.T) {
	ca, _, _ := setupTestAdminWithAccount(t)
	rec := serveRequest(ca.listAccounts, "GET", "/accounts")

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}

	var accounts []config.CodexAccount
	json.NewDecoder(rec.Body).Decode(&accounts)
	if len(accounts) != 1 {
		t.Errorf("expected 1 account, got %d", len(accounts))
	}
}

func TestGetAccount_Success(t *testing.T) {
	ca, _, _ := setupTestAdminWithAccount(t)

	router := chi.NewRouter()
	router.Get("/accounts/{id}", ca.getAccount)

	rec := serveChiRequest(router, "GET", "/accounts/1")
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestGetAccount_NotFound(t *testing.T) {
	ca, _, _ := setupTestAdmin(t)

	router := chi.NewRouter()
	router.Get("/accounts/{id}", ca.getAccount)

	rec := serveChiRequest(router, "GET", "/accounts/999")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestGetAccount_InvalidID(t *testing.T) {
	ca, _, _ := setupTestAdmin(t)

	router := chi.NewRouter()
	router.Get("/accounts/{id}", ca.getAccount)

	rec := serveChiRequest(router, "GET", "/accounts/abc")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestTriggerRegister(t *testing.T) {
	ca, _, _ := setupTestAdmin(t)
	rec := serveRequest(ca.triggerRegister, "POST", "/register")

	if rec.Code != http.StatusAccepted {
		t.Errorf("status = %d, want 202", rec.Code)
	}

	var resp struct {
		TaskID string `json:"task_id"`
		Status string `json:"status"`
	}
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp.TaskID == "" {
		t.Error("expected task_id in response")
	}
	if resp.Status != "running" {
		t.Errorf("status = %q, want running", resp.Status)
	}
}

func TestGenerateAuthURL(t *testing.T) {
	ca, _, _ := setupTestAdmin(t)
	rec := serveRequest(ca.generateAuthURL, "POST", "/login")

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}

	var resp struct {
		AuthURL string `json:"auth_url"`
	}
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp.AuthURL == "" {
		t.Error("expected auth_url in response")
	}
}

func TestTriggerRelogin_Success(t *testing.T) {
	ca, _, _ := setupTestAdminWithAccount(t)

	router := chi.NewRouter()
	router.Post("/accounts/{id}/relogin", ca.triggerRelogin)

	rec := serveChiRequest(router, "POST", "/accounts/1/relogin")
	if rec.Code != http.StatusAccepted {
		t.Errorf("status = %d, want 202", rec.Code)
	}
}

func TestTriggerRelogin_NoCredential(t *testing.T) {
	ca, _, reg := setupTestAdminWithAccount(t)

	if err := reg.RemoveEmailCredential(context.Background(), 1); err != nil {
		t.Fatalf("RemoveEmailCredential: %v", err)
	}

	router := chi.NewRouter()
	router.Post("/accounts/{id}/relogin", ca.triggerRelogin)

	rec := serveChiRequest(router, "POST", "/accounts/1/relogin")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestGetTaskStatus_Running(t *testing.T) {
	ca, _, _ := setupTestAdmin(t)
	rec := serveRequest(ca.triggerRegister, "POST", "/register")

	var resp struct {
		TaskID string `json:"task_id"`
	}
	json.NewDecoder(rec.Body).Decode(&resp)

	router := chi.NewRouter()
	router.Get("/tasks/{taskId}", ca.getTaskStatus)

	rec2 := serveChiRequest(router, "GET", "/tasks/"+resp.TaskID)
	if rec2.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec2.Code)
	}
}

func TestGetTaskStatus_NotFound(t *testing.T) {
	ca, _, _ := setupTestAdmin(t)

	router := chi.NewRouter()
	router.Get("/tasks/{taskId}", ca.getTaskStatus)

	rec := serveChiRequest(router, "GET", "/tasks/nonexistent")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestCancelTask(t *testing.T) {
	ca, _, _ := setupTestAdmin(t)
	rec := serveRequest(ca.triggerRegister, "POST", "/register")

	var resp struct {
		TaskID string `json:"task_id"`
	}
	json.NewDecoder(rec.Body).Decode(&resp)

	router := chi.NewRouter()
	router.Post("/tasks/{taskId}/cancel", ca.cancelTask)

	rec2 := serveChiRequest(router, "POST", "/tasks/"+resp.TaskID+"/cancel")
	if rec2.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec2.Code)
	}
}

func TestCancelTask_NotFound(t *testing.T) {
	ca, _, _ := setupTestAdmin(t)

	router := chi.NewRouter()
	router.Post("/tasks/{taskId}/cancel", ca.cancelTask)

	rec := serveChiRequest(router, "POST", "/tasks/nonexistent")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestManualRefresh_Success(t *testing.T) {
	ca, _, _ := setupTestAdminWithAccount(t)

	router := chi.NewRouter()
	router.Post("/accounts/{id}/refresh", ca.manualRefresh)

	rec := serveChiRequest(router, "POST", "/accounts/1/refresh")
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestManualHealthCheck_Success(t *testing.T) {
	ca, _, _ := setupTestAdminWithAccount(t)

	router := chi.NewRouter()
	router.Post("/accounts/{id}/health-check", ca.manualHealthCheck)

	rec := serveChiRequest(router, "POST", "/accounts/1/health-check")
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestAdminSetEmailCredential(t *testing.T) {
	ca, _, _ := setupTestAdminWithAccount(t)

	router := chi.NewRouter()
	router.Put("/accounts/{id}/email-credential", ca.setEmailCredential)

	body := `{"email":"new@test.com","provider":"tempmail","token":"new_token"}`
	rec := serveChiRequestWithBody(router, "PUT", "/accounts/1/email-credential", body)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestAdminDeleteEmailCredential(t *testing.T) {
	ca, _, _ := setupTestAdminWithAccount(t)

	router := chi.NewRouter()
	router.Delete("/accounts/{id}/email-credential", ca.deleteEmailCredential)

	rec := serveChiRequest(router, "DELETE", "/accounts/1/email-credential")
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestAdminDeleteAccount_Success(t *testing.T) {
	ca, _, _ := setupTestAdminWithAccount(t)

	router := chi.NewRouter()
	router.Delete("/accounts/{id}", ca.deleteAccount)

	rec := serveChiRequest(router, "DELETE", "/accounts/1")
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestAdminDeleteAccount_NotFound(t *testing.T) {
	ca, _, _ := setupTestAdmin(t)

	router := chi.NewRouter()
	router.Delete("/accounts/{id}", ca.deleteAccount)

	rec := serveChiRequest(router, "DELETE", "/accounts/999")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestGetConfig(t *testing.T) {
	ca, _, _ := setupTestAdmin(t)
	rec := serveRequest(ca.getConfig, "GET", "/config")

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}

	var cfg configResp
	json.NewDecoder(rec.Body).Decode(&cfg)
	if !cfg.AutoLoginEnabled {
		t.Error("AutoLoginEnabled should be true")
	}
}

func TestPutConfig(t *testing.T) {
	ca, _, _ := setupTestAdmin(t)
	rec := serveRequest(ca.putConfig, "PUT", "/config")

	if rec.Code != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501", rec.Code)
	}
}

func TestBrowserStatus_Disconnected(t *testing.T) {
	_, store, reg := setupTestAdmin(t)
	lm := NewLifecycleManager(
		DefaultCodexAuthConfig(), reg, store, nil, &healthProberMock{
			result: &HealthProbeResult{StatusCode: 200},
		},
	)
	ca := NewCodexAdmin(reg, nil, lm, &loginFlowRunnerMock{}, nil)
	rec := serveRequest(ca.browserStatus, "GET", "/browser/status")

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}

	var resp map[string]bool
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["connected"] {
		t.Error("expected connected=false when browserClient is nil")
	}
}
