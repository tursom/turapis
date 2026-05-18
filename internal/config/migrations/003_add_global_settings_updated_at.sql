-- 003: Add updated_at tracking to global_settings

ALTER TABLE global_settings ADD COLUMN updated_at TEXT NOT NULL DEFAULT '';
