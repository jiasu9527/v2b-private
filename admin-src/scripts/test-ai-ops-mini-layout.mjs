import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { createRequire } from 'node:module';
import { test } from 'node:test';
import React from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import ts from 'typescript';

const require = createRequire(import.meta.url);
const page = await readFile(new URL('../src/pages/AIOpsPage.tsx', import.meta.url), 'utf8');
const model = await readFile(new URL('../src/pages/aiOpsModel.ts', import.meta.url), 'utf8');
const compile = (source) => ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2020, jsx: ts.JsxEmit.React, esModuleInterop: true } }).outputText;
const helpers = {};
new Function('exports', compile(model))(helpers);
const serverIdentity = {};
new Function('exports', compile(await readFile(new URL('../src/pages/aiOpsServerIdentity.ts', import.meta.url), 'utf8')))(serverIdentity);
const ast = ts.createSourceFile('AIOpsPage.tsx', page, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
const component = ast.statements.find((node) => ts.isFunctionDeclaration(node) && node.name?.text === 'AIOpsPage');
const stateNames = component.body.statements.flatMap((node) => ts.isVariableStatement(node) ? node.declarationList.declarations.filter((decl) => ts.isArrayBindingPattern(decl.name) && ts.isCallExpression(decl.initializer) && decl.initializer.expression.getText(ast) === 'useState').map((decl) => decl.name.elements[0].name.getText(ast)) : []);
assert.ok(stateNames.includes('settingsOpen'));
const config = { enabled: true, models: ['fixture-model'], has_api_key: true, base_url: '' };
const idle = { session: { id: 21, title: '入口排查', model: 'fixture-model', created_by: 7, access_mode: 'full', full_access_by: 7, status: 'idle' }, messages: [], turns: [], actions: [] };

function render({ miniApp = true, admin = true, states = {}, screens = { lg: true }, controls = [] } = {}) {
  let index = 0;
  const values = { config, loaded: true, model: 'fixture-model', ...states };
  const react = { ...React, useState(initial) { const name = stateNames[index++]; return React.useState(Object.hasOwn(values, name) ? values[name] : initial); } };
  const antd = require('antd');
  const exports = {};
  const resolve = (name) => {
    if (name === 'react') return react;
    if (name === 'antd') return {
      ...antd, Grid: { ...antd.Grid, useBreakpoint: () => screens },
      Select: (props) => { controls.push(props); return React.createElement(antd.Select, props); },
      Drawer: ({ open, children, ...props }) => open ? React.createElement('section', { 'aria-label': props['aria-label'], className: props.rootClassName, 'data-placement': props.placement }, children) : null,
    };
    if (name === '../lib/api') return { getAdminUserInfo: () => ({ id: 7, is_admin: admin ? 1 : 0, is_staff: admin ? 0 : 1 }) };
    if (name === './aiOpsModel') return helpers;
    if (name === './aiOpsServerIdentity') return serverIdentity;
    if (name.startsWith('./')) return { __esModule: true, default: () => null, aiActionNames: {} };
    return require(name);
  };
  new Function('require', 'exports', compile(page))(resolve, exports);
  return renderToStaticMarkup(React.createElement(exports.default, { miniApp }));
}

test('mini chat uses one compact title row and hides selectors behind its settings button', () => {
  const html = render();
  assert.doesNotMatch(html, /mini-ops-header|mini-ops-quick-controls|aria-label="选择 AI 模型"|aria-label="AI 访问权限"|aria-label="推理强度"/);
  assert.match(html, /mini-ops-session-head/);
  assert.match(html, /mini-ops-nav/);
  assert.match(html, /aria-label="运维导航"/);
  assert.match(html, /aria-current="page"/);
  for (const label of ['聊天', '服务器', '知识', '设置']) assert.ok(html.includes(`<span>${label}</span>`));
  assert.doesNotMatch(html, /ant-tabs-nav|content-heading|已启用|>刷新</);
  assert.match(html, /aria-label="历史会话"/);
  assert.match(html, /aria-label="新对话"/);
  assert.match(html, /aria-label="对话设置"/);
  assert.match(html, /mini-ops-composer-row/);
  assert.match(html, /aria-label="发送消息"/);
  const servers = render({ states: { tab: 'servers' } });
  assert.match(servers, /mini-ops-header/);
});

test('staff mini navigation and conversation settings never expose admin model configuration or reasoning', () => {
  const html = render({ admin: false, states: { settingsOpen: true } });
  assert.doesNotMatch(html, /<span>设置<\/span>|aria-label="推理强度"|模型中转设置|Telegram/);
  assert.match(html, /mini-ops-settings-drawer/);
  assert.match(html, /aria-label="选择 AI 模型"/);
  assert.match(html, /aria-label="AI 访问权限"/);
  assert.match(html, /逐次确认|完全访问/);
  const admin = render({ states: { settingsOpen: true } });
  assert.match(admin, /aria-label="推理强度"/);
  assert.match(admin, /mini-ops-setting-row/);
});

test('running mini tasks swap the send circle for a visible stop control and preserve collapsed pending approvals', () => {
  const running = { ...idle, session: { ...idle.session, status: 'running' }, turns: [{ id: 31, status: 'running' }] };
  const html = render({ states: { selectedID: 21, detail: running } });
  assert.match(html, /mini-ops-send-button is-stop/);
  assert.match(html, /aria-label="停止当前任务"/);
  assert.doesNotMatch(html, /aria-label="发送消息"/);
  const pending = { ...idle, actions: [{ id: 91, session_id: 21, status: 'pending', expires_at: Math.floor(Date.now() / 1000) + 600, kind: 'panel_operation', payload: {}, tool_call_id: '', created_at: 1 }] };
  const approval = render({ states: { selectedID: 21, detail: pending } });
  assert.match(approval, /<details class="ai-ops-process"/);
  assert.doesNotMatch(approval, /<details[^>]*\bopen=/);
  assert.match(approval, /aria-label="审核操作 #91"/);
});

test('ordinary web rendering retains its original title, top tabs, toolbar and send label', () => {
  const html = render({ miniApp: false });
  assert.match(html, /content-heading/);
  assert.match(html, /ant-tabs-nav/);
  assert.match(html, /已启用/);
  assert.match(html, /<span>发送<\/span>/);
  assert.doesNotMatch(html, /mini-ops-nav|mini-ops-header|mini-ops-composer/);
  assert.match(html, /ai-ops-dialog-settings/);
  assert.match(html, /aria-label="对话设置"/);
  assert.doesNotMatch(html, /aria-label="选择 AI 模型"|aria-label="AI 访问权限"|aria-label="推理强度"/);
});


test('web conversation settings open in a side drawer on desktop and a bottom drawer on mobile', () => {
  for (const [screens, placement] of [[{ lg: true }, 'right'], [{ lg: false }, 'bottom']]) {
    const html = render({ miniApp: false, screens, states: { settingsOpen: true } });
    assert.match(html, new RegExp(`<section aria-label="对话设置" class="ai-ops-settings-drawer" data-placement="${placement}">`));
    for (const label of ['选择 AI 模型', 'AI 访问权限', '推理强度']) assert.ok(html.includes(`aria-label="${label}"`));
    assert.match(html, /ai-ops-setting-row/);
    assert.doesNotMatch(html, /mini-ops-settings-drawer/);
  }
  const mini = render({ states: { settingsOpen: true } });
  assert.match(mini, /<section aria-label="对话设置" class="mini-ops-settings-drawer" data-placement="bottom">/);
});

test('staff web settings expose model and access choices but never administrator reasoning controls', () => {
  for (const screens of [{ lg: true }, { lg: false }]) {
    const html = render({ miniApp: false, admin: false, screens, states: { settingsOpen: true } });
    assert.match(html, /ai-ops-settings-drawer/);
    assert.match(html, /aria-label="选择 AI 模型"/);
    assert.match(html, /aria-label="AI 访问权限"/);
    assert.doesNotMatch(html, /aria-label="推理强度"|模型中转设置/);
  }
});

test('shared conversation settings retain busy/loading guards and let running full access be revoked', () => {
  const running = { ...idle, session: { ...idle.session, status: 'running' }, turns: [{ id: 31, status: 'running' }] };
  for (const miniApp of [false, true]) {
    const inspect = (states) => {
      const controls = [];
      render({ miniApp, states: { settingsOpen: true, ...states }, controls });
      return Object.fromEntries(controls.map((item) => [item['aria-label'], item]));
    };
    const idleChoices = inspect({ selectedID: 21, detail: idle });
    for (const label of ['选择 AI 模型', 'AI 访问权限', '推理强度']) assert.equal(!!idleChoices[label].disabled, false, label);
    const working = inspect({ selectedID: 21, detail: running });
    assert.equal(working['选择 AI 模型'].disabled, true);
    assert.equal(working['推理强度'].disabled, true);
    assert.equal(working['AI 访问权限'].disabled, false, 'current full access can be revoked without stopping the conversation');
    assert.equal(working['AI 访问权限'].options.find((item) => item.value === 'standard').disabled, undefined);
    const standard = inspect({ selectedID: 21, detail: { ...running, session: { ...running.session, access_mode: 'standard' } } });
    assert.equal(standard['AI 访问权限'].options.find((item) => item.value === 'full').disabled, true, 'running standard tasks cannot be silently escalated');
    const busy = inspect({ selectedID: 21, detail: idle, busy: 'access' });
    for (const label of ['选择 AI 模型', 'AI 访问权限', '推理强度']) assert.equal(busy[label].disabled, true, label);
    const loading = inspect({ selectedID: 21, detail: null });
    assert.equal(loading['AI 访问权限'].disabled, true);
    assert.equal(loading['推理强度'].disabled, true);
    const initializing = inspect({ loaded: false });
    assert.equal(initializing['选择 AI 模型'].disabled, true);
    assert.equal(initializing['AI 访问权限'].disabled, true);
  }
});

const backEffect = component.body.statements.find((node) => ts.isExpressionStatement(node) && ts.isCallExpression(node.expression) && node.expression.expression.getText(ast) === 'useEffect' && node.getText(ast).includes('BackButton')).expression.arguments[0].getText(ast);
const bindBack = new Function('env', compile(`const {miniApp, historyOpen, settingsOpen, serverOpen, review, confirmation, tab, busy, knowledgeBack, window, setReview, setConfirmedOutput, setConfirmation, setResetText, closeServer, setSettingsOpen, setHistoryOpen, setTab} = env; return (${backEffect})();`));

test('SDK BackButton closes overlays or returns to chat, unregisters on cleanup and never stops a task', () => {
  for (const [state, expected] of [[{ historyOpen: true }, 'history'], [{ settingsOpen: true }, 'settings'], [{ tab: 'servers' }, 'chat'], [{ review: {} }, 'review'], [{ serverOpen: true }, 'server']]) {
    const calls = []; let handler;
    const env = { miniApp: true, historyOpen: false, settingsOpen: false, serverOpen: false, review: null, confirmation: null, tab: 'chat', busy: '', knowledgeBack: { current: null }, ...state,
      window: { Telegram: { WebApp: { BackButton: { show: () => calls.push('show'), hide: () => calls.push('hide'), onClick: (fn) => { handler = fn; }, offClick: (fn) => assert.equal(fn, handler) } } } },
      closeServer: () => calls.push('server'), setHistoryOpen: () => calls.push('history'), setSettingsOpen: () => calls.push('settings'), setTab: () => calls.push('chat'), setReview: () => calls.push('review'), setConfirmedOutput() {},
    };
    const cleanup = bindBack(env);
    assert.equal(calls[0], 'show'); handler(); assert.equal(calls[1], expected);
    cleanup(); assert.equal(calls.at(-1), 'hide');
  }
  assert.equal(bindBack({ miniApp: false }), undefined);
  assert.equal(bindBack({ miniApp: true, window: {} }), undefined);
  assert.doesNotMatch(backEffect, /stop\(|chat\/stop|close\(/);
});


test('Mini BackButton SDK failures cannot crash mounting or cleanup', () => {
  for (const method of ['show', 'onClick', 'offClick', 'hide']) {
    const button = { show() {}, hide() {}, onClick() {}, offClick() {} };
    button[method] = () => { throw new Error('Unsupported Telegram version'); };
    assert.doesNotThrow(() => {
      const cleanup = bindBack({ miniApp: true, historyOpen: true, window: { Telegram: { WebApp: { BackButton: button } } } });
      cleanup?.();
    });
  }
});

test('Mini message times use HH:mm with a complete datetime, while the ordinary page keeps full dates', () => {
  const stamp = 1793395200;
  const detail = { ...idle, messages: [{ id: 1, session_id: 21, turn_id: 1, role: 'user', content: '检查入口', created_at: stamp }] };
  const mini = render({ states: { selectedID: 21, detail } });
  assert.match(mini, /<time title="[^"]+" dateTime="[^"]+">\d{2}:\d{2}<\/time>/);
  const web = render({ miniApp: false, states: { selectedID: 21, detail } });
  assert.match(web, /<time>2026[\s\S]*?<\/time>/);
});


test('Mini SDK BackButton lets knowledge overlays consume back before switching away', () => {
  let handler;
  const calls = [];
  const cleanup = bindBack({ miniApp: true, historyOpen: false, settingsOpen: false, serverOpen: false, review: null, confirmation: null, tab: 'knowledge', busy: '',
    knowledgeBack: { current: () => { calls.push('knowledge-overlay'); return true; } },
    window: { Telegram: { WebApp: { BackButton: { show() {}, hide() {}, onClick(fn) { handler = fn; }, offClick() {} } } } },
    setTab: () => calls.push('chat'),
  });
  handler();
  assert.deepEqual(calls, ['knowledge-overlay']);
  cleanup();
});

const knowledge = await readFile(new URL('../src/pages/AIOpsKnowledge.tsx', import.meta.url), 'utf8');
const knowledgeAST = ts.createSourceFile('AIOpsKnowledge.tsx', knowledge, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
const knowledgeComponent = knowledgeAST.statements.find((node) => ts.isFunctionDeclaration(node) && node.name?.text === 'AIOpsKnowledge');
const knowledgeBackEffect = knowledgeComponent.body.statements.find((node) => ts.isExpressionStatement(node) && ts.isCallExpression(node.expression) && node.expression.expression.getText(knowledgeAST) === 'useEffect' && node.getText(knowledgeAST).includes('miniBackHandler')).expression.arguments[0].getText(knowledgeAST);
const bindKnowledgeBack = new Function('env', compile(`const {miniBackHandler, active, mode, deleting, saving, mutation, setDeleting, setDeleteError, close} = env; return (${knowledgeBackEffect})();`));

test('knowledge overlays close through normal handlers and do not dismiss while saving, with web untouched', () => {
  for (const state of [{ mode: 'view' }, { mode: 'edit' }, { mode: 'create' }, { deleting: {} }]) {
    const calls = [];
    const ref = { current: null };
    const cleanup = bindKnowledgeBack({ miniBackHandler: ref, active: true, mode: null, deleting: null, saving: false, mutation: { current: false }, ...state,
      setDeleting: (value) => calls.push(['delete', value]), setDeleteError: (value) => calls.push(['error', value]), close: () => calls.push(['close']),
    });
    assert.equal(ref.current(), true);
    assert.deepEqual(calls, state.deleting ? [['delete', null], ['error', '']] : [['close']]);
    cleanup(); assert.equal(ref.current, null);
  }
  for (const state of [{ saving: true }, { mutation: { current: true } }]) {
    const ref = { current: null };
    bindKnowledgeBack({ miniBackHandler: ref, active: true, mode: 'edit', deleting: null, saving: false, mutation: { current: false }, ...state, close() { assert.fail('must not close while saving'); } });
    assert.equal(ref.current(), true);
  }
  const ref = { current: null };
  bindKnowledgeBack({ miniBackHandler: ref, active: true, mode: null, deleting: null });
  assert.equal(ref.current(), false);
  assert.equal(bindKnowledgeBack({ active: true }), undefined, 'ordinary page does not register any Mini handler');
  assert.equal(bindKnowledgeBack({ miniBackHandler: { current: null }, active: false }), undefined);
});
