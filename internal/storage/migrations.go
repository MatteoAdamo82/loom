package storage

import (
	"database/sql"
	_ "embed"
	"fmt"
)

//go:embed schema.sql
var schemaSQL string

const currentSchemaVersion = 2

func migrate(db *sql.DB) error {
	var prior int
	row := db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_version`)
	if err := row.Scan(&prior); err != nil {
		prior = 0
	}

	if prior > 0 && prior < currentSchemaVersion {
		if err := dropLegacyObjects(db); err != nil {
			return fmt.Errorf("drop legacy objects: %w", err)
		}
	}

	if _, err := db.Exec(schemaSQL); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}

	if prior < currentSchemaVersion {
		if _, err := db.Exec(
			`INSERT OR IGNORE INTO schema_version (version) VALUES (?)`,
			currentSchemaVersion,
		); err != nil {
			return fmt.Errorf("record schema_version: %w", err)
		}
	}
	return nil
}

func dropLegacyObjects(db *sql.DB) error {
	stmts := []string{
		`DROP TABLE IF EXISTS aliases`,
		`DROP TABLE IF EXISTS note_tags`,
		`DROP TABLE IF EXISTS tags`,
		`DROP TABLE IF EXISTS operations`,
		`DROP TABLE IF EXISTS chunks`,
		`DROP TABLE IF EXISTS links`,
		`DROP TABLE IF EXISTS note_versions`,
		`DROP TABLE IF EXISTS notes`,
		`DROP TABLE IF EXISTS sources`,
		`DROP TABLE IF EXISTS search_index`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return fmt.Errorf("%s: %w", s, err)
		}
	}
	return nil
}
