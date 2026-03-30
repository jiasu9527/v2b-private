package httpapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"forest/go-api/internal/config"
)

func TestRouterCriticalCompatibilityEndpointsExist(t *testing.T) {
	router := NewRouter(config.Config{AppName: "forest-go", AdminPath: "localadmin", TelegramBotToken: "bot-token"})

	cases := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/client/app/getVersion"},
		{http.MethodGet, "/api/v1/client/app/getConfig"},
		{http.MethodGet, "/api/v1/client/subscribe"},
		{http.MethodGet, "/api/v1/user/server/fetch"},
		{http.MethodGet, "/api/v1/user/telegram/getBotInfo"},
		{http.MethodGet, "/api/v2/server/config"},
		{http.MethodGet, "/api/v1/server/UniProxy/user"},
		{http.MethodGet, "/api/v1/server/UniProxy/config"},
		{http.MethodPost, "/api/v1/server/UniProxy/push"},
		{http.MethodGet, "/api/v1/server/Deepbwork/user"},
		{http.MethodGet, "/api/v1/server/Deepbwork/config"},
		{http.MethodPost, "/api/v1/server/Deepbwork/submit"},
		{http.MethodGet, "/api/v1/server/ShadowsocksTidalab/user"},
		{http.MethodPost, "/api/v1/server/ShadowsocksTidalab/submit"},
		{http.MethodGet, "/api/v1/server/TrojanTidalab/user"},
		{http.MethodGet, "/api/v1/server/TrojanTidalab/config"},
		{http.MethodPost, "/api/v1/server/TrojanTidalab/submit"},
		{http.MethodPost, "/api/v1/guest/telegram/webhook?access_token=wrong"},
		{http.MethodPost, "/api/v1/localadmin/user/setInviteUser"},
	}

	for _, tc := range cases {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code == http.StatusNotFound {
			t.Fatalf("expected route %s %s to exist, got 404", tc.method, tc.path)
		}
	}
}

func TestGoAPIDocsListCriticalCompatibilityEndpoints(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime caller unavailable")
	}

	docsPath := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "..", "docs", "go-api.md"))
	raw, err := os.ReadFile(docsPath)
	if err != nil {
		t.Fatalf("read docs: %v", err)
	}
	content := string(raw)

	expected := []string{
		"/api/v1/client/app/getVersion",
		"/api/v1/client/app/getConfig",
		"/api/v1/client/subscribe",
		"/api/v1/user/server/fetch",
		"/api/v1/user/telegram/getBotInfo",
		"/api/v1/server/UniProxy/user",
		"/api/v1/server/UniProxy/config",
		"/api/v1/server/UniProxy/alivelist",
		"/api/v1/server/UniProxy/alive",
		"/api/v1/server/UniProxy/push",
		"/api/v1/server/Deepbwork/user",
		"/api/v1/server/Deepbwork/config",
		"/api/v1/server/Deepbwork/submit",
		"/api/v1/server/ShadowsocksTidalab/user",
		"/api/v1/server/ShadowsocksTidalab/submit",
		"/api/v1/server/TrojanTidalab/user",
		"/api/v1/server/TrojanTidalab/config",
		"/api/v1/server/TrojanTidalab/submit",
		"/api/v2/server/config",
	}

	for _, endpoint := range expected {
		if !strings.Contains(content, endpoint) {
			t.Fatalf("expected docs to list endpoint %s", endpoint)
		}
	}
}
