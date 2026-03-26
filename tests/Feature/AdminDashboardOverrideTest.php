<?php

namespace Tests\Feature;

use App\Models\Order;
use App\Models\User;
use App\Services\AuthService;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\Cache;
use Illuminate\Support\Facades\Config;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Facades\Schema;
use Tests\TestCase;

class AdminDashboardOverrideTest extends TestCase
{
    private string $sqlitePath;

    protected function setUp(): void
    {
        parent::setUp();

        $this->useSqliteDatabase();
        $this->createSchema();
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

    public function test_admin_override_includes_new_paid_user_and_last_month_metrics()
    {
        $currentMonthStart = strtotime(date('Y-m-1'));
        $lastMonthStart = strtotime('-1 month', $currentMonthStart);
        $twoMonthsAgoStart = strtotime('-2 month', $currentMonthStart);

        $admin = $this->createUser([
            'email' => 'admin-dashboard@example.com',
            'is_admin' => 1,
            'created_at' => $twoMonthsAgoStart + 3600,
        ]);

        $currentMonthRegisteredA = $this->createUser([
            'email' => 'current-a@example.com',
            'created_at' => $currentMonthStart + 3600,
        ]);
        $currentMonthRegisteredB = $this->createUser([
            'email' => 'current-b@example.com',
            'created_at' => $currentMonthStart + 7200,
        ]);

        $lastMonthRegisteredA = $this->createUser([
            'email' => 'last-a@example.com',
            'created_at' => $lastMonthStart + 3600,
        ]);
        $lastMonthRegisteredB = $this->createUser([
            'email' => 'last-b@example.com',
            'created_at' => $lastMonthStart + 7200,
        ]);
        $lastMonthRegisteredC = $this->createUser([
            'email' => 'last-c@example.com',
            'created_at' => $lastMonthStart + 10800,
        ]);

        $oldPaidUser = $this->createUser([
            'email' => 'old-paid@example.com',
            'created_at' => $twoMonthsAgoStart + 7200,
        ]);

        $this->createOrder([
            'user_id' => $currentMonthRegisteredA->id,
            'status' => 3,
            'total_amount' => 990,
            'created_at' => $currentMonthStart + 86400,
        ]);

        $this->createOrder([
            'user_id' => $lastMonthRegisteredA->id,
            'status' => 3,
            'total_amount' => 1990,
            'created_at' => $lastMonthStart + 86400,
        ]);

        $this->createOrder([
            'user_id' => $lastMonthRegisteredB->id,
            'status' => 3,
            'total_amount' => 1590,
            'created_at' => $currentMonthStart + 129600,
        ]);

        $this->createOrder([
            'user_id' => $oldPaidUser->id,
            'status' => 3,
            'total_amount' => 2990,
            'created_at' => $lastMonthStart + 172800,
        ]);

        $this->createOrder([
            'user_id' => $oldPaidUser->id,
            'status' => 3,
            'total_amount' => 3990,
            'created_at' => $currentMonthStart + 172800,
        ]);

        $this->createOrder([
            'user_id' => $currentMonthRegisteredB->id,
            'status' => 0,
            'total_amount' => 4990,
            'created_at' => $currentMonthStart + 259200,
        ]);

        $response = $this->withHeaders($this->adminHeaders($admin))
            ->getJson($this->adminApiPath('/stat/getOverride'));

        $response->assertOk()
            ->assertJsonPath('data.month_register_total', 2)
            ->assertJsonPath('data.last_month_register_total', 3)
            ->assertJsonPath('data.month_paid_user_total', 1)
            ->assertJsonPath('data.last_month_paid_user_total', 2);
    }

    private function useSqliteDatabase(): void
    {
        $this->sqlitePath = storage_path('framework/testing-admin-dashboard.sqlite');
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
        Schema::dropIfExists('v2_commission_log');
        Schema::dropIfExists('v2_ticket');
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
            $table->integer('t')->default(0);
            $table->integer('created_at')->nullable();
            $table->integer('updated_at')->nullable();
        });

        Schema::create('v2_order', function (Blueprint $table) {
            $table->increments('id');
            $table->integer('user_id')->nullable();
            $table->integer('invite_user_id')->nullable();
            $table->string('trade_no')->nullable();
            $table->tinyInteger('status')->default(0);
            $table->tinyInteger('commission_status')->default(0);
            $table->integer('commission_balance')->default(0);
            $table->integer('total_amount')->default(0);
            $table->integer('created_at')->nullable();
            $table->integer('updated_at')->nullable();
        });

        Schema::create('v2_ticket', function (Blueprint $table) {
            $table->increments('id');
            $table->tinyInteger('status')->default(0);
            $table->tinyInteger('reply_status')->default(0);
            $table->integer('created_at')->nullable();
            $table->integer('updated_at')->nullable();
        });

        Schema::create('v2_commission_log', function (Blueprint $table) {
            $table->increments('id');
            $table->integer('get_amount')->default(0);
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
        $user->t = $overrides['t'] ?? 0;
        $user->created_at = $overrides['created_at'] ?? time();
        $user->updated_at = $overrides['updated_at'] ?? $user->created_at;
        $user->save();

        return $user->fresh();
    }

    private function createOrder(array $overrides = []): Order
    {
        $order = new Order();
        $order->user_id = $overrides['user_id'] ?? null;
        $order->invite_user_id = $overrides['invite_user_id'] ?? null;
        $order->trade_no = $overrides['trade_no'] ?? uniqid('trade_', true);
        $order->status = $overrides['status'] ?? 0;
        $order->commission_status = $overrides['commission_status'] ?? 0;
        $order->commission_balance = $overrides['commission_balance'] ?? 0;
        $order->total_amount = $overrides['total_amount'] ?? 0;
        $order->created_at = $overrides['created_at'] ?? time();
        $order->updated_at = $overrides['updated_at'] ?? $order->created_at;
        $order->save();

        return $order->fresh();
    }

    private function adminApiPath(string $path): string
    {
        return '/api/v1/' . config('v2board.secure_path', 'localadmin') . $path;
    }

    private function adminHeaders(User $user): array
    {
        $authData = (new AuthService($user))->generateAuthData(Request::create('/'))['auth_data'];

        return [
            'authorization' => $authData,
            'User-Agent' => 'PHPUnit',
        ];
    }
}
