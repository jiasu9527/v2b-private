package aiops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

type miniFixtureTransport struct {
	*telegramFixtureTransport
	secret string
}

func (m *miniFixtureTransport) MiniBotToken() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.secret
}

type miniFixture struct {
	*telegramFixture
	miniTransport *miniFixtureTransport
}

func setupMiniFixture(t *testing.T) *miniFixture {
	t.Helper()
	f := &miniFixture{telegramFixture: setupTelegramFixture(t)}
	f.miniTransport = &miniFixtureTransport{telegramFixtureTransport: f.transport, secret: f.transport.bot + ":fixture_secret"}
	f.b.transport = f.miniTransport
	t.Cleanup(func() {
		for _, q := range []string{`DELETE FROM v2_ai_telegram_mini_request WHERE actor_id IN($1,$2,$3,$4)`, `DELETE FROM v2_ai_telegram_mini_token WHERE actor_id IN($1,$2,$3,$4)`} {
			_, _ = f.db.Exec(q, f.admin, f.staff, f.other, f.user)
		}
	})
	return f
}

func (f *miniFixture) auth(t *testing.T, actor int64) MiniAuthResult {
	t.Helper()
	raw := miniFixtureInitData(f.miniTransport.MiniBotToken(), actor+100000000, time.Now().Unix(), nil)
	result, err := f.b.AuthenticateMini(context.Background(), raw)
	if err != nil {
		t.Fatal("mini auth", err)
	}
	return result
}

func (f *miniFixture) freeze(t *testing.T, token string, req SendMessageRequest) {
	t.Helper()
	raw, _ := json.Marshal(req)
	cipher, err := f.s.encryptSecret(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	if err = f.b.prepareMiniRequest(context.Background(), token, miniRequest{key: "send:" + req.RequestID, kind: "send", hash: requestHash(req), cipher: cipher, requestID: req.RequestID, session: req.SessionID}); err != nil {
		t.Fatal(err)
	}
}

func TestTelegramMiniPostgresIndependentBindingAndTokenLifecycle(t *testing.T) {
	f := setupMiniFixture(t)
	ctx := context.Background()
	if _, err := f.db.Exec(`UPDATE v2_user SET telegram_id=$2 WHERE id=$1`, f.staff, f.staff+100000000); err != nil {
		t.Fatal(err)
	}
	raw := miniFixtureInitData(f.miniTransport.MiniBotToken(), f.staff+100000000, time.Now().Unix(), nil)
	if _, err := f.b.AuthenticateMini(ctx, raw); !errors.Is(err, ErrForbidden) {
		t.Fatal("ordinary subscription binding authorized Mini", err)
	}
	f.bind(t, f.staff)
	auth := f.auth(t, f.staff)
	if auth.User.ID != f.staff || auth.User.IsStaff != 1 || auth.User.IsAdmin != 0 || auth.User.ExpiresAt != auth.ExpiresAt || auth.ExpiresAt < time.Now().Add(miniTokenLifetime-time.Minute).Unix() || !strings.HasPrefix(auth.Token, "tma_") {
		t.Fatal(auth)
	}
	var stored string
	if err := f.db.QueryRow(`SELECT token_hash FROM v2_ai_telegram_mini_token WHERE actor_id=$1`, f.staff).Scan(&stored); err != nil || stored != hashOperation(auth.Token) || strings.Contains(stored, auth.Token) {
		t.Fatal("usable token persisted", stored, err)
	}
	if _, err := f.b.AuthenticateMiniToken(ctx, "fake_jwt"); !errors.Is(err, ErrForbidden) {
		t.Fatal("wrong token accepted", err)
	}
	for _, change := range []struct{ label, set, restore string }{
		{"ban", "UPDATE v2_user SET banned=1 WHERE id=$1", "UPDATE v2_user SET banned=0 WHERE id=$1"},
		{"role", "UPDATE v2_user SET is_staff=0 WHERE id=$1", "UPDATE v2_user SET is_staff=1 WHERE id=$1"},
		{"generation", "UPDATE v2_ai_telegram_binding SET generation=generation+1 WHERE actor_id=$1", "UPDATE v2_ai_telegram_binding SET generation=generation-1 WHERE actor_id=$1"},
		{"unbind", "UPDATE v2_ai_telegram_binding SET active=FALSE WHERE actor_id=$1", "UPDATE v2_ai_telegram_binding SET active=TRUE WHERE actor_id=$1"},
		{"expiration", "UPDATE v2_ai_telegram_mini_token SET expires_at=1 WHERE actor_id=$1", "UPDATE v2_ai_telegram_mini_token SET expires_at=4102444800 WHERE actor_id=$1"},
	} {
		t.Run(change.label, func(t *testing.T) {
			if _, err := f.db.Exec(change.set, f.staff); err != nil {
				t.Fatal(err)
			}
			if _, err := f.b.AuthenticateMiniToken(ctx, auth.Token); !errors.Is(err, ErrForbidden) {
				t.Fatal("stale authorization accepted", err)
			}
			if _, err := f.db.Exec(change.restore, f.staff); err != nil {
				t.Fatal(err)
			}
		})
	}
	f.transport.mu.Lock()
	f.miniTransport.secret += "_rotated"
	f.transport.mu.Unlock()
	if _, err := f.b.AuthenticateMiniToken(ctx, auth.Token); !errors.Is(err, ErrForbidden) {
		t.Fatal("old bot token session authorized", err)
	}
	if _, err := f.b.AuthenticateMini(ctx, raw); !errors.Is(err, ErrForbidden) {
		t.Fatal("old initData authorized", err)
	}
	auth = f.auth(t, f.staff)
	f.transport.mu.Lock()
	f.transport.ready = false
	f.transport.mu.Unlock()
	if _, err := f.b.AuthenticateMiniToken(ctx, auth.Token); !errors.Is(err, ErrForbidden) {
		t.Fatal("unready bot authorized", err)
	}
	f.transport.mu.Lock()
	f.transport.ready = true
	f.transport.mu.Unlock()
	for i := 0; i < 20; i++ {
		auth = f.auth(t, f.staff)
	}
	var count int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM v2_ai_telegram_mini_token WHERE actor_id=$1`, f.staff).Scan(&count); err != nil || count != 16 {
		t.Fatal("unbounded token accumulation", count, err)
	}
	if _, err := f.b.SaveConfig(ctx, f.admin, TelegramConfig{Enabled: false, DefaultModel: DefaultModels[0], DefaultAccessMode: "full"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.b.AuthenticateMiniToken(ctx, auth.Token); !errors.Is(err, ErrForbidden) {
		t.Fatal("disabled feature authorized", err)
	}
}

func TestTelegramMiniPostgresSendFrozenIdempotenceAndNoChatDelivery(t *testing.T) {
	f := setupMiniFixture(t)
	ctx := context.Background()
	f.bind(t, f.staff)
	auth := f.auth(t, f.staff)
	var before int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM v2_ai_telegram_outbox WHERE actor_id=$1`, f.staff).Scan(&before); err != nil {
		t.Fatal(err)
	}
	req := SendMessageRequest{Model: DefaultModels[0], Content: "MINI_PRIVATE_SENTINEL", RequestID: "mini-send-first"}
	result, err := f.b.MiniSendMessage(ctx, auth.Token, req)
	if err != nil || result.Session.Source != "telegram" || result.Session.CreatedBy != f.staff {
		t.Fatal(result, err)
	}
	f.waitTurn(t, result.Session.ID)
	var cipher string
	if err = f.db.QueryRow(`SELECT payload_cipher FROM v2_ai_telegram_mini_request WHERE actor_id=$1`, f.staff).Scan(&cipher); err != nil || strings.Contains(cipher, req.Content) || !strings.HasPrefix(cipher, "v1:") {
		t.Fatal("plaintext request persisted", cipher, err)
	}
	if _, err = f.db.Exec(`UPDATE v2_ai_telegram_mini_request SET status='queued',session_id=NULL,turn_id=NULL WHERE actor_id=$1`, f.staff); err != nil {
		t.Fatal(err)
	}
	replay, err := f.b.MiniSendMessage(ctx, auth.Token, req)
	if err != nil || replay.Turn.ID != result.Turn.ID || f.relayCalls.Load() != 1 {
		t.Fatal("lost ack replay duplicated", replay, err, f.relayCalls.Load())
	}
	if _, err = f.db.Exec(`UPDATE v2_ai_telegram_mini_request SET updated_at=1 WHERE actor_id=$1`, f.staff); err != nil {
		t.Fatal(err)
	}
	if err = f.b.sweepTelegram(ctx); err != nil {
		t.Fatal(err)
	}
	replay, err = f.b.MiniSendMessage(ctx, auth.Token, req)
	if err != nil || replay.Turn.ID != result.Turn.ID || f.relayCalls.Load() != 1 {
		t.Fatal("retention broke safe replay", replay, err)
	}
	changed := req
	changed.Content += " changed"
	if _, err = f.b.MiniSendMessage(ctx, auth.Token, changed); !errors.Is(err, ErrConflict) {
		t.Fatal("changed replay accepted", err)
	}
	if _, err = f.db.Exec(`UPDATE v2_ai_telegram_mini_request SET payload_cipher='' WHERE actor_id=$1`, f.staff); err != nil {
		t.Fatal(err)
	}
	if err = f.b.pollTelegramTurns(ctx); err != nil {
		t.Fatal(err)
	}
	var after int
	if err = f.db.QueryRow(`SELECT COUNT(*) FROM v2_ai_telegram_outbox WHERE actor_id=$1`, f.staff).Scan(&after); err != nil || after != before {
		t.Fatal("Mini conversation leaked into bot chat", before, after, err)
	}
	if err = f.s.DeleteSession(ctx, f.admin, result.Session.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.b.MiniSendMessage(ctx, auth.Token, req); !errors.Is(err, ErrForbidden) {
		t.Fatal("deleted request resurrected", err)
	}
	if f.relayCalls.Load() != 1 {
		t.Fatal("deleted task was replayed")
	}
}

func TestTelegramMiniPostgresLostAckRevocationAndPreparedTombstone(t *testing.T) {
	f := setupMiniFixture(t)
	ctx := context.Background()
	f.bind(t, f.staff)
	auth := f.auth(t, f.staff)
	req := SendMessageRequest{Model: DefaultModels[0], Content: "check", RequestID: "mini-lost-ack-task"}
	f.freeze(t, auth.Token, req)
	result, err := f.s.SendMessage(context.WithValue(ctx, telegramMiniSourceKey{}, true), f.staff, req)
	if err != nil || result.Session.Source != "telegram" {
		t.Fatal(result, err)
	}
	f.waitTurn(t, result.Session.ID)
	// Model a committed long-running turn whose bridge ack was lost.
	if _, err = f.db.Exec(`UPDATE v2_ai_turn SET status='running',stop_requested=FALSE WHERE id=$1`, result.Turn.ID); err != nil {
		t.Fatal(err)
	}
	if err = f.b.RevokeBinding(ctx, f.admin, f.staff); err != nil {
		t.Fatal(err)
	}
	var stop bool
	if err = f.db.QueryRow(`SELECT stop_requested FROM v2_ai_turn WHERE id=$1`, result.Turn.ID).Scan(&stop); err != nil || !stop {
		t.Fatal("unacked turn survived revocation", stop, err)
	}
	f.bind(t, f.staff)
	auth = f.auth(t, f.staff)
	if _, err = f.b.MiniSendMessage(ctx, auth.Token, req); !errors.Is(err, ErrForbidden) {
		t.Fatal("rebind resurrected old task", err)
	}
	fresh := SendMessageRequest{Model: DefaultModels[0], Content: "new check", RequestID: "mini-prepared-only"}
	f.freeze(t, auth.Token, fresh)
	if err = f.b.RevokeBinding(ctx, f.admin, f.staff); err != nil {
		t.Fatal(err)
	}
	f.bind(t, f.staff)
	auth = f.auth(t, f.staff)
	if _, err = f.b.MiniSendMessage(ctx, auth.Token, fresh); !errors.Is(err, ErrForbidden) {
		t.Fatal("pre-dispatch revoked task resurrected", err)
	}
	if f.relayCalls.Load() != 1 {
		t.Fatal("revoked queued task executed")
	}
}

func TestTelegramMiniPostgresActionOwnershipReplayAndRevocation(t *testing.T) {
	f := setupMiniFixture(t)
	ctx := context.Background()
	f.bind(t, f.staff)
	f.bind(t, f.other)
	auth, other := f.auth(t, f.staff), f.auth(t, f.other)
	session, err := f.s.CreateSession(ctx, f.staff, CreateSessionRequest{Model: DefaultModels[0]})
	if err != nil {
		t.Fatal(err)
	}
	propose := func(id string, balance int) int64 {
		t.Helper()
		args := fmt.Sprintf(`{"user_id":%d,"changes":{"balance":%d},"reason":"fixture"}`, f.user, balance)
		out, e := f.s.callTool(ctx, f.staff, session.ID, ToolCall{ID: id, Function: ToolFunction{Name: "propose_user_update", Arguments: args}})
		if e != nil || !out.Pending {
			t.Fatal(out, e)
		}
		return out.ActionID
	}
	id := propose("mini-approve", 123)
	if _, err = f.b.MiniApproveAction(ctx, other.Token, session.ID, id); !errors.Is(err, ErrNotFound) {
		t.Fatal("cross-employee approval", err)
	}
	action, err := f.b.MiniApproveAction(ctx, auth.Token, session.ID, id)
	if err != nil || action.Status != "succeeded" || action.ApprovedBy != f.staff {
		t.Fatal(action, err)
	}
	if _, err = f.db.Exec(`UPDATE v2_ai_telegram_mini_request SET status='queued' WHERE action_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(`UPDATE v2_user SET balance=456 WHERE id=$1`, f.user); err != nil {
		t.Fatal(err)
	}
	if _, err = f.b.MiniApproveAction(ctx, auth.Token, session.ID, id); err != nil {
		t.Fatal("lost approval ack cannot recover", err)
	}
	var balance int
	if err = f.db.QueryRow(`SELECT balance FROM v2_user WHERE id=$1`, f.user).Scan(&balance); err != nil || balance != 456 {
		t.Fatal("approval re-executed", balance, err)
	}
	rejectID := propose("mini-reject", 999)
	if action, err = f.b.MiniRejectAction(ctx, auth.Token, session.ID, rejectID); err != nil || action.Status != "rejected" {
		t.Fatal(action, err)
	}
	if _, err = f.b.MiniRejectAction(ctx, auth.Token, session.ID, rejectID); err != nil {
		t.Fatal("reject retry", err)
	}
	// Action came from a normal web session: mini request association alone
	// must still allow unlink to stop it without any Telegram inbox/turn.
	if _, err = f.db.Exec(`UPDATE v2_ai_action SET status='running',stop_requested=FALSE WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if err = f.b.RevokeBinding(ctx, f.admin, f.staff); err != nil {
		t.Fatal(err)
	}
	var stop bool
	if err = f.db.QueryRow(`SELECT stop_requested FROM v2_ai_action WHERE id=$1`, id).Scan(&stop); err != nil || !stop {
		t.Fatal("web-origin action survived unlink", stop, err)
	}
}

func TestTelegramMiniPostgresMutationGuardAndDeleteNoDeadlock(t *testing.T) {
	f := setupMiniFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	f.bind(t, f.admin)
	auth := f.auth(t, f.admin)
	session, err := f.s.CreateSession(ctx, f.admin, CreateSessionRequest{Model: DefaultModels[0]})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.b.RunMiniOperation(ctx, auth.Token, func(guard context.Context) error { return f.s.DeleteSession(guard, f.admin, session.ID) }); err != nil {
		t.Fatal("nested cleanup deadlocked", err)
	}
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- f.b.RunMiniOperation(ctx, auth.Token, func(context.Context) error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	revoked := make(chan error, 1)
	go func() { revoked <- f.b.RevokeBinding(ctx, f.admin, f.admin) }()
	select {
	case err = <-revoked:
		t.Fatal("revocation interleaved authorized mutation", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if err = <-revoked; err != nil {
		t.Fatal(err)
	}
	called := false
	if err = f.b.RunMiniOperation(ctx, auth.Token, func(context.Context) error { called = true; return nil }); !errors.Is(err, ErrForbidden) || called {
		t.Fatal("revoked token reached mutation", called, err)
	}
}

func TestTelegramMiniPostgresAbandonedPreparedRequestExpiresSafely(t *testing.T) {
	f := setupMiniFixture(t)
	ctx := context.Background()
	f.bind(t, f.staff)
	auth := f.auth(t, f.staff)
	abandoned := SendMessageRequest{Model: DefaultModels[0], Content: "FROZEN_PRIVATE_INPUT", RequestID: "mini-abandoned-prepared"}
	recent := SendMessageRequest{Model: DefaultModels[0], Content: "recent check", RequestID: "mini-recent-prepared"}
	f.freeze(t, auth.Token, abandoned)
	f.freeze(t, auth.Token, recent)
	if _, err := f.db.Exec(`UPDATE v2_ai_telegram_mini_request SET updated_at=1 WHERE actor_id=$1 AND request_id=$2`, f.staff, abandoned.RequestID); err != nil {
		t.Fatal(err)
	}
	if err := f.b.sweepTelegram(ctx); err != nil {
		t.Fatal(err)
	}
	var status, cipher string
	if err := f.db.QueryRow(`SELECT status,payload_cipher FROM v2_ai_telegram_mini_request WHERE actor_id=$1 AND request_id=$2`, f.staff, abandoned.RequestID).Scan(&status, &cipher); err != nil || status != "cancelled" || cipher != "" {
		t.Fatal("expired private input retained", status, cipher != "", err)
	}
	if _, err := f.b.MiniSendMessage(ctx, auth.Token, abandoned); !errors.Is(err, ErrForbidden) {
		t.Fatal("expired prepared request resurrected", err)
	}
	if f.relayCalls.Load() != 0 {
		t.Fatal("cleanup executed abandoned request")
	}
	result, err := f.b.MiniSendMessage(ctx, auth.Token, recent)
	if err != nil {
		t.Fatal("recent request was removed", err)
	}
	f.waitTurn(t, result.Session.ID)
	if f.relayCalls.Load() != 1 {
		t.Fatal("recent request not executed once")
	}
}
