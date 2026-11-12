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
  const slots = []; let cursor = 0; const sent = [], sendOptions = []; let stopped = 0;
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
  const props = { draftRef, miniApp: true, compact: true, loaded: true, busy: '', sessionBusy: false, unavailable: false, sendBlocked: false, isAdmin: true, onSend: (content, options) => { sent.push(content); sendOptions.push(options); }, onStop: () => stopped++, ...options };
  let tree;
  const render = () => { cursor = 0; tree = exports.default(props, ref); return tree; };
  const find = (type, node = tree) => node?.type === type && (type !== 'button' || node.props.className !== 'ai-ops-shortcut') ? node : node?.children?.map((child) => typeof child === 'object' ? find(type, child) : null).find(Boolean);
  const shortcut = (node = tree) => node?.props?.['aria-label'] === '发送入口排查快捷消息' ? node : node?.children?.map((child) => typeof child === 'object' ? shortcut(child) : null).find(Boolean);
  const key = (extra = {}) => {
    let prevented = false;
    find('textarea').props.onKeyDown({ key: 'Enter', nativeEvent: {}, keyCode: 13, preventDefault: () => { prevented = true; }, ...extra });
    return prevented;
  };
  render();
  return { draftRef, props, ref, sent, sendOptions, render, find, shortcut, key, stopped: () => stopped };
}

test('entry investigation shortcut sends directly on desktop and Telegram without overwriting drafts', () => {
  for (const miniApp of [true, false]) for (const draft of ['', '未发送的服务器资料\npassword: keep-me']) {
    const instance = composer(draft, { miniApp });
    const button = instance.shortcut();
    assert.ok(button);
    assert.equal(button.props.disabled, false);
    button.props.onClick();
    assert.equal(instance.sent.length, 1);
    assert.match(instance.sent[0], /没有明确目标就先问我组名或入口 IP/);
    assert.match(instance.sent[0], /核实封锁和时间后/);
    assert.match(instance.sent[0], /已有任务就继续，不要重复建组/);
    assert.doesNotMatch(instance.sent[0], /确定被墙了|已确认被墙|开启完全访问/);
    assert.deepEqual(instance.sendOptions, [{ preserveDraft: true }]);
    assert.equal(instance.draftRef.current, draft);
    instance.render();
    assert.equal(instance.find('textarea').props.value, draft);
  }
});

test('shortcuts keep existing loading, authorization and busy guards without relying on draft text', () => {
  for (const miniApp of [true, false]) for (const guard of [{ busy: 'send' }, { busy: 'access' }, { sessionBusy: true }, { unavailable: true }, { sendBlocked: true }, ...(miniApp ? [{ loaded: false }] : [])]) {
    const instance = composer('', { miniApp, ...guard });
    assert.equal(instance.shortcut().props.disabled, true);
    instance.shortcut().props.onClick();
    assert.equal(instance.sent.length, 0);
  }
  const instance = composer('正在选中文候选');
  instance.find('textarea').props.onCompositionStart();
  instance.shortcut().props.onClick();
  assert.equal(instance.sent.length, 0);
  assert.match(page, /onSend=\{\(content, options\) => void send\(content, options\)\}/);
  assert.match(page, /if \(!preserveDraft && draft\.current === content\) setDraft\(''\)/);
  assert.match(miniCss, /\.telegram-mini-app \.ai-ops-shortcut\.ant-btn[^}]*var\(--mini-surface\)[^}]*var\(--mini-link\)/);
});

test('real send handler preserves quick-message drafts, prevents double clicks and keeps the normal API envelope', async () => {
  const ast = ts.createSourceFile('AIOpsPage.tsx', page, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  const component = ast.statements.find((node) => ts.isFunctionDeclaration(node) && node.name?.text === 'AIOpsPage');
  const declaration = component.body.statements.flatMap((node) => ts.isVariableStatement(node) ? [...node.declarationList.declarations] : []).find((node) => node.name.getText(ast) === 'send');
  const program = compile(`exports.send = ${declaration.initializer.getText(ast)};`);
  for (const scenario of ['shortcut', 'normal', 'edited', 'failed', 'switched']) {
    let locked = false, resolveRequest, rejectRequest;
    const calls = [], failures = [], clears = [];
    const draft = { current: '保留这个草稿' }, selectedRef = { current: 0 }, selectionSequence = { current: 0 };
    const bindings = {
      draft, selectedRef, selectionSequence, detail: null, config: {}, model: 'fixture-model', foreignFullAccess: false,
      aiMessageTooLarge: model.aiMessageTooLarge, AI_CHAT_SIZE_ERROR: model.AI_CHAT_SIZE_ERROR, message: { error() { assert.fail('unexpected size error'); } },
      aiModelReady: () => true, aiSessionBusy: () => false,
      begin: () => { if (locked) return false; locked = true; return true; }, finish: () => { locked = false; },
      followOutput: { current: false }, sendRetry: null, accessMode: 'standard', reasoningChoice: 'default', aiRequestID: () => 'same-transport-id', setSendRetry() {},
      apiJsonPost: (path, body) => { calls.push({ path, body }); return new Promise((resolve, reject) => { resolveRequest = resolve; rejectRequest = reject; }); },
      alive: { current: true }, reasoningEdited: { current: false }, setReasoningEffort() {}, aiSessionReasoning: () => 'default', setSelectedID() {}, setTab() {},
      setDraft: (value) => { clears.push(value); draft.current = value; }, loadDetail: async () => {}, loadLists: async () => {}, fail: (error) => failures.push(error.message),
    };
    const exports = {};
    new Function('exports', ...Object.keys(bindings), program)(exports, ...Object.values(bindings));
    const quick = scenario !== 'normal' && scenario !== 'edited';
    const content = quick ? '入口排查快捷消息' : draft.current;
    const sending = exports.send(content, { preserveDraft: quick });
    await exports.send(content, { preserveDraft: quick });
    assert.equal(calls.length, 1, 'synchronous send lock must block double-clicks');
    assert.equal(calls[0].path, '/ai-ops/chat/send');
    assert.equal(calls[0].body.content, content);
    assert.equal(calls[0].body.access_mode, 'standard', 'shortcut must not elevate access');
    assert.equal(calls[0].body.request_id, 'same-transport-id');
    assert.equal('preserveDraft' in calls[0].body, false, 'draft preference is UI-only');
    if (scenario === 'edited') draft.current = '发送途中修改的草稿';
    if (scenario === 'switched') { selectedRef.current = 99; selectionSequence.current++; }
    if (scenario === 'failed') rejectRequest(new Error('fixture network error'));
    else resolveRequest({ data: { session: { id: 21 } } });
    await sending;
    assert.equal(locked, false);
    assert.deepEqual(clears, scenario === 'normal' ? [''] : []);
    assert.equal(draft.current, scenario === 'normal' ? '' : scenario === 'edited' ? '发送途中修改的草稿' : '保留这个草稿');
    assert.equal(failures.length, scenario === 'failed' ? 1 : 0);
  }
});

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

test('a 50-server paste is preserved beyond the old character cap on desktop and mini', () => {
  const password = ' !"$&|:=#中🙂'.repeat(30);
  const content = Array.from({ length: 50 }, (_, index) => `地址: 192.0.2.${index + 1}:2222\n用户名: ops\npassword: ${JSON.stringify(password)}\n`).join('\n');
  assert.ok(content.length > 12000);
  assert.equal(model.aiMessageTooLarge(content), false);
  for (const miniApp of [true, false]) {
    const instance = composer('', { miniApp });
    instance.find('textarea').props.onChange({ target: { value: content } }); instance.render();
    assert.equal(instance.find('textarea').props.maxLength, undefined);
    assert.equal(instance.find('textarea').props.value, content);
    assert.equal(instance.find('button').props.disabled, false);
    instance.key();
    assert.deepEqual(instance.sent, [content]);
    assert.equal(instance.draftRef.current, content);
  }
});

test('UTF-8 message size matches backend bytes, accepts exact limit and never truncates an oversized draft', () => {
  assert.equal(model.AI_CHAT_MAX_BYTES, 131072);
  const accepted = ['a'.repeat(131072), '中'.repeat(43690) + 'ab', '🙂'.repeat(32768)];
  const rejected = ['a'.repeat(131073), '中'.repeat(43691), '🙂'.repeat(32768) + 'a'];
  for (const content of [...accepted, ...rejected]) assert.equal(model.aiMessageTooLarge(content), Buffer.byteLength(content, 'utf8') > 131072);
  for (const miniApp of [true, false]) {
    const instance = composer(accepted[2], { miniApp });
    instance.key(); assert.deepEqual(instance.sent, [accepted[2]]);
    instance.ref.current.setValue(rejected[1]); instance.render();
    assert.equal(instance.find('textarea').props['aria-invalid'], true);
    assert.equal(instance.find('textarea').props.value, rejected[1]);
    assert.equal(instance.find('button').props.disabled, true);
    instance.key(); instance.find('button').props.onClick();
    assert.equal(instance.sent.length, 1);
    assert.equal(instance.draftRef.current, rejected[1]);
    instance.ref.current.setValue('已拆分的消息'); instance.render(); instance.key();
    assert.equal(instance.sent[1], '已拆分的消息');
  }
  const send = page.slice(page.indexOf('const send ='), page.indexOf('const saveConfig'));
  assert.match(send, /if \(aiMessageTooLarge\(content\)\) \{ message.error\(AI_CHAT_SIZE_ERROR\); return; \}/);
  assert.ok(send.indexOf('aiMessageTooLarge(content)') < send.indexOf("begin('send')"));
});

test('batch server help is collapsed by default and removes obsolete single-server chat restrictions', () => {
  for (const miniApp of [true, false]) {
    const help = composer('', { miniApp }).find('details');
    assert.equal(help.props.className, 'ai-ops-server-help');
    assert.notEqual(help.props.open, true);
    assert.match(JSON.stringify(help), /50 台/);
    assert.match(JSON.stringify(help), /192\.0\.2\.10:2222/);
    assert.match(JSON.stringify(help), /192\.0\.2\.20/);
  }
  assert.doesNotMatch(page, /不要填入聊天|每条消息一台/);
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
