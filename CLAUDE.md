# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project

`cfgsync` is a **generic per-user software configuration sync backend**. Any application can sync text-based config (≤ 4 MB per app) across devices via a language-neutral REST API. Personal-use oriented — no team/role/org. See [`docs/superpowers/specs/`](docs/superpowers/specs/) for design specs and [`CHANGELOG.md`](CHANGELOG.md) for release history.

## Commands

```bash
go mod download
go test ./...                                  # all tests
go test -race -count=1 ./...                   # CI invocation (race detector, no cache)
go test -run TestPutConfig_StorageQuota ./internal/handler/   # single test
go vet ./...
go build -trimpath -o /tmp/cfgsync ./cmd/server
JWT_SECRET=$(openssl rand -hex 32) \
BOOTSTRAP_ADMIN_EMAIL=admin@example.com \
BOOTSTRAP_ADMIN_PASSWORD=admin-pass-123 \
go run ./cmd/server                            # local dev; listens on :28972
```

CI (`.github/workflows/test.yml`) pins Go 1.22 even though `go.mod` requires 1.25.0 — keep code compatible with 1.22 unless `go.mod` is bumped.

## Configuration (env vars)

| Var | Default | Notes |
|---|---|---|
| `JWT_SECRET` | (required) | **≥ 32 bytes**, server exits at startup if missing/short |
| `LISTEN` | `:28972` | |
| `DB_PATH` | `./data.db` | SQLite file; `:memory:` only works for single-connection tests |
| `ACCESS_TTL` | `1h` | User access JWT TTL |
| `REFRESH_TTL` | `720h` (30d) | User refresh token TTL |
| `BOOTSTRAP_ADMIN_EMAIL` | (empty) | First-start admin email; created if absent, untouched if present |
| `BOOTSTRAP_ADMIN_PASSWORD` | (empty) | First-start admin password |
| `USER_STORAGE_LIMIT_MB` | `100` | Per-user current-version storage ceiling (bytes of payload) |
| `USER_APP_TOKEN_LIMIT` | `100` | Max distinct `(user, app_id)` app_tokens per user |
| `HISTORY_PER_APP` | `50` | Max history rows kept per `(user, app_id)`; `0` disables trimming |
| `MAX_PAYLOAD_BYTES` | `4194304` (4 MB) | Per-PUT payload ceiling |
| `APP_TOKEN_PREFIX` | `1rc_` | Plaintext app_token prefix (brandable) |

## Architecture

Request flow: `cmd/server/main.go` loads `config`, opens+migrates `db`, calls `db.BootstrapAdmin` (idempotent), wires `server.New`, runs `http.Server` with graceful shutdown on SIGINT/SIGTERM.

`internal/server/server.go` is the single source of truth for routes and middleware order (`recoverMW` → `logMW` → mux). It uses Go 1.22+ method-pattern routing. Five middleware classes:

1. **Public** (`GET /health`) — no auth
2. **Credential** (`POST /auth/{register,login,refresh}`) — body auth, no middleware
3. **UserMW** (`/auth/logout`, `/apps`, `/me/*`) — JWT verification, injects `uid` + `is_admin` into context
4. **UserMW + AdminMW** (`POST /admin/apps`) — requires `is_admin` claim
5. **AppTokenMW** (`GET|PUT /apps/{app_id}/config`) — opaque token DB lookup, injects `(uid, app_id)`

### Packages

- **`config`** — env loader; fails fast on weak `JWT_SECRET`.
- **`db`** — `Open` sets `journal_mode=WAL`, `foreign_keys=1`, `busy_timeout=5000`, and **`SetMaxOpenConns(1)`** (single writer, concurrent readers). `Migrate` applies embedded `schema.sql` (idempotent, all `CREATE TABLE IF NOT EXISTS`) and inserts `schema_version=2`. `BootstrapAdmin` creates admin from env on first start.
- **`auth`**:
  - `password.go` — bcrypt cost 12
  - `jwt.go` — HS256 with explicit `*jwt.SigningMethodHMAC` assertion (blocks `alg=none` downgrade). `Claims{UserID, Email, IsAdmin, RegisteredClaims}`.
  - `middleware.go` — `UserMW`/`AdminMW`/`AppTokenMW` + context helpers `UserID`/`IsAdmin`/`AppToken`
  - `id.go` — `NewID()` returns 32-char hex from 16 random bytes (used by users, refresh_tokens, app_tokens)
- **`handler`** — one file per route group. Shared helpers `writeJSON`/`writeError` in `errors.go`. **All error responses are `{"error":"<code>"}`** (some include extra fields like `current_*` or `limit_bytes`).
  - `auth.go` — Register/Login/Refresh/Logout (user JWT, no `app_id` involvement)
  - `apps.go` — `ListApps`/`GetApp` (user JWT, public app metadata)
  - `me.go` — `CreateAppToken`/`ListMyTokens`/`DeleteAppToken`/`DeleteAppData`/`GetQuota` (user JWT)
  - `admin.go` — `AdminCreateApp`/`AdminListApps`/`AdminGetApp`/`AdminPatchApp`/`AdminDeleteApp`/`AdminPromoteUser` (admin JWT, reverse-domain `appIDRegex` validation)
  - `sync.go` — `GetConfig`/`PutConfig` with optimistic lock, `?force=true`, 4 MB cap, storage quota, history trim
- **`model`** — DTOs only (`User`, `App`, `CreateAppRequest`, `CreateAppTokenRequest/Response`, `AppTokenInfo`, `Config`, `PutConfigRequest`, auth DTOs).

### Data model (schema.sql v2)

- `users(id, email UNIQUE NOCASE, password_hash, is_admin, created_at, updated_at)`
- `refresh_tokens(id, user_id, expires_at, created_at, revoked_at)` — user-layer only, no `app_id`
- `apps(app_id PK, display_name, description, created_at, created_by)` — admin-registered
- `app_tokens(token_hash PK, token_prefix, user_id, app_id, label, created_at, last_used_at, UNIQUE(user_id, app_id))` — only SHA-256 hash stored
- `configs(user_id, app_id, version, payload, updated_at, updated_by, PK(user_id, app_id))` — one row per `(user, app_id)`
- `config_history(id autoincr, user_id, app_id, version, payload, updated_by, created_at)` — append-only, trimmed to `HISTORY_PER_APP`

Schema is **not upgradeable from v1** — fresh DB required (scaffold-stage privilege).

### Two-layer token contract

1. **User JWT** (HS256, `Claims{uid, email, adm}`): email+password auth, used for all `/me/*`, `/apps`, `/admin/*` operations.
2. **App token** (opaque `1rc_<32hex>`, only SHA-256 stored): user explicitly issues one per `(user, app_id)` via `POST /me/apps/{app_id}/token`. Plaintext returned **exactly once**; clients must persist it. Used only for `GET|PUT /apps/{app_id}/config`.
3. Software clients never see the user's password — they only get the app_token the user pastes into them.
4. `AppTokenMW` enforces `claims.app_id == URL.{app_id}` so a token issued for app A cannot read app B's data, even for the same user.

### Optimistic locking protocol (`PUT /apps/{app_id}/config`)

1. Client `GET`s current state, reads `version`.
2. Client sends `PUT` with `{version: <current>, payload, updated_by}`.
3. Server compares to DB version:
   - Match (or new pair with `version=0`) → bump version, write configs + config_history, trim history.
   - Mismatch → `409 version_conflict` with `current_*` fields so the client can merge.
4. `?force=true` skips the version check but **NOT** the storage quota. Version still bumps, history still appends.

Do not bypass this protocol — clients depend on the 409 contract to detect concurrent edits.

### SQLite + `modernc.org/sqlite` gotchas

- Pure-Go driver (no CGO). DSN pragmas are URL params, set in `db.Open`.
- **`:memory:` is unsafe for parallel tests**: `modernc.org/sqlite` gives each pooled connection its own in-memory database, so goroutines see different schemas. Use temp files (see `internal/handler/testutil_test.go`'s `newTestEnv`).
- `SetMaxOpenConns(1)` is intentional; do not raise it without considering write contention.

## Code style

`.editorconfig`: **tabs** for Go, **2-space** for YAML, UTF-8 / LF / final newline / trimmed trailing whitespace (markdown excepted). Match the existing terse doc-comment style — one-line package comment per file, one-line function comments starting with the identifier name. Use `interface{}` (not `any`) for consistency with existing code.

## Error code reference

| HTTP | Code | When |
|---|---|---|
| 400 | `invalid_json` | body parse failure |
| 400 | `invalid_email_or_password` | register/login format check |
| 400 | `invalid_app_id` | app_id fails regex or `display_name` empty |
| 400 | `missing_updated_by` | PUT config without `updated_by` |
| 401 | `unauthorized` | missing Authorization header |
| 401 | `invalid_token` | JWT invalid/expired or app_token not found |
| 401 | `invalid_credentials` | login email/password mismatch |
| 401 | `invalid_refresh_token` | refresh_token missing/expired/revoked |
| 403 | `forbidden` | non-admin hitting admin endpoint; cross-app app_token use |
| 404 | `not_found` | resource does not exist |
| 409 | `email_already_registered` | register email conflict |
| 409 | `app_id_exists` | admin create app_id conflict |
| 409 | `version_conflict` | PUT version mismatch (includes `current_*` fields) |
| 413 | `payload_too_large` | single PUT > `MAX_PAYLOAD_BYTES` (includes `max_bytes`) |
| 413 | `storage_quota_exceeded` | user total storage > `USER_STORAGE_LIMIT_MB` (includes `used_bytes`/`limit_bytes`) |
| 413 | `app_token_limit_reached` | user app_token count at limit (includes `limit`) |
| 500 | `internal` | panic or DB error |

## Deploy

`deploy/install.sh` provisions a Ubuntu/Debian VPS: creates a `cfgsync` system user, downloads the release binary into `/opt/cfgsync/bin`, generates `JWT_SECRET` into `/etc/cfgsync/env` (0600), installs the systemd unit (`scripts/cfgsync.service` with `ProtectSystem=strict`/`NoNewPrivileges`) and a backup cron (`scripts/backup.sh`, SQLite online `.backup`, default 30-day retention). Caddy (`deploy/Caddyfile`) terminates TLS and reverse-proxies to `127.0.0.1:28972`. Releases are cut by pushing a `v*` tag; `release.yml` builds `linux-amd64` with `-ldflags="-s -w"` and attaches a `.sha256`.

## Known gaps (deferred to subsequent releases)

- Rate limiting (brute-force on `/auth/login`)
- Metrics / tracing (only `log.Printf` access log today)
- Admin-side WebUI (intentionally out of scope; admin uses REST API directly)
- Client SDKs
- v1 → v2 in-place migration (currently requires fresh DB)
