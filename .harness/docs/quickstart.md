# 1Remote-Cloud — Quickstart

For full design see [`cloud-sync-rfc.md`](../../1Remote/blob/main/.harness/docs/cloud-sync-rfc.md)
and [`cloud-sync-implementation.md`](../../1Remote/blob/main/.harness/docs/cloud-sync-implementation.md).

## Local dev

```bash
go mod download
JWT_SECRET=$(openssl rand -hex 32) go run ./cmd/server
# In another terminal:
curl http://127.0.0.1:28972/api/v1/health
# {"status":"ok","db":"ok"}
```

## First user

```bash
curl -X POST http://127.0.0.1:28972/api/v1/auth/register \
     -H 'Content-Type: application/json' \
     -d '{"email":"you@example.com","password":"hunter2-correct-horse"}'
```

## First config write

```bash
TOK="<access_token from register response>"
curl -X PUT http://127.0.0.1:28972/api/v1/config \
     -H "Authorization: Bearer $TOK" \
     -H 'Content-Type: application/json' \
     -d '{"version":0,"payload":"{}","updated_by":"laptop-shawn"}'
```

## Deploy

See `deploy/install.sh`. Summary:

```bash
VERSION=v0.1.0 sudo bash deploy/install.sh
```
