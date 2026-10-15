package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	usersvc "forest/go-api/internal/user"
	"github.com/DATA-DOG/go-sqlmock"
)

func TestSupportLookupAcceptsOnlyCompleteEmailOrPositiveDecimalID(t *testing.T) {
	for _, value := range []string{"", "ali", "@example.com", "alice@", "alice@example.com%", "Alice <alice@example.com>", "-1", "0", "1 OR 1=1", "9223372036854775808"} {
		if _, _, err := parseSupportLookup(value); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
	id, email, err := parseSupportLookup(" 00123 ")
	if err != nil || id != 123 || email != "" {
		t.Fatalf("ID result: %d,%s,%v", id, email, err)
	}
	id, email, err = parseSupportLookup("Alice@Example.com")
	if err != nil || id != 0 || email != "alice@example.com" {
		t.Fatalf("email result: %d,%s,%v", id, email, err)
	}
}
func TestSupportResultUsesFieldWhitelistAndAuthorizedTargets(t *testing.T) {
	result := supportUserResult(usersvc.SupportEntryPreview{User: usersvc.SupportEntryUser{ID: 5, Email: "u@example.com"}, Scopes: []usersvc.SupportEntryScope{{SourcePolicyID: 10, Targets: []SupportEntryTarget{{Key: "policy:20"}, {Key: "policy:30"}}}}}, SupportStaffRecord{TargetKeys: []string{"policy:20"}})
	if len(result.Scopes[0].Targets) != 1 || result.Scopes[0].Targets[0].Key != "policy:20" {
		t.Fatal("unauthorized target shown")
	}
	raw, _ := json.Marshal(result)
	for _, key := range []string{"password", "token", "uuid", "balance", "commission", "subscribe_url"} {
		if strings.Contains(string(raw), `"`+key+`"`) {
			t.Fatalf("sensitive field %s leaked", key)
		}
	}
}
func TestSupportChangeNormalizesAndBindsIdempotencyToEntireRequest(t *testing.T) {
	req := SupportEntryChangeRequest{UserID: 5, SourcePolicyID: 10, TargetKey: "policy:20", Reason: "repair", RequestID: "uuid-1"}
	clean, err := normalizeSupportChange(req, 1)
	if err != nil {
		t.Fatal(err)
	}
	a := supportChangeHash(clean)
	for _, change := range []func(*SupportEntryChangeRequest){func(r *SupportEntryChangeRequest) { r.UserID++ }, func(r *SupportEntryChangeRequest) { r.SourcePolicyID++ }, func(r *SupportEntryChangeRequest) { r.TargetKey = "policy:30" }, func(r *SupportEntryChangeRequest) { r.Version++ }, func(r *SupportEntryChangeRequest) { r.UA = "OtherClient" }, func(r *SupportEntryChangeRequest) { r.Reason = "other" }, func(r *SupportEntryChangeRequest) { r.ExpiresAt = 123 }} {
		next := clean
		change(&next)
		if supportChangeHash(next) == a {
			t.Fatal("idempotency hash failed to bind payload")
		}
	}
	bad := req
	bad.Restore = true
	if _, err = normalizeSupportChange(bad, 1); err == nil {
		t.Fatal("restore must not accept arbitrary target")
	}
	bad = req
	bad.Reason = " "
	if _, err = normalizeSupportChange(bad, 1); err == nil {
		t.Fatal("reason required")
	}
}
func newSupportEntryTestService(t *testing.T) (*DBService, sqlmock.Sqlmock, func()) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	s := &DBService{db: db}
	s.supportEnsureOnce.Do(func() {})
	s.clientEntryEnsureOnce.Do(func() {})
	return s, mock, func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
		db.Close()
	}
}
func expectSupportStaffRead(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(`SELECT staff.user_id, users.email, staff.enabled, staff.target_keys::TEXT`).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"id", "email", "enabled", "keys", "version", "created", "updated"}).AddRow(7, "staff@example.com", 1, `["policy:20"]`, 1, 1, 1))
}
func expectSupportMutationLocks(mock sqlmock.Sqlmock, requestID string, targetSupport bool) {
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id FROM v2_user.*FOR SHARE`).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(7))
	mock.ExpectQuery(`SELECT user_id,target_keys::TEXT FROM v2_support_staff.*FOR SHARE`).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"id", "keys"}).AddRow(7, `["policy:20"]`))
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs("7:" + requestID).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT id,is_admin,is_staff FROM v2_user.*FOR UPDATE`).WithArgs(int64(5)).WillReturnRows(sqlmock.NewRows([]string{"id", "admin", "staff"}).AddRow(5, 0, 0))
	mock.ExpectQuery(`SELECT EXISTS .*v2_support_staff`).WithArgs(int64(5)).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(targetSupport))
}
func expectSupportPreview(mock sqlmock.Sqlmock, version int64) {
	mock.ExpectQuery(`SELECT u.id,u.email,COALESCE\(p.name,''\),u.expired_at`).WithArgs(int64(5)).WillReturnRows(sqlmock.NewRows([]string{"id", "email", "plan", "expiry", "group", "plan_id", "banned", "transfer", "u", "d", "created"}).AddRow(5, "user@example.com", "Plan", nil, 1, 2, 0, 1000, 10, 20, time.Now().Unix()-86400))
	mock.ExpectQuery(`SELECT p.id,p.name,p.mode,p.action,p.conditions,p.entry_host`).WithArgs(int64(5)).WillReturnRows(sqlmock.NewRows([]string{"id", "name", "mode", "action", "conditions", "host", "assigned_host", "resolve", "type", "server_id"}).AddRow(10, "Rule A", "standard", "override", `[]`, "auto.example", "", 0, "vmess", 1))
	mock.ExpectQuery(`SELECT p.id,p.name,COALESCE\(g.id,0\)`).WillReturnRows(sqlmock.NewRows([]string{"id", "name", "group", "group_name", "host", "resolve", "type", "server_id"}).AddRow(20, "Rule B", 0, "", "target.example", 0, "vmess", 1))
	overrides := sqlmock.NewRows([]string{"source", "target", "group", "key", "active", "expiry", "version"})
	if version > 0 {
		overrides.AddRow(10, 20, 0, "policy:20", 1, 0, version)
	}
	mock.ExpectQuery(`SELECT source_policy_id,COALESCE\(target_policy_id,0\)`).WithArgs(int64(5)).WillReturnRows(overrides)
	for _, serverType := range []string{"shadowsocks", "vmess", "trojan", "tuic", "hysteria", "vless", "anytls", "v2node"} {
		rows := sqlmock.NewRows([]string{"id", "name", "group", "entry_only", "host"})
		if serverType == "vmess" {
			rows.AddRow(1, "Node", `[1]`, 0, "original.example")
		}
		mock.ExpectQuery(`SELECT id,COALESCE\(name,''\),COALESCE\(group_id,'\[\]'\),COALESCE\(client_entry_only,0\),COALESCE\(host,''\) FROM v2_server_` + serverType).WillReturnRows(rows)
	}
}
func supportTestChangeRequest() SupportEntryChangeRequest {
	return SupportEntryChangeRequest{UserID: 5, SourcePolicyID: 10, TargetKey: "policy:20", Reason: "repair", RequestID: "request-1"}
}
func TestSupportChangeRejectsOtherSupportAccountEvenWhenNotLegacyStaff(t *testing.T) {
	s, mock, done := newSupportEntryTestService(t)
	defer done()
	expectSupportStaffRead(mock)
	req := supportTestChangeRequest()
	expectSupportMutationLocks(mock, req.RequestID, true)
	mock.ExpectRollback()
	_, err := s.ChangeSupportEntry(context.Background(), 7, req)
	if !errors.Is(err, ErrSupportForbidden) {
		t.Fatalf("expected forbidden, got %v", err)
	}
}
func TestSupportChangeRechecksLiveRoleInsideTransaction(t *testing.T) {
	s, mock, done := newSupportEntryTestService(t)
	defer done()
	expectSupportStaffRead(mock)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id FROM v2_user.*FOR SHARE`).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(7))
	mock.ExpectQuery(`SELECT user_id,target_keys::TEXT FROM v2_support_staff.*FOR SHARE`).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"id", "keys"}))
	mock.ExpectRollback()
	_, err := s.ChangeSupportEntry(context.Background(), 7, supportTestChangeRequest())
	if !errors.Is(err, ErrSupportForbidden) {
		t.Fatalf("expected revoked role forbidden, got %v", err)
	}
}
func TestSupportChangeRejectsStaleVersionBeforeWriting(t *testing.T) {
	s, mock, done := newSupportEntryTestService(t)
	defer done()
	expectSupportStaffRead(mock)
	req := supportTestChangeRequest()
	expectSupportMutationLocks(mock, req.RequestID, false)
	mock.ExpectQuery(`SELECT after_state::TEXT FROM v2_support_operation`).WithArgs(int64(7), req.RequestID).WillReturnError(sql.ErrNoRows)
	expectSupportPreview(mock, 3)
	mock.ExpectRollback()
	_, err := s.ChangeSupportEntry(context.Background(), 7, req)
	if !errors.Is(err, ErrSupportEntryConflict) {
		t.Fatalf("expected stale version error, got %v", err)
	}
}
func TestSupportChangeRejectsUnauthorizedTargetOnServer(t *testing.T) {
	s, mock, done := newSupportEntryTestService(t)
	defer done()
	expectSupportStaffRead(mock)
	req := supportTestChangeRequest()
	req.TargetKey = "policy:30"
	expectSupportMutationLocks(mock, req.RequestID, false)
	mock.ExpectQuery(`SELECT after_state::TEXT FROM v2_support_operation`).WithArgs(int64(7), req.RequestID).WillReturnError(sql.ErrNoRows)
	expectSupportPreview(mock, 0)
	mock.ExpectRollback()
	_, err := s.ChangeSupportEntry(context.Background(), 7, req)
	if !errors.Is(err, ErrSupportForbidden) {
		t.Fatalf("expected server authorization error, got %v", err)
	}
}
func expectSupportMutationWrites(mock sqlmock.Sqlmock, req SupportEntryChangeRequest, logErr error) {
	mock.ExpectExec(`INSERT INTO v2_support_entry_override`).WithArgs(int64(5), int64(10), int64(20), nil, "policy:20", int64(1), nil, int64(1), int64(7), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	log := mock.ExpectExec(`INSERT INTO v2_support_operation`).WithArgs(int64(7), int64(5), int64(10), req.RequestID, req.Reason, sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg())
	if logErr != nil {
		log.WillReturnError(logErr)
	} else {
		log.WillReturnResult(sqlmock.NewResult(1, 1))
	}
}
func TestSupportChangeMutationAndAuditAreAtomic(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("audit_failure_%v", fail), func(t *testing.T) {
			s, mock, done := newSupportEntryTestService(t)
			defer done()
			expectSupportStaffRead(mock)
			req := supportTestChangeRequest()
			expectSupportMutationLocks(mock, req.RequestID, false)
			mock.ExpectQuery(`SELECT after_state::TEXT FROM v2_support_operation`).WithArgs(int64(7), req.RequestID).WillReturnError(sql.ErrNoRows)
			expectSupportPreview(mock, 0)
			var logErr error
			if fail {
				logErr = errors.New("disk full")
			}
			expectSupportMutationWrites(mock, req, logErr)
			if fail {
				mock.ExpectRollback()
			} else {
				mock.ExpectCommit()
			}
			result, err := s.ChangeSupportEntry(context.Background(), 7, req)
			if fail {
				if !errors.Is(err, logErr) {
					t.Fatalf("expected log failure, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !result.Found || result.Scopes[0].Version != 1 || result.Scopes[0].Override.EntryHost != "target.example" || result.Scopes[0].Override.Status != "active" {
				t.Fatalf("bad result: %#v", result)
			}
		})
	}
}
func TestSupportChangeIdenticalRetryIsReadOnlyAndRequestReuseRejected(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(fmt.Sprintf("changed_payload_%v", conflict), func(t *testing.T) {
			s, mock, done := newSupportEntryTestService(t)
			defer done()
			expectSupportStaffRead(mock)
			req := supportTestChangeRequest()
			saved := SupportOperationState{RequestHash: supportChangeHash(req)}
			raw, _ := json.Marshal(saved)
			if conflict {
				req.Reason = "other"
			}
			expectSupportMutationLocks(mock, req.RequestID, false)
			mock.ExpectQuery(`SELECT after_state::TEXT FROM v2_support_operation`).WithArgs(int64(7), req.RequestID).WillReturnRows(sqlmock.NewRows([]string{"state"}).AddRow(string(raw)))
			if conflict {
				mock.ExpectRollback()
			} else {
				expectSupportPreview(mock, 1)
				mock.ExpectCommit()
			}
			result, err := s.ChangeSupportEntry(context.Background(), 7, req)
			if conflict {
				if !errors.Is(err, ErrSupportEntryConflict) {
					t.Fatalf("request reuse error: %v", err)
				}
				return
			}
			if err != nil || !result.Found || result.Scopes[0].Version != 1 {
				t.Fatalf("idempotent result: %#v,%v", result, err)
			}
		})
	}
}
func TestSupportLookupDoesNotReturnPrivilegedOrAmbiguousAccounts(t *testing.T) {
	s, mock, done := newSupportEntryTestService(t)
	defer done()
	expectSupportStaffRead(mock)
	mock.ExpectQuery(`(?s)SELECT u.id FROM v2_user u WHERE LOWER\(u.email\) = \$1.*u.is_admin = 0 AND u.is_staff = 0.*NOT EXISTS .*v2_support_staff.*LIMIT 2`).WithArgs("staff@example.com").WillReturnRows(sqlmock.NewRows([]string{"id"}))
	result, err := s.LookupSupportUser(context.Background(), 7, "staff@example.com", "")
	if err != nil || result.Found || result.User != nil {
		t.Fatalf("privileged lookup: %#v,%v", result, err)
	}
}
func TestSupportOperationListFiltersActorAndStripsInternalHash(t *testing.T) {
	s, mock, done := newSupportEntryTestService(t)
	defer done()
	expectSupportStaffRead(mock)
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM v2_support_operation WHERE actor_id = \$1`).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`(?s)SELECT id,actor_id,target_user_id.*WHERE actor_id = \$1 ORDER BY id DESC LIMIT 20 OFFSET \$2`).WithArgs(int64(7), int64(0)).WillReturnRows(sqlmock.NewRows([]string{"id", "actor", "target", "source", "reason", "before", "after", "created"}).AddRow(1, 7, 5, 10, "repair", `{}`, `{"target_key":"policy:20","request_hash":"internal"}`, 100))
	result, err := s.ListSupportOperations(context.Background(), 7, 1)
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 || result.Items[0].AfterState.RequestHash != "" {
		t.Fatalf("bad list: %#v", result)
	}
}

func TestSupportChangeRestoreClearsOnlyOverrideAndWritesOneAudit(t *testing.T) {
	s, mock, done := newSupportEntryTestService(t)
	defer done()
	expectSupportStaffRead(mock)
	req := supportTestChangeRequest()
	req.Restore = true
	req.TargetKey = ""
	req.Version = 1
	expectSupportMutationLocks(mock, req.RequestID, false)
	mock.ExpectQuery(`SELECT after_state::TEXT FROM v2_support_operation`).WithArgs(int64(7), req.RequestID).WillReturnError(sql.ErrNoRows)
	expectSupportPreview(mock, 1)
	mock.ExpectExec(`INSERT INTO v2_support_entry_override`).WithArgs(int64(5), int64(10), nil, nil, "", int64(0), nil, int64(2), int64(7), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO v2_support_operation`).WithArgs(int64(7), int64(5), int64(10), req.RequestID, req.Reason, sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	result, err := s.ChangeSupportEntry(context.Background(), 7, req)
	if err != nil {
		t.Fatal(err)
	}
	scope := result.Scopes[0]
	if scope.Override.Active || scope.Override.TargetKey != "" || scope.Version != 2 || scope.Override.Status != "restored" {
		t.Fatalf("unexpected restored scope: %#v", scope)
	}
}
func TestSupportChangeNoopDoesNotEmitLog(t *testing.T) {
	s, mock, done := newSupportEntryTestService(t)
	defer done()
	expectSupportStaffRead(mock)
	req := supportTestChangeRequest()
	req.Version = 1
	expectSupportMutationLocks(mock, req.RequestID, false)
	mock.ExpectQuery(`SELECT after_state::TEXT FROM v2_support_operation`).WithArgs(int64(7), req.RequestID).WillReturnError(sql.ErrNoRows)
	expectSupportPreview(mock, 1)
	mock.ExpectRollback()
	_, err := s.ChangeSupportEntry(context.Background(), 7, req)
	if !errors.Is(err, ErrSupportEntryInvalid) {
		t.Fatalf("expected no-op rejected without log: %v", err)
	}
}
func TestSupportChangeExpiresAtMustBeFutureForFirstExecution(t *testing.T) {
	s, mock, done := newSupportEntryTestService(t)
	defer done()
	expectSupportStaffRead(mock)
	req := supportTestChangeRequest()
	req.ExpiresAt = 1
	expectSupportMutationLocks(mock, req.RequestID, false)
	mock.ExpectQuery(`SELECT after_state::TEXT FROM v2_support_operation`).WithArgs(int64(7), req.RequestID).WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()
	_, err := s.ChangeSupportEntry(context.Background(), 7, req)
	if !errors.Is(err, ErrSupportEntryInvalid) {
		t.Fatalf("expected past expiry rejected: %v", err)
	}
}

func TestSupportReadOnlyStaffCannotRestoreExistingOverride(t *testing.T) {
	s, mock, done := newSupportEntryTestService(t)
	defer done()
	expectSupportStaffRead(mock)
	req := supportTestChangeRequest()
	req.Restore = true
	req.TargetKey = ""
	req.Version = 1
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id FROM v2_user.*FOR SHARE`).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(7))
	mock.ExpectQuery(`SELECT user_id,target_keys::TEXT FROM v2_support_staff.*FOR SHARE`).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"id", "keys"}).AddRow(7, `[]`))
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs("7:" + req.RequestID).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT id,is_admin,is_staff FROM v2_user.*FOR UPDATE`).WithArgs(int64(5)).WillReturnRows(sqlmock.NewRows([]string{"id", "admin", "staff"}).AddRow(5, 0, 0))
	mock.ExpectQuery(`SELECT EXISTS .*v2_support_staff`).WithArgs(int64(5)).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery(`SELECT after_state::TEXT FROM v2_support_operation`).WithArgs(int64(7), req.RequestID).WillReturnError(sql.ErrNoRows)
	expectSupportPreview(mock, 1)
	mock.ExpectRollback()
	_, err := s.ChangeSupportEntry(context.Background(), 7, req)
	if !errors.Is(err, ErrSupportForbidden) {
		t.Fatalf("read-only staff restoration must be forbidden: %v", err)
	}
}

func TestSupportTargetLockFiltersKnownPrivilegedAccountsBeforeLocking(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery(`(?s)SELECT id,is_admin,is_staff FROM v2_user WHERE id = \$1 AND is_admin = 0 AND is_staff = 0.*NOT EXISTS .*v2_support_staff.*FOR UPDATE`).WithArgs(int64(8)).WillReturnError(sql.ErrNoRows)
	if err = lockSupportTargetUser(context.Background(), tx, 8); !errors.Is(err, ErrSupportEntryNotFound) {
		t.Fatalf("expected hidden privileged target, got %v", err)
	}
	mock.ExpectRollback()
	tx.Rollback()
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
func TestSupportResultCanRestoreUsesRoleNotCurrentCompatibleTargets(t *testing.T) {
	preview := usersvc.SupportEntryPreview{Scopes: []usersvc.SupportEntryScope{{Targets: []SupportEntryTarget{}}}}
	if supportUserResult(preview, SupportStaffRecord{}).CanRestore {
		t.Fatal("readonly role cannot restore")
	}
	if !supportUserResult(preview, SupportStaffRecord{TargetKeys: []string{"policy:deleted"}}).CanRestore {
		t.Fatal("role with stale authorized destination should still restore old override")
	}
}
