package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"forest/go-api/internal/aiops"
	"forest/go-api/internal/config"
	"forest/go-api/internal/session"
)

type streamTestOps struct {
	fakeAIOps
	finished  atomic.Bool
	revoked   atomic.Bool
	published atomic.Bool
}

func (s *streamTestOps) GetSession(_ context.Context, _ int64, id int64) (aiops.SessionDetail, error) {
	status := "running"
	draft := ""
	messages := []aiops.Message{}
	if s.finished.Load() {
		status = "idle"
	}
	if s.published.Load() || s.finished.Load() {
		messages = append(messages, aiops.Message{ID: 1, Role: "assistant", Content: "完整回复"})
	}
	turnStatus := "running"
	if status == "idle" {
		turnStatus = "completed"
	}
	return aiops.SessionDetail{Session: aiops.Session{ID: id, Status: status}, Messages: messages, Turns: []aiops.Turn{{ID: 5, SessionID: id, Status: turnStatus, DraftContent: draft, DraftRound: 1}}}, nil
}
func (s *streamTestOps) ListActions(context.Context, int64, int64) ([]aiops.Action, error) {
	return []aiops.Action{}, nil
}
func (s *streamTestOps) GetDraft(_ context.Context, _ int64, id int64) (aiops.Draft, error) {
	if s.revoked.Load() {
		return aiops.Draft{}, errors.New("postgres password=secret-never-echo")
	}
	status := "running"
	content := "正在逐步输出"
	version := int64(2)
	if s.published.Load() {
		content = ""
		version = 3
	}
	if s.finished.Load() {
		status = "completed"
		content = ""
		version = 3
	}
	return aiops.Draft{SessionID: id, TurnID: 5, Round: 1, Version: version, Content: content, Status: status}, nil
}
func TestAIHTTPStreamFlushesDraftBeforeCompletion(t *testing.T) {
	s := &streamTestOps{}
	router := NewRouter(config.Config{AdminPath: "test"}, WithAIOps(s), WithSessionService(strictAppleIDSession{identity: &session.Identity{ID: 1, IsAdmin: 1}}))
	server := httptest.NewServer(router)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/api/v1/test/ai-ops/session/stream?id=7", nil)
	req.Header.Set("Authorization", "Bearer fixture")
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 || !strings.Contains(response.Header.Get("Content-Type"), "text/event-stream") || response.Header.Get("X-Accel-Buffering") != "no" || !strings.Contains(response.Header.Get("Cache-Control"), "no-store") {
		t.Fatal("invalid SSE headers")
	}
	scan := bufio.NewScanner(response.Body)
	scan.Buffer(make([]byte, 4096), 1<<20)
	event := ""
	sawDraft, sawFinal, sawEnd := false, false, false
	for scan.Scan() {
		line := scan.Text()
		if strings.HasPrefix(line, "event: ") {
			event = strings.TrimPrefix(line, "event: ")
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		raw := strings.TrimPrefix(line, "data: ")
		switch event {
		case "draft":
			var d aiops.Draft
			if json.Unmarshal([]byte(raw), &d) != nil {
				t.Fatal("bad live draft")
			}
			if d.Content == "" {
				if !sawFinal {
					t.Fatal("draft cleared before replacement messages were sent")
				}
			} else {
				if d.Content != "正在逐步输出" || s.finished.Load() {
					t.Fatal("bad live draft")
				}
				sawDraft = true
				// Publishing messages and finishing the turn are separate commits.
				s.published.Store(true)
			}
		case "snapshot":
			var snap aiOpsSnapshot
			if json.Unmarshal([]byte(raw), &snap) != nil {
				t.Fatal("bad snapshot")
			}
			if len(snap.Messages) > 0 {
				sawFinal = true
				s.finished.Store(true)
			}
		case "end":
			sawEnd = true
		}
	}
	if err = scan.Err(); err != nil || !sawDraft || !sawFinal || !sawEnd {
		t.Fatalf("stream incomplete draft%v final%v end%v err%v", sawDraft, sawFinal, sawEnd, err)
	}
}
func TestAIHTTPStreamErrorsDoNotLeakAndUnauthorisedCannotSubscribe(t *testing.T) {
	for _, identity := range []*session.Identity{nil, {ID: 1}} {
		s := &streamTestOps{}
		r := NewRouter(config.Config{AdminPath: "test"}, WithAIOps(s), WithSessionService(strictAppleIDSession{identity: identity}))
		w := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/v1/test/ai-ops/session/stream?id=1", nil)
		req.Header.Set("Authorization", "fixture")
		r.ServeHTTP(w, req)
		if w.Code != 403 {
			t.Fatal("unauthorised SSE accepted")
		}
	}
	s := &streamTestOps{}
	s.revoked.Store(true)
	r := NewRouter(config.Config{AdminPath: "test"}, WithAIOps(s), WithSessionService(strictAppleIDSession{identity: &session.Identity{ID: 1, IsAdmin: 1}}))
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/test/ai-ops/session/stream?id=1", nil)
	req.Header.Set("Authorization", "fixture")
	r.ServeHTTP(w, req)
	if !strings.Contains(w.Body.String(), "event: error") || strings.Contains(w.Body.String(), "secret-never-echo") {
		t.Fatal("unsafe streamed error")
	}
}

type freshAIStaffSession struct {
	strictAppleIDSession
	revoked        atomic.Bool
	adminRequested atomic.Bool
	freshCalls     atomic.Int32
}

func (s *freshAIStaffSession) AuthenticateFresh(_ context.Context, _ string, requireAdmin bool) (*session.Identity, error) {
	s.freshCalls.Add(1)
	if requireAdmin {
		s.adminRequested.Store(true)
	}
	if s.revoked.Load() {
		return &session.Identity{ID: 7}, nil
	}
	return &session.Identity{ID: 7, IsStaff: 1}, nil
}

func TestAIHTTPStaffStreamRechecksFreshIdentityAndStopsOnRevocation(t *testing.T) {
	ops := &streamTestOps{}
	auth := &freshAIStaffSession{}
	router := NewRouter(config.Config{AdminPath: "test"}, WithAIOps(ops), WithSessionService(auth))
	server := httptest.NewServer(router)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/api/v1/test/ai-ops/session/stream?id=7", nil)
	req.Header.Set("Authorization", "Bearer staff-fixture")
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal("staff cannot open event stream", response.StatusCode)
	}
	scan := bufio.NewScanner(response.Body)
	sawSnapshot, sawError := false, false
	for scan.Scan() {
		line := scan.Text()
		if line == "event: snapshot" {
			sawSnapshot = true
			auth.revoked.Store(true)
		}
		if line == "event: error" {
			sawError = true
		}
	}
	if scan.Err() != nil || !sawSnapshot || !sawError || auth.freshCalls.Load() < 2 || auth.adminRequested.Load() {
		t.Fatalf("staff SSE auth did not revalidate: initial=%v error=%v calls=%d adminOnly=%v err=%v", sawSnapshot, sawError, auth.freshCalls.Load(), auth.adminRequested.Load(), scan.Err())
	}
}
