package user

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"forest/go-api/internal/config"

	"github.com/DATA-DOG/go-sqlmock"
)

type blockingOrderPaidNotifier struct {
	started chan struct{}
	release chan struct{}
	done    chan struct{}
}

func (n *blockingOrderPaidNotifier) NotifyAdmins(ctx context.Context, _ string, _ bool) error {
	close(n.started)
	defer close(n.done)
	select {
	case <-n.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestDispatchOrderPaidAdminNotificationDoesNotBlockPaymentCallback(t *testing.T) {
	notifier := &blockingOrderPaidNotifier{
		started: make(chan struct{}),
		release: make(chan struct{}),
		done:    make(chan struct{}),
	}
	service := NewDBService(config.Config{}, nil).WithAdminNotifier(notifier)

	returned := make(chan struct{})
	go func() {
		service.dispatchOrderPaidAdminNotification(adminPaymentNotification{TradeNo: "T-ASYNC", TotalAmount: 100})
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("notification dispatcher blocked the payment callback")
	}
	select {
	case <-notifier.started:
	case <-time.After(time.Second):
		t.Fatal("notification was not started asynchronously")
	}
	close(notifier.release)
	select {
	case <-notifier.done:
	case <-time.After(time.Second):
		t.Fatal("notification goroutine did not finish")
	}
}

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

func TestNotifyOrderPaidAdminsIncludesPaymentChannelAndTodayStats(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	notifier := &captureTicketNotifier{}
	service := NewDBService(config.Config{}, db).WithAdminNotifier(notifier)

	mock.ExpectQuery(`SELECT COALESCE\(NULLIF\(name, ''\), NULLIF\(payment, ''\), '未知通道'\)\s+FROM v2_payment\s+WHERE id = \$1\s+LIMIT 1`).
		WithArgs(int64(8)).
		WillReturnRows(sqlmock.NewRows([]string{"channel"}).AddRow("支付宝 / EPay"))
	mock.ExpectQuery(`SELECT COALESCE\(SUM\(total_amount\), 0\)\s+FROM v2_order\s+WHERE paid_at >= \$1 AND paid_at < \$2\s+AND status = 3`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"total"}).AddRow(int64(32866)))
	mock.ExpectQuery(`SELECT\s+COALESCE\(NULLIF\(p\.name, ''\), NULLIF\(p\.payment, ''\), '余额支付 / 未知通道'\) AS channel,\s+COUNT\(\*\) AS count,\s+COALESCE\(SUM\(o\.total_amount\), 0\) AS total\s+FROM v2_order o\s+LEFT JOIN v2_payment p ON p\.id = o\.payment_id\s+WHERE o\.paid_at >= \$1 AND o\.paid_at < \$2\s+AND o\.status = 3\s+GROUP BY COALESCE\(NULLIF\(p\.name, ''\), NULLIF\(p\.payment, ''\), '余额支付 / 未知通道'\)\s+ORDER BY total DESC, count DESC, channel ASC`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"channel", "count", "total"}).
			AddRow("支付宝 / EPay", int64(12), int64(18866)).
			AddRow("Stripe", int64(3), int64(10000)).
			AddRow("余额支付 / 未知通道", int64(5), int64(4000)))

	if err := service.notifyOrderPaidAdmins(context.Background(), adminPaymentNotification{
		TradeNo:     "T9002",
		TotalAmount: 1299,
		PaymentID:   sql.NullInt64{Int64: 8, Valid: true},
		PaidAt:      sql.NullInt64{Int64: time.Date(2026, 6, 25, 13, 20, 0, 0, time.Local).Unix(), Valid: true},
	}); err != nil {
		t.Fatalf("notify order paid admins: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
	if len(notifier.messages) != 1 {
		t.Fatalf("expected one notification, got %#v", notifier.messages)
	}
	message := notifier.messages[0]
	for _, want := range []string{
		"💰成功收款12.99元",
		"订单号：T9002",
		"支付通道：支付宝 / EPay",
		"今日收款：328.66元",
		"今日通道统计：",
		"- 支付宝 / EPay：188.66元 / 12笔",
		"- Stripe：100元 / 3笔",
		"- 余额支付 / 未知通道：40元 / 5笔",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("expected message to contain %q, got %q", want, message)
		}
	}
}
