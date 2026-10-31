import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { createContext, runInContext } from 'node:vm';
import { test } from 'node:test';
import ts from 'typescript';

const source = await readFile(new URL('../src/lib/telegramMiniTheme.ts', import.meta.url), 'utf8');
const context = createContext({ exports: {} });
runInContext(ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2020 } }).outputText, context);
const { telegramMiniTheme, miniSafeInset, miniViewportHeight } = context.exports;

test('Telegram light and dark fallbacks provide every CSS theme field', () => {
  for (const scheme of ['light', 'dark', undefined]) {
    const palette = telegramMiniTheme(scheme);
    assert.equal(palette.dark, scheme === 'dark');
    assert.equal(Object.keys(palette.colors).length, 15);
    for (const value of Object.values(palette.variables)) assert.match(value, /^#[a-f0-9]{6}$/);
    assert.equal(palette.tokens.colorPrimary, palette.colors.button_color);
    assert.equal(palette.tokens.colorBgContainer, palette.colors.section_bg_color);
  }
  assert.notEqual(telegramMiniTheme('light').colors.bg_color, telegramMiniTheme('dark').colors.bg_color);
});

test('Telegram custom colors and short RGB drive Ant Design and portalled content consistently', () => {
  const palette = telegramMiniTheme('light', { button_color: '#0a9', bg_color: '#FAFEFD', text_color: '#123', button_text_color: '#eee', section_bg_color: '#fdfdfd' });
  assert.equal(palette.tokens.colorPrimary, '#00aa99');
  assert.equal(palette.tokens.colorBgBase, '#fafefd');
  assert.equal(palette.tokens.colorText, '#112233');
  assert.equal(palette.tokens.colorTextLightSolid, '#eeeeee');
  assert.equal(palette.variables['--tg-theme-section-bg-color'], '#fdfdfd');
  assert.equal(palette.variables['--tg-theme-link-color'], '#00aa99');
});

test('malformed theme colors cannot become CSS values', () => {
  for (const value of ['red;display:none', 'url(https://example.test/x)', 'var(--other)', '#fffffff', '', null, 123, '#12']) {
    const palette = telegramMiniTheme('dark', { button_color: value, bg_color: value });
    assert.equal(palette.colors.button_color, '#8774e1');
    assert.equal(palette.colors.bg_color, '#212121');
  }
});

test('viewport and safe area helpers clamp invalid SDK values', () => {
  for (const value of [undefined, null, -10, Infinity, -Infinity, NaN, 'bad']) assert.equal(miniSafeInset(value), 0);
  assert.equal(miniSafeInset(24), 24);
  assert.equal(miniSafeInset(999999), 200);
  for (const value of [undefined, NaN, Infinity, -5, 0]) assert.equal(miniViewportHeight(value, 500), 500);
  assert.equal(miniViewportHeight(844, 500), 500);
  assert.equal(miniViewportHeight(480, 500), 480);
});
