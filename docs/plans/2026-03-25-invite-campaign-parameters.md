# Invite Campaign Parameters Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add adjustable invite-campaign parameters for per-invite discount, invitee trial traffic, and invitee trial duration without changing database structure.

**Architecture:** Store all parameters in `config/v2board.php` and reuse the existing admin config fetch/save pipeline. Registration continues to reuse the current try-out plan for group, speed, device limit, and plan assignment, while active invite campaigns can override invitee trial traffic and duration.

**Tech Stack:** Laravel, PHPUnit feature tests, compiled admin/user JS assets

---

### Task 1: Lock behavior with failing tests

**Files:**
- Modify: `tests/Feature/InviteCampaignFlowTest.php`
- Modify: `tests/Feature/InviteCampaignPageRouteTest.php`

**Step 1: Write failing tests**

Cover:
- admin invite config includes new adjustable fields
- user invite campaign fetch includes settings payload
- active campaign registration overrides invitee traffic and duration from config

**Step 2: Run tests to verify RED**

Run:
`php -d display_errors=0 -d error_reporting=24575 vendor/bin/phpunit tests/Feature/InviteCampaignFlowTest.php --filter "invite_campaign_feature_status|campaign_specific_trial|config_fetch_includes_invite_campaign"`

Expected: fail because settings are not exposed and custom invitee trial override does not exist yet.

### Task 2: Implement backend parameter support

**Files:**
- Modify: `app/Services/InviteCampaignService.php`
- Modify: `app/Http/Controllers/V1/Passport/AuthController.php`
- Modify: `app/Http/Controllers/V1/User/InviteCampaignController.php`
- Modify: `app/Http/Controllers/V1/Admin/ConfigController.php`
- Modify: `app/Http/Requests/Admin/ConfigSave.php`
- Modify: `config/v2board.php`

**Step 1: Add config getters and settings serialization**

Expose:
- `invite_campaign_reward_amount`
- `invite_campaign_expire_hours`
- `invite_campaign_try_out_transfer_gb`
- `invite_campaign_try_out_hours`

**Step 2: Apply invitee trial overrides only for active campaign registrations**

Reuse existing try-out plan for:
- `plan_id`
- `group_id`
- `device_limit`
- `speed_limit`

Override only:
- `transfer_enable`
- `expired_at`

**Step 3: Return settings to user fetch response**

Attach a `settings` payload to `/api/v1/user/invite/campaign/fetch`.

### Task 3: Expose settings in admin and local user page

**Files:**
- Modify: `public/assets/admin/umi.js`
- Modify: `public/assets/invite-campaign-common.css`
- Modify: `public/assets/user-invite-campaign-page.js`

**Step 1: Extend admin task page config card**

Add editable inputs for:
- per-invite discount in yuan
- expire hours
- invitee trial traffic in GB
- invitee trial hours

**Step 2: Update local user page to read dynamic settings**

Replace hardcoded `10元` and `48小时` copy with values from API settings.

### Task 4: Verify

**Files:**
- Verify touched PHP and JS files only

**Step 1: Syntax checks**

Run:
- `php -l` on touched PHP files
- `node --check` on touched JS files

**Step 2: Full targeted feature verification**

Run:
- `php -d display_errors=0 -d error_reporting=24575 vendor/bin/phpunit tests/Feature/InviteCampaignFlowTest.php`
- `php -d display_errors=0 -d error_reporting=24575 vendor/bin/phpunit tests/Feature/InviteCampaignPageRouteTest.php`
