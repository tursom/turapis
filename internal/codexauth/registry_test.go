package codexauth

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/tursom/turapis/internal/config"
)

type mockRegFlowRunner struct {
	registerResult *FlowResult
	registerCred   *EmailCredential
	registerErr    error
	reloginResult  *FlowResult
	reloginErr     error
}

func (m *mockRegFlowRunner) RunRegister(ctx context.Context) (*FlowResult, *EmailCredential, error) {
	return m.registerResult, m.registerCred, m.registerErr
}

func (m *mockRegFlowRunner) RunRelogin(ctx context.Context, ec *EmailCredential) (*FlowResult, error) {
	return m.reloginResult, m.reloginErr
}

func setupTestRegistry(t *testing.T) *config.Store {
	t.Helper()
	store, err := config.NewStore(":memory:")
	if err != nil {
		t.Fatalf("create test store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	if err := store.SeedBuiltinSites(); err != nil {
		t.Fatalf("seed builtin sites: %v", err)
	}

	return store
}

func testFlowResult() *FlowResult {
	return &FlowResult{
		Tokens: &TokenSet{
			AccessToken:  makeAccessJWT(`{"sub":"s","client_id":"app_EMoamEEZ73f0CkXaXp7hrann","iat":1}`),
			RefreshToken: "rt_test",
			IDToken:      "idt_test",
			AccountID:    "acc_test_001",
			ExpiresAt:    1716249600000,
		},
		Identity: &AccountIdentity{
			Email:     "test@moonlol.com",
			AccountID: "acc_test_001",
			UserID:    "user_test_001",
			PlanType:  "free",
		},
	}
}

func testEmailCredential() *EmailCredential {
	return &EmailCredential{
		Email:    "test@moonlol.com",
		Provider: "tempmail",
		Token:    "tm_xxx",
	}
}

func TestNewRegistry(t *testing.T) {
	store := setupTestRegistry(t)
	reg := NewRegistry(store, &mockRegFlowRunner{})
	if reg == nil {
		t.Error("NewRegistry returned nil")
	}
}

func TestList_Empty(t *testing.T) {
	store := setupTestRegistry(t)
	reg := NewRegistry(store, &mockRegFlowRunner{})

	accounts, err := reg.List(context.Background())
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if accounts == nil {
		t.Error("List() returned nil slice, want empty slice")
	}
	if len(accounts) != 0 {
		t.Errorf("List() len = %d, want 0", len(accounts))
	}
}

func TestRegister_Success(t *testing.T) {
	store := setupTestRegistry(t)
	fr := testFlowResult()
	ec := testEmailCredential()
	flow := &mockRegFlowRunner{
		registerResult: fr,
		registerCred:   ec,
	}
	reg := NewRegistry(store, flow)

	ctx := context.Background()
	err := reg.Register(ctx)
	if err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	account, err := reg.GetByAccountID(ctx, fr.Identity.AccountID)
	if err != nil {
		t.Fatalf("GetByAccountID(%q) error: %v", fr.Identity.AccountID, err)
	}

	if account.ID == 0 {
		t.Error("account.ID should not be 0")
	}
	if account.Email != fr.Identity.Email {
		t.Errorf("account.Email = %q, want %q", account.Email, fr.Identity.Email)
	}
	if account.AccountID != fr.Identity.AccountID {
		t.Errorf("account.AccountID = %q, want %q", account.AccountID, fr.Identity.AccountID)
	}
	if account.UserID != fr.Identity.UserID {
		t.Errorf("account.UserID = %q, want %q", account.UserID, fr.Identity.UserID)
	}
	if account.PlanType != fr.Identity.PlanType {
		t.Errorf("account.PlanType = %q, want %q", account.PlanType, fr.Identity.PlanType)
	}
	if account.Status != "active" {
		t.Errorf("account.Status = %q, want %q", account.Status, "active")
	}
	if account.ProviderID == nil {
		t.Error("account.ProviderID should not be nil")
	}

	accounts, err := reg.List(context.Background())
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(accounts) != 1 {
		t.Errorf("List() len = %d, want 1", len(accounts))
	}

	found, err := reg.GetByAccountID(ctx, fr.Identity.AccountID)
	if err != nil {
		t.Fatalf("GetByAccountID(%q) error: %v", fr.Identity.AccountID, err)
	}
	if found.ID != account.ID {
		t.Errorf("GetByAccountID returned different account: ID = %d, want %d", found.ID, account.ID)
	}
	if found.Email != fr.Identity.Email {
		t.Errorf("GetByAccountID Email = %q, want %q", found.Email, fr.Identity.Email)
	}
}

func TestRegister_DuplicateAccountID(t *testing.T) {
	store := setupTestRegistry(t)
	fr := testFlowResult()
	ec := testEmailCredential()
	flow := &mockRegFlowRunner{
		registerResult: fr,
		registerCred:   ec,
	}
	reg := NewRegistry(store, flow)

	ctx := context.Background()
	err := reg.Register(ctx)
	if err != nil {
		t.Fatalf("first Register() error: %v", err)
	}

	err = reg.Register(ctx)
	if err == nil {
		t.Error("expected error for duplicate account_id, got nil")
	}
	if !strings.Contains(err.Error(), "already exists") && !strings.Contains(err.Error(), "UNIQUE") {
		t.Errorf("expected error to mention duplicate, got: %v", err)
	}
}

func TestRegister_FlowError(t *testing.T) {
	store := setupTestRegistry(t)
	flow := &mockRegFlowRunner{
		registerErr: fmt.Errorf("flow registration failed: inbox creation error"),
	}
	reg := NewRegistry(store, flow)

	err := reg.Register(context.Background())
	if err == nil {
		t.Error("expected error from flow.RunRegister, got nil")
	}
	if !strings.Contains(err.Error(), "flow registration failed") {
		t.Errorf("expected error to contain flow message, got: %v", err)
	}
}

func TestEmailCodeLogin_Success(t *testing.T) {
	store := setupTestRegistry(t)

	fr := testFlowResult()
	ec := testEmailCredential()
	regFlow := &mockRegFlowRunner{
		registerResult: fr,
		registerCred:   ec,
	}
	reg := NewRegistry(store, regFlow)

	ctx := context.Background()
	err := reg.Register(ctx)
	if err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	account, err := reg.GetByAccountID(ctx, fr.Identity.AccountID)
	if err != nil {
		t.Fatalf("GetByAccountID(%q) error: %v", fr.Identity.AccountID, err)
	}

	reloginFR := &FlowResult{
		Tokens: &TokenSet{
			AccessToken:  makeAccessJWT(`{"sub":"s2","client_id":"app_EMoamEEZ73f0CkXaXp7hrann","iat":2}`),
			RefreshToken: "rt_relogin",
			IDToken:      "idt_test",
			AccountID:    account.AccountID,
			ExpiresAt:    1716249600000,
		},
		Identity: &AccountIdentity{
			Email:     account.Email,
			AccountID: account.AccountID,
			UserID:    account.UserID,
			PlanType:  account.PlanType,
		},
	}
	regFlow.reloginResult = reloginFR

	err = reg.EmailCodeLogin(ctx, ec)
	if err != nil {
		t.Fatalf("EmailCodeLogin() error: %v", err)
	}

	updated, err := reg.GetByID(ctx, account.ID)
	if err != nil {
		t.Fatalf("GetByID(%d) error: %v", account.ID, err)
	}
	if updated.Status != "active" {
		t.Errorf("account.Status = %q, want %q", updated.Status, "active")
	}
}

func TestEmailCodeLogin_AccountNotFound(t *testing.T) {
	store := setupTestRegistry(t)
	flow := &mockRegFlowRunner{
		reloginResult: &FlowResult{
			Tokens: &TokenSet{
				AccessToken:  makeAccessJWT(`{"sub":"s","client_id":"app_EMoamEEZ73f0CkXaXp7hrann","iat":1}`),
				RefreshToken: "rt_test",
				IDToken:      "idt_test",
				AccountID:    "nonexistent_acc_999",
				ExpiresAt:    1716249600000,
			},
			Identity: &AccountIdentity{
				Email:     "no@test.com",
				AccountID: "nonexistent_acc_999",
				UserID:    "user_nonexistent",
				PlanType:  "free",
			},
		},
	}
	reg := NewRegistry(store, flow)

	ec := testEmailCredential()
	err := reg.EmailCodeLogin(context.Background(), ec)
	if err == nil {
		t.Error("expected error for nonexistent account_id, got nil")
	}
}

func TestEmailCodeLogin_FlowError(t *testing.T) {
	store := setupTestRegistry(t)

	fr := testFlowResult()
	ec := testEmailCredential()
	regFlow := &mockRegFlowRunner{
		registerResult: fr,
		registerCred:   ec,
	}
	reg := NewRegistry(store, regFlow)

	ctx := context.Background()
	err := reg.Register(ctx)
	if err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	regFlow.reloginErr = fmt.Errorf("relogin flow error: email inbox timeout")

	err = reg.EmailCodeLogin(ctx, ec)
	if err == nil {
		t.Error("expected error from flow.RunRelogin, got nil")
	}
	if !strings.Contains(err.Error(), "relogin flow error") {
		t.Errorf("expected error to contain flow message, got: %v", err)
	}
}

func TestSetEmailCredential(t *testing.T) {
	store := setupTestRegistry(t)
	fr := testFlowResult()
	ec := testEmailCredential()
	flow := &mockRegFlowRunner{
		registerResult: fr,
		registerCred:   ec,
	}
	reg := NewRegistry(store, flow)

	ctx := context.Background()
	err := reg.Register(ctx)
	if err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	account, err := reg.GetByAccountID(ctx, fr.Identity.AccountID)
	if err != nil {
		t.Fatalf("GetByAccountID(%q) error: %v", fr.Identity.AccountID, err)
	}

	newEC := &EmailCredential{
		Email:    "new@test.com",
		Provider: "new_provider",
		Token:    "new_token_abc",
	}
	err = reg.SetEmailCredential(ctx, account.ID, newEC)
	if err != nil {
		t.Fatalf("SetEmailCredential() error: %v", err)
	}

	got, err := reg.GetEmailCredential(ctx, account.ID)
	if err != nil {
		t.Fatalf("GetEmailCredential() error: %v", err)
	}
	if got == nil {
		t.Fatal("GetEmailCredential() returned nil")
	}
	if got.Email != newEC.Email {
		t.Errorf("Email = %q, want %q", got.Email, newEC.Email)
	}
	if got.Provider != newEC.Provider {
		t.Errorf("Provider = %q, want %q", got.Provider, newEC.Provider)
	}
	if got.Token != newEC.Token {
		t.Errorf("Token = %q, want %q", got.Token, newEC.Token)
	}
}

func TestGetEmailCredential_NoCredential(t *testing.T) {
	store := setupTestRegistry(t)
	reg := NewRegistry(store, &mockRegFlowRunner{})

	ctx := context.Background()
	acc := &config.CodexAccount{
		AccountID: "acc_no_cred",
		Email:     "nocred@test.com",
		UserID:    "user_nocred",
		PlanType:  "free",
		Status:    "active",
	}
	if err := store.CreateCodexAccount(acc); err != nil {
		t.Fatalf("create account directly: %v", err)
	}

	// Retrieve the account to get its auto-assigned ID.
	found, err := reg.GetByAccountID(ctx, "acc_no_cred")
	if err != nil {
		t.Fatalf("GetByAccountID() error: %v", err)
	}

	got, err := reg.GetEmailCredential(ctx, found.ID)
	if err != nil {
		t.Fatalf("GetEmailCredential() error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil EmailCredential for fresh account, got: %+v", got)
	}
}

func TestRemove_Success(t *testing.T) {
	store := setupTestRegistry(t)
	fr := testFlowResult()
	ec := testEmailCredential()
	flow := &mockRegFlowRunner{
		registerResult: fr,
		registerCred:   ec,
	}
	reg := NewRegistry(store, flow)

	ctx := context.Background()
	err := reg.Register(ctx)
	if err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	account, err := reg.GetByAccountID(ctx, fr.Identity.AccountID)
	if err != nil {
		t.Fatalf("GetByAccountID(%q) error: %v", fr.Identity.AccountID, err)
	}

	err = reg.Remove(ctx, account.ID)
	if err != nil {
		t.Fatalf("Remove() error: %v", err)
	}

	_, err = reg.GetByID(ctx, account.ID)
	if err == nil {
		t.Error("expected error after Remove, got nil")
	}
}

func TestRemove_NotFound(t *testing.T) {
	store := setupTestRegistry(t)
	reg := NewRegistry(store, &mockRegFlowRunner{})

	err := reg.Remove(context.Background(), 99999)
	if err == nil {
		t.Error("expected error for nonexistent account, got nil")
	}
}

func TestUpdateStatus_Success(t *testing.T) {
	store := setupTestRegistry(t)
	fr := testFlowResult()
	ec := testEmailCredential()
	flow := &mockRegFlowRunner{
		registerResult: fr,
		registerCred:   ec,
	}
	reg := NewRegistry(store, flow)

	ctx := context.Background()
	err := reg.Register(ctx)
	if err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	account, err := reg.GetByAccountID(ctx, fr.Identity.AccountID)
	if err != nil {
		t.Fatalf("GetByAccountID(%q) error: %v", fr.Identity.AccountID, err)
	}

	err = reg.UpdateStatus(ctx, account.ID, "error", "token expired during refresh")
	if err != nil {
		t.Fatalf("UpdateStatus() error: %v", err)
	}

	updated, err := reg.GetByID(ctx, account.ID)
	if err != nil {
		t.Fatalf("GetByID() error: %v", err)
	}
	if updated.Status != "error" {
		t.Errorf("Status = %q, want %q", updated.Status, "error")
	}
	if updated.ErrorMsg != "token expired during refresh" {
		t.Errorf("ErrorMsg = %q, want %q", updated.ErrorMsg, "token expired during refresh")
	}
}

func TestUpdateStatus_NotFound(t *testing.T) {
	store := setupTestRegistry(t)
	reg := NewRegistry(store, &mockRegFlowRunner{})

	err := reg.UpdateStatus(context.Background(), 99999, "active", "")
	if err == nil {
		t.Error("expected error for nonexistent account, got nil")
	}
}

func TestUpdateLastRefresh(t *testing.T) {
	store := setupTestRegistry(t)
	fr := testFlowResult()
	ec := testEmailCredential()
	flow := &mockRegFlowRunner{
		registerResult: fr,
		registerCred:   ec,
	}
	reg := NewRegistry(store, flow)

	ctx := context.Background()
	err := reg.Register(ctx)
	if err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	account, err := reg.GetByAccountID(ctx, fr.Identity.AccountID)
	if err != nil {
		t.Fatalf("GetByAccountID(%q) error: %v", fr.Identity.AccountID, err)
	}

	err = reg.UpdateLastRefresh(ctx, account.ID)
	if err != nil {
		t.Fatalf("UpdateLastRefresh() error: %v", err)
	}

	updated, err := reg.GetByID(ctx, account.ID)
	if err != nil {
		t.Fatalf("GetByID() error: %v", err)
	}
	if updated.LastRefresh == "" {
		t.Error("LastRefresh should not be empty after update")
	}
}

func TestUpdateLastHealth(t *testing.T) {
	store := setupTestRegistry(t)
	fr := testFlowResult()
	ec := testEmailCredential()
	flow := &mockRegFlowRunner{
		registerResult: fr,
		registerCred:   ec,
	}
	reg := NewRegistry(store, flow)

	ctx := context.Background()
	err := reg.Register(ctx)
	if err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	account, err := reg.GetByAccountID(ctx, fr.Identity.AccountID)
	if err != nil {
		t.Fatalf("GetByAccountID(%q) error: %v", fr.Identity.AccountID, err)
	}

	err = reg.UpdateLastHealth(ctx, account.ID)
	if err != nil {
		t.Fatalf("UpdateLastHealth() error: %v", err)
	}

	updated, err := reg.GetByID(ctx, account.ID)
	if err != nil {
		t.Fatalf("GetByID() error: %v", err)
	}
	if updated.LastHealth == "" {
		t.Error("LastHealth should not be empty after update")
	}
}

func TestGetByID_NotFound(t *testing.T) {
	store := setupTestRegistry(t)
	reg := NewRegistry(store, &mockRegFlowRunner{})

	_, err := reg.GetByID(context.Background(), 99999)
	if err == nil {
		t.Error("expected error for nonexistent account, got nil")
	}
}

func TestGetByAccountID_NotFound(t *testing.T) {
	store := setupTestRegistry(t)
	reg := NewRegistry(store, &mockRegFlowRunner{})

	_, err := reg.GetByAccountID(context.Background(), "nonexistent_acc_999")
	if err == nil {
		t.Error("expected error for nonexistent account_id, got nil")
	}
}

func TestUpdateLastRefresh_NotFound(t *testing.T) {
	store := setupTestRegistry(t)
	reg := NewRegistry(store, &mockRegFlowRunner{})

	err := reg.UpdateLastRefresh(context.Background(), 99999)
	if err == nil {
		t.Error("expected error for nonexistent account, got nil")
	}
}

func TestUpdateLastHealth_NotFound(t *testing.T) {
	store := setupTestRegistry(t)
	reg := NewRegistry(store, &mockRegFlowRunner{})

	err := reg.UpdateLastHealth(context.Background(), 99999)
	if err == nil {
		t.Error("expected error for nonexistent account, got nil")
	}
}
