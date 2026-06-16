# Changelog

All notable changes to this project are documented in this file.
Format: [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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
