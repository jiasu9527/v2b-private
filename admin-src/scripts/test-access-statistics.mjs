import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { createRequire } from 'node:module';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

const require = createRequire(import.meta.url);
const source = await readFile(new URL('../src/pages/AccessStatisticsPage.tsx', import.meta.url), 'utf8');
const compiled = ts.transpileModule(source, { compilerOptions: {
  module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX, esModuleInterop: true,
  target: ts.ScriptTarget.ES2020,
} }).outputText;
const tick = () => new Promise(resolve => setImmediate(resolve));

// Exercise component request/state behavior without adding a browser framework.
// JSX is represented as a tree; Ant components do not execute in this harness.
function mount() {
  const slots = []; const effects = []; const requests = []; const timers = new Map();
  let index = 0; let nextTimer = 0; let tree;
  const useSlot = (create) => { const current = index++; if (!(current in slots)) slots[current] = create(); return current; };
  const react = {
    useState(initial) {
      const current = useSlot(() => typeof initial === 'function' ? initial() : initial);
      return [slots[current], (value) => { slots[current] = typeof value === 'function' ? value(slots[current]) : value; }];
    },
    useRef(initial) { const current = useSlot(() => ({ current: initial })); return slots[current]; },
    useEffect(effect) { const current = useSlot(() => false); if (!slots[current]) { slots[current] = true; effects.push(effect); } },
  };
  const form = Object.assign(() => null, { Item: 'Form.Item' });
  const antd = Object.fromEntries(['Alert', 'Button', 'Card', 'Col', 'Input', 'Row', 'Select', 'Space', 'Statistic', 'Table', 'Tag'].map(name => [name, name]));
  Object.assign(antd, { Form: form, DatePicker: { RangePicker: 'DatePicker.RangePicker' }, Typography: { Paragraph: 'Typography.Paragraph', Text: 'Typography.Text' } });
  const jsx = (type, props) => ({ type, props: props || {} });
  const modules = {
    react,
    'react/jsx-runtime': { jsx, jsxs: jsx, Fragment: 'Fragment' },
    antd,
    '@ant-design/icons': { ReloadOutlined: 'ReloadOutlined', SearchOutlined: 'SearchOutlined' },
    dayjs: require('dayjs'),
    '../lib/api': {
      apiGet(path, params, options) {
        const request = { path, params, options };
        requests.push(request);
        return new Promise((resolve, reject) => {
          request.resolve = value => resolve({ data: value }); request.reject = reject;
          options?.signal?.addEventListener('abort', () => reject(new Error('aborted')), { once: true });
        });
      },
      unwrapData: response => response.data,
    },
    './AccessStatisticsPage.css': {},
  };
  const exports = {};
  vm.runInNewContext(compiled, {
    exports, require(name) { assert.ok(name in modules, `unexpected import ${name}`); return modules[name]; },
    AbortController, Error, Intl,
    window: {
      setTimeout(fn, ms) { const timer = ++nextTimer; timers.set(timer, { fn, ms }); return timer; },
      clearTimeout(timer) { timers.delete(timer); },
    },
  });
  const render = () => { index = 0; tree = exports.default(); return tree; };
  render();
  const cleanups = effects.map(effect => effect());
  const walk = (node, predicate) => {
    if (!node) return [];
    if (Array.isArray(node)) return node.flatMap(item => walk(item, predicate));
    if (typeof node !== 'object') return [];
    return [...(predicate(node) ? [node] : []), ...walk(node.props?.children, predicate), ...walk(node.props?.extra, predicate)];
  };
  const text = (node) => Array.isArray(node) ? node.map(text).join('') : node && typeof node === 'object' ? text(node.props?.children) : String(node ?? '');
  return {
    requests, timers, render,
    find(predicate) { return walk(tree, predicate); },
    button(label) { const matches = walk(tree, node => node.type === 'Button' && text(node) === label); assert.equal(matches.length, 1, label); return matches[0].props; },
    routeTable() { return walk(tree, node => node.type === 'Table' && node.props.scroll?.x === 1050)[0].props; },
    async settle() { await tick(); render(); },
    close() { for (const cleanup of cleanups) cleanup?.(); },
  };
}

const row = { event_id: 'event-one', node_type: 'v2node', node_id: 14, user_id: 7, occurred_at: 100, client_ip: '', target_host: 'example.com', target_ip: '', target_port: 443, network: 'tcp', result: 'route_attempt' };
const page = (items = [row], extra = {}) => ({ items, page_size: 20, has_more: true, next_cursor: 'cursor-two', total_exact: false, ...extra });
const summary = { events: 8, users: 2, known_domain_events: 8, unknown_domain_events: 0, top_domains: [], top_ips: [] };
async function ready(harness) {
  harness.requests.find(r => r.path.endsWith('/nodes')).resolve([]);
  harness.requests.find(r => r.path.endsWith('/events')).resolve(page());
  await harness.settle();
}

test('initial load only fetches recent rows and coverage, not whole-range summary', async () => {
  const h = mount();
  try {
    assert.deepEqual(h.requests.map(r => r.path), ['/observation/nodes', '/observation/events']);
    const events = h.requests[1];
    assert.equal(events.params.to - events.params.from, 15 * 60);
    assert.equal(events.params.page, 1);
    assert.equal(events.params.cursor, undefined);
    await ready(h);
    assert.equal(h.routeTable().dataSource.length, 1);
    assert.equal(h.button('计算当前范围统计').disabled, false);
  } finally { h.close(); }
});

test('a failed summary never discards loaded rows or disables cursor pagination', async () => {
  const h = mount();
  try {
    await ready(h);
    h.button('计算当前范围统计').onClick();
    const request = h.requests.at(-1);
    assert.equal(request.path, '/observation/summary');
    request.reject(new Error('database busy'));
    await h.settle();
    assert.equal(h.routeTable().dataSource[0].event_id, 'event-one');
    assert.equal(h.button('下一页').disabled, false);
    assert.equal(h.find(node => node.type === 'Alert' && node.props.message === '汇总暂时无法计算').length, 1);
    assert.equal(h.find(node => node.type === 'Statistic').length, 0);
  } finally { h.close(); }
});

test('cursor next/previous requests retain summary without recomputing it', async () => {
  const h = mount();
  try {
    await ready(h);
    h.button('计算当前范围统计').onClick();
    h.requests.at(-1).resolve(summary);
    await h.settle();
    h.button('下一页').onClick();
    let request = h.requests.at(-1);
    assert.equal(request.path, '/observation/events');
    assert.equal(request.params.cursor, 'cursor-two');
    request.resolve(page([{ ...row, event_id: 'event-two' }], { has_more: false, next_cursor: '' }));
    await h.settle();
    assert.equal(h.routeTable().dataSource[0].event_id, 'event-two');
    assert.equal(h.button('下一页').disabled, true);
    h.button('上一页').onClick();
    request = h.requests.at(-1);
    assert.equal(request.params.cursor, undefined);
    request.resolve(page());
    await h.settle();
    assert.equal(h.requests.filter(r => r.path.endsWith('/summary')).length, 1);
    assert.equal(h.find(node => node.type === 'Statistic').length, 4);
  } finally { h.close(); }
});

test('new search aborts old summary and prevents a stale aggregate from appearing', async () => {
  const h = mount();
  try {
    await ready(h);
    h.button('计算当前范围统计').onClick();
    const staleSummary = h.requests.at(-1);
    h.button('最近 15 分钟').onClick();
    assert.equal(staleSummary.options.signal.aborted, true);
    staleSummary.resolve(summary);
    h.requests.at(-1).resolve(page([]));
    await h.settle();
    assert.equal(h.find(node => node.type === 'Statistic').length, 0);
    assert.equal(h.routeTable().dataSource.length, 0);
    assert.equal(h.find(node => node.type === 'Alert' && node.props.message === '汇总暂时无法计算').length, 0);
  } finally { h.close(); }
});

test('summary timeout is explicit and does not turn unavailable counts into zero', async () => {
  const h = mount();
  try {
    await ready(h);
    h.button('计算当前范围统计').onClick();
    const timeout = [...h.timers.values()].find(timer => timer.ms === 30000);
    assert.ok(timeout);
    timeout.fn();
    await h.settle();
    assert.equal(h.routeTable().dataSource.length, 1);
    const alert = h.find(node => node.type === 'Alert' && node.props.message === '汇总暂时无法计算')[0];
    assert.match(alert.props.description, /统计计算超时/);
    assert.match(alert.props.description, /不表示为 0/);
    assert.equal(h.find(node => node.type === 'Statistic').length, 0);
  } finally { h.close(); }
});

test('failed next page preserves its current rows and successful page position', async () => {
  const h = mount();
  try {
    await ready(h);
    h.button('下一页').onClick();
    h.requests.at(-1).reject(new Error('read timeout'));
    await h.settle();
    assert.equal(h.routeTable().dataSource[0].event_id, 'event-one');
    assert.equal(h.button('上一页').disabled, true);
    assert.equal(h.button('下一页').disabled, false);
    h.button('下一页').onClick();
    assert.equal(h.requests.at(-1).params.cursor, 'cursor-two');
  } finally { h.close(); }
});

test('changing page size restarts cursor pagination without repeating the summary', async () => {
  const h = mount();
  try {
    await ready(h);
    h.button('计算当前范围统计').onClick();
    h.requests.at(-1).resolve(summary);
    await h.settle();
    const size = h.find(node => node.type === 'Select' && node.props['aria-label'] === '每页条数')[0].props;
    size.onChange(50);
    const request = h.requests.at(-1);
    assert.equal(request.params.page_size, 50);
    assert.equal(request.params.page, 1);
    assert.equal(request.params.cursor, undefined);
    request.resolve(page([row], { page_size: 50 }));
    await h.settle();
    assert.equal(h.requests.filter(r => r.path.endsWith('/summary')).length, 1);
    assert.equal(h.find(node => node.type === 'Statistic').length, 4);
  } finally { h.close(); }
});
