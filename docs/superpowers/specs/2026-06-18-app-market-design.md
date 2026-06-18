# cfgsync 应用市场模块设计

**日期**: 2026-06-18
**状态**: Approved (用户授权"继续"推进)
**适用版本**: v0.5.0 (合并回 main 时打 tag)
**开发分支**: `feat/app-market` (从 `v0.4.0` 切出)

---

## 1. 概述

在 cfg-sync 配置同步功能之上，为 cfgsync 增加**应用市场模块**：开发者将自己的应用打包（固定 `.tar.gz`）上传到 cfgsync，cfgsync 解析包内 manifest 与文档，对外提供应用列表、详情、版本历史、二进制下载等公开只读端点。最终用户通过浏览器访问 WebUI 浏览、搜索、下载应用。

**定位**: 个人开发者发布与展示自己软件的简洁站点。规模 ≤ 1000 apps，单体程序够用。

**与 cfg-sync 的关系**: 应用市场是新增的独立模块，不修改现有 `/apps/{app_id}/config` 协议，cfg-sync 契约保持不变。

---

## 2. 核心决策汇总 (已与用户确认)

| # | 决策点 | 结论 |
|---|---|---|
| 1 | 发布权限 | admin 单人模式（用户=管理员=开发者）|
| 2 | 仓库位置 | 本地文件系统 `$REPO_DIR`（默认 `./repo`）|
| 3 | 上传方式 | 直接 `POST` 到 cfgsync（multipart 上传）|
| 4 | manifest 格式 | YAML（嵌套表达 + 人类编辑友好）|
| 5 | 多平台策略 | 一包多平台（manifest `platforms` 列各 OS/arch 二进制）|
| 6 | 下载方式 | cfgsync 流式 `io.Copy`（隐藏仓库路径）|
| 7 | Markdown 渲染 | 服务端 goldmark 预渲染为安全 HTML，前端直接显示 |
| 8 | Schema | 删库重构到 v3，无 v2→v3 迁移路径 |
| 9 | tags 字段 | 多标签数组（`["system", "ai", "cli"]`）|
| 10 | 限流 | 不内置，依赖外部 nginx 反代 |
| 11 | 平台命名 | 白名单：`linux-amd64`、`linux-arm64`、`windows-amd64`、`windows-arm64`、`darwin-amd64`、`darwin-arm64` |

---

## 3. 核心 Invariants

1. **cfg-sync 契约不变**：现有 `GET|PUT /apps/{app_id}/config` 行为完全保留；现有 webui 的 `/me`、`/admin` 路由不受影响
2. **公开只读**：应用市场所有展示端点对未登录用户开放；只有上传/删除走 admin 鉴权
3. **包即真理**：`manifest.yaml` 是元数据唯一来源；DB 行只是查询缓存。重新上传同版本 = 覆盖
4. **删版本即删包**：`DELETE release` 同时移除 DB 行和仓库文件（事务 commit 后再删文件，失败留孤儿不影响业务）
5. **平台白名单**：所有平台字符串经服务端白名单校验，客户端不能自由填写

---

## 4. 包结构 (cfgsync 定义)

### 4.1 tar.gz 内部布局

```
myapp-1.0.0.tar.gz
├── manifest.yaml          # 必需
├── README.md              # 必需：应用介绍（详情页主体）
├── INSTALL.md             # 可选：安装说明
├── USAGE.md               # 可选：使用说明
├── CHANGELOG.md           # 可选：版本说明（也作为 release_notes）
├── icon.png               # 可选：图标（推荐 256x256）
├── screenshots/           # 可选：截图（≤12 张）
│   ├── 01-main.png
│   └── 02-settings.png
└── bin/                   # 可选：多平台二进制
    ├── linux-amd64/
    ├── windows-amd64/
    └── darwin-arm64/
```

### 4.2 资源大小限制

| 资源 | 上限 |
|---|---|
| 单次上传 tar.gz | 200 MB (`MAX_PACKAGE_BYTES`) |
| manifest.yaml | 64 KB |
| 单 Markdown 文档 | 1 MB |
| icon.png | 256 KB |
| 单截图 | 2 MB |
| 截图总数 | 12 |
| README.md | 必需 |

超出限制 → `413 package_too_large` 或 `413 asset_too_large`。

### 4.3 manifest.yaml schema (v1)

```yaml
# cfgsync package manifest schema v1
schema_version: 1

# --- 必填 ---
version: "1.0.0"               # semver 字符串，cfgsync 会校验
display_name: "My App"          # 应用展示名

# --- 推荐 ---
description: "one-line summary"  # ≤200 字，详情页头部
summary: ""                      # ≤200 字，列表卡片用；缺省回落到 description
license: "MIT"
homepage: "https://..."

# --- 多维标签（用户决策 9：核心字段）---
tags:                            # 多标签，每个 ≤24 字符，≤8 个
  - system
  - ai
  - cli

keywords:                        # 搜索关键字，比 tags 更细粒度
  - automation
  - productivity

# --- 作者 ---
author:
  name: "Your Name"
  email: "you@example.com"
  url: "https://..."

# --- 系统要求 ---
requires_os:                     # 白名单：linux / windows / darwin / any
  - linux
  - windows
  - darwin

# --- 多平台二进制（纯文档包可省略）---
platforms:
  linux-amd64:
    path: bin/linux-amd64/myapp       # tar.gz 内相对路径
  windows-amd64:
    path: bin/windows-amd64/myapp.exe
  darwin-arm64:
    path: bin/darwin-arm64/myapp

# --- 可见性（覆盖 apps 表默认值）---
visibility: public                # public | unlisted | private

# --- 扩展字段（schema 演进用，原样保留，不参与查询）---
extra: {}
```

### 4.4 校验规则 (服务端解析时强制)

| 字段 | 规则 |
|---|---|
| `schema_version` | 必填，必须 == 1 |
| `version` | 必填，合法 semver (semver.org v2)，无 build metadata |
| `display_name` | 必填，1-128 字符 |
| `description` | 可选，≤200 字符 |
| `summary` | 可选，≤200 字符 |
| `tags` | 可选，每项 `^[a-z0-9][a-z0-9-]{0,23}$`，≤8 项 |
| `keywords` | 可选，每项 ≤32 字符，≤16 项 |
| `requires_os` | 可选，每项 ∈ {linux, windows, darwin, any} |
| `platforms` 的 key | 可选，必须 ∈ 白名单（见决策 11） |
| `platforms.*.path` | 必填，tar.gz 内存在的相对路径，禁止 `..` 和绝对路径 |
| `visibility` | 可选，∈ {public, unlisted, private} |
| `license` | 可选，SPDX 标识符或自由文本，≤64 字符 |
| `homepage` | 可选，合法 URL，≤512 字符 |
| `author.*` | 可选，各 ≤128 字符 |

非法 manifest → `400 invalid_manifest`，错误响应包含具体字段与原因。

---

## 5. 数据模型 (schema v3)

### 5.1 schema.sql 重写

v3 是**全新 schema**（删库重构），不是 v2 的增量。所有表 `CREATE TABLE IF NOT EXISTS`（兼容空目录首次启动），`schema_version` 设为 3。

```sql
-- cfgsync schema (version 3)
-- 应用市场模块新增：apps 扩展字段、app_tags、app_releases

CREATE TABLE IF NOT EXISTS schema_version (
    version    INTEGER PRIMARY KEY,
    applied_at INTEGER NOT NULL
);

-- ============================================================
-- 用户表（沿用 v2）
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
-- refresh_tokens（沿用 v2）
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
-- App 注册表（v3 扩展）
-- ============================================================
CREATE TABLE IF NOT EXISTS apps (
    app_id        TEXT PRIMARY KEY,
    display_name  TEXT NOT NULL,
    description   TEXT NOT NULL DEFAULT '',    -- 一句话简介（来自 manifest）
    summary       TEXT NOT NULL DEFAULT '',    -- 卡片摘要（来自 manifest）
    owner_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,  -- v3
    visibility    TEXT NOT NULL DEFAULT 'public'
                  CHECK (visibility IN ('public', 'unlisted', 'private')),  -- v3
    icon_path     TEXT NOT NULL DEFAULT '',    -- v3：仓库内 icon 相对路径
    latest_version TEXT NOT NULL DEFAULT '',   -- v3：当前最新版本字符串缓存
    created_at    INTEGER NOT NULL,
    created_by    TEXT NOT NULL REFERENCES users(id),
    updated_at    INTEGER NOT NULL             -- v3：每次新发布时 bump
);
CREATE INDEX IF NOT EXISTS idx_apps_visibility ON apps(visibility);
CREATE INDEX IF NOT EXISTS idx_apps_created_at ON apps(created_at);

-- ============================================================
-- App 标签（v3 新增，多对多）
-- ============================================================
CREATE TABLE IF NOT EXISTS app_tags (
    app_id TEXT NOT NULL REFERENCES apps(app_id) ON DELETE CASCADE,
    tag    TEXT NOT NULL,
    PRIMARY KEY (app_id, tag)
);
CREATE INDEX IF NOT EXISTS idx_tags_tag ON app_tags(tag);

-- ============================================================
-- App Token（沿用 v2，配置同步用）
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
-- 配置数据（沿用 v2，cfg-sync 协议保持不变）
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

-- ============================================================
-- App Release（v3 新增：版本发布）
-- ============================================================
CREATE TABLE IF NOT EXISTS app_releases (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    app_id          TEXT NOT NULL REFERENCES apps(app_id) ON DELETE CASCADE,
    version         TEXT NOT NULL,                  -- 原始 semver 字符串
    version_major   INTEGER NOT NULL,                -- semver 解析后
    version_minor   INTEGER NOT NULL,
    version_patch   INTEGER NOT NULL,
    version_pre     TEXT NOT NULL DEFAULT '',        -- 预发布 "rc.1" / 空
    manifest_yaml   TEXT NOT NULL,                  -- 原始 YAML
    manifest_json   TEXT NOT NULL,                  -- 解析后 JSON（查询用）
    package_size    INTEGER NOT NULL,
    package_sha256  TEXT NOT NULL,
    docs_json       TEXT NOT NULL DEFAULT '{}',     -- {readme, install, usage, changelog} 内容
    assets_json     TEXT NOT NULL DEFAULT '[]',     -- [{kind, path, size, sha256}]
    release_notes   TEXT NOT NULL DEFAULT '',        -- CHANGELOG.md 内容
    created_at      INTEGER NOT NULL,
    created_by      TEXT NOT NULL REFERENCES users(id),
    UNIQUE(app_id, version)
);
CREATE INDEX IF NOT EXISTS idx_releases_app_ver
    ON app_releases(app_id, version_major DESC, version_minor DESC, version_patch DESC, version_pre);
CREATE INDEX IF NOT EXISTS idx_releases_created
    ON app_releases(created_at DESC);
```

### 5.2 字段决策说明

- **`apps.latest_version` 字符串缓存**：避免列表页 join 求 max；新发布时事务内 UPDATE
- **`apps.updated_at`**：新发布版本时 bump，让"最近更新"排序不依赖 release 表
- **`apps.icon_path`**：应用列表卡片直接读，不 join release
- **`app_releases.version_major/minor/patch/pre`**：semver 拆解，方便 ORDER BY；预发布排正式版后
- **`app_releases.docs_json`**：把 README/INSTALL/USAGE/CHANGELOG 内容直接存 DB，避免每次请求读 FS
- **`app_releases.assets_json`**：截图、图标元信息（path/size/sha256），但 icon 数据仍在 FS
- **`apps.owner_user_id ON DELETE SET NULL`**：admin 删除时不级联删 app（应用是平台资产）

---

## 6. API 契约

### 6.1 路由总览

新增路由不影响现有 `/auth/*`、`/me/*`、`/admin/*`、`/apps/{app_id}/config`。

#### 开发者（admin only，复用 AdminMW）

| Method | Path | 说明 |
|---|---|---|
| POST | `/api/v1/dev/apps/{app_id}/releases` | 上传 .tar.gz (multipart `package`)，同步解析 |
| GET | `/api/v1/dev/apps/{app_id}/releases` | 发布历史（admin 视图，含 SHA、size） |
| DELETE | `/api/v1/dev/apps/{app_id}/releases/{version}` | 删除版本（级联删 FS 文件） |
| PATCH | `/api/v1/admin/apps/{app_id}`（已有，扩展） | 增加 `summary`、`visibility`、`icon_path` 字段 |

#### 公开（无 auth，未登录可见）

| Method | Path | 说明 |
|---|---|---|
| GET | `/api/v1/apps` | 列表，参数 `?tag=`、`?q=`、`?page=`、`?size=` |
| GET | `/api/v1/apps/{app_id}` | 详情（含 latest_release 摘要、tags、icon URL） |
| GET | `/api/v1/apps/{app_id}/releases` | 版本列表 |
| GET | `/api/v1/apps/{app_id}/releases/{version}` | 版本详情 |
| GET | `/api/v1/apps/{app_id}/releases/{version}/docs/{name}` | Markdown 原文 |
| GET | `/api/v1/apps/{app_id}/releases/{version}/docs/{name}/rendered` | goldmark 预渲染 HTML |
| GET | `/api/v1/apps/{app_id}/releases/{version}/assets/{name}` | 图标/截图，长 cache |
| GET | `/api/v1/apps/{app_id}/releases/{version}/download` | 流式下载，`?platform=` 选平台子文件 |
| GET | `/api/v1/tags` | 标签索引（tag → count） |

### 6.2 公开列表响应示例

```json
GET /api/v1/apps?tag=cli&page=1&size=20

{
  "apps": [
    {
      "app_id": "com.example.myapp",
      "display_name": "My App",
      "summary": "card summary text",
      "description": "one-liner",
      "icon_url": "/api/v1/apps/com.example.myapp/releases/1.0.0/assets/icon.png",
      "latest_version": "1.0.0",
      "tags": ["cli", "ai"],
      "updated_at": 1718700000,
      "created_at": 1718600000
    }
  ],
  "page": 1,
  "size": 20,
  "total": 42
}
```

### 6.3 公开详情响应示例

```json
GET /api/v1/apps/com.example.myapp

{
  "app_id": "com.example.myapp",
  "display_name": "My App",
  "description": "...",
  "summary": "...",
  "icon_url": "...",
  "homepage": "https://...",
  "license": "MIT",
  "author": {"name": "...", "email": "...", "url": "..."},
  "tags": ["cli", "ai"],
  "keywords": ["automation"],
  "requires_os": ["linux", "windows", "darwin"],
  "latest_release": {
    "version": "1.0.0",
    "created_at": 1718700000,
    "package_size": 12345678,
    "release_notes_url": "/api/v1/apps/com.example.myapp/releases/1.0.0/docs/CHANGELOG.md/rendered",
    "download_url": "/api/v1/apps/com.example.myapp/releases/1.0.0/download",
    "platforms": ["linux-amd64", "windows-amd64", "darwin-arm64"]
  },
  "releases_url": "/api/v1/apps/com.example.myapp/releases"
}
```

### 6.4 上传请求/响应

```
POST /api/v1/dev/apps/{app_id}/releases
Content-Type: multipart/form-data; boundary=...
Authorization: Bearer <admin JWT>

--boundary
Content-Disposition: form-data; name="package"; filename="myapp-1.0.0.tar.gz"
Content-Type: application/gzip

<binary>
--boundary--
```

成功响应：
```json
{
  "app_id": "com.example.myapp",
  "version": "1.0.0",
  "package_size": 12345678,
  "package_sha256": "abc...",
  "manifest": { /* 解析后的 YAML 对象 */ },
  "platforms": ["linux-amd64", "windows-amd64"],
  "docs": ["README", "INSTALL", "USAGE", "CHANGELOG"],
  "assets": ["icon.png", "screenshots/01-main.png"],
  "created_at": 1718700000
}
```

错误响应：
- `400 invalid_manifest`：manifest 字段校验失败，body 含 `details: [{field, reason}]`
- `404 not_found`：app_id 不存在
- `409 version_exists`：该 (app_id, version) 已发布（用 PUT 覆盖，POST 不覆盖）
- `413 package_too_large`：包超 200 MB
- `413 asset_too_large`：icon/screenshot/docs 超限

### 6.5 覆盖已存在版本

`POST` 拒绝覆盖。要覆盖用：

```
PUT /api/v1/dev/apps/{app_id}/releases/{version}    # 同样的 multipart 上传，覆盖
```

覆盖时事务内：删旧 release 行 → 写新 release 行 → 仓库原子替换目录（先写新 `extracted.tmp/`，rename）。

---

## 7. 文件仓库布局

```
$REPO_DIR/                                       # 默认 ./repo, env REPO_DIR
├── com.example.myapp/
│   ├── 1.0.0/
│   │   ├── package.tar.gz                        # 原始上传
│   │   ├── package.sha256                        # 单行 hex
│   │   └── extracted/                            # 解压结果（cfgsync 拥有）
│   │       ├── manifest.yaml
│   │       ├── README.md
│   │       ├── INSTALL.md
│   │       ├── USAGE.md
│   │       ├── CHANGELOG.md
│   │       ├── icon.png
│   │       ├── screenshots/
│   │       │   ├── 01-main.png
│   │       │   └── 02-settings.png
│   │       └── bin/
│   │           ├── linux-amd64/myapp
│   │           ├── windows-amd64/myapp.exe
│   │           └── darwin-arm64/myapp
│   └── 1.1.0/
│       └── ...
```

### 7.1 路径安全

所有从 `extracted/` 读文件的端点（docs、assets、download）必须：
1. 拒绝 `..` 路径段
2. 拒绝绝对路径
3. Clean 后必须仍在 `{REPO_DIR}/{app_id}/{version}/extracted/` 之下

### 7.2 删除版本的 FS 操作

```
DELETE /dev/apps/{app_id}/releases/{version}
1. BEGIN TX
2. DELETE FROM app_releases WHERE app_id=? AND version=?
3. SELECT 其它 release 是否存在 → 决定 apps.latest_version 是否要回退
4. UPDATE apps SET latest_version = ?
5. COMMIT TX
6. rm -rf {REPO_DIR}/{app_id}/{version}/     ← 失败仅记日志，业务不受影响
```

第 6 步失败留孤儿文件，可由后台清理 job 回收（v1 不实现，文档化即可）。

---

## 8. 配置项 (env vars)

新增：

| Var | Default | Notes |
|---|---|---|
| `REPO_DIR` | `./repo` | 包仓库根目录，相对路径以工作目录为基 |
| `MAX_PACKAGE_BYTES` | `209715200` (200 MB) | 单次上传 tar.gz 上限 |
| `MAX_MANIFEST_BYTES` | `65536` (64 KB) | manifest.yaml 上限 |
| `MAX_DOC_BYTES` | `1048576` (1 MB) | 单 Markdown 文档上限 |
| `MAX_ICON_BYTES` | `262144` (256 KB) | icon.png 上限 |
| `MAX_SCREENSHOT_BYTES` | `2097152` (2 MB) | 单截图上限 |
| `MAX_SCREENSHOTS` | `12` | 截图总数上限 |
| `PACKAGE_PLATFORM_WHITELIST` | `linux-amd64,linux-arm64,windows-amd64,windows-arm64,darwin-amd64,darwin-arm64` | 逗号分隔，可覆盖 |

---

## 9. 包解析流程

```
1. 接收 multipart package → 写到 {REPO_DIR}/{app_id}/{version}.tmp/package.tar.gz
   (限流 MaxBytesReader，超限 413)
2. 计算 sha256，校验大小
3. 流式扫描 tar.gz：
   a. 找到 manifest.yaml → 读出（≤64KB）→ 解析 YAML
   b. 找到 README.md → 必需
   c. 找到 INSTALL.md / USAGE.md / CHANGELOG.md → 可选
   d. 找到 icon.png → 校验大小
   e. 收集 screenshots/*.png → 校验数量和大小
   f. 校验 manifest.platforms.*.path 存在
   g. 校验 manifest.schema_version == 1
4. 字段校验（见 §4.4）
5. BEGIN TX
   a. INSERT app_releases
   b. DELETE FROM app_tags WHERE app_id=? → INSERT 新 tags
   c. UPDATE apps SET summary=?, description=?, icon_path=?, latest_version=?, updated_at=?
6. COMMIT TX
7. 解压整个 tar.gz 到 {REPO_DIR}/{app_id}/{version}/extracted/
8. mv {version}.tmp/ → {version}/  (原子 rename)
9. 返回成功响应
```

任意步骤失败：清理 `.tmp/` 目录，事务回滚，返回错误。

### 9.1 semver 解析

用 `golang.org/x/mod/semver` 包（已在 Go 标准库周边）。预发布版本排正式版之后：

- `1.0.0` > `1.0.0-rc.1`
- `1.0.0-alpha` < `1.0.0-beta`
- 拒绝 `1.0.0+build123`（build metadata 不存）

---

## 10. Markdown 渲染

用 `github.com/yuin/goldmark` + `github.com/yuin/goldmark-highlighting`：

```go
md := goldmark.New(
    goldmark.WithExtensions(&html.Link{Target: "_blank"}),
    highlighting.NewHighlighting(...),
    // 禁用 raw HTML
)
```

- 输出安全 HTML（goldmark 默认转义 raw HTML）
- 代码块语法高亮（chroma）
- 链接 `target="_blank" rel="noopener"`
- 不渲染远程图片（仅本地截图）

`GET .../docs/{name}/rendered` 直接返回 `text/html; charset=utf-8`。

---

## 11. WebUI 路由

```
公开（未登录可见，导航栏只显示"市场"+"登录"）：
  /                            首页（推荐 + 最新 + 热门标签）
  /apps                        应用列表（分类侧栏 + 搜索 + 卡片网格 + 分页）
  /apps/{app_id}               应用详情（介绍 + 截图轮播 + 版本 + 下载按钮）
  /apps/{app_id}/v/{version}   版本详情
  /tags/{tag}                  按标签筛选（重定向到 /apps?tag=）
  /login                       （已有）

登录后（导航栏增"我的应用"+"管理"+"退出"）：
  /me/...                      （已有，cfg-sync 配置管理）
  /dev/apps                    我开发的 app
  /dev/apps/{app_id}/new       上传新版本（拖拽 .tar.gz + 进度）
  /dev/apps/{app_id}/releases  发布历史
  /admin/...                   （已有）
```

前端用 Preact + htm，沿用现有 SPA 架构。Markdown 渲染走服务端，前端只显示 HTML。

---

## 12. 实施分阶段

| Step | 内容 | 工期 | 测试覆盖 |
|---|---|---|---|
| 1 | schema v3 重写 + model 层 + db.Migrate 真迁移逻辑（带版本号分支） | 0.5 天 | 迁移测试 |
| 2 | `internal/repo`（FS 仓库层） + manifest 解析器 + semver 工具 | 1 天 | 单元测试（合法包、各种非法包） |
| 3 | 开发者上传 API（POST /dev/.../releases）+ 包解析 + 落盘 | 0.5 天 | e2e（构造真包上传） |
| 4 | 公开 API（列表/详情/版本/文档/资源/下载/标签） | 0.5 天 | 完整覆盖 |
| 5 | Markdown 渲染端点（goldmark） | 0.5 天 | 渲染安全测试（XSS、raw HTML） |
| 6 | WebUI 公开页面 + dev 上传页 | 1.5 天 | Playwright e2e |
| **合计** | | **~4.5 天** | |

---

## 13. 风险与开放问题

### 13.1 已识别风险

| 风险 | 缓解 |
|---|---|
| 大包解析吃内存 | `archive/tar` 流式扫描；不一次性解压到内存 |
| 同版本并发上传 | DB UNIQUE 约束 + 事务 |
| 删除版本留孤儿文件 | 文档化；v1 不做后台清理 |
| 平台字符串自由填写 | 白名单常量；服务端校验 |
| 路径穿越攻击 | 严格 `filepath.Clean` + 前缀校验 |
| Markdown XSS | goldmark 默认转义 raw HTML；不渲染远程图片 |
| 包重名（不同 app） | 路径按 `{app_id}/{version}/` 隔离 |
| 大文件下载阻塞 | 流式 io.Copy；nginx 前置可分担 |

### 13.2 开放问题（v0.5.0 不解决）

- 服务端签名验证（cosign）
- FTS5 全文搜索（v1 用 LIKE，规模小够用）
- 下载次数统计
- RSS / 订阅通知
- 多 owner（团队协作）
- 包删除的软删除与历史保留

---

## 14. 与现有模块的兼容性

- `internal/auth`：完全复用，无改动
- `internal/handler/auth.go`、`me.go`、`sync.go`、`apps.go`：完全复用
- `internal/handler/admin.go`：`AdminPatchApp` 扩展接受 `summary`、`visibility`、`icon_path`（v3 字段）
- `internal/server/server.go`：新增 `dev` 路由组（admin chain）+ `public` 路由组（无 MW）
- `internal/webui`：新增公开页面组件，保留现有 `/me`、`/admin`
- `internal/db/schema.sql`：重写为 v3
- `internal/db/db.go`：`Migrate` 改为按 `schema_version` 跑分支（即便 v3 是 fresh schema，机制要立起来供未来 v4 用）

---

## 15. 引用

- [cfg-sync 多 App 配置同步设计](./2026-06-16-multi-app-config-sync-design.md) — 现有 cfg-sync 模块设计
- [SemVer 2.0.0](https://semver.org/)
- [goldmark](https://github.com/yuin/goldmark)
- [golang.org/x/mod/semver](https://pkg.go.dev/golang.org/x/mod/semver)
