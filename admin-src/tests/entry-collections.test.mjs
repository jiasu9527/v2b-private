import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

const source = readFileSync(new URL('../src/pages/clientEntryCollections.ts', import.meta.url), 'utf8');
const compiled = ts.transpileModule(source, { compilerOptions: { target: ts.ScriptTarget.ES2020, module: ts.ModuleKind.CommonJS } }).outputText;
const sandbox = { exports: {} };
vm.runInNewContext(compiled, sandbox);
const { attachEntryCollections, availableEntryCollectionOptions, canJoinEntryCollection, entryCollectionMember, entryCollectionMemberKey, parseEntryCollections } = sandbox.exports;
const plain = (value) => JSON.parse(JSON.stringify(value));
const rows = [
  { id: 9, __row_kind: 'policy', action: 'override', name: 'A' },
  { id: 2, __row_kind: 'split_group', __split_group: { id: 17, is_leaf: true }, name: 'B' },
  { id: 3, __row_kind: 'policy', action: 'original', name: 'C' },
  { id: 4, __row_kind: 'policy', action: 'hide', name: 'D' },
];
const collection = { id: 1, name: 'shared', version: 2, entry_host: 'entry.example.com', items: [{ kind: 'policy', id: 9 }, { kind: 'split_group', id: 17 }] };

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
