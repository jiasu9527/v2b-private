<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Model;

class InviteCampaignRecord extends Model
{
    protected $table = 'v2_invite_campaign_record';
    protected $dateFormat = 'U';
    protected $guarded = ['id'];
    protected $casts = [
        'created_at' => 'timestamp',
        'updated_at' => 'timestamp',
    ];
}
