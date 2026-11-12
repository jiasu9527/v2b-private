package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"forest/go-api/internal/config"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func TestClientEntryMonitorConfigUpsertPostgres(t *testing.T) {
	dsn := os.Getenv("FOREST_AIOPS_TEST_DSN")
	if dsn == "" {
		t.Skip("requires disposable FOREST_AIOPS_TEST_DSN with initialized schema")
	}
	conf, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	db := stdlib.OpenDB(*conf)
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	schema := fmt.Sprintf("monitor_upsert_%d", time.Now().UnixNano())
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
	// Dedicated empty tables inherit the actual production column types,
	// identities and checks, without touching shared fixture rows.
	for _, table := range []string{"v2_user", "v2_dns_probe", "v2_client_entry_user_policy", "v2_client_entry_user_policy_member", "v2_client_entry_user_policy_split_group", "v2_client_entry_user_policy_split_assignment", "v2_client_entry_monitor_config", "v2_client_entry_monitor", "v2_client_entry_monitor_target", "v2_client_entry_monitor_state", "v2_client_entry_auto_split_operation"} {
		exec(`CREATE TABLE ` + table + ` (LIKE public.` + table + ` INCLUDING ALL)`)
	}
	exec(`INSERT INTO v2_client_entry_monitor_config(id,revision,updated_at) VALUES(1,7,1)`)
	exec(`INSERT INTO v2_client_entry_user_policy(id,name,sort,mode,action,conditions,entry_host,enabled,created_at,updated_at) VALUES
 (1,'keep split',10,'split','override','[]','',1,1,1),
 (2,'new split',20,'split','override','[]','',1,1,1),
 (3,'new domain',30,'standard','override','[]','new.example.test',1,1,1),
 (4,'rollback domain',40,'standard','override','[]','reject.example.test',1,1,1)`)
	exec(`INSERT INTO v2_client_entry_user_policy_split_group(id,policy_id,name,path,entry_host,sort,global_sort,created_at,updated_at) VALUES
 (11,1,'A','A','keep.example.test',10,10,1,1),
 (21,2,'A','A','a.example.test',10,20,1,1),
 (22,2,'B','B','b.example.test',20,30,1,1)`)
	exec(`INSERT INTO v2_user(id,email,password,uuid,token,created_at,updated_at) VALUES(1,'monitor@example.test','x','x','x',1,1),(2,'monitor2@example.test','x','x2','x2',1,1)`)
	exec(`INSERT INTO v2_client_entry_user_policy_split_assignment(policy_id,user_id,group_id,created_at,updated_at) VALUES(1,1,11,1,1),(2,1,21,1,1),(2,2,22,1,1)`)
	exec(`INSERT INTO v2_dns_probe(id,name,token_hash,enabled,last_heartbeat_at,created_at,updated_at) VALUES(9,'test probe','test-hash',1,1,1,1)`)
	exec(`INSERT INTO v2_client_entry_monitor(id,policy_id,enabled,check_interval_sec,tcp_timeout_ms,failure_threshold,success_threshold,created_at,updated_at) VALUES(10,1,1,60,4000,4,3,1,1)`)
	exec(`INSERT INTO v2_client_entry_monitor_target(id,monitor_id,source_key,name,host,port,auto_split_enabled,generation,created_at,updated_at) VALUES(100,10,'policy:1:split-group:11','keep A','keep.example.test',8443,1,5,1,1)`)
	exec(`INSERT INTO v2_client_entry_monitor_state(target_id,probe_id,last_success,last_latency_ms,consecutive_success,consecutive_failure,last_reported_at,updated_at) VALUES(100,9,1,76,8,0,100,100)`)
	snapshot := func() string {
		t.Helper()
		var value string
		if err := scoped.QueryRowContext(ctx, `SELECT jsonb_build_object(
 'monitor',(SELECT to_jsonb(m) FROM v2_client_entry_monitor m WHERE policy_id=1),
 'target',(SELECT to_jsonb(t) FROM v2_client_entry_monitor_target t WHERE id=100),
 'state',(SELECT jsonb_agg(s ORDER BY s.id) FROM v2_client_entry_monitor_state s),
 'groups',(SELECT jsonb_agg(g ORDER BY g.id) FROM v2_client_entry_user_policy_split_group g),
 'assignments',(SELECT jsonb_agg(a ORDER BY a.id) FROM v2_client_entry_user_policy_split_assignment a))::text`).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := snapshot()
	readConf := conf.Copy()
	readConf.RuntimeParams["default_transaction_read_only"] = "on"
	readDB := stdlib.OpenDB(*readConf)
	defer readDB.Close()
	reader := NewDBService(config.Config{}, readDB)
	page, err := reader.ReadClientEntryMonitorConfig(ctx, 0, 20, 0)
	if err != nil || page.Revision != 7 || page.Total != 4 || len(page.Items) != 4 {
		t.Fatalf("read-only configuration page=%#v err=%v", page, err)
	}
	keep := page.Items[0]
	if !keep.Configured || !keep.Enabled || keep.Targets[0].Port != 8443 || !keep.Targets[0].AutoSplitEnabled || keep.CheckIntervalSec != 60 || keep.FailureThreshold != 4 {
		t.Fatalf("existing monitor settings lost: %#v", keep)
	}
	request := ClientEntryMonitorSaveRequest{Revision: page.Revision}
	for _, item := range page.Items[1:3] {
		if item.Configured || item.Enabled {
			t.Fatalf("unconfigured candidate reported as active: %#v", item)
		}
		item.Enabled = true
		request.Items = append(request.Items, item.ClientEntryMonitorSaveItem)
	}
	s := NewDBService(config.Config{}, scoped)
	s.dnsFailoverSchemaOK = true
	readyClientEntrySchemaForPolicyTest(s)
	receipt, err := s.UpsertClientEntryMonitorReceipt(ctx, request)
	if err != nil || !receipt.Committed || receipt.Revision != 8 || receipt.SavedPolicyCount != 2 || receipt.SavedTargetCount != 3 || receipt.DeletedPolicyCount != 0 {
		t.Fatalf("upsert receipt=%#v err=%v", receipt, err)
	}
	if snapshot() != before {
		t.Fatal("upsert changed omitted monitor, target, health state, groups or user assignments")
	}
	after, err := reader.ReadClientEntryMonitorConfig(ctx, 0, 20, 0)
	if err != nil || after.Revision != 8 || !after.Items[1].Configured || !after.Items[2].Configured || len(after.Items[1].Targets) != 2 || len(after.Items[2].Targets) != 1 || !reflect.DeepEqual(after.Items[0], keep) {
		t.Fatalf("saved configuration page=%#v err=%v", after, err)
	}
	if got, err := s.UpsertClientEntryMonitorReceipt(ctx, request); !errors.Is(err, ErrClientEntryMonitorRevisionConflict) || got.Committed {
		t.Fatalf("stale revision accepted: %#v %v", got, err)
	}
	stable, err := reader.ReadClientEntryMonitorConfig(ctx, 0, 20, 0)
	if err != nil || !reflect.DeepEqual(stable, after) || snapshot() != before {
		t.Fatal("stale save changed persisted configuration", err)
	}
	// Force a failure after the first policy has been updated. Neither that
	// update nor the new failing monitor may survive the transaction rollback.
	exec(`ALTER TABLE v2_client_entry_monitor_target ADD CONSTRAINT test_reject_target CHECK(host<>'reject.example.test')`)
	changed := after.Items[1].ClientEntryMonitorSaveItem
	changed.CheckIntervalSec = 90
	newItem := after.Items[3].ClientEntryMonitorSaveItem
	newItem.Enabled = true
	got, err := s.UpsertClientEntryMonitorReceipt(ctx, ClientEntryMonitorSaveRequest{Revision: 8, Items: []ClientEntryMonitorSaveItem{changed, newItem}})
	var validation *ClientEntryMonitorValidationError
	if err == nil || got.Committed || errors.As(err, &validation) {
		t.Fatalf("SQL failure misclassified: %#v %v", got, err)
	}
	stable, err = reader.ReadClientEntryMonitorConfig(ctx, 0, 20, 0)
	if err != nil || !reflect.DeepEqual(stable, after) || snapshot() != before {
		t.Fatal("failed batch did not roll back completely", err)
	}

	// Give the writer a dedicated application name so the test can wait for an
	// actual PostgreSQL lock wait, rather than relying on scheduler timing.
	raceConf := conf.Copy()
	raceConf.RuntimeParams["application_name"] = schema + "_writer"
	raceDB := stdlib.OpenDB(*raceConf)
	defer raceDB.Close()
	writer := NewDBService(config.Config{}, raceDB)
	writer.dnsFailoverSchemaOK = true
	readyClientEntrySchemaForPolicyTest(writer)
	waitForLock := func(queryPart string) {
		t.Helper()
		deadline := time.NewTimer(5 * time.Second)
		defer deadline.Stop()
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			var waiting bool
			if err := scoped.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity
WHERE application_name=$1 AND wait_event_type='Lock' AND query LIKE $2)`, schema+"_writer", "%"+queryPart+"%").Scan(&waiting); err != nil {
				t.Fatal(err)
			}
			if waiting {
				return
			}
			select {
			case <-ticker.C:
			case <-deadline.C:
				t.Fatal("writer did not reach expected PostgreSQL lock wait")
			}
		}
	}
	stateJSON := func() string {
		t.Helper()
		var value string
		if err := scoped.QueryRowContext(ctx, `SELECT jsonb_build_object(
 'revision',(SELECT revision FROM v2_client_entry_monitor_config WHERE id=1),
 'monitors',(SELECT jsonb_agg(m ORDER BY m.id) FROM v2_client_entry_monitor m),
 'targets',(SELECT jsonb_agg(t ORDER BY t.id) FROM v2_client_entry_monitor_target t))::text`).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	for _, change := range []string{"host", "split"} {
		t.Run("reject_concurrent_"+change, func(t *testing.T) {
			page, err := reader.ReadClientEntryMonitorConfig(ctx, 2, 20, 0)
			if err != nil || len(page.Items) != 1 {
				t.Fatal("read candidate", err)
			}
			gate, err := scoped.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer gate.Rollback()
			if _, err := gate.ExecContext(ctx, `SELECT revision FROM v2_client_entry_monitor_config WHERE id=1 FOR UPDATE`); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				_, err := writer.UpsertClientEntryMonitorReceipt(ctx, ClientEntryMonitorSaveRequest{Revision: page.Revision, Items: []ClientEntryMonitorSaveItem{page.Items[0].ClientEntryMonitorSaveItem}})
				done <- err
			}()
			waitForLock("SELECT revision FROM v2_client_entry_monitor_config")
			// Topology changes do not advance the monitor configuration revision.
			if change == "host" {
				exec(`UPDATE v2_client_entry_user_policy_split_group SET entry_host='changed.example.test' WHERE id=21`)
			} else {
				exec(`INSERT INTO v2_client_entry_user_policy_split_group(id,policy_id,parent_id,name,path,entry_host,sort,global_sort,created_at,updated_at) VALUES
 (23,2,21,'AA','AA','aa.example.test',10,20,1,1), (24,2,21,'AB','AB','ab.example.test',20,21,1,1)`)
			}
			if err := s.refreshClientEntryMonitorTargets(ctx); err != nil {
				t.Fatal(err)
			}
			changedState := stateJSON()
			if err := gate.Commit(); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if !errors.Is(err, ErrClientEntryMonitorRevisionConflict) {
					t.Fatalf("stale topology accepted: %v", err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if stateJSON() != changedState {
				t.Fatal("conflicting save changed new host or recreated/deleted split targets")
			}
		})
	}
	t.Run("refresh_reads_after_monitor_lock", func(t *testing.T) {
		gate, err := scoped.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer gate.Rollback()
		if _, err := gate.ExecContext(ctx, `SELECT id FROM v2_client_entry_monitor WHERE policy_id=2 FOR UPDATE`); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- writer.refreshClientEntryMonitorTargets(ctx) }()
		waitForLock("SELECT id, policy_id FROM v2_client_entry_monitor")
		if _, err := gate.ExecContext(ctx, `UPDATE v2_client_entry_user_policy_split_group SET entry_host='fresh.example.test' WHERE id=23`); err != nil {
			t.Fatal(err)
		}
		if err := gate.Commit(); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		var host string
		if err := scoped.QueryRowContext(ctx, `SELECT host FROM v2_client_entry_monitor_target WHERE source_key='policy:2:split-group:23'`).Scan(&host); err != nil || host != "fresh.example.test" {
			t.Fatalf("refresh wrote stale host %q: %v", host, err)
		}
	})
	t.Run("topology_lock_conflicts_without_waiting", func(t *testing.T) {
		page, err := reader.ReadClientEntryMonitorConfig(ctx, 2, 20, 0)
		if err != nil {
			t.Fatal(err)
		}
		gate, err := scoped.BeginTx(ctx, &sql.TxOptions{})
		if err != nil {
			t.Fatal(err)
		}
		defer gate.Rollback()
		if err := lockClientEntryVisibleOrder(ctx, gate); err != nil {
			t.Fatal(err)
		}
		before := stateJSON()
		shortCtx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		_, err = writer.UpsertClientEntryMonitorReceipt(shortCtx, ClientEntryMonitorSaveRequest{Revision: page.Revision, Items: []ClientEntryMonitorSaveItem{page.Items[0].ClientEntryMonitorSaveItem}})
		if !errors.Is(err, ErrClientEntryMonitorRevisionConflict) {
			t.Fatalf("save waited for topology instead of rejecting before writes: %v", err)
		}
		if err := writer.refreshClientEntryMonitorTargets(shortCtx); !errors.Is(err, ErrClientEntryMonitorRevisionConflict) {
			t.Fatalf("refresh waited for topology instead of rejecting before writes: %v", err)
		}
		if stateJSON() != before {
			t.Fatal("topology conflict changed monitor configuration")
		}
	})
}
