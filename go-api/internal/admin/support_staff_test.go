package admin

import (
	"context"
	"errors"
	"testing"

	"forest/go-api/internal/session"
	"github.com/DATA-DOG/go-sqlmock"
)

func expectSupportTargetsForRole(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(`SELECT p.id,p.name,COALESCE`).WillReturnRows(sqlmock.NewRows([]string{"id", "name", "group_id", "group_name", "host", "resolve", "type", "server_id"}).AddRow(20, "repair", 0, "", "repair.example", 0, "vmess", 1))
}

func TestSupportRoleLiveCheckDoesNotUseCachedIdentity(t *testing.T) {
	s, mock, done := newSupportEntryTestService(t)
	defer done()
	expectSupportStaffRead(mock)
	if ok, err := s.IsSupportStaff(context.Background(), 7); err != nil || !ok {
		t.Fatalf("enabled: %v %v", ok, err)
	}
	mock.ExpectQuery(`SELECT staff.user_id, users.email, staff.enabled, staff.target_keys::TEXT`).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"id", "email", "enabled", "keys", "version", "created", "updated"}))
	if ok, err := s.IsSupportStaff(context.Background(), 7); err != nil || ok {
		t.Fatalf("revoked: %v %v", ok, err)
	}
}

func TestSupportRoleSaveRevokesSessionsAndInvalidatesCache(t *testing.T) {
	s, mock, done := newSupportEntryTestService(t)
	defer done()
	cache := session.NewAuthCache(session.DefaultAuthCacheTTL)
	s.authCache = cache
	cache.Store("session-token", "sid", &session.Identity{ID: 7})
	expectSupportTargetsForRole(mock)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id FROM v2_user.*is_admin = 1.*FOR SHARE`).WithArgs(int64(1)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	mock.ExpectQuery(`SELECT id, email, banned, is_admin, is_staff.*FOR UPDATE`).WithArgs("staff@example.com").WillReturnRows(sqlmock.NewRows([]string{"id", "email", "banned", "admin", "staff"}).AddRow(7, "staff@example.com", 0, 0, 0))
	mock.ExpectQuery(`SELECT version, created_at FROM v2_support_staff.*FOR UPDATE`).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"version", "created"}).AddRow(1, 10))
	mock.ExpectExec(`INSERT INTO v2_support_staff`).WithArgs(int64(7), int64(1), `["policy:20"]`, int64(2), int64(10), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DELETE FROM v2_auth_session WHERE user_id = \$1`).WithArgs(int64(7)).WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectCommit()
	got, err := s.SaveSupportStaff(context.Background(), 1, SupportStaffSaveRequest{Email: "Staff@Example.com", Enabled: true, TargetKeys: []string{"policy:20", "policy:20"}, Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != 2 || !got.Enabled || len(got.TargetKeys) != 1 {
		t.Fatalf("bad result: %+v", got)
	}
	if _, ok := cache.Get("session-token"); ok {
		t.Fatal("stale authorization remained cached")
	}
}

func TestSupportRoleSaveRejectsLegacyStaffAndStaleVersion(t *testing.T) {
	for _, legacy := range []bool{true, false} {
		t.Run(map[bool]string{true: "legacy_staff", false: "version_conflict"}[legacy], func(t *testing.T) {
			s, mock, done := newSupportEntryTestService(t)
			defer done()
			expectSupportTargetsForRole(mock)
			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT id FROM v2_user.*is_admin = 1.*FOR SHARE`).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
			staff := 0
			if legacy {
				staff = 1
			}
			mock.ExpectQuery(`SELECT id, email, banned, is_admin, is_staff.*FOR UPDATE`).WillReturnRows(sqlmock.NewRows([]string{"id", "email", "banned", "admin", "staff"}).AddRow(7, "staff@example.com", 0, 0, staff))
			if !legacy {
				mock.ExpectQuery(`SELECT version, created_at.*FOR UPDATE`).WillReturnRows(sqlmock.NewRows([]string{"version", "created"}).AddRow(9, 1))
			}
			mock.ExpectRollback()
			_, err := s.SaveSupportStaff(context.Background(), 1, SupportStaffSaveRequest{Email: "staff@example.com", Enabled: true, Version: 1})
			want := ErrSupportStaffConflict
			if legacy {
				want = ErrSupportStaffInvalid
			}
			if !errors.Is(err, want) {
				t.Fatalf("got %v want %v", err, want)
			}
		})
	}
}

func TestSupportRateLimitIsPerActorActionWithoutSavingQuery(t *testing.T) {
	for _, count := range []int64{30, 31} {
		s, mock, done := newSupportEntryTestService(t)
		expectSupportStaffRead(mock)
		mock.ExpectQuery(`INSERT INTO v2_support_request_limit.*`).WithArgs(int64(7), "lookup", sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(count))
		err := s.ConsumeSupportLimit(context.Background(), 7, "lookup")
		if (count > 30) != errors.Is(err, ErrSupportRateLimited) {
			t.Fatalf("count %d err %v", count, err)
		}
		done()
	}
}
