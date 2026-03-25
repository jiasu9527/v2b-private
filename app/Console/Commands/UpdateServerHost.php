<?php

namespace App\Console\Commands;

use Illuminate\Console\Command;
use Illuminate\Support\Facades\DB;

class UpdateServerHost extends Command
{
    /**
     * V2Board 中所有可能包含 'host' 字段的服务器表
     * ⚠️ 这些表都会被依次尝试更新
     */ //php artisan server:update-host:all
    protected array $serverTables = [
        'v2_server_vless',
        'v2_server_trojan',
        'v2_server_shadowsocks', 
        'v2_server_hysteria',
        'v2_server_anytls',
        'v2_server_tuic',
        'v2_server_vmess',
        // 如果您有其他自定义的 server 表，请添加到这里
    ];
    
    /**
     * The name and signature of the console command.
     *
     * @var string
     */
    protected $signature = 'server:update-host:all'; // 更改了 signature 以示区别

    /**
     * The console command description.
     *
     * @var string
     */
    protected $description = '批量修改所有协议节点中 Host 字段的配置';

    /**
     * Execute the console command.
     *
     * @return int
     */
    public function handle()
    {
        $this->info("--- V2Board 全协议 Host 批量修改脚本 ---");

        // 1. 获取用户输入
        $oldHost = $this->ask('请输入您想要替换的【原有 Host 值】');
        if (empty($oldHost)) {
            $this->error("原有 Host 值不能为空。");
            return 1;
        }

        $newHost = $this->ask('请输入您想要替换成的【新的 Host 值】');
        if (empty($newHost)) {
            $this->error("新的 Host 值不能为空。");
            return 1;
        }

        if ($oldHost === $newHost) {
            $this->error("原有 Host 值和新的 Host 值不能相同。");
            return 1;
        }

        $this->info("原有 Host: {$oldHost}");
        $this->info("新的 Host: {$newHost}");
        $this->warn("警告: 此操作将遍历所有节点配置表进行替换，请谨慎确认！");
        
        // 2. 确认操作
        if (!$this->confirm('是否确认执行批量替换操作?')) {
            $this->info("操作已取消。");
            return 0;
        }

        // 3. 遍历并执行替换操作
        $totalUpdatedCount = 0;
        $totalTableCount = count($this->serverTables);
        $this->line("正在更新 {$totalTableCount} 张服务器配置表...");

        foreach ($this->serverTables as $index => $tableName) {
            $protocolName = strtoupper(str_replace('v2_server_', '', $tableName));
            $this->line(" - [{$protocolName}] 正在处理表: {$tableName}...");

            try {
                // 检查表是否存在 'host' 字段，防止对没有 host 的表操作失败
                // (注意：这需要查询 INFORMATION_SCHEMA，此处简化为直接尝试更新)
                
                // 执行批量更新
                $updatedCount = DB::table($tableName)
                       ->where('host', $oldHost)
                       ->update(['host' => $newHost]);
                
                if ($updatedCount > 0) {
                    $this->info("  -> 成功更新了 {$updatedCount} 个 {$protocolName} 节点。");
                    $totalUpdatedCount += $updatedCount;
                } else {
                    $this->comment("  -> {$protocolName} 表中未找到匹配项。");
                }

            } catch (\Illuminate\Database\QueryException $e) {
                // 如果表不存在 'host' 字段，这里会捕获错误并跳过
                $this->warn("  -> 警告: 表 {$tableName} 可能不包含 'host' 字段或更新失败。跳过。");
                // $this->comment("   - SQL 错误: " . $e->getMessage()); // 调试时可开启
            } catch (\Exception $e) {
                $this->error("  -> 严重错误: 更新 {$tableName} 失败。原因: " . $e->getMessage());
                return 1;
            }
        }

        // 4. 最终报告
        if ($totalUpdatedCount > 0) {
            $this->info("====================================");
            $this->info("🎉 恭喜！所有协议中总共更新了 {$totalUpdatedCount} 个节点配置。");
            $this->info("====================================");
        } else {
            $this->warn("⚠️ 未能在任何协议表中找到匹配原有 Host '{$oldHost}' 的记录。");
        }

        return 0;
    }
}