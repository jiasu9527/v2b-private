# Legacy Install Entry Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add a one-command legacy install flow that accepts an old PHP project path, imports the old `.env` and optional config files, migrates MySQL data into PostgreSQL, and then builds the Go server.

**Architecture:** Keep `scripts/appctl` as the only automation surface. Add `install-legacy` as a thin wrapper around existing `migrate-config`, PostgreSQL prompting, legacy MySQL migration, and build helpers. Make `init.sh` delegate to `install-legacy` when a legacy path argument is provided.

**Tech Stack:** Bash, existing `scripts/appctl`, Go ops commands, shell regression tests, Markdown docs.

---

### Task 1: Add failing shell coverage for the new entry

**Files:**
- Create: `scripts/test-appctl-install-legacy.sh`
- Create: `scripts/test-init-legacy.sh`

**Step 1: Write failing tests**
- Assert `install-legacy <legacy-root>` copies shared values from `legacy/.env` into `.env.go`.
- Assert it routes config migration through `go run ./cmd/ops migrate-config --legacy-root ...`.
- Assert it routes MySQL import through `go run ./cmd/ops migrate-mysql --source-env ...`.
- Assert `init.sh <legacy-root>` delegates to `scripts/appctl install-legacy <legacy-root>`.

**Step 2: Run tests to verify they fail**
Run: `bash scripts/test-appctl-install-legacy.sh && bash scripts/test-init-legacy.sh`
Expected: FAIL before production code changes.

### Task 2: Implement the minimal install-legacy flow

**Files:**
- Modify: `scripts/appctl`
- Modify: `init.sh`

**Step 1: Add path resolution and legacy env helpers**
- Accept either a legacy project directory or a direct legacy `.env` path.
- Reuse current env-copy logic by allowing a specific source env path.

**Step 2: Add `cmd_install_legacy`**
- Initialize `.env.go` when missing.
- Copy shared env values from the legacy `.env`.
- Prompt for PostgreSQL config when missing.
- Run config migration against the provided legacy root.
- Run MySQL -> PostgreSQL migration using the provided legacy `.env`.
- Build the Go binary.

**Step 3: Wire `init.sh`**
- If an argument is provided, delegate to `install-legacy`.
- Otherwise keep default `install` behavior.

### Task 3: Update docs and verify shell regression suite

**Files:**
- Modify: `readme.md`
- Modify: `docs/install.md`
- Modify: `docs/update.md`
- Modify: `docs/pg-single-command.md`
- Modify: `docs/baota-go-single-machine.md`

**Step 1: Document the new entry**
- Show `./init.sh /path/to/legacy-v2board` and `./scripts/appctl install-legacy /path/to/legacy-v2board`.
- State exactly which legacy files are required vs optional.

**Step 2: Run regression tests**
Run: `bash scripts/test-appctl-install-legacy.sh && bash scripts/test-init-legacy.sh && for t in scripts/test-menu-*.sh scripts/test-appctl-*.sh scripts/test-update-interactive.sh scripts/test-baota-docs.sh scripts/test-install-update-docs.sh; do bash "$t"; done`
Expected: PASS.
