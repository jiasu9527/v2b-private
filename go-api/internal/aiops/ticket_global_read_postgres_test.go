package aiops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestAITicketGlobalReadPostgres(t *testing.T) {
	f := setupStaffAIFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	marker := fmt.Sprint(time.Now().UnixNano())
	users := []int64{f.user}
	emails := map[int64]string{}
	var initialEmail string
	if err := f.db.QueryRowContext(ctx, `SELECT email FROM v2_user WHERE id=$1`, f.user).Scan(&initialEmail); err != nil {
		t.Fatal(err)
	}
	emails[f.user] = initialEmail
	// Three independent customer accounts ensure an omitted identity filter
	// cannot accidentally be interpreted as the current operator or one user.
	for i := 0; i < 2; i++ {
		var id int64
		email := fmt.Sprintf("global-ticket-%d-%s@example.test", i, marker)
		if err := f.db.QueryRowContext(ctx, `INSERT INTO v2_user(email,password,uuid,token,created_at,updated_at) VALUES($1,'fixture','fixture',$2,1,1) RETURNING id`, email, fmt.Sprintf("tg-%d-%s", i, marker)).Scan(&id); err != nil {
			t.Fatal(err)
		}
		users = append(users, id)
		emails[id] = email
	}
	tickets := []aiTicketDTO{}
	messages := map[int64][]aiTicketMessageDTO{}
	t.Cleanup(func() {
		for _, ticket := range tickets {
			f.db.Exec(`DELETE FROM v2_ticket_message WHERE ticket_id=$1`, ticket.ID)
			f.db.Exec(`DELETE FROM v2_ticket WHERE id=$1`, ticket.ID)
		}
		for _, id := range users[1:] {
			f.db.Exec(`DELETE FROM v2_user WHERE id=$1`, id)
		}
	})
	const createdAt int64 = 1780000000
	for index, fixture := range []struct {
		userIndex          int
		status, replyState int64
	}{{0, 0, 0}, {1, 0, 1}, {2, 0, 0}, {0, 0, 1}, {2, 1, 1}} {
		userID := users[fixture.userIndex]
		// Reverse updated_at relative to IDs: replies must not move existing
		// tickets between list pages ordered by immutable ticket ID.
		ticket := aiTicketDTO{UserID: userID, Email: emails[userID], Subject: fmt.Sprintf("充值补单 ¥%d.50 / %s", index+10, marker), Level: int64(index % 3), Status: fixture.status, ReplyStatus: fixture.replyState, CreatedAt: createdAt + int64(index*100), UpdatedAt: createdAt + int64(10000-index*100)}
		if err := f.db.QueryRowContext(ctx, `INSERT INTO v2_ticket(user_id,subject,level,status,reply_status,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id`, ticket.UserID, ticket.Subject, ticket.Level, ticket.Status, ticket.ReplyStatus, ticket.CreatedAt, ticket.UpdatedAt).Scan(&ticket.ID); err != nil {
			t.Fatal(err)
		}
		tickets = append(tickets, ticket)
		for messageIndex, content := range []string{fmt.Sprintf("订单 order-%d 已支付 %d.50 元，到账少了 2.00 元。", index, index+10), "已核对 200 分差额，请补充付款时间；这是一条员工回复。", "付款时间 2026-05-28 10:11:12；不要把 12.50 元误认成 1250 元。"} {
			author := ticket.UserID
			if messageIndex == 1 {
				author = f.staff
			}
			message := aiTicketMessageDTO{UserID: author, Message: content, FromStaff: author != ticket.UserID, CreatedAt: ticket.CreatedAt + int64(messageIndex+1), TotalCharacters: int64(utf8.RuneCountInString(content))}
			if err := f.db.QueryRowContext(ctx, `INSERT INTO v2_ticket_message(user_id,ticket_id,message,created_at,updated_at) VALUES($1,$2,$3,$4,$4) RETURNING id`, author, ticket.ID, content, message.CreatedAt).Scan(&message.ID); err != nil {
				t.Fatal(err)
			}
			messages[ticket.ID] = append(messages[ticket.ID], message)
		}
	}
	sessions := map[int64]Session{}
	for _, actor := range []int64{f.admin, f.staff, f.other} {
		session, err := f.s.CreateSession(ctx, actor, CreateSessionRequest{Model: DefaultModels[0]})
		if err != nil {
			t.Fatal(err)
		}
		sessions[actor] = session
	}
	call := func(t *testing.T, actor, sessionID int64, name string, arguments map[string]any, target any) {
		t.Helper()
		raw, err := json.Marshal(arguments)
		if err != nil {
			t.Fatal(err)
		}
		result, err := f.s.callTool(ctx, actor, sessionID, ToolCall{ID: "fixture-read", Function: ToolFunction{Name: name, Arguments: string(raw)}})
		if err != nil || result.Pending || result.ActionID != 0 {
			t.Fatalf("%s %s failed: %+v %v", name, raw, result, err)
		}
		if err := json.Unmarshal([]byte(result.Content), target); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("actual provider global call reads all open tickets without identity or reply narrowing", func(t *testing.T) {
		const arguments = `{"status":"open","reply_status":"all","limit":50,"offset":0}`
		relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, err := io.ReadAll(io.LimitReader(r.Body, maxProviderRequestBytes+1))
			if err != nil {
				t.Error("cannot read local model fixture request", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			ticketContractAssertWire(t, raw)
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": providerMessage{Role: "assistant", ToolCalls: []ToolCall{{ID: "global-pg-fixture", Type: "function", Function: ToolFunction{Name: "tickets_list_global", Arguments: arguments}}}}, "finish_reason": "tool_calls"}}})
		}))
		defer relay.Close()
		if _, err := f.s.SaveConfig(ctx, f.admin, ConfigSaveRequest{Enabled: true, BaseURL: relay.URL + "/v1", APIKey: "fixture-local-only", Models: DefaultModels}); err != nil {
			t.Fatal(err)
		}
		for _, actor := range []int64{f.admin, f.staff} {
			response, err := f.s.complete(ctx, DefaultModels[0], []providerMessage{{Role: "system", Content: ticketWorkflowPrompt}, {Role: "user", Content: "查询全站未关闭工单，包括已回复和待回复，不限制用户。"}})
			if err != nil || len(response.ToolCalls) != 1 || response.ToolCalls[0].Function.Name != "tickets_list_global" || response.ToolCalls[0].Function.Arguments != arguments {
				t.Fatal("local model global request contract failed", response, err)
			}
			outcome, err := f.s.callTool(ctx, actor, sessions[actor].ID, response.ToolCalls[0])
			var page pageDTO[aiTicketDTO]
			if err != nil || outcome.Pending || outcome.ActionID != 0 || json.Unmarshal([]byte(outcome.Content), &page) != nil {
				t.Fatal("provider global call failed in real ticket runtime", outcome, err)
			}
			if page.Total != 4 || page.Truncated || page.NextOffset != nil || !reflect.DeepEqual(page.Items, []aiTicketDTO{tickets[3], tickets[2], tickets[1], tickets[0]}) {
				t.Fatal("global query lost replied tickets or restricted one customer", page)
			}
		}
	})
	for _, actor := range []int64{f.admin, f.staff} {
		t.Run(fmt.Sprintf("global list and message pages actor=%d", actor), func(t *testing.T) {
			found := []aiTicketDTO{}
			offset := 0
			for pageNumber := 0; ; pageNumber++ {
				var page pageDTO[aiTicketDTO]
				arguments := map[string]any{"status": 0, "limit": 2}
				if offset > 0 {
					arguments["offset"] = offset
				}
				call(t, actor, sessions[actor].ID, "tickets_list", arguments, &page)
				if page.Total != 4 || page.Limit != 2 || page.Offset != offset || len(page.Items) != 2 {
					t.Fatal("global list was narrowed or pagination changed", page)
				}
				found = append(found, page.Items...)
				if !page.Truncated {
					if page.NextOffset != nil {
						t.Fatal("final list page has continuation", page)
					}
					break
				}
				if page.NextOffset == nil || *page.NextOffset <= offset || pageNumber > 3 {
					t.Fatal("non-progressing list pagination", page)
				}
				offset = *page.NextOffset
			}
			want := []aiTicketDTO{tickets[3], tickets[2], tickets[1], tickets[0]}
			if !reflect.DeepEqual(found, want) {
				t.Fatalf("global open tickets changed identities/statuses/times/subjects:\ngot %+v\nwant %+v", found, want)
			}
			for _, ticket := range found {
				allMessages := []aiTicketMessageDTO{}
				offset := 0
				for {
					var detail struct {
						Ticket   aiTicketDTO                 `json:"ticket"`
						Messages pageDTO[aiTicketMessageDTO] `json:"messages"`
					}
					call(t, actor, sessions[actor].ID, "ticket_get", map[string]any{"ticket_id": ticket.ID, "limit": 2, "offset": offset}, &detail)
					if detail.Ticket != ticket || detail.Messages.Total != 3 || detail.Messages.Offset != offset {
						t.Fatal("ticket detail metadata lost", detail)
					}
					allMessages = append(allMessages, detail.Messages.Items...)
					if !detail.Messages.Truncated {
						if detail.Messages.NextOffset != nil {
							t.Fatal("final message page has continuation", detail.Messages)
						}
						break
					}
					if detail.Messages.NextOffset == nil || *detail.Messages.NextOffset <= offset || offset > 3 {
						t.Fatal("non-progressing message pagination", detail.Messages)
					}
					offset = *detail.Messages.NextOffset
				}
				if !reflect.DeepEqual(allMessages, messages[ticket.ID]) {
					t.Fatalf("message author, amount, content or timestamp changed: got %+v want %+v", allMessages, messages[ticket.ID])
				}
				for _, message := range allMessages {
					var chunk aiTicketMessageChunkDTO
					call(t, actor, sessions[actor].ID, "ticket_message_get", map[string]any{"ticket_id": ticket.ID, "message_id": message.ID}, &chunk)
					if chunk.TicketID != ticket.ID || chunk.MessageID != message.ID || chunk.UserID != message.UserID || chunk.FromStaff != message.FromStaff || chunk.CreatedAt != message.CreatedAt || chunk.Message != message.Message || chunk.Truncated || chunk.NextContentOffset != nil || chunk.TotalCharacters != message.TotalCharacters {
						t.Fatal("single-message read lost original evidence", chunk, message)
					}
				}
			}
			var pending pageDTO[aiTicketDTO]
			call(t, actor, sessions[actor].ID, "tickets_list", map[string]any{"status": 0, "reply_status": 0, "limit": 2}, &pending)
			if pending.Total != 2 || !reflect.DeepEqual(pending.Items, []aiTicketDTO{tickets[2], tickets[0]}) {
				t.Fatal("pending reply filter incorrect", pending)
			}
			var closed pageDTO[aiTicketDTO]
			call(t, actor, sessions[actor].ID, "tickets_list", map[string]any{"status": 1, "limit": 2}, &closed)
			if closed.Total != 1 || !reflect.DeepEqual(closed.Items, []aiTicketDTO{tickets[4]}) {
				t.Fatal("closed filter incorrect", closed)
			}
			for _, test := range []struct {
				status, reply string
				want          []aiTicketDTO
			}{
				{"all", "all", []aiTicketDTO{tickets[4], tickets[3], tickets[2], tickets[1], tickets[0]}},
				{"open", "all", []aiTicketDTO{tickets[3], tickets[2], tickets[1], tickets[0]}},
				{"open", "pending", []aiTicketDTO{tickets[2], tickets[0]}},
				{"open", "replied", []aiTicketDTO{tickets[3], tickets[1]}},
				{"closed", "all", []aiTicketDTO{tickets[4]}},
				{"closed", "pending", []aiTicketDTO{}},
			} {
				found := []aiTicketDTO{}
				offset := 0
				for {
					var page pageDTO[aiTicketDTO]
					call(t, actor, sessions[actor].ID, "tickets_list_global", map[string]any{"status": test.status, "reply_status": test.reply, "limit": 2, "offset": offset}, &page)
					if page.Total != int64(len(test.want)) || page.Offset != offset || page.Limit != 2 {
						t.Fatal("global explicit status or pagination changed", test, page)
					}
					found = append(found, page.Items...)
					if !page.Truncated {
						if page.NextOffset != nil {
							t.Fatal("global final page continued", page)
						}
						break
					}
					if page.NextOffset == nil || *page.NextOffset <= offset || offset > 5 {
						t.Fatal("global tool cursor did not progress", page)
					}
					offset = *page.NextOffset
				}
				if !reflect.DeepEqual(found, test.want) {
					t.Fatal("global explicit filter changed results", test, found)
				}
			}
			var precise pageDTO[aiTicketDTO]
			call(t, actor, sessions[actor].ID, "tickets_list", map[string]any{"user_id": users[0], "email": "", "status": 0, "reply_status": 0, "limit": 50, "offset": 0}, &precise)
			if precise.Total != 1 || !reflect.DeepEqual(precise.Items, []aiTicketDTO{tickets[0]}) {
				t.Fatal("legacy precise customer query silently became global", precise)
			}
		})
	}
	t.Run("operator and own session authorization remain enforced", func(t *testing.T) {
		for _, test := range []struct {
			actor, sessionID int64
			want             error
		}{{f.user, sessions[f.staff].ID, ErrForbidden}, {f.staff, sessions[f.other].ID, ErrNotFound}, {f.staff, sessions[f.admin].ID, ErrNotFound}} {
			for _, tool := range []struct{ name, arguments string }{{"tickets_list_global", `{"status":"open","reply_status":"all","limit":2,"offset":0}`}, {"tickets_list", `{"status":0,"limit":2}`}, {"ticket_get", fmt.Sprintf(`{"ticket_id":%d,"limit":2}`, tickets[0].ID)}, {"ticket_message_get", fmt.Sprintf(`{"ticket_id":%d,"message_id":%d}`, tickets[0].ID, messages[tickets[0].ID][0].ID)}} {
				_, err := f.s.callTool(ctx, test.actor, test.sessionID, ToolCall{ID: "forbidden-read", Function: ToolFunction{Name: tool.name, Arguments: tool.arguments}})
				if !errors.Is(err, test.want) {
					t.Fatal("unauthorized global ticket read", tool.name, test, err)
				}
			}
		}
	})
	var persistedMessages, actions int
	if err := f.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_ticket_message WHERE ticket_id IN($1,$2,$3,$4,$5)`, tickets[0].ID, tickets[1].ID, tickets[2].ID, tickets[3].ID, tickets[4].ID).Scan(&persistedMessages); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_ai_action WHERE session_id IN($1,$2,$3)`, sessions[f.admin].ID, sessions[f.staff].ID, sessions[f.other].ID).Scan(&actions); err != nil || actions != 0 || persistedMessages != 15 {
		t.Fatal("read-only tools changed state", actions, persistedMessages, err)
	}
	t.Run("long unicode message can be read through final amount", func(t *testing.T) {
		content := strings.Repeat("中🙂", 2500) + "\n申请提现金额：123.45 元；最终核对金额：678.90 元。"
		ticket := tickets[0]
		var messageID int64
		messageTime := createdAt + 42
		if err := f.db.QueryRowContext(ctx, `INSERT INTO v2_ticket_message(user_id,ticket_id,message,created_at,updated_at) VALUES($1,$2,$3,$4,$4) RETURNING id`, ticket.UserID, ticket.ID, content, messageTime).Scan(&messageID); err != nil {
			t.Fatal(err)
		}
		total := int64(utf8.RuneCountInString(content))
		for _, actor := range []int64{f.admin, f.staff} {
			var detail struct {
				Ticket   aiTicketDTO                 `json:"ticket"`
				Messages pageDTO[aiTicketMessageDTO] `json:"messages"`
			}
			call(t, actor, sessions[actor].ID, "ticket_get", map[string]any{"ticket_id": ticket.ID, "limit": 1, "offset": 3}, &detail)
			if len(detail.Messages.Items) != 1 {
				t.Fatal("long message missing", detail)
			}
			message := detail.Messages.Items[0]
			if message.ID != messageID || !message.Truncated || message.TotalCharacters != total || message.NextContentOffset == nil || *message.NextContentOffset != 2000 || utf8.RuneCountInString(message.Message) != 2000 {
				t.Fatal("long message lacks continuation evidence", message)
			}
			var initial aiTicketMessageChunkDTO
			call(t, actor, sessions[actor].ID, "ticket_message_get", map[string]any{"ticket_id": ticket.ID, "message_id": messageID}, &initial)
			if initial.Message != message.Message || initial.NextContentOffset == nil || *initial.NextContentOffset != 2000 {
				t.Fatal("default content page mismatch", initial)
			}
			rebuilt := message.Message
			offset := *message.NextContentOffset
			for {
				var chunk aiTicketMessageChunkDTO
				call(t, actor, sessions[actor].ID, "ticket_message_get", map[string]any{"ticket_id": ticket.ID, "message_id": messageID, "content_offset": offset, "content_limit": 777}, &chunk)
				if chunk.TicketID != ticket.ID || chunk.MessageID != messageID || chunk.UserID != ticket.UserID || chunk.FromStaff || chunk.CreatedAt != messageTime || chunk.TotalCharacters != total || chunk.ContentOffset != offset || !utf8.ValidString(chunk.Message) || utf8.RuneCountInString(chunk.Message) > 777 {
					t.Fatal("chunk changed evidence or split unicode", chunk)
				}
				rebuilt += chunk.Message
				if !chunk.Truncated {
					if chunk.NextContentOffset != nil {
						t.Fatal("final content chunk has continuation", chunk)
					}
					break
				}
				if chunk.NextContentOffset == nil || *chunk.NextContentOffset <= offset || *chunk.NextContentOffset > total {
					t.Fatal("non-progressing content pagination", chunk)
				}
				offset = *chunk.NextContentOffset
			}
			if rebuilt != content || !strings.Contains(rebuilt, "123.45 元") || !strings.Contains(rebuilt, "678.90 元") {
				t.Fatal("full body or tail amount lost")
			}
			for _, offset := range []int64{total, total + 1, 9223372036854775807} {
				var chunk aiTicketMessageChunkDTO
				call(t, actor, sessions[actor].ID, "ticket_message_get", map[string]any{"ticket_id": ticket.ID, "message_id": messageID, "content_offset": offset}, &chunk)
				if chunk.Message != "" || chunk.Truncated || chunk.NextContentOffset != nil || chunk.TotalCharacters != total {
					t.Fatal("past-end offset is not terminal", chunk)
				}
			}
			for _, arguments := range []string{fmt.Sprintf(`{"ticket_id":%d,"message_id":%d}`, tickets[1].ID, messageID), fmt.Sprintf(`{"ticket_id":%d,"message_id":2147483647}`, ticket.ID)} {
				_, err := f.s.callTool(ctx, actor, sessions[actor].ID, ToolCall{ID: "wrong-message", Function: ToolFunction{Name: "ticket_message_get", Arguments: arguments}})
				if !errors.Is(err, ErrNotFound) {
					t.Fatal("wrong or missing ticket/message pair allowed", err)
				}
			}
		}
	})
}
