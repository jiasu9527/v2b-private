package dnspod

import (
	"context"
	"errors"
	"io"
	"net"
	"syscall"
)

// PreflightError proves that a DNS write was not sent. Only local validation,
// credential loading, and read-only prerequisite lookups may return this type.
// Never wrap an error returned after a mutation request has been dispatched.
type PreflightError struct {
	Code string
	Err  error
}

// Error deliberately excludes provider text, submitted values and credentials.
// Callers may inspect the cause locally with errors.Is/As without exposing it.
func (e *PreflightError) Error() string {
	if e != nil {
		switch e.Code {
		case "dns_invalid_arguments":
			return "DNS 参数校验未通过，尚未发送写请求；请核对域名、记录 ID 和记录值"
		case "dns_domain_metadata":
			return "无法确认 DNS 域名 ID 与套餐等级，尚未发送写请求；请重新查询域名并核对域名与 ID"
		case "dns_record_line_unavailable":
			return "DNS 解析线路预检查未通过，尚未发送写请求；请查询该域名实际支持的线路"
		}
	}
	return "DNS 写入前检查未通过，尚未发送写请求；请核对 DNS 配置或稍后重新查询"
}

func (e *PreflightError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// Retryable only classifies failures of read-only prerequisites. A transient
// provider error alone does not prove that repeating a mutation is safe.
func (e *PreflightError) Retryable() bool {
	if e == nil || e.Err == nil || errors.Is(e.Err, context.Canceled) {
		return false
	}
	switch e.Code {
	case "dns_domain_metadata", "dns_record_line_unavailable", "dns_preflight_failed":
	default:
		return false
	}
	var response *HTTPError
	if errors.As(e.Err, &response) && response != nil {
		switch response.Status {
		case 408, 429, 500, 502, 503, 504:
			return true
		default:
			return false
		}
	}
	var network net.Error
	if errors.As(e.Err, &network) && (network.Timeout() || network.Temporary()) {
		return true
	}
	for _, cause := range []error{io.EOF, io.ErrUnexpectedEOF, net.ErrClosed,
		syscall.ECONNRESET, syscall.ECONNABORTED, syscall.ECONNREFUSED,
		syscall.EPIPE, syscall.ETIMEDOUT, syscall.EHOSTUNREACH, syscall.ENETUNREACH} {
		if errors.Is(e.Err, cause) {
			return true
		}
	}
	return false
}
