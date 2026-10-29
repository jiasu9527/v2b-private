package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/aiops"
	"forest/go-api/internal/config"
)

// Uses only a disposable UTF8 database. The relay is local and deterministic;
// actual catalog dispatch, business validation and writes use production code.
func TestPanelOperationsHTTPPostgresFlow(t *testing.T) {
	dsn := os.Getenv("FOREST_AIOPS_HTTP_TEST_DSN")
	if dsn == "" {
		t.Skip("requires disposable FOREST_AIOPS_HTTP_TEST_DSN")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	var count int
	if err = db.QueryRow(`SELECT COUNT(*) FROM v2_user`).Scan(&count); err != nil || count != 0 {
		t.Fatal("requires empty test database", err)
	}
	actors := []int64{}
	for _, role := range []string{"admin", "staff"} {
		var id int64
		isAdmin, isStaff := 0, 0
		if role == "admin" {
			isAdmin = 1
		} else {
			isStaff = 1
		}
		if err = db.QueryRow(`INSERT INTO v2_user(email,password,uuid,token,is_admin,is_staff,created_at,updated_at) VALUES($1,'fixture','fixture',$2,$3,$4,1,1) RETURNING id`, "panel-"+role+"@example.test", "panel-"+role, isAdmin, isStaff).Scan(&id); err != nil {
			t.Fatal(err)
		}
		actors = append(actors, id)
	}
	defer db.Exec(`DELETE FROM v2_user WHERE id IN($1,$2)`, actors[0], actors[1])
	title := "AI panel HTTP fixture " + fmt.Sprint(time.Now().UnixNano())
	defer db.Exec(`DELETE FROM v2_notice WHERE title LIKE $1`, title+"%")
	var lock sync.Mutex
	step := 0
	currentTitle := title + " full"
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lock.Lock()
		defer lock.Unlock()
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			t.Error("invalid relay payload")
		}
		toolName := ""
		args := map[string]any{}
		switch step {
		case 0:
			toolName = "panel_operations_list"
			args = map[string]any{"category": "notices"}
		case 1:
			toolName = "panel_operation_describe"
			args = map[string]any{"operation": "notices.save"}
		case 2:
			toolName = "panel_query"
			args = map[string]any{"operation": "notices.list", "parameters": map[string]any{}}
		case 3:
			toolName = "propose_panel_operation"
			args = map[string]any{"operation": "notices.save", "parameters": map[string]any{"title": currentTitle, "content": "isolated test body", "tags": []string{"fixture"}}, "reason": "隔离测试公告"}
		}
		step++
		msg := map[string]any{"role": "assistant", "content": "完成"}
		if toolName != "" {
			raw, _ := json.Marshal(args)
			msg = map[string]any{"role": "assistant", "tool_calls": []aiops.ToolCall{{ID: fmt.Sprintf("panel-call-%d", step), Type: "function", Function: aiops.ToolFunction{Name: toolName, Arguments: string(raw)}}}}
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": msg}}})
	}))
	defer relay.Close()
	cfg := config.Config{AdminPath: "panel-test", AppKey: "isolated-panel-ops-app-key"}
	adminService := admin.NewDBService(cfg, db)
	ai := aiops.NewService(cfg, db, adminService)
	defer ai.Close()
	defer ai.ResetData(ctx, actors[0])
	NewRouter(cfg, WithAIOps(ai), WithAdminService(adminService)) // production runtime binding
	if _, err = ai.SaveConfig(ctx, actors[0], aiops.ConfigSaveRequest{Enabled: true, BaseURL: relay.URL, APIKey: "local-fixture", Models: aiops.DefaultModels}); err != nil {
		t.Fatal(err)
	}
	wait := func(actorID, sessionID int64) []aiops.Action {
		t.Helper()
		for deadline := time.Now().Add(12 * time.Second); ; {
			detail, e := ai.GetSession(ctx, actorID, sessionID)
			if e != nil {
				t.Fatal(e)
			}
			actions, e := ai.ListActions(ctx, actorID, sessionID)
			if e != nil {
				t.Fatal(e)
			}
			busy := detail.Session.Status == "running"
			for _, a := range actions {
				busy = busy || a.Status == "running"
			}
			if !busy {
				return actions
			}
			if time.Now().After(deadline) {
				t.Fatal("AI flow timed out")
			}
			time.Sleep(15 * time.Millisecond)
		}
	}
	for _, mode := range []string{"full", "standard"} {
		session, err := ai.CreateSession(ctx, actors[1], aiops.CreateSessionRequest{Model: aiops.DefaultModels[0]})
		if err != nil {
			t.Fatal(err)
		}
		if mode == "standard" {
			session, err = ai.SetAccessMode(ctx, actors[1], aiops.AccessModeRequest{ID: session.ID, Mode: mode, ExpectedVersion: &session.AccessVersion})
			if err != nil {
				t.Fatal(err)
			}
		}
		lock.Lock()
		step = 0
		currentTitle = title + " " + mode
		lock.Unlock()
		sent, err := ai.SendMessage(ctx, actors[1], aiops.SendMessageRequest{SessionID: session.ID, Model: aiops.DefaultModels[0], Content: "查询公告并新建隔离测试公告", RequestID: "panel-http-" + mode, AccessMode: mode, AccessVersion: &session.AccessVersion})
		if err != nil {
			t.Fatal(err)
		}
		actions := wait(actors[1], sent.Session.ID)
		if len(actions) != 1 {
			detail, _ := ai.GetSession(ctx, actors[1], sent.Session.ID)
			t.Fatalf("expected one action, got %+v, detail=%+v", actions, detail)
		}
		if mode == "standard" {
			if actions[0].Status != "pending" {
				t.Fatal("standard action not pending", actions[0])
			}
			if err = db.QueryRow(`SELECT COUNT(*) FROM v2_notice WHERE title=$1`, title+" standard").Scan(&count); err != nil || count != 0 {
				t.Fatal("unapproved action executed", err)
			}
			if _, err = ai.ApproveAction(ctx, actors[1], session.ID, actions[0].ID); err != nil {
				t.Fatal(err)
			}
			actions = wait(actors[1], session.ID)
		}
		if actions[0].Status != "succeeded" {
			t.Fatal("action failed", actions[0])
		}
		if err = db.QueryRow(`SELECT COUNT(*) FROM v2_notice WHERE title=$1 AND content='isolated test body'`, title+" "+mode).Scan(&count); err != nil || count != 1 {
			t.Fatal("real notice write missing", err, count)
		}
		encoded, _ := json.Marshal(actions[0])
		if strings.Contains(string(encoded), "parameters_cipher") {
			t.Fatal("cipher leaked into API action")
		}
		if err = ai.DeleteSession(ctx, actors[1], session.ID); err == nil {
			t.Fatal("staff deleted session")
		}
	}
	adapter := newPanelOperations(cfg, &routerState{admin: adminService})
	staff := aiops.PanelOperationActor{ID: actors[1]}
	for _, op := range adapter.Operations() {
		if op.AdminOnly {
			if adapter.Validate(ctx, staff, op.Name, json.RawMessage(`{}`)) == nil {
				t.Fatal("staff admin-only access", op.Name)
			}
		}
		if strings.HasPrefix(op.Path, "/payment/") || strings.HasPrefix(op.Path, "/ai-ops/") {
			t.Fatal("forbidden capability advertised", op.Name)
		}
	}
	if adapter.Validate(ctx, staff, "users.update", json.RawMessage(fmt.Sprintf(`{"id":%d,"email":"changed@example.test","banned":0,"is_admin":0,"is_staff":0,"password":"changed-password"}`, actors[0]))) == nil {
		t.Fatal("staff could modify admin account")
	}
	t.Run("concurrent promotion cannot bypass final mutation predicate", func(t *testing.T) {
		var target int64
		if err := db.QueryRow(`INSERT INTO v2_user(email,password,uuid,token,created_at,updated_at) VALUES('panel-race@example.test','fixture','unchanged','unchanged',1,1) RETURNING id`).Scan(&target); err != nil {
			t.Fatal(err)
		}
		defer db.Exec(`DELETE FROM v2_user WHERE id=$1`, target)
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err = tx.Exec(`UPDATE v2_user SET is_admin=1 WHERE id=$1`, target); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { _, err := adminService.ResetUserSecret(admin.WithAIStaffScope(ctx), target); done <- err }()
		for deadline := time.Now().Add(3 * time.Second); ; {
			var blocked bool
			err = db.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%SET token = $2%')`).Scan(&blocked)
			if err != nil {
				t.Fatal(err)
			}
			if blocked {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("reset did not wait for concurrent promotion")
			}
			time.Sleep(10 * time.Millisecond)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("staff reset a concurrently promoted user")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("reset remained blocked")
		}
		var token, uuid string
		if err = db.QueryRow(`SELECT token,uuid FROM v2_user WHERE id=$1`, target).Scan(&token, &uuid); err != nil || strings.TrimSpace(token) != "unchanged" || uuid != "unchanged" {
			t.Fatal("privileged secrets mutated", err)
		}
	})
	t.Log("real AI catalog -> query -> full-access write and standard approval -> actual PostgreSQL notice changes verified")
}
