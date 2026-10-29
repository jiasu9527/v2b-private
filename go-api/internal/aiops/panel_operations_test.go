package aiops

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
)

type fixturePanelOperations struct {
	mu             sync.Mutex
	ops            []PanelOperation
	validations    int
	executions     int
	lastActor      PanelOperationActor
	lastParameters json.RawMessage
	data           json.RawMessage
	onValidate     func(PanelOperationActor, string)
	onExecute      func(context.Context) error
}

func (p *fixturePanelOperations) Operations() []PanelOperation {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]PanelOperation(nil), p.ops...)
}
func (p *fixturePanelOperations) Validate(_ context.Context, actor PanelOperationActor, name string, parameters json.RawMessage) error {
	p.mu.Lock()
	p.validations++
	hook := p.onValidate
	p.mu.Unlock()
	if hook != nil {
		hook(actor, name)
	}
	if _, err := findPanelOperation(p, name); err != nil {
		return err
	}
	var values map[string]any
	if json.Unmarshal(parameters, &values) != nil || values == nil {
		return ErrValidation
	}
	if _, bad := values["unknown"]; bad {
		return ErrValidation
	}
	return nil
}
func (p *fixturePanelOperations) Execute(ctx context.Context, actor PanelOperationActor, name string, parameters json.RawMessage) (json.RawMessage, error) {
	p.mu.Lock()
	p.executions++
	p.lastActor = actor
	p.lastParameters = append(json.RawMessage(nil), parameters...)
	hook := p.onExecute
	data := append(json.RawMessage(nil), p.data...)
	p.mu.Unlock()
	if hook != nil {
		if err := hook(ctx); err != nil {
			return nil, err
		}
	}
	return data, nil
}
func (p *fixturePanelOperations) count() int { p.mu.Lock(); defer p.mu.Unlock(); return p.executions }

func fixtureOperation(name, path string, mutating, adminOnly bool) PanelOperation {
	method := "GET"
	if mutating {
		method = "POST"
	}
	return PanelOperation{Name: name, Category: "notices", Description: "fixture operation", Method: method, Path: path, Mutating: mutating, AdminOnly: adminOnly, Parameters: json.RawMessage(`{"type":"object","properties":{"value":{"type":"string"},"api_key":{"type":"string"}},"additionalProperties":false}`)}
}

func TestAIPanelOperationCatalogBoundaryAndRedaction(t *testing.T) {
	valid := fixtureOperation("notices.list", "/notice/fetch", false, false)
	for _, path := range []string{"/payment/fetch", "/payments/save", "/ai-ops/config", "/notice/../payment/save", "https://fixture.test/payment", "//payment/fetch", "/pa%79ment/fetch", "/notice/fetch?url=/payment"} {
		op := valid
		op.Path = path
		if panelOperationPermitted(op) {
			t.Fatal("unsafe descriptor allowed", path)
		}
	}
	if !panelOperationPermitted(valid) {
		t.Fatal("non-payment read denied")
	}
	adminOnly := fixtureOperation("config.save", "/config/save", true, true)
	runtime := &fixturePanelOperations{ops: []PanelOperation{valid, adminOnly, fixtureOperation("payments.list", "/payment/fetch", false, false)}}
	if len(sortedPanelOperations(runtime, PanelOperationActor{ID: 1}, "", "")) != 1 {
		t.Fatal("staff catalog disclosed protected operations")
	}
	if _, err := findPanelOperation(runtime, "unknown.operation"); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	if _, err := normalizedPanelParameters(json.RawMessage(`[]`)); !errors.Is(err, ErrValidation) {
		t.Fatal(err)
	}
	secret := "quote\"slash\\secret"
	args, _ := json.Marshal(map[string]any{"operation": "config.save", "parameters": map[string]any{"api_key": secret, "password_limit_enable": true, "token_configured": true, "nested": map[string]any{"private_key": "PRIVATE_SENTINEL"}}, "reason": "use " + secret})
	calls := []ToolCall{{ID: "fixture", Function: ToolFunction{Name: "propose_panel_operation", Arguments: string(args)}}}
	copy := redactPanelOperationToolCalls(calls)
	if copy[0].Function.Arguments == calls[0].Function.Arguments || strings.Contains(copy[0].Function.Arguments, "PRIVATE_SENTINEL") || strings.Contains(copy[0].Function.Arguments, "slash") || !json.Valid([]byte(copy[0].Function.Arguments)) || !strings.Contains(copy[0].Function.Arguments, `"password_limit_enable":true`) || !strings.Contains(copy[0].Function.Arguments, `"token_configured":true`) {
		t.Fatal(copy)
	}
	if calls[0].Function.Arguments != string(args) {
		t.Fatal("execution arguments mutated")
	}
	a := Action{Kind: "panel_operation", Payload: json.RawMessage(`{"operation":"config.save","parameters_cipher":"CIPHER_SENTINEL","parameters_preview":{"api_key":"[REDACTED]"}}`)}
	raw, err := json.Marshal(a)
	if err != nil || strings.Contains(string(raw), "CIPHER_SENTINEL") || !strings.Contains(string(raw), "parameters_preview") {
		t.Fatal(string(raw), err)
	}
}

func TestAIPanelOperationErrorPreservesSafeCategory(t *testing.T) {
	for _, tc := range []struct {
		status int
		code   string
	}{{400, "invalid_arguments"}, {403, "forbidden"}, {404, "not_found"}, {409, "conflict"}, {429, "rate_limited"}, {500, "backend_error"}} {
		err := safeAIError(&PanelOperationError{Status: tc.status, Code: "SECRET_CODE", Message: "raw SQL SECRET_VALUE"})
		out := toolErrorContent(err)
		if !strings.Contains(out, `"code":"`+tc.code+`"`) || strings.Contains(out, "SECRET") || strings.Contains(err.Error(), "SECRET") {
			t.Fatal(out, err)
		}
		if errors.Is(err, ErrValidation) {
			t.Fatal("HTTP response mistaken for pre-dispatch validation")
		}
	}
}
