package appleid

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strings"
	"time"
)

// AdminFinanceRequest uses inclusive calendar dates in the server's timezone.
// An omitted range means the last 30 calendar days, including today.
type AdminFinanceRequest struct {
	StartDate string `json:"start_date"`
	EndDate   string `json:"end_date"`
	ProductID *int64 `json:"product_id,omitempty"`
	Current   int64  `json:"current"`
	PageSize  int64  `json:"page_size"`
}

type AdminFinanceSummary struct {
	PaidCount   int64 `json:"paid_count"`
	PaidTotal   int64 `json:"paid_total"`
	RefundCount int64 `json:"refund_count"`
	RefundTotal int64 `json:"refund_total"`
	NetTotal    int64 `json:"net_total"`
}

type AdminFinanceDaily struct {
	Date string `json:"date"`
	AdminFinanceSummary
}

type AdminFinanceStatsResult struct {
	StartDate string              `json:"start_date"`
	EndDate   string              `json:"end_date"`
	Timezone  string              `json:"timezone"`
	Summary   AdminFinanceSummary `json:"summary"`
	Daily     []AdminFinanceDaily `json:"daily"`
}

type AdminFinanceTransaction struct {
	ID              string  `json:"id"`
	OrderID         int64   `json:"order_id"`
	TradeNo         string  `json:"trade_no"`
	UserEmail       string  `json:"user_email"`
	ProductID       int64   `json:"product_id"`
	ProductName     string  `json:"product_name"`
	EventType       string  `json:"event_type"`
	Amount          int64   `json:"amount"`
	OccurredAt      int64   `json:"occurred_at"`
	OccurredAtLocal string  `json:"occurred_at_local"`
	Status          int64   `json:"status"`
	PaymentID       *int64  `json:"payment_id,omitempty"`
	CallbackNo      *string `json:"callback_no,omitempty"`
}

type AdminFinanceTransactionsResult struct {
	Data  []AdminFinanceTransaction `json:"data"`
	Total int64                     `json:"total"`
}

type financeDay struct {
	date       string
	start, end int64
}

type financeRange struct {
	startDate, endDate, timezone string
	location                     *time.Location
	days                         []financeDay
	productID                    *int64
	current, pageSize            int64
}

func normalizeAdminFinanceRequest(req AdminFinanceRequest, now time.Time, loc *time.Location) (financeRange, error) {
	if req.ProductID != nil && *req.ProductID <= 0 || req.Current < 0 || req.PageSize < 0 || req.PageSize > adminMaxPageSize {
		return financeRange{}, ErrInvalidParameter
	}
	current, pageSize := req.Current, req.PageSize
	if current == 0 {
		current = 1
	}
	if pageSize == 0 {
		pageSize = adminDefaultPageSize
	}
	if current-1 > math.MaxInt64/pageSize {
		return financeRange{}, ErrInvalidParameter
	}
	var start, end time.Time
	if req.StartDate == "" && req.EndDate == "" {
		localNow := now.In(loc)
		end = time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, loc)
		start = end.AddDate(0, 0, -29)
	} else {
		if req.StartDate == "" || req.EndDate == "" {
			return financeRange{}, ErrInvalidParameter
		}
		var err error
		start, err = time.ParseInLocation(time.DateOnly, req.StartDate, loc)
		if err != nil || start.Format(time.DateOnly) != req.StartDate {
			return financeRange{}, ErrInvalidParameter
		}
		end, err = time.ParseInLocation(time.DateOnly, req.EndDate, loc)
		if err != nil || end.Format(time.DateOnly) != req.EndDate || end.Before(start) {
			return financeRange{}, ErrInvalidParameter
		}
	}
	result := financeRange{
		startDate: start.Format(time.DateOnly), endDate: end.Format(time.DateOnly), timezone: loc.String(),
		location: loc, productID: req.ProductID, current: current, pageSize: pageSize,
	}
	if result.timezone == "Local" {
		result.timezone = "Local (UTC" + now.In(loc).Format("-07:00") + ")"
	}
	for day := start; !day.After(end); day = day.AddDate(0, 0, 1) {
		if len(result.days) >= 366 {
			return financeRange{}, ErrInvalidParameter
		}
		next := day.AddDate(0, 0, 1)
		result.days = append(result.days, financeDay{date: day.Format(time.DateOnly), start: day.Unix(), end: next.Unix()})
	}
	return result, nil
}

func (s *DBService) prepareAdminFinance(ctx context.Context, req AdminFinanceRequest) (financeRange, error) {
	window, err := normalizeAdminFinanceRequest(req, time.Now(), time.Local)
	if err != nil {
		return financeRange{}, err
	}
	if err := s.ensureSchema(ctx); err != nil {
		return financeRange{}, err
	}
	if window.productID != nil {
		var exists bool
		if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM v2_apple_product WHERE id=$1)`, *window.productID).Scan(&exists); err != nil {
			return financeRange{}, fmt.Errorf("find apple finance product: %w", err)
		}
		if !exists {
			return financeRange{}, ErrProductNotFound
		}
	}
	return window, nil
}

// financeEventsCTE keeps receipts and refunds independent. The refund date is
// the first confirmed external refund, never the payment date. Grouping the
// audit first prevents duplicate audit entries from multiplying order amounts.
func financeEventsCTE(window financeRange) (string, []any) {
	args := []any{window.days[0].start, window.days[len(window.days)-1].end}
	productWhere, refundProductWhere := "", ""
	if window.productID != nil {
		args = append(args, *window.productID)
		productWhere = " AND o.product_id=$3"
		refundProductWhere = " AND ro.product_id=$3"
	}
	query := `WITH refund_times AS (
SELECT a.order_id,MIN(a.created_at) AS refunded_at
FROM v2_apple_order_audit a
JOIN v2_apple_order ro ON ro.id=a.order_id AND ro.status=3
WHERE a.action='refund_confirmed'` + refundProductWhere + `
GROUP BY a.order_id
), finance_events AS (
SELECT o.id AS order_id,o.user_id,o.trade_no,o.product_id,o.product_name,
'payment'::text AS event_type,(o.price::bigint+o.handling_amount::bigint) AS amount,
o.paid_at AS occurred_at,o.status,o.payment_id,o.callback_no
FROM v2_apple_order o
WHERE o.status IN (1,3,4) AND o.paid_at >= $1 AND o.paid_at < $2` + productWhere + `
UNION ALL
SELECT o.id AS order_id,o.user_id,o.trade_no,o.product_id,o.product_name,
'refund'::text AS event_type,(o.price::bigint+o.handling_amount::bigint) AS amount,
COALESCE(r.refunded_at,o.updated_at) AS occurred_at,o.status,o.payment_id,o.callback_no
FROM v2_apple_order o
LEFT JOIN refund_times r ON r.order_id=o.id
WHERE o.status=3 AND COALESCE(r.refunded_at,o.updated_at) >= $1
AND COALESCE(r.refunded_at,o.updated_at) < $2` + productWhere + `
)`
	return query, args
}

func financeStatsQuery(window financeRange) (string, []any) {
	query, args := financeEventsCTE(window)
	values := make([]string, 0, len(window.days))
	for _, day := range window.days {
		first := len(args) + 1
		values = append(values, fmt.Sprintf("($%d::text,$%d::bigint,$%d::bigint)", first, first+1, first+2))
		args = append(args, day.date, day.start, day.end)
	}
	query += `, finance_days(date,start_at,end_at) AS (VALUES ` + strings.Join(values, ",") + `)
SELECT d.date,
COUNT(*) FILTER (WHERE e.event_type='payment') AS paid_count,
COALESCE(SUM(e.amount) FILTER (WHERE e.event_type='payment'),0)::bigint AS paid_total,
COUNT(*) FILTER (WHERE e.event_type='refund') AS refund_count,
COALESCE(SUM(e.amount) FILTER (WHERE e.event_type='refund'),0)::bigint AS refund_total
FROM finance_days d LEFT JOIN finance_events e ON e.occurred_at >= d.start_at AND e.occurred_at < d.end_at
GROUP BY d.date ORDER BY d.date`
	return query, args
}

// AdminFinanceStats performs one aggregate query. Summary is derived from that
// query's daily rows, so both representations always describe the same snapshot.
func (s *DBService) AdminFinanceStats(ctx context.Context, req AdminFinanceRequest) (AdminFinanceStatsResult, error) {
	window, err := s.prepareAdminFinance(ctx, req)
	if err != nil {
		return AdminFinanceStatsResult{}, err
	}
	query, args := financeStatsQuery(window)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return AdminFinanceStatsResult{}, fmt.Errorf("query apple finance stats: %w", err)
	}
	defer rows.Close()
	result := AdminFinanceStatsResult{
		StartDate: window.startDate, EndDate: window.endDate, Timezone: window.timezone,
		Daily: make([]AdminFinanceDaily, 0, len(window.days)),
	}
	for rows.Next() {
		var day AdminFinanceDaily
		if err := rows.Scan(&day.Date, &day.PaidCount, &day.PaidTotal, &day.RefundCount, &day.RefundTotal); err != nil {
			return AdminFinanceStatsResult{}, fmt.Errorf("scan apple finance stats: %w", err)
		}
		day.NetTotal = day.PaidTotal - day.RefundTotal
		if err := addFinanceSummary(&result.Summary, day.AdminFinanceSummary); err != nil {
			return AdminFinanceStatsResult{}, err
		}
		result.Daily = append(result.Daily, day)
	}
	if err := rows.Err(); err != nil {
		return AdminFinanceStatsResult{}, err
	}
	return result, nil
}

func addFinanceSummary(total *AdminFinanceSummary, day AdminFinanceSummary) error {
	for _, pair := range [][2]int64{
		{total.PaidCount, day.PaidCount}, {total.PaidTotal, day.PaidTotal},
		{total.RefundCount, day.RefundCount}, {total.RefundTotal, day.RefundTotal},
	} {
		if pair[0] < 0 || pair[1] < 0 || pair[0] > math.MaxInt64-pair[1] {
			return fmt.Errorf("apple finance total exceeds supported range")
		}
	}
	total.PaidCount += day.PaidCount
	total.PaidTotal += day.PaidTotal
	total.RefundCount += day.RefundCount
	total.RefundTotal += day.RefundTotal
	total.NetTotal = total.PaidTotal - total.RefundTotal
	return nil
}

func (s *DBService) AdminFinanceTransactions(ctx context.Context, req AdminFinanceRequest) (AdminFinanceTransactionsResult, error) {
	window, err := s.prepareAdminFinance(ctx, req)
	if err != nil {
		return AdminFinanceTransactionsResult{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return AdminFinanceTransactionsResult{}, err
	}
	defer tx.Rollback()
	query, args := financeEventsCTE(window)
	result := AdminFinanceTransactionsResult{Data: make([]AdminFinanceTransaction, 0)}
	if err := tx.QueryRowContext(ctx, query+` SELECT COUNT(*) FROM finance_events`, args...).Scan(&result.Total); err != nil {
		return AdminFinanceTransactionsResult{}, fmt.Errorf("count apple finance transactions: %w", err)
	}
	args = append(args, window.pageSize, (window.current-1)*window.pageSize)
	query += fmt.Sprintf(` SELECT e.order_id,e.trade_no,COALESCE(u.email,''),e.product_id,e.product_name,
e.event_type,e.amount,e.occurred_at,e.status,e.payment_id,e.callback_no
FROM finance_events e LEFT JOIN v2_user u ON u.id=e.user_id
ORDER BY e.occurred_at DESC,e.order_id DESC,e.event_type DESC LIMIT $%d OFFSET $%d`, len(args)-1, len(args))
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return AdminFinanceTransactionsResult{}, fmt.Errorf("query apple finance transactions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var item AdminFinanceTransaction
		var paymentID sql.NullInt64
		var callbackNo sql.NullString
		if err := rows.Scan(&item.OrderID, &item.TradeNo, &item.UserEmail, &item.ProductID, &item.ProductName,
			&item.EventType, &item.Amount, &item.OccurredAt, &item.Status, &paymentID, &callbackNo); err != nil {
			return AdminFinanceTransactionsResult{}, fmt.Errorf("scan apple finance transaction: %w", err)
		}
		item.ID = fmt.Sprintf("%s:%d", item.EventType, item.OrderID)
		item.OccurredAtLocal = time.Unix(item.OccurredAt, 0).In(window.location).Format("2006-01-02 15:04:05 -07:00")
		if paymentID.Valid {
			item.PaymentID = &paymentID.Int64
		}
		if callbackNo.Valid {
			item.CallbackNo = &callbackNo.String
		}
		result.Data = append(result.Data, item)
	}
	if err := rows.Err(); err != nil {
		return AdminFinanceTransactionsResult{}, err
	}
	if err := rows.Close(); err != nil {
		return AdminFinanceTransactionsResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return AdminFinanceTransactionsResult{}, err
	}
	return result, nil
}
