package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
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
// database/install.pgsql.sql. It only removes fixtures it creates.
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
	cfg := config.Config{AppKey: "support-integration-fixture-key"}
	s := NewDBService(cfg, db)
	userService := usersvc.NewDBService(cfg, db).WithSupportEntries()
	suffix := fmt.Sprint(time.Now().UnixNano())
	now := time.Now().Unix()
	userIDs := []int64{}
	policyIDs := []int64{}
	var nodeID, groupID int64
	t.Cleanup(func() {
		for _, id := range userIDs {
			db.ExecContext(ctx, `DELETE FROM v2_support_operation WHERE actor_id=$1 OR target_user_id=$1`, id)
			db.ExecContext(ctx, `DELETE FROM v2_support_request_limit WHERE actor_id=$1`, id)
			db.ExecContext(ctx, `DELETE FROM v2_support_entry_override WHERE user_id=$1`, id)
			db.ExecContext(ctx, `DELETE FROM v2_support_staff WHERE user_id=$1`, id)
			db.ExecContext(ctx, `DELETE FROM v2_auth_session WHERE user_id=$1`, id)
			db.ExecContext(ctx, `DELETE FROM v2_user_subscribe_activity WHERE user_id=$1`, id)
			db.ExecContext(ctx, `DELETE FROM v2_user WHERE id=$1`, id)
		}
		for _, id := range policyIDs {
			db.ExecContext(ctx, `DELETE FROM v2_client_entry_user_policy_member WHERE policy_id=$1`, id)
			db.ExecContext(ctx, `DELETE FROM v2_client_entry_user_policy WHERE id=$1`, id)
		}
		db.ExecContext(ctx, `DELETE FROM v2_server_vmess WHERE id=$1`, nodeID)
		db.ExecContext(ctx, `DELETE FROM v2_server_group WHERE id=$1`, groupID)
	})
	createUser := func(label string, isAdmin int64) (int64, string) {
		t.Helper()
		var id int64
		email := label + suffix + "@example.test"
		err := db.QueryRowContext(ctx, `INSERT INTO v2_user(email,password,uuid,token,is_admin,transfer_enable,group_id,created_at,updated_at) VALUES($1,'fixture-password',$2,$3,$4,1000000,$5,$6,$6) RETURNING id`, email, label+suffix, fmt.Sprintf("%032s", label+suffix), isAdmin, groupID, now).Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		userIDs = append(userIDs, id)
		return id, email
	}
	if err = db.QueryRowContext(ctx, `INSERT INTO v2_server_group(name,created_at,updated_at) VALUES($1,$2,$2) RETURNING id`, "support-it-"+suffix, now).Scan(&groupID); err != nil {
		t.Fatal(err)
	}
	adminID, _ := createUser("admin", 1)
	staffID, staffEmail := createUser("staff", 0)
	readonlyID, readonlyEmail := createUser("readonly", 0)
	targetID, targetEmail := createUser("target", 0)
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
	sourceID := createPolicy("source", "automatic.example.test", fmt.Sprintf(`[{"field":"user_id","operator":"in","values":[%d]}]`, targetID))
	targetPolicy := createPolicy("destination", "repair.example.test", `[{"field":"user_id","operator":"in","values":[2147483647]}]`)
	targetKey := fmt.Sprintf("policy:%d", targetPolicy)
	staff, err := s.SaveSupportStaff(ctx, adminID, SupportStaffSaveRequest{Email: staffEmail, Enabled: true, TargetKeys: []string{targetKey}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveSupportStaff(ctx, adminID, SupportStaffSaveRequest{Email: readonlyEmail, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.IsSupportStaff(ctx, staffID); err != nil || !ok {
		t.Fatalf("staff grant: %v %v", ok, err)
	}
	legacyCtx := WithLegacyStaffScope(ctx)
	if _, err = s.GetUserInfoByID(legacyCtx, staffID); err == nil {
		t.Fatal("legacy staff could read support account")
	}
	if _, err = db.ExecContext(ctx, `UPDATE v2_user SET invite_user_id=$1 WHERE id=$2`, staffID, targetID); err != nil {
		t.Fatal(err)
	}
	customer, err := s.GetUserInfoByID(legacyCtx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"password", "token", "uuid", "invite_user"} {
		if _, ok := customer[key]; ok {
			t.Fatalf("legacy staff detail leaked %s", key)
		}
	}
	if _, err = s.BanUsers(legacyCtx, []UserFilter{{Key: "id", Condition: "=", Value: fmt.Sprint(staffID)}}); err != nil {
		t.Fatal(err)
	}
	var banned int64
	if err = db.QueryRowContext(ctx, `SELECT banned FROM v2_user WHERE id=$1`, staffID).Scan(&banned); err != nil || banned != 0 {
		t.Fatalf("legacy bulk ban reached support account: %d %v", banned, err)
	}
	view, err := s.LookupSupportUser(ctx, staffID, targetEmail, "")
	if err != nil {
		t.Fatal(err)
	}
	if !view.Found || len(view.Scopes) != 1 || view.Scopes[0].SourcePolicyID != sourceID {
		t.Fatalf("preview %+v", view)
	}
	req := SupportEntryChangeRequest{UserID: targetID, SourcePolicyID: sourceID, TargetKey: targetKey, Reason: "connection repair", RequestID: "fixture-" + suffix}
	changed, err := s.ChangeSupportEntry(ctx, staffID, req)
	if err != nil {
		t.Fatal(err)
	}
	if changed.Scopes[0].Version != 1 {
		t.Fatalf("version %+v", changed.Scopes)
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
	if logs.Items[0].AfterState.RequestHash != "" {
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
	restore := SupportEntryChangeRequest{UserID: targetID, SourcePolicyID: sourceID, Restore: true, Reason: "repair complete", Version: 1, RequestID: "restore-" + suffix}
	if _, err = s.ChangeSupportEntry(ctx, readonlyID, restore); !errors.Is(err, ErrSupportForbidden) {
		t.Fatalf("read-only restore %v", err)
	}
	if _, err = s.ChangeSupportEntry(ctx, staffID, restore); err != nil {
		t.Fatal(err)
	}
	assertHost("automatic.example.test")
	if _, err = s.LookupSupportUser(ctx, staffID, readonlyEmail, ""); err != nil {
		t.Fatal(err)
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
	if _, err = s.SaveSupportStaff(ctx, adminID, SupportStaffSaveRequest{Email: staffEmail, Enabled: false, TargetKeys: staff.TargetKeys, Version: staff.Version}); err != nil {
		t.Fatal(err)
	}
	if _, err = auth.AuthenticateFresh(ctx, token, false); !errors.Is(err, session.ErrUnauthorized) {
		t.Fatalf("revoked session accepted: %v", err)
	}
	if _, err = s.LookupSupportUser(ctx, staffID, targetEmail, ""); !errors.Is(err, ErrSupportForbidden) {
		t.Fatalf("revoked staff accepted: %v", err)
	}
	t.Log("verified live SQL: isolated role, exact user, override->subscription, reference follow/fallback, idempotency, audit, read-only restore, revocation")
}
