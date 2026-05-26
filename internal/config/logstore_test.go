package config

import (
	"context"
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

func insertDetailedTestLog(t *testing.T, ls *LogStore, log AccessLog) AccessLog {
	t.Helper()
	if err := ls.Insert(&log); err != nil {
		t.Fatalf("insert log: %v", err)
	}
	return log
}

func forceV2Ready(t *testing.T, ls *LogStore) {
	t.Helper()
	if _, err := ls.BackfillAccessLogV2(context.Background()); err != nil {
		t.Fatalf("backfill v2: %v", err)
	}
}

func TestLogStoreV2QueryFiltersPaginationAndDetail(t *testing.T) {
	ls := openTestLogStore(t)
	base := time.Date(2025, 5, 25, 10, 0, 0, 0, time.UTC)
	key1 := 11
	key2 := 22

	insertDetailedTestLog(t, ls, AccessLog{
		Timestamp:    base.Add(1 * time.Minute).UnixMilli(),
		ApiKeyID:     &key1,
		ApiKeyName:   "key-a",
		Method:       "POST",
		Path:         "/v1/responses",
		Model:        "gpt-fast",
		StatusCode:   200,
		TokensIn:     10,
		TokensOut:    5,
		ProviderName: "provider-a",
		ClientReq:    `{"large":"request"}`,
		ClientResp:   `{"large":"response"}`,
	})
	insertDetailedTestLog(t, ls, AccessLog{
		Timestamp:    base.Add(2 * time.Minute).UnixMilli(),
		ApiKeyID:     &key1,
		ApiKeyName:   "key-a",
		Method:       "POST",
		Path:         "/v1/chat/completions",
		Model:        "gpt-fast",
		StatusCode:   500,
		TokensIn:     20,
		TokensOut:    7,
		ProviderName: "provider-b",
		ErrorMsg:     "upstream failed",
	})
	insertDetailedTestLog(t, ls, AccessLog{
		Timestamp:  base.Add(3 * time.Minute).UnixMilli(),
		ApiKeyID:   &key2,
		Model:      "gpt-slow",
		StatusCode: 200,
		TokensIn:   30,
		TokensOut:  9,
	})
	forceV2Ready(t, ls)

	status := 200
	logs, total, err := ls.Query(AccessLogQuery{
		ApiKeyID: &key1,
		Model:    "gpt-fast",
		Status:   &status,
		Page:     1,
		PerPage:  10,
	})
	if err != nil {
		t.Fatalf("query v2 filters: %v", err)
	}
	if total != 1 || len(logs) != 1 {
		t.Fatalf("filtered total/len = %d/%d, want 1/1", total, len(logs))
	}
	if logs[0].ClientReq != "" || logs[0].ClientResp != "" {
		t.Fatalf("list summary should not include large payloads: %#v", logs[0])
	}

	logs, total, err = ls.Query(AccessLogQuery{Model: "gpt-fast", Page: 2, PerPage: 1})
	if err != nil {
		t.Fatalf("query v2 page 2: %v", err)
	}
	if total != 2 || len(logs) != 1 {
		t.Fatalf("page total/len = %d/%d, want 2/1", total, len(logs))
	}
	if logs[0].StatusCode != 200 {
		t.Fatalf("page 2 should contain older 200 row, got status %d", logs[0].StatusCode)
	}

	detail, err := ls.Get(logs[0].ID)
	if err != nil {
		t.Fatalf("get detail: %v", err)
	}
	if detail.ClientReq == "" || detail.ClientResp == "" {
		t.Fatalf("detail should keep full payloads: %#v", detail)
	}
}

func TestLogStoreV2StatsUsesMinuteAggregateWithPartialBoundaries(t *testing.T) {
	ls := openTestLogStore(t)
	base := time.Date(2025, 5, 25, 10, 0, 30, 0, time.UTC)

	insertTestLog(t, ls, base, 10, 1, nil)
	insertTestLog(t, ls, base.Add(30*time.Second), 20, 2, []AttemptRecord{
		{Provider: "p1", Success: false, AttemptNum: 1, StatusCode: 500},
		{Provider: "p2", Success: true, AttemptNum: 2, StatusCode: 200},
	})
	insertTestLog(t, ls, base.Add(10*time.Minute), 30, 3, nil)
	forceV2Ready(t, ls)

	stats, err := ls.Stats(base.UnixMilli(), base.Add(20*time.Minute).UnixMilli(), 10)
	if err != nil {
		t.Fatalf("stats v2: %v", err)
	}
	if len(stats) != 2 {
		t.Fatalf("expected 2 buckets, got %d", len(stats))
	}
	if stats[0].CountWithoutFailover != 1 || stats[0].TokensInWithoutFailover != 10 {
		t.Fatalf("bucket 0 without failover = count %d tokens %d", stats[0].CountWithoutFailover, stats[0].TokensInWithoutFailover)
	}
	if stats[0].CountWithFailover != 1 || stats[0].TokensInWithFailover != 20 {
		t.Fatalf("bucket 0 with failover = count %d tokens %d", stats[0].CountWithFailover, stats[0].TokensInWithFailover)
	}
	if stats[1].CountWithoutFailover != 1 || stats[1].TokensInWithoutFailover != 30 {
		t.Fatalf("bucket 1 without failover = count %d tokens %d", stats[1].CountWithoutFailover, stats[1].TokensInWithoutFailover)
	}
}

func TestLogStoreV2BackfillCheckpointAndIdempotency(t *testing.T) {
	ls := openTestLogStore(t)
	base := time.Date(2025, 5, 25, 10, 0, 0, 0, time.UTC)

	seedLegacyPrimary := func(id uint64, ts time.Time, model string, status int, tokensIn int) {
		t.Helper()
		log := AccessLog{
			ID:         int(id),
			Timestamp:  ts.UnixMilli(),
			Model:      model,
			StatusCode: status,
			TokensIn:   tokensIn,
			TokensOut:  1,
		}
		raw, err := json.Marshal(log)
		if err != nil {
			t.Fatalf("marshal legacy log: %v", err)
		}
		tsNano := uint64(ts.UnixNano())
		if err := ls.db.Set(EncodePrimaryKey(tsNano, id), raw, pebble.NoSync); err != nil {
			t.Fatalf("seed primary: %v", err)
		}
		if err := ls.db.Set(EncodeIndexKey(id), EncodeTimestampValue(tsNano), pebble.NoSync); err != nil {
			t.Fatalf("seed id index: %v", err)
		}
		if id > ls.nextID.Load() {
			ls.nextID.Store(id)
		}
		ls.total.Add(1)
	}

	seedLegacyPrimary(1, base.Add(1*time.Minute), "legacy-model", 200, 10)
	seedLegacyPrimary(2, base.Add(2*time.Minute), "legacy-model", 200, 20)

	n, err := ls.backfillAccessLogV2(context.Background(), 1, 0)
	if err != nil {
		t.Fatalf("partial backfill: %v", err)
	}
	if n != 1 {
		t.Fatalf("partial backfill rows = %d, want 1", n)
	}
	if ls.isAccessLogV2Ready() {
		t.Fatalf("v2 should not be ready after partial backfill")
	}

	n, err = ls.BackfillAccessLogV2(context.Background())
	if err != nil {
		t.Fatalf("resume backfill: %v", err)
	}
	if n != 1 {
		t.Fatalf("resume backfill rows = %d, want 1", n)
	}
	if !ls.isAccessLogV2Ready() {
		t.Fatalf("v2 should be ready after resumed backfill")
	}

	status := 200
	logs, total, err := ls.Query(AccessLogQuery{Model: "legacy-model", Status: &status, Page: 1, PerPage: 10})
	if err != nil {
		t.Fatalf("query backfilled logs: %v", err)
	}
	if total != 2 || len(logs) != 2 {
		t.Fatalf("query total/len = %d/%d, want 2/2", total, len(logs))
	}

	n, err = ls.BackfillAccessLogV2(context.Background())
	if err != nil {
		t.Fatalf("second backfill: %v", err)
	}
	if n != 0 {
		t.Fatalf("second backfill rows = %d, want 0", n)
	}
	stats, err := ls.Stats(base.UnixMilli(), base.Add(10*time.Minute).UnixMilli(), 10)
	if err != nil {
		t.Fatalf("stats after second backfill: %v", err)
	}
	if stats[0].CountWithoutFailover != 2 || stats[0].TokensInWithoutFailover != 30 {
		t.Fatalf("stats after idempotent backfill = count %d tokens %d", stats[0].CountWithoutFailover, stats[0].TokensInWithoutFailover)
	}
}

func TestLogStoreV2CleanupRemovesIndexesAndStats(t *testing.T) {
	ls := openTestLogStore(t)
	old := time.Now().AddDate(0, 0, -31)
	recent := time.Now().AddDate(0, 0, -1)
	status := 200

	insertDetailedTestLog(t, ls, AccessLog{
		Timestamp:  old.UnixMilli(),
		Model:      "cleanup-model",
		StatusCode: status,
		TokensIn:   10,
		TokensOut:  1,
	})
	insertDetailedTestLog(t, ls, AccessLog{
		Timestamp:  recent.UnixMilli(),
		Model:      "cleanup-model",
		StatusCode: status,
		TokensIn:   20,
		TokensOut:  2,
	})
	forceV2Ready(t, ls)

	deleted, err := ls.Cleanup(30)
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1", deleted)
	}

	logs, total, err := ls.Query(AccessLogQuery{Model: "cleanup-model", Status: &status, Page: 1, PerPage: 10})
	if err != nil {
		t.Fatalf("query after cleanup: %v", err)
	}
	if total != 1 || len(logs) != 1 || logs[0].TokensIn != 20 {
		t.Fatalf("remaining total/len/log = %d/%d/%#v", total, len(logs), logs)
	}

	stats, err := ls.Stats(old.Add(-time.Hour).UnixMilli(), recent.Add(time.Hour).UnixMilli(), 24*60)
	if err != nil {
		t.Fatalf("stats after cleanup: %v", err)
	}
	totalCount := 0
	for _, b := range stats {
		totalCount += b.CountWithoutFailover + b.CountWithFailover
	}
	if totalCount != 1 {
		t.Fatalf("stats count after cleanup = %d, want 1", totalCount)
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
