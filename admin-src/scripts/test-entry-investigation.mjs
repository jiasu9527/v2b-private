import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { test } from 'node:test';
import ts from 'typescript';

const source = await readFile(new URL('../src/pages/entryInvestigation.ts', import.meta.url), 'utf8');
const panel = await readFile(new URL('../src/pages/EntryInvestigationPanel.tsx', import.meta.url), 'utf8');
const parent = await readFile(new URL('../src/pages/EntryEvidencePanel.tsx', import.meta.url), 'utf8');
const css = await readFile(new URL('../src/pages/EntryInvestigationPanel.css', import.meta.url), 'utf8');
const { outputText } = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2020 } });
const lib = await import(`data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`);
const record = { id: 3, revision: 4, status: 'frozen', query: { from: 100, to: 200 } };
const start = { blockedAt: 200, note: ' 人工核实 ', hours: 24, maxRounds: 10, stopUsers: 8, confirmed: true };
const task = { case_id: 3, revision: 7, settings: {}, failures: [], report: { state: 'ready_next', executed_at: 300, branches: [{ branch: 'A' }, { branch: 'B' }], warnings: [], blockers: [], next_query: { host: '192.0.2.1' }, requires_confirmation: true } };

test('start requires a frozen case, explicit confirmation, exact pre-failure window and bounded settings', () => {
  assert.deepEqual(lib.investigationStartPayload(record, start, 400), { case_id: 3, revision: 4, blocked_at: 200, note: '人工核实', min_observe_seconds: 86400, max_rounds: 10, stop_users: 8 });
  for (const changed of [{ confirmed: false }, { blockedAt: 199 }, { blockedAt: 201 }, { blockedAt: 401 }, { note: '' }, { note: '字'.repeat(2001) }, { hours: null }, { hours: 0 }, { hours: 169 }, { hours: 1.2 }, { maxRounds: 0 }, { maxRounds: 31 }, { stopUsers: 0 }, { stopUsers: 1 }, { stopUsers: 9 }]) assert.throws(() => lib.investigationStartPayload(record, { ...start, ...changed }, 400));
  assert.throws(() => lib.investigationStartPayload({ ...record, status: 'executed' }, start, 400));
  assert.throws(() => lib.investigationStartPayload({ ...record, revision: 0 }, start, 400));
  assert.throws(() => lib.investigationStartPayload({ ...record, parent_case_id: 1 }, start, 400));
  assert.throws(() => lib.investigationStartPayload(record, { ...start, note: '证据\0说明' }, 400));
});

test('manual failure binds task revision and controls without creating an IP mutation', () => {
  const input = { branch: 'A', blockedAt: 350, note: ' 确认依据 ', controlStatus: 'unknown', confirmed: true };
  assert.deepEqual(lib.investigationConfirmPayload(task, input, 400), { case_id: 3, revision: 7, branch: 'A', blocked_at: 350, note: '确认依据', control_status: 'unknown' });
  for (const changed of [{ confirmed: false }, { branch: 'R' }, { branch: '' }, { blockedAt: 299 }, { blockedAt: 401 }, { note: '' }, { controlStatus: 'probably' }]) assert.throws(() => lib.investigationConfirmPayload(task, { ...input, ...changed }, 400));
  assert.throws(() => lib.investigationConfirmPayload({ ...task, report: { ...task.report, executed_at: 0 } }, input, 400));
  assert.throws(() => lib.investigationConfirmPayload(task, { ...input, note: '证据\0说明' }, 400));
  assert.throws(() => lib.investigationConfirmPayload(task, { ...input, blockedAt: 300 }, 400));
  assert.equal(lib.investigationConfirmPayload({ ...task, next_case_id: 9 }, input, 400).branch, 'A');
});

test('next round is allowed only for a reviewed unblocked server report and never repeats a prepared child', () => {
  assert.equal(lib.investigationCanPrepareNext(task), true);
  assert.equal(lib.investigationCanPrepareNext(null), false);
  for (const state of ['observing', 'waiting_delivery', 'evidence_gap', 'both_blocked', 'manual_review', 'unknown']) assert.equal(lib.investigationCanPrepareNext({ ...task, report: { ...task.report, state } }), false);
  assert.equal(lib.investigationCanPrepareNext({ ...task, next_case_id: 10 }), false);
  assert.equal(lib.investigationCanPrepareNext({ ...task, report: { ...task.report, blockers: ['shared host'] } }), false);
  assert.equal(lib.investigationCanPrepareNext({ ...task, report: { ...task.report, next_query: undefined } }), false);
});

test('malformed or wrong-case responses cannot overwrite an active report', () => {
  assert.equal(lib.parseEntryInvestigation(task, 3), task);
  for (const changed of [null, {}, { ...task, case_id: 0 }, { ...task, revision: 0 }, { ...task, report: null }, { ...task, failures: 'no' }, { ...task, report: { ...task.report, branches: undefined } }]) assert.throws(() => lib.parseEntryInvestigation(changed, 3));
  assert.throws(() => lib.parseEntryInvestigation(task, 8));
});

test('read-only polling aborts stale requests and does not auto execute a split or confirm blockage', () => {
  assert.match(panel, /new AbortController\(\)/);
  assert.match(panel, /token !== sequence\.current/);
  assert.match(panel, /currentCase\.current !== record\.id/);
  assert.match(panel, /readRequest\.current\?\.abort\(\)/);
  assert.match(panel, /window\.clearInterval\(timer\)/);
  assert.match(panel, /60000/);
  assert.match(panel, /cache: 'no-store'/);
  assert.match(panel, /errorText\(error\) === INVESTIGATION_NOT_FOUND/);
  assert.doesNotMatch(panel, /\/split-execute|\/split-create|\/split-group/);
  assert.match(panel, /investigationConfirmPayload\(failure\.task/);
  assert.match(panel, /revision: nextTask\.revision/);
  assert.match(panel, /仅冻结下一轮，不立即修改入口/);
  assert.match(panel, /setWriteBlocked\(true\)/);
  assert.match(panel, /if \(busyRef\.current/);
  assert.match(panel, /disabled=\{locked \|\| writeBlocked\} onClick=\{\(\) => openFailure\(branch\.branch\)\}/);
});

test('display separates assignment, confirmed server delivery and uncertain users and fits mobile', () => {
	assert.ok(panel.includes('直接在 AI 运维说'));
	assert.ok(panel.includes('本页用于查看进度，也可手动办理'));
  for (const wording of ['分配人数', '实际下发人数', '未记录下发', '异常前已下发', '不证明客户端已导入或使用', '不会认定账号本人有责任']) assert.ok(panel.includes(wording), wording);
  assert.match(panel, /人工确认异常/);
  assert.match(panel, /INVESTIGATION_HEALTH\[branch\.health\] \|\| '检测状态待核对'/);
  assert.match(parent, /<EntryInvestigationPanel key=\{record\.id\}/);
  assert.match(parent, /onBusyChange=\{setInvestigationBusy\}/);
  assert.match(parent, /busyRef\.current \|\| investigationBusy/);
  assert.match(css, /@media\(max-width:575\.98px\)/);
  assert.match(css, /overflow-wrap:anywhere/);
  assert.match(panel, /<Col xs=\{24\} sm=\{12\}/);
});
