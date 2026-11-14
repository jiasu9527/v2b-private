package aiops

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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
)

func TestRegistrationOnlyBatchDoesNotRelaxMixedOperations(t *testing.T) {
	register := ToolCall{Function: ToolFunction{Name: "ssh_server_register"}}
	ssh := ToolCall{Function: ToolFunction{Name: "ssh_status"}}
	for _, tc := range []struct {
		calls []ToolCall
		want  bool
	}{{nil, false}, {[]ToolCall{register, register}, true}, {[]ToolCall{register, ssh}, false}, {[]ToolCall{ssh, register}, false}, {make([]ToolCall, 51), false}} {
		if registrationOnlyBatch(tc.calls) != tc.want {
			t.Fatal("mixed/oversized batch relaxed the pending-action barrier")
		}
	}
}

func TestAIChatBatchRegistersAllOrProposesAllWithoutExposingCredentials(t *testing.T) {
	dsn := os.Getenv("FOREST_AIOPS_TEST_DSN")
	if dsn == "" {
		t.Skip("requires disposable FOREST_AIOPS_TEST_DSN")
	}
	for _, mode := range []string{"full", "standard"} {
		t.Run(mode, func(t *testing.T) {
			db, err := sql.Open("pgx", dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
			defer cancel()
			host, port, _, auths, execs := testSSHHost(t, false)
			count := 2
			if mode == "standard" {
				count = 25 // Exceeds the old 20-card limit, without executing any.
			}
			var calls atomic.Int32
			relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error("invalid request")
					w.WriteHeader(500)
					return
				}
				var req completionRequest
				if json.Unmarshal(raw, &req) != nil {
					t.Error("invalid completion request")
					w.WriteHeader(500)
					return
				}
				answer := providerMessage{Role: "assistant"}
				switch calls.Add(1) {
				case 1:
					if !bytes.Contains(raw, []byte("batch-fixture-secret-0")) {
						t.Error("free-form server paste did not reach current AI turn")
					}
					items := make([]any, 0, count)
					for i := 0; i < count; i++ {
						items = append(items, map[string]any{"host": host, "port": port, "username": fmt.Sprintf("user%d", i), "password": fmt.Sprintf("batch-fixture-secret-%d", i)})
					}
					args, _ := json.Marshal(map[string]any{"items": items})
					answer.ToolCalls = []ToolCall{{ID: "stage-batch", Type: "function", Function: ToolFunction{Name: "ssh_server_stage", Arguments: string(args)}}}
				case 2:
					var stage struct {
						Status string `json:"status"`
						Items  []struct {
							IntakeID int64 `json:"intake_id"`
						} `json:"items"`
					}
					for _, message := range req.Messages {
						if message.Role == "tool" && message.ToolCallID == "stage-batch" {
							_ = json.Unmarshal([]byte(message.Content), &stage)
						}
					}
					if stage.Status != "staged" || len(stage.Items) != count {
						t.Errorf("expected %d staged intake references, got %d", count, len(stage.Items))
					}
					for index, item := range stage.Items {
						args, _ := json.Marshal(map[string]any{"intake_id": item.IntakeID, "reason": "登记本批服务器"})
						answer.ToolCalls = append(answer.ToolCalls, ToolCall{ID: fmt.Sprintf("register-%d", index), Type: "function", Function: ToolFunction{Name: "ssh_server_register", Arguments: string(args)}})
					}
				case 3:
					if mode != "full" {
						t.Error("standard mode advanced without approvals")
					}
					results := 0
					for _, message := range req.Messages {
						if message.Role != "tool" {
							continue
						}
						var result struct {
							Status string `json:"status"`
							Output string `json:"output"`
						}
						if json.Unmarshal([]byte(message.Content), &result) != nil || result.Status != "succeeded" || !strings.Contains(result.Output, `"server_id":`) {
							t.Error("missing actual registration result")
						}
						results++
					}
					if results != count {
						t.Error("some registrations were skipped", results)
					}
					answer.Content = "已逐台核对登记结果。"
				default:
					t.Error("unexpected model retry")
					w.WriteHeader(500)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": answer}}})
			}))
			defer relay.Close()
			s := NewService(config.Config{AppKey: "batch-registration-fixture"}, db, admin.NewDBService(config.Config{}, db))
			defer s.Close()
			if err := s.ensureSchema(ctx); err != nil {
				t.Fatal(err)
			}
			marker := fmt.Sprint(time.Now().UnixNano())
			var actor int64
			if err := db.QueryRowContext(ctx, `INSERT INTO v2_user(email,password,uuid,token,is_admin,created_at,updated_at)VALUES($1,'unused','fixture',$2,1,1,1)RETURNING id`, "batch-"+marker+"@example.test", marker).Scan(&actor); err != nil {
				t.Fatal(err)
			}
			defer func() {
				s.Close()
				db.Exec(`DELETE FROM v2_ai_session WHERE created_by=$1`, actor)
				db.Exec(`DELETE FROM v2_ai_server WHERE actor_id=$1`, actor)
				db.Exec(`DELETE FROM v2_ai_config WHERE updated_by=$1`, actor)
				db.Exec(`DELETE FROM v2_user WHERE id=$1`, actor)
			}()
			if _, err := s.SaveConfig(ctx, actor, ConfigSaveRequest{Enabled: true, BaseURL: relay.URL + "/v1", APIKey: "fixture", Models: DefaultModels}); err != nil {
				t.Fatal(err)
			}
			content := "把这些服务器都整理并登记，不要登录\n"
			for i := 0; i < count; i++ {
				content += fmt.Sprintf("%s:%d user%d batch-fixture-secret-%d\n", host, port, i, i)
			}
			req := SendMessageRequest{Content: content, Model: DefaultModels[0], RequestID: "batch-flow-" + marker, AccessMode: mode}
			sent, err := s.SendMessage(ctx, actor, req)
			if err != nil {
				t.Fatal(err)
			}
			for {
				var status, problem string
				if err := db.QueryRowContext(ctx, `SELECT status,error FROM v2_ai_turn WHERE id=$1`, sent.Turn.ID).Scan(&status, &problem); err != nil {
					t.Fatal(err)
				}
				if status != "running" {
					want := "completed"
					if mode == "standard" {
						want = "pending"
					}
					if status != want {
						t.Fatal("batch flow stopped", status, problem)
					}
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-time.After(20 * time.Millisecond):
				}
			}
			if auths.Load() != 0 || execs.Load() != 0 {
				t.Fatal("registration logged in or executed commands")
			}
			actions, err := s.ListActions(ctx, actor, sent.Session.ID)
			if err != nil || len(actions) != count {
				t.Fatal("missing actions", len(actions), err)
			}
			for _, action := range actions {
				want := "succeeded"
				if mode == "standard" {
					want = "pending"
				}
				if action.Kind != "server_register" || action.Status != want {
					t.Fatal("wrong approval behavior", action.Kind, action.Status)
				}
			}
			var registered, stored int
			if err := db.QueryRowContext(ctx, `SELECT COUNT(*),COUNT(*) FILTER(WHERE registered_server_id>0) FROM v2_ai_server_intake WHERE turn_id=$1`, sent.Turn.ID).Scan(&stored, &registered); err != nil || stored != count {
				t.Fatal("incomplete batch persistence", stored, err)
			}
			if mode == "full" && registered != count || mode == "standard" && registered != 0 {
				t.Fatal("unexpected number of registered servers", registered)
			}
			if mode == "full" {
				for i := 0; i < count; i++ {
					var cipher string
					if err := db.QueryRowContext(ctx, `SELECT password_cipher FROM v2_ai_server WHERE actor_id=$1 AND username=$2`, actor, fmt.Sprintf("user%d", i)).Scan(&cipher); err != nil {
						t.Fatal(err)
					}
					plain, err := s.decryptSecret(cipher)
					if err != nil || plain != fmt.Sprintf("batch-fixture-secret-%d", i) {
						t.Fatal("credential associated with wrong server")
					}
				}
			}
			detail, err := s.GetSession(ctx, actor, sent.Session.ID)
			if err != nil {
				t.Fatal(err)
			}
			public, _ := json.Marshal([]any{detail, actions})
			if bytes.Contains(public, []byte("batch-fixture-secret")) {
				t.Fatal("public data leaked batch credentials")
			}
			before := calls.Load()
			repeated, err := s.SendMessage(ctx, actor, req)
			if err != nil || repeated.Turn.ID != sent.Turn.ID || calls.Load() != before {
				t.Fatal("idempotent replay started registration again", err)
			}
			req.Content = strings.Replace(req.Content, "batch-fixture-secret-0", "changed-fixture-secret", 1)
			if _, err := s.SendMessage(ctx, actor, req); !errors.Is(err, ErrConflict) {
				t.Fatal("changed credentials with reused request_id should conflict", err)
			}
		})
	}
}
