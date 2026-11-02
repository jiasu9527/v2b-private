import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { test } from 'node:test';
import ts from 'typescript';

const source = await readFile(new URL('../src/pages/aiOpsComposerSize.ts', import.meta.url), 'utf8');
const compiled = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2020 } }).outputText;

function sizing({ width = 320, initialHeight = '', borderBox = true } = {}) {
  const frames = new Map(), nodes = [], calls = { computed: 0, bounds: 0, reads: 0, writes: 0, disconnect: 0 };
  let id = 0, actualHeight = initialHeight, observer;
  const input = {
    isConnected: true, value: '', selectionStart: 2, selectionEnd: 4, scrollTop: 17,
    style: { get height() { return actualHeight; }, set height(value) { calls.writes++; actualHeight = value; } },
    getBoundingClientRect() { calls.bounds++; return { width }; },
  };
  const window = Object.assign(new EventTarget(), {
    requestAnimationFrame(callback) { frames.set(++id, callback); return id; },
    cancelAnimationFrame(key) { frames.delete(key); },
    getComputedStyle(target) {
      assert.equal(target, input); calls.computed++;
      const values = { 'box-sizing': borderBox ? 'border-box' : 'content-box', width: `${width}px`, 'line-height': '22px', 'padding-top': '12px', 'padding-bottom': '12px', 'padding-left': '14px', 'padding-right': '14px', 'border-top-width': '0px', 'border-bottom-width': '0px', 'border-left-width': '0px', 'border-right-width': '0px' };
      return { getPropertyValue: (key) => values[key] || '' };
    },
  });
  const document = Object.assign(new EventTarget(), {
    visibilityState: 'visible', fonts: new EventTarget(),
    body: { appendChild(node) { nodes.push(node); } },
    createElement(tag) {
      assert.equal(tag, 'textarea');
      return {
        value: '', style: { cssText: '', setProperty(key, value) { this[key] = value; } }, attrs: {},
        setAttribute(key, value) { this.attrs[key] = value; },
        remove() { nodes.splice(nodes.indexOf(this), 1); },
        get scrollHeight() {
          calls.reads++;
          const contentWidth = parseFloat(this.style.width) - (borderBox ? 28 : 0);
          const chars = Math.max(1, Math.floor(contentWidth / 8));
          const lines = this.value.split('\n').reduce((count, line) => count + Math.max(1, Math.ceil(line.length / chars)), 0);
          return lines * 22 + 24;
        },
      };
    },
  });
  class ResizeObserver {
    constructor(callback) { observer = callback; }
    observe(target) { assert.equal(target, input); }
    disconnect() { calls.disconnect++; }
  }
  const exports = {};
  new Function('exports', 'window', 'document', 'ResizeObserver', compiled)(exports, window, document, ResizeObserver);
  const handle = exports.watchMiniComposerSize(input);
  return {
    input, window, document, nodes, calls, frames, handle,
    flush() { const pending = [...frames.values()]; frames.clear(); for (const callback of pending) callback(); },
    resize(next, legacy = false) { width = next; observer([{ target: input, ...(legacy ? {} : { borderBoxSize: [{ inlineSize: next }] }) }]); },
  };
}

test('fallback coalesces input into one offscreen measurement per frame and caches styles', () => {
  const ui = sizing();
  for (let i = 0; i < 30; i++) { ui.input.value += 'a'; ui.handle.refresh(); }
  assert.equal(ui.frames.size, 1);
  assert.equal(ui.calls.computed, 0); assert.equal(ui.calls.reads, 0);
  ui.flush();
  assert.equal(ui.calls.computed, 1); assert.equal(ui.calls.bounds, 1); assert.equal(ui.calls.reads, 1);
  assert.equal(ui.input.style.height, '46px'); assert.equal(ui.calls.writes, 1);
  for (let i = 0; i < 5; i++) { ui.input.value += 'a'; ui.handle.refresh(); ui.flush(); }
  assert.equal(ui.calls.computed, 1); assert.equal(ui.calls.bounds, 1);
  assert.equal(ui.calls.reads, 6); assert.equal(ui.calls.writes, 1);
  ui.handle.dispose();
});

test('fallback grows to four lines, handles a large paste and shrinks when cleared without touching selection', () => {
  const ui = sizing(); ui.flush();
  const selection = [ui.input.selectionStart, ui.input.selectionEnd, ui.input.scrollTop];
  ui.input.value = 'a\nb'; ui.handle.refresh(); ui.flush();
  assert.equal(ui.input.style.height, '68px'); assert.equal(ui.input.value, 'a\nb');
  ui.input.value = 'x'.repeat(12000); ui.handle.refresh(); ui.flush();
  assert.equal(ui.input.style.height, '112px'); assert.equal(ui.input.value.length, 12000);
  assert.deepEqual([ui.input.selectionStart, ui.input.selectionEnd, ui.input.scrollTop], selection);
  ui.input.value = ''; ui.handle.refresh(); ui.flush();
  assert.equal(ui.input.style.height, '46px'); assert.equal(ui.input.value, '');
  ui.handle.dispose();
});

test('height-only ResizeObserver notifications do not schedule a feedback loop; width and fonts recalibrate', () => {
  const ui = sizing(); ui.flush();
  ui.resize(320); assert.equal(ui.frames.size, 0);
  ui.resize(200); ui.resize(200); assert.equal(ui.frames.size, 1); ui.flush();
  assert.equal(ui.calls.computed, 2); assert.equal(ui.calls.reads, 2);
  ui.resize(200, true); assert.equal(ui.frames.size, 0);
  ui.document.fonts.dispatchEvent(new Event('loadingdone')); ui.flush();
  assert.equal(ui.calls.computed, 3);
  ui.window.dispatchEvent(new Event('orientationchange')); ui.flush();
  assert.equal(ui.calls.computed, 4);
  ui.handle.dispose();
});

test('hidden tabs never cache zero width and resuming recalibrates before measuring', () => {
  const ui = sizing({ width: 0 }); ui.flush();
  assert.equal(ui.calls.computed, 0); assert.equal(ui.calls.reads, 0); assert.equal(ui.calls.writes, 0);
  ui.resize(320); ui.flush(); assert.equal(ui.input.style.height, '46px');
  ui.document.visibilityState = 'hidden'; ui.input.value = 'one\ntwo'; ui.handle.refresh(); ui.flush();
  assert.equal(ui.calls.reads, 1);
  ui.document.visibilityState = 'visible'; ui.document.dispatchEvent(new Event('visibilitychange')); ui.flush();
  assert.equal(ui.input.style.height, '68px'); assert.equal(ui.calls.computed, 2);
  ui.handle.dispose();
});

test('content-box calculations exclude padding while preserving one-to-four line bounds', () => {
  const ui = sizing({ borderBox: false }); ui.flush();
  assert.equal(ui.input.style.height, '22px');
  ui.input.value = 'a\nb\nc\nd\ne'; ui.handle.refresh(); ui.flush();
  assert.equal(ui.input.style.height, '88px'); ui.handle.dispose();
});

test('disposing cancels work, removes observers and mirror content, restores owned height, and ignores late callbacks', () => {
  const ui = sizing({ initialHeight: '48px' }); ui.flush();
  const shadow = ui.nodes[0];
  assert.equal(shadow.attrs['aria-hidden'], 'true'); assert.equal(shadow.readOnly, true);
  ui.input.value = 'draft'; ui.handle.refresh();
  const late = [...ui.frames.values()][0];
  ui.handle.dispose();
  assert.equal(ui.frames.size, 0); assert.equal(ui.nodes.length, 0); assert.equal(shadow.value, '');
  assert.equal(ui.calls.disconnect, 1); assert.equal(ui.input.style.height, '48px');
  const before = { ...ui.calls }; late(); ui.handle.refresh();
  ui.window.dispatchEvent(new Event('resize')); ui.document.fonts.dispatchEvent(new Event('loadingdone'));
  assert.deepEqual(ui.calls, before); assert.equal(ui.frames.size, 0);
  const replacement = sizing(); replacement.flush(); replacement.input.style.height = '99px';
  replacement.handle.dispose(); assert.equal(replacement.input.style.height, '99px');
});
