package handler

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/1remote/1remote-cloud/internal/auth"
)

// seedAppTokenFor creates an app + app_token row for (uid, appID) and returns the plaintext.
func (e *testEnv) seedAppTokenFor(t *testing.T, uid, appID, label string) string {
	t.Helper()
	now := nowUnix()
	if _, err := e.db.Exec(
		`INSERT INTO apps (app_id, display_name, description, created_at, created_by) VALUES (?, 'X', '', ?, ?)`,
		appID, now, uid,
	); err != nil {
		t.Fatalf("seed app: %v", err)
	}
	plaintext := e.cfg.AppTokenPrefix + "test_" + uid[:8]
	sum := sha256.Sum256([]byte(plaintext))
	if _, err := e.db.Exec(
		`INSERT INTO app_tokens (token_hash, token_prefix, user_id, app_id, label, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		hex.EncodeToString(sum[:]), plaintext[:12], uid, appID, label, now,
	); err != nil {
		t.Fatalf("seed app_token: %v", err)
	}
	return plaintext
}

// doAppTokenReq builds a request, sets the path {app_id} value, attaches the Bearer token,
// and dispatches through the handler chain.
func doAppTokenReq(t *testing.T, h http.Handler, method, path, appID, token string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
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

func TestGetConfig_NewUser_ReturnsZeroVersion(t *testing.T) {
	env := newTestEnv(t)
	uid := env.seedUser(t, "u@example.com", "password123", false)
	tok := env.seedAppTokenFor(t, uid, "com.foo", "")

	h := auth.AppTokenMW(env.db, GetConfig(env.db))
	w := doAppTokenReq(t, h, "GET", "/api/v1/apps/com.foo/config", "com.foo", tok, nil)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"version":0`) {
		t.Errorf("expected version:0 in %s", w.Body.String())
	}
}

func TestPutConfig_FirstTime_CreatesV1(t *testing.T) {
	env := newTestEnv(t)
	uid := env.seedUser(t, "u@example.com", "password123", false)
	tok := env.seedAppTokenFor(t, uid, "com.foo", "")

	body := map[string]interface{}{
		"version":    0,
		"payload":    `{"hello":"world"}`,
		"updated_by": "MBA",
	}
	h := auth.AppTokenMW(env.db, PutConfig(env.db, env.cfg))
	w := doAppTokenReq(t, h, "PUT", "/api/v1/apps/com.foo/config", "com.foo", tok, body)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"version":1`) {
		t.Errorf("expected version:1 in %s", w.Body.String())
	}
}

func TestPutConfig_Conflict_Returns409(t *testing.T) {
	env := newTestEnv(t)
	uid := env.seedUser(t, "u@example.com", "password123", false)
	tok := env.seedAppTokenFor(t, uid, "com.foo", "")

	h := auth.AppTokenMW(env.db, PutConfig(env.db, env.cfg))
	w1 := doAppTokenReq(t, h, "PUT", "/api/v1/apps/com.foo/config", "com.foo", tok,
		map[string]interface{}{"version": 0, "payload": "a", "updated_by": "MBA"})
	if w1.Code != 200 {
		t.Fatalf("first write status=%d body=%s", w1.Code, w1.Body.String())
	}

	w2 := doAppTokenReq(t, h, "PUT", "/api/v1/apps/com.foo/config", "com.foo", tok,
		map[string]interface{}{"version": 0, "payload": "b", "updated_by": "MBA"})
	if w2.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d body=%s", w2.Code, w2.Body.String())
	}
	if !strings.Contains(w2.Body.String(), `"current_version":1`) {
		t.Errorf("expected current_version:1 in %s", w2.Body.String())
	}
}

func TestPutConfig_Force_Overwrites(t *testing.T) {
	env := newTestEnv(t)
	uid := env.seedUser(t, "u@example.com", "password123", false)
	tok := env.seedAppTokenFor(t, uid, "com.foo", "")

	h := auth.AppTokenMW(env.db, PutConfig(env.db, env.cfg))
	doAppTokenReq(t, h, "PUT", "/api/v1/apps/com.foo/config", "com.foo", tok,
		map[string]interface{}{"version": 0, "payload": "a", "updated_by": "MBA"})

	w2 := doAppTokenReq(t, h, "PUT", "/api/v1/apps/com.foo/config?force=true", "com.foo", tok,
		map[string]interface{}{"version": 0, "payload": "forced", "updated_by": "MBA"})
	if w2.Code != 200 {
		t.Fatalf("force status=%d body=%s", w2.Code, w2.Body.String())
	}
	if !strings.Contains(w2.Body.String(), `"version":2`) {
		t.Errorf("expected version:2 in %s", w2.Body.String())
	}
}

func TestPutConfig_TooLarge_Returns413(t *testing.T) {
	env := newTestEnv(t)
	env.cfg.MaxPayloadBytes = 16
	uid := env.seedUser(t, "u@example.com", "password123", false)
	tok := env.seedAppTokenFor(t, uid, "com.foo", "")

	h := auth.AppTokenMW(env.db, PutConfig(env.db, env.cfg))
	bigPayload := strings.Repeat("x", 100)
	w := doAppTokenReq(t, h, "PUT", "/api/v1/apps/com.foo/config", "com.foo", tok,
		map[string]interface{}{"version": 0, "payload": bigPayload, "updated_by": "MBA"})
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("expected 413, got %d body=%s", w.Code, w.Body.String())
	}
}
