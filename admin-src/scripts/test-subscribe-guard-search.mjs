import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { test } from 'node:test';
import ts from 'typescript';

const source = await readFile(new URL('../src/lib/subscribeGuardSearch.ts', import.meta.url), 'utf8');
const { outputText } = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2020 } });
const { emptyGuardFilters, normalizedGuardFilters, entrySearchSelection, guardFilterError, guardSearchParams, guardFilterFingerprint, entrySearchOptions } = await import(`data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`);

test('empty form does not load all users and sort alone is not a filter', () => {
  assert.match(guardFilterError(emptyGuardFilters()), /至少填写/);
  assert.match(guardFilterError({ ...emptyGuardFilters(), sort: 'count' }), /至少填写/);
  assert.throws(() => guardSearchParams(emptyGuardFilters()), /至少填写/);
  assert.equal(guardFilterError({ ...emptyGuardFilters(), result: 'blocked' }), '');
});

test('account, rule, UA, event time and result remain one AND request', () => {
  const filters = { ...emptyGuardFilters(), account: '  user_1%+ops@example.test  ', account_match: 'exact', entry: 'policy:12/group:34', ua: '  Forest Android  ', ua_match: 'not_contains', start_at: 1700000000, end_at: 1700086400, result: 'blocked', sort: 'count' };
  const original = structuredClone(filters);
  assert.deepEqual(guardSearchParams(filters, 2, 50), { account: 'user_1%+ops@example.test', account_match: 'exact', policy_id: 12, group_id: 34, ua: 'Forest Android', ua_match: 'not_contains', start_at: 1700000000, end_at: 1700086400, result: 'blocked', sort: 'count', current: 2, page_size: 50 });
  assert.deepEqual(filters, original);
});

test('all supported UA operators and numeric user IDs are passed unchanged', () => {
  for (const ua_match of ['contains', 'equals', 'not_contains']) {
    const params = guardSearchParams({ ...emptyGuardFilters(), account: '  12345 ', ua: ' Foo/1.0 ', ua_match });
    assert.equal(params.ua_match, ua_match);
    assert.equal(params.account, '12345');
    assert.equal(params.ua, 'Foo/1.0');
  }
});

test('account and policy only queries do not accidentally impose log conditions', () => {
  assert.deepEqual(guardSearchParams({ ...emptyGuardFilters(), account: 'member' }), { account: 'member', account_match: 'contains', result: 'all', sort: 'last_at', current: 1, page_size: 20 });
  assert.deepEqual(guardSearchParams({ ...emptyGuardFilters(), entry: 'policy:7' }), { policy_id: 7, result: 'all', sort: 'last_at', current: 1, page_size: 20 });
});

test('time boundary remains start inclusive/end exclusive without day or timezone rounding', () => {
  const filters = { ...emptyGuardFilters(), start_at: 1700000012, end_at: 1700000013 };
  assert.equal(guardFilterError(filters), '');
  assert.equal(guardSearchParams(filters).start_at, 1700000012);
  assert.equal(guardSearchParams(filters).end_at, 1700000013);
  assert.match(guardFilterError({ ...filters, end_at: undefined }), /完整/);
  assert.match(guardFilterError({ ...filters, start_at: undefined }), /完整/);
  for (const end_at of [1700000012, 1700000011, NaN, Infinity, 1700000012.5]) assert.match(guardFilterError({ ...filters, end_at }), /晚于/);
});

test('rule and child group identifiers cannot be mixed or silently dropped', () => {
  assert.deepEqual(entrySearchSelection(''), {});
  assert.deepEqual(entrySearchSelection('policy:12'), { policy_id: 12 });
  assert.deepEqual(entrySearchSelection('policy:12/group:34'), { policy_id: 12, group_id: 34 });
  for (const value of ['group:34', 'policy:0', 'policy:12/group:0', 'policy:12/group:34/extra', 'policy:-1']) assert.throws(() => entrySearchSelection(value), /入口规则/);
});

test('rule options show ordinary and fixed parent plus leaf groups only with disabled state', () => {
  const policies = [
    { id: 1, name: 'VIP', enabled: 1, mode: 'standard' },
    { id: 2, name: '固定组', enabled: 1, mode: 'split', split_groups: [{ id: 20, name: '父级', is_leaf: false }, { id: 22, name: 'B', is_leaf: 1, sort: 2 }, { id: 21, name: 'A', is_leaf: true, sort: 1 }, { id: 23, name: '旧字段缺失', sort: 0 }] },
    { id: 3, name: '已停用规则', enabled: 0, mode: 'split', split_groups: [{ id: 30, name: '停用子组', is_leaf: true }] },
  ];
  const original = structuredClone(policies);
  const options = entrySearchOptions(policies);
  assert.deepEqual(options.map((option) => option.value), ['policy:1', 'policy:2', 'policy:2/group:21', 'policy:2/group:22', 'policy:3', 'policy:3/group:30']);
  assert.match(options[1].label, /全部子组/);
  assert.equal(options[0].disabled, false);
  assert.equal(options[4].disabled, true);
  assert.match(options[5].label, /已停用/);
  assert.deepEqual(policies, original);
  assert.deepEqual(entrySearchOptions(undefined), []);
});

test('draft edits cannot mutate submitted filters used for pagination', () => {
  let draft = { ...emptyGuardFilters(), account: '  first@example.test  ', ua: 'Forest' };
  const applied = normalizedGuardFilters(draft);
  draft = { ...draft, account: 'other@example.test', ua: 'Clash', entry: 'policy:9' };
  assert.notEqual(guardFilterFingerprint(draft), guardFilterFingerprint(applied));
  assert.deepEqual(guardSearchParams(applied, 3, 10), { account: 'first@example.test', account_match: 'contains', ua: 'Forest', ua_match: 'contains', result: 'all', sort: 'last_at', current: 3, page_size: 10 });
  assert.equal(guardFilterFingerprint({ ...applied, account: ` ${applied.account} ` }), guardFilterFingerprint(applied));
  assert.equal(guardSearchParams(applied, 0, 1000).page_size, 100);
});

test('page integration keeps a static component, applied pagination, cancellation and honest summaries', async () => {
  const component = await readFile(new URL('../src/pages/SubscribeGuardSearch.tsx', import.meta.url), 'utf8');
  const page = await readFile(new URL('../src/pages/SubscribeGuardPage.tsx', import.meta.url), 'utf8');
  assert.match(page, /import SubscribeGuardSearch from '\.\/SubscribeGuardSearch'/);
  assert.doesNotMatch(page, /subscribe-guard\/(user-search|ua-search)/);
  assert.doesNotMatch(component, /import\s*\(/);
  assert.match(component, /search\(applied, pagination/);
  assert.match(component, /ticket !== sequence\.current/);
  assert.match(component, /controller\.abort\(\)/);
  assert.match(component, /signal: controller\.signal, cache: 'no-store'/);
  assert.match(component, /changed\.revision > submittedStatusRevision/);
  assert.match(component, /row\.entry_policy_uncertain === true/);
  assert.ok(component.includes('入口规则无法确定'));
  for (const text of ['不是历史分组', '同一条请求', '不影响总统计和排行榜', '已放行不代表订阅下载成功', '防护关闭期间', '0 次', '下表及翻页仍使用上次查询条件']) assert.ok(component.includes(text), text);
});
