package config

import (
	"testing"
)

func setupTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewStore(":memory:")
	if err != nil {
		t.Fatalf("create test store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestCreateAPIKey(t *testing.T) {
	store := setupTestStore(t)

	key, err := store.CreateAPIKey("test-key")
	if err != nil {
		t.Fatalf("create api key: %v", err)
	}

	if key.ID == 0 {
		t.Error("expected non-zero id")
	}
	if len(key.Key) < 50 {
		t.Errorf("expected long key, got %q (%d chars)", key.Key, len(key.Key))
	}
	if key.Key[:3] != "sk-" {
		t.Errorf("expected key to start with sk-, got %q", key.Key)
	}
	if key.Name != "test-key" {
		t.Errorf("expected name test-key, got %q", key.Name)
	}
	if !key.Enabled {
		t.Error("expected key to be enabled")
	}
}

func TestListAPIKeys(t *testing.T) {
	store := setupTestStore(t)

	// 空列表
	keys, err := store.ListAPIKeys()
	if err != nil {
		t.Fatalf("list api keys: %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("expected empty list, got %d keys", len(keys))
	}
	if keys == nil {
		t.Error("expected non-nil slice")
	}

	// 创建两个 key 后列表应有 2 个
	store.CreateAPIKey("key-a")
	store.CreateAPIKey("key-b")

	keys, err = store.ListAPIKeys()
	if err != nil {
		t.Fatalf("list api keys: %v", err)
	}
	if len(keys) != 2 {
		t.Errorf("expected 2 keys, got %d", len(keys))
	}
}

func TestRevokeAPIKey(t *testing.T) {
	store := setupTestStore(t)

	key, _ := store.CreateAPIKey("revoke-me")

	// 吊销前验证通过
	validated, err := store.ValidateAPIKey(key.Key)
	if err != nil {
		t.Fatalf("validate before revoke: %v", err)
	}
	if validated.ID != key.ID {
		t.Errorf("expected id %d, got %d", key.ID, validated.ID)
	}

	// 吊销
	if err := store.RevokeAPIKey(key.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	// 吊销后验证失败
	_, err = store.ValidateAPIKey(key.Key)
	if err == nil {
		t.Error("expected error after revoke")
	}
}

func TestValidateAPIKey_Invalid(t *testing.T) {
	store := setupTestStore(t)

	_, err := store.ValidateAPIKey("sk-nonexistent")
	if err == nil {
		t.Error("expected error for invalid key")
	}
}

// --- Site Tests ---

func TestSiteCRUD(t *testing.T) {
	store := setupTestStore(t)

	// Create
	site := &Site{Name: "Test Site", BaseURL: "https://example.com", Protocol: "openai", AuthMode: "api_key", Enabled: true}
	if err := store.CreateSite(site); err != nil {
		t.Fatalf("create site: %v", err)
	}
	if site.ID == 0 {
		t.Error("expected non-zero id")
	}

	// Read
	got, err := store.GetSite(site.ID)
	if err != nil {
		t.Fatalf("get site: %v", err)
	}
	if got.Name != "Test Site" {
		t.Errorf("expected name 'Test Site', got %q", got.Name)
	}

	// Update
	site.Name = "Updated Site"
	if err := store.UpdateSite(site); err != nil {
		t.Fatalf("update site: %v", err)
	}
	got, _ = store.GetSite(site.ID)
	if got.Name != "Updated Site" {
		t.Errorf("expected name 'Updated Site', got %q", got.Name)
	}

	// Delete
	if err := store.DeleteSite(site.ID); err != nil {
		t.Fatalf("delete site: %v", err)
	}
	_, err = store.GetSite(site.ID)
	if err == nil {
		t.Error("expected error after delete")
	}
}

func TestSiteModelsCRUD(t *testing.T) {
	store := setupTestStore(t)

	// Create site first
	site := &Site{Name: "Model Site", BaseURL: "https://example.com", Protocol: "openai", AuthMode: "api_key", Enabled: true}
	store.CreateSite(site)

	// Add models
	if err := store.AddSiteModel(site.ID, "model-1", "Model One"); err != nil {
		t.Fatalf("add model: %v", err)
	}
	store.AddSiteModel(site.ID, "model-2", "Model Two")

	// List
	models, err := store.GetSiteModels(site.ID)
	if err != nil {
		t.Fatalf("get models: %v", err)
	}
	if len(models) != 2 {
		t.Errorf("expected 2 models, got %d", len(models))
	}

	// Delete model
	if err := store.DeleteSiteModel(models[0].ID); err != nil {
		t.Fatalf("delete model: %v", err)
	}
	models, _ = store.GetSiteModels(site.ID)
	if len(models) != 1 {
		t.Errorf("expected 1 model after delete, got %d", len(models))
	}

	// Cascade: delete site should delete models
	store.DeleteSite(site.ID)
	models, _ = store.GetSiteModels(site.ID)
	if len(models) != 0 {
		t.Errorf("expected 0 models after cascade delete, got %d", len(models))
	}
}

func TestCreateProviderFromSite_OK(t *testing.T) {
	store := setupTestStore(t)

	// Create a site with models
	site := &Site{Name: "Test Provider Site", BaseURL: "https://api.test.com", Protocol: "openai", AuthMode: "api_key", Enabled: true}
	store.CreateSite(site)
	store.AddSiteModel(site.ID, "gpt-4o", "gpt-4o")
	store.AddSiteModel(site.ID, "gpt-4o-mini", "gpt-4o-mini")

	provider, mappingsCreated, err := store.CreateProviderFromSite(site.ID, "", "sk-test-key-12345", nil)
	if err != nil {
		t.Fatalf("create provider from site: %v", err)
	}
	if provider.ID == 0 {
		t.Error("expected non-zero provider id")
	}
	if provider.Name != "Test Provider Site" {
		t.Errorf("expected name 'Test Provider Site', got %q", provider.Name)
	}
	if provider.AuthMode != "api_key" {
		t.Errorf("expected auth_mode 'api_key', got %q", provider.AuthMode)
	}
	if provider.APIKey != "sk-test-key-12345" {
		t.Errorf("expected api_key 'sk-test-key-12345', got %q", provider.APIKey)
	}
	if mappingsCreated != 2 {
		t.Errorf("expected 2 mappings, got %d", mappingsCreated)
	}

	// Verify model_mappings exist
	mappings, _ := store.ListModelMappings()
	count := 0
	for _, m := range mappings {
		if m.ProviderID == provider.ID {
			count++
		}
	}
	if count != 2 {
		t.Errorf("expected 2 model_mappings for new provider, got %d", count)
	}
}

func TestCreateProviderFromSite_NameConflict(t *testing.T) {
	store := setupTestStore(t)

	site := &Site{Name: "NameConflict Site", BaseURL: "https://api.test.com", Protocol: "openai", AuthMode: "api_key", Enabled: true}
	store.CreateSite(site)
	store.AddSiteModel(site.ID, "gpt-4o", "gpt-4o")

	// First creation with site name
	_, _, err := store.CreateProviderFromSite(site.ID, "", "sk-key-1", nil)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}

	// Second creation with name override
	provider2, _, err := store.CreateProviderFromSite(site.ID, "NameConflict Site (2)", "sk-key-2", nil)
	if err != nil {
		t.Fatalf("second create with override: %v", err)
	}
	if provider2.Name != "NameConflict Site (2)" {
		t.Errorf("expected overridden name, got %q", provider2.Name)
	}
}

func TestCreateProviderFromSite_ModelNameUsesModelID(t *testing.T) {
	store := setupTestStore(t)

	site := &Site{Name: "ModelID Test Site", BaseURL: "https://api.test.com", Protocol: "openai", AuthMode: "api_key", Enabled: true}
	store.CreateSite(site)
	store.AddSiteModel(site.ID, "gpt-4o-2024-05-13", "GPT-4o Latest")

	_, _, err := store.CreateProviderFromSite(site.ID, "", "sk-test-key", nil)
	if err != nil {
		t.Fatalf("create provider from site: %v", err)
	}

	// The model_mapping must use the model_id ("gpt-4o-2024-05-13"), not the display name ("GPT-4o Latest")
	chain, err := store.GetPriorityChain("gpt-4o-2024-05-13")
	if err != nil {
		t.Fatalf("get priority chain: %v", err)
	}
	if len(chain) == 0 {
		t.Error("expected to find mapping by model_id 'gpt-4o-2024-05-13', but got none")
	}

	// Confirm the display name does NOT accidentally match
	chain2, _ := store.GetPriorityChain("GPT-4o Latest")
	if len(chain2) != 0 {
		t.Error("did not expect mapping by display name 'GPT-4o Latest'")
	}
}

func TestCreateProviderFromSite_TransactionRollback(t *testing.T) {
	store := setupTestStore(t)

	site := &Site{Name: "Rollback Site", BaseURL: "https://api.test.com", Protocol: "openai", AuthMode: "api_key", Enabled: true}
	store.CreateSite(site)
	store.AddSiteModel(site.ID, "rollback-test-model", "rollback-test-model")

	// First creation succeeds
	_, _, err := store.CreateProviderFromSite(site.ID, "rollback-provider", "sk-ok", nil)
	if err != nil {
		t.Fatalf("first create should succeed: %v", err)
	}

	// Second creation with same name triggers UNIQUE(providers.name) → rollback
	_, _, err = store.CreateProviderFromSite(site.ID, "rollback-provider", "sk-key-2", nil)
	if err == nil {
		t.Error("expected error on duplicate name, got nil")
	}

	// Verify no second provider was created with that name (the first one still exists)
	providers, _ := store.ListProviders()
	count := 0
	for _, p := range providers {
		if p.Name == "rollback-provider" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 provider named 'rollback-provider', got %d", count)
	}
}

// --- CodexAccount CRUD tests ---

func TestCodexAccountCRUD(t *testing.T) {
	store := setupTestStore(t)

	a := &CodexAccount{
		Email:     "test@example.com",
		AccountID: "acc_test_crud",
		UserID:    "user_test",
		PlanType:  "free",
		Status:    "active",
		Metadata:  `{"email_credential":{"email":"test@example.com","provider":"mock","token":"tok_xxx"}}`,
	}

	if err := store.CreateCodexAccount(a); err != nil {
		t.Fatalf("create codex account: %v", err)
	}
	if a.ID == 0 {
		t.Error("expected non-zero id after create")
	}
	if a.CreatedAt == "" {
		t.Error("expected CreatedAt to be set")
	}

	got, err := store.GetCodexAccount(a.ID)
	if err != nil {
		t.Fatalf("get codex account: %v", err)
	}
	if got.Email != "test@example.com" {
		t.Errorf("Email = %s, want test@example.com", got.Email)
	}
	if got.AccountID != "acc_test_crud" {
		t.Errorf("AccountID = %s, want acc_test_crud", got.AccountID)
	}
	if got.Status != "active" {
		t.Errorf("Status = %s, want active", got.Status)
	}

	got2, err := store.GetCodexAccountByAccountID("acc_test_crud")
	if err != nil {
		t.Fatalf("get codex account by account_id: %v", err)
	}
	if got2.ID != a.ID {
		t.Errorf("ID = %d, want %d", got2.ID, a.ID)
	}

	_, err = store.FindCodexAccountByProviderID(-1)
	if err == nil {
		t.Error("expected error for non-matching provider_id")
	}

	a.PlanType = "plus"
	a.Status = "expired"
	a.ErrorMsg = "token expired"
	a.LastRefresh = "2025-06-01T00:00:00Z"
	if err := store.UpdateCodexAccount(a); err != nil {
		t.Fatalf("update codex account: %v", err)
	}
	got3, _ := store.GetCodexAccount(a.ID)
	if got3.PlanType != "plus" {
		t.Errorf("PlanType = %s, want plus", got3.PlanType)
	}
	if got3.Status != "expired" {
		t.Errorf("Status = %s, want expired", got3.Status)
	}

	if err := store.UpdateCodexAccountStatus(a.ID, "needs_login", "relogin required"); err != nil {
		t.Fatalf("update status: %v", err)
	}
	got4, _ := store.GetCodexAccount(a.ID)
	if got4.Status != "needs_login" {
		t.Errorf("Status = %s, want needs_login", got4.Status)
	}
	if got4.ErrorMsg != "relogin required" {
		t.Errorf("ErrorMsg = %s, want relogin required", got4.ErrorMsg)
	}

	if err := store.UpdateCodexAccountRefresh(a.ID); err != nil {
		t.Fatalf("update refresh: %v", err)
	}
	got5, _ := store.GetCodexAccount(a.ID)
	if got5.LastRefresh == "" {
		t.Error("expected LastRefresh to be set")
	}

	if err := store.UpdateCodexAccountHealth(a.ID); err != nil {
		t.Fatalf("update health: %v", err)
	}
	got6, _ := store.GetCodexAccount(a.ID)
	if got6.LastHealth == "" {
		t.Error("expected LastHealth to be set")
	}

	if err := store.DeleteCodexAccount(a.ID); err != nil {
		t.Fatalf("delete codex account: %v", err)
	}
	_, err = store.GetCodexAccount(a.ID)
	if err == nil {
		t.Error("expected error after delete")
	}
}

func TestListCodexAccounts_Empty(t *testing.T) {
	store := setupTestStore(t)

	accounts, err := store.ListCodexAccounts()
	if err != nil {
		t.Fatalf("list codex accounts: %v", err)
	}
	if len(accounts) != 0 {
		t.Errorf("expected empty list, got %d accounts", len(accounts))
	}
	if accounts == nil {
		t.Error("expected non-nil slice")
	}

	if err := store.CreateCodexAccount(&CodexAccount{Email: "a@test.com", AccountID: "acc_a", UserID: "user_a", Status: "active"}); err != nil {
		t.Fatalf("create a: %v", err)
	}
	if err := store.CreateCodexAccount(&CodexAccount{Email: "b@test.com", AccountID: "acc_b", UserID: "user_b", Status: "expired"}); err != nil {
		t.Fatalf("create b: %v", err)
	}

	accounts, err = store.ListCodexAccounts()
	if err != nil {
		t.Fatalf("list after create: %v", err)
	}
	if len(accounts) != 2 {
		t.Errorf("expected 2 accounts, got %d", len(accounts))
	}

	active, err := store.ListActiveCodexAccounts()
	if err != nil {
		t.Fatalf("list active: %v", err)
	}
	if len(active) != 1 {
		t.Errorf("expected 1 active account, got %d", len(active))
	}
	if active[0].Email != "a@test.com" {
		t.Errorf("active Email = %s, want a@test.com", active[0].Email)
	}
}

func TestCreateCodexAccount_DuplicateAccountID(t *testing.T) {
	store := setupTestStore(t)

	a := &CodexAccount{Email: "dup@test.com", AccountID: "acc_dup", UserID: "user_dup", Status: "active"}
	if err := store.CreateCodexAccount(a); err != nil {
		t.Fatalf("first create: %v", err)
	}

	b := &CodexAccount{Email: "other@test.com", AccountID: "acc_dup", UserID: "user_other", Status: "active"}
	if err := store.CreateCodexAccount(b); err == nil {
		t.Error("expected UNIQUE constraint error on duplicate account_id")
	}
}

func TestCodexAccount_ProviderCascade(t *testing.T) {
	store := setupTestStore(t)

	p := &Provider{Name: "cascade-test", BaseURL: "https://api.test.com", APIKey: "sk-test", Protocol: "openai", AuthMode: "api_key", Priority: 100, Enabled: true}
	if err := store.CreateProvider(p); err != nil {
		t.Fatalf("create provider: %v", err)
	}

	a := &CodexAccount{Email: "cascade@test.com", AccountID: "acc_cascade", UserID: "user_cas", Status: "active"}
	a.ProviderID = &p.ID
	if err := store.CreateCodexAccount(a); err != nil {
		t.Fatalf("create codex account: %v", err)
	}

	got, err := store.FindCodexAccountByProviderID(p.ID)
	if err != nil {
		t.Fatalf("find by provider_id: %v", err)
	}
	if got.ID != a.ID {
		t.Errorf("expected account ID %d, got %d", a.ID, got.ID)
	}

	if err := store.DeleteProvider(p.ID); err != nil {
		t.Fatalf("delete provider: %v", err)
	}

	got2, err := store.GetCodexAccount(a.ID)
	if err != nil {
		t.Fatalf("get codex account after provider delete: %v", err)
	}
	if got2.ProviderID != nil {
		t.Errorf("expected ProviderID to be nil after cascade, got %v", *got2.ProviderID)
	}
}

func TestGetCodexAccount_NotFound(t *testing.T) {
	store := setupTestStore(t)

	_, err := store.GetCodexAccount(-1)
	if err == nil {
		t.Error("expected error for non-existent id")
	}

	_, err = store.GetCodexAccountByAccountID("nonexistent")
	if err == nil {
		t.Error("expected error for non-existent account_id")
	}
}
