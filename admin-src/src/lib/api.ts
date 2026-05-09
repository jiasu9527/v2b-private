export type AnyRecord = Record<string, any>;

const AUTH_KEY = 'forest_admin_auth_data';
const ADMIN_USER_KEY = 'forest_admin_user_info';
const ADMIN_PATH_KEY = 'forest_admin_path';
const DEFAULT_ADMIN_PATH = 'localadmin';

export function getSettings(): AnyRecord {
  return (window as any).settings || {};
}

export function getAdminPath(): string {
  const saved = normalizeAdminPath(localStorage.getItem(ADMIN_PATH_KEY));
  const fromSettings = normalizeAdminPath(getSettings().secure_path);
  const path = saved || fromSettings || pathAdminSegment() || DEFAULT_ADMIN_PATH;
  return String(path).replace(/^\/+|\/+$/g, '');
}

export function setAdminPath(path: string) {
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
  return localStorage.getItem(AUTH_KEY) || '';
}

export function setAuth(token: string) {
  localStorage.setItem(AUTH_KEY, token || '');
}

export function getAdminUserInfo(): AnyRecord {
  try {
    return JSON.parse(localStorage.getItem(ADMIN_USER_KEY) || '{}') || {};
  } catch {
    return {};
  }
}

export function setAdminUserInfo(user: AnyRecord = {}) {
  const email = String(user.email || user.mail || user.account || '').trim();
  const payload = { ...user, email };
  localStorage.setItem(ADMIN_USER_KEY, JSON.stringify(payload));
}

export function clearAdminUserInfo() {
  localStorage.removeItem(ADMIN_USER_KEY);
}

export function clearAuth() {
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

export async function apiGet(path: string, params: AnyRecord = {}) {
  const qs = toQuery(params);
  const res = await fetch(`/api/v1/${getAdminPath()}${path}${qs ? `?${qs}` : ''}`, { credentials: 'same-origin' });
  return parseResponse(res);
}

export async function apiPost(path: string, body: AnyRecord = {}, options: { form?: boolean; keepEmpty?: boolean; raw?: boolean } = {}) {
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
  return parseResponse(res, { raw: options.raw });
}

export async function passportLogin(email: string, password: string) {
  const res = await fetch('/api/v1/passport/auth/login', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    credentials: 'same-origin',
    body: JSON.stringify({ email, password }),
  });
  return parseResponse(res);
}

export async function checkLogin() {
  const res = await fetch(`/api/v1/user/checkLogin?${toQuery()}`, { credentials: 'same-origin' });
  return parseResponse(res);
}

export async function getCurrentUserInfo() {
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
