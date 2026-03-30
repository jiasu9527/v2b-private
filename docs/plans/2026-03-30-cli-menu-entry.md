# CLI Menu Entry Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add a human-friendly Chinese menu entry script with main/sub menus that wraps existing `scripts/appctl` commands and a few read-only utilities.

**Architecture:** Keep `scripts/appctl` as the automation-safe command surface. Add a new root-level `menu.sh` interactive wrapper that routes numbered selections to existing commands or small shell helpers for logs, env summary, and process/port inspection.

**Tech Stack:** Bash, existing `scripts/appctl`, repo shell tests.

---

### Task 1: Define menu coverage and route table

**Files:**
- Create: `menu.sh`
- Test: `scripts/test-menu-routing.sh`

**Step 1: Write the failing test**
- Assert the menu can route an installation action to `./scripts/appctl install`.
- Assert the menu can route a database migration action to `./scripts/appctl migrate-mysql`.
- Assert the menu can route a service action to `./scripts/appctl restart`.

**Step 2: Run test to verify it fails**
Run: `bash scripts/test-menu-routing.sh`
Expected: FAIL because `menu.sh` does not exist.

**Step 3: Write minimal implementation**
- Add `menu.sh` with a main loop and numbered submenus.
- Route actions through helper functions that call `scripts/appctl`.

**Step 4: Run test to verify it passes**
Run: `bash scripts/test-menu-routing.sh`
Expected: PASS.

### Task 2: Add read-only utilities to the menu

**Files:**
- Modify: `menu.sh`
- Test: `scripts/test-menu-helpers.sh`

**Step 1: Write the failing test**
- Assert menu routes env-file/status actions.
- Assert read-only helpers print/log expected shell commands when injected with test doubles.

**Step 2: Run test to verify it fails**
Run: `bash scripts/test-menu-helpers.sh`
Expected: FAIL before helper options exist.

**Step 3: Write minimal implementation**
- Add submenu entries for logs, env summary, PID/process status, and port inspection.
- Keep helpers shell-only and safe if optional binaries are missing.

**Step 4: Run test to verify it passes**
Run: `bash scripts/test-menu-helpers.sh`
Expected: PASS.

### Task 3: Verify integration surface

**Files:**
- Modify: `readme.md`
- Modify: `docs/pg-single-command.md`
- Modify: `docs/baota.md` (if menu usage is documented there)
- Test: existing shell test suites

**Step 1: Update docs minimally**
- Mention `./menu.sh` as the interactive entry for manual operations.

**Step 2: Run verification**
Run: `for t in scripts/test-menu-*.sh scripts/test-appctl-*.sh scripts/test-update-interactive.sh scripts/test-baota-docs.sh scripts/test-install-update-docs.sh; do bash "$t"; done`
Expected: PASS.

**Step 3: Commit**
```bash
git add menu.sh scripts/test-menu-routing.sh scripts/test-menu-helpers.sh readme.md docs/pg-single-command.md docs/baota.md
 git commit -m "feat: add interactive management menu"
```
