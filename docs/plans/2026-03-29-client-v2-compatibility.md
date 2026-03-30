# Client And V2 Compatibility Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Close the remaining Go runtime compatibility gaps for client subscription/app endpoints and the v2 node config endpoint so the runtime path is Go-only.

**Architecture:** Extend the HTTP router with dedicated client/v2 handlers, resolve client subscribe tokens inside Go, reuse existing user/node services for server lookup and server lists, and generate minimal compatible text/YAML payloads from normalized server data. Keep scope to behavior already exercised by the frontend and common clients.

**Tech Stack:** Go HTTP handlers, existing `user` and `nodeapi` services, YAML parsing/emission for Clash app config, router tests, local `appctl` smoke verification.

---

### Task 1: Lock Failing Router Tests

**Files:**
- Modify: `go-api/internal/http/router_test.go`
- Modify: `go-api/internal/http/router_server_fetch_test.go`

**Step 1: Write the failing test**
- Add router tests for:
  - `GET /api/v1/client/app/getVersion`
  - `GET /api/v1/client/app/getConfig`
  - `GET /api/v1/client/subscribe`
  - `GET /api/v2/server/config`

**Step 2: Run test to verify it fails**
- Run: `go test ./internal/http -run 'TestRouter(ClientApp|ClientSubscribe|ServerV2)'`
- Expected: FAIL because routes/handlers are missing.

**Step 3: Write minimal implementation**
- Add handlers and wiring only for behavior asserted by tests.

**Step 4: Run test to verify it passes**
- Run the same focused command until green.

### Task 2: Add Client Token Resolution And Config Fields

**Files:**
- Modify: `go-api/internal/config/config.go`
- Modify: `go-api/internal/user/service.go`
- Create or modify: `go-api/internal/http/router_client.go`

**Step 1: Write the failing test**
- Cover direct token, one-time token, and time-window token resolution via HTTP route tests.
- Cover app version payload shape from config.

**Step 2: Run test to verify it fails**
- Run focused router tests.

**Step 3: Write minimal implementation**
- Add config fields for app versions/download URLs and v2 node traffic thresholds.
- Add DB-backed token resolution helper in `user` service.
- Add app version response logic.

**Step 4: Run test to verify it passes**
- Re-run focused tests.

### Task 3: Generate Client Config And Subscribe Payloads

**Files:**
- Create or modify: `go-api/internal/http/router_client.go`
- Create: `go-api/internal/http/router_client_subscribe.go`
- Modify: `go-api/go.mod` if YAML package is needed

**Step 1: Write the failing test**
- Assert Clash app config includes generated proxies.
- Assert subscribe output returns base64 text and expected headers.

**Step 2: Run test to verify it fails**
- Run focused router tests.

**Step 3: Write minimal implementation**
- Reuse normalized `user.Servers(...)` output.
- Support common outputs first: general/base64, Clash app config, version payload.

**Step 4: Run test to verify it passes**
- Re-run focused tests.

### Task 4: Verify End To End

**Files:**
- No source changes required if green.

**Step 1: Run package tests**
- Run: `go test ./internal/http ./internal/user ./internal/nodeapi`

**Step 2: Run full suite**
- Run: `go test ./...`

**Step 3: Smoke test locally**
- Run foreground server: `./scripts/appctl run`
- Verify the new routes with `curl`.
