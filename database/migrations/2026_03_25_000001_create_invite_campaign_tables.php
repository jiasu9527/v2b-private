<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

return new class extends Migration {
    public function up()
    {
        if (Schema::hasTable('v2_invite_code') && !Schema::hasColumn('v2_invite_code', 'invite_campaign_id')) {
            Schema::table('v2_invite_code', function (Blueprint $table) {
                $table->integer('invite_campaign_id')->nullable()->after('status');
            });
        }

        if (Schema::hasTable('v2_order')) {
            Schema::table('v2_order', function (Blueprint $table) {
                if (!Schema::hasColumn('v2_order', 'invite_campaign_id')) {
                    $table->integer('invite_campaign_id')->nullable()->after('actual_commission_balance');
                }
                if (!Schema::hasColumn('v2_order', 'invite_campaign_discount_amount')) {
                    $table->integer('invite_campaign_discount_amount')->default(0)->after('invite_campaign_id');
                }
            });
        }

        if (!Schema::hasTable('v2_invite_campaign')) {
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
        }

        if (!Schema::hasTable('v2_invite_campaign_record')) {
            Schema::create('v2_invite_campaign_record', function (Blueprint $table) {
                $table->increments('id');
                $table->integer('campaign_id');
                $table->integer('invitee_user_id');
                $table->string('invite_code');
                $table->integer('reward_amount');
                $table->integer('created_at')->nullable();
                $table->integer('updated_at')->nullable();
                $table->unique(['campaign_id', 'invitee_user_id'], 'uniq_campaign_invitee');
                $table->index(['campaign_id'], 'idx_campaign_id');
            });
        }
    }

    public function down()
    {
        Schema::dropIfExists('v2_invite_campaign_record');
        Schema::dropIfExists('v2_invite_campaign');

        if (Schema::hasTable('v2_order') && Schema::hasColumn('v2_order', 'invite_campaign_discount_amount')) {
            Schema::table('v2_order', function (Blueprint $table) {
                $table->dropColumn('invite_campaign_discount_amount');
            });
        }

        if (Schema::hasTable('v2_order') && Schema::hasColumn('v2_order', 'invite_campaign_id')) {
            Schema::table('v2_order', function (Blueprint $table) {
                $table->dropColumn('invite_campaign_id');
            });
        }

        if (Schema::hasTable('v2_invite_code') && Schema::hasColumn('v2_invite_code', 'invite_campaign_id')) {
            Schema::table('v2_invite_code', function (Blueprint $table) {
                $table->dropColumn('invite_campaign_id');
            });
        }
    }
};
