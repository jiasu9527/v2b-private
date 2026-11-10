package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"forest/go-api/internal/config"
)

func TestRouterQuantumultXAnyTLSIgnoresInactiveReality(t *testing.T) {
	for _, kind := range []string{"anytls", "v2node"} {
		for _, mode := range []any{1, "1", float64(1), true, nil} {
			for _, key := range []string{quantumultXTestRealityKey, "inactive-invalid-key"} {
				t.Run(kind+"/"+stringValue(mode)+"/"+key, func(t *testing.T) {
					service := quantumultXHTTPService()
					node := quantumultXNode(map[string]any{
						"type": kind, "protocol": "anytls", "tls": mode,
						"tls_settings": map[string]any{
							"server_name": "tls.example.test", "allow_insecure": false,
							"public_key": key, "short_id": "0123456789abcdef",
						},
					})
					before, _ := json.Marshal(node)
					service.servers = []map[string]any{node}
					router := NewRouter(config.Config{}, WithUserService(service))
					for _, ua := range []string{"Quantumult X/1.5.6", "Quantumult%20X/1.5.6", "Shadowrocket/2.2"} {
						req := httptest.NewRequest(http.MethodGet, "/api/v1/client/subscribe?token=fixture-token", nil)
						req.Header.Set("User-Agent", ua)
						w := httptest.NewRecorder()
						router.ServeHTTP(w, req)
						if w.Code != http.StatusOK {
							t.Fatalf("subscription failed: %d", w.Code)
						}
						body := w.Body.String()
						if strings.HasPrefix(ua, "Shadowrocket") {
							decoded, err := base64.StdEncoding.DecodeString(body)
							if err != nil {
								t.Fatal(err)
							}
							body = string(decoded)
							if !strings.Contains(body, "anytls://"+quantumultXTestUUID+"@192.0.2.7:54101/") || !strings.Contains(body, "sni=tls.example.test") || !strings.Contains(body, "insecure=0") {
								t.Fatalf("generic AnyTLS connection settings changed: %s", body)
							}
						} else {
							for _, want := range []string{"anytls=192.0.2.7:54101,", "password=" + quantumultXTestUUID, "over-tls=true", "tls-host=tls.example.test", "tls-verification=true"} {
								if !strings.Contains(body, want) {
									t.Fatalf("QX lost active connection option %q: %s", want, body)
								}
							}
						}
						if strings.Contains(body, "reality-") || strings.Contains(body, key) {
							t.Fatalf("inactive Reality leaked into %s subscription: %s", ua, body)
						}
						if service.lastServerUA != ua {
							t.Fatal("entry rules did not receive the original UA")
						}
					}
					after, _ := json.Marshal(node)
					if string(before) != string(after) {
						t.Fatal("subscription rendering modified stored node settings")
					}
				})
			}
		}
	}
}
