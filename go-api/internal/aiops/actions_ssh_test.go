package aiops

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"forest/go-api/internal/config"
	"golang.org/x/crypto/ssh"
)

func TestSSHExecutionTimeoutBounds(t *testing.T) {
	for _, seconds := range []int{0, 30, 120, 900, 1800} {
		got, err := normalizeSSHTimeout(seconds)
		if err != nil || (seconds != 0 && got != seconds) || (seconds == 0 && got != 120) {
			t.Fatalf("timeout %d: %d %v", seconds, got, err)
		}
		raw, _ := json.Marshal(map[string]int{"timeout_seconds": seconds})
		if duration := actionExecutionLifetime(Action{Kind: "ssh_command", Payload: raw}); duration != time.Duration(got)*time.Second {
			t.Fatalf("wrong execution duration: %v", duration)
		}
	}
	for _, seconds := range []int{-1, 1, 29, 1801, 2147483647} {
		if _, err := normalizeSSHTimeout(seconds); err == nil {
			t.Fatalf("accepted timeout %d", seconds)
		}
	}
	if got := actionExecutionLifetime(Action{Kind: "ticket_settings", Payload: json.RawMessage(`{"timeout_seconds":1800}`)}); got != executionLifetime {
		t.Fatal("non-SSH action used SSH timeout", got)
	}
}

func TestSSHProgressAndTimeoutPostgres(t *testing.T) {
	dsn := os.Getenv("FOREST_AIOPS_TEST_DSN")
	if dsn == "" {
		t.Skip("dedicated disposable FOREST_AIOPS_TEST_DSN required")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := NewService(config.Config{AppKey: "ssh-progress-fixture"}, db, nil)
	defer s.Close()
	ctx := context.Background()
	if err = s.ensureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	var actor, sessionID, actionID int64
	marker := fmt.Sprint(time.Now().UnixNano())
	if err = db.QueryRow(`INSERT INTO v2_user(email,password,uuid,token,is_admin,created_at,updated_at)VALUES($1,'unused','test',$2,1,1,1)RETURNING id`, marker+"@example.test", marker).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM v2_user WHERE id=$1`, actor)
	if err = db.QueryRow(`INSERT INTO v2_ai_session(title,model,created_by,created_at,updated_at)VALUES('ssh progress','fixture',$1,1,1)RETURNING id`, actor).Scan(&sessionID); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM v2_ai_session WHERE id=$1`, sessionID)
	now := time.Now().Unix()
	if err = db.QueryRow(`INSERT INTO v2_ai_action(session_id,actor_id,tool_call_id,kind,target_id,payload,snapshot_hash,request_hash,reason,status,expires_at,claimed_at,created_at,updated_at)VALUES($1,$2,'ssh-progress','ssh_command',42,'{"timeout_seconds":900}','snapshot','request','fixture','running',$3,$4,$4,$4)RETURNING id`, sessionID, actor, now+900, now-200).Scan(&actionID); err != nil {
		t.Fatal(err)
	}
	s.persistSSHProgress(ctx, actionID, "installing stage one")
	s.persistSSHProgress(ctx, actionID, "installing stage two")
	if err = s.reconcileExpiredActions(ctx); err != nil {
		t.Fatal(err)
	}
	var status, output string
	if err = db.QueryRow(`SELECT status,output FROM v2_ai_action WHERE id=$1`, actionID).Scan(&status, &output); err != nil || status != "running" || output != "installing stage two" {
		t.Fatalf("long install prematurely expired or progress missing %q %q %v", status, output, err)
	}
	if err = s.rejectBusySSHServer(ctx, 42); err != ErrBusy {
		t.Fatalf("long SSH operation did not block maintenance: %v", err)
	}
	if _, err = db.Exec(`UPDATE v2_ai_action SET claimed_at=$2 WHERE id=$1`, actionID, now-961); err != nil {
		t.Fatal(err)
	}
	if err = s.reconcileExpiredActions(ctx); err != nil {
		t.Fatal(err)
	}
	s.persistSSHProgress(ctx, actionID, "late progress must be ignored")
	if err = db.QueryRow(`SELECT status,output FROM v2_ai_action WHERE id=$1`, actionID).Scan(&status, &output); err != nil || status != "unknown" || output != "installing stage two" {
		t.Fatalf("stale execution or late progress incorrectly handled %q %q %v", status, output, err)
	}
	if _, err = db.Exec(`DELETE FROM v2_ai_action WHERE id=$1`, actionID); err != nil {
		t.Fatal(err)
	}
	s.persistSSHProgress(ctx, actionID, "must not resurrect")
	var count int
	if err = db.QueryRow(`SELECT COUNT(*) FROM v2_ai_action WHERE id=$1`, actionID).Scan(&count); err != nil || count != 0 {
		t.Fatal("progress resurrected action", count, err)
	}
	for _, payload := range []string{`{}`, `{"timeout_seconds":null}`, `{"timeout_seconds":"abc"}`, `{"timeout_seconds":-1}`, `{"timeout_seconds":true}`, `{"timeout_seconds":2147483647999999999999999999999999999}`, `{"timeout_seconds":1801}`, `{"timeout_seconds":[]}`} {
		var seconds int
		query := `SELECT ` + actionStaleSecondsSQL + ` FROM (SELECT 'ssh_command' AS kind,$1::JSONB AS payload) a`
		if err = db.QueryRow(query, payload).Scan(&seconds); err != nil || seconds != 180 {
			t.Fatalf("unsafe legacy payload %s => %d %v", payload, seconds, err)
		}
	}
	for _, seconds := range []int{30, 120, 900, 1800} {
		var stale int
		query := `SELECT ` + actionStaleSecondsSQL + ` FROM (SELECT 'ssh_command' AS kind,$1::JSONB AS payload) a`
		if err = db.QueryRow(query, fmt.Sprintf(`{"timeout_seconds":%d}`, seconds)).Scan(&stale); err != nil || stale != seconds+60 {
			t.Fatalf("valid timeout %d => %d %v", seconds, stale, err)
		}
	}
	if strings.Contains(output, "late") {
		t.Fatal("late progress persisted")
	}
	t.Run("approved SSH streams output in same persisted action", func(t *testing.T) {
		if _, err = s.SaveConfig(ctx, actor, ConfigSaveRequest{Enabled: true, BaseURL: "https://fixture.example.test/v1", Models: DefaultModels, APIKey: "ssh-fixture-only"}); err != nil {
			t.Fatal(err)
		}
		defer db.Exec(`DELETE FROM v2_ai_config WHERE updated_by=$1`, actor)
		release := make(chan struct{})
		host, port, pin, _, execs := testSSHHostWithExec(t, func(channel ssh.Channel) {
			_, _ = io.WriteString(channel, "deploying fixture\npassword=secret-")
			<-release
			_, _ = io.WriteString(channel, "test\ndeployment complete\n")
		})
		saved, err := s.SaveServer(ctx, actor, ServerSaveRequest{Name: "fixture landing", Host: host, Port: port, Username: "root", HostKeySHA256: pin, Password: "secret-test", Enabled: true})
		if err != nil {
			close(release)
			t.Fatal(err)
		}
		defer func() {
			s.Close()
			db.Exec(`DELETE FROM v2_ai_server WHERE id=$1`, saved.ID)
		}()
		defer close(release)
		for _, timeout := range []int{0, 900} {
			raw, _ := json.Marshal(sshPayload{ServerID: saved.ID, Command: "install fixture", TimeoutSeconds: timeout})
			a, err := s.proposeAction(ctx, actor, sessionID, fmt.Sprintf("ssh-deploy-%d", timeout), actionSpec{Kind: "ssh_command", TargetID: saved.ID, Payload: raw, Reason: "install landing fixture"})
			if err != nil {
				t.Fatal(err)
			}
			var payload sshPayload
			if err = json.Unmarshal(a.Payload, &payload); err != nil || payload.TimeoutSeconds == 0 || (timeout == 900 && payload.TimeoutSeconds != 900) || (timeout == 0 && payload.TimeoutSeconds != 120) {
				t.Fatalf("timeout did not survive approval payload: %+v %v", payload, err)
			}
			if execs.Load() != 0 {
				t.Fatal("SSH executed before approval")
			}
			if timeout == 0 {
				if _, err = s.RejectAction(ctx, actor, sessionID, a.ID); err != nil {
					t.Fatal(err)
				}
				continue
			}
			approved, err := s.ApproveAction(ctx, actor, sessionID, a.ID)
			if err != nil || approved.Status != "running" {
				t.Fatalf("approval failed: %+v %v", approved, err)
			}
			deadline := time.Now().Add(4 * time.Second)
			for {
				if err = db.QueryRow(`SELECT status,output FROM v2_ai_action WHERE id=$1`, a.ID).Scan(&status, &output); err != nil {
					t.Fatal(err)
				}
				if strings.Contains(output, "deploying fixture") {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("output did not persist while SSH was still running: status=%s output=%q", status, output)
				}
				time.Sleep(20 * time.Millisecond)
			}
			if status != "running" || strings.Contains(output, "secret-") || execs.Load() != 1 {
				t.Fatalf("bad progress snapshot: %s %q executions=%d", status, output, execs.Load())
			}
			// Approving a card while the model writes its final explanation must
			// not hold that response behind a potentially 30-minute SSH install.
			var turnID int64
			shortCtx, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()
			if err = db.QueryRowContext(shortCtx, `INSERT INTO v2_ai_turn(session_id,actor_id,request_id,request_hash,model,status,created_at,expires_at)VALUES($1,$2,'ssh-final-explanation','fixture','fixture','running',$3,$4)RETURNING id`, sessionID, actor, now, now+120).Scan(&turnID); err != nil {
				t.Fatal(err)
			}
			if _, err = db.ExecContext(shortCtx, `UPDATE v2_ai_session SET status='running' WHERE id=$1`, sessionID); err != nil {
				t.Fatal("SSH blocked session status update", err)
			}
			if err = s.appendMessages(shortCtx, actor, sessionID, turnID, []providerMessage{{Role: "assistant", Content: "安装正在运行，输出实时更新。"}}); err != nil {
				t.Fatal("SSH blocked final conversation message", err)
			}
			s.finishTurn(sessionID, turnID, "succeeded", "")
			detail, err := s.GetSession(shortCtx, actor, sessionID)
			if err != nil || detail.Session.Status != "idle" || len(detail.Messages) != 1 || detail.Messages[0].Content != "安装正在运行，输出实时更新。" {
				t.Fatalf("SSH blocked session streaming/read: %+v %v", detail, err)
			}
			if err = s.DeleteSession(shortCtx, actor, sessionID); !errors.Is(err, ErrBusy) {
				t.Fatal("running SSH session deletion must still be refused", err)
			}
			if _, err = s.ApproveAction(ctx, actor, sessionID, a.ID); !errors.Is(err, ErrConflict) && !errors.Is(err, ErrBusy) {
				t.Fatal("duplicate approved", err)
			}
			if err = s.DeleteServer(ctx, actor, saved.ID, saved.Version); !errors.Is(err, ErrBusy) {
				t.Fatal("running server was not busy", err)
			}
			release <- struct{}{}
			deadline = time.Now().Add(3 * time.Second)
			for {
				if err = db.QueryRow(`SELECT status,output FROM v2_ai_action WHERE id=$1`, a.ID).Scan(&status, &output); err != nil {
					t.Fatal(err)
				}
				if status != "running" {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("SSH completion was not persisted")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if status != "succeeded" || !strings.Contains(output, "deployment complete") || strings.Contains(output, "secret-test") || execs.Load() != 1 {
				t.Fatalf("bad completion snapshot: %s %q executions=%d", status, output, execs.Load())
			}
		}
	})
}
