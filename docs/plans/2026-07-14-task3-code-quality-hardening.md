# Task 3 Probe Protocol Quality Hardening Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Make probe result ingestion durable, constant-round-trip, proxy-safe, index-authenticated, and initialized before the public server starts, without implementing DNSPod switching.

**Architecture:** PostgreSQL stores accepted probe state and a per-group evaluation outbox in one transaction. Result ingestion classifies persisted replay tombstones before authorizing only new targets, then uses JSONB recordsets for constant-count batch SQL. Public probe IP extraction has an isolated trusted-proxy resolver configured by explicit CIDRs; the existing global request IP behavior remains unchanged.

**Tech Stack:** Go 1.25, `database/sql`, PostgreSQL JSONB/PLpgSQL, `net/netip`, `crypto/subtle`, `sqlmock`.

---

### Task 1: Persistent evaluation outbox schema

**Files:**
- Modify: `go-api/internal/platform/postgres/dns_failover_schema.go`
- Modify: `go-api/internal/platform/postgres/dns_failover_schema_test.go`
- Modify: `go-api/internal/admin/dns_failover_decision_test.go`

1. Add failing schema tests for `v2_dns_failover_eval_outbox`, unique `group_id`, retry fields, FK, due index, and inbox `created_at` index.
2. Run `go test ./internal/platform/postgres -run DNSFailover -count=1 -v` and confirm RED.
3. Add the idempotent table, constraints, and indexes; keep the inbox FK catalog migration conditional.
4. Re-run focused schema tests and confirm GREEN.

### Task 2: Indexed probe authentication

**Files:**
- Modify: `go-api/internal/admin/dns_probe.go`
- Modify: `go-api/internal/admin/dns_probe_test.go`

1. Replace full-scan expectations with a failing unique-index query test covering valid, absent, disabled, and malformed returned hashes.
2. Query `token_hash=$1 AND enabled=1 LIMIT 1`, select a fixed 32-byte dummy on no/malformed row, and always execute one `subtle.ConstantTimeCompare`.
3. Run `go test ./internal/admin -run DNSProbeAuthenticate -count=1 -v` and confirm GREEN.

### Task 3: Probe-only trusted proxy boundary and strict HTTP

**Files:**
- Modify: `go-api/internal/config/config.go`
- Modify: `go-api/internal/config/config_test.go`
- Modify: `go-api/internal/http/router.go`
- Modify: `go-api/internal/http/router_probe.go`
- Modify: `go-api/internal/http/router_probe_test.go`

1. Add failing tests for default loopback CIDRs, configured IPv4/IPv6 CIDRs, invalid CIDR rejection, trusted/untrusted peers, spoofed CF/XFF, strict content type, 413, and `WWW-Authenticate`.
2. Restore the pre-Task-3 global `requestIP` implementation.
3. Build a probe-only resolver from validated `PROBE_TRUSTED_PROXY_CIDRS`; trust forwarding headers only when the socket peer is in a trusted prefix.
4. Re-run config and HTTP focused tests.

### Task 4: Constant-round-trip result transaction and durable wakeups

**Files:**
- Modify: `go-api/internal/admin/dns_probe.go`
- Modify: `go-api/internal/admin/dns_probe_test.go`

1. Add failing tests for duplicate-first classification, deleted/unbound replay tombstones, new unauthorized targets, duplicate target rejection, 500-result constant SQL count, batch inbox/state SQL, saturated streaks, outbox rollback, and accepted group outbox upsert.
2. Serialize request rows as JSONB. Batch-query existing result IDs first, authorize only nonduplicates, batch-insert inbox rows with `RETURNING`, and batch-upsert state rows.
3. Saturate streaks with `CASE WHEN old >= 2147483647 THEN 2147483647 ELSE old + 1 END`.
4. Upsert one outbox row per actually accepted group in the same transaction and advance prewarm once per accepted batch.
5. After commit, invoke the evaluator only as a wake hint using `context.WithoutCancel`, a short timeout, and panic recovery; errors never alter the accepted response.
6. Re-run admin focused tests.

### Task 5: Safe request time and startup schema initialization

**Files:**
- Modify: `go-api/internal/admin/dns_probe.go`
- Modify: `go-api/internal/admin/service.go`
- Modify: `go-api/cmd/server/main.go`
- Create: `go-api/cmd/server/main_test.go`

1. Add failing tests for future/negative heartbeat timestamps, cutoff comparison, one captured request time, and exported eager schema initialization marking lazy state ready.
2. Use a captured method-entry Unix timestamp and compare `last_heartbeat_at < now-offline` with overflow-safe cutoff helpers.
3. Eagerly initialize DNS failover schema after DB service construction and before HTTP server construction; startup returns/fatals on failure.
4. Keep lazy initialization only as a defensive fallback.

### Task 6: Verification and Task 9 notes

1. Run focused admin/http/postgres tests, `go test ./...`, `go vet ./...`, and race tests.
2. Run `git diff --check`, remove `go-api/internal/storage`, and verify a clean intended diff.
3. Task 4 consumes `v2_dns_failover_eval_outbox` by due `next_attempt_at`, updating attempts/backoff/error and deleting or acknowledging only after evaluation succeeds.
4. Task 9 must exercise real PostgreSQL empty-schema, legacy CASCADE, existing SET NULL, repeated initialization, and concurrent outbox consumption. It must also set `search_path=tenant,public` with shadow tables in both schemas and prove the atomic inbox migration changes only `tenant`.
5. Tombstones have no automatic deletion in Task 3. Task 9 must define retention from the maximum probe retry/replay window before any cleanup is introduced.
