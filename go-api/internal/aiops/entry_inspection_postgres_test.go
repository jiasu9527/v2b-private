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

func TestAIEntryInspectionPostgresToolsReceiptAndReadOnly(t *testing.T) {
	f := setupStaffAIFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := platformpostgres.EnsureClientEntrySchema(ctx, f.db); err != nil {
		t.Fatal(err)
	}
	if err := platformpostgres.EnsureDNSFailoverSchema(ctx, f.db); err != nil {
		t.Fatal(err)
	}
	// A full save intentionally removes omitted monitors. This fixture must
	// never replace other tests' monitor settings, even on a disposable DB.
	var existing int
	if err := f.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_client_entry_monitor`).Scan(&existing); err != nil {
		t.Fatal(err)
	}
	if existing != 0 {
		t.Skip("requires disposable database without pre-existing monitor fixtures")
	}
	session, err := f.s.CreateSession(ctx, f.staff, CreateSessionRequest{Model: DefaultModels[0]})
	if err != nil {
		t.Fatal(err)
	}
	marker := fmt.Sprint(time.Now().UnixNano())
	host := "inspection-" + marker + ".example.test"
	var node, policy, probe int64
	if err = f.db.QueryRowContext(ctx, `INSERT INTO v2_server_vmess(group_id,name,host,port,server_port,rate,network,created_at,updated_at) VALUES('[]','inspection node','192.0.2.52','54101',54101,'1','tcp',1,1) RETURNING id`).Scan(&node); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.db.Exec(`DELETE FROM v2_server_vmess WHERE id=$1`, node) })
	if err = f.db.QueryRowContext(ctx, `INSERT INTO v2_client_entry_user_policy(name,entry_host,conditions,extra_nodes,created_at,updated_at) VALUES('inspection rule',$1,'[]','["trojan://MUST_NOT_EXPOSE_INSPECTION_EXTRA_NODE@extra.example.test:443#fixture"]',1,1) RETURNING id`, host).Scan(&policy); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		f.db.Exec(`DELETE FROM v2_client_entry_monitor WHERE policy_id=$1`, policy)
		f.db.Exec(`DELETE FROM v2_client_entry_user_policy_member WHERE policy_id=$1`, policy)
		f.db.Exec(`DELETE FROM v2_client_entry_user_policy WHERE id=$1`, policy)
	})
	if _, err = f.db.ExecContext(ctx, `INSERT INTO v2_client_entry_user_policy_member(policy_id,server_type,server_id,sort,created_at,updated_at) VALUES($1,'vmess',$2,10,1,1)`, policy, node); err != nil {
		t.Fatal(err)
	}
	if err = f.db.QueryRowContext(ctx, `INSERT INTO v2_dns_probe(name,token_hash,token_plaintext,last_heartbeat_at,created_at,updated_at) VALUES('inspection probe',$1,'MUST_NOT_EXPOSE_INSPECTION_PROBE',$2,1,1) RETURNING id`, hashOperation(marker), time.Now().Unix()).Scan(&probe); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.db.Exec(`DELETE FROM v2_dns_probe WHERE id=$1`, probe) })

	call := func(name string, args map[string]any) ToolCall {
		raw, e := json.Marshal(args)
		if e != nil {
			t.Fatal(e)
		}
		return ToolCall{ID: "inspection-" + name, Function: ToolFunction{Name: name, Arguments: string(raw)}}
	}
	read := func(name string, args map[string]any, out any) string {
		t.Helper()
		got, e := f.s.callTool(ctx, f.staff, session.ID, call(name, args))
		if e != nil {
			t.Fatalf("%s: %v", name, e)
		}
		if strings.Contains(got.Content, "MUST_NOT_EXPOSE") || len(got.Content) > maxToolOutputBytes {
			t.Fatalf("unsafe/oversized %s projection", name)
		}
		if out != nil {
			if e = json.Unmarshal([]byte(got.Content), out); e != nil {
				t.Fatal(e)
			}
		}
		return got.Content
	}
	for _, name := range []string{"host_references", "entry_coverage", "entry_health", "entry_health_detail"} {
		args := map[string]any{}
		if name == "host_references" {
			args["host"] = host
		}
		if name == "entry_health_detail" {
			args["key"] = "unused"
		}
		for _, actor := range []int64{f.other, f.user} {
			_, e := f.s.callTool(ctx, actor, session.ID, call(name, args))
			want := ErrNotFound
			if actor == f.user {
				want = ErrForbidden
			}
			if !errors.Is(e, want) {
				t.Fatalf("%s actor %d expected %v, got %v", name, actor, want, e)
			}
		}
	}
	var refs struct {
		Revision string                    `json:"revision"`
		Page     pageDTO[hostReferenceDTO] `json:"page"`
	}
	read("host_references", map[string]any{"host": strings.ToUpper(host) + ".", "port": 54101}, &refs)
	foundPolicy, foundNode := false, false
	for _, r := range refs.Page.Items {
		if r.PolicyID != policy || r.CoverageStatus != "unconfigured" {
			t.Fatalf("unexpected unmonitored source %#v", r)
		}
		foundPolicy = foundPolicy || r.Reference.Type == "policy" && r.Reference.ID == policy
		foundNode = foundNode || r.Reference.Type == "node" && r.Reference.ID == node
	}
	if !foundPolicy || !foundNode || len(refs.Revision) != 64 {
		t.Fatalf("unmonitored references omitted: %#v", refs)
	}
	var coverage struct {
		Summary admin.EntryMonitorInventorySummary `json:"summary"`
		Page    pageDTO[entryCoverageSourceDTO]    `json:"page"`
	}
	read("entry_coverage", map[string]any{"gaps_only": true}, &coverage)
	foundGap := false
	for _, row := range coverage.Page.Items {
		if row.PolicyID == policy && row.CoverageStatus == "unconfigured" && !row.AddressCovered {
			foundGap = true
		}
	}
	if !foundGap || coverage.Summary.SourceGaps < 1 || coverage.Summary.AddressGaps < 1 {
		t.Fatalf("unmonitored gap missing: %#v", coverage)
	}
	var health healthDTO
	read("entry_health", map[string]any{}, &health)
	for _, row := range health.Entries.Items {
		if row.Host == host {
			t.Fatal("unmonitored endpoint shown on employee health list")
		}
	}

	panel := admin.NewDBService(config.Config{}, f.db)
	var oldRevision, storedRevision int64
	if err = f.db.QueryRowContext(ctx, `SELECT revision FROM v2_client_entry_monitor_config WHERE id=1`).Scan(&oldRevision); err != nil {
		t.Fatal(err)
	}
	receipt, err := panel.SaveClientEntryMonitorReceipt(ctx, admin.ClientEntryMonitorSaveRequest{Revision: oldRevision, Items: []admin.ClientEntryMonitorSaveItem{{PolicyID: policy, Enabled: true, Targets: []admin.ClientEntryMonitorTargetSaveRequest{{SourceKey: fmt.Sprintf("policy:%d", policy), Port: 54101}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.db.QueryRowContext(ctx, `SELECT revision FROM v2_client_entry_monitor_config WHERE id=1`).Scan(&storedRevision); err != nil {
		t.Fatal(err)
	}
	receiptRaw, _ := json.Marshal(receipt)
	if !receipt.Committed || receipt.Revision != oldRevision+1 || receipt.Revision != storedRevision || receipt.SavedPolicyCount != 1 || receipt.SavedTargetCount != 1 || receipt.DeletedPolicyCount != 0 || len(receiptRaw) > 400 || strings.Contains(string(receiptRaw), "policies") {
		t.Fatalf("receipt=%s db_revision=%d", receiptRaw, storedRevision)
	}
	if _, err = panel.SaveClientEntryMonitorReceipt(ctx, admin.ClientEntryMonitorSaveRequest{Revision: oldRevision, Items: []admin.ClientEntryMonitorSaveItem{}}); !errors.Is(err, admin.ErrClientEntryMonitorRevisionConflict) {
		t.Fatalf("stale save=%v", err)
	}
	var target int64
	if err = f.db.QueryRowContext(ctx, `SELECT t.id FROM v2_client_entry_monitor_target t JOIN v2_client_entry_monitor m ON m.id=t.monitor_id WHERE m.policy_id=$1`, policy).Scan(&target); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.ExecContext(ctx, `INSERT INTO v2_client_entry_monitor_state(target_id,probe_id,last_success,last_latency_ms,last_resolved_ip,consecutive_success,consecutive_failure,last_reported_at,updated_at) VALUES($1,$2,1,42,'192.0.2.52',3,0,$3,$3)`, target, probe, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	read("entry_health", map[string]any{}, &health)
	key := ""
	for _, row := range health.Entries.Items {
		if row.Host == host {
			key = row.Key
			if row.SourceCount != 1 || len(row.Sources) != 0 || len(row.Probes) != 0 {
				t.Fatalf("health must contain only counts: %#v", row)
			}
		}
	}
	if key == "" {
		t.Fatal("monitored endpoint missing")
	}
	var sources struct {
		Page     pageDTO[healthSourceDTO] `json:"page"`
		Revision string                   `json:"revision"`
	}
	read("entry_health_detail", map[string]any{"key": key, "section": "sources"}, &sources)
	if len(sources.Page.Items) != 1 || sources.Page.Items[0].PolicyID != policy || !sources.Page.Items[0].Monitored {
		t.Fatalf("sources=%#v", sources)
	}
	var probes struct {
		Page pageDTO[probeDTO] `json:"page"`
	}
	read("entry_health_detail", map[string]any{"key": key, "section": "probes", "revision": sources.Revision}, &probes)
	foundProbe := false
	for _, p := range probes.Page.Items {
		if p.ID == probe && p.LatencyMS != nil && *p.LatencyMS == 42 {
			foundProbe = true
		}
	}
	if !foundProbe {
		t.Fatalf("probe detail missing fixture observation: %#v", probes)
	}
	// Registering the monitor changed host references: old pagination must
	// conflict rather than combine unconfigured and configured snapshots.
	_, err = f.s.callTool(ctx, f.staff, session.ID, call("host_references", map[string]any{"host": host, "port": 54101, "revision": refs.Revision}))
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("changed topology revision accepted: %v", err)
	}

	readOnlyDB, err := sql.Open("pgx", os.Getenv("FOREST_AIOPS_TEST_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	defer readOnlyDB.Close()
	readOnlyDB.SetMaxOpenConns(1)
	readOnlyDB.SetMaxIdleConns(1)
	if _, err = readOnlyDB.ExecContext(ctx, `SET default_transaction_read_only=on`); err != nil {
		t.Fatal(err)
	}
	var readOnly string
	if err = readOnlyDB.QueryRowContext(ctx, `SHOW default_transaction_read_only`).Scan(&readOnly); err != nil || readOnly != "on" {
		t.Fatalf("read only connection %q %v", readOnly, err)
	}
	// Fresh services have no primed schema-once guards. PostgreSQL rejects any
	// hidden DDL/DML from these supposedly recovery-safe read paths.
	ro := admin.NewDBService(config.Config{}, readOnlyDB)
	inventory, err := ro.EntryMonitorInventory(ctx)
	if err != nil {
		t.Fatalf("inventory is not first-call read-only: %v", err)
	}
	foundInventory := false
	for _, row := range inventory.Sources {
		if row.PolicyID == policy {
			foundInventory = true
		}
	}
	if !foundInventory {
		t.Fatal("read-only inventory omitted fixture")
	}
	overview, err := admin.NewDBService(config.Config{}, readOnlyDB).SupportEntryOverview(ctx)
	if err != nil {
		t.Fatalf("health is not first-call read-only: %v", err)
	}
	foundHealth := false
	for _, row := range overview.Items {
		if row.Host == host {
			foundHealth = true
		}
	}
	if !foundHealth {
		t.Fatal("read-only health omitted fixture")
	}
}
