# Admin Stat Compatibility Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Finish the remaining Go admin stat compatibility endpoints so the Go router covers the legacy admin stat surface without PHP fallback.

**Architecture:** Extend the Go admin service with three legacy-compatible stat methods and wire them into the HTTP router under the existing admin auth flow. Keep the payload shape aligned with the historical Laravel behavior and remove the stale "not yet migrated" doc note once the routes are covered.

**Tech Stack:** Go 1.25, `net/http`, PostgreSQL via `database/sql`, existing Go router/admin service test patterns.

---

### Task 1: Add failing router tests for the missing admin stat compatibility endpoints

**Files:**
- Modify: `go-api/internal/http/router_test.go`
- Test: `go-api/internal/http/router_test.go`

**Step 1: Write the failing test**

Add router tests for:
- `GET /api/v1/<admin_path>/stat/getStat`
- `GET /api/v1/<admin_path>/stat/getRanking`
- `GET /api/v1/<admin_path>/stat/getStatRecord`

**Step 2: Run test to verify it fails**

Run: `cd go-api && go test ./internal/http -run 'TestRouterAdminStat(GetStat|GetRanking|GetStatRecord)Endpoint'`
Expected: FAIL because the fake admin service and router do not support these handlers yet.

**Step 3: Write minimal implementation**

Extend the fake admin service with the required methods and make the tests assert the request params and payload shape.

**Step 4: Run test to verify it passes**

Run: `cd go-api && go test ./internal/http -run 'TestRouterAdminStat(GetStat|GetRanking|GetStatRecord)Endpoint'`
Expected: PASS.

### Task 2: Implement the missing admin stat compatibility service methods and routes

**Files:**
- Modify: `go-api/internal/admin/service.go`
- Modify: `go-api/internal/admin/stat.go`
- Modify: `go-api/internal/http/router.go`

**Step 1: Write the minimal implementation**

Add admin service methods for:
- summary `getStat`
- ranking `getRanking`
- ranged stat series `getStatRecord`

Then wire the three admin routes into the router using existing admin auth/error handling.

**Step 2: Run targeted tests**

Run: `cd go-api && go test ./internal/http -run 'TestRouterAdminStat(GetStat|GetRanking|GetStatRecord)Endpoint'`
Expected: PASS.

### Task 3: Update the migration docs and verify the full Go stack

**Files:**
- Modify: `docs/go-api.md`

**Step 1: Remove the stale migration gap note**

Delete the note claiming `getStat/getRanking/getStatRecord` are not migrated.

**Step 2: Run full verification**

Run:
- `cd go-api && go test ./...`
- `cd go-api && go build ./...`

Expected: PASS.
