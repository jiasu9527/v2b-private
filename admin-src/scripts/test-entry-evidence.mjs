import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { test } from 'node:test';
import ts from 'typescript';

const source = await readFile(new URL('../src/pages/entryEvidence.ts', import.meta.url), 'utf8');
const panel = await readFile(new URL('../src/pages/EntryEvidencePanel.tsx', import.meta.url), 'utf8');
const page = await readFile(new URL('../src/pages/ClientEntryUserPolicyPage.tsx', import.meta.url), 'utf8');
const { outputText } = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2020 } });
const lib = await import(`data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`);
const filters = { target: 'policy:5', host: ' entry.example.test ', port: 54101, from: 1000, to: 2000, ua: ' Forest ', account: ' user@example.test ' };

test('monitor deep links select exact leaf and preserve the explicit incident window', () => {
  assert.deepEqual(lib.evidenceDeepLink('', '#/server/client-entry-user-policy?evidence=1&policy_id=4&group_id=9&host=192.0.2.1&port=54101&from=100&to=200', 999), { target: 'split_group:9', host: '192.0.2.1', port: 54101, from: 100, to: 200, ua: '', account: '' });
  assert.equal(lib.evidenceDeepLink('?policy_id=4', '', 100000).target, 'policy:4');
  assert.equal(lib.evidenceDeepLink('', '#/path?group_id=NaN&policy_id=-1', 100000).target, '');
  assert.equal(lib.evidenceDeepLink('', '#/path', 100000).port, 54101);
});

test('query preserves target identity, exact window and every submitted filter', () => {
  assert.deepEqual(lib.evidenceQuery(filters, 3, 50), { kind: 'policy', target_id: 5, host: 'entry.example.test', port: 54101, from: 1000, to: 2000, ua: 'Forest', account: 'user@example.test', current: 3, page_size: 50 });
  assert.deepEqual(lib.evidenceTarget('split_group:42'), { kind: 'split_group', target_id: 42 });
  assert.equal(lib.evidenceTarget(''), null);
  for (const invalid of ['policy:0', 'policy:-1', 'split_group:1x', 'other:1', 'policy:9007199254740992']) assert.throws(() => lib.evidenceTarget(invalid));
  assert.equal(lib.evidenceQuery({ ...filters, target: '', ua: '', account: '' }).kind, undefined);
  assert.equal(lib.evidenceQuery(filters, 0, 1000).page_size, 100);
});

test('invalid or partial inputs never crash dirty-state rendering but cannot be submitted', () => {
  for (const invalid of [{ to: 1000 }, { from: NaN }, { port: 0 }, { port: 65536 }, { port: 1.5 }, { target: 'bad' }, { to: 1000 + 32 * 86400 }]) {
    const value = { ...filters, ...invalid };
    assert.doesNotThrow(() => lib.evidenceFilterKey(value));
    assert.throws(() => lib.evidenceQuery(value));
  }
  assert.match(lib.evidenceFilterError({ ...filters, target: '' }, true), /指定/);
  assert.match(lib.evidenceFilterError({ ...filters, host: '' }, true), /入口/);
  assert.match(lib.evidenceFilterError({ ...filters, port: undefined }, true), /端口/);
  assert.equal(lib.evidenceFilterKey(filters), lib.evidenceFilterKey({ ...filters, host: filters.host.trim(), ua: 'Forest', account: 'user@example.test' }));
});

test('frozen payload binds the exact rule version and candidate evidence hash', () => {
  const payload = lib.evidenceFreezePayload(filters, 'rule-revision', 'candidate-hash', ' fixture ', [9, 3, 9]);
  assert.equal(payload.revision, 'rule-revision');
  assert.equal(payload.expected_candidates_hash, 'candidate-hash');
  assert.equal(payload.title, 'fixture');
  assert.deepEqual(payload.selected_user_ids, [3, 9]);
  assert.equal(payload.current, undefined);
  assert.equal(payload.page_size, undefined);
  assert.equal(lib.evidenceFreezePayload(filters, 'rule', 'hash', '').selected_user_ids, undefined, 'omitted selection means explicitly chosen all-matching action');
  assert.deepEqual(lib.evidenceFreezePayload(filters, 'rule', 'hash', '', [1, 1]).selected_user_ids, [1], 'a single user may be archived, but is not a splittable group');
  for (const selection of [[], [1, -1], [1, 1.2]]) assert.throws(() => lib.evidenceFreezePayload(filters, 'rule', 'hash', '', selection));
  assert.throws(() => lib.evidenceFreezePayload(filters, '', 'hash', ''));
  assert.throws(() => lib.evidenceFreezePayload(filters, 'rule', '', ''));
});

test('pool execution never resubmits stale host previews; manual execution requires two distinct hosts', () => {
  const record = { id: 6, revision: 3 };
  assert.deepEqual(lib.evidenceSplitPayload(record, true, 'old-a', 'old-b'), { case_id: 6, revision: 3, use_backup_ip_pool: true });
  assert.deepEqual(lib.evidenceSplitPayload(record, false, ' a.test ', 'b.test'), { case_id: 6, revision: 3, use_backup_ip_pool: false, entry_host_a: 'a.test', entry_host_b: 'b.test' });
  for (const hosts of [['', 'b'], ['a', ''], ['SAME', 'same']]) assert.throws(() => lib.evidenceSplitPayload(record, false, ...hosts));
  assert.throws(() => lib.evidenceSplitPayload({ id: 6, revision: 0 }, true, '', ''));
});

test('options retain disabled targets without confusing policy IDs and group IDs', () => {
  const options = lib.evidenceTargetOptions([{ id: 1, name: 'normal', enabled: 0 }, { id: 1, name: 'parent', enabled: 1, __split_group: { id: 1, name: 'leaf', entry_host: 'leaf.test' }, __entry_collection: { entry_host: 'collection.test' } }, { id: 1 }, null, { id: -3 }]);
  assert.equal(options.length, 2);
  assert.match(options[0].label, /已停用/);
  assert.equal(options[1].value, 'split_group:1');
  assert.equal(options[1].host, 'collection.test');
});

test('304, partial and failed responses cannot be presented as client receipt', () => {
  assert.match(lib.evidenceResultLabel({ status: 304, response_complete: true, coverage: 'complete' }), /未下发新正文/);
  assert.match(lib.evidenceResultLabel({ status: 500, response_complete: true, coverage: 'complete' }), /非有效下发/);
  assert.match(lib.evidenceResultLabel({ status: 200, response_complete: false, coverage: 'complete' }), /未完成/);
  assert.match(lib.evidenceResultLabel({ status: 200, response_complete: true, coverage: 'partial' }), /部分覆盖/);
  assert.match(lib.evidenceResultLabel({ status: 200, response_complete: true, coverage: 'complete' }), /服务端写入成功/);
  assert.doesNotMatch(lib.evidenceResultLabel({ status: 200, response_complete: true, coverage: 'complete' }), /客户端.*收到/);
});

test('observation durations and conclusion choices preserve evidence limits', () => {
  assert.equal(lib.evidenceDuration(0), '0 秒');
  assert.equal(lib.evidenceDuration(90), '1 分钟');
  assert.equal(lib.evidenceDuration(3660), '1 小时 1 分钟');
  assert.equal(lib.evidenceDuration(90000), '1 天 1 小时');
  assert.equal(lib.evidenceDuration(NaN), '—');
  assert.deepEqual(lib.EVIDENCE_CONCLUSIONS, ['正常', '单组复现', '两组异常', '未复现', '证据不足']);
  assert.match(panel, /尚无记录人数/);
  assert.match(panel, /已记录服务端下发人数/);
  assert.match(panel, /evidence_truncated/);
  assert.match(panel, /<CandidateEvidence user=\{user\}/);
  assert.doesNotMatch(panel, /未拉取人数|尚未拉取|已连接人数/);
});

test('paged case tree links local rounds and marks unloaded parents without cyclic rendering', () => {
  const records = [{ id: 1 }, { id: 2, parent_case_id: 1 }, { id: 3, parent_case_id: 2 }, { id: 4, parent_case_id: 99 }];
  const tree = lib.evidenceCaseTree(records);
  assert.deepEqual(tree.map((item) => item.record.id), [1, 4]);
  assert.equal(tree[0].children[0].children[0].record.id, 3);
  assert.equal(tree[1].unloadedParentID, 99);
  assert.deepEqual(lib.evidenceCaseTree([]), []);
  const cyclic = lib.evidenceCaseTree([{ id: 1, parent_case_id: 2 }, { id: 2, parent_case_id: 1 }, { id: 3, parent_case_id: 3 }]);
  assert.equal(cyclic.length, 3);
  assert.ok(cyclic.every((item) => item.children.length === 0));
  assert.match(panel, /仅展示当前页已加载档案，不是完整排查树/);
});

test('context classifications cannot become selectable frozen evidence and observation refresh preserves drafts', () => {
  assert.deepEqual(Object.keys(lib.EVIDENCE_CLASSIFICATIONS), ['window_delivered', 'historical_exposure', 'request_only']);
  const contextualPanel = panel.slice(panel.indexOf('const contextualEvidence'), panel.indexOf('const filters'));
  assert.match(contextualPanel, /不会加入勾选或冻结名单/);
  assert.doesNotMatch(contextualPanel, /rowSelection|evidenceFreezePayload/);
  assert.match(panel, /ENTRY_EVIDENCE_PATH\}\/context/);
  assert.match(panel, /contextSequence/);
  assert.match(panel, /resetDraft \|\| draftCaseID.current !== id \|\| !dirtyDraft/);
  assert.match(panel, /revision: draftBase.current.revision/);
  assert.match(panel, /freeze_allowed/);
  assert.match(panel, /selected.length < 1 \|\| !freezeAllowed/);
  assert.match(panel, /record.selected_count < 2/);
  assert.match(panel, /monitoring_status/);
  assert.match(panel, /monitoring_warning/);
  assert.match(panel, /search\('candidates', appliedCandidates, page, size, false\)/);
  assert.match(panel, /if \(resetSelection \|\| next.candidates_hash/);
});

test('probe archives distinguish observed events, retention gaps and limited recent display', () => {
  assert.match(panel, /<ProbeEvents record=\{record\}/);
  assert.match(panel, /probe_events_total/);
  assert.match(panel, /probe_events_truncated/);
  assert.match(panel, /probe_events_gap/);
  assert.match(panel, /最多最近 100 条/);
  assert.match(panel, /不自动判断被墙或用户责任/);
  assert.match(panel, /没有事件不代表入口正常/);
  assert.match(panel, /未展示不等于不存在/);
});

test('UI binds separate submitted query scopes and protects mutation boundaries', () => {
  assert.match(panel, /appliedDeliveries/);
  assert.match(panel, /appliedCandidates/);
  assert.match(panel, /expected_candidates_hash|evidenceFreezePayload/);
  assert.match(panel, /preview\.revision !== record\.revision/);
  assert.match(panel, /split-execute`, input/);
  assert.match(panel, /isAdmin && <Popconfirm/);
  assert.match(panel, /isAdmin \? \[\{ key: 'settings'/);
  assert.match(panel, /caseSequence\.current\+\+/);
  assert.match(panel, /queryRequest\.current\?\.abort/);
  assert.match(panel, /setPreview\(null\); setPreviewInput\(null\)/);
  assert.match(page, /value: 'evidence', label: '异常排查'/);
  assert.match(page, /get\('evidence'\) === '1'/);
  assert.match(page, /<EntryEvidencePanel rows=\{rows\}/);
  assert.doesNotMatch(panel, /dangerouslySetInnerHTML/);
});
