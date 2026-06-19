// Package db owns the SQLite connection and schema migrations.
package db

import (
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/viccom/cfgsync/internal/auth"
	"github.com/viccom/cfgsync/internal/config"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

// currentSchemaVersion is the highest schema_version this binary knows how
// to migrate TO. Bump this when a new migrationStep is added.
const currentSchemaVersion = 3

// Open opens (or creates) the SQLite database at path and applies WAL mode.
// If the parent directory of path does not exist, it is created (0700) so a
// fresh deployment with DB_PATH=/var/lib/cfgsync/data.db works without manual
// mkdir. :memory: and bare filenames skip this step.
func Open(path string) (*sql.DB, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." && dir != "/" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create db dir %s: %w", dir, err)
		}
	}
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

// Migrate walks the schema from whatever version the DB currently records up
// to currentSchemaVersion. Each step is idempotent and runs at most once per
// DB (tracked in the schema_version table). Fresh DBs run every step in
// order; existing DBs only run steps newer than their recorded version.
//
// Steps 1 and 2 are legacy no-ops (the v1/v2 layouts are not forward-
// compatible with v3 and are intentionally unsupported). Step 3 is the
// current fresh-rewrite schema. Future v4+ steps will be ALTER-based and
// forward-compatible.
func Migrate(d *sql.DB) error {
	if err := ensureSchemaVersionTable(d); err != nil {
		return fmt.Errorf("ensure schema_version table: %w", err)
	}
	applied, err := readAppliedVersions(d)
	if err != nil {
		return fmt.Errorf("read applied versions: %w", err)
	}
	for v := 1; v <= currentSchemaVersion; v++ {
		if _, ok := applied[v]; ok {
			continue
		}
		step, ok := migrationSteps[v]
		if !ok {
			return fmt.Errorf("no migration step registered for schema_version=%d", v)
		}
		if err := step(d); err != nil {
			return fmt.Errorf("apply schema_version=%d: %w", v, err)
		}
		if _, err := d.Exec(
			`INSERT OR IGNORE INTO schema_version (version, applied_at) VALUES (?, ?)`,
			v, time.Now().Unix(),
		); err != nil {
			return fmt.Errorf("record schema_version=%d: %w", v, err)
		}
	}
	return nil
}

// migrationSteps maps schema_version → migration function.
// Each function must be safe to run on a DB that has already had earlier
// steps applied (use CREATE TABLE IF NOT EXISTS, guard ALTER TABLE with
// column-existence checks, etc.).
var migrationSteps = map[int]func(*sql.DB) error{
	1: applyV1,
	2: applyV2,
	3: applyV3,
}

// applyV1 / applyV2: legacy baseline steps. They are no-ops because v3's
// schema.sql covers every table via CREATE TABLE IF NOT EXISTS. They exist
// so the version ledger records progress for any future tooling that walks
// migration history.
func applyV1(d *sql.DB) error { return nil }
func applyV2(d *sql.DB) error { return nil }

// applyV3: current schema (app market module). Probes the apps table for
// the v3 column set BEFORE running schema.sql so a v2-era DB fails with an
// actionable error message instead of crashing mid-migration on
// CREATE INDEX idx_apps_visibility. After schema.sql runs, the probe runs
// again to catch the case where a future edit to schema.sql accidentally
// drops a column while leaving CREATE TABLE IF NOT EXISTS as a no-op.
//
// v2 → v3 in-place upgrade is intentionally unsupported; operators must
// delete the DB and restart. See
// docs/superpowers/specs/2026-06-18-app-market-design.md §2 decision 8.
func applyV3(d *sql.DB) error {
	if err := assertAppsHasV3ColumnsOrAbsent(d); err != nil {
		return err
	}
	if _, err := d.Exec(schemaSQL); err != nil {
		return fmt.Errorf("apply schema.sql: %w", err)
	}
	if err := assertAppsHasV3ColumnsOrAbsent(d); err != nil {
		return fmt.Errorf("post-schema probe: %w", err)
	}
	return nil
}

// v3ColumnsAddedBySchema is the set of apps columns schema.sql adds beyond
// the v2 baseline. assertAppsHasV3ColumnsOrAbsent uses this to detect a
// v2-era apps table that schema.sql's CREATE TABLE IF NOT EXISTS would
// silently leave untouched.
var v3ColumnsAddedBySchema = []string{
	"visibility", "summary", "icon_path", "latest_version", "updated_at",
	"owner_user_id",
}

func assertAppsHasV3ColumnsOrAbsent(d *sql.DB) error {
	var appsExists int
	err := d.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'apps'`,
	).Scan(&appsExists)
	if err != nil {
		return fmt.Errorf("probe apps existence: %w", err)
	}
	if appsExists == 0 {
		return nil // fresh DB — schema.sql will create apps with v3 columns
	}
	for _, col := range v3ColumnsAddedBySchema {
		var n int
		err := d.QueryRow(
			`SELECT COUNT(*) FROM pragma_table_info('apps') WHERE name = ?`,
			col,
		).Scan(&n)
		if err != nil {
			return fmt.Errorf("probe apps.%s: %w", col, err)
		}
		if n == 0 {
			return fmt.Errorf(
				"apps table is missing column %q — "+
					"v2 → v3 in-place upgrade is not supported; delete the DB file and restart "+
					"(see docs/superpowers/specs/2026-06-18-app-market-design.md §2 decision 8)",
				col,
			)
		}
	}
	return nil
}

func ensureSchemaVersionTable(d *sql.DB) error {
	_, err := d.Exec(`CREATE TABLE IF NOT EXISTS schema_version (
		version    INTEGER PRIMARY KEY,
		applied_at INTEGER NOT NULL
	)`)
	return err
}

func readAppliedVersions(d *sql.DB) (map[int]struct{}, error) {
	rows, err := d.Query(`SELECT version FROM schema_version`)
	if err != nil {
		// schema_version table missing — treat as fresh install.
		return map[int]struct{}{}, nil
	}
	defer rows.Close()
	out := make(map[int]struct{})
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out[v] = struct{}{}
	}
	return out, rows.Err()
}

// BootstrapAdmin ensures the bootstrap admin user exists and is an admin.
// No-op if env vars are empty. Behavior when the email already exists:
//   - Password is NEVER overwritten (the existing user's credentials stay).
//   - is_admin is set to 1 if it was 0 (so a pre-registered regular account
//     can be "adopted" as the bootstrap admin without re-creating it).
//
// This prevents the failure mode where someone pre-registers the bootstrap
// email as a regular user and then deployment cannot bootstrap.
func BootstrapAdmin(d *sql.DB, cfg *config.Config) error {
	if cfg.BootstrapAdminEmail == "" || cfg.BootstrapAdminPassword == "" {
		return nil
	}

	var existingID string
	err := d.QueryRow(`SELECT id FROM users WHERE email = ?`, cfg.BootstrapAdminEmail).Scan(&existingID)
	switch {
	case err == nil:
		// Promote to admin if not already; leave password untouched.
		_, err := d.Exec(
			`UPDATE users SET is_admin = 1, updated_at = ? WHERE email = ? AND is_admin = 0`,
			time.Now().Unix(), cfg.BootstrapAdminEmail,
		)
		if err != nil {
			return fmt.Errorf("bootstrap admin promote: %w", err)
		}
		return nil
	case errors.Is(err, sql.ErrNoRows):
		// fall through to insert
	default:
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
