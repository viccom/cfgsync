package handler

import (
	"net/http"
	"strings"
	"testing"

	"github.com/1remote/1remote-cloud/internal/auth"
	"github.com/1remote/1remote-cloud/internal/model"
)

func adminChain(env *testEnv, h http.Handler) http.Handler {
	return auth.UserMW(env.cfg.JWTSecret, auth.AdminMW(h))
}

func TestAdminCreateApp_Success(t *testing.T) {
	env := newTestEnv(t)
	adminUID := env.seedUser(t, "admin@example.com", "p12345678", true)
	tok := env.userToken(adminUID, "admin@example.com", true)

	h := adminChain(env, AdminCreateApp(env.db))
	body := model.CreateAppRequest{AppID: "com.foo", DisplayName: "Foo"}
	w := doReq(t, h, "POST", "/api/v1/admin/apps", tok, body)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"app_id":"com.foo"`) {
		t.Errorf("expected app_id in %s", w.Body.String())
	}
}

func TestAdminCreateApp_RejectsNonAdmin(t *testing.T) {
	env := newTestEnv(t)
	uid := env.seedUser(t, "u@example.com", "p12345678", false)
	tok := env.userToken(uid, "u@example.com", false)

	h := adminChain(env, AdminCreateApp(env.db))
	body := model.CreateAppRequest{AppID: "com.foo", DisplayName: "Foo"}
	w := doReq(t, h, "POST", "/api/v1/admin/apps", tok, body)
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", w.Code)
	}
}

func TestAdminCreateApp_RejectsInvalidAppID(t *testing.T) {
	env := newTestEnv(t)
	adminUID := env.seedUser(t, "admin@example.com", "p12345678", true)
	tok := env.userToken(adminUID, "admin@example.com", true)

	h := adminChain(env, AdminCreateApp(env.db))
	body := model.CreateAppRequest{AppID: "invalid id with spaces", DisplayName: "X"}
	w := doReq(t, h, "POST", "/api/v1/admin/apps", tok, body)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestAdminCreateApp_Duplicate(t *testing.T) {
	env := newTestEnv(t)
	adminUID := env.seedUser(t, "admin@example.com", "p12345678", true)
	env.seedApp(t, "com.foo", "Foo", adminUID)
	tok := env.userToken(adminUID, "admin@example.com", true)

	h := adminChain(env, AdminCreateApp(env.db))
	body := model.CreateAppRequest{AppID: "com.foo", DisplayName: "Foo2"}
	w := doReq(t, h, "POST", "/api/v1/admin/apps", tok, body)
	if w.Code != http.StatusConflict {
		t.Errorf("expected 409, got %d", w.Code)
	}
}
