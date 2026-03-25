<?php

namespace Tests\Feature;

use App\Models\Order;
use App\Models\Payment;
use App\Models\User;
use App\Services\AuthService;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\Cache;
use Illuminate\Support\Facades\Config;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Facades\Queue;
use Illuminate\Support\Facades\Schema;
use Tests\TestCase;

class OrderCancelRecoveryTest extends TestCase
{
    private string $sqlitePath;

    protected function setUp(): void
    {
        parent::setUp();

        $this->useSqliteDatabase();
        $this->createSchema();
        Cache::flush();
        Queue::fake();
    }

    protected function tearDown(): void
    {
        DB::disconnect('sqlite');
        if (isset($this->sqlitePath) && file_exists($this->sqlitePath)) {
            unlink($this->sqlitePath);
        }

        parent::tearDown();
    }

    public function test_payment_notify_recovers_recently_cancelled_order_and_reclaims_balance()
    {
        $user = $this->createUser([
            'email' => 'recover-user@example.com',
            'balance' => 600,
        ]);
        $payment = $this->createPayment();
        $order = $this->createOrder([
            'user_id' => $user->id,
            'trade_no' => 'recover-trade-001',
            'status' => 0,
            'total_amount' => 1990,
            'balance_amount' => 500,
        ]);

        $this->withHeaders($this->userHeaders($user))
            ->postJson('/api/v1/user/order/cancel', [
                'trade_no' => $order->trade_no,
            ])
            ->assertOk();

        $order->refresh();
        $user->refresh();
        $this->assertSame(2, $order->status);
        $this->assertSame(1100, $user->balance);

        $this->post('/api/v1/guest/payment/notify/LocalDev/' . $payment->uuid, [
            'out_trade_no' => $order->trade_no,
            'trade_no' => 'notify-recover-001',
        ])
            ->assertOk()
            ->assertSee('success');

        $order->refresh();
        $user->refresh();

        $this->assertSame(1, $order->status);
        $this->assertSame('notify-recover-001', $order->callback_no);
        $this->assertNotNull($order->paid_at);
        $this->assertSame(600, $user->balance);
    }

    public function test_admin_can_mark_recently_cancelled_order_as_paid()
    {
        $admin = $this->createUser([
            'email' => 'recover-admin@example.com',
            'is_admin' => 1,
        ]);
        $user = $this->createUser([
            'email' => 'recover-user-2@example.com',
            'balance' => 900,
        ]);
        $order = $this->createOrder([
            'user_id' => $user->id,
            'trade_no' => 'recover-trade-002',
            'status' => 0,
            'total_amount' => 2990,
            'balance_amount' => 300,
        ]);

        $this->withHeaders($this->userHeaders($user))
            ->postJson('/api/v1/user/order/cancel', [
                'trade_no' => $order->trade_no,
            ])
            ->assertOk();

        $this->withHeaders($this->adminHeaders($admin))
            ->postJson($this->adminApiPath('/order/paid'), [
                'trade_no' => $order->trade_no,
            ])
            ->assertOk();

        $order->refresh();
        $user->refresh();

        $this->assertSame(1, $order->status);
        $this->assertSame('manual_operation', $order->callback_no);
        $this->assertNotNull($order->paid_at);
        $this->assertSame(900, $user->balance);
    }

    public function test_admin_bundle_contains_cancelled_order_recovery_action()
    {
        $bundle = file_get_contents(public_path('assets/admin/umi.js'));

        $this->assertNotFalse($bundle);
        $this->assertStringContainsString('补单', $bundle);
    }

    private function useSqliteDatabase(): void
    {
        $this->sqlitePath = storage_path('framework/testing-order-cancel-recovery.sqlite');
        if (file_exists($this->sqlitePath)) {
            unlink($this->sqlitePath);
        }
        touch($this->sqlitePath);

        Config::set('database.default', 'sqlite');
        Config::set('database.connections.sqlite.database', $this->sqlitePath);

        DB::purge('sqlite');
        DB::reconnect('sqlite');
    }

    private function createSchema(): void
    {
        Schema::dropIfExists('v2_payment');
        Schema::dropIfExists('v2_order');
        Schema::dropIfExists('v2_user');

        Schema::create('v2_user', function (Blueprint $table) {
            $table->increments('id');
            $table->string('email')->unique();
            $table->string('password');
            $table->string('uuid');
            $table->string('token');
            $table->tinyInteger('is_admin')->default(0);
            $table->tinyInteger('is_staff')->default(0);
            $table->integer('balance')->default(0);
            $table->integer('t')->default(0);
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
            $table->string('period')->default('month_price');
            $table->string('trade_no')->unique();
            $table->string('callback_no')->nullable();
            $table->integer('total_amount')->default(0);
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

        Schema::create('v2_payment', function (Blueprint $table) {
            $table->increments('id');
            $table->char('uuid', 32);
            $table->string('payment', 32);
            $table->string('name');
            $table->string('icon')->nullable();
            $table->text('config');
            $table->string('notify_domain')->nullable();
            $table->integer('handling_fee_fixed')->nullable();
            $table->decimal('handling_fee_percent', 5, 2)->nullable();
            $table->tinyInteger('enable')->default(1);
            $table->integer('sort')->nullable();
            $table->integer('created_at')->nullable();
            $table->integer('updated_at')->nullable();
        });
    }

    private function createUser(array $overrides = []): User
    {
        $user = new User();
        $user->email = $overrides['email'] ?? ('user' . uniqid() . '@example.com');
        $user->password = $overrides['password'] ?? 'password123';
        $user->uuid = $overrides['uuid'] ?? uniqid('uuid_', true);
        $user->token = $overrides['token'] ?? uniqid('token_', true);
        $user->is_admin = $overrides['is_admin'] ?? 0;
        $user->is_staff = $overrides['is_staff'] ?? 0;
        $user->balance = $overrides['balance'] ?? 0;
        $user->t = $overrides['t'] ?? 0;
        $user->created_at = $overrides['created_at'] ?? time();
        $user->updated_at = $overrides['updated_at'] ?? $user->created_at;
        $user->save();

        return $user->fresh();
    }

    private function createOrder(array $overrides = []): Order
    {
        $order = new Order();
        $order->invite_user_id = $overrides['invite_user_id'] ?? null;
        $order->user_id = $overrides['user_id'];
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
        $order->created_at = $overrides['created_at'] ?? time();
        $order->updated_at = $overrides['updated_at'] ?? $order->created_at;
        $order->save();

        return $order->fresh();
    }

    private function createPayment(array $overrides = []): Payment
    {
        $payment = new Payment();
        $payment->uuid = $overrides['uuid'] ?? substr(md5((string) microtime(true)), 0, 32);
        $payment->payment = $overrides['payment'] ?? 'LocalDev';
        $payment->name = $overrides['name'] ?? 'Local Dev';
        $payment->icon = $overrides['icon'] ?? null;
        $payment->config = $overrides['config'] ?? [];
        $payment->notify_domain = $overrides['notify_domain'] ?? null;
        $payment->handling_fee_fixed = $overrides['handling_fee_fixed'] ?? null;
        $payment->handling_fee_percent = $overrides['handling_fee_percent'] ?? null;
        $payment->enable = $overrides['enable'] ?? 1;
        $payment->sort = $overrides['sort'] ?? 0;
        $payment->created_at = $overrides['created_at'] ?? time();
        $payment->updated_at = $overrides['updated_at'] ?? $payment->created_at;
        $payment->save();

        return $payment->fresh();
    }

    private function userHeaders(User $user): array
    {
        $authData = (new AuthService($user))->generateAuthData(Request::create('/'))['auth_data'];

        return [
            'authorization' => $authData,
            'User-Agent' => 'PHPUnit',
        ];
    }

    private function adminHeaders(User $user): array
    {
        return $this->userHeaders($user);
    }

    private function adminApiPath(string $path): string
    {
        return '/api/v1/' . config('v2board.secure_path', 'localadmin') . $path;
    }
}
