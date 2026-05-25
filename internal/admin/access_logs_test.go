package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/tursom/turapis/internal/config"
)

func setupTestAdminWithLogStore(t *testing.T) *Admin {
	t.Helper()
	store, err := config.NewStore(":memory:", t.TempDir())
	if err != nil {
		t.Fatalf("create test store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	auth := NewAdminAuth(store)
	t.Cleanup(func() { auth.Shutdown() })
	return New(store, nil, auth)
}

func insertAccessLog(t *testing.T, a *Admin, ts time.Time, tokensIn, tokensOut int, attemptsJSON string) {
	t.Helper()
	log := &config.AccessLog{
		Timestamp:    ts.Format(time.RFC3339),
		TokensIn:     tokensIn,
		TokensOut:    tokensOut,
		AttemptsJSON: attemptsJSON,
	}
	if err := a.store.InsertAccessLog(log); err != nil {
		t.Fatalf("insert access log: %v", err)
	}
}

func TestAccessLogStats_Success(t *testing.T) {
	admin := setupTestAdminWithLogStore(t)
	base := time.Date(2025, 5, 25, 10, 0, 0, 0, time.UTC)

	attemptsNormal, _ := json.Marshal([]config.AttemptRecord{
		{Provider: "p1", Success: true, AttemptNum: 1, StatusCode: 200, DurationMs: 100},
	})
	attemptsFailover, _ := json.Marshal([]config.AttemptRecord{
		{Provider: "p1", Success: false, AttemptNum: 1, StatusCode: 500, DurationMs: 200},
		{Provider: "p2", Success: true, AttemptNum: 2, StatusCode: 200, DurationMs: 150},
	})

	insertAccessLog(t, admin, base.Add(2*time.Minute), 100, 50, string(attemptsNormal))
	insertAccessLog(t, admin, base.Add(5*time.Minute), 200, 80, string(attemptsFailover))
	insertAccessLog(t, admin, base.Add(12*time.Minute), 150, 60, "")

	r := chi.NewRouter()
	r.Get("/access-logs/stats", admin.accessLogStats)

	req := httptest.NewRequest("GET", "/access-logs/stats?from="+base.Format(time.RFC3339)+"&to="+base.Add(20*time.Minute).Format(time.RFC3339)+"&interval=10", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Buckets []config.BucketStat `json:"buckets"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(resp.Buckets) != 2 {
		t.Fatalf("expected 2 buckets, got %d", len(resp.Buckets))
	}

	b0 := resp.Buckets[0]
	if b0.CountWithoutFailover != 1 || b0.CountWithFailover != 1 {
		t.Errorf("bucket 0: want (1 without, 1 with), got (%d, %d)", b0.CountWithoutFailover, b0.CountWithFailover)
	}
	if b0.TokensInWithoutFailover != 100 || b0.TokensInWithFailover != 200 {
		t.Errorf("bucket 0 tokens_in: want (100 without, 200 with), got (%d, %d)", b0.TokensInWithoutFailover, b0.TokensInWithFailover)
	}

	b1 := resp.Buckets[1]
	if b1.CountWithoutFailover != 1 || b1.CountWithFailover != 0 {
		t.Errorf("bucket 1: want (1 without, 0 with), got (%d, %d)", b1.CountWithoutFailover, b1.CountWithFailover)
	}
}

func TestAccessLogStats_MissingParams(t *testing.T) {
	admin := setupTestAdminWithLogStore(t)

	r := chi.NewRouter()
	r.Get("/access-logs/stats", admin.accessLogStats)

	req := httptest.NewRequest("GET", "/access-logs/stats", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing params, got %d", w.Code)
	}
}

func TestAccessLogStats_MissingToParam(t *testing.T) {
	admin := setupTestAdminWithLogStore(t)
	base := time.Date(2025, 5, 25, 10, 0, 0, 0, time.UTC)

	r := chi.NewRouter()
	r.Get("/access-logs/stats", admin.accessLogStats)

	req := httptest.NewRequest("GET", "/access-logs/stats?from="+base.Format(time.RFC3339), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing 'to' param, got %d", w.Code)
	}
}

func TestAccessLogStats_InvalidInterval(t *testing.T) {
	admin := setupTestAdminWithLogStore(t)
	base := time.Date(2025, 5, 25, 10, 0, 0, 0, time.UTC)

	r := chi.NewRouter()
	r.Get("/access-logs/stats", admin.accessLogStats)

	req := httptest.NewRequest("GET", "/access-logs/stats?from="+base.Format(time.RFC3339)+"&to="+base.Add(20*time.Minute).Format(time.RFC3339)+"&interval=0", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for interval=0, got %d", w.Code)
	}
}

func TestAccessLogStats_DefaultInterval(t *testing.T) {
	admin := setupTestAdminWithLogStore(t)
	base := time.Date(2025, 5, 25, 10, 0, 0, 0, time.UTC)

	insertAccessLog(t, admin, base.Add(2*time.Minute), 100, 50, "")

	r := chi.NewRouter()
	r.Get("/access-logs/stats", admin.accessLogStats)

	req := httptest.NewRequest("GET", "/access-logs/stats?from="+base.Format(time.RFC3339)+"&to="+base.Add(30*time.Minute).Format(time.RFC3339), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Buckets []config.BucketStat `json:"buckets"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(resp.Buckets) != 3 {
		t.Fatalf("expected 3 buckets with default interval=10, got %d", len(resp.Buckets))
	}
}

func TestAccessLogStats_NoLogStore(t *testing.T) {
	store, err := config.NewStore(":memory:")
	if err != nil {
		t.Fatalf("create test store: %v", err)
	}
	defer store.Close()

	auth := NewAdminAuth(store)
	defer auth.Shutdown()
	admin := New(store, nil, auth)

	base := time.Date(2025, 5, 25, 10, 0, 0, 0, time.UTC)

	r := chi.NewRouter()
	r.Get("/access-logs/stats", admin.accessLogStats)

	req := httptest.NewRequest("GET", "/access-logs/stats?from="+base.Format(time.RFC3339)+"&to="+base.Add(10*time.Minute).Format(time.RFC3339), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Buckets []config.BucketStat `json:"buckets"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(resp.Buckets) != 0 {
		t.Errorf("expected 0 buckets when no log store, got %d", len(resp.Buckets))
	}
}
