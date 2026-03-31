package httpapi

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"forest/go-api/internal/config"
)

func TestWithMiddlewareDisablesAccessLogByDefault(t *testing.T) {
	var buf bytes.Buffer
	restore := swapLoggerOutput(&buf)
	defer restore()

	handler := withMiddleware(config.Config{}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/demo", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}
	if strings.Contains(buf.String(), "GET /demo") {
		t.Fatalf("expected no access log, got %q", buf.String())
	}
}

func TestWithMiddlewareLogsSlowRequests(t *testing.T) {
	var buf bytes.Buffer
	restore := swapLoggerOutput(&buf)
	defer restore()

	handler := withMiddleware(config.Config{SlowRequestLogThreshold: time.Millisecond}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Millisecond)
		w.WriteHeader(http.StatusCreated)
	}))

	req := httptest.NewRequest(http.MethodPost, "/slow", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", rec.Code)
	}
	output := buf.String()
	if !strings.Contains(output, "slow request") || !strings.Contains(output, "/slow") {
		t.Fatalf("expected slow request log, got %q", output)
	}
}

func swapLoggerOutput(buf *bytes.Buffer) func() {
	previousWriter := log.Writer()
	previousFlags := log.Flags()
	log.SetOutput(buf)
	log.SetFlags(0)
	return func() {
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
	}
}
