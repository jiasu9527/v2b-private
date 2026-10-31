import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { createRequire } from 'node:module';
import { test } from 'node:test';
import React from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import ts from 'typescript';

const model = await readFile(new URL('../src/pages/aiOpsModel.ts', import.meta.url), 'utf8');
const component = await readFile(new URL('../src/pages/AIOpsTelegram.tsx', import.meta.url), 'utf8');
const page = await readFile(new URL('../src/pages/AIOpsPage.tsx', import.meta.url), 'utf8');
const { outputText } = ts.transpileModule(model, { compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2020 } });
const helpers = await import(`data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`);
const { aiTelegramActorID, aiTelegramBindings, aiTelegramConfig } = helpers;
const config = { enabled: true, default_model: 'fixture-model', default_access_mode: 'full', stream_updates: true, notify_completion: false };
const own = { actor_id: 7, email: 'own@example.test', telegram_id: '1234567890123', active: true, session_id: 20, bound_at: 1700000000 };
const other = { ...own, actor_id: 8, email: 'other@example.test', telegram_id: 888888, session_id: 21 };
const status = { config, bindings: [own, other], my_binding: own, models: ['fixture-model'], bot_ready: true };

test('staff filtering retains only their own binding even for an overbroad response', () => {
  assert.deepEqual(aiTelegramBindings(status, 7, false), [own]);
  assert.deepEqual(aiTelegramBindings({ ...status, my_binding: other }, 7, false), [own]);
  assert.deepEqual(aiTelegramBindings({ ...status, bindings: [], my_binding: own }, 7, false), [own]);
  assert.deepEqual(aiTelegramBindings(status, 1, true), [own, other]);
  for (const id of [0, -1, 1.5, NaN]) assert.deepEqual(aiTelegramBindings(status, id, false), []);
  assert.deepEqual(aiTelegramBindings(null, 7, true), []);
});

test('config parsing accepts only declared settings and rejects invalid permissions or false-like strings', () => {
  assert.deepEqual(aiTelegramConfig({ ...config, token: 'not-preserved', unknown: true }), config);
  for (const field of ['enabled', 'stream_updates', 'notify_completion']) {
    assert.throws(() => aiTelegramConfig({ ...config, [field]: 'false' }), /配置格式不正确/);
  }
  for (const default_access_mode of ['admin', undefined, '']) assert.throws(() => aiTelegramConfig({ ...config, default_access_mode }));
  assert.equal(aiTelegramConfig({ ...config, default_access_mode: 'standard' }).default_access_mode, 'standard');
});

test('binding actor IDs must be explicit positive safe integers', () => {
  for (const id of [1, 100000, Number.MAX_SAFE_INTEGER]) assert.equal(aiTelegramActorID(id), id);
  for (const id of [undefined, null, '', '7', 0, -1, 1.5, Infinity, Number.MAX_SAFE_INTEGER + 1]) assert.throws(() => aiTelegramActorID(id), /后台操作人 ID/);
});

const require = createRequire(import.meta.url);
const compiled = ts.transpileModule(component, { compilerOptions: { module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.React, target: ts.ScriptTarget.ES2020, esModuleInterop: true } }).outputText;
function renderTelegram(isAdmin) {
  let stateIndex = 0;
  const react = { ...React, useState: (initial) => React.useState(stateIndex++ === 0 ? status : initial) };
  const exports = {};
  const resolve = (name) => {
    if (name === 'react') return react;
    if (name === './aiOpsModel') return helpers;
    if (name === '../lib/api') return { apiGet() { throw new Error('unexpected SSR request'); }, apiJsonPost() { throw new Error('unexpected SSR mutation'); } };
    return require(name);
  };
  new Function('require', 'exports', compiled)(resolve, exports);
  return renderToStaticMarkup(React.createElement(exports.default, { active: true, actorID: 7, isAdmin }));
}

test('actual staff component hides admin settings, code creation, Webhook mutation and foreign bindings', () => {
  const html = renderTelegram(false);
  assert.match(html, /我的 Telegram 绑定/);
  assert.match(html, /own@example\.test/);
  assert.match(html, /撤销绑定/);
  assert.doesNotMatch(html, /other@example\.test|888888|生成一次性绑定码|生成绑定码|保存 Telegram 设置|更新 Webhook/);
  assert.match(html, /当前使用设置/);
});

test('actual administrator component includes all bindings, configuration, code generation and Webhook update', () => {
  const html = renderTelegram(true);
  for (const text of ['own@example.test', 'other@example.test', '生成一次性绑定码', '生成绑定码', '保存 Telegram 设置', '更新 Webhook']) assert.ok(html.includes(text), text);
  assert.match(html, /不代表 Webhook 已验证/);
  assert.match(html, /完整操作卡发送成功后可在 Bot 私聊中点击批准或拒绝/);
  assert.match(html, /不会自动停止已有任务/);
  for (const command of ['/ai', '/ainew', '/aisessions', '/aimodel', '/aidetails', '/aistop', '/aiexit']) assert.ok(html.includes(command), command);
});

test('mutations have role guards, codes stay ephemeral, revocation confirms task stopping and history uses existing source', () => {
  assert.match(component, /if \(!isAdmin \|\| !status \|\| !begin\('config'\)\) return/);
  assert.match(component, /if \(!isAdmin \|\| !status \|\| !begin\('code'\)\) return/);
  assert.match(component, /if \(!isAdmin \|\| !status\?\.bot_ready \|\| !begin\('webhook'\)\) return/);
  assert.match(component, /if \(values.enabled && values.default_model && !status.models.includes\(values.default_model\)\)/);
  assert.match(component, /!isAdmin && revoking\.actor_id !== actorID/);
  assert.match(component, /setCode\(null\)/);
  assert.match(component, /code\.expires_at \* 1000 - now/);
  assert.match(component, /将停止该绑定正在进行的 Telegram AI 任务/);
  assert.doesNotMatch(component, /localStorage|sessionStorage|console\.|Input\.Password/);
  for (const endpoint of ['status', 'config', 'binding-code', 'revoke', 'webhook']) assert.ok(component.includes(`/ai-ops/telegram/${endpoint}`));
  assert.match(page, /key: 'telegram', label: 'Telegram', children: <AIOpsTelegram active=\{tab === 'telegram'\} isAdmin=\{isAdmin\} actorID=\{actorID\}/);
  assert.match(page, /session\.source === 'telegram' && <Tag color="blue">TG<\/Tag>/);
  assert.doesNotMatch(component, /\/session\/create|\/chat\/send/);
});
