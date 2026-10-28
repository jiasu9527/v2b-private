package admin

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"forest/go-api/internal/config"
	platformpostgres "forest/go-api/internal/platform/postgres"
	usersvc "forest/go-api/internal/user"
)

// Run only against a disposable database initialized with install.pgsql.sql.
// This exercises conversion SQL through the actual subscription loader, rather
// than pre-constructing split policies or inferring membership from a past UA.
func TestClientEntrySplitUAPostgresIntegration(t *testing.T) {
	dsn := os.Getenv("FOREST_SPLIT_UA_TEST_DSN")
	if dsn == "" {
		t.Skip("set FOREST_SPLIT_UA_TEST_DSN to a disposable database")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := platformpostgres.EnsureClientEntrySchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	s := NewDBService(config.Config{}, db)
	readyClientEntrySchemaForPolicyTest(s)
	u := usersvc.NewDBService(config.Config{}, db)
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
	var policyID, fallbackID, collectionID int64
	userIDs := []int64{}
	groupID := mustID(`INSERT INTO v2_server_group(name,created_at,updated_at) VALUES('split-ua-test',1,1) RETURNING id`)
	serverID := mustID(`INSERT INTO v2_server_vmess(group_id,name,host,port,server_port,rate,network,"show",created_at,updated_at) VALUES($1,'split-ua-test','original.example.test','54101',54101,'1','tcp',1,1,1) RETURNING id`, fmt.Sprintf("[%d]", groupID))
	defer func() {
		db.ExecContext(ctx, `DELETE FROM v2_client_entry_collection WHERE id=$1`, collectionID)
		db.ExecContext(ctx, `DELETE FROM v2_client_entry_user_policy WHERE id IN ($1,$2)`, policyID, fallbackID)
		for _, id := range userIDs {
			db.ExecContext(ctx, `DELETE FROM v2_user_subscribe_activity WHERE user_id=$1`, id)
			db.ExecContext(ctx, `DELETE FROM v2_user WHERE id=$1`, id)
		}
		db.ExecContext(ctx, `DELETE FROM v2_server_vmess WHERE id=$1`, serverID)
		db.ExecContext(ctx, `DELETE FROM v2_server_group WHERE id=$1`, groupID)
	}()
	suffix := time.Now().UnixNano()
	for i := 0; i < 6; i++ {
		token := fmt.Sprintf("s%016x%02d", suffix, i)
		userIDs = append(userIDs, mustID(`INSERT INTO v2_user(email,password,uuid,token,group_id,transfer_enable,expired_at,created_at,updated_at) VALUES($1,'unused','test',$2,$3,1073741824,NULL,1,1) RETURNING id`, token+"@example.test", token, groupID))
	}
	conditions := fmt.Sprintf(`[{"field":"ua","operator":"contains_any","values":["Forest"]},{"field":"user_id","operator":"between","min":%d,"max":%d},{"field":"ua","operator":"excludes_any","values":["bot"]}]`, userIDs[0], userIDs[4])
	policyID = mustID(`INSERT INTO v2_client_entry_user_policy(name,sort,action,conditions,entry_host,created_at,updated_at) VALUES('split-ua-main',10,'override',$1,'before.example.test',1,1) RETURNING id`, conditions)
	fallbackID = mustID(`INSERT INTO v2_client_entry_user_policy(name,sort,action,entry_host,created_at,updated_at) VALUES('split-ua-fallback',100,'override','fallback.example.test',1,1) RETURNING id`)
	for _, id := range []int64{policyID, fallbackID} {
		mustExec(`INSERT INTO v2_client_entry_user_policy_member(policy_id,server_type,server_id,sort,created_at,updated_at) VALUES($1,'vmess',$2,10,1,1)`, id, serverID)
	}
	converted, err := s.ConvertClientEntryUserPolicyToSplit(ctx, ClientEntryUserPolicySplitConvertRequest{PolicyID: policyID, EntryHostA: "a.example.test", EntryHostB: "b.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	if converted.SnapshotUserCount != 5 || len(converted.SplitGroups) != 2 || converted.SplitGroups[0].UserCount != 3 || converted.SplitGroups[1].UserCount != 2 {
		t.Fatalf("not a balanced static ID snapshot: %#v", converted)
	}
	assertConditions := func() {
		t.Helper()
		var raw string
		if err := db.QueryRowContext(ctx, `SELECT conditions FROM v2_client_entry_user_policy WHERE id=$1`, policyID).Scan(&raw); err != nil || raw != conditions {
			t.Fatalf("UA conditions changed: %q, err=%v", raw, err)
		}
	}
	assertRoute := func(id int64, ua, host string, expectedPolicy int64) {
		t.Helper()
		servers, err := u.Servers(ctx, id, ua)
		if err != nil {
			t.Fatal(err)
		}
		if len(servers) != 1 || fmt.Sprint(servers[0]["host"]) != host {
			t.Fatalf("user %d ua=%q host want=%s got=%#v", id, ua, host, servers)
		}
		matches, err := s.MatchClientEntryUserPolicies(ctx, []ClientEntryUserPolicyMatchRequest{{UserID: id, UA: ua}})
		if err != nil || len(matches) != 1 || matches[0].Matched == nil || matches[0].Matched.ID != expectedPolicy || matches[0].Matched.EntryHost != host {
			t.Fatalf("admin simulation differs: %#v err=%v", matches, err)
		}
		snapshot, err := s.LoadSubscribeGuardSearchSnapshot(ctx, SubscribeGuardSearchLoadRequest{UserIDs: []int64{id}})
		if err != nil {
			t.Fatal(err)
		}
		guard, uncertain := snapshot.Match(id, ua, true)
		if uncertain || len(guard) != 1 || guard[0].ID != expectedPolicy || guard[0].EntryHost != host {
			t.Fatalf("guard differs: %#v uncertain=%v", guard, uncertain)
		}
	}
	assertConditions()
	for i, id := range userIDs {
		host, winner := "a.example.test", policyID
		if i >= 3 {
			host = "b.example.test"
		}
		if i == 5 {
			host, winner = "fallback.example.test", fallbackID
		}
		assertRoute(id, "forest Android", host, winner)
		for _, ua := range []string{"Clash", "Forest bot", ""} {
			assertRoute(id, ua, "fallback.example.test", fallbackID)
		}
	}
	// A further split changes only membership and target hosts, not the UA gate.
	if _, err := s.SplitClientEntryUserPolicyGroup(ctx, ClientEntryUserPolicyGroupSplitRequest{PolicyID: policyID, GroupID: converted.SplitGroups[0].ID, EntryHostA: "a1.example.test", EntryHostB: "a2.example.test"}); err != nil {
		t.Fatal(err)
	}
	assertConditions()
	assertRoute(userIDs[0], "Forest", "a1.example.test", policyID)
	assertRoute(userIDs[2], "Forest", "a2.example.test", policyID)
	assertRoute(userIDs[0], "Clash", "fallback.example.test", fallbackID)
	var leafID int64
	if err := db.QueryRowContext(ctx, `SELECT group_id FROM v2_client_entry_user_policy_split_assignment WHERE policy_id=$1 AND user_id=$2`, policyID, userIDs[0]).Scan(&leafID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateClientEntryUserPolicySplitGroupHost(ctx, ClientEntryUserPolicyGroupHostUpdateRequest{PolicyID: policyID, GroupID: leafID, Name: "renamed", EntryHost: "edited.example.test"}); err != nil {
		t.Fatal(err)
	}
	assertConditions()
	assertRoute(userIDs[0], "Forest", "edited.example.test", policyID)
	// Collection host overrides must not turn a UA-gated leaf into an open rule.
	collection, err := s.SaveClientEntryCollection(ctx, ClientEntryCollectionSaveRequest{Name: "split-ua-collection", EntryHost: "collection.example.test", Items: []ClientEntryCollectionItem{{Kind: "split_group", ID: leafID}, {Kind: "split_group", ID: converted.SplitGroups[1].ID}}})
	if err != nil {
		t.Fatal(err)
	}
	collectionID = collection.ID
	assertConditions()
	assertRoute(userIDs[0], "Forest", "collection.example.test", policyID)
	assertRoute(userIDs[0], "Clash", "fallback.example.test", fallbackID)
	// No new user is auto-assigned merely because it now uses an accepted UA.
	var count int64
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_client_entry_user_policy_split_assignment WHERE policy_id=$1`, policyID).Scan(&count); err != nil || count != 5 {
		t.Fatalf("membership changed after requests: count=%d err=%v", count, err)
	}
}
