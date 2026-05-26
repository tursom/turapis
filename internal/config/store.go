package config

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jmoiron/sqlx"
	"golang.org/x/crypto/bcrypt"

	_ "modernc.org/sqlite"
)

// Provider 上游 API 提供者配置
type Provider struct {
	ID             int              `db:"id" json:"id"`
	Name           string           `db:"name" json:"name"`
	BaseURL        string           `db:"base_url" json:"base_url"`
	APIKey         string           `db:"api_key" json:"api_key"`
	Protocol       string           `db:"protocol" json:"protocol"`
	AuthMode       string           `db:"auth_mode" json:"auth_mode"`
	Priority       int              `db:"priority" json:"priority"`
	Enabled        bool             `db:"enabled" json:"enabled"`
	SupportedTools string           `db:"supported_tools" json:"supported_tools"`
	Proxy          string           `db:"proxy" json:"proxy"`
	Quota          *json.RawMessage `db:"-" json:"quota,omitempty"`
	CreatedAt      int64            `db:"created_at" json:"created_at"`
	UpdatedAt      int64            `db:"updated_at" json:"updated_at"`
}

// Site 站点预设（Provider 模板，不含认证信息）
type Site struct {
	ID        int    `db:"id" json:"id"`
	Name      string `db:"name" json:"name"`
	BaseURL   string `db:"base_url" json:"base_url"`
	Protocol  string `db:"protocol" json:"protocol"`
	AuthMode  string `db:"auth_mode" json:"auth_mode"`
	Enabled   bool   `db:"enabled" json:"enabled"`
	CreatedAt int64  `db:"created_at" json:"created_at"`
	UpdatedAt int64  `db:"updated_at" json:"updated_at"`
}

// SiteModel 站点预设模型
type SiteModel struct {
	ID        int    `db:"id" json:"id"`
	SiteID    int    `db:"site_id" json:"site_id"`
	ModelID   string `db:"model_id" json:"model_id"`
	ModelName string `db:"model_name" json:"model_name"`
}

// ModelMapping 模型到 Provider 的映射
type ModelMapping struct {
	ID         int    `db:"id" json:"id"`
	ModelName  string `db:"model_name" json:"model_name"`
	ProviderID int    `db:"provider_id" json:"provider_id"`
	Priority   int    `db:"priority" json:"priority"`
	Enabled    bool   `db:"enabled" json:"enabled"`
	CreatedAt  int64  `db:"created_at" json:"created_at"`
}

// PriorityChainEntry 优先级链中的一个条目（Provider ID + 优先级）
type PriorityChainEntry struct {
	ProviderID int `db:"provider_id" json:"provider_id"`
	Priority   int `db:"priority" json:"priority"`
}

// Store SQLite 配置存储
type Store struct {
	DB       *sqlx.DB
	LogStore *LogStore
}

// NewStore 创建新的 Store，初始化数据库和表。
// logDBPath 可选：指定 Pebble 数据库路径用于访问日志存储，留空则使用 SQLite。
func NewStore(dbPath string, logDBPath ...string) (*Store, error) {
	var dsn string
	if dbPath == ":memory:" {
		dsn = "file::memory:?cache=shared"
	} else {
		dsn = fmt.Sprintf("file:%s?_journal_mode=WAL&_busy_timeout=5000", dbPath)
	}
	db, err := sqlx.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	// 单连接避免 WAL 锁竞争（SQLite 串行化写入，多连接无意义）
	db.SetMaxOpenConns(1)

	// 备用路径：通过 PRAGMA 设置
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA foreign_keys=ON",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("pragma %s: %w", pragma, err)
		}
	}

	if err := runMigrations(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("run migrations: %w", err)
	}

	s := &Store{DB: db}
	if len(logDBPath) > 0 && logDBPath[0] != "" {
		ls, err := OpenLogStore(logDBPath[0])
		if err != nil {
			db.Close()
			return nil, fmt.Errorf("open log store: %w", err)
		}
		s.LogStore = ls
		if n, err := ls.MigrateFromSQLite(db); err != nil {
			slog.Warn("access_log_migration_failed", "err", err)
		} else if n > 0 {
			slog.Info("access_log_migrated", "from", "sqlite", "to", "pebble", "rows", n)
		}
		ls.StartAccessLogV2Backfill(context.Background())
	}
	return s, nil
}

// Close 关闭数据库连接
func (s *Store) Close() error {
	if s.LogStore != nil {
		if err := s.LogStore.Close(); err != nil {
			slog.Warn("logstore_close_failed", "err", err)
		}
	}
	return s.DB.Close()
}

// --- Session CRUD ---

func (s *Store) CreateSession(token string, userID int64, expiresAt time.Time) error {
	now := time.Now().UnixMilli()
	_, err := s.DB.Exec(
		"INSERT INTO sessions (token, user_id, expires_at, created_at) VALUES (?, ?, ?, ?)",
		token, userID, expiresAt.UnixMilli(), now,
	)
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

func (s *Store) DeleteSession(token string) error {
	_, err := s.DB.Exec("DELETE FROM sessions WHERE token = ?", token)
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

func (s *Store) DeleteExpiredSessions() (int64, error) {
	result, err := s.DB.Exec(
		"DELETE FROM sessions WHERE expires_at < ?",
		time.Now().UnixMilli(),
	)
	if err != nil {
		return 0, fmt.Errorf("delete expired sessions: %w", err)
	}
	n, _ := result.RowsAffected()
	return n, nil
}

func (s *Store) ValidateSession(token string) bool {
	var expiresAt int64
	err := s.DB.Get(&expiresAt, "SELECT expires_at FROM sessions WHERE token = ?", token)
	if err != nil {
		return false
	}
	return time.Now().UnixMilli() < expiresAt
}

type SessionInfo struct {
	UserID   int64  `db:"user_id"`
	Role     string `db:"role"`
	Username string `db:"username"`
}

func (s *Store) GetSessionUser(token string) (*SessionInfo, error) {
	var info SessionInfo
	err := s.DB.Get(&info,
		`SELECT u.id AS "user_id", u.role, u.username
		 FROM sessions s JOIN users u ON u.id = s.user_id
		 WHERE s.token = ? AND s.expires_at > ? AND u.enabled = 1`,
		token, time.Now().UnixMilli(),
	)
	if err != nil {
		return nil, fmt.Errorf("get session user: %w", err)
	}
	return &info, nil
}

func (s *Store) RefreshSession(token string, ttls ...time.Duration) bool {
	ttl := 24 * time.Hour
	if len(ttls) > 0 {
		ttl = ttls[0]
	}
	newExpires := time.Now().Add(ttl).UnixMilli()
	result, err := s.DB.Exec(
		"UPDATE sessions SET expires_at = ? WHERE token = ? AND expires_at > ?",
		newExpires, token, time.Now().UnixMilli(),
	)
	if err != nil {
		return false
	}
	n, _ := result.RowsAffected()
	return n > 0
}

// --- Provider CRUD ---

// CreateProvider 创建 Provider
func (s *Store) CreateProvider(p *Provider) error {
	now := time.Now().UnixMilli()
	p.CreatedAt = now
	p.UpdatedAt = now

	result, err := s.DB.NamedExec(
		`INSERT INTO providers (name, base_url, api_key, protocol, auth_mode, priority, enabled, supported_tools, proxy, created_at, updated_at)
		 VALUES (:name, :base_url, :api_key, :protocol, :auth_mode, :priority, :enabled, :supported_tools, :proxy, :created_at, :updated_at)`,
		p,
	)
	if err != nil {
		return fmt.Errorf("create provider: %w", err)
	}
	id, _ := result.LastInsertId()
	p.ID = int(id)
	return nil
}

// GetProvider 获取单个 Provider
func (s *Store) GetProvider(id int) (*Provider, error) {
	var p Provider
	err := s.DB.Get(&p, "SELECT * FROM providers WHERE id = ?", id)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("provider %d not found", id)
		}
		return nil, fmt.Errorf("get provider: %w", err)
	}
	return &p, nil
}

// ListProviders 列出所有 Provider（按优先级排序）
func (s *Store) ListProviders() ([]Provider, error) {
	var providers []Provider
	err := s.DB.Select(&providers, "SELECT * FROM providers ORDER BY priority ASC")
	if err != nil {
		return nil, fmt.Errorf("list providers: %w", err)
	}
	if providers == nil {
		providers = []Provider{}
	}
	return providers, nil
}

// ListEnabledProviders 列出所有启用的 Provider（按优先级排序）
func (s *Store) ListEnabledProviders() ([]Provider, error) {
	var providers []Provider
	err := s.DB.Select(&providers, "SELECT * FROM providers WHERE enabled = 1 ORDER BY priority ASC")
	if err != nil {
		return nil, fmt.Errorf("list enabled providers: %w", err)
	}
	if providers == nil {
		providers = []Provider{}
	}
	return providers, nil
}

// UpdateProvider 更新 Provider
func (s *Store) UpdateProvider(p *Provider) error {
	p.UpdatedAt = time.Now().UnixMilli()
	_, err := s.DB.NamedExec(
		`UPDATE providers SET name=:name, base_url=:base_url, api_key=:api_key,
		 protocol=:protocol, auth_mode=:auth_mode, priority=:priority, enabled=:enabled,
		 supported_tools=:supported_tools, proxy=:proxy, updated_at=:updated_at
		 WHERE id=:id`,
		p,
	)
	if err != nil {
		return fmt.Errorf("update provider: %w", err)
	}
	return nil
}

// DeleteProvider 删除 Provider（级联删除关联的 model_mappings 和 provider_models）
func (s *Store) DeleteProvider(id int) error {
	_, err := s.DB.Exec("DELETE FROM providers WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete provider: %w", err)
	}
	return nil
}

// --- ModelMapping CRUD ---

// CreateModelMapping 创建模型映射
func (s *Store) CreateModelMapping(m *ModelMapping) error {
	m.CreatedAt = time.Now().UnixMilli()
	result, err := s.DB.NamedExec(
		`INSERT INTO model_mappings (model_name, provider_id, priority, enabled, created_at)
		 VALUES (:model_name, :provider_id, :priority, :enabled, :created_at)`,
		m,
	)
	if err != nil {
		return fmt.Errorf("create model mapping: %w", err)
	}
	id, _ := result.LastInsertId()
	m.ID = int(id)
	return nil
}

// ListModelMappings 列出所有模型映射
func (s *Store) ListModelMappings() ([]ModelMapping, error) {
	var mappings []ModelMapping
	err := s.DB.Select(&mappings, "SELECT * FROM model_mappings ORDER BY model_name, priority ASC")
	if err != nil {
		return nil, fmt.Errorf("list model mappings: %w", err)
	}
	if mappings == nil {
		mappings = []ModelMapping{}
	}
	return mappings, nil
}

// GetPriorityChain 获取指定模型的优先级链（单 SQL JOIN，原子读取）
func (s *Store) GetPriorityChain(modelName string) ([]PriorityChainEntry, error) {
	var entries []PriorityChainEntry
	query := `
		SELECT mm.provider_id, mm.priority
		FROM model_mappings mm
		JOIN providers p ON p.id = mm.provider_id
		WHERE mm.model_name = ? AND mm.enabled = 1 AND p.enabled = 1
		ORDER BY mm.priority ASC
	`
	err := s.DB.Select(&entries, query, modelName)
	if err != nil {
		return nil, fmt.Errorf("get priority chain: %w", err)
	}
	if entries == nil {
		return []PriorityChainEntry{}, nil
	}
	return entries, nil
}

// UpdateModelMapping 更新模型映射
func (s *Store) UpdateModelMapping(m *ModelMapping) error {
	_, err := s.DB.NamedExec(
		`UPDATE model_mappings SET model_name=:model_name, provider_id=:provider_id,
		 priority=:priority, enabled=:enabled WHERE id=:id`,
		m,
	)
	if err != nil {
		return fmt.Errorf("update model mapping: %w", err)
	}
	return nil
}

// DeleteModelMapping 删除模型映射
func (s *Store) DeleteModelMapping(id int) error {
	_, err := s.DB.Exec("DELETE FROM model_mappings WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete model mapping: %w", err)
	}
	return nil
}

// --- Global Settings ---

// GetSetting 获取全局设置
func (s *Store) GetSetting(key string) (string, error) {
	var value string
	err := s.DB.Get(&value, "SELECT value FROM global_settings WHERE key = ?", key)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", nil
		}
		return "", fmt.Errorf("get setting: %w", err)
	}
	return value, nil
}

// SetSetting 设置全局设置
func (s *Store) SetSetting(key, value string) error {
	_, err := s.DB.Exec(
		`INSERT INTO global_settings (key, value, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value, time.Now().UnixMilli(),
	)
	if err != nil {
		return fmt.Errorf("set setting: %w", err)
	}
	return nil
}

// GetProvidersForModel 返回支持指定模型的 provider ID 集合
func (s *Store) GetProvidersForModel(modelName string) (map[int]bool, error) {
	var ids []int
	err := s.DB.Select(&ids, "SELECT DISTINCT provider_id FROM provider_models WHERE model_name = ?", modelName)
	if err != nil {
		return nil, fmt.Errorf("get providers for model: %w", err)
	}
	m := make(map[int]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m, nil
}

// GetProvidersWithAnyModel 返回拥有任意 provider_models 记录的 provider ID 集合
func (s *Store) GetProvidersWithAnyModel() (map[int]bool, error) {
	var ids []int
	err := s.DB.Select(&ids, "SELECT DISTINCT provider_id FROM provider_models")
	if err != nil {
		return nil, fmt.Errorf("get providers with any model: %w", err)
	}
	m := make(map[int]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m, nil
}

// GetDefaultPriorityChain 获取全局默认优先级链
// 支持多种格式：
//
//	ID 新格式: [[101, 102], [103]] — 同组共享优先级（provider ID）
//	名称新格式: [["name1", "name2"], ["name3"]] — 同组共享优先级（provider name）
//	ID 旧格式: [101, 102, 103] — 每个 provider 独立优先级
//	名称旧格式: ["name1", "name2"] — 每个 provider 独立优先级
func (s *Store) GetDefaultPriorityChain() ([]PriorityChainEntry, error) {
	val, err := s.GetSetting("default_priority_chain")
	if err != nil {
		return nil, err
	}
	if val == "" {
		// 无全局默认链，返回所有启用的 Provider 按优先级排序
		providers, err := s.ListEnabledProviders()
		if err != nil {
			return nil, err
		}
		entries := make([]PriorityChainEntry, len(providers))
		for i, p := range providers {
			entries[i] = PriorityChainEntry{ProviderID: p.ID, Priority: p.Priority}
		}
		return entries, nil
	}

	// 尝试新格式（ID 版）[[group1], [group2], ...]
	var groupsInt [][]int
	if err := json.Unmarshal([]byte(val), &groupsInt); err == nil {
		entries := s.chainFromIntGroups(groupsInt)
		if len(entries) > 0 {
			return entries, nil
		}
	}

	// 尝试新格式（名称版）[[name1, name2], [name3], ...]
	var groupsStr [][]string
	if err := json.Unmarshal([]byte(val), &groupsStr); err == nil {
		entries := s.chainFromStrGroups(groupsStr)
		if len(entries) > 0 {
			return entries, nil
		}
	}

	// 回退旧格式 []int
	var providerIDs []int
	if err := json.Unmarshal([]byte(val), &providerIDs); err == nil {
		entries := s.chainFromIntSlice(providerIDs)
		if len(entries) > 0 {
			return entries, nil
		}
	}

	// 回退旧格式 []string
	var providerNames []string
	if err := json.Unmarshal([]byte(val), &providerNames); err == nil {
		entries := s.chainFromStrSlice(providerNames)
		if len(entries) > 0 {
			return entries, nil
		}
	}

	return nil, fmt.Errorf("parse default_priority_chain: unrecognized format")
}

func (s *Store) chainFromIntGroups(groups [][]int) []PriorityChainEntry {
	entries := make([]PriorityChainEntry, 0)
	for gi, group := range groups {
		basePriority := (gi + 1) * 10
		for pi, id := range group {
			p, err := s.GetProvider(id)
			if err != nil || !p.Enabled {
				continue
			}
			entries = append(entries, PriorityChainEntry{
				ProviderID: id,
				Priority:   basePriority + pi,
			})
		}
	}
	return entries
}

func (s *Store) chainFromStrGroups(groups [][]string) []PriorityChainEntry {
	entries := make([]PriorityChainEntry, 0)
	for gi, group := range groups {
		basePriority := (gi + 1) * 10
		for pi, name := range group {
			id, err := s.GetProviderIDByName(name)
			if err != nil {
				continue
			}
			entries = append(entries, PriorityChainEntry{
				ProviderID: id,
				Priority:   basePriority + pi,
			})
		}
	}
	return entries
}

func (s *Store) chainFromIntSlice(ids []int) []PriorityChainEntry {
	entries := make([]PriorityChainEntry, 0, len(ids))
	for i, id := range ids {
		p, err := s.GetProvider(id)
		if err != nil || !p.Enabled {
			continue
		}
		entries = append(entries, PriorityChainEntry{ProviderID: id, Priority: (i + 1) * 10})
	}
	return entries
}

func (s *Store) chainFromStrSlice(names []string) []PriorityChainEntry {
	entries := make([]PriorityChainEntry, 0, len(names))
	for i, name := range names {
		id, err := s.GetProviderIDByName(name)
		if err != nil {
			continue
		}
		entries = append(entries, PriorityChainEntry{ProviderID: id, Priority: (i + 1) * 10})
	}
	return entries
}

// GetProviderIDByName 根据名称查询 Provider ID
func (s *Store) GetProviderIDByName(name string) (int, error) {
	var id int
	err := s.DB.Get(&id, "SELECT id FROM providers WHERE name = ?", name)
	if err != nil {
		if err == sql.ErrNoRows {
			return 0, fmt.Errorf("provider %q not found", name)
		}
		return 0, fmt.Errorf("get provider by name: %w", err)
	}
	return id, nil
}

// --- Provider Models ---

// AddProviderModel 添加自动发现的模型
func (s *Store) AddProviderModel(providerID int, modelID, modelName string) error {
	_, err := s.DB.Exec(
		`INSERT OR IGNORE INTO provider_models (provider_id, model_id, model_name) VALUES (?, ?, ?)`,
		providerID, modelID, modelName,
	)
	return err
}

// GetProviderModels 获取某 Provider 的模型列表
func (s *Store) GetProviderModels(providerID int) ([]struct {
	ID        int    `db:"id" json:"id"`
	ModelID   string `db:"model_id" json:"model_id"`
	ModelName string `db:"model_name" json:"model_name"`
}, error) {
	var models []struct {
		ID        int    `db:"id" json:"id"`
		ModelID   string `db:"model_id" json:"model_id"`
		ModelName string `db:"model_name" json:"model_name"`
	}
	err := s.DB.Select(&models, "SELECT id, model_id, model_name FROM provider_models WHERE provider_id = ?", providerID)
	if err != nil {
		return nil, err
	}
	if models == nil {
		models = []struct {
			ID        int    `db:"id" json:"id"`
			ModelID   string `db:"model_id" json:"model_id"`
			ModelName string `db:"model_name" json:"model_name"`
		}{}
	}
	return models, nil
}

// User represents an admin panel user with role-based access.
type User struct {
	ID           int64  `db:"id"            json:"id"`
	Username     string `db:"username"      json:"username"`
	PasswordHash string `db:"password_hash" json:"-"`
	Role         string `db:"role"          json:"role"`
	Enabled      bool   `db:"enabled"       json:"enabled"`
	CreatedAt    int64  `db:"created_at"    json:"created_at"`
}

// APIKey 下游客户端 API 密钥
type APIKey struct {
	ID          int    `db:"id" json:"id"`
	Key         string `db:"key" json:"key"`
	Name        string `db:"name" json:"name"`
	Enabled     bool   `db:"enabled" json:"enabled"`
	Permissions string `db:"permissions" json:"permissions"`
	ExpiresAt   *int64 `db:"expires_at" json:"-"`
	RateLimit   *int   `db:"rate_limit" json:"-"`
	CreatedAt   int64  `db:"created_at" json:"created_at"`
}

// APIKeyPermissions defines the parsed permissions JSON structure.
type APIKeyPermissions struct {
	AllowedModels    []string `json:"allowed_models,omitempty"`
	AllowedProviders []string `json:"allowed_providers,omitempty"`
}

// ParsePermissions parses the permissions JSON string into APIKeyPermissions.
func (k *APIKey) ParsePermissions() *APIKeyPermissions {
	if k.Permissions == "" || k.Permissions == "{}" {
		return nil
	}
	var p APIKeyPermissions
	if err := json.Unmarshal([]byte(k.Permissions), &p); err != nil {
		return nil
	}
	// Return nil if both lists are empty (equivalent to no restrictions)
	if len(p.AllowedModels) == 0 && len(p.AllowedProviders) == 0 {
		return nil
	}
	return &p
}

// IsModelAllowed checks if a model is permitted by this key's restrictions.
// Returns true if there are no model restrictions or the model is in the allowlist.
func (k *APIKey) IsModelAllowed(model string) bool {
	p := k.ParsePermissions()
	if p == nil || len(p.AllowedModels) == 0 {
		return true
	}
	for _, m := range p.AllowedModels {
		if m == model {
			return true
		}
	}
	return false
}

// IsProviderAllowed checks if a provider is permitted by this key's restrictions.
// Returns true if there are no provider restrictions or the provider is in the allowlist.
func (k *APIKey) IsProviderAllowed(provider string) bool {
	p := k.ParsePermissions()
	if p == nil || len(p.AllowedProviders) == 0 {
		return true
	}
	for _, pr := range p.AllowedProviders {
		if pr == provider {
			return true
		}
	}
	return false
}

// CreateAPIKey 创建 API Key
func (s *Store) CreateAPIKey(name string) (*APIKey, error) {
	key := "sk-" + randomHex(48)

	now := time.Now().UnixMilli()
	result, err := s.DB.Exec(
		`INSERT INTO api_keys (key, name, enabled, permissions, created_at)
		 VALUES (?, ?, 1, '{}', ?)`,
		key, name, now,
	)
	if err != nil {
		return nil, fmt.Errorf("create api key: %w", err)
	}
	id, _ := result.LastInsertId()

	return &APIKey{
		ID:        int(id),
		Key:       key,
		Name:      name,
		Enabled:   true,
		CreatedAt: now,
	}, nil
}

// ListAPIKeys 列出所有 API Key（按时间倒序）
func (s *Store) ListAPIKeys() ([]APIKey, error) {
	var keys []APIKey
	err := s.DB.Select(&keys, "SELECT * FROM api_keys ORDER BY created_at DESC")
	if err != nil {
		return nil, fmt.Errorf("list api keys: %w", err)
	}
	if keys == nil {
		keys = []APIKey{}
	}
	return keys, nil
}

// RevokeAPIKey 吊销 API Key
func (s *Store) RevokeAPIKey(id int) error {
	_, err := s.DB.Exec("UPDATE api_keys SET enabled = 0 WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("revoke api key: %w", err)
	}
	return nil
}

// GetAPIKey 获取单个 API Key（不含脱敏）
func (s *Store) GetAPIKey(id int) (*APIKey, error) {
	var k APIKey
	err := s.DB.Get(&k, "SELECT * FROM api_keys WHERE id = ?", id)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("api key %d not found", id)
		}
		return nil, fmt.Errorf("get api key: %w", err)
	}
	return &k, nil
}

// UpdateAPIKey 更新 API Key 的 name、enabled 和 permissions
func (s *Store) UpdateAPIKey(id int, name string, enabled bool, permissions string) error {
	result, err := s.DB.Exec(
		"UPDATE api_keys SET name = ?, enabled = ?, permissions = ? WHERE id = ?",
		name, enabled, permissions, id,
	)
	if err != nil {
		return fmt.Errorf("update api key: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("api key %d not found", id)
	}
	return nil
}

// ValidateAPIKey 验证 API Key
func (s *Store) ValidateAPIKey(key string) (*APIKey, error) {
	var k APIKey
	err := s.DB.Get(&k, "SELECT * FROM api_keys WHERE key = ? AND enabled = 1", key)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("invalid api key")
		}
		return nil, fmt.Errorf("validate api key: %w", err)
	}
	return &k, nil
}

// --- User CRUD ---

func (s *Store) CreateUser(username, password, role string) (int64, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return 0, fmt.Errorf("hash password: %w", err)
	}
	result, err := s.DB.Exec(
		"INSERT INTO users (username, password_hash, role, created_at) VALUES (?, ?, ?, ?)",
		username, string(hash), role, time.Now().UnixMilli(),
	)
	if err != nil {
		return 0, fmt.Errorf("create user: %w", err)
	}
	id, _ := result.LastInsertId()
	return id, nil
}

func (s *Store) GetUser(id int64) (*User, error) {
	var u User
	err := s.DB.Get(&u, "SELECT * FROM users WHERE id = ?", id)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("user %d not found", id)
		}
		return nil, fmt.Errorf("get user: %w", err)
	}
	return &u, nil
}

func (s *Store) GetUserByUsername(username string) (*User, error) {
	var u User
	err := s.DB.Get(&u, "SELECT * FROM users WHERE username = ?", username)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("user %q not found", username)
		}
		return nil, fmt.Errorf("get user by username: %w", err)
	}
	return &u, nil
}

func (s *Store) ListUsers() ([]User, error) {
	var users []User
	err := s.DB.Select(&users, "SELECT * FROM users ORDER BY created_at ASC")
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	if users == nil {
		users = []User{}
	}
	return users, nil
}

func (s *Store) UpdateUser(id int64, username string, enabled bool, role string, password string) error {
	if password != "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			return fmt.Errorf("hash password: %w", err)
		}
		_, err = s.DB.Exec(
			"UPDATE users SET username = ?, enabled = ?, role = ?, password_hash = ? WHERE id = ?",
			username, enabled, role, string(hash), id,
		)
		return err
	}
	result, err := s.DB.Exec(
		"UPDATE users SET username = ?, enabled = ?, role = ? WHERE id = ?",
		username, enabled, role, id,
	)
	if err != nil {
		return fmt.Errorf("update user: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("user %d not found", id)
	}
	return nil
}

func (s *Store) DeleteUser(id int64) error {
	_, err := s.DB.Exec("DELETE FROM users WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	return nil
}

func (s *Store) ValidateUserPassword(username, password string) (*User, error) {
	u, err := s.GetUserByUsername(username)
	if err != nil {
		return nil, err
	}
	if !u.Enabled {
		return nil, fmt.Errorf("user %q is disabled", username)
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) != nil {
		return nil, fmt.Errorf("invalid password")
	}
	return u, nil
}

func (s *Store) CountUsers() (int, error) {
	var count int
	err := s.DB.Get(&count, "SELECT COUNT(*) FROM users")
	return count, err
}

func (s *Store) GetProviderAPIKey(id int) (string, error) {
	var apiKey string
	err := s.DB.Get(&apiKey, "SELECT api_key FROM providers WHERE id = ?", id)
	if err != nil {
		return "", fmt.Errorf("get provider api key: %w", err)
	}
	return apiKey, nil
}

func (s *Store) UpdateProviderAPIKey(id int, apiKey string) error {
	_, err := s.DB.Exec("UPDATE providers SET api_key = ?, updated_at = ? WHERE id = ?", apiKey, time.Now().UnixMilli(), id)
	if err != nil {
		return fmt.Errorf("update provider api key: %w", err)
	}
	return nil
}

func (s *Store) UpdateProviderAPIKeyIfCurrent(id int, currentAPIKey string, newAPIKey string) (bool, error) {
	result, err := s.DB.Exec(
		"UPDATE providers SET api_key = ?, updated_at = ? WHERE id = ? AND api_key = ?",
		newAPIKey, time.Now().UnixMilli(), id, currentAPIKey,
	)
	if err != nil {
		return false, fmt.Errorf("update provider api key: %w", err)
	}
	n, _ := result.RowsAffected()
	return n > 0, nil
}

// SaveProviderQuota 将配额数据存入 provider 的 api_key JSON 中的 tokens.quota 字段。
// 兼容新旧两种格式：{"tokens":{...}} 和 {"credential": {"tokens":{...}}}。
func (s *Store) SaveProviderQuota(id int, quotaJSON []byte) error {
	var quotaObj interface{}
	if err := json.Unmarshal(quotaJSON, &quotaObj); err != nil {
		return fmt.Errorf("unmarshal quota: %w", err)
	}

	for attempt := 0; attempt < 5; attempt++ {
		apiKey, err := s.GetProviderAPIKey(id)
		if err != nil {
			return fmt.Errorf("get api_key: %w", err)
		}
		var creds map[string]interface{}
		if err := json.Unmarshal([]byte(apiKey), &creds); err != nil {
			return nil
		}

		tokens := resolveTokensMap(creds)
		if tokens == nil {
			tokens = make(map[string]interface{})
			credential, ok := creds["credential"].(map[string]interface{})
			if ok {
				credential["tokens"] = tokens
			} else {
				creds["credential"] = map[string]interface{}{"tokens": tokens}
			}
		}
		tokens["quota"] = quotaObj

		newAPIKey, err := json.Marshal(creds)
		if err != nil {
			return fmt.Errorf("marshal credential: %w", err)
		}
		updated, err := s.UpdateProviderAPIKeyIfCurrent(id, apiKey, string(newAPIKey))
		if err != nil {
			return err
		}
		if updated {
			return nil
		}
	}
	return fmt.Errorf("provider api key changed while saving quota")
}

func resolveTokensMap(creds map[string]interface{}) map[string]interface{} {
	if t, ok := creds["tokens"].(map[string]interface{}); ok {
		return t
	}
	if cr, ok := creds["credential"].(map[string]interface{}); ok {
		if t, ok := cr["tokens"].(map[string]interface{}); ok {
			return t
		}
	}
	return nil
}

func ParseProviderQuota(apiKey string) *json.RawMessage {
	var creds map[string]interface{}
	if err := json.Unmarshal([]byte(apiKey), &creds); err != nil {
		return nil
	}
	tokens := resolveTokensMap(creds)
	if tokens == nil {
		return nil
	}
	q, ok := tokens["quota"]
	if !ok {
		return nil
	}
	b, err := json.Marshal(q)
	if err != nil {
		return nil
	}
	raw := json.RawMessage(b)
	return &raw
}

// --- Site CRUD ---

func (s *Store) CreateSite(site *Site) error {
	now := time.Now().UnixMilli()
	site.CreatedAt = now
	site.UpdatedAt = now
	result, err := s.DB.NamedExec(
		`INSERT INTO sites (name, base_url, protocol, auth_mode, enabled, created_at, updated_at)
		 VALUES (:name, :base_url, :protocol, :auth_mode, :enabled, :created_at, :updated_at)`,
		site,
	)
	if err != nil {
		return fmt.Errorf("create site: %w", err)
	}
	id, _ := result.LastInsertId()
	site.ID = int(id)
	return nil
}

func (s *Store) GetSite(id int) (*Site, error) {
	var site Site
	err := s.DB.Get(&site, "SELECT * FROM sites WHERE id = ?", id)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("site %d not found", id)
		}
		return nil, fmt.Errorf("get site: %w", err)
	}
	return &site, nil
}

func (s *Store) ListSites() ([]Site, error) {
	var sites []Site
	err := s.DB.Select(&sites, "SELECT * FROM sites ORDER BY name ASC")
	if err != nil {
		return nil, fmt.Errorf("list sites: %w", err)
	}
	if sites == nil {
		sites = []Site{}
	}
	return sites, nil
}

func (s *Store) UpdateSite(site *Site) error {
	site.UpdatedAt = time.Now().UnixMilli()
	_, err := s.DB.NamedExec(
		`UPDATE sites SET name=:name, base_url=:base_url, protocol=:protocol,
		 auth_mode=:auth_mode, enabled=:enabled, updated_at=:updated_at
		 WHERE id=:id`,
		site,
	)
	if err != nil {
		return fmt.Errorf("update site: %w", err)
	}
	return nil
}

func (s *Store) DeleteSite(id int) error {
	_, err := s.DB.Exec("DELETE FROM sites WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete site: %w", err)
	}
	return nil
}

// --- Site Models ---

func (s *Store) AddSiteModel(siteID int, modelID, modelName string) error {
	_, err := s.DB.Exec(
		`INSERT OR IGNORE INTO site_models (site_id, model_id, model_name) VALUES (?, ?, ?)`,
		siteID, modelID, modelName,
	)
	return err
}

func (s *Store) GetSiteModels(siteID int) ([]SiteModel, error) {
	var models []SiteModel
	err := s.DB.Select(&models, "SELECT * FROM site_models WHERE site_id = ? ORDER BY id", siteID)
	if err != nil {
		return nil, fmt.Errorf("get site models: %w", err)
	}
	if models == nil {
		models = []SiteModel{}
	}
	return models, nil
}

func (s *Store) DeleteSiteModel(id int) error {
	_, err := s.DB.Exec("DELETE FROM site_models WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete site model: %w", err)
	}
	return nil
}

// --- Transaction helpers ---

func (s *Store) createProviderTx(tx *sqlx.Tx, p *Provider) error {
	now := time.Now().UnixMilli()
	p.CreatedAt = now
	p.UpdatedAt = now
	result, err := tx.NamedExec(
		`INSERT INTO providers (name, base_url, api_key, protocol, auth_mode, priority, enabled, supported_tools, proxy, created_at, updated_at)
		 VALUES (:name, :base_url, :api_key, :protocol, :auth_mode, :priority, :enabled, :supported_tools, :proxy, :created_at, :updated_at)`,
		p,
	)
	if err != nil {
		return fmt.Errorf("create provider tx: %w", err)
	}
	id, _ := result.LastInsertId()
	p.ID = int(id)
	return nil
}

func (s *Store) createModelMappingTx(tx *sqlx.Tx, m *ModelMapping) error {
	m.CreatedAt = time.Now().UnixMilli()
	result, err := tx.NamedExec(
		`INSERT INTO model_mappings (model_name, provider_id, priority, enabled, created_at)
		 VALUES (:model_name, :provider_id, :priority, :enabled, :created_at)`,
		m,
	)
	if err != nil {
		return fmt.Errorf("create model mapping tx: %w", err)
	}
	id, _ := result.LastInsertId()
	m.ID = int(id)
	return nil
}

// --- Create Provider from Site ---

func (s *Store) CreateProviderFromSite(siteID int, nameOverride string, apiKey string, oauthJSON json.RawMessage) (*Provider, int, error) {
	site, err := s.GetSite(siteID)
	if err != nil {
		return nil, 0, fmt.Errorf("get site: %w", err)
	}

	name := site.Name
	if nameOverride != "" {
		name = nameOverride
	}

	var credential string
	switch site.AuthMode {
	case "api_key":
		credential = apiKey
	case "oauth":
		if len(oauthJSON) > 0 {
			credential = string(oauthJSON)
		}
	default:
		credential = apiKey
	}

	provider := &Provider{
		Name:     name,
		BaseURL:  site.BaseURL,
		APIKey:   credential,
		Protocol: site.Protocol,
		AuthMode: site.AuthMode,
		Priority: 100,
		Enabled:  true,
	}

	siteModels, err := s.GetSiteModels(siteID)
	if err != nil {
		return nil, 0, fmt.Errorf("get site models: %w", err)
	}

	tx, err := s.DB.Beginx()
	if err != nil {
		return nil, 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	if err := s.createProviderTx(tx, provider); err != nil {
		return nil, 0, err
	}

	mappingsCreated := 0
	for _, sm := range siteModels {
		m := &ModelMapping{
			ModelName:  sm.ModelID,
			ProviderID: provider.ID,
			Priority:   100,
			Enabled:    true,
		}
		if err := s.createModelMappingTx(tx, m); err != nil {
			return nil, 0, fmt.Errorf("create mapping for %s: %w", sm.ModelID, err)
		}
		mappingsCreated++
	}

	if err := tx.Commit(); err != nil {
		return nil, 0, fmt.Errorf("commit tx: %w", err)
	}

	return provider, mappingsCreated, nil
}

// --- Seed Built-in Sites ---

func (s *Store) SeedBuiltinSites() error {
	builtinSites := []struct {
		Site   Site
		Models []struct{ ModelID, ModelName string }
	}{
		{
			Site: Site{Name: "DeepSeek 官方", BaseURL: "https://api.deepseek.com", Protocol: "openai", AuthMode: "api_key", Enabled: true},
			Models: []struct{ ModelID, ModelName string }{
				{"deepseek-chat", "deepseek-chat"},
				{"deepseek-reasoner", "deepseek-reasoner"},
			},
		},
		{
			Site: Site{Name: "OpenAI 官方", BaseURL: "https://api.openai.com", Protocol: "openai", AuthMode: "api_key", Enabled: true},
			Models: []struct{ ModelID, ModelName string }{
				{"gpt-4o", "gpt-4o"},
				{"gpt-4o-mini", "gpt-4o-mini"},
				{"gpt-4-turbo", "gpt-4-turbo"},
			},
		},
		{
			Site: Site{Name: "Anthropic 官方", BaseURL: "https://api.anthropic.com", Protocol: "anthropic", AuthMode: "api_key", Enabled: true},
			Models: []struct{ ModelID, ModelName string }{
				{"claude-sonnet-4-6", "claude-sonnet-4-6"},
				{"claude-opus-4-7", "claude-opus-4-7"},
				{"claude-haiku-4-5", "claude-haiku-4-5"},
			},
		},
		{
			Site: Site{Name: "Codex (ChatGPT)", BaseURL: "https://chatgpt.com/backend-api/codex", Protocol: "openai", AuthMode: "oauth", Enabled: true},
			Models: []struct{ ModelID, ModelName string }{
				{"gpt-4o", "gpt-4o"},
				{"gpt-4o-mini", "gpt-4o-mini"},
			},
		},
		{
			Site: Site{Name: "opencode go 订阅", BaseURL: "https://opencode.ai/zen/go/v1", Protocol: "openai", AuthMode: "api_key", Enabled: true},
			Models: []struct{ ModelID, ModelName string }{
				{"deepseek-v4-flash", "deepseek-v4-flash"},
				{"deepseek-v4-pro", "deepseek-v4-pro"},
				{"glm-5.1", "glm-5.1"},
				{"kimi-k2.6", "kimi-k2.6"},
				{"mimo-v2.5-pro", "mimo-v2.5-pro"},
				{"minimax-m2.7", "minimax-m2.7"},
				{"qwen3.5-plus", "qwen3.5-plus"},
			},
		},
	}

	for _, bs := range builtinSites {
		now := time.Now().UnixMilli()
		bs.Site.CreatedAt = now
		bs.Site.UpdatedAt = now

		_, err := s.DB.NamedExec(
			`INSERT INTO sites (name, base_url, protocol, auth_mode, enabled, created_at, updated_at)
			 VALUES (:name, :base_url, :protocol, :auth_mode, :enabled, :created_at, :updated_at)
			 ON CONFLICT(name) DO UPDATE SET
			   base_url=excluded.base_url,
			   protocol=excluded.protocol,
			   auth_mode=excluded.auth_mode,
			   updated_at=excluded.updated_at`,
			&bs.Site,
		)
		if err != nil {
			return fmt.Errorf("seed site %s: %w", bs.Site.Name, err)
		}

		if bs.Site.ID == 0 {
			var siteID int
			if err := s.DB.Get(&siteID, "SELECT id FROM sites WHERE name = ?", bs.Site.Name); err != nil {
				return fmt.Errorf("get site id for %s: %w", bs.Site.Name, err)
			}
			bs.Site.ID = siteID
		}

		_, err = s.DB.Exec("DELETE FROM site_models WHERE site_id = ?", bs.Site.ID)
		if err != nil {
			return fmt.Errorf("delete old models for %s: %w", bs.Site.Name, err)
		}

		for _, m := range bs.Models {
			_, err := s.DB.Exec("INSERT INTO site_models (site_id, model_id, model_name) VALUES (?, ?, ?)",
				bs.Site.ID, m.ModelID, m.ModelName)
			if err != nil {
				return fmt.Errorf("seed model %s for %s: %w", m.ModelID, bs.Site.Name, err)
			}
		}
	}

	return nil
}

// --- Access Logs ---

// AttemptRecord 单次 provider 尝试记录（成功或失败），存入 access log
type AttemptRecord struct {
	Provider    string `json:"provider"`
	StatusCode  int    `json:"status_code"`
	Error       string `json:"error,omitempty"`
	DurationMs  int64  `json:"duration_ms"`
	QuotaBefore string `json:"quota_before,omitempty"`
	QuotaAfter  string `json:"quota_after,omitempty"`
	Success     bool   `json:"success"`
	AttemptNum  int    `json:"attempt_num"`
}

// AccessLog API 访问日志
type AccessLog struct {
	ID           int    `db:"id" json:"id"`
	Timestamp    int64  `db:"timestamp" json:"timestamp"`
	ApiKeyID     *int   `db:"api_key_id" json:"api_key_id"`
	ApiKeyName   string `db:"api_key_name" json:"api_key_name"`
	Method       string `db:"method" json:"method"`
	Path         string `db:"path" json:"path"`
	Model        string `db:"model" json:"model"`
	StatusCode   int    `db:"status_code" json:"status_code"`
	TokensIn     int    `db:"tokens_in" json:"tokens_in"`
	TokensOut    int    `db:"tokens_out" json:"tokens_out"`
	DurationMs   int    `db:"duration_ms" json:"duration_ms"`
	RemoteIP     string `db:"remote_ip" json:"remote_ip"`
	RequestID    string `db:"request_id" json:"request_id"`
	ProviderName string `db:"provider_name" json:"provider_name"`
	ErrorMsg     string `db:"error_msg" json:"error_msg"`
	RawBody      string `db:"raw_body" json:"raw_body"`
	RawResponse  string `db:"raw_response" json:"raw_response"`
	ClientReq    string `db:"client_req" json:"client_req"`
	ClientResp   string `db:"client_resp" json:"client_resp"`
	UpstreamReq  string `db:"upstream_req" json:"upstream_req"`
	UpstreamResp string `db:"upstream_resp" json:"upstream_resp"`
	QuotaBefore  string `db:"quota_before" json:"quota_before"`
	QuotaAfter   string `db:"quota_after" json:"quota_after"`
	AttemptsJSON string `db:"attempts_json" json:"attempts_json,omitempty"`
}

// AccessLogQuery 访问日志查询参数
type AccessLogQuery struct {
	ApiKeyID *int   `json:"api_key_id,omitempty"`
	Model    string `json:"model,omitempty"`
	Status   *int   `json:"status,omitempty"`
	StartAt  *int64 `json:"start_at,omitempty"`
	EndAt    *int64 `json:"end_at,omitempty"`
	Page     int    `json:"page"`
	PerPage  int    `json:"per_page"`
}

// BucketStat 时间桶访问日志统计
type BucketStat struct {
	Start                    int64 `json:"start"`
	End                      int64 `json:"end"`
	CountWithFailover        int   `json:"count_with_failover"`
	CountWithoutFailover     int   `json:"count_without_failover"`
	TokensInWithFailover     int   `json:"tokens_in_with_failover"`
	TokensInWithoutFailover  int   `json:"tokens_in_without_failover"`
	TokensOutWithFailover    int   `json:"tokens_out_with_failover"`
	TokensOutWithoutFailover int   `json:"tokens_out_without_failover"`
}

// InsertAccessLog 插入访问日志（委托给 LogStore，如未配置则跳过）
func (s *Store) InsertAccessLog(log *AccessLog) error {
	if s.LogStore == nil {
		return nil
	}
	return s.LogStore.Insert(log)
}

// QueryAccessLogs 查询访问日志（分页）
func (s *Store) QueryAccessLogs(q AccessLogQuery) ([]AccessLog, int, error) {
	if s.LogStore == nil {
		return []AccessLog{}, 0, nil
	}
	return s.LogStore.Query(q)
}

// CleanupOldLogs 清理保留天数之前的日志，返回删除行数
func (s *Store) CleanupOldLogs(retentionDays int) (int64, error) {
	if s.LogStore == nil {
		return 0, nil
	}
	return s.LogStore.Cleanup(retentionDays)
}

func (s *Store) GetAccessLog(id int) (*AccessLog, error) {
	if s.LogStore == nil {
		return nil, fmt.Errorf("access log %d not found", id)
	}
	return s.LogStore.Get(id)
}

func (s *Store) GetAccessLogStats(startAt, endAt int64, intervalMinutes int) ([]BucketStat, error) {
	if s.LogStore == nil {
		return []BucketStat{}, nil
	}
	return s.LogStore.Stats(startAt, endAt, intervalMinutes)
}

// StartCleanup 启动定时清理任务
func (s *Store) StartCleanup(ctx context.Context, interval time.Duration, retentionDays int) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				n, err := s.CleanupOldLogs(retentionDays)
				if err != nil {
					slog.Error("access_log_cleanup_failed", "err", err)
				} else if n > 0 {
					slog.Info("access_log_cleanup", "deleted", n)
				}
			case <-ctx.Done():
				return
			}
		}
	}()
}

func randomHex(n int) string {
	b := make([]byte, n/2)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}
