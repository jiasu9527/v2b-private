package admin

import (
	"context"
	"crypto/sha256"
	"database/sql/driver"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestDNSFailoverSettingsValidateNormalizeAndPersistProbeAPIURL(t *testing.T) {
	root := t.TempDir()
	oldRoot := adminProjectRoot
	adminProjectRoot = root
	t.Cleanup(func() { adminProjectRoot = oldRoot })

	service := &DBService{}
	for _, rawURL := range []string{
		"http://probe.example.com",
		"ftp://probe.example.com",
		"https://probe.example.com/path?token=secret",
	} {
		if _, err := service.SaveDNSFailoverSettings(context.Background(), DNSFailoverSettingsSaveRequest{ProbeAPIURL: rawURL}); err == nil || !strings.Contains(err.Error(), "探针接入地址") {
			t.Fatalf("SaveDNSFailoverSettings(%q) error = %v, want actionable Chinese validation error", rawURL, err)
		}
	}

	for _, rawURL := range []string{
		"https://probe.example.com///",
		"http://localhost:8080/",
		"http://127.0.0.1:8080/",
	} {
		settings, err := service.SaveDNSFailoverSettings(context.Background(), DNSFailoverSettingsSaveRequest{ProbeAPIURL: rawURL})
		if err != nil {
			t.Fatalf("SaveDNSFailoverSettings(%q): %v", rawURL, err)
		}
		want := strings.TrimRight(rawURL, "/")
		if settings.ProbeAPIURL != want {
			t.Fatalf("normalized URL = %q, want %q", settings.ProbeAPIURL, want)
		}
		loaded, err := service.GetDNSFailoverSettings(context.Background())
		if err != nil {
			t.Fatalf("GetDNSFailoverSettings: %v", err)
		}
		if loaded != settings {
			t.Fatalf("loaded settings = %#v, want %#v", loaded, settings)
		}
	}

	raw, err := os.ReadFile(filepath.Join(root, "config", "admin.json"))
	if err != nil {
		t.Fatalf("read persisted settings: %v", err)
	}
	var persisted map[string]any
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatalf("decode persisted settings: %v", err)
	}
	if persisted[dnsProbeAPIURLKey] != "http://127.0.0.1:8080" {
		t.Fatalf("persisted dns_probe_api_url = %#v", persisted[dnsProbeAPIURLKey])
	}
}

type dnsProbeTokenHashMatcher struct {
	value string
}

func (matcher *dnsProbeTokenHashMatcher) Match(value driver.Value) bool {
	text, ok := value.(string)
	if !ok || len(text) != sha256.Size*2 {
		return false
	}
	if _, err := hex.DecodeString(text); err != nil {
		return false
	}
	matcher.value = text
	return true
}

func TestDNSFailoverProbeSecretIsReturnedOnceAndOnlyHashIsStored(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	service := &DBService{db: db, dnsFailoverSchemaOK: true}
	hashMatcher := &dnsProbeTokenHashMatcher{}
	mock.ExpectQuery(`INSERT INTO v2_dns_probe \(name, token_hash, enabled, created_at, updated_at\)\s+VALUES \(\$1, \$2, 1, \$3, \$4\)\s+RETURNING id, created_at, updated_at`).
		WithArgs("北京探针", hashMatcher, sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(int64(7), int64(100), int64(100)))

	created, err := service.CreateDNSProbe(context.Background(), DNSProbeCreateRequest{Name: " 北京探针 "})
	if err != nil {
		t.Fatalf("CreateDNSProbe: %v", err)
	}
	secretBytes, err := base64.RawURLEncoding.DecodeString(created.Secret)
	if err != nil {
		t.Fatalf("probe secret is not raw URL-safe base64: %v", err)
	}
	if len(secretBytes) != 32 {
		t.Fatalf("probe secret decoded length = %d, want 32", len(secretBytes))
	}
	wantHashBytes := sha256.Sum256([]byte(created.Secret))
	if hashMatcher.value != hex.EncodeToString(wantHashBytes[:]) {
		t.Fatalf("stored hash %q does not match returned secret", hashMatcher.value)
	}
	if created.Probe.ID != 7 || created.Probe.Name != "北京探针" || !created.Probe.Enabled {
		t.Fatalf("unexpected created probe: %#v", created.Probe)
	}

	mock.ExpectQuery(`SELECT id, name, enabled, version, arch, public_ip, last_heartbeat_at, prewarm_count, created_at, updated_at\s+FROM v2_dns_probe\s+ORDER BY id ASC`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "enabled", "version", "arch", "public_ip", "last_heartbeat_at", "prewarm_count", "created_at", "updated_at"}).
			AddRow(int64(7), "北京探针", int64(1), "v1.2.3", "amd64", "203.0.113.7", nil, int64(0), int64(100), int64(100)))
	probes, err := service.ListDNSProbes(context.Background())
	if err != nil {
		t.Fatalf("ListDNSProbes: %v", err)
	}
	if len(probes) != 1 || probes[0].ID != 7 || probes[0].Name != "北京探针" {
		t.Fatalf("unexpected probes: %#v", probes)
	}
	encoded, err := json.Marshal(probes)
	if err != nil {
		t.Fatalf("marshal probes: %v", err)
	}
	if strings.Contains(string(encoded), created.Secret) || strings.Contains(string(encoded), hashMatcher.value) || strings.Contains(string(encoded), "token_hash") {
		t.Fatalf("probe list leaked secret material: %s", encoded)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestDNSFailoverProbeListAndRevoke(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	service := &DBService{db: db, dnsFailoverSchemaOK: true}
	mock.ExpectQuery(`SELECT id, name, enabled, version, arch, public_ip, last_heartbeat_at, prewarm_count, created_at, updated_at\s+FROM v2_dns_probe\s+ORDER BY id ASC`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "enabled", "version", "arch", "public_ip", "last_heartbeat_at", "prewarm_count", "created_at", "updated_at"}).
			AddRow(int64(3), "上海探针", int64(1), "", "", "", int64(90), int64(3), int64(10), int64(90)))
	probes, err := service.ListDNSProbes(context.Background())
	if err != nil {
		t.Fatalf("ListDNSProbes: %v", err)
	}
	if len(probes) != 1 || probes[0].LastHeartbeatAt == nil || *probes[0].LastHeartbeatAt != 90 {
		t.Fatalf("unexpected probes: %#v", probes)
	}

	mock.ExpectExec(`UPDATE v2_dns_probe SET enabled = \$2, updated_at = \$3 WHERE id = \$1`).
		WithArgs(int64(3), int64(0), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	if ok, err := service.SetDNSProbeEnabled(context.Background(), 3, false); err != nil || !ok {
		t.Fatalf("SetDNSProbeEnabled revoke = %v, %v", ok, err)
	}

	mock.ExpectExec(`UPDATE v2_dns_probe SET enabled = \$2, updated_at = \$3 WHERE id = \$1`).
		WithArgs(int64(3), int64(1), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	if ok, err := service.SetDNSProbeEnabled(context.Background(), 3, true); err != nil || !ok {
		t.Fatalf("SetDNSProbeEnabled enable = %v, %v", ok, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func validDNSFailoverRuleSaveRequest() DNSFailoverRuleSaveRequest {
	weight := int64(10)
	return DNSFailoverRuleSaveRequest{
		Name:                        "主站故障转移",
		DomainID:                    101,
		Domain:                      "Example.COM.",
		RecordID:                    202,
		Subdomain:                   "www",
		RecordLineID:                "0=0",
		RecordLineName:              "默认",
		TTL:                         600,
		MX:                          0,
		Weight:                      &weight,
		Enabled:                     true,
		AutoFailback:                true,
		CheckIntervalSec:            30,
		TCPTimeoutMS:                3000,
		FailureThreshold:            3,
		SuccessThreshold:            6,
		SingleProbeFailureThreshold: 5,
		SingleProbeSuccessThreshold: 8,
		ProbeOfflineSec:             90,
		CooldownSec:                 300,
		ProbeIDs:                    []int64{9, 4, 9},
		Targets: []DNSFailoverTargetSaveRequest{
			{Sort: 20, Name: "备用域名", DNSType: " cname ", DNSValue: "Backup.Example.COM.", CheckHost: "Backup.Example.COM.", CheckPort: 443, Enabled: true},
			{Sort: 10, Name: "停用旧地址", DNSType: "A", DNSValue: "192.0.2.10", CheckHost: "192.0.2.10", CheckPort: 443, Enabled: false},
			{Sort: 15, Name: "IPv6", DNSType: "aaaa", DNSValue: "2001:0db8::1", CheckHost: "2001:db8::1", CheckPort: 443, Enabled: true},
		},
	}
}

func TestDNSFailoverRuleValidationNormalizesTargetTypesAndRejectsBadValues(t *testing.T) {
	request := validDNSFailoverRuleSaveRequest()
	if err := normalizeDNSFailoverRuleSaveRequest(&request); err != nil {
		t.Fatalf("normalize valid rule: %v", err)
	}
	if request.Domain != "example.com" {
		t.Fatalf("normalized domain = %q", request.Domain)
	}
	if request.Targets[0].Sort != 10 || request.Targets[1].Sort != 15 || request.Targets[2].Sort != 20 {
		t.Fatalf("targets were not sorted: %#v", request.Targets)
	}
	if request.Targets[1].DNSValue != "2001:db8::1" || request.Targets[2].DNSValue != "backup.example.com" || request.Targets[2].CheckHost != "backup.example.com" {
		t.Fatalf("targets were not normalized: %#v", request.Targets)
	}
	if len(request.ProbeIDs) != 2 || request.ProbeIDs[0] != 4 || request.ProbeIDs[1] != 9 {
		t.Fatalf("probe IDs were not sorted/deduplicated: %#v", request.ProbeIDs)
	}

	tests := []struct {
		name   string
		mutate func(*DNSFailoverRuleSaveRequest)
		want   string
	}{
		{name: "A rejects IPv6", mutate: func(req *DNSFailoverRuleSaveRequest) {
			req.Targets[0].DNSType, req.Targets[0].DNSValue = "A", "2001:db8::1"
		}, want: "IPv4"},
		{name: "AAAA rejects IPv4", mutate: func(req *DNSFailoverRuleSaveRequest) {
			req.Targets[0].DNSType, req.Targets[0].DNSValue = "AAAA", "192.0.2.1"
		}, want: "IPv6"},
		{name: "CNAME rejects scheme", mutate: func(req *DNSFailoverRuleSaveRequest) {
			req.Targets[0].DNSType, req.Targets[0].DNSValue = "CNAME", "https://backup.example.com"
		}, want: "CNAME"},
		{name: "CNAME rejects port", mutate: func(req *DNSFailoverRuleSaveRequest) {
			req.Targets[0].DNSType, req.Targets[0].DNSValue = "CNAME", "backup.example.com:443"
		}, want: "CNAME"},
		{name: "duplicate sort", mutate: func(req *DNSFailoverRuleSaveRequest) { req.Targets[1].Sort = req.Targets[0].Sort }, want: "排序"},
		{name: "bad port", mutate: func(req *DNSFailoverRuleSaveRequest) { req.Targets[0].CheckPort = 65536 }, want: "端口"},
		{name: "no enabled target", mutate: func(req *DNSFailoverRuleSaveRequest) {
			for i := range req.Targets {
				req.Targets[i].Enabled = false
			}
		}, want: "启用目标"},
		{name: "threshold relation", mutate: func(req *DNSFailoverRuleSaveRequest) { req.SingleProbeFailureThreshold = req.FailureThreshold }, want: "单探针"},
		{name: "no probes", mutate: func(req *DNSFailoverRuleSaveRequest) { req.ProbeIDs = nil }, want: "探针"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := validDNSFailoverRuleSaveRequest()
			test.mutate(&request)
			if err := normalizeDNSFailoverRuleSaveRequest(&request); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("normalize error = %v, want message containing %q", err, test.want)
			}
		})
	}
}

func TestDNSFailoverRuleValidationRejectsPostgresIntegerOverflow(t *testing.T) {
	tooLarge := int64(1 << 31)
	for _, test := range []struct {
		name   string
		mutate func(*DNSFailoverRuleSaveRequest)
		want   string
	}{
		{name: "ttl", mutate: func(request *DNSFailoverRuleSaveRequest) { request.TTL = tooLarge }, want: "2147483647"},
		{name: "mx", mutate: func(request *DNSFailoverRuleSaveRequest) { request.MX = tooLarge }, want: "2147483647"},
		{name: "weight", mutate: func(request *DNSFailoverRuleSaveRequest) { request.Weight = &tooLarge }, want: "2147483647"},
		{name: "check interval", mutate: func(request *DNSFailoverRuleSaveRequest) { request.CheckIntervalSec = tooLarge }, want: "2147483647"},
		{name: "tcp timeout", mutate: func(request *DNSFailoverRuleSaveRequest) { request.TCPTimeoutMS = tooLarge }, want: "2147483647"},
		{name: "failure threshold", mutate: func(request *DNSFailoverRuleSaveRequest) { request.FailureThreshold = tooLarge }, want: "2147483647"},
		{name: "success threshold", mutate: func(request *DNSFailoverRuleSaveRequest) { request.SuccessThreshold = tooLarge }, want: "2147483647"},
		{name: "single failure threshold", mutate: func(request *DNSFailoverRuleSaveRequest) { request.SingleProbeFailureThreshold = tooLarge }, want: "2147483647"},
		{name: "single success threshold", mutate: func(request *DNSFailoverRuleSaveRequest) { request.SingleProbeSuccessThreshold = tooLarge }, want: "2147483647"},
		{name: "probe offline", mutate: func(request *DNSFailoverRuleSaveRequest) { request.ProbeOfflineSec = tooLarge }, want: "2147483647"},
		{name: "cooldown", mutate: func(request *DNSFailoverRuleSaveRequest) { request.CooldownSec = tooLarge }, want: "2147483647"},
		{name: "target sort", mutate: func(request *DNSFailoverRuleSaveRequest) { request.Targets[0].Sort = tooLarge }, want: "2147483647"},
		{name: "target port", mutate: func(request *DNSFailoverRuleSaveRequest) { request.Targets[0].CheckPort = tooLarge }, want: "65535"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := validDNSFailoverRuleSaveRequest()
			test.mutate(&request)
			if err := normalizeDNSFailoverRuleSaveRequest(&request); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("normalize error = %v, want upper-bound message containing %q", err, test.want)
			}
		})
	}
}

func TestDNSFailoverRuleValidationRejectsOversizedDNSPodRecordFieldsByRuneCount(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*DNSFailoverRuleSaveRequest)
		want   string
	}{
		{name: "subdomain", mutate: func(request *DNSFailoverRuleSaveRequest) { request.Subdomain = strings.Repeat("测", 256) }, want: "主机记录"},
		{name: "record line id", mutate: func(request *DNSFailoverRuleSaveRequest) { request.RecordLineID = strings.Repeat("线", 256) }, want: "线路 ID"},
		{name: "record line name", mutate: func(request *DNSFailoverRuleSaveRequest) { request.RecordLineName = strings.Repeat("线", 256) }, want: "线路名称"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := validDNSFailoverRuleSaveRequest()
			test.mutate(&request)
			if err := normalizeDNSFailoverRuleSaveRequest(&request); err == nil || !strings.Contains(err.Error(), test.want) || !strings.Contains(err.Error(), "255") {
				t.Fatalf("normalize error = %v, want %s UTF-8 rune length rejection", err, test.want)
			}
		})
	}
}

func TestDNSFailoverTargetValueUsesStrictIPTextVersions(t *testing.T) {
	for _, value := range []string{"::ffff:192.0.2.1", "::ffff:c000:201"} {
		if normalized, err := normalizeDNSFailoverTargetValue("A", value); err == nil {
			t.Fatalf("A mapped IPv6 %q normalized to %q, want rejection", value, normalized)
		}
	}
	if normalized, err := normalizeDNSFailoverTargetValue("AAAA", "::ffff:192.0.2.1"); err == nil {
		t.Fatalf("AAAA mapped IPv6 normalized to %q, want rejection", normalized)
	}
	if normalized, err := normalizeDNSFailoverTargetValue("AAAA", "2001:0DB8:0:0:0:0:0:1"); err != nil || normalized != "2001:db8::1" {
		t.Fatalf("AAAA normalization = %q, %v, want 2001:db8::1", normalized, err)
	}
}

func TestDNSFailoverCreateRuleSavesSortedTargetsAndProbeBindingsTransactionally(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := &DBService{db: db, dnsFailoverSchemaOK: true}

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id FROM v2_dns_probe WHERE enabled = 1 AND id IN \(\$1, \$2\) ORDER BY id ASC FOR SHARE`).
		WithArgs(int64(4), int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(4)).AddRow(int64(9)))
	mock.ExpectQuery(`INSERT INTO v2_dns_failover_group .*current_target_id.*VALUES .*NULL.*RETURNING id, created_at, updated_at`).
		WithArgs(
			"主站故障转移", int64(101), "example.com", int64(202), "www", "0=0", "默认", int64(600), int64(0), int64(10),
			int64(1), int64(1), int64(30), int64(3000), int64(3), int64(6), int64(5), int64(8), int64(90), int64(300),
			sqlmock.AnyArg(), sqlmock.AnyArg(),
		).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(int64(50), int64(100), int64(100)))
	mock.ExpectQuery(`INSERT INTO v2_dns_failover_target .*RETURNING id, created_at, updated_at`).
		WithArgs(int64(50), int64(10), "停用旧地址", "A", "192.0.2.10", "192.0.2.10", int64(443), int64(0), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(int64(70), int64(100), int64(100)))
	mock.ExpectQuery(`INSERT INTO v2_dns_failover_target .*RETURNING id, created_at, updated_at`).
		WithArgs(int64(50), int64(15), "IPv6", "AAAA", "2001:db8::1", "2001:db8::1", int64(443), int64(1), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(int64(71), int64(100), int64(100)))
	mock.ExpectQuery(`INSERT INTO v2_dns_failover_target .*RETURNING id, created_at, updated_at`).
		WithArgs(int64(50), int64(20), "备用域名", "CNAME", "backup.example.com", "backup.example.com", int64(443), int64(1), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(int64(72), int64(100), int64(100)))
	mock.ExpectExec(`UPDATE v2_dns_failover_group SET current_target_id = \$2, updated_at = \$3 WHERE id = \$1`).
		WithArgs(int64(50), int64(71), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO v2_dns_failover_group_probe \(group_id, probe_id, created_at, updated_at\) VALUES \(\$1, \$2, \$3, \$4\)`).
		WithArgs(int64(50), int64(4), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`INSERT INTO v2_dns_failover_group_probe \(group_id, probe_id, created_at, updated_at\) VALUES \(\$1, \$2, \$3, \$4\)`).
		WithArgs(int64(50), int64(9), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(2, 1))
	mock.ExpectCommit()

	created, err := service.SaveDNSFailoverRule(context.Background(), validDNSFailoverRuleSaveRequest())
	if err != nil {
		t.Fatalf("SaveDNSFailoverRule create: %v", err)
	}
	if created.ID != 50 || created.CurrentTargetID == nil || *created.CurrentTargetID != 71 {
		t.Fatalf("unexpected created rule: %#v", created)
	}
	if len(created.Targets) != 3 || created.Targets[0].ID != 70 || created.Targets[2].DNSValue != "backup.example.com" {
		t.Fatalf("unexpected created targets: %#v", created.Targets)
	}
	if len(created.ProbeIDs) != 2 || created.ProbeIDs[0] != 4 || created.ProbeIDs[1] != 9 {
		t.Fatalf("unexpected created probe bindings: %#v", created.ProbeIDs)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func dnsFailoverSingleTargetCreateRequest() DNSFailoverRuleSaveRequest {
	request := validDNSFailoverRuleSaveRequest()
	request.ProbeIDs = []int64{4}
	request.Targets = []DNSFailoverTargetSaveRequest{
		{Sort: 10, Name: "主目标", DNSType: "A", DNSValue: "192.0.2.10", CheckHost: "192.0.2.10", CheckPort: 443, Enabled: true},
	}
	return request
}

func expectDNSFailoverCreateRuleStart(mock sqlmock.Sqlmock) {
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id FROM v2_dns_probe WHERE enabled = 1 AND id IN \(\$1\) ORDER BY id ASC FOR SHARE`).
		WithArgs(int64(4)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(4)))
	mock.ExpectQuery(`INSERT INTO v2_dns_failover_group .*RETURNING id, created_at, updated_at`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(int64(50), int64(100), int64(100)))
}

func TestDNSFailoverCreateRuleRollsBackAndPreservesTargetWriteError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := &DBService{db: db, dnsFailoverSchemaOK: true}
	dbErr := errors.New("target insert failed")

	expectDNSFailoverCreateRuleStart(mock)
	mock.ExpectQuery(`INSERT INTO v2_dns_failover_target .*RETURNING id, created_at, updated_at`).WillReturnError(dbErr)
	mock.ExpectRollback()

	if _, err := service.SaveDNSFailoverRule(context.Background(), dnsFailoverSingleTargetCreateRequest()); err == nil || !errors.Is(err, dbErr) || !strings.Contains(err.Error(), "目标") {
		t.Fatalf("SaveDNSFailoverRule target error = %v, want wrapped target write error", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestDNSFailoverCreateRuleRollsBackAndPreservesCurrentTargetError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := &DBService{db: db, dnsFailoverSchemaOK: true}
	dbErr := errors.New("current target update failed")

	expectDNSFailoverCreateRuleStart(mock)
	mock.ExpectQuery(`INSERT INTO v2_dns_failover_target .*RETURNING id, created_at, updated_at`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(int64(71), int64(110), int64(110)))
	mock.ExpectExec(`UPDATE v2_dns_failover_group SET current_target_id = \$2, updated_at = \$3 WHERE id = \$1`).WillReturnError(dbErr)
	mock.ExpectRollback()

	if _, err := service.SaveDNSFailoverRule(context.Background(), dnsFailoverSingleTargetCreateRequest()); err == nil || !errors.Is(err, dbErr) || !strings.Contains(err.Error(), "当前") {
		t.Fatalf("SaveDNSFailoverRule current target error = %v, want wrapped current target error", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestDNSFailoverCreateRuleRollsBackAndPreservesProbeBindingError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := &DBService{db: db, dnsFailoverSchemaOK: true}
	dbErr := errors.New("probe binding insert failed")

	expectDNSFailoverCreateRuleStart(mock)
	mock.ExpectQuery(`INSERT INTO v2_dns_failover_target .*RETURNING id, created_at, updated_at`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(int64(71), int64(110), int64(110)))
	mock.ExpectExec(`UPDATE v2_dns_failover_group SET current_target_id = \$2, updated_at = \$3 WHERE id = \$1`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO v2_dns_failover_group_probe .*VALUES`).WillReturnError(dbErr)
	mock.ExpectRollback()

	if _, err := service.SaveDNSFailoverRule(context.Background(), dnsFailoverSingleTargetCreateRequest()); err == nil || !errors.Is(err, dbErr) || !strings.Contains(err.Error(), "探针绑定") {
		t.Fatalf("SaveDNSFailoverRule probe binding error = %v, want wrapped binding error", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func dnsFailoverGroupRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "name", "domain_id", "domain", "record_id", "subdomain", "record_line_id", "record_line_name", "ttl", "mx", "weight", "current_target_id",
		"enabled", "auto_failback", "check_interval_sec", "tcp_timeout_ms", "failure_threshold", "success_threshold", "single_probe_failure_threshold",
		"single_probe_success_threshold", "probe_offline_sec", "cooldown_sec", "last_switch_at", "last_switch_reason", "created_at", "updated_at",
	}).AddRow(
		int64(50), "主站故障转移", int64(101), "example.com", int64(202), "www", "0=0", "默认", int64(600), int64(0), int64(10), int64(71),
		int64(1), int64(1), int64(30), int64(3000), int64(3), int64(6), int64(5), int64(8), int64(90), int64(300), nil, "", int64(100), int64(200),
	)
}

func expectDNSFailoverRuleRelations(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(`SELECT id, group_id, sort, name, dns_type, dns_value, check_host, check_port, enabled, created_at, updated_at\s+FROM v2_dns_failover_target\s+WHERE group_id IN \(\$1\)\s+ORDER BY group_id ASC, sort ASC, id ASC`).
		WithArgs(int64(50)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "group_id", "sort", "name", "dns_type", "dns_value", "check_host", "check_port", "enabled", "created_at", "updated_at"}).
			AddRow(int64(71), int64(50), int64(15), "IPv6", "AAAA", "2001:db8::1", "2001:db8::1", int64(443), int64(1), int64(100), int64(200)).
			AddRow(int64(72), int64(50), int64(20), "备用域名", "CNAME", "backup.example.com", "backup.example.com", int64(443), int64(1), int64(100), int64(200)))
	mock.ExpectQuery(`SELECT group_id, probe_id\s+FROM v2_dns_failover_group_probe\s+WHERE group_id IN \(\$1\)\s+ORDER BY group_id ASC, probe_id ASC`).
		WithArgs(int64(50)).
		WillReturnRows(sqlmock.NewRows([]string{"group_id", "probe_id"}).AddRow(int64(50), int64(4)).AddRow(int64(50), int64(9)))
}

func TestDNSFailoverRuleListAndDetailIncludeSortedTargetsAndProbeBindings(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := &DBService{db: db, dnsFailoverSchemaOK: true}

	mock.ExpectQuery(`SELECT .* FROM v2_dns_failover_group ORDER BY id ASC`).WillReturnRows(dnsFailoverGroupRows())
	expectDNSFailoverRuleRelations(mock)
	rules, err := service.ListDNSFailoverRules(context.Background())
	if err != nil {
		t.Fatalf("ListDNSFailoverRules: %v", err)
	}
	if len(rules) != 1 || len(rules[0].Targets) != 2 || rules[0].Targets[0].ID != 71 || len(rules[0].ProbeIDs) != 2 {
		t.Fatalf("unexpected rules: %#v", rules)
	}

	mock.ExpectQuery(`SELECT .* FROM v2_dns_failover_group WHERE id = \$1 ORDER BY id ASC`).WithArgs(int64(50)).WillReturnRows(dnsFailoverGroupRows())
	expectDNSFailoverRuleRelations(mock)
	rule, err := service.GetDNSFailoverRule(context.Background(), 50)
	if err != nil {
		t.Fatalf("GetDNSFailoverRule: %v", err)
	}
	if rule.ID != 50 || rule.CurrentTargetID == nil || *rule.CurrentTargetID != 71 || rule.Weight == nil || *rule.Weight != 10 {
		t.Fatalf("unexpected rule detail: %#v", rule)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func dnsFailoverUpdateRequestKeepingCurrent() DNSFailoverRuleSaveRequest {
	id := int64(50)
	request := validDNSFailoverRuleSaveRequest()
	request.ID = &id
	request.ProbeIDs = []int64{4}
	request.Targets = []DNSFailoverTargetSaveRequest{
		{ID: 71, Sort: 15, Name: "当前 IPv6", DNSType: "AAAA", DNSValue: "2001:db8::1", CheckHost: "2001:db8::1", CheckPort: 443, Enabled: true},
		{ID: 72, Sort: 20, Name: "备用域名", DNSType: "CNAME", DNSValue: "backup.example.com", CheckHost: "backup.example.com", CheckPort: 443, Enabled: true},
	}
	return request
}

func expectDNSFailoverUpdateLocks(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(`SELECT current_target_id, created_at FROM v2_dns_failover_group WHERE id = \$1 FOR UPDATE`).
		WithArgs(int64(50)).
		WillReturnRows(sqlmock.NewRows([]string{"current_target_id", "created_at"}).AddRow(int64(71), int64(100)))
	mock.ExpectQuery(`SELECT id, sort, dns_type, dns_value, check_host, check_port, enabled, created_at FROM v2_dns_failover_target WHERE group_id = \$1 ORDER BY id ASC FOR UPDATE`).
		WithArgs(int64(50)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "sort", "dns_type", "dns_value", "check_host", "check_port", "enabled", "created_at"}).
			AddRow(int64(71), int64(15), "AAAA", "2001:db8::1", "2001:db8::1", int64(443), int64(1), int64(110)).
			AddRow(int64(72), int64(20), "CNAME", "backup.example.com", "backup.example.com", int64(443), int64(1), int64(120)))
}

func TestDNSFailoverUpdateRuleRejectsRemovingOrDisablingCurrentTarget(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*DNSFailoverRuleSaveRequest)
		want   string
	}{
		{name: "remove", mutate: func(request *DNSFailoverRuleSaveRequest) { request.Targets = request.Targets[1:] }, want: "删除"},
		{name: "disable", mutate: func(request *DNSFailoverRuleSaveRequest) { request.Targets[0].Enabled = false }, want: "停用"},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			defer db.Close()
			service := &DBService{db: db, dnsFailoverSchemaOK: true}
			request := dnsFailoverUpdateRequestKeepingCurrent()
			test.mutate(&request)

			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT id FROM v2_dns_probe WHERE enabled = 1 AND id IN \(\$1\) ORDER BY id ASC FOR SHARE`).WithArgs(int64(4)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(4)))
			expectDNSFailoverUpdateLocks(mock)
			mock.ExpectRollback()

			if _, err := service.SaveDNSFailoverRule(context.Background(), request); err == nil || !strings.Contains(err.Error(), "当前目标") || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("SaveDNSFailoverRule error = %v, want current target %s rejection", err, test.want)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("sql expectations: %v", err)
			}
		})
	}
}

func TestDNSFailoverUpdateRuleRejectsCurrentTargetCriticalFieldChanges(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*DNSFailoverTargetSaveRequest)
	}{
		{name: "dns type", mutate: func(target *DNSFailoverTargetSaveRequest) { target.DNSType, target.DNSValue = "A", "192.0.2.1" }},
		{name: "dns value", mutate: func(target *DNSFailoverTargetSaveRequest) { target.DNSValue = "2001:db8::2" }},
		{name: "check host", mutate: func(target *DNSFailoverTargetSaveRequest) { target.CheckHost = "check.example.com" }},
		{name: "check port", mutate: func(target *DNSFailoverTargetSaveRequest) { target.CheckPort = 8443 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			defer db.Close()
			service := &DBService{db: db, dnsFailoverSchemaOK: true}
			request := dnsFailoverUpdateRequestKeepingCurrent()
			test.mutate(&request.Targets[0])

			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT id FROM v2_dns_probe WHERE enabled = 1 AND id IN \(\$1\) ORDER BY id ASC FOR SHARE`).WithArgs(int64(4)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(4)))
			expectDNSFailoverUpdateLocks(mock)
			mock.ExpectRollback()

			if _, err := service.SaveDNSFailoverRule(context.Background(), request); err == nil || !strings.Contains(err.Error(), "当前目标") || !strings.Contains(err.Error(), "关键字段") {
				t.Fatalf("SaveDNSFailoverRule error = %v, want current target critical field rejection", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("sql expectations: %v", err)
			}
		})
	}
}

func TestDNSFailoverUpdateRuleAllowsCurrentTargetSortAndNameAndPreservesCreatedAt(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := &DBService{db: db, dnsFailoverSchemaOK: true}

	request := dnsFailoverUpdateRequestKeepingCurrent()
	request.Targets[0].Sort = 12
	request.Targets[0].Name = "当前 IPv6（改名）"

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id FROM v2_dns_probe WHERE enabled = 1 AND id IN \(\$1\) ORDER BY id ASC FOR SHARE`).WithArgs(int64(4)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(4)))
	expectDNSFailoverUpdateLocks(mock)
	mock.ExpectExec(`UPDATE v2_dns_failover_group SET name = \$2,.*updated_at = \$22 WHERE id = \$1`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE v2_dns_failover_target SET sort = sort \+ \$2, updated_at = \$3 WHERE group_id = \$1`).WithArgs(int64(50), sqlmock.AnyArg(), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(`UPDATE v2_dns_failover_target SET sort = \$3, name = \$4, dns_type = \$5, dns_value = \$6, check_host = \$7, check_port = \$8, enabled = \$9, updated_at = \$10 WHERE group_id = \$1 AND id = \$2`).
		WithArgs(int64(50), int64(71), int64(12), "当前 IPv6（改名）", "AAAA", "2001:db8::1", "2001:db8::1", int64(443), int64(1), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE v2_dns_failover_target SET sort = \$3, name = \$4, dns_type = \$5, dns_value = \$6, check_host = \$7, check_port = \$8, enabled = \$9, updated_at = \$10 WHERE group_id = \$1 AND id = \$2`).
		WithArgs(int64(50), int64(72), int64(20), "备用域名", "CNAME", "backup.example.com", "backup.example.com", int64(443), int64(1), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DELETE FROM v2_dns_failover_group_probe WHERE group_id = \$1`).WithArgs(int64(50)).WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(`INSERT INTO v2_dns_failover_group_probe \(group_id, probe_id, created_at, updated_at\) VALUES \(\$1, \$2, \$3, \$4\)`).WithArgs(int64(50), int64(4), sqlmock.AnyArg(), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	updated, err := service.SaveDNSFailoverRule(context.Background(), request)
	if err != nil {
		t.Fatalf("SaveDNSFailoverRule update: %v", err)
	}
	if updated.CurrentTargetID == nil || *updated.CurrentTargetID != 71 || updated.CreatedAt != 100 || len(updated.Targets) != 2 || updated.Targets[0].CreatedAt != 110 || updated.Targets[1].CreatedAt != 120 {
		t.Fatalf("unexpected updated rule: %#v", updated)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestDNSFailoverUpdateRuleRollsBackAndPreservesRemovedTargetDeleteError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := &DBService{db: db, dnsFailoverSchemaOK: true}
	dbErr := errors.New("target delete failed")
	request := dnsFailoverUpdateRequestKeepingCurrent()
	request.Targets = request.Targets[:1]

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id FROM v2_dns_probe WHERE enabled = 1 AND id IN \(\$1\) ORDER BY id ASC FOR SHARE`).WithArgs(int64(4)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(4)))
	expectDNSFailoverUpdateLocks(mock)
	mock.ExpectExec(`UPDATE v2_dns_failover_group SET name = \$2,.*updated_at = \$22 WHERE id = \$1`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE v2_dns_failover_target SET sort = sort \+ \$2, updated_at = \$3 WHERE group_id = \$1`).WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(`UPDATE v2_dns_failover_target SET sort = \$3, name = \$4, dns_type = \$5, dns_value = \$6, check_host = \$7, check_port = \$8, enabled = \$9, updated_at = \$10 WHERE group_id = \$1 AND id = \$2`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DELETE FROM v2_dns_failover_target WHERE group_id = \$1 AND id IN \(\$2\)`).WithArgs(int64(50), int64(72)).WillReturnError(dbErr)
	mock.ExpectRollback()

	if _, err := service.SaveDNSFailoverRule(context.Background(), request); err == nil || !errors.Is(err, dbErr) || !strings.Contains(err.Error(), "删除") {
		t.Fatalf("SaveDNSFailoverRule delete error = %v, want wrapped delete error", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestDNSFailoverDeleteRuleRejectsEnabledRule(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := &DBService{db: db, dnsFailoverSchemaOK: true}

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT enabled FROM v2_dns_failover_group WHERE id = \$1 FOR UPDATE`).WithArgs(int64(50)).WillReturnRows(sqlmock.NewRows([]string{"enabled"}).AddRow(int64(1)))
	mock.ExpectRollback()

	if ok, err := service.DeleteDNSFailoverRule(context.Background(), 50); err == nil || ok || !strings.Contains(err.Error(), "先停用") {
		t.Fatalf("DeleteDNSFailoverRule enabled = %v, %v, want rejection requiring disable", ok, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestDNSFailoverRuleToggleAndDeleteDisabledRule(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := &DBService{db: db, dnsFailoverSchemaOK: true}

	mock.ExpectExec(`UPDATE v2_dns_failover_group SET enabled = \$2, updated_at = \$3 WHERE id = \$1`).WithArgs(int64(50), int64(0), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	if ok, err := service.SetDNSFailoverRuleEnabled(context.Background(), 50, false); err != nil || !ok {
		t.Fatalf("SetDNSFailoverRuleEnabled = %v, %v", ok, err)
	}
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT enabled FROM v2_dns_failover_group WHERE id = \$1 FOR UPDATE`).WithArgs(int64(50)).WillReturnRows(sqlmock.NewRows([]string{"enabled"}).AddRow(int64(0)))
	mock.ExpectExec(`DELETE FROM v2_dns_failover_group WHERE id = \$1`).WithArgs(int64(50)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if ok, err := service.DeleteDNSFailoverRule(context.Background(), 50); err != nil || !ok {
		t.Fatalf("DeleteDNSFailoverRule = %v, %v", ok, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestDNSFailoverDeleteRuleRollsBackAndPreservesDeleteError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := &DBService{db: db, dnsFailoverSchemaOK: true}
	dbErr := errors.New("group delete failed")

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT enabled FROM v2_dns_failover_group WHERE id = \$1 FOR UPDATE`).WithArgs(int64(50)).WillReturnRows(sqlmock.NewRows([]string{"enabled"}).AddRow(int64(0)))
	mock.ExpectExec(`DELETE FROM v2_dns_failover_group WHERE id = \$1`).WithArgs(int64(50)).WillReturnError(dbErr)
	mock.ExpectRollback()

	if ok, err := service.DeleteDNSFailoverRule(context.Background(), 50); err == nil || ok || !errors.Is(err, dbErr) || !strings.Contains(err.Error(), "删除") {
		t.Fatalf("DeleteDNSFailoverRule error = %v, want wrapped delete error", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestDNSFailoverEventListPaginatesWithoutFiltersAndScansNullableFields(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := &DBService{db: db, dnsFailoverSchemaOK: true}

	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM v2_dns_failover_event`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(42)))
	mock.ExpectQuery(`SELECT id, group_id, probe_id, target_id, event_type, message, details, dedupe_key, notified_at, created_at\s+FROM v2_dns_failover_event\s+ORDER BY created_at DESC, id DESC\s+LIMIT \$1 OFFSET \$2`).
		WithArgs(int64(25), int64(25)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "group_id", "probe_id", "target_id", "event_type", "message", "details", "dedupe_key", "notified_at", "created_at"}).
			AddRow(int64(8), int64(50), nil, int64(71), "failover", "已切换", `{"from":70,"to":71}`, "switch:50:71", nil, int64(200)).
			AddRow(int64(7), int64(50), int64(4), nil, "probe_offline", "探针离线", `{}`, "probe:4:offline", int64(190), int64(190)))

	result, err := service.ListDNSFailoverEvents(context.Background(), DNSFailoverEventListRequest{Current: 2, PageSize: 25})
	if err != nil {
		t.Fatalf("ListDNSFailoverEvents: %v", err)
	}
	if result.Total != 42 || result.Current != 2 || result.PageSize != 25 || len(result.Data) != 2 {
		t.Fatalf("unexpected event list result: %#v", result)
	}
	if result.Data[0].ProbeID != nil || result.Data[0].TargetID == nil || *result.Data[0].TargetID != 71 || result.Data[0].NotifiedAt != nil {
		t.Fatalf("unexpected nullable fields in first event: %#v", result.Data[0])
	}
	if result.Data[1].ProbeID == nil || *result.Data[1].ProbeID != 4 || result.Data[1].TargetID != nil || result.Data[1].NotifiedAt == nil || *result.Data[1].NotifiedAt != 190 {
		t.Fatalf("unexpected nullable fields in second event: %#v", result.Data[1])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestDNSFailoverEventListFiltersByGroupAndTrimmedEventType(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := &DBService{db: db, dnsFailoverSchemaOK: true}
	groupID := int64(50)

	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM v2_dns_failover_event WHERE group_id = \$1 AND event_type = \$2`).
		WithArgs(groupID, "failover").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(1)))
	mock.ExpectQuery(`SELECT id, group_id, probe_id, target_id, event_type, message, details, dedupe_key, notified_at, created_at\s+FROM v2_dns_failover_event WHERE group_id = \$1 AND event_type = \$2\s+ORDER BY created_at DESC, id DESC\s+LIMIT \$3 OFFSET \$4`).
		WithArgs(groupID, "failover", int64(10), int64(20)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "group_id", "probe_id", "target_id", "event_type", "message", "details", "dedupe_key", "notified_at", "created_at"}).
			AddRow(int64(8), groupID, nil, int64(71), "failover", "已切换", `{}`, "switch", nil, int64(200)))

	result, err := service.ListDNSFailoverEvents(context.Background(), DNSFailoverEventListRequest{
		GroupID:   &groupID,
		EventType: "  failover  ",
		Current:   3,
		PageSize:  10,
	})
	if err != nil {
		t.Fatalf("ListDNSFailoverEvents filtered: %v", err)
	}
	if result.Total != 1 || len(result.Data) != 1 || result.Data[0].EventType != "failover" {
		t.Fatalf("unexpected filtered events: %#v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestDNSFailoverEventListNormalizesInvalidPagination(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := &DBService{db: db, dnsFailoverSchemaOK: true}

	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM v2_dns_failover_event`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(0)))
	mock.ExpectQuery(`SELECT id, group_id, probe_id, target_id, event_type, message, details, dedupe_key, notified_at, created_at\s+FROM v2_dns_failover_event\s+ORDER BY created_at DESC, id DESC\s+LIMIT \$1 OFFSET \$2`).
		WithArgs(int64(100), int64(0)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "group_id", "probe_id", "target_id", "event_type", "message", "details", "dedupe_key", "notified_at", "created_at"}))

	result, err := service.ListDNSFailoverEvents(context.Background(), DNSFailoverEventListRequest{Current: -3, PageSize: 1000})
	if err != nil {
		t.Fatalf("ListDNSFailoverEvents normalized page: %v", err)
	}
	if result.Current != 1 || result.PageSize != 100 || result.Total != 0 || len(result.Data) != 0 {
		t.Fatalf("unexpected normalized result: %#v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestDNSFailoverEventListReturnsQueryError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := &DBService{db: db, dnsFailoverSchemaOK: true}

	dbErr := errors.New("database unavailable")
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM v2_dns_failover_event`).WillReturnError(dbErr)
	if _, err := service.ListDNSFailoverEvents(context.Background(), DNSFailoverEventListRequest{}); err == nil || !errors.Is(err, dbErr) || !strings.Contains(err.Error(), "事件总数") {
		t.Fatalf("ListDNSFailoverEvents count error = %v, want wrapped actionable Chinese query error", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestDNSFailoverEventListReturnsDataQueryError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := &DBService{db: db, dnsFailoverSchemaOK: true}
	dbErr := errors.New("event data query failed")

	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM v2_dns_failover_event`).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(1)))
	mock.ExpectQuery(`SELECT id, group_id, probe_id, target_id, event_type, message, details, dedupe_key, notified_at, created_at\s+FROM v2_dns_failover_event`).WillReturnError(dbErr)

	if _, err := service.ListDNSFailoverEvents(context.Background(), DNSFailoverEventListRequest{}); err == nil || !errors.Is(err, dbErr) || !strings.Contains(err.Error(), "事件") {
		t.Fatalf("ListDNSFailoverEvents data query error = %v, want wrapped actionable Chinese query error", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestDNSFailoverEventListReturnsRowsError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := &DBService{db: db, dnsFailoverSchemaOK: true}
	rowErr := errors.New("event cursor failed")

	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM v2_dns_failover_event`).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(2)))
	mock.ExpectQuery(`SELECT id, group_id, probe_id, target_id, event_type, message, details, dedupe_key, notified_at, created_at\s+FROM v2_dns_failover_event`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "group_id", "probe_id", "target_id", "event_type", "message", "details", "dedupe_key", "notified_at", "created_at"}).
			AddRow(int64(8), int64(50), nil, int64(71), "failover", "已切换", `{}`, "switch", nil, int64(200)).
			AddRow(int64(7), int64(50), int64(4), nil, "probe_offline", "离线", `{}`, "offline", nil, int64(190)).
			RowError(1, rowErr))

	if _, err := service.ListDNSFailoverEvents(context.Background(), DNSFailoverEventListRequest{}); err == nil || !errors.Is(err, rowErr) || !strings.Contains(err.Error(), "事件") {
		t.Fatalf("ListDNSFailoverEvents rows error = %v, want wrapped rows.Err", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}
