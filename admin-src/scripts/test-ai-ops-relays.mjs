import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { test } from 'node:test';
import ts from 'typescript';

const source = await readFile(new URL('../src/pages/aiOpsModel.ts', import.meta.url), 'utf8');
const { outputText } = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2020 } });
const { aiFallbackRelayForms, aiPrepareFallbackRelays, AI_DEFAULT_FAILOVER_TIMEOUT, AI_MAX_FALLBACK_RELAYS } = await import(`data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`);
const models = ['gpt-6.1-sol', 'claude-fable-5-1'];
const saved = [
  { id: 'backup-a', name: 'A', base_url: 'https://a.example.test/v1', models: [models[0]], enabled: true, has_api_key: true },
  { id: 'backup-b', name: 'B', base_url: 'https://b.example.test/v1', models: [], enabled: false, has_api_key: false },
];

test('legacy configurations default to no backup, a 30 second timeout, and a maximum of four backups', () => {
  assert.deepEqual(aiFallbackRelayForms(), []);
  assert.deepEqual(aiPrepareFallbackRelays(), []);
  assert.equal(AI_DEFAULT_FAILOVER_TIMEOUT, 30);
  assert.equal(AI_MAX_FALLBACK_RELAYS, 4);
});

test('hydration never loads a secret, clears key controls and does not mutate the response', () => {
  const response = saved.map((item) => ({ ...item, api_key: 'must-never-load', clear_api_key: true }));
  const form = aiFallbackRelayForms(response);
  assert.equal(form[0].api_key, '');
  assert.equal(form[0].clear_api_key, false);
  assert.equal(form[0].has_api_key, undefined);
  form[0].models.push(models[1]);
  assert.deepEqual(response[0].models, [models[0]]);
  assert.equal(response[0].api_key, 'must-never-load');
});

test('priority changes preserve identity and a blank key remains bound to its own saved backup', () => {
  const forms = aiFallbackRelayForms(saved).reverse();
  const payload = aiPrepareFallbackRelays(forms, saved, models);
  assert.deepEqual(payload.map((item) => [item.id, item.base_url, item.api_key]), [
    ['backup-b', saved[1].base_url, ''], ['backup-a', saved[0].base_url, ''],
  ]);
  assert.deepEqual(payload[0].models, []);
  assert.equal(payload[0].has_api_key, undefined);
  assert.deepEqual(saved.map((item) => item.id), ['backup-a', 'backup-b']);
});

test('new backups omit ID, preserve explicit model case, and can be saved without keys only while disabled', () => {
  const input = { id: '', name: ' New ', base_url: ' https://new.example.test/v1 ', enabled: true, models: ['gpt-6.1-sol', ' gpt-6.1-sol '], api_key: 'new-fixture-key', clear_api_key: false };
  const [output] = aiPrepareFallbackRelays([input], saved, models);
  assert.equal(Object.hasOwn(output, 'id'), false);
  assert.equal(output.name, 'New');
  assert.equal(output.base_url, 'https://new.example.test/v1');
  assert.deepEqual(output.models, [models[0]]);
  assert.equal(output.api_key, 'new-fixture-key');
  assert.throws(() => aiPrepareFallbackRelays([{ ...input, api_key: '' }], saved, models), /启用前请填写 API Key/);
  assert.equal(aiPrepareFallbackRelays([{ ...input, enabled: false, api_key: '' }], saved, models)[0].api_key, '');
});

test('changing a saved address cannot silently forward its old key; fresh key or explicit clear is required', () => {
  const changed = { ...aiFallbackRelayForms(saved)[0], base_url: 'https://other.example.test/v1' };
  assert.throws(() => aiPrepareFallbackRelays([changed], saved, models), /修改地址后请重新输入/);
  assert.equal(aiPrepareFallbackRelays([{ ...changed, api_key: 'replacement-key' }], saved, models)[0].api_key, 'replacement-key');
  assert.throws(() => aiPrepareFallbackRelays([{ ...changed, clear_api_key: true }], saved, models), /启用前请填写 API Key/);
  assert.equal(aiPrepareFallbackRelays([{ ...changed, clear_api_key: true, enabled: false }], saved, models)[0].clear_api_key, true);
  assert.throws(() => aiPrepareFallbackRelays([{ ...changed, api_key: 'replacement-key', clear_api_key: true }], saved, models), /不能同时使用/);
});

test('unknown or duplicate identities cannot inherit keys; removing all backups submits an explicit empty list', () => {
  const forms = aiFallbackRelayForms(saved);
  assert.throws(() => aiPrepareFallbackRelays([{ ...forms[0], id: 'unknown' }], saved, models), /配置已变化/);
  assert.throws(() => aiPrepareFallbackRelays([forms[0], forms[0]], saved, models), /配置已变化/);
  assert.deepEqual(aiPrepareFallbackRelays([], saved, models), []);
  assert.throws(() => aiPrepareFallbackRelays(Array(5).fill({}), saved, models), /最多配置 4 个/);
});

test('recreating a deleted backup with the same name and address never inherits its old identity or key', () => {
  const replacement = { name: saved[0].name, base_url: saved[0].base_url, models: [], enabled: true, api_key: '', clear_api_key: false };
  assert.throws(() => aiPrepareFallbackRelays([replacement], saved, models), /启用前请填写 API Key/);
  const [output] = aiPrepareFallbackRelays([{ ...replacement, enabled: false }], saved, models);
  assert.equal(Object.hasOwn(output, 'id'), false);
  assert.equal(output.api_key, '');
});

test('removing a global model requires an explicit backup edit instead of silently broadening model support', () => {
  const forms = aiFallbackRelayForms(saved);
  assert.throws(() => aiPrepareFallbackRelays(forms, saved, [models[1]]), /当前全局模型列表/);
  assert.deepEqual(aiPrepareFallbackRelays([{ ...forms[0], models: [] }], saved, [models[1]])[0].models, []);
  assert.deepEqual(aiPrepareFallbackRelays([{ ...forms[0], models: [models[1]] }], saved, [models[1]])[0].models, [models[1]]);
});
