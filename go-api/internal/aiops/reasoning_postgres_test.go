package aiops

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/config"
)

func TestReasoningEffortPostgres(t *testing.T) {
	dsn := os.Getenv("FOREST_AIOPS_TEST_DSN")
	if dsn == "" {
		t.Skip("dedicated disposable PostgreSQL required")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	s := NewService(config.Config{AppKey: "reasoning-fixture"}, db, admin.NewDBService(config.Config{}, db))
	defer s.Close()
	if err = s.ensureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	var actor int64
	marker := fmt.Sprint(time.Now().UnixNano())
	if err = db.QueryRow(`INSERT INTO v2_user(email,password,uuid,token,is_admin,created_at,updated_at)VALUES($1,'fixture','fixture',$2,1,1,1)RETURNING id`, "reasoning-"+marker+"@example.test", marker).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM v2_user WHERE id=$1`, actor)
	defer db.Exec(`DELETE FROM v2_ai_config WHERE updated_by=$1`, actor)
	defer db.Exec(`DELETE FROM v2_ai_session WHERE created_by=$1`, actor)
	var scenario atomic.Int64
	var calls atomic.Int64
	var mu sync.Mutex
	type observed struct {
		model, effort string
		present       bool
	}
	seen := []observed{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var raw map[string]json.RawMessage
		if json.NewDecoder(r.Body).Decode(&raw) != nil {
			t.Error("bad request")
			w.WriteHeader(400)
			return
		}
		var model, effort string
		json.Unmarshal(raw["model"], &model)
		json.Unmarshal(raw["reasoning_effort"], &effort)
		_, present := raw["reasoning_effort"]
		mu.Lock()
		seen = append(seen, observed{model, effort, present})
		mu.Unlock()
		n := calls.Add(1)
		if scenario.Load() == 1 && n == 1 {
			// Changing the model default during tools must affect only later turns.
			if _, err := db.Exec(`UPDATE v2_ai_config SET reasoning_efforts=$1::JSONB WHERE id=1`, `{"model-a":"low","model-b":"medium"}`); err != nil {
				t.Error(err)
			}
			w.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"read-servers","type":"function","function":{"name":"servers_list","arguments":"{}"}}]}}]}`))
			return
		}
		if scenario.Load() == 2 {
			w.WriteHeader(400)
			w.Write([]byte(`{"error":{"code":"unsupported_parameter","param":"reasoning_effort","message":"fixture-secret"}}`))
			return
		}
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"}}]}`))
	}))
	defer server.Close()
	models := []string{"model-a", "model-b"}
	save := func(efforts *map[string]string) Config {
		t.Helper()
		v, e := s.SaveConfig(ctx, actor, ConfigSaveRequest{Enabled: true, BaseURL: server.URL, Models: models, APIKey: "fixture-secret", ReasoningEfforts: efforts})
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	wait := func(result SendMessageResult) SessionDetail {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			d, e := s.GetSession(ctx, actor, result.Session.ID)
			if e != nil {
				t.Fatal(e)
			}
			if d.Session.Status != "running" {
				return d
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("turn timeout")
		return SessionDetail{}
	}
	send := func(sessionID int64, model, effort string) SendMessageResult {
		t.Helper()
		r, e := s.SendMessage(ctx, actor, SendMessageRequest{SessionID: sessionID, Model: model, Content: "check fixture", RequestID: fmt.Sprintf("reasoning-%d", time.Now().UnixNano()), ReasoningEffort: effort})
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	resetSeen := func() { calls.Store(0); mu.Lock(); seen = nil; mu.Unlock() }
	last := func() observed {
		t.Helper()
		mu.Lock()
		defer mu.Unlock()
		if len(seen) == 0 {
			t.Fatal("no relay request")
		}
		return seen[len(seen)-1]
	}
	t.Run("legacy config defaults preservation and explicit reset", func(t *testing.T) {
		cfg := save(nil)
		if cfg.ReasoningEfforts["model-a"] != "auto" || cfg.ReasoningEfforts["model-b"] != "auto" {
			t.Fatal(cfg)
		}
		efforts := map[string]string{"model-a": "high"}
		cfg = save(&efforts)
		if cfg.ReasoningEfforts["model-a"] != "high" || cfg.ReasoningEfforts["model-b"] != "auto" {
			t.Fatal(cfg)
		}
		cfg = save(nil)
		if cfg.ReasoningEfforts["model-a"] != "high" {
			t.Fatal("old client erased configuration")
		}
		read, e := s.GetConfig(ctx, actor)
		if e != nil || read.ReasoningEfforts["model-a"] != "high" {
			t.Fatal(read, e)
		}
		invalid := map[string]string{"unknown": "high"}
		if _, e := s.SaveConfig(ctx, actor, ConfigSaveRequest{Models: models, ReasoningEfforts: &invalid}); !errors.Is(e, ErrValidation) {
			t.Fatal("unknown model accepted", e)
		}
		empty := map[string]string{}
		cfg = save(&empty)
		if cfg.ReasoningEfforts["model-a"] != "auto" {
			t.Fatal(cfg)
		}
	})
	t.Run("default fixed across tool rounds and model selection", func(t *testing.T) {
		efforts := map[string]string{"model-a": "high", "model-b": "medium"}
		save(&efforts)
		scenario.Store(1)
		resetSeen()
		first := send(0, "model-a", "")
		d := wait(first)
		if first.Session.ReasoningEffort != "default" || first.Turn.ReasoningEffort != "high" || d.Turns[0].Status != "completed" {
			t.Fatal(first, d.Turns)
		}
		mu.Lock()
		got := append([]observed{}, seen...)
		mu.Unlock()
		if len(got) != 2 || got[0].effort != "high" || got[1].effort != "high" {
			t.Fatal("default changed in middle of turn", got)
		}
		scenario.Store(0)
		second := send(first.Session.ID, "model-b", "")
		wait(second)
		if second.Turn.ReasoningEffort != "medium" || last().effort != "medium" {
			t.Fatal("wrong selected model default", second, last())
		}
		third := send(first.Session.ID, "model-a", "none")
		wait(third)
		if third.Session.ReasoningEffort != "none" || last().effort != "none" {
			t.Fatal(third, last())
		}
		fourth := send(first.Session.ID, "model-b", "")
		wait(fourth)
		if fourth.Turn.ReasoningEffort != "none" || last().effort != "none" {
			t.Fatal("stored explicit selection lost", fourth, last())
		}
		fifth := send(first.Session.ID, "model-a", "auto")
		wait(fifth)
		if fifth.Turn.ReasoningEffort != "auto" || last().present {
			t.Fatal("auto was sent upstream", fifth, last())
		}
	})
	t.Run("explicit selection survives refresh and idempotency conflicts", func(t *testing.T) {
		created, e := s.CreateSession(ctx, actor, CreateSessionRequest{Model: "model-a", ReasoningEffort: "xhigh"})
		if e != nil || created.ReasoningEffort != "xhigh" {
			t.Fatal(created, e)
		}
		request := SendMessageRequest{SessionID: created.ID, Model: "model-a", Content: "fixture once", RequestID: "reasoning-idempotency-" + marker}
		first, e := s.SendMessage(ctx, actor, request)
		if e != nil {
			t.Fatal(e)
		}
		wait(first)
		retry, e := s.SendMessage(ctx, actor, request)
		if e != nil || retry.Turn.ID != first.Turn.ID {
			t.Fatal(retry, e)
		}
		request.ReasoningEffort = "high"
		if _, e = s.SendMessage(ctx, actor, request); !errors.Is(e, ErrConflict) {
			t.Fatal("changed reasoning accepted with existing request id", e)
		}
		if first.Turn.ReasoningEffort != "xhigh" || last().effort != "xhigh" {
			t.Fatal(first, last())
		}
	})
	t.Run("unsupported upstream value fails once without fallback", func(t *testing.T) {
		scenario.Store(2)
		defer scenario.Store(0)
		resetSeen()
		result := send(0, "model-a", "xhigh")
		d := wait(result)
		if calls.Load() != 1 || d.Turns[0].Status != "failed" || !strings.Contains(d.Turns[0].Error, "中转默认（auto）") || strings.Contains(d.Turns[0].Error, "fixture-secret") {
			t.Fatal("unsafe fallback/rejection", calls.Load(), d.Turns)
		}
	})
}
