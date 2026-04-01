package admin

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestLegacyStatWindowsBackfillRecentDaysExcludingToday(t *testing.T) {
	now := time.Unix(1_711_699_600, 0).UTC()

	windows := legacyStatWindows(now)
	if len(windows) != 7 {
		t.Fatalf("expected seven stat windows, got %#v", windows)
	}
	if windows[0].recordAt != 1711065600 || windows[0].startAt != 1711065600 || windows[0].endAt != 1711152000 {
		t.Fatalf("unexpected oldest backfill window: %#v", windows[0])
	}
	if windows[6].recordAt != 1711584000 || windows[6].startAt != 1711584000 || windows[6].endAt != 1711670400 {
		t.Fatalf("unexpected newest backfill window: %#v", windows[6])
	}
}

func TestRefreshLegacyStatsBackfillsRecentDaysAndDeletesTodaySnapshot(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	service := &DBService{db: db}

	mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM v2_stat WHERE record_at = $1`)).
		WithArgs(sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	for i := 0; i < 7; i++ {
		expectLegacyStatSummaryQueries(mock)
		expectLegacyStatUpsert(mock)
	}

	if err := service.RefreshLegacyStats(context.Background()); err != nil {
		t.Fatalf("refresh legacy stats: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func expectLegacyStatSummaryQueries(mock sqlmock.Sqlmock) {
	expectInt64Query(mock, `SELECT COUNT(*) FROM v2_order WHERE created_at >= $1 AND created_at < $2`, 2, 10)
	expectInt64Query(mock, `SELECT COALESCE(SUM(total_amount), 0) FROM v2_order WHERE created_at >= $1 AND created_at < $2`, 2, 1000)
	expectInt64Query(mock, `SELECT COUNT(*) FROM v2_order WHERE paid_at >= $1 AND paid_at < $2 AND status NOT IN (0, 2)`, 2, 8)
	expectInt64Query(mock, `SELECT COALESCE(SUM(total_amount), 0) FROM v2_order WHERE paid_at >= $1 AND paid_at < $2 AND status NOT IN (0, 2)`, 2, 800)
	expectInt64Query(mock, `SELECT COUNT(*) FROM v2_commission_log WHERE created_at >= $1 AND created_at < $2`, 2, 2)
	expectInt64Query(mock, `SELECT COALESCE(SUM(get_amount), 0) FROM v2_commission_log WHERE created_at >= $1 AND created_at < $2`, 2, 200)
	expectInt64Query(mock, `SELECT COUNT(*) FROM v2_user WHERE created_at >= $1 AND created_at < $2`, 2, 5)
	expectInt64Query(mock, `SELECT COUNT(*) FROM v2_user WHERE created_at >= $1 AND created_at < $2 AND invite_user_id IS NOT NULL`, 2, 3)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT CAST(COALESCE(SUM(u) + SUM(d), 0) AS text) FROM v2_stat_server WHERE created_at >= $1 AND created_at < $2`)).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow("4096"))
}

func expectLegacyStatUpsert(mock sqlmock.Sqlmock) {
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO v2_stat (
record_at, record_type, order_count, order_total, commission_count, commission_total,
paid_count, paid_total, register_count, invite_count, transfer_used_total, created_at, updated_at
) VALUES (
$1, 'd', $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $11
)
ON CONFLICT (record_at) DO UPDATE SET
order_count = EXCLUDED.order_count,
order_total = EXCLUDED.order_total,
commission_count = EXCLUDED.commission_count,
commission_total = EXCLUDED.commission_total,
paid_count = EXCLUDED.paid_count,
paid_total = EXCLUDED.paid_total,
register_count = EXCLUDED.register_count,
invite_count = EXCLUDED.invite_count,
transfer_used_total = EXCLUDED.transfer_used_total,
updated_at = EXCLUDED.updated_at`)).
		WithArgs(
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
		).
		WillReturnResult(sqlmock.NewResult(1, 1))
}
