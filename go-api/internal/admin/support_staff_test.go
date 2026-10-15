package admin

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestSupportRoleLiveCheckUsesExistingStaffAndDoesNotUseCachedIdentity(t *testing.T) {
	s, mock, done := newSupportEntryTestService(t)
	defer done()
	expectSupportStaffRead(mock)
	if ok, err := s.IsSupportStaff(context.Background(), 7); err != nil || !ok {
		t.Fatalf("existing is_staff employee should be allowed: %v %v", ok, err)
	}
	mock.ExpectQuery(`SELECT id,email FROM v2_user WHERE id = \$1 AND banned = 0 AND is_admin = 0 AND is_staff = 1`).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"id", "email"}))
	if ok, err := s.IsSupportStaff(context.Background(), 7); err != nil || ok {
		t.Fatalf("removed is_staff flag must take effect immediately: %v %v", ok, err)
	}
}
func TestSupportRoleRequiresUnbannedNonAdminExistingStaff(t *testing.T) {
	for _, name := range []string{"plain_user", "administrator", "banned_staff", "missing_user"} {
		t.Run(name, func(t *testing.T) {
			s, mock, done := newSupportEntryTestService(t)
			defer done()
			mock.ExpectQuery(`SELECT id,email FROM v2_user WHERE id = \$1 AND banned = 0 AND is_admin = 0 AND is_staff = 1`).WithArgs(int64(7)).WillReturnError(sql.ErrNoRows)
			_, err := s.requireSupportStaff(context.Background(), 7)
			if !errors.Is(err, ErrSupportForbidden) {
				t.Fatalf("expected %s denied: %v", name, err)
			}
		})
	}
}
func TestSupportRoleDatabaseErrorIsNotSilentlyAuthorized(t *testing.T) {
	s, mock, done := newSupportEntryTestService(t)
	defer done()
	dbErr := errors.New("database unavailable")
	mock.ExpectQuery(`SELECT id,email FROM v2_user`).WithArgs(int64(7)).WillReturnError(dbErr)
	ok, err := s.IsSupportStaff(context.Background(), 7)
	if ok || !errors.Is(err, dbErr) {
		t.Fatalf("database failure must not authorize: %v,%v", ok, err)
	}
}
func TestSupportRateLimitIsPerExistingEmployeeAndActionWithoutSavingQuery(t *testing.T) {
	for _, tc := range []struct {
		action  string
		count   int64
		limited bool
	}{{"lookup", 30, false}, {"lookup", 31, true}, {"change", 20, false}, {"change", 21, true}} {
		t.Run(tc.action+map[bool]string{false: "_allowed", true: "_limited"}[tc.limited], func(t *testing.T) {
			s, mock, done := newSupportEntryTestService(t)
			defer done()
			expectSupportStaffRead(mock)
			mock.ExpectQuery(`INSERT INTO v2_support_request_limit.*`).WithArgs(int64(7), tc.action, sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(tc.count))
			err := s.ConsumeSupportLimit(context.Background(), 7, tc.action)
			if errors.Is(err, ErrSupportRateLimited) != tc.limited {
				t.Fatalf("count %d err %v", tc.count, err)
			}
		})
	}
}
func TestSupportRateLimitRejectsUnknownActionWithoutWriting(t *testing.T) {
	s, _, done := newSupportEntryTestService(t)
	defer done()
	if err := s.ConsumeSupportLimit(context.Background(), 7, "export"); !errors.Is(err, ErrSupportEntryInvalid) {
		t.Fatalf("unknown action error: %v", err)
	}
}
