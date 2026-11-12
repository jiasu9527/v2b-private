package admin

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"forest/go-api/internal/config"
	"forest/go-api/internal/entryevidence"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func TestHistoricalGroupSelectionSplitPostgres(t *testing.T) {
	dsn := os.Getenv("FOREST_ENTRY_EVIDENCE_TEST_DSN")
	if dsn == "" {
		t.Skip("requires disposable FOREST_ENTRY_EVIDENCE_TEST_DSN with initialized schema")
	}
	conf, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	root := stdlib.OpenDB(*conf)
	defer root.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	schema := fmt.Sprintf("history_selection_%d", time.Now().UnixNano())
	if _, err = root.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	defer root.ExecContext(context.Background(), `DROP SCHEMA `+schema+` CASCADE`)
	conf.RuntimeParams["search_path"] = schema
	db := stdlib.OpenDB(*conf)
	defer db.Close()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, table := range []string{"v2_user", "v2_server_vmess", "v2_client_entry_user_policy", "v2_client_entry_user_policy_member", "v2_client_entry_user_policy_split_group", "v2_client_entry_user_policy_split_assignment"} {
		exec(`CREATE TABLE ` + table + ` (LIKE public.` + table + ` INCLUDING ALL)`)
	}
	store := entryevidence.NewStore(db)
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	for id := 1; id <= 14; id++ {
		exec(`INSERT INTO v2_user(id,email,password,uuid,token,created_at,updated_at) VALUES($1,$2,'x',$3,$4,1,1)`, id, fmt.Sprintf("history-%d@example.test", id), fmt.Sprintf("uuid-%d", id), fmt.Sprintf("token-%d", id))
	}
	exec(`INSERT INTO v2_server_vmess(id,group_id,name,host,port,server_port,rate,network,"show",created_at,updated_at) VALUES(11,'[1]','history','original.example.test','54101',54101,'1','tcp',1,1,1)`)
	// A current fixed assignment is deliberately present without historical
	// delivery. The query must not mistake it for a historical recipient.
	exec(`INSERT INTO v2_client_entry_user_policy(id,name,sort,mode,action,conditions,entry_host,enabled,created_at,updated_at) VALUES(700,'old source',10,'split','override','[]','',1,1,1)`)
	exec(`INSERT INTO v2_client_entry_user_policy_split_group(id,policy_id,name,path,entry_host,sort,global_sort,created_at,updated_at) VALUES(700,700,'old leaf','A','192.0.2.1',10,10,1,1)`)
	exec(`INSERT INTO v2_client_entry_user_policy_split_assignment(policy_id,user_id,group_id,created_at,updated_at) VALUES(700,11,700,1,1)`)
	now := time.Now().Unix()
	from, to := now-100, now
	node := func(group int64, host string) entryevidence.Node {
		return entryevidence.Node{ServerType: "vmess", ServerID: 11, PolicyID: 700, GroupID: group, DeliveredHost: host, SourceHost: host, Port: "54101", Source: "split"}
	}
	write := func(label string, user, at int64, status int, coverage string, nodes ...entryevidence.Node) {
		t.Helper()
		if err := store.Record(ctx, entryevidence.Request{ID: label, UserID: user, RequestedAt: at, CompletedAt: at, Status: status, Coverage: coverage, ResponseComplete: true, Transport: "subscription"}, nodes); err != nil {
			t.Fatal(err)
		}
	}
	a, b, fault := "192.0.2.1", "192.0.2.2", "192.0.2.99"
	for _, user := range []int64{1, 2, 3, 6, 7, 8, 9, 13} {
		write(fmt.Sprintf("base-%d", user), user, now-50, 200, "complete", node(700, a))
	}
	write("repeat-1", 1, now-40, 200, "complete", node(701, b), node(700, a))
	write("excluded-unrelated-source", 2, now-20, 200, "complete", node(999, fault))
	write("incomplete", 4, now-40, 200, "partial", node(700, a))
	write("wrong-source", 5, now-40, 200, "complete", node(999, a))
	exec(`DELETE FROM v2_user WHERE id=6`)
	write("failed-exclusion", 7, now-20, 503, "complete", node(999, fault))
	write("exclusion-outside-window", 8, to, 200, "complete", node(999, fault))
	write("exclusion-other-node", 9, now-20, 200, "complete", node(700, a), node(999, fault))
	write("intersection-same-node", 10, from, 200, "complete", node(700, b), node(999, a))
	write("bad-write-candidate", 12, now-40, 200, "complete", node(700, a))
	exec(`UPDATE v2_entry_delivery_request SET write_error='short_write',response_complete=true WHERE id='bad-write-candidate'`)
	write("bad-write-exclusion", 13, now-20, 200, "complete", node(999, fault))
	exec(`UPDATE v2_entry_delivery_request SET write_error='short_write',response_complete=true WHERE id='bad-write-exclusion'`)
	extra := node(700, a)
	extra.Source = "extra"
	write("extra-is-not-group-entry", 14, now-40, 200, "complete", extra)
	selection := entryevidence.HistorySelection{GroupIDs: []int64{701, 700, 700}, ExcludeHosts: []string{fault, fault}}
	want := []int64{1, 3, 7, 8, 10, 13}
	ids, err := entryevidence.HistoricalSelectionUserIDs(ctx, db, selection, from, to)
	if err != nil || !reflect.DeepEqual(ids, want) {
		t.Fatal("history union/exclusion semantics", ids, err)
	}
	selection.Host = a
	ids, err = entryevidence.HistoricalSelectionUserIDs(ctx, db, selection, from, to)
	if err != nil || !reflect.DeepEqual(ids, []int64{1, 3, 7, 8, 13}) {
		t.Fatal("host/group must match the same delivered node", ids, err)
	}
	selection.Host = ""
	s := NewDBService(config.Config{}, db)
	readyClientEntrySchemaForPolicyTest(s)
	req := ClientEntryUserPolicySplitPreviewRequest{HistoryGroupIDs: selection.GroupIDs, HistoryExcludeHosts: selection.ExcludeHosts, HistoryFrom: from, HistoryTo: to}
	preview, err := s.PreviewClientEntryUserPolicySplit(ctx, req)
	if err != nil || preview.UserCount != 6 || !reflect.DeepEqual(preview.HistoryGroupIDs, []int64{700, 701}) || !reflect.DeepEqual(preview.HistoryExcludeHosts, []string{fault}) {
		t.Fatal("normalized selection not echoed", preview, err)
	}
	create := ClientEntryUserPolicySplitCreateRequest{Name: "extracted", HistoryGroupIDs: preview.HistoryGroupIDs, HistoryExcludeHosts: preview.HistoryExcludeHosts, HistoryFrom: preview.From, HistoryTo: preview.To, SnapshotHash: preview.SnapshotHash, Members: []ClientEntryGroupMemberSaveRequest{{ServerType: "vmess", ServerID: 11}}, EntryHostA: "192.0.2.7", EntryHostB: "192.0.2.8"}
	altered := create
	altered.HistoryGroupIDs = []int64{700} // Same users, changed selection must still invalidate.
	if _, err = s.CreateClientEntryUserPolicySplit(ctx, altered); !errors.Is(err, ErrEntryEvidenceConflict) {
		t.Fatal("changed filter accepted even with same recipients", err)
	}
	record, err := s.CreateClientEntryUserPolicySplit(ctx, create)
	if err != nil || record.SnapshotUserCount != 6 || len(record.SplitGroups) != 2 || record.SplitGroups[0].UserCount != 3 || record.SplitGroups[1].UserCount != 3 {
		t.Fatal("balanced independent groups not created", record, err)
	}
	var persisted []int64
	rows, err := db.QueryContext(ctx, `SELECT user_id FROM v2_client_entry_user_policy_split_assignment WHERE policy_id=$1 ORDER BY user_id`, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		persisted = append(persisted, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if !reflect.DeepEqual(persisted, want) {
		t.Fatal("wrong frozen assignments", persisted)
	}
	var old int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_client_entry_user_policy_split_assignment WHERE policy_id=700 AND user_id=11 AND group_id=700`).Scan(&old); err != nil || old != 1 {
		t.Fatal("source assignment changed", old, err)
	}
	// Late evidence inside the fixed window changes membership and requires a
	// fresh preview. It must not be silently included in another creation.
	write("late-ingested", 11, now-30, 200, "complete", node(700, a))
	if _, err = s.CreateClientEntryUserPolicySplit(ctx, create); !errors.Is(err, ErrEntryEvidenceConflict) {
		t.Fatal("late evidence changed frozen recipients", err)
	}
}
