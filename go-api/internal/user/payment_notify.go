package user

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"
)

const unknownPaymentChannel = "余额支付 / 未知通道"

// Admin statistics and Telegram delivery are best-effort side effects. Keep
// them off the provider callback response path and bound the number of helper
// goroutines if the shared queue is saturated. This limit never delays or
// rejects payment settlement itself.
var orderPaidNotificationSlots = make(chan struct{}, 8)

type adminPaymentNotification struct {
	TradeNo     string
	TotalAmount int64
	PaymentID   sql.NullInt64
	PaidAt      sql.NullInt64
}

type paymentChannelStat struct {
	Channel string
	Count   int64
	Total   int64
}

func (s *DBService) notifyOrderPaidAdmins(ctx context.Context, notice adminPaymentNotification) error {
	if s == nil || s.notifier == nil {
		return nil
	}

	tradeNo := strings.TrimSpace(notice.TradeNo)
	if tradeNo == "" {
		return nil
	}

	message := s.buildOrderPaidAdminMessage(ctx, notice)
	return s.notifier.NotifyAdmins(ctx, message, false)
}

func (s *DBService) dispatchOrderPaidAdminNotification(notice adminPaymentNotification) {
	if s == nil || s.notifier == nil {
		return
	}
	select {
	case orderPaidNotificationSlots <- struct{}{}:
	default:
		log.Printf("payment admin notification skipped: queue saturated trade_no=%q", strings.TrimSpace(notice.TradeNo))
		return
	}

	go func() {
		defer func() { <-orderPaidNotificationSlots }()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := s.notifyOrderPaidAdmins(ctx, notice); err != nil {
			log.Printf("payment admin notification failed trade_no=%q err=%q", strings.TrimSpace(notice.TradeNo), err.Error())
		}
	}()
}

func (s *DBService) buildOrderPaidAdminMessage(ctx context.Context, notice adminPaymentNotification) string {
	tradeNo := strings.TrimSpace(notice.TradeNo)
	amount := formatCNYCents(notice.TotalAmount)

	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("💰成功收款%s元\n———————————————\n订单号：%s", amount, tradeNo))

	if s == nil || s.db == nil {
		return builder.String()
	}

	channel := unknownPaymentChannel
	if notice.PaymentID.Valid && notice.PaymentID.Int64 > 0 {
		if name, err := s.loadPaymentChannelName(ctx, notice.PaymentID.Int64); err == nil && strings.TrimSpace(name) != "" {
			channel = strings.TrimSpace(name)
		}
	}
	builder.WriteString("\n支付通道：")
	builder.WriteString(channel)

	start, end := paidDayRange(notice.PaidAt)
	if total, err := s.loadTodayPaidTotal(ctx, start, end); err == nil {
		builder.WriteString("\n\n今日收款：")
		builder.WriteString(formatCNYCents(total))
		builder.WriteString("元")
	}
	if stats, err := s.loadTodayPaymentChannelStats(ctx, start, end); err == nil && len(stats) > 0 {
		builder.WriteString("\n\n今日通道统计：")
		for _, stat := range stats {
			builder.WriteString("\n- ")
			builder.WriteString(stat.Channel)
			builder.WriteString("：")
			builder.WriteString(formatCNYCents(stat.Total))
			builder.WriteString("元 / ")
			builder.WriteString(strconv.FormatInt(stat.Count, 10))
			builder.WriteString("笔")
		}
	}
	return builder.String()
}

func (s *DBService) loadPaymentChannelName(ctx context.Context, paymentID int64) (string, error) {
	var channel string
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(NULLIF(name, ''), NULLIF(payment, ''), '未知通道')
FROM v2_payment
WHERE id = $1
LIMIT 1`, paymentID).Scan(&channel)
	if err != nil {
		return "", fmt.Errorf("query payment channel: %w", err)
	}
	return channel, nil
}

func (s *DBService) loadTodayPaidTotal(ctx context.Context, start, end int64) (int64, error) {
	var total int64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(total_amount), 0)
FROM v2_order
WHERE paid_at >= $1 AND paid_at < $2
AND status = 3`, start, end).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("query today paid total: %w", err)
	}
	return total, nil
}

func (s *DBService) loadTodayPaymentChannelStats(ctx context.Context, start, end int64) ([]paymentChannelStat, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT
COALESCE(NULLIF(p.name, ''), NULLIF(p.payment, ''), '余额支付 / 未知通道') AS channel,
COUNT(*) AS count,
COALESCE(SUM(o.total_amount), 0) AS total
FROM v2_order o
LEFT JOIN v2_payment p ON p.id = o.payment_id
WHERE o.paid_at >= $1 AND o.paid_at < $2
AND o.status = 3
GROUP BY COALESCE(NULLIF(p.name, ''), NULLIF(p.payment, ''), '余额支付 / 未知通道')
ORDER BY total DESC, count DESC, channel ASC`, start, end)
	if err != nil {
		return nil, fmt.Errorf("query today payment channel stats: %w", err)
	}
	defer rows.Close()

	stats := make([]paymentChannelStat, 0)
	for rows.Next() {
		var stat paymentChannelStat
		if err := rows.Scan(&stat.Channel, &stat.Count, &stat.Total); err != nil {
			return nil, fmt.Errorf("scan today payment channel stat: %w", err)
		}
		stat.Channel = strings.TrimSpace(stat.Channel)
		if stat.Channel == "" {
			stat.Channel = unknownPaymentChannel
		}
		stats = append(stats, stat)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate today payment channel stats: %w", err)
	}
	return stats, nil
}

func paidDayRange(paidAt sql.NullInt64) (int64, int64) {
	now := time.Now()
	if paidAt.Valid && paidAt.Int64 > 0 {
		now = time.Unix(paidAt.Int64, 0)
	}
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	return start.Unix(), start.AddDate(0, 0, 1).Unix()
}

func formatCNYCents(cents int64) string {
	return strconv.FormatFloat(float64(cents)/100, 'f', -1, 64)
}
