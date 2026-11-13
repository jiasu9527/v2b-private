package aiops

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"forest/go-api/internal/admin"
)

type entryStatusFlowPanel struct {
	*targetRecoveryPanel
	simReads    atomic.Int32
	statusReads atomic.Int32
}

func (p *entryStatusFlowPanel) SimulateClientEntryUserPolicy(context.Context, admin.ClientEntryUserPolicySimulationRequest) (admin.ClientEntryUserPolicySimulationResult, error) {
	p.simReads.Add(1)
	return admin.ClientEntryUserPolicySimulationResult{Found: true, User: &admin.ClientEntryUserPolicySimulationUser{ID: 99, Email: "entry-query@example.test"}, Matched: &admin.ClientEntryUserPolicyRecord{ID: 488, Enabled: 1, Action: "override", EntryHost: "entry-488.example.test"}}, nil
}

func (p *entryStatusFlowPanel) EntryMonitorInventory(ctx context.Context) (admin.EntryMonitorInventoryOverview, error) {
	p.statusReads.Add(1)
	return p.targetRecoveryPanel.EntryMonitorInventory(ctx)
}

func TestAIEntryStatusFlowPostgres(t *testing.T) {
	for _, tool := range []string{"entry_simulate", "entry_probe_targets"} {
		t.Run(tool+" returns real evidence in one read", func(t *testing.T) {
			f := setupStaffAIFixture(t)
			p := &entryStatusFlowPanel{targetRecoveryPanel: recoveryFixture()}
			p.Service = f.s.panel.(admin.Service)
			p.inventory.Sources[0].Probes = entryStatusFixture(488, 13615664, "entry-488.example.test", 54101).Probes
			f.s.panel = p
			var calls atomic.Int32
			relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req completionRequest
				if json.NewDecoder(r.Body).Decode(&req) != nil {
					t.Error("invalid request")
					w.WriteHeader(400)
					return
				}
				n := int(calls.Add(1))
				assertContextToolPairs(t, req.Messages)
				if n == 1 {
					args := `{"email":"entry-query@example.test","ua":"test-client"}`
					if tool == "entry_probe_targets" {
						args = `{"policy_id":488,"target_id":2426}`
					}
					entryProgressReply(w, n, tool, args)
					return
				}
				if n != 2 {
					t.Error("unexpected repeated read", n)
				}
				found := false
				for _, message := range req.Messages {
					if message.Role != "tool" {
						continue
					}
					found = strings.Contains(message.Content, `"target_id":13615664`) && strings.Contains(message.Content, `"port":54101`) && strings.Contains(message.Content, `"latency_ms":53`)
					if tool == "entry_probe_targets" && (!strings.Contains(message.Content, `"code":"target_not_found"`) || !strings.Contains(message.Content, `"total":0`)) {
						t.Error("missing wrong-ID diagnosis")
					}
				}
				if !found {
					t.Error("model did not receive existing observations and real target")
				}
				entryProgressReply(w, n, "", "")
			}))
			defer relay.Close()
			continuousConfigure(t, f, relay.URL)
			started := continuousSend(t, f, SendMessageRequest{Content: "查看该用户入口已有状态，不发检测", AccessMode: "standard"})
			turn := continuousWait(t, f, started.Turn.ID)
			if turn.Status != "completed" || turn.Error != "" || calls.Load() != 2 || p.statusReads.Load() != 1 {
				t.Fatal("status flow did not finish", turn, calls.Load())
			}
			var actions int
			if err := f.db.QueryRow(`SELECT COUNT(*) FROM v2_ai_action WHERE session_id=$1`, started.Session.ID).Scan(&actions); err != nil || actions != 0 {
				t.Fatal("read dispatched action", actions, err)
			}
		})
	}
}

func TestAIEntryStatusPreservesAccessBoundaryPostgres(t *testing.T) {
	f := setupStaffAIFixture(t)
	p := &entryStatusFlowPanel{targetRecoveryPanel: recoveryFixture()}
	p.Service = f.s.panel.(admin.Service)
	f.s.panel = p
	session, err := f.s.CreateSession(context.Background(), f.staff, CreateSessionRequest{Model: DefaultModels[0]})
	if err != nil {
		t.Fatal(err)
	}
	for _, actor := range []int64{f.user, f.other} {
		for _, call := range []ToolCall{
			entryProgressCall("entry_simulate", `{"email":"entry-query@example.test","ua":""}`),
			entryProgressCall("entry_probe_targets", `{"policy_id":488,"target_id":1}`),
		} {
			_, err := f.s.callTool(context.Background(), actor, session.ID, call)
			want := ErrForbidden
			if actor == f.other {
				want = ErrNotFound // do not disclose another operator's session
			}
			if !errors.Is(err, want) {
				t.Fatalf("unauthorized actor reached entry data: %v", err)
			}
		}
	}
	if p.simReads.Load() != 0 || p.statusReads.Load() != 0 || p.reads != 0 {
		t.Fatal("authorization happened after query")
	}
}
