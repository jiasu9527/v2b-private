# Guest Invite Preview Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add a guest invite preview API that returns the final effective gift traffic and gift duration for a given invite code.

**Architecture:** Reuse `InviteCampaignService` as the single source of truth for invite reward calculation. The service will compute final effective invitee trial values by combining the default try-out settings with any active campaign override. The new guest endpoint only exposes the computed result and the invite code state (`campaign`, `normal`, `invalid`).

**Tech Stack:** Laravel, PHPUnit feature tests

---

### Task 1: Write failing tests

**Files:**
- Modify: `tests/Feature/InviteCampaignFlowTest.php`

**Step 1: Add three cases**

- valid invite code with active campaign returns `campaign`
- valid invite code without active campaign returns `normal`
- invalid invite code returns `invalid`

**Step 2: Verify RED**

Run:
`php -d display_errors=0 -d error_reporting=24575 vendor/bin/phpunit tests/Feature/InviteCampaignFlowTest.php --filter "guest_invite_preview"`

Expected: fail because route/controller/service do not exist yet.

### Task 2: Implement shared reward calculation

**Files:**
- Modify: `app/Services/InviteCampaignService.php`
- Modify: `app/Http/Controllers/V1/Passport/AuthController.php`

**Step 1: Add helpers**

Compute:
- default try-out transfer GB/hours
- effective transfer GB/hours for active campaign
- preview payload shape

**Step 2: Reuse helpers in registration**

Ensure preview and actual registration still use the same effective values.

### Task 3: Add guest endpoint

**Files:**
- Create: `app/Http/Controllers/V1/Guest/InviteController.php`
- Modify: `app/Http/Routes/V1/GuestRoute.php`

**Step 1: Implement `GET /api/v1/guest/invite/preview`**

Return:
- `campaign` with countdown
- `normal` with final default trial values
- `invalid` with zero/null values

### Task 4: Verify

**Files:**
- Verify touched PHP files

**Step 1: Syntax checks**

Run:
- `php -l app/Services/InviteCampaignService.php`
- `php -l app/Http/Controllers/V1/Guest/InviteController.php`
- `php -l app/Http/Routes/V1/GuestRoute.php`

**Step 2: Targeted tests**

Run:
- `php -d display_errors=0 -d error_reporting=24575 vendor/bin/phpunit tests/Feature/InviteCampaignFlowTest.php --filter "guest_invite_preview"`
