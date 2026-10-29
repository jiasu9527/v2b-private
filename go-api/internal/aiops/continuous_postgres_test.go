package aiops

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func continuousReply(w http.ResponseWriter, round int, keepGoing bool, content string) {
	message := providerMessage{Role: "assistant", Content: content}
	finish := "stop"
	if keepGoing {
		message.ToolCalls = []ToolCall{{ID: fmt.Sprintf("continuous-query-%d", round), Type: "function", Function: ToolFunction{Name: "groups_list", Arguments: `{}`}}}
		finish = "tool_calls"
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": message, "finish_reason": finish}}})
}

func continuousConfigure(t *testing.T, f staffAIFixture, url string) {
	t.Helper()
	if _, err := f.s.SaveConfig(context.Background(), f.admin, ConfigSaveRequest{Enabled: true, BaseURL: url + "/v1", APIKey: "fixture", Models: DefaultModels}); err != nil {
		t.Fatal(err)
	}
}

func continuousWait(t *testing.T, f staffAIFixture, turnID int64) Turn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for {
		turn, err := scanTurn(f.db.QueryRowContext(ctx, `SELECT `+turnColumns+` FROM v2_ai_turn WHERE id=$1`, turnID))
		if err != nil {
			t.Fatal(err)
		}
		if turn.Status != "running" {
			return turn
		}
		select {
		case <-ctx.Done():
			t.Fatal("continuous turn did not terminate", ctx.Err())
		case <-time.After(15 * time.Millisecond):
		}
	}
}

func continuousSend(t *testing.T, f staffAIFixture, req SendMessageRequest) SendMessageResult {
	t.Helper()
	req.Model = DefaultModels[0]
	req.RequestID = fmt.Sprintf("continuous-%d", time.Now().UnixNano())
	result, err := f.s.SendMessage(context.Background(), f.staff, req)
	if err != nil {
		t.Fatal("continuous task admission failed", err)
	}
	return result
}

func TestAIContinuousPostgres(t *testing.T) {
	t.Run("full access exceeds forty rounds while context stays bounded", func(t *testing.T) {
		f := setupStaffAIFixture(t)
		const toolRounds = 90
		const task = "CONTINUOUS_ORIGINAL_TASK_MUST_REMAIN"
		var calls atomic.Int32
		var compacted atomic.Bool
		var maxMessages, maxMessageBytes atomic.Int64
		relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, err := io.ReadAll(io.LimitReader(r.Body, maxProviderRequestBytes+1))
			var request completionRequest
			if err != nil || len(raw) > maxProviderRequestBytes || json.Unmarshal(raw, &request) != nil {
				t.Error("invalid or oversized continuous model request", err, len(raw))
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			n := int(calls.Add(1))
			if n > toolRounds+1 {
				t.Error("model was called after final response")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			messageJSON, _ := json.Marshal(request.Messages)
			maxMessages.Store(max(maxMessages.Load(), int64(len(request.Messages))))
			maxMessageBytes.Store(max(maxMessageBytes.Load(), int64(len(messageJSON))))
			if len(request.Messages) > maxContextMessages || len(messageJSON) > maxHistoryContextBytes {
				t.Error("continuous context grew beyond its bound", len(request.Messages), len(messageJSON))
			}
			if len(request.Messages) == 0 || request.Messages[0].Role != "system" || !strings.Contains(request.Messages[0].Content, paymentConfigurationBoundaryPrompt) {
				t.Error("long-running context lost system policy")
			}
			foundTask, foundPrevious := false, n == 1
			for _, message := range request.Messages {
				if message.Role == "user" && message.Content == task {
					foundTask = true
				}
				if message.Content == contextOmittedNotice {
					compacted.Store(true)
				}
				if message.Role == "tool" && message.ToolCallID == fmt.Sprintf("continuous-query-%d", n-1) {
					foundPrevious = true
					var result map[string]any
					if json.Unmarshal([]byte(message.Content), &result) != nil || result["status"] == "error" {
						t.Error("continuous query did not execute successfully", message.Content)
					}
				}
			}
			if !foundTask || !foundPrevious {
				t.Error("compaction lost task or latest tool result", n, foundTask, foundPrevious)
			}
			assertContextToolPairs(t, request.Messages)
			content := fmt.Sprintf("fixture analysis round %d\n", n) + strings.Repeat("bounded work detail ", 430)
			if n > toolRounds {
				content = "CONTINUOUS_TASK_FINISHED"
			}
			continuousReply(w, n, n <= toolRounds, content)
		}))
		defer relay.Close()
		continuousConfigure(t, f, relay.URL)
		client := f.s.httpClient
		f.s.httpClient = providerClientFunc(func(request *http.Request) (*http.Response, error) {
			if _, hasDeadline := request.Context().Deadline(); hasDeadline {
				t.Error("full worker still inherits a fixed lifetime deadline")
			}
			return client.Do(request)
		})
		started := continuousSend(t, f, SendMessageRequest{Content: task, AccessMode: "full"})
		turn := continuousWait(t, f, started.Turn.ID)
		if turn.Status != "completed" || turn.Error != "" || turn.DraftRound != toolRounds+1 || calls.Load() != toolRounds+1 {
			t.Fatalf("continuous full task stopped early: turn=%+v calls=%d", turn, calls.Load())
		}
		if !compacted.Load() || maxMessageBytes.Load() > maxHistoryContextBytes || maxMessages.Load() > maxContextMessages {
			t.Fatalf("context was not compacted under sustained load: compacted=%v messages=%d bytes=%d", compacted.Load(), maxMessages.Load(), maxMessageBytes.Load())
		}
		var persisted int
		if err := f.db.QueryRow(`SELECT COUNT(*) FROM v2_ai_message WHERE turn_id=$1`, turn.ID).Scan(&persisted); err != nil || persisted != toolRounds*2+2 {
			t.Fatal("compaction deleted persisted history", persisted, err)
		}
	})

	t.Run("full session can continue beyond four thousand persisted messages", func(t *testing.T) {
		f := setupStaffAIFixture(t)
		var calls atomic.Int32
		relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			n := int(calls.Add(1))
			var request completionRequest
			if json.NewDecoder(r.Body).Decode(&request) != nil {
				w.WriteHeader(400)
				return
			}
			if n == 2 {
				found := false
				for _, message := range request.Messages {
					if message.Role == "user" && message.Content == "AFTER_FOUR_THOUSAND_CURRENT_TASK" {
						found = true
					}
				}
				if !found || len(request.Messages) > maxContextMessages {
					t.Error("large stored session was not compacted for next request")
				}
			}
			continuousReply(w, n, false, "fixture complete")
		}))
		defer relay.Close()
		continuousConfigure(t, f, relay.URL)
		first := continuousSend(t, f, SendMessageRequest{Content: "create long lived session", AccessMode: "full"})
		if turn := continuousWait(t, f, first.Turn.ID); turn.Status != "completed" {
			t.Fatal(turn)
		}
		if _, err := f.db.Exec(`INSERT INTO v2_ai_message(session_id,turn_id,role,content,created_at) SELECT $1,$2,'assistant','fixture historical message '||n,1 FROM generate_series(1,4100) n`, first.Session.ID, first.Turn.ID); err != nil {
			t.Fatal(err)
		}
		version := first.Session.AccessVersion
		second := continuousSend(t, f, SendMessageRequest{SessionID: first.Session.ID, Content: "AFTER_FOUR_THOUSAND_CURRENT_TASK", AccessMode: "full", AccessVersion: &version})
		if turn := continuousWait(t, f, second.Turn.ID); turn.Status != "completed" || calls.Load() != 2 {
			t.Fatal("full session stopped at historical message count", turn, calls.Load())
		}
		var count int
		if err := f.db.QueryRow(`SELECT COUNT(*) FROM v2_ai_message WHERE session_id=$1`, first.Session.ID).Scan(&count); err != nil || count != 4104 {
			t.Fatal("history was deleted to admit continuation", count, err)
		}
	})

	t.Run("operator stop cancels an otherwise unbounded tool loop", func(t *testing.T) {
		f := setupStaffAIFixture(t)
		var calls atomic.Int32
		reached, release := make(chan struct{}), make(chan struct{})
		relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Drain the request so cancellation closes the response connection.
			_, _ = io.Copy(io.Discard, r.Body)
			n := int(calls.Add(1))
			if n == 46 {
				close(reached)
				select {
				case <-release:
				case <-r.Context().Done():
				}
				return
			}
			continuousReply(w, n, true, "fixture still working")
		}))
		defer relay.Close()
		defer close(release)
		continuousConfigure(t, f, relay.URL)
		started := continuousSend(t, f, SendMessageRequest{Content: "continuous task to stop manually", AccessMode: "full"})
		select {
		case <-reached:
		case <-time.After(15 * time.Second):
			t.Fatal("unbounded task stopped before stop button could interrupt it", calls.Load())
		}
		if err := f.s.StopSession(context.Background(), f.staff, started.Session.ID); err != nil {
			t.Fatal(err)
		}
		turn := continuousWait(t, f, started.Turn.ID)
		f.s.wg.Wait()
		if turn.Status != "interrupted" || !turn.StopRequested || calls.Load() != 46 {
			t.Fatal("operator stop failed to terminate continuous loop", turn, calls.Load())
		}
		var toolResults int
		if err := f.db.QueryRow(`SELECT COUNT(*) FROM v2_ai_message WHERE turn_id=$1 AND role='tool'`, turn.ID).Scan(&toolResults); err != nil || toolResults != 45 {
			t.Fatal("post-stop tool executed or previous results lost", toolResults, err)
		}
	})

	t.Run("standard mode retains its six round bound", func(t *testing.T) {
		f := setupStaffAIFixture(t)
		var calls atomic.Int32
		relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			n := int(calls.Add(1))
			continuousReply(w, n, true, "fixture standard query")
		}))
		defer relay.Close()
		continuousConfigure(t, f, relay.URL)
		client := f.s.httpClient
		f.s.httpClient = providerClientFunc(func(request *http.Request) (*http.Response, error) {
			deadline, hasDeadline := request.Context().Deadline()
			if !hasDeadline || time.Until(deadline) > 120*time.Second {
				t.Error("standard worker lost its bounded timeout")
			}
			return client.Do(request)
		})
		started := continuousSend(t, f, SendMessageRequest{Content: "bounded standard task", AccessMode: "standard"})
		turn := continuousWait(t, f, started.Turn.ID)
		if turn.Status != "failed" || calls.Load() != 6 || turn.AccessMode != "standard" {
			t.Fatal("standard mode no longer bounded", turn, calls.Load())
		}
	})
}
