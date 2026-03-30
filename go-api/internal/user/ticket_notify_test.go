package user

import (
	"context"
	"strings"
	"testing"

	"forest/go-api/internal/config"
)

type captureTicketNotifier struct {
	messages     []string
	includeStaff []bool
}

func (c *captureTicketNotifier) NotifyAdmins(_ context.Context, message string, includeStaff bool) error {
	c.messages = append(c.messages, message)
	c.includeStaff = append(c.includeStaff, includeStaff)
	return nil
}

func TestNotifyTicketAdminsUsesTelegramNotifier(t *testing.T) {
	notifier := &captureTicketNotifier{}
	service := NewDBService(config.Config{}, nil).WithAdminNotifier(notifier)

	if err := service.notifyTicketAdmins(context.Background(), ticketAdminNotification{
		TicketID: 9,
		UserID:   7,
		Subject:  "Need help",
		Message:  "hello world",
	}); err != nil {
		t.Fatalf("notify ticket admins: %v", err)
	}

	if len(notifier.messages) != 1 {
		t.Fatalf("expected one notification, got %#v", notifier.messages)
	}
	if len(notifier.includeStaff) != 1 || !notifier.includeStaff[0] {
		t.Fatalf("expected include staff notification, got %#v", notifier.includeStaff)
	}
	message := notifier.messages[0]
	if !strings.Contains(message, "工单提醒 #9") || !strings.Contains(message, "用户ID：7") || !strings.Contains(message, "Need help") {
		t.Fatalf("unexpected notification message: %q", message)
	}
}
