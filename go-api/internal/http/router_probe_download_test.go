package httpapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forest/go-api/internal/config"
)

func TestRouterProbeInstallRendersValidatedScript(t *testing.T) {
	router := NewRouter(config.Config{ProbeStorageDir: t.TempDir()})
	req := httptest.NewRequest(http.MethodGet, "https://panel.example.com/probe/install.sh?api_url=https%3A%2F%2Fpanel.example.com%2Fapi%2Fv1&token=probe_token-1&interval=45", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "text/x-shellscript") {
		t.Fatalf("unexpected content type %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("unexpected cache control %q", got)
	}
	body := rec.Body.String()
	for _, want := range []string{"https://panel.example.com/api/v1", "probe_token-1", "INTERVAL=45", "https://panel.example.com/probe/download/linux"} {
		if !strings.Contains(body, want) {
			t.Fatalf("script missing %q: %s", want, body)
		}
	}

	bad := httptest.NewRequest(http.MethodGet, "/probe/install.sh?api_url=file%3A%2F%2F%2Ftmp%2Fx&token=$(id)", nil)
	badRec := httptest.NewRecorder()
	router.ServeHTTP(badRec, bad)
	if badRec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", badRec.Code)
	}
}

func TestRouterProbeDownloadUsesFixedArtifactNames(t *testing.T) {
	storage := t.TempDir()
	binary := filepath.Join(storage, "forest-probe-linux-amd64")
	if err := os.WriteFile(binary, []byte("probe-amd64"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary+".sha256", []byte("abc  forest-probe-linux-amd64\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	router := NewRouter(config.Config{ProbeStorageDir: storage})

	for _, path := range []string{"/probe/download/linux/amd64", "/probe/download/linux/amd64.sha256"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s expected 200, got %d", path, rec.Code)
		}
		if !strings.Contains(rec.Header().Get("Content-Disposition"), "attachment") {
			t.Fatalf("%s missing attachment", path)
		}
		if rec.Header().Get("Cache-Control") == "" {
			t.Fatalf("%s missing cache policy", path)
		}
		if rec.Header().Get("Content-Type") != "application/octet-stream" {
			t.Fatalf("%s unexpected content type %q", path, rec.Header().Get("Content-Type"))
		}
	}
	traversal := httptest.NewRequest(http.MethodGet, "/probe/download/linux/../amd64", nil)
	traversalRec := httptest.NewRecorder()
	router.ServeHTTP(traversalRec, traversal)
	if traversalRec.Code != http.StatusNotFound {
		t.Fatalf("traversal expected 404, got %d", traversalRec.Code)
	}
	missing := httptest.NewRequest(http.MethodGet, "/probe/download/linux/arm64", nil)
	missingRec := httptest.NewRecorder()
	router.ServeHTTP(missingRec, missing)
	if missingRec.Code != http.StatusNotFound {
		t.Fatalf("missing expected 404, got %d", missingRec.Code)
	}
}
