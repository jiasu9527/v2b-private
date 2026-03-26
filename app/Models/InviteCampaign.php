<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Model;

class InviteCampaign extends Model
{
    public const STATUS_ACTIVE = 0;
    public const STATUS_COMPLETED = 1;
    public const STATUS_EXPIRED = 2;
    public const STATUS_ABANDONED = 3;
    public const STATUS_USED = 4;

    protected $table = 'v2_invite_campaign';
    protected $dateFormat = 'U';
    protected $guarded = ['id'];
    protected $casts = [
        'created_at' => 'timestamp',
        'updated_at' => 'timestamp',
    ];
}
