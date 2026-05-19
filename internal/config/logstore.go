package config

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/jmoiron/sqlx"
)

// ── binary key layout ──────────────────────────────────────────────
//
// Primary:  [0x01][ts_ns:uint64be 8B][counter:uint64be 8B] → JSON   (17 B)
// Index:    [0x02][counter:uint64be 8B] → ts_ns:uint64be 8B          (9 B)
//
// Pebble snappy-compresses values; binary keys are compact and sort
// correctly under lexicographic comparison.

const (
	pebblePrefixMeta    = 0x00
	pebblePrefixPrimary = 0x01
	pebblePrefixIndex   = 0x02
)

// LogStore is a Pebble-backed access log storage engine.
type LogStore struct {
	db     *pebble.DB
	nextID atomic.Uint64
}

// OpenLogStore opens (or creates) a Pebble database at path.
func OpenLogStore(path string) (*LogStore, error) {
	db, err := pebble.Open(path, &pebble.Options{})
	if err != nil {
		return nil, fmt.Errorf("pebble open %s: %w", path, err)
	}
	s := &LogStore{db: db}
	s.initCounter()
	return s, nil
}

// Close flushes and closes the database.
func (s *LogStore) Close() error { return s.db.Close() }

// DB exposes the underlying *pebble.DB for direct batch use by the writer.
func (s *LogStore) DB() *pebble.DB { return s.db }

// NextID atomically returns the next monotonic log id.
func (s *LogStore) NextID() uint64 { return s.nextID.Add(1) }

// ── key encoding helpers (public for use by gateway writer) ──────

// EncodePrimaryKey builds the 17-byte primary key.
func EncodePrimaryKey(tsNano uint64, counter uint64) []byte {
	k := make([]byte, 17)
	k[0] = pebblePrefixPrimary
	binary.BigEndian.PutUint64(k[1:9], tsNano)
	binary.BigEndian.PutUint64(k[9:17], counter)
	return k
}

// EncodeIndexKey builds the 9-byte id-lookup key.
func EncodeIndexKey(counter uint64) []byte {
	k := make([]byte, 9)
	k[0] = pebblePrefixIndex
	binary.BigEndian.PutUint64(k[1:9], counter)
	return k
}

// EncodeTimestampValue encodes a uint64 nanosecond timestamp as 8 bytes.
func EncodeTimestampValue(tsNano uint64) []byte {
	v := make([]byte, 8)
	binary.BigEndian.PutUint64(v, tsNano)
	return v
}

func decodePrimaryKey(k []byte) (tsNano, counter uint64) {
	tsNano = binary.BigEndian.Uint64(k[1:9])
	counter = binary.BigEndian.Uint64(k[9:17])
	return
}

// ── init ──────────────────────────────────────────────────────────

func (s *LogStore) initCounter() {
	iter, _ := s.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte{pebblePrefixIndex, 0, 0, 0, 0, 0, 0, 0, 0},
		UpperBound: []byte{pebblePrefixIndex + 1, 0, 0, 0, 0, 0, 0, 0, 0},
	})
	defer iter.Close()
	if iter.Last() {
		s.nextID.Store(binary.BigEndian.Uint64(iter.Key()[1:9]))
	}
}

// ── query bounds ───────────────────────────────────────────────────

var primaryKeyMin = []byte{pebblePrefixPrimary,
	0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}

var primaryKeyMax = []byte{pebblePrefixPrimary,
	0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF,
	0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}

func primaryKeyLowerBound(tsNano uint64) []byte {
	return EncodePrimaryKey(tsNano, 0)
}

func primaryKeyUpperBound(tsNano uint64) []byte {
	return EncodePrimaryKey(tsNano+1, 0)
}

// ── CRUD ───────────────────────────────────────────────────────────

// Insert adds a single log entry. Called from the batch writer goroutine.
func (s *LogStore) Insert(log *AccessLog) error {
	id := s.NextID()
	log.ID = int(id)

	ts, err := time.Parse(time.RFC3339, log.Timestamp)
	if err != nil {
		ts = time.Now()
	}
	tsNano := uint64(ts.UnixNano())

	jsonData, err := json.Marshal(log)
	if err != nil {
		return fmt.Errorf("marshal log %d: %w", id, err)
	}

	b := s.db.NewBatch()
	defer b.Close()

	b.Set(EncodePrimaryKey(tsNano, id), jsonData, nil)
	b.Set(EncodeIndexKey(id), EncodeTimestampValue(tsNano), nil)

	return b.Commit(pebble.NoSync)
}

// Query returns paginated, filtered access logs (newest first).
func (s *LogStore) Query(q AccessLogQuery) ([]AccessLog, int, error) {
	if q.Page < 1 {
		q.Page = 1
	}
	if q.PerPage < 1 || q.PerPage > 100 {
		q.PerPage = 20
	}

	lowerBound, upperBound, err := s.queryBounds(q.StartAt, q.EndAt)
	if err != nil {
		return nil, 0, err
	}

	iter, _ := s.db.NewIter(&pebble.IterOptions{
		LowerBound: lowerBound,
		UpperBound: upperBound,
	})
	defer iter.Close()

	var matches []AccessLog
	total := 0
	offset := (q.Page - 1) * q.PerPage

	for iter.SeekLT(upperBound); iter.Valid(); iter.Prev() {
		var log AccessLog
		if err := json.Unmarshal(iter.Value(), &log); err != nil {
			continue
		}

		if !s.matchFilters(&log, &q) {
			continue
		}

		total++
		if total > offset && len(matches) < q.PerPage {
			matches = append(matches, log)
		}
	}

	if matches == nil {
		matches = []AccessLog{}
	}
	return matches, total, nil
}

// Get returns a single access log by its numeric id.
func (s *LogStore) Get(id int) (*AccessLog, error) {
	indexKey := EncodeIndexKey(uint64(id))
	tsVal, closer, err := s.db.Get(indexKey)
	if err == pebble.ErrNotFound {
		return nil, fmt.Errorf("access log %d not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("get access log %d: %w", id, err)
	}
	tsNano := binary.BigEndian.Uint64(tsVal)
	closer.Close()

	primaryKey := EncodePrimaryKey(tsNano, uint64(id))
	jsonData, closer2, err := s.db.Get(primaryKey)
	if err == pebble.ErrNotFound {
		return nil, fmt.Errorf("access log %d not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("get access log %d data: %w", id, err)
	}
	defer closer2.Close()

	var log AccessLog
	if err := json.Unmarshal(jsonData, &log); err != nil {
		return nil, fmt.Errorf("unmarshal access log %d: %w", id, err)
	}
	return &log, nil
}

// Cleanup deletes entries older than retentionDays and returns the
// number of deleted rows.
func (s *LogStore) Cleanup(retentionDays int) (int64, error) {
	cutoff := time.Now().AddDate(0, 0, -retentionDays)
	cutoffNano := uint64(cutoff.UnixNano())

	start := primaryKeyMin
	end := primaryKeyLowerBound(cutoffNano)

	iter, _ := s.db.NewIter(&pebble.IterOptions{
		LowerBound: start,
		UpperBound: end,
	})
	defer iter.Close()

	b := s.db.NewBatch()
	defer b.Close()

	var count int64
	flush := func() {
		if b.Count() > 0 {
			_ = b.Commit(pebble.NoSync)
			b.Close()
			b = s.db.NewBatch()
		}
	}

	for iter.First(); iter.Valid(); iter.Next() {
		_, counter := decodePrimaryKey(iter.Key())
		_ = b.Delete(iter.Key(), nil)
		_ = b.Delete(EncodeIndexKey(counter), nil)
		count++
		if b.Count() >= 1000 {
			flush()
		}
	}
	flush()

	_ = s.db.Compact(context.Background(), start, end, true)
	return count, nil
}

var migrationMarkerKey = []byte{pebblePrefixMeta, 'm', 'i', 'g', 'r', 'a', 't', 'e', 'd'}
var migrationCheckpointKey = []byte{pebblePrefixMeta, 'm', 'i', 'g', 'c', 'h', 'k'}

// MigrateFromSQLite imports legacy SQLite access_logs into Pebble.
// Crash-safe via idempotent checkpoint — safe to call on every startup.
func (s *LogStore) MigrateFromSQLite(sqlDB *sqlx.DB) (int, error) {
	if _, c, err := s.db.Get(migrationMarkerKey); err == nil {
		c.Close()
		return 0, nil
	}

	var tableExists int
	if err := sqlDB.Get(&tableExists,
		"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='access_logs'"); err != nil || tableExists == 0 {
		return 0, nil
	}

	var lastSQLiteID int64
	if val, c, err := s.db.Get(migrationCheckpointKey); err == nil {
		lastSQLiteID = int64(binary.BigEndian.Uint64(val))
		c.Close()
	}

	rows, err := sqlDB.Queryx("SELECT * FROM access_logs WHERE id > ? ORDER BY id ASC", lastSQLiteID)
	if err != nil {
		return 0, fmt.Errorf("query legacy access_logs: %w", err)
	}
	defer rows.Close()

	b := s.db.NewBatch()
	migrated := 0

	flush := func() {
		if b.Count() == 0 {
			return
		}
		if err := b.Commit(pebble.NoSync); err != nil {
			slog.Error("pebble_migration_commit_failed", "err", err)
		}
		b.Close()
		b = s.db.NewBatch()
	}

	for rows.Next() {
		var log AccessLog
		if err := rows.StructScan(&log); err != nil {
			slog.Warn("pebble_migration_scan_failed", "err", err)
			continue
		}

		sqliteID := int64(log.ID)
		pebbleID := s.NextID()
		log.ID = int(pebbleID)

		ts, err := time.Parse(time.RFC3339, log.Timestamp)
		if err != nil {
			ts = time.Now()
		}
		tsNano := uint64(ts.UnixNano())

		jsonData, err := json.Marshal(&log)
		if err != nil {
			continue
		}

		_ = b.Set(EncodePrimaryKey(tsNano, pebbleID), jsonData, nil)
		_ = b.Set(EncodeIndexKey(pebbleID), EncodeTimestampValue(tsNano), nil)

		var ck [8]byte
		binary.BigEndian.PutUint64(ck[:], uint64(sqliteID))
		_ = b.Set(migrationCheckpointKey, ck[:], nil)
		migrated++

		if b.Count() >= 500 {
			flush()
		}
	}
	flush()

	if err := rows.Err(); err != nil {
		return migrated, fmt.Errorf("iterate legacy access_logs: %w", err)
	}

	if err := s.db.Set(migrationMarkerKey, []byte{1}, pebble.Sync); err != nil {
		return migrated, fmt.Errorf("write migration marker: %w", err)
	}

	if _, err := sqlDB.Exec("DROP TABLE IF EXISTS access_logs"); err != nil {
		slog.Warn("pebble_migration_drop_table_failed", "err", err)
	}

	_ = s.db.Delete(migrationCheckpointKey, pebble.Sync)

	if migrated > 0 {
		slog.Info("access_logs_migrated", "from", "sqlite", "to", "pebble", "rows", migrated)
	}
	return migrated, nil
}

// ── helpers ────────────────────────────────────────────────────────

func (s *LogStore) queryBounds(startAt, endAt string) (lower, upper []byte, err error) {
	if startAt != "" {
		ts, e := time.Parse(time.RFC3339, startAt)
		if e != nil {
			return nil, nil, fmt.Errorf("parse start_at: %w", e)
		}
		lower = primaryKeyLowerBound(uint64(ts.UnixNano()))
	} else {
		lower = primaryKeyMin
	}

	if endAt != "" {
		ts, e := time.Parse(time.RFC3339, endAt)
		if e != nil {
			return nil, nil, fmt.Errorf("parse end_at: %w", e)
		}
		upper = primaryKeyUpperBound(uint64(ts.UnixNano()))
	} else {
		upper = primaryKeyMax
	}

	return
}

func (s *LogStore) matchFilters(log *AccessLog, q *AccessLogQuery) bool {
	if q.ApiKeyID != nil && (log.ApiKeyID == nil || *log.ApiKeyID != *q.ApiKeyID) {
		return false
	}
	if q.Model != "" && log.Model != q.Model {
		return false
	}
	if q.Status != nil && log.StatusCode != *q.Status {
		return false
	}
	return true
}
