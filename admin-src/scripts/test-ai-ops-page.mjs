import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { test } from 'node:test';
import ts from 'typescript';
const source = await readFile(new URL('../src/pages/aiOpsModel.ts', import.meta.url), 'utf8');
const page = await readFile(new URL('../src/pages/AIOpsPage.tsx', import.meta.url), 'utf8');
const app = await readFile(new URL('../src/App.tsx', import.meta.url), 'utf8');
const { outputText } = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2020 } });
const { AI_DEFAULT_MODELS, aiActionStatus, aiCanApprove, aiSessionBusy, aiLatestTurn, aiParseModels, aiFingerprintMatches, aiRequestID } = await import(`data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`);
const now = 1900000000000;
const action = { status: 'pending', expires_at: now / 1000 + 60 };

test('approval requires a pending unexpired action and enabled AI; result states stay immutable', () => {
  assert.equal(aiCanApprove(action, true, now), true);
  assert.equal(aiCanApprove(action, false, now), false);
  assert.equal(aiCanApprove(action, true, now + 60000), false);
  assert.equal(aiActionStatus(action, now + 60000), 'expired');
  for (const status of ['running', 'succeeded', 'failed', 'unknown', 'rejected', 'expired']) {
    assert.equal(aiCanApprove({ ...action, status }, true, now), false);
    assert.equal(aiActionStatus({ ...action, status }, now + 60000), status);
  }
});

test('sending stays blocked while any session turn or action is running', () => {
  assert.equal(aiSessionBusy(null), false);
  const detail = { session: { status: 'idle' }, turns: [], actions: [] };
  assert.equal(aiSessionBusy(detail), false);
  assert.equal(aiSessionBusy({ ...detail, session: { status: 'running' } }), true);
  assert.equal(aiSessionBusy({ ...detail, turns: [{ status: 'running' }] }), true);
  assert.equal(aiSessionBusy({ ...detail, actions: [{ status: 'running' }] }), true);
  assert.equal(aiSessionBusy({ ...detail, actions: [{ status: 'pending' }] }), false);
});

test('four requested IDs are preserved and model list editing trims and deduplicates only', () => {
  assert.deepEqual(AI_DEFAULT_MODELS, ['gpt-6.1-sol', 'claude-fable-5-1', 'deepseek-v4.1-flash', 'glm-5.3']);
  assert.deepEqual(aiParseModels(' gpt-6.1-sol \n\nCustom-Model\r\nCustom-Model\ncustom-model'), ['gpt-6.1-sol', 'Custom-Model', 'custom-model']);
});

test('observed fingerprint is scoped to exact current host and port, never auto-trusted', () => {
  const observed = { host: 'node.example.test', port: 2222, fingerprint: 'SHA256:observed' };
  assert.equal(aiFingerprintMatches(observed, ' node.example.test ', 2222), true);
  assert.equal(aiFingerprintMatches(observed, 'another.example.test', 2222), false);
  assert.equal(aiFingerprintMatches(observed, 'node.example.test', 22), false);
  assert.equal(aiFingerprintMatches(null, 'node.example.test', 2222), false);
  const probe = page.slice(page.indexOf('const probeFingerprint'), page.indexOf('const saveServer'));
  assert.doesNotMatch(probe, /setFieldsValue|setFieldValue/);
  assert.match(probe, /aiFingerprintMatches/);
  assert.match(page, /获取主机指纹（仅观察）/);
});

test('page is administrator-only and introduces no MCP menu or browser persistence', () => {
  assert.match(app, /key: '\/config\/ai-ops'.*label: 'AI 运维'/);
  assert.match(app, /route === '\/config\/ai-ops'\) return <AIOpsPage/);
  assert.match(app, /const visibleMenu = supportOnly \? \[.*员工工作台.*\] : menu/);
  assert.match(app, /supportOnly \? <SupportWorkbenchPage[\s\S]*: <Page key=\{path\}/);
  assert.doesNotMatch(page, /localStorage|sessionStorage|console\.|dangerouslySetInnerHTML|eval\(|MCP/);
  assert.doesNotMatch(app, /MCPPage|config\/mcp/);
});

test('SSH secrets stay in settings-only requests and clear on save/close', () => {
  const send = page.slice(page.indexOf('const send ='), page.indexOf('const saveConfig'));
  assert.doesNotMatch(send, /password|private_key|api_key:/);
  assert.match(page, /configForm\.setFieldsValue\(\{ api_key: '', clear_api_key: false \}\)/);
  assert.match(page, /setServerOpen\(false\); setFingerprint\(null\); serverForm\.resetFields\(\)/);
  assert.match(page, /setServerOpen\(false\); serverForm\.resetFields\(\)/);
  assert.match(page, /api_key: values\.api_key \|\| ''/);
});

test('every server command requires manual one-action approval with full unrendered command and output warning', () => {
  assert.match(page, /<pre className="ai-ops-pre ai-ops-command">\{command\}<\/pre>/);
  assert.match(page, /所有服务器命令（包括状态诊断）和部署操作都必须逐项批准/);
  assert.match(page, /输出会进入对话并发往中转模型/);
  assert.match(page, /setReview\(\{ action, decision: 'approve' \}\)/);
  assert.match(page, /!confirmedOutput \|\| !aiCanApprove/);
  assert.match(page, /apiJsonPost\(`\/ai-ops\/action\/\$\{review\.decision\}`/);
  assert.match(page, /session_id: review\.action\.session_id, id: review\.action\.id/);
});

test('destructive actions require explicit confirmation and reset distinguishes all three data categories', () => {
  assert.match(page, /resetText !== 'RESET_AI_DATA'/);
  assert.match(page, /apiJsonPost\('\/ai-ops\/reset', \{ confirm: true, confirm_text: 'RESET_AI_DATA' \}\)/);
  assert.match(page, /apiJsonPost\('\/ai-ops\/session\/clear', \{ confirm: true \}\)/);
  assert.match(page, /包括中转配置及 API Key、全部服务器 SSH 凭据、所有管理员的会话与操作记录/);
  assert.match(page, /已在远程启动的命令不保证停止或回滚/);
});

test('async send uses stable retry request IDs and does not replace a subsequently selected conversation', () => {
  assert.notEqual(aiRequestID(), aiRequestID());
  assert.match(page, /sendRetry\.sessionID === originalID && sendRetry\.model === model && sendRetry\.content === content/);
  assert.match(page, /request_id: retry\.requestID/);
  assert.match(page, /selectionSequence\.current === selection/);
  assert.match(page, /requestSequence|detailSequence/);
  assert.match(page, /cache: 'no-store'/);
});

test('latest turn selection is stable for descending API order and shows the newest failure', () => {
  const old = { id: 1, status: 'completed', error: '' };
  const recent = { id: 3, status: 'failed', error: 'new failure' };
  assert.equal(aiLatestTurn([recent, old]), recent);
  assert.equal(aiLatestTurn([old, recent]), recent);
  assert.equal(aiLatestTurn([]), undefined);
  assert.match(page, /aiLatestTurn\(detail\?\.turns \|\| \[\]\)/);
});

test('action cards expose optional approver/rejector IDs without showing zero or undefined IDs', () => {
  for (const field of ['approved_by', 'rejected_by']) {
    assert.ok(source.includes(`${field}?: number`));
    assert.ok(page.includes(`(action.${field} ?? 0) > 0`));
    assert.ok(page.includes(`管理员 #{action.${field}}`));
  }
});

test('an unconfigured disabled provider can be saved and enabling it requires its Base URL', () => {
  const baseURLField = page.split('<Form.Item name="base_url"')[1].split('</Form.Item>')[0];
  assert.ok(baseURLField.includes("dependencies={['enabled']}"));
  assert.ok(baseURLField.includes("required: !!getFieldValue('enabled')"));
  assert.ok(baseURLField.includes("whitespace: !!getFieldValue('enabled')"));
});
