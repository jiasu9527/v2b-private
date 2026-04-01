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

func TestRouterDoesNotServeFrontendShell(t *testing.T) {
	root := t.TempDir()
	writeUIFixture(t, root)

	router := NewRouter(config.Config{
		AppName:        "Forest Site",
		AppDescription: "Fast and stable",
		AppURL:         "https://forest.test",
		Logo:           "https://cdn.example.com/logo.png",
		AdminPath:      "localadmin",
		PublicDir:      filepath.Join(root, "public"),
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestRouterDoesNotFallbackToDefaultThemeShell(t *testing.T) {
	root := t.TempDir()
	writeUIFixture(t, root)

	router := NewRouter(config.Config{
		AppName:        "Forest Site",
		AppDescription: "Fast and stable",
		AppURL:         "https://forest.test",
		Logo:           "https://cdn.example.com/logo.png",
		AdminPath:      "localadmin",
		PublicDir:      filepath.Join(root, "public"),
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestRouterServesAdminShellAndCatchAll(t *testing.T) {
	root := t.TempDir()
	writeUIFixture(t, root)

	router := NewRouter(config.Config{
		AppName:        "Forest Site",
		AppDescription: "Fast and stable",
		AppURL:         "https://forest.test",
		Logo:           "https://cdn.example.com/logo.png",
		AdminPath:      "localadmin",
		PublicDir:      filepath.Join(root, "public"),
	})

	for _, path := range []string{"/localadmin", "/localadmin/users"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 for %s, got %d", path, rec.Code)
		}
		body := rec.Body.String()
		for _, needle := range []string{
			`/assets/admin/umi.js`,
			`/assets/admin/custom.js`,
			`<div id="root"></div>`,
			`"secure_path":"localadmin"`,
		} {
			if !strings.Contains(body, needle) {
				t.Fatalf("expected admin shell for %s to contain %q, body=%s", path, needle, body)
			}
		}
	}
}

func TestRouterServesOnlyAdminInviteCampaignPage(t *testing.T) {
	root := t.TempDir()
	writeUIFixture(t, root)

	router := NewRouter(config.Config{
		AppName:        "Forest Site",
		AppDescription: "Fast and stable",
		AppURL:         "https://forest.test",
		Logo:           "https://cdn.example.com/logo.png",
		AdminPath:      "localadmin",
		PublicDir:      filepath.Join(root, "public"),
	})

	cases := []struct {
		path   string
		code   int
		needle string
	}{
		{path: "/invite-campaign", code: http.StatusForbidden},
		{path: "/localadmin/invite-campaign", code: http.StatusOK, needle: `InviteCampaignAdminPage`},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != tc.code {
			t.Fatalf("expected %d for %s, got %d", tc.code, tc.path, rec.Code)
		}
		if tc.needle != "" && !strings.Contains(rec.Body.String(), tc.needle) {
			t.Fatalf("expected %s page to contain %q, body=%s", tc.path, tc.needle, rec.Body.String())
		}
	}
}

func TestRouterMissingStaticAssetStillReturnsNotFound(t *testing.T) {
	root := t.TempDir()
	writeUIFixture(t, root)

	router := NewRouter(config.Config{
		AppName:        "Forest Site",
		AppDescription: "Fast and stable",
		AppURL:         "https://forest.test",
		Logo:           "https://cdn.example.com/logo.png",
		AdminPath:      "localadmin",
		PublicDir:      filepath.Join(root, "public"),
	})

	req := httptest.NewRequest(http.MethodGet, "/assets/missing.js", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing asset, got %d", rec.Code)
	}
}

func writeUIFixture(t *testing.T, root string) {
	t.Helper()

	mustWriteFile(t, filepath.Join(root, "config", "admin.json"), `{
  "app_name": "Forest Site",
  "app_description": "Fast and stable",
  "app_url": "https://forest.test",
  "logo": "https://cdn.example.com/logo.png",
  "frontend_theme_sidebar": "light",
  "frontend_theme_header": "dark",
  "frontend_theme_color": "green",
  "frontend_background_url": "https://cdn.example.com/bg.jpg"
}`)
	for _, path := range []string{
		filepath.Join(root, "public", "assets", "admin", "vendors.async.js"),
		filepath.Join(root, "public", "assets", "admin", "components.async.js"),
		filepath.Join(root, "public", "assets", "admin", "umi.js"),
		filepath.Join(root, "public", "assets", "admin", "umi.css"),
		filepath.Join(root, "public", "assets", "admin", "components.chunk.css"),
		filepath.Join(root, "public", "assets", "admin", "custom.js"),
		filepath.Join(root, "public", "assets", "admin", "custom.css"),
		filepath.Join(root, "public", "assets", "invite-campaign-common.css"),
		filepath.Join(root, "public", "assets", "user-invite-campaign-page.js"),
		filepath.Join(root, "public", "assets", "admin-invite-campaign-page.js"),
	} {
		mustWriteFile(t, path, "// fixture")
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
