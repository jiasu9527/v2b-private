package httpapi

import (
	"encoding/json"
	"strings"
	"testing"

	"forest/go-api/internal/session"
)

func TestAIOpsModelEndpointsConfigHTTP(t *testing.T) {
	body := `{"enabled":true,"base_url":"","models":["gpt-6.1-sol","deepseek-v4.1-flash"],"model_endpoints":{"gpt-6.1-sol":{"base_url":"https://first.example.test/v1","api_key":"fixture-first-only"},"deepseek-v4.1-flash":{"base_url":"https://second.example.test/v1","api_key":"fixture-second-only"}}}`
	f := &fakeAIOps{}
	w := aiOpsTestRequest(t, f, &session.Identity{ID: 7, IsAdmin: 1}, "POST", "config/save", body)
	if w.Code != 200 || f.actor != 7 || f.calls != 1 {
		t.Fatalf("config route rejected per-model endpoints: %d %s", w.Code, w.Body.String())
	}
	raw, err := json.Marshal(f.configRequest)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		ModelEndpoints map[string]struct {
			BaseURL string `json:"base_url"`
			APIKey  string `json:"api_key"`
		} `json:"model_endpoints"`
	}
	if json.Unmarshal(raw, &got) != nil || len(got.ModelEndpoints) != 2 || got.ModelEndpoints["gpt-6.1-sol"].APIKey != "fixture-first-only" || got.ModelEndpoints["deepseek-v4.1-flash"].BaseURL != "https://second.example.test/v1" {
		t.Fatal("model endpoint configuration was lost or mixed before service dispatch")
	}
	if strings.Contains(w.Body.String(), "fixture-first-only") || strings.Contains(w.Body.String(), "fixture-second-only") || !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
		t.Fatal("unsafe configuration response")
	}
	f = &fakeAIOps{}
	w = aiOpsTestRequest(t, f, &session.Identity{ID: 8, IsStaff: 1}, "POST", "config/save", body)
	if w.Code != 403 || f.calls != 0 {
		t.Fatal("staff reached model endpoint configuration")
	}
}

func TestAIOpsModelEndpointsHTTPPreservesOmittedVersusEmpty(t *testing.T) {
	for _, tc := range []struct {
		body    string
		present bool
	}{
		{`{"models":["gpt-6.1-sol"]}`, false},
		{`{"models":["gpt-6.1-sol"],"model_endpoints":{}}`, true},
	} {
		f := &fakeAIOps{}
		w := aiOpsTestRequest(t, f, &session.Identity{ID: 7, IsAdmin: 1}, "POST", "config/save", tc.body)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		raw, _ := json.Marshal(f.configRequest)
		var got map[string]json.RawMessage
		if json.Unmarshal(raw, &got) != nil {
			t.Fatal("invalid config payload")
		}
		value, exists := got["model_endpoints"]
		if exists != tc.present || exists && string(value) != "{}" {
			t.Fatal("omitted overrides and explicit reset must remain distinct")
		}
	}
}

func TestAIOpsModelEndpointsHTTPRejectsMalformedFields(t *testing.T) {
	for _, fragment := range []string{
		`null`,
		`[]`,
		`{"gpt-6.1-sol":null}`,
		`{"gpt-6.1-sol":{"base_url":"https://a.example.test/v1"},"gpt-6.1-sol":{"base_url":"https://b.example.test/v1"}}`,
		`{"gpt-6.1-sol":{"base_url":1}}`,
		`{"gpt-6.1-sol":{"api_key":["fixture"]}}`,
		`{"gpt-6.1-sol":{"base_url":"https://example.test/v1","api_key_cipher":"injected"}}`,
		`{"gpt-6.1-sol":{"clear_api_key":"true"}}`,
	} {
		f := &fakeAIOps{}
		w := aiOpsTestRequest(t, f, &session.Identity{ID: 7, IsAdmin: 1}, "POST", "config/save", `{"models":["gpt-6.1-sol"],"model_endpoints":`+fragment+`}`)
		if w.Code != 400 || f.calls != 0 {
			t.Fatal("malformed endpoint reached service", w.Code, fragment)
		}
	}
}
