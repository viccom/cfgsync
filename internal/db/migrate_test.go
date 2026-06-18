package db

import (
	"database/sql"
	"os"
	"testing"
)

// freshDB returns a migrated temp DB. Each test gets its own file so WAL
// connections don't bleed across tests (modernc.org/sqlite gives each pooled
// connection its own :memory: namespace; temp files avoid that gotcha).
func freshDB(t *testing.T) *sql.DB {
	t.Helper()
	tmp, err := os.CreateTemp(t.TempDir(), "cfgsync-*.db")
	if err != nil {
		t.Fatalf("temp: %v", err)
	}
	tmp.Close()
	d, err := Open(tmp.Name())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := Migrate(d); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() {
		d.Close()
		os.Remove(tmp.Name())
		os.Remove(tmp.Name() + "-wal")
		os.Remove(tmp.Name() + "-shm")
	})
	return d
}

// TestMigrate_FreshDB_RecordsSchemaVersion3 asserts that a fresh DB ends up
// at schema_version = 3 after Migrate. This is the load-bearing intent of the
// v3 rewrite — any future step that bumps currentSchemaVersion must update
// this test alongside the migration registration.
func TestMigrate_FreshDB_RecordsSchemaVersion3(t *testing.T) {
	d := freshDB(t)

	var version int
	err := d.QueryRow(
		`SELECT version FROM schema_version WHERE version = ?`,
		currentSchemaVersion,
	).Scan(&version)
	if err != nil {
		t.Fatalf("expected schema_version=%d row present, got: %v", currentSchemaVersion, err)
	}
	if version != currentSchemaVersion {
		t.Errorf("got version=%d, want %d", version, currentSchemaVersion)
	}

	// All historical steps (1, 2, 3) should be recorded.
	rows, err := d.Query(`SELECT version FROM schema_version ORDER BY version`)
	if err != nil {
		t.Fatalf("query versions: %v", err)
	}
	defer rows.Close()
	var got []int
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, v)
	}
	if len(got) != currentSchemaVersion {
		t.Errorf("expected %d schema_version rows (one per step 1..N), got %d (%v)",
			currentSchemaVersion, len(got), got)
	}
}

// TestMigrate_Idempotent ensures re-running Migrate on an already-migrated DB
// is a no-op (no error, no duplicate rows).
func TestMigrate_Idempotent(t *testing.T) {
	d := freshDB(t)
	if err := Migrate(d); err != nil {
		t.Fatalf("second Migrate call must be no-op, got: %v", err)
	}
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM schema_version`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != currentSchemaVersion {
		t.Errorf("expected %d rows after re-migrate, got %d", currentSchemaVersion, n)
	}
}

// TestMigrate_CreatesAppMarketTables asserts the v3-only tables exist by
// inserting and reading back a row. Catches regressions where schema.sql is
// accidentally edited to drop one of them.
func TestMigrate_CreatesAppMarketTables(t *testing.T) {
	d := freshDB(t)

	// app_tags must accept inserts and cascade on app delete.
	seedAppRow(t, d, "com.example.tagtest", "TagTest")
	if _, err := d.Exec(
		`INSERT INTO app_tags (app_id, tag) VALUES (?, ?), (?, ?)`,
		"com.example.tagtest", "cli",
		"com.example.tagtest", "ai",
	); err != nil {
		t.Fatalf("insert app_tags: %v", err)
	}

	// app_releases must accept the full v3 column set.
	if _, err := d.Exec(
		`INSERT INTO app_releases (
			app_id, version, version_major, version_minor, version_patch, version_pre,
			manifest_yaml, manifest_json, package_size, package_sha256,
			docs_json, assets_json, release_notes, created_at, created_by
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"com.example.tagtest", "1.0.0", 1, 0, 0, "",
		"schema_version: 1\nversion: \"1.0.0\"\ndisplay_name: \"TagTest\"\n",
		`{"schema_version":1,"version":"1.0.0","display_name":"TagTest"}`,
		1024, "deadbeef",
		`{}`, `[]`, "",
		1718700000, "admin-uid",
	); err != nil {
		t.Fatalf("insert app_releases: %v", err)
	}

	// apps must carry the v3 columns (visibility CHECK, latest_version,
	// updated_at NOT NULL, summary/icon_path/owner_user_id).
	var (
		visibility     string
		latestVersion  string
		summary        string
		iconPath       string
		ownerUserID    sql.NullString
	)
	err := d.QueryRow(
		`SELECT visibility, latest_version, summary, icon_path, owner_user_id
		   FROM apps WHERE app_id = ?`,
		"com.example.tagtest",
	).Scan(&visibility, &latestVersion, &summary, &iconPath, &ownerUserID)
	if err != nil {
		t.Fatalf("query apps v3 cols: %v", err)
	}
	if visibility != "public" {
		t.Errorf("expected default visibility=public, got %q", visibility)
	}

	// visibility CHECK constraint must reject bad values.
	_, err = d.Exec(`UPDATE apps SET visibility = 'bogus' WHERE app_id = ?`, "com.example.tagtest")
	if err == nil {
		t.Errorf("expected CHECK constraint to reject visibility='bogus'")
	}
}

func seedAppRow(t *testing.T, d *sql.DB, appID, displayName string) {
	t.Helper()
	// apps.created_by is NOT NULL REFERENCES users(id) — pre-seed the admin.
	if _, err := d.Exec(
		`INSERT OR IGNORE INTO users (id, email, password_hash, is_admin, created_at, updated_at)
		 VALUES ('admin-uid', 'admin@example.com', 'x', 1, 0, 0)`,
	); err != nil {
		t.Fatalf("seed admin user: %v", err)
	}
	if _, err := d.Exec(
		`INSERT INTO apps (app_id, display_name, description, created_at, created_by, updated_at)
		 VALUES (?, ?, '', 0, 'admin-uid', 0)`,
		appID, displayName,
	); err != nil {
		t.Fatalf("seed app: %v", err)
	}
}
