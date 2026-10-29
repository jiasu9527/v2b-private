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
	platformpostgres "forest/go-api/internal/platform/postgres"
)

func TestAIEntrySplitActionsPostgres(t *testing.T) {
	dsn := os.Getenv("FOREST_AIOPS_TEST_DSN")
	if dsn == "" {
		t.Skip("dedicated disposable PostgreSQL required")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err = platformpostgres.EnsureClientEntrySchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err = platformpostgres.EnsureDNSFailoverSchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	panel := admin.NewDBService(config.Config{}, db)
	s := NewService(config.Config{AppKey: "entry-split-fixture"}, db, panel)
	defer s.Close()
	if err = s.ensureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	var actor, session, probe, policy int64
	userIDs := []int64{}
	marker := fmt.Sprint(time.Now().UnixNano())
	now := time.Now().Unix()
	for i := 0; i < 5; i++ {
		var id int64
		role := 0
		if i == 0 {
			role = 1
		}
		if err = db.QueryRow(`INSERT INTO v2_user(email,password,uuid,token,is_admin,created_at,updated_at)VALUES($1,'fixture','fixture',$2,$3,1,1)RETURNING id`, fmt.Sprintf("split-action-%s-%d@example.test", marker, i), fmt.Sprintf("split-%s-%d", marker, i), role).Scan(&id); err != nil {
			t.Fatal(err)
		}
		userIDs = append(userIDs, id)
	}
	actor = userIDs[0]
	defer func() {
		for _, id := range userIDs {
			db.Exec(`DELETE FROM v2_user WHERE id=$1`, id)
		}
	}()
	if err = db.QueryRow(`INSERT INTO v2_ai_session(title,model,created_by,created_at,updated_at)VALUES('split','fixture',$1,1,1)RETURNING id`, actor).Scan(&session); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM v2_ai_session WHERE id=$1`, session)
	if _, err = s.SaveConfig(ctx, actor, ConfigSaveRequest{Enabled: true, BaseURL: "https://fixture.example.test/v1", Models: DefaultModels, APIKey: "fixture"}); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM v2_ai_config WHERE updated_by=$1`, actor)
	// Exercise every split action through the newly opened employee path after
	// an administrator has configured the relay. The session still belongs to
	// this operator and global configuration remains administrator-only.
	if _, err = db.Exec(`UPDATE v2_user SET is_admin=0,is_staff=1 WHERE id=$1`, actor); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`INSERT INTO v2_dns_probe(name,token_hash,last_heartbeat_at,created_at,updated_at)VALUES('split action',$1,$2,1,1)RETURNING id`, fmt.Sprintf("%064x", time.Now().UnixNano()), now).Scan(&probe); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM v2_dns_probe WHERE id=$1`, probe)
	conditions := fmt.Sprintf(`[{"field":"user_id","operator":"between","min":%d,"max":%d},{"field":"ua","operator":"contains_any","values":["Forest"]}]`, userIDs[1], userIDs[4])
	if err = db.QueryRow(`INSERT INTO v2_client_entry_user_policy(name,sort,action,conditions,entry_host,created_at,updated_at)VALUES('AI split fixture',10,'override',$1,'old.example.test',1,1)RETURNING id`, conditions).Scan(&policy); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM v2_client_entry_user_policy WHERE id=$1`, policy)
	var revision string
	tx, _ := db.BeginTx(ctx, nil)
	rule, err := admin.LoadAIEntryRule(ctx, tx, "policy", policy)
	tx.Rollback()
	if err != nil {
		t.Fatal(err)
	}
	revision = rule.Revision
	toolCall := func(name string, args any) (ToolOutcome, error) {
		raw, _ := json.Marshal(args)
		return s.callTool(ctx, actor, session, ToolCall{ID: fmt.Sprintf("call-%d", time.Now().UnixNano()), Function: ToolFunction{Name: name, Arguments: string(raw)}})
	}
	proposal, err := toolCall("propose_entry_split", map[string]any{"kind": "policy", "target_id": policy, "revision": revision, "use_backup_ip_pool": false, "entry_host_a": "a.example.test", "entry_host_b": "b.example.test", "reason": "explicit requested split"})
	if err != nil || !proposal.Pending {
		t.Fatal(proposal, err)
	}
	done, err := s.ApproveAction(ctx, actor, session, proposal.ActionID)
	if err != nil || done.Status != "succeeded" || !strings.Contains(done.Output, `"split_completed":true`) {
		t.Fatal(done, err)
	}
	if _, err = s.ApproveAction(ctx, actor, session, proposal.ActionID); !errors.Is(err, ErrConflict) {
		t.Fatal("replayed conversion", err)
	}
	var leaf int64
	var actual string
	if err = db.QueryRow(`SELECT id FROM v2_client_entry_user_policy_split_group WHERE policy_id=$1 ORDER BY id LIMIT 1`, policy).Scan(&leaf); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT conditions FROM v2_client_entry_user_policy WHERE id=$1`, policy).Scan(&actual); err != nil || actual != conditions {
		t.Fatal("UA not preserved", actual, err)
	}
	var monitor, target int64
	if err = db.QueryRow(`INSERT INTO v2_client_entry_monitor(policy_id,enabled,check_interval_sec,tcp_timeout_ms,failure_threshold,success_threshold,created_at,updated_at)VALUES($1,1,30,3000,3,2,$2,$2)RETURNING id`, policy, now).Scan(&monitor); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`INSERT INTO v2_client_entry_monitor_target(monitor_id,source_key,name,host,port,sort,auto_split_enabled,created_at,updated_at)VALUES($1,$2,'fixture','a.example.test',54101,10,0,$3,$3)RETURNING id`, monitor, fmt.Sprintf("policy:%d:split-group:%d", policy, leaf), now).Scan(&target); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM v2_client_entry_auto_split_operation WHERE policy_id=$1`, policy)
	for _, enabled := range []bool{true, false} {
		if _, err = db.Exec(`INSERT INTO v2_client_entry_monitor_state(target_id,probe_id,last_success,consecutive_failure,last_reported_at,updated_at)VALUES($1,$2,0,3,$3,$3)`, target, probe, now); err != nil {
			t.Fatal(err)
		}
		var operation int64
		if err = db.QueryRow(`INSERT INTO v2_client_entry_auto_split_operation(monitor_id,target_id,target_generation,policy_id,source_group_id,source_host,status,created_at,updated_at)SELECT monitor_id,id,generation,$2,$3,host,'pending',$4,$4 FROM v2_client_entry_monitor_target WHERE id=$1 RETURNING id`, target, policy, leaf, now).Scan(&operation); err != nil {
			t.Fatal(err)
		}
		snapshot, err := panel.ReadAIEntrySplitTarget(ctx, target)
		if err != nil {
			t.Fatal(err)
		}
		proposal, err = toolCall("propose_entry_auto_split", map[string]any{"target_id": target, "revision": snapshot.Revision, "enabled": enabled, "reason": "explicit requested toggle"})
		if err != nil || !proposal.Pending {
			t.Fatal(proposal, err)
		}
		done, err = s.ApproveAction(ctx, actor, session, proposal.ActionID)
		if err != nil || done.Status != "succeeded" {
			t.Fatal(done, err)
		}
		var states int
		var operationStatus string
		var flag bool
		if err = db.QueryRow(`SELECT COUNT(*) FROM v2_client_entry_monitor_state WHERE target_id=$1`, target).Scan(&states); err != nil || states != 0 {
			t.Fatal("JSON action did not reset target states", enabled, states, err)
		}
		if err = db.QueryRow(`SELECT status FROM v2_client_entry_auto_split_operation WHERE id=$1`, operation).Scan(&operationStatus); err != nil || operationStatus != "cancelled" {
			t.Fatal("JSON action did not cancel old incident", enabled, operationStatus, err)
		}
		if err = db.QueryRow(`SELECT auto_split_enabled=1 FROM v2_client_entry_monitor_target WHERE id=$1`, target).Scan(&flag); err != nil || flag != enabled {
			t.Fatal("toggle wrong flag", flag, err)
		}
	}
	// Closed automatic splitting does not block an explicit manual action.
	if _, err = db.Exec(`INSERT INTO v2_client_entry_monitor_state(target_id,probe_id,last_success,consecutive_failure,last_reported_at,updated_at)VALUES($1,$2,0,3,$3,$3)`, target, probe, now); err != nil {
		t.Fatal(err)
	}
	snapshot, err := panel.ReadAIEntrySplitTarget(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	proposal, err = toolCall("propose_entry_manual_split", map[string]any{"target_id": target, "revision": snapshot.Revision, "reason": "manual despite auto disabled"})
	if err != nil || !proposal.Pending {
		t.Fatal(proposal, err)
	}
	done, err = s.ApproveAction(ctx, actor, session, proposal.ActionID)
	if err != nil || done.Status != "succeeded" || !strings.Contains(done.Output, `"split_completed":false`) {
		t.Fatal(done, err)
	}
	var result struct {
		OperationID int64 `json:"operation_id"`
	}
	if json.Unmarshal([]byte(done.Output), &result) != nil || result.OperationID <= 0 {
		t.Fatal("durable operation missing", done.Output)
	}
	status, err := toolCall("entry_split_operation", map[string]any{"operation_id": result.OperationID})
	if err != nil || !strings.Contains(status.Content, `"status":"pending"`) {
		t.Fatal(status, err)
	}
}
