package dnspod

import (
	"errors"
	"strings"
	"testing"
)

func TestPreflightErrorNeverExposesCause(t *testing.T) {
	cause := errors.New("provider-secret login_token=secret example.com 192.0.2.1")
	for _, code := range []string{"dns_invalid_arguments", "dns_domain_metadata", "dns_record_line_unavailable", "dns_preflight_failed", "untrusted-provider-code"} {
		err := &PreflightError{Code: code, Err: cause}
		if !errors.Is(err, cause) || !strings.Contains(err.Error(), "尚未发送写请求") {
			t.Fatalf("preflight contract: %s", err)
		}
		for _, sensitive := range []string{"provider-secret", "login_token", "example.com", "192.0.2.1", "untrusted-provider-code"} {
			if strings.Contains(err.Error(), sensitive) {
				t.Fatalf("unsafe preflight message: %s", err)
			}
		}
	}
	var err *PreflightError
	if err.Unwrap() != nil || !strings.Contains(err.Error(), "尚未发送写请求") {
		t.Fatal("nil preflight must have a safe message and no cause")
	}
}
