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
  invalid_multipart: '上传请求格式错误（缺少 multipart body）',
  missing_package: '未选择上传文件',
  invalid_package: '压缩包无法解析（不是合法的 tar.gz）',
  manifest_required: '压缩包缺少 manifest.yaml',
  manifest_too_large: 'manifest.yaml 超过大小限制',
  invalid_manifest: 'manifest.yaml 校验失败',
  version_mismatch: 'URL 中的版本号与 manifest.yaml 不一致',
  readme_required: '压缩包缺少 README.md',
  doc_read_failed: '文档读取失败',
  doc_too_large: '文档超过大小限制',
  icon_read_failed: 'icon.png 读取失败',
  icon_too_large: 'icon.png 超过大小限制',
  too_many_screenshots: '截图数量超过上限',
  screenshot_read_failed: '截图读取失败',
  screenshot_too_large: '截图超过大小限制',
  invalid_platform: '请求的平台不存在',
  version_exists: '该版本已存在（用 PUT 覆盖）',
  package_too_large: '压缩包超过大小限制',
  internal: '服务器内部错误，请稍后重试',
};

// ============================================================
// signals (global state) — automatic re-render via useSignals()
// ============================================================
const jwtSignal = signal(localStorage.getItem(LS_JWT) || null);
const refreshSignal = signal(localStorage.getItem(LS_REFRESH) || null);
const userSignal = computed(() => (jwtSignal.value ? decodeJwt(jwtSignal.value) : null));
const routeSignal = signal(parseLocation());
// routeTickSignal increments on every navigate() call. useApi subscribes to
// it so list pages automatically refetch when the user returns to them
// (e.g. AdminApps after editing one row, DevAppList after uploading a
// release). Without it, same-path navigates would show stale data because
// useApi's path string didn't change.
const routeTickSignal = signal(0);
const toastSignal = signal(null);        // { kind, text, id } | null
const menuOpenSignal = signal(false);    // user dropdown

// ============================================================
// routing
// ============================================================
function parseLocation() {
  const path = location.pathname || '/';
  return { path, segments: path.split('/').filter(Boolean), search: location.search || '' };
}
function navigate(to) {
  if (location.pathname + location.search !== to) {
    history.pushState({}, '', to);
  }
  routeSignal.value = parseLocation();
  routeTickSignal.value++;
}
window.addEventListener('popstate', () => {
  routeSignal.value = parseLocation();
  routeTickSignal.value++;
});

// Public marketplace paths — visible without login. Everything else
// (cfg-sync /me/*, /admin/*, /show-token/*) requires a valid JWT.
const PUBLIC_PATHS = ['/', '/apps'];
function isPublicPath(path) {
  if (path === '/login' || path === '/register') return false;
  for (const p of PUBLIC_PATHS) {
    if (path === p) return true;
    if (path.startsWith(p + '/')) return true;
  }
  return false;
}

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

// uploadMultipart POSTs a multipart/form-data body with a single "package"
// field. Uses XMLHttpRequest because fetch() cannot expose per-write
// progress on the upload side. onProgress receives a 0..1 ratio (null when
// the server is processing after the upload finishes).
//
// Resolves with the parsed JSON body on 2xx, rejects with an Error decorated
// with .status / .code / .body — same shape as call() so callers can use the
// same ERR_MSGS lookup path.
function uploadMultipart(path, file, onProgress) {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open('POST', path);
    const t = jwtSignal.value;
    if (t) xhr.setRequestHeader('Authorization', `Bearer ${t}`);
    if (onProgress) {
      xhr.upload.onprogress = (e) => {
        if (e.lengthComputable) onProgress(e.loaded / e.total);
      };
      xhr.upload.onload = () => onProgress(null);  // server processing
    }
    xhr.onload = () => {
      let data = null;
      try { data = xhr.responseText ? JSON.parse(xhr.responseText) : null; } catch {}
      if (xhr.status >= 200 && xhr.status < 300) {
        resolve(data);
        return;
      }
      const code = data?.error || `http_${xhr.status}`;
      const err = new Error(ERR_MSGS[code] || code);
      err.status = xhr.status;
      err.code = code;
      err.body = data;
      reject(err);
    };
    xhr.onerror = () => {
      const err = new Error('网络错误，上传失败');
      err.status = 0;
      err.code = 'network';
      reject(err);
    };
    xhr.onabort = () => {
      const err = new Error('已取消上传');
      err.status = 0;
      err.code = 'aborted';
      reject(err);
    };
    const fd = new FormData();
    fd.append('package', file);
    xhr.send(fd);
  });
}

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
  // Subscribes to routeTickSignal so list pages refetch automatically
  // when the user navigates back to them (see navigate()).
  const [data, setData] = useState(null);
  const [err, setErr] = useState(null);
  const [loading, setLoading] = useState(true);
  const [tick, setTick] = useState(0);
  const routeTick = routeTickSignal.value;
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
  }, [method, path, ...deps, tick, routeTick]);
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

  // Auto-redirect: not logged in + protected path -> /login,
  // non-admin hitting /admin -> /apps, expired session -> clear + redirect,
  // already-logged-in hitting /login or /register -> /apps.
  useEffect(() => {
    if (jwt && isExpired(user)) {
      showToast('err', '会话已过期，请重新登录');
      clearLocalSession();
      if (!isPublicPath(path)) navigate('/login');
      return;
    }
    if (jwt && (path === '/login' || path === '/register')) {
      navigate('/apps');
      return;
    }
    if (!jwt && !isPublicPath(path) && path !== '/login' && path !== '/register') {
      navigate('/login');
      return;
    }
    if (jwt && segments[0] === 'admin' && !(user && user.is_admin)) {
      showToast('err', '需要管理员权限');
      navigate('/apps');
    }
    if (jwt && segments[0] === 'dev' && !(user && user.is_admin)) {
      showToast('err', '只有管理员可以发布应用');
      navigate('/apps');
    }
  }, [jwt, path, user]);

  // Close user menu on route change.
  useEffect(() => { menuOpenSignal.value = false; }, [path]);

  if (!jwt && (path === '/login' || path === '/register')) {
    return html`<${AuthPage} mode=${path === '/register' ? 'register' : 'login'} />`;
  }
  // Unauthenticated + protected path: a useEffect above will redirect to
  // /login; meanwhile render Loading so we don't flash a 401-shaped page.
  if (!jwt && !isPublicPath(path)) return html`<${Loading} />`;

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
  // Public marketplace routes (visible without login).
  if (path === '/') return html`<${CatalogHome} />`;
  if (path === '/apps') return html`<${CatalogList} />`;
  if (segments[0] === 'apps' && segments[1] && segments[2] === 'v' && segments[3])
    return html`<${CatalogReleaseDetail} appId=${segments[1]} version=${decodeURIComponent(segments[3])} />`;
  if (segments[0] === 'apps' && segments[1])
    return html`<${CatalogAppDetail} appId=${segments[1]} />`;

  // Developer release-management routes (admin only — NavDevLinks only
  // shows for admins, and the backend endpoints are behind AdminMW).
  if (path === '/dev/apps') return html`<${DevAppList} />`;
  if (segments[0] === 'dev' && segments[1] === 'apps' && segments[2] && segments[3] === 'releases')
    return html`<${DevReleaseList} appId=${segments[2]} />`;
  if (segments[0] === 'dev' && segments[1] === 'apps' && segments[2] && segments[3] === 'new')
    return html`<${DevUploadForm} appId=${segments[2]} />`;

  // Authenticated cfg-sync routes. Formerly /apps/* — relocated under
  // /me/apps/* to free the /apps namespace for the public marketplace.
  if (path === '/me/apps') return html`<${MyApps} />`;
  if (segments[0] === 'me' && segments[1] === 'apps' && segments[2])
    return html`<${AppDetail} appId=${segments[2]} />`;
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

  const isActive = (p) => path === p || (p !== '/' && path.startsWith(p));
  const initial = (user && user.email) ? user.email[0].toUpperCase() : '?';

  return html`
    <nav class="nav" aria-label="primary">
      <a class="nav-brand" href="/"
         onClick=${(e) => { e.preventDefault(); navigate('/'); }}>
        <span class="nav-brand-mark">cf</span>
        cfgsync
      </a>
      <div class="nav-right">
        <a class="nav-link ${isActive('/apps') ? 'active' : ''}"
           href="/apps"
           onClick=${(e) => { e.preventDefault(); navigate('/apps'); }}>
          <span>应用市场</span>
        </a>
        ${user ? html`
          <a class="nav-link ${isActive('/me/apps') ? 'active' : ''}"
             href="/me/apps"
             onClick=${(e) => { e.preventDefault(); navigate('/me/apps'); }}>
            <span>我的配置</span>
          </a>
          <a class="nav-link ${isActive('/me') ? 'active' : ''}"
             href="/me"
             onClick=${(e) => { e.preventDefault(); navigate('/me'); }}>
            <span>配额</span>
          </a>
          ${isAdmin ? html`<${NavAdminLinks} isActive=${isActive} />` : null}
          ${isAdmin ? html`<${NavDevLinks} isActive=${isActive} />` : null}
          <span class="nav-divider"></span>
          <button ref=${ref} class="nav-user-btn"
                  onClick=${(e) => { e.stopPropagation(); menuOpenSignal.value = !menuOpen; }}
                  aria-haspopup="menu" aria-expanded=${menuOpen}>
            <span class="nav-user-btn-avatar">${initial}</span>
            <span>${user.email}</span>
            <span aria-hidden="true">▾</span>
          </button>
          ${menuOpen ? html`<${NavUserMenu} user=${user} onNavigate=${() => navigate('/me/settings')} />` : null}
        ` : html`
          <a class="nav-link ${isActive('/login') ? 'active' : ''}"
             href="/login"
             onClick=${(e) => { e.preventDefault(); navigate('/login'); }}>
            <span>登录</span>
          </a>
        `}
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

// NavDevLinks — admin-only entry to release management. Sibling of
// NavAdminLinks, kept separate so htm doesn't have to parse
// `${A && html\`...\`}` across complex children.
function NavDevLinks({ isActive }) {
  return html`
    <a class="nav-link ${isActive('/dev/apps') ? 'active' : ''}"
       href="/dev/apps"
       onClick=${(e) => { e.preventDefault(); navigate('/dev/apps'); }}>我的发布</a>
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
          <a class="btn" href="/"
             onClick=${(e) => { e.preventDefault(); navigate('/'); }}>返回首页</a>
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
      <a href="/me/apps" onClick=${(e) => { e.preventDefault(); navigate('/me/apps'); }}>我的应用</a>
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
               onClick=${(e) => { e.preventDefault(); navigate('/me/apps/' + a.app_id); }}
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
      <a href="/me/apps" onClick=${(e) => { e.preventDefault(); navigate('/me/apps'); }}>我的应用</a>
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
        <a href="/me/apps/${appId}" onClick=${(e) => { e.preventDefault(); navigate('/me/apps/' + appId); }}>${appId}</a>
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
           onClick=${(e) => { e.preventDefault(); navigate('/me/apps/' + appId); }}>我已保存</a>
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
  const { data, err, loading, reload } = useApi('GET', path, [offset]);

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
      reload();
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
// Catalog (public marketplace) — visible without login
// ============================================================

function CatalogCard({ app }) {
  return html`
    <a class="catalog-card" href="/apps/${app.app_id}"
       onClick=${(e) => { e.preventDefault(); navigate('/apps/' + app.app_id); }}>
      <div class="catalog-card-icon">
        ${app.icon_url
          ? html`<img src=${app.icon_url} alt="" loading="lazy" />`
          : html`<span class="catalog-card-icon-fallback">${(app.display_name || '?')[0].toUpperCase()}</span>`}
      </div>
      <div class="catalog-card-body">
        <div class="catalog-card-title">${app.display_name}</div>
        <div class="catalog-card-summary">${app.summary || html`<span class="muted">（无简介）</span>`}</div>
        <div class="catalog-card-meta">
          ${app.latest_version ? html`<span class="badge">v${app.latest_version}</span>` : null}
          ${(app.tags || []).slice(0, 3).map((t) => html`<span class="tag-chip-sm">${t}</span>`)}
        </div>
      </div>
    </a>
  `;
}

function Pagination({ page, size, total, onPage }) {
  const pages = Math.max(1, Math.ceil(total / size));
  if (pages <= 1) return null;
  return html`
    <div class="pagination">
      <button class="btn btn-sm" disabled=${page <= 1} onClick=${() => onPage(page - 1)}>上一页</button>
      <span class="pagination-info">${page} / ${pages}（共 ${total}）</span>
      <button class="btn btn-sm" disabled=${page >= pages} onClick=${() => onPage(page + 1)}>下一页</button>
    </div>
  `;
}

function CatalogHome() {
  const { data, err, loading } = useApi('GET', '/api/v1/catalog/apps?size=12', []);
  const { data: tagsData } = useApi('GET', '/api/v1/catalog/tags', []);
  if (err) return html`<${ErrorBox} err=${err} />`;
  if (loading) return html`<div class="loading-text">加载中…</div>`;
  const apps = data?.apps || [];
  const tags = tagsData?.tags || [];
  return html`
    <div class="page-header">
      <div class="page-header-content">
        <h1 class="page-title">应用市场</h1>
        <p class="page-description">浏览、搜索、下载 cfgsync 上发布的应用。</p>
      </div>
    </div>
    ${tags.length > 0 ? html`
      <div class="card" style="margin-bottom:16px">
        <h3 style="margin:0 0 8px">热门标签</h3>
        <div class="tag-cloud">
          ${tags.slice(0, 20).map((t) => html`
            <a class="tag-chip" href="/apps?tag=${encodeURIComponent(t.tag)}"
               onClick=${(e) => { e.preventDefault(); navigate('/apps?tag=' + encodeURIComponent(t.tag)); }}>
              ${t.tag}<span class="tag-count">${t.count}</span>
            </a>
          `)}
        </div>
      </div>
    ` : null}
    <h2 class="section-title">最新发布</h2>
    ${apps.length === 0
      ? html`<div class="empty">
          <div class="empty-icon">○</div>
          <div class="empty-title">暂无应用</div>
          <div class="empty-desc">等开发者上传第一个应用后会出现在这里。</div>
        </div>`
      : html`<div class="catalog-grid">
          ${apps.map((a) => html`<${CatalogCard} key=${a.app_id} app=${a} />`)}
        </div>`}
  `;
}

function CatalogList() {
  const params = new URLSearchParams(location.search);
  const initialTag = params.get('tag') || '';
  const initialQ = params.get('q') || '';
  const initialPage = parseInt(params.get('page') || '1', 10) || 1;
  const [tag, setTag] = useState(initialTag);
  const [q, setQ] = useState(initialQ);
  const [page, setPage] = useState(initialPage);
  const [searchInput, setSearchInput] = useState(initialQ);

  // Sync state from URL whenever the route changes (user clicks a tag chip
  // in CatalogHome or pagination elsewhere that navigates with ?tag=/ ?q=).
  // Without this, useState initial* values would persist across same-path
  // navigations and the filter UI would show stale state.
  const routeSearch = routeSignal.value.search;
  useEffect(() => {
    const p = new URLSearchParams(routeSearch);
    setTag(p.get('tag') || '');
    setQ(p.get('q') || '');
    setPage(parseInt(p.get('page') || '1', 10) || 1);
    setSearchInput(p.get('q') || '');
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [routeSearch]);

  let query = `/api/v1/catalog/apps?page=${page}&size=20`;
  if (tag) query += `&tag=${encodeURIComponent(tag)}`;
  if (q) query += `&q=${encodeURIComponent(q)}`;

  const { data, err, loading } = useApi('GET', query, [tag, q, page]);
  const { data: tagsData } = useApi('GET', '/api/v1/catalog/tags', []);

  const submitSearch = (e) => { e.preventDefault(); setQ(searchInput); setPage(1); };
  const clearFilters = () => { setTag(''); setQ(''); setSearchInput(''); setPage(1); };

  return html`
    <nav class="breadcrumb">
      <a href="/" onClick=${(e) => { e.preventDefault(); navigate('/'); }}>应用市场</a>
      <span class="breadcrumb-sep">/</span>
      <span class="breadcrumb-current">所有应用</span>
    </nav>
    <div class="catalog-layout">
      <aside class="catalog-sidebar">
        <div class="card">
          <h3 style="margin:0 0 8px">标签</h3>
          <div class="catalog-tags-list">
            <button class="tag-chip ${tag === '' ? 'active' : ''}"
                    onClick=${() => { setTag(''); setPage(1); }}>全部</button>
            ${(tagsData?.tags || []).map((t) => html`
              <button class="tag-chip ${tag === t.tag ? 'active' : ''}"
                      onClick=${() => { setTag(t.tag); setPage(1); }}>
                ${t.tag}<span class="tag-count">${t.count}</span>
              </button>
            `)}
          </div>
        </div>
      </aside>
      <div class="catalog-main">
        <form class="catalog-search" onSubmit=${submitSearch}>
          <input type="text" placeholder="搜索应用名称或描述…"
                 value=${searchInput}
                 onInput=${(e) => setSearchInput(e.target.value)} />
          <button class="btn btn-primary" type="submit">搜索</button>
          ${(tag || q) ? html`<button class="btn" type="button" onClick=${clearFilters}>清除</button>` : null}
          ${tag ? html`<span class="catalog-filter-hint">按标签 <code>${tag}</code> 过滤</span>` : null}
        </form>
        ${err ? html`<${ErrorBox} err=${err} />` :
          loading ? html`<div class="loading-text">加载中…</div>` :
          (data?.apps?.length || 0) === 0 ? html`
            <div class="empty">
              <div class="empty-icon">○</div>
              <div class="empty-title">未找到应用</div>
              <div class="empty-desc">${tag || q ? '换个标签或关键字试试。' : '还没有应用被发布。'}</div>
            </div>
          ` : html`
            <div class="catalog-grid">
              ${(data.apps || []).map((a) => html`<${CatalogCard} key=${a.app_id} app=${a} />`)}
            </div>
            <${Pagination} page=${data.page} size=${data.size} total=${data.total} onPage=${setPage} />
          `}
      </div>
    </div>
  `;
}

function CatalogDocSection({ appId, version, name, title, defaultExpanded }) {
  const [content, setContent] = useState(null);
  const [expanded, setExpanded] = useState(defaultExpanded !== false);
  const [err, setErr] = useState(null);
  useEffect(() => {
    if (!version || !expanded) return;
    setContent(null); setErr(null);
    fetch(`/api/v1/catalog/apps/${appId}/releases/${version}/docs/${name}/rendered`)
      .then((r) => r.ok ? r.text() : Promise.reject(new Error('加载失败')))
      .then(setContent)
      .catch((e) => setErr(e.message));
  }, [appId, version, name, expanded]);
  if (!version) return null;
  return html`
    <div class="card catalog-doc-card">
      <div class="catalog-doc-header" onClick=${() => setExpanded(!expanded)}>
        <h3 style="margin:0">${title}</h3>
        <span class="catalog-doc-toggle" aria-hidden="true">${expanded ? '−' : '+'}</span>
      </div>
      ${expanded ? html`
        ${err ? html`<div class="notice notice-danger"><span class="notice-icon">!</span>${err}</div>` :
          content === null ? html`<div class="loading-text">加载中…</div>` :
          html`<div class="catalog-doc-body markdown" dangerouslySetInnerHTML=${{ __html: content }} />`}
      ` : null}
    </div>
  `;
}

function CatalogReleasesList({ appId }) {
  const { data, err, loading } = useApi('GET', `/api/v1/catalog/apps/${appId}/releases`, [appId]);
  if (err) return html`<${ErrorBox} err=${err} />`;
  if (loading) return html`<div class="loading-text">加载中…</div>`;
  const releases = data?.releases || [];
  if (releases.length === 0) return null;
  return html`
    <div class="card">
      <h3 style="margin:0 0 8px">版本历史</h3>
      <div class="table-wrap">
        <table class="table">
          <thead><tr><th>版本</th><th>大小</th><th>发布时间</th><th></th></tr></thead>
          <tbody>
            ${releases.map((r) => html`
              <tr key=${r.version}>
                <td><strong>v${r.version}</strong></td>
                <td>${humanBytes(r.package_size)}</td>
                <td class="muted">${fmtTime(r.created_at)}</td>
                <td>
                  <a class="btn btn-sm" href="/apps/${appId}/v/${r.version}"
                     onClick=${(e) => { e.preventDefault(); navigate('/apps/' + appId + '/v/' + r.version); }}>详情</a>
                </td>
              </tr>
            `)}
          </tbody>
        </table>
      </div>
    </div>
  `;
}

function CatalogAppDetail({ appId }) {
  const { data, err, loading } = useApi('GET', `/api/v1/catalog/apps/${appId}`, [appId]);
  if (err) return html`<${ErrorBox} err=${err} />`;
  if (loading) return html`<div class="loading-text">加载中…</div>`;
  const latest = data?.latest_release;
  const tags = data?.tags || [];
  return html`
    <nav class="breadcrumb">
      <a href="/" onClick=${(e) => { e.preventDefault(); navigate('/'); }}>应用市场</a>
      <span class="breadcrumb-sep">/</span>
      <a href="/apps" onClick=${(e) => { e.preventDefault(); navigate('/apps'); }}>所有应用</a>
      <span class="breadcrumb-sep">/</span>
      <span class="breadcrumb-current">${data?.display_name || appId}</span>
    </nav>
    <div class="catalog-detail-header">
      ${data?.icon_url
        ? html`<img class="catalog-detail-icon" src=${data.icon_url} alt="" />`
        : html`<div class="catalog-detail-icon fallback">${(data?.display_name || '?')[0].toUpperCase()}</div>`}
      <div class="catalog-detail-meta">
        <h1>${data?.display_name || appId}</h1>
        ${data?.summary ? html`<p class="catalog-detail-summary">${data.summary}</p>` : null}
        ${tags.length > 0 ? html`
          <div class="catalog-detail-tags">
            ${tags.map((t) => html`<span class="tag-chip">${t}</span>`)}
          </div>
        ` : null}
        <div class="catalog-detail-info">
          ${data?.license ? html`<span class="info-row"><strong>License:</strong> ${data.license}</span>` : null}
          ${data?.homepage ? html`<span class="info-row"><strong>主页:</strong> <a href=${data.homepage} target="_blank" rel="noopener">${data.homepage}</a></span>` : null}
          ${data?.author?.name ? html`<span class="info-row"><strong>作者:</strong> ${data.author.name}</span>` : null}
        </div>
        ${latest ? html`
          <div class="catalog-detail-download">
            <span class="muted">最新版本 <strong>v${latest.version}</strong> · ${fmtTime(latest.created_at)}</span>
            <a class="btn btn-primary" href=${latest.download_url}>下载包</a>
            <a class="btn" href="/apps/${appId}/v/${latest.version}"
               onClick=${(e) => { e.preventDefault(); navigate('/apps/' + appId + '/v/' + latest.version); }}>版本详情</a>
          </div>
        ` : html`<div class="notice">暂无可下载的版本</div>`}
      </div>
    </div>
    ${latest ? html`<${CatalogDocSection} appId=${appId} version=${latest.version} name="README.md" title="应用介绍" defaultExpanded=${true} />` : null}
    ${latest ? html`<${CatalogDocSection} appId=${appId} version=${latest.version} name="INSTALL.md" title="安装说明" />` : null}
    ${latest ? html`<${CatalogDocSection} appId=${appId} version=${latest.version} name="USAGE.md" title="使用说明" />` : null}
    <${CatalogReleasesList} appId=${appId} />
  `;
}

function CatalogReleaseDetail({ appId, version }) {
  const { data, err, loading } = useApi('GET', `/api/v1/catalog/apps/${appId}/releases/${version}`, [appId, version]);
  if (err) return html`<${ErrorBox} err=${err} />`;
  if (loading) return html`<div class="loading-text">加载中…</div>`;
  const platforms = data?.platforms || [];
  return html`
    <nav class="breadcrumb">
      <a href="/" onClick=${(e) => { e.preventDefault(); navigate('/'); }}>应用市场</a>
      <span class="breadcrumb-sep">/</span>
      <a href="/apps/${appId}" onClick=${(e) => { e.preventDefault(); navigate('/apps/' + appId); }}>${appId}</a>
      <span class="breadcrumb-sep">/</span>
      <span class="breadcrumb-current">v${version}</span>
    </nav>
    <div class="page-header">
      <div class="page-header-content">
        <h1 class="page-title">${appId} <span class="muted">— v${version}</span></h1>
        <p class="page-description">发布于 ${fmtTime(data?.created_at)} · ${humanBytes(data?.package_size || 0)}</p>
      </div>
    </div>
    <div class="card">
      <h3 style="margin:0 0 12px">下载</h3>
      <div class="catalog-download-options">
        <a class="btn btn-primary" href=${data?.download_url}>完整包 (.tar.gz)</a>
        ${platforms.length > 0 ? html`<span class="catalog-download-or">或选择平台：</span>` : null}
        ${platforms.map((p) => html`
          <a class="btn" key=${p} href="${data?.download_url}?platform=${p}">${p}</a>
        `)}
      </div>
      ${data?.package_sha256 ? html`
        <div class="catalog-sha">
          <strong>全包 SHA256:</strong> <code>${data.package_sha256}</code>
          <div class="muted" style="font-size:12px;margin-top:4px">
            平台二进制单独下载时不附 sha256；如需校验请下载完整包后用此值验证。
          </div>
        </div>
      ` : null}
    </div>
    <${CatalogDocSection} appId=${appId} version=${version} name="CHANGELOG.md" title="版本说明" defaultExpanded=${true} />
    <${CatalogDocSection} appId=${appId} version=${version} name="INSTALL.md" title="安装说明" />
  `;
}

// ============================================================
// Dev: release management (admin only)
//
// Routes (mirrors spec §11):
//   /dev/apps                                  list apps owned/managed by admin
//   /dev/apps/{app_id}/releases                release history + delete
//   /dev/apps/{app_id}/new                     drag-drop .tar.gz upload + progress
//
// Single-admin mode (design decision 1): the same admin who registers app_id
// is also the only publisher, so /dev/apps simply re-uses GET /admin/apps.
// ============================================================

// shortSha truncates a 64-char hex SHA to 12 chars for table display.
function shortSha(s) { return s ? s.slice(0, 12) : '—'; }

// formatValidationError turns an invalid_manifest response body into a
// readable list. Server sends { error:"invalid_manifest", fields:[{path,msg}] }.
function formatValidationError(body) {
  if (!body) return null;
  if (body.error !== 'invalid_manifest') return null;
  const fields = body.fields || [];
  if (fields.length === 0) return 'manifest.yaml 校验失败';
  return html`<ul class="manifest-err-list">
    ${fields.map((f) => html`<li><code>${f.path || 'manifest'}</code> — ${f.msg}</li>`)}
  </ul>`;
}

function DevAppList() {
  const { data, err, loading } = useApi('GET', '/api/v1/admin/apps');
  if (err) return html`<${ErrorBox} err=${err} />`;
  if (loading) return html`<div class="loading-text">加载中…</div>`;
  const apps = data?.apps || [];
  return html`
    <nav class="breadcrumb">
      <a href="/dev/apps" onClick=${(e) => { e.preventDefault(); navigate('/dev/apps'); }}>我的发布</a>
    </nav>
    <div class="page-header">
      <div class="page-header-content">
        <h1 class="page-title">我的发布</h1>
        <p class="page-description">为已注册的 app 上传新版本、查看发布历史。</p>
      </div>
    </div>

    ${apps.length === 0
      ? html`
        <div class="empty">
          <div class="empty-icon">+</div>
          <div class="empty-title">还没有可发布的 app</div>
          <div class="empty-desc">先到「应用管理」注册一个 app_id，然后回到这里上传版本。</div>
          <div class="empty-cta">
            <button class="btn btn-primary" onClick=${() => navigate('/admin/apps')}>去注册 app</button>
          </div>
        </div>
      `
      : html`
        <div class="table-wrap">
          <table class="table">
            <thead>
              <tr><th>app_id</th><th>显示名</th><th>最新版本</th><th>创建时间</th><th></th></tr>
            </thead>
            <tbody>
              ${apps.map((a) => html`
                <tr key=${a.app_id}>
                  <td><code>${a.app_id}</code></td>
                  <td>${a.display_name}</td>
                  <td>${a.latest_version ? html`<span class="badge">v${a.latest_version}</span>` : html`<span class="muted">—</span>`}</td>
                  <td class="muted">${fmtTime(a.created_at)}</td>
                  <td class="table-actions">
                    <a class="btn btn-sm btn-primary" href=${'/dev/apps/' + a.app_id + '/new'}
                       onClick=${(e) => { e.preventDefault(); navigate('/dev/apps/' + a.app_id + '/new'); }}>上传版本</a>
                    <a class="btn btn-sm" href=${'/dev/apps/' + a.app_id + '/releases'}
                       onClick=${(e) => { e.preventDefault(); navigate('/dev/apps/' + a.app_id + '/releases'); }}>发布历史</a>
                  </td>
                </tr>
              `)}
            </tbody>
          </table>
        </div>
      `}
  `;
}

function DevReleaseList({ appId }) {
  const { data, err, loading, reload } = useApi('GET', `/api/v1/dev/apps/${appId}/releases`, [appId]);
  const [busyVer, setBusyVer] = useState(null);
  const [confirmVer, setConfirmVer] = useState(null);

  const del = async (version) => {
    setBusyVer(version); setConfirmVer(null);
    try {
      await call('DELETE', `/api/v1/dev/apps/${appId}/releases/${version}`);
      showToast('ok', `已删除 v${version}`);
      reload();
    } catch (e) {
      showToast('err', e.message);
    } finally { setBusyVer(null); }
  };

  if (err) return html`<${ErrorBox} err=${err} />`;
  if (loading) return html`<div class="loading-text">加载中…</div>`;
  const releases = data?.releases || [];
  return html`
    <nav class="breadcrumb">
      <a href="/dev/apps" onClick=${(e) => { e.preventDefault(); navigate('/dev/apps'); }}>我的发布</a>
      <span class="breadcrumb-sep">/</span>
      <span class="breadcrumb-current">${appId}</span>
    </nav>
    <div class="page-header">
      <div class="page-header-content">
        <h1 class="page-title">发布历史 <span class="muted" style="font-weight:normal">— ${appId}</span></h1>
        <p class="page-description">已上传的所有版本，按 semver 倒序排列。删除操作不可恢复（但可重新上传）。</p>
      </div>
      <button class="btn btn-primary" onClick=${() => navigate('/dev/apps/' + appId + '/new')}>+ 上传新版本</button>
    </div>

    ${releases.length === 0
      ? html`
        <div class="empty">
          <div class="empty-icon">○</div>
          <div class="empty-title">还没有任何版本</div>
          <div class="empty-desc">第一个版本将作为 latest_version。</div>
          <div class="empty-cta">
            <button class="btn btn-primary" onClick=${() => navigate('/dev/apps/' + appId + '/new')}>上传第一个版本</button>
          </div>
        </div>
      `
      : html`
        <div class="table-wrap">
          <table class="table">
            <thead>
              <tr><th>版本</th><th>大小</th><th>SHA256</th><th>发布时间</th><th>发布者</th><th></th></tr>
            </thead>
            <tbody>
              ${releases.map((r) => html`
                <tr key=${r.version}>
                  <td><strong>v${r.version}</strong></td>
                  <td>${humanBytes(r.package_size)}</td>
                  <td><code class="mono-sm">${shortSha(r.package_sha256)}</code></td>
                  <td class="muted">${fmtTime(r.created_at)}</td>
                  <td class="muted">${r.created_by || '—'}</td>
                  <td class="table-actions">
                    ${confirmVer === r.version
                      ? html`<button class="btn btn-sm btn-danger" disabled=${busyVer === r.version}
                              onClick=${() => del(r.version)}>${busyVer === r.version ? '删除中…' : '确认删除'}</button>
                          <button class="btn btn-sm" disabled=${busyVer === r.version}
                              onClick=${() => setConfirmVer(null)}>取消</button>`
                      : html`<a class="btn btn-sm" href="/apps/${appId}/v/${r.version}"
                              onClick=${(e) => { e.preventDefault(); navigate('/apps/' + appId + '/v/' + r.version); }}>查看</a>
                          <button class="btn btn-sm btn-danger" onClick=${() => setConfirmVer(r.version)}>删除</button>`
                    }
                  </td>
                </tr>
              `)}
            </tbody>
          </table>
        </div>
      `}
  `;
}

// DevUploadForm — drag-drop .tar.gz + progress + error display.
//
// The actual upload goes through uploadMultipart (XHR) so we can show a
// live progress bar. call() cannot do that — fetch() lacks per-write
// progress on the request body.
//
// Error handling distinguishes:
//   - network/aborted: show a generic notice
//   - server side: use ERR_MSGS; if invalid_manifest, also render the
//     field-level details returned by the server
function DevUploadForm({ appId }) {
  const [file, setFile] = useState(null);
  const [dragOver, setDragOver] = useState(false);
  // uploadPhase: 'idle' | 'uploading' | 'processing' (server churn after bytes received)
  // 'uploading' is paired with progress 0..1; 'processing' shows a full bar with
  // "服务器处理中…" because the server is unpacking / validating the tarball.
  // Splitting these as two states avoids the previous bug where onProgress(null)
  // collapsed both "idle" and "processing" into the same code path.
  const [uploadPhase, setUploadPhase] = useState('idle');
  const [progress, setProgress] = useState(0);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState(null);
  const inputRef = useRef(null);

  const onPick = (f) => {
    if (!f) return;
    if (!/\.tar\.gz$/i.test(f.name) && f.type !== 'application/gzip') {
      setErr(new Error('请选择 .tar.gz 文件'));
      return;
    }
    setFile(f); setErr(null);
  };

  const submit = async () => {
    if (!file) return;
    setBusy(true); setErr(null); setProgress(0); setUploadPhase('uploading');
    try {
      await uploadMultipart(`/api/v1/dev/apps/${appId}/releases`, file, (ratio) => {
        if (ratio === null) {
          // upload.upload.onload fired — bytes are on the server, waiting for
          // tar extraction + manifest validation + DB write.
          setUploadPhase('processing');
        } else {
          setProgress(ratio);
        }
      });
      showToast('ok', '上传成功');
      navigate('/dev/apps/' + appId + '/releases');
    } catch (e) {
      setErr(e);
      setUploadPhase('idle');
    } finally { setBusy(false); }
  };

  const onDrop = (e) => {
    e.preventDefault();
    setDragOver(false);
    const f = e.dataTransfer.files && e.dataTransfer.files[0];
    if (f) onPick(f);
  };

  const showProgress = uploadPhase !== 'idle';
  const progressPct = uploadPhase === 'processing' ? 100 : Math.round(progress * 100);

  return html`
    <nav class="breadcrumb">
      <a href="/dev/apps" onClick=${(e) => { e.preventDefault(); navigate('/dev/apps'); }}>我的发布</a>
      <span class="breadcrumb-sep">/</span>
      <a href=${'/dev/apps/' + appId + '/releases'}
         onClick=${(e) => { e.preventDefault(); navigate('/dev/apps/' + appId + '/releases'); }}>${appId}</a>
      <span class="breadcrumb-sep">/</span>
      <span class="breadcrumb-current">上传新版本</span>
    </nav>
    <div class="page-header">
      <div class="page-header-content">
        <h1 class="page-title">上传新版本 <span class="muted" style="font-weight:normal">— ${appId}</span></h1>
        <p class="page-description">拖入或选择 <code>.tar.gz</code> 压缩包。包内需含 <code>manifest.yaml</code> 和 <code>README.md</code>，可选 <code>icon.png</code> / <code>screenshots/</code> / <code>INSTALL.md</code> 等。</p>
      </div>
    </div>

    <div class="card">
      <div class=${'dropzone' + (dragOver ? ' dropzone-active' : '')}
           onDragOver=${(e) => { e.preventDefault(); setDragOver(true); }}
           onDragLeave=${() => setDragOver(false)}
           onDrop=${onDrop}
           onClick=${() => inputRef.current && inputRef.current.click()}>
        <input ref=${inputRef} type="file" accept=".tar.gz,application/gzip"
               style="display:none"
               onChange=${(e) => onPick(e.target.files && e.target.files[0])} />
        ${file
          ? html`<div class="dropzone-file">
              <span class="dropzone-file-name">${file.name}</span>
              <span class="muted">${humanBytes(file.size)}</span>
            </div>`
          : html`<div class="dropzone-hint">
              <div class="dropzone-hint-icon">⤴</div>
              <div>点击选择文件，或拖拽 <code>.tar.gz</code> 到这里</div>
            </div>`}
      </div>

      ${showProgress ? html`
        <div class="progress" style="margin-top:16px"
             role="progressbar" aria-valuenow=${progressPct} aria-valuemin="0" aria-valuemax="100">
          <div class="progress-bar" style=${{ width: progressPct + '%' }}></div>
          <span class="progress-label">${uploadPhase === 'processing' ? '服务器处理中…' : progressPct + '%'}</span>
        </div>
      ` : null}

      ${err ? html`
        <div class="notice notice-danger" style="margin-top:16px">
          <span class="notice-icon">!</span>
          <div>
            <div><strong>${err.message}</strong></div>
            ${formatValidationError(err.body) || (err.body?.version ? html`<div class="muted" style="margin-top:4px">冲突版本：<code>v${err.body.version}</code></div>` : null)}
          </div>
        </div>
      ` : null}

      <div class="form-actions" style="margin-top:16px">
        <button class="btn btn-primary" disabled=${!file || busy} onClick=${submit}>
          ${busy ? '上传中…' : '开始上传'}
        </button>
        <a class="btn" href=${'/dev/apps/' + appId + '/releases'}
           onClick=${(e) => { e.preventDefault(); navigate('/dev/apps/' + appId + '/releases'); }}>取消</a>
      </div>
    </div>

    <div class="card" style="margin-top:16px">
      <h3 style="margin:0 0 8px">包结构要求</h3>
      <ul class="hint-list">
        <li><code>manifest.yaml</code> — 必需，schema_version=1，version 字段决定 release 版本号。</li>
        <li><code>README.md</code> — 必需，最少一段简介。</li>
        <li><code>INSTALL.md</code> / <code>USAGE.md</code> / <code>CHANGELOG.md</code> — 可选，前端按需展示。</li>
        <li><code>icon.png</code> — 可选，建议 256×256。</li>
        <li><code>screenshots/</code> — 可选截图目录，单图 ≤ 1 MB，总数 ≤ 8。</li>
        <li><code>bin/...</code> 等可执行文件 — manifest.platforms.<i>os-arch</i>.path 指向。</li>
      </ul>
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
