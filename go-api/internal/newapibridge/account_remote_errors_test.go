package newapibridge

import (
	"errors"
	"strings"
	"testing"
)

func TestWorkspaceSecurityContextErrorIsNotModelContext(t *testing.T) {
	for _, raw := range []string{
		`{"success":false,"code":"SECURITY_CONTEXT_INVALID","message":"PRIVATE_SECRET"}`,
		`{"success":false,"data":{"code":"SECURITY_CONTEXT_INVALID"}}`,
		`{"error":{"code":"SECURITY_CONTEXT_INVALID","message":"PRIVATE_SECRET"}}`,
	} {
		err := workspaceRemoteError(400, []byte(raw))
		if !errors.Is(err, ErrWorkspaceVerification) || strings.Contains(err.Error(), "PRIVATE_SECRET") {
			t.Fatalf("security verification failure misclassified: %v", err)
		}
	}
	for _, raw := range []string{
		`{"error":{"code":"context_length_exceeded"}}`,
		`{"error":{"message":"maximum context length exceeded"}}`,
		`{"error":{"type":"context_window_exceeded"}}`,
	} {
		if err := workspaceRemoteError(400, []byte(raw)); !errors.Is(err, ErrWorkspaceContext) {
			t.Fatalf("model context failure misclassified: %v", err)
		}
	}
}
