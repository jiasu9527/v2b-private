<?php

namespace App\Services;

use App\Models\InviteCampaign;
use App\Models\InviteCampaignRecord;
use App\Models\InviteCode;
use App\Models\Order;
use App\Models\Plan;
use App\Models\User;
use App\Utils\Helper;
use Illuminate\Support\Facades\DB;

class InviteCampaignService
{
    public function getRewardAmount(): int
    {
        return (int) config('v2board.invite_campaign_reward_amount', 1000);
    }

    public function getExpireSeconds(): int
    {
        return (int) config('v2board.invite_campaign_expire_hours', 48) * 3600;
    }

    public function getCurrentCampaignByUserId(int $userId): ?InviteCampaign
    {
        $campaign = InviteCampaign::where('user_id', $userId)
            ->whereIn('status', [
                InviteCampaign::STATUS_ACTIVE,
                InviteCampaign::STATUS_COMPLETED,
            ])
            ->orderByDesc('id')
            ->first();

        if (!$campaign) {
            return null;
        }

        return $this->refreshCampaignStatus($campaign);
    }

    public function createCampaign(User $user, Plan $plan, string $period): InviteCampaign
    {
        if ($this->getCurrentCampaignByUserId($user->id)) {
            abort(500, __('There is already an active invite campaign task'));
        }

        return DB::transaction(function () use ($user, $plan, $period) {
            // Bind campaign to a normal long-term invite code.
            $inviteCode = InviteCode::where('user_id', $user->id)
                ->where('status', 0)
                ->lockForUpdate()
                ->orderBy('id', 'asc')
                ->first();
            if (!$inviteCode) {
                $inviteCode = new InviteCode();
                $inviteCode->user_id = $user->id;
                $inviteCode->code = $this->generateInviteCode();
                $inviteCode->status = 0;
                $inviteCode->save();
            }

            $campaign = new InviteCampaign();
            $campaign->user_id = $user->id;
            $campaign->plan_id = $plan->id;
            $campaign->period = $period;
            $campaign->reward_amount = $this->getRewardAmount();
            $campaign->target_amount = (int) $plan->{$period};
            $campaign->current_amount = 0;
            $campaign->invite_count = 0;
            $campaign->status = InviteCampaign::STATUS_ACTIVE;
            $campaign->started_at = time();
            $campaign->expired_at = time() + $this->getExpireSeconds();
            $campaign->invite_code_id = $inviteCode->id;
            $campaign->invite_code = $inviteCode->code;
            $campaign->save();

            return $campaign->fresh();
        });
    }

    public function getRecords(int $userId, ?int $campaignId, int $current, int $pageSize): array
    {
        $campaign = $campaignId
            ? InviteCampaign::where('id', $campaignId)->where('user_id', $userId)->first()
            : $this->getCurrentCampaignByUserId($userId);

        if (!$campaign) {
            return [
                'data' => [],
                'total' => 0,
            ];
        }

        $builder = InviteCampaignRecord::where('campaign_id', $campaign->id)
            ->orderByDesc('id');

        return [
            'data' => $builder->forPage($current, $pageSize)->get(),
            'total' => $builder->count(),
        ];
    }

    public function abandonCurrentCampaign(int $userId): bool
    {
        $campaign = $this->getCurrentCampaignByUserId($userId);
        if (!$campaign) {
            abort(500, __('Invite campaign task does not exist'));
        }

        return $this->closeCampaign($campaign, InviteCampaign::STATUS_ABANDONED, 'abandoned_at');
    }

    public function refreshCampaignStatus(InviteCampaign $campaign): InviteCampaign
    {
        if (
            in_array($campaign->status, [InviteCampaign::STATUS_ACTIVE, InviteCampaign::STATUS_COMPLETED], true)
            && $campaign->expired_at <= time()
        ) {
            $this->closeCampaign($campaign, InviteCampaign::STATUS_EXPIRED);
        } elseif (
            $campaign->status === InviteCampaign::STATUS_ACTIVE
            && $campaign->current_amount >= $campaign->target_amount
        ) {
            $this->closeCampaign($campaign, InviteCampaign::STATUS_COMPLETED, 'completed_at');
        }

        return $campaign->fresh();
    }

    public function serializeCampaign(?InviteCampaign $campaign): ?array
    {
        if (!$campaign) {
            return null;
        }

        $campaign = $this->refreshCampaignStatus($campaign);

        return [
            'id' => $campaign->id,
            'user_id' => $campaign->user_id,
            'plan_id' => $campaign->plan_id,
            'period' => $campaign->period,
            'invite_code_id' => $campaign->invite_code_id,
            'invite_code' => $campaign->invite_code,
            'reward_amount' => (int) $campaign->reward_amount,
            'target_amount' => (int) $campaign->target_amount,
            'current_amount' => (int) $campaign->current_amount,
            'remaining_amount' => max(0, (int) $campaign->target_amount - (int) $campaign->current_amount),
            'discount_amount' => min((int) $campaign->current_amount, (int) $campaign->target_amount),
            'invite_count' => (int) $campaign->invite_count,
            'status' => (int) $campaign->status,
            'started_at' => (int) $campaign->started_at,
            'expired_at' => (int) $campaign->expired_at,
            'completed_at' => $campaign->completed_at,
            'abandoned_at' => $campaign->abandoned_at,
            'used_at' => $campaign->used_at,
            'plan' => Plan::find($campaign->plan_id),
        ];
    }

    public function getCampaignByInviteCode(string $code): ?InviteCampaign
    {
        $inviteCode = InviteCode::where('code', $code)->first();
        if (!$inviteCode || !$inviteCode->user_id) {
            return null;
        }

        $campaign = InviteCampaign::where('user_id', $inviteCode->user_id)
            ->where('invite_code', $inviteCode->code)
            ->whereIn('status', [
                InviteCampaign::STATUS_ACTIVE,
                InviteCampaign::STATUS_COMPLETED,
            ])
            ->orderByDesc('id')
            ->first();
        if (!$campaign) {
            return null;
        }

        $campaign = $this->refreshCampaignStatus($campaign);
        if (!in_array($campaign->status, [InviteCampaign::STATUS_ACTIVE, InviteCampaign::STATUS_COMPLETED], true)) {
            return null;
        }

        return $campaign;
    }

    public function accrueRegistration(InviteCode $inviteCode, User $invitee): ?InviteCampaign
    {
        if (!$inviteCode->user_id) {
            return null;
        }

        return DB::transaction(function () use ($inviteCode, $invitee) {
            $campaign = InviteCampaign::where('user_id', $inviteCode->user_id)
                ->where('invite_code', $inviteCode->code)
                ->whereIn('status', [
                    InviteCampaign::STATUS_ACTIVE,
                    InviteCampaign::STATUS_COMPLETED,
                ])
                ->lockForUpdate()
                ->orderByDesc('id')
                ->first();
            if (!$campaign) {
                return null;
            }

            $campaign = $this->refreshCampaignStatus($campaign);
            if ($campaign->status !== InviteCampaign::STATUS_ACTIVE) {
                return $campaign;
            }

            if (InviteCampaignRecord::where('campaign_id', $campaign->id)->where('invitee_user_id', $invitee->id)->exists()) {
                return $campaign;
            }

            InviteCampaignRecord::create([
                'campaign_id' => $campaign->id,
                'invitee_user_id' => $invitee->id,
                'invite_code' => $inviteCode->code,
                'reward_amount' => $campaign->reward_amount,
            ]);

            $campaign->current_amount = min(
                $campaign->target_amount,
                $campaign->current_amount + $campaign->reward_amount
            );
            $campaign->invite_count = $campaign->invite_count + 1;
            $campaign->save();

            return $this->refreshCampaignStatus($campaign);
        });
    }

    public function applyOrderDiscount(Order $order, User $user): void
    {
        $campaign = $this->getCurrentCampaignByUserId($user->id);
        if (!$campaign) {
            return;
        }

        if (!in_array($campaign->status, [InviteCampaign::STATUS_ACTIVE, InviteCampaign::STATUS_COMPLETED], true)) {
            return;
        }

        if ((int) $campaign->plan_id !== (int) $order->plan_id || (string) $campaign->period !== (string) $order->period) {
            return;
        }

        $discountAmount = min((int) $campaign->current_amount, (int) $order->total_amount);
        if ($discountAmount <= 0) {
            return;
        }

        $order->invite_campaign_id = $campaign->id;
        $order->invite_campaign_discount_amount = $discountAmount;
        $order->total_amount = $order->total_amount - $discountAmount;
    }

    public function markUsedByOrder(Order $order): void
    {
        if (!$order->invite_campaign_id) {
            return;
        }

        $campaign = InviteCampaign::find($order->invite_campaign_id);
        if (!$campaign) {
            return;
        }

        if ($campaign->status === InviteCampaign::STATUS_USED) {
            return;
        }

        $campaign->status = InviteCampaign::STATUS_USED;
        $campaign->used_at = time();
        $campaign->save();
    }

    private function closeCampaign(InviteCampaign $campaign, int $status, ?string $timestampField = null): bool
    {
        if ($campaign->status === $status) {
            return true;
        }

        $campaign->status = $status;
        if ($timestampField) {
            $campaign->{$timestampField} = time();
        }
        return $campaign->save();
    }

    private function generateInviteCode(): string
    {
        do {
            $code = Helper::randomChar(8);
        } while (InviteCode::where('code', $code)->exists());

        return $code;
    }
}
