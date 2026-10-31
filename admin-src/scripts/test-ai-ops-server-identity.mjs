import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { test } from 'node:test';
import ts from 'typescript';

const source = await readFile(new URL('../src/pages/aiOpsServerIdentity.ts', import.meta.url), 'utf8');
const compiled = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2020 } }).outputText;
const helpers = {};
new Function('exports', compiled)(helpers);
const { aiServerConnection } = helpers;

test('SSH display preserves IPv4 addresses, domain names, usernames and ports', () => {
  assert.equal(aiServerConnection({ username: 'root', host: '192.0.2.10', port: 22 }), 'root@192.0.2.10:22');
  assert.equal(aiServerConnection({ username: 'operations', host: 'Node-01.example.test', port: 54101 }), 'operations@Node-01.example.test:54101');
});

test('SSH display brackets IPv6 without duplicating existing brackets', () => {
  for (const host of ['2001:db8::1234', '::1', 'fe80::1%eth0', '::ffff:192.0.2.10']) {
    assert.equal(aiServerConnection({ username: 'root', host, port: 2222 }), `root@[${host}]:2222`);
    assert.equal(aiServerConnection({ username: 'root', host: `[${host}]`, port: 2222 }), `root@[${host}]:2222`);
  }
});

test('SSH display does not mutate credentials or silently substitute connection values', () => {
  const server = Object.freeze({ username: 'ops', host: '2001:db8::9', port: 0 });
  assert.equal(aiServerConnection(server), 'ops@[2001:db8::9]:0');
  assert.deepEqual(server, { username: 'ops', host: '2001:db8::9', port: 0 });
});
