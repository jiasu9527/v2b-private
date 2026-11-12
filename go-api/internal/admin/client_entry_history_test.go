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
	"forest/go-api/internal/entryevidence"
	platformpostgres "forest/go-api/internal/platform/postgres"
	usersvc "forest/go-api/internal/user"
)

func TestHistoricalEntryWindowValidation(t *testing.T) {
	now := time.Now().Unix()
	for _, v := range []struct {
		host     string
		from, to int64
	}{{"", now - 1, now}, {"https://x.test/?token=x", now - 1, now}, {"192.0.2.7", 0, now}, {"192.0.2.7", now, now}, {"192.0.2.7", now - 1, now + 100}} {
		if _, err := normalizeHistoricalEntryWindow(v.host, v.from, v.to); err == nil {
			t.Fatal("accepted invalid window", v)
		}
	}
	if host, err := normalizeHistoricalEntryWindow("192.0.2.7", now-10, now); err != nil || host != "192.0.2.7" {
		t.Fatal(host, err)
	}
	if historicalEntryHash("192.0.2.7", 1, 2, []int64{1, 2}) == historicalEntryHash("192.0.2.7", 1, 2, []int64{1, 3}) {
		t.Fatal("snapshot failed to bind membership")
	}
}

func TestHistoricalEntrySelectionHashBindsFiltersAndMembership(t *testing.T) {
	now := time.Now().Unix()
	selection, err := normalizeHistoricalEntrySelection("", []int64{9, 7, 9}, []string{"Fault.Example.TEST", "fault.example.test"}, now-60, now)
	if err != nil || !reflect.DeepEqual(selection.GroupIDs, []int64{7, 9}) || !reflect.DeepEqual(selection.ExcludeHosts, []string{"fault.example.test"}) {
		t.Fatal(selection, err)
	}
	ids := []int64{1, 2}
	hash := historicalEntrySelectionHash(selection, now-60, now, ids)
	for _, changed := range []entryevidence.HistorySelection{
		{GroupIDs: []int64{7}, ExcludeHosts: selection.ExcludeHosts},
		{GroupIDs: selection.GroupIDs},
		{GroupIDs: selection.GroupIDs, ExcludeHosts: []string{"192.0.2.7"}},
		{Host: "192.0.2.8", GroupIDs: selection.GroupIDs, ExcludeHosts: selection.ExcludeHosts},
	} {
		if historicalEntrySelectionHash(changed, now-60, now, ids) == hash {
			t.Fatal("filter mutation did not invalidate preview", changed)
		}
	}
	if historicalEntrySelectionHash(selection, now-59, now, ids) == hash || historicalEntrySelectionHash(selection, now-60, now, []int64{1, 3}) == hash {
		t.Fatal("window or membership mutation did not invalidate preview")
	}
	if historicalEntrySelectionHash(entryevidence.HistorySelection{Host: "192.0.2.7"}, 1, 2, ids) != historicalEntryHash("192.0.2.7", 1, 2, ids) {
		t.Fatal("existing host-only preview compatibility lost")
	}
}

func TestHistoricalEntrySplitPostgres(t *testing.T) {
	dsn := os.Getenv("FOREST_ENTRY_EVIDENCE_TEST_DSN")
	if dsn == "" {
		t.Skip("requires disposable FOREST_ENTRY_EVIDENCE_TEST_DSN")
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
	store := entryevidence.NewStore(db)
	if err = store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	s := NewDBService(config.Config{}, db)
	readyClientEntrySchemaForPolicyTest(s)
	id := func(q string, args ...any) int64 {
		t.Helper()
		var v int64
		if err := db.QueryRowContext(ctx, q, args...).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	marker := fmt.Sprint(time.Now().UnixNano())
	now := time.Now().Unix()
	host := "192.0.2.147"
	group := id(`INSERT INTO v2_server_group(name,created_at,updated_at)VALUES($1,1,1) RETURNING id`, marker)
	server := id(`INSERT INTO v2_server_vmess(group_id,name,host,port,server_port,rate,network,"show",created_at,updated_at)VALUES($1,'history','original.example.test','54101',54101,'1','tcp',1,1,1) RETURNING id`, fmt.Sprintf("[%d]", group))
	ids := []int64{}
	policies := []int64{}
	defer func() {
		for _, p := range policies {
			db.Exec(`DELETE FROM v2_client_entry_user_policy WHERE id=$1`, p)
		}
		for _, u := range ids {
			db.Exec(`DELETE FROM v2_entry_delivery_request WHERE user_id=$1`, u)
			db.Exec(`DELETE FROM v2_user WHERE id=$1`, u)
		}
		db.Exec(`DELETE FROM v2_server_vmess WHERE id=$1`, server)
		db.Exec(`DELETE FROM v2_server_group WHERE id=$1`, group)
	}()
	for i := 0; i < 6; i++ {
		ids = append(ids, id(`INSERT INTO v2_user(email,password,uuid,token,group_id,transfer_enable,created_at,updated_at)VALUES($1,'x','x',$3,$2,1073741824,1,1) RETURNING id`, fmt.Sprintf("history-%s-%d@example.test", marker, i), group, fmt.Sprintf("h%s%02d", marker, i)))
	}
	node := entryevidence.Node{ServerType: "vmess", ServerID: server, PolicyID: 999, DeliveredHost: host, SourceHost: host, Port: "54101", Source: "policy"}
	write := func(label string, u, at int64, status int, coverage string, nodes ...entryevidence.Node) {
		t.Helper()
		err := store.Record(ctx, entryevidence.Request{ID: marker + label, UserID: u, EmailAtEvent: fmt.Sprintf("user-%d@example.test", u), RequestedAt: at, CompletedAt: at, Transport: "subscription", Status: status, Coverage: coverage, ResponseComplete: status == 200}, nodes)
		if err != nil {
			t.Fatal(err)
		}
	}
	// Same user+two nodes+many pulls remains one person. Another rule/port also qualifies.
	write("repeat1", ids[0], now-50, 200, "complete", node, node)
	write("repeat2", ids[0], now-40, 200, "complete", node)
	other := node
	other.PolicyID = 1000
	other.Port = "443"
	write("other-rule", ids[1], now-30, 200, "complete", other)
	write("partial", ids[2], now-30, 200, "partial", node)
	write("blocked", ids[3], now-30, 403, "complete", node)
	wrong := node
	wrong.DeliveredHost = "192.0.2.148"
	write("wrong", ids[4], now-30, 200, "complete", wrong)
	write("deleted", ids[5], now-30, 200, "complete", node)
	exec(`DELETE FROM v2_user WHERE id=$1`, ids[5])
	req := ClientEntryUserPolicySplitPreviewRequest{HistoryHost: host, HistoryFrom: now - 100, HistoryTo: now}
	preview, err := s.PreviewClientEntryUserPolicySplit(ctx, req)
	if err != nil || preview.UserCount != 2 || preview.SnapshotHash == "" {
		t.Fatalf("preview %+v %v", preview, err)
	}
	hosts, err := store.Hosts(ctx, entryevidence.Filter{Host: host})
	if err != nil || hosts.Total != 1 || hosts.Items[0].Users != 3 {
		t.Fatal("host union", hosts, err)
	}
	users, err := store.Users(ctx, entryevidence.Filter{Host: host})
	if err != nil || users.Total != 3 {
		t.Fatal("deleted historical user lost", users, err)
	}
	create := ClientEntryUserPolicySplitCreateRequest{Name: "historical " + marker, HistoryHost: host, HistoryFrom: preview.From, HistoryTo: preview.To, SnapshotHash: preview.SnapshotHash, Members: []ClientEntryGroupMemberSaveRequest{{ServerType: "vmess", ServerID: server}}, EntryHostA: "192.0.2.150", EntryHostB: "192.0.2.151"}
	invalid := create
	invalid.SnapshotHash = "stale"
	if _, err = s.CreateClientEntryUserPolicySplit(ctx, invalid); !errors.Is(err, ErrEntryEvidenceConflict) {
		t.Fatal("stale snapshot accepted", err)
	}
	// A later pull is outside the frozen interval and must not drift into the group.
	write("after-preview", ids[4], now+2, 200, "complete", node)
	record, err := s.CreateClientEntryUserPolicySplit(ctx, create)
	if err != nil {
		t.Fatal(err)
	}
	policies = append(policies, record.ID)
	if record.SnapshotUserCount != 2 || len(record.SplitGroups) != 2 || record.SplitGroups[0].UserCount != 1 || record.SplitGroups[1].UserCount != 1 {
		t.Fatal("not balanced", record)
	}
	rows, err := db.Query(`SELECT user_id FROM v2_client_entry_user_policy_split_assignment WHERE policy_id=$1 ORDER BY user_id`, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	actual := []int64{}
	for rows.Next() {
		var u int64
		rows.Scan(&u)
		actual = append(actual, u)
	}
	rows.Close()
	if !reflect.DeepEqual(actual, ids[:2]) {
		t.Fatal("wrong historical membership", actual, ids[:2])
	}
	runtime := usersvc.NewDBService(config.Config{}, db)
	for i := 0; i < 3; i++ {
		servers, err := runtime.Servers(ctx, ids[i], "Forest")
		expected := "original.example.test"
		if i == 0 {
			expected = "192.0.2.150"
		}
		if i == 1 {
			expected = "192.0.2.151"
		}
		if err != nil || len(servers) != 1 || fmt.Sprint(servers[0]["host"]) != expected {
			t.Fatalf("runtime %d: %+v %v", i, servers, err)
		}
	}

	// A single fixed group needs only one entry and can be split later.
	single := create
	single.Name = "single " + marker
	single.HistorySingleGroup = true
	single.EntryHostA = host
	single.EntryHostB = ""
	singleRecord, err := s.CreateClientEntryUserPolicySplit(ctx, single)
	if err != nil {
		t.Fatal("single creation", err)
	}
	policies = append(policies, singleRecord.ID)
	if len(singleRecord.SplitGroups) != 1 || singleRecord.SplitGroups[0].UserCount != 2 || singleRecord.SnapshotUserCount != 2 {
		t.Fatal("single group fragmented", singleRecord)
	}
	exec(`UPDATE v2_client_entry_user_policy SET enabled=0 WHERE id=$1`, record.ID)
	for _, uid := range ids[:2] {
		servers, e := runtime.Servers(ctx, uid, "Forest")
		if e != nil || len(servers) != 1 || fmt.Sprint(servers[0]["host"]) != host {
			t.Fatal("single runtime", servers, e)
		}
	}
	continued, e := s.SplitClientEntryUserPolicyGroup(ctx, ClientEntryUserPolicyGroupSplitRequest{PolicyID: singleRecord.ID, GroupID: singleRecord.SplitGroups[0].ID, EntryHostA: "192.0.2.152", EntryHostB: "192.0.2.153"})
	if e != nil {
		t.Fatal("single group cannot split later", e)
	}
	if continued.ID != singleRecord.ID {
		t.Fatal("later split changed policy", continued)
	}
	var leaves int
	if err = db.QueryRow(`SELECT COUNT(DISTINCT group_id) FROM v2_client_entry_user_policy_split_assignment WHERE policy_id=$1`, singleRecord.ID).Scan(&leaves); err != nil || leaves != 2 {
		t.Fatal("expected two assignment leaves", leaves, err)
	}
	for i, uid := range ids[:2] {
		servers, e := runtime.Servers(ctx, uid, "Forest")
		expected := []string{"192.0.2.152", "192.0.2.153"}[i]
		if e != nil || len(servers) != 1 || fmt.Sprint(servers[0]["host"]) != expected {
			t.Fatal("later binary runtime", servers, e)
		}
	}
	// Exact second boundaries beneath the minute UI still use [from,to).
	onePreview, e := s.PreviewClientEntryUserPolicySplit(ctx, ClientEntryUserPolicySplitPreviewRequest{HistoryHost: host, HistoryFrom: now - 45, HistoryTo: now - 35})
	if e != nil || onePreview.UserCount != 1 {
		t.Fatal("precise interval", onePreview, e)
	}
	one := single
	one.Name = "one " + marker
	one.HistoryFrom = onePreview.From
	one.HistoryTo = onePreview.To
	one.SnapshotHash = onePreview.SnapshotHash
	oneRecord, e := s.CreateClientEntryUserPolicySplit(ctx, one)
	if e != nil {
		t.Fatal("one user single group rejected", e)
	}
	policies = append(policies, oneRecord.ID)
	if len(oneRecord.SplitGroups) != 1 || oneRecord.SnapshotUserCount != 1 {
		t.Fatal("one user lost", oneRecord)
	}
	illegal := one
	illegal.HistoryHost = ""
	if _, e = s.CreateClientEntryUserPolicySplit(ctx, illegal); !errors.Is(e, ErrEntryEvidenceInvalid) {
		t.Fatal("single mode allowed on recent-user binary", e)
	}
	// Deleting evidence does not delete the independent fixed assignment.
	exec(`DELETE FROM v2_entry_delivery_request WHERE id LIKE $1`, marker+"%")
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM v2_client_entry_user_policy_split_assignment WHERE policy_id=$1`, record.ID).Scan(&count)
	if count != 2 {
		t.Fatal("evidence cleanup changed frozen group")
	}
}
