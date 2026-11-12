package admin

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"forest/go-api/internal/config"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func TestRetirementPostgres(t *testing.T) {
	dsn := os.Getenv("FOREST_AIOPS_TEST_DSN")
	if dsn == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	conf, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	db := stdlib.OpenDB(*conf)
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	schema := fmt.Sprintf("retirement_%d", time.Now().UnixNano())
	if _, err = db.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	defer db.ExecContext(context.Background(), `DROP SCHEMA `+schema+` CASCADE`)
	conf.RuntimeParams["search_path"] = schema
	scoped := stdlib.OpenDB(*conf)
	defer scoped.Close()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := scoped.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, table := range []string{"v2_user", "v2_client_entry_user_policy", "v2_client_entry_user_policy_member", "v2_client_entry_user_policy_split_group", "v2_client_entry_user_policy_split_assignment", "v2_client_entry_collection", "v2_client_entry_collection_member", "v2_client_entry_group_ip"} {
		exec(`CREATE TABLE ` + table + ` (LIKE public.` + table + ` INCLUDING ALL)`)
	}
	exec(`INSERT INTO v2_user(id,email,password,uuid,token,is_admin,created_at,updated_at) VALUES(1,'admin@example.test','x','x1','x1',1,1,1),(2,'second-admin@example.test','x','x2','x2',1,1,1),(101,'a@example.test','x','x101','x101',0,1,1),(102,'b@example.test','x','x102','x102',0,1,1),(103,'c@example.test','x','x103','x103',0,1,1)`)
	service := NewDBService(config.Config{}, scoped)
	readyClientEntrySchemaForPolicyTest(service)

	if err = service.ensureClientEntryRetirement(ctx, 1); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO v2_client_entry_user_policy(id,name,sort,mode,action,conditions,entry_host,enabled,created_at,updated_at) VALUES(10,'old',100,'split','override','[]','',1,1,1),(20,'new',20,'split','override','[]','',1,1,1),(30,'fallback',1000,'standard','override','[]','192.0.2.30',1,1,1)`)
	exec(`INSERT INTO v2_client_entry_user_policy_member(policy_id,server_type,server_id,sort,created_at,updated_at) VALUES(10,'v2node',14,10,1,1),(20,'v2node',14,10,1,1),(30,'v2node',14,10,1,1)`)
	exec(`INSERT INTO v2_client_entry_user_policy_split_group(id,policy_id,parent_id,name,path,entry_host,sort,global_sort,created_at,updated_at) VALUES(10,10,NULL,'root','R','',10,NULL,1,1),(11,10,10,'old A','R.A','192.0.2.11',10,100,1,1),(12,10,10,'sibling','R.B','192.0.2.12',20,110,1,1),(21,20,NULL,'new A','A','192.0.2.21',10,20,1,1)`)
	exec(`INSERT INTO v2_client_entry_user_policy_split_assignment(policy_id,group_id,user_id,created_at,updated_at) VALUES(10,11,101,1,1),(10,11,102,1,1),(10,12,103,1,1),(20,21,101,1,1)`)
	exec(`INSERT INTO v2_dns_probe(id,name,token_hash,enabled,last_heartbeat_at,created_at,updated_at) VALUES(1,'test','x',1,1,1,1)`)
	exec(`INSERT INTO v2_client_entry_monitor(id,policy_id,enabled,created_at,updated_at) VALUES(10,10,1,1,1),(20,20,1,1,1)`)
	exec(`INSERT INTO v2_client_entry_monitor_target(id,monitor_id,source_key,name,host,port,auto_split_enabled,created_at,updated_at) VALUES(11,10,'policy:10:split-group:11','old A','192.0.2.11',443,1,1,1),(12,10,'policy:10:split-group:12','keep sibling','192.0.2.12',443,1,1,1),(21,20,'policy:20:split-group:21','new A','192.0.2.21',443,0,1,1)`)
	exec(`INSERT INTO v2_client_entry_monitor_state(target_id,probe_id,last_success,updated_at) VALUES(11,1,0,1),(12,1,1,1),(21,1,1,1)`)
	exec(`INSERT INTO v2_client_entry_backup_ip(id,name,ip,port,created_at,updated_at) VALUES(11,'old','192.0.2.11',443,1,1),(12,'sibling','192.0.2.12',443,1,1)`)
	exec(`INSERT INTO v2_client_entry_auto_split_operation(id,monitor_id,target_id,target_generation,policy_id,source_group_id,source_host,status,created_at,updated_at) VALUES
 (1,10,11,1,10,11,'192.0.2.11','pending',1,1),
 (2,10,11,1,10,11,'192.0.2.11','succeeded',1,1),
 (3,10,12,1,10,12,'192.0.2.12','pending',1,1)`)
	exec(`INSERT INTO v2_client_entry_backup_ip_state(backup_ip_id,probe_id,last_success,updated_at) VALUES(11,1,0,1),(12,1,1,1)`)
	req := ClientEntryRetirementPreviewRequest{GroupIDs: []int64{11}, ReplacementGroupIDs: []int64{21}, AllowFallback: true, FallbackPolicyID: 30, Reason: "remaining users use fallback"}
	preview, err := service.PreviewClientEntryRetirement(ctx, 1, req)
	if err != nil || !preview.CanExecute || preview.ReplacementCoveredUsers != 1 || preview.FallbackUsers != 1 {
		t.Fatal(preview, err)
	}
	if len(preview.PreviewHash) != 64 || preview.PreviewID < 1 || preview.ExpiresAt <= time.Now().Unix() || !strings.Contains(strings.Join(preview.Warnings, " "), "保底尚未配置监测") {
		t.Fatal("preview missing durable evidence", preview)
	}
	readConf := conf.Copy()
	readConf.RuntimeParams["default_transaction_read_only"] = "on"
	readDB := stdlib.OpenDB(*readConf)
	defer readDB.Close()
	reader := NewDBService(config.Config{}, readDB)
	status, err := reader.GetClientEntryRetirement(ctx, 1, preview.PreviewID)
	if err != nil || status.Committed || status.Status != "previewed" {
		t.Fatal(status, err)
	}
	if _, err = reader.GetClientEntryRetirement(ctx, 2, preview.PreviewID); !errors.Is(err, ErrClientEntryRetirementNotFound) {
		t.Fatal("actor escaped preview binding", err)
	}
	apply := ClientEntryRetirementExecuteRequest{PreviewID: preview.PreviewID, PreviewHash: preview.PreviewHash, IdempotencyKey: "test-retirement"}
	// A changed ID set of equal size must be detected, with no deletion.
	exec(`UPDATE v2_client_entry_user_policy_split_assignment SET user_id=103 WHERE group_id=21`)
	if receipt, err := service.ExecuteClientEntryRetirement(ctx, 1, apply); !errors.Is(err, ErrClientEntryRetirementConflict) || receipt.Committed {
		t.Fatal("stale membership accepted", receipt, err)
	}
	exec(`UPDATE v2_client_entry_user_policy_split_assignment SET user_id=101 WHERE group_id=21`)
	// Force failure after the target deletion; the whole transaction must restore
	// targets, failed probe state and assignments, and write no completion receipt.
	exec(`CREATE FUNCTION fail_retire_assignment() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'forced rollback'; END $$`)
	exec(`CREATE TRIGGER fail_retire_assignment BEFORE DELETE ON v2_client_entry_user_policy_split_assignment FOR EACH ROW EXECUTE FUNCTION fail_retire_assignment()`)
	if receipt, err := service.ExecuteClientEntryRetirement(ctx, 1, apply); err == nil || receipt.Committed || errors.Is(err, ErrClientEntryRetirementInvalid) {
		t.Fatal("SQL failure mislabeled", receipt, err)
	}
	var count int64
	if err = scoped.QueryRowContext(ctx, `SELECT count(*) FROM v2_client_entry_monitor_state WHERE target_id=11`).Scan(&count); err != nil || count != 1 {
		t.Fatal("transaction did not restore health", count, err)
	}
	exec(`DROP TRIGGER fail_retire_assignment ON v2_client_entry_user_policy_split_assignment`)
	type applyResult struct {
		receipt ClientEntryRetirementReceipt
		err     error
	}
	results := make(chan applyResult, 3)
	for i := 0; i < 3; i++ {
		go func() { r, e := service.ExecuteClientEntryRetirement(ctx, 1, apply); results <- applyResult{r, e} }()
	}
	var receipt ClientEntryRetirementReceipt
	firstCount, replayCount := 0, 0
	for i := 0; i < 3; i++ {
		res := <-results
		if res.err != nil {
			t.Fatal("concurrent execute", res.err)
		}
		if res.receipt.Replayed {
			replayCount++
		} else {
			firstCount++
			receipt = res.receipt
		}
	}
	if firstCount != 1 || replayCount != 2 {
		t.Fatal("not exactly one committed mutation", firstCount, replayCount)
	}
	if err != nil || !receipt.Committed || receipt.RemovedGroups != 1 || receipt.RemovedAncestors != 0 || receipt.RemovedAssignments != 2 || receipt.RemovedMonitorTargets != 1 || receipt.CancelledPendingOperations != 1 || !reflect.DeepEqual(receipt.ReleasedIPs, []string{"192.0.2.11"}) {
		t.Fatal("execution failed", receipt, err)
	}
	for _, q := range []string{`SELECT count(*) FROM v2_client_entry_user_policy_split_group WHERE id=12`, `SELECT count(*) FROM v2_client_entry_user_policy_split_group WHERE id=10`, `SELECT count(*) FROM v2_client_entry_user_policy_split_assignment WHERE group_id=21 AND user_id=101`, `SELECT count(*) FROM v2_client_entry_monitor_target WHERE id=12 AND auto_split_enabled=1`, `SELECT count(*) FROM v2_client_entry_backup_ip_state WHERE backup_ip_id=11 AND last_success=0`,
		`SELECT count(*) FROM v2_client_entry_auto_split_operation WHERE id=1 AND status='cancelled'`,
		`SELECT count(*) FROM v2_client_entry_auto_split_operation WHERE id=2 AND status='succeeded' AND updated_at=1 AND completed_at IS NULL`,
		`SELECT count(*) FROM v2_client_entry_auto_split_operation WHERE id=3 AND status='pending' AND updated_at=1`} {
		if err = scoped.QueryRowContext(ctx, q).Scan(&count); err != nil || count != 1 {
			t.Fatal("preserved record lost", q, count, err)
		}
	}
	status, err = reader.GetClientEntryRetirement(ctx, 1, preview.PreviewID)
	if err != nil || !reflect.DeepEqual(status, receipt) {
		t.Fatal("read-only receipt mismatch", status, err)
	}
	replay, err := service.ExecuteClientEntryRetirement(ctx, 1, apply)
	if err != nil || !replay.Replayed || replay.MonitorRevision != receipt.MonitorRevision {
		t.Fatal("idempotency failed", replay, err)
	}
	apply.IdempotencyKey = "other-key"
	if _, err = service.ExecuteClientEntryRetirement(ctx, 1, apply); !errors.Is(err, ErrClientEntryRetirementConflict) {
		t.Fatal("changed idempotency accepted", err)
	}
	// Delete the remaining sibling and prune only its now-empty parent/policy.
	req.GroupIDs = []int64{12}
	preview, err = service.PreviewClientEntryRetirement(ctx, 1, req)
	if err != nil || !preview.CanExecute || !reflect.DeepEqual(preview.RemovedAncestorIDs, []int64{10}) || !reflect.DeepEqual(preview.RemovedPolicyIDs, []int64{10}) {
		t.Fatal("tree closure preview", preview, err)
	}
	receipt, err = service.ExecuteClientEntryRetirement(ctx, 1, ClientEntryRetirementExecuteRequest{PreviewID: preview.PreviewID, PreviewHash: preview.PreviewHash, IdempotencyKey: "second-retirement"})
	if err != nil || !receipt.Committed || receipt.RemovedAncestors != 1 {
		t.Fatal(receipt, err)
	}
	if err = scoped.QueryRowContext(ctx, `SELECT count(*) FROM v2_client_entry_user_policy WHERE id=10`).Scan(&count); err != nil || count != 0 {
		t.Fatal("empty policy survived", count, err)
	}
	var backup string
	if err = scoped.QueryRowContext(ctx, `SELECT snapshot::text FROM v2_client_entry_retirement WHERE id=$1`, preview.PreviewID).Scan(&backup); err != nil || !strings.Contains(backup, "execution_audit") || !strings.Contains(backup, "192.0.2.12") {
		t.Fatal("prechange audit missing", err)
	}
}
