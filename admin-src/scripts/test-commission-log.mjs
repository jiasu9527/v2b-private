import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import ts from 'typescript';

const source = await readFile(new URL('../src/pages/commissionLogModel.ts', import.meta.url), 'utf8');
const helpers = {};
new Function('exports', ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2020 } }).outputText)(helpers);
const { hasCommissionSelection, commissionApplyRequest } = helpers;

test('visibility alone is whole-table scope; status zero remains a real filter', () => {
  for (const filter of [{}, { visibility: 'active' }, { visibility: 'deleted' }, { ids: [] }, { user_id: null }, { trade_no: '  ' }]) assert.equal(hasCommissionSelection(filter), false);
  for (const filter of [{ id: 1 }, { ids: [1, 2] }, { invite_user_id: 61624 }, { user_id: 81886 }, { trade_no: 'order-1' }, { created_from: 0 }, { commission_status: 0 }]) assert.equal(hasCommissionSelection(filter), true);
});

test('batch retries reuse the exact preview and key, never send a fresh filter', () => {
  const draft = { action: 'delete', filter: { invite_user_id: 61624 } };
  const preview = { action: 'delete', preview_hash: 'frozen-preview', all_records: false };
  const first = commissionApplyRequest(draft, preview, 'stable-key', false);
  draft.filter.invite_user_id = 81886;
  assert.deepEqual(commissionApplyRequest(draft, preview, 'stable-key', false), first);
  assert.deepEqual(first, { path: '/commission-log/delete-many', body: { preview_hash: 'frozen-preview', idempotency_key: 'stable-key', confirm: true, confirm_all: false } });
});

test('single delete binds ID and restore uses only a matching restore preview', () => {
  const preview = { action: 'delete', preview_hash: 'preview', all_records: false };
  const req = commissionApplyRequest({ action: 'delete', filter: { id: 15 }, singleID: 15 }, preview, 'key', false);
  assert.equal(req.path, '/commission-log/delete'); assert.equal(req.body.id, 15);
  assert.throws(() => commissionApplyRequest({ action: 'restore' }, preview, 'key', false));
  assert.equal(commissionApplyRequest({ action: 'restore' }, { ...preview, action: 'restore' }, 'key', false).path, '/commission-log/restore');
});

test('whole-table execution needs the second confirmation and a stable key', () => {
  const draft = { action: 'delete', filter: {} };
  const preview = { action: 'delete', preview_hash: 'preview', all_records: true };
  assert.throws(() => commissionApplyRequest(draft, preview, 'key', false));
  assert.throws(() => commissionApplyRequest(draft, preview, '', true));
  assert.equal(commissionApplyRequest(draft, preview, 'key', true).body.confirm_all, true);
});

test('order page exposes ledger as a tab; confirmation UI documents no balance changes', async () => {
  const order = await readFile(new URL('../src/pages/OrderPage.tsx', import.meta.url), 'utf8');
  const ui = await readFile(new URL('../src/pages/CommissionLogTab.tsx', import.meta.url), 'utf8');
  assert.match(order, /key: 'commission', label: '返佣记录'/);
  assert.match(ui, /不扣余额、不重发佣金/);
  assert.match(ui, /commissionApplyRequest\(draft, preview, idempotencyKey, finalAllConfirmed\)/);
  assert.match(ui, /preview\.all_records && <Checkbox/);
});
