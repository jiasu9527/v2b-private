package aiops

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/crypto/ssh"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/config"
	platformpostgres "forest/go-api/internal/platform/postgres"
)

func TestAIFullAccessPostgres(t *testing.T) {
	dsn := os.Getenv("FOREST_AIOPS_TEST_DSN")
	if dsn == "" {
		t.Skip("disposable PostgreSQL required")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err = platformpostgres.EnsureClientEntrySchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	s := NewService(config.Config{AppKey: "full-access-fixture"}, db, admin.NewDBService(config.Config{}, db))
	defer s.Close()
	if err = s.ensureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	var actor, other, user, node int64
	marker := fmt.Sprint(time.Now().UnixNano())
	for i, dest := range []*int64{&actor, &other, &user} {
		role := 1
		if i == 2 {
			role = 0
		}
		if err = db.QueryRow(`INSERT INTO v2_user(email,password,uuid,token,is_admin,created_at,updated_at)VALUES($1,'fixture','fixture',$2,$3,1,1)RETURNING id`, fmt.Sprintf("full-%d-%s@example.test", i, marker), fmt.Sprintf("full-%d-%s", i, marker), role).Scan(dest); err != nil {
			t.Fatal(err)
		}
	}
	defer db.Exec(`DELETE FROM v2_user WHERE id IN($1,$2,$3)`, actor, other, user)
	if err = db.QueryRow(`INSERT INTO v2_server_vmess(group_id,name,host,port,server_port,rate,network,"show",created_at,updated_at)VALUES('[]','full fixture','old.example.test','54101',54101,'1','tcp',1,1,1)RETURNING id`).Scan(&node); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM v2_server_vmess WHERE id=$1`, node)
	var sshServerID atomic.Int64
	var calls atomic.Int64
	var scenario atomic.Int64
	blocked := make(chan struct{}, 1)
	release := make(chan struct{})
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req completionRequest
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			t.Error("decode")
			return
		}
		n := calls.Add(1)
		if scenario.Load() == 4 || scenario.Load() == 5 {
			args, _ := json.Marshal(map[string]any{"server_id": sshServerID.Load(), "command": "fixture deploy", "timeout_seconds": 900, "reason": "stop fixture"})
			msg := providerMessage{Role: "assistant", ToolCalls: []ToolCall{{ID: "ssh-stop", Type: "function", Function: ToolFunction{Name: "propose_ssh_command", Arguments: string(args)}}}}
			if scenario.Load() == 5 {
				next, _ := json.Marshal(map[string]any{"node_type": "vmess", "node_id": node, "changes": map[string]any{"host": "must-not-write.example.test"}, "reason": "must be skipped after failed SSH"})
				msg.ToolCalls = append(msg.ToolCalls, ToolCall{ID: "must-skip", Type: "function", Function: ToolFunction{Name: "propose_node_change", Arguments: string(next)}})
			}
			json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": msg}}})
			return
		}

		if scenario.Load() == 2 {
			blocked <- struct{}{}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		if scenario.Load() == 3 {
			w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"}}]}`))
			return
		}
		if n >= 3 && scenario.Load() == 1 {
			w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"两个修改均已完成"}}]}`))
			return
		}
		host := "first.example.test"
		if n == 2 {
			host = "second.example.test"
		}
		args, _ := json.Marshal(map[string]any{"node_type": "vmess", "node_id": node, "changes": map[string]any{"host": host}, "reason": "fixture update"})
		msg := providerMessage{Role: "assistant", ToolCalls: []ToolCall{{ID: "reused-call", Type: "function", Function: ToolFunction{Name: "propose_node_change", Arguments: string(args)}}}}
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": msg}}})
	}))
	defer relay.Close()
	if _, err = s.SaveConfig(ctx, actor, ConfigSaveRequest{Enabled: true, BaseURL: relay.URL + "/v1", APIKey: "fixture", Models: DefaultModels}); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM v2_ai_config WHERE updated_by=$1`, actor)
	sessions := []int64{}
	defer func() {
		s.Close()
		for _, id := range sessions {
			db.Exec(`DELETE FROM v2_ai_session WHERE id=$1`, id)
		}
	}()
	send := func(mode string) SendMessageResult {
		t.Helper()
		calls.Store(0)
		r, e := s.SendMessage(ctx, actor, SendMessageRequest{Model: DefaultModels[0], Content: "按要求修改两个节点配置", RequestID: fmt.Sprintf("full-req-%d", time.Now().UnixNano()), AccessMode: mode})
		if e != nil {
			t.Fatal(e)
		}
		sessions = append(sessions, r.Session.ID)
		return r
	}
	wait := func(id int64) Turn {
		t.Helper()
		end := time.Now().Add(8 * time.Second)
		for time.Now().Before(end) {
			d, e := s.GetSession(ctx, actor, id)
			if e != nil {
				t.Fatal(e)
			}
			if len(d.Turns) > 0 && d.Turns[0].Status != "running" {
				return d.Turns[0]
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("turn did not stop")
		return Turn{}
	}
	host := func() string {
		var h string
		if e := db.QueryRow(`SELECT host FROM v2_server_vmess WHERE id=$1`, node).Scan(&h); e != nil {
			t.Fatal(e)
		}
		return h
	}
	t.Run("explicit standard leaves pending", func(t *testing.T) {
		scenario.Store(0)
		r := send("standard")
		tr := wait(r.Session.ID)
		if tr.Status != "pending" || host() != "old.example.test" || calls.Load() != 1 {
			t.Fatalf("default executed %+v host=%s calls=%d", tr, host(), calls.Load())
		}
	})
	t.Run("default full executes sequentially and results return", func(t *testing.T) {
		scenario.Store(1)
		r := send("")
		tr := wait(r.Session.ID)
		if r.Session.AccessMode != "full" || r.Session.FullAccessBy != actor || tr.AccessMode != "full" {
			t.Fatalf("new conversation did not default to full: session=%+v turn=%+v", r.Session, tr)
		}
		if tr.Status != "completed" || host() != "second.example.test" || calls.Load() != 3 {
			t.Fatalf("full result %+v host=%s calls=%d", tr, host(), calls.Load())
		}
		actions, e := s.ListActions(ctx, actor, r.Session.ID)
		if e != nil || len(actions) != 2 {
			t.Fatal(actions, e)
		}
		for _, a := range actions {
			if a.Status != "succeeded" || a.ApprovalMode != "full_access" || a.ApprovedBy != actor || a.AuthorizationTurnID != tr.ID {
				t.Fatalf("wrong authorization %+v", a)
			}
			if _, e = s.ApproveAction(ctx, actor, r.Session.ID, a.ID); !errors.Is(e, ErrConflict) {
				t.Fatal("replayed", e)
			}
		}
		v := r.Session.AccessVersion
		changed, e := s.SetAccessMode(ctx, actor, AccessModeRequest{ID: r.Session.ID, Mode: "standard", ExpectedVersion: &v})
		if e != nil || changed.AccessVersion <= v {
			t.Fatal(changed, e)
		}
		_, e = s.SetAccessMode(ctx, actor, AccessModeRequest{ID: r.Session.ID, Mode: "full", ExpectedVersion: &v})
		if !errors.Is(e, ErrConflict) {
			t.Fatal("stale mode overwritten", e)
		}
		_, e = s.SendMessage(ctx, actor, SendMessageRequest{SessionID: r.Session.ID, Model: DefaultModels[0], Content: "stale send", RequestID: "stale-send-mode", AccessMode: "full", AccessVersion: &v})
		if !errors.Is(e, ErrConflict) {
			t.Fatal("stale send reauthorized", e)
		}
		if _, e = s.SetAccessMode(ctx, user, AccessModeRequest{ID: r.Session.ID, Mode: "full", ExpectedVersion: &v}); !errors.Is(e, ErrForbidden) {
			t.Fatal("user grant", e)
		}
	})
	t.Run("mode revocation while response is arriving", func(t *testing.T) {
		scenario.Store(2)
		r := send("full")
		select {
		case <-blocked:
		case <-time.After(3 * time.Second):
			t.Fatal("relay not reached")
		}
		if _, e := s.SetAccessMode(ctx, other, AccessModeRequest{ID: r.Session.ID, Mode: "standard", ExpectedVersion: &r.Session.AccessVersion}); e != nil {
			t.Fatal(e)
		}
		close(release)
		tr := wait(r.Session.ID)
		if tr.Status != "interrupted" && tr.Status != "failed" {
			t.Fatal(tr)
		}
		if host() != "second.example.test" {
			t.Fatal("revoked task wrote")
		}
	})
	t.Run("other administrator does not inherit grant", func(t *testing.T) {
		scenario.Store(3)
		r := send("full")
		wait(r.Session.ID)
		_, e := s.SendMessage(ctx, actor, SendMessageRequest{SessionID: r.Session.ID, Model: DefaultModels[0], Content: "missing version", RequestID: "missing-version-full", AccessMode: "full"})
		if !errors.Is(e, ErrForbidden) {
			t.Fatal("full without explicit version", e)
		}
		version := r.Session.AccessVersion
		_, e = s.SendMessage(ctx, other, SendMessageRequest{SessionID: r.Session.ID, Model: DefaultModels[0], Content: "foreign grant", RequestID: "foreign-full-grant", AccessMode: "full", AccessVersion: &version})
		if !errors.Is(e, ErrForbidden) {
			t.Fatal("foreign full grant", e)
		}
		scenario.Store(0)
		calls.Store(0)
		response, e := s.SendMessage(ctx, other, SendMessageRequest{SessionID: r.Session.ID, Model: DefaultModels[0], Content: "modify", RequestID: "other-admin-req"})
		if e != nil {
			t.Fatal(e)
		}
		tr := wait(response.Session.ID)
		if tr.AccessMode != "standard" || tr.Status != "pending" || host() != "second.example.test" {
			t.Fatal(tr, host())
		}
	})
	t.Run("full long worker lease", func(t *testing.T) {
		scenario.Store(3)
		r := send("full")
		wait(r.Session.ID)
		var delta int64
		if e := db.QueryRow(`SELECT expires_at-created_at FROM v2_ai_turn WHERE id=$1`, r.Turn.ID).Scan(&delta); e != nil || delta < 3600 {
			t.Fatal(delta, e)
		}
	})

	for _, method := range []string{"stop", "revoke"} {
		t.Run("cross-instance "+method+" cancels running SSH without long lock", func(t *testing.T) {
			releaseSSH := make(chan struct{})
			host, port, pin, _, execs := testSSHHostWithExec(t, func(ch ssh.Channel) { _, _ = io.WriteString(ch, "installation started\n"); <-releaseSSH })
			saved, e := s.SaveServer(ctx, actor, ServerSaveRequest{Name: "stop fixture", Host: host, Port: port, Username: "root", Password: "secret-test", HostKeySHA256: pin, Enabled: true})
			if e != nil {
				close(releaseSSH)
				t.Fatal(e)
			}
			defer db.Exec(`DELETE FROM v2_ai_server WHERE id=$1`, saved.ID)
			defer close(releaseSSH)
			second := NewService(config.Config{AppKey: "full-access-fixture"}, db, admin.NewDBService(config.Config{}, db))
			defer second.Close()
			// Schema is ready before the latency measurement.
			if e = second.ensureSchema(ctx); e != nil {
				t.Fatal(e)
			}
			sshServerID.Store(saved.ID)
			scenario.Store(4)
			r := send("full")
			end := time.Now().Add(4 * time.Second)
			for execs.Load() == 0 && time.Now().Before(end) {
				time.Sleep(20 * time.Millisecond)
			}
			if execs.Load() != 1 {
				t.Fatal("SSH never started")
			}
			short, cancel := context.WithTimeout(ctx, 900*time.Millisecond)
			defer cancel()
			var revoked Session
			if method == "stop" {
				e = second.StopSession(short, other, r.Session.ID)
			} else {
				revoked, e = second.SetAccessMode(short, other, AccessModeRequest{ID: r.Session.ID, Mode: "standard", ExpectedVersion: &r.Session.AccessVersion})
			}
			if e != nil {
				t.Fatal("cross-instance cancellation blocked", e)
			}
			tr := wait(r.Session.ID)
			if tr.Status != "interrupted" {
				t.Fatal(tr)
			}
			end = time.Now().Add(4 * time.Second)
			var actions []Action
			for time.Now().Before(end) {
				actions, e = s.ListActions(ctx, actor, r.Session.ID)
				if e != nil {
					t.Fatal(e)
				}
				if len(actions) == 1 && actions[0].Status != "running" {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			if len(actions) != 1 || actions[0].Status != "unknown" || calls.Load() != 1 || execs.Load() != 1 {
				t.Fatalf("stop retried or result wrong actions=%+v calls=%d execs=%d", actions, calls.Load(), execs.Load())
			}
			if method == "revoke" {
				// A subsequent grant belongs to future work only. The stopped
				// turn's marker is immutable and cannot be revived by switching.
				if _, e = second.SetAccessMode(ctx, actor, AccessModeRequest{ID: r.Session.ID, Mode: "full", ExpectedVersion: &revoked.AccessVersion}); e != nil {
					t.Fatal(e)
				}
				if e = s.requireFullAccess(ctx, actor, r.Session.ID, tr.ID); !errors.Is(e, ErrForbidden) {
					t.Fatal("old turn revived", e)
				}
				if calls.Load() != 1 || execs.Load() != 1 {
					t.Fatal("grant resumed old task")
				}
			}
		})

	}

	for _, failure := range []string{"exit", "disconnect"} {
		t.Run("failed SSH "+failure+" prevents remaining calls and next model request", func(t *testing.T) {
			host, port, pin, _, execs := testSSHHostWithExec(t, func(ch ssh.Channel) {
				_, _ = io.WriteString(ch, "fixture deployment failed\n")
				if failure == "exit" {
					_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Code uint32 }{7}))
				}
				_ = ch.Close()
			})
			saved, e := s.SaveServer(ctx, actor, ServerSaveRequest{Name: "failed fixture", Host: host, Port: port, Username: "root", Password: "secret-test", HostKeySHA256: pin, Enabled: true})
			if e != nil {
				t.Fatal(e)
			}
			defer db.Exec(`DELETE FROM v2_ai_server WHERE id=$1`, saved.ID)
			sshServerID.Store(saved.ID)
			scenario.Store(5)
			r := send("full")
			tr := wait(r.Session.ID)
			actions, e := s.ListActions(ctx, actor, r.Session.ID)
			want := "failed"
			if failure == "disconnect" {
				want = "unknown"
			}
			if e != nil || tr.Status != "failed" || len(actions) != 1 || actions[0].Status != want || calls.Load() != 1 || execs.Load() != 1 {
				t.Fatalf("failure continued actions=%+v turn=%+v calls=%d execs=%d err=%v", actions, tr, calls.Load(), execs.Load(), e)
			}
			var nodeHost string
			if e = db.QueryRow(`SELECT host FROM v2_server_vmess WHERE id=$1`, node).Scan(&nodeHost); e != nil || nodeHost != "second.example.test" {
				t.Fatal("subsequent write happened", nodeHost, e)
			}
			detail, e := s.GetSession(ctx, actor, r.Session.ID)
			if e != nil {
				t.Fatal(e)
			}
			paired := map[string]string{}
			for _, m := range detail.Messages {
				if m.Role == "tool" {
					paired[m.ToolCallID] = m.Content
				}
			}
			if !strings.Contains(paired["ssh-stop"], `"status":"`+want+`"`) || !strings.Contains(paired["must-skip"], `"status":"skipped"`) {
				t.Fatalf("failure results not paired: %+v", paired)
			}
		})
	}

	t.Run("large history shows newest 400 messages without deleting old ones", func(t *testing.T) {
		scenario.Store(3)
		r := send("")
		wait(r.Session.ID)
		if _, e := db.Exec(`INSERT INTO v2_ai_message(session_id,turn_id,role,content,created_at)SELECT $1,$2,'assistant','message-'||n,1 FROM generate_series(1,420) n`, r.Session.ID, r.Turn.ID); e != nil {
			t.Fatal(e)
		}
		detail, e := s.GetSession(ctx, actor, r.Session.ID)
		if e != nil || !detail.MessagesTruncated || len(detail.Messages) != 400 || detail.Messages[0].Content != "message-21" || detail.Messages[399].Content != "message-420" {
			t.Fatalf("wrong history window len=%d trunc=%v error=%v", len(detail.Messages), detail.MessagesTruncated, e)
		}
		var count int
		if e = db.QueryRow(`SELECT COUNT(*) FROM v2_ai_message WHERE session_id=$1`, r.Session.ID).Scan(&count); e != nil || count != 422 {
			t.Fatal("history deleted", count, e)
		}
		if _, e = s.SetAccessMode(ctx, actor, AccessModeRequest{ID: r.Session.ID, Mode: "full"}); !errors.Is(e, ErrValidation) {
			t.Fatal("grant without version", e)
		}
	})
	t.Run("stop refuses ordinary users", func(t *testing.T) {
		if e := s.StopSession(ctx, user, sessions[0]); !errors.Is(e, ErrForbidden) {
			t.Fatal(e)
		}
	})
}
