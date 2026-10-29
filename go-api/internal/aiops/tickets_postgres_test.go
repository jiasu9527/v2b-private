package aiops

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/config"
)

type ticketTestPanel struct {
	admin.Service
	mu            sync.Mutex
	status        int64
	notifications int
}

func (p *ticketTestPanel) FetchConfig(context.Context, string) (map[string]any, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return map[string]any{"ticket": map[string]any{"ticket_status": p.status}, "email": map[string]any{"password": "MUST_NOT_LEAK"}}, nil
}
func (p *ticketTestPanel) CompareAndSetAITicketStatus(_ context.Context, expected, status int64) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.status != expected {
		return admin.ErrAITicketConfigConflict
	}
	p.status = status
	return nil
}
func (p *ticketTestPanel) NotifyAITicketReply(context.Context, int64, string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.notifications++
	return nil
}

func TestAITicketActionsPostgres(t *testing.T) {
	dsn := os.Getenv("FOREST_AIOPS_TEST_DSN")
	if dsn == "" {
		t.Skip("disposable FOREST_AIOPS_TEST_DSN required")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	panel := &ticketTestPanel{}
	s := NewService(config.Config{AppKey: "ticket-fixture-only"}, db, panel)
	defer s.Close()
	if err = s.ensureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	var actor, user, sessionID, ticketID int64
	marker := fmt.Sprint(time.Now().UnixNano())
	for _, target := range []struct {
		id     *int64
		role   int
		prefix string
	}{{&actor, 1, "actor"}, {&user, 0, "user"}} {
		if err = db.QueryRow(`INSERT INTO v2_user(email,password,uuid,token,is_admin,created_at,updated_at)VALUES($1,'unused','fixture',$2,$3,1,1)RETURNING id`, target.prefix+marker+"@example.test", target.prefix+marker, target.role).Scan(target.id); err != nil {
			t.Fatal(err)
		}
	}
	defer db.Exec(`DELETE FROM v2_user WHERE id IN($1,$2)`, actor, user)
	if _, err = s.SaveConfig(ctx, actor, ConfigSaveRequest{Enabled: true, BaseURL: "https://fixture.example.test/v1", Models: DefaultModels, APIKey: "fixture-only"}); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM v2_ai_config WHERE updated_by=$1`, actor)
	if err = db.QueryRow(`INSERT INTO v2_ai_session(title,model,created_by,created_at,updated_at)VALUES('ticket fixture','fixture',$1,1,1)RETURNING id`, actor).Scan(&sessionID); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM v2_ai_session WHERE id=$1`, sessionID)
	if err = db.QueryRow(`INSERT INTO v2_ticket(user_id,subject,level,status,reply_status,created_at,updated_at)VALUES($1,'fixture subject',1,0,0,1,1)RETURNING id`, user).Scan(&ticketID); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM v2_ticket WHERE id=$1`, ticketID)
	defer db.Exec(`DELETE FROM v2_ticket_message WHERE ticket_id=$1`, ticketID)
	if _, err = db.Exec(`INSERT INTO v2_ticket_message(user_id,ticket_id,message,created_at,updated_at)VALUES($1,$2,'user question',1,1)`, user, ticketID); err != nil {
		t.Fatal(err)
	}
	propose := func(call, operation, message string) Action {
		t.Helper()
		raw, _ := json.Marshal(ticketUpdatePayload{TicketID: ticketID, Operation: operation, Message: message})
		a, e := s.proposeAction(ctx, actor, sessionID, call, actionSpec{Kind: "ticket_update", TargetID: ticketID, TargetType: "ticket", Payload: raw, Reason: "fixture"})
		if e != nil {
			t.Fatal(e)
		}
		return a
	}
	closeAction := propose("close", "close", "")
	panel.mu.Lock()
	initialNotifications := panel.notifications
	panel.mu.Unlock()
	if initialNotifications != 0 {
		t.Fatal("proposal sent notification")
	}
	var status int64
	if err = db.QueryRow(`SELECT status FROM v2_ticket WHERE id=$1`, ticketID).Scan(&status); err != nil || status != 0 {
		t.Fatal("proposal changed ticket", err)
	}
	if _, err = s.ApproveAction(ctx, actor, sessionID, closeAction.ID); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT status FROM v2_ticket WHERE id=$1`, ticketID).Scan(&status); err != nil || status != 1 {
		t.Fatal("close not applied", err)
	}
	if _, err = s.ApproveAction(ctx, actor, sessionID, closeAction.ID); !errors.Is(err, ErrConflict) {
		t.Fatal("duplicate approval", err)
	}
	reopen := propose("reopen", "reopen", "")
	if _, err = s.ApproveAction(ctx, actor, sessionID, reopen.ID); err != nil {
		t.Fatal(err)
	}
	panel.mu.Lock()
	closedNotifications := panel.notifications
	panel.mu.Unlock()
	if closedNotifications != 0 {
		t.Fatal("close/reopen sent notification")
	}
	stale := propose("stale", "reply", "old draft")
	if _, err = db.Exec(`INSERT INTO v2_ticket_message(user_id,ticket_id,message,created_at,updated_at)VALUES($1,$2,'new user context',1,1)`, user, ticketID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ApproveAction(ctx, actor, sessionID, stale.ID); !errors.Is(err, ErrConflict) {
		t.Fatal("new message did not invalidate reply", err)
	}
	reply := propose("reply", "reply", "exact approved reply\nsecond line")
	if _, err = s.ApproveAction(ctx, actor, sessionID, reply.ID); err != nil {
		t.Fatal(err)
	}
	var count, author int64
	var message string
	if err = db.QueryRow(`SELECT COUNT(*) FROM v2_ticket_message WHERE ticket_id=$1 AND message=$2`, ticketID, "exact approved reply\nsecond line").Scan(&count); err != nil || count != 1 {
		t.Fatal("reply not once", err)
	}
	if err = db.QueryRow(`SELECT user_id,message FROM v2_ticket_message WHERE ticket_id=$1 ORDER BY id DESC LIMIT 1`, ticketID).Scan(&author, &message); err != nil || author != actor || message != "exact approved reply\nsecond line" {
		t.Fatal("reply attribution/content", err)
	}
	panel.mu.Lock()
	notifications := panel.notifications
	panel.mu.Unlock()
	if notifications != 1 {
		t.Fatal("reply notification count", notifications)
	}
	if _, err = s.ApproveAction(ctx, actor, sessionID, reply.ID); !errors.Is(err, ErrConflict) {
		t.Fatal("reply replay", err)
	}
	panel.mu.Lock()
	replayedNotifications := panel.notifications
	panel.mu.Unlock()
	if replayedNotifications != 1 {
		t.Fatal("replayed approval sent notification")
	}
	read, err := s.callTicketTool(ctx, actor, sessionID, ToolCall{Function: ToolFunction{Name: "ticket_get", Arguments: fmt.Sprintf(`{"ticket_id":%d,"limit":1}`, ticketID)}})
	if err != nil || !strings.Contains(read.Content, `"next_offset":1`) || strings.Contains(read.Content, "MUST_NOT_LEAK") {
		t.Fatal("message pagination", err)
	}
	settings, err := s.callTicketTool(ctx, actor, sessionID, ToolCall{Function: ToolFunction{Name: "ticket_settings", Arguments: `{}`}})
	if err != nil || strings.Contains(settings.Content, "MUST_NOT_LEAK") || !strings.Contains(settings.Content, `"mode":"all"`) {
		t.Fatal("settings projection", err)
	}
	raw, _ := json.Marshal(ticketSettingsPayload{Mode: "disabled", Status: 2})
	setting, err := s.proposeAction(ctx, actor, sessionID, "settings", actionSpec{Kind: "ticket_settings", TargetType: "ticket_settings", Payload: raw, Reason: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ApproveAction(ctx, actor, sessionID, setting.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var state string
		if err = db.QueryRow(`SELECT status FROM v2_ai_action WHERE id=$1`, setting.ID).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state == "succeeded" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	panel.mu.Lock()
	status = panel.status
	panel.mu.Unlock()
	if status != 2 {
		t.Fatal("settings not applied")
	}
	s.Close()
}
