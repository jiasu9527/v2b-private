package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestLegacyStaffUserDetailOmitsSecretsAndInviteUser(t *testing.T) {
	s, mock, done := newSupportEntryTestService(t)
	defer done()
	mock.ExpectQuery(`(?s)SELECT row_to_json.*SELECT u\.\* FROM v2_user u WHERE u.id = \$1 AND u.is_admin = 0 AND u.is_staff = 0.*NOT EXISTS .*v2_support_staff support.*LIMIT 1`).
		WithArgs(int64(9)).WillReturnRows(sqlmock.NewRows([]string{"user"}).AddRow(`{"id":9,"email":"customer@example.test","password":"hash","password_salt":"salt","token":"secret","uuid":"secret-uuid","invite_user":{"id":7,"password":"staff-hash"},"balance":123,"is_admin":0,"is_staff":0}`))
	got, err := s.GetUserInfoByID(WithLegacyStaffScope(context.Background()), 9)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(got)
	for _, key := range []string{"password", "password_salt", "token", "uuid", "invite_user", "subscribe_url"} {
		if _, ok := got[key]; ok {
			t.Fatalf("leaked %s in %s", key, raw)
		}
	}
	if got["email"] != "customer@example.test" || got["balance"] == nil {
		t.Fatalf("legacy customer editing fields lost: %s", raw)
	}
}

func TestLegacyStaffGetInfoRejectsAnySupportMembership(t *testing.T) {
	s, mock, done := newSupportEntryTestService(t)
	defer done()
	// No enabled predicate: disabled support accounts remain privileged targets.
	mock.ExpectQuery(`(?s)SELECT row_to_json.*NOT EXISTS \(SELECT 1 FROM v2_support_staff support WHERE support.user_id = u.id\)`).
		WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"user"}))
	if _, err := s.GetUserInfoByID(WithLegacyStaffScope(context.Background()), 7); err == nil {
		t.Fatal("support account was visible to legacy staff")
	}
}

func TestLegacyStaffUserFilterCannotDropRoleExclusion(t *testing.T) {
	s, _, done := newSupportEntryTestService(t)
	defer done()
	for _, filters := range [][]UserFilter{nil, {{Key: "email", Condition: "=", Value: "staff@example.test"}}} {
		where, _, err := s.buildUserWhere(WithLegacyStaffScope(context.Background()), filters)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(where, legacyStaffUserPredicate) || strings.Contains(where, "enabled") {
			t.Fatalf("missing membership exclusion: %s", where)
		}
	}
	where, _, err := s.buildUserWhere(context.Background(), nil)
	if err != nil || where != "" {
		t.Fatalf("administrator scope changed: %q %v", where, err)
	}
}

func expectLegacyUpdateInput(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(`SELECT email FROM v2_user WHERE id = \$1 LIMIT 1`).WithArgs(int64(9)).WillReturnRows(sqlmock.NewRows([]string{"email"}).AddRow("customer@example.test"))
	mock.ExpectQuery(`SELECT id FROM v2_user WHERE LOWER\(email\) = \$1 LIMIT 1`).WithArgs("customer@example.test").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(9))
	mock.ExpectBegin()
}

func TestLegacyStaffUpdateRechecksAfterLockAndRollsBackRoleGrant(t *testing.T) {
	for _, role := range []string{"support", "administrator", "legacy_staff"} {
		t.Run(role, func(t *testing.T) {
			s, mock, done := newSupportEntryTestService(t)
			defer done()
			expectLegacyUpdateInput(mock)
			isAdmin, isStaff := 0, 0
			if role == "administrator" {
				isAdmin = 1
			}
			if role == "legacy_staff" {
				isStaff = 1
			}
			mock.ExpectQuery(`SELECT is_admin, is_staff FROM v2_user WHERE id = \$1 FOR UPDATE`).WithArgs(int64(9)).WillReturnRows(sqlmock.NewRows([]string{"admin", "staff"}).AddRow(isAdmin, isStaff))
			mock.ExpectQuery(`SELECT EXISTS \(SELECT 1 FROM v2_support_staff WHERE user_id = \$1\)`).WithArgs(int64(9)).WillReturnRows(sqlmock.NewRows([]string{"support"}).AddRow(role == "support"))
			mock.ExpectRollback()
			_, err := s.UpdateUser(WithLegacyStaffScope(context.Background()), UserUpdateRequest{ID: 9, Values: map[string]string{"email": "customer@example.test", "banned": "0", "password": "new-password"}})
			if !errors.Is(err, ErrSupportForbidden) {
				t.Fatalf("expected forbidden after concurrent grant, got %v", err)
			}
		})
	}
}

func TestLegacyStaffUpdateNormalCustomerPreservesPrivilegeFlags(t *testing.T) {
	s, mock, done := newSupportEntryTestService(t)
	defer done()
	expectLegacyUpdateInput(mock)
	mock.ExpectQuery(`SELECT is_admin, is_staff.*FOR UPDATE`).WithArgs(int64(9)).WillReturnRows(sqlmock.NewRows([]string{"admin", "staff"}).AddRow(0, 0))
	mock.ExpectQuery(`SELECT EXISTS .*v2_support_staff`).WithArgs(int64(9)).WillReturnRows(sqlmock.NewRows([]string{"support"}).AddRow(false))
	mock.ExpectExec(`UPDATE v2_user SET email = \$2, banned = \$3, is_admin = \$4, is_staff = \$5, updated_at = \$6 WHERE id = \$1`).WithArgs(int64(9), "customer@example.test", int64(0), int64(0), int64(0), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	ok, err := s.UpdateUser(WithLegacyStaffScope(context.Background()), UserUpdateRequest{ID: 9, Values: map[string]string{"email": "customer@example.test", "banned": "0", "is_admin": "1", "is_staff": "1"}})
	if err != nil || !ok {
		t.Fatalf("normal update failed: %v", err)
	}
}

func TestLegacyStaffBulkBanRevalidatesMembershipBeforeSessionsOrUpdate(t *testing.T) {
	for _, ban := range []bool{true, false} {
		t.Run(map[bool]string{true: "ban", false: "unban"}[ban], func(t *testing.T) {
			s, mock, done := newSupportEntryTestService(t)
			defer done()
			mock.ExpectQuery(`(?s)SELECT id FROM v2_user u WHERE.*NOT EXISTS.*v2_support_staff.*ORDER BY u.id ASC`).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(7).AddRow(9))
			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT id FROM v2_user WHERE id IN \(\$1,\$2\) ORDER BY id FOR UPDATE`).WithArgs(int64(7), int64(9)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(7).AddRow(9))
			mock.ExpectQuery(`(?s)SELECT u.id FROM v2_user u WHERE u.id IN \(\$1,\$2\) AND.*NOT EXISTS.*v2_support_staff`).WithArgs(int64(7), int64(9)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(9))
			value := int64(0)
			if ban {
				value = 1
				mock.ExpectExec(`DELETE FROM v2_auth_session WHERE user_id IN \(\$1\)`).WithArgs(int64(9)).WillReturnResult(sqlmock.NewResult(0, 1))
			}
			mock.ExpectExec(`UPDATE v2_user SET banned = \$1, updated_at = \$2 WHERE id IN \(\$3\)`).WithArgs(value, sqlmock.AnyArg(), int64(9)).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectCommit()
			ok, err := s.setUsersBanned(WithLegacyStaffScope(context.Background()), nil, value)
			if err != nil || !ok {
				t.Fatalf("bulk status failed: %v", err)
			}
		})
	}
}

func TestLegacyStaffBulkMailNeverSelectsSupportMembership(t *testing.T) {
	s, mock, done := newSupportEntryTestService(t)
	defer done()
	mock.ExpectQuery(`(?s)SELECT email FROM v2_user u WHERE.*NOT EXISTS \(SELECT 1 FROM v2_support_staff support WHERE support.user_id = u.id\).*ORDER BY`).WillReturnError(sql.ErrConnDone)
	_, err := s.SendUserMail(WithLegacyStaffScope(context.Background()), UserMailRequest{Subject: "test", Content: "test"})
	if !errors.Is(err, sql.ErrConnDone) {
		t.Fatalf("query was not scoped: %v", err)
	}
}
