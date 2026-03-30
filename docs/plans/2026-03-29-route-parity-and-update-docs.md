# Route Parity And Update Docs Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add a full legacy-to-Go HTTP route parity regression test, update stale Go API parity docs, and verify the real update-time environment requirements for single-machine BaoTa deployment.

**Architecture:** Snapshot the legacy PHP route surface into a checked-in JSON fixture so parity tests do not depend on deleted PHP files or git history. Add a Go test that loads the fixture, expands dynamic admin and managed-server routes, and asserts the Go router never returns `404` for any legacy route. Update docs to state that HTTP route coverage is complete while calling out remaining semantic differences like queue monitor compatibility.

**Tech Stack:** Go test, JSON fixture, bash deployment scripts, Markdown docs.

---

### Task 1: Add failing full-route parity regression test

**Files:**
- Create: `go-api/internal/http/testdata/legacy_route_parity.json`
- Create: `go-api/internal/http/route_parity_test.go`

**Step 1: Write fixture and failing test for full legacy route surface**
- Capture all legacy public HTTP routes from old PHP route/controller entrypoints.
- Assert every fixture route exists in Go router and does not return `404`.
- Assert fixture includes representative admin/user/server/payment routes so the test cannot silently degrade into a partial list.

**Step 2: Run test to verify it fails if fixture/test is incomplete or route expansion is wrong**
Run: `cd go-api && go test ./internal/http -run TestLegacyRouteParity`
Expected: FAIL until fixture expansion and test logic are correct.

### Task 2: Implement minimal parity test support

**Files:**
- Modify: `go-api/internal/http/route_parity_test.go`

**Step 1: Add dynamic admin path normalization and managed-server route expansion**
- Use `localadmin` in test config.
- For POST routes, use empty bodies and accept any non-404 response.
- Keep the test focused on route existence, not business success.

**Step 2: Re-run targeted test until green**
Run: `cd go-api && go test ./internal/http -run TestLegacyRouteParity`
Expected: PASS.

### Task 3: Update stale parity documentation

**Files:**
- Modify: `docs/go-api.md`

**Step 1: Replace outdated “not full parity” wording**
- State that legacy PHP HTTP business routes now have Go route coverage.
- Call out remaining differences as semantic/runtime compatibility gaps rather than missing routes.

**Step 2: Keep notes concrete and bounded**
- Mention queue monitor compatibility as an example of semantic difference.

### Task 4: Verify update-time configuration expectations

**Files:**
- Read: `update.sh`
- Read: `scripts/appctl`
- Read: `.env.go.example`
- Read: `docs/pg-single-command.md`

**Step 1: Verify what `./update.sh` actually requires**
- Confirm whether only PostgreSQL DB credentials are required.
- Confirm whether `APP_KEY`, `ADMIN_EMAIL`, or BaoTa-specific service config are required during update.

**Step 2: Report exact operational requirements with commands**
- Provide the minimal `.env.go` keys needed on BaoTa.
- Distinguish “required for update” from “required for running service”.
