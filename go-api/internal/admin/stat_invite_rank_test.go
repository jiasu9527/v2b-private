package admin

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestGetInviteLastRankUsesYesterdayRange(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	service := &DBService{db: db}
	startAt, endAt := dayRange(-1)

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT invite_user_id, COUNT(*) AS count
FROM v2_user
WHERE created_at >= $1 AND created_at < $2 AND invite_user_id IS NOT NULL
GROUP BY invite_user_id
ORDER BY count DESC
LIMIT $3`)).
		WithArgs(startAt, endAt, int64(15)).
		WillReturnRows(sqlmock.NewRows([]string{"invite_user_id", "count"}).
			AddRow(int64(12), int64(6)))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT email FROM v2_user WHERE id = $1 LIMIT 1`)).
		WithArgs(int64(12)).
		WillReturnRows(sqlmock.NewRows([]string{"email"}).AddRow("invite-last@example.com"))

	rows, err := service.GetInviteLastRank(context.Background())
	if err != nil {
		t.Fatalf("get invite last rank: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0]["email"] != "invite-last@example.com" || rows[0]["count"] != int64(6) {
		t.Fatalf("unexpected row: %#v", rows[0])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestGetInviteTodayRankUsesTodayRange(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	service := &DBService{db: db}
	startAt, endAt := dayRange(0)

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT invite_user_id, COUNT(*) AS count
FROM v2_user
WHERE created_at >= $1 AND created_at < $2 AND invite_user_id IS NOT NULL
GROUP BY invite_user_id
ORDER BY count DESC
LIMIT $3`)).
		WithArgs(startAt, endAt, int64(15)).
		WillReturnRows(sqlmock.NewRows([]string{"invite_user_id", "count"}).
			AddRow(int64(15), int64(8)))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT email FROM v2_user WHERE id = $1 LIMIT 1`)).
		WithArgs(int64(15)).
		WillReturnRows(sqlmock.NewRows([]string{"email"}).AddRow("invite-today@example.com"))

	rows, err := service.GetInviteTodayRank(context.Background())
	if err != nil {
		t.Fatalf("get invite today rank: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0]["email"] != "invite-today@example.com" || rows[0]["count"] != int64(8) {
		t.Fatalf("unexpected row: %#v", rows[0])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}
