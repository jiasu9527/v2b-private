package passport

import (
	"context"
	"forest/go-api/internal/config"
	"github.com/DATA-DOG/go-sqlmock"
	"golang.org/x/crypto/bcrypt"
	"testing"
)

func TestSensitiveReauthDoesNotCreateSessions(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("fixture-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, password string
		count          int
		banned         int64
		status         int
	}{
		{"valid", "fixture-password", 1, 0, 0}, {"wrong", "wrong-password", 1, 0, 403},
		{"suspended", "fixture-password", 1, 1, 403}, {"rate limited", "fixture-password", 11, 0, 429},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, e := sqlmock.New()
			if e != nil {
				t.Fatal(e)
			}
			defer db.Close()
			s := NewDBServiceWithConfig(config.Config{}, db)
			s.ensureOne.Do(func() {})
			mock.ExpectQuery(`INSERT INTO v2_runtime_kv`).WithArgs("NEWAPI_REAUTH:7", sqlmock.AnyArg(), sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"attempts"}).AddRow(tc.count))
			if tc.count <= 10 {
				mock.ExpectQuery(`SELECT id, email, password`).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"id", "email", "password", "password_algo", "password_salt", "token", "is_admin", "is_staff", "banned"}).AddRow(int64(7), "fixture@example.test", string(hash), nil, nil, "never-return-this", 0, 0, tc.banned))
			}
			e = s.VerifyCurrentPassword(context.Background(), 7, tc.password)
			if tc.status == 0 && e != nil {
				t.Fatal(e)
			}
			if tc.status != 0 && HTTPStatus(e) != tc.status {
				t.Fatalf("wanted %d got %v", tc.status, e)
			}
			if e = mock.ExpectationsWereMet(); e != nil {
				t.Fatal(e)
			}
		})
	}
}
