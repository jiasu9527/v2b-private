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
const compile = (source) => ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2020, jsx: ts.JsxEmit.React, esModuleInterop: true },
}).outputText;
runInContext(compile(component + '\nexport { webLoginRequest, runFailure, responseResult };'), createContext(exposed));
const { aiOpsLoginURL, webLoginRequest, runFailure, responseResult, runAIOpsWebLogin } = exposed.exports;
const parser = await readFile(new URL('../src/pages/aiOpsWebLoginText.ts', import.meta.url), 'utf8');
const parsed = {};
new Function('exports', compile(parser))(parsed);
const { parseAIWebLoginText } = parsed;
const example = '帮我登录 https://panel.example.com，账号 demo，密码 example-password，看看服务状态。';

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

test('chat accepts a sentence, a newline paste, or reordered fields with a fixed target', () => {
  assert.deepEqual(parseAIWebLoginText(example), {
    kind: 'login', url: 'https://panel.example.com', username: 'demo', password: 'example-password', task: '看看服务状态。',
  });
  assert.deepEqual(parseAIWebLoginText('https://panel.example.com/login\r\ndemo\r\nfixture!$\r\n查看证书到期时间'), {
    kind: 'login', url: 'https://panel.example.com/login', username: 'demo', password: 'fixture!$', task: '查看证书到期时间',
  });
  assert.deepEqual(parseAIWebLoginText('查询内容：证书何时到期\n密码：“ keep，has spaces! ”\n用户名：demo\n网站地址：https://panel.example.com\n目标页面：https://panel.example.com/cert'), {
    kind: 'login', url: 'https://panel.example.com', target_url: 'https://panel.example.com/cert',
    username: 'demo', password: ' keep，has spaces! ', task: '证书何时到期',
  });
});

test('missing, ambiguous, or mixed login details do not become ordinary AI chat', () => {
  for (const input of [
    'https://panel.example.com\ndemo\nfixture',
    'https://panel.example.com\ndemo\nfixture\n系统状态',
    '帮我登录 https://panel.example.com，账号 demo，看看服务状态。',
    '帮我登录 https://panel.example.com，账号 demo，密码 fixture，密码 fixture2，看看服务状态。',
    example + '\nhttps://other.example.com',
    '查 https://demo:fixture@panel.example.com',
    example + '内容'.repeat(7_000),
  ]) {
    const result = parseAIWebLoginText(input);
    assert.equal(result.kind, 'invalid');
    assert.doesNotMatch(result.message, /fixture|example-password|panel\.example/);
  }
  for (const input of [
    '看一下 https://docs.example.com 的登录说明',
    '查一下查询失败的订单',
    '先 SSH 登录 192.0.2.1\n账号 demo\n密码 fixture\n安装地址：https://panel.example.com',
  ]) assert.equal(parseAIWebLoginText(input).kind, 'chat');
});

test('conversation send uses the dedicated request without retry or fallback', async () => {
  const ast = ts.createSourceFile('AIOpsPage.tsx', page, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  const fn = ast.statements.find((node) => ts.isFunctionDeclaration(node) && node.name?.text === 'AIOpsPage');
  const send = fn.body.statements.flatMap((node) => ts.isVariableStatement(node) ? [...node.declarationList.declarations] : []).find((node) => node.name.getText(ast) === 'send');
  const program = compile(`exports.send = ${send.initializer.getText(ast)};`);
  for (const scenario of ['ok', 'bad-response', 'network-error', 'cancelled', 'missing', 'cross-site', 'insecure', 'mini', 'staff']) {
    const calls = [], cards = [], retry = [];
    let sending = false;
    const content = scenario === 'missing' ? example.replace('，密码 example-password', '') :
      scenario === 'cross-site' ? example + '\n目标页面：https://different.example.com/' : example;
    const draft = { current: content };
    const bindings = {
      draft, parseAIWebLoginText, aiOpsLoginURL, runAIOpsWebLogin, AbortController,
      window: { location: { protocol: scenario === 'insecure' ? 'http:' : 'https:' } },
      aiMessageTooLarge: () => false, isAdmin: scenario !== 'staff', miniApp: scenario === 'mini', model: 'fixture-model',
      followOutput: { current: false }, alive: { current: true }, webLoginRun: { current: null },
      setWebLoginChat: (value) => cards.push(value),
      setDraft: (value) => { draft.current = value; },
      setSendRetry: (value) => retry.push(value),
      begin(name) { assert.equal(name, 'web-login'); assert.equal(sending, false); return (sending = true); },
      finish() { sending = false; },
      aiModelReady: () => assert.fail('ordinary chat is not a fallback'),
      apiJsonPost: () => assert.fail('ordinary chat must never receive website credentials'),
    };
    exposed.fetch = async (url, request) => {
      calls.push({ url, request });
      if (scenario === 'network-error') throw new Error('example-password');
      if (scenario === 'cancelled') bindings.webLoginRun.current.abort();
      if (scenario === 'bad-response') return new Response(JSON.stringify({ message: 'example-password' }), { status: 502 });
      return new Response(JSON.stringify({ data: { origin: 'https://panel.example.com', page_title: 'Panel', summary: '服务正常', model: 'fixture-model' } }));
    };
    const compiled = {};
    new Function('exports', ...Object.keys(bindings), program)(compiled, ...Object.values(bindings));
    await compiled.send();
    const dispatched = ['ok', 'bad-response', 'network-error', 'cancelled'].includes(scenario);
    assert.equal(sending, false);
    assert.equal(calls.length, dispatched ? 1 : 0);
    assert.deepEqual(retry, dispatched ? [null] : []);
    assert.equal(draft.current, dispatched ? '' : content);
    if (dispatched) {
      assert.equal(calls[0].url, '/api/v1/fixture/ai-ops/web-login/run');
      const body = JSON.parse(calls[0].request.body);
      assert.equal(body.password, 'example-password');
      assert.equal(body.task, '看看服务状态。');
      assert.doesNotMatch(body.task, /example-password|demo/);
    }
    assert.equal(cards.at(-1)?.state, scenario === 'ok' ? 'done' : scenario === 'cancelled' ? 'loading' : 'error');
    assert.doesNotMatch(JSON.stringify(cards), /example-password/);
  }
});
