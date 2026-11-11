import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { createContext, runInContext } from 'node:vm';
import { test } from 'node:test';
import dayjs from 'dayjs';
import ts from 'typescript';

const source = await readFile(new URL('../src/pages/NewAPIConversations.tsx', import.meta.url), 'utf8');
const integration = await readFile(new URL('../src/pages/NewAPIIntegrationPage.tsx', import.meta.url), 'utf8');
const styles = await readFile(new URL('../src/pages/NewAPIConversations.css', import.meta.url), 'utf8');
const compiled = ts.transpileModule(source, { compilerOptions: {
  module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2020, jsx: ts.JsxEmit.React, esModuleInterop: true,
} }).outputText;
const tick = () => new Promise((resolve) => setImmediate(resolve));
const plain = (value) => JSON.parse(JSON.stringify(value));
const filter = (extra = {}) => ({ user_id: '', email: '', keyword: '', model: '', ...extra });
const row = (id = 1) => ({ id, user_id: 7, user_email: 'fixture@example.test', user_deleted: false,
  title: `会话 ${id}`, mode: 'character', model: 'fixture-model', character_id: 3, character_name: '角色',
  message_count: 3, created_at: 1800000000, updated_at: 1800000100, last_message_at: 1800000100 });
const page = (items = [row()], extra = {}) => ({ items, total: items.length, current: 1, page_size: 20, ...extra });
const msg = (id, conversationID = 1, content = `消息 ${id}`) => ({ id, conversation_id: conversationID, role: id % 2 ? 'assistant' : 'user', content, status: 'complete', created_at: 1800000000 + id, updated_at: 1800000000 + id });
const detail = (id = 1, extra = {}) => ({
  conversation: { id, title: `会话 ${id}`, mode: 'character', model: 'fixture-model', character_id: 3, persona: '用户角色设定', parent_id: 0, created_at: 1800000000, updated_at: 1800000100 },
  user_id: 7, user_email: 'fixture@example.test', user_deleted: false,
  character_snapshot: { name: '角色', description: '描述', personality: '性格', scenario: '场景', character_book: { entries: [{ name: '背景', content: '角色知识' }] } },
  messages: [msg(4, id), msg(5, id)], has_more: true, next_before_id: 4, ...extra,
});

function runtime({ mount = true } = {}) {
  const slots = [], effects = [], layouts = [], requests = [];
  let cursor = 0, tree;
  const enqueueEffect = (queue, callback, deps) => {
    const index = cursor++, previous = slots[index];
    if (!previous || deps.some((value, i) => !Object.is(value, previous.deps[i]))) {
      previous?.cleanup?.();
      const slot = { deps, cleanup: undefined }; slots[index] = slot;
      queue.push(() => { slot.cleanup = callback(); });
    }
  };
  const react = {
    createElement: (type, props, ...children) => ({ type, props: { ...props, children } }),
    useState(initial) {
      const index = cursor++;
      if (!(index in slots)) slots[index] = typeof initial === 'function' ? initial() : initial;
      return [slots[index], (next) => { slots[index] = typeof next === 'function' ? next(slots[index]) : next; }];
    },
    useRef(initial) { const index = cursor++; if (!(index in slots)) slots[index] = { current: initial }; return slots[index]; },
    useEffect: (callback, deps) => enqueueEffect(effects, callback, deps),
    useLayoutEffect: (callback, deps) => enqueueEffect(layouts, callback, deps),
  };
  const antd = Object.fromEntries(['Alert', 'Button', 'Card', 'Collapse', 'DatePicker', 'Descriptions', 'Drawer', 'Empty', 'Form', 'Input', 'Select', 'Space', 'Spin', 'Table', 'Tag'].map((name) => [name, name]));
  antd.Form = Object.assign(function Form() {}, { Item: 'Form.Item' });
  antd.Descriptions = Object.assign(function Descriptions() {}, { Item: 'Descriptions.Item' });
  antd.Empty = Object.assign(function Empty() {}, { PRESENTED_IMAGE_SIMPLE: 'empty' });
  antd.Typography = { Text: 'Text' };
  const api = { apiGet(path, params, options) {
    let resolve, reject;
    const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
    requests.push({ path, params: plain(params), options, resolve: (data) => resolve({ data }), reject });
    return promise;
  } };
  const context = createContext({ exports: {}, console, Intl, AbortController, require: (name) => {
    if (name === 'react') return react;
    if (name === 'antd') return antd;
    if (name === 'dayjs') return dayjs;
    if (name === '../lib/api') return api;
    return {};
  } });
  runInContext(compiled, context);
  const render = () => {
    cursor = 0; tree = context.exports.default();
    while (layouts.length) layouts.shift()();
    return tree;
  };
  const nodes = () => {
    const found = [], seen = new Set();
    const visit = (value) => {
      if (!value || typeof value !== 'object' || seen.has(value)) return;
      seen.add(value);
      if ('type' in value && value.props) found.push(value);
      for (const entry of Object.values(value)) visit(entry);
    };
    visit(tree); return found;
  };
  const table = () => nodes().find((node) => node.type === 'Table');
  const button = (label) => nodes().find((node) => node.type === 'Button' && node.props.children[0] === label);
  const input = (label) => nodes().find((node) => node.props['aria-label'] === label);
  const change = (label, value) => { input(label).props.onChange({ target: { value } }); render(); };
  const query = () => nodes().find((node) => node.type === antd.Form).props.onFinish();
  const open = (id) => { const action = table().props.columns.find((column) => column.key === 'action'); return action.render(null, row(id)).props.onClick(); };
  const drawer = () => nodes().find((node) => node.type === 'Drawer');
  const messages = () => nodes().filter((node) => node.type === 'article');
  const resolve = async (index, data) => { requests[index].resolve(data); await tick(); render(); };
  const unmount = () => { for (const slot of slots) slot?.cleanup?.(); };
  if (mount) { render(); while (effects.length) effects.shift()(); render(); }
  return { exports: context.exports, render, nodes, table, button, input, change, query, open, drawer, messages, requests, resolve, unmount };
}

test('query whitelists filters, validates IDs/type/time range, and preserves the exclusive end', () => {
  const { adminConversationQuery: query } = runtime({ mount: false }).exports;
  assert.deepEqual(plain(query(filter({ user_id: ' 7 ', email: ' a@example.test ', keyword: ' 故事 ', model: ' model ', mode: 'character', start_at: 1800000000, end_at: 1800000060, auth_data: 'not-allowed' }), 2, 50)), {
    current: 2, page_size: 50, user_id: 7, email: 'a@example.test', keyword: '故事', model: 'model', mode: 'character', start_at: 1800000000, end_at: 1800000060,
  });
  for (const id of ['0', '-1', '1.5', '1e3', '9007199254740992']) assert.throws(() => query(filter({ user_id: id }), 1, 20), /正整数/);
  for (const extra of [{ mode: 'admin' }, { keyword: 'a'.repeat(101) }, { model: 'a'.repeat(201) }, { email: 'a'.repeat(255) }, { start_at: 0 }, { end_at: NaN }, { start_at: 10, end_at: 10 }]) assert.throws(() => query(filter(extra), 1, 20));
  assert.throws(() => query(filter(), 100001, 20));
  assert.throws(() => query(filter(), 1, 101));
});

test('strict response parsing rejects malformed pages and cross-conversation messages', () => {
  const ex = runtime({ mount: false }).exports;
  assert.equal(ex.adminConversationPage(page()).items.length, 1);
  for (const value of [null, true, {}, page([row()], { total: -1 }), page([{ ...row(), user_id: 0 }]), page([{ ...row(), user_deleted: undefined }])]) assert.throws(() => ex.adminConversationPage(value));
  assert.throws(() => ex.adminConversationDetail(detail(2), 1), /归属/);
  assert.throws(() => ex.adminConversationDetail(detail(1, { messages: [msg(2, 3)] }), 1), /归属/);
  assert.throws(() => ex.adminMessagePage({ items: [msg(2), msg(2)], has_more: false, next_before_id: 0 }, 1), /归属/);
  assert.throws(() => ex.adminMessagePage({ items: [msg(4)], has_more: false, next_before_id: 0 }, 1, 4), /归属/);
  assert.throws(() => ex.adminMessagePage({ items: [msg(3)], has_more: true, next_before_id: 2 }, 1), /游标/);
});

test('message pages and merged history remain chronological and do not mutate inputs', () => {
  const ex = runtime({ mount: false }).exports;
  const original = [msg(3), msg(2)];
  const decoded = ex.adminMessagePage({ items: original, has_more: true, next_before_id: 2 }, 1, 4);
  assert.deepEqual(plain(decoded.items.map((item) => item.id)), [2, 3]);
  assert.deepEqual(original.map((item) => item.id), [3, 2]);
  const merged = ex.mergeAdminMessages([msg(4), msg(5)], [msg(3), msg(4, 1, 'older copy')]);
  assert.deepEqual(plain(merged.map((item) => item.id)), [3, 4, 5]);
  assert.equal(merged[1].content, '消息 4');
});

test('integration mounts the read-only viewer only while the chat tab is selected', () => {
  assert.match(integration, /import NewAPIConversations from '\.\/NewAPIConversations'/);
  assert.match(integration, /key: 'conversations', label: '用户聊天', children: tab === 'conversations' \? <NewAPIConversations \/> : null/);
  assert.doesNotMatch(source, /apiJsonPost|apiPost|apiWrite|dangerouslySetInnerHTML|innerHTML|markdown|localStorage|sessionStorage/);
  const r = runtime();
  assert.equal(r.requests.length, 1);
  assert.equal(r.requests[0].path, '/new-api/conversations');
  assert.equal(r.requests[0].options.cache, 'no-store');
  assert(r.requests[0].options.signal instanceof AbortSignal);
});

test('loading, query failure and successful empty results are visibly distinct', async () => {
  const r = runtime();
  assert.equal(r.table().props.loading, true);
  assert.match(r.table().props.locale.emptyText, /正在读取/);
  r.requests[0].reject(new Error('没有管理员权限')); await tick(); r.render();
  assert.equal(r.table().props.loading, false);
  assert.equal(r.table().props.dataSource.length, 0);
  assert.match(r.nodes().find((node) => node.type === 'Alert').props.message, /管理员权限/);
  r.query(); await r.resolve(1, page([]));
  assert.match(r.table().props.locale.emptyText, /没有符合条件/);
});

test('stale list responses cannot overwrite newer filters or page results', async () => {
  const r = runtime();
  r.change('聊天用户 ID', '8'); r.query(); r.render();
  assert.equal(r.requests[0].options.signal.aborted, true);
  await r.resolve(1, page([{ ...row(8), user_id: 8 }]));
  await r.resolve(0, page([row(1)]));
  assert.equal(r.table().props.dataSource[0].id, 8);
  r.table().props.pagination.onChange(2, 50);
  assert.deepEqual(r.requests[2].params, { current: 2, page_size: 50, user_id: 8 });
  await r.resolve(2, page([row(9)], { current: 2, page_size: 50, total: 100 }));
  assert.equal(r.table().props.pagination.current, 2);
});

test('invalid filter submission cancels in-flight reads and never silently queries all users', async () => {
  const r = runtime();
  r.change('聊天用户 ID', '-1'); r.query(); r.render();
  assert.equal(r.requests.length, 1);
  assert.equal(r.requests[0].options.signal.aborted, true);
  await r.resolve(0, page([row()]));
  assert.equal(r.table().props.dataSource.length, 0);
  assert.match(r.nodes().find((node) => node.type === 'Alert').props.message, /正整数/);
});

test('mismatched returned page is shown as a query error, never mislabeled as requested page', async () => {
  const r = runtime(); await r.resolve(0, page());
  r.table().props.pagination.onChange(2, 20);
  await r.resolve(1, page());
  assert.equal(r.table().props.dataSource.length, 0);
  assert.match(r.nodes().find((node) => node.type === 'Alert').props.message, /页码与查询不一致/);
});

test('unsubmitted draft filters do not leak into pagination and time inputs use minute precision', async () => {
  const r = runtime(); await r.resolve(0, page());
  r.change('聊天用户邮箱', 'not-yet-applied@example.test');
  r.table().props.pagination.onChange(2, 20);
  assert.deepEqual(r.requests[1].params, { current: 2, page_size: 20 });
  r.input('聊天创建开始时间').props.onChange(dayjs('2026-11-11T12:34:59'));
  r.render();
  assert.equal(r.input('聊天创建开始时间').props.value.second(), 0);
  assert.equal(r.input('聊天创建开始时间').props.format, 'YYYY-MM-DD HH:mm');
});

test('switching conversation ignores older detail responses and closing clears all content', async () => {
  const r = runtime(); await r.resolve(0, page([row(1), row(2)]));
  r.open(1); r.render(); assert.equal(r.drawer().props.open, true);
  assert.equal(r.messages().length, 0);
  r.open(2); r.render(); assert.equal(r.requests[1].options.signal.aborted, true);
  await r.resolve(2, detail(2)); await r.resolve(1, detail(1));
  assert.match(r.drawer().props.title, /#2/);
  assert.equal(r.messages().length, 2);
  assert.equal(r.nodes().find((node) => node.props.detail)?.props.detail.conversation.id, 2);
  r.drawer().props.onClose(); r.render();
  assert.equal(r.drawer().props.open, false);
  assert.equal(r.messages().length, 0);
});

test('loading older preserves scroll position, chronological order and uses one guarded GET', async () => {
  const r = runtime(); await r.resolve(0, page()); r.open(1); await r.resolve(1, detail());
  const container = r.input('聊天消息记录');
  const element = { scrollHeight: 800, scrollTop: 50 }; container.props.ref.current = element;
  r.button('加载更早消息').props.onClick();
  r.button('加载更早消息').props.onClick();
  assert.equal(r.requests.length, 3);
  assert.deepEqual(r.requests[2].params, { id: 1, before_id: 4, page_size: 100 });
  r.requests[2].resolve({ items: [msg(3), msg(2)], has_more: false, next_before_id: 0 });
  await tick(); element.scrollHeight = 1200; r.render();
  assert.equal(element.scrollTop, 450);
  assert.equal(r.messages().length, 4);
  assert.equal(r.button('加载更早消息').props.disabled, true);
  const texts = r.messages().map((node) => node.props.children[1].props.value);
  assert.deepEqual(texts, ['消息 2', '消息 3', '消息 4', '消息 5']);
});

test('older messages cannot reappear after closing, switching or unmounting', async () => {
  const r = runtime(); await r.resolve(0, page([row(1), row(2)])); r.open(1); await r.resolve(1, detail());
  r.button('加载更早消息').props.onClick(); r.open(2); r.render();
  assert.equal(r.requests[2].options.signal.aborted, true);
  await r.resolve(3, detail(2)); await r.resolve(2, { items: [msg(2)], has_more: false, next_before_id: 0 });
  assert.equal(r.messages().length, 2);
  assert.equal(r.nodes().find((node) => node.props.detail)?.props.detail.conversation.id, 2);
  r.button('加载更早消息').props.onClick(); r.drawer().props.onClose(); r.render();
  await r.resolve(4, { items: [msg(2, 2)], has_more: false, next_before_id: 0 });
  assert.equal(r.messages().length, 0);
  r.open(1); r.unmount();
  assert.equal(r.requests[5].options.signal.aborted, true);
  r.requests[5].resolve(detail(1)); await tick(); r.render();
  assert.equal(r.messages().length, 0);
});

test('detail errors show a retry and history errors retain the successfully loaded messages', async () => {
  const r = runtime(); await r.resolve(0, page()); r.open(1);
  r.requests[1].reject(new Error('会话不存在')); await tick(); r.render();
  assert.match(r.nodes().find((node) => node.type === 'Alert').props.message, /不存在/);
  r.nodes().find((node) => node.type === 'Alert').props.action.props.onClick(); await r.resolve(2, detail());
  r.button('加载更早消息').props.onClick(); r.requests[3].reject(new Error('读取历史失败')); await tick(); r.render();
  assert.equal(r.messages().length, 2);
  assert.match(r.nodes().find((node) => node.type === 'Alert').props.message, /历史失败/);
  assert.equal(r.button('加载更早消息').props.loading, false);
});

test('HTML-looking messages and snapshot content remain plain text without auto-loaded media or arbitrary extensions', async () => {
  const r = runtime(); await r.resolve(0, page()); r.open(1);
  const hostile = '<img src="https://tracker.invalid" onerror="alert(1)"><script>secret()</script>';
  const record = detail(1, { messages: [msg(1, 1, hostile)], has_more: false, next_before_id: 0 });
  record.character_snapshot.description = hostile;
  record.character_snapshot.extensions = { avatar: 'https://tracker.invalid', api_key: 'must-not-render' };
  await r.resolve(1, record);
  const textNode = r.messages()[0].props.children[1];
  const rendered = textNode.type(textNode.props);
  assert.equal(rendered.type, 'div');
  assert.equal(rendered.props.children[0], hostile);
  assert.equal(rendered.props.dangerouslySetInnerHTML, undefined);
  const contextNode = r.nodes().find((node) => node.props.detail);
  const collapse = contextNode.type(contextNode.props);
  assert.equal(collapse.type, 'Collapse');
  assert.equal(collapse.props.defaultActiveKey, undefined);
  assert.equal(JSON.stringify(collapse).includes('must-not-render'), false);
  assert.equal(r.requests.length, 2, 'displaying records must not request remote media or models');
});

test('mobile layout wraps filters, text and metadata while the table can scroll horizontally', () => {
  const r = runtime();
  assert(r.table().props.scroll.x >= 1000);
  assert.equal(r.drawer().props.width, 'min(880px, 100vw)');
  assert.match(styles, /overflow-wrap:\s*anywhere/);
  assert.match(styles, /white-space:\s*pre-wrap/);
  assert.match(styles, /@media \(max-width: 576px\)/);
  assert.match(styles, /grid-template-columns: minmax\(0, 1fr\)/);
  assert.match(styles, /overflow-anchor: none/);
});
