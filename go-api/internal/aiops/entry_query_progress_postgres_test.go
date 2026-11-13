package aiops

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

const entryProgressWrongRevision = `{"host":"example.test","port":443,"limit":50,"offset":0,"revision":"71"}`

func entryProgressReply(w http.ResponseWriter, round int, name, arguments string) {
	message := providerMessage{Role: "assistant", Content: "根据已取得结果说明证据范围"}
	finish := "stop"
	if name != "" {
		message.ToolCalls = []ToolCall{{ID: fmt.Sprintf("entry-progress-%d", round), Type: "function", Function: ToolFunction{Name: name, Arguments: arguments}}}
		finish = "tool_calls"
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": message, "finish_reason": finish}}})
}

func hasEntryProgressCorrection(messages []providerMessage) bool {
	for _, message := range messages {
		if message.Role == "system" && message.Content == entryQueryCorrectionPrompt {
			return true
		}
	}
	return false
}

func TestAIEntryQueryProgressPostgres(t *testing.T) {
	t.Run("empty target filter exits instead of hundreds of rounds", func(t *testing.T) {
		f := setupStaffAIFixture(t)
		var calls atomic.Int32
		relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var request completionRequest
			if json.NewDecoder(r.Body).Decode(&request) != nil {
				t.Error("invalid model request")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			n := int(calls.Add(1))
			assertContextToolPairs(t, request.Messages)
			if n > 5 {
				t.Error("empty target loop did not stop")
				entryProgressReply(w, n, "", "")
				return
			}
			if hasEntryProgressCorrection(request.Messages) != (n >= 3) {
				t.Error("missing or premature target-id correction", n)
			}
			entryProgressReply(w, n, "entry_probe_targets", `{"limit":50,"offset":0,"policy_id":511,"target_id":1}`)
		}))
		defer relay.Close()
		continuousConfigure(t, f, relay.URL)
		started := continuousSend(t, f, SendMessageRequest{Content: "只查询入口已有状态", AccessMode: "full"})
		turn := continuousWait(t, f, started.Turn.ID)
		if turn.Status != "completed" || turn.Error != "" || turn.DraftRound != 5 || calls.Load() != 5 {
			t.Fatal("empty target loop did not finish", turn, calls.Load())
		}
		var results, actions, summaries int
		if err := f.db.QueryRow(`SELECT COUNT(*) FROM v2_ai_message WHERE turn_id=$1 AND role='tool' AND content::jsonb->'targets'->>'total'='0'`, turn.ID).Scan(&results); err != nil || results != 5 {
			t.Fatal("empty target evidence not persisted", results, err)
		}
		if err := f.db.QueryRow(`SELECT COUNT(*) FROM v2_ai_action WHERE session_id=$1`, started.Session.ID).Scan(&actions); err != nil || actions != 0 {
			t.Fatal("query correction dispatched writes or probes", actions, err)
		}
		expected := (&entryQueryProgressGuard{repeatedReasons: entryQueryEmptyTargets}).stoppedMessage()
		if err := f.db.QueryRow(`SELECT COUNT(*) FROM v2_ai_message WHERE turn_id=$1 AND role='assistant' AND content=$2`, turn.ID, expected).Scan(&summaries); err != nil || summaries != 1 {
			t.Fatal("empty target diagnosis not persisted", summaries, err)
		}
	})
	for _, mode := range []string{"full", "standard"} {
		t.Run(mode+" invalid revision loop ends and fresh user turn resets", func(t *testing.T) {
			f := setupStaffAIFixture(t)
			var calls atomic.Int32
			relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request completionRequest
				if json.NewDecoder(r.Body).Decode(&request) != nil {
					t.Error("invalid model request")
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				n := int(calls.Add(1))
				if n > 10 {
					t.Error("repeated invalid entry read did not stop")
					entryProgressReply(w, n, "", "")
					return
				}
				assertContextToolPairs(t, request.Messages)
				if want := (n-1)%5 >= 2; hasEntryProgressCorrection(request.Messages) != want {
					t.Errorf("round %d correction=%v want=%v", n, !want, want)
				}
				entryProgressReply(w, n, "host_references", entryProgressWrongRevision)
			}))
			defer relay.Close()
			continuousConfigure(t, f, relay.URL)
			var session Session
			for turnIndex := 0; turnIndex < 2; turnIndex++ {
				req := SendMessageRequest{Content: "只查看这个入口的已有状态", AccessMode: mode}
				if turnIndex > 0 {
					req.SessionID, req.AccessVersion = session.ID, &session.AccessVersion
				}
				started := continuousSend(t, f, req)
				session = started.Session
				turn := continuousWait(t, f, started.Turn.ID)
				if turn.Status != "completed" || turn.Error != "" || turn.DraftRound != 5 || calls.Load() != int32((turnIndex+1)*5) {
					t.Fatal("entry loop failed to finish honestly", turn, calls.Load())
				}
				var summaries, results, actions int
				expected := (&entryQueryProgressGuard{repeatedReasons: entryQueryInvalidArguments}).stoppedMessage()
				if err := f.db.QueryRow(`SELECT COUNT(*) FROM v2_ai_message WHERE turn_id=$1 AND role='assistant' AND content=$2`, turn.ID, expected).Scan(&summaries); err != nil || summaries != 1 {
					t.Fatal("missing persisted no-progress explanation", summaries, err)
				}
				if err := f.db.QueryRow(`SELECT COUNT(*) FROM v2_ai_message WHERE turn_id=$1 AND role='tool' AND content::jsonb->>'code'='invalid_arguments'`, turn.ID).Scan(&results); err != nil || results != 5 {
					t.Fatal("tool result history lost", results, err)
				}
				if err := f.db.QueryRow(`SELECT COUNT(*) FROM v2_ai_action WHERE session_id=$1`, session.ID).Scan(&actions); err != nil || actions != 0 {
					t.Fatal("query recovery created write/probe action", actions, err)
				}
			}
		})
	}
	t.Run("productive alternate query clears stale warning and repeat count", func(t *testing.T) {
		f := setupStaffAIFixture(t)
		var calls atomic.Int32
		relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var request completionRequest
			if json.NewDecoder(r.Body).Decode(&request) != nil {
				t.Error("invalid model request")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			n := int(calls.Add(1))
			assertContextToolPairs(t, request.Messages)
			switch n {
			case 1, 2, 4, 5:
				if n == 4 && hasEntryProgressCorrection(request.Messages) {
					t.Error("successful unrelated read retained stale correction")
				}
				entryProgressReply(w, n, "host_references", entryProgressWrongRevision)
			case 3:
				if !hasEntryProgressCorrection(request.Messages) {
					t.Error("model did not receive correction")
				}
				entryProgressReply(w, n, "user_lookup", fmt.Sprintf(`{"query":%q}`, fmt.Sprint(f.user)))
			case 6:
				entryProgressReply(w, n, "", "")
			default:
				t.Error("unexpected extra model request", n)
				w.WriteHeader(http.StatusBadRequest)
			}
		}))
		defer relay.Close()
		continuousConfigure(t, f, relay.URL)
		started := continuousSend(t, f, SendMessageRequest{Content: "查询用户已有入口状态，不发新探测", AccessMode: "full"})
		turn := continuousWait(t, f, started.Turn.ID)
		if turn.Status != "completed" || turn.Error != "" || calls.Load() != 6 {
			t.Fatal("productive work was prematurely stopped", turn, calls.Load())
		}
		var summaries int
		if err := f.db.QueryRow(`SELECT COUNT(*) FROM v2_ai_message WHERE turn_id=$1 AND content LIKE '%' || $2`, turn.ID, entryQueryStoppedMessage).Scan(&summaries); err != nil || summaries != 0 {
			t.Fatal("unexpected forced summary", summaries, err)
		}
	})
}
