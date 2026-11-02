import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { createRequire } from 'node:module';
import { test } from 'node:test';
import React from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import ts from 'typescript';

const require = createRequire(import.meta.url);
const compile = (source) => ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2020, jsx: ts.JsxEmit.React, esModuleInterop: true } }).outputText;
const source = await readFile(new URL('../src/pages/AIOpsTranscript.tsx', import.meta.url), 'utf8');
const helpers = {}, identity = {};
new Function('exports', compile(await readFile(new URL('../src/pages/aiOpsModel.ts', import.meta.url), 'utf8')))(helpers);
new Function('exports', compile(await readFile(new URL('../src/pages/aiOpsServerIdentity.ts', import.meta.url), 'utf8')))(identity);

function load({ open = false } = {}) {
  const exports = {}, components = new Map();
  const counts = { lookup: 0, markdown: 0, action: 0 };
  const resolve = (name) => {
    if (name === 'react') return {
      ...React,
      useLayoutEffect() {},
      useState: (initial) => React.useState(open || initial),
      memo(render, compare) { components.set(render.name, { render, compare }); return React.memo(render, compare); },
    };
    if (name === './aiOpsModel') return { ...helpers, aiToolResultName(...args) { counts.lookup++; return helpers.aiToolResultName(...args); } };
    if (name === './aiOpsServerIdentity') return identity;
    if (name === './AIOpsMarkdown') return { __esModule: true, default({ content }) { counts.markdown++; return React.createElement('div', { className: 'fixture-markdown' }, content); } };
    if (name === './AIOpsActionSummary') return { __esModule: true, aiActionNames: { ssh_command: 'SSH 命令' }, default() { counts.action++; return React.createElement('div', null, 'action summary'); } };
    return require(name);
  };
  new Function('require', 'exports', compile(source))(resolve, exports);
  return { ...exports, counts, components };
}
const now = 1900000000000;
const makeAction = (id, status = 'succeeded') => ({ id, session_id: 1, authorization_turn_id: 1, tool_call_id: `turn:1:${id}`, kind: 'ssh_command', status, approval_mode: 'full_access', reason: 'check service', expires_at: now / 1000 + 60, payload: { command: 'printf fixture-command', host: '192.0.2.1', username: 'ops', port: 22 }, output: 'fixture-output' });
function fixture(count = 1) {
  const messages = [];
  for (let index = 0; index < count; index++) {
    messages.push({ id: index * 2 + 1, session_id: 1, turn_id: 1, role: 'assistant', content: index ? '' : 'visible reply', created_at: now / 1000, tool_calls: [{ id: `call-${index}`, function: { name: 'entry_health', arguments: '{"target":"fixture-target"}' } }] });
    messages.push({ id: index * 2 + 2, session_id: 1, turn_id: 1, role: 'tool', content: 'fixture-tool-result', tool_call_id: `call-${index}`, created_at: now / 1000 });
  }
  return { session: { id: 1, status: 'idle' }, messages, turns: [{ id: 1, status: 'completed' }], actions: [makeAction(1)] };
}
const props = (detail) => ({ detail, selectedID: 1, miniApp: true, busy: false, enabled: true, now, streamConnected: false, onReview() {}, onLayout() {} });

test('a 400-message collapsed transcript mounts no tool or action details, yet keeps visible replies and counts', () => {
  const component = load();
  const detail = fixture(200);
  Object.defineProperty(detail.actions[0], 'payload', { get() { assert.fail('collapsed process must never read command payload'); } });
  const html = renderToStaticMarkup(React.createElement(component.default, props(detail)));
  assert.match(html, /visible reply/);
  assert.match(html, /处理过程 · 401 项/);
  assert.doesNotMatch(html, /fixture-tool-result|fixture-target|fixture-command|fixture-output|ai-ops-process-content/);
  assert.equal(component.counts.lookup, 0);
  assert.equal(component.counts.action, 0);
  assert.equal(component.counts.markdown, 1);
});

test('expanding a process renders complete tool arguments, outputs and action command', () => {
  const component = load({ open: true });
  const html = renderToStaticMarkup(React.createElement(component.default, props(fixture())));
  for (const text of ['ai-ops-process-content', 'entry_health', 'fixture-target', 'fixture-tool-result', 'fixture-command', 'fixture-output', 'ops@192.0.2.1:22']) assert.ok(html.includes(text), text);
  assert.equal(component.counts.action, 1);
  assert.equal(component.counts.lookup, 2);
});

test('pending review remains accessible while collapsed and becomes unavailable after expiration', () => {
  const component = load();
  const detail = fixture();
  detail.actions[0] = { ...makeAction(1, 'pending'), approval_mode: 'standard' };
  const active = renderToStaticMarkup(React.createElement(component.default, props(detail)));
  assert.match(active, /aria-label="审核操作 #1"/);
  assert.doesNotMatch(active, /ai-ops-process-review[^>]*disabled/);
  assert.doesNotMatch(active, /fixture-command|ai-ops-process-content/);
  const expired = renderToStaticMarkup(React.createElement(component.default, { ...props(detail), now: now + 60000 }));
  assert.doesNotMatch(expired, /aria-label="审核操作 #1"/);
  const disabled = renderToStaticMarkup(React.createElement(component.default, { ...props(detail), enabled: false }));
  assert.match(disabled, /aria-label="审核操作 #1" disabled=""/);
});

test('approval modal details never depend on whether the process was expanded', () => {
  const component = load();
  const action = { ...makeAction(1, 'pending'), approval_mode: 'standard' };
  const html = renderToStaticMarkup(React.createElement(component.ActionDetails, { action }));
  assert.match(html, /完整命令（逐次确认后执行）/);
  assert.match(html, /printf fixture-command/);
});

test('streamed drafts bypass old message rows and do not rebuild expanded process trees', () => {
  const { components } = load();
  const detail = fixture();
  const group = helpers.aiConversationTurns(detail)[0];
  group.turn = { ...group.turn, status: 'running', draft_content: 'a', draft_version: 1 };
  const first = { group, miniApp: true, busy: false, sessionBusy: false, enabled: true, now: 0, onReview() {}, onLayout() {}, draftContent: 'a' };
  const next = { ...first, group: { ...group, messages: [...group.messages], actions: [...group.actions], turn: { ...group.turn, draft_content: 'ab', draft_version: 2 } }, draftContent: 'ab' };
  assert.equal(components.get('ProcessDetails').compare(first, next), true);
  assert.equal(components.get('ConversationTurn').compare(first, next), false);
  assert.equal(components.get('ConversationTurn').compare(first, { ...next, draftContent: 'a' }), true);
  const changed = { ...next, group: { ...next.group, actions: [{ ...group.actions[0], output: 'new output' }] } };
  assert.equal(components.get('ProcessDetails').compare(first, changed), false);
  assert.equal(components.get('ConversationTurn').compare(first, changed), false);
  assert.equal(components.get('ProcessDetails').compare(first, { ...next, group: { ...next.group, turn: { ...next.group.turn, error: 'failed' } } }), false);
  assert.equal(components.get('ProcessDetails').compare(first, { ...next, busy: true }), false);
  assert.equal(components.get('ProcessDetails').compare(first, { ...next, now: now + 60000 }), false);
  assert.equal(components.get('ProcessDetails').compare(first, { ...next, group: { ...next.group, key: '2:1' } }), false);
});
