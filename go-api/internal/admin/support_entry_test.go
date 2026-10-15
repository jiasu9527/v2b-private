package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"forest/go-api/internal/cliententry"
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
		t.Fatalf("ID: %d,%s,%v", id, email, err)
	}
	id, email, err = parseSupportLookup("Alice@Example.com")
	if err != nil || id != 0 || email != "alice@example.com" {
		t.Fatalf("email: %d,%s,%v", id, email, err)
	}
}
func TestSupportResultUsesFieldWhitelistAndMembershipTargets(t *testing.T) {
	result := supportUserResult(usersvc.SupportEntryPreview{User: usersvc.SupportEntryUser{ID: 5, Email: "u@example.com"}, EntryMembership: usersvc.SupportEntryMembership{Targets: []SupportEntryTarget{{Key: "policy:20"}}}})
	if len(result.EntryMembership.Targets) != 1 {
		t.Fatal("membership destination lost")
	}
	raw, _ := json.Marshal(result)
	for _, key := range []string{"password", "token", "uuid", "balance", "commission", "subscribe_url"} {
		if strings.Contains(string(raw), `"`+key+`"`) {
			t.Fatalf("leaked %s", key)
		}
	}
}
func TestSupportChangeNormalizesAndBindsIdempotencyToEntireRequest(t *testing.T) {
	req := SupportEntryChangeRequest{UserID: 5, TargetKey: "policy:20", Reason: "repair", RequestID: "uuid-1"}
	clean, err := normalizeSupportChange(req, 1)
	if err != nil {
		t.Fatal(err)
	}
	hash := supportChangeHash(clean)
	for _, change := range []func(*SupportEntryChangeRequest){func(r *SupportEntryChangeRequest) { r.UserID++ }, func(r *SupportEntryChangeRequest) { r.TargetKey = "policy:30" }, func(r *SupportEntryChangeRequest) { r.Version++ }, func(r *SupportEntryChangeRequest) { r.UA = "Other" }, func(r *SupportEntryChangeRequest) { r.Reason = "other" }} {
		next := clean
		change(&next)
		if supportChangeHash(next) == hash {
			t.Fatal("request hash failed to bind payload")
		}
	}
	for _, change := range []func(*SupportEntryChangeRequest){func(r *SupportEntryChangeRequest) { r.SourcePolicyID = 10 }, func(r *SupportEntryChangeRequest) { r.ExpiresAt = 1 }, func(r *SupportEntryChangeRequest) { r.Restore = true }, func(r *SupportEntryChangeRequest) { r.Reason = " " }, func(r *SupportEntryChangeRequest) { r.Version = 1 << 53 }} {
		bad := req
		change(&bad)
		if _, err := normalizeSupportChange(bad, 1); err == nil {
			t.Fatalf("accepted invalid/legacy write: %#v", bad)
		}
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
	mock.ExpectQuery(`SELECT id,email FROM v2_user WHERE id = \$1 AND banned = 0 AND is_admin = 0 AND is_staff = 1`).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"id", "email"}).AddRow(7, "staff@example.com"))
}
func expectSupportMutationLocks(mock sqlmock.Sqlmock, requestID string, targetStaff bool) {
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id,email FROM v2_user.*is_staff = 1 FOR SHARE`).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"id", "email"}).AddRow(7, "staff@example.com"))
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs("7:" + requestID).WillReturnResult(sqlmock.NewResult(0, 0))
	rows := sqlmock.NewRows([]string{"id", "admin", "staff"})
	if !targetStaff {
		rows.AddRow(5, 0, 0)
	}
	mock.ExpectQuery(`SELECT id,is_admin,is_staff FROM v2_user.*is_admin = 0 AND is_staff = 0 FOR UPDATE`).WithArgs(int64(5)).WillReturnRows(rows)
}

type supportMembershipFixture struct{ target, old, oldEnabled, legacy bool }

func supportMembershipConditions(member bool, other bool) string {
	values := []string{}
	if other {
		values = append(values, "other@example.com")
	}
	if member {
		values = append(values, "user@example.com")
	}
	raw, _ := json.Marshal(values)
	return `[{"field":"email","operator":"in","values":` + string(raw) + `}]`
}
func supportMembershipRows(f supportMembershipFixture) *sqlmock.Rows {
	enabled := 0
	if f.oldEnabled {
		enabled = 1
	}
	return sqlmock.NewRows([]string{"id", "name", "enabled", "conditions", "host", "resolve", "has_members", "sort", "members"}).AddRow(20, "VIP", 1, supportMembershipConditions(f.target, true), "target.example", 0, true, 10, "vmess:1").AddRow(30, "Old", enabled, supportMembershipConditions(f.old, false), "old.example", 0, true, 20, "vless:2")
}
func supportLegacyRecords(f supportMembershipFixture) []usersvc.SupportEntryOverride {
	if f.legacy {
		return []usersvc.SupportEntryOverride{{SourcePolicyID: 10, TargetPolicyID: 20, TargetKey: "policy:20", Active: true, Version: 1}}
	}
	return []usersvc.SupportEntryOverride{}
}
func supportMembershipVersion(t *testing.T, f supportMembershipFixture) int64 {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery(`(?s)SELECT p.id,p.name,p.enabled,p.conditions,p.entry_host,p.resolve_entry_host`).WillReturnRows(supportMembershipRows(f))
	state, err := usersvc.LoadSupportEntryMembership(context.Background(), db, cliententry.Subject{UserID: 5, Email: "user@example.com", PlanID: 2}, nil, supportLegacyRecords(f), 100)
	if err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	return state.Version
}
func expectSupportPolicyLock(mock sqlmock.Sqlmock, f supportMembershipFixture) {
	mock.ExpectExec(`SELECT pg_advisory_xact_lock\(\$1\)`).WithArgs(clientEntryVisibleOrderLockKey).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`(?s)SELECT p.id,p.name,p.enabled,p.conditions,p.entry_host,p.resolve_entry_host.*ORDER BY p.id FOR UPDATE OF p`).WillReturnRows(supportMembershipRows(f))
}
func expectSupportPreview(mock sqlmock.Sqlmock, f supportMembershipFixture) {
	mock.ExpectQuery(`SELECT u.id,u.email,COALESCE\(p.name,''\),u.expired_at`).WithArgs(int64(5)).WillReturnRows(sqlmock.NewRows([]string{"id", "email", "plan", "expiry", "group", "plan_id", "banned", "transfer", "u", "d", "created"}).AddRow(5, "user@example.com", "Plan", nil, 1, 2, 0, 1000, 10, 20, 100))
	policies := sqlmock.NewRows([]string{"id", "name", "mode", "action", "conditions", "host", "assigned", "resolve", "type", "server_id"})
	if f.target {
		policies.AddRow(20, "VIP", "standard", "override", supportMembershipConditions(true, true), "target.example", "", 0, "vmess", 1)
	}
	policies.AddRow(10, "Default", "standard", "override", `[]`, "auto.example", "", 0, "vmess", 1)
	mock.ExpectQuery(`SELECT p.id,p.name,p.mode,p.action,p.conditions,p.entry_host`).WithArgs(int64(5)).WillReturnRows(policies)
	mock.ExpectQuery(`SELECT p.id,p.name,COALESCE\(g.id,0\)`).WillReturnRows(sqlmock.NewRows([]string{"id", "name", "group", "group_name", "host", "resolve", "type", "server_id"}).AddRow(20, "VIP", 0, "", "target.example", 0, "vmess", 1))
	records := sqlmock.NewRows([]string{"source", "target", "group", "key", "active", "expiry", "version"})
	if f.legacy {
		records.AddRow(10, 20, 0, "policy:20", 1, 0, 1)
	}
	mock.ExpectQuery(`SELECT source_policy_id,COALESCE\(target_policy_id,0\)`).WithArgs(int64(5)).WillReturnRows(records)
	for _, serverType := range []string{"shadowsocks", "vmess", "trojan", "tuic", "hysteria", "vless", "anytls", "v2node"} {
		rows := sqlmock.NewRows([]string{"id", "name", "group", "entry_only", "host"})
		if serverType == "vmess" {
			rows.AddRow(1, "Node", `[1]`, 0, "original.example")
		}
		mock.ExpectQuery(`SELECT id,COALESCE\(name,''\),COALESCE\(group_id,'\[\]'\),COALESCE\(client_entry_only,0\),COALESCE\(host,''\) FROM v2_server_` + serverType).WillReturnRows(rows)
	}
	mock.ExpectQuery(`(?s)SELECT p.id,p.name,p.enabled,p.conditions,p.entry_host,p.resolve_entry_host`).WillReturnRows(supportMembershipRows(f))
}
func expectSupportEntitlementForEntry(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(`(?s)SELECT u.id,u.plan_id,COALESCE\(p.name,''\),u.group_id,u.transfer_enable,u.u,u.d,u.expired_at,u.device_limit,u.speed_limit`).WithArgs(int64(5)).WillReturnRows(sqlmock.NewRows([]string{"id", "plan_id", "plan_name", "group_id", "transfer_enable", "u", "d", "expired_at", "device_limit", "speed_limit"}).AddRow(5, 2, "Plan", 1, 1000, 10, 20, nil, nil, nil))
}
func supportTestChangeRequest(t *testing.T, f supportMembershipFixture) SupportEntryChangeRequest {
	return SupportEntryChangeRequest{UserID: 5, TargetKey: "policy:20", Reason: "repair", RequestID: "request-1", Version: supportMembershipVersion(t, f)}
}
func expectSupportIdempotencyMissing(mock sqlmock.Sqlmock, req SupportEntryChangeRequest) {
	mock.ExpectQuery(regexp.QuoteMeta(supportRequestStateQuery)).WithArgs(int64(7), req.RequestID).WillReturnError(sql.ErrNoRows)
}
func TestSupportChangeRejectsOtherStaffAccounts(t *testing.T) {
	s, mock, done := newSupportEntryTestService(t)
	defer done()
	expectSupportStaffRead(mock)
	req := supportTestChangeRequest(t, supportMembershipFixture{})
	expectSupportMutationLocks(mock, req.RequestID, true)
	mock.ExpectRollback()
	_, err := s.ChangeSupportEntry(context.Background(), 7, req)
	if !errors.Is(err, ErrSupportEntryNotFound) {
		t.Fatalf("privileged target: %v", err)
	}
}
func TestSupportChangeRechecksLiveRoleInsideTransaction(t *testing.T) {
	s, mock, done := newSupportEntryTestService(t)
	defer done()
	expectSupportStaffRead(mock)
	req := supportTestChangeRequest(t, supportMembershipFixture{})
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id,email FROM v2_user.*is_staff = 1 FOR SHARE`).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"id", "email"}))
	mock.ExpectRollback()
	_, err := s.ChangeSupportEntry(context.Background(), 7, req)
	if !errors.Is(err, ErrSupportForbidden) {
		t.Fatalf("revoked role: %v", err)
	}
}
func TestSupportChangeRejectsStaleVersionAndUnavailableTarget(t *testing.T) {
	for _, stale := range []bool{true, false} {
		t.Run(fmt.Sprint(stale), func(t *testing.T) {
			f := supportMembershipFixture{}
			s, mock, done := newSupportEntryTestService(t)
			defer done()
			req := supportTestChangeRequest(t, f)
			if stale {
				req.Version++
			} else {
				req.TargetKey = "policy:999"
			}
			expectSupportStaffRead(mock)
			expectSupportMutationLocks(mock, req.RequestID, false)
			expectSupportIdempotencyMissing(mock, req)
			expectSupportPolicyLock(mock, f)
			expectSupportPreview(mock, f)
			mock.ExpectRollback()
			_, err := s.ChangeSupportEntry(context.Background(), 7, req)
			want := ErrSupportEntryInvalid
			if stale {
				want = ErrSupportEntryConflict
			}
			if !errors.Is(err, want) {
				t.Fatalf("got %v want %v", err, want)
			}
		})
	}
}
func expectSupportUniqueEntryEmail(mock sqlmock.Sqlmock, collisions int64) {
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM v2_user WHERE LOWER\(TRIM\(email\)\) = LOWER\(TRIM\(\$1\)\) AND id <> \$2`).WithArgs("user@example.com", int64(5)).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(collisions))
}
func expectSupportEntryAudit(mock sqlmock.Sqlmock, req SupportEntryChangeRequest, fail bool) {
	exp := mock.ExpectExec(`INSERT INTO v2_support_operation`).WithArgs(int64(7), int64(5), int64(0), req.RequestID, req.Reason, sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg())
	if fail {
		exp.WillReturnError(errors.New("audit unavailable"))
	} else {
		exp.WillReturnResult(sqlmock.NewResult(1, 1))
	}
}
func TestSupportChangeMembershipAndAuditAreAtomicAndPreserveOtherEmails(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			before := supportMembershipFixture{old: true, legacy: true}
			after := supportMembershipFixture{target: true}
			s, mock, done := newSupportEntryTestService(t)
			defer done()
			req := supportTestChangeRequest(t, before)
			expectSupportStaffRead(mock)
			expectSupportMutationLocks(mock, req.RequestID, false)
			expectSupportIdempotencyMissing(mock, req)
			expectSupportPolicyLock(mock, before)
			expectSupportPreview(mock, before)
			expectSupportUniqueEntryEmail(mock, 0)
			add, _ := cliententry.DecodeConditions(supportMembershipConditions(true, true))
			encoded, _ := cliententry.EncodeConditions(add)
			mock.ExpectExec(`UPDATE v2_client_entry_user_policy SET conditions=\$2,updated_at=\$3`).WithArgs(int64(20), encoded, sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
			empty, _ := cliententry.DecodeConditions(supportMembershipConditions(false, false))
			encoded, _ = cliententry.EncodeConditions(empty)
			mock.ExpectExec(`UPDATE v2_client_entry_user_policy SET conditions=\$2,updated_at=\$3`).WithArgs(int64(30), encoded, sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec(`UPDATE v2_support_entry_override SET active=0`).WithArgs(int64(5), int64(7), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
			expectSupportPreview(mock, after)
			expectSupportEntryAudit(mock, req, fail)
			if fail {
				mock.ExpectRollback()
			} else {
				expectSupportEntitlementForEntry(mock)
				mock.ExpectCommit()
			}
			result, err := s.ChangeSupportEntry(context.Background(), 7, req)
			if fail {
				if err == nil || !strings.Contains(err.Error(), "audit unavailable") {
					t.Fatalf("audit failure: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !result.Found || result.Entitlement == nil || len(result.EntryMembership.CurrentGroups) != 1 || !result.EntryMembership.CurrentGroups[0].Effective || result.EntryMembership.HasLegacyOverride || result.EntryMembership.Version != supportMembershipVersion(t, after) {
				t.Fatalf("unexpected result %#v", result)
			}
		})
	}
}
func TestSupportChangeRestoreRemovesDisabledCurrentGroupAndLegacyOverride(t *testing.T) {
	before := supportMembershipFixture{old: true, legacy: true}
	after := supportMembershipFixture{}
	s, mock, done := newSupportEntryTestService(t)
	defer done()
	req := supportTestChangeRequest(t, before)
	req.Restore = true
	req.TargetKey = ""
	expectSupportStaffRead(mock)
	expectSupportMutationLocks(mock, req.RequestID, false)
	expectSupportIdempotencyMissing(mock, req)
	expectSupportPolicyLock(mock, before)
	expectSupportPreview(mock, before)
	expectSupportUniqueEntryEmail(mock, 0)
	empty, _ := cliententry.DecodeConditions(supportMembershipConditions(false, false))
	encoded, _ := cliententry.EncodeConditions(empty)
	mock.ExpectExec(`UPDATE v2_client_entry_user_policy SET conditions=\$2,updated_at=\$3`).WithArgs(int64(30), encoded, sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE v2_support_entry_override SET active=0`).WithArgs(int64(5), int64(7), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	expectSupportPreview(mock, after)
	expectSupportEntryAudit(mock, req, false)
	expectSupportEntitlementForEntry(mock)
	mock.ExpectCommit()
	result, err := s.ChangeSupportEntry(context.Background(), 7, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.EntryMembership.CurrentGroups) != 0 || result.EntryMembership.HasLegacyOverride || result.Scopes[0].AutomaticEntryHost != "auto.example" {
		t.Fatalf("restore: %#v", result)
	}
}
func TestSupportChangeNoopDoesNotEmitLog(t *testing.T) {
	f := supportMembershipFixture{target: true}
	s, mock, done := newSupportEntryTestService(t)
	defer done()
	req := supportTestChangeRequest(t, f)
	expectSupportStaffRead(mock)
	expectSupportMutationLocks(mock, req.RequestID, false)
	expectSupportIdempotencyMissing(mock, req)
	expectSupportPolicyLock(mock, f)
	expectSupportPreview(mock, f)
	expectSupportUniqueEntryEmail(mock, 0)
	mock.ExpectExec(`UPDATE v2_support_entry_override SET active=0`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()
	_, err := s.ChangeSupportEntry(context.Background(), 7, req)
	if !errors.Is(err, ErrSupportEntryInvalid) {
		t.Fatalf("noop: %v", err)
	}
}
func TestSupportChangeIdenticalRetryIsReadOnlyAndRequestReuseRejected(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(fmt.Sprint(conflict), func(t *testing.T) {
			f := supportMembershipFixture{target: true}
			s, mock, done := newSupportEntryTestService(t)
			defer done()
			req := supportTestChangeRequest(t, f)
			raw, _ := json.Marshal(SupportOperationState{Kind: "entry_membership", RequestHash: supportChangeHash(req)})
			if conflict {
				req.Reason = "different"
			}
			expectSupportStaffRead(mock)
			expectSupportMutationLocks(mock, req.RequestID, false)
			mock.ExpectQuery(regexp.QuoteMeta(supportRequestStateQuery)).WithArgs(int64(7), req.RequestID).WillReturnRows(sqlmock.NewRows([]string{"state"}).AddRow(string(raw)))
			if conflict {
				mock.ExpectRollback()
			} else {
				expectSupportPreview(mock, f)
				expectSupportEntitlementForEntry(mock)
				mock.ExpectCommit()
			}
			result, err := s.ChangeSupportEntry(context.Background(), 7, req)
			if conflict {
				if !errors.Is(err, ErrSupportEntryConflict) {
					t.Fatalf("id reuse: %v", err)
				}
			} else if err != nil || !result.Found || result.Entitlement == nil {
				t.Fatalf("retry: %#v,%v", result, err)
			}
		})
	}
}
func TestSupportLookupDoesNotReturnPrivilegedOrAmbiguousAccounts(t *testing.T) {
	s, mock, done := newSupportEntryTestService(t)
	defer done()
	expectSupportStaffRead(mock)
	mock.ExpectQuery(`(?s)SELECT u.id FROM v2_user u WHERE LOWER\(u.email\) = \$1.*u.is_admin = 0 AND u.is_staff = 0.*LIMIT 2`).WithArgs("staff@example.com").WillReturnRows(sqlmock.NewRows([]string{"id"}))
	result, err := s.LookupSupportUser(context.Background(), 7, "staff@example.com", "")
	if err != nil || result.Found || result.User != nil {
		t.Fatalf("privileged lookup: %#v,%v", result, err)
	}
}
func TestSupportOperationListFiltersActorPreservesEntitlementsAndStripsHash(t *testing.T) {
	s, mock, done := newSupportEntryTestService(t)
	defer done()
	expectSupportStaffRead(mock)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT COUNT(*),COALESCE(MAX(id),0) FROM v2_support_operation WHERE actor_id = $1`)).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"count", "latest_id"}).AddRow(1, 1))
	mock.ExpectQuery(`(?s)SELECT id,actor_id,target_user_id.*WHERE actor_id = \$1 ORDER BY id DESC LIMIT 20 OFFSET \$2`).WithArgs(int64(7), int64(0)).WillReturnRows(sqlmock.NewRows([]string{"id", "actor", "target", "source", "reason", "before", "after", "created"}).AddRow(1, 7, 5, 0, "repair", `{}`, `{"kind":"entitlement","plan_id":12,"transfer_enable":1099511627776,"expired_at":123,"request_hash":"internal"}`, 100))
	result, err := s.ListSupportOperations(context.Background(), 7, 1)
	if err != nil {
		t.Fatal(err)
	}
	raw := string(result.Items[0].AfterState)
	if result.Total != 1 || result.LatestID != 1 || strings.Contains(raw, "request_hash") || !strings.Contains(raw, `"transfer_enable":1099511627776`) {
		t.Fatalf("list lost state/privacy: %s", raw)
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
	mock.ExpectQuery(`(?s)SELECT id,is_admin,is_staff FROM v2_user WHERE id = \$1 AND is_admin = 0 AND is_staff = 0 FOR UPDATE`).WithArgs(int64(8)).WillReturnError(sql.ErrNoRows)
	if err = lockSupportTargetUser(context.Background(), tx, 8); !errors.Is(err, ErrSupportEntryNotFound) {
		t.Fatal(err)
	}
	mock.ExpectRollback()
	tx.Rollback()
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSupportChangeRejectsNormalizedEmailCollisionsBeforeAnyWrite(t *testing.T) {
	for _, restore := range []bool{false, true} {
		t.Run(fmt.Sprintf("restore_%v", restore), func(t *testing.T) {
			before := supportMembershipFixture{old: true, legacy: true}
			s, mock, done := newSupportEntryTestService(t)
			defer done()
			req := supportTestChangeRequest(t, before)
			if restore {
				req.Restore = true
				req.TargetKey = ""
			}
			expectSupportStaffRead(mock)
			expectSupportMutationLocks(mock, req.RequestID, false)
			expectSupportIdempotencyMissing(mock, req)
			expectSupportPolicyLock(mock, before)
			expectSupportPreview(mock, before)
			// The query intentionally includes administrators and employees: it must not
			// inherit the ordinary-user filtering used to authorize the target account.
			expectSupportUniqueEntryEmail(mock, 1)
			mock.ExpectRollback()
			_, err := s.ChangeSupportEntry(context.Background(), 7, req)
			if !errors.Is(err, ErrSupportEntryInvalid) || !strings.Contains(err.Error(), "管理员先处理邮箱冲突") {
				t.Fatalf("collision accepted or unclear error: %v", err)
			}
		})
	}
}
