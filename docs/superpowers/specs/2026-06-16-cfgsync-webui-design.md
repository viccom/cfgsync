# cfgsync WebUI Design

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:writing-plans to break this spec into an implementation plan; then use superpowers:subagent-driven-development to execute it task-by-task.

**Goal:** Give the operator and their family/friends a browser-based UI for cfgsync — register/login, browse available apps, issue/revoke per-(user,app) tokens, view one's own quota, and (for admins) manage the registered app list and promote users.

**Architecture:** A static single-page application built with **Preact + htm** (no build step), embedded into the Go binary via `embed.FS`, and served by the existing `http.ServeMux`. All data flows through the existing `/api/v1/*` REST endpoints. The only backend change is a new admin-only listing endpoint `GET /api/v1/admin/users`.

**Tech stack:** Preact 10 + @preact/signals + htm + a hand-rolled 1.5 KB fetch wrapper; vanilla CSS with custom-property variables (light theme only); Go 1.22+ `embed.FS`.

---

## 1. Scope and non-goals

### 1.1 In scope (this spec)

- Embedded SPA served from the cfgsync binary on the same port (`:28972`).
- Pages for: register, login, my-apps list, per-app token management, my-quota, my-settings (logout only; change password is out of scope until backend adds `PUT /me/password`), admin apps CRUD, admin user list, admin promote-user.
- Client-side route guard: redirect to `/login` when no JWT, to `/apps` when a non-admin hits an `/admin/*` route.
- One new backend endpoint: `GET /api/v1/admin/users` (admin only, paginated, returns `id`, `email`, `is_admin`, `created_at`).
- Embedding: `internal/webui/dist/` shipped inside the binary via `embed.FS`; `GET /` (and any other non-`/api/v1/*` path) returns `index.html` for SPA routing, while specific assets (`/assets/app.js`, `/assets/app.css`) are served from the embedded FS.

### 1.2 Explicit non-goals (do NOT do in this spec)

- **No payload display or editing.** The WebUI never renders or accepts config payloads. Config read/write continues to happen exclusively through software clients using `app_token`.
- No WebSocket / SSE / real-time updates.
- No version history / diff view / conflict resolution UI (no payload view = no diff).
- No audit log page.
- No multi-language UI. Chinese only.
- No theme switcher. Light theme only.
- No team / organization / role hierarchies beyond the existing `is_admin` bit.
- No rate limiting, CSP hardening, or other security work. (CORS is unchanged: same-origin only by default; we are not setting any new headers.)
- No admin user-creation UI. Admins are bootstrapped via env vars (`BOOTSTRAP_ADMIN_*`) and promoted via the existing promote endpoint; new family/friend accounts register themselves through the public registration page.
- No file upload, no Monaco / CodeMirror, no drag-and-drop, no rich text.

---

## 2. Backend change: `GET /api/v1/admin/users`

### 2.1 Contract

**Auth:** `UserMW` + `AdminMW` (admin only).

**Query params:**
- `limit` (int, default 20, max 100, ≤ 0 → 20)
- `offset` (int, default 0, < 0 → 0)

**Response 200:**
```json
{
  "users": [
    { "id": "abcd...", "email": "user@example.com", "is_admin": false, "created_at": 1718000000 }
  ],
  "limit": 20,
  "offset": 0
}
```

**Response errors:** `401 unauthorized`, `403 forbidden` (non-admin), `500 internal`.

**Implementation notes:**
- File: `internal/handler/admin.go`, function `AdminListUsers(db *sql.DB) http.HandlerFunc`.
- Reuse the pagination pattern from `AdminListApps` (server.go line 41-48).
- Wire in `internal/server/server.go` under the `adminChain` block.
- `password_hash` is NEVER selected into the struct.
- ORDER BY `created_at DESC` (newest first), matching `AdminListApps`.
- New `model.AdminUserInfo` struct in `internal/model/user.go`:
  ```go
  type AdminUserInfo struct {
      ID        string `json:"id"`
      Email     string `json:"email"`
      IsAdmin   bool   `json:"is_admin"`
      CreatedAt int64  `json:"created_at"`
  }
  ```

### 2.2 Tests

Add to `internal/handler/admin_test.go`:
- `TestAdminListUsers_ReturnsAll` — seed 3 users, 1 admin; verify all returned with correct fields and no `password_hash` in body.
- `TestAdminListUsers_Pagination` — seed 25 users, request `?limit=10&offset=10`, verify slice.
- `TestAdminListUsers_RejectsNonAdmin` — non-admin → 403.
- `TestAdminListUsers_DefaultLimit` — omit params, expect `limit: 20`.

---

## 3. File layout and embedding

### 3.1 New directory

```
internal/webui/
├── dist/                  # source of truth, served verbatim
│   ├── index.html         # single HTML entry; references /assets/app.js, /assets/app.css
│   └── assets/
│       ├── app.js         # Preact + htm bundle (single file, no source maps needed)
│       └── app.css        # all styles (light theme)
│   # No favicon.ico on purpose; browser request returns 404 (harmless).
└── webui.go               # embed.FS declaration + http handler
```

**No build step.** `dist/` is committed to the repo. The handler reads files verbatim.

### 3.2 Embedding and serving

```go
// internal/webui/webui.go
package webui

import (
    "embed"
    "io/fs"
    "net/http"
)

//go:embed all:dist
var distFS embed.FS

// Handler returns an http.Handler that serves the embedded SPA.
// - /assets/*  → files under dist/assets/; paths under assets/ that miss
//                return 404 (NOT the SPA fallback, so a typo in a
//                <script src=...> doesn't get silently swallowed as
//                index.html-as-JS).
// - everything else → dist/index.html (SPA fallback for client-side routing)
func Handler() http.Handler {
    sub, _ := fs.Sub(distFS, "dist")
    fserver := http.FileServer(http.FS(sub))
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        // Try the file as-is. If it doesn't exist, fall back to index.html.
        path := r.URL.Path
        if path == "/" {
            serveIndex(w, r, sub)
            return
        }
        if _, err := fs.Stat(sub, path[1:]); err == nil {
            fserver.ServeHTTP(w, r)
            return
        }
        // Asset miss → 404 (don't serve HTML where JS/CSS is expected).
        if strings.HasPrefix(path, "/assets/") {
            http.NotFound(w, r)
            return
        }
        // Unknown path under root → SPA fallback.
        serveIndex(w, r, sub)
    })
}

func serveIndex(w http.ResponseWriter, r *http.Request, sub fs.FS) {
    f, err := sub.Open("index.html")
    if err != nil {
        http.Error(w, "ui not built", http.StatusInternalServerError)
        return
    }
    defer f.Close()
    w.Header().Set("Content-Type", "text/html; charset=utf-8")
    // (no Cache-Control: index.html is small and we want fresh code on deploy)
    _, _ = io.Copy(w, f)
}
```

### 3.3 Wiring in `internal/server/server.go`

Add to `New()`:

```go
mux.Handle("/", webui.Handler()) // fall-through; method-less
```

The order matters: keep `/api/v1/*` registrations first (already explicit), then the catch-all `/` last. Go 1.22's method-aware mux means `/api/v1/health` (with method) will still match the explicit handler before the catch-all takes the `GET /` path.

### 3.4 Why a single `index.html` fallback

Preact Router uses the History API. Direct hits to `/apps`, `/admin/apps`, etc. must return `index.html` so the SPA can mount and route. The fallback handler in §3.2 covers this.

---

## 4. Frontend architecture

### 4.1 Runtime dependencies (CDN-pinned in `index.html`)

- `preact@10.22.0` — core + hooks
- `@preact/signals@1.2.3` — reactive state
- `htm@3.1.1` — JSX-in-template-literal

These are loaded as ES modules from a CDN with `?module` or `?dist/preact.module.js` paths, **pinned by version**, and the `<script type="module">` block in `index.html` imports them and then imports `/assets/app.js`. The Go binary does NOT bundle them — only `app.js` (our code) is embedded. CDN choice: `esm.sh` with version-locked URLs.

If a self-hosted / offline mode is desired later, this can be revisited; it is out of scope here.

### 4.2 Application skeleton (app.js)

```text
app.js (single file, IIFE/ESM)
├── imports: preact, preact/hooks, @preact/signals, htm
├── const html = htm.bind(h)         // htm helper bound to Preact's h
├── api.js (inline)                  // 1.5KB fetch wrapper, see §4.3
├── state.js (inline)                // signals: jwtSignal, userSignal, toastSignal
├── router.js (inline)               // tiny history-based router
├── components/
│   ├── <App>          — top-level switch on path
│   ├── <Login>        — login + register tab
│   ├── <MyApps>       — /apps list
│   ├── <AppDetail>    — /apps/:id  token management
│   ├── <MyQuota>      — /me quota view
│   ├── <MySettings>   — /me/settings change-password + logout
│   ├── <AdminApps>    — /admin/apps list + new
│   ├── <AdminAppEdit> — /admin/apps/:id edit/delete
│   ├── <AdminUsers>   — /admin/users list + promote
│   ├── <ShowToken>    — one-time plaintext token display
│   └── <Toast>        — global toast
└── mount: render(<App/>, document.body)
```

### 4.3 API client (`api.js`)

```js
// 1.5 KB target. No deps.
const BASE = '';  // same-origin

let jwtRef = { value: null };
export const setJwt = (v) => { jwtRef.value = v; };
export const getJwt = () => jwtRef.value;

export async function call(method, path, body) {
  const headers = { 'Content-Type': 'application/json' };
  const t = jwtRef.value;
  if (t) headers['Authorization'] = `Bearer ${t}`;
  const res = await fetch(BASE + path, {
    method,
    headers,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  const text = await res.text();
  let data = null;
  try { data = text ? JSON.parse(text) : null; } catch {}
  if (!res.ok) {
    const err = new Error(data?.error || `http_${res.status}`);
    err.status = res.status;
    err.body = data;
    throw err;
  }
  return data;
}
```

### 4.4 State (`state.js`)

- `jwtSignal` — current user JWT string, persisted to `localStorage.cfgsync_jwt`. Effects sync both ways on mount.
- `userSignal` — `{ email, is_admin, exp }` decoded from JWT (no signature check needed; the server will reject on 401).
- `toastSignal` — `{ kind: 'ok'|'err'|'info', text: string, ttl: number }`. Auto-clears after `ttl` ms.
- `routeSignal` — `{ path: string, params: object }`. Driven by `popstate` + `pushState` wrapper.

### 4.5 Router (`router.js`)

- On `popstate` and on every `link.click`, update `routeSignal`.
- A `routeSignal.subscribe` rerenders `<App>`.
- Path patterns: `/`, `/login`, `/register`, `/apps`, `/apps/:id`, `/me`, `/me/settings`, `/admin/apps`, `/admin/apps/:id`, `/admin/users`, `/show-token/:id/:token`.
- Guard: if `!jwtSignal.value` and path is not `/login` or `/register` → push `/login`. If path starts with `/admin` and `!userSignal.value.is_admin` → push `/apps` and show toast "需要管理员权限".

### 4.6 Error → Chinese mapping (single object)

```js
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
```

`api.js` throws `Error(msg)` where `msg` is the mapped Chinese text, with `err.status` and `err.body` preserved for callers that care (e.g. quota errors expose `used_bytes` / `limit_bytes`).

---

## 5. Page-by-page specification

### 5.1 `/login` and `/register` (combined page with tabs)

- Form: email + password.
- Submit → `POST /api/v1/auth/login` (or `/register`).
- On success: store `access_token` + `refresh_token` in `localStorage` (`cfgsync_jwt` and `cfgsync_refresh`); decode JWT payload to populate `userSignal`; route to `/apps` (or `/admin/apps` if `is_admin`).
- On `email_already_registered` during register, show inline error under email field.
- Show link "还没账号？去注册" / "已有账号？去登录" switching tabs.

### 5.2 `/apps` (My Apps)

- Title: "我的应用"
- Layout: card grid. Each card = one app from `GET /api/v1/apps`.
- Each card shows:
  - `display_name` (large)
  - `app_id` (small, monospace)
  - `description` (truncated to 2 lines)
  - "管理 Token (n)" button → `/apps/:id`
- Empty state: "暂无可用应用。请联系管理员注册 app_id。"

### 5.3 `/apps/:id` (App Detail / Token Management)

- Title: app `display_name` + small `app_id` underneath.
- Section "我名下的 Token":
  - List fetched from `GET /api/v1/me/tokens`, filtered client-side to those with matching `app_id`.
  - Each row: `label` (or "未命名"), `token_prefix` (small grey), 创建时间, 最后使用时间, "撤销" button.
  - "撤销" click → inline confirmation "确认撤销 X？[取消] [确认]" → `DELETE /api/v1/me/tokens/{token_prefix}`.
  - "新建 Token" button at top.
- New-token modal/inline form:
  - Field: `label` (optional, placeholder "例如：MacBook Air").
  - Submit → `POST /api/v1/me/apps/{app_id}/token`.
  - On 200: navigate to `/show-token/:id/:token` and show one-time token page.
  - On 409 (limit reached): show "已达 app_token 上限" inline.

### 5.4 `/show-token/:id/:token` (One-time plaintext token display)

This is the **most security-sensitive page** in the UI.

- Layout: centered card, large monospace text.
- Top: ⚠️ "请立即复制此 token。离开此页面后将无法再看到完整 token。"
- Token displayed in a `<code>` block with select-all on click.
- "复制" button (uses `navigator.clipboard.writeText`).
- "我已保存，去应用详情" button → `/apps/:id`.
- No "back" affordance; navigating away warns `beforeunload`.
- Route param token 须经 `decodeURIComponent` 后再显示。URL is allowed to contain the token; this is acceptable because the token is a bearer credential the user must see at least once, and our model is "show it once, then never again" (server does not store plaintext). Hashed token + prefix live in the DB; the plaintext lives in the URL bar briefly. (Mitigation: discourage screenshots via the warning, but we accept the URL exposure.)
- Route is registered in the router; if user pastes this URL fresh, the token is rendered but the page warns "正在显示新创建的 token" with the same copy button. We do NOT clear the token from `localStorage` because we never stored it there.

### 5.5 `/me` (My Quota)

- Title: "我的配额"
- Top: progress bar `used_bytes / limit_bytes` with humanized labels (e.g. "12.3 MB / 100 MB").
- Below: per-app table — `app_id`, size, last update time, "管理" button. (We do NOT show payload content.)
- "管理" navigates to `/apps/:id`.

### 5.6 `/me/settings` (Change Password + Logout)

- Section "修改密码":
  - Fields: old password, new password (≥ 8 chars), confirm.
  - Submit → no dedicated endpoint exists; we **reuse** the same flow as register: there's no current `/me/password` endpoint. **Spec decision: skip change-password in v0.3.** The settings page only has "退出登录".
- "退出登录" button:
  - `POST /api/v1/auth/logout` with `refresh_token` in body.
  - Clear `localStorage.cfgsync_jwt`, `cfgsync_refresh`.
  - Navigate to `/login`.

> **Note:** the "change password" feature is intentionally omitted because the backend has no `PUT /me/password` endpoint. Adding it would require a separate spec. This is a known gap; users who need to change passwords contact the admin, who can reset by issuing a one-time credential out of band (not in scope).

### 5.7 `/admin/apps` (Admin: Apps List)

- Title: "应用管理" + "新建应用" button.
- Table: `app_id`, `display_name`, `description` (truncated), `created_at`, `created_by` (email if we have it from join), actions.
- Each row "编辑" → `/admin/apps/:id`, "删除" → inline confirm.
- "新建应用" → `/admin/apps/new` (uses the same edit page with empty fields).
- Backend: `GET /api/v1/admin/apps`, `POST /api/v1/admin/apps`, `PATCH /api/v1/admin/apps/:id`, `DELETE /api/v1/admin/apps/:id`.

### 5.8 `/admin/apps/:id` (Admin: App Edit / New)

- Fields: `app_id` (disabled when editing), `display_name` (required, ≤ 256), `description` (optional, ≤ 1024).
- Submit:
  - Create: `POST /api/v1/admin/apps`.
  - Edit: `PATCH /api/v1/admin/apps/:id`.
- "删除此应用" at bottom: inline confirm "确认删除 X？这会级联删除所有用户在此 app 下的所有数据（config / 历史 / token），无法恢复。"
- 404 handling: if app disappears while editing, redirect to `/admin/apps` with toast.

### 5.9 `/admin/users` (Admin: User List)

- Title: "用户管理".
- Table: `email`, `id` (truncated to first 8 + "...", click to copy), `is_admin`, `created_at`, actions.
- "提升为管理员" button for non-admin rows → `POST /api/v1/admin/users/:id/promote`.
- "已是管理员" badge for admin rows; no demote action.
- Pagination: "?limit=20&offset=0" mirrors server, with "上一页 / 下一页" links.

### 5.10 404 / catch-all

- Unknown route → "页面不存在 [返回首页]".

---

## 6. Visual design

### 6.1 Theme: light only

CSS custom properties (defined in `:root` of `app.css`):

```css
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
  --shadow: 0 1px 3px rgba(0,0,0,0.08);
  --font: -apple-system, BlinkMacSystemFont, "Segoe UI", "PingFang SC",
          "Hiragino Sans GB", "Microsoft YaHei", sans-serif;
  --mono: ui-monospace, SFMono-Regular, "SF Mono", Menlo, Consolas, monospace;
}
```

### 6.2 Layout

- Top nav (sticky): logo text "cfgsync" left; right side shows current user email and a "退出" link. If admin, also shows a dropdown "管理" with links to `/admin/apps` and `/admin/users`.
- Main content: max-width 960px, centered, 24px padding.
- Cards: 12px padding, 1px border, 6px radius, subtle shadow.
- Forms: stacked labels above inputs; 8px vertical gap; submit button full-width on mobile, auto-width on ≥640px.
- Buttons: 8px 16px padding, 6px radius, primary color for primary actions, plain border for secondary, danger for delete/revoke.

### 6.3 Responsive

- Single column on <640px.
- Tables become stacked cards (label above value) on narrow screens via a `@media (max-width: 640px)` rule.

---

## 7. State transitions and edge cases

### 7.1 Login flow

```
/login submit → POST /auth/login
  200: localStorage.setItem(jwt, refresh); userSignal=jwt payload; route /apps
  401 invalid_credentials: inline error under password field
  400 invalid_email_or_password: same inline error
  network error: top toast "网络错误，请重试"
```

### 7.2 Token expiry during use

- Every API call goes through `call()`. If `res.status === 401` and `body.error === 'invalid_token'`, attempt one silent refresh for **idempotent** methods (GET / DELETE / PUT with `If-Match`-like behavior) only:
  1. `POST /auth/refresh` with `cfgsync_refresh` from localStorage.
  2. On 200: update `cfgsync_jwt` + `cfgsync_refresh`, retry the original call once.
  3. On failure: clear localStorage, push `/login`, show toast "会话已过期，请重新登录".
- For **non-idempotent** methods (POST that create/mutate), 401 is treated as "session expired, ask the user to log in again" — no silent retry, since the original body's side effects may already have happened on the server and retrying would risk duplicates. The toast reads "会话已过期，请重新登录".
- Refresh+retry must not loop; the wrapper sets a one-shot flag.

### 7.3 Create-token success

```
POST /me/apps/:id/token → 200 { token, app_id, label, created_at }
  pushState('/show-token/' + app_id + '/' + encodeURIComponent(token))
  ShowToken component renders, with beforeunload guard.
  User clicks "我已保存" → pushState('/apps/' + app_id).
```

### 7.4 Admin app deletion with non-zero users

- Inline confirm message explicitly says "all per-user data will be wiped via cascade".
- 204 success → toast "已删除" → `/admin/apps`.
- 404 (race) → toast "应用已被删除" → `/admin/apps`.

### 7.5 Empty / loading / error states (universal)

- Loading: a centered "加载中…" text or a simple inline spinner (CSS only).
- Empty: a centered illustration-less message + suggested action link.
- Error: toast at top, page state unchanged.

---

## 8. Build, ship, verify

### 8.1 Source of truth

`internal/webui/dist/` is checked into git. No `npm install`. No CI build step. The CI workflow (`.github/workflows/test.yml`) does not change.

### 8.2 What "done" looks like

- All 73 existing backend tests still pass.
- 4 new backend tests for `GET /admin/users` pass.
- A new test in `internal/webui/webui_test.go` (or `server_test.go` extension) verifies:
  - `GET /` returns 200 with `Content-Type: text/html` and contains `<div id="app">` (or whatever the mount point is).
  - `GET /assets/app.js` returns 200 with the embedded JS.
  - `GET /nonexistent-route` returns 200 with `index.html` (SPA fallback).
  - `GET /api/v1/health` still works (catch-all `/` did not shadow it).
- Manual smoke (documented in plan, not in CI): start server, register, login, create token, copy, navigate to admin apps, create app, see it on `/apps`.

### 8.3 What we do NOT do

- No CSP, X-Frame-Options, Referrer-Policy headers (out of scope per §1.2).
- No service worker, no offline mode.
- No bundle hashing / cache busting. (Refresh-after-deploy is acceptable; users are family-scale.)
- No automated browser tests (Playwright was added as a plugin in `settings.json` but we do not write tests in this scope).

---

## 9. Risks and known limits

| Risk | Mitigation |
|---|---|
| CDN unavailability (esm.sh) for Preact/htm/signals | Users on the local network are family-scale; the service is personal-use. If CDN is down, the UI is broken until CDN recovers. Future work: bundle vendor scripts into the embedded FS. |
| Token shown in URL bar of `/show-token/...` | Acceptable per design; warned via UI; not stored in localStorage; not logged by server (no server log line in the spec). |
| `localStorage` XSS exposure of JWT | Out of scope (no security work). WebUI does not render any user-provided HTML, so the practical XSS surface is near-zero. |
| `delete` cascading wipes all family members' data | Confirmed by inline confirm dialog text. No "undo" / soft-delete in this spec. |
| Bootstrap admin email and password are env-only | If admin forgets password, the only recovery is to drop into the DB and reset `password_hash`. Not a UI concern. |

---

## 10. File-level change summary (for the plan)

| File | Change |
|---|---|
| `internal/handler/admin.go` | Add `AdminListUsers(db) http.HandlerFunc` (~25 LOC). |
| `internal/handler/admin_test.go` | Add 4 tests. |
| `internal/model/user.go` | Add `AdminUserInfo` struct. |
| `internal/server/server.go` | Register `GET /api/v1/admin/users` in `adminChain`. |
| `internal/webui/webui.go` | New file: `embed.FS` + fallback handler. |
| `internal/webui/webui_test.go` | New file: 4 tests (index.html, /assets/app.js, fallback, /api/v1/health not shadowed). |
| `internal/webui/dist/index.html` | New file: HTML shell with ES module import of preact/signals/htm and `/assets/app.js`. |
| `internal/webui/dist/assets/app.js` | New file: ~600-900 LOC of Preact components. |
| `internal/webui/dist/assets/app.css` | New file: light theme variables + layout. |
| `CHANGELOG.md` | New `0.3.0` entry: "feat(webui): embedded SPA for user and admin management". |

No other files change. The Go module does not gain any runtime dependencies (Preact is loaded by the browser, not Go).
