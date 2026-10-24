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
  const [installSQL, updateSQL, schemaGo, statsGo, collectorGo, cleanupGo] = await Promise.all([
    read('database/install.pgsql.sql'),
    read('database/update.pgsql.sql'),
    read('go-api/internal/platform/postgres/online_location_schema.go'),
    read('go-api/internal/admin/stat_online.go'),
    read('go-api/internal/nodeapi/online_location.go'),
    read('go-api/internal/admin/cleanup.go'),
  ])
  const statsSQL = statsGo.match(/const onlineStatsQuery = `([\s\S]*?)`/)?.[1]
  assert.ok(statsSQL, 'failed to extract production statistics SQL')
  const schemaSQL = [...schemaGo.matchAll(/`([\s\S]*?)`/g)].map((match) => match[1])
  assert.equal(schemaSQL.length, 3, 'expected lock, table and index DDL')
  const upsertParts = collectorGo.match(/`(WITH incoming[\s\S]*?VALUES )`\+strings.Join\(values, ","\)\+`([\s\S]*?)`, args\.\.\./)
  assert.ok(upsertParts, 'failed to extract production collector SQL')
  const cleanupSQL = [...cleanupGo.matchAll(/`([\s\S]*?)`/g)].map((match) => match[1]).find((sql) => sql.includes('DELETE FROM v2_user_online_location'))
  assert.ok(cleanupSQL, 'failed to extract production cleanup SQL')

  // Run the actual complete install/update scripts, not duplicated test DDL.
  await db.exec(installSQL)
  await db.exec(updateSQL)
  await db.exec(updateSQL)
  for (let attempt = 0; attempt < 2; attempt += 1) {
    await db.transaction(async (transaction) => {
      for (const statement of schemaSQL) await transaction.exec(statement)
    })
  }
  checked()
  const columns = await db.query("SELECT column_name FROM information_schema.columns WHERE table_name = 'v2_user_online_location' ORDER BY ordinal_position")
  assert.deepEqual(columns.rows.map((row) => row.column_name), ['user_id', 'country_code', 'country_name', 'province_code', 'province_name', 'unknown_reason', 'last_seen_at'])
  checked()

  const activities = [now, now - 600, now - 601, now - 86400, now - 86401, now - 604800, now - 604801, 0, now, now, now, now]
  for (const [index, activity] of activities.entries()) {
    const id = index + 1
    await db.query('INSERT INTO v2_user(id,email,password,uuid,token,t,created_at,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$7)', [id, `user-${id}@example.test`, 'unused-test-hash', `fixture-user-${id}`, `fixture-token-${id}`, activity, now])
  }
  const insertLocation = async (id, at, country = 'CN', province = '440000', reason = '') => {
    await db.query('INSERT INTO v2_user_online_location(user_id,country_code,country_name,province_code,province_name,unknown_reason,last_seen_at) VALUES ($1,$2,$3,$4,$5,$6,$7)', [id, country, country === 'CN' ? '中国' : '', province, province ? '广东' : '', reason, at])
  }
  await insertLocation(1, now)
  await insertLocation(2, now - 600)
  await insertLocation(3, now) // Recent location alone cannot make a user online.
  await insertLocation(10, now - 601)
  await insertLocation(11, now + 1)
  await insertLocation(12, now, '', '', 'database_unavailable')
  const statistics = (await db.query(statsSQL, [now - 600, now - 86400, now - 604800, now])).rows
  const summary = statistics.find((row) => row.kind === 'summary')
  assert.deepEqual([Number(summary.users), Number(summary.current_online), Number(summary.unique_24h), Number(summary.unique_7d)], [12, 6, 8, 10])
  checked()
  const regions = statistics.filter((row) => row.kind === 'region')
  assert.equal(regions.reduce((sum, row) => sum + Number(row.users), 0), 6)
  assert.equal(regions.find((row) => row.country_code === 'CN')?.users, 2)
  for (const reason of ['not_reported', 'location_stale', 'invalid_timestamp', 'database_unavailable']) {
    const row = regions.find((item) => item.unknown_reason === reason)
    assert.equal(row?.users, 1, `missing expected unknown bucket ${reason}`)
    assert.equal(row?.country_code, '', `stale/unknown location must not be on the map: ${reason}`)
  }
  checked()

  const upsert = async (records) => {
    const parameters = records.flat()
    const values = records.map((_, index) => `(${Array.from({ length: 7 }, (unused, field) => `$${index * 7 + field + 1}::${field === 0 ? 'integer' : field === 6 ? 'bigint' : 'text'}`).join(',')})`).join(',')
    return db.query(upsertParts[1] + values + upsertParts[2], parameters)
  }
  const location = async (id) => (await db.query('SELECT * FROM v2_user_online_location WHERE user_id=$1', [id])).rows[0]
  const record = (id, at, province = '440000') => [id, 'CN', '中国', province, province === '440000' ? '广东' : '北京', '', at]
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
  await upsert([record(1, now + 61, '110000')])
  assert.equal((await location(1)).last_seen_at, now + 61)
  assert.equal((await location(1)).province_code, '110000')
  checked() // A newer changed location updates without waiting a minute.
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
  const empty = (await db.query(statsSQL, [now - 600, now - 86400, now - 604800, now])).rows
  assert.equal(empty.length, 1)
  assert.equal(empty[0].kind, 'summary')
  assert.deepEqual([empty[0].users, empty[0].current_online, empty[0].unique_24h, empty[0].unique_7d], [0, 0, 0, 0])
  checked()
  console.log(`Online analytics PostgreSQL smoke test passed (${checks} scenarios, isolated PGlite database).`)
} finally {
  await db.close()
}
