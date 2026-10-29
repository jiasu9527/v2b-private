import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { test } from 'node:test';
import ts from 'typescript';
const source = await readFile(new URL('../src/pages/aiOpsKnowledgeModel.ts', import.meta.url), 'utf8');
const { outputText } = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2020 } });
const { aiKnowledgeCanRead, aiKnowledgeDetail, aiKnowledgeList, aiKnowledgeSavePayload, aiKnowledgeValues } = await import(`data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`);
const entry = { id: 12, title: '入口排查', summary: '摘要', content: '# 检查\n\n    保留缩进\n', category: 'network', status: 'active', pinned: true, version: 3, source_kind: 'ai', source_note: '已验证', source_session_id: 4, source_turn_id: 8, created_by: 1, updated_by: 1, created_at: 1, updated_at: 2 };

test('staff can read published knowledge and only their own drafts; disabled material remains administrator-only', () => {
  assert.equal(aiKnowledgeCanRead(entry, 7, false), true);
  assert.equal(aiKnowledgeCanRead({ ...entry, status: 'draft', created_by: 7 }, 7, false), true);
  assert.equal(aiKnowledgeCanRead({ ...entry, status: 'draft' }, 7, false), false);
  assert.equal(aiKnowledgeCanRead({ ...entry, status: 'disabled', created_by: 7 }, 7, false), false);
  for (const status of ['active', 'draft', 'disabled']) assert.equal(aiKnowledgeCanRead({ ...entry, status }, 1, true), true);
});

test('staff create payload cannot publish, pin, edit an existing entry, or forge source metadata', () => {
  const payload = aiKnowledgeSavePayload({ ...entry, title: ' 新草稿 ', source_note: 'forged' }, null, false);
  assert.deepEqual(payload, { title: '新草稿', summary: entry.summary, content: entry.content, category: 'network', status: 'draft', pinned: false });
  assert.throws(() => aiKnowledgeSavePayload(aiKnowledgeValues(entry), entry, false), /不能修改已有资料/);
  for (const field of ['id', 'expected_version', 'source_kind', 'source_note', 'source_session_id', 'created_by']) assert.equal(Object.hasOwn(payload, field), false);
});

test('administrator edits preserve exact optimistic-lock version and source provenance is never submitted by the form', () => {
  const payload = aiKnowledgeSavePayload({ ...aiKnowledgeValues(entry), status: 'disabled' }, entry, true);
  assert.equal(payload.id, 12); assert.equal(payload.expected_version, 3); assert.equal(payload.status, 'disabled'); assert.equal(payload.pinned, true);
  assert.equal(payload.content, entry.content, 'Markdown indentation and trailing newline are retained');
  assert.equal(Object.hasOwn(payload, 'source_note'), false);
  assert.equal(Object.hasOwn(aiKnowledgeSavePayload(aiKnowledgeValues(entry), null, true), 'expected_version'), false);
  for (const version of [undefined, 0, -1, 1.5]) assert.throws(() => aiKnowledgeSavePayload(aiKnowledgeValues(entry), { ...entry, version }, true), /资料版本缺失/);
});

test('malformed list responses are errors, while real empty pages and list rows without content are accepted', () => {
  const { content, ...summary } = entry;
  const list = { data: [summary], total: 1, current: 1, page_size: 20 };
  assert.equal(aiKnowledgeList(list), list);
  assert.deepEqual(aiKnowledgeList({ data: [], total: 0, current: 1, page_size: 20 }).data, []);
  for (const invalid of [[], null, { data: [] }, { ...list, total: -1 }, { ...list, current: 0 }, { ...list, data: [{ ...summary, version: undefined }] }, { ...list, data: [{ ...summary, status: 'unknown' }] }]) assert.throws(() => aiKnowledgeList(invalid), /知识列表格式不正确/);
  assert.throws(() => aiKnowledgeDetail(summary), /知识详情格式不正确/);
  assert.equal(aiKnowledgeDetail(entry), entry);
});

test('new forms start as unpinned drafts and category/status and content limits are enforced', () => {
  assert.deepEqual(aiKnowledgeValues(), { title: '', summary: '', content: '', category: 'other', status: 'draft', pinned: false });
  const valid = aiKnowledgeValues(entry);
  for (const changes of [{ title: ' ' }, { title: 'a'.repeat(121) }, { summary: 'a'.repeat(501) }, { content: ' \n' }, { content: 'a'.repeat(12001) }, { category: 'bad' }, { status: 'bad' }]) assert.throws(() => aiKnowledgeSavePayload({ ...valid, ...changes }, null, true));
  assert.equal(aiKnowledgeSavePayload({ ...valid, title: '测'.repeat(120), summary: 'x'.repeat(500), content: 'x'.repeat(12000) }, null, true).content.length, 12000);
});
