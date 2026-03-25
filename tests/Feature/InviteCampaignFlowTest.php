<?php

namespace Tests\Feature;

use App\Models\InviteCampaign;
use App\Models\InviteCampaignRecord;
use App\Models\InviteCode;
use App\Models\Order;
use App\Models\Payment;
use App\Models\Plan;
use App\Models\ServerVmess;
use App\Models\User;
use App\Services\AuthService;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\Cache;
use Illuminate\Support\Facades\Config;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Facades\Schema;
use Tests\TestCase;

class InviteCampaignFlowTest extends TestCase
{
    private string $sqlitePath;

    protected function setUp(): void
    {
        parent::setUp();

        $this->useSqliteDatabase();
        $this->createBaseSchema();
        $this->seedBaseConfig();
        Cache::flush();
    }

    protected function tearDown(): void
    {
        DB::disconnect('sqlite');
        if (isset($this->sqlitePath) && file_exists($this->sqlitePath)) {
            unlink($this->sqlitePath);
        }

        parent::tearDown();
    }

    public function test_user_can_create_active_invite_campaign()
    {
        $user = $this->createUser();
        $inviteCode = $this->createInviteCode($user, [
            'code' => 'LONGCODE',
        ]);
        $plan = $this->createPlan([
            'month_price' => 9800,
        ]);

        $response = $this->withHeaders($this->userHeaders($user))
            ->postJson('/api/v1/user/invite/campaign/save', [
                'plan_id' => $plan->id,
                'period' => 'month_price',
            ]);

        $response->assertOk()
            ->assertJsonPath('data.plan_id', $plan->id)
            ->assertJsonPath('data.period', 'month_price')
            ->assertJsonPath('data.target_amount', 9800)
            ->assertJsonPath('data.current_amount', 0)
            ->assertJsonPath('data.reward_amount', 1000)
            ->assertJsonPath('data.status', InviteCampaign::STATUS_ACTIVE);

        $campaign = InviteCampaign::first();
        $this->assertNotNull($campaign);
        $this->assertSame($user->id, $campaign->user_id);
        $this->assertSame($plan->id, $campaign->plan_id);
        $this->assertSame('month_price', $campaign->period);
        $this->assertSame(9800, $campaign->target_amount);
        $this->assertSame(1000, $campaign->reward_amount);
        $this->assertSame(0, $campaign->current_amount);
        $this->assertSame(InviteCampaign::STATUS_ACTIVE, $campaign->status);
        $this->assertSame(48 * 3600, $campaign->expired_at - $campaign->started_at);
        $this->assertSame($inviteCode->id, $campaign->invite_code_id);
        $this->assertSame($inviteCode->code, $campaign->invite_code);
        $this->assertSame(1, InviteCode::count());
    }

    public function test_user_cannot_create_second_current_campaign()
    {
        $user = $this->createUser();
        $plan = $this->createPlan([
            'month_price' => 9800,
        ]);

        $this->withHeaders($this->userHeaders($user))
            ->postJson('/api/v1/user/invite/campaign/save', [
                'plan_id' => $plan->id,
                'period' => 'month_price',
            ])
            ->assertOk();

        $this->withHeaders($this->userHeaders($user))
            ->postJson('/api/v1/user/invite/campaign/save', [
                'plan_id' => $plan->id,
                'period' => 'month_price',
            ])
            ->assertStatus(500);

        $this->assertSame(1, InviteCampaign::count());
    }

    public function test_registration_with_bound_normal_invite_code_increases_campaign_progress_and_grants_try_out()
    {
        $inviter = $this->createUser();
        $inviteCode = $this->createInviteCode($inviter, [
            'code' => 'NORMAL001',
        ]);
        $campaignPlan = $this->createPlan([
            'month_price' => 9800,
        ]);
        $trialPlan = $this->createPlan([
            'name' => 'Trial',
            'month_price' => 1000,
            'transfer_enable' => 10,
            'device_limit' => 1,
            'group_id' => 2,
            'speed_limit' => 10,
        ]);

        Config::set('v2board.try_out_plan_id', $trialPlan->id);
        Config::set('v2board.try_out_hour', 24);

        $this->withHeaders($this->userHeaders($inviter))
            ->postJson('/api/v1/user/invite/campaign/save', [
                'plan_id' => $campaignPlan->id,
                'period' => 'month_price',
            ])
            ->assertOk();

        $campaign = InviteCampaign::first();
        $this->assertSame($inviteCode->code, $campaign->invite_code);

        $response = $this->withHeaders($this->guestHeaders())
            ->postJson('/api/v1/passport/auth/register', [
                'email' => 'invitee@example.com',
                'password' => 'password123',
                'invite_code' => $inviteCode->code,
            ]);

        $response->assertOk();

        $campaign->refresh();
        $invitee = User::where('email', 'invitee@example.com')->first();

        $this->assertNotNull($invitee);
        $this->assertSame($inviter->id, $invitee->invite_user_id);
        $this->assertSame(1000, $campaign->current_amount);
        $this->assertSame(1, $campaign->invite_count);
        $this->assertSame(InviteCampaign::STATUS_ACTIVE, $campaign->status);
        $this->assertDatabaseHas('v2_invite_campaign_record', [
            'campaign_id' => $campaign->id,
            'invitee_user_id' => $invitee->id,
            'reward_amount' => 1000,
        ]);
        $this->assertSame($trialPlan->id, $invitee->plan_id);
        $this->assertSame($trialPlan->group_id, $invitee->group_id);
        $this->assertSame($trialPlan->device_limit, $invitee->device_limit);
        $this->assertSame($trialPlan->speed_limit, $invitee->speed_limit);
        $this->assertGreaterThan(time(), $invitee->expired_at);
    }

    public function test_expired_campaign_code_does_not_increase_campaign_progress()
    {
        $inviter = $this->createUser();
        $inviteCode = $this->createInviteCode($inviter, [
            'code' => 'NORMAL002',
        ]);
        $plan = $this->createPlan([
            'month_price' => 9800,
        ]);

        $this->withHeaders($this->userHeaders($inviter))
            ->postJson('/api/v1/user/invite/campaign/save', [
                'plan_id' => $plan->id,
                'period' => 'month_price',
            ])
            ->assertOk();

        $campaign = InviteCampaign::first();
        $campaign->expired_at = time() - 10;
        $campaign->save();

        $response = $this->withHeaders($this->guestHeaders())
            ->postJson('/api/v1/passport/auth/register', [
                'email' => 'late-invitee@example.com',
                'password' => 'password123',
                'invite_code' => $inviteCode->code,
            ]);

        $response->assertOk();

        $campaign->refresh();
        $invitee = User::where('email', 'late-invitee@example.com')->first();

        $this->assertNotNull($invitee);
        $this->assertSame($inviter->id, $invitee->invite_user_id);
        $this->assertSame(0, $campaign->current_amount);
        $this->assertSame(0, $campaign->invite_count);
        $this->assertSame(InviteCampaign::STATUS_EXPIRED, $campaign->status);
        $this->assertSame(0, InviteCampaignRecord::count());
    }

    public function test_campaign_is_completed_when_progress_reaches_target()
    {
        $inviter = $this->createUser();
        $inviteCode = $this->createInviteCode($inviter, [
            'code' => 'NORMAL003',
        ]);
        $plan = $this->createPlan([
            'month_price' => 1000,
        ]);

        $this->withHeaders($this->userHeaders($inviter))
            ->postJson('/api/v1/user/invite/campaign/save', [
                'plan_id' => $plan->id,
                'period' => 'month_price',
            ])
            ->assertOk();

        $campaign = InviteCampaign::first();

        $this->withHeaders($this->guestHeaders())
            ->postJson('/api/v1/passport/auth/register', [
                'email' => 'done@example.com',
                'password' => 'password123',
                'invite_code' => $inviteCode->code,
            ])
            ->assertOk();

        $campaign->refresh();

        $this->assertSame(1000, $campaign->current_amount);
        $this->assertSame(InviteCampaign::STATUS_COMPLETED, $campaign->status);
        $this->assertSame(0, InviteCode::where('code', $campaign->invite_code)->value('status'));
    }

    public function test_registration_with_other_normal_invite_code_keeps_commission_but_does_not_increase_bound_campaign()
    {
        $inviter = $this->createUser();
        $boundInviteCode = $this->createInviteCode($inviter, [
            'code' => 'BOUND001',
        ]);
        $otherInviteCode = $this->createInviteCode($inviter, [
            'code' => 'OTHER001',
        ]);
        $plan = $this->createPlan([
            'month_price' => 9800,
        ]);

        $this->createCampaign($inviter, $plan, [
            'invite_code_id' => $boundInviteCode->id,
            'invite_code' => $boundInviteCode->code,
        ]);

        $campaign = InviteCampaign::first();

        $this->withHeaders($this->guestHeaders())
            ->postJson('/api/v1/passport/auth/register', [
                'email' => 'other-code@example.com',
                'password' => 'password123',
                'invite_code' => $otherInviteCode->code,
            ])
            ->assertOk();

        $campaign->refresh();
        $invitee = User::where('email', 'other-code@example.com')->first();

        $this->assertNotNull($invitee);
        $this->assertSame($inviter->id, $invitee->invite_user_id);
        $this->assertSame(0, $campaign->current_amount);
        $this->assertSame(0, $campaign->invite_count);
        $this->assertSame(0, InviteCampaignRecord::count());
    }

    public function test_matching_campaign_discount_is_applied_to_order()
    {
        $user = $this->createUser();
        $plan = $this->createPlan([
            'month_price' => 9800,
        ]);
        $campaign = $this->createCampaign($user, $plan, [
            'period' => 'month_price',
            'current_amount' => 3000,
            'status' => InviteCampaign::STATUS_ACTIVE,
        ]);

        $response = $this->withHeaders($this->userHeaders($user))
            ->postJson('/api/v1/user/order/save', [
                'plan_id' => $plan->id,
                'period' => 'month_price',
            ]);

        $response->assertOk();

        $order = Order::where('trade_no', $response->json('data'))->first();

        $this->assertNotNull($order);
        $this->assertSame($campaign->id, $order->invite_campaign_id);
        $this->assertSame(3000, $order->invite_campaign_discount_amount);
        $this->assertSame(6800, $order->total_amount);
    }

    public function test_campaign_discount_is_capped_by_order_total()
    {
        $user = $this->createUser();
        $plan = $this->createPlan([
            'month_price' => 9800,
        ]);

        $this->createCampaign($user, $plan, [
            'period' => 'month_price',
            'current_amount' => 20000,
            'status' => InviteCampaign::STATUS_COMPLETED,
        ]);

        $response = $this->withHeaders($this->userHeaders($user))
            ->postJson('/api/v1/user/order/save', [
                'plan_id' => $plan->id,
                'period' => 'month_price',
            ]);

        $response->assertOk();

        $order = Order::where('trade_no', $response->json('data'))->first();

        $this->assertSame(9800, $order->invite_campaign_discount_amount);
        $this->assertSame(0, $order->total_amount);
    }

    public function test_non_matching_campaign_is_not_applied_to_order()
    {
        $user = $this->createUser();
        $plan = $this->createPlan([
            'month_price' => 9800,
            'quarter_price' => 26000,
        ]);

        $this->createCampaign($user, $plan, [
            'period' => 'month_price',
            'current_amount' => 3000,
            'status' => InviteCampaign::STATUS_ACTIVE,
        ]);

        $response = $this->withHeaders($this->userHeaders($user))
            ->postJson('/api/v1/user/order/save', [
                'plan_id' => $plan->id,
                'period' => 'quarter_price',
            ]);

        $response->assertOk();

        $order = Order::where('trade_no', $response->json('data'))->first();

        $this->assertNull($order->invite_campaign_id);
        $this->assertSame(0, $order->invite_campaign_discount_amount);
        $this->assertSame(26000, $order->total_amount);
    }

    public function test_user_can_abandon_current_campaign()
    {
        $user = $this->createUser();
        $plan = $this->createPlan([
            'month_price' => 9800,
        ]);
        $campaign = $this->createCampaign($user, $plan, [
            'period' => 'month_price',
            'current_amount' => 2000,
            'status' => InviteCampaign::STATUS_ACTIVE,
        ]);

        $this->withHeaders($this->userHeaders($user))
            ->postJson('/api/v1/user/invite/campaign/abandon')
            ->assertOk()
            ->assertJsonPath('data', true);

        $campaign->refresh();
        $this->assertSame(InviteCampaign::STATUS_ABANDONED, $campaign->status);
        $this->assertSame(0, InviteCode::where('code', $campaign->invite_code)->value('status'));
    }

    public function test_local_dev_checkout_marks_order_paid_and_activates_subscription()
    {
        $user = $this->createUser();
        $plan = $this->createPlan([
            'month_price' => 9800,
            'group_id' => 1,
            'transfer_enable' => 100,
            'device_limit' => 2,
            'speed_limit' => 100,
        ]);
        $payment = $this->createPayment([
            'payment' => 'LocalDev',
            'name' => 'Local Dev',
        ]);

        $orderResponse = $this->withHeaders($this->userHeaders($user))
            ->postJson('/api/v1/user/order/save', [
                'plan_id' => $plan->id,
                'period' => 'month_price',
            ]);

        $orderResponse->assertOk();
        $tradeNo = $orderResponse->json('data');

        $checkoutResponse = $this->withHeaders($this->userHeaders($user))
            ->postJson('/api/v1/user/order/checkout', [
                'trade_no' => $tradeNo,
                'method' => $payment->id,
            ]);

        $checkoutResponse->assertOk()
            ->assertJsonPath('type', 1);
        $this->assertStringContainsString('/#/order/' . $tradeNo, $checkoutResponse->json('data'));

        $order = Order::where('trade_no', $tradeNo)->first();
        $user->refresh();

        $this->assertNotNull($order);
        $this->assertSame(3, $order->status);
        $this->assertNotNull($order->paid_at);
        $this->assertSame($plan->id, $user->plan_id);
        $this->assertSame($plan->group_id, $user->group_id);
        $this->assertSame($plan->device_limit, $user->device_limit);
        $this->assertSame($plan->speed_limit, $user->speed_limit);
        $this->assertGreaterThan(time(), $user->expired_at);
    }

    public function test_active_user_can_fetch_vmess_server()
    {
        $user = $this->createUser([
            'plan_id' => 1,
            'group_id' => 1,
            'transfer_enable' => 100 * 1073741824,
            'device_limit' => 2,
            'speed_limit' => 100,
            'expired_at' => time() + 86400,
        ]);
        $server = $this->createVmessServer([
            'group_id' => [1],
        ]);

        $response = $this->withHeaders($this->userHeaders($user))
            ->getJson('/api/v1/user/server/fetch');

        $response->assertOk()
            ->assertJsonCount(1, 'data')
            ->assertJsonPath('data.0.id', $server->id)
            ->assertJsonPath('data.0.type', 'vmess')
            ->assertJsonPath('data.0.host', '127.0.0.1');
    }

    public function test_admin_can_fetch_invite_campaign_list()
    {
        $admin = $this->createUser([
            'email' => 'admin@example.com',
            'is_admin' => 1,
        ]);
        $inviter = $this->createUser([
            'email' => 'inviter@example.com',
        ]);
        $plan = $this->createPlan([
            'name' => 'Target Plan',
            'month_price' => 9800,
        ]);
        $campaign = $this->createCampaign($inviter, $plan, [
            'current_amount' => 1000,
        ]);

        $response = $this->withHeaders($this->adminHeaders($admin))
            ->getJson($this->adminApiPath('/invite/campaign/fetch?current=1&pageSize=10'));

        $response->assertOk()
            ->assertJsonPath('total', 1)
            ->assertJsonPath('data.0.id', $campaign->id)
            ->assertJsonPath('data.0.user_email', $inviter->email)
            ->assertJsonPath('data.0.plan_name', $plan->name)
            ->assertJsonPath('data.0.current_amount', 1000);
    }

    public function test_admin_can_fetch_invite_campaign_detail()
    {
        $admin = $this->createUser([
            'email' => 'admin-detail@example.com',
            'is_admin' => 1,
        ]);
        $inviter = $this->createUser([
            'email' => 'detail-inviter@example.com',
        ]);
        $plan = $this->createPlan([
            'name' => 'Detail Plan',
            'month_price' => 9800,
        ]);
        $campaign = $this->createCampaign($inviter, $plan, [
            'status' => InviteCampaign::STATUS_USED,
            'used_at' => time(),
            'current_amount' => 2000,
        ]);
        $order = $this->createOrder([
            'user_id' => $inviter->id,
            'plan_id' => $plan->id,
            'period' => 'month_price',
            'trade_no' => 'used-order-001',
            'status' => 3,
            'invite_campaign_id' => $campaign->id,
            'invite_campaign_discount_amount' => 2000,
            'total_amount' => 7800,
        ]);

        $response = $this->withHeaders($this->adminHeaders($admin))
            ->postJson($this->adminApiPath('/invite/campaign/detail'), [
                'id' => $campaign->id,
            ]);

        $response->assertOk()
            ->assertJsonPath('data.id', $campaign->id)
            ->assertJsonPath('data.user.email', $inviter->email)
            ->assertJsonPath('data.plan.name', $plan->name)
            ->assertJsonPath('data.used_order.trade_no', $order->trade_no)
            ->assertJsonPath('data.used_order.invite_campaign_discount_amount', 2000);
    }

    public function test_admin_can_fetch_invite_campaign_records()
    {
        $admin = $this->createUser([
            'email' => 'admin-records@example.com',
            'is_admin' => 1,
        ]);
        $inviter = $this->createUser([
            'email' => 'records-inviter@example.com',
        ]);
        $plan = $this->createPlan([
            'month_price' => 9800,
        ]);
        $campaign = $this->createCampaign($inviter, $plan);
        $invitee = $this->createUser([
            'email' => 'records-invitee@example.com',
        ]);

        InviteCampaignRecord::create([
            'campaign_id' => $campaign->id,
            'invitee_user_id' => $invitee->id,
            'invite_code' => $campaign->invite_code,
            'reward_amount' => 1000,
        ]);

        $response = $this->withHeaders($this->adminHeaders($admin))
            ->getJson($this->adminApiPath('/invite/campaign/records?campaign_id=' . $campaign->id . '&current=1&page_size=10'));

        $response->assertOk()
            ->assertJsonPath('total', 1)
            ->assertJsonPath('data.0.campaign_id', $campaign->id)
            ->assertJsonPath('data.0.invitee_email', $invitee->email)
            ->assertJsonPath('data.0.reward_amount', 1000);
    }

    private function useSqliteDatabase(): void
    {
        $this->sqlitePath = storage_path('framework/testing-invite-campaign.sqlite');
        if (file_exists($this->sqlitePath)) {
            unlink($this->sqlitePath);
        }
        touch($this->sqlitePath);

        Config::set('database.default', 'sqlite');
        Config::set('database.connections.sqlite.database', $this->sqlitePath);

        DB::purge('sqlite');
        DB::reconnect('sqlite');
    }

    private function createBaseSchema(): void
    {
        Schema::dropIfExists('v2_invite_campaign_record');
        Schema::dropIfExists('v2_invite_campaign');
        Schema::dropIfExists('v2_log');
        Schema::dropIfExists('v2_order');
        Schema::dropIfExists('v2_invite_code');
        Schema::dropIfExists('v2_payment');
        Schema::dropIfExists('v2_plan');
        Schema::dropIfExists('v2_server_anytls');
        Schema::dropIfExists('v2_server_hysteria');
        Schema::dropIfExists('v2_server_shadowsocks');
        Schema::dropIfExists('v2_server_trojan');
        Schema::dropIfExists('v2_server_tuic');
        Schema::dropIfExists('v2_server_v2node');
        Schema::dropIfExists('v2_server_vless');
        Schema::dropIfExists('v2_server_vmess');
        Schema::dropIfExists('v2_user');

        Schema::create('v2_user', function (Blueprint $table) {
            $table->increments('id');
            $table->string('email')->unique();
            $table->string('password');
            $table->string('password_algo')->nullable();
            $table->string('password_salt')->nullable();
            $table->string('uuid');
            $table->string('token');
            $table->integer('invite_user_id')->nullable();
            $table->integer('transfer_enable')->default(0);
            $table->integer('device_limit')->nullable();
            $table->integer('plan_id')->nullable();
            $table->integer('group_id')->nullable();
            $table->integer('expired_at')->nullable();
            $table->integer('speed_limit')->nullable();
            $table->integer('balance')->default(0);
            $table->integer('commission_balance')->default(0);
            $table->integer('commission_rate')->nullable();
            $table->tinyInteger('commission_type')->default(0);
            $table->integer('discount')->default(0);
            $table->tinyInteger('banned')->default(0);
            $table->tinyInteger('is_admin')->default(0);
            $table->tinyInteger('is_staff')->default(0);
            $table->integer('u')->default(0);
            $table->integer('d')->default(0);
            $table->integer('last_login_at')->nullable();
            $table->integer('created_at')->nullable();
            $table->integer('updated_at')->nullable();
        });

        Schema::create('v2_plan', function (Blueprint $table) {
            $table->increments('id');
            $table->integer('group_id')->default(1);
            $table->integer('transfer_enable')->default(100);
            $table->integer('device_limit')->default(2);
            $table->string('name');
            $table->integer('speed_limit')->nullable();
            $table->tinyInteger('show')->default(1);
            $table->integer('sort')->nullable();
            $table->tinyInteger('renew')->default(1);
            $table->text('content')->nullable();
            $table->integer('month_price')->nullable();
            $table->integer('quarter_price')->nullable();
            $table->integer('half_year_price')->nullable();
            $table->integer('year_price')->nullable();
            $table->integer('two_year_price')->nullable();
            $table->integer('three_year_price')->nullable();
            $table->integer('onetime_price')->nullable();
            $table->integer('reset_price')->nullable();
            $table->tinyInteger('reset_traffic_method')->nullable();
            $table->integer('capacity_limit')->nullable();
            $table->integer('created_at')->nullable();
            $table->integer('updated_at')->nullable();
        });

        Schema::create('v2_invite_code', function (Blueprint $table) {
            $table->increments('id');
            $table->integer('user_id')->nullable();
            $table->string('code')->unique();
            $table->tinyInteger('status')->default(0);
            $table->integer('invite_campaign_id')->nullable();
            $table->integer('created_at')->nullable();
            $table->integer('updated_at')->nullable();
        });

        Schema::create('v2_order', function (Blueprint $table) {
            $table->increments('id');
            $table->integer('invite_user_id')->nullable();
            $table->integer('user_id');
            $table->integer('plan_id')->default(0);
            $table->integer('coupon_id')->nullable();
            $table->integer('payment_id')->nullable();
            $table->integer('type')->default(1);
            $table->string('period');
            $table->string('trade_no')->unique();
            $table->string('callback_no')->nullable();
            $table->integer('total_amount');
            $table->integer('handling_amount')->nullable();
            $table->integer('discount_amount')->default(0);
            $table->integer('surplus_amount')->default(0);
            $table->integer('refund_amount')->default(0);
            $table->integer('balance_amount')->default(0);
            $table->text('surplus_order_ids')->nullable();
            $table->tinyInteger('status')->default(0);
            $table->tinyInteger('commission_status')->default(0);
            $table->integer('commission_balance')->default(0);
            $table->integer('actual_commission_balance')->default(0);
            $table->integer('invite_campaign_id')->nullable();
            $table->integer('invite_campaign_discount_amount')->default(0);
            $table->integer('paid_at')->nullable();
            $table->integer('created_at')->nullable();
            $table->integer('updated_at')->nullable();
        });

        Schema::create('v2_log', function (Blueprint $table) {
            $table->increments('id');
            $table->string('title');
            $table->string('level', 32);
            $table->string('host')->nullable();
            $table->string('uri')->nullable();
            $table->string('method', 16)->nullable();
            $table->string('ip', 64)->nullable();
            $table->text('data')->nullable();
            $table->text('context')->nullable();
            $table->integer('created_at')->nullable();
            $table->integer('updated_at')->nullable();
        });

        Schema::create('v2_payment', function (Blueprint $table) {
            $table->increments('id');
            $table->char('uuid', 32);
            $table->string('payment', 16);
            $table->string('name');
            $table->string('icon')->nullable();
            $table->text('config');
            $table->string('notify_domain', 128)->nullable();
            $table->integer('handling_fee_fixed')->nullable();
            $table->decimal('handling_fee_percent', 5, 2)->nullable();
            $table->tinyInteger('enable')->default(0);
            $table->integer('sort')->nullable();
            $table->integer('created_at')->nullable();
            $table->integer('updated_at')->nullable();
        });

        Schema::create('v2_server_vmess', function (Blueprint $table) {
            $table->increments('id');
            $table->text('group_id');
            $table->text('route_id')->nullable();
            $table->string('name');
            $table->integer('parent_id')->nullable();
            $table->string('host');
            $table->string('port', 11);
            $table->integer('server_port');
            $table->tinyInteger('tls')->default(0);
            $table->text('tags')->nullable();
            $table->string('rate', 11);
            $table->string('network', 11);
            $table->text('rules')->nullable();
            $table->text('networkSettings')->nullable();
            $table->text('tlsSettings')->nullable();
            $table->text('ruleSettings')->nullable();
            $table->text('dnsSettings')->nullable();
            $table->tinyInteger('show')->default(0);
            $table->integer('sort')->nullable();
            $table->integer('created_at')->nullable();
            $table->integer('updated_at')->nullable();
        });

        Schema::create('v2_server_shadowsocks', function (Blueprint $table) {
            $table->increments('id');
            $table->text('group_id')->nullable();
            $table->text('route_id')->nullable();
            $table->integer('parent_id')->nullable();
            $table->text('tags')->nullable();
            $table->string('name')->nullable();
            $table->string('rate', 11)->nullable();
            $table->string('host')->nullable();
            $table->string('port', 11)->nullable();
            $table->integer('server_port')->nullable();
            $table->string('cipher')->nullable();
            $table->string('obfs', 11)->nullable();
            $table->text('obfs_settings')->nullable();
            $table->tinyInteger('show')->default(0);
            $table->integer('sort')->nullable();
            $table->integer('created_at')->nullable();
            $table->integer('updated_at')->nullable();
        });

        Schema::create('v2_server_trojan', function (Blueprint $table) {
            $table->increments('id');
            $table->text('group_id')->nullable();
            $table->text('route_id')->nullable();
            $table->integer('parent_id')->nullable();
            $table->text('tags')->nullable();
            $table->string('name')->nullable();
            $table->string('rate', 11)->nullable();
            $table->string('host')->nullable();
            $table->string('port', 11)->nullable();
            $table->integer('server_port')->nullable();
            $table->string('network', 11)->nullable();
            $table->text('network_settings')->nullable();
            $table->tinyInteger('allow_insecure')->default(0);
            $table->string('server_name')->nullable();
            $table->tinyInteger('show')->default(0);
            $table->integer('sort')->nullable();
            $table->integer('created_at')->nullable();
            $table->integer('updated_at')->nullable();
        });

        Schema::create('v2_server_tuic', function (Blueprint $table) {
            $table->increments('id');
            $table->text('group_id')->nullable();
            $table->text('route_id')->nullable();
            $table->string('name')->nullable();
            $table->integer('parent_id')->nullable();
            $table->string('host')->nullable();
            $table->string('port', 11)->nullable();
            $table->integer('server_port')->nullable();
            $table->text('tags')->nullable();
            $table->string('rate', 11)->nullable();
            $table->tinyInteger('show')->default(0);
            $table->integer('sort')->nullable();
            $table->string('server_name')->nullable();
            $table->tinyInteger('insecure')->default(0);
            $table->tinyInteger('disable_sni')->default(0);
            $table->string('udp_relay_mode')->nullable();
            $table->tinyInteger('zero_rtt_handshake')->default(0);
            $table->string('congestion_control')->nullable();
            $table->integer('created_at')->nullable();
            $table->integer('updated_at')->nullable();
        });

        Schema::create('v2_server_hysteria', function (Blueprint $table) {
            $table->increments('id');
            $table->integer('version')->default(1);
            $table->text('group_id')->nullable();
            $table->text('route_id')->nullable();
            $table->string('name')->nullable();
            $table->integer('parent_id')->nullable();
            $table->string('host')->nullable();
            $table->string('port', 11)->nullable();
            $table->integer('server_port')->nullable();
            $table->text('tags')->nullable();
            $table->string('rate', 11)->nullable();
            $table->tinyInteger('show')->default(0);
            $table->integer('sort')->nullable();
            $table->integer('up_mbps')->default(0);
            $table->integer('down_mbps')->default(0);
            $table->string('obfs')->nullable();
            $table->string('obfs_password')->nullable();
            $table->string('server_name')->nullable();
            $table->tinyInteger('insecure')->default(0);
            $table->integer('created_at')->nullable();
            $table->integer('updated_at')->nullable();
        });

        Schema::create('v2_server_vless', function (Blueprint $table) {
            $table->increments('id');
            $table->text('group_id')->nullable();
            $table->text('route_id')->nullable();
            $table->string('name')->nullable();
            $table->integer('parent_id')->nullable();
            $table->string('host')->nullable();
            $table->integer('port')->nullable();
            $table->integer('server_port')->nullable();
            $table->tinyInteger('tls')->default(0);
            $table->text('tls_settings')->nullable();
            $table->string('flow')->nullable();
            $table->string('network', 11)->nullable();
            $table->text('network_settings')->nullable();
            $table->string('encryption')->nullable();
            $table->text('encryption_settings')->nullable();
            $table->text('tags')->nullable();
            $table->string('rate', 11)->nullable();
            $table->tinyInteger('show')->default(0);
            $table->integer('sort')->nullable();
            $table->integer('created_at')->nullable();
            $table->integer('updated_at')->nullable();
        });

        Schema::create('v2_server_anytls', function (Blueprint $table) {
            $table->increments('id');
            $table->text('group_id')->nullable();
            $table->text('route_id')->nullable();
            $table->string('name')->nullable();
            $table->integer('parent_id')->nullable();
            $table->string('host')->nullable();
            $table->string('port', 11)->nullable();
            $table->integer('server_port')->nullable();
            $table->text('tags')->nullable();
            $table->string('rate', 11)->nullable();
            $table->tinyInteger('show')->default(0);
            $table->integer('sort')->nullable();
            $table->string('server_name')->nullable();
            $table->tinyInteger('insecure')->default(0);
            $table->text('padding_scheme')->nullable();
            $table->integer('created_at')->nullable();
            $table->integer('updated_at')->nullable();
        });

        Schema::create('v2_server_v2node', function (Blueprint $table) {
            $table->increments('id');
            $table->text('group_id')->nullable();
            $table->text('route_id')->nullable();
            $table->string('name')->nullable();
            $table->integer('parent_id')->nullable();
            $table->string('host')->nullable();
            $table->integer('port')->nullable();
            $table->integer('server_port')->nullable();
            $table->tinyInteger('tls')->default(0);
            $table->text('tls_settings')->nullable();
            $table->string('flow')->nullable();
            $table->string('network', 11)->nullable();
            $table->text('network_settings')->nullable();
            $table->string('encryption')->nullable();
            $table->text('encryption_settings')->nullable();
            $table->text('tags')->nullable();
            $table->string('rate', 11)->nullable();
            $table->tinyInteger('show')->default(0);
            $table->integer('sort')->nullable();
            $table->text('padding_scheme')->nullable();
            $table->integer('created_at')->nullable();
            $table->integer('updated_at')->nullable();
        });

        Schema::create('v2_invite_campaign', function (Blueprint $table) {
            $table->increments('id');
            $table->integer('user_id');
            $table->integer('plan_id');
            $table->string('period');
            $table->integer('invite_code_id')->nullable();
            $table->string('invite_code')->nullable();
            $table->integer('reward_amount');
            $table->integer('target_amount');
            $table->integer('current_amount')->default(0);
            $table->integer('invite_count')->default(0);
            $table->tinyInteger('status')->default(0);
            $table->integer('started_at');
            $table->integer('expired_at');
            $table->integer('completed_at')->nullable();
            $table->integer('abandoned_at')->nullable();
            $table->integer('used_at')->nullable();
            $table->integer('created_at')->nullable();
            $table->integer('updated_at')->nullable();
        });

        Schema::create('v2_invite_campaign_record', function (Blueprint $table) {
            $table->increments('id');
            $table->integer('campaign_id');
            $table->integer('invitee_user_id');
            $table->string('invite_code');
            $table->integer('reward_amount');
            $table->integer('created_at')->nullable();
            $table->integer('updated_at')->nullable();
        });
    }

    private function seedBaseConfig(): void
    {
        Config::set('app.debug', false);
        Config::set('v2board.recaptcha_enable', 0);
        Config::set('v2board.email_whitelist_enable', 0);
        Config::set('v2board.email_gmail_limit_enable', 0);
        Config::set('v2board.stop_register', 0);
        Config::set('v2board.invite_force', 0);
        Config::set('v2board.email_verify', 0);
        Config::set('v2board.register_limit_by_ip_enable', 0);
        Config::set('v2board.try_out_plan_id', 0);
        Config::set('v2board.try_out_hour', 24);
        Config::set('v2board.invite_never_expire', 1);
        Config::set('v2board.invite_campaign_reward_amount', 1000);
        Config::set('v2board.invite_campaign_expire_hours', 48);
    }

    private function createUser(array $overrides = []): User
    {
        $user = new User();
        $user->email = $overrides['email'] ?? ('user' . uniqid() . '@example.com');
        $user->password = $overrides['password'] ?? password_hash('password123', PASSWORD_DEFAULT);
        $user->uuid = $overrides['uuid'] ?? uniqid('uuid-', true);
        $user->token = $overrides['token'] ?? uniqid('token-', true);
        $user->invite_user_id = $overrides['invite_user_id'] ?? null;
        $user->transfer_enable = $overrides['transfer_enable'] ?? 0;
        $user->device_limit = $overrides['device_limit'] ?? 0;
        $user->plan_id = $overrides['plan_id'] ?? null;
        $user->group_id = $overrides['group_id'] ?? null;
        $user->expired_at = $overrides['expired_at'] ?? null;
        $user->speed_limit = $overrides['speed_limit'] ?? null;
        $user->balance = $overrides['balance'] ?? 0;
        $user->commission_balance = $overrides['commission_balance'] ?? 0;
        $user->commission_rate = $overrides['commission_rate'] ?? null;
        $user->commission_type = $overrides['commission_type'] ?? 0;
        $user->discount = $overrides['discount'] ?? 0;
        $user->banned = $overrides['banned'] ?? 0;
        $user->u = $overrides['u'] ?? 0;
        $user->d = $overrides['d'] ?? 0;
        $user->is_admin = $overrides['is_admin'] ?? 0;
        $user->is_staff = $overrides['is_staff'] ?? 0;
        $user->save();

        return $user->fresh();
    }

    private function createPlan(array $overrides = []): Plan
    {
        $plan = new Plan();
        $plan->group_id = $overrides['group_id'] ?? 1;
        $plan->transfer_enable = $overrides['transfer_enable'] ?? 100;
        $plan->device_limit = $overrides['device_limit'] ?? 2;
        $plan->name = $overrides['name'] ?? ('Plan ' . uniqid());
        $plan->speed_limit = $overrides['speed_limit'] ?? 100;
        $plan->show = $overrides['show'] ?? 1;
        $plan->sort = $overrides['sort'] ?? 0;
        $plan->renew = $overrides['renew'] ?? 1;
        $plan->month_price = $overrides['month_price'] ?? null;
        $plan->quarter_price = $overrides['quarter_price'] ?? null;
        $plan->half_year_price = $overrides['half_year_price'] ?? null;
        $plan->year_price = $overrides['year_price'] ?? null;
        $plan->two_year_price = $overrides['two_year_price'] ?? null;
        $plan->three_year_price = $overrides['three_year_price'] ?? null;
        $plan->onetime_price = $overrides['onetime_price'] ?? null;
        $plan->reset_price = $overrides['reset_price'] ?? null;
        $plan->save();

        return $plan->fresh();
    }

    private function createCampaign(User $user, Plan $plan, array $overrides = []): InviteCampaign
    {
        $inviteCode = null;
        if (isset($overrides['invite_code_id']) || isset($overrides['invite_code'])) {
            $inviteCode = InviteCode::query()
                ->when(isset($overrides['invite_code_id']), function ($query) use ($overrides) {
                    $query->where('id', $overrides['invite_code_id']);
                })
                ->when(isset($overrides['invite_code']), function ($query) use ($overrides) {
                    $query->where('code', $overrides['invite_code']);
                })
                ->first();
        }
        if (!$inviteCode) {
            $inviteCode = $this->createInviteCode($user, [
                'code' => $overrides['invite_code'] ?? substr(md5((string) microtime(true)), 0, 8),
                'status' => $overrides['invite_code_status'] ?? 0,
            ]);
        }

        $campaign = new InviteCampaign();
        $campaign->user_id = $user->id;
        $campaign->plan_id = $plan->id;
        $campaign->period = $overrides['period'] ?? 'month_price';
        $campaign->reward_amount = $overrides['reward_amount'] ?? 1000;
        $campaign->target_amount = $overrides['target_amount'] ?? ($plan->{$campaign->period} ?? 0);
        $campaign->current_amount = $overrides['current_amount'] ?? 0;
        $campaign->invite_count = $overrides['invite_count'] ?? 0;
        $campaign->status = $overrides['status'] ?? InviteCampaign::STATUS_ACTIVE;
        $campaign->started_at = $overrides['started_at'] ?? time();
        $campaign->expired_at = $overrides['expired_at'] ?? (time() + 48 * 3600);
        $campaign->completed_at = $overrides['completed_at'] ?? null;
        $campaign->abandoned_at = $overrides['abandoned_at'] ?? null;
        $campaign->used_at = $overrides['used_at'] ?? null;
        $campaign->invite_code_id = $inviteCode->id;
        $campaign->invite_code = $inviteCode->code;
        $campaign->save();

        return $campaign->fresh();
    }

    private function createInviteCode(User $user, array $overrides = []): InviteCode
    {
        $inviteCode = new InviteCode();
        $inviteCode->user_id = $user->id;
        $inviteCode->code = $overrides['code'] ?? substr(md5((string) microtime(true)), 0, 8);
        $inviteCode->status = $overrides['status'] ?? 0;
        $inviteCode->invite_campaign_id = $overrides['invite_campaign_id'] ?? null;
        $inviteCode->save();

        return $inviteCode->fresh();
    }

    private function createPayment(array $overrides = []): Payment
    {
        $payment = new Payment();
        $payment->uuid = $overrides['uuid'] ?? str_pad(substr(md5((string) microtime(true)), 0, 16), 32, '0');
        $payment->payment = $overrides['payment'] ?? 'LocalDev';
        $payment->name = $overrides['name'] ?? 'Local Dev';
        $payment->icon = $overrides['icon'] ?? null;
        $payment->config = $overrides['config'] ?? [];
        $payment->notify_domain = $overrides['notify_domain'] ?? null;
        $payment->handling_fee_fixed = $overrides['handling_fee_fixed'] ?? null;
        $payment->handling_fee_percent = $overrides['handling_fee_percent'] ?? null;
        $payment->enable = $overrides['enable'] ?? 1;
        $payment->sort = $overrides['sort'] ?? 0;
        $payment->save();

        return $payment->fresh();
    }

    private function createOrder(array $overrides = []): Order
    {
        $order = new Order();
        $order->invite_user_id = $overrides['invite_user_id'] ?? null;
        $order->user_id = $overrides['user_id'] ?? 1;
        $order->plan_id = $overrides['plan_id'] ?? 0;
        $order->coupon_id = $overrides['coupon_id'] ?? null;
        $order->payment_id = $overrides['payment_id'] ?? null;
        $order->type = $overrides['type'] ?? 1;
        $order->period = $overrides['period'] ?? 'month_price';
        $order->trade_no = $overrides['trade_no'] ?? ('trade-' . uniqid());
        $order->callback_no = $overrides['callback_no'] ?? null;
        $order->total_amount = $overrides['total_amount'] ?? 0;
        $order->handling_amount = $overrides['handling_amount'] ?? null;
        $order->discount_amount = $overrides['discount_amount'] ?? 0;
        $order->surplus_amount = $overrides['surplus_amount'] ?? 0;
        $order->refund_amount = $overrides['refund_amount'] ?? 0;
        $order->balance_amount = $overrides['balance_amount'] ?? 0;
        $order->surplus_order_ids = $overrides['surplus_order_ids'] ?? null;
        $order->status = $overrides['status'] ?? 0;
        $order->commission_status = $overrides['commission_status'] ?? 0;
        $order->commission_balance = $overrides['commission_balance'] ?? 0;
        $order->actual_commission_balance = $overrides['actual_commission_balance'] ?? 0;
        $order->invite_campaign_id = $overrides['invite_campaign_id'] ?? null;
        $order->invite_campaign_discount_amount = $overrides['invite_campaign_discount_amount'] ?? 0;
        $order->paid_at = $overrides['paid_at'] ?? null;
        $order->save();

        return $order->fresh();
    }

    private function createVmessServer(array $overrides = []): ServerVmess
    {
        $server = new ServerVmess();
        $server->group_id = $overrides['group_id'] ?? [1];
        $server->route_id = $overrides['route_id'] ?? [];
        $server->name = $overrides['name'] ?? 'Local VMess';
        $server->parent_id = $overrides['parent_id'] ?? null;
        $server->host = $overrides['host'] ?? '127.0.0.1';
        $server->port = $overrides['port'] ?? '443';
        $server->server_port = $overrides['server_port'] ?? 443;
        $server->tls = $overrides['tls'] ?? 0;
        $server->tags = $overrides['tags'] ?? [];
        $server->rate = $overrides['rate'] ?? '1.0';
        $server->network = $overrides['network'] ?? 'ws';
        $server->rules = $overrides['rules'] ?? null;
        $server->networkSettings = $overrides['networkSettings'] ?? [
            'path' => '/ws',
            'headers' => [
                'Host' => '127.0.0.1',
            ],
        ];
        $server->tlsSettings = $overrides['tlsSettings'] ?? [];
        $server->ruleSettings = $overrides['ruleSettings'] ?? [];
        $server->dnsSettings = $overrides['dnsSettings'] ?? [];
        $server->show = $overrides['show'] ?? 1;
        $server->sort = $overrides['sort'] ?? 0;
        $server->save();

        return $server->fresh();
    }

    private function adminHeaders(User $user): array
    {
        return $this->userHeaders($user);
    }

    private function adminApiPath(string $path): string
    {
        return '/api/v1/' . config('v2board.secure_path', 'localadmin') . $path;
    }

    private function userHeaders(User $user): array
    {
        $authData = (new AuthService($user))->generateAuthData(Request::create('/'))['auth_data'];

        return [
            'authorization' => $authData,
            'User-Agent' => 'PHPUnit',
        ];
    }

    private function guestHeaders(): array
    {
        return [
            'User-Agent' => 'PHPUnit',
        ];
    }
}
