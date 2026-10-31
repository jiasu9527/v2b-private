import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { createRequire } from 'node:module';
import { test } from 'node:test';
import React from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import ts from 'typescript';

const source = await readFile(new URL('../src/pages/AIOpsActionSummary.tsx', import.meta.url), 'utf8');
const { outputText } = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2020, jsx: ts.JsxEmit.ReactJSX } });
const identity = {};
const identitySource = await readFile(new URL('../src/pages/aiOpsServerIdentity.ts', import.meta.url), 'utf8');
new Function('exports', ts.transpileModule(identitySource, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2020 } }).outputText)(identity);
const require = createRequire(import.meta.url);
const module = { exports: {} };
new Function('require', 'exports', 'module', outputText)((name) => name === './aiOpsServerIdentity' ? identity : require(name), module.exports, module);
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

test('registration summaries preserve full long names and format IPv6 identities unambiguously', () => {
  const name = '美国洛杉矶-长名称落地服务器-'.repeat(5);
  const html = render({ ...action, payload: { ...action.payload, name, host: '2001:db8::20' } });
  assert.ok(html.includes(name));
  assert.ok(html.includes('ops@[2001:db8::20]:2222'));
  assert.ok(html.includes('class="ai-ops-wrap"'));
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

test('manual split distinguishes an accepted asynchronous operation from completed splitting', () => {
  const html = render({kind:'entry_manual_split',target_id:4,payload:{target:{target_id:4,name:'入口一',host:'192.0.2.40',port:54101,user_count:64,auto_split_enabled:false}},output:JSON.stringify({operation_id:8,operation_status:'pending',split_completed:false})});
  assert.equal(aiActionNames.entry_manual_split,'手动触发入口二分');
  assert.ok(html.includes('不依赖也不自动开启永久自动二分开关'));
  assert.ok(html.includes('任务已受理，二分尚未完成。'));
  assert.ok(html.includes('64 人'));
});

test('automatic and direct splits disclose persistent switch changes, targets and preserved UA conditions', () => {
  const automatic=render({kind:'entry_auto_split',target_id:4,payload:{target:{target_id:4,auto_split_enabled:false},enabled:true}});
  assert.ok(automatic.includes('关闭 → 开启'));
  const direct=render({kind:'entry_split',target_id:4,payload:{selection:{kind:'split_group',target_id:4,use_backup_ip_pool:true},direct:{name:'VIP入口',source_host:'192.0.2.40',user_count:100}},output:JSON.stringify({split_completed:true,groups:[{group_id:5,name:'子组 A',entry_host:'192.0.2.41'},{group_id:6,name:'子组 B',entry_host:'192.0.2.42'}]})});
  assert.ok(direct.includes('保留 UA 条件并建立两个子组'));
  assert.ok(direct.includes('从 IP 池分配两个入口'));
  assert.ok(direct.includes('192.0.2.41'));
  assert.ok(direct.includes('192.0.2.42'));
  assert.ok(direct.includes('已完成'));
});
