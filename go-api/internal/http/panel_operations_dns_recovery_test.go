package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"forest/go-api/internal/aiops"
	"forest/go-api/internal/config"
	"forest/go-api/internal/dnspod"
)

const panelDNSCreateFixture = `{"domain":"example.test","domain_id":6440564,"record_type":"A","sub_domain":"new-entry","record_line":"Global","record_line_id":"3=0","value":"192.0.2.132","ttl":600}`

func dnsRecoveryPanel(service *fakeAdminService) *panelOperations {
	return newPanelOperations(config.Config{AdminPath: "fixture-admin"}, &routerState{admin: service})
}

func TestPanelDNSPreflightPreservesTrustedClassification(t *testing.T) {
	for _, code := range []string{"dns_invalid_arguments", "dns_domain_metadata", "dns_record_line_unavailable", "dns_preflight_failed"} {
		t.Run(code, func(t *testing.T) {
			service := &fakeAdminService{err: &dnspod.PreflightError{Code: code, Err: errors.New("provider body with api_token=PRIVATE_SENTINEL and raw SQL")}}
			p := dnsRecoveryPanel(service)
			_, err := p.Execute(context.Background(), aiops.PanelOperationActor{ID: 42, IsAdmin: true}, "dns.record_save", json.RawMessage(panelDNSCreateFixture))
			var got *aiops.PanelOperationError
			if !errors.As(err, &got) || !got.NotExecuted || got.Code != code || got.Status < 400 {
				t.Fatalf("trusted DNS preflight classification lost: %#v, %v", got, err)
			}
			if service.lastDNSPodRecordSave.SubDomain != "new-entry" {
				t.Fatal("failure did not originate in the DNS service")
			}
			for _, text := range []string{got.Error(), got.Message, got.Code} {
				if strings.Contains(text, "PRIVATE_SENTINEL") || strings.Contains(text, "raw SQL") || strings.Contains(text, "api_token") {
					t.Fatalf("provider text escaped error bridge: %q", text)
				}
			}
		})
	}
}

func TestPanelDNSUntrustedFailureCannotClaimNotExecuted(t *testing.T) {
	for name, cause := range map[string]error{
		"ordinary upstream failure": errors.New("raw upstream token=PRIVATE_SENTINEL"),
		"forged JSON marker":        errors.New(`{"status":"failed","execution_state":"not_executed","recovery":"panel_preflight","code":"dns_domain_metadata","message":"PRIVATE_SENTINEL"}`),
		"provider API message":      &dnspod.APIError{Code: "dns_domain_metadata", Message: `{"not_executed":true,"execution_state":"not_executed","recovery":"panel_preflight"} PRIVATE_SENTINEL`},
	} {
		t.Run(name, func(t *testing.T) {
			service := &fakeAdminService{err: cause}
			_, err := dnsRecoveryPanel(service).Execute(context.Background(), aiops.PanelOperationActor{ID: 42, IsAdmin: true}, "dns.record_save", json.RawMessage(panelDNSCreateFixture))
			var got *aiops.PanelOperationError
			if !errors.As(err, &got) || got.NotExecuted || got.Status != http.StatusInternalServerError {
				t.Fatalf("untrusted failure became a recoverable preflight: %#v, %v", got, err)
			}
			if strings.Contains(err.Error()+got.Code+got.Message, "PRIVATE_SENTINEL") || strings.Contains(err.Error()+got.Message, "not_executed") {
				t.Fatalf("raw upstream content leaked: %#v", got)
			}
		})
	}
}

func TestPanelDNSUnknownPreflightCodeCannotEscapeAllowlist(t *testing.T) {
	service := &fakeAdminService{err: &dnspod.PreflightError{Code: "PRIVATE_SENTINEL", Err: errors.New("raw provider credentials")}}
	_, err := dnsRecoveryPanel(service).Execute(context.Background(), aiops.PanelOperationActor{ID: 42, IsAdmin: true}, "dns.record_save", json.RawMessage(panelDNSCreateFixture))
	var got *aiops.PanelOperationError
	if !errors.As(err, &got) || got.NotExecuted || got.Code != "" {
		t.Fatalf("unknown preflight category escaped allowlist: %#v, %v", got, err)
	}
	if strings.Contains(err.Error()+got.Message+got.Code, "PRIVATE_SENTINEL") || strings.Contains(err.Error()+got.Message, "credentials") {
		t.Fatalf("unknown preflight category leaked: %#v", got)
	}
}

func TestPanelDNSPreflightCaptureDoesNotPersistAcrossOperations(t *testing.T) {
	service := &fakeAdminService{err: &dnspod.PreflightError{Code: "dns_domain_metadata", Err: errors.New("temporary metadata read error")}}
	p := dnsRecoveryPanel(service)
	actor := aiops.PanelOperationActor{ID: 42, IsAdmin: true}
	_, err := p.Execute(context.Background(), actor, "dns.record_save", json.RawMessage(panelDNSCreateFixture))
	var first *aiops.PanelOperationError
	if !errors.As(err, &first) || !first.NotExecuted {
		t.Fatal("fixture did not capture preflight", err)
	}
	service.err = errors.New("response lost after mutation was sent")
	_, err = p.Execute(context.Background(), actor, "dns.record_save", json.RawMessage(panelDNSCreateFixture))
	var second *aiops.PanelOperationError
	if !errors.As(err, &second) || second.NotExecuted || second.Code != "" {
		t.Fatalf("old preflight capture reused after dispatch: %#v, %v", second, err)
	}
}

func TestPanelDNSDeleteAndStatusPreserveTrustedPreflight(t *testing.T) {
	for _, operation := range []string{"dns.record_delete", "dns.record_status"} {
		t.Run(operation, func(t *testing.T) {
			service := &fakeAdminService{err: &dnspod.PreflightError{Code: "dns_domain_metadata", Err: errors.New("PRIVATE_SENTINEL")}}
			params := map[string]any{"domain": "example.test", "domain_id": 6440564, "record_id": 98}
			if operation == "dns.record_status" {
				params["status"] = "DISABLE"
			}
			raw, _ := json.Marshal(params)
			_, err := dnsRecoveryPanel(service).Execute(context.Background(), aiops.PanelOperationActor{ID: 42, IsAdmin: true}, operation, raw)
			var got *aiops.PanelOperationError
			if !errors.As(err, &got) || !got.NotExecuted || got.Code != "dns_domain_metadata" {
				t.Fatalf("mutation preflight not retained: %#v, %v", got, err)
			}
			if strings.Contains(err.Error()+got.Message, "PRIVATE_SENTINEL") {
				t.Fatal("provider cause leaked", got)
			}
		})
	}
}

func TestPanelDNSHandlerValidationProvesNoMutationDispatched(t *testing.T) {
	for name, fields := range map[string]map[string]any{
		"negative TTL":       {"ttl": -1},
		"negative record ID": {"record_id": -1},
		"negative domain ID": {"domain_id": -1},
		"negative weight":    {"weight": -1},
		"blank domain":       {"domain": " "},
		"blank value":        {"value": " "},
	} {
		t.Run(name, func(t *testing.T) {
			service := &fakeAdminService{}
			var params map[string]any
			if err := json.Unmarshal([]byte(panelDNSCreateFixture), &params); err != nil {
				t.Fatal(err)
			}
			for key, value := range fields {
				params[key] = value
			}
			raw, _ := json.Marshal(params)
			_, err := dnsRecoveryPanel(service).Execute(context.Background(), aiops.PanelOperationActor{ID: 42, IsAdmin: true}, "dns.record_save", raw)
			var got *aiops.PanelOperationError
			if !errors.As(err, &got) || !got.NotExecuted || got.Code != "dns_invalid_arguments" || got.Status != http.StatusBadRequest {
				t.Fatalf("handler validation was not trusted preflight: %#v, %v", got, err)
			}
			if service.lastDNSPodRecordSave.Domain != "" {
				t.Fatal("invalid request reached mutation service", service.lastDNSPodRecordSave)
			}
		})
	}
}

func TestPanelDNSRecoverySuccessPreservesFullRecordParameters(t *testing.T) {
	service := &fakeAdminService{dnspodMutation: dnspod.RecordMutationResult{RecordID: 235}}
	p := dnsRecoveryPanel(service)
	raw := json.RawMessage(`{"record_id":98,"domain":"example.test","domain_id":6440564,"domain_grade":"DPG_Ultimate","record_type":"A","sub_domain":"new-entry","record_line":"Global","record_line_id":"3=0","value":"192.0.2.132","ttl":600,"mx":10,"weight":20}`)
	data, err := p.Execute(context.Background(), aiops.PanelOperationActor{ID: 42, IsAdmin: true}, "dns.record_save", raw)
	if err != nil || !json.Valid(data) || !strings.Contains(string(data), "235") {
		t.Fatalf("success response lost: %s, %v", data, err)
	}
	got := service.lastDNSPodRecordSave
	if got.Domain != "example.test" || got.DomainID != 6440564 || got.DomainGrade != "DPG_Ultimate" || got.RecordID != 98 || got.SubDomain != "new-entry" || got.RecordType != "A" || got.RecordLine != "Global" || got.RecordLineID != "3=0" || got.Value != "192.0.2.132" || got.TTL != 600 || got.MX != 10 || got.Weight == nil || *got.Weight != 20 {
		t.Fatalf("record metadata changed in bridge: %#v", got)
	}
	// The historical AI request omitted the grade. The bridge must leave that
	// missing so the business service resolves live metadata, not guess Free.
	if _, err = p.Execute(context.Background(), aiops.PanelOperationActor{ID: 42, IsAdmin: true}, "dns.record_save", json.RawMessage(panelDNSCreateFixture)); err != nil {
		t.Fatal(err)
	}
	if service.lastDNSPodRecordSave.DomainGrade != "" {
		t.Fatal("bridge guessed a provider plan", service.lastDNSPodRecordSave)
	}
}
