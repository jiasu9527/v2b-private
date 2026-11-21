import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { createContext, runInContext } from 'node:vm';
import { test } from 'node:test';
import ts from 'typescript';

const component = await readFile(new URL('../src/pages/AIOpsWebLogin.tsx', import.meta.url), 'utf8');
const page = await readFile(new URL('../src/pages/AIOpsPage.tsx', import.meta.url), 'utf8');
// Load the production URL guard and transport functions without mounting React.
const exposed = { exports: {}, URL, TextEncoder, AbortController, Response, require(name) {
  if (name === 'antd') return { Typography: {} };
  if (name === '../lib/api') return { aiOpsRequestURL: (path) => '/api/v1/fixture' + path, aiOpsRequestHeaders: () => ({ Authorization: 'fixture-admin' }) };
  return {};
} };
const output = ts.transpileModule(component + '\nexport { webLoginRequest, runFailure, responseResult };', {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2020, jsx: ts.JsxEmit.React, esModuleInterop: true },
}).outputText;
runInContext(output, createContext(exposed));
const { aiOpsLoginURL, webLoginRequest, runFailure, responseResult } = exposed.exports;

test('browser login validates TLS and refuses ambiguous or credential-bearing URLs', () => {
  assert.equal(aiOpsLoginURL(' https://bücher.example.com/login ').href, 'https://xn--bcher-kva.example.com/login');
  assert.equal(aiOpsLoginURL('https://panel.example.com').origin, 'https://panel.example.com');
  for (const raw of [
    'http://panel.example.com', '//panel.example.com', 'https://user:secret@panel.example.com',
    'https://@panel.example.com', 'https://panel.example.com/login?password=secret',
    'https://panel.example.com/?', 'https://panel.example.com/#', 'https://panel.example.com/#account',
    String.raw`https://ignored\@panel.example.com`, 'https://pa\nnel.example.test',
    'https://panel.example.com:8443/', 'https://127.0.0.1/', 'https://panel.internal/login',
    'https://panel.example.com/./login', 'https://panel.example.com/%2e/Login', 'https://panel.example.com/login%2fnext',
    'https://panel.example.com/' + 'x'.repeat(2048),
  ]) assert.equal(aiOpsLoginURL(raw), null, raw);
});

test('credentials are sent only in the single POST to Forest with no cookies, query, or redirect', async () => {
  const receipts = [];
  exposed.fetch = async (...args) => { receipts.push(args); return new Response('{}'); };
  const controller = new AbortController();
  const fields = { url: 'https://panel.example.com', username: 'sample-user', password: 'fixture-password', task: '判定服务状态' };
  await webLoginRequest('/run', controller.signal, fields);
  assert.equal(receipts.length, 1);
  const [[url, init]] = receipts;
  assert.equal(url, '/api/v1/fixture/ai-ops/web-login/run');
  assert.equal(init.headers.Authorization, 'fixture-admin');
  assert.equal(init.headers['Content-Type'], 'application/json');
  assert.deepEqual(JSON.parse(init.body), fields);
  assert.equal(init.credentials, 'omit');
  assert.equal(init.redirect, 'error');
  assert.equal(init.cache, 'no-store');
  assert.equal(init.referrerPolicy, 'no-referrer');
  assert.equal(init.signal, controller.signal);
  await webLoginRequest('/status', controller.signal);
  assert.equal(receipts[1][1].body, undefined);
});

test('upstream errors are not mirrored and stale/wrong-origin results are rejected', async () => {
  const error = await runFailure(new Response(JSON.stringify({ code: 'not-a-known-code', message: 'leaked-password' }), { status: 502 }));
  assert.doesNotMatch(error, /leaked-password/);
  const verification = await runFailure(new Response(JSON.stringify({ code: 'verification_required', message: 'upstream-credential' }), { status: 422 }));
  assert.match(verification, /验证码/);
  assert.doesNotMatch(verification, /upstream-credential/);
  const output = { origin: 'https://panel.example.com', summary: '服务正常', page_title: '<img>', model: 'fixture-model' };
  assert.equal(responseResult({ data: output }, 'https://someone-else.example.test'), null);
  assert.equal(responseResult({ data: { ...output, summary: 'x'.repeat(24_001) } }, output.origin), null);
  assert.equal(responseResult({ data: output }, output.origin).page_title, '<img>');
});

test('web login is an isolated administrator tab and secrets are cleared before awaiting the result', () => {
  assert.match(page, /!miniApp && isAdmin \? \[\{ key: 'web-login'/);
  assert.match(page, /tab === 'web-login' \? <AIOpsWebLogin key=\{actorID\}/);
  assert.match(component, /setFieldValue\('password', ''\);\s*const response = await pending/);
  assert.match(component, /addEventListener\('pagehide', handlePageHide\)/);
  assert.match(component, /\{result\.summary\}/);
  assert.doesNotMatch(component, /localStorage|sessionStorage|document\.cookie|console\.|dangerouslySetInnerHTML|\/chat\/send/);
});
