package appleid

import (
	"context"
	"database/sql/driver"
	"errors"
	"math"
	"reflect"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestFinanceDateRangeUsesCalendarDaysAcrossDST(t *testing.T) {
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		start, end string
		hours      []int64
	}{
		{"2026-03-07", "2026-03-09", []int64{24, 23, 24}},
		{"2026-10-31", "2026-11-02", []int64{24, 25, 24}},
	} {
		t.Run(tt.start, func(t *testing.T) {
			window, err := normalizeAdminFinanceRequest(AdminFinanceRequest{StartDate: tt.start, EndDate: tt.end}, time.Now(), loc)
			if err != nil {
				t.Fatal(err)
			}
			var hours []int64
			for i, day := range window.days {
				hours = append(hours, (day.end-day.start)/3600)
				if i > 0 && day.start != window.days[i-1].end {
					t.Fatal("calendar days must be contiguous")
				}
			}
			if !reflect.DeepEqual(hours, tt.hours) || window.timezone != "America/Los_Angeles" {
				t.Fatalf("hours = %v, timezone = %s", hours, window.timezone)
			}
		})
	}
	// The UTC date is April 1; the server-local date remains March 31.
	now := time.Date(2026, 4, 1, 2, 0, 0, 0, time.UTC)
	window, err := normalizeAdminFinanceRequest(AdminFinanceRequest{}, now, loc)
	if err != nil {
		t.Fatal(err)
	}
	if window.startDate != "2026-03-02" || window.endDate != "2026-03-31" || len(window.days) != 30 {
		t.Fatalf("unexpected default range: %+v", window)
	}
}

func TestFinanceRangeRejectsInvalidFilters(t *testing.T) {
	badProduct := int64(0)
	tests := []AdminFinanceRequest{
		{StartDate: "2026-01-01"}, {EndDate: "2026-01-01"},
		{StartDate: "2026-1-01", EndDate: "2026-01-02"},
		{StartDate: " 2026-01-01", EndDate: "2026-01-02"},
		{StartDate: "2026-02-29", EndDate: "2026-03-01"},
		{StartDate: "2026-01-03", EndDate: "2026-01-02"},
		{StartDate: "2024-01-01", EndDate: "2025-01-01"},
		{ProductID: &badProduct}, {Current: -1}, {PageSize: -1}, {PageSize: 201},
		{Current: math.MaxInt64, PageSize: 2},
	}
	service, mock := newAdminTestService(t)
	for _, req := range tests {
		if _, err := service.AdminFinanceStats(context.Background(), req); !errors.Is(err, ErrInvalidParameter) {
			t.Errorf("stats request %+v error = %v", req, err)
		}
		if _, err := service.AdminFinanceTransactions(context.Background(), req); !errors.Is(err, ErrInvalidParameter) {
			t.Errorf("transactions request %+v error = %v", req, err)
		}
	}
	window, err := normalizeAdminFinanceRequest(AdminFinanceRequest{StartDate: "2024-01-01", EndDate: "2024-12-31"}, time.Now(), time.UTC)
	if err != nil || len(window.days) != 366 {
		t.Fatalf("366-day leap-year range rejected: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestFinanceRejectsUnknownProduct(t *testing.T) {
	for _, stats := range []bool{true, false} {
		service, mock := newAdminTestService(t)
		productID := int64(99)
		mock.ExpectQuery(regexp.QuoteMeta(`SELECT EXISTS(SELECT 1 FROM v2_apple_product WHERE id=$1)`)).
			WithArgs(productID).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
		var err error
		if stats {
			_, err = service.AdminFinanceStats(context.Background(), AdminFinanceRequest{ProductID: &productID})
		} else {
			_, err = service.AdminFinanceTransactions(context.Background(), AdminFinanceRequest{ProductID: &productID})
		}
		if !errors.Is(err, ErrProductNotFound) {
			t.Fatalf("error = %v, want ErrProductNotFound", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	}
}

func financeDriverArgs(args []any) []driver.Value {
	values := make([]driver.Value, len(args))
	for i, arg := range args {
		values[i] = arg
	}
	return values
}

func TestFinanceStatsSummaryMatchesDailyCentsAndRefundOnlyDays(t *testing.T) {
	service, mock := newAdminTestService(t)
	productID := int64(7)
	req := AdminFinanceRequest{StartDate: "2026-03-01", EndDate: "2026-03-03", ProductID: &productID}
	window, err := normalizeAdminFinanceRequest(req, time.Now(), time.Local)
	if err != nil {
		t.Fatal(err)
	}
	query, args := financeStatsQuery(window)
	mock.ExpectQuery(`SELECT EXISTS`).WithArgs(productID).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	// The third day has only a refund for a payment outside this date range.
	// Amounts deliberately exceed a signed 32-bit integer and include odd cents.
	mock.ExpectQuery(regexp.QuoteMeta(query)).WithArgs(financeDriverArgs(args)...).
		WillReturnRows(sqlmock.NewRows([]string{"date", "paid_count", "paid_total", "refund_count", "refund_total"}).
			AddRow("2026-03-01", int64(2), int64(3000000007), int64(0), int64(0)).
			AddRow("2026-03-02", int64(0), int64(0), int64(0), int64(0)).
			AddRow("2026-03-03", int64(0), int64(0), int64(1), int64(101)))
	result, err := service.AdminFinanceStats(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	want := AdminFinanceSummary{PaidCount: 2, PaidTotal: 3000000007, RefundCount: 1, RefundTotal: 101, NetTotal: 2999999906}
	if result.Summary != want || len(result.Daily) != 3 || result.Daily[1].NetTotal != 0 || result.Daily[2].NetTotal != -101 {
		t.Fatalf("unexpected finance stats: %+v", result)
	}
	if result.StartDate != req.StartDate || result.EndDate != req.EndDate || result.Timezone == "" {
		t.Fatalf("missing date-range metadata: %+v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestFinanceTransactionsPagedWithDistinctEventIDs(t *testing.T) {
	service, mock := newAdminTestService(t)
	req := AdminFinanceRequest{StartDate: "2026-04-01", EndDate: "2026-04-02", Current: 2, PageSize: 2}
	window, err := normalizeAdminFinanceRequest(req, time.Now(), time.Local)
	if err != nil {
		t.Fatal(err)
	}
	_, args := financeEventsCTE(window)
	refundedAt := window.days[1].start + 100
	paidAt := window.days[0].start + 100
	mock.ExpectBegin()
	mock.ExpectQuery(`WITH refund_times[\s\S]*SELECT COUNT\(\*\) FROM finance_events`).WithArgs(financeDriverArgs(args)...).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(4)))
	pageArgs := append(financeDriverArgs(args), int64(2), int64(2))
	mock.ExpectQuery(`WITH refund_times[\s\S]*ORDER BY e.occurred_at DESC,e.order_id DESC,e.event_type DESC LIMIT \$3 OFFSET \$4`).WithArgs(pageArgs...).
		WillReturnRows(sqlmock.NewRows([]string{"order_id", "trade_no", "email", "product_id", "product_name", "event_type", "amount", "occurred_at", "status", "payment_id", "callback_no"}).
			AddRow(int64(41), "trade-41", "user@example.com", int64(7), "US ID", "refund", int64(3000000007), refundedAt, OrderRefunded, nil, nil).
			AddRow(int64(41), "trade-41", "user@example.com", int64(7), "US ID", "payment", int64(3000000007), paidAt, OrderRefunded, int64(3), "callback-41"))
	mock.ExpectCommit()
	result, err := service.AdminFinanceTransactions(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 4 || len(result.Data) != 2 || result.Data[0].ID != "refund:41" || result.Data[1].ID != "payment:41" {
		t.Fatalf("unexpected transactions: %+v", result)
	}
	if result.Data[0].PaymentID != nil || result.Data[0].CallbackNo != nil || result.Data[1].PaymentID == nil || *result.Data[1].PaymentID != 3 || *result.Data[1].CallbackNo != "callback-41" {
		t.Fatal("payment references were not preserved")
	}
	if result.Data[0].OccurredAtLocal != time.Unix(refundedAt, 0).In(time.Local).Format("2006-01-02 15:04:05 -07:00") {
		t.Fatalf("incorrect server-local event time: %+v", result.Data[0])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestFinanceTransactionsRollbackOnReadFailure(t *testing.T) {
	service, mock := newAdminTestService(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`WITH refund_times`).WillReturnError(errors.New("database unavailable"))
	mock.ExpectRollback()
	if _, err := service.AdminFinanceTransactions(context.Background(), AdminFinanceRequest{}); err == nil {
		t.Fatal("expected read failure")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestFinanceSummaryRejectsOverflow(t *testing.T) {
	total := AdminFinanceSummary{PaidTotal: math.MaxInt64}
	if err := addFinanceSummary(&total, AdminFinanceSummary{PaidTotal: 1}); err == nil {
		t.Fatal("expected overflow failure")
	}
	if total.PaidTotal != math.MaxInt64 {
		t.Fatal("overflow must not mutate the previous total")
	}
}
