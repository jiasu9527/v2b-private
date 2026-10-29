package aiops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

func TestAIPanelOperationsPostgresLifecycle(t *testing.T) {
	f := setupStaffAIFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runtime := &fixturePanelOperations{ops: []PanelOperation{fixtureOperation("notices.list", "/notice/fetch", false, false), fixtureOperation("notices.save", "/notice/save", true, false), fixtureOperation("config.save", "/config/save", true, true), fixtureOperation("payments.list", "/payment/fetch", false, false)}, data: json.RawMessage(`{"saved":true}`)}
	f.s.SetPanelOperations(runtime)
	sessions := map[int64]Session{}
	for _, actor := range []int64{f.admin, f.staff, f.other} {
		v, err := f.s.CreateSession(ctx, actor, CreateSessionRequest{Model: DefaultModels[0]})
		if err != nil {
			t.Fatal(err)
		}
		sessions[actor] = v
	}
	call := func(actor int64, name string, args any) (ToolOutcome, error) {
		raw, _ := json.Marshal(args)
		return f.s.callTool(ctx, actor, sessions[actor].ID, ToolCall{ID: fmt.Sprintf("fixture-%d", time.Now().UnixNano()), Function: ToolFunction{Name: name, Arguments: string(raw)}})
	}
	propose := func(actor int64, operation string, params any) Action {
		t.Helper()
		out, err := call(actor, "propose_panel_operation", map[string]any{"operation": operation, "parameters": params, "reason": "fixture change"})
		if err != nil || !out.Pending {
			t.Fatal(out, err)
		}
		a, err := scanAction(f.db.QueryRowContext(ctx, `SELECT `+actionColumns+` FROM v2_ai_action WHERE id=$1`, out.ActionID))
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	wait := func(id int64) Action {
		t.Helper()
		for {
			a, err := scanAction(f.db.QueryRowContext(ctx, `SELECT `+actionColumns+` FROM v2_ai_action WHERE id=$1`, id))
			if err != nil {
				t.Fatal(err)
			}
			if a.Status != "running" {
				return a
			}
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	t.Run("catalog schema read write and payment boundaries", func(t *testing.T) {
		out, err := call(f.staff, "panel_operations_list", map[string]any{"limit": 1})
		var page pageDTO[map[string]any]
		json.Unmarshal([]byte(out.Content), &page)
		if err != nil || page.Total != 2 || page.NextOffset == nil {
			t.Fatal(out, err)
		}
		out, err = call(f.staff, "panel_operation_describe", map[string]any{"operation": "notices.save"})
		if err != nil || !strings.Contains(out.Content, "parameters") {
			t.Fatal(out, err)
		}
		for _, tc := range []struct {
			tool, op string
			actor    int64
		}{{"panel_query", "notices.save", f.staff}, {"propose_panel_operation", "notices.list", f.staff}, {"panel_query", "payments.list", f.admin}, {"panel_query", "missing.operation", f.admin}, {"propose_panel_operation", "config.save", f.staff}} {
			_, err := call(tc.actor, tc.tool, map[string]any{"operation": tc.op, "parameters": map[string]any{}, "reason": "fixture"})
			if err == nil {
				t.Fatal("forbidden operation accepted", tc)
			}
		}
		if runtime.count() != 0 {
			t.Fatal("rejected request executed")
		}
		out, err = call(f.staff, "panel_query", map[string]any{"operation": "notices.list", "parameters": map[string]any{}})
		if err != nil || !strings.Contains(out.Content, `"saved":true`) {
			t.Fatal(out, err)
		}
	})
	t.Run("manual mutation encrypted and single execution", func(t *testing.T) {
		before := runtime.count()
		a := propose(f.staff, "notices.save", map[string]any{"value": "notice", "api_key": "PRIVATE_SENTINEL"})
		if runtime.count() != before || strings.Contains(string(a.Payload), "PRIVATE_SENTINEL") || !strings.Contains(string(a.Payload), "parameters_cipher") {
			t.Fatal(a, runtime.count())
		}
		public, _ := json.Marshal(a)
		if strings.Contains(string(public), "parameters_cipher") {
			t.Fatal("cipher exposed")
		}
		if _, err := f.s.ApproveAction(ctx, f.admin, a.SessionID, a.ID); err != nil {
			t.Fatal(err)
		}
		done := wait(a.ID)
		if done.Status != "succeeded" || runtime.count() != before+1 {
			t.Fatal(done, runtime.count())
		}
		runtime.mu.Lock()
		plain := string(runtime.lastParameters)
		executionActor := runtime.lastActor
		runtime.mu.Unlock()
		if !strings.Contains(plain, "PRIVATE_SENTINEL") {
			t.Fatal("encrypted value not restored for execution")
		}
		if executionActor.ID != f.admin || executionActor.IsAdmin {
			t.Fatal("admin approval widened original employee scope or lost audit identity", executionActor)
		}
		var chunk panelResultChunk
		if json.Unmarshal([]byte(done.Output), &chunk) != nil || chunk.ResultID <= 0 {
			t.Fatal(done.Output)
		}
		if _, err := f.s.readPanelResult(ctx, f.staff, a.SessionID, chunk.ResultID, 0, 1000); err != nil {
			t.Fatal("owner cannot read admin-approved ordinary result", err)
		}
		if _, err := f.s.ApproveAction(ctx, f.admin, a.SessionID, a.ID); !errors.Is(err, ErrConflict) {
			t.Fatal(err)
		}
		if runtime.count() != before+1 {
			t.Fatal("replay executed")
		}
	})
	t.Run("demoted origin cannot be elevated by admin approval", func(t *testing.T) {
		f.db.ExecContext(ctx, `UPDATE v2_user SET is_admin=1 WHERE id=$1`, f.other)
		a := propose(f.other, "config.save", map[string]any{"value": "fixture"})
		f.db.ExecContext(ctx, `UPDATE v2_user SET is_admin=0 WHERE id=$1`, f.other)
		before := runtime.count()
		if _, err := f.s.ApproveAction(ctx, f.admin, a.SessionID, a.ID); !errors.Is(err, ErrForbidden) {
			t.Fatal(err)
		}
		if runtime.count() != before {
			t.Fatal("admin escalated staff origin")
		}
	})
	t.Run("admin operation cannot disclose into employee conversation", func(t *testing.T) {
		runtime.mu.Lock()
		runtime.ops = append(runtime.ops, fixtureOperation("config.read", "/config/fetch", false, true))
		runtime.mu.Unlock()
		before := runtime.count()
		for _, name := range []string{"panel_operation_describe", "panel_query", "propose_panel_operation"} {
			args := map[string]any{"operation": "config.read"}
			if name == "panel_query" {
				args["parameters"] = map[string]any{}
			}
			if name == "propose_panel_operation" {
				args["operation"] = "config.save"
				args["parameters"] = map[string]any{}
				args["reason"] = "fixture"
			}
			raw, _ := json.Marshal(args)
			_, err := f.s.callTool(ctx, f.admin, sessions[f.staff].ID, ToolCall{ID: "foreign-admin-settings", Function: ToolFunction{Name: name, Arguments: string(raw)}})
			if !errors.Is(err, ErrForbidden) {
				t.Fatal("admin operation entered staff conversation", name, err)
			}
		}
		if runtime.count() != before {
			t.Fatal("staff conversation disclosed admin output")
		}
	})
	t.Run("descriptor drift rejects existing approval", func(t *testing.T) {
		a := propose(f.staff, "notices.save", map[string]any{"value": "drift"})
		before := runtime.count()
		runtime.mu.Lock()
		runtime.ops[1].Description = "changed descriptor"
		runtime.mu.Unlock()
		if _, err := f.s.ApproveAction(ctx, f.staff, a.SessionID, a.ID); !errors.Is(err, ErrConflict) {
			t.Fatal(err)
		}
		if runtime.count() != before {
			t.Fatal("changed catalog dispatched")
		}
	})
	t.Run("full access uses same lifecycle and stops before dispatch", func(t *testing.T) {
		now := time.Now().Unix()
		var turn int64
		if err := f.db.QueryRowContext(ctx, `INSERT INTO v2_ai_turn(session_id,actor_id,request_id,request_hash,model,status,created_at,expires_at,access_mode) VALUES($1,$2,$3,'fixture',$4,'running',$5,$6,'full') RETURNING id`, sessions[f.staff].ID, f.staff, fmt.Sprintf("panel-full-%d", now), DefaultModels[0], now, now+120).Scan(&turn); err != nil {
			t.Fatal(err)
		}
		parameters := json.RawMessage(`{"value":"full"}`)
		payload, _ := json.Marshal(panelOperationPayload{Operation: "notices.save", Parameters: parameters})
		a, err := f.s.proposeAction(ctx, f.staff, sessions[f.staff].ID, fmt.Sprintf("turn:%d:round:1:fixture", turn), actionSpec{Kind: "panel_operation", TargetType: "notices.save", Payload: payload, Reason: "full operation"})
		if err != nil {
			t.Fatal(err)
		}
		before := runtime.count()
		out, err := f.s.automaticActionResult(ctx, f.staff, a.SessionID, turn, ToolOutcome{ActionID: a.ID, Pending: true})
		if err != nil || !strings.Contains(out.Content, "result_id") || runtime.count() != before+1 {
			t.Fatal(out, err, runtime.count())
		}
		done := wait(a.ID)
		if done.ApprovalMode != "full_access" || done.Status != "succeeded" {
			t.Fatal(done)
		}
		// Simulate a pending action already claimed when the stop flag arrives.
		stopped := propose(f.staff, "notices.save", map[string]any{"value": "stopped"})
		var snapshot string
		if err = f.db.QueryRowContext(ctx, `UPDATE v2_ai_action SET status='running',approved_by=$2,stop_requested=TRUE WHERE id=$1 RETURNING snapshot_hash`, stopped.ID, f.staff).Scan(&snapshot); err != nil {
			t.Fatal(err)
		}
		stopped.ApprovedBy = f.staff
		before = runtime.count()
		status, _, _ := f.s.executePanelOperation(ctx, stopped, snapshot)
		if status != "failed" || runtime.count() != before {
			t.Fatal("stopped action dispatched", status, runtime.count())
		}
		f.db.ExecContext(ctx, `UPDATE v2_ai_action SET status='failed' WHERE id=$1`, stopped.ID)
		f.db.ExecContext(ctx, `UPDATE v2_ai_turn SET status='completed' WHERE id=$1`, turn)
	})
	t.Run("execution rechecks origin role after validation", func(t *testing.T) {
		f.db.ExecContext(ctx, `UPDATE v2_user SET is_admin=1 WHERE id=$1`, f.other)
		a := propose(f.other, "config.save", map[string]any{"value": "role-race"})
		var snapshot string
		if err := f.db.QueryRowContext(ctx, `UPDATE v2_ai_action SET status='running',approved_by=$2 WHERE id=$1 RETURNING snapshot_hash`, a.ID, f.admin).Scan(&snapshot); err != nil {
			t.Fatal(err)
		}
		a.ApprovedBy = f.admin
		var revoked atomic.Bool
		runtime.mu.Lock()
		runtime.onValidate = func(actor PanelOperationActor, _ string) {
			if actor.ID == f.other && revoked.CompareAndSwap(false, true) {
				if _, err := f.db.ExecContext(ctx, `UPDATE v2_user SET is_admin=0 WHERE id=$1`, f.other); err != nil {
					t.Error(err)
				}
			}
		}
		runtime.mu.Unlock()
		before := runtime.count()
		status, _, _ := f.s.executePanelOperation(ctx, a, snapshot)
		runtime.mu.Lock()
		runtime.onValidate = nil
		runtime.mu.Unlock()
		if status != "failed" || runtime.count() != before || !revoked.Load() {
			t.Fatal("revoked original privilege dispatched", status, runtime.count())
		}
		f.db.ExecContext(ctx, `UPDATE v2_ai_action SET status='failed' WHERE id=$1`, a.ID)
	})
	t.Run("adapter error after request remains unknown and classified", func(t *testing.T) {
		a := propose(f.staff, "notices.save", map[string]any{"value": "backend-conflict"})
		runtime.mu.Lock()
		runtime.onExecute = func(context.Context) error { return &PanelOperationError{Status: 409, Message: "RAW_SECRET"} }
		runtime.mu.Unlock()
		if _, err := f.s.ApproveAction(ctx, f.staff, a.SessionID, a.ID); err != nil {
			t.Fatal(err)
		}
		done := wait(a.ID)
		runtime.mu.Lock()
		runtime.onExecute = nil
		runtime.mu.Unlock()
		if done.Status != "unknown" || strings.Contains(done.Error, "RAW_SECRET") {
			t.Fatal(done)
		}
	})
	t.Run("admin only result cannot be read after demotion", func(t *testing.T) {
		f.db.ExecContext(ctx, `UPDATE v2_user SET is_admin=1 WHERE id=$1`, f.other)
		raw, err := f.s.savePanelResult(ctx, f.other, sessions[f.other].ID, "config.save", json.RawMessage(`{"state":"admin-only"}`), true)
		if err != nil {
			t.Fatal(err)
		}
		var chunk panelResultChunk
		json.Unmarshal([]byte(raw), &chunk)
		f.db.ExecContext(ctx, `UPDATE v2_user SET is_admin=0 WHERE id=$1`, f.other)
		if _, err = f.s.readPanelResult(ctx, f.other, sessions[f.other].ID, chunk.ResultID, 0, 1000); !errors.Is(err, ErrNotFound) {
			t.Fatal("demoted admin retained result", err)
		}
	})
	t.Run("large result complete unicode and isolation expiry cascade", func(t *testing.T) {
		content := strings.Repeat("中🙂", 18000) + "尾部678.90"
		data, _ := json.Marshal(map[string]any{"text": content, "token_configured": true, "password_limit_enable": 1})
		runtime.mu.Lock()
		runtime.data = data
		runtime.mu.Unlock()
		out, err := call(f.staff, "panel_query", map[string]any{"operation": "notices.list", "parameters": map[string]any{}})
		if err != nil {
			t.Fatal(err)
		}
		var chunk panelResultChunk
		if json.Unmarshal([]byte(out.Content), &chunk) != nil || chunk.ResultID <= 0 || !chunk.Truncated {
			t.Fatal(out)
		}
		id := chunk.ResultID
		rebuilt := chunk.Content
		for chunk.Truncated {
			if chunk.NextContentOffset == nil {
				t.Fatal(chunk)
			}
			raw, err := f.s.readPanelResult(ctx, f.staff, sessions[f.staff].ID, id, *chunk.NextContentOffset, 2000)
			if err != nil {
				t.Fatal(err)
			}
			if len(raw) > maxToolOutputBytes || !utf8.ValidString(raw) {
				t.Fatal("result exceeds boundary")
			}
			json.Unmarshal([]byte(raw), &chunk)
			rebuilt += chunk.Content
		}
		if rebuilt != string(data) {
			t.Fatal("result lost data or flags")
		}
		if _, err = f.s.readPanelResult(ctx, f.other, sessions[f.other].ID, id, 0, 1000); !errors.Is(err, ErrNotFound) {
			t.Fatal("cross-session result exposed", err)
		}
		if _, err = f.s.readPanelResult(ctx, f.other, sessions[f.staff].ID, id, 0, 1000); !errors.Is(err, ErrNotFound) {
			t.Fatal("foreign session accessible", err)
		}
		f.db.ExecContext(ctx, `UPDATE v2_ai_result SET expires_at=0 WHERE id=$1`, id)
		if _, err = f.s.readPanelResult(ctx, f.staff, sessions[f.staff].ID, id, 0, 1000); !errors.Is(err, ErrNotFound) {
			t.Fatal("expired result accessible", err)
		}
		if err = f.s.DeleteSession(ctx, f.admin, sessions[f.staff].ID); err != nil {
			t.Fatal(err)
		}
		var count int
		f.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_ai_result WHERE session_id=$1`, sessions[f.staff].ID).Scan(&count)
		if count != 0 {
			t.Fatal("result survived session delete")
		}
	})
}
