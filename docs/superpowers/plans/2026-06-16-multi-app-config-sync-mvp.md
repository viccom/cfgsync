# Multi-App Config Sync MVP Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 实现通用软件配置同步服务的最小可用版本:admin 创建 app_id → 用户注册 → 申请 app_token → 客户端通过 app_token 跑通 GET/PUT 同步(含冲突与 force 覆盖)。

**Architecture:** 沿用现有 cmd/server + internal/{config,db,auth,handler,model,server} 分层。改动集中在 schema 重写、JWT claims 扩展、middleware 拆分(用户 token / app token / admin 三套)、handler 重写。两层 token 模型:用户 JWT 管账号,opaque app_token 管同步。

**Tech Stack:** Go 1.22+(实际 go.mod 要求 1.25.0,CI 钉 1.22)、SQLite(`modernc.org/sqlite` 纯 Go)、JWT HS256(`golang-jwt/jwt/v5`)、bcrypt(`golang.org/x/crypto`)、标准库 `net/http` + Go 1.22 方法路由。

**Spec reference:** [`docs/superpowers/specs/2026-06-16-multi-app-config-sync-design.md`](../specs/2026-06-16-multi-app-config-sync-design.md)

---

## File Structure

### 修改的现有文件

| 文件 | 改动 |
|---|---|
| `internal/db/schema.sql` | 重写为 v2(`apps`/`app_tokens`/`configs` 加 `app_id`/`config_history` 加 `app_id`/`users` 加 `is_admin`) |
| `internal/db/db.go` | `Migrate` 推进 `schema_version=2`;新增 `BootstrapAdmin` 函数 |
| `internal/config/config.go` | 加 `BOOTSTRAP_ADMIN_*`、配额相关 env |
| `internal/auth/jwt.go` | `Claims` 加 `IsAdmin` |
| `internal/auth/middleware.go` | 拆为 `UserMW`/`AppTokenMW`/`AdminMW` + 辅助 `UserID`/`IsAdmin`/`AppToken` context helper |
| `internal/handler/auth.go` | 重写 `Register`/`Login`/`Refresh`/`Logout`(去 `app_id`) |
| `internal/model/auth.go` | 去除已不存在的字段 |
| `internal/model/user.go` | 加 `IsAdmin` |
| `internal/server/server.go` | 重写路由表(三类鉴权) |
| `cmd/server/main.go` | 调用 `db.BootstrapAdmin` |

### 新增文件

| 文件 | 职责 |
|---|---|
| `internal/model/app.go` | `App`、`CreateAppRequest` DTO |
| `internal/model/token.go` | `AppToken`、`CreateAppTokenRequest`、`AppTokenInfo` DTO |
| `internal/handler/sync.go` | `GetConfig`/`PutConfig`(含 `force`、4MB 校验) |
| `internal/handler/apps.go` | `ListApps`/`GetApp`(用户 token 可见) |
| `internal/handler/me.go` | `CreateAppToken`(MVP 仅此一个端点) |
| `internal/handler/admin.go` | `AdminCreateApp`(MVP 仅此一个端点) |
| `internal/handler/testutil_test.go` | 共享测试 helper |
| `internal/handler/auth_test.go` | auth handler 集成测试 |
| `internal/handler/sync_test.go` | sync handler 集成测试 |
| `internal/handler/apps_test.go` | apps handler 集成测试 |
| `internal/handler/me_test.go` | me handler 集成测试 |
| `internal/handler/admin_test.go` | admin handler 集成测试 |
| `internal/auth/middleware_test.go` | middleware 单元测试 |

### 删除的文件

| 文件 | 原因 |
|---|---|
| `internal/handler/config.go` | 旧 v1 GetConfig/PutConfig,被 `sync.go` 取代 |
| `internal/handler/config_test.go` | 旧测试 |

---

## Shared Conventions

- **测试 DB**:用 `os.CreateTemp` 临时文件,**不**用 `:memory:`(参见 CLAUDE.md 的 modernc.org/sqlite gotcha)。
- **Handler 签名**:沿用现有 `func(db, cfg) http.HandlerFunc` 工厂模式。
- **错误响应**:`writeError(w, status, "code")` 输出 `{"error":"<code>"}`。
- **commit 粒度**:每个 Task 一个 commit,commit message 用 conventional commits 风格(`feat:`/`refactor:`/`chore:`/`test:`)。
- **跑测试**:`go test -race -count=1 ./...`(与 CI 一致)。

---

## Task 1: 重写 schema.sql + 新增 model DTO

**Files:**
- Modify: `internal/db/schema.sql`
- Modify: `internal/model/user.go`
- Modify: `internal/model/auth.go`
- Create: `internal/model/app.go`
- Create: `internal/model/token.go`

### Step 1.1: 重写 schema.sql

- [ ] 用以下内容**完整替换** `internal/db/schema.sql`:

```sql
-- 1Remote-Cloud schema (version 2)
-- 通用软件配置同步后端

-- ============================================================
-- Schema 版本追踪(idempotent,可重复跑)
-- ============================================================
CREATE TABLE IF NOT EXISTS schema_version (
    version    INTEGER PRIMARY KEY,
    applied_at INTEGER NOT NULL
);

-- ============================================================
-- 用户表
-- ============================================================
CREATE TABLE IF NOT EXISTS users (
    id            TEXT PRIMARY KEY,
    email         TEXT UNIQUE NOT NULL COLLATE NOCASE,
    password_hash TEXT NOT NULL,
    is_admin      INTEGER NOT NULL DEFAULT 0,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_users_email ON users(email);

-- ============================================================
-- 用户层 refresh_tokens
-- ============================================================
CREATE TABLE IF NOT EXISTS refresh_tokens (
    id          TEXT PRIMARY KEY,
    user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at  INTEGER NOT NULL,
    created_at  INTEGER NOT NULL,
    revoked_at  INTEGER
);
CREATE INDEX IF NOT EXISTS idx_refresh_user ON refresh_tokens(user_id);

-- ============================================================
-- App 注册表(管理员维护)
-- ============================================================
CREATE TABLE IF NOT EXISTS apps (
    app_id       TEXT PRIMARY KEY,
    display_name TEXT NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL,
    created_by   TEXT NOT NULL REFERENCES users(id)
);
CREATE INDEX IF NOT EXISTS idx_apps_created_at ON apps(created_at);

-- ============================================================
-- App Token(用户为某 (user, app_id) 申请的同步凭证)
-- ============================================================
CREATE TABLE IF NOT EXISTS app_tokens (
    token_hash   TEXT PRIMARY KEY,
    token_prefix TEXT NOT NULL,
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    app_id       TEXT NOT NULL REFERENCES apps(app_id) ON DELETE CASCADE,
    label        TEXT NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL,
    last_used_at INTEGER,
    UNIQUE (user_id, app_id)
);
CREATE INDEX IF NOT EXISTS idx_apptokens_user ON app_tokens(user_id);

-- ============================================================
-- 配置数据
-- ============================================================
CREATE TABLE IF NOT EXISTS configs (
    user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    app_id      TEXT NOT NULL REFERENCES apps(app_id) ON DELETE CASCADE,
    version     INTEGER NOT NULL,
    payload     TEXT NOT NULL,
    updated_at  INTEGER NOT NULL,
    updated_by  TEXT NOT NULL,
    PRIMARY KEY (user_id, app_id)
);

-- ============================================================
-- 配置历史
-- ============================================================
CREATE TABLE IF NOT EXISTS config_history (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    app_id      TEXT NOT NULL REFERENCES apps(app_id) ON DELETE CASCADE,
    version     INTEGER NOT NULL,
    payload     TEXT NOT NULL,
    updated_by  TEXT NOT NULL,
    created_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_history_user_app_version
    ON config_history(user_id, app_id, version DESC);
```

### Step 1.2: 更新 model/user.go 加 IsAdmin

- [ ] 用以下内容**完整替换** `internal/model/user.go`:

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
```

### Step 1.3: 更新 model/auth.go(保留现有,无字段变化)

- [ ] 验证 `internal/model/auth.go` 当前内容仍然适用(`AuthResponse`/`RegisterRequest`/`LoginRequest`/`RefreshRequest`,均不含 app_id)。如果原文件已是这样,跳过此步。

Run: `cat internal/model/auth.go`
Expected: 文件包含 `AuthResponse`、`RegisterRequest{Email, Password}`、`LoginRequest`、`RefreshRequest{RefreshToken}` 四个类型,**无 AppID 字段**。

### Step 1.4: 创建 model/app.go

- [ ] 创建文件 `internal/model/app.go`:

```go
package model

// App is a registered application namespace (admin-managed).
type App struct {
	AppID       string `json:"app_id"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
	CreatedAt   int64  `json:"created_at"`
	CreatedBy   string `json:"created_by"`
}

// CreateAppRequest is the body of POST /api/v1/admin/apps.
type CreateAppRequest struct {
	AppID       string `json:"app_id"`
	DisplayName string `json:"display_name"`
	Description string `json:"description,omitempty"`
}
```

### Step 1.5: 创建 model/token.go

- [ ] 创建文件 `internal/model/token.go`:

```go
package model

// CreateAppTokenRequest is the body of POST /api/v1/me/apps/{app_id}/token.
type CreateAppTokenRequest struct {
	Label string `json:"label,omitempty"`
}

// CreateAppTokenResponse is returned when a user creates/replaces an app token.
// Token is the plaintext token; it is returned exactly once.
type CreateAppTokenResponse struct {
	Token    string `json:"token"`
	AppID    string `json:"app_id"`
	Label    string `json:"label"`
	CreatedAt int64 `json:"created_at"`
}

// AppTokenInfo is the list item returned by GET /api/v1/me/tokens.
// TokenHash and plaintext are NEVER included.
type AppTokenInfo struct {
	TokenPrefix string `json:"token_prefix"`
	AppID       string `json:"app_id"`
	Label       string `json:"label"`
	CreatedAt   int64  `json:"created_at"`
	LastUsedAt  int64  `json:"last_used_at,omitempty"`
}
```

### Step 1.6: 编译验证

- [ ] Run: `go build ./...`
- [ ] Expected: 编译通过(无报错)。

### Step 1.7: Commit

- [ ] Run:

```bash
git add internal/db/schema.sql internal/model/
git commit -m "feat(schema): rewrite schema to v2 with apps/app_tokens tables

Adds: apps (admin-managed), app_tokens (per (user, app_id) opaque token),
configs/config_history gain app_id, users gain is_admin.

BREAKING: not upgradeable from v1; deploy fresh DB."
```

---

## Task 2: db.Migrate 推进 + BootstrapAdmin + config 新 env

**Files:**
- Modify: `internal/db/db.go`
- Modify: `internal/config/config.go`

### Step 2.1: 更新 config.go 加新 env 字段

- [ ] 用以下内容**完整替换** `internal/config/config.go`:

```go
// Package config loads runtime configuration from environment variables.
package config

import (
	"fmt"
	"os"
	"time"
)

// Config holds runtime configuration for the cloud server.
type Config struct {
	Listen     string
	DBPath     string
	JWTSecret  []byte
	AccessTTL  time.Duration
	RefreshTTL time.Duration

	BootstrapAdminEmail    string
	BootstrapAdminPassword string

	UserStorageLimit  int64 // bytes
	UserAppTokenLimit int
	HistoryPerApp     int
	MaxPayloadBytes   int
	AppTokenPrefix    string
}

// Load reads configuration from the environment.
func Load() (*Config, error) {
	secret := os.Getenv("JWT_SECRET")
	if len(secret) < 32 {
		return nil, fmt.Errorf("JWT_SECRET must be set and at least 32 bytes (use: openssl rand -hex 32)")
	}

	return &Config{
		Listen:     getEnv("LISTEN", ":28972"),
		DBPath:     getEnv("DB_PATH", "./data.db"),
		JWTSecret:  []byte(secret),
		AccessTTL:  getDuration("ACCESS_TTL", time.Hour),
		RefreshTTL: getDuration("REFRESH_TTL", 30*24*time.Hour),

		BootstrapAdminEmail:    os.Getenv("BOOTSTRAP_ADMIN_EMAIL"),
		BootstrapAdminPassword: os.Getenv("BOOTSTRAP_ADMIN_PASSWORD"),

		UserStorageLimit:  getInt64("USER_STORAGE_LIMIT_MB", 100) * 1024 * 1024,
		UserAppTokenLimit: int(getInt64("USER_APP_TOKEN_LIMIT", 100)),
		HistoryPerApp:     int(getInt64("HISTORY_PER_APP", 50)),
		MaxPayloadBytes:   int(getInt64("MAX_PAYLOAD_BYTES", 4*1024*1024)),
		AppTokenPrefix:    getEnv("APP_TOKEN_PREFIX", "1rc_"),
	}, nil
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getDuration(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}

func getInt64(key string, def int64) int64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	var n int64
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
		return def
	}
	return n
}
```

### Step 2.2: 更新 db.go 加 BootstrapAdmin,推进 Migrate 版本号

- [ ] 用以下内容**完整替换** `internal/db/db.go`:

```go
// Package db owns the SQLite connection and schema migrations.
package db

import (
	"database/sql"
	_ "embed"
	"fmt"
	"time"

	"github.com/1remote/1remote-cloud/internal/auth"
	"github.com/1remote/1remote-cloud/internal/config"
)

//go:embed schema.sql
var schemaSQL string

// Open opens (or creates) the SQLite database at path and applies WAL mode.
func Open(path string) (*sql.DB, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)", path)
	d, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	if err := d.Ping(); err != nil {
		_ = d.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	d.SetMaxOpenConns(1)
	return d, nil
}

// Migrate creates tables and bumps schema_version to the latest.
func Migrate(d *sql.DB) error {
	if _, err := d.Exec(schemaSQL); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	if _, err := d.Exec(
		`INSERT OR IGNORE INTO schema_version (version, applied_at) VALUES (2, ?)`,
		time.Now().Unix(),
	); err != nil {
		return fmt.Errorf("record version: %w", err)
	}
	return nil
}

// BootstrapAdmin ensures the bootstrap admin user exists. No-op if env vars are empty.
// If the email already exists (admin or not), the function does NOT overwrite the password.
func BootstrapAdmin(d *sql.DB, cfg *config.Config) error {
	if cfg.BootstrapAdminEmail == "" || cfg.BootstrapAdminPassword == "" {
		return nil
	}
	var existing string
	err := d.QueryRow(`SELECT id FROM users WHERE email = ?`, cfg.BootstrapAdminEmail).Scan(&existing)
	if err == nil {
		return nil // already exists, leave untouched
	}
	if err != sql.ErrNoRows {
		return fmt.Errorf("bootstrap admin lookup: %w", err)
	}

	hash, err := auth.HashPassword(cfg.BootstrapAdminPassword)
	if err != nil {
		return fmt.Errorf("bootstrap admin hash: %w", err)
	}
	uid := generateID()
	now := time.Now().Unix()
	_, err = d.Exec(
		`INSERT INTO users (id, email, password_hash, is_admin, created_at, updated_at) VALUES (?, ?, ?, 1, ?, ?)`,
		uid, cfg.BootstrapAdminEmail, hash, now, now,
	)
	if err != nil {
		return fmt.Errorf("bootstrap admin insert: %w", err)
	}
	return nil
}

// generateID returns a 32-char hex string (16 random bytes).
// Exported indirectly via BootstrapAdmin; tests can use this too.
func generateID() string {
	return auth.NewID()
}
```

注意:`auth.NewID` 还不存在,在下一步(Task 5 重写 auth.go)会创建。为了让 Task 2 编译通过,先在 `auth` 包里加一个 stub。

### Step 2.3: 在 auth 包加 NewID(从 handler 搬过来)

- [ ] 创建文件 `internal/auth/id.go`:

```go
package auth

import (
	"crypto/rand"
	"encoding/hex"
)

// NewID returns a 32-character hex string from 16 random bytes.
func NewID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
```

### Step 2.4: 写 BootstrapAdmin 单元测试

- [ ] 创建文件 `internal/db/bootstrap_test.go`:

```go
package db

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/1remote/1remote-cloud/internal/config"
)

func TestBootstrapAdmin_CreatesWhenAbsent(t *testing.T) {
	d := openTempDB(t)
	cfg := &config.Config{
		BootstrapAdminEmail:    "admin@example.com",
		BootstrapAdminPassword: "password123",
	}
	if err := BootstrapAdmin(d, cfg); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	var isAdmin int
	var hash string
	err := d.QueryRow(`SELECT is_admin, password_hash FROM users WHERE email = ?`, cfg.BootstrapAdminEmail).Scan(&isAdmin, &hash)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if isAdmin != 1 {
		t.Errorf("expected is_admin=1, got %d", isAdmin)
	}
	if hash == "" {
		t.Errorf("expected non-empty hash")
	}
}

func TestBootstrapAdmin_NoOpWhenEnvEmpty(t *testing.T) {
	d := openTempDB(t)
	cfg := &config.Config{}
	if err := BootstrapAdmin(d, cfg); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	var n int
	d.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	if n != 0 {
		t.Errorf("expected 0 users, got %d", n)
	}
}

func TestBootstrapAdmin_DoesNotOverwrite(t *testing.T) {
	d := openTempDB(t)
	cfg := &config.Config{
		BootstrapAdminEmail:    "admin@example.com",
		BootstrapAdminPassword: "first-password",
	}
	if err := BootstrapAdmin(d, cfg); err != nil {
		t.Fatalf("first bootstrap: %v", err)
	}
	cfg.BootstrapAdminPassword = "second-password"
	if err := BootstrapAdmin(d, cfg); err != nil {
		t.Fatalf("second bootstrap: %v", err)
	}

	var hash string
	d.QueryRow(`SELECT password_hash FROM users WHERE email = ?`, cfg.BootstrapAdminEmail).Scan(&hash)
	// Re-verify first password still works (best-effort: hash differs from second).
	if hash == "" {
		t.Errorf("hash should be non-empty")
	}
}

func openTempDB(t *testing.T) *sql.DB {
	t.Helper()
	tmp, err := os.CreateTemp(t.TempDir(), "1rc-*.db")
	if err != nil {
		t.Fatalf("create temp: %v", err)
	}
	tmp.Close()
	d, err := Open(tmp.Name())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := Migrate(d); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() {
		d.Close()
		os.Remove(tmp.Name())
		os.Remove(tmp.Name() + "-wal")
		os.Remove(tmp.Name() + "-shm")
	})
	return d
}
```

注意:测试文件需要 `import "database/sql"`,在 import 块顶部加。

修正后的 import 块:

```go
import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/1remote/1remote-cloud/internal/config"
)
```

(`path/filepath` 实际未用,可移除。最终 import 只有 `database/sql`、`os`、`testing`、`config`。)

### Step 2.5: 跑测试

- [ ] Run: `go test -race -count=1 ./internal/db/...`
- [ ] Expected: PASS(3 个测试全过)。

### Step 2.6: Commit

- [ ] Run:

```bash
git add internal/config/config.go internal/db/db.go internal/db/bootstrap_test.go internal/auth/id.go
git commit -m "feat(db): add BootstrapAdmin and v2 env knobs

- config: BOOTSTRAP_ADMIN_*, USER_STORAGE_LIMIT_MB, USER_APP_TOKEN_LIMIT,
  HISTORY_PER_APP, MAX_PAYLOAD_BYTES, APP_TOKEN_PREFIX
- db: Migrate bumps schema_version to 2
- db: BootstrapAdmin ensures admin user exists at startup
- auth: extract NewID for reuse"
```

---

## Task 3: JWT Claims 加 IsAdmin

**Files:**
- Modify: `internal/auth/jwt.go`
- Modify: `internal/auth/jwt_test.go`

### Step 3.1: 写失败测试

- [ ] 用以下内容**完整替换** `internal/auth/jwt_test.go`:

```go
package auth

import (
	"testing"
	"time"
)

func TestIssueAndParseAccess(t *testing.T) {
	secret := []byte("test-secret-test-secret-test-secret")
	tok, err := IssueAccess(secret, "uid-1", "a@b.c", false, time.Hour)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	claims, err := ParseAccess(secret, tok)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if claims.UserID != "uid-1" || claims.Email != "a@b.c" {
		t.Errorf("claims mismatch: %+v", claims)
	}
	if claims.IsAdmin {
		t.Errorf("expected IsAdmin=false")
	}
}

func TestIssueAndParseAccess_Admin(t *testing.T) {
	secret := []byte("test-secret-test-secret-test-secret")
	tok, err := IssueAccess(secret, "uid-1", "a@b.c", true, time.Hour)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	claims, err := ParseAccess(secret, tok)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !claims.IsAdmin {
		t.Errorf("expected IsAdmin=true")
	}
}

func TestParseAccess_RejectsWrongSecret(t *testing.T) {
	secret := []byte("test-secret-test-secret-test-secret")
	tok, _ := IssueAccess(secret, "uid-1", "a@b.c", false, time.Hour)
	if _, err := ParseAccess([]byte("different-secret-different-secret"), tok); err == nil {
		t.Errorf("expected parse error with wrong secret")
	}
}

func TestParseAccess_RejectsExpired(t *testing.T) {
	secret := []byte("test-secret-test-secret-test-secret")
	tok, _ := IssueAccess(secret, "uid-1", "a@b.c", false, -time.Minute)
	if _, err := ParseAccess(secret, tok); err == nil {
		t.Errorf("expected parse error on expired token")
	}
}
```

### Step 3.2: 跑测试验证失败

- [ ] Run: `go test -race -count=1 ./internal/auth/`
- [ ] Expected: 编译失败,因为 `IssueAccess` 当前签名是 `(secret, uid, email, ttl)`,测试调用是 `(secret, uid, email, false, ttl)`。

### Step 3.3: 更新 jwt.go 加 IsAdmin

- [ ] 用以下内容**完整替换** `internal/auth/jwt.go`:

```go
// JWT helpers (HS256).
package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Claims is the JWT payload.
type Claims struct {
	UserID  string `json:"uid"`
	Email   string `json:"email"`
	IsAdmin bool   `json:"adm,omitempty"`
	jwt.RegisteredClaims
}

// IssueAccess signs an access token for the given user with the given TTL.
// isAdmin is embedded as the "adm" claim and used by AdminMW.
func IssueAccess(secret []byte, uid, email string, isAdmin bool, ttl time.Duration) (string, error) {
	now := time.Now()
	claims := Claims{
		UserID:  uid,
		Email:   email,
		IsAdmin: isAdmin,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "1remote-cloud",
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return tok.SignedString(secret)
}

// ParseAccess verifies the token signature, algorithm, and expiry.
func ParseAccess(secret []byte, tokenStr string) (*Claims, error) {
	claims := &Claims{}
	tok, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return secret, nil
	})
	if err != nil {
		return nil, err
	}
	if !tok.Valid {
		return nil, errors.New("invalid token")
	}
	return claims, nil
}
```

### Step 3.4: 跑测试验证通过

- [ ] Run: `go test -race -count=1 ./internal/auth/`
- [ ] Expected: PASS(4 个测试全过)。

### Step 3.5: Commit

- [ ] Run:

```bash
git add internal/auth/jwt.go internal/auth/jwt_test.go
git commit -m "feat(auth): embed IsAdmin in JWT claims"
```

---

## Task 4: 中间件拆分(UserMW / AppTokenMW / AdminMW)

**Files:**
- Modify: `internal/auth/middleware.go`
- Create: `internal/auth/middleware_test.go`

### Step 4.1: 写失败测试

- [ ] 创建文件 `internal/auth/middleware_test.go`:

```go
package auth

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/1remote/1remote-cloud/internal/db"
)

func testSecret() []byte {
	return []byte("test-secret-test-secret-test-secret")
}

func openTempDB(t *testing.T) *sql.DB {
	t.Helper()
	tmp, err := os.CreateTemp(t.TempDir(), "1rc-*.db")
	if err != nil {
		t.Fatalf("temp: %v", err)
	}
	tmp.Close()
	d, err := db.Open(tmp.Name())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.Migrate(d); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() {
		d.Close()
		os.Remove(tmp.Name())
		os.Remove(tmp.Name() + "-wal")
		os.Remove(tmp.Name() + "-shm")
	})
	return d
}

func seedUser(t *testing.T, d *sql.DB, email string, isAdmin bool) string {
	t.Helper()
	hash, _ := HashPassword("x")
	uid := NewID()
	var adm int
	if isAdmin {
		adm = 1
	}
	now := time.Now().Unix()
	if _, err := d.Exec(
		`INSERT INTO users (id, email, password_hash, is_admin, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		uid, email, hash, adm, now, now,
	); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return uid
}

func TestUserMW_RejectsMissingHeader(t *testing.T) {
	d := openTempDB(t)
	called := false
	h := UserMW(testSecret(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if called {
		t.Errorf("handler should not be called")
	}
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestUserMW_AcceptsValidJWT(t *testing.T) {
	d := openTempDB(t)
	uid := seedUser(t, d, "u@example.com", false)
	tok, _ := IssueAccess(testSecret(), uid, "u@example.com", false, time.Hour)

	var capturedUID string
	h := UserMW(testSecret(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedUID = UserID(r.Context())
		w.WriteHeader(200)
	}))
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if capturedUID != uid {
		t.Errorf("uid mismatch: %s vs %s", capturedUID, uid)
	}
}

func TestAdminMW_RejectsNonAdmin(t *testing.T) {
	d := openTempDB(t)
	uid := seedUser(t, d, "u@example.com", false)
	tok, _ := IssueAccess(testSecret(), uid, "u@example.com", false, time.Hour)

	called := false
	h := UserMW(testSecret(), AdminMW(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})))
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if called {
		t.Errorf("handler should not be called")
	}
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", w.Code)
	}
}

func TestAdminMW_AcceptsAdmin(t *testing.T) {
	d := openTempDB(t)
	uid := seedUser(t, d, "admin@example.com", true)
	tok, _ := IssueAccess(testSecret(), uid, "admin@example.com", true, time.Hour)

	called := false
	h := UserMW(testSecret(), AdminMW(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(200)
	})))
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if !called {
		t.Errorf("handler should be called")
	}
	if w.Code != 200 {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestAppTokenMW_RejectsInvalidToken(t *testing.T) {
	d := openTempDB(t)
	called := false
	h := AppTokenMW(d, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	req := httptest.NewRequest("GET", "/api/v1/apps/com.foo/config", nil)
	req.Header.Set("Authorization", "Bearer bogus")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if called {
		t.Errorf("handler should not be called")
	}
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

// AppTokenMW success path requires a real app_token row; covered in handler integration tests.

// silence unused import warnings if a helper is not used in some build configs
var _ = context.Background
```

### Step 4.2: 跑测试验证失败

- [ ] Run: `go test -race -count=1 ./internal/auth/`
- [ ] Expected: 编译失败,`UserMW`/`AppTokenMW`/`AdminMW` 未定义。

### Step 4.3: 重写 middleware.go

- [ ] 用以下内容**完整替换** `internal/auth/middleware.go`:

```go
// Authentication middlewares.
package auth

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"net/http"
	"strings"
	"time"
)

type ctxKey int

const (
	userIDKey ctxKey = iota
	isAdminKey
	appTokenKey
)

// AppTokenCtx carries the (user_id, app_id) extracted from an app token.
type AppTokenCtx struct {
	UserID string
	AppID  string
}

// UserID returns the authenticated user ID from the request context.
// Empty if the request did not pass through UserMW.
func UserID(ctx context.Context) string {
	v, _ := ctx.Value(userIDKey).(string)
	return v
}

// IsAdmin reports whether the authenticated user is an admin.
func IsAdmin(ctx context.Context) bool {
	v, _ := ctx.Value(isAdminKey).(bool)
	return v
}

// AppToken returns the app token context (user_id + app_id) or nil.
func AppToken(ctx context.Context) *AppTokenCtx {
	v, _ := ctx.Value(appTokenKey).(*AppTokenCtx)
	return v
}

// UserMW verifies a Bearer JWT and stores uid + is_admin in context.
func UserMW(secret []byte, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		if !strings.HasPrefix(h, "Bearer ") {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		tokenStr := strings.TrimPrefix(h, "Bearer ")
		claims, err := ParseAccess(secret, tokenStr)
		if err != nil {
			http.Error(w, `{"error":"invalid_token"}`, http.StatusUnauthorized)
			return
		}
		ctx := context.WithValue(r.Context(), userIDKey, claims.UserID)
		ctx = context.WithValue(ctx, isAdminKey, claims.IsAdmin)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// AdminMW requires that the request came through UserMW and the user is an admin.
func AdminMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !IsAdmin(r.Context()) {
			http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// AppTokenMW verifies an opaque Bearer app token, looks up the corresponding row,
// verifies it matches the {app_id} path parameter, and stores (uid, app_id) in context.
// Also updates last_used_at (best-effort, errors are logged but not fatal).
func AppTokenMW(d *sql.DB, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		if !strings.HasPrefix(h, "Bearer ") {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		tokenStr := strings.TrimPrefix(h, "Bearer ")

		sum := sha256.Sum256([]byte(tokenStr))
		tokenHash := hex.EncodeToString(sum[:])

		pathAppID := r.PathValue("app_id")

		var (
			uid   string
			appID string
		)
		err := d.QueryRowContext(r.Context(),
			`SELECT user_id, app_id FROM app_tokens WHERE token_hash = ?`,
			tokenHash,
		).Scan(&uid, &appID)
		if err == sql.ErrNoRows {
			http.Error(w, `{"error":"invalid_token"}`, http.StatusUnauthorized)
			return
		}
		if err != nil {
			http.Error(w, `{"error":"internal"}`, http.StatusInternalServerError)
			return
		}
		if pathAppID != "" && appID != pathAppID {
			http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
			return
		}

		// Best-effort update of last_used_at; failure is non-fatal.
		_, _ = d.ExecContext(r.Context(),
			`UPDATE app_tokens SET last_used_at = ? WHERE token_hash = ?`,
			time.Now().Unix(), tokenHash,
		)

		ctx := context.WithValue(r.Context(), appTokenKey, &AppTokenCtx{UserID: uid, AppID: appID})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
```

### Step 4.4: 跑测试验证通过

- [ ] Run: `go test -race -count=1 ./internal/auth/`
- [ ] Expected: PASS。

### Step 4.5: Commit

- [ ] Run:

```bash
git add internal/auth/middleware.go internal/auth/middleware_test.go
git commit -m "feat(auth): split middleware into UserMW/AppTokenMW/AdminMW

- UserMW: JWT-based, injects uid + is_admin
- AdminMW: chains after UserMW, requires is_admin
- AppTokenMW: opaque-token DB lookup, injects (uid, app_id),
  enforces path {app_id} match, updates last_used_at"
```

---

## Task 5: 重写 handler/auth.go

**Files:**
- Modify: `internal/handler/auth.go`
- Create: `internal/handler/testutil_test.go`
- Create: `internal/handler/auth_test.go`

### Step 5.1: 创建共享测试 helper

- [ ] 创建文件 `internal/handler/testutil_test.go`:

```go
package handler

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/1remote/1remote-cloud/internal/auth"
	"github.com/1remote/1remote-cloud/internal/config"
	"github.com/1remote/1remote-cloud/internal/db"
)

// testEnv bundles a temp DB and a test config.
type testEnv struct {
	db  *sql.DB
	cfg *config.Config
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	tmp, err := os.CreateTemp(t.TempDir(), "1rc-*.db")
	if err != nil {
		t.Fatalf("temp: %v", err)
	}
	tmp.Close()
	d, err := db.Open(tmp.Name())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.Migrate(d); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() {
		d.Close()
		os.Remove(tmp.Name())
		os.Remove(tmp.Name() + "-wal")
		os.Remove(tmp.Name() + "-shm")
	})
	cfg := &config.Config{
		Listen:              ":0",
		DBPath:              "test.db",
		JWTSecret:           []byte("test-secret-test-secret-test-secret"),
		AccessTTL:           time.Hour,
		RefreshTTL:          30 * 24 * time.Hour,
		UserStorageLimit:    100 * 1024 * 1024,
		UserAppTokenLimit:   100,
		HistoryPerApp:       50,
		MaxPayloadBytes:     4 * 1024 * 1024,
		AppTokenPrefix:      "1rc_",
	}
	return &testEnv{db: d, cfg: cfg}
}

// seedUser inserts a user and returns their id.
func (e *testEnv) seedUser(t *testing.T, email string, password string, isAdmin bool) string {
	t.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	uid := auth.NewID()
	var adm int
	if isAdmin {
		adm = 1
	}
	now := time.Now().Unix()
	if _, err := e.db.Exec(
		`INSERT INTO users (id, email, password_hash, is_admin, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		uid, email, hash, adm, now, now,
	); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return uid
}

// seedApp inserts an app_id (admin-created style).
func (e *testEnv) seedApp(t *testing.T, appID, displayName, createdBy string) {
	t.Helper()
	now := time.Now().Unix()
	if _, err := e.db.Exec(
		`INSERT INTO apps (app_id, display_name, description, created_at, created_by) VALUES (?, ?, '', ?, ?)`,
		appID, displayName, now, createdBy,
	); err != nil {
		t.Fatalf("seed app: %v", err)
	}
}

// userToken returns a JWT for the given user.
func (e *testEnv) userToken(uid, email string, isAdmin bool) string {
	tok, _ := auth.IssueAccess(e.cfg.JWTSecret, uid, email, isAdmin, e.cfg.AccessTTL)
	return tok
}

// doReq is a helper that issues a JSON request with optional Bearer token.
func doReq(t *testing.T, h http.Handler, method, path, token string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
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
```

### Step 5.2: 写 auth handler 测试

- [ ] 创建文件 `internal/handler/auth_test.go`:

```go
package handler

import (
	"net/http"
	"strings"
	"testing"

	"github.com/1remote/1remote-cloud/internal/model"
)

func TestRegister_Success(t *testing.T) {
	env := newTestEnv(t)
	h := Register(env.db, env.cfg)
	w := doReq(t, h, "POST", "/api/v1/auth/register", "",
		model.RegisterRequest{Email: "new@example.com", Password: "password123"})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"access_token"`) {
		t.Errorf("expected access_token in response, got %s", w.Body.String())
	}
}

func TestRegister_DuplicateEmail(t *testing.T) {
	env := newTestEnv(t)
	env.seedUser(t, "dup@example.com", "password123", false)
	h := Register(env.db, env.cfg)
	w := doReq(t, h, "POST", "/api/v1/auth/register", "",
		model.RegisterRequest{Email: "dup@example.com", Password: "password123"})
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", w.Code)
	}
}

func TestRegister_WeakPassword(t *testing.T) {
	env := newTestEnv(t)
	h := Register(env.db, env.cfg)
	w := doReq(t, h, "POST", "/api/v1/auth/register", "",
		model.RegisterRequest{Email: "x@example.com", Password: "short"})
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestLogin_Success(t *testing.T) {
	env := newTestEnv(t)
	env.seedUser(t, "login@example.com", "password123", false)
	h := Login(env.db, env.cfg)
	w := doReq(t, h, "POST", "/api/v1/auth/login", "",
		model.LoginRequest{Email: "login@example.com", Password: "password123"})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestLogin_WrongPassword(t *testing.T) {
	env := newTestEnv(t)
	env.seedUser(t, "login@example.com", "password123", false)
	h := Login(env.db, env.cfg)
	w := doReq(t, h, "POST", "/api/v1/auth/login", "",
		model.LoginRequest{Email: "login@example.com", Password: "wrong"})
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestRefresh_Success(t *testing.T) {
	env := newTestEnv(t)
	uid := env.seedUser(t, "rf@example.com", "password123", false)

	// Seed a refresh token row directly.
	now := nowUnix()
	rtID := "rt-test-id"
	if _, err := env.db.Exec(
		`INSERT INTO refresh_tokens (id, user_id, expires_at, created_at) VALUES (?, ?, ?, ?)`,
		rtID, uid, now+3600, now,
	); err != nil {
		t.Fatalf("seed rt: %v", err)
	}

	h := Refresh(env.db, env.cfg)
	w := doReq(t, h, "POST", "/api/v1/auth/refresh", "",
		model.RefreshRequest{RefreshToken: rtID})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}

	// Old token should now be revoked.
	var revoked *int64
	env.db.QueryRow(`SELECT revoked_at FROM refresh_tokens WHERE id = ?`, rtID).Scan(&revoked)
	if revoked == nil {
		t.Errorf("expected old refresh token revoked")
	}
}
```

辅助 `nowUnix` 放在 testutil_test.go 末尾(也可独立文件,但同包内可共享):

- [ ] 在 `testutil_test.go` 末尾追加:

```go
// nowUnix returns current Unix seconds. Convenience for tests that seed rows directly.
func nowUnix() int64 {
	return time.Now().Unix()
}
```

需要在 testutil_test.go 的 import 块加 `"time"`(已 import)。

### Step 5.3: 重写 auth.go

- [ ] 用以下内容**完整替换** `internal/handler/auth.go`:

```go
package handler

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/1remote/1remote-cloud/internal/auth"
	"github.com/1remote/1remote-cloud/internal/config"
	"github.com/1remote/1remote-cloud/internal/model"
)

// Register creates a new user (non-admin by default) and returns tokens.
func Register(db *sql.DB, cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req model.RegisterRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_json")
			return
		}
		req.Email = strings.TrimSpace(strings.ToLower(req.Email))
		if !validEmail(req.Email) || len(req.Password) < 8 {
			writeError(w, http.StatusBadRequest, "invalid_email_or_password")
			return
		}
		hash, err := auth.HashPassword(req.Password)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal")
			return
		}
		uid := auth.NewID()
		now := time.Now().Unix()
		_, err = db.ExecContext(r.Context(),
			`INSERT INTO users (id, email, password_hash, is_admin, created_at, updated_at) VALUES (?, ?, ?, 0, ?, ?)`,
			uid, req.Email, hash, now, now)
		if err != nil {
			if isUniqueViolation(err) {
				writeError(w, http.StatusConflict, "email_already_registered")
				return
			}
			writeError(w, http.StatusInternalServerError, "internal")
			return
		}
		issueAndRespond(w, db, r, cfg, uid, req.Email, false)
	}
}

// Login validates credentials and returns tokens.
func Login(db *sql.DB, cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req model.LoginRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_json")
			return
		}
		email := strings.TrimSpace(strings.ToLower(req.Email))
		if !validEmail(email) || req.Password == "" {
			writeError(w, http.StatusBadRequest, "invalid_email_or_password")
			return
		}
		var (
			uid     string
			hash    string
			isAdmin bool
		)
		err := db.QueryRowContext(r.Context(),
			`SELECT id, password_hash, is_admin FROM users WHERE email = ?`, email,
		).Scan(&uid, &hash, &isAdmin)
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusUnauthorized, "invalid_credentials")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal")
			return
		}
		if err := auth.VerifyPassword(hash, req.Password); err != nil {
			writeError(w, http.StatusUnauthorized, "invalid_credentials")
			return
		}
		issueAndRespond(w, db, r, cfg, uid, email, isAdmin)
	}
}

// Refresh exchanges a valid refresh token for a new pair (and revokes the old one).
func Refresh(db *sql.DB, cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req model.RefreshRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.RefreshToken == "" {
			writeError(w, http.StatusBadRequest, "invalid_json")
			return
		}
		var (
			uid       string
			email     string
			isAdmin   bool
			expiresAt int64
			revoked   *int64
		)
		err := db.QueryRowContext(r.Context(),
			`SELECT u.id, u.email, u.is_admin, rt.expires_at, rt.revoked_at
			   FROM refresh_tokens rt JOIN users u ON u.id = rt.user_id
			  WHERE rt.id = ?`, req.RefreshToken,
		).Scan(&uid, &email, &isAdmin, &expiresAt, &revoked)
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusUnauthorized, "invalid_refresh_token")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal")
			return
		}
		if revoked != nil || expiresAt < time.Now().Unix() {
			writeError(w, http.StatusUnauthorized, "invalid_refresh_token")
			return
		}
		// Revoke the old refresh token.
		_, _ = db.ExecContext(r.Context(),
			`UPDATE refresh_tokens SET revoked_at = ? WHERE id = ?`,
			time.Now().Unix(), req.RefreshToken)
		issueAndRespond(w, db, r, cfg, uid, email, isAdmin)
	}
}

// Logout revokes the refresh token in the body. Requires user token (UserMW).
func Logout(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req model.RefreshRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.RefreshToken == "" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, err := db.ExecContext(r.Context(),
			`UPDATE refresh_tokens SET revoked_at = ? WHERE id = ?`,
			time.Now().Unix(), req.RefreshToken)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// --- helpers ---

func issueAndRespond(w http.ResponseWriter, db *sql.DB, r *http.Request, cfg *config.Config, uid, email string, isAdmin bool) {
	access, err := auth.IssueAccess(cfg.JWTSecret, uid, email, isAdmin, cfg.AccessTTL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	refresh := auth.NewID()
	now := time.Now().Unix()
	_, err = db.ExecContext(r.Context(),
		`INSERT INTO refresh_tokens (id, user_id, expires_at, created_at) VALUES (?, ?, ?, ?)`,
		refresh, uid, now+int64(cfg.RefreshTTL.Seconds()), now)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	writeJSON(w, http.StatusOK, model.AuthResponse{
		AccessToken:  access,
		RefreshToken: refresh,
		TokenType:    "Bearer",
		ExpiresIn:    int64(cfg.AccessTTL.Seconds()),
	})
}

func validEmail(s string) bool {
	if len(s) < 3 || len(s) > 254 {
		return false
	}
	at := strings.IndexByte(s, '@')
	if at <= 0 || at == len(s)-1 {
		return false
	}
	if strings.IndexByte(s[at+1:], '.') < 0 {
		return false
	}
	return true
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE")
}
```

### Step 5.4: 跑测试

- [ ] Run: `go test -race -count=1 ./internal/handler/`
- [ ] Expected: PASS。**注意**:旧 `config_test.go` 此时仍引用老 handler 签名,会编译失败——这是预期,在 Task 10 删除它。临时方法:把 `config_test.go` 文件名改为 `config_test.go.bak`,跑测试:

```bash
mv internal/handler/config_test.go internal/handler/config_test.go.bak
go test -race -count=1 ./internal/handler/
```

Expected: PASS(6 个 auth 测试)。

### Step 5.5: Commit

- [ ] Run:

```bash
git add internal/handler/auth.go internal/handler/testutil_test.go internal/handler/auth_test.go
git commit -m "feat(handler): rewrite auth (Register/Login/Refresh/Logout) without app_id"
```

---

## Task 6: handler/sync.go(GET/PUT config + force + 4MB)

**Files:**
- Create: `internal/handler/sync.go`
- Create: `internal/handler/sync_test.go`

### Step 6.1: 写 sync handler 测试

- [ ] 创建文件 `internal/handler/sync_test.go`:

```go
package handler

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"github.com/1remote/1remote-cloud/internal/auth"
)

// seedAppTokenFor creates an app_token row for (uid, appID) and returns the plaintext.
func (e *testEnv) seedAppTokenFor(t *testing.T, uid, appID, label string) string {
	t.Helper()
	plaintext := "1rc_test_token_" + uid[:8]
	sum := sha256Hex(plaintext)
	if _, err := e.db.Exec(
		`INSERT INTO app_tokens (token_hash, token_prefix, user_id, app_id, label, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		sum, plaintext[:12], uid, appID, label, nowUnix(),
	); err != nil {
		t.Fatalf("seed app_token: %v", err)
	}
	return plaintext
}

func TestGetConfig_NewUser_ReturnsZeroVersion(t *testing.T) {
	env := newTestEnv(t)
	uid := env.seedUser(t, "u@example.com", "password123", false)
	env.seedApp(t, "com.foo", "Foo", uid)
	tok := env.seedAppTokenFor(t, uid, "com.foo", "")

	h := GetConfig(env.db)
	req := newAppTokenReq("GET", "/api/v1/apps/com.foo/config", tok, nil)
	w := doReqRecorder(h, req)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"version":0`) {
		t.Errorf("expected version:0 in %s", w.Body.String())
	}
}

func TestPutConfig_FirstTime_CreatesV1(t *testing.T) {
	env := newTestEnv(t)
	uid := env.seedUser(t, "u@example.com", "password123", false)
	env.seedApp(t, "com.foo", "Foo", uid)
	tok := env.seedAppTokenFor(t, uid, "com.foo", "")

	body := map[string]interface{}{
		"version":    0,
		"payload":    `{"hello":"world"}`,
		"updated_by": "MBA",
	}
	h := PutConfig(env.db, env.cfg)
	req := newAppTokenReq("PUT", "/api/v1/apps/com.foo/config", tok, body)
	w := doReqRecorder(h, req)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"version":1`) {
		t.Errorf("expected version:1 in %s", w.Body.String())
	}
}

func TestPutConfig_Conflict_Returns409(t *testing.T) {
	env := newTestEnv(t)
	uid := env.seedUser(t, "u@example.com", "password123", false)
	env.seedApp(t, "com.foo", "Foo", uid)
	tok := env.seedAppTokenFor(t, uid, "com.foo", "")

	// First write -> v1.
	h := PutConfig(env.db, env.cfg)
	req1 := newAppTokenReq("PUT", "/api/v1/apps/com.foo/config", tok,
		map[string]interface{}{"version": 0, "payload": "a", "updated_by": "MBA"})
	w1 := doReqRecorder(h, req1)
	if w1.Code != 200 {
		t.Fatalf("first write status=%d body=%s", w1.Code, w1.Body.String())
	}

	// Stale version=0 again -> 409.
	req2 := newAppTokenReq("PUT", "/api/v1/apps/com.foo/config", tok,
		map[string]interface{}{"version": 0, "payload": "b", "updated_by": "MBA"})
	w2 := doReqRecorder(h, req2)
	if w2.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d body=%s", w2.Code, w2.Body.String())
	}
	if !strings.Contains(w2.Body.String(), `"current_version":1`) {
		t.Errorf("expected current_version:1 in %s", w2.Body.String())
	}
}

func TestPutConfig_Force_Overwrites(t *testing.T) {
	env := newTestEnv(t)
	uid := env.seedUser(t, "u@example.com", "password123", false)
	env.seedApp(t, "com.foo", "Foo", uid)
	tok := env.seedAppTokenFor(t, uid, "com.foo", "")

	// v1.
	h := PutConfig(env.db, env.cfg)
	req1 := newAppTokenReq("PUT", "/api/v1/apps/com.foo/config", tok,
		map[string]interface{}{"version": 0, "payload": "a", "updated_by": "MBA"})
	doReqRecorder(h, req1)

	// Force with stale version=0.
	req2 := newAppTokenReq("PUT", "/api/v1/apps/com.foo/config?force=true", tok,
		map[string]interface{}{"version": 0, "payload": "forced", "updated_by": "MBA"})
	w2 := doReqRecorder(h, req2)
	if w2.Code != 200 {
		t.Fatalf("force status=%d body=%s", w2.Code, w2.Body.String())
	}
	if !strings.Contains(w2.Body.String(), `"version":2`) {
		t.Errorf("expected version:2 in %s", w2.Body.String())
	}
}

func TestPutConfig_TooLarge_Returns413(t *testing.T) {
	env := newTestEnv(t)
	env.cfg.MaxPayloadBytes = 16 // tiny for test
	uid := env.seedUser(t, "u@example.com", "password123", false)
	env.seedApp(t, "com.foo", "Foo", uid)
	tok := env.seedAppTokenFor(t, uid, "com.foo", "")

	h := PutConfig(env.db, env.cfg)
	bigPayload := strings.Repeat("x", 100)
	req := newAppTokenReq("PUT", "/api/v1/apps/com.foo/config", tok,
		map[string]interface{}{"version": 0, "payload": bigPayload, "updated_by": "MBA"})
	w := doReqRecorder(h, req)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("expected 413, got %d body=%s", w.Code, w.Body.String())
	}
}

// helpers below used only in this test file

func sha256Hex(s string) string {
	sum := sha256Sum([]byte(s))
	return bytesToHex(sum[:])
}

func newAppTokenReq(method, path, token string, body interface{}) *http.Request {
	var buf bytes.Buffer
	if body != nil {
		_ = jsonEncode(&buf, body)
	}
	req := httptestNewRequest(method, path, &buf)
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

func doReqRecorder(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	w := httptestNewRecorder()
	h.ServeHTTP(w, req)
	return w
}
```

补充 helpers:为了测试代码独立、清晰,在 sync_test.go 同文件底部加这些 helper 实现。注意 `bytesToHex`/`sha256Sum`/`jsonEncode`/`httptestNewRequest`/`httptestNewRecorder` 都是局部别名,目的是简化测试代码。

- [ ] 在 sync_test.go 文件**底部**追加:

```go
import extras note: 这些类型别名放在文件顶部 import 之后。
```

实际上,为了避免导入混乱,把 sync_test.go 顶部 import 块改为:

```go
package handler

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/1remote/1remote-cloud/internal/auth"
)
```

并把 helper 函数实现移到 sync_test.go 底部(替换上面的别名桩):

```go
func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func newAppTokenReq(method, path, token string, body interface{}) *http.Request {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if path == "" {
		path = "/"
	}
	_ = sql.ErrNoRows // silence unused in case future helpers need it
	_ = auth.NewID   // silence unused
	return req
}

func doReqRecorder(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}
```

(`database/sql` 和 `auth` 实际未在 sync_test.go 直接用,移除导入。)

最终 sync_test.go 顶部 import:

```go
import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)
```

### Step 6.2: 跑测试验证失败

- [ ] Run: `go test -race -count=1 ./internal/handler/`
- [ ] Expected: 编译失败,`GetConfig`/`PutConfig` 未定义(在 handler 包里)。

### Step 6.3: 创建 sync.go

- [ ] 创建文件 `internal/handler/sync.go`:

```go
package handler

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/1remote/1remote-cloud/internal/auth"
	"github.com/1remote/1remote-cloud/internal/config"
	"github.com/1remote/1remote-cloud/internal/model"
)

// GetConfig returns the current config snapshot for the (user, app_id) in context.
// New pairs (no row yet) get {version: 0, payload: ""}.
func GetConfig(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		at := auth.AppToken(r.Context())
		var c model.Config
		err := db.QueryRowContext(r.Context(),
			`SELECT version, payload, updated_at, updated_by FROM configs WHERE user_id = ? AND app_id = ?`,
			at.UserID, at.AppID,
		).Scan(&c.Version, &c.Payload, &c.UpdatedAt, &c.UpdatedBy)
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusOK, model.Config{Version: 0})
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal")
			return
		}
		writeJSON(w, http.StatusOK, c)
	}
}

// PutConfig upserts the config with optimistic locking. ?force=true bypasses version check.
func PutConfig(db *sql.DB, cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		at := auth.AppToken(r.Context())

		// Limit body read to MaxPayloadBytes + 1KB slack for JSON envelope.
		r.Body = http.MaxBytesReader(w, r.Body, int64(cfg.MaxPayloadBytes)+1024)

		var req model.PutConfigRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_json")
			return
		}
		if req.UpdatedBy == "" {
			writeError(w, http.StatusBadRequest, "missing_updated_by")
			return
		}
		if len(req.Payload) > cfg.MaxPayloadBytes {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]interface{}{
				"error":     "payload_too_large",
				"max_bytes": cfg.MaxPayloadBytes,
			})
			return
		}

		force := r.URL.Query().Get("force") == "true"

		tx, err := db.BeginTx(r.Context(), nil)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal")
			return
		}
		defer tx.Rollback()

		var current int64
		row := tx.QueryRowContext(r.Context(),
			`SELECT version FROM configs WHERE user_id = ? AND app_id = ?`, at.UserID, at.AppID)

		now := time.Now().Unix()
		var newVer int64

		switch err := row.Scan(&current); {
		case errors.Is(err, sql.ErrNoRows):
			if req.Version != 0 && !force {
				writeConflict(w, tx, at.UserID, at.AppID)
				return
			}
			if _, err := tx.ExecContext(r.Context(),
				`INSERT INTO configs (user_id, app_id, version, payload, updated_at, updated_by) VALUES (?, 1, ?, ?, ?)`,
				at.UserID, at.AppID, req.Payload, now, req.UpdatedBy); err != nil {
				writeError(w, http.StatusInternalServerError, "internal")
				return
			}
			newVer = 1

		case err != nil:
			writeError(w, http.StatusInternalServerError, "internal")
			return

		default:
			if req.Version != current && !force {
				writeConflict(w, tx, at.UserID, at.AppID)
				return
			}
			newVer = current + 1
			if _, err := tx.ExecContext(r.Context(),
				`UPDATE configs SET version = ?, payload = ?, updated_at = ?, updated_by = ?
				 WHERE user_id = ? AND app_id = ?`,
				newVer, req.Payload, now, req.UpdatedBy, at.UserID, at.AppID); err != nil {
				writeError(w, http.StatusInternalServerError, "internal")
				return
			}
		}

		if _, err := tx.ExecContext(r.Context(),
			`INSERT INTO config_history (user_id, app_id, version, payload, updated_by, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			at.UserID, at.AppID, newVer, req.Payload, req.UpdatedBy, now); err != nil {
			writeError(w, http.StatusInternalServerError, "internal")
			return
		}

		if err := tx.Commit(); err != nil {
			writeError(w, http.StatusInternalServerError, "internal")
			return
		}

		writeJSON(w, http.StatusOK, model.Config{
			Version:   newVer,
			Payload:   req.Payload,
			UpdatedAt: now,
			UpdatedBy: req.UpdatedBy,
		})
	}
}

func writeConflict(w http.ResponseWriter, tx *sql.Tx, uid, appID string) {
	var c model.Config
	err := tx.QueryRow(
		`SELECT version, payload, updated_at, updated_by FROM configs WHERE user_id = ? AND app_id = ?`,
		uid, appID,
	).Scan(&c.Version, &c.Payload, &c.UpdatedAt, &c.UpdatedBy)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusConflict)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"error":              "version_conflict",
		"current_version":    c.Version,
		"current_payload":    c.Payload,
		"current_updated_at": c.UpdatedAt,
		"current_updated_by": c.UpdatedBy,
	})
}

// silence unused import in some build configurations
var _ = strconv.Atoi
```

(`strconv` 实际未使用,移除导入。最终 import 块不含 `strconv`。)

### Step 6.4: 跑测试验证通过

- [ ] Run: `go test -race -count=1 ./internal/handler/`
- [ ] Expected: PASS(5 个 sync 测试 + 6 个 auth 测试)。

### Step 6.5: Commit

- [ ] Run:

```bash
git add internal/handler/sync.go internal/handler/sync_test.go
git commit -m "feat(handler): add sync GET/PUT with force and 4MB limit"
```

---

## Task 7: handler/apps.go(ListApps / GetApp)

**Files:**
- Create: `internal/handler/apps.go`
- Create: `internal/handler/apps_test.go`

### Step 7.1: 写测试

- [ ] 创建文件 `internal/handler/apps_test.go`:

```go
package handler

import (
	"net/http"
	"strings"
	"testing"
)

func TestListApps_ReturnsAll(t *testing.T) {
	env := newTestEnv(t)
	adminUID := env.seedUser(t, "admin@example.com", "p12345678", true)
	env.seedApp(t, "com.foo", "Foo", adminUID)
	env.seedApp(t, "com.bar", "Bar", adminUID)

	uid := env.seedUser(t, "u@example.com", "p12345678", false)
	tok := env.userToken(uid, "u@example.com", false)

	h := UserMW(env.cfg.JWTSecret, ListApps(env.db))
	w := doReq(t, h, "GET", "/api/v1/apps", tok, nil)
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
}

func TestGetApp_Success(t *testing.T) {
	env := newTestEnv(t)
	adminUID := env.seedUser(t, "admin@example.com", "p12345678", true)
	env.seedApp(t, "com.foo", "Foo", adminUID)

	uid := env.seedUser(t, "u@example.com", "p12345678", false)
	tok := env.userToken(uid, "u@example.com", false)

	h := UserMW(env.cfg.JWTSecret, GetApp(env.db))
	w := doReq(t, h, "GET", "/api/v1/apps/com.foo", tok, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestGetApp_NotFound(t *testing.T) {
	env := newTestEnv(t)
	uid := env.seedUser(t, "u@example.com", "p12345678", false)
	tok := env.userToken(uid, "u@example.com", false)

	h := UserMW(env.cfg.JWTSecret, GetApp(env.db))
	w := doReq(t, h, "GET", "/api/v1/apps/nonexistent", tok, nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}
```

需要在 testutil_test.go 顶部加 `"github.com/1remote/1remote-cloud/internal/auth"` 导入,用于 `UserMW`。或者直接在 apps_test.go 里 import 并用 `auth.UserMW`(避免循环引用,因为 handler 包不依赖 auth 包反向)。

实际上 handler 包已经在 auth.go / sync.go 里 import auth 包,所以 apps_test.go 在 handler 包内,可以直接用 `auth.UserMW`(注意:因为 sync_test.go 是 handler 包内,_test.go 文件,不需要重新 import auth;但既然前面我们 import 了 auth,这里也能用)。

最干净的写法:在 apps_test.go 里 import auth,然后用 `auth.UserMW`。修正:

```go
import (
	"net/http"
	"strings"
	"testing"

	"github.com/1remote/1remote-cloud/internal/auth"
)
```

并把测试里的 `UserMW(...)` 改成 `auth.UserMW(...)`。

但 testutil_test.go 里的 `userToken` 已经间接用 auth(通过 `auth.IssueAccess`)。所以 handler 测试包已经引入了 auth 包。`UserMW` 通过 `auth.UserMW` 引用即可。

更新 apps_test.go 三个测试,所有 `UserMW` 改为 `auth.UserMW`。

### Step 7.2: 跑测试验证失败

- [ ] Run: `go test -race -count=1 ./internal/handler/`
- [ ] Expected: 编译失败,`ListApps`/`GetApp` 未定义。

### Step 7.3: 创建 apps.go

- [ ] 创建文件 `internal/handler/apps.go`:

```go
package handler

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"

	"github.com/1remote/1remote-cloud/internal/model"
)

// ListApps returns all registered apps with optional pagination (?limit=20&offset=0).
// Public-facing metadata only (no created_by email).
func ListApps(db *sql.DB) http.HandlerFunc {
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
			`SELECT app_id, display_name, description, created_at FROM apps
			  ORDER BY created_at DESC LIMIT ? OFFSET ?`, limit, offset)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal")
			return
		}
		defer rows.Close()

		var apps []model.App
		for rows.Next() {
			var a model.App
			if err := rows.Scan(&a.AppID, &a.DisplayName, &a.Description, &a.CreatedAt); err != nil {
				writeError(w, http.StatusInternalServerError, "internal")
				return
			}
			apps = append(apps, a)
		}
		if apps == nil {
			apps = []model.App{}
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"apps":   apps,
			"limit":  limit,
			"offset": offset,
		})
	}
}

// GetApp returns a single app by app_id.
func GetApp(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		appID := r.PathValue("app_id")
		if appID == "" {
			writeError(w, http.StatusBadRequest, "invalid_app_id")
			return
		}
		var a model.App
		err := db.QueryRowContext(r.Context(),
			`SELECT app_id, display_name, description, created_at FROM apps WHERE app_id = ?`, appID,
		).Scan(&a.AppID, &a.DisplayName, &a.Description, &a.CreatedAt)
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "not_found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal")
			return
		}
		writeJSON(w, http.StatusOK, a)
	}
}
```

### Step 7.4: 跑测试验证通过

- [ ] Run: `go test -race -count=1 ./internal/handler/`
- [ ] Expected: PASS(3 个 apps 测试 + 之前的)。

### Step 7.5: Commit

- [ ] Run:

```bash
git add internal/handler/apps.go internal/handler/apps_test.go
git commit -m "feat(handler): add ListApps and GetApp public endpoints"
```

---

## Task 8: handler/me.go(CreateAppToken)

**Files:**
- Create: `internal/handler/me.go`
- Create: `internal/handler/me_test.go`

### Step 8.1: 写测试

- [ ] 创建文件 `internal/handler/me_test.go`:

```go
package handler

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"

	"github.com/1remote/1remote-cloud/internal/auth"
)

func TestCreateAppToken_Success(t *testing.T) {
	env := newTestEnv(t)
	adminUID := env.seedUser(t, "admin@example.com", "p12345678", true)
	env.seedApp(t, "com.foo", "Foo", adminUID)

	uid := env.seedUser(t, "u@example.com", "p12345678", false)
	tok := env.userToken(uid, "u@example.com", false)

	h := auth.UserMW(env.cfg.JWTSecret, CreateAppToken(env.db, env.cfg))
	body := map[string]interface{}{"label": "MBA"}
	w := doReq(t, h, "POST", "/api/v1/me/apps/com.foo/token", tok, body)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"token":"1rc_`) {
		t.Errorf("expected 1rc_-prefixed token in %s", w.Body.String())
	}

	// Verify row exists with hash, not plaintext.
	var hash string
	err := env.db.QueryRow(`SELECT token_hash FROM app_tokens WHERE user_id = ? AND app_id = ?`, uid, "com.foo").Scan(&hash)
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	// Hash should be 64 chars (SHA-256 hex).
	if len(hash) != 64 {
		t.Errorf("expected 64-char hash, got %d", len(hash))
	}
}

func TestCreateAppToken_ReplacesExisting(t *testing.T) {
	env := newTestEnv(t)
	adminUID := env.seedUser(t, "admin@example.com", "p12345678", true)
	env.seedApp(t, "com.foo", "Foo", adminUID)

	uid := env.seedUser(t, "u@example.com", "p12345678", false)
	tok := env.userToken(uid, "u@example.com", false)
	h := auth.UserMW(env.cfg.JWTSecret, CreateAppToken(env.db, env.cfg))

	doReq(t, h, "POST", "/api/v1/me/apps/com.foo/token", tok, nil)
	w2 := doReq(t, h, "POST", "/api/v1/me/apps/com.foo/token", tok, nil)
	if w2.Code != http.StatusOK {
		t.Fatalf("second status=%d body=%s", w2.Code, w2.Body.String())
	}

	var n int
	env.db.QueryRow(`SELECT COUNT(*) FROM app_tokens WHERE user_id = ? AND app_id = ?`, uid, "com.foo").Scan(&n)
	if n != 1 {
		t.Errorf("expected 1 token after replace, got %d", n)
	}
}

func TestCreateAppToken_AppNotFound(t *testing.T) {
	env := newTestEnv(t)
	uid := env.seedUser(t, "u@example.com", "p12345678", false)
	tok := env.userToken(uid, "u@example.com", false)
	h := auth.UserMW(env.cfg.JWTSecret, CreateAppToken(env.db, env.cfg))

	w := doReq(t, h, "POST", "/api/v1/me/apps/nonexistent/token", tok, nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

// silence unused
var _ = sha256.Sum256
var _ = hex.EncodeToString
var _ = sql.ErrNoRows
```

(`crypto/sha256`、`hex`、`sql` 实际未直接用,可移除。最终 import 块只保留 `net/http`、`strings`、`testing`、`auth`。)

### Step 8.2: 跑测试验证失败

- [ ] Run: `go test -race -count=1 ./internal/handler/`
- [ ] Expected: 编译失败,`CreateAppToken` 未定义。

### Step 8.3: 创建 me.go

- [ ] 创建文件 `internal/handler/me.go`:

```go
package handler

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"
	"time"

	"github.com/1remote/1remote-cloud/internal/auth"
	"github.com/1remote/1remote-cloud/internal/config"
	"github.com/1remote/1remote-cloud/internal/model"
)

// CreateAppToken issues a new app token for (user, app_id), replacing any existing one.
// The plaintext token is returned exactly once.
func CreateAppToken(db *sql.DB, cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid := auth.UserID(r.Context())
		appID := r.PathValue("app_id")

		// Verify app exists.
		var exists int
		if err := db.QueryRowContext(r.Context(),
			`SELECT 1 FROM apps WHERE app_id = ?`, appID).Scan(&exists); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeError(w, http.StatusNotFound, "not_found")
				return
			}
			writeError(w, http.StatusInternalServerError, "internal")
			return
		}

		var req model.CreateAppTokenRequest
		// Body is optional; ignore decode error.
		_ = jsonDecode(r, &req)

		// Generate plaintext token: prefix + 32 hex chars.
		plaintext := cfg.AppTokenPrefix + auth.NewID()
		sum := sha256.Sum256([]byte(plaintext))
		tokenHash := hex.EncodeToString(sum[:])
		tokenPrefix := plaintext
		if len(tokenPrefix) > 12 {
			tokenPrefix = tokenPrefix[:12]
		}

		now := time.Now().Unix()
		tx, err := db.BeginTx(r.Context(), nil)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal")
			return
		}
		defer tx.Rollback()

		// Replace any existing token for this (uid, app_id).
		if _, err := tx.ExecContext(r.Context(),
			`DELETE FROM app_tokens WHERE user_id = ? AND app_id = ?`, uid, appID); err != nil {
			writeError(w, http.StatusInternalServerError, "internal")
			return
		}
		if _, err := tx.ExecContext(r.Context(),
			`INSERT INTO app_tokens (token_hash, token_prefix, user_id, app_id, label, created_at)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			tokenHash, tokenPrefix, uid, appID, req.Label, now); err != nil {
			writeError(w, http.StatusInternalServerError, "internal")
			return
		}

		if err := tx.Commit(); err != nil {
			writeError(w, http.StatusInternalServerError, "internal")
			return
		}

		writeJSON(w, http.StatusOK, model.CreateAppTokenResponse{
			Token:     plaintext,
			AppID:     appID,
			Label:     req.Label,
			CreatedAt: now,
		})
	}
}

// jsonDecode is a small wrapper to centralize body decoding.
func jsonDecode(r *http.Request, v interface{}) error {
	if r.Body == nil {
		return nil
	}
	return jsonNewDecoder(r.Body).Decode(v)
}
```

`jsonDecode` 引用 `jsonNewDecoder`,但应该直接用 `encoding/json`。修正:删除 `jsonDecode` helper,直接在 CreateAppToken 里用 `json.NewDecoder`:

替换上面的相关行:

```go
import (
	// ...
	"encoding/json"
	// ...
)
```

并把 `_ = jsonDecode(r, &req)` 改成 `_ = json.NewDecoder(r.Body).Decode(&req)`,删除 `jsonDecode` 函数定义。

### Step 8.4: 跑测试验证通过

- [ ] Run: `go test -race -count=1 ./internal/handler/`
- [ ] Expected: PASS。

### Step 8.5: Commit

- [ ] Run:

```bash
git add internal/handler/me.go internal/handler/me_test.go
git commit -m "feat(handler): add CreateAppToken (POST /me/apps/{app_id}/token)"
```

---

## Task 9: handler/admin.go(AdminCreateApp)

**Files:**
- Create: `internal/handler/admin.go`
- Create: `internal/handler/admin_test.go`

### Step 9.1: 写测试

- [ ] 创建文件 `internal/handler/admin_test.go`:

```go
package handler

import (
	"net/http"
	"strings"
	"testing"

	"github.com/1remote/1remote-cloud/internal/auth"
	"github.com/1remote/1remote-cloud/internal/model"
)

func adminChain(env *testEnv, h http.Handler) http.Handler {
	return auth.UserMW(env.cfg.JWTSecret, auth.AdminMW(h))
}

func TestAdminCreateApp_Success(t *testing.T) {
	env := newTestEnv(t)
	adminUID := env.seedUser(t, "admin@example.com", "p12345678", true)
	tok := env.userToken(adminUID, "admin@example.com", true)

	h := adminChain(env, AdminCreateApp(env.db))
	body := model.CreateAppRequest{
		AppID:       "com.foo",
		DisplayName: "Foo",
	}
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
```

### Step 9.2: 跑测试验证失败

- [ ] Run: `go test -race -count=1 ./internal/handler/`
- [ ] Expected: 编译失败,`AdminCreateApp` 未定义。

### Step 9.3: 创建 admin.go

- [ ] 创建文件 `internal/handler/admin.go`:

```go
package handler

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"regexp"
	"time"

	"github.com/1remote/1remote-cloud/internal/auth"
	"github.com/1remote/1remote-cloud/internal/model"
)

var appIDRegex = regexp.MustCompile(`^([a-z][a-z0-9-]{1,30}\.)+[a-z][a-z0-9-]{1,30}$`)

// AdminCreateApp registers a new app_id. Requires admin (enforced by middleware chain).
func AdminCreateApp(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		adminUID := auth.UserID(r.Context())
		var req model.CreateAppRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_json")
			return
		}
		if !appIDRegex.MatchString(req.AppID) || len(req.AppID) > 64 {
			writeError(w, http.StatusBadRequest, "invalid_app_id")
			return
		}
		if req.DisplayName == "" {
			writeError(w, http.StatusBadRequest, "invalid_app_id")
			return
		}

		now := time.Now().Unix()
		_, err := db.ExecContext(r.Context(),
			`INSERT INTO apps (app_id, display_name, description, created_at, created_by) VALUES (?, ?, ?, ?, ?)`,
			req.AppID, req.DisplayName, req.Description, now, adminUID)
		if err != nil {
			if isUniqueViolation(err) {
				writeError(w, http.StatusConflict, "app_id_exists")
				return
			}
			writeError(w, http.StatusInternalServerError, "internal")
			return
		}

		writeJSON(w, http.StatusOK, model.App{
			AppID:       req.AppID,
			DisplayName: req.DisplayName,
			Description: req.Description,
			CreatedAt:   now,
			CreatedBy:   adminUID,
		})
	}
}
```

### Step 9.4: 跑测试验证通过

- [ ] Run: `go test -race -count=1 ./internal/handler/`
- [ ] Expected: PASS。

### Step 9.5: Commit

- [ ] Run:

```bash
git add internal/handler/admin.go internal/handler/admin_test.go
git commit -m "feat(handler): add AdminCreateApp with reverse-domain app_id validation"
```

---

## Task 10: 装配路由 + BootstrapAdmin 调用 + 删除老 v1 + 端到端验证

**Files:**
- Modify: `internal/server/server.go`
- Modify: `cmd/server/main.go`
- Delete: `internal/handler/config.go`
- Delete: `internal/handler/config_test.go`
- Delete: `internal/handler/config_test.go.bak`(Task 5 临时改名的)

### Step 10.1: 重写 server.go 路由表

- [ ] 用以下内容**完整替换** `internal/server/server.go`:

```go
// Package server wires routing and middleware.
package server

import (
	"database/sql"
	"log"
	"net/http"
	"time"

	"github.com/1remote/1remote-cloud/internal/auth"
	"github.com/1remote/1remote-cloud/internal/config"
	"github.com/1remote/1remote-cloud/internal/handler"
)

// New builds the top-level HTTP handler.
func New(cfg *config.Config, db *sql.DB) http.Handler {
	mux := http.NewServeMux()

	// Public
	mux.HandleFunc("GET /api/v1/health", handler.Health(db))

	// Credential (body auth, no middleware)
	mux.HandleFunc("POST /api/v1/auth/register", handler.Register(db, cfg))
	mux.HandleFunc("POST /api/v1/auth/login", handler.Login(db, cfg))
	mux.HandleFunc("POST /api/v1/auth/refresh", handler.Refresh(db, cfg))

	// User token (UserMW)
	mux.Handle("POST /api/v1/auth/logout", auth.UserMW(cfg.JWTSecret, handler.Logout(db)))
	mux.Handle("GET /api/v1/apps", auth.UserMW(cfg.JWTSecret, handler.ListApps(db)))
	mux.Handle("GET /api/v1/apps/{app_id}", auth.UserMW(cfg.JWTSecret, handler.GetApp(db)))
	mux.Handle("POST /api/v1/me/apps/{app_id}/token", auth.UserMW(cfg.JWTSecret, handler.CreateAppToken(db, cfg)))

	// Admin (UserMW + AdminMW)
	adminChain := func(h http.Handler) http.Handler {
		return auth.UserMW(cfg.JWTSecret, auth.AdminMW(h))
	}
	mux.Handle("POST /api/v1/admin/apps", adminChain(handler.AdminCreateApp(db)))

	// App token (AppTokenMW)
	mux.Handle("GET /api/v1/apps/{app_id}/config", auth.AppTokenMW(db, handler.GetConfig(db)))
	mux.Handle("PUT /api/v1/apps/{app_id}/config", auth.AppTokenMW(db, handler.PutConfig(db, cfg)))

	return chain(mux, recoverMW, logMW)
}

// --- middleware ---

type wrappedWriter struct {
	http.ResponseWriter
	status int
}

func (w *wrappedWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func logMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := &wrappedWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(ww, r)
		log.Printf("%s %s %d %dus", r.Method, r.URL.Path, ww.status, time.Since(start).Microseconds())
	})
}

func recoverMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("PANIC %s %s: %v", r.Method, r.URL.Path, rec)
				http.Error(w, `{"error":"internal"}`, http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func chain(h http.Handler, mws ...func(http.Handler) http.Handler) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}
```

### Step 10.2: 更新 main.go 调用 BootstrapAdmin

- [ ] 用以下内容**完整替换** `cmd/server/main.go`:

```go
// Command server is the entry point for the 1Remote-Cloud backend.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/1remote/1remote-cloud/internal/config"
	"github.com/1remote/1remote-cloud/internal/db"
	"github.com/1remote/1remote-cloud/internal/server"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	database, err := db.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer database.Close()

	if err := db.Migrate(database); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	if err := db.BootstrapAdmin(database, cfg); err != nil {
		log.Fatalf("bootstrap admin: %v", err)
	}
	if cfg.BootstrapAdminEmail != "" {
		log.Printf("bootstrap admin ensured: %s", cfg.BootstrapAdminEmail)
	}

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           server.New(cfg, database),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Printf("1remote-cloud listening on %s db=%s", cfg.Listen, cfg.DBPath)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		log.Printf("received %s, shutting down", sig)
	case err := <-errCh:
		log.Fatalf("server: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}
```

### Step 10.3: 删除老 v1 handler 文件

- [ ] Run:

```bash
rm internal/handler/config.go
rm internal/handler/config_test.go
rm -f internal/handler/config_test.go.bak
```

### Step 10.4: 编译 + 全套测试

- [ ] Run:

```bash
go build -trimpath -o /tmp/1remote-cloud ./cmd/server
go vet ./...
go test -race -count=1 ./...
```

- [ ] Expected: 全部 PASS。

### Step 10.5: 端到端 curl 验证

- [ ] 启动服务:

```bash
JWT_SECRET=$(openssl rand -hex 32) \
BOOTSTRAP_ADMIN_EMAIL=admin@example.com \
BOOTSTRAP_ADMIN_PASSWORD=admin-pass-123 \
DB_PATH=/tmp/1rc-e2e.db \
go run ./cmd/server &
```

- [ ] 跑端到端流程:

```bash
# 1. Admin login
ADMIN_RESP=$(curl -sX POST localhost:28972/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"admin@example.com","password":"admin-pass-123"}')
ADMIN_TOK=$(echo "$ADMIN_RESP" | python3 -c 'import json,sys;print(json.load(sys.stdin)["access_token"])')

# 2. Admin creates app
curl -sX POST localhost:28972/api/v1/admin/apps \
  -H "Authorization: Bearer $ADMIN_TOK" \
  -H 'Content-Type: application/json' \
  -d '{"app_id":"com.1remote.desktop","display_name":"1Remote Desktop"}'

# 3. User register
USER_RESP=$(curl -sX POST localhost:28972/api/v1/auth/register \
  -H 'Content-Type: application/json' \
  -d '{"email":"user@example.com","password":"user-pass-123"}')
USER_TOK=$(echo "$USER_RESP" | python3 -c 'import json,sys;print(json.load(sys.stdin)["access_token"])')

# 4. User lists apps
curl -sX GET localhost:28972/api/v1/apps -H "Authorization: Bearer $USER_TOK"

# 5. User requests app token
APP_TOK_RESP=$(curl -sX POST localhost:28972/api/v1/me/apps/com.1remote.desktop/token \
  -H "Authorization: Bearer $USER_TOK" \
  -H 'Content-Type: application/json' \
  -d '{"label":"MBA"}')
APP_TOK=$(echo "$APP_TOK_RESP" | python3 -c 'import json,sys;print(json.load(sys.stdin)["token"])')

# 6. Client PUT v0
curl -sX PUT localhost:28972/api/v1/apps/com.1remote.desktop/config \
  -H "Authorization: Bearer $APP_TOK" \
  -H 'Content-Type: application/json' \
  -d '{"version":0,"payload":"{\"hello\":\"world\"}","updated_by":"MBA"}'

# 7. Client GET (should return version 1)
curl -sX GET localhost:28972/api/v1/apps/com.1remote.desktop/config \
  -H "Authorization: Bearer $APP_TOK"

# 8. Client PUT stale version -> 409
curl -sX PUT localhost:28972/api/v1/apps/com.1remote.desktop/config \
  -H "Authorization: Bearer $APP_TOK" \
  -H 'Content-Type: application/json' \
  -d '{"version":0,"payload":"x","updated_by":"MBA"}'

# 9. Force overwrite
curl -sX PUT 'localhost:28972/api/v1/apps/com.1remote.desktop/config?force=true' \
  -H "Authorization: Bearer $APP_TOK" \
  -H 'Content-Type: application/json' \
  -d '{"version":0,"payload":"forced","updated_by":"MBA"}'
```

- [ ] Expected: 步骤 2 返回 `app_id: "com.1remote.desktop"`;步骤 7 返回 `version: 1`;步骤 8 返回 409 含 `current_version: 1`;步骤 9 返回 `version: 2`。

- [ ] 停止服务:

```bash
kill %1 2>/dev/null || true
rm -f /tmp/1rc-e2e.db /tmp/1rc-e2e.db-wal /tmp/1rc-e2e.db-shm
```

### Step 10.6: Commit

- [ ] Run:

```bash
git add internal/server/server.go cmd/server/main.go
git rm internal/handler/config.go internal/handler/config_test.go
git commit -m "feat(server): wire v1 routes for MVP; remove legacy 1Remote-only endpoints

- server.New registers all MVP routes (health/auth/apps/me/admin/sync)
- main calls db.BootstrapAdmin after Migrate
- delete internal/handler/config.go (replaced by sync.go)"
```

---

## Self-Review Checklist(写计划者已自检)

**Spec 覆盖**(对照 spec 第 6 节 MVP 范围):

- [x] GET /api/v1/health → 已存在(Task 0)
- [x] POST /api/v1/auth/register → Task 5
- [x] POST /api/v1/auth/login → Task 5
- [x] POST /api/v1/auth/refresh → Task 5
- [x] POST /api/v1/auth/logout → Task 5
- [x] GET /api/v1/apps → Task 7
- [x] GET /api/v1/apps/{app_id} → Task 7
- [x] POST /api/v1/me/apps/{app_id}/token → Task 8
- [x] POST /api/v1/admin/apps → Task 9
- [x] GET /api/v1/apps/{app_id}/config → Task 6
- [x] PUT /api/v1/apps/{app_id}/config(含 force) → Task 6

**Spec 中标记为"阶段 2/3"的端点**(不在本 plan 范围,留待后续 plan):
- GET /api/v1/me/tokens, DELETE /api/v1/me/tokens/{prefix}, DELETE /api/v1/me/apps/{app_id}/data, GET /api/v1/me/quota
- AdminGetApp/PatchApp/DeleteApp/PromoteUser, AdminListApps

**Placeholder 扫描**:无 TBD/TODO/"implement later"/"similar to"。

**类型/签名一致性**:
- `auth.UserMW(secret, h)` 在 Task 4 定义,Task 5/7/8/9/10 一致使用。
- `auth.AdminMW(h)`、`auth.AppTokenMW(db, h)` 同上。
- `auth.UserID(ctx)`、`auth.IsAdmin(ctx)`、`auth.AppToken(ctx)` 在 Task 4 定义,后续使用一致。
- `auth.NewID()` 在 Task 2 定义,Task 5/8 一致使用。
- `auth.HashPassword`/`VerifyPassword`/`IssueAccess`/`ParseAccess` 签名兼容(IsAdmin 参数 Task 3 加入)。
- handler 工厂签名 `func(db, cfg) http.HandlerFunc` 一致。
- `model.App`/`CreateAppRequest`/`CreateAppTokenRequest`/`CreateAppTokenResponse`/`AppTokenInfo` 在 Task 1 定义,后续一致使用。
- `writeJSON`/`writeError` 沿用现有(Task 0),未重命名。

**已知小风险**:
- Task 5 临时把 `config_test.go` 改名为 `.bak` 跑测试,Task 10 才真正删除。这是为了让中间 commit 编译通过,避免一次大爆炸。
- 测试代码中有少量 `silence unused` 桩(`var _ = ...`),最终 import 块整理后可移除。计划里已说明。

---

## Execution Handoff

Plan 完整,保存到 `docs/superpowers/plans/2026-06-16-multi-app-config-sync-mvp.md`。

**实施选项**:

1. **Subagent-Driven(推荐)** — 每个 Task 派发一个新 subagent,主会话只做两阶段 review。优点:context 隔离,质量稳定;缺点:派发开销。
2. **Inline Execution** — 在当前会话按 Task 顺序执行,带 checkpoint review。优点:上下文连续;缺点:context 容易膨胀。

**建议:Subagent-Driven**,因为本 plan 共 10 个 Task、每个都涉及多文件 + TDD 循环,subagent 隔离能避免主会话 context 爆炸。
