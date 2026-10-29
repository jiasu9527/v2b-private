package aiops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAICapabilityPostgresStoredActionsAndModelBoundary(t *testing.T) {
	f := setupStaffAIFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var requests atomic.Int64
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var request completionRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if len(request.Messages) == 0 || request.Messages[0].Role != "system" || !strings.Contains(request.Messages[0].Content, paymentConfigurationBoundaryPrompt) {
			t.Error("model did not receive payment configuration boundary")
		}
		for _, tool := range request.Tools {
			if !toolCapabilityAllowed(tool.Function.Name) {
				t.Error("unpermitted tool was advertised", tool.Function.Name)
			}
		}
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"}}]}`))
	}))
	defer relay.Close()
	if _, err := f.s.SaveConfig(ctx, f.admin, ConfigSaveRequest{Enabled: true, BaseURL: relay.URL, APIKey: "fixture", Models: DefaultModels}); err != nil {
		t.Fatal(err)
	}
	created, err := f.s.CreateSession(ctx, f.staff, CreateSessionRequest{Model: DefaultModels[0]})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	var turnID int64
	if err = f.db.QueryRowContext(ctx, `INSERT INTO v2_ai_turn(session_id,actor_id,request_id,request_hash,model,status,created_at,expires_at,access_mode) VALUES($1,$2,$3,'fixture',$4,'running',$5,$6,'full') RETURNING id`, created.ID, f.staff, fmt.Sprintf("capability-turn-%d", now), DefaultModels[0], now, now+120).Scan(&turnID); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"payment_configuration", "payment_config_save", "future_action"} {
		t.Run(kind, func(t *testing.T) {
			var id int64
			callID := fmt.Sprintf("turn:%d:%s", turnID, kind)
			if err = f.db.QueryRowContext(ctx, `INSERT INTO v2_ai_action(session_id,actor_id,tool_call_id,kind,payload,snapshot_hash,request_hash,reason,status,expires_at,created_at,updated_at) VALUES($1,$2,$3,$4,'{}','fixture','fixture','fixture','pending',$5,$6,$6) RETURNING id`, created.ID, f.staff, callID, kind, now+120, now).Scan(&id); err != nil {
				t.Fatal(err)
			}
			if _, err = f.s.ApproveAction(ctx, f.staff, created.ID, id); !errors.Is(err, ErrForbidden) {
				t.Fatal("manual approval accepted forbidden kind", err)
			}
			if _, err = f.s.automaticActionResult(ctx, f.staff, created.ID, turnID, ToolOutcome{ActionID: id, Pending: true}); !errors.Is(err, ErrForbidden) {
				t.Fatal("full access accepted forbidden kind", err)
			}
			action, err := scanAction(f.db.QueryRowContext(ctx, `SELECT `+actionColumns+` FROM v2_ai_action WHERE id=$1`, id))
			if err != nil || action.Status != "pending" || action.ClaimedAt != 0 || action.ApprovedBy != 0 || action.Output != "" {
				t.Fatal("denied action mutated or claimed", action, err)
			}
			f.s.mu.Lock()
			workers := len(f.s.workers)
			f.s.mu.Unlock()
			if workers != 0 || requests.Load() != 0 {
				t.Fatal("denied action dispatched execution", workers, requests.Load())
			}

			// A persisted task reaching an executor after a deployment is also
			// denied; only its terminal failure record is written.
			if _, err = f.db.ExecContext(ctx, `UPDATE v2_ai_action SET status='running' WHERE id=$1`, id); err != nil {
				t.Fatal(err)
			}
			workerCtx, stopWorker := context.WithCancel(ctx)
			f.s.mu.Lock()
			f.s.workers[-id] = stopWorker
			f.s.wg.Add(1)
			f.s.mu.Unlock()
			f.s.executeAction(workerCtx, stopWorker, action, "fixture")
			action, err = scanAction(f.db.QueryRowContext(ctx, `SELECT `+actionColumns+` FROM v2_ai_action WHERE id=$1`, id))
			if err != nil || action.Status != "failed" || action.Output != "" || !strings.Contains(action.Error, "无此业务操作权限") || requests.Load() != 0 {
				t.Fatal("executor did not fail closed", action, err)
			}
		})
	}
	if _, err = f.db.ExecContext(ctx, `UPDATE v2_ai_turn SET status='completed' WHERE id=$1`, turnID); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"standard", "full"} {
		t.Run("model boundary "+mode, func(t *testing.T) {
			sent, err := f.s.SendMessage(ctx, f.staff, SendMessageRequest{Model: DefaultModels[0], Content: "查询节点状态", RequestID: fmt.Sprintf("capability-model-%s-%d", mode, time.Now().UnixNano()), AccessMode: mode})
			if err != nil {
				t.Fatal(err)
			}
			for {
				detail, err := f.s.GetSession(ctx, f.staff, sent.Session.ID)
				if err != nil {
					t.Fatal(err)
				}
				if detail.Session.Status != "running" {
					if len(detail.Turns) != 1 || detail.Turns[0].Status != "completed" || detail.Turns[0].AccessMode != mode {
						t.Fatal("allowed conversation failed", detail)
					}
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-time.After(10 * time.Millisecond):
				}
			}
		})
	}
	if requests.Load() != 2 {
		t.Fatal("unexpected relay requests", requests.Load())
	}
}
