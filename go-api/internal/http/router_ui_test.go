package httpapi

import (
	"bufio"
	"bytes"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	rec := newHijackRecorder()
	router.ServeHTTP(rec, req)

	if !rec.hijacked || !rec.conn.closed {
		t.Fatalf("expected unknown ui path to close connection")
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
	rec := newHijackRecorder()
	router.ServeHTTP(rec, req)

	if !rec.hijacked || !rec.conn.closed {
		t.Fatalf("expected unknown ui path to close connection")
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
		{path: "/invite-campaign", code: 0},
		{path: "/localadmin/invite-campaign", code: http.StatusOK, needle: `InviteCampaignAdminPage`},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		if tc.code == 0 {
			rec := newHijackRecorder()
			router.ServeHTTP(rec, req)
			if !rec.hijacked || !rec.conn.closed {
				t.Fatalf("expected %s to close connection", tc.path)
			}
			continue
		}
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

func TestRouterUnknownUIPathFallsBackToForbiddenWithoutHijacker(t *testing.T) {
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
		t.Fatalf("expected 403 fallback, got %d", rec.Code)
	}
	if rec.Body.String() != "连接已关闭" {
		t.Fatalf("expected fallback body 连接已关闭, got %q", rec.Body.String())
	}
}

type hijackRecorder struct {
	header   http.Header
	body     bytes.Buffer
	code     int
	hijacked bool
	conn     *fakeConn
}

func newHijackRecorder() *hijackRecorder {
	return &hijackRecorder{
		header: make(http.Header),
		conn:   &fakeConn{},
	}
}

func (r *hijackRecorder) Header() http.Header {
	return r.header
}

func (r *hijackRecorder) Write(data []byte) (int, error) {
	if r.code == 0 {
		r.code = http.StatusOK
	}
	return r.body.Write(data)
}

func (r *hijackRecorder) WriteHeader(statusCode int) {
	r.code = statusCode
}

func (r *hijackRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	r.hijacked = true
	return r.conn, bufio.NewReadWriter(bufio.NewReader(bytes.NewReader(nil)), bufio.NewWriter(&r.body)), nil
}

type fakeConn struct {
	closed bool
}

func (c *fakeConn) Read(_ []byte) (int, error)         { return 0, errors.New("not implemented") }
func (c *fakeConn) Write(b []byte) (int, error)        { return len(b), nil }
func (c *fakeConn) Close() error                       { c.closed = true; return nil }
func (c *fakeConn) LocalAddr() net.Addr                { return fakeAddr("local") }
func (c *fakeConn) RemoteAddr() net.Addr               { return fakeAddr("remote") }
func (c *fakeConn) SetDeadline(_ time.Time) error      { return nil }
func (c *fakeConn) SetReadDeadline(_ time.Time) error  { return nil }
func (c *fakeConn) SetWriteDeadline(_ time.Time) error { return nil }

type fakeAddr string

func (a fakeAddr) Network() string { return "tcp" }
func (a fakeAddr) String() string  { return string(a) }

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
