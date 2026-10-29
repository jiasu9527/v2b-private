package aiops

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestActionSSHExitCodePostgresPersistence(t *testing.T) {
	f := setupStaffAIFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := f.s.CreateSession(ctx, f.admin, CreateSessionRequest{Model: DefaultModels[0]})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"known", "unknown"} {
		t.Run(name, func(t *testing.T) {
			host, port, pin, _, execs := testSSHHostWithExec(t, func(ch ssh.Channel) {
				_, _ = io.WriteString(ch, "partial action output\n")
				if name == "known" {
					_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Code uint32 }{17}))
				}
				_ = ch.Close()
			})
			server, e := f.s.SaveServer(ctx, f.admin, ServerSaveRequest{Name: "exit fixture", Host: host, Port: port, Username: "root", Password: "secret-test", HostKeySHA256: pin, Enabled: true})
			if e != nil {
				t.Fatal(e)
			}
			payload, _ := json.Marshal(sshPayload{ServerID: server.ID, Command: "fixture-change"})
			action, e := f.s.proposeAction(ctx, f.admin, session.ID, "exit-code-"+name, actionSpec{Kind: "ssh_command", TargetID: server.ID, Payload: payload, Reason: "verify fixture exit"})
			if e != nil || action.ExitCode != nil {
				t.Fatal("new action invented exit status", e)
			}
			if _, e = f.s.ApproveAction(ctx, f.admin, session.ID, action.ID); e != nil {
				t.Fatal(e)
			}
			for {
				action, e = scanAction(f.db.QueryRowContext(ctx, `SELECT `+actionColumns+` FROM v2_ai_action WHERE id=$1`, action.ID))
				if e != nil {
					t.Fatal(e)
				}
				if action.Status != "running" {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-time.After(10 * time.Millisecond):
				}
			}
			if execs.Load() != 1 || !strings.Contains(action.Output, "partial action output") {
				t.Fatal("action duplicated or output lost", execs.Load(), action)
			}
			public, e := json.Marshal(action)
			if e != nil {
				t.Fatal(e)
			}
			if name == "known" {
				if action.Status != "failed" || action.ExitCode == nil || *action.ExitCode != 17 || !strings.Contains(string(public), `"exit_code":17`) {
					t.Fatal("known exit not persisted", action)
				}
			} else if action.Status != "unknown" || action.ExitCode != nil || strings.Contains(string(public), "exit_code") {
				t.Fatal("unknown exit misrepresented", action)
			}
		})
	}
}

func TestActionSSHExitCodePostgresUpgradePreservesLegacyUnknown(t *testing.T) {
	f := setupStaffAIFixture(t)
	ctx := context.Background()
	session, err := f.s.CreateSession(ctx, f.admin, CreateSessionRequest{Model: DefaultModels[0]})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.ExecContext(ctx, `ALTER TABLE v2_ai_action DROP COLUMN exit_code`); err != nil {
		t.Fatal(err)
	}
	var actionID int64
	if err = f.db.QueryRowContext(ctx, `INSERT INTO v2_ai_action(session_id,actor_id,tool_call_id,kind,payload,snapshot_hash,request_hash,reason,status,error,expires_at,created_at,updated_at) VALUES($1,$2,'legacy-exit','ssh_command','{}','snapshot','request','fixture','failed',$3,$4,1,1) RETURNING id`, session.ID, f.admin, "远程命令退出码 7；旧版未存结构化退出码", time.Now().Add(time.Hour).Unix()).Scan(&actionID); err != nil {
		t.Fatal(err)
	}
	upgraded := NewService(f.s.cfg, f.db, f.s.panel)
	defer upgraded.Close()
	if err = upgraded.ensureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	action, err := scanAction(f.db.QueryRowContext(ctx, `SELECT `+actionColumns+` FROM v2_ai_action WHERE id=$1`, actionID))
	if err != nil || action.ExitCode != nil || action.Status != "failed" {
		t.Fatal("legacy action exit guessed during upgrade", action, err)
	}
	result, err := automaticActionOutcome(action)
	if err == nil || result.SSHExitCode != nil || !strings.Contains(formatAutomaticActionError(result, err), fmt.Sprintf("#%d", actionID)) {
		t.Fatal("unstructured legacy error became recoverable", result, err)
	}
}
