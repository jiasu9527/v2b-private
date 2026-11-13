import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { createRequire } from 'node:module';
import { test } from 'node:test';
import React from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import ts from 'typescript';

const require = createRequire(import.meta.url);
const compile = (source) => ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2020, jsx: ts.JsxEmit.ReactJSX, esModuleInterop: true } }).outputText;
const identity = {};
new Function('exports', compile(await readFile(new URL('../src/pages/aiOpsServerIdentity.ts', import.meta.url), 'utf8')))(identity);
const code = compile(await readFile(new URL('../src/pages/AIOpsServersPanel.tsx', import.meta.url), 'utf8'));
function component(states = []) {
  let index = 0;
  const exports = {};
  const react = { ...React, useState(initial) { return React.useState(index < states.length ? states[index++] : initial); } };
  new Function('require', 'exports', code)((name) => name === 'react' ? react : name === './aiOpsServerIdentity' ? identity : require(name), exports);
  return exports;
}
const server = { id: 1, name: '香港入口-很长的服务器名称-' + 'OperationsServer'.repeat(12), host: '2001:db8::1234', port: 54101, username: 'root', host_key_sha256: 'SHA256:fixture', enabled: true, version: 3, has_password: true, has_private_key: false };
const render = (props = {}, states) => renderToStaticMarkup(React.createElement(component(states).default, { servers: [server], compact: true, busy: false, unavailable: false, onEdit() {}, onSetGroup() { return Promise.resolve(); }, onCheck() {}, onDeploy() {}, onDelete() {}, onRefresh() {}, ...props }));

test('compact servers show full names and IPv6, with actions outside any fixed table', () => {
  const html = render();
  assert.ok(html.includes(server.name));
  assert.ok(html.includes('root@[2001:db8::1234]:54101'));
  assert.match(html, /class="ai-ops-server-card"/);
  assert.doesNotMatch(html, /<table|ant-table-cell-fix-right|text-overflow/);
  assert.doesNotMatch(html, /<details class="ai-ops-server-fingerprint">/);
  assert.match(html, /aria-label="搜索服务器"/);
});

test('desktop servers retain the table and the full long identity', () => {
  const html = render({ compact: false });
  assert.match(html, /<table/);
  assert.ok(html.includes(server.name));
  assert.doesNotMatch(html, /<article class="ai-ops-server-card"/);
});

test('server filtering matches name, host, username or ID without mutating the source', () => {
  const { filterAIServers } = component();
  const other = { ...server, id: 32, name: 'American Node', host: 'edge.example.test', username: 'maintenance' };
  const rows = Object.freeze([Object.freeze({ ...server }), Object.freeze(other)]);
  for (const query of [' american ', 'EDGE.EXAMPLE', 'mainTenance', '32']) assert.deepEqual(filterAIServers(rows, query), [other]);
  assert.deepEqual(filterAIServers(rows, ''), rows);
  assert.deepEqual(filterAIServers(rows, 'nonexistent'), []);
});

test('card pagination bounds the rendered list and recovers when the last page disappears', () => {
  const servers = Array.from({ length: 51 }, (_, i) => ({ ...server, id: i + 1, name: `fixture-${i + 1}` }));
  assert.equal((render({ servers }).match(/<article /g) || []).length, 50);
  const pageTwo = render({ servers }, ['', 2]);
  assert.equal((pageTwo.match(/<article /g) || []).length, 1);
  assert.ok(pageTwo.includes('fixture-51'));
  const shrunk = render({ servers: servers.slice(0, 1) }, ['', 2]);
  assert.equal((shrunk.match(/<article /g) || []).length, 1);
  assert.ok(shrunk.includes('fixture-1'));
});

test('disabled and unavailable servers cannot run checks or deploy, and busy blocks all actions', () => {
  for (const props of [{ servers: [{ ...server, enabled: false }] }, { unavailable: true }]) {
    const buttons = render(props).match(/<button\b[^>]*>[\s\S]*?<\/button>/g) || [];
    for (const label of ['检查', '部署']) assert.ok(buttons.some((button) => button.includes(label) && button.includes('disabled=""')));
    for (const label of ['编辑', '分组', '删除']) assert.ok(buttons.some((button) => button.includes(label) && !button.includes('disabled=""')));
  }
  const busy = render({ busy: true }).match(/<button\b[^>]*>[\s\S]*?<\/button>/g) || [];
  for (const label of ['登记服务器', '编辑', '分组', '删除', '检查', '部署']) assert.ok(busy.some((button) => button.includes(label) && button.includes('disabled=""')));
});

test('server group names are normalized, bounded, and search uses persisted values', () => {
  const { normalizeAIServerGroup, filterAIServers } = component();
  assert.equal(normalizeAIServerGroup(['  香港线路  ']), '香港线路');
  assert.equal(normalizeAIServerGroup([]), '');
  assert.throws(() => normalizeAIServerGroup(['A', 'B']), /只能选择一个分组/);
  assert.throws(() => normalizeAIServerGroup(['中'.repeat(101)]), /最多 100/);
  assert.throws(() => normalizeAIServerGroup(['test\u0000hidden']), /控制字符/);
  const grouped = { ...server, group: '香港线路' };
  assert.deepEqual(filterAIServers([grouped], '香港'), [grouped]);
  assert.match(render({ servers: [grouped] }), /香港线路/);
  const selected = render({ servers: [grouped] }, ['', 1, 'group:香港线路']);
  assert.match(selected, /香港线路 ·/);
  const reserved = { ...server, group: '__none__' };
  assert.match(render({ servers: [reserved] }, ['', 1, 'group:__none__']), /共 1 台/);
});

test('group save is a backend action, not a console-only placeholder', async () => {
  const page = await readFile(new URL('../src/pages/AIOpsPage.tsx', import.meta.url), 'utf8');
  const panel = await readFile(new URL('../src/pages/AIOpsServersPanel.tsx', import.meta.url), 'utf8');
  assert.match(page, /apiJsonPost\('\/ai-ops\/server\/group', \{ id: server\.id, version: server\.version, group \}\)/);
  assert.match(panel, /await onSetGroup\(editingServer, group\)/);
  assert.match(panel, /message\.success\(group \? '服务器分组已保存' : '已移出分组'\)/);
  assert.doesNotMatch(panel, /console\.log|TODO: 实现更新分组/);
});

test('empty and unmatched server lists have distinct messages', () => {
  assert.ok(render({ servers: [] }).includes('暂无登记的服务器'));
  assert.ok(render({}, ['unmatched', 1]).includes('没有匹配的服务器'));
});
