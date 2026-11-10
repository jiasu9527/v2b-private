package newapibridge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestChatPostgresRequestReceiptIsolationBranchAndDeletion(t *testing.T) {
	s := chatFixture(t)
	ctx := context.Background()
	c := chatCreateFixture(t, s, 1)
	request := ChatSendRequest{ConversationID: c.ID, ClientRequestID: "receipt-source", Content: "hello"}
	var calls atomic.Int32
	open := func(context.Context, int64, string, map[string]any) (io.ReadCloser, error) {
		calls.Add(1)
		return chatSuccessfulStream(), nil
	}
	if _, err := s.GetChatRequest(ctx, 0, request.ClientRequestID); !errors.Is(err, ErrForbidden) {
		t.Fatal("unauthenticated receipt", err)
	}
	for _, invalid := range []string{"", "short", "request/invalid", strings.Repeat("a", 101)} {
		if _, err := s.GetChatRequest(ctx, 1, invalid); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid receipt id", invalid, err)
		}
	}
	state, err := s.GetChatRequest(ctx, 1, request.ClientRequestID)
	if err != nil || state.Found || state.Deleted || state.ConversationID != 0 {
		t.Fatal("missing receipt", state, err)
	}
	emit := func(string, any) error { return nil } // Client lost every SSE frame.
	if err = s.sendChatWithOpener(ctx, 1, "fixture", request, emit, open); err != nil {
		t.Fatal(err)
	}
	state, err = s.GetChatRequest(ctx, 1, request.ClientRequestID)
	if err != nil || !state.Found || state.Deleted || state.Status != "complete" || state.ConversationID != c.ID || state.AssistantMessageID <= state.UserMessageID {
		t.Fatal("source receipt", state, err)
	}
	branch := ChatSendRequest{ConversationID: c.ID, ClientRequestID: "receipt-branch", BranchFromMessageID: state.AssistantMessageID}
	if err = s.sendChatWithOpener(ctx, 1, "fixture", branch, emit, open); err != nil {
		t.Fatal(err)
	}
	branchState, err := s.GetChatRequest(ctx, 1, branch.ClientRequestID)
	if err != nil || !branchState.Found || branchState.Deleted || branchState.Status != "complete" || branchState.ConversationID == c.ID {
		t.Fatal("lost meta branch recovery", branchState, err)
	}
	branchDetail, err := s.GetConversation(ctx, 1, branchState.ConversationID)
	if err != nil || branchDetail.Conversation.ParentID != c.ID || branchDetail.Messages[len(branchDetail.Messages)-1].ID != branchState.AssistantMessageID {
		t.Fatal("receipt points to wrong branch", branchDetail, err)
	}
	other, err := s.GetChatRequest(ctx, 2, branch.ClientRequestID)
	if err != nil || other.Found || other.ConversationID != 0 || other.Status != "" {
		t.Fatal("cross-user receipt disclosure", other, err)
	}
	encoded, err := json.Marshal(branchState)
	if err != nil || strings.Contains(string(encoded), "request_hash") || strings.Contains(string(encoded), "content") {
		t.Fatal("receipt leaked private input", string(encoded), err)
	}
	if err = s.DeleteConversation(ctx, 1, branchState.ConversationID); err != nil {
		t.Fatal(err)
	}
	deleted, err := s.GetChatRequest(ctx, 1, branch.ClientRequestID)
	if err != nil || !deleted.Found || !deleted.Deleted || deleted.ConversationID != branchState.ConversationID {
		t.Fatal("deleted tombstone receipt", deleted, err)
	}
	if err = s.sendChatWithOpener(ctx, 1, "fixture", branch, emit, open); !errors.Is(err, ErrConflict) || calls.Load() != 2 {
		t.Fatal("receipt/replay made another paid request", calls.Load(), err)
	}
}

func TestChatPostgresRequestReceiptRunningIsReadOnlyAndErrorsAreNotAbsence(t *testing.T) {
	s := chatFixture(t)
	ctx := context.Background()
	c := chatCreateFixture(t, s, 1)
	req := ChatSendRequest{ConversationID: c.ID, ClientRequestID: "receipt-running", Content: "hello"}
	if _, err := s.prepareChat(ctx, 1, req); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE v2_newapi_chat SET active_until=$1 WHERE id=$2`, time.Now().Add(-time.Minute).Unix(), c.ID); err != nil {
		t.Fatal(err)
	}
	state, err := s.GetChatRequest(ctx, 1, req.ClientRequestID)
	if err != nil || !state.Found || state.Status != "running" || state.Deleted {
		t.Fatal("read-only receipt modified expired run", state, err)
	}
	// Existing detail recovery owns the lease transition; receipt only reflects it.
	if _, err = s.GetConversation(ctx, 1, c.ID); err != nil {
		t.Fatal(err)
	}
	state, err = s.GetChatRequest(ctx, 1, req.ClientRequestID)
	if err != nil || state.Status != "unknown" {
		t.Fatal("recovered receipt", state, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = s.GetChatRequest(cancelled, 1, req.ClientRequestID); err == nil {
		t.Fatal("query failure was mistaken for absence")
	}
}

func TestWorkspaceSSECompletePartialAndNoToolExecution(t *testing.T) {
	cases := []struct {
		input, want string
		fail        bool
	}{
		{"data: {\"choices\":[{\"delta\":{\"content\":\"你好\"}}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", "你好", false},
		{"data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n", "partial", true},
		{"data: {\"error\":{\"message\":\"secret credential\"}}\n\n", "", true},
		{"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"name\":\"ssh\"}]}}]}\n\n", "", true},
	}
	for _, tc := range cases {
		var text strings.Builder
		_, err := readWorkspaceStream(strings.NewReader(tc.input), func(s string) error { text.WriteString(s); return nil })
		if (err != nil) != tc.fail || text.String() != tc.want {
			t.Fatal(text.String(), err)
		}
	}
}

func chatFixture(t *testing.T) *Service {
	t.Helper()
	s, _, _ := walletPostgresFixture(t)
	// The parent service migration also installs these statements; executing IF
	// NOT EXISTS makes this fixture useful while sibling integration is ongoing.
	for _, stmt := range workspaceChatSchema {
		if _, err := s.db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	return s
}
func chatCreateFixture(t *testing.T, s *Service, userID int64) Conversation {
	t.Helper()
	c, err := s.CreateConversation(context.Background(), userID, ConversationCreateRequest{Mode: "chat", Model: "fixture-model"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func chatSuccessfulStream() io.ReadCloser {
	return io.NopCloser(strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\ndata: [DONE]\n\n"))
}
func TestChatPostgresIdempotencyIsolationBranchAndTombstone(t *testing.T) {
	s := chatFixture(t)
	ctx := context.Background()
	c := chatCreateFixture(t, s, 1)
	var calls atomic.Int32
	open := func(context.Context, int64, string, map[string]any) (io.ReadCloser, error) {
		calls.Add(1)
		return chatSuccessfulStream(), nil
	}
	emit := func(string, any) error { return nil }
	req := ChatSendRequest{ConversationID: c.ID, ClientRequestID: "request-one", Content: "hello"}
	if err := s.sendChatWithOpener(ctx, 1, "test", req, emit, open); err != nil {
		t.Fatal(err)
	}
	if err := s.sendChatWithOpener(ctx, 1, "test", req, emit, open); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("duplicate paid request", calls.Load())
	}
	if _, err := s.GetConversation(ctx, 2, c.ID); !errors.Is(err, ErrForbidden) {
		t.Fatal("cross-user", err)
	}
	cross := req
	cross.ClientRequestID = "cross-user-request"
	if err := s.sendChatWithOpener(ctx, 2, "test", cross, emit, open); !errors.Is(err, ErrForbidden) {
		t.Fatal("cross-user send", err)
	}
	changed := req
	changed.Content = "changed"
	if err := s.sendChatWithOpener(ctx, 1, "test", changed, emit, open); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	detail, err := s.GetConversation(ctx, 1, c.ID)
	if err != nil || len(detail.Messages) != 2 {
		t.Fatal(detail, err)
	}
	branch := ChatSendRequest{ConversationID: c.ID, ClientRequestID: "request-branch", BranchFromMessageID: detail.Messages[1].ID}
	var branchID int64
	onEvent := func(event string, data any) error {
		if event == "meta" {
			branchID = data.(map[string]any)["conversation_id"].(int64)
		}
		return nil
	}
	if err = s.sendChatWithOpener(ctx, 1, "test", branch, onEvent, open); err != nil {
		t.Fatal(err)
	}
	if branchID == c.ID {
		t.Fatal("branch modified source")
	}
	detail, err = s.GetConversation(ctx, 1, c.ID)
	if err != nil || len(detail.Messages) != 2 {
		t.Fatal(detail, err)
	}
	if err = s.DeleteConversation(ctx, 1, c.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.sendChatWithOpener(ctx, 1, "test", req, emit, open); !errors.Is(err, ErrConflict) {
		t.Fatal("deleted replay", err)
	}
	if calls.Load() != 2 {
		t.Fatal(calls.Load())
	}
}
func TestChatPostgresConcurrentAndPartialNoAutomaticRetry(t *testing.T) {
	s := chatFixture(t)
	c := chatCreateFixture(t, s, 1)
	ctx := context.Background()
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	open := func(context.Context, int64, string, map[string]any) (io.ReadCloser, error) {
		calls.Add(1)
		close(started)
		<-release
		return io.NopCloser(strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"saved partial\"}}]}\n\n")), nil
	}
	req := ChatSendRequest{ConversationID: c.ID, ClientRequestID: "request-partial", Content: "hello"}
	emit := func(string, any) error { return nil }
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := s.sendChatWithOpener(ctx, 1, "test", req, emit, open); !errors.Is(err, ErrChatInterrupted) {
			t.Error(err)
		}
	}()
	<-started
	other := req
	other.ClientRequestID = "request-other"
	for _, attempt := range []ChatSendRequest{req, other} {
		if err := s.sendChatWithOpener(ctx, 1, "test", attempt, emit, open); !errors.Is(err, ErrChatBusy) {
			t.Fatal("concurrent", err)
		}
	}
	close(release)
	wg.Wait()
	if err := s.sendChatWithOpener(ctx, 1, "test", req, emit, open); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("partial replay retried upstream")
	}
	d, err := s.GetConversation(ctx, 1, c.ID)
	if err != nil || len(d.Messages) != 2 || d.Messages[1].Status != "partial" || d.Messages[1].Content != "saved partial" {
		t.Fatal(d, err)
	}
}
func TestChatPostgresCrashLeaseAndCharacterIsolation(t *testing.T) {
	s := chatFixture(t)
	ctx := context.Background()
	c := chatCreateFixture(t, s, 1)
	req := ChatSendRequest{ConversationID: c.ID, ClientRequestID: "request-crash", Content: "hello"}
	if _, err := s.prepareChat(ctx, 1, req); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE v2_newapi_chat SET active_until=$1 WHERE id=$2`, time.Now().Add(-time.Minute).Unix(), c.ID); err != nil {
		t.Fatal(err)
	}
	d, err := s.GetConversation(ctx, 1, c.ID)
	if err != nil || d.Messages[1].Status != "unknown" {
		t.Fatal(d, err)
	}
	if err := s.sendChatWithOpener(ctx, 1, "test", req, func(string, any) error { return nil }, func(context.Context, int64, string, map[string]any) (io.ReadCloser, error) {
		t.Fatal("crash retry called upstream")
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	character, err := s.SaveCharacter(ctx, 1, CharacterSaveRequest{Data: fixtureCard().Data})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.GetCharacter(ctx, 2, character.ID); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	if _, err = s.SaveCharacter(ctx, 2, CharacterSaveRequest{ID: character.ID, ExpectedVersion: 1, Data: fixtureCard().Data}); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if err = s.DeleteCharacter(ctx, 2, character.ID); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
}

func TestChatPostgresCancellationSavesPartialAndClosesUpstream(t *testing.T) {
	s := chatFixture(t)
	c := chatCreateFixture(t, s, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	read, write := io.Pipe()
	open := func(context.Context, int64, string, map[string]any) (io.ReadCloser, error) {
		go func() {
			_, _ = io.WriteString(write, "data: {\"choices\":[{\"delta\":{\"content\":\"before cancel\"}}]}\n\n")
		}()
		return read, nil
	}
	emit := func(event string, data any) error {
		if event == "delta" {
			cancel()
		}
		return nil
	}
	err := s.sendChatWithOpener(ctx, 1, "test", ChatSendRequest{ConversationID: c.ID, ClientRequestID: "request-cancel", Content: "hi"}, emit, open)
	if !errors.Is(err, ErrChatInterrupted) {
		t.Fatal(err)
	}
	_ = write.Close()
	d, err := s.GetConversation(context.Background(), 1, c.ID)
	if err != nil || len(d.Messages) != 2 || d.Messages[1].Status != "partial" || d.Messages[1].Content != "before cancel" {
		t.Fatal(d, err)
	}
}

func TestChatPostgresMessagePaginationAndBranchCrashReplay(t *testing.T) {
	s := chatFixture(t)
	ctx := context.Background()
	c := chatCreateFixture(t, s, 1)
	open := func(context.Context, int64, string, map[string]any) (io.ReadCloser, error) {
		return chatSuccessfulStream(), nil
	}
	req := ChatSendRequest{ConversationID: c.ID, ClientRequestID: "pagination-original", Content: "hi"}
	if err := s.sendChatWithOpener(ctx, 1, "test", req, func(string, any) error { return nil }, open); err != nil {
		t.Fatal(err)
	}
	p, err := s.GetConversationMessages(ctx, 1, c.ID, 0, 1)
	if err != nil || len(p.Items) != 1 || !p.HasMore || p.Items[0].Role != "assistant" {
		t.Fatal(p, err)
	}
	q, err := s.GetConversationMessages(ctx, 1, c.ID, p.NextBeforeID, 1)
	if err != nil || len(q.Items) != 1 || q.HasMore || q.Items[0].Role != "user" {
		t.Fatal(q, err)
	}
	branch := ChatSendRequest{ConversationID: c.ID, ClientRequestID: "branch-crash-replay", BranchFromMessageID: p.Items[0].ID}
	plan, err := s.prepareChat(ctx, 1, branch)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`UPDATE v2_newapi_chat SET active_until=$1 WHERE id=$2`, time.Now().Add(-time.Minute).Unix(), plan.Conversation.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.sendChatWithOpener(ctx, 1, "test", branch, func(string, any) error { return nil }, func(context.Context, int64, string, map[string]any) (io.ReadCloser, error) {
		t.Fatal("stale branch opened upstream")
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestChatPostgresPreGenerationErrorsAreFailedAndIdempotent(t *testing.T) {
	s := chatFixture(t)
	ctx := context.Background()
	cases := []struct {
		failure error
		code    string
	}{
		{ErrWorkspaceQuota, "insufficient_ai_balance"},
		{ErrWorkspaceRateLimit, "model_rate_limit"},
		{ErrWorkspaceContext, "model_context_limit"},
		{ErrWorkspaceModel, "model_unavailable"},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			c := chatCreateFixture(t, s, 1)
			calls := 0
			open := func(context.Context, int64, string, map[string]any) (io.ReadCloser, error) {
				calls++
				return nil, tc.failure
			}
			req := ChatSendRequest{ConversationID: c.ID, ClientRequestID: "failure-" + tc.code, Content: "hi"}
			gotCode := ""
			emit := func(event string, data any) error {
				if event == "error" {
					gotCode = data.(map[string]any)["code"].(string)
				}
				return nil
			}
			if err := s.sendChatWithOpener(ctx, 1, "test", req, emit, open); !errors.Is(err, tc.failure) {
				t.Fatal(err)
			}
			if gotCode != tc.code {
				t.Fatal(gotCode)
			}
			detail, err := s.GetConversation(ctx, 1, c.ID)
			if err != nil || len(detail.Messages) != 2 || detail.Messages[1].Status != "failed" {
				t.Fatal(detail, err)
			}
			if err = s.sendChatWithOpener(ctx, 1, "test", req, emit, open); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatal("failed request invoked paid endpoint twice", calls)
			}
		})
	}
	if chatOpenFailedBeforeGeneration(ErrRemoteUnavailable) {
		t.Fatal("transport error cannot prove rejection")
	}
}
