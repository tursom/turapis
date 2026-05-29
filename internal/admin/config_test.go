package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tursom/turapis/internal/config"
)

func TestUpdateConfigAcceptsAccessLogSaveBodies(t *testing.T) {
	store, err := config.NewStore(":memory:")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer store.Close()
	admin := &Admin{store: store}

	req := httptest.NewRequest(http.MethodPut, "/admin/config", strings.NewReader(`{"key":"access_log_save_bodies","value":"false"}`))
	rec := httptest.NewRecorder()

	admin.updateConfig(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	got, err := store.GetSetting("access_log_save_bodies")
	if err != nil {
		t.Fatalf("get setting: %v", err)
	}
	if got != "false" {
		t.Fatalf("access_log_save_bodies = %q, want false", got)
	}
}

func TestUpdateConfigRejectsInvalidAccessLogSaveBodies(t *testing.T) {
	store, err := config.NewStore(":memory:")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer store.Close()
	admin := &Admin{store: store}

	req := httptest.NewRequest(http.MethodPut, "/admin/config", strings.NewReader(`{"key":"access_log_save_bodies","value":"nope"}`))
	rec := httptest.NewRecorder()

	admin.updateConfig(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	got, err := store.GetSetting("access_log_save_bodies")
	if err != nil {
		t.Fatalf("get setting: %v", err)
	}
	if got != "" {
		t.Fatalf("access_log_save_bodies = %q, want empty", got)
	}
}
