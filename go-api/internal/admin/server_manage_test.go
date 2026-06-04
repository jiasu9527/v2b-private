package admin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	cfgpkg "forest/go-api/internal/config"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestSortManagedServersUpdatesMultipleTypes(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE v2_server_vmess SET sort = \$2, updated_at = \$3 WHERE id = \$1`).
		WithArgs(int64(3), int64(0), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE v2_server_trojan SET sort = \$2, updated_at = \$3 WHERE id = \$1`).
		WithArgs(int64(5), int64(1), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	service := NewDBService(cfgpkg.Config{}, db)
	ok, err := service.SortManagedServers(context.Background(), map[string]map[int64]int64{
		"vmess":  {3: 0},
		"trojan": {5: 1},
	})
	if err != nil {
		t.Fatalf("sort managed servers: %v", err)
	}
	if !ok {
		t.Fatalf("expected ok")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestNormalizeManagedServerObjectFieldsParsesJSONString(t *testing.T) {
	item := map[string]any{
		"tls_settings":        `{"server_name":"edge.example.com","allow_insecure":"1"}`,
		"network_settings":    `{"path":"/ws"}`,
		"encryption_settings": `{"mode":"native"}`,
		"tlsSettings":         `{"serverName":"legacy.example.com","allowInsecure":"0"}`,
	}

	normalizeManagedServerObjectFields(item)

	for _, key := range []string{"tls_settings", "network_settings", "encryption_settings", "tlsSettings"} {
		value, ok := item[key].(map[string]any)
		if !ok {
			t.Fatalf("expected %s to decode into map, got %#v", key, item[key])
		}
		if len(value) == 0 {
			t.Fatalf("expected %s map to stay non-empty", key)
		}
	}
}

func TestNormalizeManagedServerObjectFieldsKeepsExistingMap(t *testing.T) {
	original := map[string]any{"server_name": "edge.example.com"}
	item := map[string]any{
		"tls_settings": original,
	}

	normalizeManagedServerObjectFields(item)

	got, ok := item["tls_settings"].(map[string]any)
	if !ok {
		t.Fatalf("expected existing map to stay map, got %#v", item["tls_settings"])
	}
	if !jsonEqual(got, original) {
		t.Fatalf("expected existing map to stay unchanged, got %#v", got)
	}
}

func TestV2nodeInstallCommandUsesRepoDefaultInstaller(t *testing.T) {
	root := t.TempDir()

	oldRoot := adminProjectRoot
	adminProjectRoot = root
	defer func() { adminProjectRoot = oldRoot }()

	service := &DBService{cfg: cfgpkg.Config{AppURL: "https://panel.example.com"}}
	command := service.v2nodeInstallCommand(map[string]any{"id": int64(7)})

	if strings.Contains(command, "your-node-installer.example") {
		t.Fatalf("expected default installer placeholder to be removed, got %q", command)
	}
	if strings.Contains(command, "jiasu9527/v2b-private") {
		t.Fatalf("expected node install command to avoid panel repo, got %q", command)
	}
	if !strings.HasPrefix(command, "wget -N https://raw.githubusercontent.com/jiasu9527/v2node/main/script/install.sh && bash install.sh") {
		t.Fatalf("expected v2node repo default installer command, got %q", command)
	}
	if !strings.Contains(command, "--api-host https://panel.example.com") {
		t.Fatalf("expected api host flag in command, got %q", command)
	}
	if !strings.Contains(command, "--node-id 7") {
		t.Fatalf("expected node id flag in command, got %q", command)
	}
}

func jsonEqual(left, right map[string]any) bool {
	leftRaw, _ := json.Marshal(left)
	rightRaw, _ := json.Marshal(right)
	return string(leftRaw) == string(rightRaw)
}

func TestV2nodeInstallCommandSupportsConfiguredScriptURL(t *testing.T) {
	root := t.TempDir()
	writeAdminJSONFixture(t, root, map[string]any{
		"server_api_url":                 "https://api.example.com",
		"server_token":                   "forest-secret",
		"server_node_install_script_url": "https://download.example.com/node/install.sh",
	})

	oldRoot := adminProjectRoot
	adminProjectRoot = root
	defer func() { adminProjectRoot = oldRoot }()

	service := &DBService{cfg: cfgpkg.Config{AppURL: "https://panel.example.com"}}
	command := service.v2nodeInstallCommand(map[string]any{"id": 3})

	if !strings.HasPrefix(command, "wget -N https://download.example.com/node/install.sh && bash install.sh") {
		t.Fatalf("expected configured script url in command, got %q", command)
	}
	if !strings.Contains(command, "--api-host https://api.example.com") {
		t.Fatalf("expected server_api_url override in command, got %q", command)
	}
	if !strings.Contains(command, "--node-id 3") {
		t.Fatalf("expected node id flag in command, got %q", command)
	}
	if !strings.Contains(command, "--api-key forest-secret") {
		t.Fatalf("expected api key flag in command, got %q", command)
	}
}

func TestV2nodeInstallCommandIncludesDDNSArgs(t *testing.T) {
	root := t.TempDir()
	writeAdminJSONFixture(t, root, map[string]any{
		"server_api_url":               "https://api.example.com",
		"server_token":                 "forest-secret-token",
		"server_cf_api_token":          "cf-token",
		"server_cf_zone_id":            "zone-id",
		"server_cf_record_type":        "A",
		"server_cf_ttl":                1,
		"server_cf_proxied":            0,
		"server_ddns_interval":         1,
		"server_block_check_url":       "https://www.baidu.com/",
		"server_block_check_threshold": 3,
		"server_change_ip_wait":        60,
		"server_change_ip_cooldown":    1800,
	})

	oldRoot := adminProjectRoot
	adminProjectRoot = root
	defer func() { adminProjectRoot = oldRoot }()

	service := &DBService{cfg: cfgpkg.Config{AppURL: "https://panel.example.com"}}
	command := service.v2nodeInstallCommand(map[string]any{
		"id":   int64(25),
		"host": "hk.example.com",
		"ddns_settings": map[string]any{
			"enabled":             true,
			"change_ip_curl":      "curl -fsS 'https://provider.example/change?node=hk'",
			"block_check_url":     "https://www.baidu.com/",
			"block_check_keyword": "百度",
		},
	})

	for _, want := range []string{
		"--enable-ddns",
		"--cf-token cf-token",
		"--cf-zone-id zone-id",
		"--cf-record hk.example.com",
		"--ddns-interval 1",
		"--block-check-url https://www.baidu.com/",
		"--block-check-keyword '百度'",
		"--change-ip-curl",
		"https://provider.example/change?node=hk",
	} {
		if !strings.Contains(command, want) {
			t.Fatalf("expected command to contain %q, got %q", want, command)
		}
	}
}

func TestV2nodeInstallCommandSkipsDDNSWhenDisabled(t *testing.T) {
	service := &DBService{cfg: cfgpkg.Config{AppURL: "https://panel.example.com"}}
	command := service.v2nodeInstallCommand(map[string]any{
		"id":            int64(25),
		"host":          "hk.example.com",
		"ddns_settings": map[string]any{"enabled": false},
	})
	if strings.Contains(command, "--enable-ddns") {
		t.Fatalf("expected disabled DDNS to omit args, got %q", command)
	}
}

func TestNormalizeManagedServerSavePayloadV2nodeKeepsSendThrough(t *testing.T) {
	payload := map[string]any{
		"group_id":           []any{float64(1)},
		"name":               "Node-A",
		"host":               "node.example.com",
		"listen_ip":          "0.0.0.0",
		"send_through":       "198.51.100.7",
		"port":               "443",
		"server_port":        float64(8443),
		"rate":               "1",
		"protocol":           "vless",
		"tls":                float64(1),
		"network":            "tcp",
		"disable_sni":        float64(0),
		"zero_rtt_handshake": float64(0),
	}

	_, values, err := normalizeManagedServerSavePayload("v2node", payload)
	if err != nil {
		t.Fatalf("normalizeManagedServerSavePayload() error = %v", err)
	}
	if got := values["send_through"]; got != "198.51.100.7" {
		t.Fatalf("send_through = %#v, want %q", got, "198.51.100.7")
	}
}
