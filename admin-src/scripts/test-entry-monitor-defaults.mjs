import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { test } from 'node:test';
import ts from 'typescript';

// Execute the actual pure normalizers without loading React or its CSS in Node.
const source = await readFile(new URL('../src/pages/DNSFailoverEntryMonitorTab.tsx', import.meta.url), 'utf8');
const ast = ts.createSourceFile('DNSFailoverEntryMonitorTab.tsx', source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
const functions = new Set(['list', 'numberValue', 'boolValue', 'normalizeAction', 'memberSourceKey', 'memberHost', 'normalizePolicy', 'derivedPolicyTargets', 'normalizeTarget', 'normalizeDraft']);
const constants = new Set(['DEFAULT_ENTRY_MONITOR_PORT', 'DEFAULT_ENTRY_MONITOR_TIMEOUT_MS', 'DEFAULT_ENTRY_MONITOR_FAILURE_THRESHOLD', 'DEFAULT_ENTRY_MONITOR_SUCCESS_THRESHOLD']);
const declarations = ast.statements.filter((node) =>
  (ts.isFunctionDeclaration(node) && functions.has(node.name?.text)) ||
  (ts.isVariableStatement(node) && node.declarationList.declarations.some((declaration) => constants.has(declaration.name.getText(ast)))));
assert.equal(declarations.length, functions.size + constants.size);
const fixture = declarations.map((node) => node.getText(ast)).join('\n') + '\nexport { normalizePolicy, derivedPolicyTargets, normalizeTarget, normalizeDraft };';
const { outputText } = ts.transpileModule(fixture, { compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2020 } });
const { normalizePolicy, derivedPolicyTargets, normalizeTarget, normalizeDraft } = await import(`data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`);

test('new ordinary and fixed leaf entry monitors use TCP 54101', () => {
  const ordinary = normalizePolicy({ id: 1, name: 'VIP', action: 'override', entry_host: 'entry.example.test' });
  assert.equal(derivedPolicyTargets(ordinary)[0].port, 54101);
  const split = normalizePolicy({ id: 2, action: 'override', targets: [
    { source_key: 'policy:2:split-group:10', host: 'leaf.example.test', suggested_port: 54101 },
    { source_key: 'policy:2:split-group:11', host: 'fallback.example.test' },
    { source_key: 'policy:2:split-group:12', host: 'null.example.test', suggested_port: null },
  ] });
  assert.deepEqual(derivedPolicyTargets(split).map((target) => target.port), [54101, 54101, 54101]);
  assert.equal(normalizeDraft(ordinary).targets[0].port, 54101);
});

test('original-node ports and explicit candidate ports are not overwritten', () => {
  const original = normalizePolicy({ id: 3, action: 'original', members: [
    { host: 'tls.example.test', port: 443 },
    { host: 'custom.example.test', server_port: 8443 },
    { host: 'unset.example.test' },
  ] });
  assert.deepEqual(derivedPolicyTargets(original).map((target) => target.port), [443, 8443, 54101]);
  const candidates = normalizePolicy({ id: 4, targets: [
    { host: 'saved.example.test', port: 8443, suggested_port: 54101 },
    { host: 'tls.example.test', suggested_port: 443 },
  ] });
  assert.deepEqual(derivedPolicyTargets(candidates).map((target) => target.port), [8443, 443]);
});

test('saved monitor ports survive reload; missing ports default to 54101', () => {
  const policy = normalizePolicy({ id: 1, action: 'override', entry_host: 'entry.example.test' });
  for (const port of [443, 8443, 54101]) {
    const draft = normalizeDraft(policy, { targets: [{ source_key: 'policy:1', host: 'entry.example.test', port }] });
    assert.equal(draft.targets[0].port, port);
  }
  assert.equal(normalizeTarget({ host: 'missing.example.test' }, 0).port, 54101);
  assert.equal(normalizeTarget({ host: 'null.example.test', port: null }, 0).port, 54101);
  // Invalid explicit input remains invalid for save validation, not a hidden reset.
  assert.equal(normalizeTarget({ host: 'invalid.example.test', port: 0 }, 0).port, 0);
  assert.deepEqual(derivedPolicyTargets(normalizePolicy({ id: 5, action: 'hide', entry_host: 'hidden.example.test' })), []);
});
