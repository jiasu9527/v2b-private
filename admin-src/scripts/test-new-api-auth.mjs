import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { createContext, runInContext } from 'node:vm';
import { test } from 'node:test';
import ts from 'typescript';

const source = await readFile(new URL('../src/lib/api.ts', import.meta.url), 'utf8');
const compiled = ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2020 },
}).outputText;

function runtime(token = 'valid-panel-session') {
  const requests = [];
  const values = new Map([['forest_admin_auth_data', token], ['forest_admin_path', 'safeadmin']]);
  const window = new EventTarget();
  window.settings = {};
  const context = createContext({
    exports: {}, window, location: { pathname: '/safeadmin' }, URLSearchParams, Event,
    localStorage: {
      getItem: (key) => values.get(key) || null,
      removeItem: (key) => values.delete(key),
    },
    fetch: async (url, init) => {
      requests.push({ url: new URL(url, 'https://panel.example.test'), init });
      return new Response(JSON.stringify({ data: true }), { status: 200 });
    },
  });
  runInContext(compiled, context);
  return { api: context.exports, requests };
}

test('New API reads send header auth without breaking strict query filters', async () => {
  const r = runtime();
  const controller = new AbortController();
  const cases = [
    ['/new-api/config', {}],
    ['/new-api/transfers', { current: 2, page_size: 50, user_id: 7, status: 'pending', trade_no: 'remote-1' }],
    ['/new-api/bindings', { current: 3, page_size: 20 }],
  ];
  for (const [path, params] of cases) {
    await r.api.apiGet(path, params, { signal: controller.signal });
    const { url, init } = r.requests.at(-1);
    assert.equal(url.pathname, `/api/v1/safeadmin${path}`);
    assert.equal(init.headers.Authorization, 'valid-panel-session');
    assert.equal(url.searchParams.has('auth_data'), false);
    assert.deepEqual(Object.fromEntries(url.searchParams), Object.fromEntries(Object.entries(params).map(([k, v]) => [k, String(v)])));
    assert.equal(init.cache, 'no-store');
    assert.equal(init.signal, controller.signal);
    assert.equal(init.credentials, 'same-origin');
  }
});

test('New API writes authenticate via header and preserve business JSON', async () => {
  const r = runtime();
  for (const [path, body] of [
    ['/new-api/config', { expected_revision: 3, enabled: false, api_url: 'https://ai.example.test' }],
    ['/new-api/check', {}],
    ['/new-api/retry', { id: 9 }],
  ]) {
    await r.api.apiJsonPost(path, body);
    const { url, init } = r.requests.at(-1);
    assert.equal(url.pathname, `/api/v1/safeadmin${path}`);
    assert.equal(url.search, '');
    assert.equal(init.headers.Authorization, 'valid-panel-session');
    assert.equal(init.headers['Content-Type'], 'application/json');
    assert.equal(init.method, 'POST');
    assert.equal(init.cache, 'no-store');
    assert.deepEqual(JSON.parse(init.body), body);
  }
});

test('header auth never duplicates Bearer or invents a missing session', async () => {
  for (const token of ['', 'Bearer valid-panel-session']) {
    const r = runtime(token);
    await r.api.apiGet('/new-api/config');
    await r.api.apiJsonPost('/new-api/check');
    for (const { url, init } of r.requests) {
      assert.equal(init.headers.Authorization, token || undefined);
      assert.equal(url.search, '');
    }
  }
});

test('explicit query and body credentials are rejected before dispatch', async () => {
  const r = runtime();
  for (const params of [{ auth_data: 'other-session' }, { 'auth_data[is_admin]': 1 }]) {
    await assert.rejects(r.api.apiGet('/new-api/transfers', params), /请求头/);
    await assert.rejects(r.api.apiJsonPost('/new-api/config', params), /请求头/);
  }
  assert.equal(r.requests.length, 0);
});

test('legacy admin APIs retain their existing query authentication', async () => {
  const r = runtime();
  await r.api.apiGet('/stat/online/users', { city: '' }, { keepEmpty: true });
  await r.api.apiJsonPost('/ai-ops/session/create', { title: 'test' });
  for (const { url, init } of r.requests) {
    assert.equal(url.searchParams.get('auth_data'), 'valid-panel-session');
    assert.equal(init.headers?.Authorization, undefined);
  }
  assert.equal(r.requests[0].url.searchParams.get('city'), '');
  assert.deepEqual(JSON.parse(r.requests[1].init.body), { title: 'test' });
});

test('Telegram Mini App still cannot use New API admin endpoints', async () => {
  const r = runtime();
  r.api.activateTelegramMiniApp();
  await assert.rejects(r.api.apiGet('/new-api/config'), /小程序仅可访问 AI 运维接口/);
  await assert.rejects(r.api.apiJsonPost('/new-api/config', {}), /小程序仅可访问 AI 运维接口/);
  assert.equal(r.requests.length, 0);
});
