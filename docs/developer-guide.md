# cfgsync 开发者指南

本文档面向**想把 cfgsync 接入自己软件**的开发者。读完你应该能用几十行代码让你的软件具备跨设备配置同步能力。

如果你是普通用户（想把 token 粘到软件里），请看 [`user-guide.md`](user-guide.md)。

---

## 目录

- [1. 平台定位](#1-平台定位)
- [2. 集成最小路径](#2-集成最小路径)
- [3. 核心概念](#3-核心概念)
- [4. 接口规范](#4-接口规范)
- [5. 乐观锁协议](#5-乐观锁协议)
- [6. 错误码完整对照](#6-错误码完整对照)
- [7. 配额与限制](#7-配额与限制)
- [8. 推荐客户端实现模式](#8-推荐客户端实现模式)
- [9. 完整 curl 示例](#9-完整-curl-示例)
- [10. 参考实现（伪代码）](#10-参考实现伪代码)
- [11. 边界情况与陷阱](#11-边界情况与陷阱)

---

## 1. 平台定位

cfgsync 是一个**通用的、按 `(用户, 应用)` 隔离的文本配置同步后端**。

- 你（软件开发者）注册一个 `app_id`。
- 你的用户从 WebUI 拿到一个 `app_token`。
- 你的软件用这个 token 调两个接口：`GET` 拉取，`PUT` 推送。

平台**不解析 payload 内容**——你存 JSON、YAML、TOML、INI、纯文本都行。平台只保证：
1. 按 `(user_id, app_id)` 隔离。
2. 提供"版本号"用于多设备并发写冲突检测。
3. 每次写入保留一份历史（最近 50 次，可调）。

---

## 2. 集成最小路径

三步：

1. **注册 `app_id`**：联系平台管理员，让他通过 WebUI 创建一个反域格式 ID（如 `com.yourcompany.yourapp`）。
2. **引导用户申请 token**：在你的软件里加一个"cfgsync 配置"页面，让用户**从浏览器登录 cfgsync → 我的应用 → 你的应用 → 生成 token → 粘回软件**。软件只接收 token 字符串，不接触用户密码。
3. **实现 GET / PUT**：见 [§4](#4-接口规范)。

---

## 3. 核心概念

### 3.1 `app_id` —— 数据格式契约的指纹

- 反域格式：`com.example.myapp`、`io.github.user.tool`。
- **同 `app_id` 的所有版本共用同一份 payload schema**。如果你的软件 2.0 改了配置结构（破坏性变更），建议申请新的 `app_id`（如 `com.example.myapp.v2`），让两代客户端各存各的、互不污染。
- 跨用户共用同一 `app_id` 时，所有用户的 payload 应是兼容格式（但用户之间数据隔离）。

### 3.2 两层 token 模型

| 层级 | 持有者 | 用途 | 形态 |
|---|---|---|---|
| **用户 JWT** | 人（通过 WebUI） | 登录、管理 `app_token`、admin 操作 | HS256 JWT，1 小时 TTL |
| **app_token** | 你的软件客户端 | 调 `GET / PUT /apps/{app_id}/config` | 不透明串 `1rc_<32hex>`，无 TTL，可吊销 |

**核心约束**：你的软件**永远不接触用户密码**。用户把 `app_token` 粘到你的软件里，你的软件拿这个 token 调 API。

### 3.3 `app_token` 的特点

- 32 hex 字符（前缀 `1rc_`，共 36 字符）。
- 每个 `(user_id, app_id)` 唯一——用户重复申请会让旧 token 失效。
- **平台只存 SHA-256 hash**，明文仅在创建时返回一次。
- 跨 `app_id` 不能复用——平台在 `AppTokenMW` 中校验 `token.app_id == URL.{app_id}`，不匹配返回 `403 forbidden`。
- `last_used_at` 由平台在每次 `GET` 时自动更新（用于用户在 WebUI 中查看）。
- 撤销：用户可在 WebUI 点"撤销"，或重新生成（旧的失效）。

---

## 4. 接口规范

你的软件只需要实现这两个端点的客户端。

所有路径以 `/api/v1` 前缀。鉴权统一用 HTTP header：

```
Authorization: Bearer <app_token>
```

### 4.1 `GET /api/v1/apps/{app_id}/config`

拉取当前配置快照。

**响应 200**：

```json
{
  "version": 7,
  "payload": "{\"theme\":\"dark\", ...}",
  "updated_at": 1718000000,
  "updated_by": "MBA"
}
```

| 字段 | 类型 | 说明 |
|---|---|---|
| `version` | int | 配置版本号，新用户/未写入过的 `(user, app_id)` 返回 `0` |
| `payload` | string | 配置内容（UTF-8 文本），新用户返回 `""` |
| `updated_at` | unix 秒 | 上次 PUT 的时间，新用户为 `0` |
| `updated_by` | string | 上次 PUT 时客户端传入的标识（如设备名） |

**新用户的特殊响应**：`{"version":0,"payload":"","updated_at":0,"updated_by":""}`（HTTP 200，**不是** 404）。

**响应 401 `invalid_token`**：token 已被撤销或不正确。
**响应 403 `forbidden`**：token 不属于这个 `app_id`（同用户跨 app 复用）。

### 4.2 `PUT /api/v1/apps/{app_id}/config`

推送配置。带乐观锁。

**请求体**：

```json
{
  "version": 7,
  "payload": "{\"theme\":\"light\", ...}",
  "updated_by": "MacBook Air"
}
```

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `version` | int | 是 | 客户端最近一次 GET 拿到的 `version`。首次写入传 `0`。 |
| `payload` | string | 是 | 新配置内容。字节数 ≤ `MAX_PAYLOAD_BYTES`（默认 4 MB）。 |
| `updated_by` | string | 是 | 客户端标识（设备名/软件名）。便于多人协作时审计。**不可空**，否则 `400 missing_updated_by`。 |

**响应 200**：

```json
{
  "version": 8,
  "payload": "{\"theme\":\"light\", ...}",
  "updated_at": 1718000123,
  "updated_by": "MacBook Air"
}
```

写入成功，返回**写入后**的快照（`version` 已 +1）。

**响应 409 `version_conflict`**：

```json
{
  "error": "version_conflict",
  "current_version": 8,
  "current_payload": "{\"theme\":\"dark\", ...}",
  "current_updated_at": 1718000050,
  "current_updated_by": "iPhone"
}
```

云端版本不匹配——别人/别设备在你 GET 之后 PUT 过。响应包含云端当前完整状态，**客户端必须自治合并**（参见 [§5](#5-乐观锁协议)）。

**响应 413 `payload_too_large`**：单次 PUT payload 字节数 > `MAX_PAYLOAD_BYTES`（4 MB）。响应含 `max_bytes`。

**响应 413 `storage_quota_exceeded`**：用户当前总存储（所有 `app_id` 加总）+ 新 payload 字节数 - 旧 payload 字节数 > `USER_STORAGE_LIMIT_MB`（100 MB）。响应含 `used_bytes`、`limit_bytes`。**`?force=true` 不能绕过配额**。

### 4.3 `PUT /api/v1/apps/{app_id}/config?force=true`

强制覆盖。跳过 version 检查，但仍：
- 字节数限制（4 MB）
- 配额限制（100 MB）
- version 仍然 +1
- 历史仍然追加一份

**慎用**。一般客户端不应自动使用 force——会让其他设备的修改丢失。仅用于：
- 用户明确选择"丢弃本地，强制覆盖远端"
- 首次写入（`version=0` 时，force 与否行为相同）

---

## 5. 乐观锁协议

cfgsync 用**客户端侧乐观锁**，不是数据库锁。流程：

```
客户端                                   平台
   │                                      │
   │ 1. GET /apps/{app_id}/config          │
   ├─────────────────────────────────────►│
   │   {version: 7, payload: "..."}       │
   │◄─────────────────────────────────────┤
   │                                      │
   │   (用户编辑配置)                       │
   │                                      │
   │ 2. PUT /apps/{app_id}/config          │
   │    body: {version: 7, payload: "..."} │
   ├─────────────────────────────────────►│
   │                                      │
   │    [平台比对]                          │
   │    DB.version == 7?                   │
   │                                      │
   │    是 → 写入，DB.version = 8          │
   │       返回 200 {version: 8}           │
   │    否 → 返回 409 + current_*          │
   │◄─────────────────────────────────────┤
```

### 处理 409 的三种策略

收到 `409 version_conflict` 时，客户端有三种选择：

**A. Last-Write-Wins（LWW）**
直接用 `?force=true` 覆盖远端。最简单，但会丢失别设备的修改。**仅在你确定 payload 没有合并价值时用**（如纯文本笔记）。

**B. 三路合并**
拿 `current_payload`、本地修改前 payload、本地修改后 payload，做应用语义级合并。JSON 可用 JSON Merge Patch（RFC 7396）或 JSON Patch（RFC 6902）。合并后再 PUT。

**C. 让用户选**
弹窗："云端已被 X 在 Y 修改过，如何处理？[保留云端 / 保留本地 / 取消]"。

### 实现要点

1. **客户端必须缓存"修改前的 payload"**——光记 `version` 不够，409 时无法做三向合并。
2. **重试前必须 GET 一次最新状态**（或用 409 响应里的 `current_*`）。
3. **不要在循环里无限重试**——失败 N 次后让用户介入。
4. **不要用 `?force=true` 作为兜底**——会丢失别人工作。仅用户明确选择时使用。

---

## 6. 错误码完整对照

所有错误响应统一格式：`{"error": "<code>"}`。部分含附加字段。

| HTTP | code | 触发场景 | 附加字段 |
|---|---|---|---|
| 400 | `invalid_json` | body 解析失败 / 格式错 | |
| 400 | `missing_updated_by` | PUT 未提供 `updated_by` | |
| 401 | `unauthorized` | 缺 Authorization header | |
| 401 | `invalid_token` | `app_token` 不存在 / 已撤销 | |
| 403 | `forbidden` | token 与 URL 中的 `{app_id}` 不匹配 | |
| 413 | `payload_too_large` | 单次 PUT > 4 MB | `max_bytes` |
| 413 | `storage_quota_exceeded` | 用户总存储超 100 MB | `used_bytes`, `limit_bytes` |
| 409 | `version_conflict` | PUT version 不匹配 | `current_version`, `current_payload`, `current_updated_at`, `current_updated_by` |
| 500 | `internal` | 服务器内部错误（panic / DB 错误） | |

> **客户端处理 401 的特别说明**：`invalid_token` 通常意味着用户在 WebUI 撤销了 token，或重新生成了新 token。你的软件应该提示用户"token 已失效，请重新申请"，不要静默重试。

> **客户端处理 5xx 的策略**：保留本地修改，提示用户"同步失败，已保留本地修改，将在下次启动时重试"。**不要吞掉本地数据**。

---

## 7. 配额与限制

| 项 | 默认值 | 触发场景 | 备注 |
|---|---|---|---|
| 单次 PUT 字节 | 4 MB | PUT payload 字节数超限 | `MAX_PAYLOAD_BYTES` env，413 `payload_too_large` |
| 用户总存储 | 100 MB | 用户所有 `app_id` 当前 payload 总字节超限 | `USER_STORAGE_LIMIT_MB` env，413 `storage_quota_exceeded` |
| 用户 app_token 数 | 100 | 用户在 ≥100 个不同 `app_id` 上申请过 token | `USER_APP_TOKEN_LIMIT` env，413 `app_token_limit_reached` |
| 历史保留 | 50 条 / `(user, app_id)` | 每次 PUT 后裁剪 | `HISTORY_PER_APP` env，0 表示禁用裁剪 |

**重要**：
- 历史不计入配额。
- 替换 token 不消耗新的 token 数量（`UNIQUE(user_id, app_id)` 约束，旧 row 先 DELETE 后 INSERT）。
- `force=true` **不能绕过**任何配额限制。

---

## 8. 推荐客户端实现模式

### 8.1 启动时：拉取远端

```pseudo
function onAppStart():
    remote = GET /apps/{app_id}/config
    if remote.version > local.version:
        # 远端有更新，用远端覆盖本地
        local.payload = remote.payload
        local.version = remote.version
        applyToUI(local.payload)
    else if remote.version == 0 and local.payload == "":
        # 全新用户，无操作
        pass
    else if remote.version < local.version:
        # 本地比远端新？说明上次 PUT 失败但本地状态已 advance
        # 触发一次 PUT
        pushLocal()
```

### 8.2 用户保存时：推送本地

```pseudo
function onUserSave():
    newPayload = serializeFromUI()
    pushOnce(newPayload, local.version)

function pushOnce(newPayload, baseVersion):
    resp = PUT /apps/{app_id}/config with {version: baseVersion, payload: newPayload, updated_by: deviceName}
    if resp.status == 200:
        local.version = resp.version
        local.payload = newPayload
    elif resp.status == 409:
        # 别人改过，合并
        merged = mergeThreeWay(
            base = local.payload,         # 我修改前
            mine = newPayload,            # 我修改后
            theirs = resp.current_payload # 远端当前
        )
        if merged is None:
            # 无法自动合并，让用户选
            askUser(resp.current_*)
        else:
            # 用合并结果重试，base 用最新的 current_version
            pushOnce(merged, resp.current_version)
    elif resp.status == 413:
        showQuotaError(resp)
    else:
        # 网络错/500，保留本地，下次重试
        scheduleRetry()
```

### 8.3 后台轮询（可选）

如果你的软件是长时间运行的（如 IDE），可以每 N 分钟 GET 一次，发现 `version` 变化就拉取并应用。**注意**：GET 会让平台更新 `last_used_at`，不要轮询太频繁（建议 ≥ 5 分钟一次）。

### 8.4 退出时：最后一次推送

```pseudo
function onAppExit():
    if local.dirty:
        pushOnce(local.payload, local.version)
        # 即使失败也直接退出，下次启动会重试
```

### 8.5 多设备用户场景

cfgsync 不区分设备。所有用同一个 `(用户, 应用)` token 的设备，看到的是同一份配置。`updated_by` 字段是你区分设备的唯一手段——建议每次 PUT 都传一个稳定的设备标识（hostname / 设备名）。

---

## 9. 完整 curl 示例

假设：
- 服务地址：`https://cfgsync.example.com`
- `app_id`：`com.example.myapp`
- 用户已通过 WebUI 申请到 token：`1rc_abcdef0123456789abcdef0123456789`（示例）

```bash
BASE=https://cfgsync.example.com
APP_ID=com.example.myapp
APP_TOK=1rc_abcdef0123456789abcdef0123456789

# 1. 拉取当前配置
curl -sfX GET "$BASE/api/v1/apps/$APP_ID/config" \
  -H "Authorization: Bearer $APP_TOK"
# 新用户响应：{"version":0,"payload":"","updated_at":0,"updated_by":""}

# 2. 首次写入（version=0）
curl -sfX PUT "$BASE/api/v1/apps/$APP_ID/config" \
  -H "Authorization: Bearer $APP_TOK" \
  -H "Content-Type: application/json" \
  -d '{
    "version": 0,
    "payload": "{\"theme\":\"dark\",\"fontSize\":14}",
    "updated_by": "MacBook Air"
  }'
# 响应：{"version":1,"payload":"...","updated_at":1718000000,"updated_by":"MacBook Air"}

# 3. 第二次写入（version=1）
curl -sfX PUT "$BASE/api/v1/apps/$APP_ID/config" \
  -H "Authorization: Bearer $APP_TOK" \
  -H "Content-Type: application/json" \
  -d '{
    "version": 1,
    "payload": "{\"theme\":\"light\",\"fontSize\":14}",
    "updated_by": "MacBook Air"
  }'
# 响应：{"version":2,...}

# 4. 模拟并发冲突：用旧的 version=1 再 PUT
curl -i -X PUT "$BASE/api/v1/apps/$APP_ID/config" \
  -H "Authorization: Bearer $APP_TOK" \
  -H "Content-Type: application/json" \
  -d '{
    "version": 1,
    "payload": "{\"theme\":\"blue\"}",
    "updated_by": "iPhone"
  }'
# HTTP/1.1 409 Conflict
# {
#   "error": "version_conflict",
#   "current_version": 2,
#   "current_payload": "{\"theme\":\"light\",\"fontSize\":14}",
#   "current_updated_at": 1718000050,
#   "current_updated_by": "MacBook Air"
# }

# 5. 强制覆盖（慎用）
curl -sfX PUT "$BASE/api/v1/apps/$APP_ID/config?force=true" \
  -H "Authorization: Bearer $APP_TOK" \
  -H "Content-Type: application/json" \
  -d '{
    "version": 0,
    "payload": "{\"theme\":\"blue\"}",
    "updated_by": "iPhone"
  }'
# 响应：{"version":3,...}  (force 也 +1)
```

---

## 10. 参考实现（伪代码）

最小可用 Python 客户端（`requests` 库）：

```python
import requests

class CfgSyncClient:
    def __init__(self, base_url, app_id, app_token, device_name):
        self.base = base_url.rstrip('/')
        self.app_id = app_id
        self.device = device_name
        self.s = requests.Session()
        self.s.headers['Authorization'] = f'Bearer {app_token}'

    def get(self):
        """返回 (version, payload)；新用户返回 (0, '')。"""
        r = self.s.get(f'{self.base}/api/v1/apps/{self.app_id}/config')
        if r.status_code == 401:
            raise TokenRevoked()
        r.raise_for_status()
        d = r.json()
        return d['version'], d['payload']

    def put(self, payload, base_version, force=False):
        """推送 payload。返回新 version。失败抛 Conflict / QuotaExceeded。"""
        url = f'{self.base}/api/v1/apps/{self.app_id}/config'
        if force:
            url += '?force=true'
        r = self.s.put(url, json={
            'version': base_version,
            'payload': payload,
            'updated_by': self.device,
        })
        if r.status_code == 200:
            return r.json()['version']
        if r.status_code == 409:
            cur = r.json()
            raise Conflict(current_version=cur['current_version'],
                           current_payload=cur['current_payload'])
        if r.status_code == 413:
            body = r.json()
            raise QuotaExceeded(body)
        raise SyncError(f'{r.status_code}: {r.text}')

# 使用
c = CfgSyncClient('https://cfgsync.example.com',
                  'com.example.myapp',
                  '1rc_xxxxx',
                  'MacBook Air')

ver, payload = c.get()
new_payload = update_config(payload)
try:
    c.put(new_payload, ver)
except Conflict as e:
    # 简单 LWW：让用户选
    if user_confirms_overwrite(e.current_payload):
        c.put(new_payload, e.current_version, force=True)
```

---

## 11. 边界情况与陷阱

### 11.1 token 失效

- 用户在 WebUI 撤销 → 你的下次请求收到 `401 invalid_token`。
- 用户重新生成新 token → 旧 token 立即失效，同样 `401`。
- **处理**：弹窗"cfgsync token 已失效，请重新申请"，让用户粘贴新 token。**不要静默无限重试**。

### 11.2 `version` 是 `int64`，但 JSON 解析注意

平台返回的 `version` 是 `int64`。JavaScript 默认 `Number` 安全整数上限是 2^53，对绝大多数场景足够。但如果你写 Go 客户端，请用 `int64`/`json.Number`，不要用 `int`（在 32 位平台上溢出）。

### 11.3 payload 必须 UTF-8 文本

`payload` 字段是 string。平台用 SQLite TEXT 存储，按 UTF-8 字节数计算配额。**不要塞二进制**——必须先 base64 编码。但 base64 后字节数 +33%，4 MB 上限实际只能存约 3 MB 原始二进制。

### 11.4 PUT 不是幂等的

同一个 PUT（同 version、同 payload）连续发两次：
- 第一次：200，version +1
- 第二次：409（version 已经不匹配了）

如果你想做"幂等保存"（同一份修改只生效一次），需要在客户端自己用 payload hash 去重。

### 11.5 网络超时

平台 server 配置：
- `ReadTimeout: 15s`
- `WriteTimeout: 15s`
- `IdleTimeout: 60s`

4 MB PUT 在慢网络上可能逼近 15s。建议客户端 timeout 设 30s 以上。

### 11.6 `updated_by` 不能空

`PUT` 时 `updated_by` 字段空字符串会返回 `400 missing_updated_by`。建议传设备名、软件版本或用户名。

### 11.7 `last_used_at` 不是同步信号

平台在每次 `GET` 时更新 `app_tokens.last_used_at`。这只是给用户在 WebUI 看的元数据，**不会触发任何同步动作**。你的客户端不需要轮询它。

### 11.8 HTTP/HTTPS

平台本身支持 HTTP（开发）和 HTTPS（生产，由 Caddy 终结 TLS）。**生产环境务必走 HTTPS**——`app_token` 是 bearer 凭证，HTTP 传输会被中间人窃取。

### 11.9 不要存敏感凭据

cfgsync 的设计假设是"信任用户、信任管理员"。管理员有 SQL 权限能直接读你的 payload。如果你的配置包含第三方 API key、密码等敏感信息，**请在客户端先加密**再 PUT。平台不提供加密功能。

### 11.10 schema 变更

如果你改了 payload schema（破坏性变更）：
- **推荐**：申请新的 `app_id`（如 `com.example.myapp.v2`）。老用户跨代不冲突，新用户用新 ID。
- **不推荐**：在原 `app_id` 上做"自动迁移"——会破坏正在用旧版本客户端的用户。

---

## 附录：进一步阅读

- 用户使用文档：[`user-guide.md`](user-guide.md)
- 项目 README：[`../README.md`](../README.md)
- 设计规范（深度）：[`superpowers/specs/2026-06-16-multi-app-config-sync-design.md`](superpowers/specs/2026-06-16-multi-app-config-sync-design.md)
- WebUI 设计：[`superpowers/specs/2026-06-16-cfgsync-webui-design.md`](superpowers/specs/2026-06-16-cfgsync-webui-design.md)
- CHANGELOG：[`../CHANGELOG.md`](../CHANGELOG.md)

如有问题或想要 SDK（Go / Python / JS），请提 issue。
