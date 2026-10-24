#!/usr/bin/env node
// Optional PostgreSQL smoke test; operates only on a new in-memory PGlite DB.
// Install the pinned test dependency outside the repository, then run:
//   test_dir=$(mktemp -d)
//   npm install --prefix "$test_dir" --no-audit --no-fund @electric-sql/pglite@0.3.14
//   node scripts/test-online-analytics-sql.mjs "$test_dir/node_modules/@electric-sql/pglite/dist/index.js"
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { dirname, resolve } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const read = (file) => readFile(resolve(root, file), 'utf8')
const { PGlite } = await import(process.argv[2] ? pathToFileURL(resolve(process.argv[2])).href : '@electric-sql/pglite')
const db = new PGlite()
const now = 1792830000
let checks = 0
const checked = () => { checks += 1 }

try {
  const [installSQL, updateSQL, schemaGo, statsGo, collectorGo, cleanupGo, usersGo, refreshGo] = await Promise.all([
    read('database/install.pgsql.sql'),
    read('database/update.pgsql.sql'),
    read('go-api/internal/platform/postgres/online_location_schema.go'),
    read('go-api/internal/admin/stat_online.go'),
    read('go-api/internal/nodeapi/online_location.go'),
    read('go-api/internal/admin/cleanup.go'),
    read('go-api/internal/admin/stat_online_users.go'),
    read('go-api/internal/nodeapi/online_location_refresh.go'),
  ])
  const locationSQL = statsGo.match(/const onlineLocationsCTE = `([\s\S]*?)`/)?.[1]
  const statsSuffix = statsGo.match(/const onlineStatsQuery = onlineLocationsCTE \+ `([\s\S]*?)`/)?.[1]
  const usersSuffix = usersGo.match(/const onlineUsersQuery = onlineLocationsCTE \+ `([\s\S]*?)`/)?.[1]
  assert.ok(locationSQL && statsSuffix && usersSuffix, 'failed to extract production statistics/users SQL')
  const statsSQL = locationSQL + statsSuffix
  const usersSQL = locationSQL + usersSuffix
  const refreshSQL = refreshGo.match(/const onlineLocationRefreshQuery = `([\s\S]*?)`/)?.[1]
  assert.ok(refreshSQL, 'failed to extract production cached location refresh SQL')
  const schemaSQL = [...schemaGo.matchAll(/`([\s\S]*?)`/g)].map((match) => match[1])
  assert.equal(schemaSQL.length, 4, 'expected lock, table, city migration and index DDL')
  const upsertParts = collectorGo.match(/`(WITH incoming[\s\S]*?VALUES )`\+strings.Join\(values, ","\)\+`([\s\S]*?)`, args\.\.\./)
  assert.ok(upsertParts, 'failed to extract production collector SQL')
  const cleanupSQL = [...cleanupGo.matchAll(/`([\s\S]*?)`/g)].map((match) => match[1]).find((sql) => sql.includes('DELETE FROM v2_user_online_location'))
  assert.ok(cleanupSQL, 'failed to extract production cleanup SQL')

  // Run the actual complete install/update scripts, not duplicated test DDL.
  await db.exec(installSQL)
  await db.exec('ALTER TABLE v2_user_online_location DROP COLUMN city_name')
  await db.query('INSERT INTO v2_user(id,email,password,uuid,token,t,created_at,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$6,$6)', [900, 'legacy@example.test', 'unused-test-hash', 'legacy-user', 'legacy-token', now])
  await db.query("INSERT INTO v2_user_online_location(user_id,country_code,country_name,province_code,province_name,last_seen_at) VALUES (900,'CN','中国','440000','广东',$1)", [now])
  await db.exec(updateSQL)
  await db.exec(updateSQL)
  assert.deepEqual((await db.query('SELECT province_code,city_name,last_seen_at FROM v2_user_online_location WHERE user_id=900')).rows, [{ province_code: '440000', city_name: '', last_seen_at: now }])
  checked() // SQL update upgrades old rows without inventing a city or deleting data.
  await db.exec('ALTER TABLE v2_user_online_location DROP COLUMN city_name')
  for (let attempt = 0; attempt < 2; attempt += 1) {
    await db.transaction(async (transaction) => {
      for (const statement of schemaSQL) await transaction.exec(statement)
    })
  }
  checked()
  const columns = await db.query("SELECT column_name FROM information_schema.columns WHERE table_name = 'v2_user_online_location' ORDER BY ordinal_position")
  assert.deepEqual(columns.rows.map((row) => row.column_name).sort(), ['user_id', 'country_code', 'country_name', 'province_code', 'province_name', 'city_name', 'unknown_reason', 'last_seen_at'].sort())
  assert.deepEqual((await db.query('SELECT province_code,city_name,last_seen_at FROM v2_user_online_location WHERE user_id=900')).rows, [{ province_code: '440000', city_name: '', last_seen_at: now }])
  await db.exec('DELETE FROM v2_user WHERE id=900')
  checked()

  const activities = [now, now - 600, now - 601, now - 86400, now - 86401, now - 604800, now - 604801, 0, now, now, now, now]
  for (const [index, activity] of activities.entries()) {
    const id = index + 1
    await db.query('INSERT INTO v2_user(id,email,password,uuid,token,t,created_at,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$7)', [id, `user-${id}@example.test`, 'unused-test-hash', `fixture-user-${id}`, `fixture-token-${id}`, activity, now])
  }
  const insertLocation = async (id, at, country = 'CN', province = '440000', reason = '', city = '') => {
    await db.query('INSERT INTO v2_user_online_location(user_id,country_code,country_name,province_code,province_name,city_name,unknown_reason,last_seen_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)', [id, country, country === 'CN' ? '中国' : '', province, province ? '广东' : '', city, reason, at])
  }
  await insertLocation(1, now, 'CN', '440000', '', '深圳市')
  await insertLocation(2, now - 600)
  await insertLocation(3, now) // Recent location alone cannot make a user online.
  await insertLocation(10, now - 601)
  await insertLocation(11, now + 1)
  await insertLocation(12, now, '', '', 'database_unavailable')
  const statistics = (await db.query(statsSQL, [now - 600, now, now - 86400, now - 604800])).rows
  const summary = statistics.find((row) => row.kind === 'summary')
  assert.deepEqual([Number(summary.users), Number(summary.current_online), Number(summary.unique_24h), Number(summary.unique_7d)], [12, 6, 8, 10])
  checked()

  const users = async (scope = 'all', { country = '', province = '', city = '', reason = '', search = '', id = 0, limit = 20, offset = 0 } = {}) => {
    const result = (await db.query(usersSQL, [now - 600, now, scope, country, province, city, reason, search, id, limit, offset])).rows
    return { total: Number(result[0].total), rows: result.filter((row) => Number(row.id) > 0) }
  }
  assert.equal((await users()).total, summary.current_online)
  assert.deepEqual((await users('city', { province: '440000', city: '深圳' })).rows.map((row) => row.id), [1])
  assert.deepEqual((await users('city', { province: '440000' })).rows.map((row) => row.id), [2])
  assert.equal((await users('province', { province: '440000' })).total, 2)
  assert.equal((await users('country', { country: 'CN' })).total, 2)
  for (const reason of ['not_reported', 'location_stale', 'invalid_timestamp', 'database_unavailable']) {
    const unknown = await users('unknown', { reason })
    assert.equal(unknown.total, 1)
    assert.equal(unknown.rows[0].unknown_reason, reason)
    assert.equal(unknown.rows[0].country_code, '')
  }
  assert.equal((await users('unknown')).total, 4)
  assert.deepEqual((await users('all', { limit: 2 })).rows.map((row) => row.id), [1, 9])
  assert.deepEqual((await users('all', { limit: 2, offset: 2 })).rows.map((row) => row.id), [10, 11])
  assert.deepEqual(await users('all', { limit: 2, offset: 20 }), { total: 6, rows: [] })
  assert.deepEqual((await users('all', { search: 'USER-1@EXAMPLE.TEST' })).rows.map((row) => row.id), [1])
  assert.deepEqual((await users('all', { search: '2', id: 2 })).rows.map((row) => row.id), [2])
  assert.equal((await users('all', { search: '%' })).total, 0, 'email search is exact, not LIKE')
  assert.equal((await users('city', { province: '440000', city: "深圳' OR 1=1 --" })).total, 0)
  checked() // User lists match map totals, preserve unknown reasons and safely paginate/search.

  for (let id = 13; id <= 16; id += 1) {
    await db.query('INSERT INTO v2_user(id,email,password,uuid,token,t,created_at,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$6,$6)', [id, `user-${id}@example.test`, 'unused-test-hash', `fixture-user-${id}`, `fixture-token-${id}`, now])
  }
  await insertLocation(13, now, 'CN', '440000', '', '深圳')
  await insertLocation(14, now, 'CN', '320000', '', '深圳市')
  await insertLocation(15, now, 'US', '440000', '', '深圳市')
  await insertLocation(16, now, 'ZZ', '440000', '', '深圳市')
  assert.deepEqual((await users('city', { province: '440000', city: '深圳' })).rows.map((row) => row.id), [1, 13])
  assert.equal((await users('city', { province: '320000', city: '深圳' })).total, 1)
  assert.equal((await users('province', { province: '440000' })).total, 3)
  assert.equal((await users('unknown', { reason: 'not_found' })).total, 1)
  assert.equal((await users('country', { country: 'US' })).rows[0].city_name, '')
  await db.query("UPDATE v2_user_online_location SET city_name='unknown' WHERE user_id=2")
  assert.deepEqual((await users('city', { province: '440000' })).rows.map((row) => row.id), [2])
  await db.query("UPDATE v2_user_online_location SET province_code='bad' WHERE user_id=14")
  assert.deepEqual((await users('province')).rows.map((row) => row.id), [14])
  assert.deepEqual((await users('city')).rows.map((row) => row.id), [14])
  await db.exec('DELETE FROM v2_user WHERE id BETWEEN 13 AND 16')
  checked() // Normalized city aliases never cross provinces; invalid regions stay unknown.
  for (const [id, expiry] of [[1, now + 1], [2, 0], [3, now + 1], [9, now + 1], [10, now + 1], [11, now], [12, now - 1]]) {
    await db.query('INSERT INTO v2_runtime_kv(k,v,expire_at,created_at,updated_at) VALUES ($1,$2,$3,$4,$4)', [`ALIVE_IP_USER_${id}`, '{}', expiry, now])
  }
  const refreshCandidates = async (cursor = 0, limit = 500) => (await db.query(refreshSQL, [now - 600, now, cursor, limit])).rows.map((row) => row.id)
  assert.deepEqual(await refreshCandidates(), [9, 10])
  await db.query("UPDATE v2_user_online_location SET city_name='' WHERE user_id=2")
  assert.deepEqual(await refreshCandidates(), [2, 9, 10])
  assert.deepEqual(await refreshCandidates(2, 1), [9])
  assert.deepEqual(await refreshCandidates(9), [10])
  checked() // Cache repair only scans online users with live keys, bounded by cursor and page size.
  const regions = statistics.filter((row) => row.kind === 'region')
  assert.equal(regions.reduce((sum, row) => sum + Number(row.users), 0), 6)
  assert.equal(regions.filter((row) => row.country_code === 'CN').reduce((sum, row) => sum + Number(row.users), 0), 2)
  assert.equal(regions.find((row) => row.city_name === '深圳市')?.users, 1)
  assert.equal(regions.find((row) => row.country_code === 'CN' && row.city_name === '')?.users, 1)
  for (const reason of ['not_reported', 'location_stale', 'invalid_timestamp', 'database_unavailable']) {
    const row = regions.find((item) => item.unknown_reason === reason)
    assert.equal(row?.users, 1, `missing expected unknown bucket ${reason}`)
    assert.equal(row?.country_code, '', `stale/unknown location must not be on the map: ${reason}`)
  }
  checked()

  const upsert = async (records) => {
    const parameters = records.flat()
    const values = records.map((_, index) => `(${Array.from({ length: 8 }, (unused, field) => `$${index * 8 + field + 1}::${field === 0 ? 'integer' : field === 7 ? 'bigint' : 'text'}`).join(',')})`).join(',')
    return db.query(upsertParts[1] + values + upsertParts[2], parameters)
  }
  const location = async (id) => (await db.query('SELECT * FROM v2_user_online_location WHERE user_id=$1', [id])).rows[0]
  const record = (id, at, province = '440000', city = province === '440000' ? '深圳市' : '北京市') => [id, 'CN', '中国', province, province === '440000' ? '广东' : '北京', city, '', at]
  await db.exec('TRUNCATE v2_user_online_location')
  await upsert([record(2, now), record(1, now), record(99999, now)])
  assert.equal((await db.query('SELECT COUNT(*) AS count FROM v2_user_online_location')).rows[0].count, 2)
  checked() // Foreign unknown users are ignored and multiple records insert.
  await upsert([record(1, now, '110000')])
  assert.equal((await location(1)).province_code, '440000')
  await upsert([record(1, now - 1, '110000')])
  assert.equal((await location(1)).province_code, '440000')
  checked() // Same-time and older reports cannot replace a newer location.
  await upsert([record(1, now + 59)])
  assert.equal((await location(1)).last_seen_at, now)
  await upsert([record(1, now + 60)])
  assert.equal((await location(1)).last_seen_at, now + 60)
  checked() // Unchanged locations refresh at the 60-second boundary.
  await upsert([record(1, now + 61, '440000', '广州市')])
  assert.equal((await location(1)).city_name, '广州市')
  assert.equal((await location(1)).last_seen_at, now + 61)
  checked() // Moving cities within one province refreshes without waiting a minute.
  await upsert([record(1, now + 62, '110000')])
  assert.equal((await location(1)).last_seen_at, now + 62)
  assert.equal((await location(1)).province_code, '110000')
  checked() // A newer changed location updates without waiting a minute.
  const unknownRecord = (id, at) => [id, '', '', '', '', '', 'invalid_ip', at]
  await upsert([unknownRecord(1, now + 63)])
  await upsert([unknownRecord(1, now + 662)])
  assert.equal((await location(1)).country_code, 'CN')
  assert.equal((await location(1)).last_seen_at, now + 62)
  await upsert([unknownRecord(1, now + 663)])
  assert.equal((await location(1)).country_code, '')
  assert.equal((await location(1)).last_seen_at, now + 663)
  checked() // Failed reports cannot replace or renew a location still within 600 seconds.
  await upsert([record(1, now + 663, '440000')])
  assert.equal((await location(1)).province_code, '440000')
  await upsert([record(1, now + 663, '110000')])
  assert.equal((await location(1)).province_code, '440000')
  checked() // Same-time cache repair can improve unknown to known without changing known region.
  await upsert([record(2, now + 700, '440000', '')])
  await upsert([record(2, now + 700, '440000', '深圳市')])
  assert.equal((await location(2)).city_name, '深圳市')
  await upsert([record(2, now + 700, '440000', '')])
  assert.equal((await location(2)).city_name, '深圳市')
  assert.equal((await location(2)).last_seen_at, now + 700)
  checked() // Equal-time repair fills missing city without erasing known city or aging the evidence.
  await upsert([[2, 'CN', '中国', '', '', '', '', now + 701]])
  await upsert([record(2, now + 701, '440000', '')])
  assert.equal((await location(2)).province_code, '440000')
  assert.equal((await location(2)).city_name, '')
  await upsert([record(2, now + 701, '440000', '深圳市')])
  await upsert([record(2, now + 701, '440000', '广州市')])
  assert.equal((await location(2)).city_name, '深圳市')
  assert.equal((await location(2)).last_seen_at, now + 701)
  checked() // Same-second reports can fill a missing province and city, without replacing a known city.
  await upsert([unknownRecord(1, now + 1400)])
  await upsert([record(1, now + 1349)])
  assert.equal((await location(1)).country_code, 'CN')
  assert.equal((await location(1)).last_seen_at, now + 1349)
  await upsert([unknownRecord(1, now + 2000)])
  await upsert([record(1, now + 1899)])
  assert.equal((await location(1)).country_code, '')
  assert.equal((await location(1)).last_seen_at, now + 2000)
  checked() // A still-live cached observation can repair unknown without pretending its time is newer.
  await db.query('DELETE FROM v2_user WHERE id=$1', [2])
  assert.equal(await location(2), undefined)
  checked() // Deleting an account also deletes its summary.

  await insertLocation(7, now - 604801)
  await insertLocation(8, now - 604800)
  await db.query(cleanupSQL, [now - 604800, 100])
  assert.equal(await location(7), undefined)
  assert.ok(await location(8))
  checked() // Cleanup excludes the inclusive seven-day boundary.

  await db.exec('DELETE FROM v2_user')
  const empty = (await db.query(statsSQL, [now - 600, now, now - 86400, now - 604800])).rows
  assert.equal(empty.length, 1)
  assert.equal(empty[0].kind, 'summary')
  assert.deepEqual([empty[0].users, empty[0].current_online, empty[0].unique_24h, empty[0].unique_7d], [0, 0, 0, 0])
  checked()
  console.log(`Online analytics PostgreSQL smoke test passed (${checks} scenarios, isolated PGlite database).`)
} finally {
  await db.close()
}
