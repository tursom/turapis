package codexauth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tursom/turapis/internal/config"
)

type tokenRefresherMock struct {
	err error
}

func (m *tokenRefresherMock) Refresh(store RegistryStore, providerID int, proxyURL string) error {
	return m.err
}

type healthProberMock struct {
	result *HealthProbeResult
	err    error
}

func (m *healthProberMock) Probe(ctx context.Context, accessToken string) (*HealthProbeResult, error) {
	return m.result, m.err
}

func TestDefaultCodexAuthConfig(t *testing.T) {
	cfg := DefaultCodexAuthConfig()

	if !cfg.AutoLoginEnabled {
		t.Error("AutoLoginEnabled should be true")
	}
	if !cfg.AutoRefreshEnabled {
		t.Error("AutoRefreshEnabled should be true")
	}
	if !cfg.AutoHealthEnabled {
		t.Error("AutoHealthEnabled should be true")
	}
	if cfg.AutoLoginInterval != 1*time.Hour {
		t.Errorf("AutoLoginInterval = %v, want 1h", cfg.AutoLoginInterval)
	}
	if cfg.RefreshInterval != 7*24*time.Hour {
		t.Errorf("RefreshInterval = %v, want 7d", cfg.RefreshInterval)
	}
	if cfg.HealthCheckInterval != 24*time.Hour {
		t.Errorf("HealthCheckInterval = %v, want 24h", cfg.HealthCheckInterval)
	}
	if cfg.MaxConcurrentLogins != 1 {
		t.Errorf("MaxConcurrentLogins = %d, want 1", cfg.MaxConcurrentLogins)
	}
}

func TestNewLifecycleManager_Defaults(t *testing.T) {
	store := setupTestRegistry(t)
	reg := NewRegistry(store, &mockRegFlowRunner{})
	prober := &healthProberMock{}

	lm := NewLifecycleManager(CodexAuthConfig{}, reg, store, nil, prober)

	if lm.cfg.AutoLoginInterval != 1*time.Hour {
		t.Errorf("AutoLoginInterval = %v, want 1h", lm.cfg.AutoLoginInterval)
	}
	if lm.cfg.RefreshInterval != 7*24*time.Hour {
		t.Errorf("RefreshInterval = %v, want 7d", lm.cfg.RefreshInterval)
	}
	if lm.cfg.HealthCheckInterval != 24*time.Hour {
		t.Errorf("HealthCheckInterval = %v, want 24h", lm.cfg.HealthCheckInterval)
	}
	if lm.cfg.MaxConcurrentLogins != 1 {
		t.Errorf("MaxConcurrentLogins = %d, want 1", lm.cfg.MaxConcurrentLogins)
	}
	if lm.reg != reg {
		t.Error("reg not set")
	}
	if lm.store != store {
		t.Error("store not set")
	}
}

func TestNewLifecycleManager_CustomValues(t *testing.T) {
	store := setupTestRegistry(t)
	reg := NewRegistry(store, &mockRegFlowRunner{})
	prober := &healthProberMock{}

	cfg := CodexAuthConfig{
		AutoLoginInterval:   30 * time.Minute,
		RefreshInterval:     3 * 24 * time.Hour,
		HealthCheckInterval: 12 * time.Hour,
		MaxConcurrentLogins: 3,
		ProxyURL:            "http://proxy:8080",
	}

	lm := NewLifecycleManager(cfg, reg, store, nil, prober)

	if lm.cfg.AutoLoginInterval != 30*time.Minute {
		t.Errorf("AutoLoginInterval = %v", lm.cfg.AutoLoginInterval)
	}
	if lm.cfg.MaxConcurrentLogins != 3 {
		t.Errorf("MaxConcurrentLogins = %d", lm.cfg.MaxConcurrentLogins)
	}
	if lm.cfg.ProxyURL != "http://proxy:8080" {
		t.Errorf("ProxyURL = %s", lm.cfg.ProxyURL)
	}
}

func TestLifecycleStartShutdown(t *testing.T) {
	store := setupTestRegistry(t)
	flowMock := &RegFlowRunnerMock{
		RunRegisterFunc: func(ctx context.Context) (*FlowResult, *EmailCredential, error) {
			return testFlowResult(), testEmailCredential(), nil
		},
	}
	reg := NewRegistry(store, flowMock)
	prober := &healthProberMock{}

	cfg := CodexAuthConfig{
		AutoLoginEnabled:   true,
		AutoRefreshEnabled: true,
		AutoHealthEnabled:  true,
		AutoLoginInterval:  10 * time.Millisecond,
		RefreshInterval:    10 * time.Minute,
		HealthCheckInterval: 10 * time.Minute,
		MaxConcurrentLogins: 1,
	}

	lm := NewLifecycleManager(cfg, reg, store, nil, prober)
	lm.Start(context.Background())

	time.Sleep(50 * time.Millisecond)
	lm.Shutdown()

	calls := flowMock.RunRegisterCalls()
	if len(calls) == 0 {
		t.Error("auto register routine did not fire")
	}
}

func TestAutoRegisterFires(t *testing.T) {
	store := setupTestRegistry(t)
	flowMock := &RegFlowRunnerMock{
		RunRegisterFunc: func(ctx context.Context) (*FlowResult, *EmailCredential, error) {
			return testFlowResult(), testEmailCredential(), nil
		},
	}
	reg := NewRegistry(store, flowMock)

	cfg := CodexAuthConfig{
		AutoLoginEnabled:   true,
		AutoRefreshEnabled:  false,
		AutoHealthEnabled:   false,
		AutoLoginInterval:   10 * time.Millisecond,
		MaxConcurrentLogins: 1,
	}

	lm := NewLifecycleManager(cfg, reg, store, nil, &healthProberMock{})
	lm.Start(context.Background())

	time.Sleep(60 * time.Millisecond)
	lm.Shutdown()

	calls := flowMock.RunRegisterCalls()
	if len(calls) < 1 {
		t.Errorf("expected at least 1 RunRegister call, got %d", len(calls))
	}
}

func TestAutoRegisterError(t *testing.T) {
	store := setupTestRegistry(t)
	flowMock := &RegFlowRunnerMock{
		RunRegisterFunc: func(ctx context.Context) (*FlowResult, *EmailCredential, error) {
			return nil, nil, errors.New("register failed")
		},
	}
	reg := NewRegistry(store, flowMock)

	lm := NewLifecycleManager(
		CodexAuthConfig{
			AutoLoginEnabled:   true,
			AutoRefreshEnabled:  false,
			AutoHealthEnabled:   false,
			AutoLoginInterval:   10 * time.Millisecond,
			MaxConcurrentLogins: 1,
		},
		reg, store, nil, &healthProberMock{},
	)

	lm.Start(context.Background())
	time.Sleep(60 * time.Millisecond)
	lm.Shutdown()

	if len(flowMock.RunRegisterCalls()) < 1 {
		t.Error("register routine did not fire")
	}
}

func TestRefreshTokenSuccess(t *testing.T) {
	store := setupTestRegistry(t)
	fr := testFlowResult()
	ec := testEmailCredential()

	flowMock := &RegFlowRunnerMock{
		RunRegisterFunc: func(ctx context.Context) (*FlowResult, *EmailCredential, error) {
			return fr, ec, nil
		},
	}
	reg := NewRegistry(store, flowMock)

	if err := reg.Register(context.Background()); err != nil {
		t.Fatalf("Register: %v", err)
	}

	accounts, _ := reg.List(context.Background())
	account := accounts[0]

	if err := reg.UpdateStatus(context.Background(), account.ID, "needs_login", "test"); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}

	refresher := &tokenRefresherMock{}
	lm := NewLifecycleManager(
		DefaultCodexAuthConfig(), reg, store, refresher, &healthProberMock{},
	)

	lm.refreshOneAccount(account, context.Background())

	updated, err := reg.GetByID(context.Background(), account.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}

	if updated.Status != "active" {
		t.Errorf("status = %q, want active", updated.Status)
	}
	if updated.LastRefresh == "" {
		t.Error("LastRefresh not updated")
	}
}

func TestRefreshTokenFailureFallbackRelogin(t *testing.T) {
	store := setupTestRegistry(t)
	fr := testFlowResult()
	ec := testEmailCredential()

	flowMock := &RegFlowRunnerMock{
		RunRegisterFunc: func(ctx context.Context) (*FlowResult, *EmailCredential, error) {
			return fr, ec, nil
		},
		RunReloginFunc: func(ctx context.Context, cred *EmailCredential) (*FlowResult, error) {
			return fr, nil
		},
	}
	reg := NewRegistry(store, flowMock)

	if err := reg.Register(context.Background()); err != nil {
		t.Fatalf("Register: %v", err)
	}

	accounts, _ := reg.List(context.Background())
	account := accounts[0]

	refresher := &tokenRefresherMock{err: errors.New("refresh failed")}
	lm := NewLifecycleManager(
		DefaultCodexAuthConfig(), reg, store, refresher, &healthProberMock{},
	)

	lm.refreshOneAccount(account, context.Background())

	updated, err := reg.GetByID(context.Background(), account.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}

	if updated.Status != "active" {
		t.Errorf("status = %q, want active (fallback relogin should succeed)", updated.Status)
	}
	if updated.LastRefresh == "" {
		t.Error("LastRefresh not updated after fallback")
	}

	reloginCalls := flowMock.RunReloginCalls()
	if len(reloginCalls) != 1 {
		t.Errorf("expected 1 RunRelogin call, got %d", len(reloginCalls))
	}
}

func TestRefreshTokenFailureNoCredential(t *testing.T) {
	store := setupTestRegistry(t)
	fr := testFlowResult()

	flowMock := &RegFlowRunnerMock{
		RunRegisterFunc: func(ctx context.Context) (*FlowResult, *EmailCredential, error) {
			return fr, testEmailCredential(), nil
		},
	}
	reg := NewRegistry(store, flowMock)

	if err := reg.Register(context.Background()); err != nil {
		t.Fatalf("Register: %v", err)
	}

	accounts, _ := reg.List(context.Background())
	account := accounts[0]

	{
		dbAccount, err := store.GetCodexAccount(account.ID)
		if err != nil {
			t.Fatalf("GetCodexAccount: %v", err)
		}
		dbAccount.Metadata = "{}"
		if err := store.UpdateCodexAccount(dbAccount); err != nil {
			t.Fatalf("UpdateCodexAccount: %v", err)
		}
	}

	refresher := &tokenRefresherMock{err: errors.New("refresh failed")}
	lm := NewLifecycleManager(
		DefaultCodexAuthConfig(), reg, store, refresher, &healthProberMock{},
	)

	lm.refreshOneAccount(account, context.Background())

	updated, err := reg.GetByID(context.Background(), account.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}

	if updated.Status == "active" {
		t.Errorf("status = %q, want needs_login (no credential for fallback)", updated.Status)
	}
}

func TestHealthCheckProbeSuccess(t *testing.T) {
	store := setupTestRegistry(t)
	fr := testFlowResult()
	ec := testEmailCredential()

	flowMock := &RegFlowRunnerMock{
		RunRegisterFunc: func(ctx context.Context) (*FlowResult, *EmailCredential, error) {
			return fr, ec, nil
		},
	}
	reg := NewRegistry(store, flowMock)

	if err := reg.Register(context.Background()); err != nil {
		t.Fatalf("Register: %v", err)
	}

	accounts, _ := reg.List(context.Background())
	account := accounts[0]

	prober := &healthProberMock{
		result: &HealthProbeResult{StatusCode: 200},
	}
	lm := NewLifecycleManager(
		DefaultCodexAuthConfig(), reg, store, nil, prober,
	)

	lm.healthCheckOneAccount(account, context.Background())

	updated, err := reg.GetByID(context.Background(), account.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}

	if updated.LastHealth == "" {
		t.Error("LastHealth not updated")
	}
	if updated.Status != "active" {
		t.Errorf("status = %q, want active", updated.Status)
	}
}

func TestHealthCheckProbe401TriggersRelogin(t *testing.T) {
	store := setupTestRegistry(t)
	fr := testFlowResult()
	ec := testEmailCredential()

	flowMock := &RegFlowRunnerMock{
		RunRegisterFunc: func(ctx context.Context) (*FlowResult, *EmailCredential, error) {
			return fr, ec, nil
		},
		RunReloginFunc: func(ctx context.Context, cred *EmailCredential) (*FlowResult, error) {
			return fr, nil
		},
	}
	reg := NewRegistry(store, flowMock)

	if err := reg.Register(context.Background()); err != nil {
		t.Fatalf("Register: %v", err)
	}

	accounts, _ := reg.List(context.Background())
	account := accounts[0]

	if err := reg.UpdateStatus(context.Background(), account.ID, "needs_login", "test"); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}

	prober := &healthProberMock{
		result: &HealthProbeResult{StatusCode: 401},
	}
	lm := NewLifecycleManager(
		DefaultCodexAuthConfig(), reg, store, nil, prober,
	)

	lm.healthCheckOneAccount(account, context.Background())

	updated, err := reg.GetByID(context.Background(), account.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}

	if updated.LastHealth == "" {
		t.Error("LastHealth not updated")
	}

	reloginCalls := flowMock.RunReloginCalls()
	if len(reloginCalls) != 1 {
		t.Errorf("expected 1 RunRelogin call for 401 fallback, got %d", len(reloginCalls))
	}
}

func TestHealthCheckNoProviderID(t *testing.T) {
	store := setupTestRegistry(t)
	reg := NewRegistry(store, &mockRegFlowRunner{})

	lm := NewLifecycleManager(
		DefaultCodexAuthConfig(), reg, store, nil, &healthProberMock{},
	)

	lm.healthCheckOneAccount(config.CodexAccount{ID: 999, ProviderID: nil}, context.Background())
}

func TestExtractAccessTokenFromCredentialJSON(t *testing.T) {
	tests := []struct {
		name string
		json string
		want string
	}{
		{"valid", `{"tokens":{"access_token":"abc123","refresh_token":"rt"}}`, "abc123"},
		{"empty tokens", `{}`, ""},
		{"no access_token", `{"tokens":{"refresh_token":"rt"}}`, ""},
		{"invalid json", `not json`, ""},
		{"null json", "null", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractAccessTokenFromCredentialJSON(tt.json)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCycleOnEmptyList(t *testing.T) {
	store := setupTestRegistry(t)
	reg := NewRegistry(store, &mockRegFlowRunner{})

	lm := NewLifecycleManager(
		DefaultCodexAuthConfig(), reg, store, nil, &healthProberMock{},
	)

	lm.runRefreshCycle()
	lm.runHealthCheckCycle()
}
