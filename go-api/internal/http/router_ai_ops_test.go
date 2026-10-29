package httpapi

import (
	"context"
	"errors"
	"fmt"
	"forest/go-api/internal/admin"
	"forest/go-api/internal/aiops"
	"forest/go-api/internal/config"
	"forest/go-api/internal/session"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeAIOps struct {
	calls      int
	actor, id  int64
	err        error
	accessMode string
}

func (f *fakeAIOps) called(id int64) { f.calls++; f.actor = id }
func (f *fakeAIOps) GetConfig(_ context.Context, id int64) (aiops.Config, error) {
	f.called(id)
	return aiops.Config{Models: aiops.DefaultModels, HasAPIKey: true}, f.err
}
func (f *fakeAIOps) SaveConfig(_ context.Context, id int64, r aiops.ConfigSaveRequest) (aiops.Config, error) {
	f.called(id)
	return aiops.Config{Enabled: r.Enabled, BaseURL: r.BaseURL, HasAPIKey: true, Models: r.Models}, f.err
}
func (f *fakeAIOps) ListSessions(_ context.Context, id int64) ([]aiops.Session, error) {
	f.called(id)
	return []aiops.Session{{ID: 1}}, f.err
}
func (f *fakeAIOps) GetSession(_ context.Context, actor, id int64) (aiops.SessionDetail, error) {
	f.called(actor)
	f.id = id
	return aiops.SessionDetail{Session: aiops.Session{ID: id}, Messages: []aiops.Message{}, Turns: []aiops.Turn{}}, f.err
}
func (f *fakeAIOps) CreateSession(_ context.Context, actor int64, r aiops.CreateSessionRequest) (aiops.Session, error) {
	f.called(actor)
	return aiops.Session{ID: 1, Model: r.Model}, f.err
}
func (f *fakeAIOps) SendMessage(_ context.Context, actor int64, r aiops.SendMessageRequest) (aiops.SendMessageResult, error) {
	f.called(actor)
	return aiops.SendMessageResult{Session: aiops.Session{ID: r.SessionID}, Turn: aiops.Turn{ID: 1, Status: "running"}}, f.err
}
func (f *fakeAIOps) SetAccessMode(_ context.Context, actor int64, r aiops.AccessModeRequest) (aiops.Session, error) {
	f.called(actor)
	f.id = r.ID
	f.accessMode = r.Mode
	return aiops.Session{ID: r.ID, AccessMode: r.Mode, FullAccessBy: actor}, f.err
}
func (f *fakeAIOps) StopSession(_ context.Context, actor, id int64) error {
	f.called(actor)
	f.id = id
	return f.err
}
func (f *fakeAIOps) DeleteSession(_ context.Context, actor, id int64) error {
	f.called(actor)
	f.id = id
	return f.err
}
func (f *fakeAIOps) ClearSessions(_ context.Context, actor int64) error {
	f.called(actor)
	return f.err
}
func (f *fakeAIOps) ResetData(_ context.Context, actor int64) error { f.called(actor); return f.err }
func (f *fakeAIOps) ListServers(_ context.Context, actor int64) ([]aiops.Server, error) {
	f.called(actor)
	return []aiops.Server{{ID: 1, HasPassword: true}}, f.err
}
func (f *fakeAIOps) SaveServer(_ context.Context, actor int64, r aiops.ServerSaveRequest) (aiops.Server, error) {
	f.called(actor)
	return aiops.Server{ID: 1, HasPassword: r.Password != ""}, f.err
}
func (f *fakeAIOps) DeleteServer(_ context.Context, actor, id, version int64) error {
	f.called(actor)
	f.id = id
	return f.err
}
func (f *fakeAIOps) ProbeHostKey(_ context.Context, actor int64, r aiops.HostKeyRequest) (aiops.HostKeyResult, error) {
	f.called(actor)
	return aiops.HostKeyResult{Host: r.Host, Port: r.Port, Fingerprint: "SHA256:fixture"}, f.err
}
func (f *fakeAIOps) ListActions(_ context.Context, actor, id int64) ([]aiops.Action, error) {
	f.called(actor)
	return []aiops.Action{{ID: 4, SessionID: id, Status: "pending"}}, f.err
}
func (f *fakeAIOps) ApproveAction(_ context.Context, actor, sid, id int64) (aiops.Action, error) {
	f.called(actor)
	f.id = id
	return aiops.Action{ID: id, SessionID: sid, Status: "running"}, f.err
}
func (f *fakeAIOps) RejectAction(_ context.Context, actor, sid, id int64) (aiops.Action, error) {
	f.called(actor)
	f.id = id
	return aiops.Action{ID: id, SessionID: sid, Status: "rejected"}, f.err
}
func aiOpsTestRequest(t *testing.T, f *fakeAIOps, identity *session.Identity, method, action, body string) *httptest.ResponseRecorder {
	t.Helper()
	router := NewRouter(config.Config{AdminPath: "test"}, WithSessionService(strictAppleIDSession{identity: identity}), WithAIOps(f))
	r := httptest.NewRequest(method, "/api/v1/test/ai-ops/"+action, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer fixture")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	return w
}
func TestAIOpsRoutesRequireFreshOperator(t *testing.T) {
	for _, identity := range []*session.Identity{nil, {ID: 7}, {ID: 7, IsStaff: 1}, {ID: 7, IsAdmin: 1}} {
		f := &fakeAIOps{}
		w := aiOpsTestRequest(t, f, identity, "POST", "action/approve", `{"session_id":1,"id":2}`)
		if identity != nil && (identity.IsAdmin == 1 || identity.IsStaff == 1) {
			if w.Code != 202 || f.actor != 7 || f.id != 2 {
				t.Fatalf("admin %d calls%d", w.Code, f.calls)
			}
		} else if w.Code != 403 || f.calls != 0 {
			t.Fatalf("unauthorized %d calls%d", w.Code, f.calls)
		}
	}
}
func TestAIOpsRoutesExplicitDestructiveConfirmationAndBodyValidation(t *testing.T) {
	for _, tc := range []struct {
		action, body string
		want         int
	}{
		{"session/delete", `{"id":1}`, 400}, {"session/delete", `{"id":1,"confirm":true}`, 200},
		{"session/clear", `{"confirm":false}`, 400}, {"session/clear", `{"confirm":true}`, 200},
		{"reset", `{"confirm":true}`, 400}, {"reset", `{"confirm":true,"confirm_text":"RESET_AI_DATA"}`, 200},
		{"server/delete", `{"id":1,"version":2}`, 400}, {"server/delete", `{"id":1,"version":2,"confirm":true}`, 200},
		{"action/approve", `{"session_id":1,"id":2,"actor_id":99}`, 400},
		{"action/approve", `{"session_id":1,"id":2} {"id":3}`, 400},
		{"chat/send", `{"session_id":1,"model":"gpt-6.1-sol","content":"check","request_id":"fixture-001"}`, 202},
	} {
		f := &fakeAIOps{}
		w := aiOpsTestRequest(t, f, &session.Identity{ID: 7, IsAdmin: 1}, "POST", tc.action, tc.body)
		if w.Code != tc.want {
			t.Fatalf("%s got%d %s", tc.action, w.Code, w.Body.String())
		}
		if tc.want == 400 && f.calls != 0 {
			t.Fatal("invalid payload called service")
		}
	}
}
func TestAIOpsManagementSecretsErrorsAndSessionActions(t *testing.T) {
	f := &fakeAIOps{}
	identity := &session.Identity{ID: 7, IsAdmin: 1}
	w := aiOpsTestRequest(t, f, identity, "GET", "session?id=1", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"actions"`) || !strings.Contains(w.Body.String(), `"pending"`) {
		t.Fatalf("session envelope %s", w.Body.String())
	}
	w = aiOpsTestRequest(t, f, identity, "POST", "config/save", `{"enabled":true,"base_url":"https://relay.example.test/v1","api_key":"never-return-secret","models":["gpt-6.1-sol"]}`)
	if w.Code != 200 || strings.Contains(w.Body.String(), "never-return-secret") {
		t.Fatalf("config leaked%s", w.Body.String())
	}
	f.err = errors.New("postgres://password=never-return-secret")
	w = aiOpsTestRequest(t, f, identity, "GET", "config", "")
	if w.Code != 500 || strings.Contains(w.Body.String(), "never-return-secret") {
		t.Fatal("raw error leaked")
	}
	if !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
		t.Fatal("cacheable ai config")
	}
	w = aiOpsTestRequest(t, f, identity, "GET", "reset", "")
	if w.Code != 405 {
		t.Fatal("wrong method accepted")
	}
}

func TestAIOpsSplitErrorsExplainMissingCapacityAndChangedTargets(t *testing.T) {
	for _, tc := range []struct {
		err     error
		status  int
		message string
	}{
		{fmt.Errorf("%w：可用且健康的备用 IP 不足 2 个，未二分、未领取 IP", admin.ErrAIEntrySplitInvalid), 400, "备用 IP 不足 2 个"},
		{admin.ErrAIEntrySplitConflict, 409, "入口配置或名单已变化"},
	} {
		w := aiOpsTestRequest(t, &fakeAIOps{err: tc.err}, &session.Identity{ID: 7, IsStaff: 1}, "POST", "action/approve", `{"session_id":1,"id":2}`)
		if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.message) {
			t.Fatalf("split cause lost: %d %s", w.Code, w.Body.String())
		}
	}
}

func TestAIOpsSSHIntakeErrorExplainsHowToCorrectWithoutEchoingCredentials(t *testing.T) {
	for _, problem := range []error{aiops.ErrSSHIntakeInvalid, aiops.ErrSSHIntakeMultiple} {
		f := &fakeAIOps{err: fmt.Errorf("secret-password: %w", problem)}
		w := aiOpsTestRequest(t, f, &session.Identity{ID: 7, IsAdmin: 1}, "POST", "chat/send", `{"model":"gpt-6.1-sol","content":"fixture","request_id":"fixture-intake"}`)
		if w.Code != 400 || !strings.Contains(w.Body.String(), "凭据未发送给模型") || strings.Contains(w.Body.String(), "secret-password") {
			t.Fatalf("unexpected intake error: %d %s", w.Code, w.Body.String())
		}
	}
}

func TestAIOpsAccessAndStopRequireRealOperatorAndStrictPayload(t *testing.T) {
	for _, action := range []string{"session/access", "chat/stop"} {
		body := `{"id":9}`
		if action == "session/access" {
			body = `{"id":9,"mode":"full","expected_version":2}`
		}
		for _, identity := range []*session.Identity{nil, {ID: 7}, {ID: 7, IsStaff: 1}, {ID: 7, IsAdmin: 1}} {
			f := &fakeAIOps{}
			w := aiOpsTestRequest(t, f, identity, "POST", action, body)
			if identity != nil && (identity.IsAdmin == 1 || identity.IsStaff == 1) {
				if w.Code != 200 || f.actor != 7 || f.id != 9 || f.calls != 1 {
					t.Fatalf("admin request %s: %d %+v", action, w.Code, f)
				}
			} else if w.Code != 403 || f.calls != 0 {
				t.Fatalf("unauthorized %s: %d calls=%d", action, w.Code, f.calls)
			}
		}
		f := &fakeAIOps{}
		w := aiOpsTestRequest(t, f, &session.Identity{ID: 7, IsAdmin: 1}, "POST", action, strings.TrimSuffix(body, "}")+`,"actor_id":999}`)
		if w.Code != 400 || f.calls != 0 {
			t.Fatal("caller supplied identity accepted", action, w.Code)
		}
		w = aiOpsTestRequest(t, f, &session.Identity{ID: 7, IsAdmin: 1}, "GET", action, "")
		if w.Code != 405 || f.calls != 0 {
			t.Fatal("GET mutated permissions", action, w.Code)
		}
	}
}

func TestAIOpsGlobalManagementRemainsAdminOnly(t *testing.T) {
	for _, action := range []string{"config/save", "session/clear", "reset"} {
		f := &fakeAIOps{}
		w := aiOpsTestRequest(t, f, &session.Identity{ID: 7, IsStaff: 1}, "POST", action, `{"confirm":true,"confirm_text":"RESET_AI_DATA"}`)
		if w.Code != 403 || f.calls != 0 {
			t.Fatalf("staff reached global management %s: status=%d calls=%d", action, w.Code, f.calls)
		}
	}
	for _, action := range []string{"config", "sessions", "servers", "session?id=1"} {
		f := &fakeAIOps{}
		w := aiOpsTestRequest(t, f, &session.Identity{ID: 7, IsStaff: 1}, "GET", action, "")
		if w.Code != 200 || f.actor != 7 {
			t.Fatalf("staff operational route blocked %s: status=%d", action, w.Code)
		}
	}
}
