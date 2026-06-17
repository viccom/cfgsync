// cfgsync WebUI — single-file Preact SPA. No build step.
//
// Fixes vs v0.3.0:
//  - Top-level <App> now subscribes to all relevant signals via useSignals(),
//    so login / nav clicks / state changes re-render without manual refresh.
//  - Layout / interaction matches the new design system in app.css (GitHub
//    Primer-inspired light theme: sticky nav, breadcrumbs, badges, skeleton
//    loaders, empty states, modal dialogs, toasts).
//  - Removed the Login/Register tab switch — single form with a "Sign up"
//    toggle below the button.
//  - All page components are now Preact function components using hooks.

import { h, render } from 'https://esm.sh/preact@10.22.0';
import {
  useState, useEffect, useRef, useCallback, useMemo,
} from 'https://esm.sh/preact@10.22.0/hooks';
import { signal, computed, useSignalEffect } from 'https://esm.sh/@preact/signals@1.2.3?deps=preact@10.22.0';
import htm from 'https://esm.sh/htm@3.1.1';

const html = htm.bind(h);

// ============================================================
// constants
// ============================================================
const LS_JWT = 'cfgsync_jwt';
const LS_REFRESH = 'cfgsync_refresh';

// app_id reverse-domain regex. Pulled out of JSX so htm's template parser
// doesn't mistake the curly braces for an expression.
const APP_ID_PATTERN = '^([a-z0-9][a-z0-9-]{1,30}\\.)+[a-z0-9][a-z0-9-]{1,30}$';

// Server-side error code -> user-facing Chinese message.
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
// signals (global state) — automatic re-render via useSignals()
// ============================================================
const jwtSignal = signal(localStorage.getItem(LS_JWT) || null);
const refreshSignal = signal(localStorage.getItem(LS_REFRESH) || null);
const userSignal = computed(() => (jwtSignal.value ? decodeJwt(jwtSignal.value) : null));
const routeSignal = signal(parseLocation());
const toastSignal = signal(null);        // { kind, text, id } | null
const menuOpenSignal = signal(false);    // user dropdown

// ============================================================
// routing
// ============================================================
function parseLocation() {
  const path = location.pathname || '/';
  return { path, segments: path.split('/').filter(Boolean) };
}
function navigate(to) {
  if (location.pathname !== to) {
    history.pushState({}, '', to);
    routeSignal.value = parseLocation();
  } else {
    routeSignal.value = parseLocation(); // force re-render even when same
  }
}
window.addEventListener('popstate', () => { routeSignal.value = parseLocation(); });

// ============================================================
// jwt decode
// ============================================================
function decodeJwt(jwt) {
  try {
    const parts = jwt.split('.');
    if (parts.length !== 3) return null;
    const payload = JSON.parse(atob(parts[1].replace(/-/g, '+').replace(/_/g, '/')));
    return { id: payload.uid, email: payload.email, is_admin: !!payload.adm, exp: payload.exp };
  } catch { return null; }
}

function isExpired(decoded) {
  if (!decoded || !decoded.exp) return false; // unknown expiry
  return decoded.exp * 1000 < Date.now();
}

// ============================================================
// api client — fetch wrapper with silent refresh on 401
// ============================================================
let refreshInFlight = null;
async function tryRefresh() {
  if (refreshInFlight) return refreshInFlight;
  const r = refreshSignal.value;
  if (!r) return false;
  refreshInFlight = (async () => {
    try {
      const res = await fetch('/api/v1/auth/refresh', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ refresh_token: r }),
      });
      if (!res.ok) return false;
      const data = await res.json();
      localStorage.setItem(LS_JWT, data.access_token);
      localStorage.setItem(LS_REFRESH, data.refresh_token);
      jwtSignal.value = data.access_token;
      refreshSignal.value = data.refresh_token;
      return true;
    } catch { return false; }
    finally { refreshInFlight = null; }
  })();
  return refreshInFlight;
}

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
    return { res, data };
  };

  let { res, data } = await doFetch();

  // Silent refresh + retry on 401 for idempotent methods.
  if (res.status === 401 && !opts._retried && !isAuthFreePath(path) && isIdempotent(method)) {
    const ok = await tryRefresh();
    if (ok) {
      ({ res, data } = await doFetch());
    }
  }

  if (!res.ok) {
    const code = data?.error || `http_${res.status}`;
    const err = new Error(ERR_MSGS[code] || code);
    err.status = res.status;
    err.body = data;
    err.code = code;
    throw err;
  }
  return data;
}

function isAuthFreePath(path) { return path.startsWith('/api/v1/auth/'); }
function isIdempotent(m) { return m === 'GET' || m === 'DELETE' || m === 'PUT' || m === 'HEAD'; }

// ============================================================
// toasts
// ============================================================
let toastSeq = 0;
function showToast(kind, text) {
  toastSignal.value = { kind, text, id: ++toastSeq };
  setTimeout(() => {
    if (toastSignal.value?.id === toastSeq) toastSignal.value = null;
  }, 3500);
}

// ============================================================
// auth actions
// ============================================================
function clearLocalSession() {
  localStorage.removeItem(LS_JWT);
  localStorage.removeItem(LS_REFRESH);
  jwtSignal.value = null;
  refreshSignal.value = null;
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
  clearLocalSession();
  showToast('info', '已退出登录');
  menuOpenSignal.value = false;
  navigate('/login');
}

// ============================================================
// shared hooks
// ============================================================
function useApi(method, path, deps = []) {
  // Returns { data, err, loading, reload } for a single-shot GET.
  const [data, setData] = useState(null);
  const [err, setErr] = useState(null);
  const [loading, setLoading] = useState(true);
  const [tick, setTick] = useState(0);
  useEffect(() => {
    let alive = true;
    setLoading(true);
    setErr(null);
    call(method, path).then(
      (d) => { if (alive) { setData(d); setLoading(false); } },
      (e) => { if (alive) { setErr(e); setLoading(false); } },
    );
    return () => { alive = false; };
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [method, path, ...deps, tick]);
  return { data, err, loading, reload: () => setTick(t => t + 1) };
}

function usePageClickAway(enabled, onClose) {
  const ref = useRef(null);
  useEffect(() => {
    if (!enabled) return;
    const handler = (e) => {
      if (ref.current && !ref.current.contains(e.target)) onClose();
    };
    document.addEventListener('mousedown', handler);
    return () => document.removeEventListener('mousedown', handler);
  }, [enabled, onClose]);
  return ref;
}

// ============================================================
// top-level <App>
// ============================================================
function App() {
  // Subscribe to all signals that affect rendering. This is the v0.3.0 fix:
  // without explicit subscription, changes to jwtSignal / routeSignal did
  // not trigger re-render.

  const { path, segments } = routeSignal.value;
  const user = userSignal.value;
  const jwt = jwtSignal.value;

  // Auto-redirect: not logged in -> /login, non-admin hitting /admin -> /apps.
  useEffect(() => {
    if (!jwt && path !== '/login' && path !== '/register' && path !== '/') {
      navigate('/login');
    } else if (jwt && path === '/') {
      navigate(user && user.is_admin ? '/admin/apps' : '/apps');
    } else if (jwt && segments[0] === 'admin' && !(user && user.is_admin)) {
      showToast('err', '需要管理员权限');
      navigate('/apps');
    } else if (jwt && isExpired(user)) {
      showToast('err', '会话已过期，请重新登录');
      clearLocalSession();
      navigate('/login');
    }
  }, [jwt, path, user]);

  // Close user menu on route change.
  useEffect(() => { menuOpenSignal.value = false; }, [path]);

  if (!jwt && (path === '/login' || path === '/register')) {
    return html`<${AuthPage} mode=${path === '/register' ? 'register' : 'login'} />`;
  }
  if (!jwt) return html`<${Loading} />`;

  return html`
    <${Nav} />
    <main class="main ${isAdminPath(segments) ? 'main-wide' : ''}">
      ${renderRoute(path, segments)}
    </main>
    <${ToastStack} />
  `;
}

function isAdminPath(segments) { return segments[0] === 'admin'; }

function renderRoute(path, segments) {
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
    return html`<${ShowToken} appId=${segments[1]} token=${decodeURIComponent(segments.slice(2).join('/'))} />`;
  }
  return html`<${NotFound} />`;
}

// ============================================================
// Nav (sticky top bar with user dropdown)
// ============================================================
function Nav() {

  const user = userSignal.value;
  const { path } = routeSignal.value;
  const isAdmin = user && user.is_admin;
  const menuOpen = menuOpenSignal.value;
  const ref = usePageClickAway(menuOpen, () => { menuOpenSignal.value = false; });

  const isActive = (p) => path === p;
  const initial = (user && user.email) ? user.email[0].toUpperCase() : '?';

  return html`
    <nav class="nav" aria-label="primary">
      <a class="nav-brand" href="/apps"
         onClick=${(e) => { e.preventDefault(); navigate('/apps'); }}>
        <span class="nav-brand-mark">cf</span>
        cfgsync
      </a>
      <div class="nav-right">
        ${isAdmin ? html`<${NavAdminLinks} isActive=${isActive} />` : null}
        <a class="nav-link ${isActive('/me') ? 'active' : ''}"
           href="/me"
           onClick=${(e) => { e.preventDefault(); navigate('/me'); }}>
          <span>配额</span>
        </a>
        <a class="nav-link ${isActive('/me/settings') ? 'active' : ''}"
           href="/me/settings"
           onClick=${(e) => { e.preventDefault(); navigate('/me/settings'); }}>
          <span>设置</span>
        </a>
        <span class="nav-divider"></span>
        <button ref=${ref} class="nav-user-btn"
                onClick=${(e) => { e.stopPropagation(); menuOpenSignal.value = !menuOpen; }}
                aria-haspopup="menu" aria-expanded=${menuOpen}>
          <span class="nav-user-btn-avatar">${initial}</span>
          <span>${user ? user.email : ''}</span>
          <span aria-hidden="true">▾</span>
        </button>
        ${menuOpen ? html`<${NavUserMenu} onNavigate=${() => navigate('/me/settings')} />` : null}
      </div>
    </nav>
  `;
}

// ============================================================
// NavAdminLinks — admin-only nav entries (应用管理 / 用户管理), as a
// separate htm template so the parent <Nav> doesn't use the
// ${A && html`...`} pattern htm can't parse across multi-line children.
// ============================================================
function NavAdminLinks({ isActive }) {
  return html`
    <a class="nav-link ${isActive('/admin/apps') ? 'active' : ''}"
       href="/admin/apps"
       onClick=${(e) => { e.preventDefault(); navigate('/admin/apps'); }}>应用管理</a>
    <a class="nav-link ${isActive('/admin/users') ? 'active' : ''}"
       href="/admin/users"
       onClick=${(e) => { e.preventDefault(); navigate('/admin/users'); }}>用户管理</a>
  `;
}

// ============================================================
// AuthFooterLink — "Don't have an account? Sign up" / inverse, as a
// separate htm template to keep the AuthPage template parser-simple.
// ============================================================
function AuthFooterLink({ kind, setTab }) {
  if (kind === 'register') {
    return html`
      <span>没有账号？<a href="/register" onClick=${(e) => { e.preventDefault(); setTab('register'); }}>立即注册</a></span>
    `;
  }
  return html`
    <span>已有账号？<a href="/login" onClick=${(e) => { e.preventDefault(); setTab('login'); }}>直接登录</a></span>
  `;
}

// ============================================================
// NavUserMenu — dropdown rendered as a separate htm template so the
// parent <Nav> template doesn't have to use ${A && html`...`} (which
// htm can't parse when the inner template contains complex children).
// ============================================================
function NavUserMenu({ user, onNavigate }) {
  return html`
    <div class="nav-user-menu" role="menu">
      <div class="nav-user-menu-header">
        <div class="nav-user-menu-email">${user.email}</div>
        <div class="nav-user-menu-role">${user.is_admin ? '管理员' : '普通用户'}</div>
      </div>
      <button class="nav-user-menu-item" role="menuitem"
              onClick=${onNavigate}>账号设置</button>
      <button class="nav-user-menu-item danger" role="menuitem"
              onClick=${logout}>退出登录</button>
    </div>
  `;
}

// ============================================================
// Toast stack
// ============================================================
function ToastStack() {

  const t = toastSignal.value;
  if (!t) return null;
  return html`
    <div class="toast-stack" role="status" aria-live="polite">
      <div class=${'toast toast-' + t.kind}>
        <span>${t.text}</span>
        <button class="toast-close" aria-label="关闭"
                onClick=${() => { toastSignal.value = null; }}>×</button>
      </div>
    </div>
  `;
}

// ============================================================
// Loading / NotFound
// ============================================================
function Loading() {
  return html`<div class="loading-text" style="padding:48px;text-align:center">加载中…</div>`;
}

function NotFound() {
  return html`
    <div class="main">
      <div class="empty">
        <div class="empty-icon">?</div>
        <div class="empty-title">页面不存在</div>
        <div class="empty-desc">你访问的路径没有匹配任何页面。</div>
        <div class="empty-cta">
          <a class="btn" href="/apps"
             onClick=${(e) => { e.preventDefault(); navigate('/apps'); }}>返回首页</a>
        </div>
      </div>
    </div>
  `;
}

// ============================================================
// Auth (login + register)
// ============================================================
function AuthPage({ mode }) {
  // local state
  const [tab, setTab] = useState(mode === 'register' ? 'register' : 'login');
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState(null);

  const submit = async (e) => {
    e.preventDefault();
    setBusy(true); setErr(null);
    try {
      const path = tab === 'login' ? '/api/v1/auth/login' : '/api/v1/auth/register';
      const data = await call('POST', path, { email, password });
      localStorage.setItem(LS_JWT, data.access_token);
      localStorage.setItem(LS_REFRESH, data.refresh_token);
      jwtSignal.value = data.access_token;
      refreshSignal.value = data.refresh_token;
      // redirect happens via App's useEffect
    } catch (e) { setErr(e.message); setBusy(false); }
  };

  return html`
    <a class="skip-link" href="#main">跳到主内容</a>
    <div class="auth-page" id="main">
      <div class="auth-card">
        <div class="auth-brand">
          <div class="auth-brand-mark">cf</div>
          <div class="auth-brand-name">cfgsync</div>
        </div>
        <div class="auth-tabs" role="tablist">
          <button class=${'auth-tab ' + (tab === 'login' ? 'active' : '')}
                  role="tab" aria-selected=${tab === 'login'}
                  onClick=${() => setTab('login')}>登录</button>
          <button class=${'auth-tab ' + (tab === 'register' ? 'active' : '')}
                  role="tab" aria-selected=${tab === 'register'}
                  onClick=${() => setTab('register')}>注册</button>
        </div>
        <form class="form" onSubmit=${submit} novalidate>
          <div class="form-row">
            <label for="email">邮箱</label>
            <input id="email" name="email" type="email" autocomplete="email"
                   required value=${email} onInput=${(e) => setEmail(e.target.value)} />
          </div>
          <div class="form-row">
            <label for="password">密码</label>
            <input id="password" name="password" type="password" autocomplete=${tab === 'login' ? 'current-password' : 'new-password'}
                   required minLength="8" value=${password} onInput=${(e) => setPassword(e.target.value)} />
            <span class="hint">至少 8 位</span>
          </div>
          ${err && html`<div class="notice notice-danger"><span class="notice-icon">!</span><span>${err}</span></div>`}
          <div class="form-actions">
            <button class="btn btn-primary btn-block" type="submit" disabled=${busy}>
              ${busy ? '处理中…' : (tab === 'login' ? '登录' : '注册')}
            </button>
          </div>
        </form>
        <div class="form-footer">
          ${tab === 'login' ? html`<${AuthFooterLink} kind="register" tab=${tab} setTab=${setTab} />` : html`<${AuthFooterLink} kind="login" tab=${tab} setTab=${setTab} />`}
        </div>
      </div>
    </div>
  `;
}

// ============================================================
// MyApps
// ============================================================
function MyApps() {
  const { data, err, loading } = useApi('GET', '/api/v1/apps');
  if (err) return html`<${ErrorBox} err=${err} />`;
  if (loading) return html`<${AppsSkeleton} />`;
  const apps = data?.apps || [];

  return html`
    <nav class="breadcrumb" aria-label="breadcrumbs">
      <a href="/apps" onClick=${(e) => { e.preventDefault(); navigate('/apps'); }}>我的应用</a>
    </nav>
    <div class="page-header">
      <div class="page-header-content">
        <h1 class="page-title">我的应用</h1>
        <p class="page-description">浏览可用应用，配置同步凭证。</p>
      </div>
    </div>

    ${apps.length === 0
      ? html`
        <div class="empty">
          <div class="empty-icon">⌘</div>
          <div class="empty-title">暂无可用应用</div>
          <div class="empty-desc">管理员还没有注册任何 app_id。如果你已经搭建了服务端，请联系管理员添加应用。</div>
        </div>
      `
      : html`
        <div class="grid" style="display:grid;grid-template-columns:repeat(auto-fill,minmax(280px,1fr));gap:12px">
          ${apps.map((a) => html`
            <a key=${a.app_id} class="card" href=${'/apps/' + a.app_id}
               onClick=${(e) => { e.preventDefault(); navigate('/apps/' + a.app_id); }}
               style="display:block;color:inherit;text-decoration:none">
              <div class="card-title">${a.display_name}</div>
              <div class="card-meta mono">${a.app_id}</div>
              ${a.description && html`<p style="margin:8px 0 0;color:var(--text-muted);font-size:13px">${a.description}</p>`}
              <div class="btn-row" style="margin-top:12px">
                <span class="btn btn-sm">管理 Token</span>
              </div>
            </a>
          `)}
        </div>
      `}
  `;
}

function AppsSkeleton() {
  return html`
    <div class="grid" style="display:grid;grid-template-columns:repeat(auto-fill,minmax(280px,1fr));gap:12px">
      ${[1, 2, 3].map(() => html`<div class="card"><div class="skeleton" style="height:80px"></div></div>`)}
    </div>
  `;
}

function ErrorBox({ err }) {
  return html`
    <div class="notice notice-danger">
      <span class="notice-icon">!</span>
      <div>
        <div><strong>加载失败</strong></div>
        <div>${err.message}</div>
        ${err.status === 401 && html`
          <div style="margin-top:8px">
            <a class="btn btn-sm" href="/login"
               onClick=${(e) => { e.preventDefault(); clearLocalSession(); navigate('/login'); }}>
              重新登录
            </a>
          </div>
        `}
      </div>
    </div>
  `;
}

// ============================================================
// AppDetail (per-app token management)
// ============================================================
function AppDetail({ appId }) {

  const { data, err, loading, reload } = useApi('GET', '/api/v1/me/tokens', [appId]);
  const [label, setLabel] = useState('');
  const [creating, setCreating] = useState(false);
  const [confirmDel, setConfirmDel] = useState(null);
  const [errMsg, setErrMsg] = useState(null);

  if (err) return html`<${ErrorBox} err=${err} />`;
  if (loading) return html`<div class="loading-text">加载中…</div>`;

  const myTokens = (data?.tokens || []).filter((t) => t.app_id === appId);

  const create = async (e) => {
    e.preventDefault();
    setCreating(true); setErrMsg(null);
    try {
      const data = await call('POST', `/api/v1/me/apps/${appId}/token`, { label });
      navigate('/show-token/' + appId + '/' + encodeURIComponent(data.token));
    } catch (e) {
      setErrMsg(e.message);
      setCreating(false);
    }
  };

  const revoke = async (prefix) => {
    setConfirmDel(null);
    try {
      await call('DELETE', `/api/v1/me/tokens/${prefix}`);
      showToast('ok', '已撤销');
      reload();
    } catch (e) { showToast('err', e.message); }
  };

  return html`
    <nav class="breadcrumb">
      <a href="/apps" onClick=${(e) => { e.preventDefault(); navigate('/apps'); }}>我的应用</a>
      <span class="breadcrumb-sep">/</span>
      <span class="breadcrumb-current">${appId}</span>
    </nav>
    <div class="page-header">
      <div class="page-header-content">
        <h1 class="page-title">${appId}</h1>
        <p class="page-description">在此应用的同步凭证管理。</p>
      </div>
    </div>

    <div class="card">
      <h3 style="margin-top:0">新建 Token</h3>
      <form class="form form-wide" onSubmit=${create}>
        <div class="form-row-inline">
          <div class="form-row">
            <label for="label">标签（可选）</label>
            <input id="label" type="text" placeholder="如 MacBook Air"
                   value=${label} onInput=${(e) => setLabel(e.target.value)} />
            <span class="hint">便于在多台设备间区分。</span>
          </div>
          <div class="form-row" style="justify-content:flex-end">
            <div class="form-actions">
              <button class="btn btn-primary" type="submit" disabled=${creating}>
                ${creating ? '生成中…' : '生成新 Token'}
              </button>
            </div>
          </div>
        </div>
        ${errMsg && html`<div class="notice notice-danger"><span class="notice-icon">!</span>${errMsg}</div>`}
      </form>
    </div>

    <div class="card">
      <h3 style="margin-top:0">我名下的 Token</h3>
      ${myTokens.length === 0
        ? html`
          <div class="empty" style="padding:32px 16px">
            <div class="empty-icon" style="width:40px;height:40px;font-size:18px">○</div>
            <div class="empty-title" style="font-size:14px">还没有 Token</div>
            <div class="empty-desc">生成一个 token 粘到软件客户端即可启用同步。</div>
          </div>
        `
        : html`
          <div class="table-wrap" style="margin:0">
            <table class="table">
              <thead>
                <tr><th>标签</th><th>前缀</th><th>创建时间</th><th>最后使用</th><th></th></tr>
              </thead>
              <tbody>
                ${myTokens.map((t) => html`
                  <tr key=${t.token_prefix}>
                    <td data-label="标签">${t.label || html`<span class="loading-text">未命名</span>`}</td>
                    <td data-label="前缀"><code>${t.token_prefix}…</code></td>
                    <td data-label="创建时间" class="muted">${fmtTime(t.created_at)}</td>
                    <td data-label="最后使用" class="muted">${t.last_used_at ? fmtTime(t.last_used_at) : '从未'}</td>
                    <td class="table-actions">
                      ${confirmDel === t.token_prefix
                        ? html`<span class="btn-row">
                            <span style="color:var(--danger);font-size:13px">确认撤销？</span>
                            <button class="btn btn-sm btn-danger" onClick=${() => revoke(t.token_prefix)}>确认</button>
                            <button class="btn btn-sm" onClick=${() => setConfirmDel(null)}>取消</button>
                          </span>`
                        : html`<button class="btn btn-sm btn-danger" onClick=${() => setConfirmDel(t.token_prefix)}>撤销</button>`}
                    </td>
                  </tr>
                `)}
              </tbody>
            </table>
          </div>
        `}
    </div>
  `;
}

// ============================================================
// MyQuota
// ============================================================
function MyQuota() {
  const { data, err, loading } = useApi('GET', '/api/v1/me/quota');
  if (err) return html`<${ErrorBox} err=${err} />`;
  if (loading) return html`<div class="loading-text">加载中…</div>`;

  const used = data?.storage_used_bytes || 0;
  const limit = data?.storage_limit_bytes || 1;
  const tokenCount = data?.app_token_count || 0;
  const tokenLimit = data?.app_token_limit || 0;
  const pct = Math.min(100, Math.round((used / limit) * 100));
  const fillClass = pct >= 95 ? 'danger' : pct >= 80 ? 'warn' : '';

  return html`
    <nav class="breadcrumb">
      <a href="/me" onClick=${(e) => { e.preventDefault(); navigate('/me'); }}>配额</a>
    </nav>
    <div class="page-header">
      <div class="page-header-content">
        <h1 class="page-title">我的配额</h1>
        <p class="page-description">查看你的存储与 token 使用情况。</p>
      </div>
    </div>

    <div class="card">
      <div class="card-header">
        <div>
          <div class="card-title">存储用量</div>
          <div class="card-meta">所有应用当前 payload 字节总和</div>
        </div>
        <span class="badge ${pct >= 95 ? 'badge-danger' : pct >= 80 ? 'badge-warning' : ''}">${pct}%</span>
      </div>
      <div class="quota-bar">
        <div class=${'quota-bar-fill ' + fillClass} style=${'width:' + pct + '%'}></div>
      </div>
      <div class="quota-stats">
        <span>已用 <strong>${humanBytes(used)}</strong></span>
        <span>上限 <strong>${humanBytes(limit)}</strong></span>
      </div>
    </div>

    <div class="card">
      <div class="card-header">
        <div>
          <div class="card-title">App Token</div>
          <div class="card-meta">你已为多少个应用申请了同步凭证</div>
        </div>
        <span class="badge">${tokenCount} / ${tokenLimit}</span>
      </div>
      <p class="loading-text" style="margin:0">
        数量受管理员配置的 <code>USER_APP_TOKEN_LIMIT</code> 限制。每个 (你, app_id) 只能有一个有效 token；再次申请会让旧 token 失效。
      </p>
    </div>

    <div class="card">
      <h3 style="margin-top:0">各应用占用</h3>
      <p class="loading-text" style="margin:0">
        按 app 维度的占用明细尚未在 WebUI 暴露。如需查看具体数据，请联系管理员或参考 <code>/api/v1/me/quota</code> 接口。
      </p>
    </div>
  `;
}

// ============================================================
// MySettings
// ============================================================
function MySettings() {

  const user = userSignal.value;
  return html`
    <nav class="breadcrumb">
      <a href="/me/settings" onClick=${(e) => { e.preventDefault(); navigate('/me/settings'); }}>设置</a>
    </nav>
    <div class="page-header">
      <div class="page-header-content">
        <h1 class="page-title">账号设置</h1>
      </div>
    </div>

    <div class="card">
      <h3 style="margin-top:0">账号信息</h3>
      <dl style="margin:0;display:grid;grid-template-columns:120px 1fr;gap:8px 16px">
        <dt class="loading-text">邮箱</dt><dd style="margin:0">${user ? user.email : ''}</dd>
        <dt class="loading-text">角色</dt><dd style="margin:0">${user && user.is_admin ? html`<span class="badge badge-primary">管理员</span>` : html`<span class="badge">普通用户</span>`}</dd>
        <dt class="loading-text">用户 ID</dt><dd style="margin:0" class="mono" title=${user ? user.id : ''}>${user ? (user.id || '').slice(0, 16) + '…' : ''}</dd>
      </dl>
    </div>

    <div class="card">
      <h3 style="margin-top:0">会话</h3>
      <p class="loading-text">Token 存于浏览器 localStorage，登出后会立即失效。</p>
      <button class="btn btn-danger" onClick=${logout}>退出登录</button>
    </div>

    <div class="card">
      <h3 style="margin-top:0">修改密码</h3>
      <p class="loading-text">该功能尚未提供。如需修改请联系管理员通过 SQL 重置 <code>password_hash</code>。</p>
    </div>
  `;
}

// ============================================================
// ShowToken (one-time display after creation)
// ============================================================
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
    } catch { showToast('err', '复制失败，请手动选中'); }
  };

  // Connect-string version: bundles server URL, app_id, and token for the
  // 1Remote client (see its ConnectString.Parse / CloudSyncService).
  const copyConnect = async () => {
    const json = JSON.stringify({ v: 1, url: location.origin, app_id: appId, token });
    const b64 = btoa(json).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
    const cs = 'cfgsync1:' + b64;
    try {
      await navigator.clipboard.writeText(cs);
      showToast('ok', '已复制为接入串');
    } catch { showToast('err', '复制失败，请手动选中'); }
  };

  return html`
    <div class="main main-narrow">
      <nav class="breadcrumb">
        <a href="/apps/${appId}" onClick=${(e) => { e.preventDefault(); navigate('/apps/' + appId); }}>${appId}</a>
        <span class="breadcrumb-sep">/</span>
        <span class="breadcrumb-current">新 Token</span>
      </nav>
      <div class="page-header">
        <div class="page-header-content">
          <h1 class="page-title">新 Token 已生成</h1>
          <p class="page-description">立即复制保存，离开此页面后将无法再次看到完整 token。</p>
        </div>
      </div>

      <div class="notice notice-warning">
        <span class="notice-icon">⚠</span>
        <span><strong>仅显示一次</strong>。页面刷新或离开后无法找回，请妥善保存。</span>
      </div>

      <div class="card">
        <div class="card-header">
          <div class="card-title">App</div>
          <span class="badge mono">${appId}</span>
        </div>
        <div class="card-title" style="margin-bottom:8px">Token</div>
        <div class="code-block lg" onClick=${(e) => e.target.select && e.target.select()}>${token}</div>
        <div class="btn-row" style="margin-top:16px">
          <button class="btn btn-primary" onClick=${copy}>复制 token</button>
          <button class="btn" onClick=${copyConnect}>复制为接入串</button>
        </div>
        <p class="loading-text" style="margin-top:12px">
          「复制为接入串」会把 URL、app_id、token 一起打包，1Remote 客户端可直接粘贴。
        </p>
      </div>

      <div class="btn-row-right" style="margin-top:16px">
        <a class="btn" href=${'/apps/' + appId}
           onClick=${(e) => { e.preventDefault(); navigate('/apps/' + appId); }}>我已保存</a>
      </div>
    </div>
  `;
}

// ============================================================
// Admin: Apps
// ============================================================
function AdminApps() {

  const { data, err, loading } = useApi('GET', '/api/v1/admin/apps');
  if (err) return html`<${ErrorBox} err=${err} />`;
  if (loading) return html`<div class="loading-text">加载中…</div>`;
  const apps = data?.apps || [];

  return html`
    <nav class="breadcrumb">
      <a href="/admin/apps" onClick=${(e) => { e.preventDefault(); navigate('/admin/apps'); }}>应用管理</a>
    </nav>
    <div class="page-header">
      <div class="page-header-content">
        <h1 class="page-title">应用管理</h1>
        <p class="page-description">注册新的 app_id 或编辑现有应用。</p>
      </div>
      <button class="btn btn-primary" onClick=${() => navigate('/admin/apps/new')}>+ 新建应用</button>
    </div>

    ${apps.length === 0
      ? html`
        <div class="empty">
          <div class="empty-icon">+</div>
          <div class="empty-title">还没有任何应用</div>
          <div class="empty-desc">先注册一个 app_id，用户才能申请 token 开始同步。</div>
          <div class="empty-cta">
            <button class="btn btn-primary" onClick=${() => navigate('/admin/apps/new')}>新建应用</button>
          </div>
        </div>
      `
      : html`
        <div class="table-wrap">
          <table class="table">
            <thead>
              <tr><th>app_id</th><th>显示名</th><th>描述</th><th>创建时间</th><th></th></tr>
            </thead>
            <tbody>
              ${apps.map((a) => html`
                <tr key=${a.app_id}>
                  <td data-label="app_id"><code>${a.app_id}</code></td>
                  <td data-label="显示名">${a.display_name}</td>
                  <td data-label="描述" class="muted" style="max-width:320px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">${a.description || '—'}</td>
                  <td data-label="创建时间" class="muted">${fmtTime(a.created_at)}</td>
                  <td class="table-actions">
                    <a class="btn btn-sm" href=${'/admin/apps/' + a.app_id}
                       onClick=${(e) => { e.preventDefault(); navigate('/admin/apps/' + a.app_id); }}>编辑</a>
                  </td>
                </tr>
              `)}
            </tbody>
          </table>
        </div>
      `}
  `;
}

// ============================================================
// Admin: AppEdit (new + edit)
// ============================================================
function AdminAppEdit({ mode, appId }) {
  const isNew = mode === 'new';
  const [appId_, setAppId] = useState(appId || '');
  const [displayName, setDisplayName] = useState('');
  const [description, setDescription] = useState('');
  const [err, setErr] = useState(null);
  const [busy, setBusy] = useState(false);
  const [confirmDel, setConfirmDel] = useState(false);
  const [loaded, setLoaded] = useState(isNew);

  useEffect(() => {
    if (isNew) return;
    call('GET', `/api/v1/admin/apps/${appId}`).then(
      (a) => { setAppId(a.app_id); setDisplayName(a.display_name); setDescription(a.description || ''); setLoaded(true); },
      (e) => { setErr(e.message); setLoaded(true); }
    );
  }, [appId]);

  const submit = async (e) => {
    e.preventDefault();
    setBusy(true); setErr(null);
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
    } catch (e) { setErr(e.message); setBusy(false); }
  };

  const del = async () => {
    setBusy(true); setErr(null);
    try {
      await call('DELETE', `/api/v1/admin/apps/${appId}`);
      showToast('ok', '已删除');
      navigate('/admin/apps');
    } catch (e) { setErr(e.message); setBusy(false); }
  };

  if (!loaded) return html`<div class="loading-text">加载中…</div>`;

  return html`
    <nav class="breadcrumb">
      <a href="/admin/apps" onClick=${(e) => { e.preventDefault(); navigate('/admin/apps'); }}>应用管理</a>
      <span class="breadcrumb-sep">/</span>
      <span class="breadcrumb-current">${isNew ? '新建' : appId}</span>
    </nav>
    <div class="page-header">
      <div class="page-header-content">
        <h1 class="page-title">${isNew ? '新建应用' : '编辑应用'}</h1>
        <p class="page-description">${isNew ? '注册一个新的 app_id' : '修改显示名或描述'}</p>
      </div>
    </div>

    <form class="card" onSubmit=${submit} novalidate>
      <div class="form form-wide">
        <div class="form-row">
          <label for="appid">app_id</label>
          <input id="appid" type="text" required disabled=${!isNew}
                 value=${appId_} onInput=${(e) => setAppId(e.target.value)}
                 pattern=${APP_ID_PATTERN} />
          <span class="hint">反域格式，如 <code>com.example.myapp</code>。创建后不可修改。</span>
        </div>
        <div class="form-row">
          <label for="dname">显示名</label>
          <input id="dname" type="text" required maxLength="256"
                 value=${displayName} onInput=${(e) => setDisplayName(e.target.value)} />
          <span class="hint">在 WebUI 列表中显示的友好名称。</span>
        </div>
        <div class="form-row">
          <label for="desc">描述（可选）</label>
          <textarea id="desc" rows="3" maxLength="1024"
                    value=${description} onInput=${(e) => setDescription(e.target.value)}></textarea>
        </div>
        ${err && html`<div class="notice notice-danger"><span class="notice-icon">!</span>${err}</div>`}
        <div class="form-actions">
          <button class="btn btn-primary" type="submit" disabled=${busy}>
            ${busy ? '保存中…' : '保存'}
          </button>
          <a class="btn" href="/admin/apps"
             onClick=${(e) => { e.preventDefault(); navigate('/admin/apps'); }}>取消</a>
        </div>
      </div>
    </form>

    ${!isNew && html`
      <div class="card">
        <h3 style="margin-top:0">删除应用</h3>
        ${confirmDel
          ? html`
            <div class="notice notice-danger">
              <span class="notice-icon">!</span>
              <div>
                <div><strong>确认删除 ${appId}？</strong></div>
                <div>这会级联删除所有用户在该 app 下的所有数据（配置、历史、token），无法恢复。</div>
              </div>
            </div>
            <div class="btn-row">
              <button class="btn btn-danger" disabled=${busy} onClick=${del}>确认删除</button>
              <button class="btn" onClick=${() => setConfirmDel(false)}>取消</button>
            </div>
          `
          : html`
            <p class="loading-text">删除后无法恢复。建议先通知所有使用此 app 的用户。</p>
            <button class="btn btn-danger" onClick=${() => setConfirmDel(true)}>删除此应用</button>
          `}
      </div>
    `}
  `;
}

// ============================================================
// Admin: Users
// ============================================================
function AdminUsers() {

  const [offset, setOffset] = useState(0);
  const limit = 20;
  const path = `/api/v1/admin/users?limit=${limit}&offset=${offset}`;
  const { data, err, loading } = useApi('GET', path, [offset]);

  if (err) return html`<${ErrorBox} err=${err} />`;
  if (loading) return html`<div class="loading-text">加载中…</div>`;

  const users = data?.users || [];
  const total = users.length < limit ? offset + users.length : offset + limit + 1;
  const page = Math.floor(offset / limit) + 1;
  const hasPrev = offset > 0;
  const hasNext = users.length === limit;

  const promote = async (id) => {
    try {
      await call('POST', `/api/v1/admin/users/${id}/promote`);
      showToast('ok', '已提升为管理员');
      // Trigger reload by toggling offset (force useEffect re-run via dep).
      setOffset((o) => o);
      // No state change; force reload by incrementing via a tick. We just re-render.
      // Simpler: navigate to same path forces reload via key trick — use a re-key
      // by navigating to self with a query string. But simpler is to just reload
      // via setting offset briefly.
      setOffset((o) => o);
    } catch (e) { showToast('err', e.message); }
  };

  return html`
    <nav class="breadcrumb">
      <a href="/admin/users" onClick=${(e) => { e.preventDefault(); navigate('/admin/users'); }}>用户管理</a>
    </nav>
    <div class="page-header">
      <div class="page-header-content">
        <h1 class="page-title">用户管理</h1>
        <p class="page-description">查看所有用户，提升管理员权限。</p>
      </div>
    </div>

    <div class="table-wrap">
      <table class="table">
        <thead>
          <tr><th>邮箱</th><th>用户 ID</th><th>角色</th><th>创建时间</th><th></th></tr>
        </thead>
        <tbody>
          ${users.length === 0
            ? html`<tr><td colspan="5"><div class="table-empty">还没有用户</div></td></tr>`
            : users.map((u) => html`
              <tr key=${u.id}>
                <td data-label="邮箱">${u.email}</td>
                <td data-label="用户 ID" class="mono" title=${u.id}><code>${u.id.slice(0, 8)}…</code></td>
                <td data-label="角色">${u.is_admin
                  ? html`<span class="badge badge-primary">管理员</span>`
                  : html`<span class="badge">普通用户</span>`}</td>
                <td data-label="创建时间" class="muted">${fmtTime(u.created_at)}</td>
                <td class="table-actions">
                  ${u.is_admin
                    ? html`<span class="loading-text">已是管理员</span>`
                    : html`<button class="btn btn-sm" onClick=${() => promote(u.id)}>提升为管理员</button>`}
                </td>
              </tr>
            `)}
        </tbody>
      </table>
    </div>

    <div class="pagination">
      <span class="pagination-info">第 ${page} 页</span>
      <div class="btn-row">
        <button class="btn btn-sm" disabled=${!hasPrev} onClick=${() => setOffset(Math.max(0, offset - limit))}>上一页</button>
        <button class="btn btn-sm" disabled=${!hasNext} onClick=${() => setOffset(offset + limit)}>下一页</button>
      </div>
    </div>
  `;
}

// ============================================================
// helpers
// ============================================================
function fmtTime(unix) {
  if (!unix) return '—';
  const d = new Date(unix * 1000);
  const yyyy = d.getFullYear();
  const mm = String(d.getMonth() + 1).padStart(2, '0');
  const dd = String(d.getDate()).padStart(2, '0');
  const hh = String(d.getHours()).padStart(2, '0');
  const mi = String(d.getMinutes()).padStart(2, '0');
  return `${yyyy}-${mm}-${dd} ${hh}:${mi}`;
}

function humanBytes(n) {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / 1024 / 1024).toFixed(2)} MB`;
}

// Note: removed unused div() helper — htm uses 'class' not 'className' for
// HTML elements, and `<div class="...">` is direct enough.

// ============================================================
// mount
// ============================================================
render(h(App), document.getElementById('app'));
