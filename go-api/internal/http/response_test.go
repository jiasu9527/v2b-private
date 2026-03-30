package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWriteJSONUsesPlainApplicationJSONContentType(t *testing.T) {
	rec := httptest.NewRecorder()

	writeJSON(rec, http.StatusOK, map[string]any{"data": true})

	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("expected Content-Type application/json, got %q", got)
	}
}
