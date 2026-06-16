package server

import (
	"bytes"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/viccom/cfgsync/internal/auth"
	"github.com/viccom/cfgsync/internal/config"
	"github.com/viccom/cfgsync/internal/db"
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

	h := New(cfg, env)

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
