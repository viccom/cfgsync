package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/viccom/cfgsync/internal/auth"
	"github.com/viccom/cfgsync/internal/model"
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

func TestAdminCreateApp_RejectsOversizedDisplayName(t *testing.T) {
	env := newTestEnv(t)
	adminUID := env.seedUser(t, "admin@example.com", "p12345678", true)
	tok := env.userToken(adminUID, "admin@example.com", true)

	h := adminChain(env, AdminCreateApp(env.db))
	body := model.CreateAppRequest{
		AppID:       "com.foo",
		DisplayName: strings.Repeat("x", 257),
	}
	w := doReq(t, h, "POST", "/api/v1/admin/apps", tok, body)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 on >256-char display_name, got %d", w.Code)
	}
}

func TestAdminCreateApp_RejectsOversizedDescription(t *testing.T) {
	env := newTestEnv(t)
	adminUID := env.seedUser(t, "admin@example.com", "p12345678", true)
	tok := env.userToken(adminUID, "admin@example.com", true)

	h := adminChain(env, AdminCreateApp(env.db))
	body := model.CreateAppRequest{
		AppID:       "com.foo",
		DisplayName: "Foo",
		Description: strings.Repeat("x", 1025),
	}
	w := doReq(t, h, "POST", "/api/v1/admin/apps", tok, body)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 on >1024-char description, got %d", w.Code)
	}
}

// --- helpers for path-scoped admin endpoints ---

func doAdminGet(h http.Handler, path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", path, &bytes.Buffer{})
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// doAdminAppPath dispatches a request whose path ends in /{app_id}, setting
// the {app_id} value the way Go 1.22 mux would. method+body control the rest.
func doAdminAppPath(h http.Handler, method, appID, token string, body interface{}) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, "/api/v1/admin/apps/"+appID, &buf)
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

func doAdminPromote(h http.Handler, targetUID, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/api/v1/admin/users/"+targetUID+"/promote", &bytes.Buffer{})
	req.SetPathValue("user_id", targetUID)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// --- AdminListApps ---

func TestAdminListApps_ReturnsAllWithCreatedBy(t *testing.T) {
	env := newTestEnv(t)
	adminUID := env.seedUser(t, "admin@example.com", "p12345678", true)
	env.seedApp(t, "com.foo", "Foo", adminUID)
	env.seedApp(t, "com.bar", "Bar", adminUID)
	tok := env.userToken(adminUID, "admin@example.com", true)

	h := adminChain(env, AdminListApps(env.db))
	w := doAdminGet(h, "/api/v1/admin/apps", tok)
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
	// Admin view must include created_by (user_id).
	if !strings.Contains(body, `"created_by":"`+adminUID+`"`) {
		t.Errorf("expected created_by=%s in %s", adminUID, body)
	}
}

func TestAdminListApps_RejectsNonAdmin(t *testing.T) {
	env := newTestEnv(t)
	uid := env.seedUser(t, "u@example.com", "p12345678", false)
	tok := env.userToken(uid, "u@example.com", false)

	h := adminChain(env, AdminListApps(env.db))
	w := doAdminGet(h, "/api/v1/admin/apps", tok)
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", w.Code)
	}
}

// --- AdminGetApp ---

func TestAdminGetApp_IncludesCreatedByEmail(t *testing.T) {
	env := newTestEnv(t)
	adminUID := env.seedUser(t, "admin@example.com", "p12345678", true)
	env.seedApp(t, "com.foo", "Foo", adminUID)
	tok := env.userToken(adminUID, "admin@example.com", true)

	h := adminChain(env, AdminGetApp(env.db))
	w := doAdminAppPath(h, "GET", "com.foo", tok, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `"created_by_email":"admin@example.com"`) {
		t.Errorf("expected created_by_email in %s", body)
	}
}

func TestAdminGetApp_NotFound(t *testing.T) {
	env := newTestEnv(t)
	adminUID := env.seedUser(t, "admin@example.com", "p12345678", true)
	tok := env.userToken(adminUID, "admin@example.com", true)

	h := adminChain(env, AdminGetApp(env.db))
	w := doAdminAppPath(h, "GET", "nonexistent", tok, nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

// --- AdminPatchApp ---

func TestAdminPatchApp_UpdatesDisplayName(t *testing.T) {
	env := newTestEnv(t)
	adminUID := env.seedUser(t, "admin@example.com", "p12345678", true)
	env.seedApp(t, "com.foo", "OldName", adminUID)
	tok := env.userToken(adminUID, "admin@example.com", true)

	newName := "NewName"
	h := adminChain(env, AdminPatchApp(env.db))
	w := doAdminAppPath(h, "PATCH", "com.foo", tok, model.PatchAppRequest{DisplayName: &newName})
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"display_name":"NewName"`) {
		t.Errorf("expected new name in %s", w.Body.String())
	}

	// description should be unchanged (still "" from seedApp).
	var got string
	env.db.QueryRow(`SELECT description FROM apps WHERE app_id = 'com.foo'`).Scan(&got)
	if got != "" {
		t.Errorf("description should be untouched, got %q", got)
	}
}

func TestAdminPatchApp_UpdatesDescriptionOnly(t *testing.T) {
	env := newTestEnv(t)
	adminUID := env.seedUser(t, "admin@example.com", "p12345678", true)
	env.seedApp(t, "com.foo", "Foo", adminUID)
	tok := env.userToken(adminUID, "admin@example.com", true)

	newDesc := "A description"
	h := adminChain(env, AdminPatchApp(env.db))
	w := doAdminAppPath(h, "PATCH", "com.foo", tok, model.PatchAppRequest{Description: &newDesc})
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var gotName, gotDesc string
	env.db.QueryRow(`SELECT display_name, description FROM apps WHERE app_id = 'com.foo'`).Scan(&gotName, &gotDesc)
	if gotName != "Foo" {
		t.Errorf("display_name should be untouched, got %q", gotName)
	}
	if gotDesc != "A description" {
		t.Errorf("description should be updated, got %q", gotDesc)
	}
}

func TestAdminPatchApp_EmptyBodyReturns400(t *testing.T) {
	env := newTestEnv(t)
	adminUID := env.seedUser(t, "admin@example.com", "p12345678", true)
	env.seedApp(t, "com.foo", "Foo", adminUID)
	tok := env.userToken(adminUID, "admin@example.com", true)

	h := adminChain(env, AdminPatchApp(env.db))
	w := doAdminAppPath(h, "PATCH", "com.foo", tok, map[string]interface{}{})
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 on empty patch, got %d", w.Code)
	}
}

func TestAdminPatchApp_NotFound(t *testing.T) {
	env := newTestEnv(t)
	adminUID := env.seedUser(t, "admin@example.com", "p12345678", true)
	tok := env.userToken(adminUID, "admin@example.com", true)

	newName := "X"
	h := adminChain(env, AdminPatchApp(env.db))
	w := doAdminAppPath(h, "PATCH", "nonexistent", tok, model.PatchAppRequest{DisplayName: &newName})
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

// --- AdminDeleteApp ---

func TestAdminDeleteApp_CascadesUserData(t *testing.T) {
	env := newTestEnv(t)
	adminUID := env.seedUser(t, "admin@example.com", "p12345678", true)
	env.seedApp(t, "com.foo", "Foo", adminUID)
	tok := env.userToken(adminUID, "admin@example.com", true)

	// Seed user data that should be wiped by cascade.
	uid := env.seedUser(t, "u@example.com", "p12345678", false)
	now := nowUnix()
	if _, err := env.db.Exec(
		`INSERT INTO configs (user_id, app_id, version, payload, updated_at, updated_by) VALUES (?, 'com.foo', 1, 'p', ?, 'me')`,
		uid, now,
	); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	if _, err := env.db.Exec(
		`INSERT INTO config_history (user_id, app_id, version, payload, updated_by, created_at) VALUES (?, 'com.foo', 1, 'p', 'me', ?)`,
		uid, now,
	); err != nil {
		t.Fatalf("seed history: %v", err)
	}
	if _, err := env.db.Exec(
		`INSERT INTO app_tokens (token_hash, token_prefix, user_id, app_id, label, created_at) VALUES ('h', '1rc_xxxxxxxx', ?, 'com.foo', '', ?)`,
		uid, now,
	); err != nil {
		t.Fatalf("seed token: %v", err)
	}

	h := adminChain(env, AdminDeleteApp(env.db))
	w := doAdminAppPath(h, "DELETE", "com.foo", tok, nil)
	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d body=%s", w.Code, w.Body.String())
	}

	// FK ON DELETE CASCADE should have wiped everything.
	for _, table := range []string{"apps", "configs", "config_history", "app_tokens"} {
		var n int
		env.db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE app_id = 'com.foo'`).Scan(&n)
		if n != 0 {
			t.Errorf("expected %s to be empty for com.foo after delete, got %d", table, n)
		}
	}
}

func TestAdminDeleteApp_NotFound(t *testing.T) {
	env := newTestEnv(t)
	adminUID := env.seedUser(t, "admin@example.com", "p12345678", true)
	tok := env.userToken(adminUID, "admin@example.com", true)

	h := adminChain(env, AdminDeleteApp(env.db))
	w := doAdminAppPath(h, "DELETE", "nonexistent", tok, nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

// --- AdminPromoteUser ---

func TestAdminPromoteUser_Success(t *testing.T) {
	env := newTestEnv(t)
	adminUID := env.seedUser(t, "admin@example.com", "p12345678", true)
	targetUID := env.seedUser(t, "victim@example.com", "p12345678", false)
	tok := env.userToken(adminUID, "admin@example.com", true)

	h := adminChain(env, AdminPromoteUser(env.db))
	w := doAdminPromote(h, targetUID, tok)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	var isAdmin int
	env.db.QueryRow(`SELECT is_admin FROM users WHERE id = ?`, targetUID).Scan(&isAdmin)
	if isAdmin != 1 {
		t.Errorf("expected target to be admin, got is_admin=%d", isAdmin)
	}
}

func TestAdminPromoteUser_Idempotent(t *testing.T) {
	env := newTestEnv(t)
	adminUID := env.seedUser(t, "admin@example.com", "p12345678", true)
	// Target is already admin.
	targetUID := env.seedUser(t, "second@example.com", "p12345678", true)
	tok := env.userToken(adminUID, "admin@example.com", true)

	h := adminChain(env, AdminPromoteUser(env.db))
	w := doAdminPromote(h, targetUID, tok)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 on idempotent promote, got %d", w.Code)
	}
}

func TestAdminPromoteUser_NotFound(t *testing.T) {
	env := newTestEnv(t)
	adminUID := env.seedUser(t, "admin@example.com", "p12345678", true)
	tok := env.userToken(adminUID, "admin@example.com", true)

	h := adminChain(env, AdminPromoteUser(env.db))
	w := doAdminPromote(h, "no-such-user-id", tok)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestAdminPromoteUser_RejectsNonAdmin(t *testing.T) {
	env := newTestEnv(t)
	uid := env.seedUser(t, "u@example.com", "p12345678", false)
	targetUID := env.seedUser(t, "v@example.com", "p12345678", false)
	tok := env.userToken(uid, "u@example.com", false)

	h := adminChain(env, AdminPromoteUser(env.db))
	w := doAdminPromote(h, targetUID, tok)
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", w.Code)
	}
}

// --- AdminListUsers ---

func TestAdminListUsers_ReturnsAll(t *testing.T) {
	env := newTestEnv(t)
	adminUID := env.seedUser(t, "admin@example.com", "p12345678", true)
	env.seedUser(t, "alice@example.com", "p12345678", false)
	env.seedUser(t, "bob@example.com", "p12345678", false)
	tok := env.userToken(adminUID, "admin@example.com", true)

	h := adminChain(env, AdminListUsers(env.db))
	w := doAdminGet(h, "/api/v1/admin/users", tok)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{`"email":"admin@example.com"`, `"email":"alice@example.com"`, `"email":"bob@example.com"`} {
		if !strings.Contains(body, want) {
			t.Errorf("expected %s in body, got %s", want, body)
		}
	}
	if strings.Contains(body, "password_hash") {
		t.Errorf("password_hash must never appear in admin user list: %s", body)
	}
}

func TestAdminListUsers_DefaultLimit(t *testing.T) {
	env := newTestEnv(t)
	adminUID := env.seedUser(t, "admin@example.com", "p12345678", true)
	tok := env.userToken(adminUID, "admin@example.com", true)

	h := adminChain(env, AdminListUsers(env.db))
	w := doAdminGet(h, "/api/v1/admin/users", tok)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"limit":20`) {
		t.Errorf("expected default limit 20, got %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"offset":0`) {
		t.Errorf("expected default offset 0, got %s", w.Body.String())
	}
}

func TestAdminListUsers_Pagination(t *testing.T) {
	env := newTestEnv(t)
	adminUID := env.seedUser(t, "admin@example.com", "p12345678", true)
	for i := 0; i < 25; i++ {
		env.seedUser(t, "u"+strconv.Itoa(i)+"@example.com", "p12345678", false)
	}
	tok := env.userToken(adminUID, "admin@example.com", true)

	h := adminChain(env, AdminListUsers(env.db))
	w := doAdminGet(h, "/api/v1/admin/users?limit=10&offset=10", tok)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Users  []model.AdminUserInfo `json:"users"`
		Limit  int                   `json:"limit"`
		Offset int                   `json:"offset"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v body=%s", err, w.Body.String())
	}
	if resp.Limit != 10 || resp.Offset != 10 {
		t.Errorf("expected limit=10 offset=10, got limit=%d offset=%d", resp.Limit, resp.Offset)
	}
	if len(resp.Users) != 10 {
		t.Errorf("expected 10 users in page, got %d", len(resp.Users))
	}
}

func TestAdminListUsers_RejectsNonAdmin(t *testing.T) {
	env := newTestEnv(t)
	uid := env.seedUser(t, "u@example.com", "p12345678", false)
	tok := env.userToken(uid, "u@example.com", false)

	h := adminChain(env, AdminListUsers(env.db))
	w := doAdminGet(h, "/api/v1/admin/users", tok)
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", w.Code)
	}
}
