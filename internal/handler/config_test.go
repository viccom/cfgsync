package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/1remote/1remote-cloud/internal/auth"
	"github.com/1remote/1remote-cloud/internal/config"
	"github.com/1remote/1remote-cloud/internal/db"
	"github.com/1remote/1remote-cloud/internal/model"
)

func newTestServer(t *testing.T) (http.Handler, string) {
	t.Helper()
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.Migrate(d); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	cfg := &config.Config{
		Listen:     ":0",
		DBPath:     ":memory:",
		JWTSecret:  []byte("test-secret-test-secret-test-secret"),
		AccessTTL:  3600_000_000_000, // 1h
		RefreshTTL: 30 * 24 * 3600_000_000_000,
	}

	// Register a user directly via SQL so we have a known user_id.
	uid := "test-uid-1"
	hash, _ := auth.HashPassword("hunter2-correct-horse")
	if _, err := d.Exec(
		`INSERT INTO users (id, email, password_hash, created_at, updated_at) VALUES (?, ?, ?, 1, 1)`,
		uid, "test@example.com", hash); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/health", Health(d))
	mux.Handle("GET /api/v1/config", auth.Middleware(cfg.JWTSecret, GetConfig(d)))
	mux.Handle("PUT /api/v1/config", auth.Middleware(cfg.JWTSecret, PutConfig(d)))

	tok, _ := auth.IssueAccess(cfg.JWTSecret, uid, "test@example.com", cfg.AccessTTL)
	return mux, tok
}

func doJSON(t *testing.T, h http.Handler, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestConfig_Get_NewUser_ReturnsZeroVersion(t *testing.T) {
	h, _ := newTestServer(t)
	// Use a different uid that doesn't have a config row.
	secret := []byte("test-secret-test-secret-test-secret")
	tok, _ := auth.IssueAccess(secret, "no-such-uid", "x@y.z", 3600_000_000_000)
	w := doJSON(t, h, "GET", "/api/v1/config", tok, nil)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var c model.Config
	_ = json.Unmarshal(w.Body.Bytes(), &c)
	if c.Version != 0 {
		t.Errorf("expected version=0, got %d", c.Version)
	}
}

func TestConfig_Put_FirstTime_CreatesV1(t *testing.T) {
	h, tok := newTestServer(t)
	w := doJSON(t, h, "PUT", "/api/v1/config", tok, model.PutConfigRequest{
		Version: 0, Payload: `{"hello":"world"}`, UpdatedBy: "test",
	})
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"version":1`) {
		t.Errorf("expected version:1 in %s", w.Body.String())
	}
}

func TestConfig_Put_Conflict_Returns409(t *testing.T) {
	h, tok := newTestServer(t)
	// First write -> v1.
	w1 := doJSON(t, h, "PUT", "/api/v1/config", tok, model.PutConfigRequest{
		Version: 0, Payload: "a", UpdatedBy: "test",
	})
	if w1.Code != 200 {
		t.Fatalf("first write status=%d body=%s", w1.Code, w1.Body.String())
	}
	// Second write with stale version 0 -> 409.
	w2 := doJSON(t, h, "PUT", "/api/v1/config", tok, model.PutConfigRequest{
		Version: 0, Payload: "b", UpdatedBy: "test",
	})
	if w2.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d body=%s", w2.Code, w2.Body.String())
	}
	if !strings.Contains(w2.Body.String(), `"current_version":1`) {
		t.Errorf("expected current_version:1 in %s", w2.Body.String())
	}
}

func TestConfig_Put_HappyPath_VersionIncrements(t *testing.T) {
	h, tok := newTestServer(t)
	for i := int64(0); i < 3; i++ {
		w := doJSON(t, h, "PUT", "/api/v1/config", tok, model.PutConfigRequest{
			Version: i, Payload: "p", UpdatedBy: "test",
		})
		if w.Code != 200 {
			t.Fatalf("write %d status=%d body=%s", i, w.Code, w.Body.String())
		}
		var c model.Config
		_ = json.Unmarshal(w.Body.Bytes(), &c)
		if c.Version != i+1 {
			t.Errorf("write %d: expected version=%d, got %d", i, i+1, c.Version)
		}
	}
}

func TestConfig_Unauthorized(t *testing.T) {
	h, _ := newTestServer(t)
	w := doJSON(t, h, "GET", "/api/v1/config", "", nil)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}
