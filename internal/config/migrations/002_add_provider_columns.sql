-- 002: Add supported_tools and proxy to providers

ALTER TABLE providers ADD COLUMN supported_tools TEXT NOT NULL DEFAULT '["web_search"]';
ALTER TABLE providers ADD COLUMN proxy TEXT NOT NULL DEFAULT '';
