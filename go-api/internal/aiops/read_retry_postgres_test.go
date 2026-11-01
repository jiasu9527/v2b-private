package aiops

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestReadToolRetryPostgresPanelQueryAndRevocation(t *testing.T) {
	for _, revoked := range []bool{false, true} {
		name := "transient query restored"
		if revoked {
			name = "permissions rechecked before retry"
		}
		t.Run(name, func(t *testing.T) {
			f := setupStaffAIFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			session, err := f.s.CreateSession(ctx, f.staff, CreateSessionRequest{Model: DefaultModels[0]})
			if err != nil {
				t.Fatal(err)
			}
			data, _ := json.Marshal(map[string]string{"content": strings.Repeat("fixture", 3000)})
			runtime := &fixturePanelOperations{ops: []PanelOperation{fixtureOperation("notices.list", "/notice/fetch", false, false)}, data: data}
			runtime.onExecute = func(ctx context.Context) error {
				if runtime.count() != 1 {
					return nil
				}
				if revoked {
					if _, err := f.db.ExecContext(ctx, `UPDATE v2_user SET is_staff=0 WHERE id=$1`, f.staff); err != nil {
						t.Fatal(err)
					}
				}
				return &PanelOperationError{Status: 503, Message: "PRIVATE_SENTINEL"}
			}
			f.s.SetPanelOperations(runtime)
			out, err := f.s.callToolWithRetry(ctx, f.staff, session.ID, ToolCall{ID: "fixture-retry", Function: ToolFunction{Name: "panel_query", Arguments: `{"operation":"notices.list","parameters":{}}`}}, nil)
			if strings.Contains(out.Content, "PRIVATE_SENTINEL") || out.Pending || out.ActionID != 0 {
				t.Fatal(out, err)
			}
			wantResults := 1
			if revoked {
				wantResults = 0
				if !errors.Is(err, ErrForbidden) || runtime.count() != 1 {
					t.Fatal("revoked operator retried backend", out, err, runtime.count())
				}
			} else {
				var chunk panelResultChunk
				if err != nil || runtime.count() != 2 || json.Unmarshal([]byte(out.Content), &chunk) != nil || chunk.ResultID <= 0 || !chunk.Truncated {
					t.Fatal(out, err, runtime.count())
				}
			}
			var results, actions int
			if err := f.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_ai_result WHERE session_id=$1`, session.ID).Scan(&results); err != nil {
				t.Fatal(err)
			}
			if err := f.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_ai_action WHERE session_id=$1`, session.ID).Scan(&actions); err != nil {
				t.Fatal(err)
			}
			if results != wantResults || actions != 0 {
				t.Fatal("read retry created duplicate results or actions", results, actions)
			}
		})
	}
}
