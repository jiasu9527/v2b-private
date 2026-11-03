package aiops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/config"
	"github.com/DATA-DOG/go-sqlmock"
)

func TestEntryHealthDetailsPageAllRelationsWithoutInflatingList(t *testing.T) {
	v := admin.SupportEntryOverview{Coverage: "entry_assignment", CoverageNote: "configured only, TCP not business", GeneratedAt: 100}
	item := admin.SupportEntryItem{Key: "tcp:192.0.2.10:54101", Host: "192.0.2.10", Port: 54101, Status: "healthy"}
	for i := 1; i <= 86; i++ {
		item.Sources = append(item.Sources, admin.SupportEntrySource{PolicyID: int64(i), SourceKey: fmt.Sprintf("policy:%d:override", i), PolicyName: strings.Repeat("来源<&🚀", 100), PolicyEnabled: true, Status: "healthy"})
		item.Probes = append(item.Probes, admin.SupportEntryProbe{ProbeID: int64(i), ProbeName: strings.Repeat("探针<&🚀", 100), Status: "healthy"})
	}
	v.Items = []admin.SupportEntryItem{item}
	v.Summary.Total = 1
	list := projectHealth(v, "", 20, 0)
	raw := requireBoundedJSON(t, list)
	if list.CoverageNote != v.CoverageNote || strings.Contains(string(raw), `"sources":`) || strings.Contains(string(raw), `"probes":`) || len(raw) > 1500 {
		t.Fatalf("list should contain only counts and scope: %s", raw)
	}
	for _, section := range []string{"sources", "probes"} {
		seen, offset := 0, 0
		revision := ""
		for {
			out, err := projectHealthDetail(v, entryInspectionArgs{Key: item.Key, Section: section, Limit: 50, Offset: offset, Revision: revision})
			if err != nil {
				t.Fatal(err)
			}
			raw = requireBoundedJSON(t, out)
			var response struct {
				Revision string                  `json:"revision"`
				Page     pageDTO[map[string]any] `json:"page"`
			}
			if err = json.Unmarshal(raw, &response); err != nil {
				t.Fatal(err)
			}
			revision = response.Revision
			if response.Page.Total != 86 || len(response.Page.Items) == 0 {
				t.Fatal("lost relation page")
			}
			for _, row := range response.Page.Items {
				seen++
				id := row["policy_id"]
				if section == "probes" {
					id = row["id"]
				}
				if id != float64(seen) {
					t.Fatalf("skipped/duplicated %s: %v expected %d", section, id, seen)
				}
			}
			if !response.Page.Truncated {
				break
			}
			offset = *response.Page.NextOffset
		}
		if seen != 86 {
			t.Fatalf("incomplete %s: %d", section, seen)
		}
	}
	// Measurements may change without destroying a topology-pinned page.
	revision := healthTopologyRevision(item)
	v.Items[0].Probes[0].Status = "unhealthy"
	if _, err := projectHealthDetail(v, entryInspectionArgs{Key: item.Key, Limit: 20, Revision: revision}); err != nil {
		t.Fatal(err)
	}
	v.Items[0].Sources = append(v.Items[0].Sources, admin.SupportEntrySource{PolicyID: 999, SourceKey: "policy:999:override"})
	if _, err := projectHealthDetail(v, entryInspectionArgs{Key: item.Key, Limit: 20, Revision: revision}); !errors.Is(err, ErrConflict) {
		t.Fatal("changed relation set not rejected", err)
	}
	if _, err := projectHealthDetail(v, entryInspectionArgs{Key: "missing", Limit: 20}); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestEntryInventoryProjectionPagesReferencesAndGaps(t *testing.T) {
	s := admin.EntryMonitorInventorySource{Key: "policy:1:override", SourceKey: "policy:1:override", Host: "example.test", Port: 54101, PolicyID: 1, PolicyEnabled: true, CoverageStatus: "unconfigured", Issues: []string{"unconfigured"}, AddressCovered: true}
	for i := 1; i <= 86; i++ {
		s.References = append(s.References, admin.EntryMonitorReference{Type: "node", ID: int64(i), Protocol: "vless", Name: strings.Repeat("节点<&", 100)})
	}
	v := admin.EntryMonitorInventoryOverview{Coverage: "current_entry_assignment_candidates", CoverageNote: "not historic deliveries", Sources: []admin.EntryMonitorInventorySource{s}, Summary: admin.EntryMonitorInventorySummary{SourceGaps: 1, AddressGaps: 0}}
	other := s
	other.Key = "policy:2:override"
	other.SourceKey = other.Key
	other.Port = 443
	v.Sources = append(v.Sources, other)
	port := int64(54101)
	offset, seen := 0, 0
	revision := ""
	for {
		out, err := projectEntryInventory(v, entryInspectionArgs{Host: "example.test", Port: &port, Limit: 50, Offset: offset, Revision: revision}, "host_references")
		if err != nil {
			t.Fatal(err)
		}
		raw := requireBoundedJSON(t, out)
		var response struct {
			Revision string                    `json:"revision"`
			Page     pageDTO[hostReferenceDTO] `json:"page"`
		}
		json.Unmarshal(raw, &response)
		revision = response.Revision
		if response.Page.Total != 86 {
			t.Fatal("mixed distinct ports or lost refs")
		}
		for _, row := range response.Page.Items {
			seen++
			if row.Reference.Type != "node" || row.Reference.ID != int64(seen) || row.Port != port {
				t.Fatal(row)
			}
		}
		if !response.Page.Truncated {
			break
		}
		offset = *response.Page.NextOffset
	}
	if seen != 86 {
		t.Fatal(seen)
	}
	v.Sources[0].References = append(v.Sources[0].References, admin.EntryMonitorReference{Type: "policy", ID: 1})
	if _, err := projectEntryInventory(v, entryInspectionArgs{Host: "example.test", Port: &port, Limit: 50, Revision: revision}, "host_references"); !errors.Is(err, ErrConflict) {
		t.Fatal("reference change not detected", err)
	}
	out, err := projectEntryInventory(v, entryInspectionArgs{Limit: 20}, "entry_coverage")
	if err != nil {
		t.Fatal(err)
	}
	raw := requireBoundedJSON(t, out)
	if strings.Contains(string(raw), `"references":`) || strings.Contains(string(raw), `"probes":`) || !strings.Contains(string(raw), `"address_gaps":0`) || !strings.Contains(string(raw), `"source_gaps":1`) {
		t.Fatal(string(raw))
	}
}

func TestEntryInspectionToolAuthorizationSchemaAndErrors(t *testing.T) {
	s, mock, panel := newPaginationToolFixture(t)
	panel.health.Items[0].Sources = []admin.SupportEntrySource{{PolicyID: 1, SourceKey: "policy:1:override"}}
	good, err := paginationToolCall(t, s, mock, "entry_health_detail", `{"key":"1"}`)
	if err != nil || !strings.Contains(good.Content, `"policy_id":1`) || good.Pending {
		t.Fatal(good, err)
	}
	for _, args := range []string{`{"key":"1","offset":1}`, `{"key":"1","revision":"fake"}`, `{"key":"1","section":"anything"}`, `{"key":"1","limit":null}`, `{"key":"1","host":"a"}`} {
		before := panel.calls
		out, err := paginationToolCall(t, s, mock, "entry_health_detail", args)
		if !errors.Is(err, ErrValidation) || out.Pending || panel.calls != before {
			t.Fatalf("invalid args reached panel: %s %v", args, err)
		}
	}
	for _, name := range []string{"entry_health_detail", "entry_coverage", "host_references"} {
		if !s.panelRecoveryReadOnlyCall(ToolCall{Function: ToolFunction{Name: name}}) {
			t.Fatal("reviewed read not recoverable", name)
		}
	}
}

func TestCompactPanelReceiptReturnsInlineWithoutResultDatabase(t *testing.T) {
	s := NewService(config.Config{}, nil, nil)
	defer s.Close()
	raw := json.RawMessage(`{"committed":true,"revision":7,"saved_policy_count":86,"saved_target_count":86,"deleted_policy_count":0}`)
	out, err := s.savePanelResult(context.Background(), 1, 2, "entry_monitors.save", raw, false)
	if err != nil || !strings.Contains(out, `"committed":true`) || strings.Contains(out, "result_id") {
		t.Fatal(out, err)
	}
}

func TestEntryRuleReadRevisionRejectsChangedPages(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := NewService(config.Config{}, db, nil)
	defer s.Close()
	expectLoad := func(rawPolicy string, commit bool) {
		mock.ExpectBegin()
		mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(`SELECT to_jsonb\(p\)::TEXT,mode,name,sort,action,conditions,entry_host,resolve_entry_host,enabled,remarks`).WithArgs(int64(1)).WillReturnRows(sqlmock.NewRows([]string{"raw", "mode", "name", "sort", "action", "conditions", "entry_host", "resolve", "enabled", "remarks"}).AddRow(rawPolicy, "standard", "fixture", 1, "original", "[]", "", 0, 1, ""))
		mock.ExpectQuery(`SELECT server_type,server_id FROM v2_client_entry_user_policy_member`).WithArgs(int64(1)).WillReturnRows(sqlmock.NewRows([]string{"server_type", "server_id"}).AddRow("vless", 1))
		mock.ExpectQuery(`SELECT c.id,c.entry_host,c.resolve_entry_host,to_jsonb\(c\)::TEXT`).WithArgs(int64(1)).WillReturnRows(sqlmock.NewRows([]string{"id", "host", "resolve", "raw"}))
		if commit {
			mock.ExpectCommit()
		} else {
			mock.ExpectRollback()
		}
	}
	invoke := func(args string) (ToolOutcome, error) {
		return s.callEntryRuleTool(context.Background(), 1, 2, ToolCall{Function: ToolFunction{Name: "entry_rule_detail", Arguments: args}})
	}
	expectLoad(`{"id":1}`, true)
	first, err := invoke(`{"kind":"policy","target_id":1}`)
	if err != nil {
		t.Fatal(err)
	}
	var data struct {
		Rule struct {
			Revision string `json:"revision"`
		} `json:"rule"`
	}
	json.Unmarshal([]byte(first.Content), &data)
	revision := data.Rule.Revision
	for _, args := range []string{`{"kind":"policy","target_id":1,"offset":1}`, `{"kind":"policy","target_id":1,"revision":"invalid"}`} {
		if _, err = invoke(args); !errors.Is(err, ErrValidation) {
			t.Fatal(err)
		}
	}
	expectLoad(`{"id":1}`, true)
	if _, err = invoke(fmt.Sprintf(`{"kind":"policy","target_id":1,"section":"members","offset":1,"revision":%q}`, revision)); err != nil {
		t.Fatal(err)
	}
	expectLoad(`{"id":1,"changed":true}`, false)
	if _, err = invoke(fmt.Sprintf(`{"kind":"policy","target_id":1,"section":"members","offset":1,"revision":%q}`, revision)); !errors.Is(err, ErrConflict) {
		t.Fatal("changed page leaked", err)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestEntryCoverageDefaultKeepsTCPAnomaliesAndPinsFilterMembership(t *testing.T) {
	v := admin.EntryMonitorInventoryOverview{}
	for i, status := range []string{"healthy", "unhealthy", "partial"} {
		v.Sources = append(v.Sources, admin.EntryMonitorInventorySource{Key: fmt.Sprint(i), CoverageStatus: "ready", Status: status, Issues: []string{}})
	}
	decode := func(out any) (string, int64) {
		t.Helper()
		raw := requireBoundedJSON(t, out)
		var response struct {
			Revision string                          `json:"revision"`
			Page     pageDTO[entryCoverageSourceDTO] `json:"page"`
		}
		json.Unmarshal(raw, &response)
		return response.Revision, response.Page.Total
	}
	out, err := projectEntryInventory(v, entryInspectionArgs{Limit: 1}, "entry_coverage")
	if err != nil {
		t.Fatal(err)
	}
	rev, total := decode(out)
	if total != 2 {
		t.Fatal("TCP anomalies lost", total)
	}
	no := false
	all, err := projectEntryInventory(v, entryInspectionArgs{Limit: 50, GapsOnly: &no}, "entry_coverage")
	if err != nil {
		t.Fatal(err)
	}
	_, total = decode(all)
	if total != 3 {
		t.Fatal(total)
	}
	v.Sources[0].Status = "unhealthy"
	if _, err = projectEntryInventory(v, entryInspectionArgs{Limit: 1, Offset: 1, Revision: rev}, "entry_coverage"); !errors.Is(err, ErrConflict) {
		t.Fatal("filtered membership shifted without conflict", err)
	}
}

func TestPanelMutationInlineReceiptFitsAutomaticSummary(t *testing.T) {
	small := json.RawMessage(`{"committed":true,"revision":8}`)
	if panelMutationNeedsChunk(PanelOperation{}, small) || !panelMutationNeedsChunk(PanelOperation{AdminOnly: true}, small) || !panelMutationNeedsChunk(PanelOperation{}, json.RawMessage(`{"text":"`+strings.Repeat("x", 5000)+`"}`)) {
		t.Fatal("mutation chunk thresholds or protected result policy")
	}
	escaped := json.RawMessage(`{"text":"` + strings.Repeat("<", 2000) + `"}`)
	if !panelMutationNeedsChunk(PanelOperation{}, escaped) {
		t.Fatal("HTML escaping could inflate and truncate inline output")
	}
	s := NewService(config.Config{}, nil, nil)
	defer s.Close()
	edge, _ := json.Marshal(map[string]string{"text": strings.Repeat("x", 3980)})
	if panelMutationNeedsChunk(PanelOperation{}, edge) {
		t.Fatal("small result should fit")
	}
	inline, err := s.savePanelResult(context.Background(), 1, 2, "entry_monitors.save", edge, false)
	if err != nil {
		t.Fatal(err)
	}
	var summary struct {
		Output    string `json:"output"`
		Truncated bool   `json:"output_truncated"`
	}
	if err = json.Unmarshal([]byte(automaticActionSummary(Action{Kind: "panel_operation", Status: "succeeded", Output: inline})), &summary); err != nil || summary.Truncated || summary.Output != inline {
		t.Fatal("unrecoverable inline truncation", err)
	}
}
