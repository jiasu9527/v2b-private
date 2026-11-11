package newapibridge

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestOAuthIdentityUsesEmailWithoutChangingSubject(t *testing.T) {
	for _, email := range []string{"customer@example.com", "long.customer.address+ai@example.test"} {
		t.Run(email, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			mock.ExpectQuery(`SELECT email,banned FROM v2_user WHERE id=\$1`).WithArgs(int64(42)).
				WillReturnRows(sqlmock.NewRows([]string{"email", "banned"}).AddRow(email, 0))
			s := &Service{db: db}
			u, err := s.UserInfo(context.Background(), 42)
			if err != nil {
				t.Fatal(err)
			}
			if u.Subject != "forest:42" || u.Email != email || u.PreferredUsername != email {
				t.Fatalf("email must be the preferred account, without changing stable subject: %+v", u)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestWorkspacePostgresLoginAccountUsesRemoteEmail(t *testing.T) {
	for _, tc := range []struct{ email, want string }{
		{"customer@example.com", "customer@example.com"},
		{"long.customer.address+ai@example.test", "long.customer.address+ai@example.test"},
		{"", "forest_1"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			s, f := workspacePostgresFixture(t)
			f.user.Email = tc.email
			ctx := context.Background()
			connected, err := s.ConnectAccount(ctx, 1, "auth")
			if err != nil {
				t.Fatal(err)
			}
			account, err := s.WorkspaceAccount(ctx, 1, "auth")
			if err != nil {
				t.Fatal(err)
			}
			for _, view := range []map[string]any{connected, account} {
				if view["login_account"] != tc.want || view["email"] != tc.email || view["username"] != "forest_1" || view["remote_user_id"] != int64(4) {
					t.Fatalf("must display remote email without renaming or rebinding: %+v", view)
				}
			}
			var username string
			if err := s.db.QueryRow(`SELECT username FROM v2_newapi_account WHERE user_id=1`).Scan(&username); err != nil || username != "forest_1" {
				t.Fatalf("stored upstream username changed: %q %v", username, err)
			}
		})
	}
}
