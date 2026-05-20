package provider

import (
	"net/http"
	"testing"
)

func TestParseQuotaReadsCodexQuotaHeaders(t *testing.T) {
	h := http.Header{}
	h.Set("x-codex-primary-used-percent", "12.5")
	h.Set("x-codex-primary-reset-after-seconds", "300")
	h.Set("x-codex-primary-window-minutes", "300")
	h.Set("x-codex-secondary-used-percent", "45")
	h.Set("x-codex-secondary-reset-after-seconds", "0")
	h.Set("x-codex-secondary-window-minutes", "10080")
	h.Set("x-codex-tertiary-used-percent", "100")
	h.Set("x-codex-tertiary-reset-after-seconds", "86400")
	h.Set("x-codex-tertiary-window-minutes", "43200")

	got := ParseQuota(h)

	assertQuotaEntry(t, got, "primary", 12.5, 300, 300)
	assertQuotaEntry(t, got, "secondary", 45, 0, 10080)
	assertQuotaEntry(t, got, "tertiary", 100, 86400, 43200)
}

func TestParseQuotaIgnoresMissingAndInvalidHeaders(t *testing.T) {
	h := http.Header{}
	h.Set("x-codex-primary-used-percent", "not-a-number")
	h.Set("x-codex-primary-reset-after-seconds", "also-bad")
	h.Set("x-codex-secondary-used-percent", "7")
	h.Set("x-codex-secondary-window-minutes", "10080")

	got := ParseQuota(h)

	if _, ok := got["primary"]; ok {
		t.Fatalf("primary quota should be omitted for invalid-only headers: %#v", got)
	}
	secondary, ok := got["secondary"].(map[string]interface{})
	if !ok {
		t.Fatalf("secondary quota missing: %#v", got)
	}
	if secondary["used_percent"] != 7.0 {
		t.Fatalf("secondary used_percent = %#v, want 7", secondary["used_percent"])
	}
	if secondary["window_minutes"] != 10080 {
		t.Fatalf("secondary window_minutes = %#v, want 10080", secondary["window_minutes"])
	}
	if _, ok := secondary["reset_after_seconds"]; ok {
		t.Fatalf("secondary reset_after_seconds should be omitted: %#v", secondary)
	}
}

func assertQuotaEntry(t *testing.T, quota map[string]interface{}, name string, used float64, reset, window int) {
	t.Helper()
	entry, ok := quota[name].(map[string]interface{})
	if !ok {
		t.Fatalf("%s quota missing: %#v", name, quota)
	}
	if entry["used_percent"] != used {
		t.Fatalf("%s used_percent = %#v, want %v", name, entry["used_percent"], used)
	}
	if entry["reset_after_seconds"] != reset {
		t.Fatalf("%s reset_after_seconds = %#v, want %d", name, entry["reset_after_seconds"], reset)
	}
	if entry["window_minutes"] != window {
		t.Fatalf("%s window_minutes = %#v, want %d", name, entry["window_minutes"], window)
	}
}
