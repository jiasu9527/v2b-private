package user

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

type adminPaymentNotification struct {
	TradeNo     string
	TotalAmount int64
}

func (s *DBService) notifyOrderPaidAdmins(ctx context.Context, notice adminPaymentNotification) error {
	if s == nil || s.notifier == nil {
		return nil
	}

	tradeNo := strings.TrimSpace(notice.TradeNo)
	if tradeNo == "" {
		return nil
	}

	amount := strconv.FormatFloat(float64(notice.TotalAmount)/100, 'f', -1, 64)
	message := fmt.Sprintf("💰成功收款%s元\n———————————————\n订单号：%s", amount, tradeNo)
	return s.notifier.NotifyAdmins(ctx, message, false)
}
