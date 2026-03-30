package user

import (
	"context"
	"fmt"
	"strings"
)

type ticketAdminNotification struct {
	TicketID int64
	UserID   int64
	Subject  string
	Message  string
}

func (s *DBService) notifyTicketAdmins(ctx context.Context, notice ticketAdminNotification) error {
	if s == nil || s.notifier == nil {
		return nil
	}

	builder := &strings.Builder{}
	fmt.Fprintf(builder, "工单提醒 #%d\n", notice.TicketID)
	fmt.Fprintf(builder, "用户ID：%d\n", notice.UserID)
	if subject := strings.TrimSpace(notice.Subject); subject != "" {
		fmt.Fprintf(builder, "主题：%s\n", subject)
	}
	if message := strings.TrimSpace(notice.Message); message != "" {
		fmt.Fprintf(builder, "内容：\n%s", message)
	}
	return s.notifier.NotifyAdmins(ctx, builder.String(), true)
}
