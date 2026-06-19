-- cfgsync schema (version 3)
-- 通用软件配置同步 + 应用市场模块

-- ============================================================
-- Schema 版本追踪
-- Migrate 按 schema_version 跑显式分支，不再只靠 CREATE IF NOT EXISTS。
-- 当前版本 = 3 (fresh rewrite，无 v2 → v3 迁移路径)。
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
-- App 注册表 (v3 扩展：owner / visibility / icon / latest_version)
-- ============================================================
CREATE TABLE IF NOT EXISTS apps (
    app_id         TEXT PRIMARY KEY,
    display_name   TEXT NOT NULL,
    description    TEXT NOT NULL DEFAULT '',
    summary        TEXT NOT NULL DEFAULT '',
    owner_user_id  TEXT REFERENCES users(id) ON DELETE SET NULL,
    visibility     TEXT NOT NULL DEFAULT 'public'
                   CHECK (visibility IN ('public', 'unlisted', 'private')),
    icon_path      TEXT NOT NULL DEFAULT '',
    latest_version TEXT NOT NULL DEFAULT '',
    created_at     INTEGER NOT NULL,
    created_by     TEXT NOT NULL REFERENCES users(id),
    updated_at     INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_apps_visibility ON apps(visibility);
CREATE INDEX IF NOT EXISTS idx_apps_created_at ON apps(created_at);
CREATE INDEX IF NOT EXISTS idx_apps_updated_at ON apps(updated_at);

-- ============================================================
-- App 标签 (v3 新增，多对多)
-- ============================================================
CREATE TABLE IF NOT EXISTS app_tags (
    app_id TEXT NOT NULL REFERENCES apps(app_id) ON DELETE CASCADE,
    tag    TEXT NOT NULL,
    PRIMARY KEY (app_id, tag)
);
CREATE INDEX IF NOT EXISTS idx_tags_tag ON app_tags(tag);

-- ============================================================
-- App Token (cfg-sync 协议用，沿用 v2)
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
-- 配置数据 (cfg-sync 协议用，沿用 v2)
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
-- 配置历史 (cfg-sync 协议用，沿用 v2)
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

-- ============================================================
-- App Release (v3 新增：应用市场版本发布)
-- ============================================================
CREATE TABLE IF NOT EXISTS app_releases (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    app_id          TEXT NOT NULL REFERENCES apps(app_id) ON DELETE CASCADE,
    version         TEXT NOT NULL,
    version_major   INTEGER NOT NULL,
    version_minor   INTEGER NOT NULL,
    version_patch   INTEGER NOT NULL,
    version_pre     TEXT NOT NULL DEFAULT '',
    manifest_yaml   TEXT NOT NULL,
    manifest_json   TEXT NOT NULL,
    package_size    INTEGER NOT NULL,
    package_sha256  TEXT NOT NULL,
    docs_json       TEXT NOT NULL DEFAULT '{}',
    assets_json     TEXT NOT NULL DEFAULT '[]',
    release_notes   TEXT NOT NULL DEFAULT '',
    created_at      INTEGER NOT NULL,
    -- created_by is NOT NULL because every release is published by an
    -- authenticated admin (auth.UserID from the JWT). FK has no ON DELETE
    -- clause: cfgsync has no admin-deletion API today. If one is added,
    -- the right action is RESTRICT (force the operator to reassign or
    -- delete the releases first) — never CASCADE, which would silently
    -- erase published artifacts.
    created_by      TEXT NOT NULL REFERENCES users(id),
    UNIQUE(app_id, version)
);
CREATE INDEX IF NOT EXISTS idx_releases_app_ver
    ON app_releases(app_id, version_major DESC, version_minor DESC, version_patch DESC, version_pre);
CREATE INDEX IF NOT EXISTS idx_releases_created
    ON app_releases(created_at DESC);
