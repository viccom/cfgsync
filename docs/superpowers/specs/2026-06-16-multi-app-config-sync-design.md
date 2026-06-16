# 1Remote-Cloud 多 App 配置同步设计

**日期**:2026-06-16
**状态**:Approved (用户授权"自行深度审查,尽快进入实施")
**适用版本**:scaffold → v1.0 第一阶段

---

## 1. 概述

将 1Remote-Cloud 从「1Remote 客户端专属同步后端」演进为「面向个人用户的通用软件配置同步服务」。平台提供统一 REST API,任何软件(由软件开发者主动适配,或通过旁路监听程序)都可接入,把任意文本类配置同步到云端。

**典型场景**:用户 U 在笔记本和台式机上同时使用 1Remote,两边都登入平台拉取/推送同一份配置;用户 U 同时使用 VSCode,通过旁路程序把 `settings.json` 同步到平台另一份命名空间下。

**平台中立原则**:平台不校验用户在某个 app_id 下存的数据是否"真的属于"那个软件。用户存什么、用得对不对,是用户的事。平台只保证 `(user_id, app_id)` 维度的强隔离、单调 version、配置历史。

---

## 2. 核心决策汇总(已与用户确认)

| # | 决策点 | 结论 |
|---|---|---|
| 1 | 加密边界 | 仅 HTTPS,服务端明文持有 payload |
| 2 | app_id 治理 | 服务端预注册,管理员创建,全局唯一 |
| 3 | app_id 语义 | 数据格式契约的指纹(同软件兼容版本系列共用) |
| 4 | admin 账号 | 普通用户 + `is_admin` 标记,env bootstrap |
| 5 | 数据模型 | 每个 `(user, app_id)` 一份 blob,4MB 单次上限 |
| 6 | token 架构 | 两层:用户 token(JWT,管理用)+ app token(opaque,同步用) |
| 7 | app token 形态 | opaque 长串,无 expiry,可吊销,UNIQUE(user, app_id),新申请自动替换旧 |
| 8 | 冲突协议 | LWW + 409 + 可选 force 端点(`?force=true`) |
| 9 | 配额 | 100MB/用户、100 app_token/用户、50 history/(user, app_id)、4MB 单 PUT |
| 10 | v1 命运 | 删除老 v1 端点,新协议从 `/api/v1` 重新开始 |
| 11 | 实施分阶段 | MVP 优先,跑通 happy path 再加固 |

---

## 3. 核心 Invariants

设计中的不变量,任何改动都不能违反:

1. **平台定位**:通用配置同步空间,不是开发者 SDK 集成平台。
2. **app_id 语义**:数据格式契约的指纹,管理员颁发。
3. **平台中立**:`(user_id, app_id)` 维度强隔离 + 单调 version + 配置历史;不校验 payload 内容。
4. **明文存储**:服务端明文持有 payload(传输层 HTTPS)。
5. **两层 token**:用户 token(JWT)管账号,app token(opaque)管同步;软件客户端永远不接触用户密码。
6. **乐观锁契约**:PUT 必须带 version,服务端 version 不匹配返回 409 + current_state,客户端自治合并。

---

## 4. 整体架构

### 4.1 进程结构(沿用现有,改动小)

```
cmd/server/main.go
  ├─ config.Load()
  ├─ db.Open(cfg.DBPath)
  ├─ db.Migrate(database)              # schema_version 推进到 2
  ├─ db.BootstrapAdmin(database, cfg)  # 首次启动创建 admin
  └─ http.Server{Handler: server.New(cfg, database)}
```

### 4.2 包结构

```
internal/
├── config/    [改] 加 BOOTSTRAP_ADMIN_*、QUOTA_* 字段
├── db/
│   ├── db.go        [改] 加 BootstrapAdmin
│   └── schema.sql   [大改] 重写,见第 5 节
├── auth/
│   ├── jwt.go         [微调] Claims 加 IsAdmin 字段(AppID 不放 JWT,因为 app token 不用 JWT)
│   ├── password.go    [不动]
│   └── middleware.go  [大改] 拆成 userMW / appTokenMW / adminMW
├── handler/
│   ├── errors.go      [不动] writeJSON / writeError
│   ├── health.go      [不动]
│   ├── auth.go        [大改] 重写 Register/Login/Refresh/Logout,去掉 app_id 概念
│   ├── apps.go        [新] GET /apps、GET /apps/{app_id}(用户 token 可见)
│   ├── me.go          [新] /me/tokens、/me/apps/{app_id}/token、/me/apps/{app_id}/data、/me/quota
│   ├── admin.go       [新] /admin/apps 增删改查、/admin/users/{id}/promote
│   └── sync.go        [新] GET/PUT /apps/{app_id}/config(app token 鉴权,含 force)
└── model/
    ├── user.go        [改] 加 IsAdmin
    ├── auth.go        [改] Register/Login/Refresh DTO 去 app_id
    ├── app.go         [新] App、CreateAppRequest
    ├── token.go       [新] AppToken、CreateTokenRequest、Quota
    └── config.go      [微调] PutConfigRequest 加 AppID 不必要(URL 里已有)
```

依赖方向:`cmd → server → handler → {auth, model, config, db}`;`db` 不依赖业务包;`model` 不依赖任何业务包;`auth` 依赖 `model` + JWT 库。

### 4.3 两类请求流

**A. 用户 token 流**(管理操作)

```
HTTP → recoverMW → logMW → mux → userMW → 具体 handler
                                        (验证 JWT,ctx 注入 uid + is_admin)
```

**B. app token 流**(同步操作)

```
HTTP → recoverMW → logMW → mux → appTokenMW → sync handler
                                        (查 app_tokens 表,ctx 注入 uid + app_id)
```

**C. admin 流**(治理操作)

```
HTTP → recoverMW → logMW → mux → userMW → adminMW → admin handler
                                        (验证 is_admin)
```

一个请求只会走其中一条路径,取决于 URL。

---

## 5. 数据模型

### 5.1 完整 schema.sql(v2)

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
    id            TEXT PRIMARY KEY,           -- 32 字符 hex(16 bytes random)
    email         TEXT UNIQUE NOT NULL COLLATE NOCASE,
    password_hash TEXT NOT NULL,               -- bcrypt cost 12
    is_admin      INTEGER NOT NULL DEFAULT 0,  -- 0/1
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_users_email ON users(email);

-- ============================================================
-- 用户层 refresh_tokens(access/refresh 双层,无 app_id)
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
    app_id       TEXT PRIMARY KEY,            -- 反向域名,如 com.1remote.desktop
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
    token_hash   TEXT PRIMARY KEY,            -- SHA-256(明文 token) hex
    token_prefix TEXT NOT NULL,               -- 明文 token 前 12 字符(列表展示用)
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    app_id       TEXT NOT NULL REFERENCES apps(app_id) ON DELETE CASCADE,
    label        TEXT NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL,
    last_used_at INTEGER,
    UNIQUE (user_id, app_id)                  -- 一对一约束
);
CREATE INDEX IF NOT EXISTS idx_apptokens_user ON app_tokens(user_id);

-- ============================================================
-- 配置数据(核心)
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
-- 配置历史(每次 PUT 追加一份)
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

### 5.2 schema_version 推进

`db.Migrate` 流程:
1. 执行 `schema.sql`(全 `CREATE TABLE IF NOT EXISTS`,idempotent)。
2. `INSERT OR IGNORE INTO schema_version (version, applied_at) VALUES (2, ...)`。

旧库(version=1)的兼容:由于 v2 修改了 `configs`/`config_history` 表结构(加 `app_id`),且 v1 没有真实用户,**不支持从 v1 库平滑升级**——部署时删 `data.db` 重来。这是 scaffold 阶段的特权,正式发布后需要写迁移脚本。

### 5.3 关键索引选择

- `idx_users_email`:登录查询。
- `idx_refresh_user`:列出/吊销用户 refresh token。
- `idx_apptokens_user`:`GET /me/tokens` 列表。
- `idx_history_user_app_version`:历史裁剪(查最老的 N+1 条)。
- `apps`/`configs`/`app_tokens` 主键已足够,无需额外索引。

---

## 6. API 完整清单

### 6.1 公开(无鉴权)

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/v1/health` | 健康检查,返回 `{"status":"ok","db":"ok"}` |

### 6.2 凭证获取(email + password → 用户 token)

| 方法 | 路径 | 请求体 | 响应 |
|---|---|---|---|
| POST | `/api/v1/auth/register` | `{email, password}` | `{access_token, refresh_token, token_type, expires_in}` |
| POST | `/api/v1/auth/login` | `{email, password}` | 同上 |

注册流程:校验邮箱格式 + 密码长度 ≥ 8 → bcrypt 哈希 → INSERT 用户 → 签发 token 对。**注册时不带 app_id**(用户层与应用层解耦)。

### 6.3 凭证刷新(refresh_token → 新用户 token)

| 方法 | 路径 | 请求体 | 响应 |
|---|---|---|---|
| POST | `/api/v1/auth/refresh` | `{refresh_token}` | 新的 `{access_token, refresh_token, ...}` |

流程:查 refresh_tokens 表 → 校验未过期且未吊销 → 吊销旧 refresh(`revoked_at` 写当前时间)→ 签发新 refresh + access。

### 6.4 用户 token 鉴权(`userMW`)

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/api/v1/auth/logout` | body: `{refresh_token}`,吊销该 refresh |
| GET | `/api/v1/apps` | 列出所有 app_id(分页,`?limit=20&offset=0`),返回 `app_id`+`display_name`+`description`+`created_at` |
| GET | `/api/v1/apps/{app_id}` | 单个 app 详情 |
| GET | `/api/v1/me/tokens` | 列出自己的 app token(`token_prefix` + `app_id` + `label` + `created_at` + `last_used_at`,**不含明文 token**) |
| POST | `/api/v1/me/apps/{app_id}/token` | 申请/替换该 (user, app_id) 的 app token;body `{label?}`;响应 `{token, app_id, created_at}`(明文 token **仅此一次返回**) |
| DELETE | `/api/v1/me/tokens/{token_prefix}` | 按 prefix 吊销自己的 app token |
| DELETE | `/api/v1/me/apps/{app_id}/data` | 删除该 (user, app_id) 的 configs + config_history + app_tokens(事务) |
| GET | `/api/v1/me/quota` | 返回 `{storage_used_bytes, storage_limit_bytes, app_token_count, app_token_limit}` |

### 6.5 用户 token + admin 鉴权(`userMW` + `adminMW`)

| 方法 | 路径 | 请求体/说明 |
|---|---|---|
| GET | `/api/v1/admin/apps` | 列出所有 app(分页) |
| POST | `/api/v1/admin/apps` | body `{app_id, display_name, description?}`;创建 app_id |
| GET | `/api/v1/admin/apps/{app_id}` | 详情(含 `created_by` email) |
| PATCH | `/api/v1/admin/apps/{app_id}` | body `{display_name?, description?}`;部分更新 |
| DELETE | `/api/v1/admin/apps/{app_id}` | 删除 app_id;**默认行为:级联删除所有用户的该 app_id 数据**(简化,后期可改为 30 天保留) |
| POST | `/api/v1/admin/users/{user_id}/promote` | 提升 user 为 admin |

### 6.6 App token 鉴权(`appTokenMW`)

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/v1/apps/{app_id}/config` | 读取该 (user, app_id) 的配置;新用户返回 `{version: 0, payload: ""}`(200,非 404) |
| PUT | `/api/v1/apps/{app_id}/config` | 乐观锁写入;body `{version, payload, updated_by}`;version 不匹配返回 409 |
| PUT | `/api/v1/apps/{app_id}/config?force=true` | 强制覆盖,忽略 version;version 仍 +1,history 追加 |

**路径冲突说明**:`GET /api/v1/apps/{app_id}`(userMW)与 `GET /api/v1/apps/{app_id}/config`(appTokenMW)在 Go 1.22 mux 下是不同路径,不会冲突。

### 6.7 鉴权机制对照

| 中间件 | 验证方式 | 注入 context |
|---|---|---|
| `userMW` | JWT 验签 + 解析 claims | `uid`, `is_admin` |
| `appTokenMW` | SHA-256(token) → 查 `app_tokens` 表 | `uid`, `app_id` |
| `adminMW` | 在 userMW 之后,校验 `is_admin == true` | 无新增 |

---

## 7. 关键流程

### 7.1 PUT /api/v1/apps/{app_id}/config(乐观锁)

```
1. appTokenMW 验证 token,ctx 拿到 (uid, app_id)
2. 解析 body: PutConfigRequest{version, payload, updated_by}
3. 校验:
   - updated_by 非空 → 否则 400
   - len(payload) <= 4MB → 否则 413 payload_too_large
   - (非 force 模式)user 当前总存储 + len(payload) - len(old_payload) <= 100MB
     → 否则 413 storage_quota_exceeded
   - app_token 数量(此处不动,在 POST /me/.../token 时校验)
4. force = query 参数 force == "true"
5. BeginTx
6. SELECT version, payload FROM configs WHERE user_id=? AND app_id=?
7. 三分支:
   a. ErrNoRows + version==0 + !force:
      INSERT configs (user_id, app_id, version=1, payload, updated_at, updated_by)
      new_ver = 1
   b. ErrNoRows + force:
      同 a(force 在新数据上无意义,但保持代码路径统一)
   c. row exists + (version == current || force):
      UPDATE configs SET version=?, payload=?, ... WHERE user_id=? AND app_id=?
      new_ver = current + 1
   d. row exists + version != current + !force:
      返回 409 version_conflict + current_state(包括 current_payload、current_version、current_updated_at、current_updated_by)
8. INSERT config_history
9. 历史裁剪:
   DELETE FROM config_history
    WHERE user_id=? AND app_id=?
      AND id NOT IN (
        SELECT id FROM config_history
         WHERE user_id=? AND app_id=?
         ORDER BY created_at DESC LIMIT 50
      )
10. Commit
11. 响应 200 + 新 Config{version: new_ver, payload, updated_at, updated_by}
```

### 7.2 POST /api/v1/me/apps/{app_id}/token

```
1. userMW 验证,ctx 拿到 uid
2. 查 apps 表确认 app_id 存在 → 否则 404
3. 查 user 当前 app_token 数量 → 若 >= 100 返回 413(用 413 表达"超额",语义上可议)
4. 生成明文 token = "1rc_" + hex(rand 32 bytes),共 68 字符
5. token_hash = SHA-256 hex;token_prefix = token[:12]
6. BEGIN Tx:
   - DELETE FROM app_tokens WHERE user_id=? AND app_id=? (自动替换旧 token)
   - INSERT app_tokens (token_hash, token_prefix, user_id, app_id, label, created_at)
   - COMMIT
7. 响应 200 + {token: 明文, app_id, label, created_at}(明文仅此一次)
```

### 7.3 Bootstrap admin(启动时)

```
1. 从 cfg 读 BOOTSTRAP_ADMIN_EMAIL / BOOTSTRAP_ADMIN_PASSWORD
2. 若两者任一为空,跳过 bootstrap(允许"无 admin"模式启动,但不能创建 app_id)
3. SELECT id FROM users WHERE email = ?
4. 若存在,跳过(不覆盖密码)
5. 若不存在:
   - hash = bcrypt(BOOTSTRAP_ADMIN_PASSWORD, cost=12)
   - INSERT users (id=random_hex_32, email, password_hash=hash, is_admin=1, ...)
6. log 出来:"bootstrap admin ensured: <email>"
```

---

## 8. 中间件实现要点

### 8.1 `userMW(secret, next) http.Handler`

- 取 `Authorization: Bearer ${jwt}` → JWT 验签 → 解析 claims。
- claims 形状:
  ```go
  type Claims struct {
      UserID  string `json:"uid"`
      Email   string `json:"email"`
      IsAdmin bool   `json:"adm,omitempty"`
      jwt.RegisteredClaims
  }
  ```
- 注入 ctx:`uid`、`is_admin`。
- 失败:401 `{"error":"unauthorized"}` 或 `{"error":"invalid_token"}`。

### 8.2 `appTokenMW(db, next) http.Handler`

- 取 `Authorization: Bearer ${token}` → SHA-256 hash → 查 `app_tokens` 表。
- 校验:`app_tokens.app_id` 必须等于 URL `{app_id}` 路径参数(防止用 A app 的 token 调 B app 的端点)。
- 注入 ctx:`uid`、`app_id`。
- 异步或同步更新 `last_used_at`(第一阶段:同步更新,实现简单;若性能问题再优化)。
- 失败:401 `{"error":"invalid_token"}` 或 403 `{"error":"forbidden"}`(app_id 不匹配)。

### 8.3 `adminMW(next) http.Handler`

- 从 ctx 读 `is_admin`,false 则 403 `{"error":"forbidden"}`。
- 必须在 `userMW` 之后链式调用。

### 8.4 server.New 路由表

```go
func New(cfg *config.Config, db *sql.DB) http.Handler {
    mux := http.NewServeMux()

    // 公开
    mux.HandleFunc("GET /api/v1/health", handler.Health(db))

    // 凭证获取(无中间件,body 鉴权)
    mux.HandleFunc("POST /api/v1/auth/register", handler.Register(db, cfg))
    mux.HandleFunc("POST /api/v1/auth/login", handler.Login(db, cfg))
    mux.HandleFunc("POST /api/v1/auth/refresh", handler.Refresh(db, cfg))

    // 用户 token
    mux.Handle("POST /api/v1/auth/logout", userMW(cfg.JWTSecret, handler.Logout(db)))
    mux.Handle("GET /api/v1/apps", userMW(cfg.JWTSecret, handler.ListApps(db)))
    mux.Handle("GET /api/v1/apps/{app_id}", userMW(cfg.JWTSecret, handler.GetApp(db)))
    mux.Handle("GET /api/v1/me/tokens", userMW(cfg.JWTSecret, handler.ListMyTokens(db)))
    mux.Handle("POST /api/v1/me/apps/{app_id}/token", userMW(cfg.JWTSecret, handler.CreateAppToken(db, cfg)))
    mux.Handle("DELETE /api/v1/me/tokens/{token_prefix}", userMW(cfg.JWTSecret, handler.DeleteAppToken(db)))
    mux.Handle("DELETE /api/v1/me/apps/{app_id}/data", userMW(cfg.JWTSecret, handler.DeleteAppData(db)))
    mux.Handle("GET /api/v1/me/quota", userMW(cfg.JWTSecret, handler.GetQuota(db, cfg)))

    // Admin
    adminChain := func(h http.Handler) http.Handler {
        return userMW(cfg.JWTSecret, adminMW(h))
    }
    mux.Handle("GET /api/v1/admin/apps", adminChain(handler.AdminListApps(db)))
    mux.Handle("POST /api/v1/admin/apps", adminChain(handler.AdminCreateApp(db)))
    mux.Handle("GET /api/v1/admin/apps/{app_id}", adminChain(handler.AdminGetApp(db)))
    mux.Handle("PATCH /api/v1/admin/apps/{app_id}", adminChain(handler.AdminPatchApp(db)))
    mux.Handle("DELETE /api/v1/admin/apps/{app_id}", adminChain(handler.AdminDeleteApp(db)))
    mux.Handle("POST /api/v1/admin/users/{user_id}/promote", adminChain(handler.AdminPromoteUser(db)))

    // App token
    mux.Handle("GET /api/v1/apps/{app_id}/config", appTokenMW(db, handler.GetConfig(db)))
    mux.Handle("PUT /api/v1/apps/{app_id}/config", appTokenMW(db, handler.PutConfig(db, cfg)))

    return chain(mux, recoverMW, logMW)
}
```

---

## 9. 错误响应约定

**所有错误响应统一形状**:`{"error": "<code>"}`,部分错误附带额外字段。

| HTTP | error code | 触发场景 | 附加字段 |
|---|---|---|---|
| 400 | `invalid_json` | body 解析失败 | |
| 400 | `invalid_email_or_password` | register/login 格式校验失败 | |
| 400 | `invalid_app_id` | app_id 不符合反向域名格式 | |
| 400 | `missing_updated_by` | PUT 未提供 updated_by | |
| 401 | `unauthorized` | 缺 Authorization 头 | |
| 401 | `invalid_token` | JWT 无效/过期 或 app_token 查不到 | |
| 401 | `invalid_credentials` | login 邮箱密码不匹配 | |
| 401 | `invalid_refresh_token` | refresh_token 不存在/过期/已吊销 | |
| 403 | `forbidden` | 普通用户访问 admin 端点;app_token 路径不匹配 | |
| 404 | `not_found` | 资源不存在(app_id、token_prefix 等) | |
| 409 | `email_already_registered` | register 邮箱冲突 | |
| 409 | `app_id_exists` | admin 创建 app_id 冲突 | |
| 409 | `version_conflict` | PUT version 不匹配 | `current_version`、`current_payload`、`current_updated_at`、`current_updated_by` |
| 413 | `payload_too_large` | 单次 PUT payload > 4MB | `max_bytes` |
| 413 | `storage_quota_exceeded` | 用户总存储超 100MB | `used_bytes`、`limit_bytes` |
| 413 | `app_token_limit_reached` | 用户 app_token 数已达上限 | `limit` |
| 500 | `internal` | panic 或 DB 错误 | |

---

## 10. 配置(env 变量)

| 变量 | 默认值 | 说明 |
|---|---|---|
| `LISTEN` | `:28972` | 监听地址 |
| `DB_PATH` | `./data.db` | SQLite 文件路径 |
| `JWT_SECRET` | (必填) | HS256 密钥,≥32 字节 |
| `ACCESS_TTL` | `1h` | access JWT 过期 |
| `REFRESH_TTL` | `720h` (30d) | refresh token 过期 |
| `BOOTSTRAP_ADMIN_EMAIL` | (空) | 首次启动创建的 admin 邮箱 |
| `BOOTSTRAP_ADMIN_PASSWORD` | (空) | admin 初始密码 |
| `USER_STORAGE_LIMIT_MB` | `100` | 每用户当前 payload 总存储上限(MB) |
| `USER_APP_TOKEN_LIMIT` | `100` | 每用户 app_token 数量上限 |
| `HISTORY_PER_APP` | `50` | 每 (user, app_id) 历史条数上限 |
| `MAX_PAYLOAD_BYTES` | `4194304` (4MB) | 单次 PUT payload 字节上限 |
| `APP_TOKEN_PREFIX` | `1rc_` | app_token 明文前缀(可定制品牌) |

---

## 11. 测试策略

### 11.1 测试模式

- **DB**:用临时文件(`os.CreateTemp`),不用 `:memory:`(参见 CLAUDE.md 关于 modernc.org/sqlite 的 gotcha)。
- **HTTP**:`net/http/httptest`,沿用 `config_test.go` 的 `httptest.NewRequest` + `httptest.NewRecorder` 模式。
- **复用 worktree 里 `extra_test.go` 的 `newTestServerWithToken` helper 模式**(临时文件 DB + 标准用户 + 标准 token)。

### 11.2 测试覆盖矩阵

| 单元测试 | 覆盖 |
|---|---|
| `auth/jwt_test.go` | issue/parse、错误密钥、过期 |
| `auth/password_test.go` | hash/verify |
| `auth/middleware_test.go`(新) | userMW 各种失败场景、appTokenMW 查表、adminMW 拒绝非 admin |

| 集成测试 | 覆盖 |
|---|---|
| `handler/auth_test.go` | register 重复、login 错密码、refresh 流转、logout 吊销 |
| `handler/sync_test.go` | 新用户 GET 返回 v0、PUT 创建 v1、PUT 冲突 409、force 覆盖、4MB 拒绝、quota 超限 |
| `handler/me_test.go` | 申请 token、列表不含明文、吊销、删除 app 数据 |
| `handler/admin_test.go` | 创建/列出/删除 app、非 admin 403、promote |
| `handler/apps_test.go` | 公开列表、单个详情、不存在 404 |

### 11.3 阶段性端到端验证(curl)

每阶段结束跑一遍 curl 流程,见第 12 节。

---

## 12. 分阶段实施计划

### 阶段 1(MVP)— 核心同步跑通

**目标**:admin 创建 app → 用户注册 → 申请 app_token → 客户端 PUT/GET 同步,包括冲突与 force。

**任务清单**:

1. 重写 `internal/db/schema.sql`(v2)。
2. 改 `internal/db/db.go`:
   - `Migrate` 推进 `schema_version` 到 2。
   - 新增 `BootstrapAdmin(db, cfg)` 函数。
3. 改 `internal/config/config.go`:加新 env 字段。
4. 改 `internal/auth/jwt.go`:`Claims` 加 `IsAdmin`。
5. 大改 `internal/auth/middleware.go`:拆 `userMW`、`appTokenMW`、`adminMW`。
6. 重写 `internal/handler/auth.go`:`Register`/`Login`/`Refresh`/`Logout`(去 app_id)。
7. 新增 `internal/handler/sync.go`:`GetConfig`/`PutConfig`(含 force、4MB 校验、乐观锁)。
8. 新增 `internal/handler/me.go`:仅 `CreateAppToken`(其他端点阶段 2 加)。
9. 新增 `internal/handler/apps.go`:`ListApps`/`GetApp`。
10. 新增 `internal/model/{app,token}.go`。
11. 改 `internal/server/server.go`:注册阶段 1 涉及的路由。
12. 删除老 v1 测试,新增阶段 1 测试。
13. 改 `cmd/server/main.go`:加 `db.BootstrapAdmin` 调用。

**验证 curl 流程**:

```bash
# 启动(带 bootstrap admin)
JWT_SECRET=$(openssl rand -hex 32) \
BOOTSTRAP_ADMIN_EMAIL=admin@example.com \
BOOTSTRAP_ADMIN_PASSWORD=admin-pass-123 \
go run ./cmd/server &

# Admin 登录拿用户 token
ADMIN_TOK=$(curl -sX POST localhost:28972/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"admin@example.com","password":"admin-pass-123"}' | jq -r .access_token)

# Admin 创建 app_id
curl -sX POST localhost:28972/api/v1/admin/apps \
  -H "Authorization: Bearer $ADMIN_TOK" \
  -H 'Content-Type: application/json' \
  -d '{"app_id":"com.1remote.desktop","display_name":"1Remote Desktop"}'

# 普通用户注册
USER_TOK=$(curl -sX POST localhost:28972/api/v1/auth/register \
  -H 'Content-Type: application/json' \
  -d '{"email":"user@example.com","password":"user-pass-123"}' | jq -r .access_token)

# 用户申请 app_token
APP_TOK=$(curl -sX POST localhost:28972/api/v1/me/apps/com.1remote.desktop/token \
  -H "Authorization: Bearer $USER_TOK" \
  -H 'Content-Type: application/json' \
  -d '{"label":"MacBook Air"}' | jq -r .token)

# 同步 PUT
curl -sX PUT localhost:28972/api/v1/apps/com.1remote.desktop/config \
  -H "Authorization: Bearer $APP_TOK" \
  -H 'Content-Type: application/json' \
  -d '{"version":0,"payload":"{\"hello\":\"world\"}","updated_by":"MBA"}'

# 同步 GET
curl -sX GET localhost:28972/api/v1/apps/com.1remote.desktop/config \
  -H "Authorization: Bearer $APP_TOK"

# 冲突场景:用旧 version 重 PUT → 409
curl -sX PUT localhost:28972/api/v1/apps/com.1remote.desktop/config \
  -H "Authorization: Bearer $APP_TOK" \
  -H 'Content-Type: application/json' \
  -d '{"version":0,"payload":"x","updated_by":"MBA"}'

# Force 覆盖
curl -sX PUT 'localhost:28972/api/v1/apps/com.1remote.desktop/config?force=true' \
  -H "Authorization: Bearer $APP_TOK" \
  -H 'Content-Type: application/json' \
  -d '{"version":0,"payload":"forced","updated_by":"MBA"}'
```

**完成标准**:上述 curl 全部走通,测试 `go test -race ./...` 通过。

### 阶段 2(加固)— 配额、历史、token 管理

**目标**:配额触发拒绝、历史裁剪生效、token 管理 API 完整。

**任务清单**:

1. 完善 `internal/handler/me.go`:加 `ListMyTokens`/`DeleteAppToken`/`DeleteAppData`/`GetQuota`。
2. 加配额逻辑:
   - `PutConfig`:4MB 单 PUT(阶段 1 已做)+ 100MB 总存储。
   - `CreateAppToken`:100 个上限校验。
3. 加历史裁剪:每次 PUT 后保留最近 `HISTORY_PER_APP` 条。
4. 测试:配额触发、历史裁剪、token 管理。

**完成标准**:配额触发拒绝、历史裁剪后剩 50 条、token 列表/吊销正常。

### 阶段 3(治理完善)— Admin API 完整集

**目标**:admin 全套管理 API、公开 GET /apps、promote user。

**任务清单**:

1. 完善 `internal/handler/admin.go`:`AdminListApps`/`AdminCreateApp`/`AdminGetApp`/`AdminPatchApp`/`AdminDeleteApp`/`AdminPromoteUser`。
2. 完善 `internal/handler/apps.go`:`ListApps`/`GetApp` 分页。
3. 测试:admin 完整流程,非 admin 403。

**完成标准**:admin 全流程可用,所有端点测试覆盖。

---

## 13. 安全考虑

| 风险 | 缓解 |
|---|---|
| SQL 注入 | 全部 parameterized query(`?` 占位符),无字符串拼接 |
| JWT 算法降级 | `ParseAccess` 显式断言 `*jwt.SigningMethodHMAC`,拒绝 `none`/RSA |
| 密码爆破 | bcrypt cost 12;rate limiting 第一阶段不做(列入 gap) |
| JWT_SECRET 弱 | 启动时强制 ≥32 字节 |
| app_token 数据库泄露后复用 | 只存 SHA-256 hash,不存明文 |
| app_token 路径混淆 | `appTokenMW` 强制 `token.app_id == URL.app_id` |
| payload 巨型导致 OOM | 4MB hard limit,Content-Length + 实际字节双校验 |
| 历史 DDoS 增长 | 每 (user, app_id) 自动裁剪到 50 条 |
| admin 提权滥用 | adminMW 校验,所有 admin 操作走 JWT(可审计) |
| Bootstrap 密码弱 | env 设置,运维责任;首次启动后 admin 应通过 PATCH 改密(第一阶段 admin 改密走 SQL,阶段 3 加 API) |

---

## 14. 已知 Gap(第一阶段不做,列入未来工作)

| 项 | 说明 | 优先级 |
|---|---|---|
| Rate limiting | 防暴力撞库、防 PUT 滥用 | 高(阶段 4) |
| Metrics / tracing | Prometheus / OTLP | 中 |
| WebUI 管理 | 用户/admin 的图形界面 | 中(用户明确暂不做) |
| 应用密码(per-app password) | 比 app_token 更细粒度的访问控制 | 低 |
| 设备指纹 / 异地告警 | 安全增强 | 低 |
| 配置内容搜索 / 索引 | 平台中立原则下意义不大 | 低 |
| Diff/patch 同步 | 大 payload 增量同步;4MB 内 YAGNI | 低 |
| 多 admin / 审计日志 | 当前 is_admin 单点;操作审计 | 中 |
| 客户端 SDK | 多语言 SDK(Go/Python/JS) | 中 |
| admin 改密 API | 第一阶段 admin 改密走 SQL | 高(阶段 3 加) |
| app_id 删除数据保留期 | 当前级联删除;30 天保留更友好 | 中 |

---

## 15. 实施前置确认

- 当前 v1 端点全部删除(`internal/handler/auth.go`、`config.go` + 测试)。
- `data.db` 不支持 v1→v2 平滑升级,部署时删除重建(scaffold 特权)。
- `refresh_tokens` 表保留(用户层 access/refresh 双层沿用)。
- 测试 DB 用临时文件,不用 `:memory:`。
- 第一阶段不引入新依赖(继续用标准库 + 现有 3 个外部包)。

---

## 16. 设计自审清单

完成本文档后,以下问题已自检:

- [x] 无 TBD/TODO/占位符
- [x] schema.sql、API、中间件三者一致(字段名、路径、token 类型)
- [x] 范围聚焦:第一阶段只做 MVP,加固/治理分阶段
- [x] 无歧义条款:
  - `logout` 走 userMW(access token 还有效时才能吊销 refresh),若 access 过期需先 refresh
  - `DELETE /me/tokens/{token_prefix}` 按 prefix 作 key,WHERE 子句带 `user_id` 防跨用户碰撞
  - `force` 在 ErrNoRows 分支无意义但代码路径统一(直接走 INSERT)
  - app_token 明文仅创建时返回一次,后续列表只展示 prefix
  - app_token `last_used_at` 同步更新(第一阶段),性能问题再优化
- [x] 安全考虑覆盖 OWASP 常见风险点
- [x] 测试矩阵覆盖所有 handler
- [x] 与现有代码风格一致(handler 工厂模式、writeJSON/writeError、临时文件 DB)
