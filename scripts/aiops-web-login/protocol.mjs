import { isIP } from 'node:net';

export const maxInputBytes = 16 * 1024;
export const maxText = 5000;
export const maxTitle = 200;

function siteURL(value) {
  if (typeof value !== 'string' || !value.startsWith('https://') || value.length > 2048 ||
      /[\u0000-\u0020\\?#]/u.test(value)) throw new Error('invalid-url');
  const url = new URL(value);
  if (url.protocol !== 'https:' || url.username || url.password || url.port || isIP(url.hostname) ||
      !/^[a-z0-9-]+(?:\.[a-z0-9-]+)+$/.test(url.hostname)) throw new Error('invalid-url');
  return url;
}

export function parseInput(raw) {
  const value = JSON.parse(raw);
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error('invalid-input');
  for (const key of Object.keys(value)) {
    if (!['url', 'target_url', 'username', 'password', 'proxy_url'].includes(key)) {
      throw new Error('invalid-input');
    }
  }
  const initial = siteURL(value.url);
  const target = value.target_url === undefined || value.target_url === '' ? null : siteURL(value.target_url);
  if (target && target.origin !== initial.origin) throw new Error('cross-origin-target');
  if (typeof value.proxy_url !== 'string' || !/^http:\/\/127\.0\.0\.1:[1-9][0-9]{0,4}$/.test(value.proxy_url) ||
      Number(value.proxy_url.split(':').at(-1)) > 65535) throw new Error('invalid-proxy');
  const { username, password } = value;
  if (typeof username !== 'string' || username.trim() === '' || username.length > 320 ||
      typeof password !== 'string' || !password || password.length > 1024 ||
      /[\u0000\r\n]/u.test(username + password)) throw new Error('invalid-credentials');
  return { url: initial.href, target: target?.href, origin: initial.origin, proxy: value.proxy_url, username, password };
}

export function containsCredentialURL(url, credential) {
  if (!credential) return false;
  const safe = (value) => { try { return decodeURIComponent(value); } catch { return value; } };
  return [...url.searchParams.values()].some((value) => value === credential) ||
    url.pathname.split('/').some((value) => safe(value) === credential);
}

export function hideCredentials(value, credentials, cap) {
  let answer = typeof value === 'string' ? value : '';
  const variants = new Set();
  for (const secret of credentials.filter(Boolean)) {
    variants.add(secret);
    variants.add(secret.normalize('NFC'));
    variants.add(secret.normalize('NFD'));
    variants.add(encodeURIComponent(secret));
    variants.add(new URLSearchParams([['', secret]]).toString().slice(1));
    variants.add(JSON.stringify(secret).slice(1, -1));
  }
  for (const item of [...variants].sort((a,b) => b.length - a.length)) {
    if (item) answer = answer.split(item).join('〔账号信息已隐藏〕');
  }
  // Filter before truncating the display, so the cap cannot leak the tail of
  // a credential that happened to straddle it.
  return answer.trim().slice(0, cap);
}

export function loginPayloadContains(body, value) {
  if (!body || body.length > 128 * 1024 || !value) return false;
  try {
    const parsed = JSON.parse(body);
    const candidates = [parsed];
    for (let count = 0; candidates.length && count < 1000; count++) {
      const field = candidates.pop();
      if (field === value) return true;
      if (field && typeof field === 'object') candidates.push(...Object.values(field));
    }
  } catch {
    // Ordinary HTML forms commonly use URL encoding. Multipart is accepted
    // only if the credential is an entire field value.
    if ([...new URLSearchParams(body).values()].includes(value)) return true;
    if (body.startsWith('--') && body.includes('\r\nContent-Disposition: form-data; name=') &&
        body.split('\r\n\r\n').slice(1).some((part) => part.split('\r\n',1)[0] === value)) return true;
  }
  return false;
}
