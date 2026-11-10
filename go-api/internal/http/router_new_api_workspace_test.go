package httpapi

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"forest/go-api/internal/config"
	"forest/go-api/internal/newapibridge"
	"forest/go-api/internal/passport"
	"forest/go-api/internal/session"
)

var (
	_ newAPIWorkspaceAccountService  = (*newapibridge.Service)(nil)
	_ newAPIWorkspaceChatService     = (*newapibridge.Service)(nil)
	_ newAPIWorkspacePasswordService = (*newapibridge.Service)(nil)
	_ newAPIPasswordVerifier         = (*passport.DBService)(nil)
)

type workspaceHTTPFixture struct {
	newAPIHTTPFixture
	newAPIWorkspaceAccountService
	newAPIWorkspaceChatService
	newAPIWorkspacePasswordService
	quote  string
	stream func(context.Context, func(string, any) error) error
}

func (s *workspaceHTTPFixture) WorkspaceAccount(_ context.Context, id int64, auth string) (map[string]any, error) {
	s.actor, s.issuedAuth = id, auth
	s.calls++
	return map[string]any{"connected": true, "quota": 3000}, nil
}
func (s *workspaceHTTPFixture) ConfirmWorkspaceTransfer(_ context.Context, id int64, auth, quote string) (newapibridge.Transfer, error) {
	s.actor, s.issuedAuth, s.quote = id, auth, quote
	s.calls++
	return newapibridge.Transfer{ID: 1, UserID: id, Status: "pending"}, nil
}
func (s *workspaceHTTPFixture) GetConversation(_ context.Context, id, conversation int64) (newapibridge.ConversationDetail, error) {
	s.actor = id
	s.calls++
	if conversation != 8 {
		return newapibridge.ConversationDetail{}, newapibridge.ErrForbidden
	}
	return newapibridge.ConversationDetail{Conversation: newapibridge.Conversation{ID: 8}}, nil
}
func (s *workspaceHTTPFixture) RevealWorkspacePassword(_ context.Context, id int64, auth string) (map[string]any, error) {
	s.actor, s.issuedAuth = id, auth
	s.calls++
	return map[string]any{"password": "test-generated-password"}, nil
}
func (s *workspaceHTTPFixture) SendChat(ctx context.Context, id int64, auth string, req newapibridge.ChatSendRequest, emit func(string, any) error) error {
	return s.stream(ctx, emit)
}
func (s *workspaceHTTPFixture) GetChatRequest(_ context.Context, id int64, requestID string) (newapibridge.ChatRequestState, error) {
	s.actor = id
	s.quote = requestID
	s.calls++
	return newapibridge.ChatRequestState{Found: true}, nil
}

type workspacePasswordVerifier struct {
	passport.Service
	calls int
	actor int64
	err   error
}

func (s *workspacePasswordVerifier) VerifyCurrentPassword(_ context.Context, id int64, password string) error {
	s.calls++
	s.actor = id
	return s.err
}
func workspaceTestRouter(s *workspaceHTTPFixture, v *workspacePasswordVerifier) (http.Handler, *newAPIFreshSession) {
	sessions := &newAPIFreshSession{identity: &session.Identity{ID: 7007}}
	return NewRouter(config.Config{AdminPath: "localadmin"}, WithSessionService(sessions), WithNewAPIBridge(s), WithPassportService(v)), sessions
}

func TestWorkspaceHTTPIdentityAndStrictInputs(t *testing.T) {
	s := &workspaceHTTPFixture{}
	r, sessions := workspaceTestRouter(s, &workspacePasswordVerifier{})
	base := "/api/v1/user/new-api/"
	w := newAPIHTTPCall(t, r, "GET", base+"account", "", "panel-auth")
	if w.Code != 200 || s.actor != 7007 || s.issuedAuth != "panel-auth" || sessions.freshCalls != 1 {
		t.Fatal(w.Code, w.Body.String(), s.actor)
	}
	before := s.calls
	for _, tt := range []struct{ method, path, body, token string }{
		{"GET", "account", "", "nap_valid"},
		{"GET", "account?auth_data=panel-auth", "", ""},
		{"GET", "account?user_id=4", "", "panel-auth"},
		{"GET", "account?ignored=1", "", "panel-auth"},
		{"GET", "conversations/detail?id=8&id=9", "", "panel-auth"},
		{"GET", "conversations/detail?id=-1", "", "panel-auth"},
		{"GET", "conversations/detail?id=8&user_id=9", "", "panel-auth"},
		{"POST", "transfer/confirm", `{"quote_id":"q","user_id":9}`, "panel-auth"},
		{"POST", "transfer/confirm", `{"quote_id":"q","amount_fen":1}`, "panel-auth"},
		{"POST", "transfer/confirm?quote_id=other", `{"quote_id":"q"}`, "panel-auth"},
		{"POST", "transfer/confirm", `{"quote_id":"q"} {}`, "panel-auth"},
		{"POST", "transfer/confirm", `null`, "panel-auth"},
		{"POST", "transfer/confirm", `[]`, "panel-auth"},
		{"GET", "transfer/confirm", "", "panel-auth"},
	} {
		w = newAPIHTTPCall(t, r, tt.method, base+tt.path, tt.body, tt.token)
		if w.Code < 400 || s.calls != before {
			t.Fatalf("%s: status=%d calls=%d %s", tt.path, w.Code, s.calls, w.Body.String())
		}
	}
	w = newAPIHTTPCall(t, r, "POST", base+"transfer/confirm", `{"quote_id":"server-held-quote"}`, "panel-auth")
	if w.Code != 200 || s.quote != "server-held-quote" || s.actor != 7007 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = newAPIHTTPCall(t, r, "GET", base+"conversations/detail?id=8", "", "panel-auth")
	if w.Code != 200 || s.actor != 7007 {
		t.Fatal(w.Code, w.Body.String())
	}
	sessions.revoked = true
	before = s.calls
	w = newAPIHTTPCall(t, r, "GET", base+"account", "", "panel-auth")
	if w.Code < 400 || s.calls != before {
		t.Fatal("revoked session accepted")
	}
}

func TestWorkspaceHTTPPasswordReauthBeforeRemote(t *testing.T) {
	s := &workspaceHTTPFixture{}
	v := &workspacePasswordVerifier{err: passport.NewHTTPError(403, "本站密码验证未通过")}
	r, _ := workspaceTestRouter(s, v)
	path := "/api/v1/user/new-api/password/reveal"
	w := newAPIHTTPCall(t, r, "POST", path, `{"panel_password":"wrong-password"}`, "panel-auth")
	if w.Code != 403 || v.calls != 1 || s.calls != 0 {
		t.Fatal(w.Code, w.Body.String())
	}
	v.err = nil
	w = newAPIHTTPCall(t, r, "POST", path, `{"panel_password":"correct-password","user_id":8}`, "panel-auth")
	if w.Code != 400 || v.calls != 1 || s.calls != 0 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = newAPIHTTPCall(t, r, "POST", path, `{"panel_password":"correct-password"}`, "panel-auth")
	if w.Code != 200 || v.actor != 7007 || s.actor != 7007 || s.calls != 1 || !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
		t.Fatal(w.Code, w.Header(), w.Body.String())
	}
}

func TestWorkspaceHTTPChatReceiptIsReadOnlyAndOwnerScoped(t *testing.T) {
	s := &workspaceHTTPFixture{}
	r, _ := workspaceTestRouter(s, &workspacePasswordVerifier{})
	path := "/api/v1/user/new-api/chat/request?client_request_id=lost-meta-123"
	w := newAPIHTTPCall(t, r, "GET", path, "", "panel-auth")
	if w.Code != 200 || s.actor != 7007 || s.quote != "lost-meta-123" || s.calls != 1 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = newAPIHTTPCall(t, r, "GET", path+"&user_id=9", "", "panel-auth")
	if w.Code != 400 || s.calls != 1 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = newAPIHTTPCall(t, r, "GET", path, "", "nap_valid")
	if w.Code != 403 || s.calls != 1 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestWorkspaceHTTPStreamTerminalEventsAndErrorPrivacy(t *testing.T) {
	for _, tc := range []struct {
		name        string
		stream      func(context.Context, func(string, any) error) error
		status      int
		errorEvents int
		tail        string
	}{
		{"preflight", func(_ context.Context, _ func(string, any) error) error { return errors.New("REMOTE_SECRET") }, 503, 0, ""},
		{"reported", func(_ context.Context, emit func(string, any) error) error {
			_ = emit("meta", map[string]any{"conversation_id": 1})
			_ = emit("error", map[string]any{"message": "已保留片段"})
			_ = emit("done", map[string]any{"status": "partial"})
			return newapibridge.ErrChatInterrupted
		}, 200, 1, "event: done"},
		{"unreported", func(_ context.Context, emit func(string, any) error) error {
			_ = emit("delta", map[string]any{"text": "data: safe\n\nevent: secret"})
			return errors.New("REMOTE_SECRET")
		}, 200, 1, "event: error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &workspaceHTTPFixture{stream: tc.stream}
			r, _ := workspaceTestRouter(s, &workspacePasswordVerifier{})
			w := newAPIHTTPCall(t, r, "POST", "/api/v1/user/new-api/chat/send", `{"conversation_id":1,"client_request_id":"test-http-request","content":"你好"}`, "panel-auth")
			body := w.Body.String()
			if w.Code != tc.status || strings.Contains(body, "REMOTE_SECRET") || strings.Count(body, "event: error\n") != tc.errorEvents || strings.Contains(body, "\nevent: secret") {
				t.Fatal(w.Code, body)
			}
			if tc.tail != "" && strings.LastIndex(body, tc.tail) != strings.LastIndex(body, "event: ") {
				t.Fatal("terminal event order", body)
			}
		})
	}
}

func TestWorkspaceHTTPStreamingFlushAndDisconnect(t *testing.T) {
	canceled := make(chan struct{})
	s := &workspaceHTTPFixture{stream: func(ctx context.Context, emit func(string, any) error) error {
		if e := emit("meta", map[string]any{"conversation_id": 1}); e != nil {
			return e
		}
		<-ctx.Done()
		close(canceled)
		return ctx.Err()
	}}
	r, _ := workspaceTestRouter(s, &workspacePasswordVerifier{})
	server := httptest.NewServer(r)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", server.URL+"/api/v1/user/new-api/chat/send", strings.NewReader(`{"conversation_id":1,"client_request_id":"test-stream-request","content":"你好"}`))
	req.Header.Set("Authorization", "Bearer panel-auth")
	req.Header.Set("Content-Type", "application/json")
	res, e := server.Client().Do(req)
	if e != nil {
		t.Fatal(e)
	}
	reader := bufio.NewReader(res.Body)
	line, e := reader.ReadString('\n')
	if e != nil || line != "event: meta\n" || res.Header.Get("X-Accel-Buffering") != "no" {
		t.Fatal(line, e, res.Header)
	}
	res.Body.Close()
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("client disconnect did not cancel upstream")
	}
}

func TestWorkspaceHTTPStreamOverridesShortServerWriteDeadline(t *testing.T) {
	s := &workspaceHTTPFixture{stream: func(ctx context.Context, emit func(string, any) error) error {
		select {
		case <-time.After(100 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
		return emit("done", map[string]any{"status": "complete"})
	}}
	r, _ := workspaceTestRouter(s, &workspacePasswordVerifier{})
	server := httptest.NewUnstartedServer(r)
	server.Config.WriteTimeout = 20 * time.Millisecond
	server.Start()
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", server.URL+"/api/v1/user/new-api/chat/send", strings.NewReader(`{"conversation_id":1,"client_request_id":"slow-first-frame","content":"hi"}`))
	req.Header.Set("Authorization", "Bearer panel-auth")
	req.Header.Set("Content-Type", "application/json")
	res, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil || !strings.Contains(string(body), "event: done") {
		t.Fatal(string(body), err)
	}
}

func TestWorkspaceHTTPRejectsImportBeforeService(t *testing.T) {
	s := &workspaceHTTPFixture{}
	r, _ := workspaceTestRouter(s, &workspacePasswordVerifier{})
	for _, body := range []string{`{"encoding":"base64","payload":"%bad"}`, `{"encoding":"url","payload":"https://example.test/private"}`} {
		w := newAPIHTTPCall(t, r, "POST", "/api/v1/user/new-api/characters/import", body, "panel-auth")
		if w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	req := httptest.NewRequest("POST", "/api/v1/user/new-api/characters/import", io.NopCloser(strings.NewReader(`{}`)))
	req.Header.Set("Authorization", "Bearer panel-auth")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 415 {
		t.Fatal(w.Code, w.Body.String())
	}
}
