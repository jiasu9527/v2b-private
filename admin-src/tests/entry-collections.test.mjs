import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

const source = readFileSync(new URL('../src/pages/clientEntryCollections.ts', import.meta.url), 'utf8');
const compiled = ts.transpileModule(source, { compilerOptions: { target: ts.ScriptTarget.ES2020, module: ts.ModuleKind.CommonJS } }).outputText;
const sandbox = { exports: {} };
vm.runInNewContext(compiled, sandbox);
const { attachEntryCollections, availableEntryCollectionOptions, collectionResolveDefaults, effectiveEntryResolveHost, entryCollectionResolveLabel, canJoinEntryCollection, entryCollectionMember, entryCollectionMemberKey, parseEntryCollections } = sandbox.exports;
const plain = (value) => JSON.parse(JSON.stringify(value));
const rows = [
  { id: 9, __row_kind: 'policy', action: 'override', name: 'A' },
  { id: 2, __row_kind: 'split_group', __split_group: { id: 17, is_leaf: true }, name: 'B' },
  { id: 3, __row_kind: 'policy', action: 'original', name: 'C' },
  { id: 4, __row_kind: 'policy', action: 'hide', name: 'D' },
];
const collection = { id: 1, name: 'shared', version: 2, entry_host: 'entry.example.com', resolve_entry_host: null, items: [{ kind: 'policy', id: 9 }, { kind: 'split_group', id: 17 }] };

test('collections keep original rows and global priority order intact', () => {
  const output = attachEntryCollections(rows, [collection]);
  assert.deepEqual(plain(output.map(entryCollectionMember)), [{ kind: 'policy', id: 9 }, { kind: 'split_group', id: 17 }, { kind: 'policy', id: 3 }, { kind: 'policy', id: 4 }]);
  assert.equal(output[0].__entry_collection, collection);
  assert.equal(output[1].__entry_collection, collection);
  assert.equal(output[2].__entry_collection, undefined);
  assert.equal(rows[0].__entry_collection, undefined);
  assert.equal(output.filter((row) => !row.__entry_collection).length, 2);
});

test('only override rules and current split leaves are selectable', () => {
  assert.deepEqual(rows.filter(canJoinEntryCollection).map((row) => entryCollectionMemberKey(entryCollectionMember(row))), ['policy:9', 'split_group:17']);
});

test('member editor cannot silently take members from another collection', () => {
  const options = [{ value: 'policy:1' }, { value: 'policy:2', collection_id: 7 }, { value: 'policy:3', collection_id: 8 }];
  assert.deepEqual(availableEntryCollectionOptions(options).map((item) => item.value), ['policy:1']);
  assert.deepEqual(availableEntryCollectionOptions(options, 7).map((item) => item.value), ['policy:1', 'policy:2']);
});

test('failed or malformed collection fetch does not turn grouped members into ungrouped rules', () => {
  for (const value of [undefined, null, {}, { message: 'service unavailable' }, [{ ...collection, items: undefined }], [{ ...collection, version: 0 }], [{ ...collection, items: [{ kind: 'unknown', id: 1 }] }]]) {
    assert.throws(() => parseEntryCollections(value));
  }
  assert.deepEqual(plain(parseEntryCollections([])), []);
  assert.deepEqual(plain(parseEntryCollections([collection])), [collection]);
});

test('duplicate membership and stale missing rules are rejected', () => {
  assert.throws(() => attachEntryCollections(rows, [collection, { ...collection, id: 2 }]));
  assert.throws(() => attachEntryCollections(rows.slice(1), [collection]));
});


test('split leaf resolution overrides its parent and legacy leaves inherit the parent', () => {
  assert.equal(effectiveEntryResolveHost({ resolve_entry_host: 1, __split_group: { resolve_entry_host: 0 } }), false);
  assert.equal(effectiveEntryResolveHost({ resolve_entry_host: 0, __split_group: { resolve_entry_host: 1 } }), true);
  assert.equal(effectiveEntryResolveHost({ resolve_entry_host: 1, __split_group: { resolve_entry_host: null } }), true);
  assert.equal(effectiveEntryResolveHost({ resolve_entry_host: 0, __split_group: {} }), false);
  assert.equal(effectiveEntryResolveHost({ resolve_entry_host: 1 }), true);
  assert.equal(effectiveEntryResolveHost({ resolve_entry_host: 0 }), false);
});

test('new and legacy collections default from selected members without silently enabling mixed settings', () => {
  const options = [
    { value: 'policy:1', resolve_entry_host: true },
    { value: 'split_group:2', resolve_entry_host: true },
    { value: 'split_group:3', resolve_entry_host: false },
  ];
  assert.deepEqual(plain(collectionResolveDefaults(options, ['policy:1', 'split_group:2'])), { enabled: true, mixed: false });
  assert.deepEqual(plain(collectionResolveDefaults(options, ['policy:1', 'split_group:3'])), { enabled: false, mixed: true });
  assert.deepEqual(plain(collectionResolveDefaults(options, ['split_group:3'])), { enabled: false, mixed: false });
  assert.deepEqual(plain(collectionResolveDefaults(options, ['policy:1', 'split_group:2'], collection)), { enabled: true, mixed: false });
  assert.deepEqual(plain(collectionResolveDefaults(options, [])), { enabled: false, mixed: false });
});

test('configured collection keeps its setting when members are changed', () => {
  const options = [{ value: 'policy:1', resolve_entry_host: false }];
  assert.equal(collectionResolveDefaults(options, ['policy:1'], { ...collection, resolve_entry_host: 1 }).enabled, true);
  assert.equal(collectionResolveDefaults([{ value: 'policy:1', resolve_entry_host: true }], ['policy:1'], { ...collection, resolve_entry_host: 0 }).enabled, false);
});

test('collection resolution states are parsed distinctly and invalid states fail closed', () => {
  for (const value of [0, 1, null]) {
    assert.equal(parseEntryCollections([{ ...collection, resolve_entry_host: value }])[0].resolve_entry_host, value);
  }
  const old = { ...collection };
  delete old.resolve_entry_host;
  assert.equal(parseEntryCollections([old])[0].resolve_entry_host, null);
  for (const value of [2, -1, false, '1']) assert.throws(() => parseEntryCollections([{ ...collection, resolve_entry_host: value }]));
  assert.equal(entryCollectionResolveLabel(collection), '解析设置待统一');
  assert.equal(entryCollectionResolveLabel({ ...collection, resolve_entry_host: 1 }), '解析为 IP');
  assert.equal(entryCollectionResolveLabel({ ...collection, resolve_entry_host: 0 }), '直接下发域名 / IP');
});
