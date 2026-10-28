import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { test } from 'node:test';
import ts from 'typescript';

const helper = await readFile(new URL('../src/pages/clientEntrySplitConditions.ts', import.meta.url), 'utf8');
const page = await readFile(new URL('../src/pages/ClientEntryUserPolicyPage.tsx', import.meta.url), 'utf8');
const ast = ts.createSourceFile('ClientEntryUserPolicyPage.tsx', page, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
const functions = new Set(['canConvertRangeToSplit', 'parseExtraNodes', 'normalizePolicyAction']);
const declarations = ast.statements.filter((node) => ts.isFunctionDeclaration(node) && functions.has(node.name?.text));
assert.equal(declarations.length, functions.size);
const fixture = helper + '\nconst singleUserIDRange = splitUserIDRange;\n' + declarations.map((node) => node.getText(ast)).join('\n') + '\nexport { canConvertRangeToSplit };';
const { outputText } = ts.transpileModule(fixture, { compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2020 } });
const { splitUserIDRange, splitUAConditions, canConvertRangeToSplit } = await import(`data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`);

const range = { field: 'user_id', operator: 'between', min: 100, max: 200 };
const ua = { field: 'ua', operator: 'contains_any', values: ['Forest', 'Clash'] };

test('ID range plus one or multiple valid UA guards enables fixed split in any order', () => {
  const combinations = [[range], [range, ua], [ua, range], [range, ua, { field: 'ua', operator: 'excludes_any', values: ['curl'] }]];
  for (const conditions of combinations) {
    const original = structuredClone(conditions);
    assert.deepEqual(splitUserIDRange(conditions), { min: 100, max: 200 });
    assert.equal(canConvertRangeToSplit({ mode: 'standard', action: 'override', conditions }), true);
    assert.deepEqual(conditions, original);
  }
  assert.deepEqual(splitUserIDRange(JSON.stringify(JSON.stringify([ua, range]))), { min: 100, max: 200 });
  assert.deepEqual(splitUserIDRange([{ ...range, min: '100', max: '200' }]), { min: 100, max: 200 });
});

test('all supported UA operators including normalized aliases preserve their guards', () => {
  for (const operator of ['empty', 'not_empty', 'is_empty', 'is_not_empty']) {
    const conditions = [{ field: 'ua', operator }, range];
    assert.deepEqual(splitUserIDRange(conditions), { min: 100, max: 200 });
    assert.equal(splitUAConditions(conditions)[0].operator, operator.replace(/^is_/, ''));
  }
  assert.deepEqual(splitUAConditions([range, { ...ua, values: [' Forest ', 'Clash'] }]), [{ field: 'ua', operator: 'contains_any', values: ['Forest', 'Clash'] }]);
  assert.deepEqual(splitUAConditions([range]), []);
});

test('unsupported or malformed conditions are never silently dropped to permit conversion', () => {
  for (const conditions of [
    [], [ua], [range, range], [range, null], [range, {}], [range, []],
    [range, { field: 'plan_id', operator: 'between', min: 1, max: 2 }],
    [range, { field: 'email', operator: 'in', values: ['x@example.test'] }],
    [range, { field: 'unknown', operator: 'eq', value: 1 }],
    [range, { ...ua, operator: 'equals' }], [range, { ...ua, values: [] }],
    [range, { ...ua, values: [''] }], [range, { ...ua, values: ['Forest', ' '] }],
    [range, { ...ua, values: [123] }], '[bad-json', { conditions: [range] },
  ]) assert.equal(splitUserIDRange(conditions), undefined, JSON.stringify(conditions));
  assert.equal(canConvertRangeToSplit({ conditions: [range, ua], __split_conversion_conditions: [range, ua, { field: 'future-field', operator: 'eq', value: 1 }] }), false);
});

test('invalid ID range bounds cannot expose conversion', () => {
  for (const changes of [{ min: 0 }, { min: -1 }, { min: 100.5 }, { min: ' ' }, { min: null }, { max: 99 }, { max: undefined }, { max: Infinity }, { max: Number.MAX_SAFE_INTEGER + 1 }]) {
    assert.equal(splitUserIDRange([{ ...range, ...changes }, ua]), undefined);
  }
});

test('existing mode, action and extra-node restrictions remain in force', () => {
  const row = { action: 'override', mode: 'standard', conditions: [range, ua] };
  for (const changes of [{ mode: 'split' }, { action: 'hide' }, { action: 'original' }, { extra_nodes: ['trojan://fixture@example.test:443'] }]) {
    assert.equal(canConvertRangeToSplit({ ...row, ...changes }), false);
  }
});

test('conversion and split editing never send replacement conditions; every leaf keeps visible UA guards', () => {
  assert.match(page, /splitUserIDRange as singleUserIDRange, splitUAConditions/);
  assert.match(page, /__split_conversion_conditions: row\.conditions/);
  assert.ok(page.includes('不按历史 UA 筛选名单'));
  assert.ok(page.includes('固定人数是用户名单人数，不代表当前使用该 UA 的人数'));
  assert.ok(page.includes('不匹配时继续后续规则'));
  assert.ok(page.includes('节点、解析设置、全局优先级和原有 UA 条件都会继承'));
  assert.ok((page.match(/<SplitUAConditions conditions=\{row\.conditions\} \/>/g) || []).length >= 4);
  for (const endpoint of ['split-convert', 'split-group', 'split-group-host']) {
    const payload = page.match(new RegExp(`apiPost\\('/server/client-entry-user-policy/${endpoint}', \\{([\\s\\S]*?)\\n      \\}\\);`));
    assert.ok(payload, endpoint);
    assert.doesNotMatch(payload[1], /conditions\s*:/, `${endpoint} must let the server retain conditions`);
  }
  const render = page.slice(page.indexOf('const renderConditions ='), page.indexOf('const renderHost ='));
  assert.match(render, /isSplitGroupDisplayRow[\s\S]*splitUAConditions\(row\.conditions\)/);
  assert.match(render, /conditionSummary\(condition\)/);
});
