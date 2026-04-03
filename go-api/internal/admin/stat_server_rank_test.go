package admin

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestGetServerRankFallsBackToReadableNameWhenServerMissing(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	svc := &DBService{db: db}

	mock.ExpectQuery(`SELECT server_id, server_type, CAST\(u AS bigint\), CAST\(d AS bigint\), CAST\(CAST\(u AS numeric\) \+ CAST\(d AS numeric\) AS double precision\) AS total`).
		WithArgs(int64(1), int64(2)).
		WillReturnRows(sqlmock.NewRows([]string{"server_id", "server_type", "u", "d", "total"}).
			AddRow(int64(8), "vmess", int64(10), int64(20), float64(bytesPerGiB)))
	mock.ExpectQuery(`SELECT name FROM v2_server_vmess WHERE id = \$1 LIMIT 1`).
		WithArgs(int64(8)).
		WillReturnRows(sqlmock.NewRows([]string{"name"}))

	rows, err := svc.getServerRank(context.Background(), 1, 2)
	if err != nil {
		t.Fatalf("get server rank: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0]["server_name"] != "vmess #8" {
		t.Fatalf("expected fallback server_name, got %#v", rows[0]["server_name"])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}
