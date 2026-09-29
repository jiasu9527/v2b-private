import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import ts from 'typescript';

const source = readFileSync(new URL('../src/pages/apple-id-import.ts', import.meta.url), 'utf8');
const compiled = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2020 } }).outputText;
const { parseAppleIDImport, appleIDPriceToCents } = await import(`data:text/javascript;base64,${Buffer.from(compiled).toString('base64')}`);

test('每行保留完整账号资料、分隔符、密保和首尾空格', () => {
  assert.deepEqual(parseAppleIDImport('  User@icloud.com----secret----密保答案  \nsecond@example.com | pass | answer'), [
    { credential: '  User@icloud.com----secret----密保答案  ' },
    { credential: 'second@example.com | pass | answer' },
  ]);
});

test('空行忽略，重复完整资料拒绝且错误不暴露内容', () => {
  assert.throws(() => parseAppleIDImport('secret-account----secret-password\n\nSECRET-ACCOUNT----SECRET-PASSWORD'), (error) => {
    assert.match(error.message, /第 2 条账号资料重复/);
    assert.doesNotMatch(error.message, /secret|account|password/i);
    return true;
  });
});

test('空输入和超过 500 条拒绝', () => {
  assert.throws(() => parseAppleIDImport('\n  \n'), /至少/);
  const rows = Array.from({ length: 501 }, (_, i) => `user-${i} | password | answer`);
  assert.throws(() => parseAppleIDImport(rows.join('\n')), /500/);
});

test('账号资料内容不要求固定格式', () => {
  assert.deepEqual(parseAppleIDImport('only-one-line\naccount password security-question answer'), [
    { credential: 'only-one-line' },
    { credential: 'account password security-question answer' },
  ]);
});

test('product prices convert decimal yuan to integer cents precisely', () => {
  assert.equal(appleIDPriceToCents('19.90'), 1990);
  assert.equal(appleIDPriceToCents(0.29), 29);
  assert.equal(appleIDPriceToCents('1.01'), 101);
  for (const value of ['', '0', '-1', '1.001', 'Infinity', '9007199254740992']) assert.throws(() => appleIDPriceToCents(value));
});
