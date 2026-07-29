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
	req := httptest.NewRequest(http.MethodGet, "http://internal-upstream.example/api/v1/probe/install.sh?api_url=https%3A%2F%2Fprobe-public.example.com&token=probe_token-1&interval=45", nil)
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
	for _, want := range []string{"https://probe-public.example.com", "probe_token-1", "INTERVAL=45", "https://probe-public.example.com/api/v1/probe/download/linux", `EXPECTED_SHA=$(awk 'NR==1 {print $1}'`, `ACTUAL_SHA=$(sha256sum "$TMP_DIR/$ARTIFACT"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("script missing %q: %s", want, body)
		}
	}
	for _, want := range []string{"systemctl enable forest-probe.service", "systemctl restart forest-probe.service"} {
		if !strings.Contains(body, want) {
			t.Fatalf("script missing update activation command %q: %s", want, body)
		}
	}
	if strings.Contains(body, "systemctl enable --now forest-probe.service") {
		t.Fatalf("installer would leave an already-running probe on the old binary: %s", body)
	}
	if strings.Contains(body, "internal-upstream.example") {
		t.Fatalf("script leaked reverse-proxy upstream host into DOWNLOAD_BASE: %s", body)
	}
	if !strings.Contains(body, `"interval":%s`) || strings.Contains(body, `"interval":"%ss"`) {
		t.Fatalf("script must persist interval as JSON seconds number: %s", body)
	}

	bad := httptest.NewRequest(http.MethodGet, "/api/v1/probe/install.sh?api_url=file%3A%2F%2F%2Ftmp%2Fx&token=$(id)", nil)
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

	for _, path := range []string{"/api/v1/probe/download/linux/amd64", "/api/v1/probe/download/linux/amd64.sha256"} {
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
	traversal := httptest.NewRequest(http.MethodGet, "/api/v1/probe/download/linux/../amd64", nil)
	traversalRec := httptest.NewRecorder()
	router.ServeHTTP(traversalRec, traversal)
	if traversalRec.Code != http.StatusNotFound {
		t.Fatalf("traversal expected 404, got %d", traversalRec.Code)
	}
	missing := httptest.NewRequest(http.MethodGet, "/api/v1/probe/download/linux/arm64", nil)
	missingRec := httptest.NewRecorder()
	router.ServeHTTP(missingRec, missing)
	if missingRec.Code != http.StatusNotFound {
		t.Fatalf("missing expected 404, got %d", missingRec.Code)
	}
}

func TestRouterRejectsLegacyProbeDistributionPaths(t *testing.T) {
	router := NewRouter(config.Config{ProbeStorageDir: t.TempDir()})
	for _, path := range []string{"/probe/install.sh", "/probe/download/linux/amd64"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status=%d, want 404", path, rec.Code)
		}
	}
}
