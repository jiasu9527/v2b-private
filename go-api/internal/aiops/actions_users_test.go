package aiops

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

	"forest/go-api/internal/admin"
	"forest/go-api/internal/config"
)

func TestAIUserChangesValidation(t *testing.T) {
	for _, raw := range []string{`{"is_admin":1}`, `{"is_staff":1}`, `{"password":"test"}`, `{"token":"test"}`, `{"balance":-1}`, `{"balance":2147483648}`, `{"discount":101}`, `{"commission_type":3}`, `{"plan_id":0}`, `{"expired_at":0}`, `{"device_limit":"5"}`, `{"banned":null}`, `{"reset_usage":true}`} {
		var changes map[string]json.RawMessage
		_ = json.Unmarshal([]byte(raw), &changes)
		if _, err := normalizeUserChanges(changes); !errors.Is(err, ErrValidation) {
			t.Fatalf("accepted invalid patch %s: %v", raw, err)
		}
	}
	var changes map[string]json.RawMessage
	_ = json.Unmarshal([]byte(`{"expired_at":null,"discount":null,"remarks":"  修复套餐  ","transfer_enable":1073741824,"banned":0}`), &changes)
	got, err := normalizeUserChanges(changes)
	if err != nil || string(got["expired_at"]) != "null" || string(got["remarks"]) != `"修复套餐"` {
		t.Fatal(got, err)
	}
}
func TestAIUserOperationsPostgres(t *testing.T) {
	dsn := os.Getenv("FOREST_AIOPS_TEST_DSN")
	if dsn == "" {
		t.Skip("requires disposable FOREST_AIOPS_TEST_DSN")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	s := NewService(config.Config{AppKey: "user-fixture"}, db, admin.NewDBService(config.Config{}, db))
	defer s.Close()
	if err = s.ensureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	marker := fmt.Sprint(time.Now().UnixNano())
	var actor, userID, sessionID, groupID, planID int64
	if err = db.QueryRow(`INSERT INTO v2_user(email,password,uuid,token,is_admin,created_at,updated_at) VALUES($1,'fixture-password','fixture-uuid',$2,1,1,1) RETURNING id`, "admin-"+marker+"@example.test", "adm-"+marker).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM v2_user WHERE id=$1`, actor)
	if err = db.QueryRow(`INSERT INTO v2_user(email,password,uuid,token,u,d,balance,expired_at,created_at,updated_at) VALUES($1,'fixture-password','fixture-uuid',$2,11,22,250,2000000000,1,1) RETURNING id`, "user-"+marker+"@example.test", "usr-"+marker).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM v2_auth_session WHERE user_id=$1`, userID)
	defer db.Exec(`DELETE FROM v2_user WHERE id=$1`, userID)
	if _, err = s.SaveConfig(ctx, actor, ConfigSaveRequest{Enabled: true, BaseURL: "https://fixture.example.test/v1", APIKey: "fixture", Models: DefaultModels}); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM v2_ai_config WHERE updated_by=$1`, actor)
	if err = db.QueryRow(`INSERT INTO v2_ai_session(title,model,created_by,created_at,updated_at) VALUES('users','fixture',$1,1,1) RETURNING id`, actor).Scan(&sessionID); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM v2_ai_session WHERE id=$1`, sessionID)
	if err = db.QueryRow(`INSERT INTO v2_server_group(name,created_at,updated_at) VALUES('fixture-group',1,1) RETURNING id`).Scan(&groupID); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM v2_server_group WHERE id=$1`, groupID)
	if err = db.QueryRow(`INSERT INTO v2_plan(group_id,transfer_enable,device_limit,speed_limit,name,created_at,updated_at) VALUES($1,100,5,80,'fixture-plan',1,1) RETURNING id`, groupID).Scan(&planID); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM v2_plan WHERE id=$1`, planID)
	propose := func(key, patch string, reset bool) Action {
		t.Helper()
		args := fmt.Sprintf(`{"user_id":%d,"changes":%s,"reset_usage":%v,"reason":"fixture repair"}`, userID, patch, reset)
		out, e := s.callTool(ctx, actor, sessionID, ToolCall{ID: key, Function: ToolFunction{Name: "propose_user_update", Arguments: args}})
		if e != nil || !out.Pending {
			t.Fatalf("proposal: %s %v", out.Content, e)
		}
		var v struct {
			ID int64 `json:"action_id"`
		}
		if json.Unmarshal([]byte(out.Content), &v) != nil || v.ID == 0 {
			t.Fatal(out.Content)
		}
		actions, e := s.ListActions(ctx, actor, sessionID)
		if e != nil {
			t.Fatal(e)
		}
		for _, a := range actions {
			if a.ID == v.ID {
				return a
			}
		}
		t.Fatal("missing action")
		return Action{}
	}
	t.Run("plan defaults apply only after approval and traffic does not invalidate", func(t *testing.T) {
		a := propose("plan", fmt.Sprintf(`{"plan_id":%d,"expired_at":null,"balance":500,"discount":85,"remarks":"已补单"}`, planID), false)
		var quota, u, d, balance int64
		var gotPlan, gotGroup, expiry, device, speed sql.NullInt64
		if err = db.QueryRow(`SELECT transfer_enable,balance FROM v2_user WHERE id=$1`, userID).Scan(&quota, &balance); err != nil || quota != 0 || balance != 250 {
			t.Fatal("proposal changed user", err)
		}
		if _, err = db.Exec(`UPDATE v2_user SET u=12,d=24,t=123 WHERE id=$1`, userID); err != nil {
			t.Fatal(err)
		}
		done, e := s.ApproveAction(ctx, actor, sessionID, a.ID)
		if e != nil || done.Status != "succeeded" {
			t.Fatal(done, e)
		}
		err = db.QueryRow(`SELECT plan_id,group_id,transfer_enable,device_limit,speed_limit,expired_at,u,d,balance FROM v2_user WHERE id=$1`, userID).Scan(&gotPlan, &gotGroup, &quota, &device, &speed, &expiry, &u, &d, &balance)
		if err != nil || gotPlan.Int64 != planID || gotGroup.Int64 != groupID || quota != 100*(1<<30) || device.Int64 != 5 || speed.Int64 != 80 || expiry.Valid || u != 12 || d != 24 || balance != 500 {
			t.Fatalf("wrong user state: quota=%d u=%d d=%d balance=%d err=%v", quota, u, d, balance, err)
		}
		if _, e = s.ApproveAction(ctx, actor, sessionID, a.ID); !errors.Is(e, ErrConflict) {
			t.Fatal("duplicate approval", e)
		}
	})
	t.Run("concurrent user and plan changes reject", func(t *testing.T) {
		a := propose("stale-user", `{"balance":900}`, false)
		db.Exec(`UPDATE v2_user SET balance=700 WHERE id=$1`, userID)
		if _, e := s.ApproveAction(ctx, actor, sessionID, a.ID); !errors.Is(e, ErrConflict) {
			t.Fatal("stale user", e)
		}
		a = propose("stale-plan", fmt.Sprintf(`{"plan_id":%d}`, planID), false)
		db.Exec(`UPDATE v2_plan SET transfer_enable=200 WHERE id=$1`, planID)
		if _, e := s.ApproveAction(ctx, actor, sessionID, a.ID); !errors.Is(e, ErrConflict) {
			t.Fatal("stale plan", e)
		}
	})
	t.Run("explicit overrides reset and ban clear sessions", func(t *testing.T) {
		if _, e := db.Exec(`INSERT INTO v2_auth_session(user_id,session_id,login_at,created_at,updated_at) VALUES($1,'fixture-session',1,1,1)`, userID); e != nil {
			t.Fatal(e)
		}
		a := propose("ban-reset", fmt.Sprintf(`{"plan_id":%d,"transfer_enable":1234,"device_limit":null,"speed_limit":12,"banned":1,"commission_balance":300,"commission_rate":15,"commission_type":2}`, planID), true)
		if _, e := s.ApproveAction(ctx, actor, sessionID, a.ID); e != nil {
			t.Fatal(e)
		}
		var quota, u, d, banned, count int64
		var device sql.NullInt64
		if e := db.QueryRow(`SELECT transfer_enable,u,d,banned,device_limit FROM v2_user WHERE id=$1`, userID).Scan(&quota, &u, &d, &banned, &device); e != nil || quota != 1234 || u != 0 || d != 0 || banned != 1 || device.Valid {
			t.Fatal("bad override/reset", e)
		}
		if e := db.QueryRow(`SELECT COUNT(*) FROM v2_auth_session WHERE user_id=$1`, userID).Scan(&count); e != nil || count != 0 {
			t.Fatal("ban kept session", e)
		}
	})
	t.Run("read tools omit credentials and retain plan byte compatibility", func(t *testing.T) {
		db.Exec(`UPDATE v2_plan SET transfer_enable=107374182400 WHERE id=$1`, planID)
		if _, e := db.Exec(`UPDATE v2_user SET remarks=$2 WHERE id=$1`, userID, strings.Repeat("备注", 2100)); e != nil {
			t.Fatal(e)
		}
		for _, c := range []struct{ name, args string }{{"plans_list", `{}`}, {"groups_list", `{}`}, {"user_detail", fmt.Sprintf(`{"user_id":%d}`, userID)}} {
			out, e := s.callTool(ctx, actor, sessionID, ToolCall{ID: c.name, Function: ToolFunction{Name: c.name, Arguments: c.args}})
			if e != nil || out.Pending {
				t.Fatal(c.name, e)
			}
			for _, secret := range []string{"fixture-password", "fixture-uuid", "usr-" + marker} {
				if strings.Contains(out.Content, secret) {
					t.Fatal("secret leaked")
				}
			}
			if c.name == "plans_list" && !strings.Contains(out.Content, `"transfer_enable":107374182400`) {
				t.Fatal("already-byte quota multiplied", out.Content)
			}
			if c.name == "user_detail" && !strings.Contains(out.Content, `"remarks_truncated":true`) {
				t.Fatal("partial remarks presented as complete")
			}
		}
	})

	t.Run("ordinary account cannot be elevated by nested or extra fields", func(t *testing.T) {
		for i, changes := range []string{`{"is_admin":1}`, `{"password":"abc123456"}`, `{"u":0}`, `{"banned":true}`, `{"balance":"100"}`} {
			args := fmt.Sprintf(`{"user_id":%d,"changes":%s,"reason":"fixture"}`, userID, changes)
			if _, e := s.callTool(ctx, actor, sessionID, ToolCall{ID: fmt.Sprint("bad-change-", i), Function: ToolFunction{Name: "propose_user_update", Arguments: args}}); !errors.Is(e, ErrValidation) {
				t.Fatal("invalid patch accepted", changes, e)
			}
		}
		var isAdmin, isStaff int64
		if e := db.QueryRow(`SELECT is_admin,is_staff FROM v2_user WHERE id=$1`, userID).Scan(&isAdmin, &isStaff); e != nil || isAdmin != 0 || isStaff != 0 {
			t.Fatal("role changed", e)
		}
	})
	t.Run("administrator targets are rejected", func(t *testing.T) {
		args := fmt.Sprintf(`{"user_id":%d,"changes":{"banned":1},"reason":"fixture"}`, actor)
		if _, e := s.callTool(ctx, actor, sessionID, ToolCall{ID: "admin-target", Function: ToolFunction{Name: "propose_user_update", Arguments: args}}); !errors.Is(e, ErrNotFound) {
			t.Fatal("administrator editable", e)
		}
	})
}
