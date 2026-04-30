package user

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"forest/go-api/internal/config"
	"github.com/DATA-DOG/go-sqlmock"
)

const trafficResetCycleKeyPrefix = "TRAFFIC_RESET_CYCLE_USER_"

func expectNoTrafficResetCandidates(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(`SELECT u.id, u.plan_id, u.u, u.d, COALESCE\(u.expired_at, 0\), u.updated_at, p.reset_traffic_method\s+FROM v2_user u\s+JOIN v2_plan p ON p.id = u.plan_id\s+WHERE u.plan_id IS NOT NULL AND \(u.expired_at > \$1 OR u.expired_at IS NULL OR u.expired_at <= 0\)\s+ORDER BY u.id ASC\s+LIMIT \$2`).
		WithArgs(sqlmock.AnyArg(), orderHandleBatchLimit).
		WillReturnRows(sqlmock.NewRows([]string{"id", "plan_id", "u", "d", "expired_at", "updated_at", "reset_traffic_method"}))
}

func TestHandlePendingOrdersBackfillsMonthlyTrafficReset(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	service := NewDBService(config.Config{
		CommissionAutoCheckEnable: false,
		OrderKeepDays:             0,
		ResetTrafficMethod:        0,
	}, db)

	now := time.Now()
	expiredAt := now.Add(7 * 24 * time.Hour).Unix()
	cycleStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()).Unix()
	staleUpdatedAt := cycleStart - 3600
	expectedMarker := fmt.Sprintf("plan:%d|method:%d|start:%d", int64(7), int64(0), cycleStart)

	mock.ExpectQuery(`SELECT trade_no\s+FROM v2_order\s+WHERE status IN \(0, 1\)`).
		WithArgs(orderHandleBatchLimit).
		WillReturnRows(sqlmock.NewRows([]string{"trade_no"}))
	mock.ExpectQuery(`SELECT u.id, u.plan_id, u.u, u.d, COALESCE\(u.expired_at, 0\), u.updated_at, p.reset_traffic_method\s+FROM v2_user u\s+JOIN v2_plan p ON p.id = u.plan_id\s+WHERE u.plan_id IS NOT NULL AND \(u.expired_at > \$1 OR u.expired_at IS NULL OR u.expired_at <= 0\)\s+ORDER BY u.id ASC\s+LIMIT \$2`).
		WithArgs(sqlmock.AnyArg(), orderHandleBatchLimit).
		WillReturnRows(sqlmock.NewRows([]string{"id", "plan_id", "u", "d", "expired_at", "updated_at", "reset_traffic_method"}).
			AddRow(int64(10), int64(7), int64(123), int64(456), expiredAt, staleUpdatedAt, nil),
		)
	mock.ExpectQuery(`SELECT v, expire_at FROM v2_runtime_kv WHERE k = \$1 LIMIT 1`).
		WithArgs(trafficResetCycleKeyPrefix + "10").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(`UPDATE v2_user SET u = 0, d = 0, updated_at = \$2 WHERE id = \$1`).
		WithArgs(int64(10), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO v2_runtime_kv \(k, v, expire_at, created_at, updated_at\)`).
		WithArgs(trafficResetCycleKeyPrefix+"10", expectedMarker, sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := service.HandlePendingOrders(context.Background()); err != nil {
		t.Fatalf("handle pending orders: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestHandlePendingOrdersSkipsTrafficResetWhenCycleAlreadyApplied(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	service := NewDBService(config.Config{
		CommissionAutoCheckEnable: false,
		OrderKeepDays:             0,
		ResetTrafficMethod:        0,
	}, db)

	now := time.Now()
	expiredAt := now.Add(7 * 24 * time.Hour).Unix()
	cycleStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()).Unix()
	expectedMarker := fmt.Sprintf("plan:%d|method:%d|start:%d", int64(7), int64(0), cycleStart)

	mock.ExpectQuery(`SELECT trade_no\s+FROM v2_order\s+WHERE status IN \(0, 1\)`).
		WithArgs(orderHandleBatchLimit).
		WillReturnRows(sqlmock.NewRows([]string{"trade_no"}))
	mock.ExpectQuery(`SELECT u.id, u.plan_id, u.u, u.d, COALESCE\(u.expired_at, 0\), u.updated_at, p.reset_traffic_method\s+FROM v2_user u\s+JOIN v2_plan p ON p.id = u.plan_id\s+WHERE u.plan_id IS NOT NULL AND \(u.expired_at > \$1 OR u.expired_at IS NULL OR u.expired_at <= 0\)\s+ORDER BY u.id ASC\s+LIMIT \$2`).
		WithArgs(sqlmock.AnyArg(), orderHandleBatchLimit).
		WillReturnRows(sqlmock.NewRows([]string{"id", "plan_id", "u", "d", "expired_at", "updated_at", "reset_traffic_method"}).
			AddRow(int64(10), int64(7), int64(123), int64(456), expiredAt, cycleStart-3600, nil),
		)
	mock.ExpectQuery(`SELECT v, expire_at FROM v2_runtime_kv WHERE k = \$1 LIMIT 1`).
		WithArgs(trafficResetCycleKeyPrefix + "10").
		WillReturnRows(sqlmock.NewRows([]string{"v", "expire_at"}).
			AddRow(expectedMarker, int64(0)),
		)

	if err := service.HandlePendingOrders(context.Background()); err != nil {
		t.Fatalf("handle pending orders: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestHandlePendingOrdersSeedsMonthlyTrafficCycleWithoutResetForFreshSubscription(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	service := NewDBService(config.Config{
		CommissionAutoCheckEnable: false,
		OrderKeepDays:             0,
		ResetTrafficMethod:        0,
	}, db)

	now := time.Now()
	expiredAt := now.Add(7 * 24 * time.Hour).Unix()
	cycleStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()).Unix()
	freshUpdatedAt := cycleStart + 3600
	expectedMarker := fmt.Sprintf("plan:%d|method:%d|start:%d", int64(7), int64(0), cycleStart)

	mock.ExpectQuery(`SELECT trade_no\s+FROM v2_order\s+WHERE status IN \(0, 1\)`).
		WithArgs(orderHandleBatchLimit).
		WillReturnRows(sqlmock.NewRows([]string{"trade_no"}))
	mock.ExpectQuery(`SELECT u.id, u.plan_id, u.u, u.d, COALESCE\(u.expired_at, 0\), u.updated_at, p.reset_traffic_method\s+FROM v2_user u\s+JOIN v2_plan p ON p.id = u.plan_id\s+WHERE u.plan_id IS NOT NULL AND \(u.expired_at > \$1 OR u.expired_at IS NULL OR u.expired_at <= 0\)\s+ORDER BY u.id ASC\s+LIMIT \$2`).
		WithArgs(sqlmock.AnyArg(), orderHandleBatchLimit).
		WillReturnRows(sqlmock.NewRows([]string{"id", "plan_id", "u", "d", "expired_at", "updated_at", "reset_traffic_method"}).
			AddRow(int64(10), int64(7), int64(123), int64(456), expiredAt, freshUpdatedAt, nil),
		)
	mock.ExpectQuery(`SELECT v, expire_at FROM v2_runtime_kv WHERE k = \$1 LIMIT 1`).
		WithArgs(trafficResetCycleKeyPrefix + "10").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(`INSERT INTO v2_runtime_kv \(k, v, expire_at, created_at, updated_at\)`).
		WithArgs(trafficResetCycleKeyPrefix+"10", expectedMarker, sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := service.HandlePendingOrders(context.Background()); err != nil {
		t.Fatalf("handle pending orders: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestSweepTrafficResetsReturnsSummaryCounts(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	service := NewDBService(config.Config{
		ResetTrafficMethod: 0,
	}, db)

	now := time.Now()
	expiredAt := now.Add(7 * 24 * time.Hour).Unix()
	cycleStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()).Unix()
	expectedMarker := fmt.Sprintf("plan:%d|method:%d|start:%d", int64(7), int64(0), cycleStart)

	mock.ExpectQuery(`SELECT u.id, u.plan_id, u.u, u.d, COALESCE\(u.expired_at, 0\), u.updated_at, p.reset_traffic_method\s+FROM v2_user u\s+JOIN v2_plan p ON p.id = u.plan_id\s+WHERE u.plan_id IS NOT NULL AND \(u.expired_at > \$1 OR u.expired_at IS NULL OR u.expired_at <= 0\)\s+ORDER BY u.id ASC`).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "plan_id", "u", "d", "expired_at", "updated_at", "reset_traffic_method"}).
			AddRow(int64(10), int64(7), int64(123), int64(456), expiredAt, cycleStart-3600, nil).
			AddRow(int64(11), int64(7), int64(12), int64(34), expiredAt, cycleStart+3600, nil).
			AddRow(int64(12), int64(7), int64(56), int64(78), expiredAt, cycleStart-3600, nil),
		)
	mock.ExpectQuery(`SELECT v, expire_at FROM v2_runtime_kv WHERE k = \$1 LIMIT 1`).
		WithArgs(trafficResetCycleKeyPrefix + "10").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(`UPDATE v2_user SET u = 0, d = 0, updated_at = \$2 WHERE id = \$1`).
		WithArgs(int64(10), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO v2_runtime_kv \(k, v, expire_at, created_at, updated_at\)`).
		WithArgs(trafficResetCycleKeyPrefix+"10", expectedMarker, sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT v, expire_at FROM v2_runtime_kv WHERE k = \$1 LIMIT 1`).
		WithArgs(trafficResetCycleKeyPrefix + "11").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(`UPDATE v2_user SET u = 0, d = 0, updated_at = \$2 WHERE id = \$1`).
		WithArgs(int64(11), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO v2_runtime_kv \(k, v, expire_at, created_at, updated_at\)`).
		WithArgs(trafficResetCycleKeyPrefix+"11", expectedMarker, sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT v, expire_at FROM v2_runtime_kv WHERE k = \$1 LIMIT 1`).
		WithArgs(trafficResetCycleKeyPrefix + "12").
		WillReturnRows(sqlmock.NewRows([]string{"v", "expire_at"}).
			AddRow(expectedMarker, int64(0)),
		)

	result, err := service.SweepTrafficResets(context.Background())
	if err != nil {
		t.Fatalf("sweep traffic resets: %v", err)
	}
	if result.Scanned != 3 || result.Reset != 2 || result.MarkedOnly != 0 || result.Skipped != 1 {
		t.Fatalf("unexpected result: %#v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestSweepTrafficResetsIncludesNoExpiryMonthlyUsers(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	service := NewDBService(config.Config{
		ResetTrafficMethod: 0,
	}, db)

	now := time.Now()
	cycleStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()).Unix()
	expectedMarker := fmt.Sprintf("plan:%d|method:%d|start:%d", int64(9), int64(0), cycleStart)

	mock.ExpectQuery(`SELECT u.id, u.plan_id, u.u, u.d, COALESCE\(u.expired_at, 0\), u.updated_at, p.reset_traffic_method\s+FROM v2_user u\s+JOIN v2_plan p ON p.id = u.plan_id\s+WHERE u.plan_id IS NOT NULL AND \(u.expired_at > \$1 OR u.expired_at IS NULL OR u.expired_at <= 0\)\s+ORDER BY u.id ASC`).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "plan_id", "u", "d", "expired_at", "updated_at", "reset_traffic_method"}).
			AddRow(int64(20), int64(9), int64(100), int64(200), int64(0), cycleStart+3600, int64(0)),
		)
	mock.ExpectQuery(`SELECT v, expire_at FROM v2_runtime_kv WHERE k = \$1 LIMIT 1`).
		WithArgs(trafficResetCycleKeyPrefix + "20").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(`UPDATE v2_user SET u = 0, d = 0, updated_at = \$2 WHERE id = \$1`).
		WithArgs(int64(20), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO v2_runtime_kv \(k, v, expire_at, created_at, updated_at\)`).
		WithArgs(trafficResetCycleKeyPrefix+"20", expectedMarker, sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	result, err := service.SweepTrafficResets(context.Background())
	if err != nil {
		t.Fatalf("sweep traffic resets: %v", err)
	}
	if result.Reset != 1 {
		t.Fatalf("expected one reset for no-expiry user, got %#v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}
