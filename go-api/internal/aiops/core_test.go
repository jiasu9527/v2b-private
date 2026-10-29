package aiops

import (
	"context"
	"encoding/json"
	"forest/go-api/internal/config"
	"github.com/DATA-DOG/go-sqlmock"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAISecretsAuthenticatedEncryption(t *testing.T) {
	s := NewService(config.Config{AppKey: "test-app-key"}, nil, nil)
	defer s.Close()
	cipher, err := s.encryptSecret("SECRET")
	if err != nil || strings.Contains(cipher, "SECRET") {
		t.Fatal(err)
	}
	plain, err := s.decryptSecret(cipher)
	if err != nil || plain != "SECRET" {
		t.Fatal(err)
	}
	other := NewService(config.Config{AppKey: "other"}, nil, nil)
	if _, err = other.decryptSecret(cipher); err == nil {
		t.Fatal("wrong key accepted")
	}
	empty := NewService(config.Config{}, nil, nil)
	if _, err = empty.encryptSecret("secret"); err == nil {
		t.Fatal("empty APP_KEY accepted")
	}
	if _, err = s.decryptSecret(cipher[:len(cipher)-2] + "xx"); err == nil {
		t.Fatal("tamper accepted")
	}
}
func TestAIProviderURLAndModels(t *testing.T) {
	for _, raw := range []string{"http://evil.test/v1", "https://user:pass@example.test/v1", "https://example.test/v1?token=x", "file:///tmp/test"} {
		if _, err := normalizeBaseURL(raw); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	for _, raw := range []string{"https://relay.test", "https://relay.test/v1", "http://127.0.0.1:9999/v1"} {
		if _, err := normalizeBaseURL(raw); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := normalizeModels([]string{"gpt-6.1-sol", "claude-fable-5-1", "deepseek-v4.1-flash", "glm-5.3"}); err != nil {
		t.Fatal(err)
	}
}
func TestAIProviderExactModelRedirectAndSafeErrors(t *testing.T) {
	for _, mode := range []string{"ok", "redirect", "error"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			leaked := false
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked = true }))
			defer target.Close()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer test-secret" {
					t.Error("wrong request")
				}
				var body completionRequest
				if json.NewDecoder(r.Body).Decode(&body) != nil || body.Model != "claude-fable-5-1" {
					t.Error("model substituted")
				}
				if mode == "redirect" {
					http.Redirect(w, r, target.URL, 307)
					return
				}
				if mode == "error" {
					w.WriteHeader(401)
					w.Write([]byte("upstream echo test-secret"))
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
			}))
			defer srv.Close()
			db, mock, _ := sqlmock.New()
			defer db.Close()
			s := NewService(config.Config{AppKey: "test"}, db, nil)
			cipher, _ := s.encryptSecret("test-secret")
			mock.ExpectQuery(`SELECT enabled,base_url,models::TEXT,api_key_cipher`).WillReturnRows(sqlmock.NewRows([]string{"enabled", "base_url", "models", "key"}).AddRow(true, srv.URL+"/v1", `["claude-fable-5-1"]`, cipher))
			msg, err := s.complete(context.Background(), "claude-fable-5-1", []providerMessage{{Role: "user", Content: "hello"}})
			if mode == "ok" && (err != nil || msg.Content != "ok") {
				t.Fatal(err)
			}
			if mode != "ok" && (err == nil || strings.Contains(err.Error(), "test-secret")) {
				t.Fatal("unsafe error")
			}
			if calls != 1 || leaked {
				t.Fatal("redirect followed")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
