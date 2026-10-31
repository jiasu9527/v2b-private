export type AnyRecord = Record<string, any>;

const AUTH_KEY = 'forest_admin_auth_data';
const ADMIN_USER_KEY = 'forest_admin_user_info';
const ADMIN_PATH_KEY = 'forest_admin_path';
const DEFAULT_ADMIN_PATH = 'localadmin';

export type TelegramMiniIdentity = { id: number; email: string; is_admin: number; is_staff: number };
export type TelegramMiniSession = { token: string; expires_at: number; user: TelegramMiniIdentity };
let telegramMiniActive = false;
let telegramMiniSession: TelegramMiniSession | null = null;

// Mini App credentials never share the ordinary administrator storage context.
export function activateTelegramMiniApp() { telegramMiniActive = true; telegramMiniSession = null; }
export function isTelegramMiniApp() { return telegramMiniActive; }
export function telegramMiniIdentity(value: unknown): TelegramMiniIdentity {
  const user = value as TelegramMiniIdentity;
  if (!user || !Number.isSafeInteger(user.id) || user.id <= 0 || typeof user.email !== 'string' ||
      ![0, 1].includes(user.is_admin) || ![0, 1].includes(user.is_staff) || !(user.is_admin || user.is_staff)) throw new Error('运维身份无效，请从 Bot 重新打开');
  return { id: user.id, email: user.email, is_admin: user.is_admin, is_staff: user.is_staff };
}
export function setTelegramMiniSession(value: TelegramMiniSession) {
  if (!telegramMiniActive || !value || typeof value.token !== 'string' || !value.token || /\s/.test(value.token) ||
      !Number.isSafeInteger(value.expires_at) || value.expires_at * 1000 <= Date.now()) throw new Error('小程序登录已过期，请从 Bot 重新打开');
  telegramMiniSession = { token: value.token, expires_at: value.expires_at, user: telegramMiniIdentity(value.user) };
}
export function invalidateTelegramMiniSession() {
  if (!telegramMiniActive) return;
  telegramMiniSession = null;
  window.dispatchEvent(new Event('forest-mini-auth-invalid'));
}
function requireMiniSession() {
  if (!telegramMiniActive || !telegramMiniSession || telegramMiniSession.expires_at * 1000 <= Date.now()) {
    invalidateTelegramMiniSession();
    throw new Error('小程序登录已过期或绑定已撤销，请从 Bot 重新打开');
  }
  return telegramMiniSession;
}
function prohibitMiniRequest() {
  if (telegramMiniActive) throw new Error('小程序仅可访问 AI 运维接口');
}
export function aiOpsRequestURL(path: string): string {
  if (!telegramMiniActive) return `/api/v1/${getAdminPath()}${path}`;
  if (!/^\/ai-ops\/[a-z0-9/-]+$/i.test(path) || path.includes('//')) throw new Error('小程序仅可访问 AI 运维接口');
  return `/api/v1/ai-ops-mini/${path.slice('/ai-ops/'.length)}`;
}
export function aiOpsRequestHeaders(): Record<string, string> {
  return { Authorization: telegramMiniActive ? `Bearer ${requireMiniSession().token}` : getAuth() };
}
function miniQuery(params: AnyRecord, options: { keepEmpty?: boolean } = {}) {
  const query = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (['auth_data', 'token', 'init_data'].includes(key)) throw new Error('小程序凭据不能放在请求参数中');
    appendValue(query, key, value, options);
  }
  return query.toString();
}
export async function authenticateTelegramMiniApp(initData: string, signal?: AbortSignal): Promise<TelegramMiniSession> {
  if (!telegramMiniActive || !initData) throw new Error('请从 Telegram Bot 打开 AI 运维小程序');
  const response = await fetch('/api/v1/ai-ops-mini/auth', {
    method: 'POST', credentials: 'omit', cache: 'no-store', signal,
    headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ init_data: initData }),
  });
  const result = await parseResponse(response);
  return result?.data;
}
export async function getTelegramMiniIdentity(signal?: AbortSignal): Promise<TelegramMiniIdentity> {
  const response = await fetch('/api/v1/ai-ops-mini/identity', { credentials: 'omit', cache: 'no-store', signal, headers: aiOpsRequestHeaders() });
  if (response.status === 403) invalidateTelegramMiniSession();
  return telegramMiniIdentity((await parseResponse(response))?.data);
}

export function getSettings(): AnyRecord {
  return (window as any).settings || {};
}

export function getAdminPath(): string {
  if (telegramMiniActive) return 'ai-ops-mini';
  const saved = normalizeAdminPath(localStorage.getItem(ADMIN_PATH_KEY));
  const fromSettings = normalizeAdminPath(getSettings().secure_path);
  const path = saved || fromSettings || pathAdminSegment() || DEFAULT_ADMIN_PATH;
  return String(path).replace(/^\/+|\/+$/g, '');
}

export function setAdminPath(path: string) {
  prohibitMiniRequest();
  localStorage.setItem(ADMIN_PATH_KEY, normalizeAdminPath(path) || DEFAULT_ADMIN_PATH);
}

export function pathAdminSegment(pathname = location.pathname): string {
  const parts = pathname.split('/').filter(Boolean);
  if (parts[0] === 'assets' && parts[1] === 'admin-new') {
    return DEFAULT_ADMIN_PATH;
  }
  return parts[0] || '';
}

function normalizeAdminPath(value: any): string {
  const path = String(value || '').replace(/^\/+|\/+$/g, '');
  if (!path || path === 'assets' || path === 'admin-new' || path.startsWith('assets/')) return '';
  return path;
}

export function getAuth(): string {
  if (telegramMiniActive) return telegramMiniSession?.token || '';
  return localStorage.getItem(AUTH_KEY) || '';
}

export function setAuth(token: string) {
  prohibitMiniRequest();
  localStorage.setItem(AUTH_KEY, token || '');
}

export function getAdminUserInfo(): AnyRecord {
  if (telegramMiniActive) return telegramMiniSession ? { ...telegramMiniSession.user } : {};
  try {
    return JSON.parse(localStorage.getItem(ADMIN_USER_KEY) || '{}') || {};
  } catch {
    return {};
  }
}

export function setAdminUserInfo(user: AnyRecord = {}) {
  prohibitMiniRequest();
  const email = String(user.email || user.mail || user.account || '').trim();
  const payload = { ...user, email };
  localStorage.setItem(ADMIN_USER_KEY, JSON.stringify(payload));
}

export function clearAdminUserInfo() {
  if (telegramMiniActive) { telegramMiniSession = null; return; }
  localStorage.removeItem(ADMIN_USER_KEY);
}

export function clearAuth() {
  if (telegramMiniActive) { telegramMiniSession = null; return; }
  localStorage.removeItem(AUTH_KEY);
  clearAdminUserInfo();
}

function appendValue(q: URLSearchParams, key: string, value: any, options: { keepEmpty?: boolean } = {}) {
  if (value === undefined) return;
  if (value === null || value === '') {
    if (options.keepEmpty) q.set(key, '');
    return;
  }
  if (Array.isArray(value)) {
    value.forEach((item, index) => {
      if (typeof item === 'object' && item !== null) {
        Object.entries(item).forEach(([childKey, childValue]) => appendValue(q, `${key}[${index}][${childKey}]`, childValue, options));
        return;
      }
      appendValue(q, `${key}[${index}]`, item, options);
    });
    return;
  }
  if (typeof value === 'object') {
    Object.entries(value).forEach(([childKey, childValue]) => appendValue(q, `${key}[${childKey}]`, childValue, options));
    return;
  }
  q.set(key, String(value));
}

function toQuery(params: AnyRecord = {}, options: { keepEmpty?: boolean } = {}) {
  const q = new URLSearchParams();
  Object.entries(params).forEach(([k, v]) => appendValue(q, k, v, options));
  const auth = getAuth();
  if (auth && !q.has('auth_data')) q.set('auth_data', auth);
  return q.toString();
}

function normalizeError(json: any, fallback: string) {
  return json?.message || json?.msg || json?.error || fallback;
}

async function parseResponse(res: Response, options: { raw?: boolean } = {}) {
  const text = await res.text();
  if (res.status === 401) {
    if (telegramMiniActive) invalidateTelegramMiniSession();
    else { clearAuth(); window.dispatchEvent(new Event('forest-auth-invalid')); }
  }
  if (options.raw) {
    if (!res.ok) {
      let fallback = text || res.statusText;
      try {
        const payload = text ? JSON.parse(text) : null;
        fallback = normalizeError(payload, res.statusText);
      } catch {
        // keep plain-text fallback
      }
      throw new Error(fallback);
    }
    return text;
  }
  let payload: any = null;
  try {
    payload = text ? JSON.parse(text) : null;
  } catch {
    if (!res.ok) throw new Error(text || res.statusText);
    return text;
  }
  if (!res.ok) throw new Error(normalizeError(payload, res.statusText));
  if (payload?.message && payload.data === undefined && res.status >= 400) throw new Error(payload.message);
  return payload;
}

export async function apiGet(path: string, params: AnyRecord = {}, options: { signal?: AbortSignal; cache?: RequestCache; keepEmpty?: boolean } = {}) {
  if (telegramMiniActive) {
    const url = aiOpsRequestURL(path);
    const qs = miniQuery(params, options);
    return parseResponse(await fetch(`${url}${qs ? `?${qs}` : ''}`, { credentials: 'omit', signal: options.signal, cache: 'no-store', headers: aiOpsRequestHeaders() }));
  }
  const qs = toQuery(params, { keepEmpty: options.keepEmpty });
  const res = await fetch(`/api/v1/${getAdminPath()}${path}${qs ? `?${qs}` : ''}`, { credentials: 'same-origin', signal: options.signal, cache: options.cache });
  if (path.startsWith('/apple-id/')) await revalidateSupportIdentity(res);
  return parseResponse(res);
}

export async function apiWrite(method: 'POST' | 'PUT' | 'PATCH' | 'DELETE', path: string, body: AnyRecord = {}) {
  if (telegramMiniActive) {
    const url = aiOpsRequestURL(path);
    return parseResponse(await fetch(url, { method, credentials: 'omit', cache: 'no-store', headers: { ...aiOpsRequestHeaders(), 'Content-Type': 'application/json' }, body: method === 'DELETE' ? undefined : JSON.stringify(body) }));
  }
  // Strict JSON admin endpoints authenticate from the query string, so auth_data is not added to their decoded payload.
  const auth = getAuth();
  const suffix = auth ? `${path.includes('?') ? '&' : '?'}auth_data=${encodeURIComponent(auth)}` : '';
  const res = await fetch(`/api/v1/${getAdminPath()}${path}${suffix}`, { method, credentials: 'same-origin', headers: { 'Content-Type': 'application/json' }, body: method === 'DELETE' ? undefined : JSON.stringify(body) });
  return parseResponse(res);
}
export const apiJsonPost = (path: string, body: AnyRecord = {}) => apiWrite('POST', path, body);
export const apiPut = (path: string, body: AnyRecord = {}) => apiWrite('PUT', path, body);
export const apiPatch = (path: string, body: AnyRecord = {}) => apiWrite('PATCH', path, body);
export const apiDelete = (path: string) => apiWrite('DELETE', path);

export async function apiPost(path: string, body: AnyRecord = {}, options: { form?: boolean; keepEmpty?: boolean; raw?: boolean } = {}) {
  if (telegramMiniActive) return apiJsonPost(path, body);
  const payload = { ...body, auth_data: body.auth_data ?? getAuth() };
  const init: RequestInit = {
    method: 'POST',
    credentials: 'same-origin',
  };
  if (options.form) {
    init.headers = { 'Content-Type': 'application/x-www-form-urlencoded; charset=UTF-8' };
    init.body = toQuery(payload, { keepEmpty: options.keepEmpty });
  } else {
    init.headers = { 'Content-Type': 'application/json' };
    init.body = JSON.stringify(payload);
  }
  const res = await fetch(`/api/v1/${getAdminPath()}${path}`, init);
  if (path.startsWith('/apple-id/')) await revalidateSupportIdentity(res);
  return parseResponse(res, { raw: options.raw });
}

export async function passportLogin(email: string, password: string) {
  prohibitMiniRequest();
  const res = await fetch('/api/v1/passport/auth/login', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    credentials: 'same-origin',
    body: JSON.stringify({ email, password }),
  });
  return parseResponse(res);
}

export async function checkLogin() {
  prohibitMiniRequest();
  const res = await fetch(`/api/v1/user/checkLogin?${toQuery()}`, { credentials: 'same-origin' });
  return parseResponse(res);
}

export async function getCurrentUserInfo() {
  prohibitMiniRequest();
  const res = await fetch(`/api/v1/user/info?${toQuery()}`, { credentials: 'same-origin' });
  return parseResponse(res);
}

export function unwrapList(payload: any): any[] {
  const data = payload?.data;
  if (Array.isArray(data)) return data;
  if (Array.isArray(data?.data)) return data.data;
  if (Array.isArray(data?.list)) return data.list;
  if (Array.isArray(data?.items)) return data.items;
  return [];
}

export function unwrapTotal(payload: any, rows: any[] = []) {
  return Number(payload?.total ?? payload?.data?.total ?? payload?.data?.total_count ?? rows.length) || 0;
}

export function unwrapData(payload: any): any {
  return payload?.data ?? payload;
}

export function money(cents: any) {
  const n = Number(cents || 0);
  if (!Number.isFinite(n)) return '-';
  return `¥${(n / 100).toFixed(2)}`;
}

export function bytes(value: any) {
  const n = Number(value || 0);
  if (!Number.isFinite(n)) return '-';
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
  let x = n; let i = 0;
  while (x >= 1024 && i < units.length - 1) { x /= 1024; i += 1; }
  return `${x.toFixed(i === 0 ? 0 : 2)} ${units[i]}`;
}

export function gbToBytes(value: any) {
  const n = Number(value || 0);
  if (!Number.isFinite(n)) return value;
  return Math.round(n * 1024 * 1024 * 1024);
}


export function bytesToGBText(value: any) {
  const n = Number(value || 0);
  if (!Number.isFinite(n)) return '-';
  return (n / 1024 / 1024 / 1024).toFixed(2);
}

export function bytesToGB(value: any) {
  const n = Number(value || 0);
  if (!Number.isFinite(n)) return 0;
  return Number((n / 1024 / 1024 / 1024).toFixed(2));
}

export function unixTime(value: any) {
  const n = Number(value || 0);
  if (!n) return '-';
  return new Date(n * 1000).toLocaleString();
}

export function truthy(v: any) {
  return v === true || v === 1 || v === '1' || v === 'true';
}

export function boolNumber(v: any) {
  return truthy(v) ? 1 : 0;
}

export function safeJsonParse(value: any, fallback: any = value) {
  if (typeof value !== 'string') return value;
  const trimmed = value.trim();
  if (!trimmed) return fallback;
  if (!trimmed.startsWith('{') && !trimmed.startsWith('[')) return fallback;
  try { return JSON.parse(trimmed); } catch { return fallback; }
}

export function compactObject<T extends AnyRecord>(value: T): T {
  Object.keys(value).forEach((key) => {
    if (value[key] === undefined || value[key] === '') delete value[key];
  });
  return value;
}

let supportIdentityRefresh: Promise<void> | null = null;
async function revalidateSupportIdentity(res: Response) {
  if (res.status !== 403) return;
  if (!supportIdentityRefresh) {
    supportIdentityRefresh = (async () => {
      const token = getAuth();
      let revoked = false;
      try {
        const response = await fetch(`/api/v1/user/checkLogin?${toQuery()}`, { credentials: 'same-origin', cache: 'no-store' });
        if (response.status === 401 || response.status === 403) revoked = true;
        else if (response.ok) {
          const payload = await response.json();
          revoked = !payload?.data?.is_staff && !payload?.data?.is_admin;
        }
      } catch { /* A transport error is not proof of revoked access. */ }
      if (revoked && token === getAuth()) {
        clearAuth();
        window.dispatchEvent(new Event('forest-auth-invalid'));
      }
    })().finally(() => { supportIdentityRefresh = null; });
  }
  await supportIdentityRefresh;
}

// Support endpoints are independent of the administrator route and accept no auth fields in their JSON DTOs.
export async function supportGet(path: string, params: AnyRecord = {}) {
  prohibitMiniRequest();
  const qs = new URLSearchParams();
  Object.entries(params).forEach(([key, value]) => appendValue(qs, key, value));
  const auth = getAuth();
  const res = await fetch(`/api/v1/support${path}${qs.size ? `?${qs}` : ''}`, { credentials: 'same-origin', cache: 'no-store', headers: auth ? { Authorization: `Bearer ${auth}` } : {} });
  await revalidateSupportIdentity(res);
  return parseResponse(res);
}

export async function supportPost(path: string, body: AnyRecord = {}) {
  prohibitMiniRequest();
  const auth = getAuth();
  const res = await fetch(`/api/v1/support${path}`, {
    method: 'POST', credentials: 'same-origin', cache: 'no-store',
    headers: { 'Content-Type': 'application/json', ...(auth ? { Authorization: `Bearer ${auth}` } : {}) }, body: JSON.stringify(body),
  });
  await revalidateSupportIdentity(res);
  return parseResponse(res);
}
