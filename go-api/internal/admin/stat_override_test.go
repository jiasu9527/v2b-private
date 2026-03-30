package admin

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestDBServiceGetStatOverrideUsesPaidAtForIncome(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	service := &DBService{db: db}

	expectInt64Query(mock, `SELECT COUNT(*) FROM v2_user WHERE t >= $1`, 1, 11)
	expectInt64Query(mock, `SELECT COALESCE(SUM(total_amount), 0) FROM v2_order WHERE paid_at >= $1 AND paid_at < $2 AND status NOT IN (0, 2)`, 2, 1200)
	expectInt64Query(mock, `SELECT COUNT(*) FROM v2_user WHERE created_at >= $1 AND created_at < $2`, 2, 21)
	expectInt64Query(mock, `SELECT COUNT(*) FROM v2_user WHERE created_at >= $1 AND created_at < $2`, 2, 22)
	expectInt64Query(mock, `SELECT COUNT(*)
FROM v2_user
WHERE created_at >= $1 AND created_at < $2
AND EXISTS (
	SELECT 1
	FROM v2_order
	WHERE v2_order.user_id = v2_user.id
	AND v2_order.user_id IS NOT NULL
	AND v2_order.user_id > 0
	AND v2_order.status NOT IN (0, 2)
)`, 2, 31)
	expectInt64Query(mock, `SELECT COUNT(*)
FROM v2_user
WHERE created_at >= $1 AND created_at < $2
AND EXISTS (
	SELECT 1
	FROM v2_order
	WHERE v2_order.user_id = v2_user.id
	AND v2_order.user_id IS NOT NULL
	AND v2_order.user_id > 0
	AND v2_order.status NOT IN (0, 2)
)`, 2, 32)
	expectInt64Query(mock, `SELECT COUNT(*) FROM v2_user WHERE created_at >= $1 AND created_at < $2`, 2, 41)
	expectInt64Query(mock, `SELECT COUNT(*) FROM v2_ticket WHERE status = 0 AND reply_status = 0`, 0, 51)
	expectInt64Query(mock, `SELECT COUNT(*) FROM v2_order WHERE commission_status = 0 AND invite_user_id IS NOT NULL AND status NOT IN (0, 2) AND commission_balance > 0`, 0, 61)
	expectInt64Query(mock, `SELECT COALESCE(SUM(total_amount), 0) FROM v2_order WHERE paid_at >= $1 AND paid_at < $2 AND status NOT IN (0, 2)`, 2, 2200)
	expectInt64Query(mock, `SELECT COALESCE(SUM(total_amount), 0) FROM v2_order WHERE paid_at >= $1 AND paid_at < $2 AND status NOT IN (0, 2)`, 2, 3200)
	expectInt64Query(mock, `SELECT COALESCE(SUM(get_amount), 0) FROM v2_commission_log WHERE created_at >= $1 AND created_at < $2`, 2, 71)
	expectInt64Query(mock, `SELECT COALESCE(SUM(get_amount), 0) FROM v2_commission_log WHERE created_at >= $1 AND created_at < $2`, 2, 72)

	result, err := service.GetStatOverride(context.Background())
	if err != nil {
		t.Fatalf("get stat override: %v", err)
	}

	if result["month_income"] != int64(1200) {
		t.Fatalf("unexpected month_income: %#v", result["month_income"])
	}
	if result["day_income"] != int64(2200) {
		t.Fatalf("unexpected day_income: %#v", result["day_income"])
	}
	if result["last_month_income"] != int64(3200) {
		t.Fatalf("unexpected last_month_income: %#v", result["last_month_income"])
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func expectInt64Query(mock sqlmock.Sqlmock, query string, argsCount int, value int64) {
	expected := mock.ExpectQuery(regexp.QuoteMeta(query))
	switch argsCount {
	case 0:
	case 1:
		expected.WithArgs(sqlmock.AnyArg())
	case 2:
		expected.WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg())
	default:
		panic("unsupported args count")
	}
	expected.WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(value))
}
