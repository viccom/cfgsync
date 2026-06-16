// cfgsync WebUI — single-file Preact SPA. No build step.

import { h, render } from 'https://esm.sh/preact@10.22.0';
import { useState, useEffect } from 'https://esm.sh/preact@10.22.0/hooks';
import { signal, computed } from 'https://esm.sh/@preact/signals@1.2.3';
import htm from 'https://esm.sh/htm@3.1.1';

const html = htm.bind(h);

// ============================================================
// constants
// ============================================================
const LS_JWT = 'cfgsync_jwt';
const LS_REFRESH = 'cfgsync_refresh';

const ERR_MSGS = {
  invalid_json: '请求格式错误',
  invalid_email_or_password: '邮箱或密码格式不正确（密码至少 8 位）',
  invalid_credentials: '邮箱或密码不正确',
  invalid_token: '登录已失效，请重新登录',
  invalid_refresh_token: '会话已过期，请重新登录',
  unauthorized: '请先登录',
  forbidden: '权限不足',
  not_found: '资源不存在',
  email_already_registered: '该邮箱已注册',
  invalid_app_id: 'app_id 格式错误（应为反域格式，如 com.example.app）',
  app_id_exists: '该 app_id 已被注册',
  app_token_limit_reached: '已达 app_token 上限',
  payload_too_large: '配置超过 4 MB',
  storage_quota_exceeded: '存储配额已满',
  version_conflict: '配置已被其他设备更新',
  internal: '服务器内部错误，请稍后重试',
};

// ============================================================
// signals (global state)
// ============================================================
const jwtSignal = signal(localStorage.getItem(LS_JWT) || null);
const refreshSignal = signal(localStorage.getItem(LS_REFRESH) || null);
const userSignal = computed(() => (jwtSignal.value ? decodeJwt(jwtSignal.value) : null));
const toastSignal = signal(null);
const routeSignal = signal(parseLocation());

window.addEventListener('popstate', () => {
  routeSignal.value = parseLocation();
});

function parseLocation() {
  const path = location.pathname || '/';
  const segments = path.split('/').filter(Boolean);
  return { path, segments };
}

function navigate(to) {
  if (location.pathname !== to) {
    history.pushState({}, '', to);
    routeSignal.value = parseLocation();
  }
}

// ============================================================
// api client
// ============================================================
async function call(method, path, body, opts = {}) {
  const headers = { 'Content-Type': 'application/json' };
  const t = jwtSignal.value;
  if (t) headers['Authorization'] = `Bearer ${t}`;
  const doFetch = async () => {
    const res = await fetch(path, {
      method,
      headers,
      body: body !== undefined ? JSON.stringify(body) : undefined,
    });
    const text = await res.text();
    let data = null;
    try { data = text ? JSON.parse(text) : null; } catch {}
    if (!res.ok) {
      const code = data?.error || `http_${res.status}`;
      const err = new Error(ERR_MSGS[code] || code);
      err.status = res.status;
      err.body = data;
      throw err;
    }
    return data;
  };

  try {
    return await doFetch();
  } catch (err) {
    if (err.status === 401 && !opts._retried && isIdempotent(method)) {
      const refreshed = await tryRefresh();
      if (refreshed) {
        opts._retried = true;
        return await doFetch();
      }
    }
    throw err;
  }
}

function isIdempotent(method) {
  return method === 'GET' || method === 'DELETE' || method === 'PUT' || method === 'HEAD';
}

async function tryRefresh() {
  const r = refreshSignal.value;
  if (!r) return false;
  try {
    const res = await fetch('/api/v1/auth/refresh', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ refresh_token: r }),
    });
    const data = await res.json();
    if (!res.ok) return false;
    localStorage.setItem(LS_JWT, data.access_token);
    localStorage.setItem(LS_REFRESH, data.refresh_token);
    jwtSignal.value = data.access_token;
    refreshSignal.value = data.refresh_token;
    return true;
  } catch {
    return false;
  }
}

function logout() {
  const r = refreshSignal.value;
  if (r) {
    fetch('/api/v1/auth/logout', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ refresh_token: r }),
    }).catch(() => {});
  }
  localStorage.removeItem(LS_JWT);
  localStorage.removeItem(LS_REFRESH);
  jwtSignal.value = null;
  refreshSignal.value = null;
  navigate('/login');
}

function decodeJwt(jwt) {
  try {
    const parts = jwt.split('.');
    if (parts.length !== 3) return null;
    const payload = JSON.parse(atob(parts[1].replace(/-/g, '+').replace(/_/g, '/')));
    return { id: payload.uid, email: payload.email, is_admin: !!payload.adm, exp: payload.exp };
  } catch {
    return null;
  }
}

function showToast(kind, text, ttl = 3000) {
  toastSignal.value = { kind, text, ttl };
  if (ttl > 0) {
    setTimeout(() => {
      if (toastSignal.value && toastSignal.value.text === text) toastSignal.value = null;
    }, ttl);
  }
}

// ============================================================
// top-level <App>
// ============================================================
function App() {
  const { path, segments } = routeSignal.value;
  const jwt = jwtSignal.value;
  const user = userSignal.value;

  if (!jwt && path !== '/login' && path !== '/register') {
    queueMicrotask(() => navigate('/login'));
    return html`<${Loading} />`;
  }
  if (jwt && segments[0] === 'admin' && !(user && user.is_admin)) {
    queueMicrotask(() => {
      showToast('err', '需要管理员权限');
      navigate('/apps');
    });
    return html`<${Loading} />`;
  }

  return html`
    <${Nav} />
    <main class="main">
      ${renderRoute(path, segments)}
    </main>
    <${Toast} />
  `;
}

function renderRoute(path, segments) {
  if (path === '/' || path === '/login') return html`<${Login} tab="login" />`;
  if (path === '/register') return html`<${Login} tab="register" />`;
  if (path === '/apps') return html`<${MyApps} />`;
  if (segments[0] === 'apps' && segments[1]) return html`<${AppDetail} appId=${segments[1]} />`;
  if (path === '/me') return html`<${MyQuota} />`;
  if (path === '/me/settings') return html`<${MySettings} />`;
  if (path === '/admin/apps') return html`<${AdminApps} />`;
  if (segments[0] === 'admin' && segments[1] === 'apps' && segments[2] === 'new')
    return html`<${AdminAppEdit} mode="new" />`;
  if (segments[0] === 'admin' && segments[1] === 'apps' && segments[2])
    return html`<${AdminAppEdit} mode="edit" appId=${segments[2]} />`;
  if (path === '/admin/users') return html`<${AdminUsers} />`;
  if (segments[0] === 'show-token') {
    const id = segments[1] || '';
    const token = decodeURIComponent(segments.slice(2).join('/'));
    return html`<${ShowToken} appId=${id} token=${token} />`;
  }
  return html`<${NotFound} />`;
}

// ============================================================
// components
// ============================================================
function Nav() {
  const user = userSignal.value;
  return html`
    <nav class="nav">
      <a class="nav-brand" href="/apps" onClick=${(e) => { e.preventDefault(); navigate('/apps'); }}>cfgsync</a>
      <div class="nav-right">
        ${user && html`
          <span>${user.email}</span>
          ${user.is_admin && html`
            <a href="/admin/apps" onClick=${(e) => { e.preventDefault(); navigate('/admin/apps'); }}>应用管理</a>
            <a href="/admin/users" onClick=${(e) => { e.preventDefault(); navigate('/admin/users'); }}>用户管理</a>
          `}
          <a href="/me" onClick=${(e) => { e.preventDefault(); navigate('/me'); }}>配额</a>
          <a href="/me/settings" onClick=${(e) => { e.preventDefault(); navigate('/me/settings'); }}>设置</a>
          <button onClick=${logout}>退出</button>
        `}
      </div>
    </nav>
  `;
}

function Loading() {
  return html`<main class="main"><p class="muted">加载中…</p></main>`;
}

function NotFound() {
  return html`
    <main class="main">
      <h1>页面不存在</h1>
      <p><a href="/apps" onClick=${(e) => { e.preventDefault(); navigate('/apps'); }}>返回首页</a></p>
    </main>
  `;
}

function Toast() {
  const t = toastSignal.value;
  if (!t) return null;
  return html`<div class=${'toast toast-' + t.kind}>${t.text}</div>`;
}

function Login({ tab }) {
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState(null);
  const isLogin = tab === 'login';

  const submit = async (e) => {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    try {
      const data = await call('POST', isLogin ? '/api/v1/auth/login' : '/api/v1/auth/register', {
        email, password,
      });
      localStorage.setItem(LS_JWT, data.access_token);
      localStorage.setItem(LS_REFRESH, data.refresh_token);
      jwtSignal.value = data.access_token;
      refreshSignal.value = data.refresh_token;
      const u = userSignal.value;
      navigate(u && u.is_admin ? '/admin/apps' : '/apps');
    } catch (e) {
      setErr(e.message);
    } finally {
      setBusy(false);
    }
  };

  return html`
    <h1>${isLogin ? '登录' : '注册'}</h1>
    <form class="form" onSubmit=${submit}>
      <div>
        <label>邮箱</label>
        <input type="email" required value=${email} onInput=${(e) => setEmail(e.target.value)} />
      </div>
      <div>
        <label>密码（至少 8 位）</label>
        <input type="password" required minLength="8" value=${password} onInput=${(e) => setPassword(e.target.value)} />
      </div>
      ${err && html`<div class="field-error">${err}</div>`}
      <button class="btn btn-primary" type="submit" disabled=${busy}>${busy ? '处理中…' : (isLogin ? '登录' : '注册')}</button>
      <p class="muted" style="font-size:12px">
        ${isLogin
          ? html`还没账号？<a href="/register" onClick=${(e) => { e.preventDefault(); navigate('/register'); }}>去注册</a>`
          : html`已有账号？<a href="/login" onClick=${(e) => { e.preventDefault(); navigate('/login'); }}>去登录</a>`}
      </p>
    </form>
  `;
}

function MyApps() {
  const [apps, setApps] = useState(null);
  const [err, setErr] = useState(null);
  useEffect(() => {
    call('GET', '/api/v1/apps')
      .then((d) => setApps(d.apps || []))
      .catch((e) => setErr(e.message));
  }, []);

  if (err) return html`<p class="error-text">${err}</p>`;
  if (apps === null) return html`<p class="muted">加载中…</p>`;
  if (apps.length === 0) return html`<p class="muted">暂无可用应用。请联系管理员注册 app_id。</p>`;

  return html`
    <h1>我的应用</h1>
    <div class="grid">
      ${apps.map((a) => html`
        <div class="card" key=${a.app_id}>
          <div class="card-title">${a.display_name}</div>
          <div class="card-meta">${a.app_id}</div>
          <p>${a.description || ''}</p>
          <a class="btn" href=${'/apps/' + a.app_id} onClick=${(e) => { e.preventDefault(); navigate('/apps/' + a.app_id); }}>管理 Token</a>
        </div>
      `)}
    </div>
  `;
}

function AppDetail({ appId }) {
  const [tokens, setTokens] = useState(null);
  const [err, setErr] = useState(null);
  const [label, setLabel] = useState('');
  const [confirmDel, setConfirmDel] = useState(null);
  const [busy, setBusy] = useState(false);

  const load = () => {
    setTokens(null);
    setErr(null);
    call('GET', '/api/v1/me/tokens')
      .then((d) => setTokens((d.tokens || []).filter((t) => t.app_id === appId)))
      .catch((e) => setErr(e.message));
  };
  useEffect(load, [appId]);

  const create = async (e) => {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    try {
      const data = await call('POST', `/api/v1/me/apps/${appId}/token`, { label });
      navigate('/show-token/' + appId + '/' + encodeURIComponent(data.token));
    } catch (e) {
      setErr(e.message);
    } finally {
      setBusy(false);
    }
  };

  const revoke = async (prefix) => {
    setBusy(true);
    setErr(null);
    try {
      await call('DELETE', `/api/v1/me/tokens/${prefix}`);
      showToast('ok', '已撤销');
      setConfirmDel(null);
      load();
    } catch (e) {
      setErr(e.message);
    } finally {
      setBusy(false);
    }
  };

  return html`
    <h1>管理 Token</h1>
    <p class="card-meta">${appId}</p>

    <h2>新建 Token</h2>
    <form class="form" onSubmit=${create}>
      <div>
        <label>标签（可选）</label>
        <input value=${label} onInput=${(e) => setLabel(e.target.value)} placeholder="例如：MacBook Air" />
      </div>
      ${err && html`<div class="field-error">${err}</div>`}
      <button class="btn btn-primary" type="submit" disabled=${busy}>${busy ? '生成中…' : '生成新 Token'}</button>
    </form>

    <h2>我名下的 Token</h2>
    ${tokens === null && html`<p class="muted">加载中…</p>`}
    ${tokens && tokens.length === 0 && html`<p class="muted">还没有 Token。生成一个用于同步软件。</p>`}
    ${tokens && tokens.length > 0 && html`
      <table>
        <thead><tr><th>标签</th><th>前缀</th><th>创建时间</th><th>最后使用</th><th></th></tr></thead>
        <tbody>
          ${tokens.map((t) => html`
            <tr key=${t.token_prefix}>
              <td data-label="标签">${t.label || '未命名'}</td>
              <td data-label="前缀"><code>${t.token_prefix}…</code></td>
              <td data-label="创建时间">${fmtTime(t.created_at)}</td>
              <td data-label="最后使用">${t.last_used_at ? fmtTime(t.last_used_at) : '从未'}</td>
              <td data-label="操作">
                ${confirmDel === t.token_prefix
                  ? html`<span class="btn-row">确认撤销？<button class="btn btn-danger" onClick=${() => revoke(t.token_prefix)}>确认</button><button class="btn" onClick=${() => setConfirmDel(null)}>取消</button></span>`
                  : html`<button class="btn btn-danger" onClick=${() => setConfirmDel(t.token_prefix)}>撤销</button>`}
              </td>
            </tr>
          `)}
        </tbody>
      </table>
    `}
  `;
}

function ShowToken({ appId, token }) {
  useEffect(() => {
    const guard = (e) => { e.preventDefault(); e.returnValue = ''; };
    window.addEventListener('beforeunload', guard);
    return () => window.removeEventListener('beforeunload', guard);
  }, []);

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(token);
      showToast('ok', '已复制到剪贴板');
    } catch {
      showToast('err', '复制失败，请手动选择');
    }
  };

  return html`
    <h1>新 Token</h1>
    <div class="warning">⚠️ 请立即复制此 token。离开此页面后将无法再看到完整 token。</div>
    <p class="card-meta">app_id: ${appId}</p>
    <div class="code" onClick=${(e) => { e.target.select && e.target.select(); }}>${token}</div>
    <div class="btn-row" style="margin-top:16px">
      <button class="btn btn-primary" onClick=${copy}>复制</button>
      <a class="btn" href=${'/apps/' + appId} onClick=${(e) => { e.preventDefault(); navigate('/apps/' + appId); }}>我已保存，去应用详情</a>
    </div>
  `;
}

function MyQuota() {
  const [quota, setQuota] = useState(null);
  const [err, setErr] = useState(null);
  useEffect(() => {
    call('GET', '/api/v1/me/quota')
      .then(setQuota)
      .catch((e) => setErr(e.message));
  }, []);
  if (err) return html`<p class="error-text">${err}</p>`;
  if (!quota) return html`<p class="muted">加载中…</p>`;

  const used = quota.used_bytes || 0;
  const limit = quota.limit_bytes || 1;
  const pct = Math.min(100, Math.round((used / limit) * 100));
  return html`
    <h1>我的配额</h1>
    <div class="card">
      <div>已用 ${humanBytes(used)} / ${humanBytes(limit)}（${pct}%）</div>
      <div style="background:var(--border);height:8px;border-radius:4px;margin-top:8px;overflow:hidden">
        <div style=${'background:var(--primary);height:100%;width:' + pct + '%'}></div>
      </div>
    </div>
    <h2>各应用占用</h2>
    ${quota.per_app && quota.per_app.length > 0
      ? html`
        <table>
          <thead><tr><th>app_id</th><th>大小</th><th>最后更新</th><th></th></tr></thead>
          <tbody>
            ${quota.per_app.map((p) => html`
              <tr key=${p.app_id}>
                <td data-label="app_id"><code>${p.app_id}</code></td>
                <td data-label="大小">${humanBytes(p.bytes)}</td>
                <td data-label="最后更新">${fmtTime(p.updated_at)}</td>
                <td data-label="操作"><a class="btn" href=${'/apps/' + p.app_id} onClick=${(e) => { e.preventDefault(); navigate('/apps/' + p.app_id); }}>管理</a></td>
              </tr>
            `)}
          </tbody>
        </table>
      `
      : html`<p class="muted">还没有任何 app 的配置数据。</p>`}
  `;
}

function MySettings() {
  return html`
    <h1>设置</h1>
    <p class="muted">修改密码功能尚未提供。如需修改请联系管理员。</p>
    <h2>退出登录</h2>
    <button class="btn btn-danger" onClick=${logout}>退出</button>
  `;
}

function AdminApps() {
  const [apps, setApps] = useState(null);
  const [err, setErr] = useState(null);
  const load = () => {
    setApps(null);
    call('GET', '/api/v1/admin/apps')
      .then((d) => setApps(d.apps || []))
      .catch((e) => setErr(e.message));
  };
  useEffect(load, []);
  if (err) return html`<p class="error-text">${err}</p>`;
  if (!apps) return html`<p class="muted">加载中…</p>`;
  return html`
    <div class="btn-row" style="justify-content:space-between;margin-bottom:16px">
      <h1 style="margin:0">应用管理</h1>
      <button class="btn btn-primary" onClick=${() => navigate('/admin/apps/new')}>新建应用</button>
    </div>
    ${apps.length === 0
      ? html`<p class="muted">还没有任何应用。</p>`
      : html`
        <table>
          <thead><tr><th>app_id</th><th>显示名</th><th>描述</th><th>创建时间</th><th></th></tr></thead>
          <tbody>
            ${apps.map((a) => html`
              <tr key=${a.app_id}>
                <td data-label="app_id"><code>${a.app_id}</code></td>
                <td data-label="显示名">${a.display_name}</td>
                <td data-label="描述">${a.description || ''}</td>
                <td data-label="创建时间">${fmtTime(a.created_at)}</td>
                <td data-label="操作">
                  <a class="btn" href=${'/admin/apps/' + a.app_id} onClick=${(e) => { e.preventDefault(); navigate('/admin/apps/' + a.app_id); }}>编辑</a>
                </td>
              </tr>
            `)}
          </tbody>
        </table>
      `}
  `;
}

function AdminAppEdit({ mode, appId }) {
  const isNew = mode === 'new';
  const [appId_, setAppId] = useState(appId || '');
  const [displayName, setDisplayName] = useState('');
  const [description, setDescription] = useState('');
  const [confirmDel, setConfirmDel] = useState(false);
  const [err, setErr] = useState(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (isNew) return;
    call('GET', `/api/v1/admin/apps/${appId}`)
      .then((a) => { setDisplayName(a.display_name); setDescription(a.description || ''); })
      .catch((e) => {
        if (e.status === 404) {
          showToast('err', '应用已被删除');
          navigate('/admin/apps');
        } else {
          setErr(e.message);
        }
      });
  }, [appId]);

  const submit = async (e) => {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    try {
      if (isNew) {
        await call('POST', '/api/v1/admin/apps', { app_id: appId_, display_name: displayName, description });
        showToast('ok', '已创建');
        navigate('/admin/apps');
      } else {
        await call('PATCH', `/api/v1/admin/apps/${appId}`, { display_name: displayName, description });
        showToast('ok', '已保存');
        navigate('/admin/apps');
      }
    } catch (e) {
      setErr(e.message);
    } finally {
      setBusy(false);
    }
  };

  const del = async () => {
    setBusy(true);
    setErr(null);
    try {
      await call('DELETE', `/api/v1/admin/apps/${appId}`);
      showToast('ok', '已删除');
      navigate('/admin/apps');
    } catch (e) {
      setErr(e.message);
    } finally {
      setBusy(false);
    }
  };

  return html`
    <h1>${isNew ? '新建应用' : '编辑应用'}</h1>
    <form class="form" onSubmit=${submit}>
      <div>
        <label>app_id（反域格式，如 com.example.app）</label>
        <input
          value=${appId_}
          onInput=${(e) => setAppId(e.target.value)}
          disabled=${!isNew}
          required
          pattern="^([a-z0-9][a-z0-9-]{1,30}\\.)+[a-z0-9][a-z0-9-]{1,30}$"
        />
      </div>
      <div>
        <label>显示名（必填，≤ 256 字符）</label>
        <input value=${displayName} onInput=${(e) => setDisplayName(e.target.value)} required maxLength="256" />
      </div>
      <div>
        <label>描述（≤ 1024 字符）</label>
        <textarea rows="3" value=${description} onInput=${(e) => setDescription(e.target.value)} maxLength="1024"></textarea>
      </div>
      ${err && html`<div class="field-error">${err}</div>`}
      <button class="btn btn-primary" type="submit" disabled=${busy}>${busy ? '保存中…' : '保存'}</button>
    </form>
    ${!isNew && html`
      <h2>删除应用</h2>
      ${confirmDel
        ? html`
          <div class="warning">确认删除 ${appId}？这会级联删除所有用户在此 app 下的所有数据（config / 历史 / token），无法恢复。</div>
          <div class="btn-row">
            <button class="btn btn-danger" onClick=${del} disabled=${busy}>确认删除</button>
            <button class="btn" onClick=${() => setConfirmDel(false)}>取消</button>
          </div>
        `
        : html`<button class="btn btn-danger" onClick=${() => setConfirmDel(true)}>删除此应用</button>`}
    `}
  `;
}

function AdminUsers() {
  const [data, setData] = useState(null);
  const [err, setErr] = useState(null);
  const [offset, setOffset] = useState(0);
  const limit = 20;

  const load = () => {
    setData(null);
    call('GET', `/api/v1/admin/users?limit=${limit}&offset=${offset}`)
      .then(setData)
      .catch((e) => setErr(e.message));
  };
  useEffect(load, [offset]);

  const promote = async (id) => {
    try {
      await call('POST', `/api/v1/admin/users/${id}/promote`);
      showToast('ok', '已提升为管理员');
      load();
    } catch (e) {
      showToast('err', e.message);
    }
  };

  if (err) return html`<p class="error-text">${err}</p>`;
  if (!data) return html`<p class="muted">加载中…</p>`;

  return html`
    <h1>用户管理</h1>
    <table>
      <thead><tr><th>邮箱</th><th>ID</th><th>角色</th><th>创建时间</th><th></th></tr></thead>
      <tbody>
        ${data.users.map((u) => html`
          <tr key=${u.id}>
            <td data-label="邮箱">${u.email}</td>
            <td data-label="ID"><code title=${u.id}>${u.id.slice(0, 8)}…</code></td>
            <td data-label="角色">${u.is_admin ? html`<span style="color:var(--success)">管理员</span>` : '普通用户'}</td>
            <td data-label="创建时间">${fmtTime(u.created_at)}</td>
            <td data-label="操作">
              ${u.is_admin
                ? html`<span class="muted" style="font-size:12px">已是管理员</span>`
                : html`<button class="btn" onClick=${() => promote(u.id)}>提升为管理员</button>`}
            </td>
          </tr>
        `)}
      </tbody>
    </table>
    <div class="btn-row" style="margin-top:16px">
      <button class="btn" disabled=${offset === 0} onClick=${() => setOffset(Math.max(0, offset - limit))}>上一页</button>
      <span class="muted">第 ${Math.floor(offset / limit) + 1} 页</span>
      <button class="btn" disabled=${data.users.length < limit} onClick=${() => setOffset(offset + limit)}>下一页</button>
    </div>
  `;
}

// ============================================================
// helpers
// ============================================================
function fmtTime(unix) {
  if (!unix) return '';
  const d = new Date(unix * 1000);
  return d.toLocaleString('zh-CN', { hour12: false });
}

function humanBytes(n) {
  if (n < 1024) return n + ' B';
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + ' KB';
  return (n / 1024 / 1024).toFixed(2) + ' MB';
}

// ============================================================
// mount
// ============================================================
render(h(App), document.getElementById('app'));
