// Package db owns the SQLite connection and schema migrations.
package db

import (
	"database/sql"
	_ "embed"
	"fmt"
	"time"

	"github.com/1remote/1remote-cloud/internal/auth"
	"github.com/1remote/1remote-cloud/internal/config"

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
	d.SetMaxOpenConns(1)
	return d, nil
}

// Migrate creates tables and bumps schema_version to the latest.
func Migrate(d *sql.DB) error {
	if _, err := d.Exec(schemaSQL); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	if _, err := d.Exec(
		`INSERT OR IGNORE INTO schema_version (version, applied_at) VALUES (2, ?)`,
		time.Now().Unix(),
	); err != nil {
		return fmt.Errorf("record version: %w", err)
	}
	return nil
}

// BootstrapAdmin ensures the bootstrap admin user exists. No-op if env vars are empty.
// If the email already exists (admin or not), the function does NOT overwrite the password.
func BootstrapAdmin(d *sql.DB, cfg *config.Config) error {
	if cfg.BootstrapAdminEmail == "" || cfg.BootstrapAdminPassword == "" {
		return nil
	}
	var existing string
	err := d.QueryRow(`SELECT id FROM users WHERE email = ?`, cfg.BootstrapAdminEmail).Scan(&existing)
	if err == nil {
		return nil
	}
	if err != sql.ErrNoRows {
		return fmt.Errorf("bootstrap admin lookup: %w", err)
	}

	hash, err := auth.HashPassword(cfg.BootstrapAdminPassword)
	if err != nil {
		return fmt.Errorf("bootstrap admin hash: %w", err)
	}
	uid := auth.NewID()
	now := time.Now().Unix()
	_, err = d.Exec(
		`INSERT INTO users (id, email, password_hash, is_admin, created_at, updated_at) VALUES (?, ?, ?, 1, ?, ?)`,
		uid, cfg.BootstrapAdminEmail, hash, now, now,
	)
	if err != nil {
		return fmt.Errorf("bootstrap admin insert: %w", err)
	}
	return nil
}
