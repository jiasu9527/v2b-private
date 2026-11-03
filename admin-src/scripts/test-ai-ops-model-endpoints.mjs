import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { test } from 'node:test';
import ts from 'typescript';

const source = await readFile(new URL('../src/pages/aiOpsModel.ts', import.meta.url), 'utf8');
const { outputText } = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2020 } });
const { aiModelEndpointForms, aiPrepareModelEndpoints, aiNormalizeEndpointURL, aiModelReady } = await import(`data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`);
const models = ['gpt-6.1-sol', 'claude-fable-5-1'];
const saved = { [models[0]]: { base_url: 'https://a.example.test/v1', has_api_key: true } };

test('per-model hydration never copies secrets and retains removed identity for explicit cleanup', () => {
  const endpoints = { ...saved, old: { base_url: 'https://old.example.test/v1', has_api_key: true, api_key: 'never-copy', api_key_cipher: 'never-copy-cipher' } };
  const form = aiModelEndpointForms(models, endpoints);
  assert.deepEqual(form[models[0]], { mode: 'dedicated', base_url: saved[models[0]].base_url, api_key: '', clear_api_key: false });
  assert.deepEqual(form[models[1]], { mode: 'shared', base_url: '', api_key: '', clear_api_key: false });
  assert.equal(form.old.mode, 'dedicated');
  assert.ok(!JSON.stringify(form).includes('never-copy'));
  form[models[0]].base_url = 'changed';
  assert.equal(saved[models[0]].base_url, 'https://a.example.test/v1');
});

test('blank keys stay bound to exact model and normalized address', () => {
  const form = aiModelEndpointForms(models, saved);
  form[models[0]].base_url = ' https://a.example.test/v1/chat/completions/ ';
  assert.deepEqual(aiPrepareModelEndpoints(form, saved, models, true), { [models[0]]: { base_url: saved[models[0]].base_url, api_key: '', clear_api_key: false } });
  form[models[1]] = { ...form[models[0]] };
  assert.throws(() => aiPrepareModelEndpoints(form, saved, models, true), /独立 API Key/);
  form[models[1]].api_key = 'provider-b-key';
  const payload = aiPrepareModelEndpoints(form, saved, models, true);
  assert.equal(payload[models[1]].api_key, 'provider-b-key');
  assert.equal(payload[models[0]].api_key, '');
});

test('changing URL cannot forward old key and clear cannot combine with replacement', () => {
  const form = aiModelEndpointForms(models, saved);
  form[models[0]].base_url = 'https://b.example.test';
  assert.throws(() => aiPrepareModelEndpoints(form, saved, models, true), /修改地址后请重新输入/);
  form[models[0]].clear_api_key = true;
  assert.throws(() => aiPrepareModelEndpoints(form, saved, models, true), /启用前请填写/);
  assert.equal(aiPrepareModelEndpoints(form, saved, models, false)[models[0]].clear_api_key, true);
  form[models[0]].api_key = 'replacement';
  assert.throws(() => aiPrepareModelEndpoints(form, saved, models, false), /不能同时使用/);
  form[models[0]].clear_api_key = false;
  assert.equal(aiPrepareModelEndpoints(form, saved, models, true)[models[0]].base_url, 'https://b.example.test/v1');
});

test('removed model requires explicit override removal; shared mode never submits stale typed credentials', () => {
  const form = aiModelEndpointForms(models, saved);
  assert.throws(() => aiPrepareModelEndpoints(form, saved, [models[1]], false), /切回默认中转/);
  form[models[0]].mode = 'shared'; form[models[0]].api_key = 'stale-local-secret';
  assert.deepEqual(aiPrepareModelEndpoints(form, saved, [models[1]], false), {});
  assert.deepEqual(aiPrepareModelEndpoints({}, saved, models, false), {});
  const strange = aiModelEndpointForms(['__proto__'], JSON.parse('{"__proto__":{"base_url":"https://special.test/v1","has_api_key":true}}'));
  assert.equal(Object.getPrototypeOf(strange), Object.prototype);
  assert.equal(strange.__proto__.mode, 'dedicated');
});

test('Base URL validator rejects embedded credentials and preserves provider path', () => {
  assert.equal(aiNormalizeEndpointURL('https://example.test'), 'https://example.test/v1');
  assert.equal(aiNormalizeEndpointURL('https://example.test/chat/completions'), 'https://example.test/v1');
  assert.equal(aiNormalizeEndpointURL('https://example.test/custom/chat/completions'), 'https://example.test/custom');
  assert.equal(aiNormalizeEndpointURL('http://127.0.0.1:1234'), 'http://127.0.0.1:1234/v1');
  for (const value of ['http://public.test', 'https://user:key@example.test', 'https://example.test?key=secret', 'https://example.test/#fragment', 'not a URL']) assert.throws(() => aiNormalizeEndpointURL(value));
});

test('staff and dedicated-only config use safe readiness rather than global key presence', () => {
  const config = { enabled: true, models, has_api_key: false, ready_models: [models[0]], model_endpoints: {} };
  assert.equal(aiModelReady(config, models[0]), true);
  assert.equal(aiModelReady(config, models[1]), false);
  assert.equal(aiModelReady({ ...config, enabled: false }, models[0]), false);
  assert.equal(aiModelReady({ ...config, has_api_key: true, ready_models: [] }, models[0]), false);
  assert.equal(aiModelReady({ enabled: true, models, has_api_key: true }, models[1]), true);
  assert.equal(aiModelReady({ enabled: true, models, has_api_key: false, model_endpoints: saved }, models[0]), true);
  assert.equal(aiModelReady({ ...config, ready_models: ['removed'] }, 'removed'), false);
});

test('settings remain admin only, save explicit model map, and selected-model readiness gates chat', async () => {
  const page = await readFile(new URL('../src/pages/AIOpsPage.tsx', import.meta.url), 'utf8');
  const ui = await readFile(new URL('../src/pages/AIOpsModelEndpointSettings.tsx', import.meta.url), 'utf8');
  assert.match(page, /const settings = isAdmin \?/);
  assert.match(page, /model_endpoints: modelEndpoints/);
  assert.match(page, /configForm\.setFieldValue\('model_endpoint_forms', aiModelEndpointForms\(next.models, next.model_endpoints\)\)/);
  assert.match(page, /const unavailable = !aiModelReady\(config, model\)/);
  assert.match(page, /!content.trim\(\) \|\| !aiModelReady\(config, model\)/);
  assert.doesNotMatch(page, /!config.enabled \|\| !config.has_api_key/);
  assert.match(ui, /独立模型不会自动回退到默认中转/);
  assert.match(ui, /Input.Password[^>]+autoComplete="new-password"/);
});
