package user

import (
	"context"
	"testing"
	"time"

	"forest/go-api/internal/config"
	"github.com/DATA-DOG/go-sqlmock"
)

func TestHandlePendingOrdersCleansExpiredOrders(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	service := NewDBService(config.Config{
		CommissionAutoCheckEnable: false,
		OrderKeepDays:             30,
	}, db)

	now := time.Now().Unix()
	old := now - 31*86400

	mock.ExpectQuery(`SELECT trade_no\s+FROM v2_order\s+WHERE status IN \(0, 1\)`).
		WithArgs(orderHandleBatchLimit).
		WillReturnRows(sqlmock.NewRows([]string{"trade_no"}))
	mock.ExpectQuery(`SELECT id, user_id, status, surplus_order_ids\s+FROM v2_order\s+WHERE updated_at <= \$2 AND status IN \(2, 3, 4\)\s+ORDER BY updated_at ASC\s+LIMIT \$1`).
		WithArgs(orderHandleBatchLimit, sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "user_id", "status", "surplus_order_ids"}).
			AddRow(int64(10), int64(1), int64(2), nil).
			AddRow(int64(11), int64(1), int64(3), nil).
			AddRow(int64(12), int64(1), int64(4), `[12]`).
			AddRow(int64(20), int64(2), int64(3), `[21]`).
			AddRow(int64(21), int64(2), int64(4), nil),
		)
	mock.ExpectQuery(`SELECT id, updated_at, surplus_order_ids\s+FROM v2_order\s+WHERE user_id = \$1 AND status = 3\s+ORDER BY COALESCE\(paid_at, created_at\) DESC, id DESC LIMIT 1`).
		WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "updated_at", "surplus_order_ids"}).
			AddRow(int64(13), now, `[12]`),
		)
	mock.ExpectQuery(`SELECT id, updated_at, surplus_order_ids\s+FROM v2_order\s+WHERE user_id = \$1 AND status = 3\s+ORDER BY COALESCE\(paid_at, created_at\) DESC, id DESC LIMIT 1`).
		WithArgs(int64(2)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "updated_at", "surplus_order_ids"}).
			AddRow(int64(20), old, `[21]`),
		)
	mock.ExpectExec(`DELETE FROM v2_order WHERE id IN \(\$1,\$2,\$3,\$4\)`).
		WithArgs(int64(10), int64(11), int64(20), int64(21)).
		WillReturnResult(sqlmock.NewResult(0, 4))

	if err := service.HandlePendingOrders(context.Background()); err != nil {
		t.Fatalf("handle pending orders: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}
