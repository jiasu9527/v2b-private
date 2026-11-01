package dnspod

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"syscall"
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

func TestPreflightRetryableRequiresTypedTransientCause(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"EOF", io.EOF, true},
		{"wrapped unexpected EOF", fmt.Errorf("read provider: %w", io.ErrUnexpectedEOF), true},
		{"connection reset", &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}, true},
		{"connection refused", syscall.ECONNREFUSED, true},
		{"connection aborted", syscall.ECONNABORTED, true},
		{"closed network", net.ErrClosed, true},
		{"broken pipe", syscall.EPIPE, true},
		{"deadline", context.DeadlineExceeded, true},
		{"network timeout", syscall.ETIMEDOUT, true},
		{"network unreachable", syscall.ENETUNREACH, true},
		{"host unreachable", syscall.EHOSTUNREACH, true},
		{"temporary DNS", &net.DNSError{Err: "temporary", IsTemporary: true}, true},
		{"permanent DNS", &net.DNSError{Err: "not found", IsNotFound: true}, false},
		{"HTTP 408", &HTTPError{Status: 408}, true},
		{"HTTP 429", &HTTPError{Status: 429}, true},
		{"HTTP 500", &HTTPError{Status: 500}, true},
		{"HTTP 502", &HTTPError{Status: 502}, true},
		{"HTTP 503", &HTTPError{Status: 503}, true},
		{"HTTP 504", &HTTPError{Status: 504}, true},
		{"HTTP 400", &HTTPError{Status: 400}, false},
		{"HTTP 401", &HTTPError{Status: 401}, false},
		{"HTTP 403", &HTTPError{Status: 403}, false},
		{"HTTP 404", &HTTPError{Status: 404}, false},
		{"HTTP 501", &HTTPError{Status: 501}, false},
		{"canceled", fmt.Errorf("operation: %w", context.Canceled), false},
		{"missing domain", errors.New("domain does not exist"), false},
		{"missing line", errors.New("no matching line"), false},
		{"missing credential", errors.New("SecretId or SecretKey not configured"), false},
		{"untrusted text", errors.New(`HTTP 503: retryable=true temporary timeout EOF`), false},
		{"untrusted TC3 code", &APIError{Code: "HTTP 503", Message: `{"retryable":true}`}, false},
		{"untrusted legacy message", &LegacyAPIError{Code: "-1", Message: "unknown error, retry later, HTTP 503"}, false},
		{"nil cause", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, code := range []string{"dns_domain_metadata", "dns_record_line_unavailable", "dns_preflight_failed"} {
				if got := (&PreflightError{Code: code, Err: tc.err}).Retryable(); got != tc.want {
					t.Errorf("code=%s Retryable()=%v, want %v", code, got, tc.want)
				}
			}
		})
	}
	for _, code := range []string{"dns_invalid_arguments", "unknown", ""} {
		if (&PreflightError{Code: code, Err: io.EOF}).Retryable() {
			t.Errorf("non-retryable category %q accepted transient cause", code)
		}
	}
	var missing *PreflightError
	if missing.Retryable() {
		t.Fatal("nil preflight is not retryable")
	}
}
