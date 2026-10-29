package aiops

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestReasoningSettingsValidationAndLegacyRequestHash(t *testing.T) {
	for _, value := range []string{"auto", "none", "minimal", "low", "medium", "high", "xhigh"} {
		if !validReasoningEffort(value) || !validReasoningSelection(value) {
			t.Fatal(value)
		}
	}
	for _, value := range []string{"", "default", "max", "High", " high", "ultra"} {
		if validReasoningEffort(value) {
			t.Fatal(value)
		}
	}
	for _, values := range []map[string]string{{"unknown": "high"}, {"model": "invalid"}, {"model": "default"}} {
		if _, err := normalizeReasoningEfforts([]string{"model"}, values); !errors.Is(err, ErrValidation) {
			t.Fatal(values, err)
		}
	}
	defaults, err := normalizeReasoningEfforts([]string{"model"}, nil)
	if err != nil || defaults["model"] != "auto" {
		t.Fatal(defaults, err)
	}
	request := SendMessageRequest{AccessVersion: nil, SessionID: 12, Model: "model", Content: "fixture", RequestID: "request-legacy-123"}
	legacy := struct {
		AccessVersion *int64 `json:"access_version,omitempty"`
		AccessMode    string `json:"access_mode,omitempty"`
		SessionID     int64  `json:"session_id,omitempty"`
		Model         string `json:"model"`
		Content       string `json:"content"`
		RequestID     string `json:"request_id"`
	}{SessionID: 12, Model: "model", Content: "fixture", RequestID: "request-legacy-123"}
	raw, _ := json.Marshal(legacy)
	sum := sha256.Sum256(raw)
	if requestHash(request) != hex.EncodeToString(sum[:]) {
		t.Fatal("omitted reasoning changed legacy retry hash")
	}
	request.ReasoningEffort = "auto"
	if requestHash(request) == hex.EncodeToString(sum[:]) {
		t.Fatal("explicit selection absent from retry hash")
	}
}

func TestReasoningProviderRejectionIsActionableAndDoesNotEcho(t *testing.T) {
	for _, body := range []string{`{"error":{"code":"unsupported_parameter","param":"reasoning_effort","message":"secret-echo"}}`, `{"error":{"message":"model does not support reasoning_effort xhigh secret-echo"}}`} {
		err := providerResponseError(400, strings.NewReader(body))
		if err == nil || !strings.Contains(err.Error(), "中转默认（auto）") || strings.Contains(err.Error(), "secret-echo") {
			t.Fatal(err)
		}
	}
}
