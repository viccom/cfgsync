# 1Remote-Cloud

Generic per-user **software configuration sync** backend. Any application can sync its text-based config (up to 4 MB per app) across devices via a language-neutral REST API. Personal-use oriented — no team, role, or org concepts.

Designed so a software author can adopt it without rewriting their app: a small sidecar can watch the target app's config file and push/pull to this service. The service is platform-neutral about payload contents — what users store under a given `app_id` is the user's responsibility.

**Status**: v0.1.0 — MVP + Phase 2 complete. See [CHANGELOG.md](CHANGELOG.md).

## Quick start (dev)

```bash
# Requires Go 1.22+ (go.mod pins 1.25.0)
go mod download
JWT_SECRET=$(openssl rand -hex 32) \
BOOTSTRAP_ADMIN_EMAIL=admin@example.com \
BOOTSTRAP_ADMIN_PASSWORD=admin-pass-123 \
go run ./cmd/server
# Server listens on :28972 by default
```

In another terminal:

```bash
BASE=http://127.0.0.1:28972

# 1. Admin login
ADMIN_TOK=$(curl -sX POST $BASE/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"admin@example.com","password":"admin-pass-123"}' \
  | sed 's/.*"access_token":"\([^"]*\)".*/\1/')

# 2. Admin registers an app_id
curl -sX POST $BASE/api/v1/admin/apps \
  -H "Authorization: Bearer $ADMIN_TOK" \
  -H 'Content-Type: application/json' \
  -d '{"app_id":"com.1remote.desktop","display_name":"1Remote Desktop"}'

# 3. User registers and lists available apps
USER_TOK=$(curl -sX POST $BASE/api/v1/auth/register \
  -H 'Content-Type: application/json' \
  -d '{"email":"user@example.com","password":"user-pass-123"}' \
  | sed 's/.*"access_token":"\([^"]*\)".*/\1/')
curl -sX GET $BASE/api/v1/apps -H "Authorization: Bearer $USER_TOK"

# 4. User requests an app_token for com.1remote.desktop (plaintext returned ONCE)
APP_TOK=$(curl -sX POST $BASE/api/v1/me/apps/com.1remote.desktop/token \
  -H "Authorization: Bearer $USER_TOK" -H 'Content-Type: application/json' \
  -d '{"label":"MacBook Air"}' \
  | sed 's/.*"token":"\([^"]*\)".*/\1/')

# 5. Client uses app_token to sync config (optimistic lock + force available)
curl -sX PUT $BASE/api/v1/apps/com.1remote.desktop/config \
  -H "Authorization: Bearer $APP_TOK" -H 'Content-Type: application/json' \
  -d '{"version":0,"payload":"{\"hello\":\"world\"}","updated_by":"MBA"}'

curl -sX GET $BASE/api/v1/apps/com.1remote.desktop/config \
  -H "Authorization: Bearer $APP_TOK"
```

## Configuration (env vars)

| Var | Default | Notes |
|---|---|---|
| `JWT_SECRET` | (required) | HS256 key, must be ≥ 32 bytes. Server exits at startup if missing/short. |
| `LISTEN` | `:28972` | HTTP listen address |
| `DB_PATH` | `./data.db` | SQLite file path |
| `ACCESS_TTL` | `1h` | User access JWT TTL (`time.ParseDuration` syntax) |
| `REFRESH_TTL` | `720h` (30d) | User refresh token TTL |
| `BOOTSTRAP_ADMIN_EMAIL` | (empty) | First-start admin email; user is created if absent, left untouched if present |
| `BOOTSTRAP_ADMIN_PASSWORD` | (empty) | First-start admin password |
| `USER_STORAGE_LIMIT_MB` | `100` | Per-user current-version storage ceiling |
| `USER_APP_TOKEN_LIMIT` | `100` | Max distinct `(user, app_id)` app_tokens per user |
| `HISTORY_PER_APP` | `50` | Max `config_history` rows per `(user, app_id)`. `0` disables trimming. |
| `MAX_PAYLOAD_BYTES` | `4194304` (4 MB) | Per-PUT payload ceiling |
| `APP_TOKEN_PREFIX` | `1rc_` | Plaintext app_token prefix (brandable) |

## API surface (v1)

All paths are prefixed `/api/v1`. Errors use `{"error":"<code>"}` shape (some include extra fields like `current_*` or `limit_bytes`).

### Public

| Method | Path | Description |
|---|---|---|
| GET | `/health` | `{"status":"ok","db":"ok"}` if DB reachable |

### Credential (email + password → user JWT)

| Method | Path |
|---|---|
| POST | `/auth/register` |
| POST | `/auth/login` |
| POST | `/auth/refresh` (body: `refresh_token`) |

### User JWT (management operations)

| Method | Path | Description |
|---|---|---|
| POST | `/auth/logout` | Revoke refresh_token in body |
| GET | `/apps` | List registered apps (`?limit=20&offset=0`) |
| GET | `/apps/{app_id}` | App details |
| POST | `/me/apps/{app_id}/token` | Issue/replace app_token (plaintext returned once) |
| GET | `/me/tokens` | List own app_tokens (prefix + app_id + label + timestamps; **no plaintext/hash**) |
| DELETE | `/me/tokens/{token_prefix}` | Revoke by prefix (cross-user collision-safe) |
| DELETE | `/me/apps/{app_id}/data` | Atomic wipe of config + history + token |
| GET | `/me/quota` | `{storage_used_bytes, storage_limit_bytes, app_token_count, app_token_limit}` |

### Admin (user JWT + `is_admin` claim)

| Method | Path | Description |
|---|---|---|
| POST | `/admin/apps` | Create app_id (reverse-domain format, max 64 chars) |

Future (Phase 3): `GET /admin/apps`, `GET/PATCH/DELETE /admin/apps/{id}`, `POST /admin/users/{id}/promote`.

### App token (sync operations)

| Method | Path | Description |
|---|---|---|
| GET | `/apps/{app_id}/config` | Current snapshot; new pair returns `{version:0}` |
| PUT | `/apps/{app_id}/config` | Optimistic-lock upsert; `?force=true` bypasses version check (still bumps version, still appends history) |

Conflict response (`409`): `{error: "version_conflict", current_version, current_payload, current_updated_at, current_updated_by}` — clients use this to merge.

## Architecture

See [`docs/superpowers/specs/2026-06-16-multi-app-config-sync-design.md`](docs/superpowers/specs/2026-06-16-multi-app-config-sync-design-design.md) for the full spec. Summary:

- **Two-layer tokens**: user JWT manages the account; opaque app_token (one per `(user, app_id)`) does the sync. Software clients never see the user's password.
- **Optimistic lock**: every PUT carries `version`; mismatch → 409 with current state. Clients merge.
- **`app_id` is a contract fingerprint**, admin-registered. Same `app_id` implies compatible payload schema across all clients using it.
- **SQLite single-writer + WAL**: `SetMaxOpenConns(1)` for serial writes; WAL allows concurrent readers.
- **Quotas are resource ceilings, not advisory**: `force=true` skips version check but NOT the storage quota.

## Deploy

`deploy/install.sh` provisions an Ubuntu/Debian VPS: creates a `1remote` system user, downloads the release binary into `/opt/1remote-cloud/bin`, generates `JWT_SECRET` into `/etc/1remote-cloud/env` (0600), installs the systemd unit (`scripts/1remote-cloud.service` with `ProtectSystem=strict`), and installs a backup cron (`scripts/backup.sh` using SQLite's online `.backup`).

Caddy (`deploy/Caddyfile`) terminates TLS and reverse-proxies to `127.0.0.1:28972`.

Releases are cut by pushing a `v*` tag; `.github/workflows/release.yml` builds `linux-amd64` with `-ldflags="-s -w"` and attaches a `.sha256`.

## Spec & Plan

- Design spec: [`docs/superpowers/specs/2026-06-16-multi-app-config-sync-design.md`](docs/superpowers/specs/2026-06-16-multi-app-config-sync-design.md)
- MVP implementation plan: [`docs/superpowers/plans/2026-06-16-multi-app-config-sync-mvp.md`](docs/superpowers/plans/2026-06-16-multi-app-config-sync-mvp.md)

## License

MIT
