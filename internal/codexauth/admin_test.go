package codexauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

	ca := NewCodexAdmin(reg, nil, lm, loginFlow, newBrowserClientMock(), store, nil, nil)
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

func assertStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Errorf("status = %d, want %d", rec.Code, want)
	}
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

	ca.tasks.updateProgress(resp.TaskID, "test_step")
	rec3 := serveChiRequest(router, "GET", "/tasks/"+resp.TaskID)
	var task map[string]interface{}
	json.NewDecoder(rec3.Body).Decode(&task)
	if _, ok := task["progress_log"]; !ok {
		t.Error("expected progress_log field in task response")
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
	if cfg.BrowserURL != "" {
		t.Error("expected empty browser_url in default config")
	}
	if cfg.DefaultEmailProvider != "" {
		t.Error("expected empty default_email_provider in default config")
	}
}

func TestPutConfig(t *testing.T) {
	ca, store, _ := setupTestAdmin(t)

	// Empty body → 400 Bad Request
	rec := serveRequestWithBody(ca.putConfig, "PUT", "/config", "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("empty body status = %d, want %d", rec.Code, http.StatusBadRequest)
	}

	// Valid partial update
	body := `{"auto_login_enabled": false, "max_concurrent_logins": 5}`
	rec = serveRequestWithBody(ca.putConfig, "PUT", "/config", body)
	if rec.Code != http.StatusOK {
		t.Errorf("valid body status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp configResp
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp.AutoLoginEnabled != false {
		t.Errorf("auto_login_enabled = %v, want false", resp.AutoLoginEnabled)
	}
	if resp.MaxConcurrentLogins != 5 {
		t.Errorf("max_concurrent_logins = %d, want 5", resp.MaxConcurrentLogins)
	}

	// Verify persistence
	raw, err := store.GetSetting("codex_auth_config")
	if err != nil {
		t.Fatalf("GetSetting: %v", err)
	}
	if raw == "" {
		t.Fatal("expected config to be persisted")
	}

	t.Run("invalid email provider rejected", func(t *testing.T) {
		ca, store, _ := setupTestAdmin(t)
		body := `{"default_email_provider": "gmail"}`
		resp := serveRequestWithBody(ca.putConfig, "PUT", "/config", body)
		assertStatus(t, resp, http.StatusBadRequest)
		var respBody map[string]string
		json.NewDecoder(resp.Body).Decode(&respBody)
		if !strings.Contains(respBody["error"], "default_email_provider") {
			t.Errorf("expected email_provider error, got: %v", respBody["error"])
		}
		// Verify not persisted
		raw, _ := store.GetSetting("codex_auth_config")
		if raw != "" {
			var saved map[string]interface{}
			json.Unmarshal([]byte(raw), &saved)
			if saved["DefaultEmailProvider"] == "gmail" {
				t.Error("invalid email provider should not be persisted")
			}
		}
	})

	t.Run("valid email provider accepted", func(t *testing.T) {
		ca, store, _ := setupTestAdmin(t)
		body := `{"default_email_provider": "mailondeck", "browser_url": "ws://test:3000"}`
		resp := serveRequestWithBody(ca.putConfig, "PUT", "/config", body)
		assertStatus(t, resp, http.StatusOK)
		raw, _ := store.GetSetting("codex_auth_config")
		var saved map[string]interface{}
		if err := json.Unmarshal([]byte(raw), &saved); err == nil {
			if saved["DefaultEmailProvider"] != "mailondeck" {
				t.Error("email provider not persisted")
			}
			if saved["BrowserURL"] != "ws://test:3000" {
				t.Error("browser url not persisted")
			}
		}
	})
}

func TestBrowserStatus_Disconnected(t *testing.T) {
	_, store, reg := setupTestAdmin(t)
	lm := NewLifecycleManager(
		DefaultCodexAuthConfig(), reg, store, nil, &healthProberMock{
			result: &HealthProbeResult{StatusCode: 200},
		},
	)
	ca := NewCodexAdmin(reg, nil, lm, &loginFlowRunnerMock{}, nil, store, nil, nil)
	rec := serveRequest(ca.browserStatus, "GET", "/browser/status")

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}

	var resp map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["connected"] != false {
		t.Error("expected connected=false when browserClient is nil")
	}
}

func TestTaskProgressLog(t *testing.T) {
	tracker := newTaskTracker()
	task := tracker.create("register", nil)

	tracker.updateProgress(task.ID, "step_one")
	tracker.updateProgress(task.ID, "step_two")

	got, ok := tracker.get(task.ID)
	if !ok {
		t.Fatal("task not found")
	}
	if len(got.ProgressLog) != 2 {
		t.Fatalf("expected 2 log entries, got %d", len(got.ProgressLog))
	}
	if got.ProgressLog[0].Step != "step_one" {
		t.Errorf("first step = %s, want step_one", got.ProgressLog[0].Step)
	}
	if got.ProgressLog[1].Step != "step_two" {
		t.Errorf("second step = %s, want step_two", got.ProgressLog[1].Step)
	}
	if got.ProgressLog[0].Timestamp == "" {
		t.Error("timestamp should not be empty")
	}
}

func TestTaskTracker_List(t *testing.T) {
	tracker := newTaskTracker()
	tracker.create("register", nil)
	time.Sleep(time.Second)
	b := tracker.create("relogin", nil)

	tasks := tracker.list()
	if len(tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(tasks))
	}
	if tasks[0].ID != b.ID {
		t.Error("newest task should be first")
	}
}

func TestTaskTracker_List_Empty(t *testing.T) {
	tracker := newTaskTracker()
	tasks := tracker.list()
	if len(tasks) != 0 {
		t.Errorf("expected empty list, got %d tasks", len(tasks))
	}
}

func TestListTasks_Endpoint(t *testing.T) {
	ca, _, _ := setupTestAdmin(t)
	rec := serveRequest(ca.listTasks, "GET", "/tasks")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var tasks []map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&tasks)
	if tasks == nil {
		t.Error("expected JSON array, got null")
	}
}
