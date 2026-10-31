import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { createContext, runInContext } from 'node:vm';
import { test } from 'node:test';
import ts from 'typescript';

const apiSource = await readFile(new URL('../src/lib/api.ts', import.meta.url), 'utf8');
const streamSource = await readFile(new URL('../src/pages/aiOpsStream.ts', import.meta.url), 'utf8');
const miniSource = await readFile(new URL('../src/TelegramMiniApp.tsx', import.meta.url), 'utf8');
const pageSource = await readFile(new URL('../src/pages/AIOpsPage.tsx', import.meta.url), 'utf8');
const mainSource = await readFile(new URL('../src/main.tsx', import.meta.url), 'utf8');
const compiled = (source) => ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2020, jsx: ts.JsxEmit.React, esModuleInterop: true } }).outputText;
const plain = (value) => JSON.parse(JSON.stringify(value));
const identity = { id: 7, email: 'staff@example.test', is_admin: 0, is_staff: 1 };
const session = () => ({ token: 'mini.fixture-token', expires_at: Math.floor(Date.now() / 1000) + 3600, user: { ...identity } });

function runtime() {
  const requests = [], storageCalls = [];
  const values = new Map([['forest_admin_auth_data', 'ordinary-web-token'], ['forest_admin_user_info', JSON.stringify({ id: 99, is_admin: 1 })], ['forest_admin_path', 'safeadmin']]);
  const storage = {
    getItem(key) { storageCalls.push(['get', key]); return values.get(key) || null; },
    setItem(key, value) { storageCalls.push(['set', key, value]); values.set(key, value); },
    removeItem(key) { storageCalls.push(['remove', key]); values.delete(key); },
  };
  const window = new EventTarget(); window.settings = {};
  let respond = () => new Response(JSON.stringify({ data: true }), { status: 200, headers: { 'Content-Type': 'application/json' } });
  const context = createContext({ exports: {}, window, location: { pathname: '/ai-ops-mini' }, localStorage: storage, URLSearchParams, Event, TextDecoder,
    fetch: async (url, init) => { requests.push({ url, init }); return respond(url, init); },
  });
  runInContext(compiled(apiSource), context);
  const api = context.exports;
  const streamContext = createContext({ exports: {}, require: () => api, window, Event, TextDecoder, fetch: context.fetch });
  runInContext(compiled(streamSource), streamContext);
  return { api, stream: streamContext.exports, requests, storageCalls, values, window, respond: (next) => { respond = next; } };
}

test('ordinary web credentials and admin routing remain unchanged outside Mini App mode', async () => {
  const r = runtime();
  await r.api.apiGet('/ai-ops/sessions');
  assert.match(r.requests[0].url, /^\/api\/v1\/safeadmin\/ai-ops\/sessions\?auth_data=ordinary-web-token$/);
  assert.equal(r.api.getAdminUserInfo().id, 99);
  assert.equal(r.api.aiOpsRequestHeaders().Authorization, 'ordinary-web-token');
});

test('mini auth sends only initData once and never uses stored web credentials', async () => {
  const r = runtime(); r.api.activateTelegramMiniApp();
  r.respond(() => new Response(JSON.stringify({ data: session() })));
  const result = await r.api.authenticateTelegramMiniApp('signed-telegram-data');
  r.api.setTelegramMiniSession(result);
  assert.equal(r.requests.length, 1);
  assert.equal(r.requests[0].url, '/api/v1/ai-ops-mini/auth');
  assert.deepEqual(JSON.parse(r.requests[0].init.body), { init_data: 'signed-telegram-data' });
  assert.equal(r.requests[0].init.credentials, 'omit');
  assert.equal(r.requests[0].init.headers.Authorization, undefined);
  assert.deepEqual(plain(r.api.getAdminUserInfo()), identity);
  assert.deepEqual(r.storageCalls, []);
});

test('all mini AI verbs use their isolated prefix and Bearer header with no URL auth or cookies', async () => {
  const r = runtime(); r.api.activateTelegramMiniApp(); r.api.setTelegramMiniSession(session());
  await r.api.apiGet('/ai-ops/session', { id: 42 });
  await r.api.apiJsonPost('/ai-ops/chat/send', { session_id: 42, content: '检查入口' });
  await r.api.apiPost('/ai-ops/knowledge/save', { title: 'Example' });
  assert.deepEqual(r.requests.map((request) => request.url), ['/api/v1/ai-ops-mini/session?id=42', '/api/v1/ai-ops-mini/chat/send', '/api/v1/ai-ops-mini/knowledge/save']);
  for (const request of r.requests) {
    assert.equal(request.init.headers.Authorization, 'Bearer mini.fixture-token');
    assert.equal(request.init.credentials, 'omit');
    assert.doesNotMatch(request.url + (request.init.body || ''), /fixture-token|auth_data/);
  }
  assert.deepEqual(r.storageCalls, []);
});

test('mini mode blocks non-AI helpers and traversal before any network request', async () => {
  const r = runtime(); r.api.activateTelegramMiniApp(); r.api.setTelegramMiniSession(session());
  for (const path of ['/user/fetch', '/ai-ops/../user', '/ai-ops//session', '/ai-ops/session?auth_data=x', 'https://elsewhere.test/ai-ops/session']) {
    await assert.rejects(r.api.apiGet(path));
    await assert.rejects(r.api.apiJsonPost(path, {}));
  }
  for (const call of [() => r.api.supportGet('/user'), () => r.api.supportPost('/user', {}), () => r.api.checkLogin(), () => r.api.getCurrentUserInfo(), () => r.api.passportLogin('a', 'b'), () => r.api.apiGet('/ai-ops/session', { auth_data: 'x' })]) await assert.rejects(call());
  assert.throws(() => r.api.setAuth('overwrite'));
  assert.throws(() => r.api.setAdminUserInfo({ id: 1 }));
  assert.throws(() => r.api.setAdminPath('overwrite'));
  assert.equal(r.requests.length, 0);
  assert.deepEqual(r.storageCalls, []);
});

test('401 clears only in-memory mini auth and never relogs or deletes ordinary web auth', async () => {
  const r = runtime(); r.api.activateTelegramMiniApp(); r.api.setTelegramMiniSession(session());
  let invalidations = 0; r.window.addEventListener('forest-mini-auth-invalid', () => invalidations++);
  r.respond(() => new Response('{"message":"expired"}', { status: 401 }));
  await assert.rejects(r.api.apiGet('/ai-ops/sessions'));
  assert.equal(r.api.getAuth(), '');
  assert.equal(invalidations, 1);
  assert.equal(r.requests.length, 1);
  r.api.clearAuth(); r.api.clearAdminUserInfo();
  assert.equal(r.values.get('forest_admin_auth_data'), 'ordinary-web-token');
  assert.deepEqual(r.storageCalls, []);
  await assert.rejects(r.api.apiGet('/ai-ops/sessions'));
  assert.equal(r.requests.length, 1, 'expired credentials cannot dispatch a second request');
});

test('identity is validated from the server and 403 invalidation also preserves web auth', async () => {
  const r = runtime(); r.api.activateTelegramMiniApp(); r.api.setTelegramMiniSession(session());
  r.respond(() => new Response(JSON.stringify({ data: { ...identity, expires_at: session().expires_at } })));
  assert.deepEqual(plain(await r.api.getTelegramMiniIdentity()), identity);
  assert.equal(r.requests[0].url, '/api/v1/ai-ops-mini/identity');
  r.respond(() => new Response('{"message":"revoked"}', { status: 403 }));
  await assert.rejects(r.api.getTelegramMiniIdentity());
  assert.equal(r.api.getAuth(), '');
  assert.deepEqual(r.storageCalls, []);
  for (const user of [{ ...identity, is_staff: 0 }, { ...identity, id: 0 }, { ...identity, is_admin: '1' }]) assert.throws(() => r.api.telegramMiniIdentity(user));
});

test('mini SSE uses isolated URL/header and invalidates on 401 without a web login fallback', async () => {
  const r = runtime(); r.api.activateTelegramMiniApp(); r.api.setTelegramMiniSession(session());
  r.respond(() => new Response('event: end\ndata: {"session_id":42}\n\n', { headers: { 'Content-Type': 'text/event-stream' } }));
  const events = [];
  await r.stream.aiReadSessionStream(42, new AbortController().signal, (event) => events.push(event));
  assert.equal(events[0].event, 'end');
  assert.equal(r.requests[0].url, '/api/v1/ai-ops-mini/session/stream?id=42');
  assert.equal(r.requests[0].init.headers.Authorization, 'Bearer mini.fixture-token');
  assert.equal(r.requests[0].init.credentials, 'omit');
  r.respond(() => new Response('', { status: 401 }));
  await assert.rejects(r.stream.aiReadSessionStream(42, new AbortController().signal, () => {}));
  assert.equal(r.api.getAuth(), '');
  assert.deepEqual(r.storageCalls, []);
});

test('only explicit SSE auth_invalid errors clear mini credentials; stream/network failures do not', async () => {
  const r = runtime(); r.api.activateTelegramMiniApp(); r.api.setTelegramMiniSession(session());
  r.respond(() => new Response('event: error\ndata: {"message":"stream unavailable"}\n\n', { headers: { 'Content-Type': 'text/event-stream' } }));
  await assert.rejects(r.stream.aiReadSessionStream(42, new AbortController().signal, () => {}));
  assert.equal(r.api.getAuth(), 'mini.fixture-token');
  r.respond(() => new Response('event: error\ndata: {"code":"auth_invalid","message":"binding revoked"}\n\n', { headers: { 'Content-Type': 'text/event-stream' } }));
  await assert.rejects(r.stream.aiReadSessionStream(42, new AbortController().signal, () => {}));
  assert.equal(r.api.getAuth(), '');
  assert.deepEqual(r.storageCalls, []);
});

test('mini entry bypasses web login, revalidates identity and never trusts unsigned Telegram profile', () => {
  assert.match(mainSource, /getSettings\(\).telegram_mini_app === true/);
  assert.match(mainSource, /if \(miniApp\) activateTelegramMiniApp\(\)/);
  assert.match(mainSource, /miniApp \? <TelegramMiniApp \/> : <App \/>/);
  assert.doesNotMatch(miniSource, /initDataUnsafe|localStorage|sessionStorage|document\.cookie|passportLogin|checkLogin/);
  assert.match(miniSource, /if \(!webApp\?\.initData\) setPhase\('outside'\)/);
  assert.equal((miniSource.match(/authenticateTelegramMiniApp\(/g) || []).length, 1);
  assert.match(miniSource, /getTelegramMiniIdentity\(controller.signal\)/);
  assert.match(miniSource, /visibilitychange/);
  assert.match(miniSource, /关闭窗口不影响已经提交的后台任务/);
  assert.match(pageSource, /\.\.\.\(!miniApp \? \[\{ key: 'telegram'/);
  assert.match(pageSource, /miniApp \|\| screens.lg === false/);
});

function miniLifecycle({ initData = 'signed', authFails = false } = {}) {
  const effects = [], states = [], intervals = new Map(), timeouts = new Map();
  const calls = { auth: 0, identity: 0, clear: 0, ready: 0, expand: 0 };
  let nextTimer = 0, rejectIdentity = false, stateIndex = 0, mounted = false;
  const window = new EventTarget();
  Object.assign(window, {
    innerHeight: 700,
    setInterval(callback) { intervals.set(++nextTimer, callback); return nextTimer; },
    clearInterval(id) { intervals.delete(id); },
    setTimeout(callback) { timeouts.set(++nextTimer, callback); return nextTimer; },
    clearTimeout(id) { timeouts.delete(id); },
    Telegram: { WebApp: { initData, ready() { calls.ready++; }, expand() { calls.expand++; }, onEvent() {}, offEvent() {} } },
  });
  const document = new EventTarget();
  Object.assign(document, { visibilityState: 'visible', documentElement: { style: { setProperty() {} } }, body: { classList: { add() {}, remove() {}, toggle() {} } } });
  const react = {
    createElement: (type, props, ...children) => ({ type, props, children }),
    useState(initial) { const index = stateIndex++; if (!mounted) states[index] = initial; return [states[index], (next) => { states[index] = typeof next === 'function' ? next(states[index]) : next; }]; },
    useRef: (current) => ({ current }),
    useEffect: (callback) => { if (!mounted) effects.push(callback); },
  };
  const api = {
    authenticateTelegramMiniApp: async () => { calls.auth++; if (authFails) throw new Error('invalid'); return session(); },
    getTelegramMiniIdentity: async () => { calls.identity++; if (rejectIdentity) throw new Error('offline'); return identity; },
    setTelegramMiniSession() {},
    clearAuth() { calls.clear++; },
    invalidateTelegramMiniSession() { window.dispatchEvent(new Event('forest-mini-auth-invalid')); },
  };
  const context = createContext({ exports: {}, window, document, AbortController, require(name) {
    if (name === 'react') return react;
    if (name === 'antd') return { ConfigProvider: 'ConfigProvider', Space: 'Space', Spin: 'Spin', Button: 'Button', Alert: 'Alert', Typography: { Title: 'Title', Text: 'Text' }, theme: {} };
    if (name === './lib/api') return api;
    if (name === './pages/AIOpsPage') return { __esModule: true, default: 'AIOpsPage' };
    return {};
  } });
  runInContext(compiled(miniSource), context);
  context.exports.default();
  mounted = true;
  assert.equal(effects.length, 2);
  const cleanups = effects.map((effect) => effect());
  const cleanup = () => cleanups.forEach((clean) => clean());
  return { calls, states, intervals, timeouts, window, document, cleanup, offline(value) { rejectIdentity = value; }, render() { stateIndex = 0; return context.exports.default(); } };
}
const settle = () => new Promise((resolve) => setTimeout(resolve, 0));

test('actual MiniApp lifecycle outside Telegram never calls auth and only shows the Bot entry instruction', async () => {
  const ui = miniLifecycle({ initData: '' });
  await settle();
  assert.equal(ui.calls.auth, 0);
  assert.equal(ui.calls.identity, 0);
  assert.equal(ui.states[0], 'outside');
  ui.cleanup();
});

test('actual MiniApp lifecycle authenticates once, rechecks identity, and expires without automatically logging in again', async () => {
  const ui = miniLifecycle();
  await settle();
  assert.equal(ui.calls.auth, 1); assert.equal(ui.calls.identity, 1);
  assert.equal(ui.calls.ready, 1); assert.equal(ui.calls.expand, 1);
  assert.equal(ui.states[0], 'ready');
  ui.document.visibilityState = 'hidden';
  ui.document.dispatchEvent(new Event('visibilitychange'));
  assert.equal(ui.states[0], 'verify');
  ui.document.visibilityState = 'visible';
  ui.document.dispatchEvent(new Event('visibilitychange'));
  await settle();
  assert.equal(ui.calls.identity, 2); assert.equal(ui.states[0], 'ready');
  for (const expire of ui.timeouts.values()) expire();
  assert.equal(ui.states[0], 'invalid');
  for (const tick of ui.intervals.values()) tick();
  ui.document.dispatchEvent(new Event('visibilitychange'));
  await settle();
  assert.equal(ui.calls.auth, 1); assert.equal(ui.calls.identity, 2);
  ui.cleanup(); assert.equal(ui.intervals.size, 0); assert.equal(ui.timeouts.size, 0);
});

test('transient identity failures hide operations but can revalidate without reusing initData', async () => {
  const ui = miniLifecycle(); await settle();
  ui.offline(true); for (const tick of ui.intervals.values()) tick(); await settle();
  assert.equal(ui.states[0], 'verify'); assert.equal(ui.calls.clear, 0);
  ui.offline(false); for (const tick of ui.intervals.values()) tick(); await settle();
  assert.equal(ui.states[0], 'ready'); assert.equal(ui.calls.auth, 1);
  ui.window.dispatchEvent(new Event('forest-mini-auth-invalid'));
  for (const tick of ui.intervals.values()) tick(); await settle();
  assert.equal(ui.states[0], 'invalid'); assert.equal(ui.calls.auth, 1);
  ui.cleanup();
});

test('background and transient verification keep the identical chat subtree position and key instead of resetting draft state', async () => {
  const ui = miniLifecycle(); await settle();
  const workspace = () => ui.render().children[0].children[0];
  const before = workspace();
  assert.equal(before.props.hidden, false);
  assert.equal(before.children[0].type, 'AIOpsPage');
  const key = before.children[0].props.key;
  ui.document.visibilityState = 'hidden'; ui.document.dispatchEvent(new Event('visibilitychange'));
  const suspended = workspace();
  assert.equal(suspended.type, before.type); assert.equal(suspended.props.hidden, true);
  assert.equal(suspended.children[0].props.key, key);
  ui.document.visibilityState = 'visible'; ui.document.dispatchEvent(new Event('visibilitychange')); await settle();
  const resumed = workspace();
  assert.equal(resumed.props.hidden, false);
  assert.equal(resumed.type, before.type);
  assert.equal(resumed.children[0].type, before.children[0].type);
  assert.equal(resumed.children[0].props.key, key);
  ui.offline(true); for (const tick of ui.intervals.values()) tick(); await settle();
  assert.equal(workspace().props.hidden, true); assert.equal(workspace().children[0].props.key, key);
  ui.window.dispatchEvent(new Event('forest-mini-auth-invalid'));
  assert.equal(workspace(), false, 'revoked sessions must actually remove all chat content');
  ui.cleanup();
});
