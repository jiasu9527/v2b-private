package aiops

import (
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
	"golang.org/x/crypto/ssh"
)

type staffAIFixture struct {
	db                        *sql.DB
	s                         *Service
	admin, staff, other, user int64
}

func setupStaffAIFixture(t *testing.T) staffAIFixture {
	t.Helper()
	dsn := os.Getenv("FOREST_AIOPS_TEST_DSN")
	if dsn == "" {
		t.Skip("requires disposable FOREST_AIOPS_TEST_DSN")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	s := NewService(config.Config{AppKey: "staff-ai-fixture"}, db, admin.NewDBService(config.Config{}, db))
	f := staffAIFixture{db: db, s: s}
	if err = s.ensureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	marker := fmt.Sprint(time.Now().UnixNano())
	for i, item := range []struct {
		id           *int64
		admin, staff int
	}{{&f.admin, 1, 0}, {&f.staff, 0, 1}, {&f.other, 0, 1}, {&f.user, 0, 0}} {
		if err = db.QueryRow(`INSERT INTO v2_user(email,password,uuid,token,is_admin,is_staff,created_at,updated_at)VALUES($1,'fixture','fixture',$2,$3,$4,1,1)RETURNING id`, fmt.Sprintf("staff-%d-%s@example.test", i, marker), fmt.Sprintf("staff-%d-%s", i, marker), item.admin, item.staff).Scan(item.id); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		s.Close()
		db.Exec(`DELETE FROM v2_ai_session WHERE created_by IN($1,$2,$3,$4)`, f.admin, f.staff, f.other, f.user)
		db.Exec(`DELETE FROM v2_ai_server WHERE actor_id IN($1,$2,$3,$4)`, f.admin, f.staff, f.other, f.user)
		db.Exec(`DELETE FROM v2_ai_config WHERE updated_by=$1`, f.admin)
		db.Exec(`DELETE FROM v2_user WHERE id IN($1,$2,$3,$4)`, f.admin, f.staff, f.other, f.user)
		db.Close()
	})
	if _, err = s.SaveConfig(context.Background(), f.admin, ConfigSaveRequest{Enabled: true, BaseURL: "https://fixture.example.test/v1", APIKey: "relay-fixture", Models: DefaultModels}); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestAIStaffPostgresOwnershipAndManagementBoundaries(t *testing.T) {
	f := setupStaffAIFixture(t)
	ctx := context.Background()
	staffSession, err := f.s.CreateSession(ctx, f.staff, CreateSessionRequest{Model: DefaultModels[0]})
	if err != nil || staffSession.AccessMode != "full" || staffSession.FullAccessBy != f.staff {
		t.Fatal("staff default full session", staffSession, err)
	}
	adminSession, err := f.s.CreateSession(ctx, f.admin, CreateSessionRequest{Model: DefaultModels[0]})
	if err != nil {
		t.Fatal(err)
	}
	otherSession, err := f.s.CreateSession(ctx, f.other, CreateSessionRequest{Model: DefaultModels[0]})
	if err != nil {
		t.Fatal(err)
	}
	for _, actor := range []int64{f.staff, f.other, f.admin} {
		list, e := f.s.ListSessions(ctx, actor)
		if e != nil {
			t.Fatal(e)
		}
		if actor != f.admin && (len(list) != 1 || list[0].CreatedBy != actor) {
			t.Fatal("staff can see another person's session", list)
		}
		if actor == f.admin && len(list) != 3 {
			t.Fatal("admin cannot see all sessions", list)
		}
	}
	cfg, err := f.s.GetConfig(ctx, f.staff)
	if err != nil || cfg.BaseURL != "" || !cfg.Enabled || !cfg.HasAPIKey || len(cfg.Models) != 4 {
		t.Fatal("staff safe configuration", cfg, err)
	}
	for name, fn := range map[string]func() error{
		"save config":             func() error { _, e := f.s.SaveConfig(ctx, f.staff, ConfigSaveRequest{}); return e },
		"clear all":               func() error { return f.s.ClearSessions(ctx, f.staff) },
		"reset all":               func() error { return f.s.ResetData(ctx, f.staff) },
		"delete own full session": func() error { return f.s.DeleteSession(ctx, f.staff, staffSession.ID) },
		"ordinary user":           func() error { _, e := f.s.ListSessions(ctx, f.user); return e },
	} {
		t.Run(name, func(t *testing.T) {
			if e := fn(); !errors.Is(e, ErrForbidden) {
				t.Fatal("expected forbidden", e)
			}
		})
	}
	for _, foreign := range []Session{adminSession, otherSession} {
		if err = f.s.DeleteSession(ctx, f.staff, foreign.ID); !errors.Is(err, ErrForbidden) {
			t.Fatal("staff deletion of foreign session not forbidden", err)
		}
		for name, fn := range map[string]func() error{
			"detail":  func() error { _, e := f.s.GetSession(ctx, f.staff, foreign.ID); return e },
			"draft":   func() error { _, e := f.s.GetDraft(ctx, f.staff, foreign.ID); return e },
			"actions": func() error { _, e := f.s.ListActions(ctx, f.staff, foreign.ID); return e },
			"send": func() error {
				_, e := f.s.SendMessage(ctx, f.staff, SendMessageRequest{SessionID: foreign.ID, Model: DefaultModels[0], Content: "check", RequestID: fmt.Sprintf("foreign-%d", foreign.ID)})
				return e
			},
			"access": func() error {
				_, e := f.s.SetAccessMode(ctx, f.staff, AccessModeRequest{ID: foreign.ID, Mode: "standard", ExpectedVersion: &foreign.AccessVersion})
				return e
			},
			"stop":    func() error { return f.s.StopSession(ctx, f.staff, foreign.ID) },
			"approve": func() error { _, e := f.s.ApproveAction(ctx, f.staff, foreign.ID, 999); return e },
			"reject":  func() error { _, e := f.s.RejectAction(ctx, f.staff, foreign.ID, 999); return e },
			"query tool": func() error {
				_, e := f.s.callTool(ctx, f.staff, foreign.ID, ToolCall{ID: "cross-session", Function: ToolFunction{Name: "plans_list", Arguments: `{}`}})
				return e
			},
		} {
			t.Run(fmt.Sprintf("foreign-%d/%s", foreign.ID, name), func(t *testing.T) {
				if e := fn(); !errors.Is(e, ErrNotFound) {
					t.Fatal("cross-session operation not hidden", e)
				}
			})
		}
	}
	if _, err = f.s.GetSession(ctx, f.admin, staffSession.ID); err != nil {
		t.Fatal("admin cannot inspect staff", err)
	}
	if _, err = f.s.GetDraft(ctx, f.staff, staffSession.ID); err != nil {
		t.Fatal("staff cannot get own draft", err)
	}
	if _, err = f.s.SetAccessMode(ctx, f.staff, AccessModeRequest{ID: staffSession.ID, Mode: "standard", ExpectedVersion: &staffSession.AccessVersion}); err != nil {
		t.Fatal(err)
	}
	if err = f.s.StopSession(ctx, f.staff, staffSession.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.callTool(ctx, f.staff, staffSession.ID, ToolCall{ID: "own-query", Function: ToolFunction{Name: "plans_list", Arguments: `{}`}}); err != nil {
		t.Fatal("staff queries blocked", err)
	}
	// Employees can create and approve business changes in their own session.
	args := fmt.Sprintf(`{"user_id":%d,"changes":{"balance":321},"reason":"修复用户余额"}`, f.user)
	out, err := f.s.callTool(ctx, f.staff, staffSession.ID, ToolCall{ID: "staff-change", Function: ToolFunction{Name: "propose_user_update", Arguments: args}})
	if err != nil || !out.Pending {
		t.Fatal("staff change proposal", out, err)
	}
	action, err := f.s.ApproveAction(ctx, f.staff, staffSession.ID, out.ActionID)
	if err != nil || action.Status != "succeeded" || action.ApprovedBy != f.staff {
		t.Fatal("staff approval", action, err)
	}
	var balance int
	if err = f.db.QueryRow(`SELECT balance FROM v2_user WHERE id=$1`, f.user).Scan(&balance); err != nil || balance != 321 {
		t.Fatal("business change not applied", balance, err)
	}
	// Role revocation and bans apply on the next request without a new token.
	if _, err = f.db.Exec(`UPDATE v2_user SET is_staff=0 WHERE id=$1`, f.staff); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.GetSession(ctx, f.staff, staffSession.ID); !errors.Is(err, ErrForbidden) {
		t.Fatal("revoked staff remained authorized", err)
	}
	f.db.Exec(`UPDATE v2_user SET is_staff=1,banned=1 WHERE id=$1`, f.staff)
	if _, err = f.s.ListServers(ctx, f.staff); !errors.Is(err, ErrForbidden) {
		t.Fatal("banned staff remained authorized", err)
	}
	f.db.Exec(`UPDATE v2_user SET banned=0 WHERE id=$1`, f.staff)
	if _, err = f.db.Exec(`WITH turn AS (
		INSERT INTO v2_ai_turn(session_id,actor_id,request_id,request_hash,model,status,created_at,expires_at)
		VALUES($1,$2,'retained-message','fixture',$3,'completed',1,1) RETURNING id
	) INSERT INTO v2_ai_message(session_id,turn_id,role,content,created_at)
		SELECT $1,id,'user','staff conversation retained',1 FROM turn`, staffSession.ID, f.staff, DefaultModels[0]); err != nil {
		t.Fatal(err)
	}
	if err = f.s.DeleteSession(ctx, f.staff, staffSession.ID); !errors.Is(err, ErrForbidden) {
		t.Fatal("staff can delete own standard session", err)
	}
	detail, err := f.s.GetSession(ctx, f.admin, staffSession.ID)
	if err != nil || len(detail.Messages) != 1 || detail.Messages[0].Content != "staff conversation retained" || len(detail.Turns) != 1 {
		t.Fatal("admin cannot inspect retained staff conversation", detail, err)
	}
	actions, err := f.s.ListActions(ctx, f.admin, staffSession.ID)
	if err != nil || len(actions) != 1 || actions[0].ID != action.ID {
		t.Fatal("staff operation record not retained", actions, err)
	}
	if err = f.s.DeleteSession(ctx, f.admin, staffSession.ID); err != nil {
		t.Fatal("admin cannot delete staff conversation", err)
	}
	if _, err = f.s.GetSession(ctx, f.admin, staffSession.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("deleted staff session remains accessible", err)
	}
	var remaining int
	if err = f.db.QueryRow(`SELECT
		(SELECT COUNT(*) FROM v2_ai_message WHERE session_id=$1) +
		(SELECT COUNT(*) FROM v2_ai_turn WHERE session_id=$1) +
		(SELECT COUNT(*) FROM v2_ai_action WHERE session_id=$1)`, staffSession.ID).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatal("admin deletion left staff conversation data behind", remaining, err)
	}
	if _, err = f.s.GetSession(ctx, f.admin, adminSession.ID); err != nil {
		t.Fatal("deleting staff conversation damaged admin session", err)
	}
	if err = f.s.DeleteSession(ctx, f.admin, otherSession.ID); err != nil {
		t.Fatal("admin cannot delete staff session", err)
	}
}

func TestAIStaffPostgresFullWorkerAndSSHRevocation(t *testing.T) {
	f := setupStaffAIFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var calls atomic.Int32
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req completionRequest
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			w.WriteHeader(400)
			return
		}
		var response providerMessage
		if calls.Add(1) == 1 {
			response = providerMessage{Role: "assistant", ToolCalls: []ToolCall{{ID: "fix-balance", Type: "function", Function: ToolFunction{Name: "propose_user_update", Arguments: fmt.Sprintf(`{"user_id":%d,"changes":{"balance":654},"reason":"员工修复用户余额"}`, f.user)}}}}
		} else {
			response = providerMessage{Role: "assistant", Content: "已完成修改"}
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": response}}})
	}))
	defer relay.Close()
	if _, err := f.s.SaveConfig(ctx, f.admin, ConfigSaveRequest{Enabled: true, BaseURL: relay.URL + "/v1", APIKey: "fixture", Models: DefaultModels}); err != nil {
		t.Fatal(err)
	}
	result, err := f.s.SendMessage(ctx, f.staff, SendMessageRequest{Model: DefaultModels[0], Content: "修复用户余额", RequestID: fmt.Sprintf("staff-worker-%d", time.Now().UnixNano()), AccessMode: "full"})
	if err != nil {
		t.Fatal(err)
	}
	for {
		detail, e := f.s.GetSession(ctx, f.staff, result.Session.ID)
		if e != nil {
			t.Fatal(e)
		}
		if len(detail.Turns) > 0 && detail.Turns[0].Status != "running" {
			if detail.Turns[0].Status != "completed" {
				t.Fatal("staff worker failed", detail.Turns[0])
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
	var balance int
	f.db.QueryRow(`SELECT balance FROM v2_user WHERE id=$1`, f.user).Scan(&balance)
	if balance != 654 || calls.Load() != 2 {
		t.Fatal("staff full worker did not execute", balance, calls.Load())
	}
	actions, err := f.s.ListActions(ctx, f.staff, result.Session.ID)
	if err != nil || len(actions) != 1 || actions[0].ApprovalMode != "full_access" {
		t.Fatal("full action missing", actions, err)
	}
	release := make(chan struct{})
	defer close(release)
	started := make(chan struct{}, 1)
	host, port, pin, _, _ := testSSHHostWithExec(t, func(channel ssh.Channel) {
		io.WriteString(channel, "fixture running\n")
		started <- struct{}{}
		<-release
	})
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.s.storeSSHIntakesTx(ctx, tx, f.staff, result.Session.ID, result.Turn.ID, []SSHRegistrationInput{{Name: "staff-localhost", Host: host, Port: port, Username: "root", HostKeySHA256: pin, Password: "secret-test"}})
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var intakeID int64
	if err = f.db.QueryRow(`SELECT id FROM v2_ai_server_intake WHERE turn_id=$1`, result.Turn.ID).Scan(&intakeID); err != nil {
		t.Fatal(err)
	}
	registered, err := f.s.callTool(ctx, f.staff, result.Session.ID, ToolCall{ID: "staff-register", Function: ToolFunction{Name: "ssh_server_register", Arguments: fmt.Sprintf(`{"intake_id":%d,"reason":"登记员工运维服务器"}`, intakeID)}})
	if err != nil || !registered.Pending {
		t.Fatal("employee chat registration blocked", registered, err)
	}
	if _, err = f.s.ApproveAction(ctx, f.staff, result.Session.ID, registered.ActionID); err != nil {
		t.Fatal("employee server registration approval", err)
	}
	registeredServers, err := f.s.ListServers(ctx, f.staff)
	if err != nil || len(registeredServers) != 1 {
		t.Fatal("registered employee server missing", registeredServers, err)
	}
	server, err := f.s.SaveServer(ctx, f.staff, ServerSaveRequest{ID: registeredServers[0].ID, Version: registeredServers[0].Version, Name: "staff-localhost-updated", Host: host, Port: port, Username: "root", HostKeySHA256: pin, Enabled: true})
	if err != nil {
		t.Fatal("staff cannot save server", err)
	}
	servers, err := f.s.ListServers(ctx, f.other)
	if err != nil || len(servers) != 1 {
		t.Fatal("shared server inventory", servers, err)
	}
	pub, _ := json.Marshal(servers)
	if strings.Contains(string(pub), "secret-test") {
		t.Fatal("server credential leaked")
	}
	out, err := f.s.callTool(ctx, f.staff, result.Session.ID, ToolCall{ID: "staff-ssh", Function: ToolFunction{Name: "propose_ssh_command", Arguments: fmt.Sprintf(`{"server_id":%d,"command":"fixture-long-running","reason":"测试员工远程运维"}`, server.ID)}})
	if err != nil || !out.Pending {
		t.Fatal("staff SSH proposal", out, err)
	}
	action, err := f.s.ApproveAction(ctx, f.staff, result.Session.ID, out.ActionID)
	if err != nil || action.Status != "running" {
		t.Fatal("staff SSH blocked", action, err)
	}
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	revokeCtx, stopRevoke := context.WithTimeout(ctx, time.Second)
	_, err = f.db.ExecContext(revokeCtx, `UPDATE v2_user SET is_staff=0 WHERE id=$1`, f.staff)
	stopRevoke()
	if err != nil {
		t.Fatal("SSH retained role lock and blocked revocation", err)
	}
	for {
		var status string
		err = f.db.QueryRowContext(ctx, `SELECT status FROM v2_ai_action WHERE id=$1`, action.ID).Scan(&status)
		if err != nil {
			t.Fatal(err)
		}
		if status != "running" {
			if status == "succeeded" {
				t.Fatal("revoked SSH reported success")
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("SSH not stopped after revocation")
		case <-time.After(20 * time.Millisecond):
		}
	}
	if _, err = f.s.ListServers(ctx, f.staff); !errors.Is(err, ErrForbidden) {
		t.Fatal("revoked employee can still use servers", err)
	}
	if err = f.s.DeleteServer(ctx, f.other, server.ID, server.Version); err != nil {
		t.Fatal("employee server deletion", err)
	}
}
