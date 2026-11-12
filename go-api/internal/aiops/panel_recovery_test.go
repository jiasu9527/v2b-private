package aiops

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAIPanelRecoveryOnlyTrustedActionMetadata(t *testing.T) {
	marker := `{"execution_state":"not_executed","code":"dns_domain_metadata","recovery":"panel_preflight"}`
	for _, tc := range []struct {
		name string
		a    Action
		want panelRecoveryKind
	}{
		{"preflight", Action{Kind: "panel_operation", Status: "failed", Output: marker}, panelRecoveryPreflight},
		{"entry order validation", Action{Kind: "panel_operation", Status: "failed", Output: strings.ReplaceAll(marker, "dns_domain_metadata", "entry_order_invalid_arguments")}, panelRecoveryPreflight},
		{"entry order conflict", Action{Kind: "panel_operation", Status: "failed", Output: strings.ReplaceAll(marker, "dns_domain_metadata", "entry_order_conflict")}, panelRecoveryPreflight},
		{"ip pool validation", Action{Kind: "panel_operation", Status: "failed", Output: strings.ReplaceAll(marker, "dns_domain_metadata", "ip_pool_invalid_arguments")}, panelRecoveryPreflight},
		{"node validation", Action{Kind: "panel_operation", Status: "failed", Output: strings.ReplaceAll(marker, "dns_domain_metadata", "node_invalid_arguments")}, panelRecoveryPreflight},
		{"uncertain panel", Action{Kind: "panel_operation", Status: "unknown"}, panelRecoveryReadOnly},
		{"error text not trusted", Action{Kind: "panel_operation", Status: "failed", Error: marker}, panelRecoveryNone},
		{"forbidden", Action{Kind: "panel_operation", Status: "failed", Output: strings.ReplaceAll(marker, "dns_domain_metadata", "forbidden")}, panelRecoveryNone},
		{"unrecognized code", Action{Kind: "panel_operation", Status: "failed", Output: strings.ReplaceAll(marker, "dns_domain_metadata", "custom_safe_failure")}, panelRecoveryNone},
		{"missing marker", Action{Kind: "panel_operation", Status: "failed", Output: `{"execution_state":"not_executed","code":"dns_domain_metadata"}`}, panelRecoveryNone},
		{"ssh unknown", Action{Kind: "ssh_command", Status: "unknown", Output: marker}, panelRecoveryNone},
		{"ssh failed", Action{Kind: "ssh_command", Status: "failed", Output: marker}, panelRecoveryNone},
		{"rejected", Action{Kind: "panel_operation", Status: "rejected", Output: marker}, panelRecoveryNone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.a.ID = 35
			out, err := automaticActionOutcome(tc.a)
			if out.PanelRecovery != tc.want || (err == nil) != (tc.want != panelRecoveryNone) {
				t.Fatalf("recovery=%v err=%v", out.PanelRecovery, err)
			}
			if out.ActionID != 35 || out.Pending {
				t.Fatal("lost action identity or pending state")
			}
			raw, _ := json.Marshal(out)
			if strings.Contains(string(raw), "PanelRecovery") {
				t.Fatal("internal marker leaked")
			}
		})
	}
}

func TestAIPanelRecoveryOperationKeyCanonicalAndLossless(t *testing.T) {
	call := func(args string) ToolCall {
		return ToolCall{Function: ToolFunction{Name: "propose_panel_operation", Arguments: args}}
	}
	a := call(`{"operation":"dns.record_save","parameters":{"z":{"b":2,"a":1},"id":9007199254740993},"reason":"one"}`)
	b := call(`{ "reason":"two", "parameters":{"id":9007199254740993,"z":{"a":1,"b":2}},"operation":"dns.record_save"}`)
	key := panelRecoveryOperationKey(a)
	if key == "" || key != panelRecoveryOperationKey(b) {
		t.Fatal("JSON ordering or reason bypassed exact replay guard")
	}
	for _, raw := range []string{strings.ReplaceAll(a.Function.Arguments, "9007199254740993", "9007199254740992"), strings.ReplaceAll(a.Function.Arguments, "dns.record_save", "dns.record_delete")} {
		if key == panelRecoveryOperationKey(call(raw)) {
			t.Fatal("different operation/parameters collapsed")
		}
	}
	for _, raw := range []string{`{}`, `null`, `{"operation":"dns.record_save","parameters":[]}`, `{"operation":"dns.record_save","parameters":null}`} {
		if panelRecoveryOperationKey(call(raw)) != "" {
			t.Fatal("invalid arguments produced replay key")
		}
	}
}

func TestAIPanelRecoveryReadOnlyFailClosed(t *testing.T) {
	s := &Service{}
	s.SetPanelOperations(&fixturePanelOperations{ops: []PanelOperation{fixtureOperation("dns.records", "/dns/records", false, false), fixtureOperation("dns.record_save", "/dns/save", true, false), fixtureOperation("entry_monitors.list", "/entry-monitors/list", false, false)}})
	runtime := s.getPanelOperations().(*fixturePanelOperations)
	runtime.ops = append(runtime.ops, fixtureOperation("client_entries.list", "/client-entries/list", false, false), fixtureOperation("client_entries.resolve", "/client-entries/resolve", false, false))
	for _, name := range []string{"future.read", "users.list", "entry_evidence.case", "entry_evidence.cases"} {
		runtime.ops = append(runtime.ops, fixtureOperation(name, "/fixture/read", false, false))
	}
	for name, capability := range toolCapabilities {
		call := ToolCall{Function: ToolFunction{Name: name, Arguments: `{}`}}
		if capability.ActionKind != "" || name == "knowledge_save" || name == "knowledge_create" || name == "entry_probe_targets" || name == "entry_split_options" {
			if s.panelRecoveryReadOnlyCall(call) {
				t.Fatal("write tool permitted", name)
			}
		} else if name != "panel_query" && !s.panelRecoveryReadOnlyCall(call) {
			t.Fatal("read tool denied", name)
		}
	}
	for _, name := range []string{"future_read_sounding_tool", "ssh_status", "monitor_run", "entry_probe_run"} {
		if s.panelRecoveryReadOnlyCall(ToolCall{Function: ToolFunction{Name: name, Arguments: `{}`}}) {
			t.Fatal("unreviewed/active tool allowed", name)
		}
	}
	for _, tc := range []struct {
		op   string
		want bool
	}{{"dns.records", true}, {"dns.record_save", false}, {"dns.not_found", false}, {"entry_monitors.list", false}, {"client_entries.list", false}, {"client_entries.resolve", false}, {"future.read", false}, {"users.list", false}, {"entry_evidence.case", false}, {"entry_evidence.cases", true}} {
		raw, _ := json.Marshal(map[string]any{"operation": tc.op, "parameters": map[string]any{}})
		if s.panelRecoveryReadOnlyCall(ToolCall{Function: ToolFunction{Name: "panel_query", Arguments: string(raw)}}) != tc.want {
			t.Fatal("operation metadata not enforced", tc.op)
		}
	}
	for _, path := range []string{"/dns-failover/entry-monitors", "/server/client-entry/fetch", "/server/client-entry/resolve"} {
		runtime.ops = []PanelOperation{fixtureOperation("dns.records", path, false, false)}
		if s.panelRecoveryReadOnlyCall(ToolCall{Function: ToolFunction{Name: "panel_query", Arguments: `{"operation":"dns.records","parameters":{}}`}}) {
			t.Fatal("operation alias bypassed side-effect exclusion", path)
		}
	}
	for _, name := range []string{"entry_evidence.cases", "entry_evidence.investigation", "entry_evidence.investigation_list", "entry_evidence.delivery_stats", "entry_evidence.delivery_users", "entry_evidence.delivery_versions", "dns.config", "dns.domains", "dns.records", "dns.record_types", "dns.record_lines", "nodes.list", "node_groups.list", "node_routes.list", "entry_rules.list", "entry_rules.simulate", "entry_split.users", "entry_collections.list", "entry_monitors.runs", "ip_pool.list", "users.detail", "orders.list", "orders.detail", "plans.list", "notices.list", "tickets.list", "config.read"} {
		for _, mutating := range []bool{false, true} {
			runtime.ops = []PanelOperation{fixtureOperation(name, "/fixture/read", mutating, false)}
			args, _ := json.Marshal(map[string]any{"operation": name, "parameters": map[string]any{}})
			if s.panelRecoveryReadOnlyCall(ToolCall{Function: ToolFunction{Name: "panel_query", Arguments: string(args)}}) == mutating {
				t.Fatal("reviewed operation ignored read/write metadata", name, mutating)
			}
		}
	}
}
