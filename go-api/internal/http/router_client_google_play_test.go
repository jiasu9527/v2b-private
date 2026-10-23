package httpapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"forest/go-api/internal/config"
	"forest/go-api/internal/user"

	"gopkg.in/yaml.v3"
)

type googlePlayTestProfile struct {
	Mode    string   `yaml:"mode"`
	Rules   []string `yaml:"rules"`
	Proxies []struct {
		Name string `yaml:"name"`
	} `yaml:"proxies"`
	Groups []struct {
		Name    string   `yaml:"name"`
		Type    string   `yaml:"type"`
		Proxies []string `yaml:"proxies"`
	} `yaml:"proxy-groups"`
}

func TestRouterClientGooglePlayRuleRouting(t *testing.T) {
	publicDir, err := filepath.Abs("../../../public")
	if err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []struct {
		name, path, template string
	}{
		{"forest_android", "/api/v1/client/app/getConfig?token=test-token", "custom.app.clash.yaml"},
		{"clash_subscription", "/api/v1/client/subscribe?token=test-token&flag=clash", "custom.clash.yaml"},
	} {
		t.Run(endpoint.name, func(t *testing.T) {
			// Read the shipped template, not a fixture copied into this test. Also
			// compare its rule order with the real HTTP response to detect fallback.
			raw, err := os.ReadFile(filepath.Join(publicDir, "..", "resources", "rules", endpoint.template))
			if err != nil {
				t.Fatal(err)
			}
			var template googlePlayTestProfile
			if err := yaml.Unmarshal(raw, &template); err != nil {
				t.Fatal(err)
			}
			service := &fakeUserService{
				resolvedClientUserID: 10,
				subscribe:            user.Subscribe{UUID: "00000000-0000-4000-8000-000000000001"},
				servers: []map[string]any{{
					"type": "trojan", "name": "Test-Proxy", "host": "node.example.test",
					"port": int64(443), "server_name": "node.example.test",
				}},
			}
			router := NewRouter(config.Config{AppName: "Forest", PublicDir: publicDir}, WithUserService(service))
			req := httptest.NewRequest(http.MethodGet, endpoint.path, nil)
			req.Header.Set("User-Agent", "Forest-Android/routing-regression-test")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
			}
			var profile googlePlayTestProfile
			if err := yaml.Unmarshal(rec.Body.Bytes(), &profile); err != nil {
				t.Fatal(err)
			}
			if profile.Mode != "rule" || !reflect.DeepEqual(profile.Rules, template.Rules) {
				t.Fatal("response must preserve the shipped custom template's mode and ordered rules")
			}
			for _, tc := range []struct {
				domain, policy string
			}{
				{"services.googleapis.cn", "国际服务"},
				{"android.clients.services.googleapis.cn", "国际服务"},
				{"xn--ngstr-lra8j.com", "国际服务"},
				{"redirector.xn--ngstr-lra8j.com", "国际服务"},
				{"r1---sn-test.xn--ngstr-lra8j.com", "国际服务"},
				{"play.google.com", "国际服务"},
				{"android.clients.google.com", "国际服务"},
				{"play.googleapis.com", "国际服务"},
				{"redirector.gvt1.com", "国际服务"},
				{"r1---sn-test.gvt1.com", "国际服务"},
				{"redirector.gvt2.com", "国际服务"},
				{"redirector.gvt3.com", "国际服务"},
				{"play-lh.googleusercontent.com", "国际服务"},
				{"lh3.ggpht.com", "国际服务"},
				{"www.baidu.com", "全球直连"},
				{"www.qq.com", "全球直连"},
				{"www.bilibili.com", "全球直连"},
				{"www.12306.cn", "全球直连"},
				{"dns.alidns.com", "全球直连"},
			} {
				t.Run(tc.domain, func(t *testing.T) {
					policy, rule, err := firstGooglePlayTestDomainRule(profile.Rules, tc.domain)
					if err != nil {
						t.Fatal(err)
					}
					if policy != tc.policy {
						t.Fatalf("%s first matches %q: got %s, want %s", tc.domain, rule, policy, tc.policy)
					}
				})
			}
			t.Run("proxy_group_defaults", func(t *testing.T) {
				for _, expected := range []struct {
					name, kind, first string
				}{
					{"国际服务", "select", "节点选择"},
					{"节点选择", "select", "自动选择"},
					{"自动选择", "url-test", "Test-Proxy"},
				} {
					found := false
					for _, group := range profile.Groups {
						if group.Name != expected.name {
							continue
						}
						found = true
						if group.Type != expected.kind || len(group.Proxies) == 0 || group.Proxies[0] != expected.first {
							t.Fatalf("unexpected proxy group default: %#v", group)
						}
						for _, member := range group.Proxies {
							if member == "DIRECT" || member == "全球直连" {
								t.Fatalf("Google Play proxy chain must not offer direct routing: %#v", group)
							}
						}
					}
					if !found {
						t.Fatalf("missing proxy group %q", expected.name)
					}
				}
				if len(profile.Proxies) != 1 || profile.Proxies[0].Name != "Test-Proxy" {
					t.Fatalf("automatic selection must resolve to the emitted proxy, got %#v", profile.Proxies)
				}
			})
		})
	}
}

// This is a deliberately limited ordered-domain-rule regression checker, not
// a Clash/Mihomo kernel or a download/DNS test. A fixed non-LAN destination is
// used only to reject any preceding applicable CIDR rule; no DNS/network runs.
// GEOIP, MATCH or unsupported rules are a hard boundary: the domain must have
// matched explicitly before reaching them, rather than assuming their result.
func firstGooglePlayTestDomainRule(rules []string, domain string) (string, string, error) {
	destination := netip.MustParseAddr("8.8.8.8")
	domain = strings.ToLower(strings.TrimSuffix(domain, "."))
	for _, rule := range rules {
		parts := strings.Split(rule, ",")
		if len(parts) < 3 {
			return "", "", fmt.Errorf("%s reached non-domain boundary %q", domain, rule)
		}
		kind, value, policy := strings.TrimSpace(parts[0]), strings.ToLower(strings.TrimSpace(parts[1])), strings.TrimSpace(parts[2])
		matched := false
		switch kind {
		case "DOMAIN":
			matched = domain == value
		case "DOMAIN-SUFFIX":
			matched = domain == value || strings.HasSuffix(domain, "."+value)
		case "DOMAIN-KEYWORD":
			matched = strings.Contains(domain, value)
		case "IP-CIDR", "IP-CIDR6":
			prefix, err := netip.ParsePrefix(value)
			if err != nil {
				return "", "", err
			}
			if prefix.Contains(destination) {
				return "", "", fmt.Errorf("test destination matches CIDR %q before %s has a domain match", rule, domain)
			}
		default:
			return "", "", fmt.Errorf("%s reached unsupported/non-domain boundary %q", domain, rule)
		}
		if matched {
			return policy, rule, nil
		}
	}
	return "", "", fmt.Errorf("no explicit domain rule matched %s", domain)
}

func TestGooglePlayDomainRuleCheckerBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, domain, want string
		rules              []string
		wantErr            bool
	}{
		{"first_rule_wins", "play.google.com", "DIRECT", []string{"DOMAIN,play.google.com,DIRECT", "DOMAIN-SUFFIX,google.com,PROXY"}, false},
		{"suffix_apex", "google.com", "PROXY", []string{"DOMAIN-SUFFIX,google.com,PROXY"}, false},
		{"suffix_subdomain", "play.google.com", "PROXY", []string{"DOMAIN-SUFFIX,google.com,PROXY"}, false},
		{"keyword", "www.baidu.com", "DIRECT", []string{"DOMAIN-KEYWORD,baidu,DIRECT"}, false},
		{"suffix_label_boundary", "notgoogle.com", "", []string{"DOMAIN-SUFFIX,google.com,PROXY", "GEOIP,CN,DIRECT"}, true},
		{"suffix_must_be_final", "google.com.example.test", "", []string{"DOMAIN-SUFFIX,google.com,PROXY", "MATCH,PROXY"}, true},
		{"geoip_cannot_be_skipped", "play.google.com", "", []string{"GEOIP,CN,DIRECT", "DOMAIN-SUFFIX,google.com,PROXY"}, true},
		{"applicable_cidr_cannot_be_skipped", "play.google.com", "", []string{"IP-CIDR,8.8.8.8/32,DIRECT,no-resolve", "DOMAIN-SUFFIX,google.com,PROXY"}, true},
		{"lan_cidr_does_not_apply", "play.google.com", "PROXY", []string{"IP-CIDR,192.168.0.0/16,DIRECT,no-resolve", "DOMAIN-SUFFIX,google.com,PROXY"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, _, err := firstGooglePlayTestDomainRule(tc.rules, tc.domain)
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("got policy %q / error %v, want %q / error %t", got, err, tc.want, tc.wantErr)
			}
		})
	}
}
