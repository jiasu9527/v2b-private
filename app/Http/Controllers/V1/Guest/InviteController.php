<?php

namespace App\Http\Controllers\V1\Guest;

use App\Http\Controllers\Controller;
use App\Services\InviteCampaignService;
use Illuminate\Http\Request;

class InviteController extends Controller
{
    public function preview(Request $request)
    {
        $service = new InviteCampaignService();

        return response([
            'data' => $service->previewInviteCode($request->query('code')),
        ]);
    }
}
