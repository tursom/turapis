-- 008: Store internal timestamps as Unix milliseconds.

PRAGMA foreign_keys=OFF;
BEGIN;

CREATE TABLE providers_new (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    name            TEXT NOT NULL,
    base_url        TEXT NOT NULL,
    api_key         TEXT NOT NULL,
    protocol        TEXT NOT NULL CHECK(protocol IN ('openai', 'anthropic')),
    auth_mode       TEXT NOT NULL DEFAULT 'api_key',
    priority        INTEGER NOT NULL DEFAULT 100,
    enabled         INTEGER NOT NULL DEFAULT 1,
    supported_tools TEXT NOT NULL DEFAULT '["web_search"]',
    proxy           TEXT NOT NULL DEFAULT '',
    created_at      INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER) * 1000),
    updated_at      INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER) * 1000)
);
INSERT INTO providers_new
SELECT
    id, name, base_url, api_key, protocol, auth_mode, priority, enabled, supported_tools, proxy,
    CASE
        WHEN typeof(created_at) = 'integer' THEN created_at
        WHEN trim(coalesce(created_at, '')) != '' AND trim(created_at) NOT GLOB '*[^0-9]*'
            THEN CASE WHEN CAST(trim(created_at) AS INTEGER) < 100000000000 THEN CAST(trim(created_at) AS INTEGER) * 1000 ELSE CAST(trim(created_at) AS INTEGER) END
        WHEN strftime('%s', created_at) IS NOT NULL THEN CAST(strftime('%s', created_at) AS INTEGER) * 1000
        ELSE CAST(strftime('%s','now') AS INTEGER) * 1000
    END,
    CASE
        WHEN typeof(updated_at) = 'integer' THEN updated_at
        WHEN trim(coalesce(updated_at, '')) != '' AND trim(updated_at) NOT GLOB '*[^0-9]*'
            THEN CASE WHEN CAST(trim(updated_at) AS INTEGER) < 100000000000 THEN CAST(trim(updated_at) AS INTEGER) * 1000 ELSE CAST(trim(updated_at) AS INTEGER) END
        WHEN strftime('%s', updated_at) IS NOT NULL THEN CAST(strftime('%s', updated_at) AS INTEGER) * 1000
        ELSE CAST(strftime('%s','now') AS INTEGER) * 1000
    END
FROM providers;
DROP TABLE providers;
ALTER TABLE providers_new RENAME TO providers;
CREATE UNIQUE INDEX IF NOT EXISTS idx_providers_name ON providers(name);

CREATE TABLE model_mappings_new (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    model_name  TEXT NOT NULL,
    provider_id INTEGER NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
    priority    INTEGER NOT NULL DEFAULT 100,
    enabled     INTEGER NOT NULL DEFAULT 1,
    created_at  INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER) * 1000),
    UNIQUE(model_name, provider_id)
);
INSERT INTO model_mappings_new
SELECT
    id, model_name, provider_id, priority, enabled,
    CASE
        WHEN typeof(created_at) = 'integer' THEN created_at
        WHEN trim(coalesce(created_at, '')) != '' AND trim(created_at) NOT GLOB '*[^0-9]*'
            THEN CASE WHEN CAST(trim(created_at) AS INTEGER) < 100000000000 THEN CAST(trim(created_at) AS INTEGER) * 1000 ELSE CAST(trim(created_at) AS INTEGER) END
        WHEN strftime('%s', created_at) IS NOT NULL THEN CAST(strftime('%s', created_at) AS INTEGER) * 1000
        ELSE CAST(strftime('%s','now') AS INTEGER) * 1000
    END
FROM model_mappings;
DROP TABLE model_mappings;
ALTER TABLE model_mappings_new RENAME TO model_mappings;

CREATE TABLE api_keys_new (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    key         TEXT NOT NULL UNIQUE,
    name        TEXT NOT NULL DEFAULT '',
    enabled     INTEGER NOT NULL DEFAULT 1,
    permissions TEXT NOT NULL DEFAULT '{}',
    expires_at  INTEGER DEFAULT NULL,
    rate_limit  INTEGER DEFAULT NULL,
    created_at  INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER) * 1000)
);
INSERT INTO api_keys_new
SELECT
    id, key, name, enabled, permissions,
    CASE
        WHEN expires_at IS NULL THEN NULL
        WHEN typeof(expires_at) = 'integer' THEN expires_at
        WHEN trim(coalesce(expires_at, '')) != '' AND trim(expires_at) NOT GLOB '*[^0-9]*'
            THEN CASE WHEN CAST(trim(expires_at) AS INTEGER) < 100000000000 THEN CAST(trim(expires_at) AS INTEGER) * 1000 ELSE CAST(trim(expires_at) AS INTEGER) END
        WHEN strftime('%s', expires_at) IS NOT NULL THEN CAST(strftime('%s', expires_at) AS INTEGER) * 1000
        ELSE NULL
    END,
    rate_limit,
    CASE
        WHEN typeof(created_at) = 'integer' THEN created_at
        WHEN trim(coalesce(created_at, '')) != '' AND trim(created_at) NOT GLOB '*[^0-9]*'
            THEN CASE WHEN CAST(trim(created_at) AS INTEGER) < 100000000000 THEN CAST(trim(created_at) AS INTEGER) * 1000 ELSE CAST(trim(created_at) AS INTEGER) END
        WHEN strftime('%s', created_at) IS NOT NULL THEN CAST(strftime('%s', created_at) AS INTEGER) * 1000
        ELSE CAST(strftime('%s','now') AS INTEGER) * 1000
    END
FROM api_keys;
DROP TABLE api_keys;
ALTER TABLE api_keys_new RENAME TO api_keys;

CREATE TABLE sites_new (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT NOT NULL UNIQUE,
    base_url   TEXT NOT NULL,
    protocol   TEXT NOT NULL CHECK(protocol IN ('openai', 'anthropic')),
    auth_mode  TEXT NOT NULL DEFAULT 'api_key',
    enabled    INTEGER NOT NULL DEFAULT 1,
    created_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER) * 1000),
    updated_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER) * 1000)
);
INSERT INTO sites_new
SELECT
    id, name, base_url, protocol, auth_mode, enabled,
    CASE
        WHEN typeof(created_at) = 'integer' THEN created_at
        WHEN trim(coalesce(created_at, '')) != '' AND trim(created_at) NOT GLOB '*[^0-9]*'
            THEN CASE WHEN CAST(trim(created_at) AS INTEGER) < 100000000000 THEN CAST(trim(created_at) AS INTEGER) * 1000 ELSE CAST(trim(created_at) AS INTEGER) END
        WHEN strftime('%s', created_at) IS NOT NULL THEN CAST(strftime('%s', created_at) AS INTEGER) * 1000
        ELSE CAST(strftime('%s','now') AS INTEGER) * 1000
    END,
    CASE
        WHEN typeof(updated_at) = 'integer' THEN updated_at
        WHEN trim(coalesce(updated_at, '')) != '' AND trim(updated_at) NOT GLOB '*[^0-9]*'
            THEN CASE WHEN CAST(trim(updated_at) AS INTEGER) < 100000000000 THEN CAST(trim(updated_at) AS INTEGER) * 1000 ELSE CAST(trim(updated_at) AS INTEGER) END
        WHEN strftime('%s', updated_at) IS NOT NULL THEN CAST(strftime('%s', updated_at) AS INTEGER) * 1000
        ELSE CAST(strftime('%s','now') AS INTEGER) * 1000
    END
FROM sites;
DROP TABLE sites;
ALTER TABLE sites_new RENAME TO sites;

CREATE TABLE users_new (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    username      TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    role          TEXT NOT NULL DEFAULT 'user' CHECK(role IN ('admin', 'user')),
    enabled       INTEGER NOT NULL DEFAULT 1,
    created_at    INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER) * 1000)
);
INSERT INTO users_new
SELECT
    id, username, password_hash, role, enabled,
    CASE
        WHEN typeof(created_at) = 'integer' THEN created_at
        WHEN trim(coalesce(created_at, '')) != '' AND trim(created_at) NOT GLOB '*[^0-9]*'
            THEN CASE WHEN CAST(trim(created_at) AS INTEGER) < 100000000000 THEN CAST(trim(created_at) AS INTEGER) * 1000 ELSE CAST(trim(created_at) AS INTEGER) END
        WHEN strftime('%s', created_at) IS NOT NULL THEN CAST(strftime('%s', created_at) AS INTEGER) * 1000
        ELSE CAST(strftime('%s','now') AS INTEGER) * 1000
    END
FROM users;
DROP TABLE users;
ALTER TABLE users_new RENAME TO users;
CREATE INDEX IF NOT EXISTS idx_users_username ON users(username);
CREATE INDEX IF NOT EXISTS idx_users_role ON users(role);

CREATE TABLE sessions_new (
    token      TEXT PRIMARY KEY,
    expires_at INTEGER NOT NULL,
    created_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER) * 1000),
    user_id    INTEGER REFERENCES users(id) ON DELETE SET NULL
);
INSERT INTO sessions_new
SELECT
    token,
    CASE
        WHEN typeof(expires_at) = 'integer' THEN expires_at
        WHEN trim(coalesce(expires_at, '')) != '' AND trim(expires_at) NOT GLOB '*[^0-9]*'
            THEN CASE WHEN CAST(trim(expires_at) AS INTEGER) < 100000000000 THEN CAST(trim(expires_at) AS INTEGER) * 1000 ELSE CAST(trim(expires_at) AS INTEGER) END
        WHEN strftime('%s', expires_at) IS NOT NULL THEN CAST(strftime('%s', expires_at) AS INTEGER) * 1000
        ELSE CAST(strftime('%s','now') AS INTEGER) * 1000
    END,
    CASE
        WHEN typeof(created_at) = 'integer' THEN created_at
        WHEN trim(coalesce(created_at, '')) != '' AND trim(created_at) NOT GLOB '*[^0-9]*'
            THEN CASE WHEN CAST(trim(created_at) AS INTEGER) < 100000000000 THEN CAST(trim(created_at) AS INTEGER) * 1000 ELSE CAST(trim(created_at) AS INTEGER) END
        WHEN strftime('%s', created_at) IS NOT NULL THEN CAST(strftime('%s', created_at) AS INTEGER) * 1000
        ELSE CAST(strftime('%s','now') AS INTEGER) * 1000
    END,
    user_id
FROM sessions;
DROP TABLE sessions;
ALTER TABLE sessions_new RENAME TO sessions;
CREATE INDEX IF NOT EXISTS idx_sessions_expires_at ON sessions(expires_at);

CREATE TABLE global_settings_new (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER) * 1000)
);
INSERT INTO global_settings_new
SELECT
    key, value,
    CASE
        WHEN typeof(updated_at) = 'integer' THEN updated_at
        WHEN trim(coalesce(updated_at, '')) != '' AND trim(updated_at) NOT GLOB '*[^0-9]*'
            THEN CASE WHEN CAST(trim(updated_at) AS INTEGER) < 100000000000 THEN CAST(trim(updated_at) AS INTEGER) * 1000 ELSE CAST(trim(updated_at) AS INTEGER) END
        WHEN strftime('%s', updated_at) IS NOT NULL THEN CAST(strftime('%s', updated_at) AS INTEGER) * 1000
        ELSE CAST(strftime('%s','now') AS INTEGER) * 1000
    END
FROM global_settings;
DROP TABLE global_settings;
ALTER TABLE global_settings_new RENAME TO global_settings;

CREATE INDEX IF NOT EXISTS idx_site_models_site_id ON site_models(site_id);
CREATE INDEX IF NOT EXISTS idx_provider_models_provider_id ON provider_models(provider_id);

COMMIT;
PRAGMA foreign_keys=ON;
