import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { test } from 'node:test';
import ts from 'typescript';

const source = await readFile(new URL('../src/pages/AIOpsComposer.tsx', import.meta.url), 'utf8');
const page = await readFile(new URL('../src/pages/AIOpsPage.tsx', import.meta.url), 'utf8');
const miniCss = await readFile(new URL('../src/TelegramMiniApp.css', import.meta.url), 'utf8');
const compile = (value) => ts.transpileModule(value, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2020, jsx: ts.JsxEmit.React, esModuleInterop: true } }).outputText;
const model = {};
new Function('exports', compile(await readFile(new URL('../src/pages/aiOpsModel.ts', import.meta.url), 'utf8')))(model);

// Run the actual component and handlers with a small synchronous hook host.
// Browser performance/DOM behavior is checked separately against the built UI.
function composer(initial = '', options = {}, nativeSizing = false) {
  const slots = []; let cursor = 0; const sent = []; let stopped = 0;
  const react = {
    createElement: (type, props, ...children) => ({ type, props: props || {}, children: children.flat() }),
    forwardRef: (render) => render,
    useState(value) { const slot = cursor++; if (!(slot in slots)) slots[slot] = typeof value === 'function' ? value() : value; return [slots[slot], (next) => { slots[slot] = next; }]; },
    useRef(value) { const slot = cursor++; if (!(slot in slots)) slots[slot] = { current: value }; return slots[slot]; },
    useImperativeHandle(ref, create) { ref.current = create(); },
    useEffect() {},
  };
  const exports = {};
  new Function('require', 'exports', 'CSS', compile(source))((name) => {
    if (name === 'react') return react;
    if (name === 'antd') return { Button: 'button', Input: { TextArea: 'textarea' }, Typography: { Text: 'text' } };
    if (name === '@ant-design/icons') return { SendOutlined: 'send-icon', StopOutlined: 'stop-icon' };
    if (name === './aiOpsModel') return model;
    if (name === './aiOpsComposerSize') return { watchMiniComposerSize() { assert.fail('size effects require the DOM harness'); } };
    assert.fail(name);
  }, exports, { supports: (property, value) => nativeSizing && property === 'field-sizing' && value === 'content' });
  const ref = { current: null }, draftRef = { current: initial };
  const props = { draftRef, miniApp: true, compact: true, loaded: true, busy: '', sessionBusy: false, unavailable: false, sendBlocked: false, isAdmin: true, onSend: (content) => sent.push(content), onStop: () => stopped++, ...options };
  let tree;
  const render = () => { cursor = 0; tree = exports.default(props, ref); return tree; };
  const find = (type, node = tree) => node?.type === type ? node : node?.children?.map((child) => typeof child === 'object' ? find(type, child) : null).find(Boolean);
  const key = (extra = {}) => {
    let prevented = false;
    find('textarea').props.onKeyDown({ key: 'Enter', nativeEvent: {}, keyCode: 13, preventDefault: () => { prevented = true; }, ...extra });
    return prevented;
  };
  render();
  return { draftRef, props, ref, sent, render, find, key, stopped: () => stopped };
}

test('typing stays in composer state with exact untrimmed ref content, not parent page state', () => {
  const instance = composer();
  const text = '  安装节点\npassword: " keep spaces "\n';
  for (let end = 1; end <= text.length; end++) {
    instance.find('textarea').props.onChange({ target: { value: text.slice(0, end) } });
    assert.equal(instance.draftRef.current, text.slice(0, end));
    instance.render();
  }
  assert.equal(instance.find('textarea').props.value, text);
  assert.equal(instance.sent.length, 0);
  assert.match(page, /const draft = useRef\(''\)/);
  assert.doesNotMatch(page, /\[draft, setDraft\]|value=\{draft\}|onChange=\{.*setDraft/);
  assert.match(page, /<AIOpsComposer ref=\{composer\} draftRef=\{draft\}/);
});

test('Enter sends exact current input once; failed or pending send does not clear it', () => {
  const instance = composer('  a\npassword: value with space \n');
  assert.equal(instance.key(), true);
  assert.deepEqual(instance.sent, [instance.draftRef.current]);
  instance.render();
  assert.equal(instance.find('textarea').props.value, instance.sent[0]);
  instance.props.busy = 'send'; instance.render(); instance.key();
  assert.equal(instance.sent.length, 1);
  // The page only clears on a confirmed successful send or explicit selection.
  instance.ref.current.setValue(''); instance.render();
  assert.equal(instance.find('textarea').props.value, '');
  assert.equal(instance.draftRef.current, '');
});

test('prefills and explicit resets survive child remounts without a per-key parent callback', () => {
  const instance = composer('未发送的草稿');
  instance.ref.current.setValue('预填服务器 #3\n'); instance.render();
  assert.equal(instance.find('textarea').props.value, '预填服务器 #3\n');
  const remounted = composer(instance.draftRef.current);
  assert.equal(remounted.find('textarea').props.value, '预填服务器 #3\n');
  remounted.ref.current.setValue(''); remounted.render();
  assert.equal(remounted.find('button').props.disabled, true);
});

test('IME candidates, composition end, modifiers and held Enter cannot send accidentally', async () => {
  const instance = composer('中文输入');
  instance.find('textarea').props.onCompositionStart();
  assert.equal(instance.key(), false);
  instance.find('textarea').props.onCompositionEnd();
  assert.equal(instance.key(), false);
  await new Promise((resolve) => setTimeout(resolve, 60));
  for (const extra of [{ shiftKey: true }, { ctrlKey: true }, { altKey: true }, { metaKey: true }, { repeat: true }, { nativeEvent: { isComposing: true } }, { keyCode: 229 }]) assert.equal(instance.key(extra), false);
  assert.equal(instance.sent.length, 0);
  assert.equal(instance.key(), true);
  assert.deepEqual(instance.sent, ['中文输入']);
});

test('same permission/loading guards apply to keyboard and button, and stop stays available', () => {
  for (const options of [{ loaded: false }, { busy: 'send' }, { busy: 'access' }, { sessionBusy: true }, { unavailable: true }, { sendBlocked: true }]) {
    const instance = composer('request', options);
    instance.key();
    assert.equal(instance.sent.length, 0);
    if (!options.sessionBusy) assert.equal(instance.find('button').props.disabled, true);
  }
  const running = composer('draft', { sessionBusy: true });
  assert.equal(running.find('button').props['aria-label'], '停止当前任务');
  running.find('button').props.onClick();
  assert.equal(running.stopped(), 1);
});

test('native field sizing is progressive and legacy mobile resizing is frame-scheduled', () => {
  const native = composer('', {}, true).find('textarea').props;
  assert.equal(native.autoSize, false);
  assert.equal(native.rows, 1);
  assert.equal(native.className, 'mini-ops-native-size');
  const legacy = composer('', {}, false).find('textarea').props;
  assert.equal(legacy.autoSize, false);
  assert.equal(legacy.rows, 1);
  assert.equal(legacy.className, undefined);
  assert.match(source, /if \(!miniApp \|\| nativeSize \|\| !input\) return/);
  assert.match(source, /watchMiniComposerSize\(input\)/);
  const desktop = composer('', { miniApp: false, compact: false }, true).find('textarea').props;
  assert.deepEqual(desktop.autoSize, { minRows: 3, maxRows: 8 });
  assert.equal(desktop.className, undefined);
});

test('mobile composer height changes are immediate instead of inheriting AntD transition-all', () => {
  const style = miniCss.match(/\.telegram-mini-app \.mini-ops-composer textarea\.ant-input \{([^}]+)\}/)?.[1];
  assert.ok(style);
  assert.match(style, /transition:\s*none\s*;/);
});

test('idle conversations have no one-second transcript refresh; pending approvals still expire', () => {
  assert.match(page, /const pending = .*review.action.*\.filter\(\(action\) => action.status === 'pending'\)/);
  assert.match(page, /if \(!pending.length\) return;\s+const current = Date.now\(\);\s+setNow\(current\);\s+if \(!pending.some/);
  assert.match(page, /return \(\) => window.clearInterval\(tick\);\s+\}, \[detail\?\.actions, review\?\.action\]\)/);
  assert.doesNotMatch(page, /setInterval\(\(\) => setNow/);
});

test('approval clock calibrates expired snapshots and old reviews before deciding to start a timer', () => {
  const ast = ts.createSourceFile('AIOpsPage.tsx', page, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  const component = ast.statements.find((node) => ts.isFunctionDeclaration(node) && node.name?.text === 'AIOpsPage');
  const effect = component.body.statements.find((node) => ts.isExpressionStatement(node) && ts.isCallExpression(node.expression) && node.expression.expression.getText(ast) === 'useEffect' && node.expression.arguments[0].getText(ast).includes('const pending ='));
  assert.ok(effect);
  const compiledEffect = compile(`const effect = ${effect.expression.arguments[0].getText(ast)}; exports.run = effect;`);
  const run = (actions, review) => {
    let now = 2000000, tick; const updates = [], cleared = [];
    const exports = {};
    new Function('exports', 'detail', 'review', 'Date', 'setNow', 'window', compiledEffect)(exports, { actions }, review, { now: () => now }, (value) => updates.push(value), { setInterval: (callback) => { tick = callback; return 7; }, clearInterval: (id) => cleared.push(id) });
    const cleanup = exports.run();
    return { updates, cleared, cleanup, tick: () => tick?.(), setNow: (value) => { now = value; }, hasTick: () => !!tick };
  };
  const expired = { status: 'pending', expires_at: 1999 };
  for (const [actions, review] of [[[expired], null], [[], { action: expired }]]) {
    const result = run(actions, review);
    assert.deepEqual(result.updates, [2000000]);
    assert.equal(result.hasTick(), false);
  }
  const idle = run([{ status: 'succeeded', expires_at: 9999 }], null);
  assert.deepEqual(idle.updates, []); assert.equal(idle.hasTick(), false);
  const active = run([{ status: 'pending', expires_at: 2002 }], null);
  assert.equal(active.hasTick(), true);
  active.setNow(2001000); active.tick(); assert.deepEqual(active.cleared, []);
  active.setNow(2002000); active.tick(); assert.deepEqual(active.cleared, [7]);
  active.cleanup(); assert.deepEqual(active.cleared, [7, 7]);
});
