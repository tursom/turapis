package config

import (
	"database/sql"
	"encoding/json"
	"fmt"
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
	Priority  int    `db:"priority" json:"priority"`
	Enabled   bool   `db:"enabled" json:"enabled"`
	CreatedAt string `db:"created_at" json:"created_at"`
	UpdatedAt string `db:"updated_at" json:"updated_at"`
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
		`INSERT INTO providers (name, base_url, api_key, protocol, priority, enabled, created_at, updated_at)
		 VALUES (:name, :base_url, :api_key, :protocol, :priority, :enabled, :created_at, :updated_at)`,
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
		 protocol=:protocol, priority=:priority, enabled=:enabled, updated_at=:updated_at
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
