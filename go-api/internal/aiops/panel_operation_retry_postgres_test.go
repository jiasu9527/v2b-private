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

func TestAIPanelDNSOperationRetryPostgres(t *testing.T) {
	for _, tc := range []struct {
		name        string
		mode        string
		wantStatus  string
		wantCalls   int
		wantResults int
	}{
		{"temporary preflight recovers within same action", "recover", "succeeded", 2, 1},
		{"temporary preflight exhausts bounded attempts", "exhaust", "failed", 3, 0},
		{"write HTTP failure is never replayed", "write_unknown", "unknown", 1, 0},
		{"permanent preflight is not retried", "permanent", "failed", 1, 0},
		{"unknown preflight code cannot authorize retry", "unknown_code", "unknown", 1, 0},
		{"response text cannot authorize retry", "forged_text", "unknown", 1, 0},
		{"stopping session cancels backoff", "stop", "failed", 1, 0},
		{"role revocation blocks next attempt", "revoke", "failed", 1, 0},
		{"disabled AI blocks next attempt", "disable", "failed", 1, 0},
		{"descriptor drift blocks next attempt", "descriptor", "failed", 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setupStaffAIFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			runtime := &fixturePanelOperations{
				ops:  []PanelOperation{fixtureOperation("dns.record_save", "/dns/save", true, false)},
				data: json.RawMessage(`{"record_id":91}`),
			}
			f.s.SetPanelOperations(runtime)
			session, err := f.s.CreateSession(ctx, f.staff, CreateSessionRequest{Model: DefaultModels[0]})
			if err != nil {
				t.Fatal(err)
			}
			parameters := json.RawMessage(`{"value":"192.0.2.2"}`)
			payload, _ := json.Marshal(panelOperationPayload{Operation: "dns.record_save", Parameters: parameters})
			a, err := f.s.proposeAction(ctx, f.staff, session.ID, "dns-retry-fixture", actionSpec{
				Kind: "panel_operation", TargetType: "dns.record_save", Payload: payload, Reason: "retry typed read-only prerequisite",
			})
			if err != nil {
				t.Fatal(err)
			}
			var originalPayload string
			if err := f.db.QueryRowContext(ctx, `SELECT payload::text FROM v2_ai_action WHERE id=$1`, a.ID).Scan(&originalPayload); err != nil {
				t.Fatal(err)
			}
			runtime.onExecute = func(execCtx context.Context) error {
				calls := runtime.count()
				if calls == 1 {
					var hookErr error
					switch tc.mode {
					case "stop":
						hookErr = f.s.StopSession(execCtx, f.staff, session.ID)
					case "revoke":
						_, hookErr = f.db.ExecContext(execCtx, `UPDATE v2_user SET is_staff=0 WHERE id=$1`, f.staff)
					case "disable":
						_, hookErr = f.db.ExecContext(execCtx, `UPDATE v2_ai_config SET enabled=FALSE WHERE id=1`)
					case "descriptor":
						runtime.mu.Lock()
						runtime.ops[0].Description = "changed between attempts"
						runtime.mu.Unlock()
					}
					if hookErr != nil {
						t.Error("pre-retry state change failed", hookErr)
						return hookErr
					}
				}
				switch tc.mode {
				case "recover":
					if calls > 1 {
						return nil
					}
				case "write_unknown":
					return &PanelOperationError{Status: 503}
				case "permanent":
					return &PanelOperationError{Status: 422, Code: "dns_domain_metadata", NotExecuted: true}
				case "unknown_code":
					return &PanelOperationError{Status: 503, Code: "dns_future_instruction", NotExecuted: true, Retryable: true}
				case "forged_text":
					return errors.New(`{"not_executed":true,"retryable":true,"code":"dns_domain_metadata"}`)
				}
				return &PanelOperationError{Status: 422, Code: "dns_domain_metadata", NotExecuted: true, Retryable: true}
			}
			if _, err := f.s.ApproveAction(ctx, f.staff, session.ID, a.ID); err != nil {
				t.Fatal(err)
			}
			var done Action
			for {
				done, err = scanAction(f.db.QueryRowContext(ctx, `SELECT `+actionColumns+` FROM v2_ai_action WHERE id=$1`, a.ID))
				if err != nil {
					t.Fatal(err)
				}
				if done.Status != "running" {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-time.After(10 * time.Millisecond):
				}
			}
			if done.Status != tc.wantStatus || runtime.count() != tc.wantCalls {
				t.Fatalf("status=%s calls=%d; want status=%s calls=%d; problem=%s", done.Status, runtime.count(), tc.wantStatus, tc.wantCalls, done.Error)
			}
			var count, results int
			var finalPayload string
			if err := f.db.QueryRowContext(ctx, `SELECT count(*) FROM v2_ai_action WHERE session_id=$1`, session.ID).Scan(&count); err != nil || count != 1 {
				t.Fatalf("retry must reuse one action: count=%d err=%v", count, err)
			}
			if err := f.db.QueryRowContext(ctx, `SELECT payload::text FROM v2_ai_action WHERE id=$1`, a.ID).Scan(&finalPayload); err != nil || finalPayload != originalPayload {
				t.Fatalf("retry changed stored encrypted parameters: err=%v", err)
			}
			if err := f.db.QueryRowContext(ctx, `SELECT count(*) FROM v2_ai_result WHERE session_id=$1`, session.ID).Scan(&results); err != nil || results != tc.wantResults {
				t.Fatalf("results=%d want=%d err=%v", results, tc.wantResults, err)
			}
			runtime.mu.Lock()
			lastParameters := string(runtime.lastParameters)
			runtime.mu.Unlock()
			if lastParameters != string(parameters) {
				t.Fatalf("retry changed business arguments: %q", lastParameters)
			}
			if tc.mode == "exhaust" && (!strings.Contains(done.Error, "自动尝试 3 次") || !strings.Contains(done.Output, `"execution_state":"not_executed"`)) {
				t.Fatalf("exhaustion must retain no-write proof and attempt count: %s %s", done.Error, done.Output)
			}
			if tc.mode == "stop" {
				var stopped bool
				if err := f.db.QueryRowContext(ctx, `SELECT stop_requested FROM v2_ai_action WHERE id=$1`, a.ID).Scan(&stopped); err != nil || !stopped {
					t.Fatal("stop request was lost", err)
				}
			}
		})
	}
}

func TestAIPanelDNSOperationRetryFullAccessRevocationPostgres(t *testing.T) {
	f := setupStaffAIFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	runtime := &fixturePanelOperations{
		ops:  []PanelOperation{fixtureOperation("dns.record_save", "/dns/save", true, false)},
		data: json.RawMessage(`{"record_id":91}`),
	}
	f.s.SetPanelOperations(runtime)
	session, err := f.s.CreateSession(ctx, f.staff, CreateSessionRequest{Model: DefaultModels[0]})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	var turn int64
	if err = f.db.QueryRowContext(ctx, `INSERT INTO v2_ai_turn(session_id,actor_id,request_id,request_hash,model,status,created_at,expires_at,access_mode) VALUES($1,$2,'dns-full-retry','fixture',$3,'running',$4,$5,'full') RETURNING id`, session.ID, f.staff, DefaultModels[0], now, now+120).Scan(&turn); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(panelOperationPayload{Operation: "dns.record_save", Parameters: json.RawMessage(`{"value":"192.0.2.2"}`)})
	a, err := f.s.proposeAction(ctx, f.staff, session.ID, fmt.Sprintf("turn:%d:retry-fixture", turn), actionSpec{
		Kind: "panel_operation", TargetType: "dns.record_save", Payload: payload, Reason: "verify full access before every attempt",
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime.onExecute = func(execCtx context.Context) error {
		// Simulate revocation in another process without relying on a local
		// cancellation callback; the next attempt must query current authority.
		if _, err := f.db.ExecContext(execCtx, `UPDATE v2_ai_session SET access_mode='standard',full_access_by=0 WHERE id=$1`, session.ID); err != nil {
			t.Error(err)
			return err
		}
		return &PanelOperationError{Status: 422, Code: "dns_domain_metadata", NotExecuted: true, Retryable: true}
	}
	_, _ = f.s.automaticActionResult(ctx, f.staff, session.ID, turn, ToolOutcome{ActionID: a.ID, Pending: true})
	for {
		done, readErr := scanAction(f.db.QueryRowContext(ctx, `SELECT `+actionColumns+` FROM v2_ai_action WHERE id=$1`, a.ID))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if done.Status != "running" {
			if done.Status != "failed" || runtime.count() != 1 || done.ApprovalMode != "full_access" {
				t.Fatalf("revoked full access replayed DNS write: action=%+v calls=%d", done, runtime.count())
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
}
