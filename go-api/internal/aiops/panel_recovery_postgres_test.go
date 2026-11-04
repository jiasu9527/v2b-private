package aiops

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func panelRecoveryCall(id, name, operation, parameters string) ToolCall {
	args := map[string]any{"operation": operation, "parameters": json.RawMessage(parameters)}
	if name == "propose_panel_operation" {
		args["reason"] = "fixture verify and repair"
	}
	raw, _ := json.Marshal(args)
	return ToolCall{ID: id, Type: "function", Function: ToolFunction{Name: name, Arguments: string(raw)}}
}

func panelRecoveryRuntime(first error) *fixturePanelOperations {
	var calls atomic.Int32
	return &fixturePanelOperations{
		ops:  []PanelOperation{fixtureOperation("dns.record_save", "/dns/save", true, false), fixtureOperation("dns.records", "/dns/records", false, false), fixtureOperation("entry_monitors.list", "/entry-monitors/list", false, false)},
		data: json.RawMessage(`{"value":"fixture verified state"}`),
		onExecute: func(context.Context) error {
			if calls.Add(1) == 1 {
				return first
			}
			return nil
		},
	}
}

func TestAIPanelRecoveryPreflightPostgres(t *testing.T) {
	f := setupStaffAIFixture(t)
	runtime := panelRecoveryRuntime(&PanelOperationError{Status: 500, Code: "dns_record_line_unavailable", NotExecuted: true})
	f.s.SetPanelOperations(runtime)
	var rounds atomic.Int32
	first := panelRecoveryCall("preflight", "propose_panel_operation", "dns.record_save", `{"value":"unavailable"}`)
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request completionRequest
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			t.Error("invalid relay request")
			w.WriteHeader(400)
			return
		}
		switch rounds.Add(1) {
		case 1:
			sshRecoveryReply(w, first, panelRecoveryCall("stale-batch", "propose_panel_operation", "dns.record_save", `{"value":"must not run"}`))
		case 2:
			if got := sshRecoveryToolResult(t, request, "preflight"); got["status"] != "failed" || got["recovery_instruction"] != panelPreflightRecoveryInstruction {
				t.Error("missing preflight result", got)
			}
			if got := sshRecoveryToolResult(t, request, "stale-batch"); got["status"] != "skipped" {
				t.Error("stale batch executed", got)
			}
			replay := first
			replay.ID = "same-parameters"
			replay.Function.Arguments = `{ "reason":"a different reason", "parameters": {"value":"unavailable"}, "operation":"dns.record_save" }`
			sshRecoveryReply(w, replay, panelRecoveryCall("replay-batch", "panel_query", "dns.records", `{}`))
		case 3:
			if got := sshRecoveryToolResult(t, request, "same-parameters"); got["code"] != "panel_operation_already_failed" {
				t.Error("same request retried", got)
			}
			if got := sshRecoveryToolResult(t, request, "replay-batch"); got["status"] != "skipped" {
				t.Error("repeat batch not skipped", got)
			}
			sshRecoveryReply(w, panelRecoveryCall("inspect", "panel_query", "dns.records", `{}`))
		case 4:
			sshRecoveryReply(w, panelRecoveryCall("corrected", "propose_panel_operation", "dns.record_save", `{"value":"available"}`))
		case 5:
			if got := sshRecoveryToolResult(t, request, "corrected"); got["status"] != "succeeded" {
				t.Error("corrected request failed", got)
			}
			sshRecoveryReply(w)
		default:
			t.Error("unexpected relay call")
			sshRecoveryReply(w)
		}
	}))
	defer relay.Close()
	started := sshRecoverySend(t, f, relay.URL)
	detail := sshRecoveryWait(t, f, started.Session.ID)
	if detail.Turns[0].Status != "completed" || rounds.Load() != 5 || runtime.count() != 3 {
		t.Fatalf("turn=%+v rounds=%d executes=%d", detail.Turns[0], rounds.Load(), runtime.count())
	}
	actions, err := f.s.ListActions(context.Background(), f.staff, started.Session.ID)
	if err != nil || len(actions) != 2 {
		t.Fatal("unexpected actions", actions, err)
	}
	statuses := []string{actions[0].Status, actions[1].Status}
	sort.Strings(statuses)
	if strings.Join(statuses, ",") != "failed,succeeded" {
		t.Fatal("action status rewritten", statuses)
	}
}

func TestAIPanelRecoveryUnknownReadOnlyPostgres(t *testing.T) {
	f := setupStaffAIFixture(t)
	runtime := panelRecoveryRuntime(&PanelOperationError{Status: 500})
	for i, path := range []string{"/dns-failover/entry-monitors", "/server/client-entry/fetch", "/server/client-entry/resolve"} {
		runtime.ops = append(runtime.ops, fixtureOperation(fmt.Sprintf("side_effect_alias.read%d", i), path, false, false))
	}
	for _, name := range []string{"future.read", "users.list", "entry_evidence.case", "entry_evidence.cases"} {
		runtime.ops = append(runtime.ops, fixtureOperation(name, "/fixture/read", false, false))
	}
	runtime.ops = append(runtime.ops, fixtureOperation("dns.config", "/dns-failover/entry-monitors", false, false))
	f.s.SetPanelOperations(runtime)
	var blocked []ToolCall
	for name, capability := range toolCapabilities {
		if capability.ActionKind != "" || name == "knowledge_save" || name == "knowledge_create" || name == "entry_probe_targets" || name == "entry_split_options" {
			blocked = append(blocked, ToolCall{ID: "blocked-" + name, Type: "function", Function: ToolFunction{Name: name, Arguments: `{}`}})
		}
	}
	blocked = append(blocked, panelRecoveryCall("disguised-write", "panel_query", "dns.record_save", `{"value":"bad"}`))
	blocked = append(blocked, panelRecoveryCall("refreshing-read", "panel_query", "entry_monitors.list", `{}`))
	for _, name := range []string{"future.read", "users.list", "entry_evidence.case", "entry_evidence.cases", "dns.config"} {
		blocked = append(blocked, panelRecoveryCall("unreviewed-"+name, "panel_query", name, `{}`))
	}
	for i := 0; i < 3; i++ {
		blocked = append(blocked, panelRecoveryCall(fmt.Sprintf("refresh-alias-%d", i), "panel_query", fmt.Sprintf("side_effect_alias.read%d", i), `{}`))
	}
	blocked = append(blocked, ToolCall{ID: "future", Type: "function", Function: ToolFunction{Name: "future_tool", Arguments: `{}`}})
	var rounds atomic.Int32
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request completionRequest
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			t.Error("invalid relay request")
			w.WriteHeader(400)
			return
		}
		switch rounds.Add(1) {
		case 1:
			sshRecoveryReply(w, panelRecoveryCall("unknown", "propose_panel_operation", "dns.record_save", `{"value":"uncertain"}`), panelRecoveryCall("stale-batch", "panel_query", "dns.records", `{}`))
		case 2:
			if got := sshRecoveryToolResult(t, request, "unknown"); got["status"] != "unknown" || got["recovery_instruction"] != panelReadOnlyRecoveryInstruction {
				t.Error("unknown recovery missing", got)
			}
			if got := sshRecoveryToolResult(t, request, "stale-batch"); got["status"] != "skipped" {
				t.Error("stale batch not skipped", got)
			}
			sshRecoveryReply(w, append(blocked, panelRecoveryCall("verify", "panel_query", "dns.records", `{}`))...)
		case 3:
			for _, call := range blocked {
				if got := sshRecoveryToolResult(t, request, call.ID); got["code"] != "panel_recovery_read_only" {
					t.Error("write not blocked", call.Function.Name, got)
				}
			}
			got := sshRecoveryToolResult(t, request, "verify")
			if got["status"] == "error" || got["status"] == "skipped" {
				t.Error("readonly verification denied", got)
			}
			sshRecoveryReply(w, panelRecoveryCall("still-blocked", "propose_panel_operation", "dns.record_save", `{"value":"corrected after read"}`), panelRecoveryCall("verify-again", "panel_query", "dns.records", `{}`))
		case 4:
			if got := sshRecoveryToolResult(t, request, "still-blocked"); got["code"] != "panel_recovery_read_only" {
				t.Error("readonly mode was not sticky", got)
			}
			found := false
			for _, m := range request.Messages {
				found = found || (m.Role == "system" && strings.Contains(m.Content, panelReadOnlyRecoveryInstruction))
			}
			if !found {
				t.Error("readonly system prompt lost")
			}
			sshRecoveryReply(w)
		case 5:
			// A new user turn may re-plan; readonly mode is deliberately per-turn.
			if len(request.Messages) == 0 || request.Messages[0].Role != "system" || !strings.Contains(request.Messages[0].Content, historicalActionRecoveryPrompt) {
				t.Error("new turn lacks authoritative historical-action boundary")
			}
			sshRecoveryReply(w, panelRecoveryCall("new-turn-inspect", "panel_query", "dns.records", `{}`))
		case 6:
			if got := sshRecoveryToolResult(t, request, "new-turn-inspect"); got["status"] == "error" || got["status"] == "skipped" {
				t.Error("new turn could not verify current state", got)
			}
			sshRecoveryReply(w, panelRecoveryCall("new-turn-write", "propose_panel_operation", "dns.record_save", `{"value":"explicitly replanned"}`))
		case 7:
			if got := sshRecoveryToolResult(t, request, "new-turn-write"); got["status"] != "succeeded" {
				t.Error("new turn remained read-only", got)
			}
			sshRecoveryReply(w)
		default:
			t.Error("unexpected relay call")
			sshRecoveryReply(w)
		}
	}))
	defer relay.Close()
	started := sshRecoverySend(t, f, relay.URL)
	detail := sshRecoveryWait(t, f, started.Session.ID)
	if detail.Turns[0].Status != "failed" || !strings.Contains(detail.Turns[0].Error, "待核对") || rounds.Load() != 4 || runtime.count() != 3 {
		t.Fatalf("turn=%+v rounds=%d executes=%d", detail.Turns[0], rounds.Load(), runtime.count())
	}
	actions, err := f.s.ListActions(context.Background(), f.staff, started.Session.ID)
	if err != nil || len(actions) != 1 || actions[0].Status != "unknown" {
		t.Fatal("unknown action changed", actions, err)
	}
	_, err = f.s.SendMessage(context.Background(), f.staff, SendMessageRequest{SessionID: started.Session.ID, Model: DefaultModels[0], Content: "已核对现状，请按新参数执行", RequestID: fmt.Sprintf("panel-replan-%d", time.Now().UnixNano()), AccessMode: "full", AccessVersion: &detail.Session.AccessVersion})
	if err != nil {
		t.Fatal(err)
	}
	detail = sshRecoveryWait(t, f, started.Session.ID)
	if detail.Turns[0].Status != "completed" || rounds.Load() != 7 || runtime.count() != 5 {
		t.Fatalf("new turn=%+v rounds=%d executes=%d", detail.Turns[0], rounds.Load(), runtime.count())
	}
	actions, err = f.s.ListActions(context.Background(), f.staff, started.Session.ID)
	if err != nil || len(actions) != 2 {
		t.Fatal("unexpected replanned actions", actions, err)
	}
	statuses := []string{actions[0].Status, actions[1].Status}
	sort.Strings(statuses)
	if strings.Join(statuses, ",") != "succeeded,unknown" {
		t.Fatal("historical unknown was rewritten", statuses)
	}
}

func TestAIPanelRecoveryStopOrRevokePostgres(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		t.Run(fmt.Sprintf("revoke=%v", revoke), func(t *testing.T) {
			f := setupStaffAIFixture(t)
			runtime := panelRecoveryRuntime(&PanelOperationError{Status: 500})
			f.s.SetPanelOperations(runtime)
			reading, release := make(chan struct{}), make(chan struct{})
			var rounds atomic.Int32
			relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch rounds.Add(1) {
				case 1:
					sshRecoveryReply(w, panelRecoveryCall("unknown", "propose_panel_operation", "dns.record_save", `{"value":"uncertain"}`))
				case 2:
					close(reading)
					<-release
					sshRecoveryReply(w, panelRecoveryCall("read-after-stop", "panel_query", "dns.records", `{}`))
				default:
					t.Error("stopped turn resumed model requests")
					sshRecoveryReply(w)
				}
			}))
			defer relay.Close()
			defer close(release)
			started := sshRecoverySend(t, f, relay.URL)
			select {
			case <-reading:
			case <-time.After(5 * time.Second):
				t.Fatal("readonly phase did not start")
			}
			if revoke {
				_, err := f.s.SetAccessMode(context.Background(), f.staff, AccessModeRequest{ID: started.Session.ID, Mode: "standard", ExpectedVersion: &started.Session.AccessVersion})
				if err != nil {
					t.Fatal(err)
				}
			} else if err := f.s.StopSession(context.Background(), f.staff, started.Session.ID); err != nil {
				t.Fatal(err)
			}
			detail := sshRecoveryWait(t, f, started.Session.ID)
			if detail.Turns[0].Status != "interrupted" || rounds.Load() != 2 || runtime.count() != 1 {
				t.Fatalf("stop failed turn=%+v rounds=%d executes=%d", detail.Turns[0], rounds.Load(), runtime.count())
			}
		})
	}
}

func TestAIPoolBulkDeletePreflightCorrectionPostgres(t *testing.T) {
	f := setupStaffAIFixture(t)
	runtime := panelRecoveryRuntime(&PanelOperationError{Status: 400, Code: "ip_pool_invalid_arguments", NotExecuted: true})
	runtime.ops = []PanelOperation{fixtureOperation("ip_pool.delete_many", "/ip-pool/delete", true, false), fixtureOperation("ip_pool.list", "/ip-pool/list", false, false)}
	f.s.SetPanelOperations(runtime)
	var rounds atomic.Int32
	first := panelRecoveryCall("preflight", "propose_panel_operation", "ip_pool.delete_many", `{"scope":"selected","ids":[]}`)
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request completionRequest
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			t.Error("invalid relay request")
			w.WriteHeader(400)
			return
		}
		switch rounds.Add(1) {
		case 1:
			sshRecoveryReply(w, first, panelRecoveryCall("stale-batch", "propose_panel_operation", "ip_pool.delete_many", `{"scope":"all"}`))
		case 2:
			if got := sshRecoveryToolResult(t, request, "preflight"); got["status"] != "failed" || got["recovery_instruction"] != panelPreflightRecoveryInstruction {
				t.Error("missing preflight result", got)
			}
			if got := sshRecoveryToolResult(t, request, "stale-batch"); got["status"] != "skipped" {
				t.Error("stale batch executed", got)
			}
			replay := first
			replay.ID = "same-parameters"
			replay.Function.Arguments = `{ "reason":"a different reason", "parameters": {"scope":"selected","ids":[]}, "operation":"ip_pool.delete_many" }`
			sshRecoveryReply(w, replay, panelRecoveryCall("replay-batch", "panel_query", "ip_pool.list", `{}`))
		case 3:
			if got := sshRecoveryToolResult(t, request, "same-parameters"); got["code"] != "panel_operation_already_failed" {
				t.Error("same request retried", got)
			}
			if got := sshRecoveryToolResult(t, request, "replay-batch"); got["status"] != "skipped" {
				t.Error("repeat batch not skipped", got)
			}
			sshRecoveryReply(w, panelRecoveryCall("inspect", "panel_query", "ip_pool.list", `{}`))
		case 4:
			sshRecoveryReply(w, panelRecoveryCall("corrected", "propose_panel_operation", "ip_pool.delete_many", `{"scope":"selected","ids":[180,181]}`))
		case 5:
			if got := sshRecoveryToolResult(t, request, "corrected"); got["status"] != "succeeded" {
				t.Error("corrected request failed", got)
			}
			sshRecoveryReply(w)
		default:
			t.Error("unexpected relay call")
			sshRecoveryReply(w)
		}
	}))
	defer relay.Close()
	started := sshRecoverySend(t, f, relay.URL)
	detail := sshRecoveryWait(t, f, started.Session.ID)
	if detail.Turns[0].Status != "completed" || rounds.Load() != 5 || runtime.count() != 3 {
		t.Fatalf("turn=%+v rounds=%d executes=%d", detail.Turns[0], rounds.Load(), runtime.count())
	}
	actions, err := f.s.ListActions(context.Background(), f.staff, started.Session.ID)
	if err != nil || len(actions) != 2 {
		t.Fatal("unexpected actions", actions, err)
	}
	statuses := []string{actions[0].Status, actions[1].Status}
	sort.Strings(statuses)
	if strings.Join(statuses, ",") != "failed,succeeded" {
		t.Fatal("action status rewritten", statuses)
	}
}

func TestAIEntryOrderPreflightCorrectionPostgres(t *testing.T) {
	for _, code := range []string{"entry_order_invalid_arguments", "entry_order_conflict"} {
		t.Run(code, func(t *testing.T) {
			f := setupStaffAIFixture(t)
			runtime := panelRecoveryRuntime(&PanelOperationError{Status: 409, Code: code, NotExecuted: true})
			runtime.ops = []PanelOperation{fixtureOperation("entry_rules.move", "/server/client-entry-user-policy/move", true, false), fixtureOperation("entry_rules.order", "/server/client-entry-user-policy/order", false, false)}
			f.s.SetPanelOperations(runtime)
			var rounds atomic.Int32
			relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request completionRequest
				if json.NewDecoder(r.Body).Decode(&request) != nil {
					t.Error("invalid relay request")
					w.WriteHeader(400)
					return
				}
				switch rounds.Add(1) {
				case 1:
					sshRecoveryReply(w, panelRecoveryCall("stale", "propose_panel_operation", "entry_rules.move", `{"items":[{"kind":"split_group","id":2}],"after":{"kind":"policy","id":2},"expected_revision":"old"}`))
				case 2:
					got := sshRecoveryToolResult(t, request, "stale")
					if got["status"] != "failed" || got["recovery_instruction"] != panelPreflightRecoveryInstruction {
						t.Error("preflight failure stopped task", got)
					}
					sshRecoveryReply(w, panelRecoveryCall("inspect", "panel_query", "entry_rules.order", `{}`))
				case 3:
					got := sshRecoveryToolResult(t, request, "inspect")
					if got["status"] == "error" || got["status"] == "skipped" {
						t.Error("inspection blocked", got)
					}
					sshRecoveryReply(w, panelRecoveryCall("corrected", "propose_panel_operation", "entry_rules.move", `{"items":[{"kind":"split_group","id":2}],"after":{"kind":"policy","id":2},"expected_revision":"refreshed"}`))
				case 4:
					if got := sshRecoveryToolResult(t, request, "corrected"); got["status"] != "succeeded" {
						t.Error("corrected move blocked", got)
					}
					sshRecoveryReply(w)
				default:
					t.Error("unexpected relay call")
					sshRecoveryReply(w)
				}
			}))
			defer relay.Close()
			started := sshRecoverySend(t, f, relay.URL)
			detail := sshRecoveryWait(t, f, started.Session.ID)
			if detail.Turns[0].Status != "completed" || rounds.Load() != 4 || runtime.count() != 3 {
				t.Fatal(detail.Turns[0], rounds.Load(), runtime.count())
			}
			actions, err := f.s.ListActions(context.Background(), f.staff, started.Session.ID)
			if err != nil || len(actions) != 2 {
				t.Fatal(actions, err)
			}
			statuses := []string{actions[0].Status, actions[1].Status}
			sort.Strings(statuses)
			if strings.Join(statuses, ",") != "failed,succeeded" {
				t.Fatal(statuses)
			}
		})
	}
}
