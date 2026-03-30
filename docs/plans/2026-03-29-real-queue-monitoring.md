# Real Queue Monitoring Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Replace the fake admin queue compatibility responses with a real in-process Go job queue and real queue metrics.

**Architecture:** Introduce an in-memory queue runtime owned by the Go server process. The runtime exposes enqueue APIs, worker pools, rolling metrics, and queue snapshots. Admin/system monitor endpoints read directly from this runtime, and existing background email work is migrated from ad-hoc goroutines to queued jobs.

**Tech Stack:** Go, standard library concurrency primitives, existing HTTP/admin services, existing Playwright local browser checks.

---

### Task 1: Add queue runtime package

**Files:**
- Create: `go-api/internal/queue/runtime.go`
- Create: `go-api/internal/queue/runtime_test.go`

**Step 1: Write failing tests**
- Add tests for:
  - runtime reports stopped before start and running after start
  - enqueue increments queue length and workload snapshot
  - processed and failed jobs update metrics correctly
  - recent counters age out correctly enough for snapshot windows

**Step 2: Run test to verify it fails**
- Run: `cd go-api && go test ./internal/queue`
- Expected: FAIL because runtime package does not exist yet

**Step 3: Write minimal implementation**
- Add a runtime with:
  - configurable worker count
  - named queues
  - `Enqueue(queueName, jobName, fn)`
  - snapshot method returning status, workers, pending/inflight, oldest wait seconds, processed counts, failed counts
  - graceful `Start` and `Shutdown`

**Step 4: Run test to verify it passes**
- Run: `cd go-api && go test ./internal/queue`
- Expected: PASS

### Task 2: Wire monitor and admin queue endpoints to runtime

**Files:**
- Modify: `go-api/internal/http/router.go`
- Modify: `go-api/internal/admin/service.go`
- Modify: `go-api/cmd/server/main.go`
- Modify: `go-api/internal/http/router_test.go`
- Modify: `go-api/internal/admin/service_test.go`

**Step 1: Write failing tests**
- Add tests for:
  - `/monitor/api/stats` reflects queue runtime status instead of hard-coded value
  - admin queue stats/workload return runtime-backed values

**Step 2: Run tests to verify they fail**
- Run: `cd go-api && go test ./internal/http -run 'TestRouterMonitorStatsEndpoint|TestRouterAdminQueue'`
- Run: `cd go-api && go test ./internal/admin -run 'TestDBServiceGetQueueStats|TestDBServiceGetQueueWorkload'`

**Step 3: Write minimal implementation**
- Initialize runtime in `cmd/server/main.go`
- Expose it to admin service and monitor endpoint
- Map runtime snapshot fields onto the old admin payload contract

**Step 4: Run tests to verify they pass**
- Run the same targeted commands

### Task 3: Migrate background email work onto the queue

**Files:**
- Modify: `go-api/internal/admin/user.go`
- Modify: `go-api/internal/passport/service.go`
- Modify: `go-api/internal/passport/service_test.go`

**Step 1: Write failing tests**
- Add tests proving:
  - admin bulk email no longer spawns naked goroutines and enqueues one job per email
  - passport email verify enqueues an email job and still stores verification code
  - login-with-mail-link enqueues an email job

**Step 2: Run tests to verify they fail**
- Run: `cd go-api && go test ./internal/admin -run TestDBServiceSendUserMail`
- Run: `cd go-api && go test ./internal/passport -run 'Test.*Email|Test.*MailLink'`

**Step 3: Write minimal implementation**
- Replace ad-hoc goroutine usage with queue enqueue calls
- Keep API behavior unchanged: return success after queueing, log send result in existing mail log table

**Step 4: Run tests to verify they pass**
- Run the same targeted commands

### Task 4: Verify end-to-end admin behavior

**Files:**
- Reuse local browser scripts under `/tmp/pw`

**Step 1: Start server locally**
- Run: `cd /Users/anan/Documents/v2b && ./scripts/appctl run`

**Step 2: Run browser regression**
- Verify:
  - admin login still works
  - dashboard `/monitor/api/stats` is 200 and runtime-backed
  - queue page shows running status from runtime
  - triggering a real queued email task changes queue metrics while work is pending

**Step 3: Run full verification**
- Run: `cd go-api && go test ./...`
- Expected: PASS
