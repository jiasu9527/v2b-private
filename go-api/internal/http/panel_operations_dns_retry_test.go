package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"forest/go-api/internal/aiops"
	"forest/go-api/internal/dnspod"
)

func TestPanelDNSRetryCaptureRequiresTypedPreflightAndTransientCause(t *testing.T) {
	for _, tc := range []struct {
		name        string
		err         error
		notExecuted bool
		retryable   bool
	}{
		{"metadata disconnect", &dnspod.PreflightError{Code: "dns_domain_metadata", Err: io.EOF}, true, true},
		{"line rate limited", &dnspod.PreflightError{Code: "dns_record_line_unavailable", Err: &dnspod.HTTPError{Status: 429}}, true, true},
		{"precheck unavailable", &dnspod.PreflightError{Code: "dns_preflight_failed", Err: &dnspod.HTTPError{Status: 503}}, true, true},
		{"invalid arguments", &dnspod.PreflightError{Code: "dns_invalid_arguments", Err: io.EOF}, true, false},
		{"missing line", &dnspod.PreflightError{Code: "dns_record_line_unavailable", Err: errors.New("not available")}, true, false},
		{"metadata permission", &dnspod.PreflightError{Code: "dns_domain_metadata", Err: &dnspod.HTTPError{Status: 401}}, true, false},
		{"unknown category", &dnspod.PreflightError{Code: "PRIVATE_SENTINEL", Err: io.EOF}, false, false},
		{"dispatched write HTTP failure", &dnspod.HTTPError{Status: 503}, false, false},
		{"dispatched write disconnect", io.EOF, false, false},
		{"forged response", errors.New(`{"not_executed":true,"retryable":true,"code":"dns_domain_metadata","secret":"PRIVATE_SENTINEL"}`), false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &fakeAdminService{err: tc.err}
			_, err := dnsRecoveryPanel(service).Execute(context.Background(), aiops.PanelOperationActor{ID: 42, IsAdmin: true}, "dns.record_save", json.RawMessage(panelDNSCreateFixture))
			var got *aiops.PanelOperationError
			if !errors.As(err, &got) || got.NotExecuted != tc.notExecuted || got.Retryable != tc.retryable {
				t.Fatalf("wrong trusted retry classification: %#v, %v", got, err)
			}
			if strings.Contains(err.Error()+got.Code+got.Message, "PRIVATE_SENTINEL") {
				t.Fatalf("upstream text leaked: %#v", got)
			}
		})
	}
}

func TestPanelDNSRetryCaptureIsIsolatedPerRequest(t *testing.T) {
	service := &fakeAdminService{err: &dnspod.PreflightError{Code: "dns_domain_metadata", Err: io.EOF}}
	panel := dnsRecoveryPanel(service)
	actor := aiops.PanelOperationActor{ID: 42, IsAdmin: true}
	_, err := panel.Execute(context.Background(), actor, "dns.record_save", json.RawMessage(panelDNSCreateFixture))
	var first *aiops.PanelOperationError
	if !errors.As(err, &first) || !first.NotExecuted || !first.Retryable {
		t.Fatal("fixture did not capture retryable preflight", err)
	}
	service.err = &dnspod.HTTPError{Status: 503}
	_, err = panel.Execute(context.Background(), actor, "dns.record_save", json.RawMessage(panelDNSCreateFixture))
	var second *aiops.PanelOperationError
	if !errors.As(err, &second) || second.NotExecuted || second.Retryable || second.Code != "" {
		t.Fatalf("request reused stale retry capture: %#v, %v", second, err)
	}
}

func TestPanelDNSInvalidInputCannotRetry(t *testing.T) {
	service := &fakeAdminService{}
	invalid := strings.Replace(panelDNSCreateFixture, `"ttl":600`, `"ttl":-1`, 1)
	_, err := dnsRecoveryPanel(service).Execute(context.Background(), aiops.PanelOperationActor{ID: 42, IsAdmin: true}, "dns.record_save", json.RawMessage(invalid))
	var got *aiops.PanelOperationError
	if !errors.As(err, &got) || !got.NotExecuted || got.Retryable || got.Status != http.StatusBadRequest || got.Code != "dns_invalid_arguments" {
		t.Fatalf("invalid input must not become retryable: %#v, %v", got, err)
	}
	if service.lastDNSPodRecordSave.Domain != "" {
		t.Fatal("invalid input reached mutation service")
	}
}

func TestPanelPreflightCaptureCannotRetryInvalidOrUnknownCode(t *testing.T) {
	for _, code := range []string{"dns_invalid_arguments", "unknown"} {
		capture := &panelOperationPreflightCapture{}
		ctx := context.WithValue(context.Background(), panelOperationPreflightKey{}, capture)
		capturePanelOperationPreflight(ctx, code, true)
		if capture.retryable || (code == "unknown" && capture.code != "") {
			t.Fatalf("unsafe retry capture for %s: %#v", code, capture)
		}
	}
}
