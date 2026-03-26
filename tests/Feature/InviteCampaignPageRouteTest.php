<?php

namespace Tests\Feature;

use Illuminate\Support\Facades\Config;
use Tests\TestCase;

class InviteCampaignPageRouteTest extends TestCase
{
    public function test_user_invite_campaign_page_can_be_opened()
    {
        $response = $this->get('/invite-campaign');

        $response->assertStatus(200);
        $response->assertSee('邀请活动任务');
    }

    public function test_admin_invite_campaign_page_can_be_opened()
    {
        $securePath = Config::get('v2board.secure_path', 'houtai8888');
        $expectedVersion = $this->expectedAdminAssetVersion();

        $response = $this->get('/' . $securePath . '/invite-campaign');

        $response->assertStatus(200);
        $response->assertSee('<div id="root"></div>', false);
        $response->assertSee('/assets/admin/umi.js?v=' . $expectedVersion, false);
        $response->assertSee("version: '" . $expectedVersion . "'", false);
        $response->assertDontSee('invite-campaign-admin-app');
    }

    public function test_admin_bundle_contains_invite_campaign_menu_entry()
    {
        $bundle = file_get_contents(public_path('assets/admin/umi.js'));

        $this->assertNotFalse($bundle);
        $this->assertStringContainsString('活动任务', $bundle);
        $this->assertStringContainsString('path: "/invite-campaign"', $bundle);
        $this->assertStringContainsString('basename: "/"', $bundle);
        $this->assertStringContainsString('Object.assign({}, this.props, {', $bundle);
        $this->assertStringContainsString('campaign-shell--admin', $bundle);
        $this->assertStringContainsString('campaign-admin-loading', $bundle);
        $this->assertStringContainsString('n.listLoaded = !0', $bundle);
        $this->assertStringContainsString('admin-campaign-save-settings', $bundle);
        $this->assertStringContainsString('invite_campaign_reward_amount', $bundle);
        $this->assertStringContainsString('本月新增付费用户', $bundle);
        $this->assertStringContainsString('上月新增用户', $bundle);
        $this->assertStringContainsString('上月新增付费用户', $bundle);
    }

    private function expectedAdminAssetVersion(): string
    {
        $files = [
            public_path('assets/admin/umi.js'),
            public_path('assets/admin/custom.js'),
            public_path('assets/invite-campaign-common.css'),
        ];
        $timestamps = array_map(function ($path) {
            return file_exists($path) ? (int) filemtime($path) : 0;
        }, $files);

        return config('app.version') . '.' . max($timestamps);
    }
}
