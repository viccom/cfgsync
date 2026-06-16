package db

import (
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/viccom/cfgsync/internal/auth"
	"github.com/viccom/cfgsync/internal/config"
)

func TestBootstrapAdmin_CreatesWhenAbsent(t *testing.T) {
	d := openTempDB(t)
	cfg := &config.Config{
		BootstrapAdminEmail:    "admin@example.com",
		BootstrapAdminPassword: "password123",
	}
	if err := BootstrapAdmin(d, cfg); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	var isAdmin int
	var hash string
	err := d.QueryRow(`SELECT is_admin, password_hash FROM users WHERE email = ?`, cfg.BootstrapAdminEmail).Scan(&isAdmin, &hash)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if isAdmin != 1 {
		t.Errorf("expected is_admin=1, got %d", isAdmin)
	}
	if hash == "" {
		t.Errorf("expected non-empty hash")
	}
}

func TestBootstrapAdmin_NoOpWhenEnvEmpty(t *testing.T) {
	d := openTempDB(t)
	cfg := &config.Config{}
	if err := BootstrapAdmin(d, cfg); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	var n int
	d.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	if n != 0 {
		t.Errorf("expected 0 users, got %d", n)
	}
}

func TestBootstrapAdmin_DoesNotOverwrite(t *testing.T) {
	d := openTempDB(t)
	cfg := &config.Config{
		BootstrapAdminEmail:    "admin@example.com",
		BootstrapAdminPassword: "first-password",
	}
	if err := BootstrapAdmin(d, cfg); err != nil {
		t.Fatalf("first bootstrap: %v", err)
	}

	var firstHash string
	d.QueryRow(`SELECT password_hash FROM users WHERE email = ?`, cfg.BootstrapAdminEmail).Scan(&firstHash)

	cfg.BootstrapAdminPassword = "second-password"
	if err := BootstrapAdmin(d, cfg); err != nil {
		t.Fatalf("second bootstrap: %v", err)
	}

	var secondHash string
	d.QueryRow(`SELECT password_hash FROM users WHERE email = ?`, cfg.BootstrapAdminEmail).Scan(&secondHash)
	if firstHash != secondHash {
		t.Errorf("bootstrap should not overwrite password; hash changed from %q to %q", firstHash, secondHash)
	}
}

// If someone pre-registered the bootstrap email as a regular user, BootstrapAdmin
// should adopt them as admin without changing the password.
func TestBootstrapAdmin_PromotesExistingNonAdmin(t *testing.T) {
	d := openTempDB(t)

	// Pre-register the bootstrap email as a regular user.
	hash, err := auth.HashPassword("user-chosen-password")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	now := time.Now().Unix()
	uid := auth.NewID()
	if _, err := d.Exec(
		`INSERT INTO users (id, email, password_hash, is_admin, created_at, updated_at) VALUES (?, ?, ?, 0, ?, ?)`,
		uid, "admin@example.com", hash, now, now,
	); err != nil {
		t.Fatalf("seed non-admin: %v", err)
	}

	cfg := &config.Config{
		BootstrapAdminEmail:    "admin@example.com",
		BootstrapAdminPassword: "bootstrap-password-should-be-ignored",
	}
	if err := BootstrapAdmin(d, cfg); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	var (
		gotIsAdmin int
		gotHash    string
	)
	if err := d.QueryRow(
		`SELECT is_admin, password_hash FROM users WHERE email = ?`, cfg.BootstrapAdminEmail,
	).Scan(&gotIsAdmin, &gotHash); err != nil {
		t.Fatalf("query: %v", err)
	}
	if gotIsAdmin != 1 {
		t.Errorf("expected existing user promoted to admin (is_admin=1), got %d", gotIsAdmin)
	}
	if gotHash != hash {
		t.Errorf("bootstrap must not overwrite password; hash changed")
	}
}

// Re-running bootstrap on an already-admin user is a no-op (no error, no row changes).
func TestBootstrapAdmin_AlreadyAdminIsNoOp(t *testing.T) {
	d := openTempDB(t)
	cfg := &config.Config{
		BootstrapAdminEmail:    "admin@example.com",
		BootstrapAdminPassword: "p12345678",
	}
	if err := BootstrapAdmin(d, cfg); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := BootstrapAdmin(d, cfg); err != nil {
		t.Fatalf("second: %v", err)
	}
	var n int
	d.QueryRow(`SELECT COUNT(*) FROM users WHERE email = ?`, cfg.BootstrapAdminEmail).Scan(&n)
	if n != 1 {
		t.Errorf("expected 1 row, got %d", n)
	}
}

func openTempDB(t *testing.T) *sql.DB {
	t.Helper()
	tmp, err := os.CreateTemp(t.TempDir(), "1rc-*.db")
	if err != nil {
		t.Fatalf("create temp: %v", err)
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
