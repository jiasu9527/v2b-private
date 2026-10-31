import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { test } from 'node:test';
import ts from 'typescript';

const compile = (source) => ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2020 } }).outputText;
const model = {};
new Function('exports', compile(await readFile(new URL('../src/pages/aiOpsModel.ts', import.meta.url), 'utf8')))(model);
const source = await readFile(new URL('../src/pages/aiOpsChatScroll.ts', import.meta.url), 'utf8');
const page = await readFile(new URL('../src/pages/AIOpsPage.tsx', import.meta.url), 'utf8');

function harness({ hidden = false, delayedMount = false, resizeObserver = true } = {}) {
  const jobs = new Map();
  let sequence = 0, resize, disconnected = 0, writes = 0, top = 0;
  const observed = [];
  const pane = {
    clientHeight: hidden ? 0 : 500, scrollHeight: hidden ? 0 : 4000,
    get scrollTop() { return top; },
    set scrollTop(value) { writes++; top = Math.max(0, Math.min(value, this.scrollHeight - this.clientHeight)); },
  };
  let node = delayedMount ? null : pane;
  const follow = { current: false };
  const window = { requestAnimationFrame(callback) { jobs.set(++sequence, callback); return sequence; }, cancelAnimationFrame(id) { jobs.delete(id); } };
  const ResizeObserver = resizeObserver ? class {
    constructor(callback) { resize = callback; }
    observe(element) { observed.push(element); }
    disconnect() { disconnected++; }
  } : undefined;
  const helpers = {};
  new Function('require', 'exports', 'window', 'ResizeObserver', compile(source))(() => model, helpers, window, ResizeObserver);
  const activate = () => helpers.watchAIChatScroll(() => node, follow);
  let scroll = activate();
  const flush = () => { const callbacks = [...jobs.values()]; jobs.clear(); callbacks.forEach((callback) => callback()); };
  return {
    pane, follow, jobs, observed, flush,
    get scroll() { return scroll; }, get writes() { return writes; }, get disconnected() { return disconnected; },
    resize() { resize?.(); }, mount() { node = pane; },
    readHistory() { pane.scrollTop = 120; scroll.onScroll(); },
    reactivate() { scroll.dispose(); scroll = activate(); },
  };
}

test('opening chat restores latest content after layout, including a previously scrolled-up session', () => {
  const h = harness();
  assert.equal(h.writes, 0);
  h.flush();
  assert.equal(h.pane.scrollTop, 3500);
  h.readHistory();
  assert.equal(h.follow.current, false);
  h.reactivate();
  h.scroll.onScroll(); // Layout scroll from the previous hidden tab is ignored.
  assert.equal(h.follow.current, true);
  h.flush();
  assert.equal(h.pane.scrollTop, 3500);
});

test('hidden pane geometry never writes zero or disables following; showing the pane restores latest', () => {
  const h = harness({ hidden: true });
  h.flush(); h.scroll.onScroll();
  assert.equal(h.writes, 0);
  assert.equal(h.follow.current, true);
  assert.equal(h.observed.length, 1);
  h.pane.clientHeight = 500; h.pane.scrollHeight = 4700;
  h.resize(); h.flush();
  assert.equal(h.pane.scrollTop, 4200);
  h.pane.clientHeight = 0; h.pane.scrollHeight = 0;
  h.scroll.refresh(); h.resize(); h.flush(); h.scroll.onScroll();
  assert.equal(h.writes, 1);
  assert.equal(h.follow.current, true);
});

test('reading history is not interrupted by snapshots, streamed tokens, or viewport resize', () => {
  const h = harness(); h.flush(); h.readHistory();
  h.pane.scrollHeight += 2000;
  h.scroll.refresh(); h.resize(); h.flush();
  assert.equal(h.pane.scrollTop, 120);
  h.pane.clientHeight = 300; h.resize(); h.flush();
  assert.equal(h.pane.scrollTop, 120);
  assert.equal(h.follow.current, false);
  h.pane.scrollTop = h.pane.scrollHeight; h.scroll.onScroll();
  assert.equal(h.follow.current, true);
  h.pane.scrollHeight += 500; h.scroll.refresh(); h.flush();
  assert.equal(h.pane.scrollTop, 6200);
});

test('a reader scrolling up before a scheduled update cancels following', () => {
  const h = harness(); h.flush();
  h.pane.scrollHeight += 1000; h.scroll.refresh();
  h.readHistory(); h.flush();
  assert.equal(h.pane.scrollTop, 120);
});

test('keyboard and viewport changes keep the newest message visible when following', () => {
  const h = harness(); h.flush();
  h.pane.clientHeight = 240; h.resize(); h.flush();
  assert.equal(h.pane.scrollTop, 3760);
  h.pane.clientHeight = 650; h.resize(); h.flush();
  assert.equal(h.pane.scrollTop, 3350);
});

test('switching menus cancels stale frames and observers before a hidden pane can be changed', () => {
  const h = harness(); h.flush();
  h.scroll.refresh(); assert.equal(h.jobs.size, 1);
  h.scroll.dispose(); assert.equal(h.jobs.size, 0);
  const writes = h.writes;
  h.pane.clientHeight = 0; h.pane.scrollHeight = 0;
  h.resize(); h.scroll.refresh(); h.scroll.onScroll(); h.flush();
  assert.equal(h.writes, writes);
  assert.equal(h.jobs.size, 0);
  assert.ok(h.disconnected > 0);
});

test('delayed tab mounting attaches on the next data refresh; repeated updates coalesce', () => {
  const h = harness({ delayedMount: true }); h.flush();
  assert.equal(h.writes, 0);
  h.mount(); h.scroll.refresh(); h.scroll.refresh(); h.scroll.refresh();
  assert.equal(h.jobs.size, 1);
  h.flush();
  assert.equal(h.pane.scrollTop, 3500);
  assert.equal(h.observed.length, 1);
});

test('browsers without ResizeObserver retain initial restoration and streaming follow', () => {
  const h = harness({ resizeObserver: false }); h.flush();
  assert.equal(h.pane.scrollTop, 3500);
  h.pane.scrollHeight += 1000; h.scroll.refresh(); h.flush();
  assert.equal(h.pane.scrollTop, 4500);
  h.scroll.dispose();
});

test('page activates scrolling for all tab and session changes, not clocks or drawer toggles', () => {
  assert.match(page, /if \(tab !== 'chat'\) return;\s+const scroll = watchAIChatScroll/);
  assert.match(page, /scroll\.dispose\(\); chatScroll\.current = null; \};\s+\}, \[tab, selectedID\]\)/);
  assert.match(page, /chatScroll\.current\?\.refresh\(\);\s+\}, \[tab, selectedID, detail\?\.messages, detail\?\.actions, visibleDraft\?\.draft_content\]\)/);
  assert.match(page, /else if \(tab !== 'chat'\) setTab\('chat'\)/);
  assert.doesNotMatch(page, /pane\.scrollTop\s*=/);
});


test('a late-mounted chat pane notifies the controller without waiting for a poll', () => {
  const callback = page.match(/const mountMessageViewport = useCallback\(\(pane: HTMLDivElement \| null\) => \{([\s\S]*?)\}, \[\]\);/);
  assert.ok(callback, 'stable mount callback exists');
  const messageViewport = { current: null };
  let refreshes = 0;
  const chatScroll = { current: { refresh() { refreshes++; } } };
  const mount = new Function('pane', 'messageViewport', 'chatScroll', callback[1]);
  const node = {};
  mount(node, messageViewport, chatScroll);
  assert.equal(messageViewport.current, node);
  assert.equal(refreshes, 1);
  mount(null, messageViewport, chatScroll);
  assert.equal(messageViewport.current, null);
  assert.equal(refreshes, 1);
  chatScroll.current = null;
  mount(node, messageViewport, chatScroll);
  assert.equal(messageViewport.current, node);
  assert.match(page, /className="ai-ops-messages" ref=\{mountMessageViewport\}/);
});
