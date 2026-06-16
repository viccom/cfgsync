# cfgsync WebUI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Embed a static Preact SPA in the Go binary, served from `/` on the same port, providing login/register, per-app token management, my-quota, and admin apps/users/promote pages. Add one new backend endpoint (`GET /api/v1/admin/users`). No payload display or editing.

**Architecture:** SPA built with Preact + htm + @preact/signals, committed as raw source under `internal/webui/dist/`. Embedded into the Go binary via `embed.FS` and served through a small handler that returns `index.html` for SPA fallback. Backend gains one admin endpoint. Auth uses existing user JWT + refresh token, stored in `localStorage`.

**Tech Stack:** Go 1.22+ (module pins 1.25.0), Preact 10, @preact/signals 1.2.x, htm 3.1.x (loaded as ES modules from esm.sh CDN), modernc.org/sqlite. No new Go module deps.

---

## File Structure

Files to be created or modified, in dependency order:

| File | Role | Created / Modified |
|---|---|---|
| `internal/model/user.go` | Add `AdminUserInfo` DTO | Modified |
| `internal/handler/admin.go` | Add `AdminListUsers` handler | Modified |
| `internal/handler/admin_test.go` | Add 4 tests for `AdminListUsers` | Modified |
| `internal/server/server.go` | Register `GET /api/v1/admin/users` | Modified |
| `internal/webui/webui.go` | `embed.FS` + SPA fallback handler | Created |
| `internal/webui/webui_test.go` | 4 tests for embedding/serve/fallback | Created |
| `internal/webui/dist/index.html` | HTML shell, mounts Preact app | Created |
| `internal/webui/dist/assets/app.css` | Light-theme variables + layout | Created |
| `internal/webui/dist/assets/app.js` | Preact components, router, API client, signals | Created |
| `CHANGELOG.md` | Add 0.3.0 entry | Modified |

No other files change. The Go module does not gain any runtime dependencies (Preact/htm/signals are browser-side, loaded from CDN).

## Task Dependency Graph

```
Task 1 (model) ─→ Task 2 (handler) ─→ Task 3 (tests) ─→ Task 4 (route) ─→ Task 5 (server-level test)
                                                                            ↓
Task 6 (embed skeleton) ─→ Task 7 (embed test) ─→ Task 8 (index.html) ─→ Task 9 (CSS) ─→ Task 10 (app.js)
                                                                                            ↓
                                                                                       Task 11 (wire /)
                                                                                            ↓
                                                                                       Task 12 (CHANGELOG)
                                                                                            ↓
                                                                                       Task 13 (manual smoke)
```

Tasks 1-5 form the backend change. Tasks 6-11 form the frontend change. Tasks 12-13 close the work. Tasks 1-5 can run in parallel with 6-11.

---

## Task 1: Add `AdminUserInfo` model DTO

**Files:**
- Modify: `internal/model/user.go` (add 1 struct)

- [ ] **Step 1: Modify `internal/model/user.go` to add the struct**

Append after the existing `User` struct:

```go
// AdminUserInfo is the public-safe user summary returned by admin listing endpoints.
// password_hash is never included.
type AdminUserInfo struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	IsAdmin   bool   `json:"is_admin"`
	CreatedAt int64  `json:"created_at"`
}
```

The full file becomes:

```go
package model

// User is the server-side representation of a registered user.
type User struct {
	ID           string `json:"id"`
	Email        string `json:"email"`
	PasswordHash string `json:"-"`
	IsAdmin      bool   `json:"is_admin"`
	CreatedAt    int64  `json:"created_at"`
	UpdatedAt    int64  `json:"updated_at"`
}

// AdminUserInfo is the public-safe user summary returned by admin listing endpoints.
// password_hash is never included.
type AdminUserInfo struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	IsAdmin   bool   `json:"is_admin"`
	CreatedAt int64  `json:"created_at"`
}
```

- [ ] **Step 2: Verify it compiles**

Run: `go build ./...`
Expected: exits 0, no errors.

- [ ] **Step 3: Commit**

```bash
git add internal/model/user.go
git commit -m "feat(model): add AdminUserInfo DTO for admin user listing"
```

---

## Task 2: Add `AdminListUsers` handler

**Files:**
- Modify: `internal/handler/admin.go` (append new handler at the end of the file)

- [ ] **Step 1: Append the handler to `internal/handler/admin.go`**

At the end of the file, after `AdminPromoteUser` (after its closing `}`), add:

```go
// AdminListUsers returns a paginated list of all users (admin only).
// Default limit 20, max 100. Never includes password_hash.
func AdminListUsers(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit <= 0 || limit > 100 {
			limit = 20
		}
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		if offset < 0 {
			offset = 0
		}

		rows, err := db.QueryContext(r.Context(),
			`SELECT id, email, is_admin, created_at FROM users
			  ORDER BY created_at DESC LIMIT ? OFFSET ?`, limit, offset)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal")
			return
		}
		defer rows.Close()

		users := make([]model.AdminUserInfo, 0, limit)
		for rows.Next() {
			var u model.AdminUserInfo
			var adm int
			if err := rows.Scan(&u.ID, &u.Email, &adm, &u.CreatedAt); err != nil {
				writeError(w, http.StatusInternalServerError, "internal")
				return
			}
			u.IsAdmin = adm == 1
			users = append(users, u)
		}
		if err := rows.Err(); err != nil {
			writeError(w, http.StatusInternalServerError, "internal")
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"users":  users,
			"limit":  limit,
			"offset": offset,
		})
	}
}
```

- [ ] **Step 2: Verify it compiles**

Run: `go build ./...`
Expected: exits 0.

- [ ] **Step 3: Commit**

```bash
git add internal/handler/admin.go
git commit -m "feat(handler): add AdminListUsers endpoint"
```

---

## Task 3: Add tests for `AdminListUsers`

**Files:**
- Modify: `internal/handler/admin_test.go` (append 4 new test functions at the end)

The existing helper `adminChain` (line 15) wires `UserMW + AdminMW` and `env.userToken(...)` mints a JWT. We reuse them.

- [ ] **Step 1: Append the 4 tests to `internal/handler/admin_test.go`**

```go
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
```

- [ ] **Step 2: Add the missing imports if not present**

The test file already imports `strings`, `net/http`, `encoding/json`, and the `model` and `auth` packages. The `Pagination` test uses `strconv.Itoa` — confirm `strconv` is imported. If not, add `"strconv"` to the import block.

Current imports (from the existing file, lines 1-13):
```go
import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/viccom/cfgsync/internal/auth"
	"github.com/viccom/cfgsync/internal/model"
)
```

Add `"strconv"` (alphabetical order in the std-lib block):

```go
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
```

- [ ] **Step 3: Run the new tests**

Run: `go test -race -count=1 -run TestAdminListUsers ./internal/handler/`
Expected: PASS (4 tests, all green).

- [ ] **Step 4: Run the full suite to confirm no regression**

Run: `go test -race -count=1 ./...`
Expected: PASS (73 existing tests + 4 new = 77 tests, all green).

- [ ] **Step 5: Commit**

```bash
git add internal/handler/admin_test.go
git commit -m "test(handler): cover AdminListUsers (default limit, pagination, non-admin, password_hash leak)"
```

---

## Task 4: Wire `GET /api/v1/admin/users` in the server

**Files:**
- Modify: `internal/server/server.go` (add 1 line in the admin block)

- [ ] **Step 1: Register the route**

In `internal/server/server.go`, inside the `adminChain` block (currently lines 38-46), add one new line after `AdminListApps`:

```go
mux.Handle("GET /api/v1/admin/apps", adminChain(handler.AdminListApps(db)))
mux.Handle("POST /api/v1/admin/apps", adminChain(handler.AdminCreateApp(db)))
mux.Handle("GET /api/v1/admin/apps/{app_id}", adminChain(handler.AdminGetApp(db)))
mux.Handle("PATCH /api/v1/admin/apps/{app_id}", adminChain(handler.AdminPatchApp(db)))
mux.Handle("DELETE /api/v1/admin/apps/{app_id}", adminChain(handler.AdminDeleteApp(db)))
mux.Handle("POST /api/v1/admin/users/{user_id}/promote", adminChain(handler.AdminPromoteUser(db)))
mux.Handle("GET /api/v1/admin/users", adminChain(handler.AdminListUsers(db)))
```

- [ ] **Step 2: Verify it compiles**

Run: `go build ./...`
Expected: exits 0.

- [ ] **Step 3: Re-run the suite**

Run: `go test -race -count=1 ./...`
Expected: PASS (77 tests, same as Task 3 step 4).

- [ ] **Step 4: Commit**

```bash
git add internal/server/server.go
git commit -m "feat(server): route GET /api/v1/admin/users"
```

---

## Task 5: Add a server-level integration test for the new route

**Files:**
- Modify: `internal/server/server_test.go` (append 1 test + 2 helpers)

This test exercises the full middleware chain and confirms a non-admin gets 403 and an admin gets 200.

- [ ] **Step 1: Inspect the existing `server_test.go` to learn the harness**

Read `internal/server/server_test.go` and locate the existing import block and any DB/token helpers. The plan assumes `openTempDB(t)` already exists in this file (it does, per `internal/db/bootstrap_test.go` which is in a different package — server_test.go likely has its own equivalent or one in the same package).

- [ ] **Step 2: Append the test**

```go
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

	h := server.New(cfg, env)

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
```

- [ ] **Step 3: Add helper functions used above**

Append at the bottom of `server_test.go`:

```go
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
```

- [ ] **Step 4: Confirm imports cover `bytes`, `database/sql`, `net/http/httptest`, `strings`, `time`, and the `auth`, `config`, `server` packages**

If any are missing, add them. Imports should look like:

```go
import (
	"bytes"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/viccom/cfgsync/internal/auth"
	"github.com/viccom/cfgsync/internal/config"
	"github.com/viccom/cfgsync/internal/server"
)
```

- [ ] **Step 5: Run the new test**

Run: `go test -race -count=1 -run TestServer_AdminListUsers_RouteIsWired ./internal/server/`
Expected: PASS.

- [ ] **Step 6: Run the full suite**

Run: `go test -race -count=1 ./...`
Expected: PASS (78 tests).

- [ ] **Step 7: Commit**

```bash
git add internal/server/server_test.go
git commit -m "test(server): integration test for GET /api/v1/admin/users (403/200 + no password_hash leak)"
```

---

## Task 6: Create the webui package with embed.FS and fallback handler

**Files:**
- Create: `internal/webui/webui.go`
- Create: `internal/webui/dist/index.html` (placeholder, real content in Task 8)
- Create: `internal/webui/dist/assets/.keep` (empty marker so the directory exists in git)

- [ ] **Step 1: Create the directory structure**

```bash
mkdir -p internal/webui/dist/assets
touch internal/webui/dist/assets/.keep
```

- [ ] **Step 2: Create `internal/webui/webui.go`**

```go
// Package webui embeds the static SPA into the Go binary and serves it
// with a fallback to index.html for client-side routing.
package webui

import (
	"embed"
	"io"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var distFS embed.FS

// Handler returns an http.Handler that serves the embedded SPA.
// - /               → dist/index.html
// - /assets/*       → files under dist/assets/ (if present in the embed)
// - anything else   → dist/index.html (SPA fallback for client-side routing)
// - /api/*          → NOT served here; mount the API handler separately.
func Handler() http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		// embed.FS.Sub only fails on programmer error; the dist/ path is hard-coded.
		panic("webui: fs.Sub: " + err.Error())
	}
	files := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			serveIndex(w, sub)
			return
		}
		// If the requested file exists in the embed, serve it.
		if _, err := fs.Stat(sub, path); err == nil {
			files.ServeHTTP(w, r)
			return
		}
		// Unknown path → SPA fallback so client-side routes work on direct hit / refresh.
		serveIndex(w, sub)
	})
}

func serveIndex(w http.ResponseWriter, sub fs.FS) {
	f, err := sub.Open("index.html")
	if err != nil {
		http.Error(w, "ui not built", http.StatusInternalServerError)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.Copy(w, f)
}
```

- [ ] **Step 3: Create a placeholder `internal/webui/dist/index.html`**

```html
<!doctype html>
<title>cfgsync</title>
```

- [ ] **Step 4: Verify it compiles**

Run: `go build ./...`
Expected: exits 0.

- [ ] **Step 5: Commit**

```bash
git add internal/webui/webui.go internal/webui/dist/index.html internal/webui/dist/assets/.keep
git commit -m "feat(webui): embed.FS skeleton with SPA fallback handler"
```

---

## Task 7: Add tests for the webui handler

**Files:**
- Create: `internal/webui/webui_test.go`

- [ ] **Step 1: Create the file**

```go
package webui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandler_ServesIndexOnRoot(t *testing.T) {
	h := Handler()
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("expected text/html, got %q", ct)
	}
	if !strings.Contains(strings.ToLower(w.Body.String()), "<!doctype html>") {
		t.Errorf("expected HTML doctype in body, got %s", w.Body.String())
	}
}

func TestHandler_ServesAssets(t *testing.T) {
	h := Handler()
	req := httptest.NewRequest("GET", "/assets/app.css", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	// app.css is added in Task 9; the test only asserts no 500 / panic.
	if w.Code == http.StatusInternalServerError {
		t.Errorf("expected non-500 status, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestHandler_SPAFallbackOnUnknownRoute(t *testing.T) {
	h := Handler()
	req := httptest.NewRequest("GET", "/some/unknown/route", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 (SPA fallback to index.html), got %d", w.Code)
	}
	if !strings.Contains(w.Header().Get("Content-Type"), "text/html") {
		t.Errorf("expected text/html for SPA fallback, got %q", w.Header().Get("Content-Type"))
	}
}

func TestHandler_DoesNotPanicOnAPIPath(t *testing.T) {
	h := Handler()
	req := httptest.NewRequest("GET", "/api/v1/health", nil)
	w := httptest.NewRecorder()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("handler panicked on /api/v1/health: %v", r)
		}
	}()
	h.ServeHTTP(w, req)
	_, _ = io.ReadAll(w.Body)
}
```

- [ ] **Step 2: Run the new tests**

Run: `go test -race -count=1 ./internal/webui/`
Expected: PASS (4 tests).

- [ ] **Step 3: Run the full suite to confirm no regression**

Run: `go test -race -count=1 ./...`
Expected: PASS (78 backend tests + 4 webui tests = 82 tests).

- [ ] **Step 4: Commit**

```bash
git add internal/webui/webui_test.go
git commit -m "test(webui): cover index.html, asset serve, SPA fallback, no-panic on /api/*"
```

---

## Task 8: Replace the placeholder `index.html` with the real HTML shell

**Files:**
- Modify: `internal/webui/dist/index.html` (full rewrite)

- [ ] **Step 1: Write the full `index.html`**

```html
<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <title>cfgsync</title>
  <link rel="stylesheet" href="/assets/app.css" />
</head>
<body>
  <div id="app"></div>
  <script type="module" src="/assets/app.js"></script>
</body>
</html>
```

- [ ] **Step 2: Verify the file is reachable from the handler**

```bash
go test -race -count=1 -run TestHandler_ServesIndexOnRoot ./internal/webui/
```

Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/webui/dist/index.html
git commit -m "feat(webui): real index.html with ES module entry"
```

---

## Task 9: Add `app.css` with the light theme

**Files:**
- Create: `internal/webui/dist/assets/app.css`

- [ ] **Step 1: Create the file**

```css
/* Light theme — single set, no toggle. */
:root {
  --bg: #ffffff;
  --bg-elevated: #f7f8fa;
  --bg-input: #ffffff;
  --border: #e4e7eb;
  --border-strong: #c5cbd3;
  --text: #1f2328;
  --text-muted: #6a737d;
  --primary: #2563eb;
  --primary-hover: #1d4ed8;
  --primary-text: #ffffff;
  --danger: #dc2626;
  --danger-hover: #b91c1c;
  --success: #16a34a;
  --warning-bg: #fef3c7;
  --warning-border: #f59e0b;
  --warning-text: #92400e;
  --radius: 6px;
  --shadow: 0 1px 3px rgba(0, 0, 0, 0.08);
  --font: -apple-system, BlinkMacSystemFont, "Segoe UI", "PingFang SC",
    "Hiragino Sans GB", "Microsoft YaHei", sans-serif;
  --mono: ui-monospace, SFMono-Regular, "SF Mono", Menlo, Consolas, monospace;
}

* {
  box-sizing: border-box;
}

html,
body {
  margin: 0;
  padding: 0;
  background: var(--bg);
  color: var(--text);
  font-family: var(--font);
  font-size: 14px;
  line-height: 1.5;
}

#app {
  min-height: 100vh;
  display: flex;
  flex-direction: column;
}

/* --- top nav --- */
.nav {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 12px 24px;
  border-bottom: 1px solid var(--border);
  background: var(--bg);
  position: sticky;
  top: 0;
  z-index: 10;
}
.nav-brand {
  font-weight: 600;
  font-size: 16px;
  color: var(--text);
  text-decoration: none;
}
.nav-right {
  display: flex;
  gap: 16px;
  align-items: center;
  font-size: 13px;
}
.nav-right a,
.nav-right button {
  color: var(--text-muted);
  text-decoration: none;
  background: none;
  border: none;
  cursor: pointer;
  font: inherit;
  padding: 0;
}
.nav-right a:hover,
.nav-right button:hover {
  color: var(--primary);
}

/* --- main --- */
.main {
  max-width: 960px;
  width: 100%;
  margin: 0 auto;
  padding: 24px;
  flex: 1;
}
h1 {
  font-size: 22px;
  margin: 0 0 16px;
}
h2 {
  font-size: 16px;
  margin: 24px 0 12px;
}

/* --- card --- */
.card {
  background: var(--bg-elevated);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  padding: 12px;
  box-shadow: var(--shadow);
  margin-bottom: 12px;
}
.card-title {
  font-weight: 600;
  font-size: 15px;
  margin-bottom: 4px;
}
.card-meta {
  color: var(--text-muted);
  font-size: 12px;
  font-family: var(--mono);
}

/* --- grid for app cards --- */
.grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
  gap: 12px;
}

/* --- forms --- */
.form {
  display: flex;
  flex-direction: column;
  gap: 8px;
  max-width: 360px;
}
label {
  font-size: 13px;
  color: var(--text-muted);
}
input,
textarea,
select {
  background: var(--bg-input);
  border: 1px solid var(--border-strong);
  border-radius: var(--radius);
  padding: 8px 10px;
  font: inherit;
  color: var(--text);
  width: 100%;
}
input:focus,
textarea:focus,
select:focus {
  outline: 2px solid var(--primary);
  outline-offset: -1px;
}
.field-error {
  color: var(--danger);
  font-size: 12px;
  margin-top: 2px;
}

/* --- buttons --- */
.btn {
  display: inline-block;
  padding: 8px 16px;
  border-radius: var(--radius);
  border: 1px solid var(--border-strong);
  background: var(--bg);
  color: var(--text);
  cursor: pointer;
  font: inherit;
  text-decoration: none;
  text-align: center;
}
.btn:hover {
  background: var(--bg-elevated);
}
.btn-primary {
  background: var(--primary);
  border-color: var(--primary);
  color: var(--primary-text);
}
.btn-primary:hover {
  background: var(--primary-hover);
}
.btn-danger {
  background: var(--bg);
  border-color: var(--danger);
  color: var(--danger);
}
.btn-danger:hover {
  background: var(--danger);
  color: var(--primary-text);
}
.btn-row {
  display: flex;
  gap: 8px;
  align-items: center;
}

/* --- table --- */
table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}
th,
td {
  text-align: left;
  padding: 8px 12px;
  border-bottom: 1px solid var(--border);
}
th {
  color: var(--text-muted);
  font-weight: 500;
}
tr:last-child td {
  border-bottom: none;
}

/* --- toast --- */
.toast {
  position: fixed;
  top: 16px;
  left: 50%;
  transform: translateX(-50%);
  padding: 10px 16px;
  border-radius: var(--radius);
  background: var(--text);
  color: var(--bg);
  box-shadow: var(--shadow);
  z-index: 100;
  max-width: 480px;
  font-size: 13px;
}
.toast-ok {
  background: var(--success);
  color: #fff;
}
.toast-err {
  background: var(--danger);
  color: #fff;
}
.toast-info {
  background: var(--text);
  color: var(--bg);
}

/* --- warning banner (for ShowToken) --- */
.warning {
  background: var(--warning-bg);
  border: 1px solid var(--warning-border);
  color: var(--warning-text);
  padding: 12px;
  border-radius: var(--radius);
  margin-bottom: 16px;
  font-size: 13px;
}

/* --- code/token display --- */
.code {
  font-family: var(--mono);
  background: var(--bg-elevated);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  padding: 12px;
  word-break: break-all;
  user-select: all;
  font-size: 13px;
}

/* --- empty / loading --- */
.muted {
  color: var(--text-muted);
  text-align: center;
  padding: 24px;
}
.error-text {
  color: var(--danger);
  font-size: 13px;
}

/* --- responsive --- */
@media (max-width: 640px) {
  .main {
    padding: 16px;
  }
  table,
  thead,
  tbody,
  th,
  td,
  tr {
    display: block;
  }
  thead {
    display: none;
  }
  tr {
    border: 1px solid var(--border);
    border-radius: var(--radius);
    margin-bottom: 8px;
    padding: 8px;
  }
  td {
    border: none;
    padding: 4px 0;
  }
  td::before {
    content: attr(data-label) "：";
    color: var(--text-muted);
    font-size: 12px;
  }
}
```

- [ ] **Step 2: Verify the asset is served**

Run: `go test -race -count=1 -run TestHandler_ServesAssets ./internal/webui/`
Expected: PASS (200 with `text/css` content-type).

- [ ] **Step 3: Commit**

```bash
git add internal/webui/dist/assets/app.css
git commit -m "feat(webui): light-theme CSS with custom properties"
```

---

## Task 10: Add `app.js` — the Preact SPA

**Files:**
- Create: `internal/webui/dist/assets/app.js`

This is the largest single file (~700 LOC). The full content is provided below; do NOT abbreviate.

- [ ] **Step 1: Create the file with the content below**

```js
// cfgsync WebUI — single-file Preact SPA. No build step.

import { h, render } from 'https://esm.sh/preact@10.22.0';
import { useState, useEffect } from 'https://esm.sh/preact@10.22.0/hooks';
import { signal, computed } from 'https://esm.sh/@preact/signals@1.2.3';
import htm from 'https://esm.sh/htm@3.1.1';

const html = htm.bind(h);

// ============================================================
// constants
// ============================================================
const LS_JWT = 'cfgsync_jwt';
const LS_REFRESH = 'cfgsync_refresh';

const ERR_MSGS = {
  invalid_json: '请求格式错误',
  invalid_email_or_password: '邮箱或密码格式不正确（密码至少 8 位）',
  invalid_credentials: '邮箱或密码不正确',
  invalid_token: '登录已失效，请重新登录',
  invalid_refresh_token: '会话已过期，请重新登录',
  unauthorized: '请先登录',
  forbidden: '权限不足',
  not_found: '资源不存在',
  email_already_registered: '该邮箱已注册',
  invalid_app_id: 'app_id 格式错误（应为反域格式，如 com.example.app）',
  app_id_exists: '该 app_id 已被注册',
  app_token_limit_reached: '已达 app_token 上限',
  payload_too_large: '配置超过 4 MB',
  storage_quota_exceeded: '存储配额已满',
  version_conflict: '配置已被其他设备更新',
  internal: '服务器内部错误，请稍后重试',
};

// ============================================================
// signals (global state)
// ============================================================
const jwtSignal = signal(localStorage.getItem(LS_JWT) || null);
const refreshSignal = signal(localStorage.getItem(LS_REFRESH) || null);
const userSignal = computed(() => (jwtSignal.value ? decodeJwt(jwtSignal.value) : null));
const toastSignal = signal(null);
const routeSignal = signal(parseLocation());

window.addEventListener('popstate', () => {
  routeSignal.value = parseLocation();
});

function parseLocation() {
  const path = location.pathname || '/';
  const segments = path.split('/').filter(Boolean);
  return { path, segments };
}

function navigate(to) {
  if (location.pathname !== to) {
    history.pushState({}, '', to);
    routeSignal.value = parseLocation();
  }
}

// ============================================================
// api client
// ============================================================
async function call(method, path, body, opts = {}) {
  const headers = { 'Content-Type': 'application/json' };
  const t = jwtSignal.value;
  if (t) headers['Authorization'] = `Bearer ${t}`;
  const doFetch = async () => {
    const res = await fetch(path, {
      method,
      headers,
      body: body !== undefined ? JSON.stringify(body) : undefined,
    });
    const text = await res.text();
    let data = null;
    try { data = text ? JSON.parse(text) : null; } catch {}
    if (!res.ok) {
      const code = data?.error || `http_${res.status}`;
      const err = new Error(ERR_MSGS[code] || code);
      err.status = res.status;
      err.body = data;
      throw err;
    }
    return data;
  };

  try {
    return await doFetch();
  } catch (err) {
    if (err.status === 401 && !opts._retried && isIdempotent(method)) {
      const refreshed = await tryRefresh();
      if (refreshed) {
        opts._retried = true;
        return await doFetch();
      }
    }
    throw err;
  }
}

function isIdempotent(method) {
  return method === 'GET' || method === 'DELETE' || method === 'PUT' || method === 'HEAD';
}

async function tryRefresh() {
  const r = refreshSignal.value;
  if (!r) return false;
  try {
    const res = await fetch('/api/v1/auth/refresh', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ refresh_token: r }),
    });
    const data = await res.json();
    if (!res.ok) return false;
    localStorageStorage: localStorage.setItem(LS_JWT, data.access_token);
    localStorage.setItem(LS_REFRESH, data.refresh_token);
    jwtSignal.value = data.access_token;
    refreshSignal.value = data.refresh_token;
    return true;
  } catch {
    return false;
  }
}

function logout() {
  const r = refreshSignal.value;
  if (r) {
    fetch('/api/v1/auth/logout', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ refresh_token: r }),
    }).catch(() => {});
  }
  localStorage.removeItem(LS_JWT);
  localStorage.removeItem(LS_REFRESH);
  jwtSignal.value = null;
  refreshSignal.value = null;
  navigate('/login');
}

function decodeJwt(jwt) {
  try {
    const parts = jwt.split('.');
    if (parts.length !== 3) return null;
    const payload = JSON.parse(atob(parts[1].replace(/-/g, '+').replace(/_/g, '/')));
    return { id: payload.uid, email: payload.email, is_admin: !!payload.adm, exp: payload.exp };
  } catch {
    return null;
  }
}

function showToast(kind, text, ttl = 3000) {
  toastSignal.value = { kind, text, ttl, _id: Date.now() + Math.random() };
  if (ttl > 0) {
    setTimeout(() => {
      if (toastSignal.value && toastSignal.value.text === text) toastSignal.value = null;
    }, ttl);
  }
}

// ============================================================
// top-level <App>
// ============================================================
function App() {
  const { path, segments } = routeSignal.value;
  const jwt = jwtSignal.value;
  const user = userSignal.value;

  if (!jwt && path !== '/login' && path !== '/register') {
    queueMicrotask(() => navigate('/login'));
    return html`<${Loading} />`;
  }
  if (jwt && segments[0] === 'admin' && !(user && user.is_admin)) {
    queueMicrotask(() => {
      showToast('err', '需要管理员权限');
      navigate('/apps');
    });
    return html`<${Loading} />`;
  }

  return html`
    <${Nav} />
    <main class="main">
      ${renderRoute(path, segments)}
    </main>
    <${Toast} />
  `;
}

function renderRoute(path, segments) {
  if (path === '/' || path === '/login') return html`<${Login} tab="login" />`;
  if (path === '/register') return html`<${Login} tab="register" />`;
  if (path === '/apps') return html`<${MyApps} />`;
  if (segments[0] === 'apps' && segments[1]) return html`<${AppDetail} appId=${segments[1]} />`;
  if (path === '/me') return html`<${MyQuota} />`;
  if (path === '/me/settings') return html`<${MySettings} />`;
  if (path === '/admin/apps') return html`<${AdminApps} />`;
  if (segments[0] === 'admin' && segments[1] === 'apps' && segments[2] === 'new')
    return html`<${AdminAppEdit} mode="new" />`;
  if (segments[0] === 'admin' && segments[1] === 'apps' && segments[2])
    return html`<${AdminAppEdit} mode="edit" appId=${segments[2]} />`;
  if (path === '/admin/users') return html`<${AdminUsers} />`;
  if (segments[0] === 'show-token') {
    const id = segments[1] || '';
    const token = decodeURIComponent(segments.slice(2).join('/'));
    return html`<${ShowToken} appId=${id} token=${token} />`;
  }
  return html`<${NotFound} />`;
}

// ============================================================
// components
// ============================================================
function Nav() {
  const user = userSignal.value;
  return html`
    <nav class="nav">
      <a class="nav-brand" href="/apps" onClick=${(e) => { e.preventDefault(); navigate('/apps'); }}>cfgsync</a>
      <div class="nav-right">
        ${user && html`
          <span>${user.email}</span>
          ${user.is_admin && html`
            <a href="/admin/apps" onClick=${(e) => { e.preventDefault(); navigate('/admin/apps'); }}>应用管理</a>
            <a href="/admin/users" onClick=${(e) => { e.preventDefault(); navigate('/admin/users'); }}>用户管理</a>
          `}
          <a href="/me" onClick=${(e) => { e.preventDefault(); navigate('/me'); }}>配额</a>
          <a href="/me/settings" onClick=${(e) => { e.preventDefault(); navigate('/me/settings'); }}>设置</a>
          <button onClick=${logout}>退出</button>
        `}
      </div>
    </nav>
  `;
}

function Loading() {
  return html`<main class="main"><p class="muted">加载中…</p></main>`;
}

function NotFound() {
  return html`
    <main class="main">
      <h1>页面不存在</h1>
      <p><a href="/apps" onClick=${(e) => { e.preventDefault(); navigate('/apps'); }}>返回首页</a></p>
    </main>
  `;
}

function Toast() {
  const t = toastSignal.value;
  if (!t) return null;
  return html`<div class=${'toast toast-' + t.kind}>${t.text}</div>`;
}

function Login({ tab }) {
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState(null);
  const isLogin = tab === 'login';

  const submit = async (e) => {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    try {
      const data = await call('POST', isLogin ? '/api/v1/auth/login' : '/api/v1/auth/register', {
        email, password,
      });
      localStorage.setItem(LS_JWT, data.access_token);
      localStorage.setItem(LS_REFRESH, data.refresh_token);
      jwtSignal.value = data.access_token;
      refreshSignal.value = data.refresh_token;
      const u = userSignal.value;
      navigate(u && u.is_admin ? '/admin/apps' : '/apps');
    } catch (e) {
      setErr(e.message);
    } finally {
      setBusy(false);
    }
  };

  return html`
    <h1>${isLogin ? '登录' : '注册'}</h1>
    <form class="form" onSubmit=${submit}>
      <div>
        <label>邮箱</label>
        <input type="email" required value=${email} onInput=${(e) => setEmail(e.target.value)} />
      </div>
      <div>
        <label>密码（至少 8 位）</label>
        <input type="password" required minLength="8" value=${password} onInput=${(e) => setPassword(e.target.value)} />
      </div>
      ${err && html`<div class="field-error">${err}</div>`}
      <button class="btn btn-primary" type="submit" disabled=${busy}>${busy ? '处理中…' : (isLogin ? '登录' : '注册')}</button>
      <p class="muted" style="font-size:12px">
        ${isLogin
          ? html`还没账号？<a href="/register" onClick=${(e) => { e.preventDefault(); navigate('/register'); }}>去注册</a>`
          : html`已有账号？<a href="/login" onClick=${(e) => { e.preventDefault(); navigate('/login'); }}>去登录</a>`}
      </p>
    </form>
  `;
}

function MyApps() {
  const [apps, setApps] = useState(null);
  const [err, setErr] = useState(null);
  useEffect(() => {
    call('GET', '/api/v1/apps')
      .then((d) => setApps(d.apps || []))
      .catch((e) => setErr(e.message));
  }, []);

  if (err) return html`<p class="error-text">${err}</p>`;
  if (apps === null) return html`<p class="muted">加载中…</p>`;
  if (apps.length === 0) return html`<p class="muted">暂无可用应用。请联系管理员注册 app_id。</p>`;

  return html`
    <h1>我的应用</h1>
    <div class="grid">
      ${apps.map((a) => html`
        <div class="card" key=${a.app_id}>
          <div class="card-title">${a.display_name}</div>
          <div class="card-meta">${a.app_id}</div>
          <p>${a.description || ''}</p>
          <a class="btn" href=${'/apps/' + a.app_id} onClick=${(e) => { e.preventDefault(); navigate('/apps/' + a.app_id); }}>管理 Token</a>
        </div>
      `)}
    </div>
  `;
}

function AppDetail({ appId }) {
  const [tokens, setTokens] = useState(null);
  const [err, setErr] = useState(null);
  const [label, setLabel] = useState('');
  const [confirmDel, setConfirmDel] = useState(null);
  const [busy, setBusy] = useState(false);

  const load = () => {
    setTokens(null);
    setErr(null);
    call('GET', '/api/v1/me/tokens')
      .then((d) => setTokens((d.tokens || []).filter((t) => t.app_id === appId)))
      .catch((e) => setErr(e.message));
  };
  useEffect(load, [appId]);

  const create = async (e) => {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    try {
      const data = await call('POST', `/api/v1/me/apps/${appId}/token`, { label });
      navigate('/show-token/' + appId + '/' + encodeURIComponent(data.token));
    } catch (e) {
      setErr(e.message);
    } finally {
      setBusy(false);
    }
  };

  const revoke = async (prefix) => {
    setBusy(true);
    setErr(null);
    try {
      await call('DELETE', `/api/v1/me/tokens/${prefix}`);
      showToast('ok', '已撤销');
      setConfirmDel(null);
      load();
    } catch (e) {
      setErr(e.message);
    } finally {
      setBusy(false);
    }
  };

  return html`
    <h1>管理 Token</h1>
    <p class="card-meta">${appId}</p>

    <h2>新建 Token</h2>
    <form class="form" onSubmit=${create}>
      <div>
        <label>标签（可选）</label>
        <input value=${label} onInput=${(e) => setLabel(e.target.value)} placeholder="例如：MacBook Air" />
      </div>
      ${err && html`<div class="field-error">${err}</div>`}
      <button class="btn btn-primary" type="submit" disabled=${busy}>${busy ? '生成中…' : '生成新 Token'}</button>
    </form>

    <h2>我名下的 Token</h2>
    ${tokens === null && html`<p class="muted">加载中…</p>`}
    ${tokens && tokens.length === 0 && html`<p class="muted">还没有 Token。生成一个用于同步软件。</p>`}
    ${tokens && tokens.length > 0 && html`
      <table>
        <thead><tr><th>标签</th><th>前缀</th><th>创建时间</th><th>最后使用</th><th></th></tr></thead>
        <tbody>
          ${tokens.map((t) => html`
            <tr key=${t.token_prefix}>
              <td data-label="标签">${t.label || '未命名'}</td>
              <td data-label="前缀"><code>${t.token_prefix}…</code></td>
              <td data-label="创建时间">${fmtTime(t.created_at)}</td>
              <td data-label="最后使用">${t.last_used_at ? fmtTime(t.last_used_at) : '从未'}</td>
              <td data-label="操作">
                ${confirmDel === t.token_prefix
                  ? html`<span class="btn-row">确认撤销？<button class="btn btn-danger" onClick=${() => revoke(t.token_prefix)}>确认</button><button class="btn" onClick=${() => setConfirmDel(null)}>取消</button></span>`
                  : html`<button class="btn btn-danger" onClick=${() => setConfirmDel(t.token_prefix)}>撤销</button>`}
              </td>
            </tr>
          `)}
        </tbody>
      </table>
    `}
  `;
}

function ShowToken({ appId, token }) {
  useEffect(() => {
    const guard = (e) => { e.preventDefault(); e.returnValue = ''; };
    window.addEventListener('beforeunload', guard);
    return () => window.removeEventListener('beforeunload', guard);
  }, []);

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(token);
      showToast('ok', '已复制到剪贴板');
    } catch {
      showToast('err', '复制失败，请手动选择');
    }
  };

  return html`
    <h1>新 Token</h1>
    <div class="warning">⚠️ 请立即复制此 token。离开此页面后将无法再看到完整 token。</div>
    <p class="card-meta">app_id: ${appId}</p>
    <div class="code" onClick=${(e) => { e.target.select && e.target.select(); }}>${token}</div>
    <div class="btn-row" style="margin-top:16px">
      <button class="btn btn-primary" onClick=${copy}>复制</button>
      <a class="btn" href=${'/apps/' + appId} onClick=${(e) => { e.preventDefault(); navigate('/apps/' + appId); }}>我已保存，去应用详情</a>
    </div>
  `;
}

function MyQuota() {
  const [quota, setQuota] = useState(null);
  const [err, setErr] = useState(null);
  useEffect(() => {
    call('GET', '/api/v1/me/quota')
      .then(setQuota)
      .catch((e) => setErr(e.message));
  }, []);
  if (err) return html`<p class="error-text">${err}</p>`;
  if (!quota) return html`<p class="muted">加载中…</p>`;

  const used = quota.used_bytes || 0;
  const limit = quota.limit_bytes || 1;
  const pct = Math.min(100, Math.round((used / limit) * 100));
  return html`
    <h1>我的配额</h1>
    <div class="card">
      <div>已用 ${humanBytes(used)} / ${humanBytes(limit)}（${pct}%）</div>
      <div style="background:var(--border);height:8px;border-radius:4px;margin-top:8px;overflow:hidden">
        <div style=${`background:var(--primary);height:100%;width:${pct}%`}></div>
      </div>
    </div>
    <h2>各应用占用</h2>
    ${quota.per_app && quota.per_app.length > 0
      ? html`
        <table>
          <thead><tr><th>app_id</th><th>大小</th><th>最后更新</th><th></th></tr></thead>
          <tbody>
            ${quota.per_app.map((p) => html`
              <tr key=${p.app_id}>
                <td data-label="app_id"><code>${p.app_id}</code></td>
                <td data-label="大小">${humanBytes(p.bytes)}</td>
                <td data-label="最后更新">${fmtTime(p.updated_at)}</td>
                <td data-label="操作"><a class="btn" href=${'/apps/' + p.app_id} onClick=${(e) => { e.preventDefault(); navigate('/apps/' + p.app_id); }}>管理</a></td>
              </tr>
            `)}
          </tbody>
        </table>
      `
      : html`<p class="muted">还没有任何 app 的配置数据。</p>`}
  `;
}

function MySettings() {
  return html`
    <h1>设置</h1>
    <p class="muted">修改密码功能尚未提供。如需修改请联系管理员。</p>
    <h2>退出登录</h2>
    <button class="btn btn-danger" onClick=${logout}>退出</button>
  `;
}

function AdminApps() {
  const [apps, setApps] = useState(null);
  const [err, setErr] = useState(null);
  const load = () => {
    setApps(null);
    call('GET', '/api/v1/admin/apps')
      .then((d) => setApps(d.apps || []))
      .catch((e) => setErr(e.message));
  };
  useEffect(load, []);
  if (err) return html`<p class="error-text">${err}</p>`;
  if (!apps) return html`<p class="muted">加载中…</p>`;
  return html`
    <div class="btn-row" style="justify-content:space-between;margin-bottom:16px">
      <h1 style="margin:0">应用管理</h1>
      <button class="btn btn-primary" onClick=${() => navigate('/admin/apps/new')}>新建应用</button>
    </div>
    ${apps.length === 0
      ? html`<p class="muted">还没有任何应用。</p>`
      : html`
        <table>
          <thead><tr><th>app_id</th><th>显示名</th><th>描述</th><th>创建时间</th><th></th></tr></thead>
          <tbody>
            ${apps.map((a) => html`
              <tr key=${a.app_id}>
                <td data-label="app_id"><code>${a.app_id}</code></td>
                <td data-label="显示名">${a.display_name}</td>
                <td data-label="描述">${a.description || ''}</td>
                <td data-label="创建时间">${fmtTime(a.created_at)}</td>
                <td data-label="操作">
                  <a class="btn" href=${'/admin/apps/' + a.app_id} onClick=${(e) => { e.preventDefault(); navigate('/admin/apps/' + a.app_id); }}>编辑</a>
                </td>
              </tr>
            `)}
          </tbody>
        </table>
      `}
  `;
}

function AdminAppEdit({ mode, appId }) {
  const isNew = mode === 'new';
  const [appId_, setAppId] = useState(appId || '');
  const [displayName, setDisplayName] = useState('');
  const [description, setDescription] = useState('');
  const [confirmDel, setConfirmDel] = useState(false);
  const [err, setErr] = useState(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (isNew) return;
    call('GET', `/api/v1/admin/apps/${appId}`)
      .then((a) => { setDisplayName(a.display_name); setDescription(a.description || ''); })
      .catch((e) => {
        if (e.status === 404) {
          showToast('err', '应用已被删除');
          navigate('/admin/apps');
        } else {
          setErr(e.message);
        }
      });
  }, [appId]);

  const submit = async (e) => {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    try {
      if (isNew) {
        await call('POST', '/api/v1/admin/apps', { app_id: appId_, display_name: displayName, description });
        showToast('ok', '已创建');
        navigate('/admin/apps');
      } else {
        await call('PATCH', `/api/v1/admin/apps/${appId}`, { display_name: displayName, description });
        showToast('ok', '已保存');
        navigate('/admin/apps');
      }
    } catch (e) {
      setErr(e.message);
    } finally {
      setBusy(false);
    }
  };

  const del = async () => {
    setBusy(true);
    setErr(null);
    try {
      await call('DELETE', `/api/v1/admin/apps/${appId}`);
      showToast('ok', '已删除');
      navigate('/admin/apps');
    } catch (e) {
      setErr(e.message);
    } finally {
      setBusy(false);
    }
  };

  return html`
    <h1>${isNew ? '新建应用' : '编辑应用'}</h1>
    <form class="form" onSubmit=${submit}>
      <div>
        <label>app_id（反域格式，如 com.example.app）</label>
        <input
          value=${appId_}
          onInput=${(e) => setAppId(e.target.value)}
          disabled=${!isNew}
          required
          pattern="^([a-z0-9][a-z0-9-]{1,30}\\.)+[a-z0-9][a-z0-9-]{1,30}$"
        />
      </div>
      <div>
        <label>显示名（必填，≤ 256 字符）</label>
        <input value=${displayName} onInput=${(e) => setDisplayName(e.target.value)} required maxLength="256" />
      </div>
      <div>
        <label>描述（≤ 1024 字符）</label>
        <textarea rows="3" value=${description} onInput=${(e) => setDescription(e.target.value)} maxLength="1024"></textarea>
      </div>
      ${err && html`<div class="field-error">${err}</div>`}
      <button class="btn btn-primary" type="submit" disabled=${busy}>${busy ? '保存中…' : '保存'}</button>
    </form>
    ${!isNew && html`
      <h2>删除应用</h2>
      ${confirmDel
        ? html`
          <div class="warning">确认删除 ${appId}？这会级联删除所有用户在此 app 下的所有数据（config / 历史 / token），无法恢复。</div>
          <div class="btn-row">
            <button class="btn btn-danger" onClick=${del} disabled=${busy}>确认删除</button>
            <button class="btn" onClick=${() => setConfirmDel(false)}>取消</button>
          </div>
        `
        : html`<button class="btn btn-danger" onClick=${() => setConfirmDel(true)}>删除此应用</button>`}
    `}
  `;
}

function AdminUsers() {
  const [data, setData] = useState(null);
  const [err, setErr] = useState(null);
  const [offset, setOffset] = useState(0);
  const limit = 20;

  const load = () => {
    setData(null);
    call('GET', `/api/v1/admin/users?limit=${limit}&offset=${offset}`)
      .then(setData)
      .catch((e) => setErr(e.message));
  };
  useEffect(load, [offset]);

  const promote = async (id) => {
    try {
      await call('POST', `/api/v1/admin/users/${id}/promote`);
      showToast('ok', '已提升为管理员');
      load();
    } catch (e) {
      showToast('err', e.message);
    }
  };

  if (err) return html`<p class="error-text">${err}</p>`;
  if (!data) return html`<p class="muted">加载中…</p>`;

  return html`
    <h1>用户管理</h1>
    <table>
      <thead><tr><th>邮箱</th><th>ID</th><th>角色</th><th>创建时间</th><th></th></tr></thead>
      <tbody>
        ${data.users.map((u) => html`
          <tr key=${u.id}>
            <td data-label="邮箱">${u.email}</td>
            <td data-label="ID"><code title=${u.id}>${u.id.slice(0, 8)}…</code></td>
            <td data-label="角色">${u.is_admin ? html`<span style="color:var(--success)">管理员</span>` : '普通用户'}</td>
            <td data-label="创建时间">${fmtTime(u.created_at)}</td>
            <td data-label="操作">
              ${u.is_admin
                ? html`<span class="muted" style="font-size:12px">已是管理员</span>`
                : html`<button class="btn" onClick=${() => promote(u.id)}>提升为管理员</button>`}
            </td>
          </tr>
        `)}
      </tbody>
    </table>
    <div class="btn-row" style="margin-top:16px">
      <button class="btn" disabled=${offset === 0} onClick=${() => setOffset(Math.max(0, offset - limit))}>上一页</button>
      <span class="muted">第 ${Math.floor(offset / limit) + 1} 页</span>
      <button class="btn" disabled=${data.users.length < limit} onClick=${() => setOffset(offset + limit)}>下一页</button>
    </div>
  `;
}

// ============================================================
// helpers
// ============================================================
function fmtTime(unix) {
  if (!unix) return '';
  const d = new Date(unix * 1000);
  return d.toLocaleString('zh-CN', { hour12: false });
}

function humanBytes(n) {
  if (n < 1024) return n + ' B';
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + ' KB';
  return (n / 1024 / 1024).toFixed(2) + ' MB';
}

// ============================================================
// mount
// ============================================================
render(h(App), document.getElementById('app'));
```

> **Note for implementer:** in `tryRefresh` the line that originally read `localStorage.setItem(LS_JWT, ...)` is preceded by a stray `localStorageStorage:` comment-typo fragment in the draft above. Verify the final file contains only `localStorage.setItem(...)` calls and no `localStorageStorage` token. If present, delete the stray fragment.

- [ ] **Step 2: Verify the file is served and the embed still works**

Run: `go build ./...`
Expected: exits 0.

Run: `go test -race -count=1 ./internal/webui/`
Expected: PASS (4 tests still pass).

- [ ] **Step 3: Run the full suite to confirm no regression**

Run: `go test -race -count=1 ./...`
Expected: PASS (82 tests).

- [ ] **Step 4: Commit**

```bash
git add internal/webui/dist/assets/app.js
git commit -m "feat(webui): full Preact SPA (login, apps, token mgmt, quota, admin pages)"
```

---

## Task 11: Wire the catch-all `/` in `server.go` and add server-level tests

**Files:**
- Modify: `internal/server/server.go` (add 1 import + 1 line)
- Modify: `internal/server/server_test.go` (add 2 tests)

- [ ] **Step 1: Add the import and registration in `server.go`**

In `internal/server/server.go`, add `"github.com/viccom/cfgsync/internal/webui"` to the import block (alphabetical order).

Then at the very end of `New()`, after the `AppTokenMW` lines, add:

```go
// Catch-all: serve the embedded SPA. /api/v1/* is matched by the more specific
// routes above (Go 1.22's method-aware mux takes the explicit handler first).
mux.Handle("/", webui.Handler())
```

- [ ] **Step 2: Verify it compiles**

Run: `go build ./...`
Expected: exits 0.

- [ ] **Step 3: Add 2 tests to `internal/server/server_test.go`**

```go
func TestServer_WebUI_ServesIndexOnRoot(t *testing.T) {
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
	h := server.New(cfg, env)
	w := doReqSimple(t, h, "GET", "/", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Header().Get("Content-Type"), "text/html") {
		t.Errorf("expected text/html, got %q", w.Header().Get("Content-Type"))
	}
}

func TestServer_WebUI_APIRouteNotShadowed(t *testing.T) {
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
	h := server.New(cfg, env)
	// /api/v1/health must return JSON, not the SPA's index.html.
	w := doReqSimple(t, h, "GET", "/api/v1/health", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Header().Get("Content-Type"), "application/json") {
		t.Errorf("expected application/json from /api/v1/health, got %q (body=%s)",
			w.Header().Get("Content-Type"), w.Body.String())
	}
	if strings.Contains(strings.ToLower(w.Body.String()), "<!doctype") {
		t.Errorf("/api/v1/health returned HTML; the webui catch-all is shadowing the API route")
	}
}
```

- [ ] **Step 4: Run the new server tests**

Run: `go test -race -count=1 -run "TestServer_WebUI" ./internal/server/`
Expected: PASS (2 tests).

- [ ] **Step 5: Run the full suite**

Run: `go test -race -count=1 ./...`
Expected: PASS (84 tests).

- [ ] **Step 6: Commit**

```bash
git add internal/server/server.go internal/server/server_test.go
git commit -m "feat(server): mount embedded webui at /; cover index.html and API-not-shadowed"
```

---

## Task 12: Add CHANGELOG entry

**Files:**
- Modify: `CHANGELOG.md` (prepend a new 0.3.0 section)

- [ ] **Step 1: Inspect the current top of `CHANGELOG.md`**

Read the first 30 lines of `CHANGELOG.md` to find the latest version section, so the new entry goes above it.

- [ ] **Step 2: Prepend a new 0.3.0 section**

Above the existing top section, add:

```markdown
## 0.3.0 — 2026-06-16

### Added

- **WebUI** — embedded Preact SPA served from `/` on the same port as the API.
  - Login / register (email + password).
  - My Apps — browse all platform-registered apps, jump to per-app token management.
  - Per-app token management — list, revoke, and create new tokens. The plaintext
    token is shown exactly once on a dedicated page with a copy button and a
    `beforeunload` guard.
  - My Quota — total usage, percentage bar, per-app breakdown. No payload content
    is displayed or editable.
  - Settings — logout.
  - Admin: Apps CRUD (list, create, edit, delete with cascade warning).
  - Admin: Users list with pagination, promote-to-admin action.
- Backend: new endpoint `GET /api/v1/admin/users` (admin only, paginated, returns
  `id`, `email`, `is_admin`, `created_at`). `password_hash` is never included.
- Embedding: SPA source lives in `internal/webui/dist/` and is shipped inside the
  Go binary via `embed.FS`. No build step. Preact + htm + @preact/signals are
  loaded at runtime as ES modules from `esm.sh` (pinned versions).

### Notes

- The WebUI does **not** display or edit config payloads. Software clients
  continue to use `app_token` for `GET|PUT /apps/{app_id}/config`.
- Change-password is intentionally not exposed; the backend has no
  `PUT /me/password` endpoint.
- No security hardening in this release (no CSP, no rate limiting, etc.) — see
  known gaps in `CLAUDE.md`.
```

- [ ] **Step 3: Run the full suite one final time**

Run: `go test -race -count=1 ./...`
Expected: PASS (84 tests).

- [ ] **Step 4: Commit**

```bash
git add CHANGELOG.md
git commit -m "docs: CHANGELOG entry for 0.3.0 (WebUI + admin user listing)"
```

---

## Task 13: Manual smoke test

This task is not gated by a test runner; it documents the manual verification the operator should perform before declaring the work done.

- [ ] **Step 1: Start the server**

```bash
JWT_SECRET=$(openssl rand -hex 32) \
BOOTSTRAP_ADMIN_EMAIL=admin@example.com \
BOOTSTRAP_ADMIN_PASSWORD=admin-pass-123 \
go run ./cmd/server
```

Expected: server starts on `:28972` with no errors.

- [ ] **Step 2: Open the SPA in a browser**

Open `http://127.0.0.1:28972/` in a browser. Expected:
- The login page renders.
- No 404s in the browser DevTools network panel.
- The page CSS loads (light theme, centered form).

- [ ] **Step 3: Log in as admin**

Use the bootstrap admin credentials. Expected: redirects to `/admin/apps`, shows "还没有任何应用".

- [ ] **Step 4: Create an app**

Click "新建应用", fill `com.example.app` / "Example" / "Test app", save. Expected: returns to `/admin/apps`, the new app appears in the table.

- [ ] **Step 5: Log out, register a regular user**

Click "退出", then click "去注册" and register `user@example.com` / `user-pass-123`. Expected: redirected to `/apps`, the `com.example.app` card is visible.

- [ ] **Step 6: Create a token**

Click "管理 Token" on the app card, then "生成新 Token" with label "test". Expected: navigates to `/show-token/com.example.app/1rc_xxx…` with the plaintext token visible and a copy button.

- [ ] **Step 7: Copy the token, navigate to app detail**

Click "复制" — toast says "已复制到剪贴板". Click "我已保存，去应用详情". Expected: the new token appears in the list with prefix `1rc_xxx…` and label "test".

- [ ] **Step 8: Verify admin user list**

Log out, log in as admin, navigate to `/admin/users`. Expected: `user@example.com` is listed; clicking "提升为管理员" makes them admin.

- [ ] **Step 9: Verify API still works**

```bash
curl -sX POST http://127.0.0.1:28972/api/v1/health
```

Expected: `{"status":"ok","db":"ok"}`.

- [ ] **Step 10: No commit — this is verification only**

If any step fails, open a bug. Otherwise, the WebUI is ready for family use.

---

## Self-Review

**1. Spec coverage:**

| Spec section | Covered by task |
|---|---|
| §1.1 SPA pages | Task 10 (all components) |
| §1.1 New endpoint `GET /api/v1/admin/users` | Tasks 1-5 |
| §1.1 `embed.FS` mounting | Tasks 6, 11 |
| §2.1 Endpoint contract | Tasks 2, 4 |
| §2.2 Tests for the endpoint | Tasks 3, 5 |
| §3 File layout & embedding | Tasks 6, 7 |
| §3.3 Wiring in `server.go` | Task 11 |
| §4.1-4.6 Frontend architecture | Task 10 |
| §5.1 Login/register | Task 10 (Login component) |
| §5.2 /apps | Task 10 (MyApps) |
| §5.3 /apps/:id token management | Task 10 (AppDetail) |
| §5.4 /show-token (security-sensitive) | Task 10 (ShowToken) |
| §5.5 /me quota | Task 10 (MyQuota) |
| §5.6 /me/settings (logout only) | Task 10 (MySettings) |
| §5.7 /admin/apps | Task 10 (AdminApps) |
| §5.8 /admin/apps/:id | Task 10 (AdminAppEdit) |
| §5.9 /admin/users | Task 10 (AdminUsers) |
| §5.10 404 | Task 10 (NotFound) |
| §6 CSS theme | Task 9 |
| §7.1 Login flow | Task 10 (Login component) |
| §7.2 Token expiry (silent refresh) | Task 10 (call/tryRefresh) |
| §7.3 Create-token success | Task 10 (AppDetail → ShowToken nav) |
| §7.4 Admin app deletion cascade | Task 10 (AdminAppEdit confirm) |
| §7.5 Empty/loading/error states | Task 10 (each component) |
| §8.1 No build step | Confirmed — `dist/` is source of truth |
| §8.2 Done criteria | Tasks 5, 7, 11, 12 |

**2. Placeholder scan:** No `TBD`/`TODO`/`XXX`/`fill in` in the plan. Every step has the actual code or test body. (One small implementer note in Task 10 flags a typo to verify on the local file — not a placeholder.)

**3. Type consistency:**
- `model.AdminUserInfo` is used in Task 1 (definition), Task 2 (handler), Task 3 (test struct field) — consistent.
- `AdminListUsers(db *sql.DB) http.HandlerFunc` is used in Tasks 2, 3, 4, 5 — consistent.
- `webui.Handler()` is used in Tasks 6, 11 — consistent.
- `LS_JWT`/`LS_REFRESH` constants in app.js match everywhere in Task 10.
- `ERR_MSGS` keys match backend error codes in `CLAUDE.md`.

All consistent.
