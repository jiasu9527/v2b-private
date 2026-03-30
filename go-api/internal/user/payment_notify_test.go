package user

import (
	"context"
	"strings"
	"testing"

	"forest/go-api/internal/config"
)

func TestNotifyOrderPaidAdminsUsesTelegramNotifier(t *testing.T) {
	notifier := &captureTicketNotifier{}
	service := NewDBService(config.Config{}, nil).WithAdminNotifier(notifier)

	if err := service.notifyOrderPaidAdmins(context.Background(), adminPaymentNotification{
		TradeNo:     "T9001",
		TotalAmount: 1299,
	}); err != nil {
		t.Fatalf("notify order paid admins: %v", err)
	}

	if len(notifier.messages) != 1 {
		t.Fatalf("expected one notification, got %#v", notifier.messages)
	}
	if len(notifier.includeStaff) != 1 || notifier.includeStaff[0] {
		t.Fatalf("expected admin-only notification, got %#v", notifier.includeStaff)
	}
	message := notifier.messages[0]
	if !strings.Contains(message, "💰成功收款12.99元") || !strings.Contains(message, "订单号：T9001") {
		t.Fatalf("unexpected order notification message: %q", message)
	}
}
