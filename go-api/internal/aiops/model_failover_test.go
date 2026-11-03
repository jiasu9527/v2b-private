package aiops

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestModelFailoverOrderAndCredentials(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		enabled bool
		want    []string
	}{
		{"fault", 401, true, []string{"first", "second", "third"}},
		{"parameter", 400, true, []string{"first"}},
		{"disabled", 401, false, []string{"first"}},
		{"success", 200, true, []string{"first"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := failoverTestService(t)
			cfg := failoverTestConfig()
			cfg.FailoverEnabled = tc.enabled
			cfg.Models = []string{"first", "second", "third"}
			cfg.ReadyModels = cfg.Models
			cfg.FallbackModels = []string{"first", "second", "third"}
			cfg.ReasoningEfforts = map[string]string{"second": "auto", "third": "low"}
			cfg.modelEndpoints = map[string]modelEndpoint{}
			var seen []string
			messages := []providerMessage{{Role: "user", Content: "继续已有操作，不要重新执行"}}
			for _, model := range cfg.Models {
				model := model
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var req completionRequest
					if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
						t.Error(err)
					}
					seen = append(seen, req.Model)
					if req.Model != model || r.Header.Get("Authorization") != "Bearer key-"+model || !reflect.DeepEqual(req.Messages, messages) {
						t.Error("model credential or context mismatch")
					}
					if model == "third" && req.ReasoningEffort != "low" {
						t.Error("backup reasoning not applied")
					}
					if model == "first" && tc.status != 200 {
						w.WriteHeader(tc.status)
						fmt.Fprint(w, `{"error":{"message":"invalid parameter"}}`)
						return
					}
					if model == "second" {
						w.WriteHeader(403)
						return
					}
					fmt.Fprint(w, failoverTestReply)
				}))
				defer server.Close()
				ep := failoverTestEndpoint(t, s, model, server.URL, "key-"+model)
				cfg.modelEndpoints[model] = modelEndpoint{BaseURL: ep.BaseURL, APIKeyCipher: ep.APIKeyCipher}
			}
			out, err := s.completeWithModelFailover(context.Background(), cfg, "", nil, "first", completionRequest{Model: "first", Messages: messages, ReasoningEffort: "high"}, nil)
			if !reflect.DeepEqual(seen, tc.want) {
				t.Fatalf("order %v want %v", seen, tc.want)
			}
			if len(tc.want) == 3 {
				if err != nil || out.actualModel != "third" || out.actualReasoning != "low" || !strings.Contains(out.Content, "灾备已从 first 切换至 third") {
					t.Fatalf("invalid result %+v %v", out, err)
				}
			} else if tc.status == 200 {
				if err != nil || out.actualModel != "" {
					t.Fatalf("primary failed %v", err)
				}
			} else if err == nil {
				t.Fatal("expected failure")
			}
		})
	}
}

func TestModelFailoverValidation(t *testing.T) {
	for _, values := range [][]string{{"unknown"}, {"a", "a"}} {
		if _, err := normalizeFallbackModels(values, []string{"a", "b"}); err == nil {
			t.Fatal("accepted invalid fallback models")
		}
	}
	values, err := normalizeFallbackModels([]string{"b", "a"}, []string{"a", "b"})
	if err != nil || !reflect.DeepEqual(values, []string{"b", "a"}) {
		t.Fatal("lost priority")
	}
	var req ConfigSaveRequest
	if json.Unmarshal([]byte(`{"fallback_models":null}`), &req) == nil {
		t.Fatal("accepted null")
	}
	if err := json.Unmarshal([]byte(`{"fallback_models":[]}`), &req); err != nil || req.FallbackModels == nil {
		t.Fatal("explicit clear lost")
	}
}

func TestModelFailoverPartialToolsAreDiscarded(t *testing.T) {
	s := failoverTestService(t)
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, streamFrame(map[string]any{"content": "失败草稿", "tool_calls": []any{map[string]any{"index": 0, "id": "never_execute", "type": "function", "function": map[string]any{"name": "knowledge_create", "arguments": `{"title":"`}}}}, nil))
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, failoverTestReply) }))
	defer second.Close()
	a := failoverTestEndpoint(t, s, "a", first.URL, "key-a")
	b := failoverTestEndpoint(t, s, "b", second.URL, "key-b")
	cfg := failoverTestConfig()
	cfg.Models = []string{"a", "b"}
	cfg.ReadyModels = cfg.Models
	cfg.FallbackModels = []string{"b"}
	cfg.modelEndpoints = map[string]modelEndpoint{"a": {BaseURL: a.BaseURL, APIKeyCipher: a.APIKeyCipher}, "b": {BaseURL: b.BaseURL, APIKeyCipher: b.APIKeyCipher}}
	var drafts []string
	out, err := s.completeWithModelFailover(context.Background(), cfg, "", nil, "a", completionRequest{Model: "a"}, func(value string) error { drafts = append(drafts, value); return nil })
	if err != nil || len(out.ToolCalls) != 0 || strings.Contains(out.Content, "失败草稿") || out.actualModel != "b" {
		t.Fatalf("partial tool leaked %+v %v", out, err)
	}
	cleared := false
	for _, draft := range drafts {
		if draft == "" {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("failed draft not cleared")
	}
}

func TestModelFailoverTriesSameModelRelayBeforeDifferentModel(t *testing.T) {
	s := failoverTestService(t)
	var seen []string
	endpoints := make([]RelayEndpoint, 3)
	for i := range endpoints {
		i := i
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req completionRequest
			json.NewDecoder(r.Body).Decode(&req)
			seen = append(seen, fmt.Sprintf("%d:%s", i, req.Model))
			if i < 2 {
				w.WriteHeader(401)
				return
			}
			fmt.Fprint(w, failoverTestReply)
		}))
		defer server.Close()
		endpoints[i] = failoverTestEndpoint(t, s, fmt.Sprint(i), server.URL, "key")
	}
	cfg := failoverTestConfig()
	cfg.BaseURL = endpoints[0].BaseURL
	cfg.Models = []string{"a", "b"}
	cfg.ReadyModels = cfg.Models
	cfg.FallbackModels = []string{"b"}
	cfg.modelEndpoints = map[string]modelEndpoint{"b": {BaseURL: endpoints[2].BaseURL, APIKeyCipher: endpoints[2].APIKeyCipher}}
	endpoints[1].Models = []string{"a"}
	out, err := s.completeWithModelFailover(context.Background(), cfg, endpoints[0].APIKeyCipher, []RelayEndpoint{endpoints[1]}, "a", completionRequest{Model: "a"}, nil)
	if err != nil || out.actualModel != "b" || !reflect.DeepEqual(seen, []string{"0:a", "1:a", "2:b"}) {
		t.Fatal("incorrect fallback sequence", seen, err)
	}
	// Both original relays are cooling down: skip them rather than waiting 60 seconds.
	seen = nil
	out, err = s.completeWithModelFailover(context.Background(), cfg, endpoints[0].APIKeyCipher, []RelayEndpoint{endpoints[1]}, "a", completionRequest{Model: "a"}, nil)
	if err != nil || out.actualModel != "b" || !reflect.DeepEqual(seen, []string{"2:b"}) {
		t.Fatal("cooldown prevented model recovery", seen, err)
	}
}
