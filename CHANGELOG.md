# Changelog

All notable changes to this project are documented in this file.
Format: [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.3.0] - 2026-06-16

Adds the embedded WebUI and a small backend endpoint to support it.

### Added

**WebUI** (embedded Preact SPA served from `/` on the same port as the API)
- Login / register (email + password) with the existing user JWT + refresh token
- My Apps — browse all platform-registered apps, jump to per-app token management
- Per-app token management — list, revoke, and create new tokens. The plaintext
  token is shown exactly once on a dedicated page with a copy button and a
  `beforeunload` guard
- My Quota — total usage, percentage bar, per-app breakdown. **No payload
  content is displayed or editable** (WebUI never touches config payloads)
- Settings — logout only
- Admin: Apps CRUD (list, create, edit, delete with cascade warning)
- Admin: Users list with pagination, promote-to-admin action

**Backend**
- `GET /api/v1/admin/users` — paginated user listing for the admin user
  management page. Returns `id`, `email`, `is_admin`, `created_at`; never
  includes `password_hash`

**Embedding**
- SPA source lives in `internal/webui/dist/` and is shipped inside the Go
  binary via `embed.FS`. No build step. Preact + htm + @preact/signals are
  loaded at runtime as ES modules from `esm.sh` (pinned versions:
  preact@10.22.0, @preact/signals@1.2.3, htm@3.1.1)
- The webui handler returns 404 for `/assets/*` misses (does not serve
  `index.html` as a script/stylesheet), and falls back to `index.html` for
  unknown non-asset paths (so client-side routes work on direct navigation /
  browser refresh)

### Notes

- The WebUI does **not** display or edit config payloads. Software clients
  continue to use `app_token` for `GET|PUT /apps/{app_id}/config`
- Change-password is intentionally not exposed; the backend has no
  `PUT /me/password` endpoint
- No security hardening in this release (no CSP, no rate limiting, etc.) — see
  known gaps in `CLAUDE.md`

## [0.2.0] - 2026-06-16

Completes the admin management API surface. No breaking changes; purely
additive endpoints and behavior is consistent with v0.1.0 contracts.

### Added

**Admin app management**
- `GET /api/v1/admin/apps` — paginated list (`?limit=20&offset=0`), includes `created_by` user_id
- `GET /api/v1/admin/apps/{app_id}` — single record with `created_by_email` (LEFT JOIN users)
- `PATCH /api/v1/admin/apps/{app_id}` — partial update via `*string` fields (`display_name` / `description`); omitted fields untouched; empty body or empty `display_name` returns 400; unknown `app_id` returns 404
- `DELETE /api/v1/admin/apps/{app_id}` — removes the app; `apps.app_id REFERENCES` with `ON DELETE CASCADE` atomically wipes all `configs` / `config_history` / `app_tokens` for that app across all users
- `POST /api/v1/admin/users/{user_id}/promote` — grants `is_admin=1`; idempotent (re-promoting an admin is a 200 no-op); unknown `user_id` returns 404

**Model**
- `model.App.CreatedByEmail` (`json:"created_by_email,omitempty"`) — only populated by admin views
- `model.PatchAppRequest` — `DisplayName` / `Description` as `*string` for partial-update semantics

### Behavior notes

- **JWT is stateless**: promoting a user does not modify their existing access token. The new admin claim appears on the next `/auth/login` or `/auth/refresh`. This matches the documented behavior from v0.1.0 §"JWT claim shape".
- **App deletion is destructive and global**: `DELETE /admin/apps/{app_id}` wipes every user's data for that app. No undo, no soft-delete. Admins must be certain.
- **Cascade is enforced by schema**, not application code: `configs.app_id`, `config_history.app_id`, and `app_tokens.app_id` all carry `REFERENCES apps(app_id) ON DELETE CASCADE`, with `PRAGMA foreign_keys=ON` set in `db.Open`.

### Tests

- 14 new handler tests covering each admin endpoint's happy path plus 403 / 404 / 400 edge cases
- E2E curl flow exercises full lifecycle: list → create → get → patch → user data seed → delete (cascade) → promote → re-login as new admin

## [0.1.0] - 2026-06-16

First usable release. Repositions the project from a 1Remote-specific sync
backend into a generic per-user software configuration sync service.

### Added

**App governance**
- `apps` table: admin-registered `app_id` (reverse-domain format), `display_name`, `description`, `created_by`
- `POST /api/v1/admin/apps` — admin creates an app_id (admin-only via `is_admin` JWT claim)
- Bootstrap admin via `BOOTSTRAP_ADMIN_EMAIL` + `BOOTSTRAP_ADMIN_PASSWORD` env vars on first start

**Two-layer token architecture**
- User JWT (HS256, `Claims{uid, email, adm}`) — management operations
- App token (opaque 32-byte hex, only SHA-256 hash stored) — sync operations, scoped to `(user_id, app_id)`
- `app_tokens` table with `UNIQUE(user_id, app_id)` and `token_prefix` for safe listing

**Sync endpoints** (app token auth)
- `GET /api/v1/apps/{app_id}/config` — current snapshot; new pair returns `{version:0}`
- `PUT /api/v1/apps/{app_id}/config` — optimistic-lock upsert (`version` must match)
- `PUT /api/v1/apps/{app_id}/config?force=true` — force overwrite, bypasses version check (still bumps version, still appends history)
- `409 version_conflict` returns `current_*` fields for client-side merge
- 4 MB hard limit per PUT (MaxBytesReader + payload size check); `413 payload_too_large`
- `config_history` table: every successful PUT appends a row

**User management endpoints** (user JWT auth)
- `GET /api/v1/apps`, `GET /api/v1/apps/{app_id}` — list/view registered apps
- `POST /api/v1/me/apps/{app_id}/token` — issue/replace app token; plaintext returned exactly once
- `GET /api/v1/me/tokens` — list own app tokens (no plaintext, no hash)
- `DELETE /api/v1/me/tokens/{token_prefix}` — revoke own token (cross-user collision-safe)
- `DELETE /api/v1/me/apps/{app_id}/data` — atomic wipe of config + history + token for one (user, app_id)
- `GET /api/v1/me/quota` — current usage vs configured limits

**Quotas and history trimming**
- `USER_STORAGE_LIMIT_MB` (default 100) — sum of `LENGTH(payload)` over user's configs; `413 storage_quota_exceeded` with `used_bytes`/`limit_bytes`. Force does NOT bypass quota.
- `USER_APP_TOKEN_LIMIT` (default 100) — max distinct `(user, app_id)` tokens per user; replacing an existing pair does not consume a new slot; `413 app_token_limit_reached`
- `HISTORY_PER_APP` (default 50) — after each PUT, trim to most recent N rows for `(user, app_id)`. Set to 0 to disable trimming.
- `MAX_PAYLOAD_BYTES` (default 4194304) — per-PUT byte ceiling
- `APP_TOKEN_PREFIX` (default `1rc_`) — plaintext token prefix for branding

**Security model**
- Bcrypt cost 12 for password hashing
- JWT HS256 with explicit `*jwt.SigningMethodHMAC` assertion (blocks `alg=none` downgrade)
- `JWT_SECRET` must be ≥ 32 bytes (fail-fast at startup)
- App tokens stored as SHA-256 hash only; plaintext returned once on creation
- `AppTokenMW` enforces `claims.app_id == URL.{app_id}` (cross-app token reuse forbidden)
- All SQL parameterized; SQLite WAL + `foreign_keys=1` + `busy_timeout=5000`; `SetMaxOpenConns(1)` for serial writes

### Changed

- **Schema rewritten to v2**: `configs` and `config_history` gained `app_id`; new `apps` and `app_tokens` tables; `users` gained `is_admin`. Old v1 schema is not upgradeable — fresh DB required (scaffold-stage privilege).
- **`internal/auth/middleware.go`**: split monolithic `Middleware` into `UserMW` (JWT), `AdminMW` (post-UserMW, requires `is_admin`), and `AppTokenMW` (opaque DB lookup with path-app_id enforcement).
- **`internal/handler/auth.go`**: `Register`/`Login`/`Refresh`/`Logout` no longer reference `app_id`; user token is decoupled from app binding.
- **`internal/server/server.go`**: route table now spans 4 middleware classes (public / body-auth / UserMW / UserMW+AdminMW / AppTokenMW).
- **`cmd/server/main.go`**: calls `db.BootstrapAdmin` after `Migrate`.

### Removed

- `internal/handler/config.go` (v1 single-blob GetConfig/PutConfig) — replaced by `sync.go` with `(user, app_id)` scoping.
- `internal/handler/config_test.go` — superseded by `sync_test.go`.

### Known gaps (deferred to subsequent releases)

- Phase 3 admin API: `GET/PATCH/DELETE /admin/apps/{id}`, `GET /admin/apps` (list), `POST /admin/users/{id}/promote`.
- Rate limiting (brute-force protection on `/auth/login`).
- Metrics / tracing (only `log.Printf` access log today).
- Admin-side WebUI (intentionally out of scope; admin uses REST API directly).
- Client SDKs (Go / Python / JS).
- v1 → v2 in-place migration (currently requires fresh DB).
