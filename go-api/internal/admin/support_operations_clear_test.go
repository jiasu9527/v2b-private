package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func expectSupportClearAdminLock(mock sqlmock.Sqlmock) {
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id FROM v2_user WHERE id = $1 AND banned = 0 AND is_admin = 1 FOR SHARE`)).
		WithArgs(int64(9)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(9))
}

func expectSupportClearTableLock(mock sqlmock.Sqlmock) *sqlmock.ExpectedExec {
	return mock.ExpectExec(regexp.QuoteMeta(`LOCK TABLE v2_support_operation IN SHARE ROW EXCLUSIVE MODE`))
}

func expectSupportClearReceiptCopy(mock sqlmock.Sqlmock, throughID int64) *sqlmock.ExpectedExec {
	return mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO v2_support_request_receipt (actor_id,request_id,kind,request_hash,created_at)
SELECT actor_id,request_id,COALESCE(after_state->>'kind',''),COALESCE(after_state->>'request_hash',''),created_at
FROM v2_support_operation WHERE id <= $1
ON CONFLICT (actor_id,request_id) DO NOTHING`)).WithArgs(throughID)
}

func expectSupportClearDelete(mock sqlmock.Sqlmock, throughID int64) *sqlmock.ExpectedExec {
	return mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM v2_support_operation WHERE id <= $1`)).WithArgs(throughID)
}

func TestSupportOperationClearRejectsUnconfirmedOrNegativeFenceWithoutDatabaseWork(t *testing.T) {
	for _, req := range []SupportOperationClearRequest{{ThroughID: 10}, {ThroughID: -1, Confirm: true}} {
		t.Run(fmt.Sprint(req), func(t *testing.T) {
			s, _, done := newSupportEntryTestService(t)
			defer done()
			if result, err := s.ClearSupportOperations(context.Background(), 9, req); !errors.Is(err, ErrSupportEntryInvalid) || result.Deleted != 0 {
				t.Fatalf("invalid request result: %+v %v", result, err)
			}
		})
	}
	for _, actorID := range []int64{0, -1} {
		s, _, done := newSupportEntryTestService(t)
		if _, err := s.ClearSupportOperations(context.Background(), actorID, SupportOperationClearRequest{ThroughID: 10, Confirm: true}); !errors.Is(err, ErrSupportForbidden) {
			t.Fatalf("invalid actor %d: %v", actorID, err)
		}
		done()
	}
}

func TestSupportOperationClearRequiresFreshUnbannedAdministratorInsideTransaction(t *testing.T) {
	// Both staff and banned/revoked administrators fail the same authoritative
	// predicate, even if a middleware authorization check succeeded earlier.
	for _, actor := range []string{"staff", "banned administrator", "revoked administrator"} {
		t.Run(actor, func(t *testing.T) {
			s, mock, done := newSupportEntryTestService(t)
			defer done()
			mock.ExpectBegin()
			mock.ExpectQuery(regexp.QuoteMeta(`SELECT id FROM v2_user WHERE id = $1 AND banned = 0 AND is_admin = 1 FOR SHARE`)).
				WithArgs(int64(9)).WillReturnError(sql.ErrNoRows)
			mock.ExpectRollback()
			result, err := s.ClearSupportOperations(context.Background(), 9, SupportOperationClearRequest{ThroughID: 10, Confirm: true})
			if !errors.Is(err, ErrSupportForbidden) || result.Deleted != 0 {
				t.Fatalf("unauthorized clear: %+v %v", result, err)
			}
		})
	}
}

func TestSupportOperationClearZeroFenceAuthenticatesButNeverDeletesEverything(t *testing.T) {
	s, mock, done := newSupportEntryTestService(t)
	defer done()
	expectSupportClearAdminLock(mock)
	mock.ExpectCommit()
	result, err := s.ClearSupportOperations(context.Background(), 9, SupportOperationClearRequest{Confirm: true})
	if err != nil || result.Deleted != 0 {
		t.Fatalf("zero fence: %+v %v", result, err)
	}
}

func TestSupportOperationClearCopiesCompactReceiptsBeforeDeletingOnlyConfirmedFence(t *testing.T) {
	s, mock, done := newSupportEntryTestService(t)
	defer done()
	// Repeating the same snapshot must not broaden the fence to logs created
	// since the original confirmation. No sequence reset or business writes.
	for _, deleted := range []int64{12, 0} {
		expectSupportClearAdminLock(mock)
		expectSupportClearTableLock(mock).WillReturnResult(sqlmock.NewResult(0, 0))
		expectSupportClearReceiptCopy(mock, 42).WillReturnResult(sqlmock.NewResult(0, deleted))
		expectSupportClearDelete(mock, 42).WillReturnResult(sqlmock.NewResult(0, deleted))
		mock.ExpectCommit()
		result, err := s.ClearSupportOperations(context.Background(), 9, SupportOperationClearRequest{ThroughID: 42, Confirm: true})
		if err != nil || result.Deleted != deleted {
			t.Fatalf("clear expected %d: %+v %v", deleted, result, err)
		}
	}
}

func TestSupportOperationClearRollsBackAnyPartialFailure(t *testing.T) {
	for _, failure := range []string{"authorization", "table lock", "receipt copy", "delete", "affected rows", "commit"} {
		t.Run(failure, func(t *testing.T) {
			s, mock, done := newSupportEntryTestService(t)
			defer done()
			cause := errors.New(failure + " failed")
			if failure == "authorization" {
				mock.ExpectBegin()
				mock.ExpectQuery(`SELECT id FROM v2_user`).WithArgs(int64(9)).WillReturnError(cause)
			} else {
				expectSupportClearAdminLock(mock)
				lock := expectSupportClearTableLock(mock)
				if failure == "table lock" {
					lock.WillReturnError(cause)
				} else {
					lock.WillReturnResult(sqlmock.NewResult(0, 0))
					copy := expectSupportClearReceiptCopy(mock, 42)
					if failure == "receipt copy" {
						copy.WillReturnError(cause)
					} else {
						copy.WillReturnResult(sqlmock.NewResult(0, 12))
						remove := expectSupportClearDelete(mock, 42)
						switch failure {
						case "delete":
							remove.WillReturnError(cause)
						case "affected rows":
							remove.WillReturnResult(sqlmock.NewErrorResult(cause))
						case "commit":
							remove.WillReturnResult(sqlmock.NewResult(0, 12))
							mock.ExpectCommit().WillReturnError(cause)
						}
					}
				}
			}
			if failure != "commit" {
				mock.ExpectRollback()
			}
			result, err := s.ClearSupportOperations(context.Background(), 9, SupportOperationClearRequest{ThroughID: 42, Confirm: true})
			if !errors.Is(err, cause) || result.Deleted != 0 {
				t.Fatalf("failed clear reported success: %+v %v", result, err)
			}
		})
	}
}

func TestSupportRequestLookupUsesOneSnapshotForLogsAndClearedReceipts(t *testing.T) {
	want := `SELECT after_state::TEXT FROM v2_support_operation WHERE actor_id = $1 AND request_id = $2
UNION ALL
SELECT jsonb_build_object('kind',kind,'request_hash',request_hash)::TEXT FROM v2_support_request_receipt WHERE actor_id = $1 AND request_id = $2
LIMIT 1`
	if supportRequestStateQuery != want {
		t.Fatal("idempotency must query the operation and its receipt in one SQL snapshot")
	}
}

func TestSupportOperationListSnapshotIncludesNewestIDBeyondCurrentPage(t *testing.T) {
	s, mock, done := newSupportEntryTestService(t)
	defer done()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT COUNT(*),COALESCE(MAX(id),0) FROM v2_support_operation`)).
		WillReturnRows(sqlmock.NewRows([]string{"total", "latest_id"}).AddRow(21, 60))
	mock.ExpectQuery(`SELECT id,actor_id,target_user_id.*ORDER BY id DESC LIMIT 20 OFFSET \$1`).WithArgs(int64(20)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "actor", "target", "source", "reason", "before", "after", "created"}).AddRow(2, 7, 5, 0, "repair", `{}`, `{}`, 100))
	result, err := s.ListSupportOperations(context.Background(), 0, 2)
	if err != nil || result.Total != 21 || result.LatestID != 60 || result.Items[0].ID != 2 {
		t.Fatalf("snapshot must cover all pages: %+v %v", result, err)
	}
}

func TestSupportOperationListEmptySnapshotUsesZeroFence(t *testing.T) {
	s, mock, done := newSupportEntryTestService(t)
	defer done()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT COUNT(*),COALESCE(MAX(id),0) FROM v2_support_operation`)).
		WillReturnRows(sqlmock.NewRows([]string{"total", "latest_id"}).AddRow(0, 0))
	mock.ExpectQuery(`SELECT id,actor_id,target_user_id`).WithArgs(int64(0)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "actor", "target", "source", "reason", "before", "after", "created"}))
	result, err := s.ListSupportOperations(context.Background(), 0, 1)
	if err != nil || result.Total != 0 || result.LatestID != 0 || result.Items == nil || len(result.Items) != 0 {
		t.Fatalf("empty snapshot: %+v %v", result, err)
	}
}

func TestSupportClearedReceiptKeepsEntryRetryReadOnly(t *testing.T) {
	s, mock, done := newSupportEntryTestService(t)
	defer done()
	fixture := supportMembershipFixture{target: true}
	req := supportTestChangeRequest(t, fixture)
	expectSupportStaffRead(mock)
	expectSupportMutationLocks(mock, req.RequestID, false)
	compact := fmt.Sprintf(`{"kind":"entry_membership","request_hash":%q}`, supportChangeHash(req))
	mock.ExpectQuery(regexp.QuoteMeta(supportRequestStateQuery)).WithArgs(int64(7), req.RequestID).
		WillReturnRows(sqlmock.NewRows([]string{"state"}).AddRow(compact))
	expectSupportPreview(mock, fixture)
	expectSupportEntitlementForEntry(mock)
	mock.ExpectCommit()
	result, err := s.ChangeSupportEntry(context.Background(), 7, req)
	if err != nil || !result.Found {
		t.Fatalf("cleared entry retry: %+v %v", result, err)
	}
}

func TestSupportClearedReceiptPreventsResettingNewTrafficOnRetry(t *testing.T) {
	s, mock, done := newSupportEntryTestService(t)
	defer done()
	req := supportEntitlementRequest()
	req.TransferEnable, req.ResetUsage = nil, true
	expectSupportStaffRead(mock)
	expectSupportMutationLocks(mock, req.RequestID, false)
	compact := fmt.Sprintf(`{"kind":"entitlement","request_hash":%q}`, supportEntitlementChangeHash(req))
	mock.ExpectQuery(regexp.QuoteMeta(supportRequestStateQuery)).WithArgs(int64(7), req.RequestID).
		WillReturnRows(sqlmock.NewRows([]string{"state"}).AddRow(compact))
	current := supportEntitlementFixture()
	current.U, current.D = 123, 456
	expectSupportEntitlementSnapshot(mock, current)
	mock.ExpectCommit()
	after, err := s.ChangeSupportEntitlement(context.Background(), 7, req)
	if err != nil || after.U != 123 || after.D != 456 {
		t.Fatalf("cleared receipt lost new traffic: %+v %v", after, err)
	}
}

func TestSupportClearedReceiptStillRejectsDifferentPayloadAndOperationKind(t *testing.T) {
	for _, kind := range []string{"entitlement", "entry_membership", ""} {
		t.Run(kind, func(t *testing.T) {
			s, mock, done := newSupportEntryTestService(t)
			defer done()
			req := supportEntitlementRequest()
			expectSupportStaffRead(mock)
			expectSupportMutationLocks(mock, req.RequestID, false)
			hash := supportEntitlementChangeHash(req)
			if kind == "entitlement" {
				hash = strings.Repeat("f", 64)
			}
			compact := fmt.Sprintf(`{"kind":%q,"request_hash":%q}`, kind, hash)
			mock.ExpectQuery(regexp.QuoteMeta(supportRequestStateQuery)).WithArgs(int64(7), req.RequestID).
				WillReturnRows(sqlmock.NewRows([]string{"state"}).AddRow(compact))
			mock.ExpectRollback()
			if _, err := s.ChangeSupportEntitlement(context.Background(), 7, req); !errors.Is(err, ErrSupportEntryConflict) {
				t.Fatalf("cleared request reuse: %v", err)
			}
		})
	}
}
