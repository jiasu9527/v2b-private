package aiops

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// This fixture speaks real SSH but never invokes a shell. Recording the exec
// payload verifies which exact commands reached the remote side of a turn.
func sshRecoveryHost(t *testing.T, execute func(string, ssh.Channel) *uint32) (ServerSaveRequest, func() []string) {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	serverConfig := &ssh.ServerConfig{PasswordCallback: func(_ ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
		if string(password) != "secret-test" {
			return nil, errors.New("fixture authentication failed")
		}
		return nil, nil
	}}
	serverConfig.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var commands []string
	connections := map[net.Conn]bool{}
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			connections[conn] = true
			mu.Unlock()
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer func() {
					conn.Close()
					mu.Lock()
					delete(connections, conn)
					mu.Unlock()
				}()
				server, channels, requests, err := ssh.NewServerConn(conn, serverConfig)
				if err != nil {
					return
				}
				defer server.Close()
				go ssh.DiscardRequests(requests)
				for incoming := range channels {
					channel, requests, err := incoming.Accept()
					if err != nil {
						return
					}
					for request := range requests {
						var payload struct{ Command string }
						if request.Type != "exec" || ssh.Unmarshal(request.Payload, &payload) != nil {
							_ = request.Reply(false, nil)
							continue
						}
						mu.Lock()
						commands = append(commands, payload.Command)
						mu.Unlock()
						_ = request.Reply(true, nil)
						if code := execute(payload.Command, channel); code != nil {
							_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Code uint32 }{*code}))
						}
						break
					}
					channel.Close()
				}
			}()
		}
	}()
	t.Cleanup(func() {
		listener.Close()
		mu.Lock()
		for conn := range connections {
			conn.Close()
		}
		mu.Unlock()
		workers.Wait()
	})
	return ServerSaveRequest{Name: "SSH recovery fixture", Host: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port, Username: "root", Password: "secret-test", HostKeySHA256: ssh.FingerprintSHA256(signer.PublicKey()), Enabled: true}, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string{}, commands...)
	}
}

func sshRecoveryCode(code uint32) *uint32 { return &code }

func sshRecoveryCall(id string, serverID int64, command string) ToolCall {
	args, _ := json.Marshal(map[string]any{"server_id": serverID, "command": command, "reason": "fixture verify and repair"})
	return ToolCall{ID: id, Type: "function", Function: ToolFunction{Name: "propose_ssh_command", Arguments: string(args)}}
}

func sshRecoveryReply(w http.ResponseWriter, calls ...ToolCall) {
	message := providerMessage{Role: "assistant", ToolCalls: calls}
	finish := "tool_calls"
	if len(calls) == 0 {
		message.Content, finish = "fixture recovery complete", "stop"
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": message, "finish_reason": finish}}})
}

func sshRecoveryToolResult(t *testing.T, request completionRequest, id string) map[string]any {
	t.Helper()
	for _, message := range request.Messages {
		if message.Role == "tool" && message.ToolCallID == id {
			var result map[string]any
			if err := json.Unmarshal([]byte(message.Content), &result); err != nil {
				t.Errorf("tool %s result is not JSON: %v", id, err)
			}
			return result
		}
	}
	t.Errorf("model request did not receive result for %s", id)
	return map[string]any{}
}

func sshRecoverySend(t *testing.T, f staffAIFixture, relayURL string) SendMessageResult {
	t.Helper()
	ctx := context.Background()
	if _, err := f.s.SaveConfig(ctx, f.admin, ConfigSaveRequest{Enabled: true, BaseURL: relayURL + "/v1", APIKey: "fixture", Models: DefaultModels}); err != nil {
		t.Fatal(err)
	}
	result, err := f.s.SendMessage(ctx, f.staff, SendMessageRequest{Model: DefaultModels[0], Content: "核对实际状态并修复配置，失败时先检查原因再调整", RequestID: fmt.Sprintf("ssh-recovery-%d", time.Now().UnixNano()), AccessMode: "full"})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func sshRecoveryWait(t *testing.T, f staffAIFixture, sessionID int64) SessionDetail {
	t.Helper()
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		detail, err := f.s.GetSession(context.Background(), f.staff, sessionID)
		if err != nil {
			t.Fatal(err)
		}
		if len(detail.Turns) > 0 && detail.Turns[0].Status != "running" {
			return detail
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("SSH recovery turn did not finish")
	return SessionDetail{}
}

func TestAISSHRecoveryPostgres(t *testing.T) {
	t.Run("nonzero skips batch then diagnoses and repairs", func(t *testing.T) {
		f := setupStaffAIFixture(t)
		serverRequest, commands := sshRecoveryHost(t, func(command string, channel ssh.Channel) *uint32 {
			switch command {
			case "fixture-apply old-value":
				_, _ = io.WriteString(channel, "fixture old value mismatch\n")
				return sshRecoveryCode(1)
			case "fixture-inspect current-value":
				_, _ = io.WriteString(channel, "fixture current value=v2\n")
				return sshRecoveryCode(0)
			case "fixture-apply corrected-value":
				_, _ = io.WriteString(channel, "fixture corrected value installed\n")
				return sshRecoveryCode(0)
			default:
				t.Error("unexpected remote command", command)
				return sshRecoveryCode(99)
			}
		})
		server, err := f.s.SaveServer(context.Background(), f.staff, serverRequest)
		if err != nil {
			t.Fatal(err)
		}
		var calls atomic.Int32
		relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var request completionRequest
			if json.NewDecoder(r.Body).Decode(&request) != nil {
				t.Error("invalid model request")
				w.WriteHeader(400)
				return
			}
			switch calls.Add(1) {
			case 1:
				skipped := ToolCall{ID: "must-skip-write", Type: "function", Function: ToolFunction{Name: "propose_user_update", Arguments: fmt.Sprintf(`{"user_id":%d,"changes":{"balance":654},"reason":"must be skipped after failed SSH"}`, f.user)}}
				sshRecoveryReply(w, sshRecoveryCall("first-failure", server.ID, "fixture-apply old-value"), skipped)
			case 2:
				failed := sshRecoveryToolResult(t, request, "first-failure")
				if failed["status"] != "failed" || failed["exit_code"] != float64(1) || failed["error"] == "" || !strings.Contains(fmt.Sprint(failed["output"]), "fixture old value mismatch") {
					t.Error("model did not receive actionable known failure", failed)
				}
				if skipped := sshRecoveryToolResult(t, request, "must-skip-write"); skipped["status"] != "skipped" {
					t.Error("remaining write was not paired as skipped", skipped)
				}
				sshRecoveryReply(w, sshRecoveryCall("inspect", server.ID, "fixture-inspect current-value"))
			case 3:
				checked := sshRecoveryToolResult(t, request, "inspect")
				if checked["status"] != "succeeded" || !strings.Contains(fmt.Sprint(checked["output"]), "current value=v2") {
					t.Error("model did not receive verification output", checked)
				}
				sshRecoveryReply(w, sshRecoveryCall("repair", server.ID, "fixture-apply corrected-value"))
			case 4:
				if repaired := sshRecoveryToolResult(t, request, "repair"); repaired["status"] != "succeeded" {
					t.Error("repair did not succeed", repaired)
				}
				sshRecoveryReply(w)
			default:
				t.Error("unexpected extra model request")
				w.WriteHeader(400)
			}
		}))
		defer relay.Close()
		started := sshRecoverySend(t, f, relay.URL)
		detail := sshRecoveryWait(t, f, started.Session.ID)
		if detail.Turns[0].Status != "completed" || calls.Load() != 4 || !reflect.DeepEqual(commands(), []string{"fixture-apply old-value", "fixture-inspect current-value", "fixture-apply corrected-value"}) {
			t.Fatalf("recovery failed or replayed command: turn=%+v calls=%d commands=%v", detail.Turns[0], calls.Load(), commands())
		}
		var balance int
		if err := f.db.QueryRow(`SELECT balance FROM v2_user WHERE id=$1`, f.user).Scan(&balance); err != nil || balance != 0 {
			t.Fatal("write after failed SSH executed", balance, err)
		}
		actions, err := f.s.ListActions(context.Background(), f.staff, started.Session.ID)
		if err != nil || len(actions) != 3 {
			t.Fatal("unexpected persisted actions", actions, err)
		}
		for _, action := range actions {
			var payload sshPayload
			if json.Unmarshal(action.Payload, &payload) != nil || action.Kind != "ssh_command" {
				t.Fatal("unexpected SSH result payload", action)
			}
			if payload.Command == "fixture-apply old-value" {
				if action.Status != "failed" || action.ExitCode == nil || *action.ExitCode != 1 {
					t.Fatal("nonzero action mislabeled", action)
				}
			} else if action.Status != "succeeded" || (action.ExitCode != nil && *action.ExitCode != 0) {
				t.Fatal("corrected command did not persist successful state", action)
			}
		}
	})

	t.Run("same failed command is rejected before execution but can be revised", func(t *testing.T) {
		f := setupStaffAIFixture(t)
		serverRequest, commands := sshRecoveryHost(t, func(command string, channel ssh.Channel) *uint32 {
			if command == "fixture-apply old-value" {
				_, _ = io.WriteString(channel, "fixture old value mismatch\n")
				return sshRecoveryCode(1)
			}
			if command != "fixture-apply new-value" {
				t.Error("unexpected remote command", command)
			}
			return sshRecoveryCode(0)
		})
		server, err := f.s.SaveServer(context.Background(), f.staff, serverRequest)
		if err != nil {
			t.Fatal(err)
		}
		var calls atomic.Int32
		relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var request completionRequest
			if json.NewDecoder(r.Body).Decode(&request) != nil {
				w.WriteHeader(400)
				return
			}
			switch calls.Add(1) {
			case 1:
				sshRecoveryReply(w, sshRecoveryCall("failure", server.ID, "fixture-apply old-value"))
			case 2:
				sshRecoveryReply(w, sshRecoveryCall("repeat", server.ID, " \tfixture-apply old-value \n"))
			case 3:
				repeated := sshRecoveryToolResult(t, request, "repeat")
				if repeated["status"] != "error" || repeated["code"] != "ssh_command_already_failed" {
					t.Error("duplicate rejection was not sent to model", repeated)
				}
				sshRecoveryReply(w, sshRecoveryCall("corrected", server.ID, "fixture-apply new-value"))
			case 4:
				sshRecoveryReply(w)
			default:
				t.Error("unexpected extra model request")
				w.WriteHeader(400)
			}
		}))
		defer relay.Close()
		started := sshRecoverySend(t, f, relay.URL)
		detail := sshRecoveryWait(t, f, started.Session.ID)
		if detail.Turns[0].Status != "completed" || calls.Load() != 4 || !reflect.DeepEqual(commands(), []string{"fixture-apply old-value", "fixture-apply new-value"}) {
			t.Fatalf("duplicate executed or correction prevented: turn=%+v calls=%d commands=%v", detail.Turns[0], calls.Load(), commands())
		}
		actions, err := f.s.ListActions(context.Background(), f.staff, started.Session.ID)
		if err != nil || len(actions) != 2 {
			t.Fatal("rejected repeated command created action", actions, err)
		}
	})

	for _, repeated := range []bool{false, true} {
		t.Run(fmt.Sprintf("three failed recovery attempts halt repeated=%v", repeated), func(t *testing.T) {
			f := setupStaffAIFixture(t)
			serverRequest, commands := sshRecoveryHost(t, func(command string, channel ssh.Channel) *uint32 {
				_, _ = io.WriteString(channel, "fixture old value mismatch\n")
				return sshRecoveryCode(1)
			})
			server, err := f.s.SaveServer(context.Background(), f.staff, serverRequest)
			if err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				if n > 3 {
					t.Error("failure budget did not stop model requests")
					w.WriteHeader(400)
					return
				}
				command := fmt.Sprintf("fixture-apply attempt-%d", n)
				if repeated {
					command = "fixture-apply same-failure"
				}
				sshRecoveryReply(w, sshRecoveryCall(fmt.Sprintf("attempt-%d", n), server.ID, command))
			}))
			defer relay.Close()
			started := sshRecoverySend(t, f, relay.URL)
			detail := sshRecoveryWait(t, f, started.Session.ID)
			wantCommands := 3
			if repeated {
				wantCommands = 1
			}
			if detail.Turns[0].Status != "failed" || calls.Load() != 3 || len(commands()) != wantCommands {
				t.Fatalf("unbounded recovery: turn=%+v calls=%d commands=%v", detail.Turns[0], calls.Load(), commands())
			}
			if detail.Turns[0].Error == "" {
				t.Fatal("exhausted recovery has no error explanation")
			}
		})
	}

	for _, stop := range []bool{false, true} {
		t.Run(fmt.Sprintf("unknown or user stop never continues stop=%v", stop), func(t *testing.T) {
			f := setupStaffAIFixture(t)
			startedSSH, releaseSSH := make(chan struct{}), make(chan struct{})
			defer close(releaseSSH)
			serverRequest, commands := sshRecoveryHost(t, func(command string, channel ssh.Channel) *uint32 {
				_, _ = io.WriteString(channel, "fixture command started\n")
				close(startedSSH)
				if stop {
					<-releaseSSH
				}
				// Closing without exit-status leaves remote outcome unknown.
				return nil
			})
			server, err := f.s.SaveServer(context.Background(), f.staff, serverRequest)
			if err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) > 1 {
					t.Error("unknown/stopped execution must not issue next model request")
				}
				sshRecoveryReply(w, sshRecoveryCall("uncertain", server.ID, "fixture-interrupted-change"), sshRecoveryCall("must-not-run", server.ID, "fixture-followup-change"))
			}))
			defer relay.Close()
			started := sshRecoverySend(t, f, relay.URL)
			select {
			case <-startedSSH:
			case <-time.After(5 * time.Second):
				t.Fatal("SSH did not start")
			}
			if stop {
				if err := f.s.StopSession(context.Background(), f.staff, started.Session.ID); err != nil {
					t.Fatal(err)
				}
			}
			detail := sshRecoveryWait(t, f, started.Session.ID)
			wantStatus := "failed"
			if stop {
				wantStatus = "interrupted"
			}
			if detail.Turns[0].Status != wantStatus || calls.Load() != 1 || !reflect.DeepEqual(commands(), []string{"fixture-interrupted-change"}) {
				t.Fatalf("uncertain/stop continued: turn=%+v calls=%d commands=%v", detail.Turns[0], calls.Load(), commands())
			}
			deadline := time.Now().Add(4 * time.Second)
			for {
				actions, err := f.s.ListActions(context.Background(), f.staff, started.Session.ID)
				if err != nil || len(actions) != 1 {
					t.Fatal("unexpected uncertain actions", actions, err)
				}
				if actions[0].Status != "running" {
					if actions[0].Status != "unknown" || actions[0].ExitCode != nil {
						t.Fatal("uncertain SSH fabricated a known exit status", actions[0])
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("stopped SSH action still running")
				}
				time.Sleep(20 * time.Millisecond)
			}
		})
	}
}
