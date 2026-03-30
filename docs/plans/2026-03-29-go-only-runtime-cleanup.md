# Go-Only Runtime Cleanup Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Remove the remaining PHP/Laravel/Webman runtime and repository dependencies so the project runs and updates as a Go + PostgreSQL application only.

**Architecture:** Replace legacy PHP-backed config persistence with Go-native JSON config files, add upgrade-time migration from legacy PHP config files into JSON, then delete the old PHP runtime tree and stale SQL/runtime assets. Keep the compiled frontend assets and PostgreSQL deployment flow intact.

**Tech Stack:** Go, JSON config files, shell scripts, Go tests, shell regression tests.

---

### Task 1: Add failing Go-only config storage regression tests

**Files:**
- Modify: `go-api/internal/admin/config_test.go`
- Modify: `go-api/internal/admin/theme_test.go`
- Modify: `go-api/internal/config/config_test.go`

**Step 1:** Write tests that expect JSON-backed admin/theme config storage and runtime config loading.

**Step 2:** Run targeted tests and verify they fail because the implementation still reads PHP config files.

### Task 2: Implement JSON-backed admin/theme/runtime config with legacy import

**Files:**
- Modify: `go-api/internal/admin/config.go`
- Modify: `go-api/internal/admin/theme.go`
- Modify: `go-api/internal/config/config.go`
- Modify: `go-api/cmd/ops/main.go`

**Step 1:** Add JSON config store helpers.

**Step 2:** Load JSON config first, fall back to legacy PHP config only for import/upgrade compatibility.

**Step 3:** Save admin and theme config to JSON files.

**Step 4:** Add ops command to import legacy PHP config/theme files into JSON during update/install.

### Task 3: Wire migration into install/update scripts and clean runtime hints

**Files:**
- Modify: `scripts/appctl`
- Modify: `init.sh`
- Modify: `update.sh`
- Modify: `.gitignore`
- Modify: `docs/go-api.md`
- Modify: `readme.md`
- Modify: `docs/pg-single-command.md`

**Step 1:** Invoke legacy-config migration before schema update/build.

**Step 2:** Remove or rename remaining PHP/Webman wording from user-facing runtime docs/scripts.

**Step 3:** Update docs to JSON config + Go-only runtime wording.

### Task 4: Delete old PHP runtime tree and stale assets

**Files:**
- Delete: `app/`
- Delete: `artisan`
- Delete: `bootstrap/`
- Delete: `composer.json`
- Delete: `composer.lock`
- Delete: `config/*.php`
- Delete: `config/theme/default.php`
- Delete: `database/install.sql`
- Delete: `database/update.sql`
- Delete: `library/`
- Delete: `public/index.php`
- Delete: `public/theme/default/dashboard.blade.php`
- Delete: `routes/`
- Delete: `tests/` legacy PHP test tree
- Delete: `vendor/`

**Step 1:** Delete only after Go config migration and docs/scripts are updated.

**Step 2:** Keep `public/assets` and `public/theme/*/config.json` intact for frontend serving.

### Task 5: Run full verification

**Files:**
- Verify: `go-api/...`
- Verify: `scripts/...`

**Step 1:** Run targeted red/green tests for config migration.

**Step 2:** Run `go test ./...` and `go build ./...`.

**Step 3:** Run shell regression scripts for appctl and legacy runtime hint cleanup.
