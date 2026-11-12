package admin

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

	"forest/go-api/internal/config"
	platform "forest/go-api/internal/platform/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func TestEntryInvestigationPostgres(t *testing.T) {
	dsn := os.Getenv("FOREST_AIOPS_TEST_DSN")
	if dsn == "" {
		t.Skip("disposable PostgreSQL required")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	base := stdlib.OpenDB(*cfg)
	defer base.Close()
	schema := fmt.Sprintf("investigation_%d", time.Now().UnixNano())
	if _, err = base.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	defer base.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
	cfg.RuntimeParams["search_path"] = schema
	db := stdlib.OpenDB(*cfg)
	defer db.Close()
	ctx := context.Background()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatal(q, err)
		}
	}
	id := func(q string, args ...any) int64 {
		t.Helper()
		var n int64
		if err := db.QueryRow(q, args...).Scan(&n); err != nil {
			t.Fatal(q, err)
		}
		return n
	}
	for _, table := range []string{"v2_user", "v2_server_vmess", "v2_server_shadowsocks", "v2_server_trojan", "v2_server_hysteria", "v2_server_vless", "v2_server_anytls", "v2_server_tuic", "v2_server_v2node", "v2_server_group", "v2_support_entry_override"} {
		exec(`CREATE TABLE ` + table + ` (LIKE public.` + table + ` INCLUDING ALL)`)
	}
	s := NewDBService(config.Config{}, db)
	now := time.Now().Unix()
	actor := id(`INSERT INTO v2_user(email,password,uuid,token,is_admin,is_staff,banned,created_at,updated_at) VALUES('operator@example.test','x','operator','operator',1,0,0,1,1) RETURNING id`)
	if err = s.prepareEntryEvidence(ctx, actor, false); err != nil {
		t.Fatal(err)
	}
	if err = platform.EnsureEntryInvestigationSchema(ctx, db); err != nil {
		t.Fatal("schema idempotence", err)
	}
	users := []int64{}
	for i := 0; i < 8; i++ {
		users = append(users, id(`INSERT INTO v2_user(email,password,uuid,token,is_admin,is_staff,banned,created_at,updated_at) VALUES($1,'x',$2::text,$2::text,0,0,0,1,1) RETURNING id`, fmt.Sprintf("user%d@example.test", i), fmt.Sprintf("u%d", i)))
	}
	sg := id(`INSERT INTO v2_server_group(name,created_at,updated_at)VALUES('test',1,1) RETURNING id`)
	node := id(`INSERT INTO v2_server_vmess(group_id,name,host,port,server_port,rate,network,"show",created_at,updated_at)VALUES($1,'test','192.0.2.254','54101',54101,'1','tcp',1,1,1) RETURNING id`, fmt.Sprintf("[%d]", sg))
	for _, u := range users {
		exec(`UPDATE v2_user SET group_id=$2,transfer_enable=1073741824,expired_at=NULL WHERE id=$1`, u, sg)
	}
	cond := fmt.Sprintf(`[{"field":"user_id","operator":"between","min":%d,"max":%d}]`, users[0], users[7])
	policy := id(`INSERT INTO v2_client_entry_user_policy(name,sort,action,conditions,entry_host,enabled,created_at,updated_at)VALUES('source',100,'override',$1,'192.0.2.10',1,1,1) RETURNING id`, cond)
	exec(`INSERT INTO v2_client_entry_user_policy_member(policy_id,server_type,server_id,sort,created_at,updated_at)VALUES($1,'vmess',$2,10,1,1)`, policy, node)
	probe := id(`INSERT INTO v2_dns_probe(name,token_hash,enabled,last_heartbeat_at,created_at,updated_at) VALUES('test','testhash',1,$1,1,1) RETURNING id`, now)
	monitor := id(`INSERT INTO v2_client_entry_monitor(policy_id,enabled,check_interval_sec,tcp_timeout_ms,failure_threshold,success_threshold,created_at,updated_at) VALUES($1,1,30,3000,3,2,1,1) RETURNING id`, policy)
	exec(`INSERT INTO v2_client_entry_monitor_probe(monitor_id,probe_id,created_at) VALUES($1,$2,1)`, monitor, probe)
	sourceTarget := id(`INSERT INTO v2_client_entry_monitor_target(monitor_id,source_key,name,host,port,created_at,updated_at) VALUES($1,$2,'source','192.0.2.10',54101,1,1) RETURNING id`, monitor, fmt.Sprintf("policy:%d:entry", policy))
	exec(`INSERT INTO v2_client_entry_monitor_state(target_id,probe_id,last_success,consecutive_failure,last_reported_at,updated_at) VALUES($1,$2,0,4,$3,$3)`, sourceTarget, probe, now)
	seq := 0
	delivery := func(user, pol, group int64, host string, at int64, complete bool) {
		t.Helper()
		seq++
		rid := fmt.Sprintf("inv-%d", seq)
		exec(`INSERT INTO v2_entry_delivery_request(id,user_id,email_at_event,ua,requested_at,completed_at,transport,status,response_complete,coverage,node_count) VALUES($1,$2,'fixture@example.test','Forest',$3,$3,'server_json',200,$4,'complete',1)`, rid, user, at, complete)
		exec(`INSERT INTO v2_entry_delivery_node(request_id,ordinal,server_type,server_id,policy_id,group_id,source_host,delivered_host,port,source) VALUES($1,0,'vmess',$2,$3,$4,$5::text,$5::text,'54101','policy')`, rid, node, pol, group, host)
	}
	for _, u := range users {
		delivery(u, policy, 0, "192.0.2.10", now-180, true)
	}
	q := EntryEvidenceQueryRequest{Kind: "policy", TargetID: policy, Host: "192.0.2.10", Port: 54101, From: now - 300, To: now - 100}
	preview, err := s.PreviewEntryEvidenceCandidates(ctx, actor, q)
	if err != nil || !preview.FreezeAllowed {
		t.Fatal("preview", preview, err)
	}
	c, err := s.CreateEntryEvidenceCase(ctx, actor, CreateEntryEvidenceCaseRequest{EntryEvidenceQueryRequest: q, Revision: preview.Target.Revision, ExpectedCandidatesHash: preview.CandidatesHash})
	if err != nil {
		t.Fatal("freeze", err)
	}
	start := EntryInvestigationStartRequest{CaseID: c.ID, Revision: c.Revision, BlockedAt: q.To, Note: "operator independently confirmed", MinObserveSeconds: 300, MaxRounds: 3, StopUsers: 2}
	task, err := s.StartEntryInvestigation(ctx, actor, start)
	if err != nil || task.Report.State != "awaiting_split_confirmation" {
		t.Fatal("start", task, err)
	}
	if replay, err := s.StartEntryInvestigation(ctx, actor, start); err != nil || replay.CaseID != task.CaseID {
		t.Fatal("start replay", err)
	}
	if _, err = s.GetEntryInvestigation(ctx, users[0], c.ID); !errors.Is(err, ErrEntryEvidenceForbidden) {
		t.Fatal("ordinary access", err)
	}
	if err = s.DeleteEntryEvidenceCase(ctx, actor, c.ID); !errors.Is(err, ErrEntryEvidenceConflict) {
		t.Fatal("protected case delete", err)
	}
	// Existing evidence split is reused; independent pool IPs are health checked.
	for i := 1; i <= 2; i++ {
		host := fmt.Sprintf("198.51.100.%d", i)
		pool := id(`INSERT INTO v2_client_entry_backup_ip(name,ip,port,enabled,check_interval_sec,tcp_timeout_ms,sort,created_at,updated_at) VALUES($1::text,$1::text,54101,1,30,3000,$2,1,1) RETURNING id`, host, i)
		exec(`INSERT INTO v2_client_entry_backup_ip_state(backup_ip_id,probe_id,last_success,consecutive_success,last_reported_at,updated_at) VALUES($1,$2,1,4,$3,$3)`, pool, probe, now)
	}
	done, err := s.ExecuteEntryEvidenceSplit(ctx, actor, EntryEvidenceSplitRequest{CaseID: c.ID, Revision: c.Revision, UseBackupIPPool: true})
	if err != nil || done.Execution == nil {
		t.Fatal("split", err)
	}
	// Move only fixture timestamps to model elapsed observation without sleeps.
	done.Execution.ExecutedAt = now - 600
	raw, _ := json.Marshal(done.Execution)
	exec(`UPDATE v2_entry_evidence_case SET execution=$2 WHERE id=$1`, c.ID, string(raw))
	for i, u := range users {
		group, host := done.Execution.GroupAID, done.Execution.EntryHostA
		if i >= 4 {
			group, host = done.Execution.GroupBID, done.Execution.EntryHostB
		}
		delivery(u, done.Execution.PolicyID, group, host, now-500, true)
	}
	// Same user repeated, incomplete requests and post-failure reads do not expand count.
	delivery(users[0], done.Execution.PolicyID, done.Execution.GroupAID, done.Execution.EntryHostA, now-450, true)
	delivery(users[0], done.Execution.PolicyID, done.Execution.GroupAID, done.Execution.EntryHostA, now-50, false)
	targetA := id(`SELECT id FROM v2_client_entry_monitor_target WHERE source_key=$1`, fmt.Sprintf("policy:%d:split-group:%d", done.Execution.PolicyID, done.Execution.GroupAID))
	targetB := id(`SELECT id FROM v2_client_entry_monitor_target WHERE source_key=$1`, fmt.Sprintf("policy:%d:split-group:%d", done.Execution.PolicyID, done.Execution.GroupBID))
	for _, state := range []struct{ id, success int64 }{{targetA, 0}, {targetB, 1}} {
		exec(`INSERT INTO v2_client_entry_monitor_state(target_id,probe_id,last_success,consecutive_success,consecutive_failure,last_reported_at,updated_at) VALUES($1,$2,$3,4,4,$4,$4) ON CONFLICT(target_id,probe_id) DO UPDATE SET last_success=$3,consecutive_success=4,consecutive_failure=4,last_reported_at=$4`, state.id, probe, state.success, now)
	}
	confirmed, err := s.ConfirmEntryInvestigationFailure(ctx, actor, EntryInvestigationConfirmRequest{CaseID: c.ID, Revision: task.Revision, Branch: "A", BlockedAt: now - 100, Note: "confirmed", ControlStatus: "healthy"})
	if err != nil || confirmed.Report.State != "ready_next" {
		t.Fatal("confirm", confirmed, err)
	}
	if confirmed.Report.Branches[0].PreFailureUsers != 4 {
		t.Fatal("dedup", confirmed.Report)
	}
	oldRevision := confirmed.Revision
	refreshed, err := s.RefreshEntryInvestigation(ctx, actor, c.ID)
	if err != nil || refreshed.Revision != oldRevision {
		t.Fatal("polling invalidates CAS", refreshed, err)
	}
	// External membership and UA changes cannot silently expand the next list.
	exec(`UPDATE v2_client_entry_user_policy_split_assignment SET group_id=$2 WHERE policy_id=$1 AND user_id=$3`, done.Execution.PolicyID, done.Execution.GroupBID, users[0])
	drift, err := s.RefreshEntryInvestigation(ctx, actor, c.ID)
	if err != nil || drift.Report.State != "needs_review" {
		t.Fatal("membership drift accepted", drift, err)
	}
	exec(`UPDATE v2_client_entry_user_policy_split_assignment SET group_id=$2 WHERE policy_id=$1 AND user_id=$3`, done.Execution.PolicyID, done.Execution.GroupAID, users[0])
	exec(`UPDATE v2_client_entry_user_policy SET conditions=$2 WHERE id=$1`, done.Execution.PolicyID, `[{"field":"ua","operator":"contains_any","values":["changed"]}]`)
	drift, err = s.RefreshEntryInvestigation(ctx, actor, c.ID)
	if err != nil || drift.Report.State != "needs_review" {
		t.Fatal("UA drift accepted", drift, err)
	}
	exec(`UPDATE v2_client_entry_user_policy SET conditions=$2 WHERE id=$1`, done.Execution.PolicyID, cond)
	// Concurrent next requests must produce just one child case and one frozen list.
	type res struct {
		task EntryInvestigation
		err  error
	}
	ch := make(chan res, 2)
	for i := 0; i < 2; i++ {
		go func() {
			x, e := s.NextEntryInvestigationRound(ctx, actor, EntryInvestigationNextRequest{CaseID: c.ID, Revision: oldRevision})
			ch <- res{x, e}
		}()
	}
	r1, r2 := <-ch, <-ch
	if r1.err != nil || r2.err != nil || r1.task.CaseID != r2.task.CaseID || r1.task.Round != 2 {
		t.Fatal("concurrent next", r1, r2)
	}
	child := r1.task
	if child.Report.State != "awaiting_split_confirmation" {
		t.Fatal("child linkage report not ready", child.Report)
	}
	childCase, err := s.GetEntryEvidenceCase(ctx, actor, child.CaseID)
	if err != nil || childCase.SelectedCount != 4 || childCase.ParentCaseID != c.ID || childCase.Execution != nil {
		t.Fatal("child frozen only", childCase, err)
	}
	restarted := NewDBService(config.Config{}, db)
	restored, err := restarted.GetEntryInvestigation(ctx, actor, child.CaseID)
	if err != nil || restored.RootCaseID != c.ID {
		t.Fatal("restart loses task", restored, err)
	}
	// Losing the retained parent's monitoring must block the child, not count as healthy.
	childMonitor := id(`SELECT monitor_id FROM v2_client_entry_monitor_target WHERE id=$1`, targetB)
	exec(`UPDATE v2_client_entry_monitor SET enabled=0 WHERE id=$1`, childMonitor)
	pausedPeer, err := s.RefreshEntryInvestigation(ctx, actor, child.CaseID)
	if err != nil || pausedPeer.Report.State != "needs_review" {
		t.Fatal("missing parent monitoring fails open", pausedPeer, err)
	}
	if _, err = s.ExecuteEntryEvidenceSplit(ctx, actor, EntryEvidenceSplitRequest{CaseID: childCase.ID, Revision: childCase.Revision, UseBackupIPPool: true}); !errors.Is(err, ErrEntryEvidenceConflict) {
		t.Fatal("child executed despite lost monitoring", err)
	}
	exec(`UPDATE v2_client_entry_monitor SET enabled=1 WHERE id=$1`, childMonitor)
	pausedPeer, err = s.RefreshEntryInvestigation(ctx, actor, child.CaseID)
	if err != nil || pausedPeer.Report.State != "awaiting_split_confirmation" {
		t.Fatal("restored parent monitoring", pausedPeer, err)
	}
	exec(`UPDATE v2_client_entry_monitor_state SET last_success=0,consecutive_failure=4 WHERE target_id=$1`, targetB)
	peerFailed, err := s.RefreshEntryInvestigation(ctx, actor, c.ID)
	if err != nil || peerFailed.Report.State != "needs_review" || len(peerFailed.Report.EvidenceRisks) == 0 {
		t.Fatal("late peer down not retained", peerFailed, err)
	}
	exec(`UPDATE v2_client_entry_monitor_state SET last_success=1 WHERE target_id=$1`, targetB)
	pausedPeer, err = s.RefreshEntryInvestigation(ctx, actor, child.CaseID)
	if err != nil || pausedPeer.Report.State != "needs_review" {
		t.Fatal("peer recovery silently erased contradiction", pausedPeer, err)
	}
	// Separate later checks use an uncontaminated fixture, not a product reset API.
	exec(`UPDATE v2_entry_investigation SET report=report - 'evidence_risks' WHERE case_id=$1`, c.ID)
	// Direct node hosts and staff override targets are independent exposure paths.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	refs, err := investigationHostReferences(ctx, tx, "192.0.2.254", 0, 0)
	if err != nil || refs == 0 {
		t.Fatal("direct node reference missed", refs, err)
	}
	if err := validateInvestigationEntryHosts(ctx, tx, childCase, "203.0.113.41", "203.0.113.42", now); !errors.Is(err, ErrEntryEvidenceInvalid) {
		t.Fatal("unprobed manual IP accepted", err)
	}
	tx.Rollback()
	exec(`INSERT INTO v2_support_entry_override(user_id,source_policy_id,target_policy_id,target_group_id,active,updated_by,updated_at) VALUES($1,$2,$3,$4,1,$5,$6)`, actor, policy, done.Execution.PolicyID, done.Execution.GroupBID, actor, now)
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	refs, err = investigationHostReferences(ctx, tx, done.Execution.EntryHostB, done.Execution.PolicyID, done.Execution.GroupBID)
	tx.Rollback()
	if err != nil || refs == 0 {
		t.Fatal("staff override reference missed", refs, err)
	}
	exec(`DELETE FROM v2_support_entry_override WHERE user_id=$1`, actor)
	// An observed contamination fact remains after the raw event rolls away.
	delivery(actor, done.Execution.PolicyID, done.Execution.GroupBID, done.Execution.EntryHostB, now-40, true)
	contaminated, err := s.RefreshEntryInvestigation(ctx, actor, c.ID)
	if err != nil || len(contaminated.Report.EvidenceRisks) == 0 {
		t.Fatal("outsider exposure not archived", contaminated, err)
	}
	exec(`DELETE FROM v2_entry_delivery_request WHERE user_id=$1`, actor)
	contaminated, err = s.RefreshEntryInvestigation(ctx, actor, c.ID)
	if err != nil || contaminated.Report.State != "needs_review" || !strings.Contains(strings.Join(contaminated.Report.EvidenceRisks, ";"), "冻名单以外") {
		t.Fatal("risk vanished with raw logs", contaminated, err)
	}
	// Restore the independent fixture to exercise late-B confirmation separately.
	exec(`UPDATE v2_entry_investigation SET report=report - 'evidence_risks' WHERE case_id=$1`, c.ID)
	// Original incident expiry is not masked by a newer execution timestamp.
	exec(`UPDATE v2_entry_evidence_case SET snapshot=jsonb_set(snapshot,'{query,from}',to_jsonb($2::BIGINT)) WHERE id=$1`, c.ID, now-8*86400)
	expired, err := s.RefreshEntryInvestigation(ctx, actor, c.ID)
	if err != nil || expired.Report.State != "needs_review" || len(expired.Report.EvidenceRisks) == 0 {
		t.Fatal("original incident retention expiry missed", expired, err)
	}
	exec(`UPDATE v2_entry_evidence_case SET snapshot=jsonb_set(snapshot,'{query,from}',to_jsonb($2::BIGINT)) WHERE id=$1`, c.ID, q.From)
	exec(`UPDATE v2_entry_investigation SET report=report - 'evidence_risks' WHERE case_id=$1`, c.ID)
	// The second round really executes through the normal evidence split path.
	// Its parent A becomes an internal node, but retained B must stay observable.
	for i := 3; i <= 4; i++ {
		host := fmt.Sprintf("198.51.100.%d", i)
		pool := id(`INSERT INTO v2_client_entry_backup_ip(name,ip,port,enabled,check_interval_sec,tcp_timeout_ms,sort,created_at,updated_at) VALUES($1::text,$1::text,54101,1,30,3000,$2,1,1) RETURNING id`, host, i)
		exec(`INSERT INTO v2_client_entry_backup_ip_state(backup_ip_id,probe_id,last_success,consecutive_success,last_reported_at,updated_at) VALUES($1,$2,1,4,$3,$3)`, pool, probe, now)
	}
	childDone, err := s.ExecuteEntryEvidenceSplit(ctx, actor, EntryEvidenceSplitRequest{CaseID: childCase.ID, Revision: childCase.Revision, UseBackupIPPool: true})
	if err != nil || childDone.Execution == nil {
		t.Fatal("second round split", childDone, err)
	}
	continued, err := s.RefreshEntryInvestigation(ctx, actor, c.ID)
	if err != nil || continued.Report.State != "continued" {
		t.Fatal("parent rejected expected child topology", continued, err)
	}
	// A late second-branch failure is still accepted after continuation, and child pauses.
	parent, err := s.GetEntryInvestigation(ctx, actor, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	parent, err = s.ConfirmEntryInvestigationFailure(ctx, actor, EntryInvestigationConfirmRequest{CaseID: c.ID, Revision: parent.Revision, Branch: "B", BlockedAt: now - 80, Note: "later second branch blocked", ControlStatus: "healthy"})
	if err != nil || parent.Report.State != "both_failed" {
		t.Fatal("late parent confirmation", parent, err)
	}
	paused, err := s.RefreshEntryInvestigation(ctx, actor, child.CaseID)
	if err != nil || paused.Report.State != "needs_review" {
		t.Fatal("ancestor contradiction not propagated", paused, err)
	}
	// Reports are metadata-only even when maintenance is called after a restart.
	count := id(`SELECT count(*) FROM v2_client_entry_user_policy_split_group`)
	if err = s.RefreshEntryInvestigations(ctx); err != nil {
		t.Fatal(err)
	}
	if id(`SELECT count(*) FROM v2_client_entry_user_policy_split_group`) != count {
		t.Fatal("maintenance mutated topology")
	}
	// One corrupt task cannot starve the next task or keep an old ready suggestion.
	originalSettings, _ := json.Marshal(parent.Settings)
	exec(`UPDATE v2_entry_investigation SET settings='"broken"'::JSONB,checked_at=0 WHERE case_id=$1`, c.ID)
	exec(`UPDATE v2_entry_investigation SET checked_at=0 WHERE case_id=$1`, child.CaseID)
	if err = s.RefreshEntryInvestigations(ctx); err == nil {
		t.Fatal("corrupt task not reported")
	}
	if id(`SELECT count(*) FROM v2_entry_investigation WHERE checked_at>0`) != 2 {
		t.Fatal("one failure starved maintenance")
	}
	exec(`UPDATE v2_entry_investigation SET settings=$2 WHERE case_id=$1`, c.ID, string(originalSettings))
	// GET against read-only PostgreSQL works after schema startup initialization.
	ro := cfg.Copy()
	ro.RuntimeParams["default_transaction_read_only"] = "on"
	readDB := stdlib.OpenDB(*ro)
	defer readDB.Close()
	reader := NewDBService(config.Config{}, readDB)
	state := &evidenceSchemaState{}
	state.once.Do(func() {})
	evidenceSchemaStates.Store(readDB, state)
	defer evidenceSchemaStates.Delete(readDB)
	if _, err = reader.GetEntryInvestigation(ctx, actor, c.ID); err != nil {
		t.Fatal("GET wrote DB", err)
	}
	_ = sql.ErrNoRows
}
