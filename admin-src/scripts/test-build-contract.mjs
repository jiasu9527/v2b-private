import assert from 'node:assert/strict';
import { mkdtemp, realpath, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { build } from 'vite';

const configFile = fileURLToPath(new URL('../vite.config.ts', import.meta.url));

async function withFixture(run) {
  const root = await realpath(await mkdtemp(path.join(tmpdir(), 'forest-admin-build-')));
  try {
    await Promise.all([
      writeFile(path.join(root, 'index.html'), '<div id="root"></div><script type="module" src="/main.js"></script>'),
      // Reproduce the entry -> lazy route -> entry cycle that loaded React twice
      // when the Go shell requested admin.js?v=... and the route imported admin.js.
      writeFile(path.join(root, 'main.js'), `
        export const sharedRuntime = { name: 'single runtime' };
        globalThis.adminRuntime = sharedRuntime;
        globalThis.openOnlineAnalytics = () => import('./route.js').then(route => route.render());
      `),
      writeFile(path.join(root, 'route.js'), `
        import { sharedRuntime } from './main.js';
        export function render() { return sharedRuntime; }
      `),
    ]);
    await run((output = {}) => build({
      configFile,
      root,
      envDir: root,
      publicDir: false,
      cacheDir: path.join(root, '.vite'),
      logLevel: 'silent',
      build: {
        write: false,
        emptyOutDir: false,
        outDir: path.join(root, 'dist'),
        rollupOptions: { input: path.join(root, 'index.html'), output },
      },
    }));
  } finally {
    await rm(root, { recursive: true, force: true });
  }
}

test('production config keeps a lazy route and its shared runtime in one admin.js', async () => {
  await withFixture(async (buildFixture) => {
    const result = await buildFixture();
    assert.ok(!Array.isArray(result) && 'output' in result);
    const chunks = result.output.filter(item => item.type === 'chunk');
    assert.equal(chunks.length, 1);
    assert.equal(chunks[0].fileName, 'admin.js');
    assert.equal(chunks[0].isEntry, true);
    assert.deepEqual(chunks[0].imports, []);
    assert.deepEqual(chunks[0].dynamicImports, []);
  });
});

test('production guard rejects code splitting before query-versioned modules can diverge', async () => {
  await withFixture(async (buildFixture) => {
    await assert.rejects(buildFixture({ inlineDynamicImports: false }), error => {
      assert.equal(error.plugin, 'single-admin-entry');
      assert.match(error.message, /query-versioned admin shell requires a self-contained admin\.js/);
      return true;
    });
  });
});
