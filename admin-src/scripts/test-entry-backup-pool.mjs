import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { test } from 'node:test';
import ts from 'typescript';

const source = await readFile(new URL('../src/pages/clientEntryBackupPool.ts', import.meta.url), 'utf8');
const page = await readFile(new URL('../src/pages/ClientEntryUserPolicyPage.tsx', import.meta.url), 'utf8');
const ast = ts.createSourceFile('clientEntryBackupPool.ts', source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS);
const declarations = ast.statements.filter((node) => !ts.isImportDeclaration(node)).map((node) => node.getText(ast).replace(/^export /, '')).join('\n');
const fixture = `export function makeHook(deps) { const { useEffect, useRef, useState, message, apiGet } = deps; ${declarations}\nreturn { availableBackupPoolIPs, useSplitBackupIPPool }; }`;
const { outputText } = ts.transpileModule(fixture, { compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2020 } });
const { makeHook } = await import(`data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`);
const { availableBackupPoolIPs } = makeHook({});
const healthy = (ip, fields = {}) => ({ ip, enabled: true, used: false, available: true, status: 'available', ...fields });
const pool = { data: { items: [healthy('192.0.2.11'), healthy('192.0.2.12'), healthy('192.0.2.13')] } };

function harness() {
  let cursor = 0;
  const slots = [], requests = [], notices = [], writes = [], cleanups = [];
  const form = { setFieldsValue: (values) => writes.push(values) };
  const { useSplitBackupIPPool } = makeHook({
    useState(initial) { const i = cursor++; if (!(i in slots)) slots[i] = initial; return [slots[i], (value) => { slots[i] = value; }]; },
    useRef(initial) { const i = cursor++; return slots[i] ??= { current: initial }; },
    useEffect(effect) { const i = cursor++; if (!(i in slots)) { slots[i] = true; cleanups.push(effect()); } },
    message: Object.fromEntries(['success', 'warning', 'error'].map((kind) => [kind, (value) => notices.push({ kind, value })])),
    apiGet(path) { return new Promise((resolve, reject) => requests.push({ path, resolve, reject })); },
  });
  return { requests, notices, writes, render() { cursor = 0; return useSplitBackupIPPool(form); }, unmount() { cleanups.forEach((cleanup) => cleanup?.()); } };
}

test('pool previews preserve order, remove duplicate IPs, and exclude disabled, used or unhealthy records', () => {
  const items = [
    healthy(' 192.0.2.2 '), healthy('192.0.2.2'),
    healthy('192.0.2.3', { enabled: false }), healthy('192.0.2.4', { enabled: 0 }),
    healthy('192.0.2.5', { used: true }), healthy('192.0.2.6', { used: '1' }),
    healthy('192.0.2.7', { available: false }), healthy('192.0.2.8', { available: undefined, status: 'unhealthy' }),
    healthy('2001:DB8::1'), healthy('2001:db8::1'), healthy('192.0.2.9', { available: undefined }),
    null, {}, healthy(''),
  ];
  for (const payload of [items, { items }, { backup_ips: items }, { data: { items } }]) {
    assert.deepEqual(availableBackupPoolIPs(payload), ['192.0.2.2', '2001:DB8::1', '192.0.2.9']);
  }
  for (const payload of [null, {}, { data: { items: {} } }]) assert.deepEqual(availableBackupPoolIPs(payload), []);
});

test('successful preview fills only two hosts and marks the submission for a transactional claim', async () => {
  const h = harness();
  const filling = h.render().fill();
  assert.equal(h.render().filling, true);
  assert.equal(h.render().isFilling(), true);
  await h.render().fill();
  assert.equal(h.requests.length, 1, 'ignore repeated clicks while a request is in flight');
  assert.equal(h.requests[0].path, '/dns-failover/entry-monitors/backup-ips');
  h.requests[0].resolve(pool);
  await filling;
  assert.deepEqual(h.writes, [{ entry_host_a: '192.0.2.11', entry_host_b: '192.0.2.12' }]);
  assert.equal(h.render().usePool, true);
  assert.equal(h.render().filling, false);
  h.render().reset(); // Wired to manual edits and every dialog opening/closing.
  assert.equal(h.render().usePool, false);
});

test('insufficient pool or fetch errors do not overwrite hosts or enable pool submission', async () => {
  for (const response of [{ items: [] }, { items: [healthy('192.0.2.1')] }, new Error('offline')]) {
    const h = harness();
    const filling = h.render().fill();
    if (response instanceof Error) h.requests[0].reject(response); else h.requests[0].resolve(response);
    await filling;
    assert.deepEqual(h.writes, []);
    assert.equal(h.render().usePool, false);
    assert.equal(h.render().filling, false);
    assert.equal(h.notices[0].kind, response instanceof Error ? 'error' : 'warning');
  }
});

test('manual edits or closing the dialog discard late pool responses and errors', async () => {
  for (const failure of [false, true]) {
    const h = harness();
    const filling = h.render().fill();
    h.render().reset();
    if (failure) h.requests[0].reject(new Error('late failure')); else h.requests[0].resolve(pool);
    await filling;
    assert.deepEqual(h.writes, []);
    assert.deepEqual(h.notices, []);
    assert.equal(h.render().usePool, false);
    assert.equal(h.render().filling, false);
  }
});

test('a failed or insufficient refresh keeps prior pool hosts subject to transactional claiming', async () => {
  for (const response of [{ items: [] }, new Error('offline')]) {
    const h = harness();
    const first = h.render().fill();
    h.requests[0].resolve(pool);
    await first;
    assert.equal(h.render().usePool, true);
    const refresh = h.render().fill();
    assert.equal(h.render().usePool, true);
    if (response instanceof Error) h.requests[1].reject(response); else h.requests[1].resolve(response);
    await refresh;
    assert.equal(h.writes.length, 1);
    assert.equal(h.render().usePool, true, 'never silently downgrade pool preview to manual hosts');
    assert.equal(h.render().filling, false);
  }
});

test('an old request cannot overwrite a reopened dialog or clear its new loading state', async () => {
  const h = harness();
  const first = h.render().fill();
  h.render().reset();
  const second = h.render().fill();
  h.requests[0].resolve(pool);
  await first;
  assert.deepEqual(h.writes, []);
  assert.equal(h.render().filling, true);
  h.requests[1].resolve({ items: [healthy('198.51.100.1'), healthy('198.51.100.2')] });
  await second;
  assert.deepEqual(h.writes, [{ entry_host_a: '198.51.100.1', entry_host_b: '198.51.100.2' }]);
  assert.equal(h.render().usePool, true);
});

test('unmounted split forms never receive a late pool result', async () => {
  const h = harness();
  const filling = h.render().fill();
  h.unmount();
  h.requests[0].resolve(pool);
  await filling;
  assert.deepEqual(h.writes, []);
  assert.deepEqual(h.notices, []);
});

test('conversion and resplit wire preview, edit reset and submit guards without replacing UA conditions', () => {
  for (const [name, next, endpoint] of [
    ['RangePolicySplitConverter', 'SplitGroupUsersModal', 'split-convert'],
    ['SplitGroupRowActions', 'SimulationModal', 'split-group'],
  ]) {
    const component = page.slice(page.indexOf(`function ${name}(`), page.indexOf(`function ${next}(`));
    assert.match(component, /useSplitBackupIPPool\(/);
    assert.match(component, /从备用 IP 池自动填入/);
    assert.match(component, /onValuesChange=\{backupPool\.reset\}/);
    assert.match(component, /onCancel=\{\(\) => \{ backupPool\.reset\(\);/);
    assert.match(component, /if \(saving \|\| backupPool\.isFilling\(\)\) return/);
    assert.match(component, /okButtonProps=\{\{ disabled: backupPool\.filling \}\}/);
    assert.match(component, /<SplitUAConditions conditions=\{row\.conditions\} \/>/);
    const payload = component.match(new RegExp(`apiPost\\('/server/client-entry-user-policy/${endpoint}', \\{([\\s\\S]*?)\\n      \\}\\);`));
    assert.ok(payload, endpoint);
    assert.match(payload[1], /use_backup_ip_pool: backupPool\.usePool/);
    assert.doesNotMatch(payload[1], /conditions\s*:/);
  }
});
