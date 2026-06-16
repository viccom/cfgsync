package handler

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/1remote/1remote-cloud/internal/auth"
)

// doGetApp dispatches a GET through h with the path {app_id} set, mimicking the mux.
func doGetApp(h http.Handler, appID, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", "/api/v1/apps/"+appID, &bytes.Buffer{})
	req.SetPathValue("app_id", appID)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestListApps_ReturnsAll(t *testing.T) {
	env := newTestEnv(t)
	adminUID := env.seedUser(t, "admin@example.com", "p12345678", true)
	env.seedApp(t, "com.foo", "Foo", adminUID)
	env.seedApp(t, "com.bar", "Bar", adminUID)

	uid := env.seedUser(t, "u@example.com", "p12345678", false)
	tok := env.userToken(uid, "u@example.com", false)

	h := auth.UserMW(env.cfg.JWTSecret, ListApps(env.db))
	w := doReq(t, h, "GET", "/api/v1/apps", tok, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `"app_id":"com.foo"`) {
		t.Errorf("expected com.foo in %s", body)
	}
	if !strings.Contains(body, `"app_id":"com.bar"`) {
		t.Errorf("expected com.bar in %s", body)
	}
}

func TestListApps_Empty(t *testing.T) {
	env := newTestEnv(t)
	uid := env.seedUser(t, "u@example.com", "p12345678", false)
	tok := env.userToken(uid, "u@example.com", false)

	h := auth.UserMW(env.cfg.JWTSecret, ListApps(env.db))
	w := doReq(t, h, "GET", "/api/v1/apps", tok, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	// Empty list should still return a JSON array, not null.
	if !strings.Contains(w.Body.String(), `"apps":[]`) {
		t.Errorf("expected empty apps array, got %s", w.Body.String())
	}
}

func TestGetApp_Success(t *testing.T) {
	env := newTestEnv(t)
	adminUID := env.seedUser(t, "admin@example.com", "p12345678", true)
	env.seedApp(t, "com.foo", "Foo", adminUID)

	uid := env.seedUser(t, "u@example.com", "p12345678", false)
	tok := env.userToken(uid, "u@example.com", false)

	h := auth.UserMW(env.cfg.JWTSecret, GetApp(env.db))
	w := doGetApp(h, "com.foo", tok)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestGetApp_NotFound(t *testing.T) {
	env := newTestEnv(t)
	uid := env.seedUser(t, "u@example.com", "p12345678", false)
	tok := env.userToken(uid, "u@example.com", false)

	h := auth.UserMW(env.cfg.JWTSecret, GetApp(env.db))
	w := doGetApp(h, "nonexistent", tok)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}
