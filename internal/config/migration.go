package config

import (
	"database/sql"
	"embed"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/jmoiron/sqlx"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

const schemaVersionKey = "schema_version"

func runMigrations(db *sqlx.DB) error {
	currentVersion, err := readSchemaVersion(db)
	if err != nil {
		return err
	}

	migrations, err := loadMigrations()
	if err != nil {
		return err
	}

	for _, m := range migrations {
		if m.version <= currentVersion {
			continue
		}

		if err := execMigration(db, m); err != nil {
			return err
		}

		if err := setSchemaVersion(db, m.version); err != nil {
			return err
		}
	}

	return nil
}

func readSchemaVersion(db *sqlx.DB) (int, error) {
	var tableExists int
	if err := db.Get(&tableExists,
		"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='global_settings'"); err != nil {
		return 0, fmt.Errorf("check global_settings: %w", err)
	}
	if tableExists == 0 {
		return 0, nil
	}

	var versionStr string
	if err := db.Get(&versionStr,
		"SELECT value FROM global_settings WHERE key = ?", schemaVersionKey); err != nil {
		if err == sql.ErrNoRows {
			return 0, nil
		}
		return 0, fmt.Errorf("read schema_version: %w", err)
	}

	v, err := strconv.Atoi(versionStr)
	if err != nil {
		return 0, fmt.Errorf("parse schema_version %q: %w", versionStr, err)
	}
	return v, nil
}

type migration struct {
	version int
	name    string
}

func loadMigrations() ([]migration, error) {
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return nil, fmt.Errorf("read migrations dir: %w", err)
	}

	var migrations []migration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		parts := strings.SplitN(e.Name(), "_", 2)
		if len(parts) < 2 {
			continue
		}
		v, err := strconv.Atoi(parts[0])
		if err != nil {
			continue
		}
		migrations = append(migrations, migration{version: v, name: e.Name()})
	}

	sort.Slice(migrations, func(i, j int) bool {
		return migrations[i].version < migrations[j].version
	})
	return migrations, nil
}

func execMigration(db *sqlx.DB, m migration) error {
	sqlBytes, err := migrationsFS.ReadFile("migrations/" + m.name)
	if err != nil {
		return fmt.Errorf("read migration %s: %w", m.name, err)
	}

	_, err = db.Exec(string(sqlBytes))
	if err != nil {
		if isColumnExistsError(err) {
			return nil
		}
		return fmt.Errorf("run migration %03d (%s): %w", m.version, m.name, err)
	}
	return nil
}

func isColumnExistsError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "duplicate column name") ||
		strings.Contains(msg, "already exists") ||
		strings.Contains(msg, "no such table")
}

func setSchemaVersion(db *sqlx.DB, version int) error {
	_, err := db.Exec(
		"INSERT OR REPLACE INTO global_settings (key, value) VALUES (?, ?)",
		schemaVersionKey, strconv.Itoa(version),
	)
	if err != nil {
		return fmt.Errorf("update schema_version to %d: %w", version, err)
	}
	return nil
}
