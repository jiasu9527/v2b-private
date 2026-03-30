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

func TestRouterServesFrontendShell(t *testing.T) {
	root := t.TempDir()
	writeUIFixture(t, root, "default")

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

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if contentType := rec.Header().Get("Content-Type"); !strings.Contains(contentType, "text/html") {
		t.Fatalf("expected html content-type, got %q", contentType)
	}

	body := rec.Body.String()
	for _, needle := range []string{
		"<title>Forest Site</title>",
		`/theme/default/assets/umi.js`,
		`/theme/default/assets/custom.js`,
		`<div id="root"></div>`,
		`window.settings = `,
		`https://cdn.example.com/logo.png`,
		`<footer>hello</footer>`,
	} {
		if !strings.Contains(body, needle) {
			t.Fatalf("expected frontend shell to contain %q, body=%s", needle, body)
		}
	}
}

func TestRouterFallsBackToDefaultThemeShell(t *testing.T) {
	root := t.TempDir()
	writeUIFixture(t, root, "missing-theme")

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

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `/theme/default/assets/umi.js`) {
		t.Fatalf("expected default theme fallback, body=%s", body)
	}
	if strings.Contains(body, `/theme/missing-theme/assets/umi.js`) {
		t.Fatalf("expected missing theme asset path to be avoided, body=%s", body)
	}
}

func TestRouterServesAdminShellAndCatchAll(t *testing.T) {
	root := t.TempDir()
	writeUIFixture(t, root, "default")

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

func TestRouterServesInviteCampaignPages(t *testing.T) {
	root := t.TempDir()
	writeUIFixture(t, root, "default")

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
		needle string
	}{
		{path: "/invite-campaign", needle: `InviteCampaignUserPage`},
		{path: "/localadmin/invite-campaign", needle: `InviteCampaignAdminPage`},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 for %s, got %d", tc.path, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), tc.needle) {
			t.Fatalf("expected %s page to contain %q, body=%s", tc.path, tc.needle, rec.Body.String())
		}
	}
}

func writeUIFixture(t *testing.T, root, frontendTheme string) {
	t.Helper()

	mustWriteFile(t, filepath.Join(root, "config", "admin.json"), `{
  "app_name": "Forest Site",
  "app_description": "Fast and stable",
  "app_url": "https://forest.test",
  "logo": "https://cdn.example.com/logo.png",
  "frontend_theme": "`+frontendTheme+`",
  "frontend_theme_sidebar": "light",
  "frontend_theme_header": "dark",
  "frontend_theme_color": "green",
  "frontend_background_url": "https://cdn.example.com/bg.jpg"
}`)
	mustWriteFile(t, filepath.Join(root, "config", "theme", "default.json"), `{
  "theme_color": "green",
  "theme_sidebar": "light",
  "theme_header": "dark",
  "background_url": "https://cdn.example.com/bg.jpg",
  "custom_html": "<footer>hello</footer>"
}`)
	mustWriteFile(t, filepath.Join(root, "public", "theme", "default", "config.json"), `{"name":"default"}`)
	for _, path := range []string{
		filepath.Join(root, "public", "theme", "default", "assets", "vendors.async.js"),
		filepath.Join(root, "public", "theme", "default", "assets", "components.async.js"),
		filepath.Join(root, "public", "theme", "default", "assets", "umi.js"),
		filepath.Join(root, "public", "theme", "default", "assets", "umi.css"),
		filepath.Join(root, "public", "theme", "default", "assets", "components.chunk.css"),
		filepath.Join(root, "public", "theme", "default", "assets", "custom.js"),
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
