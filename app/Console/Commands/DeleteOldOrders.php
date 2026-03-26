<?php

namespace App\Console\Commands;

use Illuminate\Console\Command;
use Illuminate\Support\Facades\DB;

class DeleteOldOrders extends Command
{
    protected $signature = 'orders:delete-old {days=30}';
    protected $description = 'Delete orders older than the specified number of days';

    public function handle()
    {
        $days = $this->argument('days');
        $timestamp = time() - ($days * 24 * 60 * 60);

        $this->info("Preparing to delete orders older than {$days} days...");

        // php artisan orders:delete-old 
        $count = DB::table('v2_order')
            ->where('created_at', '<', $timestamp)
            ->count();

        if ($count === 0) {
            $this->info("No orders found older than {$days} days.");
            return;
        }

        if ($this->confirm("Are you sure you want to delete {$count} orders?")) {
            DB::table('v2_order')
                ->where('created_at', '<', $timestamp)
                ->delete();

            $this->info("{$count} orders have been deleted.");
        } else {
            $this->info('Operation cancelled.');
        }
    }
}

