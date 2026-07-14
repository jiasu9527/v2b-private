# DNS Failover and Domestic TCP Probe Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add self-hosted domestic TCP probes and ordered DNSPod failover with A/AAAA/CNAME targets, consensus, automatic failback, and Telegram notifications.

**Architecture:** Keep decisions and DNS credentials in the Forest panel. A standalone `forest-probe` binary pulls tasks and reports TCP results over a dedicated configurable HTTPS domain. PostgreSQL stores probes, rules, targets, per-probe state, and events; the existing admin service applies DNSPod mutations and the existing Telegram queue sends deduplicated notifications.

**Tech Stack:** Go 1.x, PostgreSQL, existing Forest queue/Telegram/DNSPod clients, React + TypeScript + Ant Design, shell/systemd installer.

---

### Task 1: Failover schema and pure decision engine

**Files:**
- Create: `go-api/internal/platform/postgres/dns_failover_schema.go`
- Create: `go-api/internal/platform/postgres/dns_failover_schema_test.go`
- Create: `go-api/internal/admin/dns_failover_decision.go`
- Create: `go-api/internal/admin/dns_failover_decision_test.go`
- Modify: `go-api/internal/admin/service.go`

**Steps:**
1. Write sqlmock tests expecting creation of probe, group, target, group-probe, target-state, and event tables plus indexes.
2. Run `go test ./internal/platform/postgres -run DNSFailover -v` and confirm failure.
3. Implement idempotent schema creation and lazy `sync.Once` wiring on `admin.DBService`.
4. Write table-driven failing tests for dual-probe agreement, disagreement, single-probe degraded thresholds, all-probes-offline, ordered failover, cooldown, and automatic failback.
5. Implement a pure decision function returning `none`, `failover`, or `failback` and the selected target ID/reason.
6. Run both package tests and commit `feat: add dns failover state model`.

### Task 2: Probe and failover admin persistence APIs

**Files:**
- Create: `go-api/internal/admin/dns_failover.go`
- Create: `go-api/internal/admin/dns_failover_test.go`
- Modify: `go-api/internal/admin/service.go`

**Steps:**
1. Write failing sqlmock tests for probe creation/list/revoke, rule CRUD, target replacement with sort, probe binding, event pagination, and settings validation.
2. Implement request/response types with validation: supported DNS types, CNAME normalization, host/port limits, thresholds, unique sort, and selected DNSPod record fields.
3. Generate 32-byte probe secrets, return them once, and store SHA-256 only.
4. Ensure updates are transactional and do not silently delete active state.
5. Run `go test ./internal/admin -run DNSFailover -v` and commit `feat: manage dns failover rules and probes`.

### Task 3: Public probe protocol and result processing

**Files:**
- Create: `go-api/internal/admin/dns_probe.go`
- Create: `go-api/internal/admin/dns_probe_test.go`
- Create: `go-api/internal/http/router_probe.go`
- Create: `go-api/internal/http/router_probe_test.go`
- Modify: `go-api/internal/http/router.go`
- Modify: `go-api/internal/http/router_test.go`

**Steps:**
1. Write failing tests for Bearer authentication, heartbeat, task pull, malformed reports, replay-safe batch report, and IP extraction through trusted proxy helpers.
2. Implement constant-time token authentication and heartbeat/prewarm updates.
3. Return only enabled targets for rules bound to the probe.
4. Upsert result state and consecutive counters transactionally, then request evaluation of affected groups.
5. Add public routes under `/api/v1/probe/*`, with no admin-session requirement.
6. Run focused tests and commit `feat: add tcp probe protocol`.

### Task 4: DNSPod switch executor and Telegram notifications

**Files:**
- Create: `go-api/internal/admin/dns_failover_worker.go`
- Create: `go-api/internal/admin/dns_failover_worker_test.go`
- Modify: `go-api/internal/admin/service.go`
- Modify: `go-api/cmd/server/main.go`

**Steps:**
1. Add a small notifier interface compatible with `telegram.Service.NotifyAdmins` and inject it into `admin.DBService`.
2. Write failing tests for A-to-CNAME mutation, CNAME-to-A failback, candidate health checks, transaction lock/cooldown, DNSPod failure rollback, manual switch, event deduplication, and notification text.
3. Implement evaluation on result ingestion using a per-group queue key to serialize work.
4. Call the existing DNSPod client with the target's record type/value while preserving record ID, host, line, TTL, MX, and weight constraints.
5. Commit state only after DNSPod succeeds; record failures without changing active target.
6. Wire Telegram and queue dependencies in `cmd/server/main.go`.
7. Run focused tests and commit `feat: automate dns failover decisions`.

### Task 5: Standalone `forest-probe`

**Files:**
- Create: `go-api/cmd/probe/main.go`
- Create: `go-api/internal/probeagent/agent.go`
- Create: `go-api/internal/probeagent/agent_test.go`
- Create: `go-api/internal/probeagent/tcp.go`
- Create: `go-api/internal/probeagent/tcp_test.go`

**Steps:**
1. Write failing tests for IPv4/IPv6 TCP connect, hostname resolution with multiple addresses, timeout/error classification, task scheduling, batch reporting, heartbeat, and graceful cancellation.
2. Implement a dependency-injected TCP checker and HTTP client.
3. Implement config flags/file for API URL, token, name, interval, and version.
4. Never execute server-supplied shell commands and never accept arbitrary URL schemes.
5. Run `go test ./internal/probeagent ./cmd/probe -v` and commit `feat: add standalone forest probe`.

### Task 6: Self-hosted installer and binary distribution

**Files:**
- Create: `go-api/internal/http/router_probe_download.go`
- Create: `go-api/internal/http/router_probe_download_test.go`
- Create: `scripts/forest-probe-install.sh`
- Create: `scripts/test-forest-probe-install.sh`
- Modify: `scripts/appctl`
- Modify: affected `scripts/test-appctl-*.sh` expectations

**Steps:**
1. Write handler tests for install script rendering, architecture-specific downloads, path traversal rejection, checksums, and missing-artifact errors.
2. Write shell tests for architecture detection, SHA256 verification, config permissions `0600`, systemd unit creation, enable/start, and idempotent reinstall.
3. Add `appctl build-probe` and invoke it from normal build/update to cross-compile Linux amd64/arm64 into `storage/probe/` without committing binaries.
4. Serve only install/download endpoints and set `Content-Disposition`, `Cache-Control`, and content types.
5. Run Go and shell tests and commit `feat: self host probe installation packages`.

### Task 7: Admin HTTP routes

**Files:**
- Create: `go-api/internal/http/router_dns_failover.go`
- Create: `go-api/internal/http/router_dns_failover_test.go`
- Modify: `go-api/internal/http/router.go`
- Modify: `go-api/internal/http/router_test.go`

**Steps:**
1. Write failing admin-authenticated route tests for settings, probes, rules, targets, sorting, events, toggle, manual switch, and generated install command.
2. Implement strict JSON/form parsing and return actionable Chinese errors.
3. Never return token hashes or previously issued probe secrets.
4. Run router tests and commit `feat: expose dns failover admin api`.

### Task 8: Admin UI

**Files:**
- Create: `admin-src/src/pages/DNSFailoverPage.tsx`
- Modify: `admin-src/src/App.tsx`
- Modify: `admin-src/src/styles.css`
- Modify: `public/assets/admin-new/admin.js`
- Modify: `public/assets/admin-new/index.css`

**Steps:**
1. Add a “备用监控” menu next to “域名解析”.
2. Implement tabs for rules, probes, and events using existing page/table/modal patterns.
3. Implement rule editor with existing DNS record selection, probe multi-select, thresholds, A/AAAA/CNAME targets, per-target TCP host/port, and drag sorting.
4. Implement probe creation modal that shows the secret/install command exactly once with copy actions.
5. Display consensus/degraded/offline/current-target states and manual switch confirmation.
6. Run `npm run check`, build production assets, and commit `feat: add dns failover admin interface`.

### Task 9: End-to-end verification and documentation

**Files:**
- Modify: `docs/plans/2026-07-14-dns-failover-design.md` if implementation details changed
- Modify: deployment docs as needed for the dedicated probe domain reverse proxy

**Steps:**
1. Run `go test ./...`.
2. Run `npm run check && npm run build` in `admin-src`.
3. Run probe installer and appctl shell tests.
4. Run a local end-to-end scenario with two probe clients: disagreement does not switch; agreement switches A to CNAME; one probe offline allows degraded switch; recovery switches back to A.
5. Verify no binaries, tokens, generated configs, or storage artifacts are staged.
6. Request final spec and code-quality reviews, fix all important findings, and commit `test: verify dns failover workflow`.
