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

func TestAdminRuntimeAssetsOverlayAndFallback(t *testing.T) {
	root := t.TempDir()
	writeUIFixture(t, root)
	publicDir := filepath.Join(root, "public")
	legacy := filepath.Join(publicDir, "assets", "admin-new", "admin.js")
	if err := os.WriteFile(legacy, []byte("// bundled old"), 0o644); err != nil {
		t.Fatal(err)
	}
	router := NewRouter(config.Config{PublicDir: publicDir, AdminPath: "localadmin"})
	request := func(url string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, url, nil))
		return w
	}
	if w := request("/assets/admin-new/admin.js"); w.Code != 200 || !strings.Contains(w.Body.String(), "bundled old") {
		t.Fatalf("fresh checkout must use bundled fallback: %d %s", w.Code, w.Body.String())
	}
	runtime := filepath.Join(root, ".local", "admin-new")
	for _, version := range []string{"r1", "r2"} {
		release := filepath.Join(runtime, "releases", version)
		for filename, data := range map[string]string{
			".published": "",
			"admin.js":   "// runtime " + version,
			"index.css":  "/* runtime " + version + " */",
			"index.html": "<!doctype html>",
		} {
			path := filepath.Join(release, filename)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.Symlink("releases/r1", filepath.Join(runtime, "current")); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ version, body string }{{"r1", "runtime r1"}, {"r2", "runtime r2"}} {
		if item.version == "r2" {
			if err := os.Symlink("releases/r2", filepath.Join(runtime, "next")); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(filepath.Join(runtime, "next"), filepath.Join(runtime, "current")); err != nil {
				t.Fatal(err)
			}
		}
		asset := request("/assets/admin-new/admin.js")
		if asset.Code != 200 || !strings.Contains(asset.Body.String(), item.body) || strings.Contains(asset.Body.String(), "bundled old") {
			t.Fatalf("wrong active release %s: %d %s", item.version, asset.Code, asset.Body.String())
		}
		versioned := request("/assets/admin-new/admin.js?v=" + item.version)
		if versioned.Code != 200 || !strings.Contains(versioned.Body.String(), item.body) {
			t.Fatalf("wrong versioned release %s: %d %s", item.version, versioned.Code, versioned.Body.String())
		}
		shell := request("/localadmin")
		if shell.Code != 200 || !strings.Contains(shell.Body.String(), "admin.js?v="+item.version) || !strings.Contains(shell.Body.String(), "index.css?v="+item.version) {
			t.Fatalf("admin shell/version mismatch: %d %s", shell.Code, shell.Body.String())
		}
	}
	// An old page's versioned assets stay consistent across the switch.
	if old := request("/assets/admin-new/admin.js?v=r1"); old.Code != 200 || !strings.Contains(old.Body.String(), "runtime r1") {
		t.Fatalf("previously opened shell loaded mixed release: %d %s", old.Code, old.Body.String())
	}
	if old := request("/assets/admin-new/admin.js?v=123456789"); old.Code != 200 || !strings.Contains(old.Body.String(), "bundled old") {
		t.Fatalf("pre-deployment bundled shell lost matching fallback: %d %s", old.Code, old.Body.String())
	}
	partial := filepath.Join(runtime, "releases", "r3")
	if err := os.MkdirAll(partial, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(partial, "admin.js"), []byte("partial build"), 0o644); err != nil {
		t.Fatal(err)
	}
	if w := request("/assets/admin-new/admin.js?v=r3"); w.Code != 404 {
		t.Fatalf("unpublished build exposed: %d", w.Code)
	}
	partialPublished := filepath.Join(runtime, "releases", "r4")
	if err := os.MkdirAll(partialPublished, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".published", "admin.js", "index.html"} {
		if err := os.WriteFile(filepath.Join(partialPublished, name), []byte("incomplete"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if w := request("/assets/admin-new/admin.js?v=r4"); w.Code != 404 {
		t.Fatalf("incomplete published release exposed: %d", w.Code)
	}
	if err := os.WriteFile(filepath.Join(root, "secret.txt"), []byte("private fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "secret.txt"), filepath.Join(runtime, "releases", "r2", "escape.txt")); err != nil {
		t.Fatal(err)
	}
	if w := request("/assets/admin-new/escape.txt?v=r2"); w.Code != 404 || strings.Contains(w.Body.String(), "private fixture") {
		t.Fatalf("release symlink escaped its directory: %d %s", w.Code, w.Body.String())
	}
	if w := request("/assets/admin-new/.published?v=r2"); w.Code != 404 {
		t.Fatalf("release marker exposed: %d", w.Code)
	}
	if w := request("/assets/admin-new/missing.js"); w.Code != 404 {
		t.Fatalf("overlay must not mix missing files from bundled release: %d", w.Code)
	}
	if w := request("/assets/admin/umi.js"); w.Code != 200 {
		t.Fatalf("legacy public assets must still work: %d", w.Code)
	}
}

func TestRuntimeAdminAssetsRejectTraversal(t *testing.T) {
	root := t.TempDir()
	publicDir := filepath.Join(root, "public")
	assetDir := filepath.Join(root, ".local", "admin-new", "current")
	if err := os.MkdirAll(assetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(assetDir, "admin.js"), []byte("safe"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(assetDir, "index.css"), []byte("safe"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(assetDir, "index.html"), []byte("safe"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(assetDir, ".published"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, url := range []string{"/assets/admin-new/../secret", "/assets/admin-new//admin.js", "/assets/admin-new/../../.env.go"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, url, nil)
		if !serveRuntimeAdminAsset(w, r, publicDir) || w.Code != 404 {
			t.Fatalf("unsafe runtime path %s: %d", url, w.Code)
		}
	}
}
