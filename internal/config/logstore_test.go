package config

import (
	"encoding/json"
	"testing"
	"time"
)

func openTestLogStore(t *testing.T) *LogStore {
	t.Helper()
	dir := t.TempDir()
	store, err := OpenLogStore(dir)
	if err != nil {
		t.Fatalf("open log store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func insertTestLog(t *testing.T, ls *LogStore, ts time.Time, tokensIn, tokensOut int, attempts []AttemptRecord) {
	t.Helper()
	attemptsJSON := ""
	if attempts != nil {
		b, err := json.Marshal(attempts)
		if err != nil {
			t.Fatalf("marshal attempts: %v", err)
		}
		attemptsJSON = string(b)
	}
	log := &AccessLog{
		Timestamp:    ts.Format(time.RFC3339),
		TokensIn:     tokensIn,
		TokensOut:    tokensOut,
		AttemptsJSON: attemptsJSON,
	}
	if err := ls.Insert(log); err != nil {
		t.Fatalf("insert log: %v", err)
	}
}

func TestLogStoreStats_Basic(t *testing.T) {
	ls := openTestLogStore(t)
	base := time.Date(2025, 5, 25, 10, 0, 0, 0, time.UTC)

	insertTestLog(t, ls, base.Add(2*time.Minute), 100, 50, []AttemptRecord{
		{Provider: "p1", Success: true, AttemptNum: 1, StatusCode: 200, DurationMs: 100},
	})
	insertTestLog(t, ls, base.Add(5*time.Minute), 200, 80, []AttemptRecord{
		{Provider: "p1", Success: false, AttemptNum: 1, StatusCode: 500, DurationMs: 200},
		{Provider: "p2", Success: true, AttemptNum: 2, StatusCode: 200, DurationMs: 150},
	})
	insertTestLog(t, ls, base.Add(12*time.Minute), 150, 60, nil)

	stats, err := ls.Stats(
		base.Format(time.RFC3339),
		base.Add(20*time.Minute).Format(time.RFC3339),
		10,
	)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if len(stats) != 2 {
		t.Fatalf("expected 2 buckets, got %d", len(stats))
	}

	if got := stats[0].CountWithoutFailover; got != 1 {
		t.Errorf("bucket 0 count_without_failover: want 1, got %d", got)
	}
	if got := stats[0].CountWithFailover; got != 1 {
		t.Errorf("bucket 0 count_with_failover: want 1, got %d", got)
	}
	if got := stats[0].TokensInWithoutFailover; got != 100 {
		t.Errorf("bucket 0 tokens_in_without_failover: want 100, got %d", got)
	}
	if got := stats[0].TokensInWithFailover; got != 200 {
		t.Errorf("bucket 0 tokens_in_with_failover: want 200, got %d", got)
	}
	if got := stats[0].TokensOutWithoutFailover; got != 50 {
		t.Errorf("bucket 0 tokens_out_without_failover: want 50, got %d", got)
	}
	if got := stats[0].TokensOutWithFailover; got != 80 {
		t.Errorf("bucket 0 tokens_out_with_failover: want 80, got %d", got)
	}

	if got := stats[1].CountWithoutFailover; got != 1 {
		t.Errorf("bucket 1 count_without_failover: want 1, got %d", got)
	}
	if got := stats[1].CountWithFailover; got != 0 {
		t.Errorf("bucket 1 count_with_failover: want 0, got %d", got)
	}
	if got := stats[1].TokensInWithoutFailover; got != 150 {
		t.Errorf("bucket 1 tokens_in_without_failover: want 150, got %d", got)
	}
}

func TestLogStoreStats_InvalidAttemptsJSON(t *testing.T) {
	ls := openTestLogStore(t)
	base := time.Date(2025, 5, 25, 10, 0, 0, 0, time.UTC)

	log := &AccessLog{
		Timestamp:    base.Format(time.RFC3339),
		TokensIn:     100,
		TokensOut:    50,
		AttemptsJSON: "not-json",
	}
	if err := ls.Insert(log); err != nil {
		t.Fatalf("insert: %v", err)
	}

	stats, err := ls.Stats(
		base.Format(time.RFC3339),
		base.Add(10*time.Minute).Format(time.RFC3339),
		10,
	)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("expected 1 bucket, got %d", len(stats))
	}
	if got := stats[0].CountWithoutFailover; got != 1 {
		t.Errorf("expected 1 count without failover, got %d", got)
	}
	if got := stats[0].CountWithFailover; got != 0 {
		t.Errorf("expected 0 count with failover, got %d", got)
	}
}

func TestLogStoreStats_EmptyAttemptsJSON(t *testing.T) {
	ls := openTestLogStore(t)
	base := time.Date(2025, 5, 25, 10, 0, 0, 0, time.UTC)

	insertTestLog(t, ls, base.Add(1*time.Minute), 100, 50, nil)

	stats, err := ls.Stats(
		base.Format(time.RFC3339),
		base.Add(10*time.Minute).Format(time.RFC3339),
		10,
	)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("expected 1 bucket, got %d", len(stats))
	}
	if got := stats[0].CountWithoutFailover; got != 1 {
		t.Errorf("expected 1 count without failover, got %d", got)
	}
	if got := stats[0].CountWithFailover; got != 0 {
		t.Errorf("expected 0 count with failover, got %d", got)
	}
}

func TestLogStoreStats_OnlyAttemptNum(t *testing.T) {
	ls := openTestLogStore(t)
	base := time.Date(2025, 5, 25, 10, 0, 0, 0, time.UTC)

	insertTestLog(t, ls, base.Add(2*time.Minute), 100, 50, []AttemptRecord{
		{Provider: "p1", Success: true, AttemptNum: 3, StatusCode: 200, DurationMs: 100},
	})

	stats, err := ls.Stats(
		base.Format(time.RFC3339),
		base.Add(10*time.Minute).Format(time.RFC3339),
		10,
	)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("expected 1 bucket, got %d", len(stats))
	}
	if got := stats[0].CountWithFailover; got != 1 {
		t.Errorf("expected 1 count with failover, got %d", got)
	}
	if got := stats[0].CountWithoutFailover; got != 0 {
		t.Errorf("expected 0 count without failover, got %d", got)
	}
}

func TestLogStoreStats_OutOfRangeEntry(t *testing.T) {
	ls := openTestLogStore(t)
	base := time.Date(2025, 5, 25, 10, 0, 0, 0, time.UTC)

	insertTestLog(t, ls, base.Add(-1*time.Hour), 10, 5, nil)
	insertTestLog(t, ls, base.Add(5*time.Minute), 100, 50, nil)
	insertTestLog(t, ls, base.Add(2*time.Hour), 200, 80, nil)

	stats, err := ls.Stats(
		base.Format(time.RFC3339),
		base.Add(10*time.Minute).Format(time.RFC3339),
		10,
	)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("expected 1 bucket, got %d", len(stats))
	}
	if got := stats[0].CountWithoutFailover; got != 1 {
		t.Errorf("expected 1 count without failover, got %d", got)
	}
}

func TestLogStoreStats_InvalidInterval(t *testing.T) {
	ls := openTestLogStore(t)
	base := time.Date(2025, 5, 25, 10, 0, 0, 0, time.UTC)

	_, err := ls.Stats(
		base.Format(time.RFC3339),
		base.Add(10*time.Minute).Format(time.RFC3339),
		0,
	)
	if err == nil {
		t.Error("expected error for interval=0")
	}

	_, err = ls.Stats(
		base.Format(time.RFC3339),
		base.Add(10*time.Minute).Format(time.RFC3339),
		-1,
	)
	if err == nil {
		t.Error("expected error for interval=-1")
	}
}

func TestLogStoreStats_StartAfterEnd(t *testing.T) {
	ls := openTestLogStore(t)
	base := time.Date(2025, 5, 25, 10, 0, 0, 0, time.UTC)

	_, err := ls.Stats(
		base.Add(10*time.Minute).Format(time.RFC3339),
		base.Format(time.RFC3339),
		10,
	)
	if err == nil {
		t.Error("expected error for start after end")
	}
}

func TestLogStoreStats_AllEmptyBuckets(t *testing.T) {
	ls := openTestLogStore(t)
	base := time.Date(2025, 5, 25, 10, 0, 0, 0, time.UTC)

	stats, err := ls.Stats(
		base.Format(time.RFC3339),
		base.Add(30*time.Minute).Format(time.RFC3339),
		10,
	)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if len(stats) != 3 {
		t.Fatalf("expected 3 buckets, got %d", len(stats))
	}
	for i, b := range stats {
		if b.CountWithFailover != 0 {
			t.Errorf("bucket %d: expected 0 count_with_failover, got %d", i, b.CountWithFailover)
		}
		if b.CountWithoutFailover != 0 {
			t.Errorf("bucket %d: expected 0 count_without_failover, got %d", i, b.CountWithoutFailover)
		}
	}
}

func TestLogStoreStats_BucketBoundaries(t *testing.T) {
	ls := openTestLogStore(t)
	base := time.Date(2025, 5, 25, 10, 0, 0, 0, time.UTC)

	insertTestLog(t, ls, base.Add(5*time.Minute), 10, 5, nil)

	stats, err := ls.Stats(
		base.Format(time.RFC3339),
		base.Add(20*time.Minute).Format(time.RFC3339),
		10,
	)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if len(stats) != 2 {
		t.Fatalf("expected 2 buckets, got %d", len(stats))
	}
	if got := stats[0].Start; got != "2025-05-25T10:00:00Z" {
		t.Errorf("bucket 0 start: want 2025-05-25T10:00:00Z, got %s", got)
	}
	if got := stats[0].End; got != "2025-05-25T10:10:00Z" {
		t.Errorf("bucket 0 end: want 2025-05-25T10:10:00Z, got %s", got)
	}
	if got := stats[1].Start; got != "2025-05-25T10:10:00Z" {
		t.Errorf("bucket 1 start: want 2025-05-25T10:10:00Z, got %s", got)
	}
	if got := stats[1].End; got != "2025-05-25T10:20:00Z" {
		t.Errorf("bucket 1 end: want 2025-05-25T10:20:00Z, got %s", got)
	}
}

func TestLogStoreStats_DatetimeLocalFormat(t *testing.T) {
	ls := openTestLogStore(t)
	base := time.Date(2025, 5, 25, 10, 5, 0, 0, time.UTC)
	insertTestLog(t, ls, base, 100, 50, nil)

	// datetime-local format: "2025-05-25T10:00" (no seconds, no timezone)
	stats, err := ls.Stats("2025-05-25T10:00", "2025-05-25T10:10", 10)
	if err != nil {
		t.Fatalf("Stats with datetime-local format should succeed: %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("expected 1 bucket, got %d", len(stats))
	}
	if stats[0].CountWithoutFailover != 1 {
		t.Errorf("expected 1 without failover, got %d", stats[0].CountWithoutFailover)
	}
	if stats[0].TokensInWithoutFailover != 100 {
		t.Errorf("expected 100 tokens_in, got %d", stats[0].TokensInWithoutFailover)
	}
	if stats[0].TokensOutWithoutFailover != 50 {
		t.Errorf("expected 50 tokens_out, got %d", stats[0].TokensOutWithoutFailover)
	}
}
