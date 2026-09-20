import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

const source = readFileSync(new URL('../src/pages/clientEntryLayout.ts', import.meta.url), 'utf8');
const compiled = ts.transpileModule(source, { compilerOptions: { target: ts.ScriptTarget.ES2020, module: ts.ModuleKind.CommonJS } }).outputText;
const sandbox = { exports: {} };
vm.runInNewContext(compiled, sandbox);
const { filterEntryRows, entryRuleName, entryRuleHost, moveEntryRow, mergeEntryCollectionKeys } = sandbox.exports;
const plain = (value) => JSON.parse(JSON.stringify(value));
const key = (row) => row.__row_kind === 'split_group' ? `split_group:${row.__split_group.id}` : `policy:${row.id}`;
const filters = { query: '', status: 'all', kind: 'all', membership: 'all' };
const collection = { id: 3, name: '港台合集', entry_host: 'shared.EXAMPLE.com' };
const rows = [
  { id: 9, name: '香港入口', entry_host: 'hk.example.com', enabled: 1, conditions: [{ field: 'email', operator: 'in', values: ['a@example.com'] }], __entry_collection: collection },
  { id: 2, name: '不应显示的父规则', entry_host: 'parent.example.com', enabled: 1, conditions: [{ field: 'user_id', operator: 'between' }], __row_kind: 'split_group', __split_group: { id: 17, name: '香港入口', entry_host: '  203.0.113.17  ' }, __entry_collection: collection },
  { id: 3, name: '欧洲入口', entry_host: 'eu.example.com', enabled: 0, conditions: [{ field: 'user_id', operator: 'between', min: 1, max: 9 }] },
  { id: 4, name: '美国入口', entry_host: 'us.example.com', enabled: 1, conditions: JSON.stringify([{ field: 'ua', operator: 'contains_any', values: ['curl'] }]) },
  { id: 5, name: '香港邮箱备用', entry_host: 'backup.example.com', enabled: 1, conditions: [{ field: 'email', operator: 'in', values: ['b@example.com'] }] },
];
const search = (changes) => filterEntryRows(rows, { ...filters, ...changes });

test('rule names and hosts use independent leaf values instead of parent values', () => {
  assert.equal(entryRuleName(rows[1]), '香港入口');
  assert.equal(entryRuleHost(rows[1]), '203.0.113.17');
  assert.equal(entryRuleName({ id: 7 }), '规则 #7');
  assert.equal(entryRuleHost({ id: 7 }), '');
});

test('name search keeps same-name rules distinct and preserves global order', () => {
  assert.deepEqual(search({ query: '香港入口' }).map(key), ['policy:9', 'split_group:17']);
  assert.deepEqual(search({ query: '港台合集' }).map(key), ['policy:9', 'split_group:17']);
  assert.deepEqual(search({ query: '  SHARED.example.COM  ' }).map(key), ['policy:9', 'split_group:17']);
  assert.deepEqual(search({ query: '203.0.113.17' }).map(key), ['split_group:17']);
  assert.deepEqual(search({ query: 'split_group:17' }).map(key), ['split_group:17']);
  assert.deepEqual(search({ query: 'parent.example.com' }), []);
});

test('filters distinguish enabled state, ordinary conditions and fixed membership', () => {
  assert.deepEqual(search({ status: 'disabled' }).map(key), ['policy:3']);
  assert.deepEqual(search({ kind: 'email' }).map(key), ['policy:9', 'policy:5']);
  assert.deepEqual(search({ kind: 'user_id' }).map(key), ['policy:3']);
  assert.deepEqual(search({ kind: 'ua' }).map(key), ['policy:4']);
  assert.deepEqual(search({ kind: 'split' }).map(key), ['split_group:17']);
  assert.deepEqual(search({ kind: 'standard' }).map(key), ['policy:9', 'policy:3', 'policy:4', 'policy:5']);
  assert.deepEqual(search({ membership: 'grouped' }).map(key), ['policy:9', 'split_group:17']);
  assert.deepEqual(search({ membership: 'ungrouped' }).map(key), ['policy:3', 'policy:4', 'policy:5']);
});

test('combined filters retain global order without mutating the original rows', () => {
  const original = JSON.stringify(rows);
  assert.deepEqual(search({ query: '香港', status: 'enabled', kind: 'email', membership: 'ungrouped' }).map(key), ['policy:5']);
  assert.deepEqual(search({ query: 'EXAMPLE.COM', status: 'enabled', kind: 'standard' }).map(key), ['policy:9', 'policy:4', 'policy:5']);
  assert.equal(JSON.stringify(rows), original);
  assert.equal(search({ query: '港台合集' })[0], rows[0]);
  assert.deepEqual(filterEntryRows([{ id: 1, enabled: false, conditions: '{broken' }], { ...filters, kind: 'email' }), []);
});

test('moving one full-list row keeps every rule including folded members and uses final positions', () => {
  const original = rows.map(key);
  const moved = moveEntryRow(rows, 'split_group:17', 4);
  assert.deepEqual(moved.map(key), ['policy:9', 'policy:3', 'policy:4', 'split_group:17', 'policy:5']);
  assert.deepEqual(moveEntryRow(moved, 'split_group:17', 1).map(key), ['split_group:17', 'policy:9', 'policy:3', 'policy:4', 'policy:5']);
  assert.deepEqual(rows.map(key), original);
  assert.deepEqual(moved.map(key).sort(), original.slice().sort());
});

test('invalid positions or missing rows never remove data, bounds clamp to first and last', () => {
  const original = rows.map(key);
  for (const position of [NaN, Infinity, -Infinity]) assert.deepEqual(moveEntryRow(rows, 'policy:9', position).map(key), original);
  assert.deepEqual(moveEntryRow(rows, 'policy:999', 1).map(key), original);
  assert.deepEqual(moveEntryRow(rows, 'policy:5', -50).map(key), ['policy:5', 'policy:9', 'split_group:17', 'policy:3', 'policy:4']);
  assert.deepEqual(moveEntryRow(rows, 'policy:9', 999).map(key), ['split_group:17', 'policy:3', 'policy:4', 'policy:5', 'policy:9']);
  assert.deepEqual(moveEntryRow([], 'policy:9', 1), []);
});

test('adding selected rules preserves all existing collection members and removes duplicates', () => {
  const members = [{ kind: 'policy', id: 9 }, { kind: 'split_group', id: 17 }];
  assert.deepEqual(plain(mergeEntryCollectionKeys(members, ['policy:5', 'policy:9', 'policy:5'])), ['policy:9', 'split_group:17', 'policy:5']);
  assert.deepEqual(plain(mergeEntryCollectionKeys(members, [])), ['policy:9', 'split_group:17']);
  assert.deepEqual(plain(mergeEntryCollectionKeys(members, ['unknown:3', 'policy:0', 'policy:-2', 'policy:NaN', 'policy:9007199254740992'])), ['policy:9', 'split_group:17']);
  assert.deepEqual(members, [{ kind: 'policy', id: 9 }, { kind: 'split_group', id: 17 }]);
});
