package aiops

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// Real TCP/SSH fixtures exercise the distinction between a command that never
// left this process and one whose result is unknown. No shell is ever started.
func TestAISSHSetupFailureContinuesOtherHostPostgres(t *testing.T) {
	for _, tc := range []struct {
		name, mode, password, code string
		attempts                   int64
		retryable                  bool
	}{
		{"transient retries exhausted", "eof", "retry-secret-fixture", "network", 3, true},
		{"authentication failure", "success", "incorrect-secret-fixture", "authentication", 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setupStaffAIFixture(t)
			unavailable := newSSHReconnectFixture(t, func(int64) string { return tc.mode })
			failedServer, err := f.s.SaveServer(context.Background(), f.staff, ServerSaveRequest{Name: "unavailable setup fixture", Host: unavailable.host, Port: unavailable.port, Username: "root", Password: tc.password, HostKeySHA256: unavailable.pin, Enabled: true})
			if err != nil {
				t.Fatal(err)
			}
			healthyRequest, healthyCommands := sshRecoveryHost(t, func(command string, ch ssh.Channel) *uint32 {
				if command != "fixture-independent-check" {
					t.Error("stale dependent command dispatched", command)
				}
				_, _ = io.WriteString(ch, "independent fixture succeeded\n")
				return sshRecoveryCode(0)
			})
			healthyServer, err := f.s.SaveServer(context.Background(), f.staff, healthyRequest)
			if err != nil {
				t.Fatal(err)
			}
			var rounds atomic.Int32
			relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request completionRequest
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				switch rounds.Add(1) {
				case 1:
					// Calls after the failed setup were planned assuming its success and
					// must be replanned, even when they mention another server.
					sshRecoveryReply(w, sshRecoveryCall("first-unavailable", failedServer.ID, "fixture-first-command"), sshRecoveryCall("stale-dependent", healthyServer.ID, "must-not-run-stale"))
				case 2:
					failed := sshRecoveryToolResult(t, request, "first-unavailable")
					if failed["status"] != "failed" || failed["execution_state"] != "not_executed" || failed["recovery"] != "ssh_setup" || failed["server_id"] != float64(failedServer.ID) {
						t.Error("missing authoritative setup recovery", failed)
					}
					setup, ok := failed["ssh_setup_failure"].(map[string]any)
					if !ok || setup["code"] != tc.code || setup["attempts"] != float64(tc.attempts) || setup["retryable"] != tc.retryable || setup["execution_state"] != "not_executed" {
						t.Error("missing persisted setup classification", setup)
					}
					if skipped := sshRecoveryToolResult(t, request, "stale-dependent"); skipped["status"] != "skipped" {
						t.Error("stale dependent tool executed", skipped)
					}
					// Changing the shell text must not bypass a failed host's retry budget.
					sshRecoveryReply(w, sshRecoveryCall("changed-command", failedServer.ID, "fixture-different-command"))
				case 3:
					blocked := sshRecoveryToolResult(t, request, "changed-command")
					if blocked["code"] != "ssh_server_unavailable" {
						t.Error("changed command bypassed host guard", blocked)
					}
					args, _ := json.Marshal(map[string]any{"server_id": failedServer.ID, "reason": "fixture diagnostic cannot bypass host retry budget"})
					sshRecoveryReply(w, ToolCall{ID: "status-same-host", Type: "function", Function: ToolFunction{Name: "ssh_status", Arguments: string(args)}})
				case 4:
					blocked := sshRecoveryToolResult(t, request, "status-same-host")
					if blocked["code"] != "ssh_server_unavailable" {
						t.Error("diagnostic tool bypassed host guard", blocked)
					}
					sshRecoveryReply(w, sshRecoveryCall("independent-host", healthyServer.ID, "fixture-independent-check"))
				case 5:
					success := sshRecoveryToolResult(t, request, "independent-host")
					if success["status"] != "succeeded" || !strings.Contains(fmt.Sprint(success["output"]), "independent fixture succeeded") {
						t.Error("independent work did not continue", success)
					}
					sshRecoveryReply(w)
				default:
					t.Error("unexpected extra model request")
					sshRecoveryReply(w)
				}
			}))
			defer relay.Close()
			started := sshRecoverySend(t, f, relay.URL)
			detail := sshRecoveryWait(t, f, started.Session.ID)
			if detail.Turns[0].Status != "completed" || detail.Turns[0].Error != "" || rounds.Load() != 5 {
				t.Fatalf("safe pre-exec failure stopped turn: %+v rounds=%d", detail.Turns[0], rounds.Load())
			}
			if unavailable.connections.Load() != tc.attempts || len(unavailable.sentCommands()) != 0 {
				t.Fatalf("failed host reconnected or dispatched: connections=%d commands=%v", unavailable.connections.Load(), unavailable.sentCommands())
			}
			if got := healthyCommands(); !reflect.DeepEqual(got, []string{"fixture-independent-check"}) {
				t.Fatal("independent command lost or stale command executed", got)
			}
			actions, err := f.s.ListActions(context.Background(), f.staff, started.Session.ID)
			if err != nil || len(actions) != 2 {
				t.Fatalf("host guard created duplicate actions: %#v %v", actions, err)
			}
			var metadata string
			if err := f.db.QueryRow(`SELECT ssh_setup_failure::text FROM v2_ai_action WHERE session_id=$1 AND target_id=$2`, started.Session.ID, failedServer.ID).Scan(&metadata); err != nil {
				t.Fatal(err)
			}
			var saved map[string]any
			if err := json.Unmarshal([]byte(metadata), &saved); err != nil || saved["code"] != tc.code || saved["attempts"] != float64(tc.attempts) || saved["execution_state"] != "not_executed" {
				t.Fatalf("connection recovery was not persisted: %s %v", metadata, err)
			}
		})
	}
}

// A fresh user turn can retry a previously unavailable host. The first action
// remains failed evidence; it must not be revived or silently reused.
func TestAISSHSetupFailureNewUserTurnGetsFreshBudgetPostgres(t *testing.T) {
	f := setupStaffAIFixture(t)
	host := newSSHReconnectFixture(t, func(attempt int64) string {
		if attempt <= 3 {
			return "eof"
		}
		return "success"
	})
	server, err := f.s.SaveServer(context.Background(), f.staff, ServerSaveRequest{Name: "recovers in fresh turn", Host: host.host, Port: host.port, Username: "root", Password: "retry-secret-fixture", HostKeySHA256: host.pin, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	var rounds atomic.Int32
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request completionRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		switch rounds.Add(1) {
		case 1:
			sshRecoveryReply(w, sshRecoveryCall("first-turn-connection", server.ID, "fixture-retry-next-turn"))
		case 2:
			failed := sshRecoveryToolResult(t, request, "first-turn-connection")
			if failed["recovery"] != "ssh_setup" {
				t.Error("first turn did not report setup evidence", failed)
			}
			sshRecoveryReply(w)
		case 3:
			sshRecoveryReply(w, sshRecoveryCall("fresh-turn-connection", server.ID, "fixture-retry-next-turn"))
		case 4:
			success := sshRecoveryToolResult(t, request, "fresh-turn-connection")
			if success["status"] != "succeeded" {
				t.Error("fresh turn did not get new connection budget", success)
			}
			sshRecoveryReply(w)
		default:
			t.Error("unexpected model round")
			sshRecoveryReply(w)
		}
	}))
	defer relay.Close()
	first := sshRecoverySend(t, f, relay.URL)
	if detail := sshRecoveryWait(t, f, first.Session.ID); detail.Turns[0].Status != "completed" {
		t.Fatal("first recoverable turn did not finish", detail.Turns[0])
	}
	next, err := f.s.SendMessage(context.Background(), f.staff, SendMessageRequest{SessionID: first.Session.ID, Model: DefaultModels[0], Content: "现在连接已恢复，请重新检查该服务器", RequestID: fmt.Sprintf("fresh-ssh-setup-%d", time.Now().UnixNano()), AccessMode: "full", AccessVersion: &first.Session.AccessVersion})
	if err != nil {
		t.Fatal(err)
	}
	if detail := sshRecoveryWait(t, f, next.Session.ID); detail.Turns[0].Status != "completed" {
		t.Fatal("fresh user turn stopped", detail.Turns[0])
	}
	if rounds.Load() != 4 || host.connections.Load() != 4 || !reflect.DeepEqual(host.sentCommands(), []string{"fixture-retry-next-turn"}) {
		t.Fatalf("fresh retry replayed or remained blocked: rounds=%d connections=%d commands=%v", rounds.Load(), host.connections.Load(), host.sentCommands())
	}
	actions, err := f.s.ListActions(context.Background(), f.staff, first.Session.ID)
	if err != nil || len(actions) != 2 || actions[0].Status != "succeeded" || actions[1].Status != "failed" || actions[0].ID == actions[1].ID {
		t.Fatalf("fresh retry changed historical action evidence: %#v %v", actions, err)
	}
}
