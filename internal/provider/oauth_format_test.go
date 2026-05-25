package provider

import "testing"

func TestOAuthTokensJSON_NewFormat(t *testing.T) {
	newFmt := `{"email":"t@test.com","credential":{"tokens":{"access_token":"eyJtest","refresh_token":"rt"}}}`
	tokens, at := OAuthTokensJSON(newFmt)
	if at != "eyJtest" {
		t.Fatalf("new format access_token = %q, want eyJtest", at)
	}
	if tokens["refresh_token"] != "rt" {
		t.Fatalf("new format refresh_token = %v", tokens["refresh_token"])
	}
}

func TestOAuthTokensJSON_OldFormat(t *testing.T) {
	oldFmt := `{"tokens":{"access_token":"eyJold","refresh_token":"rt_old"}}`
	tokens, at := OAuthTokensJSON(oldFmt)
	if at != "eyJold" {
		t.Fatalf("old format access_token = %q, want eyJold", at)
	}
	if tokens["refresh_token"] != "rt_old" {
		t.Fatalf("old format refresh_token = %v", tokens["refresh_token"])
	}
}

func TestExtractOAuthAccessToken(t *testing.T) {
	if at := ExtractOAuthAccessToken(`{"credential":{"tokens":{"access_token":"eyJ"}}}`); at != "eyJ" {
		t.Fatalf("got %q", at)
	}
	if at := ExtractOAuthAccessToken(`{"tokens":{"access_token":"eyJ"}}`); at != "eyJ" {
		t.Fatalf("got %q", at)
	}
	if at := ExtractOAuthAccessToken(`not json`); at != "" {
		t.Fatalf("got %q, want empty", at)
	}
}

func TestNormalizeOAuthCredential(t *testing.T) {
	newFmt := `{"email":"t@test.com","credential":{"tokens":{"access_token":"eyJtest"}}}`
	norm := NormalizeOAuthCredential(newFmt)
	if norm != `{"tokens":{"access_token":"eyJtest"}}` {
		t.Fatalf("got %s", norm)
	}

	oldFmt := `{"tokens":{"access_token":"eyJold"}}`
	norm = NormalizeOAuthCredential(oldFmt)
	if norm != oldFmt {
		t.Fatalf("old format should pass through, got %s", norm)
	}
}
