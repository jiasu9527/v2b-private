import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import ts from 'typescript';

const source = readFileSync(new URL('../src/pages/entryUserEmails.ts', import.meta.url), 'utf8');
const compiled = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2020 } }).outputText;
const { hasConfiguredEmailCondition, entryUserEmailSnapshot, entryUserEmailsFilename } = await import(`data:text/javascript;base64,${Buffer.from(compiled).toString('base64')}`);
const condition = values => ({ field: 'email', operator: 'in', values });
const rule = conditions => ({ id: 7, name: 'VIP 指定组', mode: 'standard', conditions });

test('only standard email/in rules offer the mailbox action, including empty lists', () => {
  assert.equal(hasConfiguredEmailCondition(rule([condition([])])), true);
  assert.equal(hasConfiguredEmailCondition({ id: 7, conditions: [condition(['a@example.test'])] }), true);
  for (const item of [
    rule([]), rule([{ field: 'user_id', operator: 'in', values: [7] }]),
    rule([{ field: 'email', operator: 'eq', values: ['a@example.test'] }]),
    { ...rule([condition(['a@example.test'])]), mode: 'split' },
    { ...rule([condition(['a@example.test'])]), __row_kind: 'split_group' },
    { ...rule([condition(['a@example.test'])]), mode: 'future-mode' },
    rule('{broken'), rule({ field: 'email', operator: 'in' }), null,
  ]) assert.equal(hasConfiguredEmailCondition(item), false);
});

test('configured union is trimmed, lowercased, deduplicated and sorted without applying user eligibility or AND', () => {
  const policy = { ...rule([
    condition([' Z@EXAMPLE.TEST ', 'overdue@example.test', 'blocked@example.test', 'new-unregistered@example.test', 'staff-added@example.test', 'a@example.test']),
    { field: 'plan_id', operator: 'between', min: 100, max: 110 },
    condition(['z@example.test', 'B@example.test', '']),
    { field: 'ua', operator: 'contains_any', values: ['Clash'] },
  ]), enabled: 0 };
  const before = JSON.stringify(policy);
  const result = entryUserEmailSnapshot([policy], 7);
  assert.deepEqual(result.emails, ['a@example.test', 'b@example.test', 'blocked@example.test', 'new-unregistered@example.test', 'overdue@example.test', 'staff-added@example.test', 'z@example.test']);
  assert.equal(result.text, result.emails.join('\n'));
  assert.equal(result.conditionCount, 2);
  assert.equal(result.name, 'VIP 指定组');
  assert.equal(JSON.stringify(policy), before);
});

test('all configured emails are included beyond any user-list pagination limit', () => {
  const values = Array.from({ length: 351 }, (_, index) => `user-${String(index).padStart(3, '0')}@example.test`).reverse();
  const result = entryUserEmailSnapshot([rule([condition(values)])], 7);
  assert.equal(result.emails.length, 351);
  assert.equal(result.text.split('\n').length, 351);
  assert.equal(result.emails[0], 'user-000@example.test');
  assert.equal(result.emails.at(-1), 'user-350@example.test');
  assert.ok(!result.text.endsWith('\n'));
});

test('existing JSON condition encodings and numeric string IDs are compatible', () => {
  let encoded = [condition(['Upper@Example.Test'])];
  for (let depth = 0; depth <= 3; depth += 1) {
    assert.equal(hasConfiguredEmailCondition(rule(encoded)), true);
    assert.equal(entryUserEmailSnapshot([{ ...rule(encoded), id: '7', name: '  ' }], 7).text, 'upper@example.test');
    encoded = JSON.stringify(encoded);
  }
  assert.equal(entryUserEmailSnapshot([rule([condition([])])], 7).text, '');
  assert.equal(entryUserEmailSnapshot([rule([condition(['', '  '])])], 7).emails.length, 0);
});

test('fresh response disappearance, type changes and missing email conditions fail instead of reusing old values', () => {
  assert.throws(() => entryUserEmailSnapshot([], 7), /已删除/);
  assert.throws(() => entryUserEmailSnapshot([rule([condition([])]), rule([condition([])])], 7), /重复/);
  assert.throws(() => entryUserEmailSnapshot([{ ...rule([condition([])]), mode: 'split' }], 7), /固定名单/);
  assert.throws(() => entryUserEmailSnapshot([rule([{ field: 'user_id', operator: 'in', values: [7] }])], 7), /不包含/);
  for (const invalid of [undefined, null, {}, { data: [] }, '[]']) assert.throws(() => entryUserEmailSnapshot(invalid, 7), /响应格式/);
  for (const id of [0, -1, NaN, Infinity, 1.5, Number.MAX_SAFE_INTEGER + 1]) assert.throws(() => entryUserEmailSnapshot([], id), /ID/);
});

test('malformed mailbox arrays and embedded line breaks never produce partial or injected exports', () => {
  for (const values of [null, undefined, 'a@example.test', {}, [7], [null], [{}], ['not-an-email'],
    ['good@example.test', 'a@example.test\nb@example.test'], ['a@example.test\r'], ['a@example.test\t'],
    ['a@example.test\0'], ['a@example.test\u0085b@example.test'], ['a@example.test\u2028b@example.test'], ['a@example.test\u2029b@example.test']]) {
    assert.throws(() => entryUserEmailSnapshot([rule([condition(values)])], 7), /格式异常|无效内容/);
  }
  for (const invalid of ['{bad', {}, [null], [['email']], [{ field: 'email', operator: 'in' }]]) {
    assert.throws(() => entryUserEmailSnapshot([rule(invalid)], 7), /格式异常/);
  }
});

test('single-line configured strings retain plus-addressing and Unicode without asking whether users exist', () => {
  const result = entryUserEmailSnapshot([rule([condition([' USER+VIP@EXAMPLE.TEST ', '用户@例子.test'])])], 7);
  assert.deepEqual(result.emails, ['user+vip@example.test', '用户@例子.test']);
  assert.equal(new TextDecoder().decode(new TextEncoder().encode(result.text)), result.text);
});

test('download filenames use safe bounded rule labels and never expose mailbox contents', () => {
  assert.equal(entryUserEmailsFilename(7, 'VIP 指定组'), 'entry-7-VIP-指定组-emails.txt');
  const bad = entryUserEmailsFilename(7, '../../CON\\恶意\r\n:<>|?*"\u202e/规则. ');
  assert.match(bad, /^entry-7-.*-emails\.txt$/);
  assert.doesNotMatch(bad, /[\x00-\x1f\x7f-\x9f/\\:*?"<>|\u202e]/);
  assert.equal(entryUserEmailsFilename(7, ''), 'entry-7-rule-emails.txt');
  assert.equal(entryUserEmailsFilename(7, '...'), 'entry-7-rule-emails.txt');
  assert.equal(Array.from(entryUserEmailsFilename(7, '长'.repeat(300))).length, 79);
  for (const id of [0, -1, NaN, 1.2]) assert.throws(() => entryUserEmailsFilename(id, 'test'), /ID/);
});
