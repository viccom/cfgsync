package handler

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/viccom/cfgsync/internal/auth"
	"github.com/viccom/cfgsync/internal/config"
	"github.com/viccom/cfgsync/internal/db"
	"github.com/viccom/cfgsync/internal/repo"
)

// testEnv bundles a temp DB, repo, and a test config.
type testEnv struct {
	db   *sql.DB
	cfg  *config.Config
	repo *repo.Repo
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	tmp, err := os.CreateTemp(t.TempDir(), "1rc-*.db")
	if err != nil {
		t.Fatalf("temp: %v", err)
	}
	tmp.Close()
	d, err := db.Open(tmp.Name())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.Migrate(d); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() {
		d.Close()
		os.Remove(tmp.Name())
		os.Remove(tmp.Name() + "-wal")
		os.Remove(tmp.Name() + "-shm")
	})
	r, err := repo.New(filepath.Join(t.TempDir(), "repo"))
	if err != nil {
		t.Fatalf("repo.New: %v", err)
	}
	cfg := &config.Config{
		Listen:             ":0",
		DBPath:             "test.db",
		JWTSecret:          []byte("test-secret-test-secret-test-secret"),
		AccessTTL:          time.Hour,
		RefreshTTL:         30 * 24 * time.Hour,
		UserStorageLimit:   100 * 1024 * 1024,
		UserAppTokenLimit:  100,
		HistoryPerApp:      50,
		MaxPayloadBytes:    4 * 1024 * 1024,
		AppTokenPrefix:     "1rc_",
		RepoDir:            r.Root(),
		MaxPackageBytes:    10 * 1024 * 1024,
		MaxManifestBytes:   64 * 1024,
		MaxDocBytes:        1024 * 1024,
		MaxIconBytes:       256 * 1024,
		MaxScreenshotBytes: 2 * 1024 * 1024,
		MaxScreenshots:     12,
	}
	return &testEnv{db: d, cfg: cfg, repo: r}
}

// seedUser inserts a user and returns their id.
func (e *testEnv) seedUser(t *testing.T, email, password string, isAdmin bool) string {
	t.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	uid := auth.NewID()
	var adm int
	if isAdmin {
		adm = 1
	}
	now := time.Now().Unix()
	if _, err := e.db.Exec(
		`INSERT INTO users (id, email, password_hash, is_admin, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		uid, email, hash, adm, now, now,
	); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return uid
}

// seedApp inserts an app_id (admin-created style).
func (e *testEnv) seedApp(t *testing.T, appID, displayName, createdBy string) {
	t.Helper()
	now := time.Now().Unix()
	if _, err := e.db.Exec(
		`INSERT INTO apps (app_id, display_name, description, created_at, created_by, updated_at) VALUES (?, ?, '', ?, ?, ?)`,
		appID, displayName, now, createdBy, now,
	); err != nil {
		t.Fatalf("seed app: %v", err)
	}
}

// userToken returns a JWT for the given user.
func (e *testEnv) userToken(uid, email string, isAdmin bool) string {
	tok, _ := auth.IssueAccess(e.cfg.JWTSecret, uid, email, isAdmin, e.cfg.AccessTTL)
	return tok
}

// doReq issues a JSON request with an optional Bearer token.
func doReq(t *testing.T, h http.Handler, method, path, token string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func nowUnix() int64 {
	return time.Now().Unix()
}
