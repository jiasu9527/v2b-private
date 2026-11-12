package aiops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// This test exercises the real chat/action authorization path, not the evidence
// business engine. Its backend is deliberately inert: admin package PostgreSQL
// tests cover actual frozen lists and route changes separately.
func investigationConversationRuntime() *fixturePanelOperations {
	ops := []PanelOperation{}
	for _, suffix := range []string{"freeze", "investigation_start", "split_preview", "split_execute", "investigation_confirm", "investigation_next"} {
		op := fixtureOperation("entry_evidence."+suffix, "/server/client-entry-user-policy/investigations/"+strings.ReplaceAll(suffix, "_", "-"), suffix != "split_preview", false)
		op.Category = "entries"
		ops = append(ops, op)
	}
	return &fixturePanelOperations{ops: ops, data: json.RawMessage(`{"case_id":1,"revision":1,"supported":true,"saved":true}`)}
}

func investigationConversationSend(t *testing.T, f staffAIFixture, relayURL, mode string) SendMessageResult {
	t.Helper()
	ctx := context.Background()
	if _, err := f.s.SaveConfig(ctx, f.admin, ConfigSaveRequest{Enabled: true, BaseURL: relayURL + "/v1", APIKey: "fixture", Models: DefaultModels}); err != nil {
		t.Fatal(err)
	}
	out, err := f.s.SendMessage(ctx, f.staff, SendMessageRequest{
		Model: DefaultModels[0], AccessMode: mode,
		Content:   "这个入口已核实被墙，请按实际下发名单二分排查，健康池取入口；已有核实记录按记录继续。",
		RequestID: fmt.Sprintf("investigation-chat-%d", time.Now().UnixNano()),
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestEntryInvestigationConversationPostgres(t *testing.T) {
	t.Run("full chat chains investigation actions without manual pending", func(t *testing.T) {
		f := setupStaffAIFixture(t)
		runtime := investigationConversationRuntime()
		f.s.SetPanelOperations(runtime)
		steps := []string{"freeze", "investigation_start", "split_preview", "split_execute", "investigation_confirm", "investigation_next"}
		var rounds atomic.Int32
		relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var request completionRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
				return
			}
			n := int(rounds.Add(1))
			if n > 1 && n <= len(steps)+1 {
				previous := steps[n-2]
				got := sshRecoveryToolResult(t, request, previous)
				if previous == "split_preview" {
					if got["status"] == "error" || got["status"] == "pending" || got["status"] == "skipped" {
						t.Error("preview did not reach next model round", got)
					}
				} else if got["status"] != "succeeded" {
					t.Error("investigation action failed or unexpectedly requires approval", previous, got)
				}
			}
			if n > len(steps) {
				sshRecoveryReply(w)
				return
			}
			suffix, tool := steps[n-1], "propose_panel_operation"
			if suffix == "split_preview" {
				tool = "panel_query"
			}
			sshRecoveryReply(w, panelRecoveryCall(suffix, tool, "entry_evidence."+suffix, `{"value":"fixture"}`))
		}))
		defer relay.Close()
		started := investigationConversationSend(t, f, relay.URL, "full")
		detail := sshRecoveryWait(t, f, started.Session.ID)
		if detail.Turns[0].Status != "completed" || rounds.Load() != int32(len(steps)+1) || runtime.count() != len(steps) {
			t.Fatalf("turn=%+v rounds=%d backend calls=%d", detail.Turns[0], rounds.Load(), runtime.count())
		}
		actions, err := f.s.ListActions(context.Background(), f.staff, started.Session.ID)
		if err != nil || len(actions) != len(steps)-1 {
			t.Fatal("unexpected action audit", len(actions), err)
		}
		seen := map[string]bool{}
		for _, a := range actions {
			if a.Status != "succeeded" || a.ApprovalMode != "full_access" || a.ApprovedBy != f.staff || a.AuthorizationTurnID != started.Turn.ID {
				t.Fatal("action bypassed normal authorization/audit", a)
			}
			seen[a.TargetType] = true
		}
		for _, suffix := range steps {
			if suffix != "split_preview" && !seen["entry_evidence."+suffix] {
				t.Fatal("missing action", suffix)
			}
		}
	})

	t.Run("standard mode still waits for approval", func(t *testing.T) {
		f := setupStaffAIFixture(t)
		runtime := investigationConversationRuntime()
		f.s.SetPanelOperations(runtime)
		var rounds atomic.Int32
		relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rounds.Add(1)
			sshRecoveryReply(w, panelRecoveryCall("start", "propose_panel_operation", "entry_evidence.investigation_start", `{"value":"fixture"}`))
		}))
		defer relay.Close()
		started := investigationConversationSend(t, f, relay.URL, "standard")
		detail := sshRecoveryWait(t, f, started.Session.ID)
		if detail.Turns[0].Status != "pending" || rounds.Load() != 1 || runtime.count() != 0 {
			t.Fatalf("standard executed without approval: turn=%+v rounds=%d calls=%d", detail.Turns[0], rounds.Load(), runtime.count())
		}
		actions, err := f.s.ListActions(context.Background(), f.staff, started.Session.ID)
		if err != nil || len(actions) != 1 || actions[0].Status != "pending" {
			t.Fatal(actions, err)
		}
	})

	t.Run("ordinary account cannot ask AI to investigate", func(t *testing.T) {
		f := setupStaffAIFixture(t)
		runtime := investigationConversationRuntime()
		f.s.SetPanelOperations(runtime)
		_, err := f.s.SendMessage(context.Background(), f.user, SendMessageRequest{
			Model: DefaultModels[0], AccessMode: "full", Content: "帮我二分排查", RequestID: "unprivileged-investigation",
		})
		if !errors.Is(err, ErrForbidden) || runtime.count() != 0 {
			t.Fatal("ordinary account was allowed", err, runtime.count())
		}
	})

	t.Run("revoking full access stops further investigation writes", func(t *testing.T) {
		f := setupStaffAIFixture(t)
		runtime := investigationConversationRuntime()
		f.s.SetPanelOperations(runtime)
		reading, release := make(chan struct{}), make(chan struct{})
		var rounds atomic.Int32
		relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch rounds.Add(1) {
			case 1:
				sshRecoveryReply(w, panelRecoveryCall("start", "propose_panel_operation", "entry_evidence.investigation_start", `{"value":"fixture"}`))
			case 2:
				close(reading)
				select {
				case <-release:
				case <-r.Context().Done():
				}
				sshRecoveryReply(w, panelRecoveryCall("execute-after-revoke", "propose_panel_operation", "entry_evidence.split_execute", `{"value":"fixture"}`))
			default:
				t.Error("revoked turn continued")
				sshRecoveryReply(w)
			}
		}))
		defer relay.Close()
		defer close(release)
		started := investigationConversationSend(t, f, relay.URL, "full")
		select {
		case <-reading:
		case <-time.After(5 * time.Second):
			t.Fatal("initial investigation action unexpectedly stopped for approval")
		}
		_, err := f.s.SetAccessMode(context.Background(), f.staff, AccessModeRequest{ID: started.Session.ID, Mode: "standard", ExpectedVersion: &started.Session.AccessVersion})
		if err != nil {
			t.Fatal(err)
		}
		detail := sshRecoveryWait(t, f, started.Session.ID)
		if detail.Turns[0].Status != "interrupted" || runtime.count() != 1 || rounds.Load() != 2 {
			t.Fatalf("revocation ignored: turn=%+v calls=%d rounds=%d", detail.Turns[0], runtime.count(), rounds.Load())
		}
	})
}
