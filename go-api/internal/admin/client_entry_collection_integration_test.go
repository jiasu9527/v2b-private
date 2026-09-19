package admin

import (
	"context"
	"database/sql"
	"os"
	"testing"

	platformpostgres "forest/go-api/internal/platform/postgres"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// The DSN must point to a disposable database initialized with install.pgsql.sql.
// This test never connects unless explicitly enabled and rolls its fixtures back
// by removing only IDs it has created.
func TestClientEntryCollectionsPostgresIntegration(t *testing.T) {
	dsn := os.Getenv("FOREST_COLLECTION_TEST_DSN")
	if dsn == "" {
		t.Skip("set FOREST_COLLECTION_TEST_DSN to a disposable PostgreSQL database")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	if err = platformpostgres.EnsureClientEntrySchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err = platformpostgres.EnsureClientEntrySchema(ctx, db); err != nil {
		t.Fatalf("idempotency: %v", err)
	}
	s := &DBService{db: db}
	readyClientEntrySchemaForPolicyTest(s)
	var p1, p2, p3, leaf1, leaf2, serverID int64
	err = db.QueryRowContext(ctx, `INSERT INTO v2_server_vmess(group_id,name,host,port,server_port,rate,network,created_at,updated_at) VALUES('[]','collection-test','127.0.0.1','443',443,'1','tcp',1,1) RETURNING id`).Scan(&serverID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.ExecContext(ctx, `DELETE FROM v2_server_vmess WHERE id=$1`, serverID) })
	for i, target := range []*int64{&p1, &p2, &p3} {
		mode := "standard"
		if i == 2 {
			mode = "split"
		}
		err = db.QueryRowContext(ctx, `INSERT INTO v2_client_entry_user_policy(name,sort,mode,created_at,updated_at) VALUES($1,$2,$3,1,1) RETURNING id`, "collection-test", 10*(i+1), mode).Scan(target)
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		db.ExecContext(ctx, `DELETE FROM v2_client_entry_user_policy WHERE id IN ($1,$2,$3)`, p1, p2, p3)
		db.ExecContext(ctx, `DELETE FROM v2_client_entry_collection WHERE name='collection-test'`)
	})
	for i, target := range []*int64{&leaf1, &leaf2} {
		err = db.QueryRowContext(ctx, `INSERT INTO v2_client_entry_user_policy_split_group(policy_id,name,path,entry_host,global_sort,created_at,updated_at) VALUES($1,$2,$2,'original.example.com',$3,1,1) RETURNING id`, p3, string(rune('A'+i)), 30+10*i).Scan(target)
		if err != nil {
			t.Fatal(err)
		}
	}
	original := map[int64]int64{}
	for _, id := range []int64{p1, p2, p3} {
		var order int64
		if err = db.QueryRowContext(ctx, `SELECT sort FROM v2_client_entry_user_policy WHERE id=$1`, id).Scan(&order); err != nil {
			t.Fatal(err)
		}
		original[id] = order
	}
	created, err := s.SaveClientEntryCollection(ctx, ClientEntryCollectionSaveRequest{Name: "collection-test", EntryHost: "first.example.com", Items: []ClientEntryCollectionItem{{Kind: "policy", ID: p1}, {Kind: "split_group", ID: leaf1}}})
	if err != nil {
		t.Fatal(err)
	}
	if created.Version != 1 {
		t.Fatal(created)
	}
	assertHost := func(table string, id int64, want string) {
		t.Helper()
		var host string
		if err := db.QueryRowContext(ctx, `SELECT entry_host FROM `+table+` WHERE id=$1`, id).Scan(&host); err != nil || host != want {
			t.Fatalf("host %s:%d=%q want %q: %v", table, id, host, want, err)
		}
	}
	assertHost("v2_client_entry_user_policy", p1, "first.example.com")
	assertHost("v2_client_entry_user_policy_split_group", leaf1, "first.example.com")
	updated, err := s.SaveClientEntryCollection(ctx, ClientEntryCollectionSaveRequest{ID: created.ID, Version: 1, Name: "collection-test", EntryHost: "second.example.com", Items: []ClientEntryCollectionItem{{Kind: "policy", ID: p1}, {Kind: "policy", ID: p2}, {Kind: "split_group", ID: leaf1}}})
	if err != nil {
		t.Fatal(err)
	}
	assertHost("v2_client_entry_user_policy", p2, "second.example.com")
	policyReq := ClientEntryUserPolicySaveRequest{ID: &p1, Name: "collection-test-renamed", Action: "override", EntryHost: "stale.example.com", Members: []ClientEntryGroupMemberSaveRequest{{ServerType: "vmess", ServerID: serverID}}}
	if _, err = s.SaveClientEntryUserPolicy(ctx, policyReq); err == nil {
		t.Fatal("stale ordinary policy form overwrote the collection host")
	}
	policyReq.EntryHost = "second.example.com"
	if _, err = s.SaveClientEntryUserPolicy(ctx, policyReq); err != nil {
		t.Fatalf("same-host edit: %v", err)
	}
	policyReq.Action = "original"
	policyReq.EntryHost = ""
	if _, err = s.SaveClientEntryUserPolicy(ctx, policyReq); err == nil {
		t.Fatal("member action changed away from override")
	}
	if _, err = s.UpdateClientEntryUserPolicySplitGroupHost(ctx, ClientEntryUserPolicyGroupHostUpdateRequest{PolicyID: p3, GroupID: leaf1, Name: "renamed", EntryHost: "stale.example.com"}); err == nil {
		t.Fatal("stale leaf form overwrote collection host")
	}
	if _, err = s.UpdateClientEntryUserPolicySplitGroupHost(ctx, ClientEntryUserPolicyGroupHostUpdateRequest{PolicyID: p3, GroupID: leaf1, Name: "renamed", EntryHost: "second.example.com"}); err != nil {
		t.Fatalf("same-host leaf edit: %v", err)
	}
	if _, err = s.SaveClientEntryCollection(ctx, ClientEntryCollectionSaveRequest{ID: created.ID, Version: 1, Name: "collection-test", EntryHost: "stale.example.com", Items: updated.Items}); err == nil {
		t.Fatal("stale overwrite accepted")
	}
	if _, err = s.RemoveClientEntryCollectionMember(ctx, updated.ID, updated.Version, ClientEntryCollectionItem{Kind: "policy", ID: p2}); err != nil {
		t.Fatal(err)
	}
	assertHost("v2_client_entry_user_policy", p2, "second.example.com")
	// Splitting/deleting a member must remove membership and advance its version.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = lockClientEntryVisibleOrder(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err = detachClientEntryCollectionMembers(ctx, tx, "split_group", leaf1); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var version, count int64
	if err = db.QueryRowContext(ctx, `SELECT version FROM v2_client_entry_collection WHERE id=$1`, created.ID).Scan(&version); err != nil || version != 4 {
		t.Fatalf("version=%d err=%v", version, err)
	}
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_client_entry_collection_member WHERE collection_id=$1`, created.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	for id, want := range original {
		var got int64
		db.QueryRowContext(ctx, `SELECT sort FROM v2_client_entry_user_policy WHERE id=$1`, id).Scan(&got)
		if got != want {
			t.Fatalf("sort changed %d %d->%d", id, want, got)
		}
	}
	if _, err = s.DeleteClientEntryUserPolicy(ctx, p1); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_client_entry_collection WHERE id=$1`, created.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("empty collection not cleaned count=%d err=%v", count, err)
	}
	// Dissolution does not touch the copied host on either remaining rule.
	dissolved, err := s.SaveClientEntryCollection(ctx, ClientEntryCollectionSaveRequest{Name: "collection-test", EntryHost: "final.example.com", Items: []ClientEntryCollectionItem{{Kind: "policy", ID: p2}, {Kind: "split_group", ID: leaf2}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DeleteClientEntryCollection(ctx, dissolved.ID, dissolved.Version); err != nil {
		t.Fatal(err)
	}
	assertHost("v2_client_entry_user_policy", p2, "final.example.com")
	assertHost("v2_client_entry_user_policy_split_group", leaf2, "final.example.com")
}
