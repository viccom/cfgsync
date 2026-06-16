package handler

import (
	"net/http"
	"strings"
	"testing"

	"github.com/viccom/cfgsync/internal/model"
)

func TestRegister_Success(t *testing.T) {
	env := newTestEnv(t)
	h := Register(env.db, env.cfg)
	w := doReq(t, h, "POST", "/api/v1/auth/register", "",
		model.RegisterRequest{Email: "new@example.com", Password: "password123"})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"access_token"`) {
		t.Errorf("expected access_token in response, got %s", w.Body.String())
	}
}

func TestRegister_DuplicateEmail(t *testing.T) {
	env := newTestEnv(t)
	env.seedUser(t, "dup@example.com", "password123", false)
	h := Register(env.db, env.cfg)
	w := doReq(t, h, "POST", "/api/v1/auth/register", "",
		model.RegisterRequest{Email: "dup@example.com", Password: "password123"})
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", w.Code)
	}
}

func TestRegister_WeakPassword(t *testing.T) {
	env := newTestEnv(t)
	h := Register(env.db, env.cfg)
	w := doReq(t, h, "POST", "/api/v1/auth/register", "",
		model.RegisterRequest{Email: "x@example.com", Password: "short"})
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestLogin_Success(t *testing.T) {
	env := newTestEnv(t)
	env.seedUser(t, "login@example.com", "password123", false)
	h := Login(env.db, env.cfg)
	w := doReq(t, h, "POST", "/api/v1/auth/login", "",
		model.LoginRequest{Email: "login@example.com", Password: "password123"})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestLogin_WrongPassword(t *testing.T) {
	env := newTestEnv(t)
	env.seedUser(t, "login@example.com", "password123", false)
	h := Login(env.db, env.cfg)
	w := doReq(t, h, "POST", "/api/v1/auth/login", "",
		model.LoginRequest{Email: "login@example.com", Password: "wrong"})
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestRefresh_Success(t *testing.T) {
	env := newTestEnv(t)
	uid := env.seedUser(t, "rf@example.com", "password123", false)

	now := nowUnix()
	rtID := "rt-test-id"
	if _, err := env.db.Exec(
		`INSERT INTO refresh_tokens (id, user_id, expires_at, created_at) VALUES (?, ?, ?, ?)`,
		rtID, uid, now+3600, now,
	); err != nil {
		t.Fatalf("seed rt: %v", err)
	}

	h := Refresh(env.db, env.cfg)
	w := doReq(t, h, "POST", "/api/v1/auth/refresh", "",
		model.RefreshRequest{RefreshToken: rtID})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}

	var revoked *int64
	env.db.QueryRow(`SELECT revoked_at FROM refresh_tokens WHERE id = ?`, rtID).Scan(&revoked)
	if revoked == nil {
		t.Errorf("expected old refresh token revoked")
	}
}
