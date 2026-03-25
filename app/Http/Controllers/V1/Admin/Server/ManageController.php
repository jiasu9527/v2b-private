<?php

namespace App\Http\Controllers\V1\Admin\Server;

use App\Http\Controllers\Controller;
use App\Services\ServerService;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Facades\Schema;

class ManageController extends Controller
{
    protected array $serverTables = [
        'v2_server_v2node',
        'v2_server_anytls',
        'v2_server_hysteria',
        'v2_server_shadowsocks',
        'v2_server_trojan',
        'v2_server_tuic',
        'v2_server_vless',
        'v2_server_vmess',
    ];

    public function getNodes(Request $request)
    {
        $serverService = new ServerService();
        return response([
            'data' => $serverService->getAllServers()
        ]);
    }

    public function sort(Request $request)
    {
        ini_set('post_max_size', '5m');
        $params = $request->only(
            'shadowsocks',
            'vmess',
            'vless',
            'trojan',
            'tuic',
            'hysteria',
            'anytls',
            'v2node'
        ) ?? [];
        if (empty($params)) {
            $params = [
                'shadowsocks' => $_POST['shadowsocks'] ?? null,
                'vmess'       => $_POST['vmess'] ?? null,
                'vless'       => $_POST['vless'] ?? null,
                'trojan'      => $_POST['trojan'] ?? null,
                'tuic'        => $_POST['tuic'] ?? null,
                'hysteria'    => $_POST['hysteria'] ?? null,
                'anytls'      => $_POST['anytls'] ?? null,
                'v2node'      => $_POST['v2node'] ?? null,
            ];
        }
        DB::beginTransaction();
        foreach ($params as $k => $v) {
            $model = 'App\\Models\\Server' . ucfirst($k);
            foreach($v as $id => $sort) {
                if (!$model::find($id)->update(['sort' => $sort])) {
                    DB::rollBack();
                    abort(500, '保存失败');
                }
            }
        }
        DB::commit();
        return response([
            'data' => true
        ]);
    }

    public function updateHost(Request $request)
    {
        $params = $request->validate([
            'old_host' => 'required|string',
            'new_host' => 'required|string',
        ]);

        $oldHost = trim($params['old_host']);
        $newHost = trim($params['new_host']);

        if ($oldHost === '' || $newHost === '') {
            abort(500, '地址不能为空');
        }

        if ($oldHost === $newHost) {
            abort(500, '原地址和新地址不能相同');
        }

        $updatedTotal = 0;
        $updatedByTable = [];

        DB::beginTransaction();
        try {
            foreach ($this->serverTables as $tableName) {
                if (!Schema::hasTable($tableName) || !Schema::hasColumn($tableName, 'host')) {
                    continue;
                }

                $updatedCount = DB::table($tableName)
                    ->where('host', $oldHost)
                    ->update(['host' => $newHost]);

                if ($updatedCount > 0) {
                    $updatedByTable[$tableName] = $updatedCount;
                    $updatedTotal += $updatedCount;
                }
            }
        } catch (\Throwable $e) {
            DB::rollBack();
            abort(500, '批量修改地址失败');
        }
        DB::commit();

        return response([
            'data' => [
                'updated_total' => $updatedTotal,
                'updated_by_table' => $updatedByTable,
            ]
        ]);
    }
}
