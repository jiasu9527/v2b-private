import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { test } from 'node:test';
import ts from 'typescript';

const source = await readFile(new URL('../src/lib/onlineAnalytics.ts', import.meta.url), 'utf8');
const { outputText } = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2020 } });
const { provinceCityRows, withinProvinceShare, parseCityMap, onlineUsersParams, onlineUserLocation, onlineUserLocationNote } = await import(`data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`);

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

test('online user filters preserve unknown city and province scopes without leaking UI titles', () => {
  assert.deepEqual(onlineUsersParams({ title: '广东 · 城市未知', scope: 'city', province_code: '440000', city: '' }, 2, 20), { scope: 'city', province_code: '440000', city: '', page: 2, page_size: 20 });
  assert.deepEqual(onlineUsersParams({ title: '省份未知', scope: 'province', province_code: '' }, 1, 50, '  member@example.test  '), { scope: 'province', province_code: '', page: 1, page_size: 50, search: 'member@example.test' });
  assert.deepEqual(onlineUsersParams({ title: '地址格式无法识别', scope: 'unknown', reason: 'invalid_ip' }, 1, 20), { scope: 'unknown', reason: 'invalid_ip', page: 1, page_size: 20 });
});

test('user location explains global and partial unknowns without presenting stale locations as current', () => {
  const user = { id: 1, email: 'member@example.test', last_seen_at: 1000, location_seen_at: 900, country_code: 'CN', country_name: '中国', province_code: '440000', province_name: '广东', city_name: '深圳', unknown_reason: '' };
  assert.equal(onlineUserLocation(user), '中国 · 广东 · 深圳');
  assert.equal(onlineUserLocationNote(user), '');
  assert.equal(onlineUserLocationNote({ ...user, city_name: '' }), '已识别省份，城市未知');
  assert.equal(onlineUserLocationNote({ ...user, province_code: '', province_name: '', city_name: '' }), '已识别国家 / 地区，省份未知');
  assert.equal(onlineUserLocation({ ...user, unknown_reason: 'location_stale' }), '位置未知');
  assert.equal(onlineUserLocationNote({ ...user, unknown_reason: 'invalid_ip' }), '地址格式无法识别');
  assert.equal(onlineUserLocationNote({ ...user, unknown_reason: 'not_reported' }, { not_reported: '等待节点同步' }), '等待节点同步');
  assert.equal(onlineUserLocation({ ...user, country_name: '香港', province_name: '香港', city_name: '香港' }), '香港');
});

test('online user request sends an explicit empty city and retains auth and cancellation', async (t) => {
  const apiSource = await readFile(new URL('../src/lib/api.ts', import.meta.url), 'utf8');
  const compiled = ts.transpileModule(apiSource, { compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2020 } }).outputText;
  const { apiGet } = await import(`data:text/javascript;base64,${Buffer.from(compiled).toString('base64')}`);
  const original = Object.fromEntries(['fetch', 'window', 'localStorage', 'location'].map((key) => [key, Object.getOwnPropertyDescriptor(globalThis, key)]));
  t.after(() => { for (const [key, descriptor] of Object.entries(original)) { if (descriptor) Object.defineProperty(globalThis, key, descriptor); else delete globalThis[key]; } });
  globalThis.window = { settings: { secure_path: 'test-admin' } };
  globalThis.location = { pathname: '/test-admin/dashboard/online' };
  globalThis.localStorage = { getItem: (key) => key === 'forest_admin_auth_data' ? 'test-auth' : null };
  const controller = new AbortController();
  let request;
  globalThis.fetch = async (url, options) => { request = { url: new URL(url, 'https://example.test'), options }; return new Response(JSON.stringify({ data: { users: [], total: 0 } }), { status: 200 }); };
  await apiGet('/stat/online/users', onlineUsersParams({ title: '城市未知', scope: 'city', province_code: '440000', city: '' }, 1, 20), { signal: controller.signal, cache: 'no-store', keepEmpty: true });
  assert.equal(request.url.pathname, '/api/v1/test-admin/stat/online/users');
  assert.equal(request.url.searchParams.has('city'), true);
  assert.equal(request.url.searchParams.get('city'), '');
  assert.equal(request.url.searchParams.get('province_code'), '440000');
  assert.equal(request.url.searchParams.get('auth_data'), 'test-auth');
  assert.equal(request.options.signal, controller.signal);
  assert.equal(request.options.cache, 'no-store');
});
