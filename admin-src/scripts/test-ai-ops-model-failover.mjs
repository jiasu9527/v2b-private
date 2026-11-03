import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { test } from 'node:test';
const page = await readFile(new URL('../src/pages/AIOpsPage.tsx', import.meta.url), 'utf8');
const settings = await readFile(new URL('../src/pages/AIOpsRelaySettings.tsx', import.meta.url), 'utf8');
test('fallback models hydrate and submit explicit ordered arrays, including clear', () => {
  assert.match(page, /fallback_models: next\.fallback_models \|\| \[\]/);
  assert.match(page, /fallback_models: values\.fallback_models \|\| \[\]/);
  assert.match(settings, /name="fallback_models"/);
  assert.match(settings, /form\.setFieldValue\('fallback_models', next\)/);
  assert.match(settings, /next\[index\], next\[index \+ delta\]/);
});
test('settings explain credential isolation, sticky recovery and no replay', () => {
  for (const text of ['自己的 URL 和 Key','不会重复执行已经完成的操作','下一次发送仍以会话所选模型开始','留空只切中转','默认推理强度']) assert.ok(settings.includes(text), text);
  assert.match(settings, /上移备用模型/);
  assert.match(settings, /下移备用模型/);
});
