package aiops

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestSSHSetupFailurePostgresWorkerPersistence(t *testing.T) {
	for _, mode := range []string{"eof", "authentication", "exec_disconnect", "exec_nonzero", "success"} {
		t.Run(mode, func(t *testing.T) {
			f := setupStaffAIFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			host := newSSHReconnectFixture(t, func(int64) string {
				if mode == "authentication" {
					return "success"
				}
				return mode
			})
			password := "retry-secret-fixture"
			if mode == "authentication" {
				password = "wrong-secret-fixture"
			}
			server, err := f.s.SaveServer(ctx, f.staff, ServerSaveRequest{Name: "setup metadata fixture", Host: host.host, Port: host.port, Username: "root", Password: password, HostKeySHA256: host.pin, Enabled: true})
			if err != nil {
				t.Fatal(err)
			}
			session, err := f.s.CreateSession(ctx, f.staff, CreateSessionRequest{Model: DefaultModels[0]})
			if err != nil {
				t.Fatal(err)
			}
			payload, _ := json.Marshal(sshPayload{ServerID: server.ID, Command: "fixture-metadata-only"})
			a, err := f.s.proposeAction(ctx, f.staff, session.ID, "setup-meta", actionSpec{Kind: "ssh_command", TargetID: server.ID, Payload: payload, Reason: "verify local metadata"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.s.ApproveAction(ctx, f.staff, session.ID, a.ID); err != nil {
				t.Fatal(err)
			}
			for {
				a, err = scanAction(f.db.QueryRowContext(ctx, `SELECT `+actionColumns+` FROM v2_ai_action WHERE id=$1`, a.ID))
				if err != nil {
					t.Fatal(err)
				}
				if a.Status != "running" {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-time.After(20 * time.Millisecond):
				}
			}
			switch mode {
			case "eof":
				want := &SSHSetupFailure{ExecutionState: "not_executed", Stage: "handshake", Code: "network", Retryable: true, Attempts: 3}
				if a.Status != "failed" || !reflect.DeepEqual(a.SSHSetupFailure, want) || len(host.sentCommands()) != 0 {
					t.Fatalf("pre-exec proof lost: %+v", a)
				}
			case "authentication":
				if a.Status != "failed" || a.SSHSetupFailure == nil || a.SSHSetupFailure.Retryable || a.SSHSetupFailure.Code != "authentication" || len(host.sentCommands()) != 0 {
					t.Fatalf("authentication marked retryable: %+v", a)
				}
			default:
				if a.SSHSetupFailure != nil || len(host.sentCommands()) != 1 {
					t.Fatalf("executed action invented pre-exec proof: %+v", a)
				}
			}
		})
	}
}

func TestSSHSetupFailurePostgresLegacyAndMalformedNeverInventProof(t *testing.T) {
	f := setupStaffAIFixture(t)
	ctx := context.Background()
	session, err := f.s.CreateSession(ctx, f.staff, CreateSessionRequest{Model: DefaultModels[0]})
	if err != nil {
		t.Fatal(err)
	}
	for i, raw := range []any{nil, `{}`, `{"execution_state":"not_executed","stage":"exec","code":"timeout","retryable":true,"attempts":3}`} {
		var id int64
		if err := f.db.QueryRowContext(ctx, `INSERT INTO v2_ai_action(session_id,actor_id,tool_call_id,kind,payload,snapshot_hash,request_hash,reason,status,error,expires_at,created_at,updated_at,ssh_setup_failure) VALUES($1,$2,$3,'ssh_command','{}','snapshot','request','fixture','failed','SSH 握手阶段超时，已尝试 3 次连接；命令未发送',1,1,1,$4::jsonb) RETURNING id`, session.ID, f.staff, string(rune('a'+i)), raw).Scan(&id); err != nil {
			t.Fatal(err)
		}
		a, err := scanAction(f.db.QueryRowContext(ctx, `SELECT `+actionColumns+` FROM v2_ai_action WHERE id=$1`, id))
		if err != nil || a.SSHSetupFailure != nil {
			t.Fatalf("legacy/malformed text became evidence: %+v err=%v", a, err)
		}
	}
}
