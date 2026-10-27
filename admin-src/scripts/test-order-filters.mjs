import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import ts from 'typescript';

const source = await readFile(new URL('../src/pages/OrderPage.tsx', import.meta.url), 'utf8');
const file = ts.createSourceFile('OrderPage.tsx', source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
const declaration = file.statements.filter(ts.isVariableStatement)
  .flatMap((statement) => [...statement.declarationList.declarations])
  .find((item) => item.name.getText(file) === 'filterDefs');
assert.ok(declaration && ts.isArrayLiteralExpression(declaration.initializer));
const definitions = declaration.initializer.elements;
const property = (object, name) => object.properties.find((item) => ts.isPropertyAssignment(item) && item.name.getText(file) === name)?.initializer;

function conditions(key) {
  const definition = definitions.find((item) => ts.isObjectLiteralExpression(item) && property(item, 'key')?.text === key);
  assert.ok(definition, `missing ${key} filter`);
  const options = property(definition, 'condition');
  assert.ok(options && ts.isArrayLiteralExpression(options));
  return options.elements.map((option) => { assert.ok(ts.isStringLiteral(option)); return option.text; });
}

test('order user ID offers equality by default and strict numeric bounds', () => {
  assert.deepEqual(conditions('user_id'), ['=', '>', '<']);
});

test('other order ID and status filters keep their existing operators', () => {
  assert.deepEqual(conditions('invite_user_id'), ['=', '!=']);
  assert.deepEqual(conditions('status'), ['=']);
});
