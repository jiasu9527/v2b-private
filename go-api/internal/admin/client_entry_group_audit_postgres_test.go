package admin

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"forest/go-api/internal/config"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func TestClientEntryGroupAuditPostgres(t *testing.T) {
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
	schema := fmt.Sprintf("group_audit_%d", time.Now().UnixNano())
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
	for _, table := range []string{"v2_user", "v2_client_entry_user_policy", "v2_client_entry_user_policy_split_group", "v2_client_entry_user_policy_split_assignment", "v2_client_entry_collection", "v2_client_entry_collection_member", "v2_entry_delivery_request", "v2_entry_delivery_node", "v2_entry_delivery_settings"} {
		exec(`CREATE TABLE ` + table + ` (LIKE public.` + table + ` INCLUDING ALL)`)
	}
	exec(`INSERT INTO v2_entry_delivery_settings(id,keep_days) VALUES(1,7)`)
	exec(`INSERT INTO v2_user(id,email,password,uuid,token,created_at,updated_at) VALUES
 (1,'first@example.test','x','x1','x1',1,1),(2,'second@example.test','x','x2','x2',1,1),(3,'third@example.test','x','x3','x3',1,1)`)
	exec(`INSERT INTO v2_client_entry_user_policy(id,name,sort,mode,action,conditions,entry_host,enabled,created_at,updated_at) VALUES
 (10,'old',100,'split','override','[]','',1,1,1),(20,'other old',110,'split','override','[]','',1,1,1),
 (30,'replacement',10,'split','override','[]','',1,1,1),(40,'tree',150,'split','override','[]','',1,1,1)`)
	exec(`INSERT INTO v2_client_entry_user_policy_split_group(id,policy_id,parent_id,name,path,entry_host,sort,global_sort,created_at,updated_at) VALUES
 (11,10,NULL,'old A','A','192.0.2.11',10,100,1,1),(12,10,NULL,'old B','B','192.0.2.12',20,110,1,1),
 (21,20,NULL,'other A','A','configured.example.test',10,120,1,1),
 (31,30,NULL,'new A','A','192.0.2.31',10,10,1,1),(32,30,NULL,'new B','B','192.0.2.32',20,20,1,1),
 (40,40,NULL,'parent','A','',10,NULL,1,1),(41,40,40,'child','A.A','192.0.2.41',10,150,1,1)`)
	exec(`INSERT INTO v2_client_entry_collection(id,name,entry_host,version,created_at,updated_at) VALUES(5,'shared','shared.example.test',1,1,1)`)
	exec(`INSERT INTO v2_client_entry_collection_member(collection_id,split_group_id) VALUES(5,21)`)
	exec(`INSERT INTO v2_client_entry_user_policy_split_assignment(policy_id,group_id,user_id,created_at,updated_at) VALUES
 (10,11,1,1,1),(10,11,2,1,1),(10,11,4,1,1),(10,12,3,1,1),(20,21,1,1,1),(20,21,3,1,1),
 (30,31,1,1,1),(30,32,3,1,1)`)
	now := time.Now().Unix()
	write := func(id string, user, at int64, group int64, host string) {
		t.Helper()
		policy := int64(10)
		if group == 21 {
			policy = 20
		}
		exec(`INSERT INTO v2_entry_delivery_request(id,user_id,requested_at,completed_at,transport,status,response_complete,coverage,node_count) VALUES($1,$2,$3,$3,'subscription',200,true,'complete',1)`, id, user, at)
		exec(`INSERT INTO v2_entry_delivery_node(request_id,ordinal,server_type,server_id,policy_id,group_id,source_host,delivered_host,port,source) VALUES($1,0,'vless',1,$2,$3,$4,$4,'443','split')`, id, policy, group, host)
	}
	write("one", 1, now-90, 11, "192.0.2.11")
	// The same successful response serves multiple nodes and two groups.
	exec(`INSERT INTO v2_entry_delivery_node(request_id,ordinal,server_type,server_id,policy_id,group_id,source_host,delivered_host,port,source) VALUES
 ('one',1,'vless',2,10,11,'192.0.2.11','192.0.2.11','443','split'),
 ('one',2,'vless',3,20,21,'shared.example.test','shared.example.test','443','split')`)
	write("repeat", 1, now-80, 11, "192.0.2.11")
	write("old-host", 2, now-70, 11, "192.0.2.99")
	write("deleted", 4, now-60, 11, "192.0.2.11")
	write("other", 3, now-50, 12, "192.0.2.12")
	// Correct group but wrong policy must never be attributed to the group.
	write("wrong-policy", 99, now-50, 11, "192.0.2.11")
	exec(`UPDATE v2_entry_delivery_node SET policy_id=999 WHERE request_id='wrong-policy'`)
	for _, reason := range []string{"partial", "incomplete", "blocked", "write-error", "empty-host", "empty-port", "unknown-source", "unknown-protocol"} {
		write(reason, 99, now-40, 11, "192.0.2.11")
		switch reason {
		case "partial":
			exec(`UPDATE v2_entry_delivery_request SET coverage='partial' WHERE id=$1`, reason)
		case "incomplete":
			exec(`UPDATE v2_entry_delivery_request SET response_complete=false WHERE id=$1`, reason)
		case "blocked":
			exec(`UPDATE v2_entry_delivery_request SET status=403 WHERE id=$1`, reason)
		case "write-error":
			exec(`UPDATE v2_entry_delivery_request SET write_error='short write' WHERE id=$1`, reason)
		case "empty-host":
			exec(`UPDATE v2_entry_delivery_node SET delivered_host='' WHERE request_id=$1`, reason)
		case "empty-port":
			exec(`UPDATE v2_entry_delivery_node SET port='' WHERE request_id=$1`, reason)
		case "unknown-source":
			exec(`UPDATE v2_entry_delivery_node SET source='unknown' WHERE request_id=$1`, reason)
		case "unknown-protocol":
			exec(`UPDATE v2_entry_delivery_node SET server_type='unknown' WHERE request_id=$1`, reason)
		}
	}
	write("outside-left", 98, now-101, 11, "192.0.2.11")
	write("outside-right", 98, now, 11, "192.0.2.11")
	readConf := conf.Copy()
	readConf.RuntimeParams["default_transaction_read_only"] = "on"
	readDB := stdlib.OpenDB(*readConf)
	defer readDB.Close()
	service := NewDBService(config.Config{}, readDB)
	req := ClientEntryGroupAuditRequest{GroupIDs: []int64{11, 12, 21}, ReplacementGroupIDs: []int64{31, 32}, From: now - 100, To: now, PageSize: 1}
	first, err := service.AuditClientEntryGroups(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if first.Total != 3 || len(first.Items) != 1 || first.Items[0].GroupID != 11 || len(first.Revision) != 64 {
		t.Fatal(first)
	}
	wantSummary := ClientEntryGroupAuditCounts{AssignedUsers: 4, ExistingUsers: 3, DeletedUsers: 1, ReplacementCoveredUsers: 2, RemainingUsers: 1, DeliveredUsers: 4, ExistingDeliveredUsers: 3, DeletedDeliveredUsers: 1, CurrentEntryDeliveredUsers: 3, DeliveredRequests: 5, FirstDeliveryAt: now - 90, LastDeliveryAt: now - 50}
	if first.Summary != wantSummary {
		t.Fatalf("scope counts must use distinct user/request unions: got %+v want %+v", first.Summary, wantSummary)
	}
	one := first.Items[0]
	if one.ExistingUsers != 2 || one.ReplacementCoveredUsers != 1 || one.RemainingUsers != 1 || one.DeliveredUsers != 3 || one.DeliveredRequests != 4 || one.CurrentEntryDeliveredUsers != 2 || one.HostHistoryStatus != "different_host_observed" || one.DistinctObservedSourceHosts != 2 {
		t.Fatal("wrong group evidence", one)
	}
	if first.Window.CoverageCertain || first.Window.KeepDays != 7 || !strings.Contains(first.Window.Note, "不能") {
		t.Fatal("unjustified full coverage assertion", first.Window)
	}
	req.Current, req.ExpectedRevision = 2, first.Revision
	second, err := service.AuditClientEntryGroups(ctx, req)
	if err != nil || len(second.Items) != 1 || second.Items[0].GroupID != 12 || second.Summary != first.Summary || second.Revision != first.Revision {
		t.Fatal("pagination drift", second, err)
	}
	req.Current = 3
	third, err := service.AuditClientEntryGroups(ctx, req)
	if err != nil || third.Items[0].CollectionID != 5 || third.Items[0].EntryHost != "shared.example.test" || third.Items[0].ConfiguredHost != "configured.example.test" || third.Items[0].HostHistoryStatus != "no_change_observed_in_window" {
		t.Fatal("collection not reflected", third, err)
	}
	// Even equal counts may hide a changed membership. Revision binds identities.
	exec(`UPDATE v2_client_entry_user_policy_split_assignment SET user_id=5 WHERE group_id=11 AND user_id=4`)
	if _, err = service.AuditClientEntryGroups(ctx, req); !errors.Is(err, ErrClientEntryGroupAuditConflict) {
		t.Fatal("equal-count membership swap accepted", err)
	}
	exec(`UPDATE v2_client_entry_user_policy_split_assignment SET user_id=4 WHERE group_id=11 AND user_id=5`)
	exec(`UPDATE v2_client_entry_user_policy_split_assignment SET user_id=2 WHERE group_id=32 AND user_id=3`)
	if _, err = service.AuditClientEntryGroups(ctx, req); !errors.Is(err, ErrClientEntryGroupAuditConflict) {
		t.Fatal("stale membership accepted", err)
	}
	for _, ids := range [][]int64{{999}, {40}} {
		if _, err = service.AuditClientEntryGroups(ctx, ClientEntryGroupAuditRequest{GroupIDs: ids}); !errors.Is(err, ErrClientEntryGroupAuditInvalid) {
			t.Fatal("missing/nonleaf selected group accepted", ids, err)
		}
	}
	if _, err = service.AuditClientEntryGroups(ctx, ClientEntryGroupAuditRequest{GroupIDs: []int64{11}, ReplacementGroupIDs: []int64{40}}); !errors.Is(err, ErrClientEntryGroupAuditInvalid) {
		t.Fatal("nonleaf replacement accepted", err)
	}
	// Different resolved addresses from an unchanged domain are not evidence
	// that the configured entry host changed.
	write("resolved-a", 2, now-20, 21, "192.0.2.80")
	write("resolved-b", 2, now-10, 21, "192.0.2.81")
	exec(`UPDATE v2_entry_delivery_node SET source_host='shared.example.test',resolved=true WHERE request_id IN ('resolved-a','resolved-b')`)
	domain, err := service.AuditClientEntryGroups(ctx, ClientEntryGroupAuditRequest{GroupIDs: []int64{21}, From: now - 100, To: now})
	if err != nil || domain.Items[0].HostHistoryStatus != "no_change_observed_in_window" || domain.Items[0].DistinctObservedSourceHosts != 1 || domain.Items[0].CurrentEntryDeliveredUsers != 2 {
		t.Fatal("DNS changes misreported as rule changes", domain, err)
	}
	all, err := service.AuditClientEntryGroups(ctx, ClientEntryGroupAuditRequest{ReplacementGroupIDs: []int64{31, 32}, From: now - 8*86400, To: now})
	if err != nil || all.Total != 4 || !all.Window.RequestedBeforeRetention {
		t.Fatal("all leaves/default exclusion or retention warning", all, err)
	}
	for _, item := range all.Items {
		if item.GroupID == 31 || item.GroupID == 32 || item.GroupID == 40 {
			t.Fatal("replacement/parent included in all old groups", item)
		}
	}
}
