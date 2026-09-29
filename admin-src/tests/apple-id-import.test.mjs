import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import ts from 'typescript';

const source = readFileSync(new URL('../src/pages/apple-id-import.ts', import.meta.url), 'utf8');
const compiled = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2020 } }).outputText;
const { parseAppleIDImport, appleIDPriceToCents } = await import(`data:text/javascript;base64,${Buffer.from(compiled).toString('base64')}`);

test('TAB import normalizes accounts while preserving password whitespace and separators', () => {
  assert.deepEqual(parseAppleIDImport(' User@icloud.com \t  secret \tvalue  \r\n\nsecond@icloud.com\t ', 'lines'), [
    { account: 'User@icloud.com', password: '  secret \tvalue  ' },
    { account: 'second@icloud.com', password: ' ' },
  ]);
});

test('JSON import retains escaped characters, whitespace and string values', () => {
  const input = [{ account: ' user@icloud.com ', password: ' \n\t secret  ' }];
  assert.deepEqual(parseAppleIDImport(JSON.stringify(input), 'json'), [{ account: 'user@icloud.com', password: ' \n\t secret  ' }]);
});

test('duplicate accounts are rejected without exposing account or password', () => {
  assert.throws(() => parseAppleIDImport('User@icloud.com\tTOP-SECRET\nuser@icloud.com\tOTHER-SECRET', 'lines'), (error) => {
    assert.match(error.message, /第 2 条账号重复/);
    assert.doesNotMatch(error.message, /icloud|SECRET/);
    return true;
  });
});

test('malformed JSON errors never include fragments of the secret input', () => {
  assert.throws(() => parseAppleIDImport('[{"password":"DO-NOT-EXPOSE"', 'json'), (error) => {
    assert.doesNotMatch(error.message, /DO-NOT-EXPOSE|password/);
    return true;
  });
});

test('invalid fields and missing delimiters are rejected', () => {
  for (const value of ['{"account":"user"}', '[{"account":"user","password":123}]', '[{"account":" ","password":"valid"}]', '[{"account":"user","password":""}]']) {
    assert.throws(() => parseAppleIDImport(value, 'json'));
  }
  assert.throws(() => parseAppleIDImport('user@icloud.com password', 'lines'), /TAB/);
  assert.throws(() => parseAppleIDImport('\n \n', 'lines'), /至少/);
});

test('at most 500 accounts can be imported in one batch', () => {
  const rows = Array.from({ length: 501 }, (_, i) => ({ account: `user${i}@icloud.com`, password: 'p' }));
  assert.equal(parseAppleIDImport(JSON.stringify(rows.slice(0, 500)), 'json').length, 500);
  assert.throws(() => parseAppleIDImport(JSON.stringify(rows), 'json'), /500/);
});

test('product prices convert decimal yuan to integer cents precisely', () => {
  assert.equal(appleIDPriceToCents('19.90'), 1990);
  assert.equal(appleIDPriceToCents(0.29), 29);
  assert.equal(appleIDPriceToCents('1.01'), 101);
  for (const value of ['', '0', '-1', '1.001', 'Infinity', '9007199254740992']) assert.throws(() => appleIDPriceToCents(value));
});
