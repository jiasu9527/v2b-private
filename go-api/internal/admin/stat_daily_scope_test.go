package admin

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestGetStatOrderOnlyReadsDailyStats(t *testing.T) {
	previousLocal := time.Local
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	time.Local = location
	defer func() {
		time.Local = previousLocal
	}()

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
LIMIT 124`)).
		WillReturnRows(sqlmock.NewRows([]string{
			"record_at", "register_count", "paid_total", "paid_count", "commission_total", "commission_count",
		}).
			AddRow(int64(1774915200), int64(6), int64(207384), int64(51), int64(1400), int64(3)).
			AddRow(int64(1774886400), int64(5), int64(190656), int64(44), int64(1200), int64(2)))

	rows, err := service.GetStatOrder(context.Background())
	if err != nil {
		t.Fatalf("get stat order: %v", err)
	}
	if len(rows) != 5 {
		t.Fatalf("expected 5 chart rows, got %d", len(rows))
	}
	for _, row := range rows {
		if row["type"] == "收款金额" && row["value"] != float64(190656)/100 {
			t.Fatalf("expected midnight daily stat only, got %#v", row)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestGetStatRecordOnlyReadsDailyStats(t *testing.T) {
	previousLocal := time.Local
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	time.Local = location
	defer func() {
		time.Local = previousLocal
	}()

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
		}).
			AddRow(int64(6), int64(1774915200), "d", int64(11), int64(11000), int64(2), int64(700), int64(9), float64(2073.84), int64(4), int64(2), "4096", int64(1775001600), int64(1775001600)).
			AddRow(int64(7), int64(1774886400), "d", int64(10), int64(10000), int64(1), int64(500), int64(8), float64(1906.56), int64(3), int64(1), "2048", int64(1774972800), int64(1774972800)))

	rows, err := service.GetStatRecord(context.Background(), "paid_total", 1711497600, 1711584000)
	if err != nil {
		t.Fatalf("get stat record: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 stat row, got %d", len(rows))
	}
	if rows[0]["paid_total"] != float64(1906.56) {
		t.Fatalf("expected midnight daily stat record only, got %#v", rows[0])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}
