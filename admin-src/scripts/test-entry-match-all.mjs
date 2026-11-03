import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { test } from 'node:test';
import ts from 'typescript';

const helper = await readFile(new URL('../src/pages/clientEntryMatchAll.ts', import.meta.url), 'utf8');
const page = await readFile(new URL('../src/pages/ClientEntryUserPolicyPage.tsx', import.meta.url), 'utf8');
const { outputText } = ts.transpileModule(helper, { compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2020 } });
const { requiresMatchAllConfirmation } = await import(`data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`);

test('match-all confirmation applies only to newly introduced empty condition sets', () => {
  const limited = [{ field: 'ua', operator: 'contains_any', values: ['Forest'] }];
  assert.equal(requiresMatchAllConfirmation(true, [], []), true);
  assert.equal(requiresMatchAllConfirmation(false, limited, []), true);
  assert.equal(requiresMatchAllConfirmation(false, [], []), false);
  assert.equal(requiresMatchAllConfirmation(true, [], limited), false);
  assert.equal(requiresMatchAllConfirmation(false, limited, limited), false);
});

test('explicit empty email list never counts as match-all', () => {
  const denyAll = [{ field: 'email', operator: 'in', values: [] }];
  assert.equal(requiresMatchAllConfirmation(true, [], denyAll), false);
  assert.equal(requiresMatchAllConfirmation(false, denyAll, []), true);
});

test('editor requires deliberate checkbox and resets acknowledgment on reopen or adding conditions', () => {
  assert.match(page, /const needsMatchAllConfirmation = requiresMatchAllConfirmation\(!row\?\.id, parseConditions\(row\?\.conditions\), conditions\)/);
  assert.match(page, /confirm_match_all: false/);
  assert.match(page, /confirm_match_all: source\.confirm_match_all === true/);
  assert.match(page, /needsMatchAllConfirmation && <Form\.Item[\s\S]*?name="confirm_match_all"[\s\S]*?preserve=\{false\}[\s\S]*?value === true/);
  assert.match(page, /仍受节点权限和规则优先级约束/);
});
