package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"forest/go-api/internal/aiops"
	"forest/go-api/internal/config"
	"forest/go-api/internal/session"
)

type batchPasteAIOps struct {
	fakeAIOps
	content string
}

func (f *batchPasteAIOps) SendMessage(ctx context.Context, id int64, req aiops.SendMessageRequest) (aiops.SendMessageResult, error) {
	f.content = req.Content
	return f.fakeAIOps.SendMessage(ctx, id, req)
}

type batchPasteMini struct {
	fakeMiniBridge
	content string
}

func (f *batchPasteMini) MiniSendMessage(ctx context.Context, token string, req aiops.SendMessageRequest) (aiops.SendMessageResult, error) {
	f.content = req.Content
	return f.fakeMiniBridge.MiniSendMessage(ctx, token, req)
}

func TestAIOpsWebAndMiniAcceptBatchPasteWithoutTruncation(t *testing.T) {
	content := strings.Repeat("待整理的服务器资料\n", 3000) + "  password-tail  "
	body, _ := json.Marshal(aiops.SendMessageRequest{Content: content, Model: "fixture", RequestID: "batch-paste-fixture"})
	for _, mini := range []bool{false, true} {
		base := &batchPasteAIOps{}
		bridge := &batchPasteMini{fakeMiniBridge: fakeMiniBridge{identity: aiops.MiniIdentity{ID: 9, IsStaff: 1}}}
		router := NewRouter(config.Config{AdminPath: "test"}, WithAIOps(base), WithAIOpsTelegram(bridge), WithSessionService(strictAppleIDSession{identity: &session.Identity{ID: 9, IsAdmin: 1}}))
		path, token := "/api/v1/test/ai-ops/chat/send", "fixture"
		if mini {
			path, token = "/api/v1/ai-ops-mini/chat/send", "tma_fixture"
		}
		r := httptest.NewRequest("POST", path, strings.NewReader(string(body)))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		got := base.content
		if mini {
			got = bridge.content
		}
		if w.Code != 202 || got != content {
			t.Fatalf("mini=%v status=%d length=%d expected=%d", mini, w.Code, len(got), len(content))
		}
	}
}

func TestAIOpsChatDecoderRemainsStrictAndBounded(t *testing.T) {
	for _, tc := range []struct {
		name, body, contentType string
		want                    int
	}{
		{"escaped content", `{"content":"` + strings.Repeat(`\u0061`, 128<<10) + `"}`, "application/json", 200},
		{"unknown field", `{"content":"task","password":"raw"}`, "application/json", 400},
		{"trailing value", `{"content":"task"}{}`, "application/json", 400},
		{"body cap", `{"content":"` + strings.Repeat("x", 1<<20) + `"}`, "application/json", 400},
		{"type", `{}`, "text/plain", 415},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var request aiops.SendMessageRequest
			r := httptest.NewRequest("POST", "/", strings.NewReader(tc.body))
			r.Header.Set("Content-Type", tc.contentType)
			w := httptest.NewRecorder()
			ok := decodeAIOpsChatJSON(w, r, &request)
			if ok != (tc.want == 200) || w.Code != tc.want {
				t.Fatal("unexpected decoding result", ok, w.Code)
			}
		})
	}
	// The unrelated support JSON decoder must retain its smaller bound.
	var req aiops.SendMessageRequest
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"content":"`+strings.Repeat("x", 40<<10)+`"}`))
	r.Header.Set("Content-Type", "application/json")
	if decodeSupportJSON(httptest.NewRecorder(), r, &req) {
		t.Fatal("general support decoder was widened")
	}
}

func TestAIOpsSSHIntakeErrorKeepsSafeRecordIndex(t *testing.T) {
	w := httptest.NewRecorder()
	handleAIOpsError(w, fmt.Errorf("private-wrapper-fixture: %w", aiops.ErrUnavailable))
	if w.Code != 503 || strings.Contains(w.Body.String(), "fixture") {
		t.Fatal("unavailable error leaked wrapper", w.Body.String())
	}
	_, _, err := aiops.ParseSSHRegistrationInput("192.0.2.10 root first-fixture-password\n\nhost: 192.0.2.20\npassword: second-fixture-password")
	if err == nil {
		t.Fatal("incomplete second record accepted")
	}
	w = httptest.NewRecorder()
	handleAIOpsError(w, fmt.Errorf("private-wrapper-fixture: %w", err))
	if w.Code != 400 || !strings.Contains(w.Body.String(), "第 2 条") || strings.Contains(w.Body.String(), "fixture") {
		t.Fatal("public error lost index or echoed raw input/wrapper", w.Body.String())
	}
	var content strings.Builder
	for i := 1; i <= 51; i++ {
		fmt.Fprintf(&content, "192.0.2.%d root fixture-password-%d\n", i, i)
	}
	_, _, err = aiops.ParseSSHRegistrationInput(content.String())
	w = httptest.NewRecorder()
	handleAIOpsError(w, err)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "50") || strings.Contains(w.Body.String(), "fixture") {
		t.Fatal("batch limit not explained safely", w.Body.String())
	}
}
