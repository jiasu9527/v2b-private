import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { test } from 'node:test';
import ts from 'typescript';

const source = await readFile(new URL('../src/pages/NewAPIIntegrationPage.tsx', import.meta.url), 'utf8');
const app = await readFile(new URL('../src/App.tsx', import.meta.url), 'utf8');
const styles = await readFile(new URL('../src/pages/NewAPIIntegrationPage.css', import.meta.url), 'utf8');
const ast = ts.createSourceFile('NewAPIIntegrationPage.tsx', source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
const selected = ast.statements.filter((node) => {
  if (ts.isImportDeclaration(node)) return false;
  if (ts.isFunctionDeclaration(node) && node.name?.text === 'NewAPIIntegrationPage') return false;
  if (ts.isVariableStatement(node) && node.getText(ast).includes('= Typography;')) return false;
  return true;
});
const isolated = selected.map((node) => node.getText(ast)).join('\n');
const { outputText } = ts.transpileModule(isolated, { compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2020 } });
const {
  newAPIAmountToFen, newAPIFenToYuan, newAPIConfigPayload, newAPIConfigFormValues,
  newAPITransferStatus, newAPIRetryAllowed, newAPITransferQuery,
} = await import(`data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`);

function config(overrides = {}) {
  return {
    revision: 3, enabled: false, api_url: 'https://ai.example.test', site_url: 'https://panel.example.test',
    client_id: 'forest', merchant_id: 'forest-wallet', admin_user_id: 1,
    min_amount_fen: 100, max_amount_fen: 100000, daily_limit_fen: 500000,
    has_client_secret: true, has_merchant_key: true, has_admin_token: true, ...overrides,
  };
}

test('yuan amounts convert exactly into integer fen without float rounding', () => {
  for (const [yuan, fen] of [['0.01', 1], ['0.29', 29], ['1', 100], ['1.1', 110], ['1000.00', 100000], [' 0001.23 ', 123], ['90071992547409.91', Number.MAX_SAFE_INTEGER]]) {
    assert.equal(newAPIAmountToFen(yuan), fen);
    assert.equal(newAPIAmountToFen(newAPIFenToYuan(fen)), fen);
  }
  for (const input of [undefined, null, '', '0', '0.00', '-1', '+1', '1e2', '1.001', '1,000', true, NaN, Infinity, '90071992547409.92']) assert.throws(() => newAPIAmountToFen(input));
  for (const input of [-1, NaN, Infinity, '100', 0.5, Number.MAX_SAFE_INTEGER + 1]) assert.throws(() => newAPIFenToYuan(input));
  assert.equal(newAPIFenToYuan(0), '0.00');
});

test('config hydration never populates stored secrets and leaves missing site URL unguessed', () => {
  const form = newAPIConfigFormValues(config({ site_url: '', client_secret: 'must-not-hydrate', merchant_key: 'must-not-hydrate', admin_token: 'must-not-hydrate' }));
  assert.equal(form.site_url, '');
  for (const key of ['client_secret', 'merchant_key', 'admin_token']) assert.equal(form[key], '');
  assert.equal(form.min_amount_yuan, '1.00');
  assert.equal(form.max_amount_yuan, '1000.00');
});

test('save whitelists config fields, uses revision and never sends blank credentials', () => {
  const stored = config();
  const form = { ...newAPIConfigFormValues(stored), unexpected: 'not-for-api', client_secret: '  new-random-secret  ', admin_token: '  ' };
  const payload = newAPIConfigPayload(form, stored);
  assert.equal(payload.expected_revision, 3);
  assert.equal(payload.min_amount_fen, 100);
  assert.equal(payload.max_amount_fen, 100000);
  assert.equal(payload.daily_limit_fen, 500000);
  assert.equal(payload.client_secret, 'new-random-secret');
  for (const key of ['merchant_key', 'admin_token', 'unexpected', 'min_amount_yuan', 'has_client_secret', 'revision']) assert.equal(Object.hasOwn(payload, key), false);
});

test('enabling requires all saved or newly provided credentials and concrete site addresses', () => {
  const stored = config();
  const form = { ...newAPIConfigFormValues(stored), enabled: true };
  assert.equal(newAPIConfigPayload(form, stored).enabled, true);
  for (const key of ['has_client_secret', 'has_merchant_key', 'has_admin_token']) assert.throws(() => newAPIConfigPayload(form, config({ [key]: false })));
  for (const key of ['api_url', 'site_url', 'client_id', 'merchant_id']) assert.throws(() => newAPIConfigPayload({ ...form, [key]: '' }, stored));
  assert.throws(() => newAPIConfigPayload({ ...form, admin_user_id: 0 }, stored));
  assert.throws(() => newAPIConfigPayload(form, config({ revision: undefined })));
  const pending = newAPIConfigPayload({ ...form, enabled: false, site_url: '' }, config({ has_admin_token: false }));
  assert.equal(pending.enabled, false);
  assert.equal(pending.site_url, '');
});

test('URL and limit validation rejects credentials, paths, insecure schemes and inconsistent limits', () => {
  const stored = config();
  const form = newAPIConfigFormValues(stored);
  for (const url of ['http://ai.example.test', 'javascript:alert(1)', 'https://user:password@ai.example.test', 'https://ai.example.test/path', 'https://ai.example.test?token=key', 'https://ai.example.test/#x']) {
    assert.throws(() => newAPIConfigPayload({ ...form, api_url: url }, stored));
  }
  assert.equal(newAPIConfigPayload({ ...form, api_url: 'https://ai.example.test/' }, stored).api_url, 'https://ai.example.test');
  assert.throws(() => newAPIConfigPayload({ ...form, min_amount_yuan: '1001' }, stored));
  assert.throws(() => newAPIConfigPayload({ ...form, daily_limit_yuan: '999' }, stored), /每人滚动 24 小时限额/);
});

test('setup explains native OIDC registration and fixed New API callback origin', () => {
  assert.match(source, /内置 OIDC/);
  assert.match(source, /不要使用“通用自定义 OAuth”/);
  assert.match(source, /ServerAddress/);
  assert.match(source, /固定为上方已保存的 New API 站点地址/);
  assert.match(source, /https:\/\/ai\.forest555\.com/);
  assert.match(source, /不要填 V2B 本站域名或使用其他别名/);
  assert.match(source, /RegisterEnabled=true/);
  assert.match(source, /PasswordRegisterEnabled=false/);
  assert.match(source, /首次通过 OIDC 自动建号/);
  assert.match(source, /New API OIDC 回调地址/);
});

test('wallet guidance describes rolling limits and settlement continuing after disable', () => {
  assert.match(source, /每人滚动 24 小时限额（元）/);
  assert.match(source, /包含待到账/);
  assert.match(source, /不在每天零点重置/);
  assert.doesNotMatch(source, /每人每日限额/);
  assert.match(source, /关闭后不再受理新扣款/);
  assert.match(source, /此前已扣款的待到账订单仍会按原单自动核对并补通知，不自动退款/);
  assert.match(source, /待到账期间可同步轮换同一 AI 站、同一商户的密钥/);
  assert.match(source, /不能切换 AI 站地址或商户/);
});

test('only confirmed success is labeled credited, and retries only apply to pending original transfers', () => {
  assert.equal(newAPITransferStatus('succeeded').label, '已到账');
  assert.equal(newAPIRetryAllowed('pending'), true);
  for (const status of ['succeeded', 'unknown', '', 'credited', 'attention', 'sending']) {
    assert.equal(newAPIRetryAllowed(status), false);
    if (status !== 'succeeded') assert.match(newAPITransferStatus(status).label, /未知状态/);
  }
  assert.match(source, /apiJsonPost\('\/new-api\/retry', \{ id: row\.id \}\)/);
  assert.doesNotMatch(source, /\/new-api\/(?:refund|credit|delete)/);
});

test('filters submit normalized user and trade identifiers, not silent invalid fallbacks', () => {
  assert.deepEqual(newAPITransferQuery({ user_id: ' 007 ', trade_no: ' remote-1 ', status: 'pending' }, 2, 50), { current: 2, page_size: 50, user_id: 7, trade_no: 'remote-1', status: 'pending' });
  assert.deepEqual(newAPITransferQuery({ user_id: '', trade_no: '' }, 1, 20), { current: 1, page_size: 20 });
  for (const user_id of ['0', '1.2', '-1', 'oops', '1e2', '9007199254740992']) assert.throws(() => newAPITransferQuery({ user_id, trade_no: '' }, 1, 20));
  assert.match(source, /loadTransfers\(appliedFilter, page, size\)/);
});

test('admin navigation is registered while staff retains its separate restricted routing', () => {
  assert.match(app, /key: '\/config\/new-api'.*label: 'New API 接入'/);
  assert.match(app, /if \(route === '\/config\/new-api'\) return <NewAPIIntegrationPage \/>/);
  const staffMenu = app.match(/const visibleMenu = supportOnly \? (.*?) : menu;/)?.[1];
  assert.ok(staffMenu);
  assert.doesNotMatch(staffMenu, /new-api/);
  assert.match(app, /supportOnly \? staffAIOpsPage \? <AIOpsPage/);
  assert.match(source, /付款账号关联/);
  assert.match(source, /不是全部 SSO 登录账号/);
  assert.match(source, /crypto\.getRandomValues\(bytes\)/);
  assert.match(source, /label=\{label\} htmlFor=\{key\}/);
  assert.match(source, /import '\.\/NewAPIIntegrationPage\.css'/);
  assert.match(source, /className="legacy-page new-api-integration-page"/);
  assert.match(styles, /\.new-api-integration-page \.ant-alert-info\s*\{\s*background: #f5f7fa/);
  assert.match(styles, /\.new-api-integration-page \.ant-card-extra\s*\{\s*width: auto/);
  assert.doesNotMatch(source, /localStorage|sessionStorage/);
});
