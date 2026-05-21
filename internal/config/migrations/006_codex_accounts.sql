-- 006: Codex accounts table for auto-login credential management

CREATE TABLE IF NOT EXISTS codex_accounts (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    provider_id  INTEGER REFERENCES providers(id) ON DELETE SET NULL,
    email        TEXT    NOT NULL,
    account_id   TEXT    NOT NULL UNIQUE,
    user_id      TEXT    NOT NULL,
    plan_type    TEXT    NOT NULL DEFAULT '',
    status       TEXT    NOT NULL DEFAULT 'active' CHECK(status IN ('active','expired','needs_login','error')),
    last_refresh TEXT    NOT NULL DEFAULT '',
    last_health  TEXT    NOT NULL DEFAULT '',
    last_login   TEXT    NOT NULL DEFAULT '',
    error_msg    TEXT    NOT NULL DEFAULT '',
    metadata     TEXT    NOT NULL DEFAULT '{}',
    created_at   TEXT    NOT NULL DEFAULT (datetime('now')),
    updated_at   TEXT    NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_codex_accounts_email       ON codex_accounts(email);
CREATE INDEX IF NOT EXISTS idx_codex_accounts_provider_id ON codex_accounts(provider_id);
CREATE INDEX IF NOT EXISTS idx_codex_accounts_status      ON codex_accounts(status);
CREATE INDEX IF NOT EXISTS idx_codex_accounts_account_id  ON codex_accounts(account_id);
