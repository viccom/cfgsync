# 1Remote Cloud Sync Backend

Standalone backend for [1Remote](https://github.com/1Remote/1Remote)
cross-device configuration sync.

**Status**: scaffold only. Implementation per `.harness/docs/cloud-sync-implementation.md`.

## Quick start (dev)

```bash
# Requires Go 1.22+
go mod download
go test ./...
go run ./cmd/server
# Server listens on :28972 by default
# Override with env: LISTEN, DB_PATH, JWT_SECRET, ACCESS_TTL, REFRESH_TTL
```

## Layout

```
cmd/server/      entrypoint
internal/
  config/        env loader
  db/            SQLite + migrations
  auth/          bcrypt + JWT + middleware
  handler/       HTTP handlers
  model/         DTOs
  server/        routing + middleware chain
scripts/         systemd unit, backup cron
deploy/          Caddyfile, install.sh
.github/workflows/  CI (test + release)
```

## Spec

- RFC: see [`1Remote/.harness/docs/cloud-sync-rfc.md`](https://github.com/1Remote/1Remote/blob/main/.harness/docs/cloud-sync-rfc.md)
- Implementation: see [`1Remote/.harness/docs/cloud-sync-implementation.md`](https://github.com/1Remote/1Remote/blob/main/.harness/docs/cloud-sync-implementation.md)

## License

MIT
