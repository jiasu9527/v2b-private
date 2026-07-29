package admin

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestDBServiceBanUsersRevokesSessionsAndSetsStatus(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(`SELECT id FROM v2_user u WHERE u.id = \$1 ORDER BY u.id ASC`).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(9)))
	mock.ExpectBegin()
	mock.ExpectExec(`DELETE FROM v2_auth_session WHERE user_id IN \(\$1\)`).
		WithArgs(int64(9)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE v2_user SET banned = \$1, updated_at = \$2 WHERE id IN \(\$3\)`).
		WithArgs(int64(1), sqlmock.AnyArg(), int64(9)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	service := &DBService{db: db}
	updated, err := service.BanUsers(context.Background(), []UserFilter{{Key: "id", Condition: "=", Value: "9"}})
	if err != nil {
		t.Fatalf("ban user: %v", err)
	}
	if !updated {
		t.Fatal("expected ban success")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestDBServiceUnbanUsersSetsStatusWithoutDeletingSessions(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(`SELECT id FROM v2_user u WHERE u.id = \$1 ORDER BY u.id ASC`).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(9)))
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE v2_user SET banned = \$1, updated_at = \$2 WHERE id IN \(\$3\)`).
		WithArgs(int64(0), sqlmock.AnyArg(), int64(9)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	service := &DBService{db: db}
	updated, err := service.UnbanUsers(context.Background(), []UserFilter{{Key: "id", Condition: "=", Value: "9"}})
	if err != nil {
		t.Fatalf("unban user: %v", err)
	}
	if !updated {
		t.Fatal("expected unban success")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}
