# Invite Campaign Toggle Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add a global invite-campaign enable switch that admins can toggle from the existing task management page.

**Architecture:** Reuse the existing `config/v2board.php` config pipeline for persistence. Backend reads `invite_campaign_enable` to block new campaign creation and returns the flag in existing fetch endpoints. The admin compiled page reads the config via existing admin config APIs and saves changes through the existing config save endpoint.

**Tech Stack:** Laravel, PHPUnit feature tests, compiled admin `umi.js`

---

### Task 1: Protect campaign creation behind config

**Files:**
- Modify: `tests/Feature/InviteCampaignFlowTest.php`
- Modify: `app/Services/InviteCampaignService.php`
- Modify: `app/Http/Controllers/V1/User/InviteCampaignController.php`

**Step 1: Write the failing test**

Add feature coverage for:
- admin config fetch includes `invite_campaign_enable`
- user campaign creation fails when `invite_campaign_enable=0`

**Step 2: Run test to verify it fails**

Run: `php -d display_errors=0 -d error_reporting=24575 vendor/bin/phpunit tests/Feature/InviteCampaignFlowTest.php --filter "invite_campaign_enable|create_invite_campaign_when_feature_disabled|fetch_invite_campaign_config"`

Expected: FAIL because config output and creation guard do not exist yet.

**Step 3: Write minimal implementation**

Add config key plumbing and creation guard. Expose `enabled` in user campaign fetch payload.

**Step 4: Run test to verify it passes**

Run the same PHPUnit filter and confirm PASS.

### Task 2: Surface toggle in admin task page

**Files:**
- Modify: `public/assets/admin/umi.js`

**Step 1: Write the failing behavior expectation**

Document that the page should load current switch state, render a toggle, and save via `/config/save`.

**Step 2: Implement minimal page changes**

Update the invite campaign admin module to:
- fetch `/config/fetch?key=invite`
- render current enable status
- save `invite_campaign_enable` with `/config/save`

**Step 3: Verify**

Run PHP syntax checks for touched PHP files and inspect the updated `umi.js` for syntax consistency via targeted searches.
