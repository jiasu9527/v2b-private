import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { createRequire } from 'node:module';
import { test } from 'node:test';
import React from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import ts from 'typescript';

const source = await readFile(new URL('../src/pages/AIOpsActionSummary.tsx', import.meta.url), 'utf8');
const { outputText } = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2020, jsx: ts.JsxEmit.ReactJSX } });
const module = { exports: {} };
new Function('require', 'exports', 'module', outputText)(createRequire(import.meta.url), module.exports, module);
const { default: Summary, aiActionNames } = module.exports;
const action = {
  kind: 'server_register', status: 'pending', payload: {
    intake_id: 9, name: '测试落地机', host: '192.0.2.20', port: 2222, username: 'ops',
    host_key_sha256: 'SHA256:fixture', trust_source: 'first_use', has_password: true, has_private_key: true,
    password: 'must-not-appear-password', private_key: 'must-not-appear-private-key', credentials_cipher: 'must-not-appear-cipher',
  },
};
const render = (value) => renderToStaticMarkup(React.createElement(Summary, { action: value }));

test('SSH registration renders safe identity and staged credential metadata without any secret fields', () => {
  assert.equal(aiActionNames.server_register, '登记 SSH 服务器');
  const html = render(action);
  for (const text of ['测试落地机', 'ops@192.0.2.20:2222', 'SHA256:fixture', '密码已安全暂存', '私钥已安全暂存']) assert.ok(html.includes(text));
  assert.doesNotMatch(html, /must-not-appear|credentials_cipher|private_key|intake_id/);
});

test('first-use fingerprints do not claim independent verification and supplied fingerprints stay distinct', () => {
  assert.ok(render(action).includes('首次连接记录的指纹，未经独立验证；后续连接必须匹配。'));
  const supplied = render({ ...action, payload: { ...action.payload, trust_source: 'provided' } });
  assert.ok(supplied.includes('使用提供的固定指纹；后续连接必须匹配。'));
  assert.ok(!supplied.includes('首次连接记录'));
});

test('completed registrations report encrypted storage and absent credentials are not invented', () => {
  const saved = render({ ...action, status: 'succeeded' });
  assert.ok(saved.includes('密码已加密保存'));
  assert.ok(saved.includes('私钥已加密保存'));
  assert.ok(!saved.includes('已安全暂存'));
  const empty = render({ ...action, payload: { ...action.payload, has_password: false, has_private_key: false } });
  assert.ok(empty.includes('未提供'));
  assert.ok(!empty.includes('已安全暂存'));
});
