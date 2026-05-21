-- 007: Add UNIQUE constraint on providers.name for rollback safety

CREATE UNIQUE INDEX IF NOT EXISTS idx_providers_name ON providers(name);
