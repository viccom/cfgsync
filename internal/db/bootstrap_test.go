package db

import (
	"database/sql"
	"os"
	"testing"

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
