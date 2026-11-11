package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"forest/go-api/internal/newapibridge"
)

func TestWorkspaceHTTPClassifiedUpstreamErrorsAreSanitized(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{newapibridge.ErrWorkspaceCapacity, 503, "model_capacity_unavailable"},
		{newapibridge.ErrWorkspaceInvalidRequest, 400, "upstream_invalid_request"},
		{newapibridge.ErrWorkspaceResponse, 502, "upstream_invalid_response"},
		{newapibridge.ErrRemoteUnavailable, 503, "upstream_unavailable"},
		{context.DeadlineExceeded, 503, "upstream_unavailable"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			w := httptest.NewRecorder()
			workspaceError(w, fmt.Errorf("PRIVATE_SECRET https://provider.example.test: %w", tc.err))
			var view map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
				t.Fatal(err)
			}
			if w.Code != tc.status || view["code"] != tc.code || view["message"] == "" || strings.Contains(w.Body.String(), "PRIVATE_SECRET") || strings.Contains(w.Body.String(), "provider.example.test") {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
		})
	}
}
