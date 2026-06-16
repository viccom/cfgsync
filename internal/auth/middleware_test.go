package auth_test

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/viccom/cfgsync/internal/auth"
	"github.com/viccom/cfgsync/internal/db"
)

func testSecret() []byte {
	return []byte("test-secret-test-secret-test-secret")
}

func openTempDBAuth(t *testing.T) *sql.DB {
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
	return d
}

func seedUserAuth(t *testing.T, d *sql.DB, email string, isAdmin bool) string {
	t.Helper()
	hash, _ := auth.HashPassword("x")
	uid := auth.NewID()
	var adm int
	if isAdmin {
		adm = 1
	}
	now := time.Now().Unix()
	if _, err := d.Exec(
		`INSERT INTO users (id, email, password_hash, is_admin, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		uid, email, hash, adm, now, now,
	); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return uid
}

func TestUserMW_RejectsMissingHeader(t *testing.T) {
	called := false
	h := auth.UserMW(testSecret(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if called {
		t.Errorf("handler should not be called")
	}
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestUserMW_AcceptsValidJWT(t *testing.T) {
	uid := "uid-1"
	tok, _ := auth.IssueAccess(testSecret(), uid, "u@example.com", false, time.Hour)

	var capturedUID string
	h := auth.UserMW(testSecret(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedUID = auth.UserID(r.Context())
		w.WriteHeader(200)
	}))
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if capturedUID != uid {
		t.Errorf("uid mismatch: %s vs %s", capturedUID, uid)
	}
}

func TestAdminMW_RejectsNonAdmin(t *testing.T) {
	tok, _ := auth.IssueAccess(testSecret(), "uid-1", "u@example.com", false, time.Hour)

	called := false
	h := auth.UserMW(testSecret(), auth.AdminMW(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})))
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if called {
		t.Errorf("handler should not be called")
	}
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", w.Code)
	}
}

func TestAdminMW_AcceptsAdmin(t *testing.T) {
	tok, _ := auth.IssueAccess(testSecret(), "uid-1", "admin@example.com", true, time.Hour)

	called := false
	h := auth.UserMW(testSecret(), auth.AdminMW(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(200)
	})))
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if !called {
		t.Errorf("handler should be called")
	}
	if w.Code != 200 {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestAppTokenMW_RejectsInvalidToken(t *testing.T) {
	d := openTempDBAuth(t)
	called := false
	h := auth.AppTokenMW(d, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	req := httptest.NewRequest("GET", "/api/v1/apps/com.foo/config", nil)
	req.Header.Set("Authorization", "Bearer bogus")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if called {
		t.Errorf("handler should not be called")
	}
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestAppTokenMW_RejectsTokenForDifferentAppID(t *testing.T) {
	d := openTempDBAuth(t)
	uid := seedUserAuth(t, d, "u@example.com", false)
	now := time.Now().Unix()

	if _, err := d.Exec(
		`INSERT INTO apps (app_id, display_name, description, created_at, created_by) VALUES (?, 'Foo', '', ?, ?)`,
		"com.foo", now, uid,
	); err != nil {
		t.Fatalf("seed app: %v", err)
	}
	plaintext := "1rc_real_token_xyz"
	if _, err := d.Exec(
		`INSERT INTO app_tokens (token_hash, token_prefix, user_id, app_id, label, created_at)
		 VALUES (?, ?, ?, ?, '', ?)`,
		sha256HexHelper(plaintext), plaintext[:12], uid, "com.foo", now,
	); err != nil {
		t.Fatalf("seed app_token: %v", err)
	}

	h := auth.AppTokenMW(d, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	req := httptest.NewRequest("GET", "/api/v1/apps/com.bar/config", nil)
	req.SetPathValue("app_id", "com.bar")
	req.Header.Set("Authorization", "Bearer "+plaintext)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 for cross-app token, got %d", w.Code)
	}
}

func TestAppTokenMW_AcceptsValidTokenForMatchingAppID(t *testing.T) {
	d := openTempDBAuth(t)
	uid := seedUserAuth(t, d, "u@example.com", false)
	now := time.Now().Unix()
	if _, err := d.Exec(
		`INSERT INTO apps (app_id, display_name, description, created_at, created_by) VALUES (?, 'Foo', '', ?, ?)`,
		"com.foo", now, uid,
	); err != nil {
		t.Fatalf("seed app: %v", err)
	}
	plaintext := "1rc_real_token_xyz"
	if _, err := d.Exec(
		`INSERT INTO app_tokens (token_hash, token_prefix, user_id, app_id, label, created_at)
		 VALUES (?, ?, ?, ?, '', ?)`,
		sha256HexHelper(plaintext), plaintext[:12], uid, "com.foo", now,
	); err != nil {
		t.Fatalf("seed app_token: %v", err)
	}

	var capturedUID, capturedAppID string
	h := auth.AppTokenMW(d, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if at := auth.AppToken(r.Context()); at != nil {
			capturedUID = at.UserID
			capturedAppID = at.AppID
		}
		w.WriteHeader(200)
	}))
	req := httptest.NewRequest("GET", "/api/v1/apps/com.foo/config", nil)
	req.SetPathValue("app_id", "com.foo")
	req.Header.Set("Authorization", "Bearer "+plaintext)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if capturedUID != uid {
		t.Errorf("uid mismatch: %s vs %s", capturedUID, uid)
	}
	if capturedAppID != "com.foo" {
		t.Errorf("app_id mismatch: %s", capturedAppID)
	}
}

func sha256HexHelper(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
