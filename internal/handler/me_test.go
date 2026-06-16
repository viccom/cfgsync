package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/1remote/1remote-cloud/internal/auth"
)

func TestCreateAppToken_Success(t *testing.T) {
	env := newTestEnv(t)
	adminUID := env.seedUser(t, "admin@example.com", "p12345678", true)
	env.seedApp(t, "com.foo", "Foo", adminUID)

	uid := env.seedUser(t, "u@example.com", "p12345678", false)
	tok := env.userToken(uid, "u@example.com", false)

	h := auth.UserMW(env.cfg.JWTSecret, CreateAppToken(env.db, env.cfg))
	w := doPostAppToken(h, "com.foo", tok, map[string]interface{}{"label": "MBA"})
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"token":"1rc_`) {
		t.Errorf("expected 1rc_-prefixed token in %s", w.Body.String())
	}

	var hash string
	err := env.db.QueryRow(`SELECT token_hash FROM app_tokens WHERE user_id = ? AND app_id = ?`, uid, "com.foo").Scan(&hash)
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	if len(hash) != 64 {
		t.Errorf("expected 64-char SHA-256 hex hash, got %d", len(hash))
	}
}

func TestCreateAppToken_ReplacesExisting(t *testing.T) {
	env := newTestEnv(t)
	adminUID := env.seedUser(t, "admin@example.com", "p12345678", true)
	env.seedApp(t, "com.foo", "Foo", adminUID)

	uid := env.seedUser(t, "u@example.com", "p12345678", false)
	tok := env.userToken(uid, "u@example.com", false)
	h := auth.UserMW(env.cfg.JWTSecret, CreateAppToken(env.db, env.cfg))

	w1 := doPostAppToken(h, "com.foo", tok, nil)
	if w1.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%s", w1.Code, w1.Body.String())
	}
	w2 := doPostAppToken(h, "com.foo", tok, nil)
	if w2.Code != http.StatusOK {
		t.Fatalf("second status=%d body=%s", w2.Code, w2.Body.String())
	}

	var n int
	env.db.QueryRow(`SELECT COUNT(*) FROM app_tokens WHERE user_id = ? AND app_id = ?`, uid, "com.foo").Scan(&n)
	if n != 1 {
		t.Errorf("expected 1 token after replace, got %d", n)
	}
}

func TestCreateAppToken_AppNotFound(t *testing.T) {
	env := newTestEnv(t)
	uid := env.seedUser(t, "u@example.com", "p12345678", false)
	tok := env.userToken(uid, "u@example.com", false)
	h := auth.UserMW(env.cfg.JWTSecret, CreateAppToken(env.db, env.cfg))

	w := doPostAppToken(h, "nonexistent", tok, nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

// doPostAppToken posts to /api/v1/me/apps/{app_id}/token with path value set.
func doPostAppToken(h http.Handler, appID, token string, body interface{}) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest("POST", "/api/v1/me/apps/"+appID+"/token", &buf)
	req.SetPathValue("app_id", appID)
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

// doGetReq performs a GET with optional Bearer token (no body).
func doGetReq(h http.Handler, path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", path, &bytes.Buffer{})
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// doDeletePath performs DELETE with a path value set (no body).
func doDeletePath(h http.Handler, path, pathKey, pathVal, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("DELETE", path, &bytes.Buffer{})
	req.SetPathValue(pathKey, pathVal)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestListMyTokens_ReturnsOnlyMine(t *testing.T) {
	env := newTestEnv(t)
	adminUID := env.seedUser(t, "admin@example.com", "p12345678", true)
	env.seedApp(t, "com.foo", "Foo", adminUID)
	env.seedApp(t, "com.bar", "Bar", adminUID)

	uid := env.seedUser(t, "u@example.com", "p12345678", false)
	tok := env.userToken(uid, "u@example.com", false)

	createH := auth.UserMW(env.cfg.JWTSecret, CreateAppToken(env.db, env.cfg))
	doPostAppToken(createH, "com.foo", tok, map[string]interface{}{"label": "MBA"})
	doPostAppToken(createH, "com.bar", tok, map[string]interface{}{"label": "Desktop"})

	// Seed a token for a DIFFERENT user that should NOT appear in uid's list.
	otherUID := env.seedUser(t, "other@example.com", "p12345678", false)
	now := nowUnix()
	if _, err := env.db.Exec(
		`INSERT INTO app_tokens (token_hash, token_prefix, user_id, app_id, label, created_at)
		 VALUES ('deadbeef', '1rc_otheruser', ?, 'com.foo', 'not-yours', ?)`,
		otherUID, now,
	); err != nil {
		t.Fatalf("seed other user token: %v", err)
	}

	h := auth.UserMW(env.cfg.JWTSecret, ListMyTokens(env.db))
	w := doGetReq(h, "/api/v1/me/tokens", tok)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `"label":"MBA"`) {
		t.Errorf("expected MBA label in %s", body)
	}
	if !strings.Contains(body, `"label":"Desktop"`) {
		t.Errorf("expected Desktop label in %s", body)
	}
	if strings.Contains(body, `"not-yours"`) {
		t.Errorf("other user's token leaked into list: %s", body)
	}
	// Must never expose plaintext or hash.
	if strings.Contains(body, `"token":"`) {
		t.Errorf("plaintext token field should not appear: %s", body)
	}
	if strings.Contains(body, `"token_hash"`) {
		t.Errorf("token_hash field should not appear: %s", body)
	}
}

func TestDeleteAppToken_Success(t *testing.T) {
	env := newTestEnv(t)
	adminUID := env.seedUser(t, "admin@example.com", "p12345678", true)
	env.seedApp(t, "com.foo", "Foo", adminUID)

	uid := env.seedUser(t, "u@example.com", "p12345678", false)
	tok := env.userToken(uid, "u@example.com", false)

	createH := auth.UserMW(env.cfg.JWTSecret, CreateAppToken(env.db, env.cfg))
	resp := doPostAppToken(createH, "com.foo", tok, map[string]interface{}{"label": "MBA"})
	_, after, _ := strings.Cut(resp.Body.String(), `"token":"`)
	plaintext, _, _ := strings.Cut(after, `"`)
	prefix := plaintext[:12]

	deleteH := auth.UserMW(env.cfg.JWTSecret, DeleteAppToken(env.db))
	w := doDeletePath(deleteH, "/api/v1/me/tokens/"+prefix, "token_prefix", prefix, tok)
	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d body=%s", w.Code, w.Body.String())
	}

	var n int
	env.db.QueryRow(`SELECT COUNT(*) FROM app_tokens WHERE user_id = ?`, uid).Scan(&n)
	if n != 0 {
		t.Errorf("expected 0 tokens after delete, got %d", n)
	}
}

func TestDeleteAppToken_NotFound(t *testing.T) {
	env := newTestEnv(t)
	uid := env.seedUser(t, "u@example.com", "p12345678", false)
	tok := env.userToken(uid, "u@example.com", false)

	h := auth.UserMW(env.cfg.JWTSecret, DeleteAppToken(env.db))
	w := doDeletePath(h, "/api/v1/me/tokens/1rc_nonexist", "token_prefix", "1rc_nonexist", tok)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestDeleteAppToken_DoesNotDeleteOtherUsers(t *testing.T) {
	env := newTestEnv(t)
	adminUID := env.seedUser(t, "admin@example.com", "p12345678", true)
	env.seedApp(t, "com.foo", "Foo", adminUID)

	uid := env.seedUser(t, "u@example.com", "p12345678", false)
	tok := env.userToken(uid, "u@example.com", false)

	// Two users, each with a token sharing the same prefix (simulated collision).
	now := nowUnix()
	if _, err := env.db.Exec(
		`INSERT INTO app_tokens (token_hash, token_prefix, user_id, app_id, label, created_at)
		 VALUES ('h1', '1rc_collision', ?, 'com.foo', 'mine', ?)`,
		uid, now,
	); err != nil {
		t.Fatalf("seed mine: %v", err)
	}
	otherUID := env.seedUser(t, "other@example.com", "p12345678", false)
	if _, err := env.db.Exec(
		`INSERT INTO app_tokens (token_hash, token_prefix, user_id, app_id, label, created_at)
		 VALUES ('h2', '1rc_collision', ?, 'com.foo', 'theirs', ?)`,
		otherUID, now,
	); err != nil {
		t.Fatalf("seed theirs: %v", err)
	}

	h := auth.UserMW(env.cfg.JWTSecret, DeleteAppToken(env.db))
	w := doDeletePath(h, "/api/v1/me/tokens/1rc_collision", "token_prefix", "1rc_collision", tok)
	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", w.Code)
	}

	// Mine should be gone, theirs should remain.
	var mineCount, theirsCount int
	env.db.QueryRow(`SELECT COUNT(*) FROM app_tokens WHERE user_id = ? AND token_prefix = '1rc_collision'`, uid).Scan(&mineCount)
	env.db.QueryRow(`SELECT COUNT(*) FROM app_tokens WHERE user_id = ? AND token_prefix = '1rc_collision'`, otherUID).Scan(&theirsCount)
	if mineCount != 0 {
		t.Errorf("expected my token deleted, got %d", mineCount)
	}
	if theirsCount != 1 {
		t.Errorf("other user's token should be untouched, got %d", theirsCount)
	}
}

// doDeleteAppData issues DELETE /api/v1/me/apps/{app_id}/data with the path value set.
func doDeleteAppData(h http.Handler, appID, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("DELETE", "/api/v1/me/apps/"+appID+"/data", &bytes.Buffer{})
	req.SetPathValue("app_id", appID)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestDeleteAppData_WipesEverythingForPair(t *testing.T) {
	env := newTestEnv(t)
	adminUID := env.seedUser(t, "admin@example.com", "p12345678", true)
	env.seedApp(t, "com.foo", "Foo", adminUID)
	env.seedApp(t, "com.bar", "Bar", adminUID)

	uid := env.seedUser(t, "u@example.com", "p12345678", false)
	tok := env.userToken(uid, "u@example.com", false)
	now := nowUnix()

	// Seed configs + history + app_tokens for TWO (uid, app_id) pairs.
	for _, appID := range []string{"com.foo", "com.bar"} {
		if _, err := env.db.Exec(
			`INSERT INTO configs (user_id, app_id, version, payload, updated_at, updated_by) VALUES (?, ?, 1, 'p', ?, 'me')`,
			uid, appID, now,
		); err != nil {
			t.Fatalf("seed config %s: %v", appID, err)
		}
		if _, err := env.db.Exec(
			`INSERT INTO config_history (user_id, app_id, version, payload, updated_by, created_at) VALUES (?, ?, 1, 'p', 'me', ?)`,
			uid, appID, now,
		); err != nil {
			t.Fatalf("seed history %s: %v", appID, err)
		}
		if _, err := env.db.Exec(
			`INSERT INTO app_tokens (token_hash, token_prefix, user_id, app_id, label, created_at) VALUES (?, ?, ?, ?, '', ?)`,
			"hash_"+appID, "1rc_"+appID[:4], uid, appID, now,
		); err != nil {
			t.Fatalf("seed token %s: %v", appID, err)
		}
	}

	h := auth.UserMW(env.cfg.JWTSecret, DeleteAppData(env.db))
	w := doDeleteAppData(h, "com.foo", tok)
	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d body=%s", w.Code, w.Body.String())
	}

	// com.foo for uid: all gone.
	var fooConfig, fooHistory, fooToken int
	env.db.QueryRow(`SELECT COUNT(*) FROM configs        WHERE user_id = ? AND app_id = 'com.foo'`, uid).Scan(&fooConfig)
	env.db.QueryRow(`SELECT COUNT(*) FROM config_history WHERE user_id = ? AND app_id = 'com.foo'`, uid).Scan(&fooHistory)
	env.db.QueryRow(`SELECT COUNT(*) FROM app_tokens     WHERE user_id = ? AND app_id = 'com.foo'`, uid).Scan(&fooToken)
	if fooConfig != 0 || fooHistory != 0 || fooToken != 0 {
		t.Errorf("com.foo not wiped: config=%d history=%d token=%d", fooConfig, fooHistory, fooToken)
	}

	// com.bar for uid: untouched.
	var barConfig, barHistory, barToken int
	env.db.QueryRow(`SELECT COUNT(*) FROM configs        WHERE user_id = ? AND app_id = 'com.bar'`, uid).Scan(&barConfig)
	env.db.QueryRow(`SELECT COUNT(*) FROM config_history WHERE user_id = ? AND app_id = 'com.bar'`, uid).Scan(&barHistory)
	env.db.QueryRow(`SELECT COUNT(*) FROM app_tokens     WHERE user_id = ? AND app_id = 'com.bar'`, uid).Scan(&barToken)
	if barConfig != 1 || barHistory != 1 || barToken != 1 {
		t.Errorf("com.bar should be untouched: config=%d history=%d token=%d", barConfig, barHistory, barToken)
	}
}

func TestDeleteAppData_Idempotent(t *testing.T) {
	env := newTestEnv(t)
	uid := env.seedUser(t, "u@example.com", "p12345678", false)
	tok := env.userToken(uid, "u@example.com", false)

	// No prior data for (uid, com.foo) — still 204.
	h := auth.UserMW(env.cfg.JWTSecret, DeleteAppData(env.db))
	w := doDeleteAppData(h, "com.foo", tok)
	if w.Code != http.StatusNoContent {
		t.Errorf("expected 204 on empty delete, got %d", w.Code)
	}
}
