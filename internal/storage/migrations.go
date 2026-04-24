package storage

import (
	"database/sql"
	_ "embed"
	"fmt"
)

//go:embed schema.sql
var schemaSQL string

const currentSchemaVersion = 1

func migrate(db *sql.DB) error {
	if _, err := db.Exec(schemaSQL); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}

	var version int
	err := db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&version)
	if err != nil {
		return fmt.Errorf("read schema_version: %w", err)
	}

	if version < currentSchemaVersion {
		_, err := db.Exec(
			`INSERT INTO schema_version (version) VALUES (?)`,
			currentSchemaVersion,
		)
		if err != nil {
			return fmt.Errorf("record schema_version: %w", err)
		}
	}
	return nil
}
