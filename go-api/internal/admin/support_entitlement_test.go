package admin

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func supportEntitlementInt(value int64) *int64 { return &value }

func supportEntitlementFixture() SupportEntitlementSnapshot {
	state := SupportEntitlementSnapshot{UserID: 5, PlanID: supportEntitlementInt(2), PlanName: "Current plan", GroupID: supportEntitlementInt(1), TransferEnable: 1000, U: 10, D: 20, ExpiredAt: supportEntitlementInt(1900000000), DeviceLimit: supportEntitlementInt(3), SpeedLimit: supportEntitlementInt(100)}
	state.Version = supportEntitlementVersion(state)
	return state
}

func supportEntitlementRequest() SupportEntitlementChangeRequest {
	return SupportEntitlementChangeRequest{UserID: 5, TransferEnable: supportEntitlementInt(2000), Reason: "补发未到账流量", RequestID: "entitlement-1", Expected: supportEntitlementFixture().Version}
}

func expectSupportEntitlementSnapshot(mock sqlmock.Sqlmock, state SupportEntitlementSnapshot) {
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT u.id,u.plan_id,COALESCE(p.name,''),u.group_id,u.transfer_enable,u.u,u.d,u.expired_at,u.device_limit,u.speed_limit`)).WithArgs(state.UserID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "plan_id", "plan", "group", "transfer_enable", "u", "d", "expired_at", "device_limit", "speed_limit"}).AddRow(state.UserID, nullableInt64(state.PlanID), state.PlanName, nullableInt64(state.GroupID), state.TransferEnable, state.U, state.D, nullableInt64(state.ExpiredAt), nullableInt64(state.DeviceLimit), nullableInt64(state.SpeedLimit)))
}

func expectSupportEntitlementMutationStart(mock sqlmock.Sqlmock, req SupportEntitlementChangeRequest, state SupportEntitlementSnapshot) {
	expectSupportStaffRead(mock)
	expectSupportMutationLocks(mock, req.RequestID, false)
	mock.ExpectQuery(`SELECT after_state::TEXT FROM v2_support_operation`).WithArgs(int64(7), req.RequestID).WillReturnError(sql.ErrNoRows)
	expectSupportEntitlementSnapshot(mock, state)
}

type supportEntitlementLogMatcher struct {
	t      *testing.T
	before bool
	check  func(SupportEntitlementSnapshot) bool
}

func (m supportEntitlementLogMatcher) Match(value driver.Value) bool {
	raw, ok := value.(string)
	if !ok {
		return false
	}
	var state supportEntitlementOperationState
	if json.Unmarshal([]byte(raw), &state) != nil || state.Kind != "entitlement" || state.UserID != 5 || (state.RequestHash == "") != m.before {
		return false
	}
	for _, key := range []string{"password", "token", "uuid", "balance", "is_staff", "is_admin"} {
		if strings.Contains(raw, `"`+key+`"`) {
			m.t.Errorf("unrelated field leaked into audit: %s", key)
			return false
		}
	}
	return m.check == nil || m.check(state.SupportEntitlementSnapshot)
}

func expectSupportEntitlementLog(mock sqlmock.Sqlmock, t *testing.T, req SupportEntitlementChangeRequest, afterCheck func(SupportEntitlementSnapshot) bool) {
	mock.ExpectExec(`INSERT INTO v2_support_operation`).WithArgs(int64(7), int64(5), int64(0), req.RequestID, req.Reason, supportEntitlementLogMatcher{t: t, before: true}, supportEntitlementLogMatcher{t: t, check: afterCheck}, sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
}

func TestSupportEntitlementNormalizesWhitelistAndNullableExpiry(t *testing.T) {
	base := supportEntitlementRequest()
	for _, mutate := range []func(*SupportEntitlementChangeRequest){
		func(r *SupportEntitlementChangeRequest) { r.UserID = 0 },
		func(r *SupportEntitlementChangeRequest) { r.Reason = " " },
		func(r *SupportEntitlementChangeRequest) { r.Reason = strings.Repeat("字", 501) },
		func(r *SupportEntitlementChangeRequest) { r.RequestID = "bad id" },
		func(r *SupportEntitlementChangeRequest) { r.Expected = "" },
		func(r *SupportEntitlementChangeRequest) { r.Expected = strings.Repeat("Z", 64) },
		func(r *SupportEntitlementChangeRequest) { r.PlanID = supportEntitlementInt(0) },
		func(r *SupportEntitlementChangeRequest) { r.TransferEnable = supportEntitlementInt(-1) },
		func(r *SupportEntitlementChangeRequest) {
			r.TransferEnable = supportEntitlementInt(supportMaxTrafficBytes + 1)
		},
		func(r *SupportEntitlementChangeRequest) { r.ExpiredAt = json.RawMessage(`0`) },
		func(r *SupportEntitlementChangeRequest) { r.ExpiredAt = json.RawMessage(`-1`) },
		func(r *SupportEntitlementChangeRequest) { r.ExpiredAt = json.RawMessage(`1.5`) },
		func(r *SupportEntitlementChangeRequest) { r.ExpiredAt = json.RawMessage(`"1900000000"`) },
		func(r *SupportEntitlementChangeRequest) { r.ExpiredAt = json.RawMessage(`{}`) },
		func(r *SupportEntitlementChangeRequest) { r.TransferEnable = nil },
	} {
		req := base
		mutate(&req)
		if _, err := normalizeSupportEntitlementChange(req); !errors.Is(err, ErrSupportEntryInvalid) {
			t.Fatalf("accepted invalid request: %+v, err=%v", req, err)
		}
	}
	for _, expiry := range []string{"null", "  null \n", "1900000000"} {
		req := base
		req.ExpiredAt = json.RawMessage(expiry)
		clean, err := normalizeSupportEntitlementChange(req)
		if err != nil || string(clean.ExpiredAt) != strings.TrimSpace(expiry) {
			t.Fatalf("expiry %q: %s %v", expiry, clean.ExpiredAt, err)
		}
	}
}

func TestSupportEntitlementVersionIgnoresUsageAndDisplayNamesButBindsConfiguration(t *testing.T) {
	base := supportEntitlementFixture()
	usage := base
	usage.U, usage.D, usage.PlanName, usage.Version = 12345, 98765, "Renamed", "irrelevant"
	if supportEntitlementVersion(base) != supportEntitlementVersion(usage) {
		t.Fatal("usage/display changes must not conflict with entitlement repair")
	}
	for _, mutate := range []func(*SupportEntitlementSnapshot){
		func(s *SupportEntitlementSnapshot) { s.UserID++ },
		func(s *SupportEntitlementSnapshot) { s.PlanID = supportEntitlementInt(3) },
		func(s *SupportEntitlementSnapshot) { s.GroupID = nil },
		func(s *SupportEntitlementSnapshot) { s.TransferEnable++ },
		func(s *SupportEntitlementSnapshot) { s.ExpiredAt = nil },
		func(s *SupportEntitlementSnapshot) { s.DeviceLimit = nil },
		func(s *SupportEntitlementSnapshot) { s.SpeedLimit = nil },
	} {
		next := base
		mutate(&next)
		if supportEntitlementVersion(next) == supportEntitlementVersion(base) {
			t.Fatal("configuration change not reflected in version")
		}
	}
}

func TestSupportEntitlementHashBindsAllPayloadFieldsAndOperationKind(t *testing.T) {
	base := supportEntitlementRequest()
	hash := supportEntitlementChangeHash(base)
	for _, mutate := range []func(*SupportEntitlementChangeRequest){
		func(r *SupportEntitlementChangeRequest) { r.UserID++ },
		func(r *SupportEntitlementChangeRequest) { r.PlanID = supportEntitlementInt(4) },
		func(r *SupportEntitlementChangeRequest) { r.TransferEnable = supportEntitlementInt(3) },
		func(r *SupportEntitlementChangeRequest) { r.ExpiredAt = json.RawMessage(`null`) },
		func(r *SupportEntitlementChangeRequest) { r.ResetUsage = true },
		func(r *SupportEntitlementChangeRequest) { r.Reason = "other" },
		func(r *SupportEntitlementChangeRequest) { r.Expected = strings.Repeat("0", 64) },
	} {
		next := base
		mutate(&next)
		if supportEntitlementChangeHash(next) == hash {
			t.Fatal("idempotency did not bind full payload")
		}
	}
	base.RequestID = "new-key"
	if supportEntitlementChangeHash(base) != hash {
		t.Fatal("request key itself should not affect payload hash")
	}
}

func TestSupportEntitlementQuotaRepairDoesNotRewriteLiveUsage(t *testing.T) {
	s, mock, done := newSupportEntryTestService(t)
	defer done()
	req := supportEntitlementRequest()
	state := supportEntitlementFixture()
	state.U, state.D = 100, 200 // traffic changed since the employee's lookup
	expectSupportEntitlementMutationStart(mock, req, state)
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE v2_user SET transfer_enable = $2, updated_at = $3 WHERE id = $1`)).WithArgs(int64(5), int64(2000), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	expectSupportEntitlementLog(mock, t, req, func(s SupportEntitlementSnapshot) bool { return s.TransferEnable == 2000 && s.U == 100 && s.D == 200 })
	after, err := s.ChangeSupportEntitlement(context.Background(), 7, req)
	if err != nil || after.TransferEnable != 2000 || after.U != 100 || after.D != 200 || after.Version == state.Version {
		t.Fatalf("repair result: %+v err=%v", after, err)
	}
}

func TestSupportEntitlementSwitchPlanSynchronizesEntitlementsAndPreservesUsage(t *testing.T) {
	for _, customQuota := range []bool{false, true} {
		t.Run(map[bool]string{false: "plan quota", true: "explicit quota"}[customQuota], func(t *testing.T) {
			s, mock, done := newSupportEntryTestService(t)
			defer done()
			req := supportEntitlementRequest()
			req.PlanID = supportEntitlementInt(4)
			req.TransferEnable = nil
			wantQuota := 50 * planTrafficGiB
			if customQuota {
				req.TransferEnable = supportEntitlementInt(2345)
				wantQuota = 2345
			}
			expectSupportEntitlementMutationStart(mock, req, supportEntitlementFixture())
			mock.ExpectQuery(`SELECT id,name,group_id,transfer_enable,device_limit,speed_limit FROM v2_plan WHERE id = \$1 FOR SHARE`).WithArgs(int64(4)).WillReturnRows(sqlmock.NewRows([]string{"id", "name", "group", "quota", "devices", "speed"}).AddRow(4, "VIP plan", 8, 50, nil, 200))
			mock.ExpectExec(regexp.QuoteMeta(`UPDATE v2_user SET plan_id = $2, group_id = $3, device_limit = $4, speed_limit = $5, transfer_enable = $6, updated_at = $7 WHERE id = $1`)).WithArgs(int64(5), int64(4), int64(8), nil, int64(200), wantQuota, sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
			expectSupportEntitlementLog(mock, t, req, func(s SupportEntitlementSnapshot) bool {
				return s.PlanName == "VIP plan" && s.TransferEnable == wantQuota && s.U == 10 && s.D == 20 && s.DeviceLimit == nil
			})
			after, err := s.ChangeSupportEntitlement(context.Background(), 7, req)
			if err != nil || *after.PlanID != 4 || *after.GroupID != 8 || after.DeviceLimit != nil || *after.SpeedLimit != 200 || after.TransferEnable != wantQuota || *after.ExpiredAt != 1900000000 {
				t.Fatalf("plan repair: %+v, %v", after, err)
			}
		})
	}
}

func TestSupportEntitlementExpiryPermanentAndExplicitUsageReset(t *testing.T) {
	s, mock, done := newSupportEntryTestService(t)
	defer done()
	req := supportEntitlementRequest()
	req.TransferEnable, req.ExpiredAt, req.ResetUsage = nil, json.RawMessage(`null`), true
	expectSupportEntitlementMutationStart(mock, req, supportEntitlementFixture())
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE v2_user SET expired_at = $2, u = $3, d = $4, updated_at = $5 WHERE id = $1`)).WithArgs(int64(5), nil, int64(0), int64(0), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	expectSupportEntitlementLog(mock, t, req, func(s SupportEntitlementSnapshot) bool {
		return s.ExpiredAt == nil && s.U == 0 && s.D == 0 && s.TransferEnable == 1000
	})
	after, err := s.ChangeSupportEntitlement(context.Background(), 7, req)
	if err != nil || after.ExpiredAt != nil || after.U != 0 || after.D != 0 {
		t.Fatalf("expiry/reset repair: %+v, %v", after, err)
	}
}

func TestSupportEntitlementExpiryOnlyDoesNotRewritePlanQuotaOrUsage(t *testing.T) {
	s, mock, done := newSupportEntryTestService(t)
	defer done()
	req := supportEntitlementRequest()
	req.TransferEnable, req.ExpiredAt = nil, json.RawMessage(`2000000000`)
	expectSupportEntitlementMutationStart(mock, req, supportEntitlementFixture())
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE v2_user SET expired_at = $2, updated_at = $3 WHERE id = $1`)).WithArgs(int64(5), int64(2000000000), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	expectSupportEntitlementLog(mock, t, req, func(s SupportEntitlementSnapshot) bool {
		return *s.ExpiredAt == 2000000000 && *s.PlanID == 2 && s.TransferEnable == 1000 && s.U == 10 && s.D == 20
	})
	if _, err := s.ChangeSupportEntitlement(context.Background(), 7, req); err != nil {
		t.Fatal(err)
	}
}

func TestSupportEntitlementSnapshotNormalizesLegacyPermanentExpiry(t *testing.T) {
	for _, expiry := range []int64{0, -1} {
		s, mock, done := newSupportEntryTestService(t)
		state := supportEntitlementFixture()
		state.ExpiredAt = supportEntitlementInt(expiry)
		expectSupportEntitlementSnapshot(mock, state)
		result, err := LoadSupportEntitlementSnapshot(context.Background(), s.db, 5)
		if err != nil || result.ExpiredAt != nil {
			t.Errorf("legacy permanent expiry %d: %+v %v", expiry, result, err)
		}
		state.ExpiredAt = nil
		if result.Version != supportEntitlementVersion(state) {
			t.Error("permanent expiry forms must share the same version")
		}
		done()
	}
}

func TestSupportEntitlementRejectsStaleAndNoopWithoutAuditOrMutation(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(map[bool]string{false: "no change", true: "stale"}[stale], func(t *testing.T) {
			s, mock, done := newSupportEntryTestService(t)
			defer done()
			req := supportEntitlementRequest()
			wantErr := ErrSupportEntryInvalid
			if stale {
				req.Expected = strings.Repeat("0", 64)
				wantErr = ErrSupportEntryConflict
			} else {
				req.TransferEnable = supportEntitlementInt(1000)
				req.PlanID = supportEntitlementInt(2) // selecting current plan does not reset custom limits
			}
			expectSupportEntitlementMutationStart(mock, req, supportEntitlementFixture())
			mock.ExpectRollback()
			if _, err := s.ChangeSupportEntitlement(context.Background(), 7, req); !errors.Is(err, wantErr) {
				t.Fatalf("expected %v, got %v", wantErr, err)
			}
		})
	}
}

func TestSupportEntitlementRechecksActorAndHidesPrivilegedTargets(t *testing.T) {
	for _, actorRevoked := range []bool{false, true} {
		t.Run(map[bool]string{false: "privileged target", true: "revoked employee"}[actorRevoked], func(t *testing.T) {
			s, mock, done := newSupportEntryTestService(t)
			defer done()
			expectSupportStaffRead(mock)
			wantErr := ErrSupportEntryNotFound
			if actorRevoked {
				mock.ExpectBegin()
				mock.ExpectQuery(`SELECT id,email FROM v2_user.*is_staff = 1 FOR SHARE`).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"id", "email"}))
				wantErr = ErrSupportForbidden
			} else {
				expectSupportMutationLocks(mock, "entitlement-1", true)
			}
			mock.ExpectRollback()
			if _, err := s.ChangeSupportEntitlement(context.Background(), 7, supportEntitlementRequest()); !errors.Is(err, wantErr) {
				t.Fatalf("expected %v, got %v", wantErr, err)
			}
		})
	}
}

func TestSupportEntitlementRetryDoesNotMutateOrAuditAgain(t *testing.T) {
	s, mock, done := newSupportEntryTestService(t)
	defer done()
	req := supportEntitlementRequest()
	expectSupportStaffRead(mock)
	expectSupportMutationLocks(mock, req.RequestID, false)
	saved, _ := json.Marshal(supportEntitlementOperationState{Kind: "entitlement", RequestHash: supportEntitlementChangeHash(req)})
	mock.ExpectQuery(`SELECT after_state::TEXT FROM v2_support_operation`).WithArgs(int64(7), req.RequestID).WillReturnRows(sqlmock.NewRows([]string{"state"}).AddRow(string(saved)))
	current := supportEntitlementFixture()
	current.TransferEnable, current.U = 2000, 123
	expectSupportEntitlementSnapshot(mock, current)
	mock.ExpectCommit()
	after, err := s.ChangeSupportEntitlement(context.Background(), 7, req)
	if err != nil || after.TransferEnable != 2000 || after.U != 123 {
		t.Fatalf("retry result: %+v %v", after, err)
	}
}

func TestSupportEntitlementResetRetryPreservesTrafficRecordedAfterOriginalReset(t *testing.T) {
	s, mock, done := newSupportEntryTestService(t)
	defer done()
	req := supportEntitlementRequest()
	req.TransferEnable, req.ResetUsage = nil, true
	expectSupportStaffRead(mock)
	expectSupportMutationLocks(mock, req.RequestID, false)
	saved, _ := json.Marshal(supportEntitlementOperationState{Kind: "entitlement", RequestHash: supportEntitlementChangeHash(req)})
	mock.ExpectQuery(`SELECT after_state::TEXT FROM v2_support_operation`).WithArgs(int64(7), req.RequestID).WillReturnRows(sqlmock.NewRows([]string{"state"}).AddRow(string(saved)))
	current := supportEntitlementFixture()
	current.U, current.D = 40, 60
	expectSupportEntitlementSnapshot(mock, current)
	mock.ExpectCommit()
	after, err := s.ChangeSupportEntitlement(context.Background(), 7, req)
	if err != nil || after.U != 40 || after.D != 60 {
		t.Fatalf("retry reset lost new usage: %+v %v", after, err)
	}
}

func TestSupportEntitlementRequestKeyRejectsDifferentPayloadOrEntryOperation(t *testing.T) {
	for _, kind := range []string{"entry_membership", "entitlement"} {
		t.Run(kind, func(t *testing.T) {
			s, mock, done := newSupportEntryTestService(t)
			defer done()
			req := supportEntitlementRequest()
			expectSupportStaffRead(mock)
			expectSupportMutationLocks(mock, req.RequestID, false)
			hash := supportEntitlementChangeHash(req)
			if kind == "entitlement" {
				hash = "different payload"
			}
			saved, _ := json.Marshal(supportEntitlementOperationState{Kind: kind, RequestHash: hash})
			mock.ExpectQuery(`SELECT after_state::TEXT FROM v2_support_operation`).WithArgs(int64(7), req.RequestID).WillReturnRows(sqlmock.NewRows([]string{"state"}).AddRow(string(saved)))
			mock.ExpectRollback()
			if _, err := s.ChangeSupportEntitlement(context.Background(), 7, req); !errors.Is(err, ErrSupportEntryConflict) {
				t.Fatalf("expected idempotency conflict: %v", err)
			}
		})
	}
}

func TestSupportEntitlementAuditFailureRollsBackRepair(t *testing.T) {
	s, mock, done := newSupportEntryTestService(t)
	defer done()
	req := supportEntitlementRequest()
	expectSupportEntitlementMutationStart(mock, req, supportEntitlementFixture())
	mock.ExpectExec(`UPDATE v2_user SET transfer_enable`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO v2_support_operation`).WillReturnError(errors.New("audit unavailable"))
	mock.ExpectRollback()
	if _, err := s.ChangeSupportEntitlement(context.Background(), 7, req); err == nil {
		t.Fatal("repair must not succeed without its change audit")
	}
}

func TestSupportEntitlementOptionsExposeOnlyPlanEntitlements(t *testing.T) {
	s, mock, done := newSupportEntryTestService(t)
	defer done()
	expectSupportStaffRead(mock)
	mock.ExpectQuery(`SELECT id,name,transfer_enable FROM v2_plan ORDER BY sort ASC NULLS LAST,id`).WillReturnRows(sqlmock.NewRows([]string{"id", "name", "quota"}).AddRow(2, "Existing hidden plan", 50).AddRow(4, "Byte quota plan", 10*planTrafficGiB))
	result, err := s.GetSupportEntitlementOptions(context.Background(), 7)
	if err != nil || len(result) != 2 || result[0].TransferEnable != 50*planTrafficGiB || result[1].TransferEnable != 10*planTrafficGiB {
		t.Fatalf("options: %+v %v", result, err)
	}
	raw, _ := json.Marshal(result)
	for _, field := range []string{"price", "capacity", "content", "group_id"} {
		if strings.Contains(string(raw), field) {
			t.Fatalf("unexpected option field: %s", field)
		}
	}
}
