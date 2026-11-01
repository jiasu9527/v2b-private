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

// This fixture closes the first TCP connection before sending any SSH data.
// Later connections implement real SSH, but exec requests are only recorded:
// no subprocess, shell, DNS provider or production server is used.
func sshConnectionRetryFlowHost(t *testing.T, dropAfterExec bool) (ServerSaveRequest, *atomic.Int32, <-chan struct{}, func() []string) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{PasswordCallback: func(_ ssh.ConnMetadata, value []byte) (*ssh.Permissions, error) {
		if string(value) != "secret-test" {
			return nil, errors.New("fixture authentication failed")
		}
		return nil, nil
	}}
	config.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	connections := &atomic.Int32{}
	firstDropped := make(chan struct{})
	var mu sync.Mutex
	commands := []string{}
	active := map[net.Conn]struct{}{}
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			if connections.Add(1) == 1 {
				conn.Close()
				close(firstDropped)
				continue
			}
			mu.Lock()
			active[conn] = struct{}{}
			mu.Unlock()
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer func() {
					conn.Close()
					mu.Lock()
					delete(active, conn)
					mu.Unlock()
				}()
				server, channels, requests, err := ssh.NewServerConn(conn, config)
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
						_, _ = io.WriteString(channel, "fixture command accepted once\n")
						if dropAfterExec {
							// A write could have happened. Never send exit-status.
							_ = conn.Close()
							return
						}
						_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Code uint32 }{0}))
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
		for conn := range active {
			conn.Close()
		}
		mu.Unlock()
		workers.Wait()
	})
	request := ServerSaveRequest{Name: "SSH connection retry fixture", Host: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port, Username: "root", Password: "secret-test", HostKeySHA256: ssh.FingerprintSHA256(signer.PublicKey()), Enabled: true}
	return request, connections, firstDropped, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string{}, commands...)
	}
}

func TestAISSHConnectionRetryContinuesPostgres(t *testing.T) {
	for _, tool := range []string{"ssh_status", "propose_ssh_command"} {
		t.Run(tool, func(t *testing.T) {
			f := setupStaffAIFixture(t)
			serverRequest, connections, _, commands := sshConnectionRetryFlowHost(t, false)
			server, err := f.s.SaveServer(context.Background(), f.staff, serverRequest)
			if err != nil {
				t.Fatal(err)
			}
			command := "fixture-read-bandwidth-once"
			call := sshRecoveryCall("retry-connect", server.ID, command)
			if tool == "ssh_status" {
				command = diagnosticCommand
				args, _ := json.Marshal(map[string]any{"server_id": server.ID, "reason": "fixture query system status"})
				call.Function = ToolFunction{Name: tool, Arguments: string(args)}
			}
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
					sshRecoveryReply(w, call)
				case 2:
					result := sshRecoveryToolResult(t, request, "retry-connect")
					if result["status"] != "succeeded" || !strings.Contains(fmt.Sprint(result["output"]), "fixture command accepted once") {
						t.Error("model did not receive successful automatic reconnect", result)
					}
					sshRecoveryReply(w)
				default:
					t.Error("unexpected model retry")
					sshRecoveryReply(w)
				}
			}))
			defer relay.Close()
			started := sshRecoverySend(t, f, relay.URL)
			detail := sshRecoveryWait(t, f, started.Session.ID)
			if detail.Turns[0].Status != "completed" || rounds.Load() != 2 || connections.Load() != 2 || !reflect.DeepEqual(commands(), []string{command}) {
				t.Fatalf("turn=%+v rounds=%d connections=%d commands=%v", detail.Turns[0], rounds.Load(), connections.Load(), commands())
			}
			actions, err := f.s.ListActions(context.Background(), f.staff, started.Session.ID)
			if err != nil || len(actions) != 1 || actions[0].Status != "succeeded" || actions[0].Kind != "ssh_command" || actions[0].Error != "" {
				t.Fatal("reconnect created duplicate or failed actions", actions, err)
			}
			if !strings.Contains(actions[0].Output, "自动重连") || !strings.Contains(actions[0].Output, "fixture command accepted once") {
				t.Fatal("same action lost retry history or command output", actions[0].Output)
			}
		})
	}
}

func TestAISSHConnectionRetryStopOrRevokePostgres(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		t.Run(fmt.Sprintf("revoke=%v", revoke), func(t *testing.T) {
			f := setupStaffAIFixture(t)
			serverRequest, connections, firstDropped, commands := sshConnectionRetryFlowHost(t, false)
			server, err := f.s.SaveServer(context.Background(), f.staff, serverRequest)
			if err != nil {
				t.Fatal(err)
			}
			var rounds atomic.Int32
			relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if rounds.Add(1) != 1 {
					t.Error("cancelled turn continued model requests")
				}
				sshRecoveryReply(w, sshRecoveryCall("retry-connect", server.ID, "must-not-execute-after-stop"))
			}))
			defer relay.Close()
			started := sshRecoverySend(t, f, relay.URL)
			select {
			case <-firstDropped:
			case <-time.After(5 * time.Second):
				t.Fatal("initial connection did not fail")
			}
			if revoke {
				_, err = f.s.SetAccessMode(context.Background(), f.staff, AccessModeRequest{ID: started.Session.ID, Mode: "standard", ExpectedVersion: &started.Session.AccessVersion})
			} else {
				err = f.s.StopSession(context.Background(), f.staff, started.Session.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			detail := sshRecoveryWait(t, f, started.Session.ID)
			// Wait beyond the first retry delay: a detached retry goroutine must
			// not reconnect or dispatch after this turn has been cancelled.
			time.Sleep(1200 * time.Millisecond)
			if detail.Turns[0].Status != "interrupted" || rounds.Load() != 1 || connections.Load() != 1 || len(commands()) != 0 {
				t.Fatalf("turn=%+v rounds=%d connections=%d commands=%v", detail.Turns[0], rounds.Load(), connections.Load(), commands())
			}
			actions, err := f.s.ListActions(context.Background(), f.staff, started.Session.ID)
			if err != nil || len(actions) != 1 || actions[0].Status == "succeeded" || actions[0].Status == "running" || actions[0].ExitCode != nil {
				t.Fatal("stopped pre-dispatch action falsely succeeded or kept running", actions, err)
			}
		})
	}
}

func TestAISSHConnectionRetryNeverReplaysDispatchedCommandPostgres(t *testing.T) {
	f := setupStaffAIFixture(t)
	serverRequest, connections, _, commands := sshConnectionRetryFlowHost(t, true)
	server, err := f.s.SaveServer(context.Background(), f.staff, serverRequest)
	if err != nil {
		t.Fatal(err)
	}
	var rounds atomic.Int32
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rounds.Add(1) != 1 {
			t.Error("uncertain SSH write continued model requests")
		}
		sshRecoveryReply(w, sshRecoveryCall("uncertain-after-connect", server.ID, "fixture-write-once"), sshRecoveryCall("stale-after-unknown", server.ID, "must-not-execute"))
	}))
	defer relay.Close()
	started := sshRecoverySend(t, f, relay.URL)
	detail := sshRecoveryWait(t, f, started.Session.ID)
	if detail.Turns[0].Status != "failed" || rounds.Load() != 1 || connections.Load() != 2 || !reflect.DeepEqual(commands(), []string{"fixture-write-once"}) {
		t.Fatalf("turn=%+v rounds=%d connections=%d commands=%v", detail.Turns[0], rounds.Load(), connections.Load(), commands())
	}
	actions, err := f.s.ListActions(context.Background(), f.staff, started.Session.ID)
	if err != nil || len(actions) != 1 || actions[0].Status != "unknown" || actions[0].ExitCode != nil {
		t.Fatal("dispatched unknown command was retried or mislabeled", actions, err)
	}
}

func TestAISSHConnectionRetryDatabaseRevocationPostgres(t *testing.T) {
	for _, actorRevoked := range []bool{false, true} {
		t.Run(fmt.Sprintf("actor_revoked=%v", actorRevoked), func(t *testing.T) {
			f := setupStaffAIFixture(t)
			serverRequest, connections, firstDropped, commands := sshConnectionRetryFlowHost(t, false)
			server, err := f.s.SaveServer(context.Background(), f.staff, serverRequest)
			if err != nil {
				t.Fatal(err)
			}
			var rounds atomic.Int32
			relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if rounds.Add(1) != 1 {
					t.Error("database-revoked turn continued model requests")
				}
				sshRecoveryReply(w, sshRecoveryCall("retry-connect", server.ID, "must-not-execute-after-database-revocation"))
			}))
			defer relay.Close()
			started := sshRecoverySend(t, f, relay.URL)
			select {
			case <-firstDropped:
			case <-time.After(5 * time.Second):
				t.Fatal("initial connection did not fail")
			}
			// Simulate another backend instance: no local cancellation helper is
			// called. Reconnection must recheck persisted authorization/state.
			if actorRevoked {
				_, err = f.db.Exec(`UPDATE v2_user SET banned=1 WHERE id=$1`, f.staff)
			} else {
				_, err = f.db.Exec(`UPDATE v2_ai_action SET stop_requested=TRUE WHERE session_id=$1 AND status='running'`, started.Session.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			var status string
			deadline := time.Now().Add(8 * time.Second)
			for time.Now().Before(deadline) {
				if err = f.db.QueryRow(`SELECT status FROM v2_ai_turn WHERE session_id=$1 ORDER BY id DESC LIMIT 1`, started.Session.ID).Scan(&status); err != nil {
					t.Fatal(err)
				}
				if status != "running" {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			time.Sleep(1200 * time.Millisecond)
			if (status != "failed" && status != "interrupted") || rounds.Load() != 1 || connections.Load() != 1 || len(commands()) != 0 {
				t.Fatalf("status=%s rounds=%d connections=%d commands=%v", status, rounds.Load(), connections.Load(), commands())
			}
			actions, err := f.s.ListActions(context.Background(), f.admin, started.Session.ID)
			if err != nil || len(actions) != 1 || actions[0].Status == "succeeded" || actions[0].Status == "running" || actions[0].ExitCode != nil {
				t.Fatal("database-revoked action dispatched or kept running", actions, err)
			}
		})
	}
}
