import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { test } from 'node:test';
import ts from 'typescript';

const compile = (source) => ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2020 } }).outputText;
const model = {};
new Function('exports', compile(await readFile(new URL('../src/pages/aiOpsModel.ts', import.meta.url), 'utf8')))(model);
const helpers = {};
new Function('exports', 'require', compile(await readFile(new URL('../src/pages/aiOpsMiniSession.ts', import.meta.url), 'utf8')))(helpers, (name) => {
  assert.equal(name, './aiOpsModel');
  return model;
});
const { aiReadMiniSession, aiWriteMiniSession, aiClearMiniSession, aiPickMiniSession } = helpers;
const key = (actor) => `forest.ai-ops-mini.last-session.v1.${actor}`;
function memory() {
  const data = new Map();
  return { data, getItem: (key) => data.get(key) ?? null, setItem: (key, value) => data.set(key, value), removeItem: (key) => data.delete(key) };
}
const session = (id, created_by, updated_at = id) => ({ id, created_by, updated_at, created_at: 1, title: '运维记录', model: 'fixture', status: 'idle' });

test('Mini preferences use a separate actor-specific namespace and store only the decimal session ID', () => {
  const storage = memory();
  storage.setItem('admin_token', 'untouched');
  assert.equal(aiReadMiniSession(7, storage), 0);
  assert.equal(aiWriteMiniSession(7, 123, storage), true);
  assert.equal(aiWriteMiniSession(8, 456, storage), true);
  assert.deepEqual([...storage.data], [['admin_token', 'untouched'], [key(7), '123'], [key(8), '456']]);
  assert.equal(aiReadMiniSession(7, storage), 123);
  assert.equal(aiReadMiniSession(8, storage), 456);
  assert.equal(aiReadMiniSession(9, storage), 0);
  assert.equal(aiClearMiniSession(7, storage), true);
  assert.equal(aiReadMiniSession(7, storage), 0);
  assert.equal(aiReadMiniSession(8, storage), 456);
  assert.equal(storage.getItem('admin_token'), 'untouched');
});

test('invalid actor and session IDs never access or mutate storage', () => {
  const forbidden = new Proxy({}, { get() { assert.fail('storage must not be accessed'); } });
  for (const id of [0, -1, 1.5, NaN, Infinity, Number.MAX_SAFE_INTEGER + 1, '7', null, undefined]) {
    assert.equal(aiReadMiniSession(id, forbidden), 0);
    assert.equal(aiWriteMiniSession(id, 8, forbidden), false);
    assert.equal(aiWriteMiniSession(7, id, forbidden), false);
    assert.equal(aiClearMiniSession(id, forbidden), false);
  }
  const storage = memory();
  aiWriteMiniSession(7, Number.MAX_SAFE_INTEGER, storage);
  assert.equal(aiReadMiniSession(7, storage), Number.MAX_SAFE_INTEGER);
});

test('malformed persisted IDs are ignored instead of coerced into another session', () => {
  const storage = memory();
  for (const value of ['', '0', '-1', '1.5', 'NaN', 'Infinity', '1e3', '0x20', '01', ' 12 ', '12\n', '9007199254740992', '{"id":12}', 'undefined']) {
    storage.setItem(key(7), value);
    assert.equal(aiReadMiniSession(7, storage), 0, value);
  }
});

test('unavailable storage, quota failures, and denied property access never interrupt the app', () => {
  const failing = { getItem() { throw new Error('denied'); }, setItem() { throw new Error('quota'); }, removeItem() { throw new Error('denied'); } };
  for (const storage of [null, failing, new Proxy({}, { get() { throw new Error('denied'); } })]) {
    assert.equal(aiReadMiniSession(7, storage), 0);
    assert.equal(aiWriteMiniSession(7, 123, storage), false);
    assert.equal(aiClearMiniSession(7, storage), false);
  }
  const original = Object.getOwnPropertyDescriptor(globalThis, 'window');
  try {
    delete globalThis.window;
    assert.equal(aiReadMiniSession(7), 0);
    assert.equal(aiWriteMiniSession(7, 123), false);
    assert.equal(aiClearMiniSession(7), false);
    Object.defineProperty(globalThis, 'window', { configurable: true, value: { get localStorage() { throw new Error('SecurityError'); } } });
    assert.equal(aiReadMiniSession(7), 0);
    assert.equal(aiWriteMiniSession(7, 123), false);
    assert.equal(aiClearMiniSession(7), false);
    const storage = memory();
    Object.defineProperty(globalThis, 'window', { configurable: true, value: { localStorage: storage } });
    assert.equal(aiWriteMiniSession(7, 123), true);
    assert.equal(aiReadMiniSession(7), 123);
    assert.equal(aiClearMiniSession(7), true);
  } finally {
    if (original) Object.defineProperty(globalThis, 'window', original);
    else delete globalThis.window;
  }
});

test('a saved readable session wins over a newer session without modifying the server list', () => {
  const old = Object.freeze(session(10, 7, 100));
  const recent = Object.freeze(session(20, 7, 200));
  const sessions = Object.freeze([recent, old]);
  assert.equal(aiPickMiniSession(sessions, 7, false, 10), old);
  assert.deepEqual(sessions, [recent, old]);
});

test('deleted, invalid, or unauthorized saved IDs fall back to the current actor latest update only', () => {
  const own = session(20, 7, 400);
  const ownOlder = session(30, 7, 200);
  const other = session(40, 8, 900);
  for (const savedID of [0, 999, -1, 1.5, NaN, '20', other.id]) {
    assert.equal(aiPickMiniSession([ownOlder, other, own], 7, false, savedID), own);
  }
  assert.equal(aiPickMiniSession([other], 7, false, other.id), null);
  assert.equal(aiPickMiniSession([], 7, false, own.id), null);
  assert.equal(aiPickMiniSession([own, other], 8, false, own.id), other);
});

test('administrators may reopen an explicitly selected staff session but default to their own', () => {
  const own = session(10, 1, 100);
  const other = session(20, 7, 200);
  assert.equal(aiPickMiniSession([other, own], 1, true, other.id), other);
  assert.equal(aiPickMiniSession([other, own], 1, true), own);
  assert.equal(aiPickMiniSession([other, own], 1, true, 999), own);
  assert.equal(aiPickMiniSession([other], 1, true), null);
  assert.equal(aiPickMiniSession([other], 1, true, 999), null);
  for (const actor of [0, -1, NaN, 1.5, '1']) assert.equal(aiPickMiniSession([other, own], actor, true, other.id), null);
});

test('selection is deterministic for tied timestamps and rejects malformed session identity', () => {
  const lower = session(10, 7, 100);
  const higher = session(20, 7, 100);
  assert.equal(aiPickMiniSession([higher, lower], 7, false), higher);
  assert.equal(aiPickMiniSession([lower, higher], 7, false), higher);
  const invalid = [session(0, 7, 900), session('30', 7, 900), session(40, 0, 900), null];
  assert.equal(aiPickMiniSession([...invalid, lower], 7, true), lower);
  assert.equal(aiPickMiniSession([...invalid, lower], 7, true, 40), lower);
  for (const timestamp of [undefined, NaN, Infinity, -1]) assert.equal(aiPickMiniSession([session(30, 7, timestamp), lower], 7, false), lower);
});

// Execute the page's real handler bodies, not a second implementation of restore.
const page = await readFile(new URL('../src/pages/AIOpsPage.tsx', import.meta.url), 'utf8');
const pageAST = ts.createSourceFile('AIOpsPage.tsx', page, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
const component = pageAST.statements.find((node) => ts.isFunctionDeclaration(node) && node.name?.text === 'AIOpsPage');
const handlerNames = ['applySnapshot', 'loadDetail', 'changeTab', 'loadLists', 'load', 'selectSession', 'newSession', 'remove'];
const handlerDeclarations = handlerNames.map((name) => {
  const declaration = component.body.statements.flatMap((node) => ts.isVariableStatement(node) ? [...node.declarationList.declarations] : []).find((node) => node.name.getText(pageAST) === name);
  assert.ok(declaration && ts.isArrowFunction(declaration.initializer), `real page handler ${name} exists`);
  return `const ${name} = ${declaration.initializer.getText(pageAST)};`;
}).join('\n');
const pending = () => {
  let resolve;
  const promise = new Promise((done) => { resolve = done; });
  return { promise, resolve };
};
function pageHarness({ miniApp = true, actorID = 7, isAdmin = false, sessions = [session(10, actorID)], savedID = 10, confirmation = null, get, post } = {}) {
  const calls = [];
  const storage = memory();
  if (savedID) aiWriteMiniSession(actorID, savedID, storage);
  const state = { sessions: [], detail: null, tab: 'chat', draft: '', error: '' };
  const env = {
    ...model, miniApp, actorID, isAdmin, confirmation, resetText: 'RESET_AI_DATA', tab: 'chat',
    alive: { current: true }, selectedRef: { current: 0 }, selectionSequence: { current: 0 }, miniResumeDone: { current: false },
    reasoningEdited: { current: false }, followOutput: { current: true }, detailSequence: { current: 0 },
    responseList: (response) => Array.isArray(response?.data) ? response.data : [],
    aiReadMiniSession: (actor) => { calls.push(['read', actor]); return aiReadMiniSession(actor, storage); },
    aiWriteMiniSession: (actor, id) => { calls.push(['write', actor, id]); return aiWriteMiniSession(actor, id, storage); },
    aiClearMiniSession: (actor) => { calls.push(['clear', actor]); return aiClearMiniSession(actor, storage); },
    aiPickMiniSession: (...args) => { calls.push(['pick']); return aiPickMiniSession(...args); },
    apiGet: async (path, params) => {
      calls.push(['get', path, params]);
      if (get) { const result = await get(path, params); if (result !== undefined) return result; }
      if (path === '/ai-ops/sessions') return { data: sessions };
      if (path === '/ai-ops/config') return { data: { models: ['fixture'] } };
      if (path === '/ai-ops/session') return { data: { session: sessions.find((item) => item.id === params.id), messages: [], actions: [], turns: [] } };
      assert.fail(`unexpected API: ${path}`);
    },
    apiJsonPost: async (path, params) => { calls.push(['post', path, params]); if (post) return post(path, params); return {}; },
    loadServers: async () => {}, applyConfig: () => calls.push(['config']),
    begin: () => true, finish() {}, fail: (error) => calls.push(['error', error.message]), message: { success() {} },
    setSessions: (value) => { state.sessions = typeof value === 'function' ? value(state.sessions) : value; },
    setDetail: (value) => { state.detail = typeof value === 'function' ? value(state.detail) : value; },
    setTab: (value) => { state.tab = value; }, setDraft: (value) => { state.draft = value; },
    setError: (value) => { state.error = value; }, setLoading() {}, setLoaded(value) { state.loaded = value; }, setSelectedID() {},
    setReasoningEffort() {}, setSendRetry() {}, setNewAccessMode() {}, setModel() {}, setConfirmation() {}, setResetText() {},
  };
  const handlers = new Function('env', compile(`const { ${Object.keys(env).join(', ')} } = env; ${handlerDeclarations}; return { ${handlerNames.join(', ')} };`))(env);
  return { ...handlers, env, state, calls, storage };
}

test('real page restores Mini only, after config, loads detail once, and never repeats default selection', async () => {
  for (const miniApp of [true, false]) {
    const fixture = pageHarness({ miniApp });
    await fixture.load();
    assert.equal(fixture.env.selectedRef.current, miniApp ? 10 : 0);
    assert.equal(fixture.calls.filter(([kind]) => kind === 'pick').length, miniApp ? 1 : 0);
    assert.equal(fixture.calls.filter(([kind, path]) => kind === 'get' && path === '/ai-ops/session').length, miniApp ? 1 : 0);
    if (miniApp) {
      assert.ok(fixture.calls.findIndex(([kind]) => kind === 'config') < fixture.calls.findIndex(([kind]) => kind === 'pick'));
      assert.equal(fixture.state.detail.session.id, 10);
      fixture.env.selectedRef.current = 0;
      await fixture.load();
      assert.equal(fixture.env.selectedRef.current, 0);
      assert.equal(fixture.calls.filter(([kind]) => kind === 'pick').length, 1);
    } else assert.equal(fixture.calls.some(([kind]) => ['read', 'write', 'clear'].includes(kind)), false);
  }
});

test('real Mini load never overrides manual new, history selection, or navigation while config is in flight', async () => {
  for (const choice of ['new', 'select', 'navigate']) {
    const gate = pending(), arrived = pending();
    const fixture = pageHarness({ sessions: [session(10, 7), session(20, 7)], get: async (path) => {
      if (path === '/ai-ops/config') { arrived.resolve(); return gate.promise; }
    } });
    const loading = fixture.load();
    await arrived.promise;
    if (choice === 'new') { fixture.newSession(); fixture.state.draft = '尚未发送的输入'; }
    if (choice === 'select') fixture.selectSession(session(20, 7));
    if (choice === 'navigate') fixture.changeTab('knowledge');
    gate.resolve({ data: { models: ['fixture'] } });
    await loading;
    assert.equal(fixture.calls.some(([kind]) => kind === 'pick'), false, choice);
    assert.equal(fixture.env.selectedRef.current, choice === 'select' ? 20 : 0, choice);
    assert.equal(fixture.state.tab, choice === 'navigate' ? 'knowledge' : 'chat');
    if (choice === 'new') assert.equal(fixture.state.draft, '尚未发送的输入');
  }
});

test('real page filters staff sessions and clears stale defaults without restoring after failed authentication data', async () => {
  const fixture = pageHarness({ sessions: [session(99, 8)] });
  await fixture.load();
  assert.deepEqual(fixture.state.sessions, []);
  assert.equal(fixture.env.selectedRef.current, 0);
  assert.equal(aiReadMiniSession(7, fixture.storage), 0);
  assert.equal(fixture.env.miniResumeDone.current, true);
  for (const failingPath of ['/ai-ops/sessions', '/ai-ops/config']) {
    const failing = pageHarness({ get: async (path) => { if (path === failingPath) throw new Error('authentication unavailable'); } });
    await failing.load();
    assert.equal(failing.env.miniResumeDone.current, false);
    assert.equal(failing.calls.some(([kind]) => ['pick', 'write', 'clear'].includes(kind)), false);
    assert.equal(failing.state.error, 'authentication unavailable');
  }
});

test('real selection persists only after a successful readable current snapshot, never on selection or stale responses', async () => {
  const fixture = pageHarness({ sessions: [session(10, 7), session(20, 7)] });
  fixture.selectSession(session(20, 7), false);
  assert.equal(aiReadMiniSession(7, fixture.storage), 10, 'selection alone does not overwrite the last good snapshot');
  await fixture.loadDetail(20);
  assert.equal(aiReadMiniSession(7, fixture.storage), 20);
  assert.equal(fixture.calls.filter(([kind]) => kind === 'write').length, 1);
  fixture.applySnapshot({ session: session(10, 7), messages: [], turns: [], actions: [] });
  fixture.applySnapshot({ session: session(20, 8), messages: [], turns: [], actions: [] });
  fixture.env.alive.current = false;
  fixture.applySnapshot({ session: session(20, 7), messages: [], turns: [], actions: [] });
  assert.equal(fixture.calls.filter(([kind]) => kind === 'write').length, 1);
  const web = pageHarness({ miniApp: false });
  web.selectSession(session(10, 7), false);
  await web.loadDetail(10);
  assert.equal(web.calls.some(([kind]) => kind === 'write'), false);
});

test('failed or malformed detail does not persist a selected session, and late details cannot overwrite a newer manual choice', async () => {
  for (const response of [{ data: {} }, { data: { session: session(20, 7), messages: 'bad' } }, { data: { session: session(20, 8), messages: [] } }]) {
    const fixture = pageHarness({ get: async (path) => path === '/ai-ops/session' ? response : undefined });
    fixture.selectSession(session(20, 7), false);
    await assert.rejects(fixture.loadDetail(20));
    assert.equal(aiReadMiniSession(7, fixture.storage), 10);
  }
  const gate = pending();
  const fixture = pageHarness({ get: async (path) => path === '/ai-ops/session' ? gate.promise : undefined });
  fixture.selectSession(session(20, 7), false);
  const loading = fixture.loadDetail(20);
  fixture.newSession();
  gate.resolve({ data: { session: session(20, 7), messages: [], actions: [], turns: [] } });
  await loading;
  assert.equal(aiReadMiniSession(7, fixture.storage), 10);
  assert.equal(fixture.calls.some(([kind]) => kind === 'write'), false);
});

test('successful Mini delete clears only a matching preference; clear/reset remove it, server deletion and failures do not', async () => {
  for (const [kind, target, shouldClear] of [['delete', 10, true], ['delete', 20, false], ['clear', 0, true], ['reset', 0, true], ['server-delete', 0, false]]) {
    const fixture = pageHarness({ isAdmin: true, confirmation: { kind, session: session(target, 7), server: { id: 3, version: 1 } } });
    fixture.env.miniResumeDone.current = true;
    await fixture.remove();
    assert.equal(aiReadMiniSession(7, fixture.storage), shouldClear ? 0 : 10, kind);
    assert.equal(fixture.calls.some(([call]) => call === 'post'), true);
  }
  for (const options of [{ miniApp: false }, { isAdmin: false }, { post: async () => { throw new Error('delete refused'); } }]) {
    const fixture = pageHarness({ isAdmin: true, confirmation: { kind: 'delete', session: session(10, 7) }, ...options });
    fixture.env.miniResumeDone.current = true;
    await fixture.remove();
    assert.equal(aiReadMiniSession(7, fixture.storage), 10);
    assert.equal(fixture.calls.some(([kind]) => kind === 'clear'), false);
  }
});


test('a failed resumed detail does not lock fresh Mini conversations after config succeeds', async () => {
  const fixture = pageHarness({ get: async (path) => { if (path === '/ai-ops/session') throw new Error('session deleted'); } });
  await fixture.load();
  assert.equal(fixture.state.loaded, true);
  assert.equal(fixture.state.error, 'session deleted');
  assert.equal(fixture.state.detail, null);
  fixture.newSession();
  assert.equal(fixture.env.selectedRef.current, 0);
  assert.equal(fixture.state.loaded, true);
  assert.equal(aiReadMiniSession(7, fixture.storage), 10, 'failed detail cannot overwrite preference');
});
