import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { test } from 'node:test';
import ts from 'typescript';

const source = await readFile(new URL('../src/pages/subscriptionDelivery.ts', import.meta.url), 'utf8');
const panel = await readFile(new URL('../src/pages/SubscriptionDeliveryPanel.tsx', import.meta.url), 'utf8');
const page = await readFile(new URL('../src/pages/SubscribeGuardPage.tsx', import.meta.url), 'utf8');
const { outputText } = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2020 } });
const lib = await import(`data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`);
const initial = lib.initialDeliveryFilters(1700086400);

test('initial scope is last 24 hours, no guessed host, default 54101 may be cleared', () => {
  assert.equal(initial.host, '');
  assert.equal(initial.port, '54101');
  assert.equal(initial.from, 1700000000);
  assert.equal(initial.to, 1700086400);
  assert.deepEqual(lib.deliveryParams(initial), { current: 1, page_size: 20, port: '54101', from: 1700000000, to: 1700086400 });
  assert.equal(lib.deliveryParams({ ...initial, port: '' }).port, undefined);
  assert.equal(lib.deliveryParams({ ...initial, from: undefined, to: undefined }).from, undefined);
});

test('history conditions remain AND on the same request and target; input is not mutated', () => {
  const filters = { ...initial, host: ' entry.example.test ', port: ' 54101-54109 ', account: ' user@example.test ', ua: ' Forest ', policy_id: 7, group_id: 13, collection_id: 6, server_type: 'vless', server_id: -7001, rule_revision: 'a'.repeat(64) };
  const before = structuredClone(filters);
  assert.deepEqual(lib.deliveryParams(filters, 3, 50), {
    current: 3, page_size: 50, host: 'entry.example.test', port: '54101-54109', account: 'user@example.test', ua: 'Forest',
    from: 1700000000, to: 1700086400, policy_id: 7, group_id: 13, collection_id: 6, server_type: 'vless', server_id: -7001, rule_revision: 'a'.repeat(64),
  });
  assert.deepEqual(filters, before);
});

test('invalid filters and excessive paging cannot silently become broader searches', () => {
  for (const patch of [{ from: undefined }, { to: initial.from }, { to: Infinity }, { from: -1 }, { policy_id: 0 }, { group_id: -1 }, { collection_id: 0.5 }, { server_id: 0 }, { server_id: Number.MAX_SAFE_INTEGER + 1 }, { server_type: 'unknown-protocol' }, { host: 'https://example.test/subscribe?token=secret' }, { port: 'https://host' }, { rule_revision: 'short' }, { version: 'bad' }]) {
    const filters = { ...initial, ...patch };
    assert.doesNotThrow(() => lib.deliveryFilterKey(filters));
    assert.throws(() => lib.deliveryParams(filters));
  }
  assert.throws(() => lib.deliveryParams(initial, 102, 100), /翻页/);
  assert.equal(lib.deliveryParams(initial, 101, 100).current, 101);
  assert.equal(lib.deliveryParams(initial, 0, 1000).page_size, 100);
});

test('version drilldown uses exact server fingerprint while preserving user conditions', () => {
  const filters = { ...initial, account: 'alice@example.test', ua: 'Forest', server_type: 'vless', server_id: 91, collection_id: 3 };
  const version = { version: 'b'.repeat(32), rule_revision: 'a'.repeat(64), policy_id: 7, group_id: 9, collection_id: 3, provider_id: 2, source_host: 'entry.test', delivered_host: '192.0.2.3', port: '54101', source: 'split', resolved: true };
  const narrowed = lib.deliveryVersionFilters(filters, version);
  assert.deepEqual(narrowed, { ...filters, version: 'b'.repeat(32) });
  assert.equal(lib.deliveryParams(narrowed).version, version.version);
  assert.equal(lib.deliveryVersionKey(version), version.version);
  assert.notEqual(lib.deliveryFilterKey(narrowed), lib.deliveryFilterKey(filters));
  assert.throws(() => lib.deliveryVersionFilters(filters, { ...version, rule_revision: '' }), /没有下发快照版本/);
  assert.throws(() => lib.deliveryVersionFilters(filters, { ...version, version: '' }), /筛选标识无效/);
});

test('uncommitted draft edits cannot change applied pagination or version scope', () => {
  const applied = lib.normalizedDeliveryFilters({ ...initial, account: ' alice@example.test ', ua: ' Forest ' });
  const draft = { ...applied, account: 'bob@example.test', ua: 'Clash', policy_id: 10 };
  assert.notEqual(lib.deliveryFilterKey(draft), lib.deliveryFilterKey(applied));
  assert.equal(lib.deliveryParams(applied, 2, 50).account, 'alice@example.test');
  assert.equal(lib.deliveryParams(applied, 2, 50).ua, 'Forest');
  assert.equal(lib.deliveryParams(applied, 2, 50).policy_id, undefined);
});

test('missing or malformed responses are not shown as empty successful counts', () => {
  for (const value of [null, {}, { users: 0, requests: '0', first_at: 0, last_at: 0 }, { users: -1, requests: 0, first_at: 0, last_at: 0 }]) assert.throws(() => lib.deliveryStatistics(value));
  assert.equal(lib.deliveryStatistics({ users: 2, requests: 100, first_at: 1000, last_at: 2000 }).users, 2);
  for (const value of [null, {}, { items: null, total: 0, current: 1, page_size: 20 }, { items: [], total: 0, current: 0, page_size: 20 }]) assert.throws(() => lib.deliveryPage(value));
  assert.deepEqual(lib.deliveryPage({ items: [], total: 0, current: 1, page_size: 20 }).items, []);
});

test('UI remains subscription-only, collapsed, cancelable and truthful about evidence', () => {
  assert.match(page, /import SubscriptionDeliveryPanel from '\.\/SubscriptionDeliveryPanel'/);
  assert.match(page, /<SubscriptionDeliveryPanel\s*\/>/);
  assert.match(panel, /<Collapse/);
  assert.doesNotMatch(panel, /defaultActiveKey|apiPost|apiJsonPost|split-execute|freeze|propose_/);
  assert.match(panel, /ticket !== sequence\.current/);
  assert.match(panel, /pending\.current\?\.abort\(\)/);
  assert.match(panel, /signal, cache: 'no-store'/);
  assert.match(panel, /target\.filters/);
  assert.match(panel, /result\.filters, signal/);
  assert.match(panel, /rowKey="user_id"/);
  assert.match(panel, /<summary[^>]*>统计口径与记录缺口<\/summary>/);
  assert.doesNotMatch(panel, /warnings\.map\([^\n]*<Alert/);
  for (const endpoint of ['delivery-stats', 'delivery-users', 'delivery-versions']) assert.ok(panel.includes(endpoint));
  for (const text of ['仅统计完整覆盖的成功响应', '不能证明客户端已导入或使用', '首次记录不等于入口启用时间', '不能把各行人数直接相加', '本次查询未成功', '版本未知', '不会自动查询全部用户']) assert.ok(panel.includes(text), text);
});
