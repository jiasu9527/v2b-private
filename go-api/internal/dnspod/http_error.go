package dnspod

import "fmt"

// HTTPError retains only the HTTP transport status, never an upstream body.
// It does not imply whether a mutation was executed. Retryable DNS preflight
// lookups must wrap it in PreflightError before dispatching a write.
type HTTPError struct {
	Status int
}

func (e *HTTPError) Error() string {
	if e == nil {
		return "DNSPod HTTP 请求失败"
	}
	return fmt.Sprintf("DNSPod 接口返回 HTTP %d", e.Status)
}
