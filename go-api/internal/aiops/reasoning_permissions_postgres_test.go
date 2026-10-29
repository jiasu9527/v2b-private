package aiops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestReasoningAdminOnlyPostgres(t *testing.T) {
	f := setupStaffAIFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	seen := make(chan completionRequest, 64)
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request completionRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		seen <- request
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"}}]}`))
	}))
	defer relay.Close()
	models := []string{"model-a", "model-b"}
	defaults := map[string]string{"model-a": "high", "model-b": "medium"}
	if _, err := f.s.SaveConfig(ctx, f.admin, ConfigSaveRequest{Enabled: true, BaseURL: relay.URL, APIKey: "fixture", Models: models, ReasoningEfforts: &defaults}); err != nil {
		t.Fatal(err)
	}
	request := func(sessionID int64, model, effort string) SendMessageRequest {
		return SendMessageRequest{SessionID: sessionID, Model: model, Content: "check fixture", RequestID: fmt.Sprintf("reasoning-role-%d", time.Now().UnixNano()), ReasoningEffort: effort}
	}
	wait := func(result SendMessageResult, effort string) {
		t.Helper()
		for {
			detail, err := f.s.GetSession(ctx, f.admin, result.Session.ID)
			if err != nil {
				t.Fatal(err)
			}
			if detail.Session.Status != "running" {
				if len(detail.Turns) == 0 || detail.Turns[0].ID != result.Turn.ID || detail.Turns[0].Status != "completed" {
					t.Fatal("turn failed", detail.Turns)
				}
				break
			}
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(10 * time.Millisecond):
			}
		}
		select {
		case upstream := <-seen:
			want := effort
			if want == "auto" {
				want = ""
			}
			if upstream.ReasoningEffort != want || result.Turn.ReasoningEffort != effort {
				t.Fatal("wrong effective reasoning", upstream.ReasoningEffort, result.Turn.ReasoningEffort, effort)
			}
		case <-ctx.Done():
			t.Fatal("no relay request")
		}
	}
	send := func(actor int64, req SendMessageRequest, effort string) SendMessageResult {
		t.Helper()
		result, err := f.s.SendMessage(ctx, actor, req)
		if err != nil {
			t.Fatal(err)
		}
		wait(result, effort)
		return result
	}

	t.Run("staff omitted and default follow selected model", func(t *testing.T) {
		for _, selection := range []string{"", "default"} {
			for _, model := range models {
				created, err := f.s.CreateSession(ctx, f.staff, CreateSessionRequest{Model: model, ReasoningEffort: selection})
				if err != nil || created.ReasoningEffort != "default" || created.AccessMode != "full" {
					t.Fatal("staff session defaults", created, err)
				}
				result := send(f.staff, request(created.ID, model, selection), defaults[model])
				if result.Session.ReasoningEffort != "default" {
					t.Fatal("staff selection not reset", result.Session)
				}
			}
		}
		fresh := send(f.staff, request(0, "model-a", ""), "high")
		if fresh.Session.AccessMode != "full" || fresh.Turn.AccessMode != "full" {
			t.Fatal("staff full access unexpectedly changed", fresh)
		}
	})

	t.Run("staff cannot override any explicit level", func(t *testing.T) {
		var sessionsBefore, turnsBefore, sessionsAfter, turnsAfter int
		if err := f.db.QueryRow(`SELECT (SELECT COUNT(*) FROM v2_ai_session),(SELECT COUNT(*) FROM v2_ai_turn)`).Scan(&sessionsBefore, &turnsBefore); err != nil {
			t.Fatal(err)
		}
		for _, effort := range []string{"auto", "none", "minimal", "low", "medium", "high", "xhigh"} {
			if _, err := f.s.CreateSession(ctx, f.staff, CreateSessionRequest{Model: "model-a", ReasoningEffort: effort}); !errors.Is(err, ErrForbidden) {
				t.Fatal("staff create override", effort, err)
			}
			if _, err := f.s.SendMessage(ctx, f.staff, request(0, "model-a", effort)); !errors.Is(err, ErrForbidden) {
				t.Fatal("staff send override", effort, err)
			}
		}
		if err := f.db.QueryRow(`SELECT (SELECT COUNT(*) FROM v2_ai_session),(SELECT COUNT(*) FROM v2_ai_turn)`).Scan(&sessionsAfter, &turnsAfter); err != nil {
			t.Fatal(err)
		}
		if sessionsBefore != sessionsAfter || turnsBefore != turnsAfter || len(seen) != 0 {
			t.Fatal("rejected override created state or contacted relay")
		}
	})

	t.Run("legacy explicit staff session resets each new turn", func(t *testing.T) {
		created, err := f.s.CreateSession(ctx, f.staff, CreateSessionRequest{Model: "model-a"})
		if err != nil {
			t.Fatal(err)
		}
		for _, selection := range []string{"", "default"} {
			if _, err = f.db.Exec(`UPDATE v2_ai_session SET reasoning_effort='xhigh' WHERE id=$1`, created.ID); err != nil {
				t.Fatal(err)
			}
			result := send(f.staff, request(created.ID, "model-b", selection), "medium")
			detail, err := f.s.GetSession(ctx, f.staff, created.ID)
			if err != nil || result.Session.ReasoningEffort != "default" || detail.Session.ReasoningEffort != "default" {
				t.Fatal("legacy explicit override persisted", result.Session, detail.Session, err)
			}
		}
		defaults["model-b"] = "low"
		if _, err = f.s.SaveConfig(ctx, f.admin, ConfigSaveRequest{Enabled: true, BaseURL: relay.URL, Models: models, ReasoningEfforts: &defaults}); err != nil {
			t.Fatal(err)
		}
		send(f.staff, request(created.ID, "model-b", ""), "low")
	})

	t.Run("admins retain explicit choices and demotion blocks replay", func(t *testing.T) {
		for _, effort := range []string{"auto", "none", "minimal", "low", "medium", "high", "xhigh"} {
			created, err := f.s.CreateSession(ctx, f.admin, CreateSessionRequest{Model: "model-a", ReasoningEffort: effort})
			if err != nil || created.ReasoningEffort != effort {
				t.Fatal("admin selection rejected", effort, created, err)
			}
			send(f.admin, request(created.ID, "model-b", ""), effort)
		}
		if _, err := f.db.Exec(`UPDATE v2_user SET is_admin=1 WHERE id=$1`, f.other); err != nil {
			t.Fatal(err)
		}
		req := request(0, "model-a", "xhigh")
		first := send(f.other, req, "xhigh")
		if _, err := f.db.Exec(`UPDATE v2_user SET is_admin=0 WHERE id=$1`, f.other); err != nil {
			t.Fatal(err)
		}
		if _, err := f.s.SendMessage(ctx, f.other, req); !errors.Is(err, ErrForbidden) {
			t.Fatal("demoted admin replay bypassed permission", err)
		}
		result := send(f.other, request(first.Session.ID, "model-a", ""), "high")
		if result.Session.ReasoningEffort != "default" {
			t.Fatal("demoted admin inherited explicit override", result)
		}
	})

	t.Run("transaction rechecks role after preliminary authorization", func(t *testing.T) {
		for _, action := range []string{"session/create", "chat/send"} {
			t.Run(action, func(t *testing.T) {
				if _, err := f.db.ExecContext(ctx, `UPDATE v2_user SET is_admin=1 WHERE id=$1`, f.other); err != nil {
					t.Fatal(err)
				}
				blocker, err := f.db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer blocker.Rollback()
				if _, err = blocker.ExecContext(ctx, `SELECT pg_advisory_xact_lock(2026102902)`); err != nil {
					t.Fatal(err)
				}
				result := make(chan error, 1)
				go func() {
					var err error
					if action == "session/create" {
						_, err = f.s.CreateSession(ctx, f.other, CreateSessionRequest{Model: "model-a", ReasoningEffort: "xhigh"})
					} else {
						_, err = f.s.SendMessage(ctx, f.other, request(0, "model-a", "xhigh"))
					}
					result <- err
				}()
				for {
					var blocked bool
					err = f.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query='SELECT pg_advisory_xact_lock_shared(2026102902)')`).Scan(&blocked)
					if err != nil {
						t.Fatal(err)
					}
					if blocked {
						break
					}
					select {
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					case err := <-result:
						t.Fatal("request did not reach transaction", err)
					case <-time.After(10 * time.Millisecond):
					}
				}
				if _, err = f.db.ExecContext(ctx, `UPDATE v2_user SET is_admin=0 WHERE id=$1`, f.other); err != nil {
					t.Fatal(err)
				}
				if err = blocker.Rollback(); err != nil {
					t.Fatal(err)
				}
				select {
				case err := <-result:
					if !errors.Is(err, ErrForbidden) {
						t.Fatal("revoked admin was accepted inside transaction", err)
					}
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			})
		}
	})
}
