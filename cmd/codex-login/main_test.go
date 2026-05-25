package main

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/tursom/turapis/internal/codexauth"
)

func makeFakeJWT(claims map[string]any) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	payloadBytes, _ := json.Marshal(claims)
	payload := base64.RawURLEncoding.EncodeToString(payloadBytes)
	return header + "." + payload + ".fake_signature"
}

func TestFormatOutput_Success(t *testing.T) {
	at := makeFakeJWT(map[string]any{"client_id": "test-client-id"})
	result := &codexauth.FlowResult{
		Tokens: &codexauth.TokenSet{
			AccessToken:  at,
			RefreshToken: "test-refresh-token",
			IDToken:      "test-id-token",
			AccountID:    "acc-123",
			ExpiresAt:    1716249600000,
		},
		Identity: &codexauth.AccountIdentity{
			Email:     "test@example.com",
			AccountID: "acc-123",
			UserID:    "usr-456",
			PlanType:  "plus",
		},
	}

	out, err := formatOutput(result)
	if err != nil {
		t.Fatalf("formatOutput() error: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("failed to parse output JSON: %v", err)
	}

	if parsed["email"] != "test@example.com" {
		t.Errorf("email = %q, want %q", parsed["email"], "test@example.com")
	}
	if parsed["account_id"] != "acc-123" {
		t.Errorf("account_id = %q, want %q", parsed["account_id"], "acc-123")
	}
	if parsed["user_id"] != "usr-456" {
		t.Errorf("user_id = %q, want %q", parsed["user_id"], "usr-456")
	}
	if parsed["plan_type"] != "plus" {
		t.Errorf("plan_type = %q, want %q", parsed["plan_type"], "plus")
	}

	cred, ok := parsed["credential"].(map[string]any)
	if !ok {
		t.Fatal("credential field missing or not an object")
	}
	tokens, ok := cred["tokens"].(map[string]any)
	if !ok {
		t.Fatal("credential.tokens field missing or not an object")
	}

	if tokens["access_token"] != at {
		t.Errorf("access_token = %q, want JWT", tokens["access_token"])
	}
	if tokens["refresh_token"] != "test-refresh-token" {
		t.Errorf("refresh_token = %q, want %q", tokens["refresh_token"], "test-refresh-token")
	}
	if tokens["client_id"] != "test-client-id" {
		t.Errorf("client_id = %q, want %q", tokens["client_id"], "test-client-id")
	}
}

func TestFormatOutput_MissingClientID(t *testing.T) {
	result := &codexauth.FlowResult{
		Tokens: &codexauth.TokenSet{
			AccessToken: "not-a-valid-jwt",
		},
		Identity: &codexauth.AccountIdentity{
			Email: "test@example.com",
		},
	}

	_, err := formatOutput(result)
	if err == nil {
		t.Fatal("expected error for missing client_id, got nil")
	}
	if !strings.Contains(err.Error(), "client_id") {
		t.Errorf("error should mention client_id, got: %v", err)
	}
}

func TestFormatOutput_EmptyIdentityFields(t *testing.T) {
	at := makeFakeJWT(map[string]any{"client_id": "test-client"})
	result := &codexauth.FlowResult{
		Tokens: &codexauth.TokenSet{
			AccessToken: at,
			AccountID:   "acc-empty",
			ExpiresAt:   0,
		},
		Identity: &codexauth.AccountIdentity{
			Email:     "",
			AccountID: "acc-empty",
			UserID:    "",
			PlanType:  "",
		},
	}

	out, err := formatOutput(result)
	if err != nil {
		t.Fatalf("formatOutput() error on empty fields: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("failed to parse output JSON: %v", err)
	}

	if parsed["email"] != "" {
		t.Errorf("email = %q, want empty", parsed["email"])
	}
	if parsed["user_id"] != "" {
		t.Errorf("user_id = %q, want empty", parsed["user_id"])
	}

	// credential should still be valid
	if parsed["credential"] == nil {
		t.Fatal("credential field should not be nil")
	}
}
