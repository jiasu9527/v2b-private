import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { test } from 'node:test';
const source = await readFile(new URL('../src/pages/EntryHistoryIP.tsx', import.meta.url), 'utf8');
const page = await readFile(new URL('../src/pages/ClientEntryUserPolicyPage.tsx', import.meta.url), 'utf8');
test('history is reachable in entry grouping and uses actual deliveries, not current rule filters', () => {
  assert.match(page, /<EntryHistoryIP onDone=\{load\}/);
  assert.match(source, /delivery-hosts/);
  assert.match(source, /delivery-users/);
  assert.match(source, /subtract\(7, 'day'\)/);
  assert.doesNotMatch(source, /subscribe-guard|user\/fetch/);
});
test('binary creation sends the exact frozen interval and membership digest', () => {
  for (const key of ['history_host: snapshot.history_host', 'history_from: snapshot.from', 'history_to: snapshot.to', 'snapshot_hash: snapshot.snapshot_hash']) assert.ok(source.includes(key), key);
  assert.match(source, /snapshot\.user_count < minimumUsers/);
  assert.match(source, /sequence\.current/);
  assert.match(source, /已删除账号只展示历史/);
  assert.match(source, /名单不随日志过期而丢失/);
});

test('minute range and optional grouping are explicit, invalid times cannot reuse old preview', () => {
 assert.match(source, /format="YYYY-MM-DD HH:mm"/);
 assert.match(source, /history_from: from, history_to: to/);
 assert.match(source, /startOf\('minute'\)/);
 assert.match(source, /history_single_group: singleGroup/);
 assert.match(source, /singleGroup \? 1 : 2/);
 assert.match(source, /不二分，全部放一个组/);
 assert.match(source, /singleGroup \? \{\} : \{ entry_host_b/);
});
