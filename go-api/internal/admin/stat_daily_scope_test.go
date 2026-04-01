package admin

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestGetStatOrderOnlyReadsDailyStats(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	service := &DBService{db: db}

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT record_at, register_count, paid_total, paid_count, commission_total, commission_count
FROM v2_stat
WHERE record_type = 'd'
ORDER BY record_at DESC
LIMIT 31`)).
		WillReturnRows(sqlmock.NewRows([]string{
			"record_at", "register_count", "paid_total", "paid_count", "commission_total", "commission_count",
		}).AddRow(int64(1711497600), int64(5), int64(8800), int64(8), int64(1200), int64(2)))

	rows, err := service.GetStatOrder(context.Background())
	if err != nil {
		t.Fatalf("get stat order: %v", err)
	}
	if len(rows) != 5 {
		t.Fatalf("expected 5 chart rows, got %d", len(rows))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestGetStatRecordOnlyReadsDailyStats(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	service := &DBService{db: db}

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id, record_at, record_type, order_count, order_total, commission_count, commission_total, paid_count, paid_total / 100.0 AS field_value, register_count, invite_count, transfer_used_total, created_at, updated_at
FROM v2_stat
WHERE record_type = 'd' AND record_at >= $1 AND record_at < $2
ORDER BY record_at ASC`)).
		WithArgs(int64(1711497600), int64(1711584000)).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "record_at", "record_type", "order_count", "order_total", "commission_count", "commission_total", "paid_count", "field_value", "register_count", "invite_count", "transfer_used_total", "created_at", "updated_at",
		}).AddRow(int64(7), int64(1711497600), "d", int64(10), int64(10000), int64(1), int64(500), int64(8), float64(88), int64(3), int64(1), "2048", int64(1711584600), int64(1711584600)))

	rows, err := service.GetStatRecord(context.Background(), "paid_total", 1711497600, 1711584000)
	if err != nil {
		t.Fatalf("get stat record: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 stat row, got %d", len(rows))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}
