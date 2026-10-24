import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { test } from 'node:test';
import ts from 'typescript';

const source = await readFile(new URL('../src/lib/onlineAnalytics.ts', import.meta.url), 'utf8');
const { outputText } = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2020 } });
const { provinceCityRows, withinProvinceShare, parseCityMap } = await import(`data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`);

test('city rows remain inside the selected province and retain old province-only observations', () => {
  const cities = [
    { province_code: '440000', code: '深圳', name: '深圳', users: 4, share: 20 },
    { province_code: '440000', code: '', name: '城市未知', users: 1, share: 5 },
    { province_code: '330000', code: '杭州', name: '杭州', users: 7, share: 35 },
  ];
  const original = structuredClone(cities);
  const rows = provinceCityRows(cities, '440000', 8, 20);
  assert.deepEqual(rows, [
    { code: '深圳', name: '深圳', users: 4, share: 20 },
    { code: '', name: '城市未知', users: 4, share: 20 },
  ].sort((a, b) => b.users - a.users || a.code.localeCompare(b.code)));
  assert.equal(rows.reduce((sum, row) => sum + row.users, 0), 8);
  assert.deepEqual(cities, original);
});

test('missing city fields never invent a city or lose province users', () => {
  assert.deepEqual(provinceCityRows(undefined, '440000', 9, 30), [{ code: '', name: '城市未知', users: 9, share: 30 }]);
  assert.deepEqual(provinceCityRows([], '440000', 0, 0), []);
  assert.equal(withinProvinceShare(4, 8), 50);
  assert.equal(withinProvinceShare(0, 0), 0);
});

test('city data without a matching boundary remains available for the detail table', () => {
  const cities = [{ province_code: '650000', code: '巴音郭楞蒙古自治州', name: '巴音郭楞蒙古自治州', users: 3, share: 30 }];
  assert.deepEqual(provinceCityRows(cities, '650000', 3, 10), [{ code: '巴音郭楞蒙古自治州', name: '巴音郭楞蒙古自治州', users: 3, share: 30 }]);
});

test('city map loader rejects unexpected JSON before passing it to the chart', () => {
  const map = { type: 'FeatureCollection', features: [{ type: 'Feature', properties: { name: '深圳', label: '深圳市' }, geometry: { type: 'Polygon', coordinates: [[[113, 22], [114, 22], [114, 23], [113, 22]]] } }] };
  assert.equal(parseCityMap(map), map);
  assert.throws(() => parseCityMap({ data: [] }), /城市地图格式不正确/);
  assert.throws(() => parseCityMap({ type: 'FeatureCollection', features: [] }), /城市地图格式不正确/);
  assert.throws(() => parseCityMap({ ...map, features: [{ ...map.features[0], geometry: { type: 'Point', coordinates: [113, 22] } }] }), /城市地图格式不正确/);
});
