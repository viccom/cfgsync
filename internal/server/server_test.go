package server

import (
	"bytes"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/viccom/cfgsync/internal/auth"
	"github.com/viccom/cfgsync/internal/config"
	"github.com/viccom/cfgsync/internal/db"
	"github.com/viccom/cfgsync/internal/repo"
)

// TestChain_PanicPropagatesStatusToLog verifies that when an inner handler panics,
// recoverMW (running inside logMW) writes 500 through the wrappedWriter so that
// logMW records the actual status code, not the default 200.
//
// Prior to the chain-order fix, recoverMW ran OUTSIDE logMW and wrote directly
// to the underlying ResponseWriter, leaving logMW's wrappedWriter.status at 0/200
// while the client received 500 — log/client disagreed.
func TestChain_PanicPropagatesStatusToLog(t *testing.T) {
	// chain(h, logMW, recoverMW) == logMW(recoverMW(h)) — see chain() impl.
	// Outer = logMW (observes status via wrappedWriter); inner = recoverMW.
	var loggedStatus int
	capturedLogMW := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ww := &wrappedWriter{ResponseWriter: w, status: 200}
			next.ServeHTTP(ww, r)
			loggedStatus = ww.status
		})
	}

	panicH := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("simulated handler panic")
	})

	h := chain(panicH, capturedLogMW, recoverMW)

	req := httptest.NewRequest("GET", "/x", bytes.NewReader(nil))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("client saw %d, want 500", rec.Code)
	}
	if loggedStatus != http.StatusInternalServerError {
		t.Errorf("logMW recorded status %d, want 500 (chain order bug)", loggedStatus)
	}
}

// TestChain_NoPanic_PassesThrough ensures the recover+log chain doesn't
// disturb normal 200 responses.
func TestChain_NoPanic_PassesThrough(t *testing.T) {
	okH := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot) // 418 — distinctive, not 200
	})
	h := chain(okH, logMW, recoverMW)

	req := httptest.NewRequest("GET", "/x", bytes.NewReader(nil))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusTeapot {
		t.Errorf("got %d, want 418", rec.Code)
	}
}

// TestServer_AdminListUsers_RouteIsWired exercises the full middleware chain
// (UserMW + AdminMW → AdminListUsers) through server.New to confirm the route
// added in task 4 is reachable, returns 403 for non-admins, 200 for admins,
// and never leaks password_hash.
func TestServer_AdminListUsers_RouteIsWired(t *testing.T) {
	env := openTempDB(t)
	cfg := &config.Config{
		JWTSecret:         []byte("test-secret-test-secret-test-secret"),
		AccessTTL:         time.Hour,
		RefreshTTL:        30 * 24 * time.Hour,
		UserStorageLimit:  100 * 1024 * 1024,
		UserAppTokenLimit: 100,
		HistoryPerApp:     50,
		MaxPayloadBytes:   4 * 1024 * 1024,
		AppTokenPrefix:    "1rc_",
	}
	now := time.Now().Unix()
	mustExec(t, env, `INSERT INTO users (id, email, password_hash, is_admin, created_at, updated_at)
	                 VALUES ('admin-id','admin@example.com','x',1,?,?)`, now, now)
	mustExec(t, env, `INSERT INTO users (id, email, password_hash, is_admin, created_at, updated_at)
	                 VALUES ('user-id','u@example.com','x',0,?,?)`, now, now)
	adminTok, _ := auth.IssueAccess(cfg.JWTSecret, "admin-id", "admin@example.com", true, cfg.AccessTTL)
	userTok, _ := auth.IssueAccess(cfg.JWTSecret, "user-id", "u@example.com", false, cfg.AccessTTL)

	h := New(cfg, env, openTempRepo(t))

	// Non-admin → 403.
	w := doReqSimple(t, h, "GET", "/api/v1/admin/users", userTok, nil)
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 for non-admin, got %d", w.Code)
	}
	// Admin → 200 with at least one user.
	w = doReqSimple(t, h, "GET", "/api/v1/admin/users", adminTok, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for admin, got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "admin@example.com") {
		t.Errorf("expected admin email in body: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "password_hash") {
		t.Errorf("password_hash must not appear: %s", w.Body.String())
	}
}

// openTempDB creates a temporary SQLite DB with the schema applied, cleaned up after the test.
func openTempDB(t *testing.T) *sql.DB {
	t.Helper()
	tmp, err := os.CreateTemp(t.TempDir(), "1rc-*.db")
	if err != nil {
		t.Fatalf("create temp: %v", err)
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
	return d
}

// openTempRepo creates a Repo rooted at a fresh temp dir so each test
// gets its own on-disk package store (avoiding cross-test bleed).
func openTempRepo(t *testing.T) *repo.Repo {
	t.Helper()
	r, err := repo.New(filepath.Join(t.TempDir(), "repo"))
	if err != nil {
		t.Fatalf("repo.New: %v", err)
	}
	return r
}

// TestCleanupOrphans_RemovesStaleReleaseDirs covers the N9 fix: a release
// directory whose DB row is missing must be removed at startup so FS state
// converges with DB state after a crash or out-of-band deletion.
func TestCleanupOrphans_RemovesStaleReleaseDirs(t *testing.T) {
	dbase := openTempDB(t)
	r := openTempRepo(t)

	// Seed one legit release (DB + FS) and one orphan (FS only).
	now := time.Now().Unix()
	mustExec(t, dbase, `INSERT INTO users (id, email, password_hash, is_admin, created_at, updated_at)
	                    VALUES ('u1', 'a@b', 'x', 1, ?, ?)`, now, now)
	mustExec(t, dbase, `INSERT INTO apps (app_id, display_name, description, created_at, created_by, updated_at, visibility, latest_version)
	                    VALUES ('com.foo', 'Foo', '', ?, 'u1', ?, 'public', '1.0.0')`, now, now)
	mustExec(t, dbase, `INSERT INTO app_releases (app_id, version, version_major, version_minor, version_patch, version_pre,
	                    manifest_yaml, manifest_json, package_size, package_sha256, docs_json, assets_json, release_notes, created_at, created_by)
	                    VALUES ('com.foo', '1.0.0', 1, 0, 0, '', 'x', '{}', 1, 'x', '{}', '[]', '', ?, 'u1')`, now)

	// Create both release dirs on disk (skip Stage/Promote plumbing for brevity).
	for _, v := range []string{"1.0.0", "2.0.0"} {
		dir := filepath.Join(r.Root(), "com.foo", v)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		// Drop a sentinel file so the dir is non-empty.
		if err := os.WriteFile(filepath.Join(dir, "sentinel"), []byte("x"), 0o600); err != nil {
			t.Fatalf("write sentinel: %v", err)
		}
	}
	// Also drop a _staging dir to confirm it's NOT touched.
	stagingDir := filepath.Join(r.Root(), "_staging", "com.foo", "upload-1")
	if err := os.MkdirAll(stagingDir, 0o700); err != nil {
		t.Fatalf("mkdir staging: %v", err)
	}

	removed, err := CleanupOrphans(dbase, r)
	if err != nil {
		t.Fatalf("CleanupOrphans: %v", err)
	}
	if removed != 1 {
		t.Errorf("removed=%d, want 1 (only 2.0.0 should be cleaned; 1.0.0 has a DB row)", removed)
	}
	if !r.ReleaseExists("com.foo", "1.0.0") {
		t.Errorf("CleanupOrphans deleted a release that has a DB row")
	}
	if r.ReleaseExists("com.foo", "2.0.0") {
		t.Errorf("CleanupOrphans did not delete orphan 2.0.0")
	}
	// Staging must be untouched — those are in-flight uploads, not promoted releases.
	if _, err := os.Stat(stagingDir); err != nil {
		t.Errorf("staging dir was disturbed by CleanupOrphans: %v", err)
	}
}

// TestCleanupOrphans_NoOrphansIsZeroCount verifies the no-op path: with no
// orphans present the function returns 0 cleanly.
func TestCleanupOrphans_NoOrphansIsZeroCount(t *testing.T) {
	dbase := openTempDB(t)
	r := openTempRepo(t)

	removed, err := CleanupOrphans(dbase, r)
	if err != nil {
		t.Fatalf("CleanupOrphans on empty repo: %v", err)
	}
	if removed != 0 {
		t.Errorf("removed=%d, want 0 on empty repo", removed)
	}
}

func mustExec(t *testing.T, d *sql.DB, q string, args ...interface{}) {
	t.Helper()
	if _, err := d.Exec(q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

func doReqSimple(t *testing.T, h http.Handler, method, path, token string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != nil {
		r = httptest.NewRequest(method, path, bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// TestServer_WebUI_ServesIndexOnRoot confirms the embedded SPA is served at /.
func TestServer_WebUI_ServesIndexOnRoot(t *testing.T) {
	env := openTempDB(t)
	cfg := &config.Config{
		JWTSecret:         []byte("test-secret-test-secret-test-secret"),
		AccessTTL:         time.Hour,
		RefreshTTL:        30 * 24 * time.Hour,
		UserStorageLimit:  100 * 1024 * 1024,
		UserAppTokenLimit: 100,
		HistoryPerApp:     50,
		MaxPayloadBytes:   4 * 1024 * 1024,
		AppTokenPrefix:    "1rc_",
	}
	h := New(cfg, env, openTempRepo(t))
	w := doReqSimple(t, h, "GET", "/", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Header().Get("Content-Type"), "text/html") {
		t.Errorf("expected text/html, got %q", w.Header().Get("Content-Type"))
	}
}

// TestServer_WebUI_APIRouteNotShadowed confirms the catch-all webui mount does
// not shadow the explicit /api/v1/* routes.
func TestServer_WebUI_APIRouteNotShadowed(t *testing.T) {
	env := openTempDB(t)
	cfg := &config.Config{
		JWTSecret:         []byte("test-secret-test-secret-test-secret"),
		AccessTTL:         time.Hour,
		RefreshTTL:        30 * 24 * time.Hour,
		UserStorageLimit:  100 * 1024 * 1024,
		UserAppTokenLimit: 100,
		HistoryPerApp:     50,
		MaxPayloadBytes:   4 * 1024 * 1024,
		AppTokenPrefix:    "1rc_",
	}
	h := New(cfg, env, openTempRepo(t))
	w := doReqSimple(t, h, "GET", "/api/v1/health", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Header().Get("Content-Type"), "application/json") {
		t.Errorf("expected application/json from /api/v1/health, got %q (body=%s)",
			w.Header().Get("Content-Type"), w.Body.String())
	}
	if strings.Contains(strings.ToLower(w.Body.String()), "<!doctype") {
		t.Errorf("/api/v1/health returned HTML; the webui catch-all is shadowing the API route")
	}
}
