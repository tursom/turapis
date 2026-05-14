package config

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	_ "modernc.org/sqlite"
)

// Provider 上游 API 提供者配置
type Provider struct {
	ID        int    `db:"id" json:"id"`
	Name      string `db:"name" json:"name"`
	BaseURL   string `db:"base_url" json:"base_url"`
	APIKey    string `db:"api_key" json:"api_key"`
	Protocol  string `db:"protocol" json:"protocol"`
	AuthMode  string `db:"auth_mode" json:"auth_mode"`
	Priority  int    `db:"priority" json:"priority"`
	Enabled   bool   `db:"enabled" json:"enabled"`
	CreatedAt string `db:"created_at" json:"created_at"`
	UpdatedAt string `db:"updated_at" json:"updated_at"`
}

// Site 站点预设（Provider 模板，不含认证信息）
type Site struct {
	ID        int    `db:"id" json:"id"`
	Name      string `db:"name" json:"name"`
	BaseURL   string `db:"base_url" json:"base_url"`
	Protocol  string `db:"protocol" json:"protocol"`
	AuthMode  string `db:"auth_mode" json:"auth_mode"`
	Enabled   bool   `db:"enabled" json:"enabled"`
	CreatedAt string `db:"created_at" json:"created_at"`
	UpdatedAt string `db:"updated_at" json:"updated_at"`
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
	CreatedAt  string `db:"created_at" json:"created_at"`
}

// PriorityChainEntry 优先级链中的一个条目（Provider + 优先级）
type PriorityChainEntry struct {
	Provider Provider `json:"provider"`
	Priority int      `json:"priority"`
}

// Store SQLite 配置存储
type Store struct {
	DB *sqlx.DB
}

// NewStore 创建新的 Store，初始化数据库和表
func NewStore(dbPath string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_journal_mode=WAL&_busy_timeout=5000", dbPath)
	db, err := sqlx.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

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

	if err := initSchema(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}

	return &Store{DB: db}, nil
}

func initSchema(db *sqlx.DB) error {
	schema := `
	CREATE TABLE IF NOT EXISTS providers (
	    id          INTEGER PRIMARY KEY AUTOINCREMENT,
	    name        TEXT NOT NULL UNIQUE,
	    base_url    TEXT NOT NULL,
	    api_key     TEXT NOT NULL,
	    protocol    TEXT NOT NULL CHECK(protocol IN ('openai', 'anthropic')),
	    auth_mode   TEXT NOT NULL DEFAULT 'api_key',
	    priority    INTEGER NOT NULL DEFAULT 100,
	    enabled     INTEGER NOT NULL DEFAULT 1,
	    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
	    updated_at  TEXT NOT NULL DEFAULT (datetime('now'))
	);

	CREATE TABLE IF NOT EXISTS model_mappings (
	    id          INTEGER PRIMARY KEY AUTOINCREMENT,
	    model_name  TEXT NOT NULL,
	    provider_id INTEGER NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
	    priority    INTEGER NOT NULL DEFAULT 100,
	    enabled     INTEGER NOT NULL DEFAULT 1,
	    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
	    UNIQUE(model_name, provider_id)
	);

	CREATE TABLE IF NOT EXISTS global_settings (
	    key   TEXT PRIMARY KEY,
	    value TEXT NOT NULL
	);

	CREATE TABLE IF NOT EXISTS provider_models (
	    id          INTEGER PRIMARY KEY AUTOINCREMENT,
	    provider_id INTEGER NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
	    model_id    TEXT NOT NULL,
	    model_name  TEXT NOT NULL,
	    UNIQUE(provider_id, model_id)
	);

	CREATE TABLE IF NOT EXISTS api_keys (
	    id              INTEGER PRIMARY KEY AUTOINCREMENT,
	    key             TEXT NOT NULL UNIQUE,
	    name            TEXT NOT NULL DEFAULT '',
	    enabled         INTEGER NOT NULL DEFAULT 1,
	    permissions     TEXT NOT NULL DEFAULT '{}',
	    expires_at      TEXT DEFAULT NULL,
	    rate_limit      INTEGER DEFAULT NULL,
	    created_at      TEXT NOT NULL DEFAULT (datetime('now'))
	);

	CREATE TABLE IF NOT EXISTS sites (
	    id          INTEGER PRIMARY KEY AUTOINCREMENT,
	    name        TEXT NOT NULL UNIQUE,
	    base_url    TEXT NOT NULL,
	    protocol    TEXT NOT NULL CHECK(protocol IN ('openai', 'anthropic')),
	    auth_mode   TEXT NOT NULL DEFAULT 'api_key',
	    enabled     INTEGER NOT NULL DEFAULT 1,
	    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
	    updated_at  TEXT NOT NULL DEFAULT (datetime('now'))
	);

	CREATE TABLE IF NOT EXISTS site_models (
	    id          INTEGER PRIMARY KEY AUTOINCREMENT,
	    site_id     INTEGER NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
	    model_id    TEXT NOT NULL,
	    model_name  TEXT NOT NULL,
	    UNIQUE(site_id, model_id)
	);

	CREATE INDEX IF NOT EXISTS idx_site_models_site_id ON site_models(site_id);
	CREATE INDEX IF NOT EXISTS idx_provider_models_provider_id ON provider_models(provider_id);

	CREATE TABLE IF NOT EXISTS access_logs (
	    id            INTEGER PRIMARY KEY AUTOINCREMENT,
	    timestamp     TEXT NOT NULL DEFAULT (datetime('now')),
	    api_key_id    INTEGER DEFAULT NULL,
	    api_key_name  TEXT NOT NULL DEFAULT '',
	    method        TEXT NOT NULL,
	    path          TEXT NOT NULL,
	    model         TEXT NOT NULL DEFAULT '',
	    status_code   INTEGER NOT NULL DEFAULT 0,
	    tokens_in     INTEGER NOT NULL DEFAULT 0,
	    tokens_out    INTEGER NOT NULL DEFAULT 0,
	    duration_ms   INTEGER NOT NULL DEFAULT 0,
	    remote_ip     TEXT NOT NULL DEFAULT '',
	    request_id    TEXT NOT NULL DEFAULT '',
	    provider_name TEXT NOT NULL DEFAULT '',
	    error_msg     TEXT NOT NULL DEFAULT ''
	);
	CREATE INDEX IF NOT EXISTS idx_access_logs_timestamp ON access_logs(timestamp);
	CREATE INDEX IF NOT EXISTS idx_access_logs_api_key_id ON access_logs(api_key_id);
	`
	_, err := db.Exec(schema)
	return err
}

// Close 关闭数据库连接
func (s *Store) Close() error {
	return s.DB.Close()
}

// --- Provider CRUD ---

// CreateProvider 创建 Provider
func (s *Store) CreateProvider(p *Provider) error {
	now := time.Now().UTC().Format(time.RFC3339)
	p.CreatedAt = now
	p.UpdatedAt = now

	result, err := s.DB.NamedExec(
		`INSERT INTO providers (name, base_url, api_key, protocol, auth_mode, priority, enabled, created_at, updated_at)
		 VALUES (:name, :base_url, :api_key, :protocol, :auth_mode, :priority, :enabled, :created_at, :updated_at)`,
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

// GetProviderByName 按名称获取 Provider
func (s *Store) GetProviderByName(name string) (*Provider, error) {
	var p Provider
	err := s.DB.Get(&p, "SELECT * FROM providers WHERE name = ?", name)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("provider %s not found", name)
		}
		return nil, fmt.Errorf("get provider by name: %w", err)
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
	p.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	_, err := s.DB.NamedExec(
		`UPDATE providers SET name=:name, base_url=:base_url, api_key=:api_key,
		 protocol=:protocol, auth_mode=:auth_mode, priority=:priority, enabled=:enabled, updated_at=:updated_at
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
	m.CreatedAt = time.Now().UTC().Format(time.RFC3339)
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
		SELECT p.id AS "provider.id", p.name AS "provider.name", p.base_url AS "provider.base_url",
		       p.api_key AS "provider.api_key", p.protocol AS "provider.protocol",
		       p.auth_mode AS "provider.auth_mode",
		       p.priority AS "provider.priority", p.enabled AS "provider.enabled",
		       p.created_at AS "provider.created_at", p.updated_at AS "provider.updated_at",
		       mm.priority
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
		`INSERT INTO global_settings (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, value,
	)
	if err != nil {
		return fmt.Errorf("set setting: %w", err)
	}
	return nil
}

// GetDefaultPriorityChain 获取全局默认优先级链
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
			entries[i] = PriorityChainEntry{Provider: p, Priority: p.Priority}
		}
		return entries, nil
	}

	var providerNames []string
	if err := json.Unmarshal([]byte(val), &providerNames); err != nil {
		return nil, fmt.Errorf("parse default_priority_chain: %w", err)
	}

	entries := make([]PriorityChainEntry, 0, len(providerNames))
	for _, name := range providerNames {
		p, err := s.GetProviderByName(name)
		if err != nil {
			continue // skip unavailable providers
		}
		if p.Enabled {
			entries = append(entries, PriorityChainEntry{Provider: *p, Priority: p.Priority})
		}
	}
	return entries, nil
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

// APIKey 下游客户端 API 密钥
type APIKey struct {
	ID          int     `db:"id" json:"id"`
	Key         string  `db:"key" json:"key"`
	Name        string  `db:"name" json:"name"`
	Enabled     bool    `db:"enabled" json:"enabled"`
	Permissions string  `db:"permissions" json:"-"`
	ExpiresAt   *string `db:"expires_at" json:"-"`
	RateLimit   *int    `db:"rate_limit" json:"-"`
	CreatedAt   string  `db:"created_at" json:"created_at"`
}

// CreateAPIKey 创建 API Key
func (s *Store) CreateAPIKey(name string) (*APIKey, error) {
	key := "sk-" + randomHex(48)

	now := time.Now().UTC().Format(time.RFC3339)
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

// --- Site CRUD ---

func (s *Store) CreateSite(site *Site) error {
	now := time.Now().UTC().Format(time.RFC3339)
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
	site.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
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
	now := time.Now().UTC().Format(time.RFC3339)
	p.CreatedAt = now
	p.UpdatedAt = now
	result, err := tx.NamedExec(
		`INSERT INTO providers (name, base_url, api_key, protocol, auth_mode, priority, enabled, created_at, updated_at)
		 VALUES (:name, :base_url, :api_key, :protocol, :auth_mode, :priority, :enabled, :created_at, :updated_at)`,
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
	m.CreatedAt = time.Now().UTC().Format(time.RFC3339)
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
			ModelName:  sm.ModelName,
			ProviderID: provider.ID,
			Priority:   100,
			Enabled:    true,
		}
		if err := s.createModelMappingTx(tx, m); err != nil {
			return nil, 0, fmt.Errorf("create mapping for %s: %w", sm.ModelName, err)
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
			Site: Site{Name: "Codex (ChatGPT)", BaseURL: "https://api.openai.com", Protocol: "openai", AuthMode: "oauth", Enabled: true},
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
		now := time.Now().UTC().Format(time.RFC3339)
		bs.Site.CreatedAt = now
		bs.Site.UpdatedAt = now

		_, err := s.DB.NamedExec(
			`INSERT INTO sites (name, base_url, protocol, auth_mode, enabled, created_at, updated_at)
			 VALUES (:name, :base_url, :protocol, :auth_mode, :enabled, :created_at, :updated_at)
			 ON CONFLICT(name) DO UPDATE SET
			   base_url=excluded.base_url,
			   protocol=excluded.protocol,
			   auth_mode=excluded.auth_mode,
			   updated_at=datetime('now')`,
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

// AccessLog API 访问日志
type AccessLog struct {
	ID           int    `db:"id" json:"id"`
	Timestamp    string `db:"timestamp" json:"timestamp"`
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
}

// AccessLogQuery 访问日志查询参数
type AccessLogQuery struct {
	ApiKeyID *int   `json:"api_key_id,omitempty"`
	Model    string `json:"model,omitempty"`
	Status   *int   `json:"status,omitempty"`
	StartAt  string `json:"start_at,omitempty"`
	EndAt    string `json:"end_at,omitempty"`
	Page     int    `json:"page"`
	PerPage  int    `json:"per_page"`
}

// InsertAccessLog 插入访问日志
func (s *Store) InsertAccessLog(log *AccessLog) error {
	_, err := s.DB.NamedExec(
		`INSERT INTO access_logs (timestamp, api_key_id, api_key_name, method, path, model, status_code, tokens_in, tokens_out, duration_ms, remote_ip, request_id, provider_name, error_msg)
		 VALUES (:timestamp, :api_key_id, :api_key_name, :method, :path, :model, :status_code, :tokens_in, :tokens_out, :duration_ms, :remote_ip, :request_id, :provider_name, :error_msg)`,
		log,
	)
	if err != nil {
		return fmt.Errorf("insert access log: %w", err)
	}
	return nil
}

// QueryAccessLogs 查询访问日志（分页）
func (s *Store) QueryAccessLogs(q AccessLogQuery) ([]AccessLog, int, error) {
	var conditions []string
	var args []interface{}
	argIdx := 1

	if q.ApiKeyID != nil {
		conditions = append(conditions, fmt.Sprintf("api_key_id = $%d", argIdx))
		args = append(args, *q.ApiKeyID)
		argIdx++
	}
	if q.Model != "" {
		conditions = append(conditions, fmt.Sprintf("model = $%d", argIdx))
		args = append(args, q.Model)
		argIdx++
	}
	if q.Status != nil {
		conditions = append(conditions, fmt.Sprintf("status_code = $%d", argIdx))
		args = append(args, *q.Status)
		argIdx++
	}
	if q.StartAt != "" {
		conditions = append(conditions, fmt.Sprintf("timestamp >= $%d", argIdx))
		args = append(args, q.StartAt)
		argIdx++
	}
	if q.EndAt != "" {
		conditions = append(conditions, fmt.Sprintf("timestamp <= $%d", argIdx))
		args = append(args, q.EndAt)
		argIdx++
	}

	where := ""
	if len(conditions) > 0 {
		where = " WHERE " + strings.Join(conditions, " AND ")
	}

	var total int
	countQuery := "SELECT COUNT(*) FROM access_logs" + where
	if err := s.DB.Get(&total, countQuery, args...); err != nil {
		return nil, 0, fmt.Errorf("count access logs: %w", err)
	}

	if q.Page < 1 {
		q.Page = 1
	}
	if q.PerPage < 1 || q.PerPage > 100 {
		q.PerPage = 20
	}
	offset := (q.Page - 1) * q.PerPage

	limitArg := fmt.Sprintf("$%d", argIdx)
	offsetArg := fmt.Sprintf("$%d", argIdx+1)

	selectQuery := "SELECT * FROM access_logs" + where + " ORDER BY id DESC LIMIT " + limitArg + " OFFSET " + offsetArg

	var logs []AccessLog
	if err := s.DB.Select(&logs, selectQuery, append(args, q.PerPage, offset)...); err != nil {
		return nil, 0, fmt.Errorf("query access logs: %w", err)
	}
	if logs == nil {
		logs = []AccessLog{}
	}

	return logs, total, nil
}

// CleanupOldLogs 清理保留天数之前的日志，返回删除行数
func (s *Store) CleanupOldLogs(retentionDays int) (int64, error) {
	result, err := s.DB.Exec(
		"DELETE FROM access_logs WHERE timestamp < datetime('now', ?)",
		fmt.Sprintf("-%d days", retentionDays),
	)
	if err != nil {
		return 0, fmt.Errorf("cleanup old logs: %w", err)
	}
	n, _ := result.RowsAffected()
	return n, nil
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
