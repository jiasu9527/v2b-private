package aiops

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/config"
	"golang.org/x/crypto/ssh"
)

func TestAIChatRegistersServerAndContinuesSSHWithoutExposingCredential(t *testing.T) {
	dsn := os.Getenv("FOREST_AIOPS_TEST_DSN")
	if dsn == "" {
		t.Skip("requires disposable FOREST_AIOPS_TEST_DSN")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	host, port, pin, auth, execs := testSSHHostWithExec(t, func(channel ssh.Channel) { io.WriteString(channel, "system OK\npassword=secret-test\n") })
	var calls atomic.Int32
	var intakeID, serverID atomic.Int64
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		if bytes.Contains(raw, []byte("secret-test")) {
			t.Error("SSH credential reached relay")
			w.WriteHeader(500)
			return
		}
		var req completionRequest
		if json.Unmarshal(raw, &req) != nil {
			t.Error("bad request")
			w.WriteHeader(500)
			return
		}
		n := calls.Add(1)
		var answer providerMessage
		switch n {
		case 1:
			var id int64
			for _, m := range req.Messages {
				if m.Role != "user" {
					continue
				}
				match := regexp.MustCompile(`"intake_id"\s*:\s*(\d+)`).FindStringSubmatch(m.Content)
				if len(match) == 2 {
					id, _ = strconv.ParseInt(match[1], 10, 64)
				}
			}
			if id <= 0 {
				t.Error("missing safe intake reference")
				w.WriteHeader(500)
				return
			}
			intakeID.Store(id)
			if !strings.Contains(string(raw), "检查系统状态") {
				t.Error("original task missing")
			}
			args, _ := json.Marshal(map[string]any{"intake_id": id, "name": "chat registration fixture", "reason": "登记后检查系统状态"})
			answer = providerMessage{Role: "assistant", ToolCalls: []ToolCall{{ID: "register", Type: "function", Function: ToolFunction{Name: "ssh_server_register", Arguments: string(args)}}}}
		case 2:
			var result struct {
				Status string `json:"status"`
				Output string `json:"output"`
			}
			for _, m := range req.Messages {
				if m.Role == "tool" && m.ToolCallID == "register" {
					json.Unmarshal([]byte(m.Content), &result)
				}
			}
			var registered struct {
				ServerID           int64 `json:"server_id"`
				ConnectionVerified bool  `json:"connection_verified"`
			}
			if json.Unmarshal([]byte(result.Output), &registered) != nil || result.Status != "succeeded" || registered.ServerID <= 0 || registered.ConnectionVerified {
				t.Error("missing actual registration result")
				w.WriteHeader(500)
				return
			}
			serverID.Store(registered.ServerID)
			if auth.Load() != 0 || execs.Load() != 0 {
				t.Error("registration authenticated or ran command")
			}
			args, _ := json.Marshal(map[string]any{"server_id": registered.ServerID, "reason": "核对系统状态"})
			answer = providerMessage{Role: "assistant", ToolCalls: []ToolCall{{ID: "status", Type: "function", Function: ToolFunction{Name: "ssh_status", Arguments: string(args)}}}}
		case 3:
			var result struct {
				Status string `json:"status"`
				Output string `json:"output"`
			}
			for _, m := range req.Messages {
				if m.Role == "tool" && m.ToolCallID == "status" {
					json.Unmarshal([]byte(m.Content), &result)
				}
			}
			if result.Status != "succeeded" || !strings.Contains(result.Output, "system OK") || auth.Load() != 1 || execs.Load() != 1 {
				t.Error("real SSH result not returned to model")
			}
			answer = providerMessage{Role: "assistant", Content: "已登记服务器并完成系统状态检查。"}
		default:
			t.Error("unexpected extra model call")
			w.WriteHeader(500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": answer}}})
	}))
	defer relay.Close()
	s := NewService(config.Config{AppKey: "server-chat-fixture"}, db, admin.NewDBService(config.Config{}, db))
	defer s.Close()
	if err = s.ensureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	marker := fmt.Sprint(time.Now().UnixNano())
	var actor int64
	if err = db.QueryRowContext(ctx, `INSERT INTO v2_user(email,password,uuid,token,is_admin,created_at,updated_at)VALUES($1,'unused','fixture',$2,1,1,1)RETURNING id`, "register-chat-"+marker+"@example.test", marker).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	defer func() {
		s.Close()
		db.Exec(`DELETE FROM v2_ai_session WHERE created_by=$1`, actor)
		db.Exec(`DELETE FROM v2_ai_server WHERE actor_id=$1`, actor)
		db.Exec(`DELETE FROM v2_ai_config WHERE updated_by=$1`, actor)
		db.Exec(`DELETE FROM v2_user WHERE id=$1`, actor)
	}()
	if _, err = s.SaveConfig(ctx, actor, ConfigSaveRequest{Enabled: true, BaseURL: relay.URL + "/v1", APIKey: "relay-fixture", Models: DefaultModels}); err != nil {
		t.Fatal(err)
	}
	request := SendMessageRequest{Model: DefaultModels[0], Content: fmt.Sprintf("登记服务器后检查系统状态\n%s:%d\nroot\nsecret-test", host, port), RequestID: "register-chat-" + marker, AccessMode: "full"}
	sent, err := s.SendMessage(ctx, actor, request)
	if err != nil {
		t.Fatal(err)
	}
	for {
		var status, problem string
		if err = db.QueryRowContext(ctx, `SELECT status,error FROM v2_ai_turn WHERE id=$1`, sent.Turn.ID).Scan(&status, &problem); err != nil {
			t.Fatal(err)
		}
		if status != "running" {
			if status != "completed" {
				t.Fatal("turn failed", status, problem)
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
	if calls.Load() != 3 || serverID.Load() <= 0 || intakeID.Load() <= 0 {
		t.Fatal("incomplete registration flow")
	}
	servers, err := s.ListServers(ctx, actor)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, v := range servers {
		if v.ID == serverID.Load() {
			found = v.Host == host && v.Port == port && v.Username == "root" && v.HostKeySHA256 == pin && v.HasPassword
		}
	}
	if !found {
		t.Fatal("registered server configuration missing")
	}
	detail, err := s.GetSession(ctx, actor, sent.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	actions, err := s.ListActions(ctx, actor, sent.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	public, _ := json.Marshal([]any{detail, actions, servers})
	if bytes.Contains(public, []byte("secret-test")) {
		t.Fatal("credential leaked into public state")
	}
	if len(actions) != 2 {
		t.Fatal("expected registration and SSH action", len(actions))
	}
	for _, a := range actions {
		if a.Status != "succeeded" || a.ApprovalMode != "full_access" {
			t.Fatal("wrong approval source", a.Kind, a.Status)
		}
	}
	var cipher string
	if err = db.QueryRowContext(ctx, `SELECT password_cipher FROM v2_ai_server WHERE id=$1`, serverID.Load()).Scan(&cipher); err != nil || cipher == "" || cipher == "secret-test" {
		t.Fatal("credential was not encrypted", err)
	}
	retry, err := s.SendMessage(ctx, actor, request)
	if err != nil || retry.Turn.ID != sent.Turn.ID || calls.Load() != 3 {
		t.Fatal("send replay repeated task", err)
	}
	var intakeCount int
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM v2_ai_server_intake WHERE session_id=$1`, sent.Session.ID).Scan(&intakeCount); err != nil || intakeCount != 1 {
		t.Fatal("duplicate intake", intakeCount, err)
	}
}
