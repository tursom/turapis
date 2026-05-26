package config

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

func TestLogStoreMigratesStringTimestampJSON(t *testing.T) {
	dir := t.TempDir()
	db, err := pebble.Open(dir, &pebble.Options{})
	if err != nil {
		t.Fatalf("open raw pebble: %v", err)
	}
	base := time.Date(2025, 5, 25, 10, 0, 0, 0, time.UTC)
	id := uint64(1)
	tsNano := uint64(base.UnixNano())
	raw := []byte(`{"id":1,"timestamp":"` + base.Format(time.RFC3339) + `","method":"GET","path":"/v1/test"}`)
	if err := db.Set(EncodePrimaryKey(tsNano, id), raw, pebble.NoSync); err != nil {
		t.Fatalf("seed primary: %v", err)
	}
	if err := db.Set(EncodeIndexKey(id), EncodeTimestampValue(tsNano), pebble.NoSync); err != nil {
		t.Fatalf("seed index: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close raw pebble: %v", err)
	}

	ls, err := OpenLogStore(dir)
	if err != nil {
		t.Fatalf("open migrated log store: %v", err)
	}
	t.Cleanup(func() { ls.Close() })

	log, err := ls.Get(int(id))
	if err != nil {
		t.Fatalf("get migrated log: %v", err)
	}
	if log.Timestamp != base.UnixMilli() {
		t.Fatalf("timestamp = %d, want %d", log.Timestamp, base.UnixMilli())
	}
}

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
		Timestamp:    ts.UnixMilli(),
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
		base.UnixMilli(),
		base.Add(20*time.Minute).UnixMilli(),
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
		Timestamp:    base.UnixMilli(),
		TokensIn:     100,
		TokensOut:    50,
		AttemptsJSON: "not-json",
	}
	if err := ls.Insert(log); err != nil {
		t.Fatalf("insert: %v", err)
	}

	stats, err := ls.Stats(
		base.UnixMilli(),
		base.Add(10*time.Minute).UnixMilli(),
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
		base.UnixMilli(),
		base.Add(10*time.Minute).UnixMilli(),
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
		base.UnixMilli(),
		base.Add(10*time.Minute).UnixMilli(),
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
		base.UnixMilli(),
		base.Add(10*time.Minute).UnixMilli(),
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
		base.UnixMilli(),
		base.Add(10*time.Minute).UnixMilli(),
		0,
	)
	if err == nil {
		t.Error("expected error for interval=0")
	}

	_, err = ls.Stats(
		base.UnixMilli(),
		base.Add(10*time.Minute).UnixMilli(),
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
		base.Add(10*time.Minute).UnixMilli(),
		base.UnixMilli(),
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
		base.UnixMilli(),
		base.Add(30*time.Minute).UnixMilli(),
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
		base.UnixMilli(),
		base.Add(20*time.Minute).UnixMilli(),
		10,
	)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if len(stats) != 2 {
		t.Fatalf("expected 2 buckets, got %d", len(stats))
	}
	if got := stats[0].Start; got != base.UnixMilli() {
		t.Errorf("bucket 0 start: want %d, got %d", base.UnixMilli(), got)
	}
	if got := stats[0].End; got != base.Add(10*time.Minute).UnixMilli() {
		t.Errorf("bucket 0 end: want %d, got %d", base.Add(10*time.Minute).UnixMilli(), got)
	}
	if got := stats[1].Start; got != base.Add(10*time.Minute).UnixMilli() {
		t.Errorf("bucket 1 start: want %d, got %d", base.Add(10*time.Minute).UnixMilli(), got)
	}
	if got := stats[1].End; got != base.Add(20*time.Minute).UnixMilli() {
		t.Errorf("bucket 1 end: want %d, got %d", base.Add(20*time.Minute).UnixMilli(), got)
	}
}

func TestLogStoreStats_MillisecondRange(t *testing.T) {
	ls := openTestLogStore(t)
	base := time.Date(2025, 5, 25, 10, 5, 0, 0, time.UTC)
	insertTestLog(t, ls, base, 100, 50, nil)

	stats, err := ls.Stats(base.Add(-5*time.Minute).UnixMilli(), base.Add(5*time.Minute).UnixMilli(), 10)
	if err != nil {
		t.Fatalf("Stats with millisecond range should succeed: %v", err)
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
