package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"forest/go-api/internal/config"
)

type legacyRouteParityFixture struct {
	Cases []legacyRouteParityCase `json:"cases"`
}

type legacyRouteParityCase struct {
	Method string `json:"method"`
	Path   string `json:"path"`
}

func TestLegacyRouteParityFixtureCoverage(t *testing.T) {
	fixture := loadLegacyRouteParityFixture(t)
	if len(fixture.Cases) < 150 {
		t.Fatalf("expected full legacy route fixture, got only %d cases", len(fixture.Cases))
	}

	required := map[string]bool{
		"GET /api/v1/user/info":                             false,
		"POST /api/v1/user/order/save":                      false,
		"GET /api/v1/<admin_path>/payment/fetch":            false,
		"POST /api/v1/<admin_path>/server/vmess/save":       false,
		"POST /api/v1/guest/payment/notify/{method}/{uuid}": false,
		"GET /api/v2/server/config":                         false,
	}
	for _, item := range fixture.Cases {
		key := item.Method + " " + item.Path
		if _, ok := required[key]; ok {
			required[key] = true
		}
	}
	for key, ok := range required {
		if !ok {
			t.Fatalf("fixture missing required route %s", key)
		}
	}
}

func TestLegacyRouteParity(t *testing.T) {
	fixture := loadLegacyRouteParityFixture(t)
	router := NewRouter(config.Config{AppName: "forest-go", AdminPath: "localadmin", TelegramBotToken: "bot-token"})

	for _, item := range fixture.Cases {
		path := concreteLegacyRoutePath(item.Path)
		req := httptest.NewRequest(item.Method, path, strings.NewReader(""))
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code == http.StatusNotFound {
			t.Fatalf("expected legacy route %s %s to exist, got 404", item.Method, path)
		}
	}
}

func concreteLegacyRoutePath(path string) string {
	path = strings.ReplaceAll(path, "<admin_path>", "localadmin")
	path = strings.ReplaceAll(path, "{method}", "EPay")
	path = strings.ReplaceAll(path, "{uuid}", "uuid123")
	return path
}

func loadLegacyRouteParityFixture(t *testing.T) legacyRouteParityFixture {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime caller unavailable")
	}
	path := filepath.Join(filepath.Dir(file), "testdata", "legacy_route_parity.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var fixture legacyRouteParityFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	return fixture
}
