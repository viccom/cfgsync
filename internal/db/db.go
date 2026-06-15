// Package db owns the SQLite connection and schema migrations.
package db

import (
	"database/sql"
	_ "embed"
	"fmt"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

// Open opens (or creates) the SQLite database at path and applies WAL mode.
func Open(path string) (*sql.DB, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)", path)
	d, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	if err := d.Ping(); err != nil {
		_ = d.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	d.SetMaxOpenConns(1) // single writer; WAL still allows concurrent readers via SetMaxIdleConns
	return d, nil
}

// Migrate creates tables and bumps schema_version to the latest.
// Add new migrations to the migrations slice; do NOT mutate old entries.
func Migrate(d *sql.DB) error {
	if _, err := d.Exec(schemaSQL); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	if _, err := d.Exec(
		`INSERT OR IGNORE INTO schema_version (version, applied_at) VALUES (1, strftime('%s','now'))`,
	); err != nil {
		return fmt.Errorf("record version: %w", err)
	}
	return nil
}