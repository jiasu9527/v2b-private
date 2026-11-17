import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { test } from 'node:test';

const panel = await readFile(new URL('../src/pages/NodeObservationPanel.tsx', import.meta.url), 'utf8');
const parent = await readFile(new URL('../src/pages/EntryEvidencePanel.tsx', import.meta.url), 'utf8');

test('observation has separate administrator configuration and investigation views', () => {
  assert.match(parent, /isAdmin \? \[\{ key: 'node-observation', label: '节点访问观察', children: <NodeObservationPanel \/>/);
  for (const endpoint of ['/nodes', '/events', '/summary']) assert.ok(panel.includes('`${ENDPOINT}' + endpoint + '`'), endpoint);
  assert.match(panel, /apiGet\(/);
  assert.match(panel, /apiJsonPost\(`\$\{ENDPOINT\}\/nodes`/);
  assert.match(panel, /mode: editor\.mode/);
  assert.match(panel, /node\.mode === 'all'/);
  assert.match(panel, /该节点全部已鉴权用户/);
  assert.match(panel, /setIssuedToken\(result\.token\)/);
  assert.match(panel, /onCancel=\{\(\) => setIssuedToken\(''\)\}/);
  assert.doesNotMatch(panel, /localStorage|sessionStorage|X-Observation-Token/);
});

test('observation distinguishes missing coverage from no activity', () => {
  for (const wording of ['尚未启用节点观察', '域名未知', '没有记录不等于用户没有访问', '这不是采集完整率', '不能从共同域名或共同 IP 直接判断账号关联']) {
    assert.ok(panel.includes(wording), wording);
  }
  assert.match(panel, /node_id/);
  assert.match(panel, /user_id/);
  assert.match(panel, /target_port/);
  assert.match(panel, /uplink_bytes/);
  assert.match(panel, /downlink_bytes/);
});
