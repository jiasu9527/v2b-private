<?php

namespace App\Http\Controllers\V1\User;

use App\Http\Controllers\Controller;
use App\Models\Plan;
use App\Models\User;
use App\Services\InviteCampaignService;
use Illuminate\Http\Request;

class InviteCampaignController extends Controller
{
    public function save(Request $request)
    {
        $request->validate([
            'plan_id' => 'required|integer',
            'period' => 'required|in:month_price,quarter_price,half_year_price,year_price,two_year_price,three_year_price,onetime_price',
        ]);

        $plan = Plan::find($request->input('plan_id'));
        if (!$plan || !$plan->show) {
            abort(500, __('Subscription plan does not exist'));
        }
        if ($plan->{$request->input('period')} === null) {
            abort(500, __('This payment period cannot be purchased, please choose another period'));
        }

        $user = User::find($request->user['id']);
        if (!$user) {
            abort(500, __('The user does not exist'));
        }

        $service = new InviteCampaignService();
        $campaign = $service->createCampaign($user, $plan, $request->input('period'));

        return response([
            'data' => $service->serializeCampaign($campaign),
        ]);
    }

    public function fetch(Request $request)
    {
        $service = new InviteCampaignService();
        $campaign = $service->getCurrentCampaignByUserId($request->user['id']);

        return response([
            'data' => $service->serializeCampaign($campaign),
        ]);
    }

    public function records(Request $request)
    {
        $service = new InviteCampaignService();
        $current = max((int) $request->input('current', 1), 1);
        $pageSize = max((int) $request->input('page_size', 10), 1);
        $result = $service->getRecords(
            $request->user['id'],
            $request->input('campaign_id'),
            $current,
            $pageSize
        );

        return response($result);
    }

    public function abandon(Request $request)
    {
        $service = new InviteCampaignService();

        return response([
            'data' => $service->abandonCurrentCampaign($request->user['id']),
        ]);
    }
}
