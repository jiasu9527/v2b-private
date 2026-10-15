package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"forest/go-api/internal/config"
	platformpostgres "forest/go-api/internal/platform/postgres"
	"forest/go-api/internal/session"
	usersvc "forest/go-api/internal/user"
	"github.com/golang-jwt/jwt/v5"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// This opt-in test must use a disposable database initialized with
// database/install.pgsql.sql. Its operation log must be empty before this test
// runs: clearing is global, so refuse to touch any pre-existing operation rows.
// Fixture cleanup only removes records created by this test.
func TestSupportPostgresEndToEnd(t *testing.T) {
	dsn := os.Getenv("FOREST_SUPPORT_TEST_DSN")
	if dsn == "" {
		t.Skip("set FOREST_SUPPORT_TEST_DSN to a disposable PostgreSQL database")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err = platformpostgres.EnsureSupportSchema(ctx, db); err != nil {
			t.Fatal(err)
		}
	}
	if err = platformpostgres.EnsureClientEntrySchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	var existingOperations int64
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_support_operation`).Scan(&existingOperations); err != nil {
		t.Fatal(err)
	}
	if existingOperations != 0 {
		t.Fatal("refusing global log-clear test: disposable database must have no pre-existing support operations")
	}
	cfg := config.Config{AppKey: "support-integration-fixture-key"}
	s := NewDBService(cfg, db)
	userService := usersvc.NewDBService(cfg, db).WithSupportEntries()
	suffix := fmt.Sprint(time.Now().UnixNano())
	now := time.Now().Unix()
	userIDs := []int64{}
	policyIDs := []int64{}
	var nodeID, extraNodeID, groupID, planID int64
	t.Cleanup(func() {
		for _, id := range userIDs {
			db.ExecContext(ctx, `DELETE FROM v2_support_operation WHERE actor_id=$1 OR target_user_id=$1`, id)
			db.ExecContext(ctx, `DELETE FROM v2_support_request_receipt WHERE actor_id=$1`, id)
			db.ExecContext(ctx, `DELETE FROM v2_support_request_limit WHERE actor_id=$1`, id)
			db.ExecContext(ctx, `DELETE FROM v2_support_entry_override WHERE user_id=$1`, id)
			db.ExecContext(ctx, `DELETE FROM v2_auth_session WHERE user_id=$1`, id)
			db.ExecContext(ctx, `DELETE FROM v2_user_subscribe_activity WHERE user_id=$1`, id)
			db.ExecContext(ctx, `DELETE FROM v2_user WHERE id=$1`, id)
		}
		for _, id := range policyIDs {
			db.ExecContext(ctx, `DELETE FROM v2_client_entry_user_policy_member WHERE policy_id=$1`, id)
			db.ExecContext(ctx, `DELETE FROM v2_client_entry_user_policy WHERE id=$1`, id)
		}
		db.ExecContext(ctx, `DELETE FROM v2_server_vmess WHERE id=$1`, nodeID)
		db.ExecContext(ctx, `DELETE FROM v2_server_vmess WHERE id=$1`, extraNodeID)
		db.ExecContext(ctx, `DELETE FROM v2_plan WHERE id=$1`, planID)
		db.ExecContext(ctx, `DELETE FROM v2_server_group WHERE id=$1`, groupID)
	})
	createUser := func(label string, isAdmin, isStaff int64) (int64, string) {
		t.Helper()
		var id int64
		email := label + suffix + "@example.test"
		err := db.QueryRowContext(ctx, `INSERT INTO v2_user(email,password,uuid,token,is_admin,is_staff,transfer_enable,group_id,created_at,updated_at) VALUES($1,'fixture-password',$2,$3,$4,$5,1000000,$6,$7,$7) RETURNING id`, email, label+suffix, fmt.Sprintf("%032s", label+suffix), isAdmin, isStaff, groupID, now).Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		userIDs = append(userIDs, id)
		return id, email
	}
	if err = db.QueryRowContext(ctx, `INSERT INTO v2_server_group(name,created_at,updated_at) VALUES($1,$2,$2) RETURNING id`, "support-it-"+suffix, now).Scan(&groupID); err != nil {
		t.Fatal(err)
	}
	adminID, adminEmail := createUser("admin", 1, 0)
	staffID, staffEmail := createUser("staff", 0, 1)
	ordinaryID, ordinaryEmail := createUser("ordinary", 0, 0)
	targetID, targetEmail := createUser("target", 0, 0)
	if err = db.QueryRowContext(ctx, `INSERT INTO v2_server_vmess(group_id,name,host,port,server_port,rate,network,"show",created_at,updated_at) VALUES($1,$2,'original.example.test','443',443,'1','tcp',1,$3,$3) RETURNING id`, fmt.Sprintf("[%d]", groupID), "support-it-node-"+suffix, now).Scan(&nodeID); err != nil {
		t.Fatal(err)
	}
	createPolicy := func(label, host, conditions string) int64 {
		t.Helper()
		var id int64
		err := db.QueryRowContext(ctx, `INSERT INTO v2_client_entry_user_policy(name,sort,conditions,entry_host,created_at,updated_at) VALUES($1,-2000000000,$2,$3,$4,$4) RETURNING id`, label+suffix, conditions, host, now).Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		policyIDs = append(policyIDs, id)
		if _, err = db.ExecContext(ctx, `INSERT INTO v2_client_entry_user_policy_member(policy_id,server_type,server_id,sort,created_at,updated_at) VALUES($1,'vmess',$2,0,$3,$3)`, id, nodeID, now); err != nil {
			t.Fatal(err)
		}
		return id
	}
	targetPolicy := createPolicy("destination", "repair.example.test", fmt.Sprintf(`[{"field":"email","operator":"in","values":[%q]}]`, ordinaryEmail))
	secondPolicy := createPolicy("second-vip", "second.example.test", `[{"field":"email","operator":"in","values":[]}]`)
	sourceID := createPolicy("default", "automatic.example.test", `[]`)
	targetKey := fmt.Sprintf("policy:%d", targetPolicy)
	if err = db.QueryRowContext(ctx, `INSERT INTO v2_server_vmess(group_id,name,host,port,server_port,rate,network,"show",created_at,updated_at) VALUES($1,$2,'extra-original.example.test','443',443,'1','tcp',1,$3,$3) RETURNING id`, fmt.Sprintf("[%d]", groupID), "support-extra-node-"+suffix, now).Scan(&extraNodeID); err != nil {
		t.Fatal(err)
	}
	// VIP and default intentionally have different complete member sets. Joining
	// an existing email list is not the removed same-member address override.
	if _, err = db.ExecContext(ctx, `INSERT INTO v2_client_entry_user_policy_member(policy_id,server_type,server_id,sort,created_at,updated_at) VALUES($1,'vmess',$2,1,$3,$3)`, targetPolicy, extraNodeID, now); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRowContext(ctx, `INSERT INTO v2_plan(group_id,name,transfer_enable,device_limit,speed_limit,created_at,updated_at) VALUES($1,$2,20,3,50,$3,$3) RETURNING id`, groupID, "support-plan-"+suffix, now).Scan(&planID); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.IsSupportStaff(ctx, staffID); err != nil || !ok {
		t.Fatalf("existing staff works without extra provisioning: %v %v", ok, err)
	}
	for _, excluded := range []string{staffEmail, adminEmail, fmt.Sprint(adminID)} {
		view, err := s.LookupSupportUser(ctx, staffID, excluded, "")
		if err != nil || view.Found {
			t.Fatalf("staff must not query privileged account %s: %+v %v", excluded, view, err)
		}
	}
	view, err := s.LookupSupportUser(ctx, staffID, targetEmail, "")
	if err != nil {
		t.Fatal(err)
	}
	if !view.Found || view.Entitlement == nil || len(view.Scopes) != 1 || view.Scopes[0].SourcePolicyID != sourceID || len(view.EntryMembership.Targets) != 2 {
		t.Fatalf("preview %+v", view)
	}
	req := SupportEntryChangeRequest{UserID: targetID, TargetKey: targetKey, Version: view.EntryMembership.Version, Reason: "connection repair", RequestID: "fixture-" + suffix}
	changed, err := s.ChangeSupportEntry(ctx, staffID, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(changed.EntryMembership.CurrentGroups) != 1 || changed.EntryMembership.CurrentGroups[0].PolicyID != targetPolicy || !changed.EntryMembership.CurrentGroups[0].Effective {
		t.Fatalf("membership not effective %+v", changed.EntryMembership)
	}
	assertHost := func(want string) {
		t.Helper()
		nodes, err := userService.Servers(ctx, targetID, "")
		if err != nil {
			t.Fatal(err)
		}
		for _, node := range nodes {
			if fmt.Sprint(node["id"]) == fmt.Sprint(nodeID) && node["type"] == "vmess" {
				if node["host"] != want {
					t.Fatalf("host=%v want=%s", node["host"], want)
				}
				return
			}
		}
		t.Fatal("expected node missing")
	}
	assertHost("repair.example.test")
	if _, err = s.ChangeSupportEntry(ctx, staffID, req); err != nil {
		t.Fatalf("same idempotent retry: %v", err)
	}
	altered := req
	altered.Reason = "other payload"
	if _, err = s.ChangeSupportEntry(ctx, staffID, altered); !errors.Is(err, ErrSupportEntryConflict) {
		t.Fatalf("payload conflict %v", err)
	}
	logs, err := s.ListSupportOperations(ctx, staffID, 1)
	if err != nil || logs.Total != 1 {
		t.Fatalf("logs %+v err %v", logs, err)
	}
	if strings.Contains(string(logs.Items[0].AfterState), "request_hash") {
		t.Fatal("internal fingerprint exposed")
	}
	if _, err = db.ExecContext(ctx, `UPDATE v2_client_entry_user_policy SET entry_host='updated.example.test' WHERE id=$1`, targetPolicy); err != nil {
		t.Fatal(err)
	}
	assertHost("updated.example.test")
	if _, err = db.ExecContext(ctx, `UPDATE v2_client_entry_user_policy SET enabled=0 WHERE id=$1`, targetPolicy); err != nil {
		t.Fatal(err)
	}
	assertHost("automatic.example.test")
	if _, err = db.ExecContext(ctx, `UPDATE v2_client_entry_user_policy SET enabled=1 WHERE id=$1`, targetPolicy); err != nil {
		t.Fatal(err)
	}
	view, err = s.LookupSupportUser(ctx, staffID, targetEmail, "")
	if err != nil {
		t.Fatal(err)
	}
	switchGroup := SupportEntryChangeRequest{UserID: targetID, TargetKey: fmt.Sprintf("policy:%d", secondPolicy), Version: view.EntryMembership.Version, Reason: "switch VIP", RequestID: "switch-" + suffix}
	changed, err = s.ChangeSupportEntry(ctx, staffID, switchGroup)
	if err != nil || len(changed.EntryMembership.CurrentGroups) != 1 || changed.EntryMembership.CurrentGroups[0].PolicyID != secondPolicy {
		t.Fatalf("switch membership %+v %v", changed.EntryMembership, err)
	}
	assertHost("second.example.test")
	var oldList string
	if err = db.QueryRowContext(ctx, `SELECT conditions FROM v2_client_entry_user_policy WHERE id=$1`, targetPolicy).Scan(&oldList); err != nil || !strings.Contains(oldList, strings.ToLower(ordinaryEmail)) || strings.Contains(oldList, strings.ToLower(targetEmail)) {
		t.Fatalf("switch lost another user or left old membership: %s %v", oldList, err)
	}
	restore := SupportEntryChangeRequest{UserID: targetID, Restore: true, Reason: "repair complete", Version: changed.EntryMembership.Version, RequestID: "restore-" + suffix}
	if _, err = s.ChangeSupportEntry(ctx, ordinaryID, restore); !errors.Is(err, ErrSupportForbidden) {
		t.Fatalf("ordinary user restore %v", err)
	}
	if _, err = s.ChangeSupportEntry(ctx, staffID, restore); err != nil {
		t.Fatal(err)
	}
	assertHost("automatic.example.test")
	if err = db.QueryRowContext(ctx, `SELECT conditions FROM v2_client_entry_user_policy WHERE id=$1`, secondPolicy).Scan(&oldList); err != nil {
		t.Fatal(err)
	}
	var emptyRules []struct {
		Field  string            `json:"field"`
		Values []json.RawMessage `json:"values"`
	}
	if err = json.Unmarshal([]byte(oldList), &emptyRules); err != nil || len(emptyRules) != 1 || emptyRules[0].Field != "email" || len(emptyRules[0].Values) != 0 {
		t.Fatalf("last exit must retain empty email guard, not match everyone: %s %v", oldList, err)
	}
	// A manual repair changes entitlements, never manufactures payment/order
	// records. Selecting a plan adopts its defaults without resetting usage.
	view, err = s.LookupSupportUser(ctx, staffID, targetEmail, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `UPDATE v2_user SET u=100,d=200 WHERE id=$1`, targetID); err != nil {
		t.Fatal(err)
	}
	newExpiry := now + 86400*30
	benefitsReq := SupportEntitlementChangeRequest{UserID: targetID, PlanID: &planID, ExpiredAt: json.RawMessage(fmt.Sprint(newExpiry)), Expected: view.Entitlement.Version, Reason: "repair missing entitlement", RequestID: "benefits-" + suffix}
	benefits, err := s.ChangeSupportEntitlement(ctx, staffID, benefitsReq)
	if err != nil || benefits.PlanID == nil || *benefits.PlanID != planID || benefits.TransferEnable != 20*1024*1024*1024 || benefits.ExpiredAt == nil || *benefits.ExpiredAt != newExpiry || benefits.U != 100 || benefits.D != 200 || benefits.DeviceLimit == nil || *benefits.DeviceLimit != 3 {
		t.Fatalf("plan repair %+v %v", benefits, err)
	}
	staleReq := benefitsReq
	staleReq.RequestID = "stale-" + suffix
	if _, err = s.ChangeSupportEntitlement(ctx, staffID, staleReq); !errors.Is(err, ErrSupportEntryConflict) {
		t.Fatalf("stale entitlement accepted: %v", err)
	}
	resetReq := SupportEntitlementChangeRequest{UserID: targetID, Expected: benefits.Version, ResetUsage: true, Reason: "reset used traffic", RequestID: "reset-" + suffix}
	benefits, err = s.ChangeSupportEntitlement(ctx, staffID, resetReq)
	if err != nil || benefits.U != 0 || benefits.D != 0 {
		t.Fatalf("reset %+v %v", benefits, err)
	}
	if _, err = db.ExecContext(ctx, `UPDATE v2_user SET d=d+7 WHERE id=$1`, targetID); err != nil {
		t.Fatal(err)
	}
	benefits, err = s.ChangeSupportEntitlement(ctx, staffID, resetReq)
	if err != nil || benefits.D != 7 {
		t.Fatalf("retry reset new traffic again: %+v %v", benefits, err)
	}
	crossKind := SupportEntitlementChangeRequest{UserID: targetID, Expected: benefits.Version, ResetUsage: true, Reason: "cross kind", RequestID: req.RequestID}
	if _, err = s.ChangeSupportEntitlement(ctx, staffID, crossKind); !errors.Is(err, ErrSupportEntryConflict) {
		t.Fatalf("request ID shared across operation kinds accepted: %v", err)
	}
	logs, err = s.ListSupportOperations(ctx, staffID, 1)
	if err != nil || logs.Total != 5 {
		t.Fatalf("expected only five actual changes: %+v %v", logs, err)
	}
	for _, operation := range logs.Items {
		if strings.Contains(string(operation.AfterState), "request_hash") {
			t.Fatal("internal fingerprint exposed")
		}
	}
	// Administrators clear only the fence they confirmed. Add a later operation
	// for another ordinary customer so clearing cannot silently remove new work.
	clearSnapshot, err := s.ListSupportOperations(ctx, 0, 1)
	if err != nil || clearSnapshot.Total != 5 || clearSnapshot.LatestID <= 0 {
		t.Fatalf("clear snapshot %+v %v", clearSnapshot, err)
	}
	ordinaryView, err := s.LookupSupportUser(ctx, staffID, ordinaryEmail, "")
	if err != nil || ordinaryView.Entitlement == nil {
		t.Fatalf("ordinary entitlement preview %+v %v", ordinaryView, err)
	}
	laterQuota := ordinaryView.Entitlement.TransferEnable + 1
	laterReq := SupportEntitlementChangeRequest{UserID: ordinaryID, TransferEnable: &laterQuota, Expected: ordinaryView.Entitlement.Version, Reason: "new operation after confirmation snapshot", RequestID: "later-" + suffix}
	if _, err = s.ChangeSupportEntitlement(ctx, staffID, laterReq); err != nil {
		t.Fatal(err)
	}
	captureBusinessState := func() string {
		t.Helper()
		snapshot, err := LoadSupportEntitlementSnapshot(ctx, db, targetID)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		state := string(raw)
		for _, id := range []int64{targetPolicy, secondPolicy} {
			var conditions string
			if err = db.QueryRowContext(ctx, `SELECT conditions FROM v2_client_entry_user_policy WHERE id=$1`, id).Scan(&conditions); err != nil {
				t.Fatal(err)
			}
			state += "\n" + conditions
		}
		return state
	}
	beforeClear := captureBusinessState()
	clearReq := SupportOperationClearRequest{ThroughID: clearSnapshot.LatestID, Confirm: true}
	if _, err = s.ClearSupportOperations(ctx, staffID, clearReq); !errors.Is(err, ErrSupportForbidden) {
		t.Fatalf("employee must not clear logs: %v", err)
	}
	logs, err = s.ListSupportOperations(ctx, 0, 1)
	if err != nil || logs.Total != 6 || logs.LatestID <= clearSnapshot.LatestID {
		t.Fatalf("forbidden clear changed history or latest fence missing: %+v %v", logs, err)
	}
	laterID := logs.LatestID
	cleared, err := s.ClearSupportOperations(ctx, adminID, clearReq)
	if err != nil || cleared.Deleted != 5 {
		t.Fatalf("administrator clear old snapshot %+v %v", cleared, err)
	}
	logs, err = s.ListSupportOperations(ctx, 0, 1)
	if err != nil || logs.Total != 1 || len(logs.Items) != 1 || logs.LatestID != laterID || logs.Items[0].TargetUserID != ordinaryID {
		t.Fatalf("clear removed later operation or retained old details: %+v %v", logs, err)
	}
	var receiptCount int64
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_support_request_receipt WHERE actor_id=$1`, staffID).Scan(&receiptCount); err != nil || receiptCount != 5 {
		t.Fatalf("compact receipts count=%d err=%v", receiptCount, err)
	}
	cleared, err = s.ClearSupportOperations(ctx, adminID, clearReq)
	if err != nil || cleared.Deleted != 0 {
		t.Fatalf("repeated confirmation fence must be harmless: %+v %v", cleared, err)
	}
	if afterClear := captureBusinessState(); afterClear != beforeClear {
		t.Fatalf("clearing logs changed entitlements or email lists: before=%s after=%s", beforeClear, afterClear)
	}
	// Cleared reset receipts must still prevent a replay from deleting usage
	// accrued since the original reset. Membership retries must not re-enroll a
	// user who has subsequently left the original group.
	benefits, err = s.ChangeSupportEntitlement(ctx, staffID, resetReq)
	if err != nil || benefits.D != 7 {
		t.Fatalf("cleared reset replay reset new traffic: %+v %v", benefits, err)
	}
	changed, err = s.ChangeSupportEntry(ctx, staffID, req)
	if err != nil || len(changed.EntryMembership.CurrentGroups) != 0 {
		t.Fatalf("cleared membership replay rejoined old group: %+v %v", changed, err)
	}
	assertHost("automatic.example.test")
	if afterRetries := captureBusinessState(); afterRetries != beforeClear {
		t.Fatalf("cleared operation retries changed business state: before=%s after=%s", beforeClear, afterRetries)
	}
	alteredReset := resetReq
	alteredReset.Reason = "different payload after clear"
	if _, err = s.ChangeSupportEntitlement(ctx, staffID, alteredReset); !errors.Is(err, ErrSupportEntryConflict) {
		t.Fatalf("cleared reset ID accepted changed payload: %v", err)
	}
	if _, err = s.ChangeSupportEntry(ctx, staffID, altered); !errors.Is(err, ErrSupportEntryConflict) {
		t.Fatalf("cleared membership ID accepted changed payload: %v", err)
	}
	if _, err = s.ChangeSupportEntitlement(ctx, staffID, crossKind); !errors.Is(err, ErrSupportEntryConflict) {
		t.Fatalf("cleared entry ID accepted entitlement payload: %v", err)
	}
	logs, err = s.ListSupportOperations(ctx, staffID, 1)
	if err != nil || logs.Total != 1 || logs.LatestID != laterID {
		t.Fatalf("cleared idempotent/conflicting retries added history: %+v %v", logs, err)
	}
	// PostgreSQL's legacy email UNIQUE constraint is case-sensitive, while entry
	// lists are not. Even an ID lookup must not mutate a privileged lookalike.
	duplicateID, _ := createUser("duplicate", 1, 0)
	if _, err = db.ExecContext(ctx, `UPDATE v2_user SET email=$2 WHERE id=$1`, duplicateID, strings.ToUpper(targetEmail)); err != nil {
		t.Fatal(err)
	}
	view, err = s.LookupSupportUser(ctx, staffID, fmt.Sprint(targetID), "")
	if err != nil {
		t.Fatal(err)
	}
	duplicateReq := SupportEntryChangeRequest{UserID: targetID, TargetKey: targetKey, Version: view.EntryMembership.Version, Reason: "must not affect lookalike", RequestID: "duplicate-" + suffix}
	if _, err = s.ChangeSupportEntry(ctx, staffID, duplicateReq); !errors.Is(err, ErrSupportEntryInvalid) {
		t.Fatalf("case-duplicate membership should be rejected: %v", err)
	}
	assertHost("automatic.example.test")
	if view, err = s.LookupSupportUser(ctx, staffID, ordinaryEmail, ""); err != nil || !view.Found {
		t.Fatalf("ordinary customer lookup: %+v %v", view, err)
	}
	sessionID := "support-it-" + suffix
	if _, err = db.ExecContext(ctx, `INSERT INTO v2_auth_session(user_id,session_id,login_at,created_at,updated_at) VALUES($1,$2,$3,$3,$3)`, staffID, sessionID, now); err != nil {
		t.Fatal(err)
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"id": staffID, "session": sessionID}).SignedString([]byte(cfg.AppKey))
	if err != nil {
		t.Fatal(err)
	}
	auth := session.NewDBService(cfg, db).WithAuthCache(session.NewAuthCache(time.Minute))
	if _, err = auth.Authenticate(ctx, token, false); err != nil {
		t.Fatal(err)
	}
	if _, err = s.UpdateUser(ctx, UserUpdateRequest{ID: staffID, Values: map[string]string{"email": staffEmail, "banned": "0", "is_admin": "0", "is_staff": "0"}}); err != nil {
		t.Fatal(err)
	}
	if _, err = auth.AuthenticateFresh(ctx, token, false); !errors.Is(err, session.ErrUnauthorized) {
		t.Fatalf("revoked session accepted: %v", err)
	}
	if _, err = s.LookupSupportUser(ctx, staffID, targetEmail, ""); !errors.Is(err, ErrSupportForbidden) {
		t.Fatalf("revoked staff accepted: %v", err)
	}
	if _, err = s.UpdateUser(ctx, UserUpdateRequest{ID: ordinaryID, Values: map[string]string{"email": ordinaryEmail, "banned": "0", "is_admin": "0", "is_staff": "1"}}); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.IsSupportStaff(ctx, ordinaryID); err != nil || !ok {
		t.Fatalf("existing employee switch grants workbench directly: %v %v", ok, err)
	}
	t.Log("verified live SQL: exact-user staff lookup, email membership join/switch/exit->real subscription, safe empty lists, other-user preservation, reference follow/fallback, entitlement repair/reset, idempotency, mixed audit, admin-only snapshot clear, receipt retries without reset/rejoin, role switch/session revocation")
}
