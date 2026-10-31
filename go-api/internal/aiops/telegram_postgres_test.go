package aiops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type telegramFixtureTransport struct {
	mu     sync.Mutex
	bot    string
	ready  bool
	fail   bool
	calls  int
	silent int
	texts  []string
}

func (t *telegramFixtureTransport) Identity() (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.bot, t.ready
}
func (t *telegramFixtureTransport) Send(_ context.Context, _ int64, text string, _ [][]TelegramButton) (int64, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.calls++
	if t.fail {
		return 0, errors.New("fixture delivery failure")
	}
	t.texts = append(t.texts, text)
	return int64(t.calls), nil
}
func (t *telegramFixtureTransport) SendSilent(ctx context.Context, chat int64, text string, buttons [][]TelegramButton) (int64, error) {
	t.mu.Lock()
	t.silent++
	t.mu.Unlock()
	return t.Send(ctx, chat, text, buttons)
}
func (t *telegramFixtureTransport) Edit(_ context.Context, _ int64, _ int64, text string, _ [][]TelegramButton) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.calls++
	if t.fail {
		return errors.New("fixture delivery failure")
	}
	t.texts = append(t.texts, text)
	return nil
}

type telegramFixture struct {
	staffAIFixture
	b          *TelegramBridge
	transport  *telegramFixtureTransport
	update     atomic.Int64
	relayCalls atomic.Int64
}

func setupTelegramFixture(t *testing.T) *telegramFixture {
	t.Helper()
	f := &telegramFixture{staffAIFixture: setupStaffAIFixture(t)}
	f.update.Store(time.Now().UnixNano())
	f.transport = &telegramFixtureTransport{bot: fmt.Sprint(f.admin), ready: true}
	f.b = NewTelegramBridge(f.s)
	f.b.transport = f.transport
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.relayCalls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": providerMessage{Role: "assistant", Content: strings.Repeat("fixture final 😀\n", 600)}, "finish_reason": "stop"}}})
	}))
	t.Cleanup(relay.Close)
	if _, err := f.s.SaveConfig(context.Background(), f.admin, ConfigSaveRequest{Enabled: true, BaseURL: relay.URL + "/v1", APIKey: "fixture", Models: DefaultModels}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.b.SaveConfig(context.Background(), f.admin, TelegramConfig{Enabled: true, DefaultModel: DefaultModels[0], DefaultAccessMode: "full", StreamUpdates: true, NotifyCompletion: false}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		f.b.Close()
		for _, q := range []string{`DELETE FROM v2_ai_telegram_inbox WHERE actor_id IN($1,$2,$3,$4)`, `DELETE FROM v2_ai_telegram_binding WHERE actor_id IN($1,$2,$3,$4)`, `DELETE FROM v2_ai_telegram_code WHERE actor_id IN($1,$2,$3,$4)`} {
			_, _ = f.db.Exec(q, f.admin, f.staff, f.other, f.user)
		}
		_, _ = f.db.Exec(`DELETE FROM v2_ai_telegram_inbox WHERE bot_id=$1`, f.transport.bot)
	})
	return f
}

func (f *telegramFixture) input(actor int64, text string) TelegramInput {
	return TelegramInput{BotID: f.transport.bot, UpdateID: f.update.Add(1), FromID: actor + 100000000, ChatID: actor + 100000000, IsPrivate: true, Text: text}
}
func (f *telegramFixture) bind(t *testing.T, actor int64) {
	t.Helper()
	code, err := f.b.CreateBindingCode(context.Background(), f.admin, actor)
	if err != nil {
		t.Fatal(err)
	}
	handled, err := f.b.HandleUpdate(context.Background(), f.input(actor, code.Command))
	if err != nil || !handled {
		t.Fatal("bind", handled, err)
	}
}
func (f *telegramFixture) waitTurn(t *testing.T, session int64) {
	t.Helper()
	for end := time.Now().Add(8 * time.Second); time.Now().Before(end); {
		d, err := f.s.GetSession(context.Background(), f.admin, session)
		if err != nil {
			t.Fatal(err)
		}
		if len(d.Turns) > 0 && d.Turns[0].Status != "running" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("turn did not complete")
}

func TestTelegramPostgresBindingUsesIndependentOneTimeAuthorization(t *testing.T) {
	f := setupTelegramFixture(t)
	ctx := context.Background()
	if _, err := f.b.CreateBindingCode(ctx, f.staff, f.staff); !errors.Is(err, ErrForbidden) {
		t.Fatal("staff self-issued code", err)
	}
	if _, err := f.b.CreateBindingCode(ctx, f.admin, f.user); !errors.Is(err, ErrForbidden) {
		t.Fatal("ordinary user authorized", err)
	}
	if _, err := f.db.Exec(`UPDATE v2_user SET telegram_id=$2 WHERE id=$1`, f.staff, f.staff+100000000); err != nil {
		t.Fatal(err)
	}
	if handled, err := f.b.HandleUpdate(ctx, f.input(f.staff, "/ai")); err != nil || !handled {
		t.Fatal(err)
	}
	var count int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM v2_ai_telegram_binding WHERE actor_id=$1`, f.staff).Scan(&count); err != nil || count != 0 {
		t.Fatal("legacy subscription binding authorized AI", count, err)
	}
	code, err := f.b.CreateBindingCode(ctx, f.admin, f.staff)
	if err != nil {
		t.Fatal(err)
	}
	var hash string
	if err = f.db.QueryRow(`SELECT code_hash FROM v2_ai_telegram_code WHERE actor_id=$1`, f.staff).Scan(&hash); err != nil || hash == code.Code || hash != hashOperation(code.Code) {
		t.Fatal("binding code not hashed", err)
	}
	in := f.input(f.staff, code.Command)
	for attempt := 0; attempt < 2; attempt++ {
		if _, err = f.b.HandleUpdate(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = f.b.HandleUpdate(ctx, f.input(f.staff, code.Command)); err != nil {
		t.Fatal("used code should be friendly response", err)
	}
	var generation int64
	if err = f.db.QueryRow(`SELECT generation FROM v2_ai_telegram_binding WHERE actor_id=$1`, f.staff).Scan(&generation); err != nil || generation != 1 {
		t.Fatal("code reused", generation, err)
	}
	status, err := f.b.Status(ctx, f.staff)
	if err != nil || len(status.Bindings) != 1 || status.MyBinding == nil {
		t.Fatal(status, err)
	}
	if err = f.b.RevokeBinding(ctx, f.staff, f.staff); err != nil {
		t.Fatal(err)
	}
}

func TestTelegramPostgresDedupFrozenRequestAndOutboxRetry(t *testing.T) {
	f := setupTelegramFixture(t)
	ctx := context.Background()
	f.bind(t, f.admin)
	in := f.input(f.admin, "fixture task that must run once")
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := f.b.HandleUpdate(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	var cipher, requestID string
	var inboxID int64
	if err := f.db.QueryRow(`SELECT id,payload_cipher,request_id FROM v2_ai_telegram_inbox WHERE bot_id=$1 AND update_id=$2`, in.BotID, in.UpdateID).Scan(&inboxID, &cipher, &requestID); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(cipher, in.Text) || !strings.HasPrefix(cipher, "v1:") {
		t.Fatal("raw task persisted", cipher)
	}
	plain, err := f.s.decryptSecret(cipher)
	if err != nil {
		t.Fatal(err)
	}
	var frozen SendMessageRequest
	if json.Unmarshal([]byte(plain), &frozen) != nil || frozen.AccessMode != "full" || frozen.SessionID != 0 {
		t.Fatal("request not frozen", plain)
	}
	if err = f.b.dispatchTelegram(ctx); err != nil {
		t.Fatal(err)
	}
	var session int64
	if err = f.db.QueryRow(`SELECT session_id FROM v2_ai_telegram_inbox WHERE id=$1`, inboxID).Scan(&session); err != nil {
		t.Fatal(err)
	}
	f.waitTurn(t, session)
	// Emulate loss of the inbox acknowledgement after SendMessage committed.
	if _, err = f.db.Exec(`UPDATE v2_ai_telegram_inbox SET status='queued',payload_cipher=$2,turn_id=NULL,session_id=NULL WHERE id=$1`, inboxID, cipher); err != nil {
		t.Fatal(err)
	}
	if err = f.b.dispatchTelegram(ctx); err != nil {
		t.Fatal(err)
	}
	var turns int
	if err = f.db.QueryRow(`SELECT COUNT(*) FROM v2_ai_turn WHERE actor_id=$1 AND request_id=$2`, f.admin, requestID).Scan(&turns); err != nil || turns != 1 || f.relayCalls.Load() != 1 {
		t.Fatal("task reran after ack loss", turns, f.relayCalls.Load(), err)
	}
	if err = f.b.pollTelegramTurn(ctx, inboxID); err != nil {
		t.Fatal(err)
	}
	f.transport.fail = true
	if _, err = f.b.deliverTelegram(ctx); err != nil {
		t.Fatal(err)
	}
	f.transport.fail = false
	if _, err = f.db.Exec(`UPDATE v2_ai_telegram_outbox SET next_attempt_at=0 WHERE bot_id=$1`, in.BotID); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 12; n++ {
		sent, err := f.b.deliverTelegram(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !sent {
			break
		}
	}
	if f.relayCalls.Load() != 1 {
		t.Fatal("Telegram delivery failure reran model")
	}
	var queued int
	if err = f.db.QueryRow(`SELECT COUNT(*) FROM v2_ai_telegram_outbox WHERE inbox_id=$1 AND output_key LIKE 'final:%' AND status='sent'`, inboxID).Scan(&queued); err != nil || queued < 1 || f.transport.silent < 1 {
		t.Fatal("silent completion truncated body", queued, f.transport.silent, err)
	}
	// Existing full sessions must freeze the recorded access version explicitly.
	if _, err = f.b.HandleUpdate(ctx, f.input(f.admin, "fixture second turn")); err != nil {
		t.Fatal(err)
	}
	if err = f.db.QueryRow(`SELECT payload_cipher FROM v2_ai_telegram_inbox WHERE actor_id=$1 AND status='queued' AND kind='message' ORDER BY id DESC LIMIT 1`, f.admin).Scan(&cipher); err != nil {
		t.Fatal(err)
	}
	plain, err = f.s.decryptSecret(cipher)
	if err != nil || json.Unmarshal([]byte(plain), &frozen) != nil || frozen.AccessMode != "full" || frozen.AccessVersion == nil || *frozen.AccessVersion <= 0 || frozen.SessionID != session {
		t.Fatal("existing full authorization/version lost", plain, err)
	}
}

func TestTelegramPostgresRevocationDeletionAndStopPreventDispatch(t *testing.T) {
	f := setupTelegramFixture(t)
	ctx := context.Background()
	f.bind(t, f.staff)
	in := f.input(f.staff, "must never run")
	if _, err := f.b.HandleUpdate(ctx, in); err != nil {
		t.Fatal(err)
	}
	if _, err := f.b.HandleUpdate(ctx, f.input(f.staff, "/aistop")); err != nil {
		t.Fatal(err)
	}
	if err := f.b.dispatchTelegram(ctx); err != nil {
		t.Fatal(err)
	}
	if f.relayCalls.Load() != 0 {
		t.Fatal("stop started queued message")
	}
	if _, err := f.b.HandleUpdate(ctx, f.input(f.staff, "another cancelled task")); err != nil {
		t.Fatal(err)
	}
	other := NewTelegramBridge(f.s)
	other.transport = f.transport
	if err := other.RevokeBinding(ctx, f.admin, f.staff); err != nil {
		t.Fatal(err)
	}
	if err := f.b.dispatchTelegram(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.b.deliverTelegram(ctx); err != nil {
		t.Fatal(err)
	}
	if f.relayCalls.Load() != 0 {
		t.Fatal("revoked binding dispatched queued request")
	}
	f.bind(t, f.admin)
	in = f.input(f.admin, "fixture deleted session")
	if _, err := f.b.HandleUpdate(ctx, in); err != nil {
		t.Fatal(err)
	}
	if err := f.b.dispatchTelegram(ctx); err != nil {
		t.Fatal(err)
	}
	var session, inboxID int64
	if err := f.db.QueryRow(`SELECT id,session_id FROM v2_ai_telegram_inbox WHERE bot_id=$1 AND update_id=$2`, in.BotID, in.UpdateID).Scan(&inboxID, &session); err != nil {
		t.Fatal(err)
	}
	f.waitTurn(t, session)
	if err := f.s.DeleteSession(ctx, f.admin, session); err != nil {
		t.Fatal(err)
	}
	if _, err := f.b.HandleUpdate(ctx, in); err != nil {
		t.Fatal(err)
	}
	var state, payload string
	if err := f.db.QueryRow(`SELECT status,payload_cipher FROM v2_ai_telegram_inbox WHERE id=$1`, inboxID).Scan(&state, &payload); err != nil || state != "cancelled" || payload != "" {
		t.Fatal("delete removed replay tombstone or left frozen input", state, err)
	}
	before := f.relayCalls.Load()
	if err := f.b.dispatchTelegram(ctx); err != nil {
		t.Fatal(err)
	}
	if f.relayCalls.Load() != before {
		t.Fatal("deleted task resurrected")
	}
}

func TestTelegramPostgresExpiredTurnCompletesWithoutModelReplay(t *testing.T) {
	f := setupTelegramFixture(t)
	ctx := context.Background()
	f.bind(t, f.admin)
	in := f.input(f.admin, "fixture original interrupted task")
	if _, err := f.b.HandleUpdate(ctx, in); err != nil {
		t.Fatal(err)
	}
	session, turn := knowledgeRunningTurn(t, f.staffAIFixture, f.admin)
	var inboxID int64
	var request string
	if err := f.db.QueryRow(`SELECT id,request_id FROM v2_ai_telegram_inbox WHERE bot_id=$1 AND update_id=$2`, in.BotID, in.UpdateID).Scan(&inboxID, &request); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE v2_ai_turn SET request_id=$2,expires_at=$3 WHERE id=$1`, turn, request, time.Now().Add(-time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE v2_ai_session SET status='running' WHERE id=$1`, session); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE v2_ai_telegram_inbox SET status='running',session_id=$2,turn_id=$3,payload_cipher='' WHERE id=$1`, inboxID, session, turn); err != nil {
		t.Fatal(err)
	}
	if err := f.b.pollTelegramTurn(ctx, inboxID); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := f.db.QueryRow(`SELECT status FROM v2_ai_turn WHERE id=$1`, turn).Scan(&status); err != nil || status != "interrupted" {
		t.Fatal("expired turn never reconciled", status, err)
	}
	if err := f.db.QueryRow(`SELECT status FROM v2_ai_telegram_inbox WHERE id=$1`, inboxID).Scan(&status); err != nil || status != "done" || f.relayCalls.Load() != 0 {
		t.Fatal("restart recovery replayed task or kept inbox running", status, f.relayCalls.Load(), err)
	}
}

func TestTelegramPostgresBackoffRevocationAndDisabledFeedback(t *testing.T) {
	f := setupTelegramFixture(t)
	ctx := context.Background()
	f.bind(t, f.admin)
	var inboxID int64
	if err := f.db.QueryRow(`SELECT id FROM v2_ai_telegram_inbox WHERE actor_id=$1 AND kind='bind'`, f.admin).Scan(&inboxID); err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(10 * time.Minute).Unix()
	if _, err := f.db.Exec(`UPDATE v2_ai_telegram_outbox SET next_attempt_at=$2,attempts=5 WHERE inbox_id=$1`, inboxID, until); err != nil {
		t.Fatal(err)
	}
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	i, err := scanTelegramInbox(tx.QueryRowContext(ctx, `SELECT `+telegramInboxColumns+` FROM v2_ai_telegram_inbox WHERE id=$1`, inboxID))
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = f.b.queueTelegramOutput(ctx, tx, i, "reply", "updated latest draft", nil); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var next, attempts int64
	if err = f.db.QueryRow(`SELECT next_attempt_at,attempts FROM v2_ai_telegram_outbox WHERE inbox_id=$1`, inboxID).Scan(&next, &attempts); err != nil || next != until || attempts != 5 {
		t.Fatal("draft update bypassed 429/backoff", next, attempts, err)
	}
	// Model removal must never prevent disabling the bridge.
	if _, err = f.b.SaveConfig(ctx, f.admin, TelegramConfig{Enabled: false, DefaultModel: "removed-model", DefaultAccessMode: "full"}); err != nil {
		t.Fatal("disabled blocked by removed model", err)
	}
	if _, err = f.b.HandleUpdate(ctx, f.input(f.admin, "/ai")); err != nil {
		t.Fatal(err)
	}
	if err = f.b.sweepTelegram(ctx); err != nil {
		t.Fatal(err)
	}
	if sent, err := f.b.deliverTelegram(ctx); err != nil || !sent {
		t.Fatal("disabled notice was cancelled by sweep", sent, err)
	}
	if len(f.transport.texts) != 1 || !strings.Contains(f.transport.texts[0], "未启用") {
		t.Fatal("unexpected disabled response", f.transport.texts)
	}
}

func TestTelegramPostgresControlCommandsDoNotFreezeOldSession(t *testing.T) {
	f := setupTelegramFixture(t)
	ctx := context.Background()
	f.bind(t, f.admin)
	if _, err := f.b.HandleUpdate(ctx, f.input(f.admin, "/ainew")); err != nil {
		t.Fatal(err)
	}
	in := f.input(f.admin, "must wait for new session confirmation")
	if _, err := f.b.HandleUpdate(ctx, in); err != nil {
		t.Fatal(err)
	}
	var kind string
	if err := f.db.QueryRow(`SELECT kind FROM v2_ai_telegram_inbox WHERE bot_id=$1 AND update_id=$2`, in.BotID, in.UpdateID).Scan(&kind); err != nil || kind != "notice" {
		t.Fatal("text was frozen against old session while switch pending", kind, err)
	}
	if _, err := f.b.SaveConfig(ctx, f.admin, TelegramConfig{Enabled: true, DefaultModel: DefaultModels[1], DefaultAccessMode: "standard"}); err != nil {
		t.Fatal(err)
	}
	if err := f.b.dispatchTelegram(ctx); err != nil {
		t.Fatal(err)
	}
	var model, mode string
	if err := f.db.QueryRow(`SELECT model,access_mode FROM v2_ai_telegram_binding WHERE actor_id=$1`, f.admin).Scan(&model, &mode); err != nil || model != DefaultModels[1] || mode != "standard" {
		t.Fatal("new conversation used stale binding defaults", model, mode, err)
	}
}

func TestTelegramPostgresRoleAndBotRevocationStopPersistedTurns(t *testing.T) {
	for _, reason := range []string{"binding", "role", "bot"} {
		t.Run(reason, func(t *testing.T) {
			f := setupTelegramFixture(t)
			ctx := context.Background()
			f.bind(t, f.staff)
			in := f.input(f.staff, "persisted running task")
			if _, err := f.b.HandleUpdate(ctx, in); err != nil {
				t.Fatal(err)
			}
			session, turn := knowledgeRunningTurn(t, f.staffAIFixture, f.staff)
			var inboxID int64
			var request string
			if err := f.db.QueryRow(`SELECT id,request_id FROM v2_ai_telegram_inbox WHERE bot_id=$1 AND update_id=$2`, in.BotID, in.UpdateID).Scan(&inboxID, &request); err != nil {
				t.Fatal(err)
			}
			if _, err := f.db.Exec(`UPDATE v2_ai_turn SET request_id=$2 WHERE id=$1`, turn, request); err != nil {
				t.Fatal(err)
			}
			if _, err := f.db.Exec(`UPDATE v2_ai_telegram_inbox SET status='running',session_id=$2,turn_id=$3,payload_cipher='' WHERE id=$1`, inboxID, session, turn); err != nil {
				t.Fatal(err)
			}
			other := NewTelegramBridge(f.s)
			other.transport = f.transport
			switch reason {
			case "binding":
				if err := other.RevokeBinding(ctx, f.admin, f.staff); err != nil {
					t.Fatal(err)
				}
			case "role":
				if _, err := f.db.Exec(`UPDATE v2_user SET is_staff=0 WHERE id=$1`, f.staff); err != nil {
					t.Fatal(err)
				}
				if err := other.sweepTelegram(ctx); err != nil {
					t.Fatal(err)
				}
			case "bot":
				f.transport.ready = false
				if err := other.sweepTelegram(ctx); err != nil {
					t.Fatal(err)
				}
			}
			var stopped bool
			var status string
			if err := f.db.QueryRow(`SELECT stop_requested FROM v2_ai_turn WHERE id=$1`, turn).Scan(&stopped); err != nil || !stopped {
				t.Fatal("revocation did not durably stop turn", reason, err)
			}
			if err := f.db.QueryRow(`SELECT status FROM v2_ai_telegram_inbox WHERE id=$1`, inboxID).Scan(&status); err != nil || status != "cancelled" {
				t.Fatal("revocation did not cancel inbox", status, err)
			}
			if _, err := f.b.deliverTelegram(ctx); err != nil {
				t.Fatal(err)
			}
			if f.transport.calls != 0 {
				t.Fatal("revoked work still delivered output")
			}
		})
	}
}

func TestTelegramPostgresApprovalRequiresDeliveredExactCard(t *testing.T) {
	f := setupTelegramFixture(t)
	ctx := context.Background()
	f.bind(t, f.staff)
	runtime := &fixturePanelOperations{ops: []PanelOperation{fixtureOperation("notices.save", "/notice/save", true, false)}, data: json.RawMessage(`{"saved":true}`)}
	f.s.SetPanelOperations(runtime)
	session, err := f.s.CreateSession(ctx, f.staff, CreateSessionRequest{Model: DefaultModels[0]})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.SetAccessMode(ctx, f.staff, AccessModeRequest{ID: session.ID, Mode: "standard", ExpectedVersion: &session.AccessVersion}); err != nil {
		t.Fatal(err)
	}
	parameters, _ := json.Marshal(map[string]any{"value": strings.Repeat("完整变更", 1200), "api_key": "PRIVATE_SENTINEL"})
	payload, _ := json.Marshal(panelOperationPayload{Operation: "notices.save", Parameters: parameters})
	a, err := f.s.proposeAction(ctx, f.staff, session.ID, "telegram-card-fixture", actionSpec{Kind: "panel_operation", TargetType: "notices.save", Payload: payload, Reason: "fixture change"})
	if err != nil {
		t.Fatal(err)
	}
	var inboxID int64
	if err = f.db.QueryRow(`SELECT id FROM v2_ai_telegram_inbox WHERE actor_id=$1 AND kind='bind'`, f.staff).Scan(&inboxID); err != nil {
		t.Fatal(err)
	}
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	i, err := scanTelegramInbox(tx.QueryRowContext(ctx, `SELECT `+telegramInboxColumns+` FROM v2_ai_telegram_inbox WHERE id=$1`, inboxID))
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	i.SessionID = session.ID
	if err = f.b.queueTelegramActionCards(ctx, tx, i, []Action{a}); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	card, key, err := telegramActionCard(a)
	if err != nil || strings.Contains(card, "PRIVATE_SENTINEL") {
		t.Fatal("card exposed secret", err)
	}
	callback := func(key string) TelegramInput {
		in := f.input(f.staff, "")
		in.Callback = fmt.Sprintf("ai:approve:%d:%d:%s", session.ID, a.ID, key)
		return in
	}
	if _, err = f.b.HandleUpdate(ctx, callback(key)); err != nil {
		t.Fatal(err)
	}
	if err = f.b.dispatchTelegram(ctx); err != nil {
		t.Fatal(err)
	}
	if runtime.count() != 0 {
		t.Fatal("undelivered action card approved")
	}
	for n := 0; n < 20; n++ {
		sent, err := f.b.deliverTelegram(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !sent {
			break
		}
	}
	if _, err = f.b.HandleUpdate(ctx, callback("forged-fingerprint")); err != nil {
		t.Fatal(err)
	}
	if err = f.b.dispatchTelegram(ctx); err != nil {
		t.Fatal(err)
	}
	if runtime.count() != 0 {
		t.Fatal("forged card approved")
	}
	in := callback(key)
	if _, err = f.b.HandleUpdate(ctx, in); err != nil {
		t.Fatal(err)
	}
	if err = f.b.dispatchTelegram(ctx); err != nil {
		t.Fatal(err)
	}
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end) && runtime.count() == 0; {
		time.Sleep(10 * time.Millisecond)
	}
	if runtime.count() != 1 {
		t.Fatal("complete delivered card did not execute once", runtime.count())
	}
	if _, err = f.b.HandleUpdate(ctx, in); err != nil {
		t.Fatal(err)
	}
	if err = f.b.dispatchTelegram(ctx); err != nil {
		t.Fatal(err)
	}
	if runtime.count() != 1 {
		t.Fatal("duplicate approval executed twice")
	}
	var linked int64
	if err = f.db.QueryRow(`SELECT action_id FROM v2_ai_telegram_inbox WHERE bot_id=$1 AND update_id=$2`, in.BotID, in.UpdateID).Scan(&linked); err != nil || linked != a.ID {
		t.Fatal("approval target not durable before dispatch", linked, err)
	}
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); {
		var status string
		if err = f.db.QueryRow(`SELECT status FROM v2_ai_action WHERE id=$1`, a.ID).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status != "running" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Simulate a surviving remote action when the command worker's ack was
	// lost. This web-created session has no TG-origin turn to join against.
	if _, err = f.db.Exec(`UPDATE v2_ai_action SET status='running',stop_requested=FALSE WHERE id=$1`, a.ID); err != nil {
		t.Fatal(err)
	}
	if err = f.b.RevokeBinding(ctx, f.admin, f.staff); err != nil {
		t.Fatal(err)
	}
	var stopped bool
	if err = f.db.QueryRow(`SELECT stop_requested FROM v2_ai_action WHERE id=$1`, a.ID).Scan(&stopped); err != nil || !stopped {
		t.Fatal("revocation missed TG-approved action in web session", stopped, err)
	}
}

func TestTelegramPostgresCompletionNotificationDoesNotDuplicateAnswer(t *testing.T) {
	f := setupTelegramFixture(t)
	ctx := context.Background()
	f.bind(t, f.admin)
	if _, err := f.b.SaveConfig(ctx, f.admin, TelegramConfig{Enabled: true, DefaultModel: DefaultModels[0], DefaultAccessMode: "full", NotifyCompletion: true}); err != nil {
		t.Fatal(err)
	}
	in := f.input(f.admin, "notification fixture")
	if _, err := f.b.HandleUpdate(ctx, in); err != nil {
		t.Fatal(err)
	}
	if err := f.b.dispatchTelegram(ctx); err != nil {
		t.Fatal(err)
	}
	var inbox, session int64
	if err := f.db.QueryRow(`SELECT id,session_id FROM v2_ai_telegram_inbox WHERE bot_id=$1 AND update_id=$2`, in.BotID, in.UpdateID).Scan(&inbox, &session); err != nil {
		t.Fatal(err)
	}
	f.waitTurn(t, session)
	for n := 0; n < 2; n++ {
		if err := f.b.pollTelegramTurn(ctx, inbox); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	var silent bool
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM v2_ai_telegram_outbox WHERE inbox_id=$1 AND output_key='completion_notice'`, inbox).Scan(&count); err != nil || count != 1 {
		t.Fatal("completion notification not unique", count, err)
	}
	if err := f.db.QueryRow(`SELECT BOOL_AND(silent) FROM v2_ai_telegram_outbox WHERE inbox_id=$1 AND output_key<>'completion_notice'`, inbox).Scan(&silent); err != nil || !silent {
		t.Fatal("full answer chunks should not notify repeatedly", silent, err)
	}
}

func TestTelegramPostgresInvalidInputAcknowledgedWithoutTaskOrRetry(t *testing.T) {
	f := setupTelegramFixture(t)
	ctx := context.Background()
	f.bind(t, f.staff)
	for _, text := range []string{"/aistop invalid", "/aistop 9223372036854775807", strings.Repeat("x", maxContentBytes+1)} {
		in := f.input(f.staff, text)
		for n := 0; n < 2; n++ {
			handled, err := f.b.HandleUpdate(ctx, in)
			if !handled || err != nil {
				t.Fatal("user input produced retryable webhook error", handled, err)
			}
		}
		var kind, cipher string
		var count int
		if err := f.db.QueryRow(`SELECT COUNT(*) FROM v2_ai_telegram_inbox WHERE bot_id=$1 AND update_id=$2`, in.BotID, in.UpdateID).Scan(&count); err != nil || count != 1 {
			t.Fatal("invalid input lost dedup", count, err)
		}
		if err := f.db.QueryRow(`SELECT kind,payload_cipher FROM v2_ai_telegram_inbox WHERE bot_id=$1 AND update_id=$2`, in.BotID, in.UpdateID).Scan(&kind, &cipher); err != nil || kind != "rejected" || cipher != "" {
			t.Fatal("invalid input became queued or persisted raw", kind, err)
		}
	}
	if f.relayCalls.Load() != 0 {
		t.Fatal("invalid input called model")
	}
}

func TestTelegramPostgresDispatchGuardDoesNotLockOperatorAcrossAIService(t *testing.T) {
	f := setupTelegramFixture(t)
	ctx := context.Background()
	f.bind(t, f.admin)
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	i, err := scanTelegramInbox(tx.QueryRowContext(ctx, `SELECT `+telegramInboxColumns+` FROM v2_ai_telegram_inbox WHERE actor_id=$1 AND kind='bind'`, f.admin))
	if err != nil {
		t.Fatal(err)
	}
	if !f.b.validTelegramInbox(ctx, tx, i) {
		t.Fatal("binding guard rejected valid operator")
	}
	other, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Rollback()
	if _, err = other.ExecContext(ctx, `SET LOCAL lock_timeout='300ms'`); err != nil {
		t.Fatal(err)
	}
	if _, err = other.ExecContext(ctx, `UPDATE v2_user SET remarks='fixture harmless self update' WHERE id=$1`, f.admin); err != nil {
		t.Fatal("dispatch outer transaction locks operator across nested AI call", err)
	}
}
