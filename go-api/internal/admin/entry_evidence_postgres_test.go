package admin

import (
	"context"
	"database/sql"

	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"forest/go-api/internal/config"
	usersvc "forest/go-api/internal/user"
)

func TestEntryEvidenceFrozenSubsetPostgres(t *testing.T) {
	dsn := os.Getenv("FOREST_ENTRY_EVIDENCE_TEST_DSN")
	if dsn == "" {
		t.Skip("isolated FOREST_ENTRY_EVIDENCE_TEST_DSN required")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	s := NewDBService(config.Config{}, db)
	mustID := func(q string, args ...any) int64 {
		t.Helper()
		var id int64
		if err := db.QueryRowContext(ctx, q, args...).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	marker := fmt.Sprint(time.Now().UnixNano())
	now := time.Now().Unix()
	actor := mustID(`INSERT INTO v2_user(email,password,uuid,token,is_admin,is_staff,banned,created_at,updated_at) VALUES($1,'x','x',$2,1,0,0,1,1) RETURNING id`, "ev-admin-"+marker+"@example.test", "admin"+marker)
	staff := mustID(`INSERT INTO v2_user(email,password,uuid,token,is_admin,is_staff,banned,created_at,updated_at) VALUES($1,'x','x',$2,0,1,0,1,1) RETURNING id`, "ev-staff-"+marker+"@example.test", "staff"+marker)
	if err = s.prepareEntryEvidence(ctx, actor, false); err != nil {
		t.Fatal(err)
	}
	users := []int64{}
	for i := 0; i < 6; i++ {
		users = append(users, mustID(`INSERT INTO v2_user(email,password,uuid,token,is_admin,is_staff,banned,created_at,updated_at) VALUES($1,'x','x',$2,0,0,0,1,1) RETURNING id`, fmt.Sprintf("ev-%d-%s@example.test", i, marker), fmt.Sprintf("u%d%s", i, marker)))
	}
	group := mustID(`INSERT INTO v2_server_group(name,created_at,updated_at)VALUES($1,1,1) RETURNING id`, "evidence"+marker)
	server := mustID(`INSERT INTO v2_server_vmess(group_id,name,host,port,server_port,rate,network,"show",created_at,updated_at)VALUES($1,'evidence','node.example.test','54101',54101,'1','tcp',1,1,1) RETURNING id`, fmt.Sprintf("[%d]", group))
	for _, id := range users {
		exec(`UPDATE v2_user SET group_id=$2,transfer_enable=1073741824,expired_at=NULL WHERE id=$1`, id, group)
	}
	u := usersvc.NewDBService(config.Config{}, db)
	assertRoute := func(id int64, ua, host string) {
		t.Helper()
		servers, err := u.Servers(ctx, id, ua)
		if err != nil || len(servers) != 1 || fmt.Sprint(servers[0]["host"]) != host {
			t.Fatalf("runtime user %d ua %s wanted %s got %+v / %v", id, ua, host, servers, err)
		}
	}
	conditions := fmt.Sprintf(`[{"field":"user_id","operator":"between","min":%d,"max":%d},{"field":"ua","operator":"contains_any","values":["Forest"]}]`, users[0], users[5])
	policy := mustID(`INSERT INTO v2_client_entry_user_policy(name,sort,action,conditions,entry_host,enabled,created_at,updated_at)VALUES($1,100,'override',$2,'old.example.test',1,1,1)RETURNING id`, "ev-source"+marker, conditions)
	exec(`INSERT INTO v2_client_entry_user_policy_member(policy_id,server_type,server_id,sort,created_at,updated_at)VALUES($1,'vmess',$2,10,1,1)`, policy, server)
	requests := []string{}
	policies := []int64{policy}
	cases := []int64{}
	poolIDs := []int64{}
	probe := mustID(`INSERT INTO v2_dns_probe(name,token_hash,enabled,last_heartbeat_at,created_at,updated_at) VALUES($1,$2,1,$3,$3,$3) RETURNING id`, "evidence"+marker, fmt.Sprintf("%064s", marker), now)
	monitor := mustID(`INSERT INTO v2_client_entry_monitor(policy_id,enabled,check_interval_sec,tcp_timeout_ms,failure_threshold,success_threshold,created_at,updated_at) VALUES($1,1,45,4000,4,3,1,1) RETURNING id`, policy)
	exec(`INSERT INTO v2_client_entry_monitor_probe(monitor_id,probe_id,created_at) VALUES($1,$2,1)`, monitor, probe)
	monitorTarget := mustID(`INSERT INTO v2_client_entry_monitor_target(monitor_id,source_key,name,host,port,created_at,updated_at) VALUES($1,$2,'source','old.example.test',54101,1,1) RETURNING id`, monitor, fmt.Sprintf("policy:%d:entry", policy))
	exec(`INSERT INTO v2_client_entry_monitor_state(target_id,probe_id,last_success,last_error,updated_at) VALUES($1,$2,0,'secret-debug-content',1)`, monitorTarget, probe)
	exec(`INSERT INTO v2_client_entry_monitor_event(monitor_id,target_id,probe_id,event_type,details,created_at) VALUES($1,$2,$3,'secret-event-kind','secret-debug-content',1)`, monitor, monitorTarget, probe)
	defer func() {
		for _, id := range cases {
			db.Exec(`DELETE FROM v2_entry_evidence_case WHERE id=$1`, id)
		}
		for _, id := range requests {
			db.Exec(`DELETE FROM v2_entry_delivery_request WHERE id=$1`, id)
		}
		for _, id := range policies {
			db.Exec(`DELETE FROM v2_client_entry_user_policy_member WHERE policy_id=$1`, id)
			db.Exec(`DELETE FROM v2_client_entry_user_policy WHERE id=$1`, id)
		}
		for _, id := range poolIDs {
			db.Exec(`DELETE FROM v2_client_entry_backup_ip WHERE id=$1`, id)
		}
		db.Exec(`DELETE FROM v2_dns_probe WHERE id=$1`, probe)
		db.Exec(`DELETE FROM v2_server_vmess WHERE id=$1`, server)
		db.Exec(`DELETE FROM v2_server_group WHERE id=$1`, group)
		for _, id := range append(users, actor, staff) {
			db.Exec(`DELETE FROM v2_user WHERE id=$1`, id)
		}
	}()
	delivery := func(user, pol, leaf int64, host string, at int64, complete bool) {
		t.Helper()
		id := fmt.Sprintf("ev-%s-%d", marker, len(requests))
		requests = append(requests, id)
		exec(`INSERT INTO v2_entry_delivery_request(id,user_id,email_at_event,ua,requested_at,completed_at,transport,status,response_complete,coverage,node_count) VALUES($1,$2,$3,'Forest/1',$4,$4,'clash',200,$5,'complete',1)`, id, user, fmt.Sprintf("user%d@example.test", user), at, complete)
		exec(`INSERT INTO v2_entry_delivery_node(request_id,ordinal,server_type,server_id,policy_id,group_id,rule_revision,source_host,delivered_host,port,source)VALUES($1,0,'vmess',$2,$3,$4,'historical-revision','old.example.test',$5,'54101','policy')`, id, server, pol, leaf, host)
	}
	for _, id := range users[:4] {
		delivery(id, policy, 0, "old.example.test", now-10, true)
	}
	delivery(users[4], policy, 0, "old.example.test", now-10, false)
	delivery(users[5], policy, 0, "old.example.test", now, true)
	q := EntryEvidenceQueryRequest{Kind: "policy", TargetID: policy, Host: "old.example.test", Port: 54101, From: now - 60, To: now, PageSize: 100}
	preview, err := s.PreviewEntryEvidenceCandidates(ctx, staff, q)
	if err != nil || !preview.Supported || preview.Total != 4 {
		t.Fatal("preview", preview, err)
	}
	if len(preview.Items[0].Evidence) != 1 || preview.Items[0].Evidence[0].RuleRevision != "historical-revision" {
		t.Fatal("minimal source evidence missing", preview.Items[0])
	}
	for _, check := range []func() error{
		func() error { _, e := s.PreviewEntryEvidenceCandidates(ctx, users[0], q); return e },
		func() error {
			_, e := s.CreateEntryEvidenceCase(ctx, users[0], CreateEntryEvidenceCaseRequest{EntryEvidenceQueryRequest: q})
			return e
		},
		func() error { _, e := s.GetEntryEvidenceCase(ctx, users[0], 1); return e },
		func() error {
			_, e := s.ExecuteEntryEvidenceSplit(ctx, users[0], EntryEvidenceSplitRequest{CaseID: 1})
			return e
		},
		func() error { return s.DeleteEntryEvidenceCase(ctx, users[0], 1) },
	} {
		if e := check(); !errors.Is(e, ErrEntryEvidenceForbidden) {
			t.Fatal("ordinary user allowed", e)
		}
	}
	exec(`UPDATE v2_user SET banned=1 WHERE id=$1`, staff)
	if _, e := s.PreviewEntryEvidenceCandidates(ctx, staff, q); !errors.Is(e, ErrEntryEvidenceForbidden) {
		t.Fatal("banned staff allowed", e)
	}
	exec(`UPDATE v2_user SET banned=0 WHERE id=$1`, staff)
	// A final one-user hypothesis remains archivable but must never create an empty B branch.
	singleQ := q
	singleQ.Account = fmt.Sprint(users[0])
	singlePreview, err := s.PreviewEntryEvidenceCandidates(ctx, staff, singleQ)
	if err != nil || singlePreview.Supported || !singlePreview.FreezeAllowed || singlePreview.Total != 1 {
		t.Fatal("single preview", singlePreview, err)
	}
	single, err := s.CreateEntryEvidenceCase(ctx, staff, CreateEntryEvidenceCaseRequest{EntryEvidenceQueryRequest: singleQ, Revision: singlePreview.Target.Revision, ExpectedCandidatesHash: singlePreview.CandidatesHash})
	if err != nil || single.SelectedCount != 1 || single.Preview.Supported {
		t.Fatal("single archival", single, err)
	}
	cases = append(cases, single.ID)
	singleSplit := EntryEvidenceSplitRequest{CaseID: single.ID, Revision: single.Revision, EntryHostA: "single-a.test", EntryHostB: "single-b.test"}
	if p, e := s.PreviewEntryEvidenceSplit(ctx, staff, singleSplit); e != nil || p.Supported {
		t.Fatal("single split preview", p, e)
	}
	if _, e := s.ExecuteEntryEvidenceSplit(ctx, staff, singleSplit); !errors.Is(e, ErrEntryEvidenceInvalid) {
		t.Fatal("single split allowed", e)
	}
	if _, e := s.UpdateEntryEvidenceObservation(ctx, staff, EntryEvidenceObservationRequest{CaseID: single.ID, Revision: single.Revision, Observation: "最终单人证据仍可记录观察"}); e != nil {
		t.Fatal(e)
	}
	// A new completed request after preview must not silently alter frozen evidence.
	delivery(users[4], policy, 0, "old.example.test", now-9, true)
	if _, err = s.CreateEntryEvidenceCase(ctx, staff, CreateEntryEvidenceCaseRequest{EntryEvidenceQueryRequest: q, Revision: preview.Target.Revision, ExpectedCandidatesHash: preview.CandidatesHash}); !errors.Is(err, ErrEntryEvidenceConflict) {
		t.Fatal("preview hash drift accepted", err)
	}
	exec(`DELETE FROM v2_entry_delivery_request WHERE id=$1`, requests[len(requests)-1])
	c, err := s.CreateEntryEvidenceCase(ctx, staff, CreateEntryEvidenceCaseRequest{EntryEvidenceQueryRequest: q, Revision: preview.Target.Revision, ExpectedCandidatesHash: preview.CandidatesHash, SelectedUserIDs: []int64{users[0], users[2]}})
	if err != nil {
		t.Fatal(err)
	}
	cases = append(cases, c.ID)
	if strings.Contains(string(c.ProbeSnapshot), "secret-") {
		t.Fatal("free-form probe data retained", string(c.ProbeSnapshot))
	}
	if c.SelectedCount != 2 || c.ResidualCount != 4 || c.SourceUserCount != 6 || c.Preview.Mode != "overlay" {
		t.Fatal("freeze", c)
	}
	if err = s.DeleteEntryEvidenceCase(ctx, staff, c.ID); !errors.Is(err, ErrEntryEvidenceForbidden) {
		t.Fatal("staff deletion allowed", err)
	}
	// Filtering by port must occur before LIMIT; wrong-port healthy entries have
	// better sort order and must not starve the valid pair.
	for index, port := range []int64{443, 443, 54101, 54101} {
		poolID := mustID(`INSERT INTO v2_client_entry_backup_ip(name,ip,port,sort,created_at,updated_at) VALUES($1,$2,$3,$4,1,1) RETURNING id`, "evidence"+marker, fmt.Sprintf("198.18.0.%d", index+10), port, index+1)
		poolIDs = append(poolIDs, poolID)
		exec(`INSERT INTO v2_client_entry_backup_ip_state(backup_ip_id,probe_id,last_success,consecutive_success,last_reported_at,updated_at) VALUES($1,$2,1,5,$3,$3)`, poolID, probe, now)
	}
	claimTx, e := db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer claimTx.Rollback()
	claimed, e := claimHealthyEntryEvidenceBackupIPs(ctx, claimTx, 2, 54101, now)
	if e != nil || len(claimed) != 2 || claimed[0].ID != poolIDs[2] || claimed[1].ID != poolIDs[3] {
		t.Fatal("exact-port claim", claimed, e)
	}
	secondClaim, e := db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer secondClaim.Rollback()
	if _, e = claimHealthyEntryEvidenceBackupIPs(ctx, secondClaim, 2, 54101, now); !errors.Is(e, ErrClientEntryBackupIPInsufficient) {
		t.Fatal("pool double claim allowed", e)
	}
	secondClaim.Rollback()
	claimTx.Rollback()
	exec(`UPDATE v2_client_entry_backup_ip SET enabled=0 WHERE id=$1`, poolIDs[3])
	if _, e = s.ExecuteEntryEvidenceSplit(ctx, actor, EntryEvidenceSplitRequest{CaseID: c.ID, Revision: c.Revision, UseBackupIPPool: true}); !errors.Is(e, ErrClientEntryBackupIPInsufficient) {
		t.Fatal("pool shortage not rejected", e)
	}
	unchanged, e := s.GetEntryEvidenceCase(ctx, actor, c.ID)
	if e != nil || unchanged.Status != "frozen" || unchanged.Revision != c.Revision {
		t.Fatal("pool shortage mutated archive", unchanged, e)
	}
	assertRoute(users[0], "Forest/1", "old.example.test")
	exec(`UPDATE v2_client_entry_backup_ip SET enabled=1 WHERE id=$1`, poolIDs[3])
	req := EntryEvidenceSplitRequest{CaseID: c.ID, Revision: c.Revision, EntryHostA: "a.example.test", EntryHostB: "b.example.test"}
	splitPreview, err := s.PreviewEntryEvidenceSplit(ctx, actor, req)
	if err != nil || !splitPreview.Supported {
		t.Fatal("split preview", splitPreview, err)
	}
	// Rule and higher-priority routing changes both block execution.
	exec(`UPDATE v2_client_entry_user_policy SET remarks='concurrent change' WHERE id=$1`, policy)
	if _, err = s.ExecuteEntryEvidenceSplit(ctx, actor, req); !errors.Is(err, ErrEntryEvidenceConflict) {
		t.Fatal("rule change executed", err)
	}
	exec(`UPDATE v2_client_entry_user_policy SET remarks='' WHERE id=$1`, policy)
	higher := mustID(`INSERT INTO v2_client_entry_user_policy(name,sort,action,conditions,entry_host,enabled,created_at,updated_at)VALUES($1,50,'override','[]','higher.example.test',1,1,1)RETURNING id`, "ev-higher"+marker)
	policies = append(policies, higher)
	exec(`INSERT INTO v2_client_entry_user_policy_member(policy_id,server_type,server_id,sort,created_at,updated_at)VALUES($1,'vmess',$2,10,1,1)`, higher, server)
	if _, err = s.ExecuteEntryEvidenceSplit(ctx, actor, req); !errors.Is(err, ErrEntryEvidenceConflict) {
		t.Fatal("higher-priority takeover executed", err)
	}
	exec(`DELETE FROM v2_client_entry_user_policy_member WHERE policy_id=$1`, higher)
	exec(`DELETE FROM v2_client_entry_user_policy WHERE id=$1`, higher)
	// Removing the raw records must not erase or replace the frozen membership.
	for _, id := range requests {
		exec(`DELETE FROM v2_entry_delivery_request WHERE id=$1`, id)
	}
	type outcome struct {
		value EntryEvidenceCase
		err   error
	}
	ch := make(chan outcome, 2)
	for i := 0; i < 2; i++ {
		go func() { v, e := s.ExecuteEntryEvidenceSplit(ctx, actor, req); ch <- outcome{v, e} }()
	}
	first, second := <-ch, <-ch
	done, err := first.value, first.err
	if second.err != nil || done.Execution == nil || second.value.Execution == nil || done.Execution.GroupAID != second.value.Execution.GroupAID {
		t.Fatal("concurrent duplicate split", first, second)
	}
	if err != nil {
		t.Fatal("execute overlay", err)
	}
	policies = append(policies, done.Execution.PolicyID)
	if done.Execution.MonitoringStatus != "inherited" || done.Execution.MonitoringWarning != "" {
		t.Fatal("monitor not inherited", done.Execution)
	}
	var inheritedID, inheritedInterval, inheritedProbes, inheritedTargets, automatic int64
	if e := db.QueryRow(`SELECT id,check_interval_sec FROM v2_client_entry_monitor WHERE policy_id=$1`, done.Execution.PolicyID).Scan(&inheritedID, &inheritedInterval); e != nil || inheritedInterval != 45 {
		t.Fatal("monitor settings", inheritedID, inheritedInterval, e)
	}
	db.QueryRow(`SELECT COUNT(*) FROM v2_client_entry_monitor_probe WHERE monitor_id=$1 AND probe_id=$2`, inheritedID, probe).Scan(&inheritedProbes)
	db.QueryRow(`SELECT COUNT(*),COALESCE(SUM(auto_split_enabled),0) FROM v2_client_entry_monitor_target WHERE monitor_id=$1 AND port=54101`, inheritedID).Scan(&inheritedTargets, &automatic)
	if inheritedProbes != 1 || inheritedTargets != 2 || automatic != 0 {
		t.Fatal("monitor bindings or automatic flag", inheritedProbes, inheritedTargets, automatic)
	}
	// A worker-held old monitor must never deadlock topology-owning evidence execution.
	blocker, e := db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer blocker.Rollback()
	if _, e = blocker.Exec(`SELECT id FROM v2_client_entry_monitor WHERE id=$1 FOR UPDATE`, monitor); e != nil {
		t.Fatal(e)
	}
	inheritTx, e := db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer inheritTx.Rollback()
	if e = lockClientEntryVisibleOrder(ctx, inheritTx); e != nil {
		t.Fatal(e)
	}
	started := time.Now()
	status, warning, e := inheritEntryEvidenceMonitor(ctx, inheritTx, c, *done.Execution, now)
	if e != nil || status != "deferred" || warning == "" || time.Since(started) > time.Second {
		t.Fatal("monitor contention fallback", status, warning, e, time.Since(started))
	}
	inheritTx.Rollback()
	blocker.Rollback()
	// Probe observations are address-stamped event evidence, not the target's
	// present address and not arbitrary details/error text.
	var observedTarget int64
	if e = db.QueryRow(`SELECT id FROM v2_client_entry_monitor_target WHERE monitor_id=$1 AND source_key=$2`, inheritedID, fmt.Sprintf("policy:%d:split-group:%d", done.Execution.PolicyID, done.Execution.GroupAID)).Scan(&observedTarget); e != nil {
		t.Fatal(e)
	}
	addProbeEvent := func(kind, details string, at int64) {
		t.Helper()
		exec(`INSERT INTO v2_client_entry_monitor_event(monitor_id,target_id,probe_id,event_type,details,created_at) VALUES($1,$2,$3,$4,$5,$6)`, inheritedID, observedTarget, probe, kind, details, at)
	}
	validDetails := `{"host":"a.example.test","port":54101,"resolved_ip":"192.0.2.44","error":"secret-debug-content","password":"secret-debug-content"}`
	addProbeEvent("down", validDetails, done.Execution.ExecutedAt)
	addProbeEvent("recovered", `{"host":"a.example.test","port":54101,"resolved_ip":"secret-debug-content"}`, done.Execution.ExecutedAt)
	addProbeEvent("down", validDetails, done.Execution.ExecutedAt-1)
	addProbeEvent("down", `{"host":"other.example.test","port":54101}`, done.Execution.ExecutedAt)
	addProbeEvent("down", `{"host":"a.example.test","port":443}`, done.Execution.ExecutedAt)
	addProbeEvent("down", `{"host":"a.example.test","port":"54101"}`, done.Execution.ExecutedAt)
	addProbeEvent("secret-event-kind", validDetails, done.Execution.ExecutedAt)
	// Even if a monitor target is later edited, old correctly stamped events
	// remain attributed to their original round, never to the new host.
	exec(`UPDATE v2_client_entry_monitor_target SET host='later-edited.test' WHERE id=$1`, observedTarget)
	refreshProbe := func() {
		t.Helper()
		tx, e := db.BeginTx(ctx, nil)
		if e != nil {
			t.Fatal(e)
		}
		defer tx.Rollback()
		if e = refreshEntryEvidenceProbeEvents(ctx, tx, &done); e != nil {
			t.Fatal(e)
		}
		if e = tx.Commit(); e != nil {
			t.Fatal(e)
		}
	}
	refreshProbe()
	if done.ProbeEventsTotal != 2 || len(done.ProbeEvents) != 2 || done.ProbeEventsGap || done.ProbeEventsTruncated {
		t.Fatal("probe filters", done.ProbeEventsTotal, done.ProbeEvents, done.ProbeEventsGap)
	}
	for _, event := range done.ProbeEvents {
		if event.Branch != "A" || event.Host != "a.example.test" || event.Port != 54101 || strings.Contains(event.ResolvedIP, "secret") {
			t.Fatal("unsafe probe evidence", event)
		}
	}
	var savedProbeJSON string
	if e = db.QueryRow(`SELECT jsonb_agg(to_jsonb(e))::TEXT FROM v2_entry_evidence_probe_event e WHERE case_id=$1`, done.ID).Scan(&savedProbeJSON); e != nil || strings.Contains(savedProbeJSON, "secret") {
		t.Fatal("secret persisted", savedProbeJSON, e)
	}
	for i := 0; i < 100; i++ {
		addProbeEvent("down", validDetails, done.Execution.ExecutedAt)
	}
	refreshProbe()
	if done.ProbeEventsTotal != 102 || len(done.ProbeEvents) != 100 || !done.ProbeEventsTruncated {
		t.Fatal("probe bounded view", done.ProbeEventsTotal, len(done.ProbeEvents), done.ProbeEventsTruncated)
	}
	exec(`DELETE FROM v2_client_entry_monitor_event WHERE monitor_id=$1`, inheritedID)
	exec(`UPDATE v2_entry_evidence_probe_event_state SET last_refreshed_at=$2 WHERE case_id=$1`, done.ID, time.Now().Unix()-90000)
	refreshProbe()
	if done.ProbeEventsTotal != 102 || len(done.ProbeEvents) != 100 || !done.ProbeEventsGap {
		t.Fatal("probe retention or gap", done.ProbeEventsTotal, len(done.ProbeEvents), done.ProbeEventsGap)
	}
	exec(`UPDATE v2_client_entry_monitor_target SET host='a.example.test' WHERE id=$1`, observedTarget)
	assertRoute(users[0], "Forest/1", "a.example.test")
	assertRoute(users[2], "Forest/1", "b.example.test")
	for _, id := range []int64{users[1], users[3], users[4], users[5]} {
		assertRoute(id, "Forest/1", "old.example.test")
	}
	for _, id := range users {
		assertRoute(id, "Clash", "node.example.test")
	}
	different := req
	different.EntryHostB = "different.example.test"
	if _, err = s.ExecuteEntryEvidenceSplit(ctx, actor, different); !errors.Is(err, ErrEntryEvidenceConflict) {
		t.Fatal("repeat changed destinations", err)
	}
	again, err := s.ExecuteEntryEvidenceSplit(ctx, actor, req)
	if err != nil || again.Execution.GroupAID != done.Execution.GroupAID {
		t.Fatal("idempotency", again, err)
	}
	var mode, host, raw string
	var sort int64
	if err = db.QueryRow(`SELECT mode,entry_host,conditions,sort FROM v2_client_entry_user_policy WHERE id=$1`, policy).Scan(&mode, &host, &raw, &sort); err != nil || mode != "standard" || host != "old.example.test" || raw != conditions || sort != 120 {
		t.Fatal("source rule changed", mode, host, sort, err)
	}
	var count int64
	db.QueryRow(`SELECT COUNT(*) FROM v2_client_entry_user_policy_split_assignment WHERE policy_id=$1`, done.Execution.PolicyID).Scan(&count)
	if count != 2 {
		t.Fatal("overlay swept in unselected users", count)
	}
	// Subsequent leaf subset splits create a real R child, preserving every assignment.
	leafPolicy := mustID(`INSERT INTO v2_client_entry_user_policy(name,sort,mode,action,conditions,entry_host,enabled,created_at,updated_at)VALUES($1,1000,'split','override',$2,'',1,1,1)RETURNING id`, "ev-leaf"+marker, conditions)
	policies = append(policies, leafPolicy)
	exec(`INSERT INTO v2_client_entry_user_policy_member(policy_id,server_type,server_id,sort,created_at,updated_at)VALUES($1,'vmess',$2,10,1,1)`, leafPolicy, server)
	// Disable earlier policies, so historical UA really routes to this leaf now.
	exec(`UPDATE v2_client_entry_user_policy SET enabled=0 WHERE id IN($1,$2)`, policy, done.Execution.PolicyID)
	leaf := mustID(`INSERT INTO v2_client_entry_user_policy_split_group(policy_id,name,path,entry_host,sort,global_sort,created_at,updated_at)VALUES($1,'L','L','leaf.example.test',10,1000,1,1)RETURNING id`, leafPolicy)
	for _, id := range users {
		exec(`INSERT INTO v2_client_entry_user_policy_split_assignment(policy_id,user_id,group_id,created_at,updated_at)VALUES($1,$2,$3,1,1)`, leafPolicy, id, leaf)
		delivery(id, leafPolicy, leaf, "leaf.example.test", now-5, true)
	}
	q.Kind = "split_group"
	q.TargetID = leaf
	q.Host = "leaf.example.test"
	preview, err = s.PreviewEntryEvidenceCandidates(ctx, actor, q)
	if err != nil || !preview.Supported {
		t.Fatal("leaf preview", preview, err)
	}
	leafCase, err := s.CreateEntryEvidenceCase(ctx, actor, CreateEntryEvidenceCaseRequest{EntryEvidenceQueryRequest: q, Revision: preview.Target.Revision, ExpectedCandidatesHash: preview.CandidatesHash, SelectedUserIDs: users[:3]})
	if err != nil {
		t.Fatal(err)
	}
	cases = append(cases, leafCase.ID)
	// Current fixed membership changes must invalidate the frozen snapshot.
	exec(`DELETE FROM v2_client_entry_user_policy_split_assignment WHERE policy_id=$1 AND user_id=$2`, leafPolicy, users[5])
	if _, err = s.ExecuteEntryEvidenceSplit(ctx, actor, EntryEvidenceSplitRequest{CaseID: leafCase.ID, Revision: leafCase.Revision, EntryHostA: "la.example.test", EntryHostB: "lb.example.test"}); !errors.Is(err, ErrEntryEvidenceConflict) {
		t.Fatal("changed source membership executed", err)
	}
	exec(`INSERT INTO v2_client_entry_user_policy_split_assignment(policy_id,user_id,group_id,created_at,updated_at)VALUES($1,$2,$3,1,1)`, leafPolicy, users[5], leaf)
	leafDone, err := s.ExecuteEntryEvidenceSplit(ctx, actor, EntryEvidenceSplitRequest{CaseID: leafCase.ID, Revision: leafCase.Revision, EntryHostA: "la.example.test", EntryHostB: "lb.example.test"})
	if err != nil {
		t.Fatal("leaf execute", err)
	}
	if leafDone.Execution.GroupRID <= 0 {
		t.Fatal("no residual group")
	}
	db.QueryRow(`SELECT COUNT(*) FROM v2_client_entry_user_policy_split_assignment WHERE policy_id=$1`, leafPolicy).Scan(&count)
	if count != 6 {
		t.Fatal("lost unselected users", count)
	}
	db.QueryRow(`SELECT COUNT(*) FROM v2_client_entry_user_policy_split_assignment WHERE policy_id=$1 AND group_id=$2`, leafPolicy, leafDone.Execution.GroupRID).Scan(&count)
	if count != 3 {
		t.Fatal("wrong residual count", count)
	}
	db.QueryRow(`SELECT entry_host FROM v2_client_entry_user_policy_split_group WHERE id=$1`, leafDone.Execution.GroupRID).Scan(&host)
	if host != "leaf.example.test" {
		t.Fatal("residual destination changed", host)
	}
	assertRoute(users[0], "Forest/1", "la.example.test")
	assertRoute(users[1], "Forest/1", "la.example.test")
	assertRoute(users[2], "Forest/1", "lb.example.test")
	for _, id := range users[3:] {
		assertRoute(id, "Forest/1", "leaf.example.test")
	}
	for _, id := range users {
		assertRoute(id, "Clash", "node.example.test")
	}
	// A later round must target a real child, not merely cite an unrelated archive.
	nextQ := q
	nextQ.TargetID = leafDone.Execution.GroupAID
	nextQ.Host = "la.example.test"
	for _, id := range users[:2] {
		delivery(id, leafPolicy, nextQ.TargetID, nextQ.Host, now-2, true)
	}
	nextPreview, err := s.PreviewEntryEvidenceCandidates(ctx, actor, nextQ)
	if err != nil || !nextPreview.Supported {
		t.Fatal("next round", nextPreview, err)
	}
	nextReq := CreateEntryEvidenceCaseRequest{EntryEvidenceQueryRequest: nextQ, Revision: nextPreview.Target.Revision, ExpectedCandidatesHash: nextPreview.CandidatesHash, ParentCaseID: c.ID}
	if _, err = s.CreateEntryEvidenceCase(ctx, actor, nextReq); !errors.Is(err, ErrEntryEvidenceInvalid) {
		t.Fatal("unrelated parent accepted", err)
	}
	nextReq.ParentCaseID = leafCase.ID
	child, err := s.CreateEntryEvidenceCase(ctx, actor, nextReq)
	if err != nil {
		t.Fatal("valid parent rejected", err)
	}
	cases = append(cases, child.ID)
	// Evidence rows remain self-contained after log deletion.
	page, err := s.ListEntryEvidenceCandidates(ctx, staff, EntryEvidenceCandidatesRequest{CaseID: c.ID, PageSize: 100})
	if err != nil || page.Total != 4 || len(page.Items[0].Evidence) == 0 {
		t.Fatal("frozen evidence lost", page, err)
	}
	obs, err := s.UpdateEntryEvidenceObservation(ctx, staff, EntryEvidenceObservationRequest{CaseID: c.ID, Revision: done.Revision, Observation: "仅观察，不推断用户责任", Conclusion: "待复测"})
	if err != nil || obs.Revision != done.Revision+1 {
		t.Fatal(err)
	}
	if _, err = s.UpdateEntryEvidenceObservation(ctx, staff, EntryEvidenceObservationRequest{CaseID: c.ID, Revision: done.Revision}); !errors.Is(err, ErrEntryEvidenceConflict) {
		t.Fatal("stale observation accepted", err)
	}
	if err = s.DeleteEntryEvidenceCase(ctx, actor, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.GetEntryEvidenceCase(ctx, actor, c.ID); !errors.Is(err, ErrEntryEvidenceNotFound) {
		t.Fatal(err)
	}
	var stillExists bool
	db.QueryRow(`SELECT EXISTS(SELECT 1 FROM v2_client_entry_user_policy WHERE id=$1)`, done.Execution.PolicyID).Scan(&stillExists)
	if !stillExists {
		t.Fatal("deleting evidence reverted routing")
	}
	// Cross the freeze JSON chunk boundary with a real end-to-end bulk overlay.
	// The standard source still owns unselected/future users; no original rule is
	// converted and every selected assignment is inserted by one INSERT SELECT.
	exec(`INSERT INTO v2_user(email,password,uuid,token,is_admin,is_staff,banned,created_at,updated_at)
 SELECT 'ev-bulk-'||$1||'-'||i||'@example.test','x','x','bulk-'||$1||'-'||i,0,0,0,1,1 FROM generate_series(1,501) i`, marker)
	bulkRows, err := db.Query(`SELECT id FROM v2_user WHERE email LIKE $1 ORDER BY id`, "ev-bulk-"+marker+"-%@example.test")
	if err != nil {
		t.Fatal(err)
	}
	bulkIDs := []int64{}
	for bulkRows.Next() {
		var id int64
		if err = bulkRows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		bulkIDs = append(bulkIDs, id)
	}
	err = bulkRows.Err()
	bulkRows.Close()
	if err != nil || len(bulkIDs) != 501 {
		t.Fatal("bulk users", len(bulkIDs), err)
	}
	users = append(users, bulkIDs...)
	bulkCondition := fmt.Sprintf(`[{"field":"user_id","operator":"between","min":%d,"max":%d}]`, bulkIDs[0], bulkIDs[len(bulkIDs)-1])
	bulkPolicy := mustID(`INSERT INTO v2_client_entry_user_policy(name,sort,action,conditions,entry_host,enabled,created_at,updated_at) VALUES($1,5000,'override',$2,'bulk-original.test',1,1,1) RETURNING id`, "bulk-"+marker, bulkCondition)
	policies = append(policies, bulkPolicy)
	exec(`INSERT INTO v2_client_entry_user_policy_member(policy_id,server_type,server_id,sort,created_at,updated_at) VALUES($1,'vmess',$2,10,1,1)`, bulkPolicy, server)
	exec(`UPDATE v2_user SET group_id=$2,transfer_enable=1073741824 WHERE id=ANY($1::BIGINT[])`, bulkIDs, group)
	exec(`INSERT INTO v2_entry_delivery_request(id,user_id,email_at_event,ua,requested_at,completed_at,transport,status,response_complete,coverage,node_count)
 SELECT 'bulk-ev-'||id::TEXT,id,email,'Forest/1',$2,$2,'clash',200,TRUE,'complete',1 FROM v2_user WHERE id=ANY($1::BIGINT[])`, bulkIDs, now-1)
	for _, id := range bulkIDs {
		requests = append(requests, fmt.Sprintf("bulk-ev-%d", id))
	}
	exec(`INSERT INTO v2_entry_delivery_node(request_id,ordinal,server_type,server_id,policy_id,group_id,rule_revision,source_host,delivered_host,port,source)
 SELECT 'bulk-ev-'||unnest($1::BIGINT[])::TEXT,0,'vmess',$2,$3,0,'bulk-revision','node.example.test','bulk-original.test','54101','policy'`, bulkIDs, server, bulkPolicy)
	bulkQ := EntryEvidenceQueryRequest{Kind: "policy", TargetID: bulkPolicy, Host: "bulk-original.test", Port: 54101, From: now - 10, To: now, PageSize: 100}
	bulkPreview, err := s.PreviewEntryEvidenceCandidates(ctx, actor, bulkQ)
	if err != nil || !bulkPreview.FreezeAllowed || bulkPreview.Total != 501 {
		t.Fatal("bulk preview", bulkPreview.Total, bulkPreview.UnsupportedReason, err)
	}
	bulkCase, err := s.CreateEntryEvidenceCase(ctx, actor, CreateEntryEvidenceCaseRequest{EntryEvidenceQueryRequest: bulkQ, Revision: bulkPreview.Target.Revision, ExpectedCandidatesHash: bulkPreview.CandidatesHash})
	if err != nil {
		t.Fatal("bulk freeze", err)
	}
	cases = append(cases, bulkCase.ID)
	lastPage, err := s.ListEntryEvidenceCandidates(ctx, staff, EntryEvidenceCandidatesRequest{CaseID: bulkCase.ID, Current: 6, PageSize: 100})
	if err != nil || lastPage.Total != 501 || len(lastPage.Items) != 1 || lastPage.Items[0].UserID != bulkIDs[500] {
		t.Fatal("bulk chunk boundary", lastPage, err)
	}
	bulkDone, err := s.ExecuteEntryEvidenceSplit(ctx, actor, EntryEvidenceSplitRequest{CaseID: bulkCase.ID, Revision: bulkCase.Revision, EntryHostA: "bulk-a.test", EntryHostB: "bulk-b.test"})
	if err != nil {
		t.Fatal("bulk split", err)
	}
	policies = append(policies, bulkDone.Execution.PolicyID)
	if bulkDone.Execution.MonitoringStatus != "unconfigured" || bulkDone.Execution.MonitoringWarning == "" {
		t.Fatal("unconfigured monitor must remain off", bulkDone.Execution)
	}
	db.QueryRow(`SELECT COUNT(*) FROM v2_client_entry_user_policy_split_assignment WHERE policy_id=$1`, bulkDone.Execution.PolicyID).Scan(&count)
	if count != 501 {
		t.Fatal("bulk assignment count", count)
	}
	assertRoute(bulkIDs[0], "Forest/1", "bulk-a.test")
	assertRoute(bulkIDs[250], "Forest/1", "bulk-a.test")
	assertRoute(bulkIDs[251], "Forest/1", "bulk-b.test")
	assertRoute(bulkIDs[500], "Forest/1", "bulk-b.test")
}
