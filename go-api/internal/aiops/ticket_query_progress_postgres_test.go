package aiops

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const ticketRepeatedWrongArgs = `{"email":"","limit":50,"offset":0,"reply_status":0,"status":0,"user_id":1}`

func ticketProgressReply(w http.ResponseWriter, round int, name, arguments string) {
	message := providerMessage{Role: "assistant", Content: "继续核对查询结果"}
	finish := "stop"
	if name != "" {
		message.ToolCalls = []ToolCall{{ID: fmt.Sprintf("ticket-progress-%d", round), Type: "function", Function: ToolFunction{Name: name, Arguments: arguments}}}
		finish = "tool_calls"
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": message, "finish_reason": finish}}})
}

func ticketProgressSeed(t *testing.T, f staffAIFixture) int64 {
	t.Helper()
	var id int64
	if err := f.db.QueryRow(`INSERT INTO v2_ticket(user_id,subject,level,status,reply_status,created_at,updated_at) VALUES($1,'fixture open but replied',1,0,1,1,1) RETURNING id`, f.user).Scan(&id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.db.Exec(`DELETE FROM v2_ticket WHERE id=$1`, id) })
	return id
}

func TestAITicketEmptyQueryProgressPostgres(t *testing.T) {
	for _, mode := range []string{"full", "standard"} {
		t.Run(mode+" repeat loop ends and next user turn gets fresh guard", func(t *testing.T) {
			f := setupStaffAIFixture(t)
			ticketProgressSeed(t, f)
			var calls atomic.Int32
			relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request completionRequest
				if json.NewDecoder(r.Body).Decode(&request) != nil {
					t.Error("invalid model request")
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				n := int(calls.Add(1))
				if n > 6 {
					t.Error("unchanged empty query loop did not terminate")
					ticketProgressReply(w, n, "", "")
					return
				}
				assertContextToolPairs(t, request.Messages)
				if n%3 == 0 && !strings.Contains(request.Messages[0].Content, ticketEmptyQueryCorrectionPrompt) {
					t.Error("repeat correction missing before third query")
				}
				if n%3 == 1 && strings.Contains(request.Messages[0].Content, ticketEmptyQueryCorrectionPrompt) {
					t.Error("new turn inherited stale correction")
				}
				ticketProgressReply(w, n, "tickets_list", ticketRepeatedWrongArgs)
			}))
			defer relay.Close()
			continuousConfigure(t, f, relay.URL)
			var session Session
			for turnIndex := 0; turnIndex < 2; turnIndex++ {
				req := SendMessageRequest{Content: "看一下近期用户发起打开的工单", AccessMode: mode}
				if turnIndex > 0 {
					req.SessionID = session.ID
					req.AccessVersion = &session.AccessVersion
				}
				started := continuousSend(t, f, req)
				session = started.Session
				turn := continuousWait(t, f, started.Turn.ID)
				if turn.Status != "completed" || turn.Error != "" || turn.DraftRound != 3 || calls.Load() != int32((turnIndex+1)*3) {
					t.Fatal("query loop not ended cleanly after correction", turn, calls.Load())
				}
				var results, summaries, actions int
				if err := f.db.QueryRow(`SELECT COUNT(*) FROM v2_ai_message WHERE turn_id=$1 AND role='tool' AND content::jsonb->'query'->>'scope'='filtered_users' AND content::jsonb->'query'->>'user_id'='1' AND content::jsonb->>'total'='0'`, turn.ID).Scan(&results); err != nil || results != 3 {
					t.Fatal("exact user query was silently widened or history lost", results, err)
				}
				if err := f.db.QueryRow(`SELECT COUNT(*) FROM v2_ai_message WHERE turn_id=$1 AND role='assistant' AND content=$2`, turn.ID, ticketEmptyQueryStoppedMessage).Scan(&summaries); err != nil || summaries != 1 {
					t.Fatal("honest scope summary not persisted", summaries, err)
				}
				if err := f.db.QueryRow(`SELECT COUNT(*) FROM v2_ai_action WHERE session_id=$1`, session.ID).Scan(&actions); err != nil || actions != 0 {
					t.Fatal("read correction created actions", actions, err)
				}
			}
		})
	}
	t.Run("model corrects scope then reads returned ticket instead of stopping", func(t *testing.T) {
		f := setupStaffAIFixture(t)
		ticketID := ticketProgressSeed(t, f)
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
			case 1, 2:
				ticketProgressReply(w, n, "tickets_list", ticketRepeatedWrongArgs)
			case 3:
				if !strings.Contains(request.Messages[0].Content, ticketEmptyQueryCorrectionPrompt) {
					t.Error("model was not given scope correction")
				}
				ticketProgressReply(w, n, "tickets_list_global", `{"status":"open","reply_status":"all","limit":50,"offset":0}`)
			case 4:
				var page aiTicketListDTO
				last := request.Messages[len(request.Messages)-1]
				if last.Role != "tool" || json.Unmarshal([]byte(last.Content), &page) != nil || page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != ticketID || page.Items[0].ReplyStatus != 1 || page.Query.Scope != "all_users" {
					t.Error("corrected query failed to recover open replied ticket", last.Content)
				}
				if strings.Contains(request.Messages[0].Content, ticketEmptyQueryCorrectionPrompt) {
					t.Error("successful query retained stale correction")
				}
				ticketProgressReply(w, n, "ticket_get", fmt.Sprintf(`{"ticket_id":%d}`, ticketID))
			case 5:
				ticketProgressReply(w, n, "", "")
			default:
				t.Error("unexpected additional model request", n)
				w.WriteHeader(http.StatusBadRequest)
			}
		}))
		defer relay.Close()
		continuousConfigure(t, f, relay.URL)
		started := continuousSend(t, f, SendMessageRequest{Content: "查询全站未关闭工单", AccessMode: "full"})
		turn := continuousWait(t, f, started.Turn.ID)
		if turn.Status != "completed" || turn.Error != "" || calls.Load() != 5 {
			t.Fatal("scope recovery did not finish", turn, calls.Load())
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		history, err := f.s.history(ctx, started.Session.ID, turn.ID)
		if err != nil {
			t.Fatal(err)
		}
		assertContextToolPairs(t, history)
		for _, message := range history {
			if message.Content == ticketEmptyQueryStoppedMessage {
				t.Fatal("productive corrected query was stopped")
			}
		}
	})
}
