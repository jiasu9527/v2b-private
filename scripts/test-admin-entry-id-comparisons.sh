#!/usr/bin/env bash
set -euo pipefail

TASK_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${TASK_ROOT}/admin-src"

node --input-type=module <<'NODE'
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import ts from 'typescript';

const page = await readFile('src/pages/ClientEntryUserPolicyPage.tsx', 'utf8');
const ast = ts.createSourceFile('ClientEntryUserPolicyPage.tsx', page, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
const names = new Set([
  'fieldOptions', 'operatorOptions', 'defaultOperator', 'parseConditions',
  'finiteNumber', 'cleanConditions', 'validateConditionValues', 'conditionSummary',
]);
const declarations = ast.statements.filter((node) =>
  (ts.isFunctionDeclaration(node) && names.has(node.name?.text)) ||
  (ts.isVariableStatement(node) && node.declarationList.declarations.some((declaration) => names.has(declaration.name.getText(ast)))));
assert.equal(declarations.length, names.size, 'must exercise the actual page definitions');
const fixture = declarations.map((node) => node.getText(ast)).join('\n') +
  '\nexport { operatorOptions, defaultOperator, parseConditions, cleanConditions, validateConditionValues, conditionSummary };';
const { outputText } = ts.transpileModule(fixture, {
  compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2020 },
});
const { operatorOptions, defaultOperator, parseConditions, cleanConditions, validateConditionValues, conditionSummary } =
  await import(`data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`);

for (const [operator, label, symbol] of [
  ['eq', '等于', '='], ['gt', '大于', '>'], ['gte', '大于等于', '≥'],
  ['lt', '小于', '<'], ['lte', '小于等于', '≤'],
]) {
  assert.deepEqual(operatorOptions.user_id.find((item) => item.value === operator), { label, value: operator });
  for (const value of [1, 99999, 100000, 100001, Number.MAX_SAFE_INTEGER]) {
    const condition = { field: 'user_id', operator, value };
    const input = JSON.stringify([{ ...condition, value: String(value) }]);
    const parsed = parseConditions(input);
    const cleaned = cleanConditions(parsed);
    assert.deepEqual(cleaned, [condition], `${operator} must preserve its threshold when editing`);
    assert.equal(validateConditionValues(cleaned), '');
    assert.equal(conditionSummary(cleaned[0]), `用户 ID ${symbol} ${value}`);
    assert.deepEqual(cleanConditions(parseConditions(JSON.stringify(cleaned))), cleaned);
  }
  assert.notEqual(validateConditionValues([{ field: 'user_id', operator }]), '', 'missing thresholds must be rejected');
}

assert.equal(defaultOperator.user_id, 'in', 'keep the existing new-condition default');
assert.deepEqual(operatorOptions.user_id.slice(0, 2), [
  { label: '指定用户', value: 'in' }, { label: 'ID 范围（含边界）', value: 'between' },
]);
const range = { field: 'user_id', operator: 'between', min: 1, max: 100000 };
assert.deepEqual(cleanConditions([range]), [range]);
assert.equal(conditionSummary(range), '用户 ID：1 ～ 100000');
assert.equal(conditionSummary({ field: 'user_id', operator: 'in', values: [1, 100000] }), '指定用户：#1、#100000');
console.log('admin entry ID comparison tests passed');
NODE

node --test scripts/test-entry-split-ua.mjs
