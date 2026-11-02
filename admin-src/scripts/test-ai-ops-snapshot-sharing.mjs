import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { test } from 'node:test';
import ts from 'typescript';

const source = await readFile(new URL('../src/pages/aiOpsModel.ts', import.meta.url), 'utf8');
const { outputText } = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2020 } });
const { aiMergeSnapshot, aiMergeSessionAccess, aiMergeSessionList, aiApplyDraft } = await import(`data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`);
const copy = (value) => JSON.parse(JSON.stringify(value));
function fixture() {
  return {
    session: { id: 12, title: 'fixture', model: 'fixture-model', status: 'running', created_by: 7, created_at: 100, updated_at: 200, access_mode: 'standard', access_version: 3, full_access_by: 0 },
    messages: [
      { id: 1, session_id: 12, turn_id: 4, role: 'user', content: 'check service', created_at: 100 },
      { id: 2, session_id: 12, turn_id: 4, role: 'assistant', content: '', created_at: 101, tool_calls: [{ id: 'call-4', function: { name: 'panel_query', arguments: '{"operation":"nodes.list"}' } }] },
      { id: 3, session_id: 12, turn_id: 4, role: 'tool', content: '{"status":"healthy"}', tool_call_id: 'call-4', created_at: 102 },
    ],
    turns: [
      { id: 4, status: 'completed', error: '', created_at: 100, completed_at: 110 },
      { id: 5, status: 'running', error: '', created_at: 200, completed_at: 0, draft_round: 2, draft_content: 'checking', draft_version: 7 },
    ],
    actions: [
      { id: 8, session_id: 12, status: 'succeeded', kind: 'panel_operation', output: 'healthy', updated_at: 101, payload: { parameters: { ids: [1, 2], option: null }, name: 'nodes.list' } },
      { id: 9, session_id: 12, status: 'running', kind: 'ssh_command', output: 'first line', updated_at: 200, payload: { command: 'fixture read-only command' } },
    ],
    messages_truncated: false,
  };
}
function freeze(value) {
  if (value && typeof value === 'object') { Object.values(value).forEach(freeze); Object.freeze(value); }
  return value;
}

test('unchanged poll preserves the whole snapshot and every nested record without serialization', () => {
  const previous = freeze(fixture()), incoming = freeze(copy(previous));
  const stringify = JSON.stringify;
  try {
    JSON.stringify = () => { throw new Error('snapshot merging must not serialize the transcript'); };
    assert.equal(aiMergeSnapshot(previous, incoming), previous);
    assert.equal(aiMergeSessionAccess(previous.session, incoming.session), previous.session);
  } finally { JSON.stringify = stringify; }
  assert.equal(aiMergeSnapshot(previous, previous), previous);
});

test('session list polls preserve references for unchanged or absent records and replace only changed targets', () => {
  const session = fixture().session;
  const items = freeze([{ ...session, id: 10 }, session, { ...session, id: 14 }]);
  assert.equal(aiMergeSessionList(items, copy(session)), items);
  assert.equal(aiMergeSessionList(items, { ...session, id: 99 }), items);
  const empty = freeze([]);
  assert.equal(aiMergeSessionList(empty, session), empty);
  for (const change of [{ title: 'renamed' }, { status: 'completed' }]) {
    const incoming = freeze({ ...session, ...change });
    const next = aiMergeSessionList(items, incoming);
    assert.notEqual(next, items);
    assert.equal(next[0], items[0]);
    assert.equal(next[1], incoming);
    assert.equal(next[2], items[2]);
    assert.deepEqual(next.map((item) => item.id), [10, 12, 14]);
  }
  assert.equal(items[1].title, 'fixture');
  assert.equal(items[1].status, 'running');
});

test('session list retains revocation protection while permitting unrelated fields and fresh access changes', () => {
  const session = fixture().session;
  const items = freeze([session]);
  const staleFullAccess = freeze({ ...session, access_mode: 'full', access_version: 2, full_access_by: 7 });
  assert.equal(aiMergeSessionList(items, staleFullAccess), items);
  const renamed = aiMergeSessionList(items, freeze({ ...staleFullAccess, title: 'renamed' }));
  assert.notEqual(renamed, items);
  assert.notEqual(renamed[0], session);
  assert.deepEqual(renamed[0], { ...session, title: 'renamed' });
  const freshFullAccess = freeze({ ...staleFullAccess, access_version: 4 });
  const updated = aiMergeSessionList(items, freshFullAccess);
  assert.notEqual(updated, items);
  assert.equal(updated[0], freshFullAccess);
  assert.equal(items[0].access_mode, 'standard');
  assert.equal(items[0].access_version, 3);
});

test('changing one message keeps unrelated messages, actions, turns and session references', () => {
  const previous = freeze(fixture()), incoming = copy(previous);
  incoming.messages[2].content = '{"status":"changed"}';
  const next = aiMergeSnapshot(previous, incoming);
  assert.notEqual(next, previous);
  assert.notEqual(next.messages, previous.messages);
  assert.equal(next.messages[0], previous.messages[0]);
  assert.equal(next.messages[1], previous.messages[1]);
  assert.equal(next.messages[2], incoming.messages[2]);
  for (const key of ['session', 'actions', 'turns']) assert.equal(next[key], previous[key]);
  assert.equal(previous.messages[2].content, '{"status":"healthy"}');
});

test('same IDs and timestamps never hide changed fields, nested tool definitions or action payloads', () => {
  for (const update of [
    (value) => { value.messages[0].created_at += 1; },
    (value) => { value.messages[0].role = 'assistant'; },
    (value) => { value.messages[1].tool_calls[0].function.arguments = '{"operation":"groups.list"}'; },
    (value) => { value.messages[1].tool_calls[0].function.name = 'groups_list'; },
    (value) => { value.messages[1].tool_calls[0].id = 'other-call'; },
    (value) => { value.messages[2].tool_call_id = 'different'; },
    (value) => { value.actions[1].output = 'later line in the same second'; },
    (value) => { value.actions[1].status = 'succeeded'; },
    (value) => { value.actions[0].payload.parameters.ids[1] = 3; },
    (value) => { value.actions[0].payload.parameters.option = false; },
    (value) => { value.actions[0].payload.extra = 'new property'; },
    (value) => { delete value.actions[0].payload.name; },
    (value) => { delete value.messages[2].tool_call_id; },
    (value) => { value.turns[0].completed_at += 1; },
    (value) => { value.turns[0].error = 'changed'; },
    (value) => { value.session.title = 'renamed'; },
    (value) => { value.messages_truncated = true; },
    (value) => { delete value.messages_truncated; },
    (value) => { value.future_metadata = { has_more: true }; },
  ]) {
    const previous = freeze(fixture()), incoming = copy(previous);
    update(incoming);
    const next = aiMergeSnapshot(previous, incoming);
    assert.notEqual(next, previous);
    assert.deepEqual(next, incoming);
  }
});

test('object property order is irrelevant but deleted, added and reordered records remain visible', () => {
  const previous = freeze(fixture());
  const reorderedKeys = copy(previous);
  reorderedKeys.actions[0].payload = { name: 'nodes.list', parameters: { option: null, ids: [1, 2] } };
  assert.equal(aiMergeSnapshot(previous, reorderedKeys), previous);
  for (const field of ['messages', 'actions', 'turns']) {
    const incoming = copy(previous);
    incoming[field].reverse();
    let next = aiMergeSnapshot(previous, incoming);
    assert.notEqual(next[field], previous[field]);
    assert.deepEqual(next[field].map((item) => item.id), incoming[field].map((item) => item.id));
    next[field].forEach((item) => assert.equal(item, previous[field].find((old) => old.id === item.id)));
    incoming[field].pop();
    next = aiMergeSnapshot(previous, incoming);
    assert.equal(next[field].length, previous[field].length - 1);
    incoming[field] = [];
    assert.deepEqual(aiMergeSnapshot(previous, incoming)[field], []);
  }
  const appended = copy(previous);
  appended.messages.push({ ...appended.messages[0], id: 99, content: 'new message' });
  assert.equal(aiMergeSnapshot(previous, appended).messages[3], appended.messages[3]);
});

test('different sessions never borrow records from another session with coincident IDs', () => {
  const previous = freeze(fixture()), incoming = copy(previous);
  incoming.session.id = 88;
  assert.equal(aiMergeSnapshot(previous, incoming), incoming);
  assert.equal(aiMergeSnapshot(null, incoming), incoming);
});

test('duplicate draft events are no-ops; actual draft updates only change the target turn', () => {
  const previous = freeze(fixture());
  const event = { session_id: 12, turn_id: 5, round: 2, content: 'checking', version: 7 };
  assert.equal(aiApplyDraft(previous, event), previous);
  assert.equal(aiApplyDraft(previous, { ...event, version: 6, content: 'old' }), previous);
  const next = aiApplyDraft(previous, { ...event, version: 8, content: 'checked' });
  for (const key of ['session', 'messages', 'actions']) assert.equal(next[key], previous[key]);
  assert.equal(next.turns[0], previous.turns[0]);
  assert.notEqual(next.turns[1], previous.turns[1]);
  assert.equal(next.turns[1].draft_content, 'checked');
  assert.equal(aiApplyDraft(next, { ...event, version: 8, content: 'checked' }), next);
  const finished = aiApplyDraft(next, { ...event, version: 9, status: 'completed', content: '' });
  assert.equal(finished.turns[1].status, 'completed');
  assert.equal(finished.turns[1].draft_content, '');
  assert.equal(aiApplyDraft(finished, event), finished);
});

test('snapshot sharing preserves revoked access, newer cleared drafts and terminal snapshots', () => {
  const previous = fixture();
  previous.session.access_mode = 'standard'; previous.session.access_version = 4;
  previous.turns[1].draft_content = ''; previous.turns[1].draft_version = 8;
  const incoming = fixture();
  incoming.session.access_mode = 'full'; incoming.session.full_access_by = 7;
  assert.equal(aiMergeSnapshot(previous, incoming), previous);
  const fresh = copy(previous);
  fresh.turns[1].status = 'completed'; fresh.turns[1].draft_version = 9;
  const next = aiMergeSnapshot(previous, fresh);
  assert.equal(next.turns[1], fresh.turns[1]);
  assert.equal(next.messages, previous.messages);
  assert.equal(next.actions, previous.actions);
});

test('matching many turn IDs uses a linear lookup instead of a nested snapshot find', () => {
  const previous = fixture(), incoming = copy(previous);
  let idReads = 0;
  const count = 1000;
  const turns = () => Array.from({ length: count }, (_, id) => ({ get id() { idReads++; return id + 1; }, status: 'running', draft_content: '', draft_round: 1, draft_version: 5 }));
  previous.turns = turns(); incoming.turns = turns();
  assert.equal(aiMergeSnapshot(previous, incoming), previous);
  assert.ok(idReads < count * 20, `unexpectedly many ID reads: ${idReads}`);
});

test('malformed preserved draft metadata cannot override a valid incoming snapshot', () => {
  for (const badRound of [-1, 1.5, '2']) {
    const previous = fixture(), incoming = copy(previous);
    previous.turns[1].draft_round = badRound;
    previous.turns[1].draft_version += 1;
    previous.turns[1].draft_content = 'invalid cached draft';
    assert.deepEqual(aiMergeSnapshot(previous, incoming), incoming);
  }
});
