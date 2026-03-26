<?php

namespace Tests\Feature;

use App\Models\User;
use App\Services\AuthService;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\Config;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Facades\Schema;
use Tests\TestCase;

class AdminServerBulkHostUpdateTest extends TestCase
{
    private string $sqlitePath;

    protected function setUp(): void
    {
        parent::setUp();

        $this->useSqliteDatabase();
        $this->createSchema();
    }

    protected function tearDown(): void
    {
        DB::disconnect('sqlite');
        if (isset($this->sqlitePath) && file_exists($this->sqlitePath)) {
            unlink($this->sqlitePath);
        }

        parent::tearDown();
    }

    public function test_admin_can_bulk_update_server_hosts_by_exact_old_host_match()
    {
        $admin = $this->createUser([
            'email' => 'bulk-host-admin@example.com',
            'is_admin' => 1,
        ]);

        DB::table('v2_server_vmess')->insert([
            'host' => 'old.example.com',
            'created_at' => time(),
            'updated_at' => time(),
        ]);
        DB::table('v2_server_trojan')->insert([
            'host' => 'old.example.com',
            'created_at' => time(),
            'updated_at' => time(),
        ]);
        DB::table('v2_server_shadowsocks')->insert([
            'host' => 'keep.example.com',
            'created_at' => time(),
            'updated_at' => time(),
        ]);

        $this->withHeaders($this->adminHeaders($admin))
            ->postJson($this->adminApiPath('/server/manage/updateHost'), [
                'old_host' => 'old.example.com',
                'new_host' => 'new.example.com',
            ])
            ->assertOk()
            ->assertJsonPath('data.updated_total', 2);

        $this->assertSame(1, DB::table('v2_server_vmess')->where('host', 'new.example.com')->count());
        $this->assertSame(1, DB::table('v2_server_trojan')->where('host', 'new.example.com')->count());
        $this->assertSame(1, DB::table('v2_server_shadowsocks')->where('host', 'keep.example.com')->count());
    }

    public function test_server_manage_bundle_contains_bulk_host_update_action()
    {
        $bundle = file_get_contents(public_path('assets/admin/umi.js'));

        $this->assertNotFalse($bundle);
        $this->assertStringContainsString('批量修改地址', $bundle);
        $this->assertStringContainsString('展开批量修改地址', $bundle);
        $this->assertStringContainsString('收起批量修改地址', $bundle);
        $this->assertStringContainsString('原地址筛选', $bundle);
        $this->assertStringContainsString('新地址', $bundle);
        $this->assertStringContainsString('/server/manage/updateHost', $bundle);
        $this->assertStringContainsString('showBulkHostEditor: !1', $bundle);
        $this->assertStringNotContainsString('window.prompt("\\u8bf7\\u8f93\\u5165\\u9700\\u8981\\u5339\\u914d\\u7684\\u539f\\u5730\\u5740")', $bundle);
    }

    private function useSqliteDatabase(): void
    {
        $this->sqlitePath = storage_path('framework/testing-admin-server-bulk-host.sqlite');
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
        foreach ([
            'v2_log',
            'v2_server_v2node',
            'v2_server_anytls',
            'v2_server_hysteria',
            'v2_server_tuic',
            'v2_server_vless',
            'v2_server_trojan',
            'v2_server_shadowsocks',
            'v2_server_vmess',
            'v2_user',
        ] as $table) {
            Schema::dropIfExists($table);
        }

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

        foreach ([
            'v2_server_vmess',
            'v2_server_shadowsocks',
            'v2_server_trojan',
            'v2_server_vless',
            'v2_server_tuic',
            'v2_server_hysteria',
            'v2_server_anytls',
            'v2_server_v2node',
        ] as $tableName) {
            Schema::create($tableName, function (Blueprint $table) {
                $table->increments('id');
                $table->string('host')->nullable();
                $table->integer('created_at')->nullable();
                $table->integer('updated_at')->nullable();
            });
        }
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

    private function adminHeaders(User $user): array
    {
        $authData = (new AuthService($user))->generateAuthData(Request::create('/'))['auth_data'];

        return [
            'authorization' => $authData,
            'User-Agent' => 'PHPUnit',
        ];
    }

    private function adminApiPath(string $path): string
    {
        return '/api/v1/' . config('v2board.secure_path', 'localadmin') . $path;
    }
}
