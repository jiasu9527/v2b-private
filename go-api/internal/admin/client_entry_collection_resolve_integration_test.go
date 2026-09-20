package admin

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	platformpostgres "forest/go-api/internal/platform/postgres"
)

func TestClientEntryCollectionResolvePostgresIntegration(t *testing.T) {
	dsn := os.Getenv("FOREST_COLLECTION_TEST_DSN")
	if dsn == "" {
		t.Skip("set FOREST_COLLECTION_TEST_DSN to a disposable database")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	if err := platformpostgres.EnsureClientEntrySchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	s := &DBService{db: db}
	readyClientEntrySchemaForPolicyTest(s)
	mustExec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	mustID := func(query string, args ...any) int64 {
		t.Helper()
		var id int64
		if err := db.QueryRowContext(ctx, query, args...).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	var policyIDs, collectionIDs, userIDs []int64
	t.Cleanup(func() {
		for _, id := range collectionIDs {
			db.ExecContext(ctx, `DELETE FROM v2_client_entry_collection WHERE id=$1`, id)
		}
		for _, id := range policyIDs {
			db.ExecContext(ctx, `DELETE FROM v2_client_entry_user_policy WHERE id=$1`, id)
		}
		for _, id := range userIDs {
			db.ExecContext(ctx, `DELETE FROM v2_user WHERE id=$1`, id)
		}
	})
	serverID := mustID(`INSERT INTO v2_server_vmess(group_id,name,host,port,server_port,rate,network,created_at,updated_at) VALUES('[]','resolve-test','127.0.0.1','443',443,'1','tcp',1,1) RETURNING id`)
	t.Cleanup(func() { db.ExecContext(ctx, `DELETE FROM v2_server_vmess WHERE id=$1`, serverID) })
	for _, mode := range []string{"standard", "standard", "split"} {
		id := mustID(`INSERT INTO v2_client_entry_user_policy(name,sort,mode,entry_host,resolve_entry_host,created_at,updated_at) VALUES('resolve-test',$1,$2,'old.example.com',0,1,1) RETURNING id`, 10*(len(policyIDs)+1), mode)
		policyIDs = append(policyIDs, id)
		mustExec(`INSERT INTO v2_client_entry_user_policy_member(policy_id,server_type,server_id,sort,created_at,updated_at) VALUES($1,'vmess',$2,10,1,1)`, id, serverID)
	}
	p1, p2, parent := policyIDs[0], policyIDs[1], policyIDs[2]
	leaves := []int64{}
	for i, name := range []string{"A", "B", "C"} {
		leaves = append(leaves, mustID(`INSERT INTO v2_client_entry_user_policy_split_group(policy_id,name,path,entry_host,global_sort,created_at,updated_at) VALUES($1,$2,$2,'old.example.com',$3,1,1) RETURNING id`, parent, name, 30+i*10))
	}
	membersA := []ClientEntryCollectionItem{{Kind: "policy", ID: p1}, {Kind: "split_group", ID: leaves[0]}}
	membersB := []ClientEntryCollectionItem{{Kind: "policy", ID: p2}, {Kind: "split_group", ID: leaves[1]}}
	a, err := s.SaveClientEntryCollection(ctx, ClientEntryCollectionSaveRequest{Name: "resolve-test-a", EntryHost: "a.example.com", ResolveEntryHost: 1, Items: membersA})
	if err != nil {
		t.Fatal(err)
	}
	collectionIDs = append(collectionIDs, a.ID)
	b, err := s.SaveClientEntryCollection(ctx, ClientEntryCollectionSaveRequest{Name: "resolve-test-b", EntryHost: "b.example.com", ResolveEntryHost: 0, Items: membersB})
	if err != nil {
		t.Fatal(err)
	}
	collectionIDs = append(collectionIDs, b.ID)
	assertFlag := func(table string, id, want int64) {
		t.Helper()
		var got int64
		if err := db.QueryRowContext(ctx, `SELECT resolve_entry_host FROM `+table+` WHERE id=$1`, id).Scan(&got); err != nil || got != want {
			t.Fatalf("%s:%d resolve=%d want %d error=%v", table, id, got, want, err)
		}
	}
	assertEffective := func(id, want int64) {
		t.Helper()
		var got int64
		if err := db.QueryRowContext(ctx, `SELECT COALESCE(g.resolve_entry_host,p.resolve_entry_host) FROM v2_client_entry_user_policy_split_group g JOIN v2_client_entry_user_policy p ON p.id=g.policy_id WHERE g.id=$1`, id).Scan(&got); err != nil || got != want {
			t.Fatalf("leaf %d resolve=%d want %d error=%v", id, got, want, err)
		}
	}
	assertFlag("v2_client_entry_user_policy", p1, 1)
	assertFlag("v2_client_entry_user_policy", parent, 0)
	assertEffective(leaves[0], 1)
	assertEffective(leaves[1], 0)
	assertEffective(leaves[2], 0)
	// Legacy unselected leaves still inherit their parent; explicit collection leaves do not.
	mustExec(`UPDATE v2_client_entry_user_policy SET resolve_entry_host=1 WHERE id=$1`, parent)
	assertEffective(leaves[0], 1)
	assertEffective(leaves[1], 0)
	assertEffective(leaves[2], 1)
	a, err = s.SaveClientEntryCollection(ctx, ClientEntryCollectionSaveRequest{ID: a.ID, Version: a.Version, Name: a.Name, EntryHost: a.EntryHost, ResolveEntryHost: 0, Items: membersA})
	if err != nil {
		t.Fatal(err)
	}
	assertFlag("v2_client_entry_user_policy", p1, 0)
	assertEffective(leaves[0], 0)
	assertEffective(leaves[1], 0)
	assertEffective(leaves[2], 1)
	a, err = s.SaveClientEntryCollection(ctx, ClientEntryCollectionSaveRequest{ID: a.ID, Version: a.Version, Name: a.Name, EntryHost: a.EntryHost, ResolveEntryHost: 1, Items: membersA})
	if err != nil {
		t.Fatal(err)
	}
	// Old member forms cannot silently revert the collection's unified flag.
	off, on := int64(0), int64(1)
	req := ClientEntryUserPolicySaveRequest{ID: &p1, Name: "resolve-test", Action: "override", EntryHost: a.EntryHost, ResolveEntryHost: &off, Members: []ClientEntryGroupMemberSaveRequest{{ServerType: "vmess", ServerID: serverID}}}
	if _, err = s.SaveClientEntryUserPolicy(ctx, req); err == nil {
		t.Fatal("ordinary member bypassed collection resolve setting")
	}
	req.ResolveEntryHost = &on
	if _, err = s.SaveClientEntryUserPolicy(ctx, req); err != nil {
		t.Fatalf("unchanged resolve member edit: %v", err)
	}
	leafReq := ClientEntryUserPolicyGroupHostUpdateRequest{PolicyID: parent, GroupID: leaves[0], Name: "A", EntryHost: a.EntryHost, ResolveEntryHost: &off, Enabled: &on, Members: []ClientEntryGroupMemberSaveRequest{{ServerType: "vmess", ServerID: serverID}}}
	if _, err = s.UpdateClientEntryUserPolicySplitGroupHost(ctx, leafReq); err == nil {
		t.Fatal("split member bypassed collection resolve setting")
	}
	leafReq.ResolveEntryHost = &on
	if _, err = s.UpdateClientEntryUserPolicySplitGroupHost(ctx, leafReq); err != nil {
		t.Fatalf("unchanged leaf resolve edit: %v", err)
	}
	// Continue splitting a member inherits its effective flag, even if root differs.
	mustExec(`UPDATE v2_client_entry_user_policy SET resolve_entry_host=0 WHERE id=$1`, parent)
	suffix := time.Now().UnixNano()
	for i := 0; i < 2; i++ {
		token := fmt.Sprintf("r%016x%02d", suffix, i)
		uid := mustID(`INSERT INTO v2_user(email,password,uuid,token,created_at,updated_at) VALUES($1,'unused','test',$2,1,1) RETURNING id`, token+"@example.test", token)
		userIDs = append(userIDs, uid)
		mustExec(`INSERT INTO v2_client_entry_user_policy_split_assignment(policy_id,user_id,group_id,created_at,updated_at) VALUES($1,$2,$3,1,1)`, parent, uid, leaves[0])
	}
	if _, err = s.SplitClientEntryUserPolicyGroup(ctx, ClientEntryUserPolicyGroupSplitRequest{PolicyID: parent, GroupID: leaves[0], EntryHostA: "a1.example.com", EntryHostB: "a2.example.com"}); err != nil {
		t.Fatal(err)
	}
	childRows, err := db.QueryContext(ctx, `SELECT id,resolve_entry_host FROM v2_client_entry_user_policy_split_group WHERE parent_id=$1`, leaves[0])
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for childRows.Next() {
		var id, flag int64
		if err := childRows.Scan(&id, &flag); err != nil {
			t.Fatal(err)
		}
		count++
		if flag != 1 {
			t.Fatalf("child %d lost effective resolve", id)
		}
	}
	if err := childRows.Err(); err != nil {
		t.Fatal(err)
	}
	childRows.Close()
	if count != 2 {
		t.Fatalf("children=%d", count)
	}
	assertEffective(leaves[1], 0)
	assertEffective(leaves[2], 0)
	var version int64
	if err := db.QueryRowContext(ctx, `SELECT version FROM v2_client_entry_collection WHERE id=$1`, a.ID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RemoveClientEntryCollectionMember(ctx, a.ID, version, ClientEntryCollectionItem{Kind: "policy", ID: p1}); err != nil {
		t.Fatal(err)
	}
	assertFlag("v2_client_entry_user_policy", p1, 1)
	if _, err = s.DeleteClientEntryCollection(ctx, b.ID, b.Version); err != nil {
		t.Fatal(err)
	}
	assertFlag("v2_client_entry_user_policy", p2, 0)
	assertEffective(leaves[1], 0)
	// After dissolution a leaf can edit its own flag without changing siblings/root.
	leafReq.GroupID = leaves[1]
	leafReq.Name = "B"
	leafReq.EntryHost = b.EntryHost
	if _, err = s.UpdateClientEntryUserPolicySplitGroupHost(ctx, leafReq); err != nil {
		t.Fatal(err)
	}
	assertEffective(leaves[1], 1)
	assertEffective(leaves[2], 0)
	assertFlag("v2_client_entry_user_policy", parent, 0)
}
