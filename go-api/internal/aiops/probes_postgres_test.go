package aiops

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

	"forest/go-api/internal/admin"
	"forest/go-api/internal/config"
	platformpostgres "forest/go-api/internal/platform/postgres"
)

func TestAIEntryProbeSelectionPostgres(t *testing.T) {
	dsn := os.Getenv("FOREST_AIOPS_TEST_DSN")
	if dsn == "" {
		t.Skip("requires disposable FOREST_AIOPS_TEST_DSN")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err = platformpostgres.EnsureClientEntrySchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err = platformpostgres.EnsureDNSFailoverSchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	panel := admin.NewDBService(config.Config{}, db)
	s := NewService(config.Config{AppKey: "probe-fixture-key"}, db, panel)
	defer s.Close()
	if err = s.ensureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	var actor, session int64
	marker := fmt.Sprint(time.Now().UnixNano())
	if err = db.QueryRowContext(ctx, `INSERT INTO v2_user(email,password,uuid,token,is_admin,created_at,updated_at)VALUES($1,'unused','fixture',$2,1,1,1) RETURNING id`, "probe-"+marker+"@example.test", marker).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM v2_user WHERE id=$1`, actor)
	if _, err = s.SaveConfig(ctx, actor, ConfigSaveRequest{Enabled: true, BaseURL: "https://fixture.example.test/v1", APIKey: "fixture", Models: DefaultModels}); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM v2_ai_config WHERE updated_by=$1`, actor)
	if err = db.QueryRowContext(ctx, `INSERT INTO v2_ai_session(title,model,created_by,created_at,updated_at)VALUES('probe test','fixture',$1,1,1) RETURNING id`, actor).Scan(&session); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM v2_ai_session WHERE id=$1`, session)
	policies, probes := []int64{}, []int64{}
	for i := 0; i < 2; i++ {
		var policy, monitor, probe int64
		if err = db.QueryRowContext(ctx, `INSERT INTO v2_client_entry_user_policy(name,entry_host,created_at,updated_at)VALUES($1,$2,1,1) RETURNING id`, fmt.Sprintf("probe-fixture-%d", i), fmt.Sprintf("192.0.2.%d", i+1)).Scan(&policy); err != nil {
			t.Fatal(err)
		}
		defer db.Exec(`DELETE FROM v2_client_entry_user_policy WHERE id=$1`, policy)
		policies = append(policies, policy)
		if err = db.QueryRowContext(ctx, `INSERT INTO v2_client_entry_monitor(policy_id,created_at,updated_at)VALUES($1,1,1) RETURNING id`, policy).Scan(&monitor); err != nil {
			t.Fatal(err)
		}
		defer db.Exec(`DELETE FROM v2_client_entry_monitor WHERE id=$1`, monitor)
		defer db.Exec(`DELETE FROM v2_client_entry_monitor_target WHERE monitor_id=$1`, monitor)
		if err = db.QueryRowContext(ctx, `INSERT INTO v2_dns_probe(name,token_hash,token_plaintext,last_heartbeat_at,created_at,updated_at)VALUES($1,$2,'never-expose-fixture-secret',$3,1,1) RETURNING id`, fmt.Sprintf("fixture-probe-%d", i), hashOperation(marker+fmt.Sprint(i)), time.Now().Unix()).Scan(&probe); err != nil {
			t.Fatal(err)
		}
		defer db.Exec(`DELETE FROM v2_dns_probe WHERE id=$1`, probe)
		probes = append(probes, probe)
	}
	read, err := s.callProbeTool(ctx, actor, session, ToolCall{Function: ToolFunction{Name: "entry_probe_targets", Arguments: `{}`}})
	if err != nil || strings.Contains(read.Content, "never-expose-fixture-secret") {
		t.Fatal("targets/secret", err)
	}
	var target int64
	if err = db.QueryRowContext(ctx, `SELECT t.id FROM v2_client_entry_monitor_target t JOIN v2_client_entry_monitor m ON m.id=t.monitor_id WHERE m.policy_id=$1`, policies[0]).Scan(&target); err != nil {
		t.Fatal(err)
	}
	selection := admin.AIEntryProbeSelection{TargetIDs: []int64{target}, ProbeIDs: []int64{probes[0]}}
	// Omitting probe IDs retains all enabled probes without broadening targets.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	allProbes, err := panel.CaptureAIEntryProbeSelection(ctx, tx, admin.AIEntryProbeSelection{PolicyIDs: []int64{policies[0]}})
	tx.Rollback()
	if err != nil || len(allProbes.Targets) != 1 || len(allProbes.Probes) != 2 {
		t.Fatal("default probe scope", err)
	}
	args, _ := json.Marshal(map[string]any{"target_ids": selection.TargetIDs, "probe_ids": selection.ProbeIDs, "reason": "test exact target"})
	proposal, err := s.callProbeTool(ctx, actor, session, ToolCall{ID: "probe-select", Function: ToolFunction{Name: "entry_probe_run", Arguments: string(args)}})
	if err != nil || !proposal.Pending {
		t.Fatal("proposal", err)
	}
	var summary struct {
		ActionID int64 `json:"action_id"`
	}
	json.Unmarshal([]byte(proposal.Content), &summary)
	var count int
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_client_entry_monitor_run WHERE requested_by_user_id=$1`, actor).Scan(&count); err != nil || count != 0 {
		t.Fatal("unapproved dispatch", err, count)
	}
	a, err := s.ApproveAction(ctx, actor, session, summary.ActionID)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		actions, e := s.ListActions(ctx, actor, session)
		if e != nil {
			t.Fatal(e)
		}
		if len(actions) > 0 && actions[0].Status != "running" {
			a = actions[0]
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if a.Status != "succeeded" {
		t.Fatalf("dispatch %+v", a)
	}
	var runID int64
	var fingerprint string
	if err = db.QueryRowContext(ctx, `SELECT id FROM v2_client_entry_monitor_run WHERE requested_by_user_id=$1`, actor).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM v2_client_entry_monitor_run WHERE id=$1`, runID)
	if err = db.QueryRowContext(ctx, `SELECT snapshot_hash FROM v2_ai_action WHERE id=$1`, a.ID).Scan(&fingerprint); err != nil {
		t.Fatal(err)
	}
	replay, err := panel.StartAIEntryProbeRun(ctx, selection, actor, fmt.Sprintf("ai-entry-probe:%d", a.ID), fingerprint)
	if err != nil || replay != runID {
		t.Fatal("idempotent dispatch", replay, err)
	}
	if _, err = s.ApproveAction(ctx, actor, session, a.ID); !errors.Is(err, ErrConflict) {
		t.Fatal("repeat approval", err)
	}
	before, err := s.readProbeRun(ctx, runID, 20, 0)
	if err != nil || before.Expected != 1 || before.Pending != 1 || before.Received != 0 || len(before.Results.Items) != 1 {
		t.Fatalf("pending %+v %v", before, err)
	}
	if before.Results.Items[0].TargetID != target || before.Results.Items[0].ProbeID != probes[0] {
		t.Fatal("wrong expected pair")
	}
	for _, probe := range probes {
		tasks, e := panel.ListDNSProbeTasks(ctx, probe)
		if e != nil {
			t.Fatal(e)
		}
		matched := 0
		for _, task := range tasks {
			if task.RunID == runID {
				matched++
				if probe != probes[0] || task.TargetID != (int64(1)<<62)+target {
					t.Fatal("unselected target or probe dispatched")
				}
			}
		}
		want := 0
		if probe == probes[0] {
			want = 1
		}
		if matched != want {
			t.Fatal("run task count", matched, want)
		}
	}
	ok := true
	latency := int64(18)
	report := admin.DNSProbeResultsRequest{Results: []admin.DNSProbeResult{{ResultID: "fixture-probe-result", TargetID: (int64(1) << 62) + target, TargetVersion: before.Results.Items[0].TargetVersion, RunID: runID, Success: &ok, LatencyMS: &latency}}}
	report.Results[0].TargetVersion++
	if result, err := panel.ReportDNSProbeResults(ctx, probes[0], report); err != nil || result.Accepted != 0 {
		t.Fatal("wrong generation accepted", err)
	}
	report.Results[0].TargetVersion--
	if _, err = panel.ReportDNSProbeResults(ctx, probes[0], report); err != nil {
		t.Fatal("report", err)
	}
	after, err := s.readProbeRun(ctx, runID, 20, 0)
	if err != nil || after.Status != "completed" || after.Succeeded != 1 || after.Pending != 0 || after.Results.Items[0].Status != "healthy" {
		t.Fatalf("final %+v %v", after, err)
	}
	if _, err = panel.ReportDNSProbeResults(ctx, probes[0], report); err != nil {
		t.Fatal("duplicate report", err)
	}
	// A different probe reporting the same target never enters this run.
	report.Results[0].ResultID = "unselected-probe-result"
	if _, err = panel.ReportDNSProbeResults(ctx, probes[1], report); err != nil {
		t.Fatal("unselected report", err)
	}
	after, err = s.readProbeRun(ctx, runID, 20, 0)
	if err != nil || after.Received != 1 {
		t.Fatal("unselected report counted", err)
	}
	args, _ = json.Marshal(map[string]any{"policy_ids": []int64{policies[0]}, "target_ids": []int64{target}, "probe_ids": []int64{probes[0]}, "reason": "snapshot check"})
	proposal, err = s.callProbeTool(ctx, actor, session, ToolCall{ID: "probe-stale", Function: ToolFunction{Name: "entry_probe_run", Arguments: string(args)}})
	if err != nil {
		t.Fatal(err)
	}
	json.Unmarshal([]byte(proposal.Content), &summary)
	if _, err = db.ExecContext(ctx, `UPDATE v2_dns_probe SET enabled=0 WHERE id=$1`, probes[0]); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ApproveAction(ctx, actor, session, summary.ActionID); !errors.Is(err, ErrConflict) && !errors.Is(err, ErrValidation) {
		t.Fatal("changed probe accepted", err)
	}
	s.Close()
}

func TestAIProbeToolArgumentsRejectScopeWidening(t *testing.T) {
	for _, c := range []ToolCall{
		{Function: ToolFunction{Name: "entry_probe_run", Arguments: `{"target_ids":[1],"probe_ids":[2],"reason":"x","host":"evil.example"}`}},
		{Function: ToolFunction{Name: "entry_probe_targets", Arguments: `{"limit":null}`}},
		{Function: ToolFunction{Name: "entry_probe_result", Arguments: `{"run_id":1,"offset":-1}`}},
		{Function: ToolFunction{Name: "entry_probe_run", Arguments: `{"policy_ids":null,"reason":"x"}`}},
	} {
		if _, err := decodeProbeToolArgs(c); !errors.Is(err, ErrValidation) {
			t.Fatalf("accepted %s", c.Function.Arguments)
		}
	}
}
