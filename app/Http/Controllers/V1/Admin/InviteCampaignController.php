<?php

namespace App\Http\Controllers\V1\Admin;

use App\Http\Controllers\Controller;
use App\Models\InviteCampaign;
use App\Models\InviteCampaignRecord;
use App\Models\Order;
use App\Models\User;
use App\Services\InviteCampaignService;
use Illuminate\Http\Request;

class InviteCampaignController extends Controller
{
    public function fetch(Request $request)
    {
        $current = max((int) $request->input('current', 1), 1);
        $pageSize = max((int) $request->input('pageSize', 10), 10);
        $builder = InviteCampaign::orderBy('created_at', 'DESC');
        $this->filter($request, $builder);
        $total = $builder->count();
        $service = new InviteCampaignService();
        $campaigns = $builder->forPage($current, $pageSize)->get()->map(function ($campaign) use ($service) {
            $data = $service->serializeCampaign($campaign);
            $user = User::find($campaign->user_id);
            $usedOrder = Order::where('invite_campaign_id', $campaign->id)
                ->orderByDesc('id')
                ->first();
            $data['user_email'] = $user->email ?? null;
            $data['plan_name'] = $data['plan']['name'] ?? null;
            $data['used_order_trade_no'] = $usedOrder->trade_no ?? null;
            $data['used_order_status'] = $usedOrder->status ?? null;
            return $data;
        })->values();

        return response([
            'data' => $campaigns,
            'total' => $total
        ]);
    }

    public function detail(Request $request)
    {
        $campaign = InviteCampaign::find($request->input('id'));
        if (!$campaign) {
            abort(500, '任务不存在');
        }

        $service = new InviteCampaignService();
        $data = $service->serializeCampaign($campaign);
        $data['user'] = User::find($campaign->user_id);
        $data['used_order'] = Order::where('invite_campaign_id', $campaign->id)
            ->orderByDesc('id')
            ->first();

        return response([
            'data' => $data
        ]);
    }

    public function records(Request $request)
    {
        $campaign = InviteCampaign::find($request->input('campaign_id'));
        if (!$campaign) {
            abort(500, '任务不存在');
        }

        $current = max((int) $request->input('current', 1), 1);
        $pageSize = max((int) $request->input('page_size', 10), 1);
        $builder = InviteCampaignRecord::where('campaign_id', $campaign->id)
            ->orderByDesc('id');
        $total = $builder->count();
        $records = $builder->forPage($current, $pageSize)->get()->map(function ($record) {
            $invitee = User::find($record->invitee_user_id);
            $record['invitee_email'] = $invitee->email ?? null;
            return $record;
        })->values();

        return response([
            'data' => $records,
            'total' => $total
        ]);
    }

    private function filter(Request $request, $builder): void
    {
        $filters = $request->input('filter');
        if (!$filters) {
            return;
        }

        foreach ($filters as $filter) {
            if (($filter['key'] ?? null) === 'email') {
                $user = User::where('email', 'like', '%' . ($filter['value'] ?? '') . '%')->first();
                $builder->where('user_id', $user->id ?? 0);
                continue;
            }

            $condition = $filter['condition'] ?? '=';
            $value = $filter['value'] ?? null;
            if ($condition === '模糊') {
                $condition = 'like';
                $value = '%' . $value . '%';
            }
            $builder->where($filter['key'], $condition, $value);
        }
    }
}
