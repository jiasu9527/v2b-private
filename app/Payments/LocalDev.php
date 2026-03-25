<?php

namespace App\Payments;

use App\Models\Order;
use App\Services\OrderService;

class LocalDev
{
    public function __construct($config)
    {
        $this->config = $config;
    }

    public function form()
    {
        return [];
    }

    public function pay($order)
    {
        $orderModel = Order::where('trade_no', $order['trade_no'])->first();
        if (!$orderModel) {
            abort(500, 'order is not found');
        }

        $orderService = new OrderService($orderModel);
        if (!$orderService->paid('localdev-' . time())) {
            abort(500, 'local dev payment failed');
        }

        return [
            'type' => 1,
            'data' => $order['return_url'],
        ];
    }

    public function notify($params)
    {
        return [
            'trade_no' => $params['out_trade_no'] ?? null,
            'callback_no' => $params['trade_no'] ?? ('localdev-notify-' . time()),
        ];
    }
}
