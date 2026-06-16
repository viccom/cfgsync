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
