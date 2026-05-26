PRAGMA writable_schema = ON;
UPDATE sqlite_master SET sql = replace(sql, ' CHECK(protocol IN (''openai'', ''anthropic''))', '')
WHERE type = 'table' AND (name = 'providers' OR name = 'sites');
PRAGMA writable_schema = OFF;
PRAGMA schema_version = 9999;
