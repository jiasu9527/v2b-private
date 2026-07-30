package admin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forest/go-api/internal/dnspod"
)

type fakeDNSPodAPI struct {
	domains       dnspod.DescribeDomainListResult
	records       dnspod.DescribeRecordListResult
	types         dnspod.DescribeRecordTypeResult
	lines         dnspod.DescribeRecordLineListResult
	lastDomainReq dnspod.DescribeDomainListRequest
	lastRecordReq dnspod.DescribeRecordListRequest
	lastMutation  dnspod.RecordMutationRequest
	lastDelete    dnspod.DeleteRecordRequest
	lastStatus    dnspod.ModifyRecordStatusRequest
}

func (f *fakeDNSPodAPI) DescribeDomainList(_ context.Context, req dnspod.DescribeDomainListRequest) (dnspod.DescribeDomainListResult, error) {
	f.lastDomainReq = req
	return f.domains, nil
}

func (f *fakeDNSPodAPI) DescribeRecordList(_ context.Context, req dnspod.DescribeRecordListRequest) (dnspod.DescribeRecordListResult, error) {
	f.lastRecordReq = req
	return f.records, nil
}

func (f *fakeDNSPodAPI) DescribeRecordType(_ context.Context, req dnspod.DescribeRecordTypeRequest) (dnspod.DescribeRecordTypeResult, error) {
	return f.types, nil
}

func (f *fakeDNSPodAPI) DescribeRecordLineList(_ context.Context, req dnspod.DescribeRecordLineListRequest) (dnspod.DescribeRecordLineListResult, error) {
	return f.lines, nil
}

func (f *fakeDNSPodAPI) CreateRecord(_ context.Context, req dnspod.RecordMutationRequest) (dnspod.RecordMutationResult, error) {
	f.lastMutation = req
	return dnspod.RecordMutationResult{RecordID: 91}, nil
}

func (f *fakeDNSPodAPI) ModifyRecord(_ context.Context, req dnspod.RecordMutationRequest) (dnspod.RecordMutationResult, error) {
	f.lastMutation = req
	return dnspod.RecordMutationResult{RecordID: req.RecordID}, nil
}

func (f *fakeDNSPodAPI) DeleteRecord(_ context.Context, req dnspod.DeleteRecordRequest) error {
	f.lastDelete = req
	return nil
}

func (f *fakeDNSPodAPI) ModifyRecordStatus(_ context.Context, req dnspod.ModifyRecordStatusRequest) error {
	f.lastStatus = req
	return nil
}

func useDNSPodConfigRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	oldRoot := adminProjectRoot
	adminProjectRoot = root
	t.Cleanup(func() { adminProjectRoot = oldRoot })
	return root
}

func TestDBServiceSaveDNSPodConfigMasksSecretAndDoesNotExposeKey(t *testing.T) {
	root := useDNSPodConfigRoot(t)
	t.Setenv("DNSPOD_SECRET_ID", "")
	t.Setenv("DNSPOD_SECRET_KEY", "")
	service := &DBService{}

	status, err := service.SaveDNSPodConfig(context.Background(), DNSPodConfigSaveRequest{
		SecretID:  "AKID1234567890",
		SecretKey: "super-secret-key",
		Edition:   dnspod.EditionInternational,
	})
	if err != nil {
		t.Fatalf("SaveDNSPodConfig: %v", err)
	}
	if !status.Configured || status.SecretIDMasked != "AKID12****7890" || status.Source != "config" || status.Edition != dnspod.EditionInternational {
		t.Fatalf("unexpected status: %#v", status)
	}
	rawStatus, _ := json.Marshal(status)
	if string(rawStatus) == "" || strings.Contains(string(rawStatus), "super-secret-key") {
		t.Fatalf("status leaked secret key: %s", rawStatus)
	}
	rawConfig, err := os.ReadFile(filepath.Join(root, "config", "admin.json"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(rawConfig), "dnspod_secret_id") || !strings.Contains(string(rawConfig), "super-secret-key") {
		t.Fatalf("credentials were not persisted: %s", rawConfig)
	}
	info, err := os.Stat(filepath.Join(root, "config", "admin.json"))
	if err != nil {
		t.Fatalf("stat admin config: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("admin config permissions = %v; want 0600", info.Mode().Perm())
	}
}

func TestDBServiceDNSPodEnvironmentCredentialsTakePrecedence(t *testing.T) {
	useDNSPodConfigRoot(t)
	t.Setenv("DNSPOD_SECRET_ID", "ENVSECRETID1234")
	t.Setenv("DNSPOD_SECRET_KEY", "env-key")
	t.Setenv("DNSPOD_EDITION", dnspod.EditionChina)
	service := &DBService{}

	status, err := service.GetDNSPodConfig(context.Background())
	if err != nil {
		t.Fatalf("GetDNSPodConfig: %v", err)
	}
	if !status.Configured || status.Source != "env" || status.SecretIDMasked != "ENVSEC****1234" || status.Edition != dnspod.EditionChina {
		t.Fatalf("unexpected environment status: %#v", status)
	}
}

func TestDBServiceDNSPodOperationsValidateAndDelegate(t *testing.T) {
	useDNSPodConfigRoot(t)
	t.Setenv("DNSPOD_SECRET_ID", "id")
	t.Setenv("DNSPOD_SECRET_KEY", "key")
	fake := &fakeDNSPodAPI{
		domains: dnspod.DescribeDomainListResult{Domains: []dnspod.Domain{{Name: "example.com"}}, Total: 1},
		records: dnspod.DescribeRecordListResult{Records: []dnspod.Record{{RecordID: 8, Name: "www"}}, Total: 1},
	}
	gotEdition := ""
	service := &DBService{dnspodClientFactory: func(_, _, edition string) dnspodAPI {
		gotEdition = edition
		return fake
	}}

	domains, err := service.ListDNSPodDomains(context.Background(), DNSPodDomainListRequest{Current: 2, PageSize: 25, Keyword: "example"})
	if err != nil || domains.Total != 1 || fake.lastDomainReq.Offset != 25 || fake.lastDomainReq.Limit != 25 {
		t.Fatalf("unexpected domain delegation: result=%#v req=%#v err=%v", domains, fake.lastDomainReq, err)
	}
	if gotEdition != dnspod.EditionInternational {
		t.Fatalf("expected international edition by default, got %q", gotEdition)
	}
	records, err := service.ListDNSPodRecords(context.Background(), DNSPodRecordListRequest{Domain: "example.com", Current: 1, PageSize: 50, Keyword: "www", RecordType: "A"})
	if err != nil || records.Total != 1 || fake.lastRecordReq.Domain != "example.com" || fake.lastRecordReq.RecordType != "A" {
		t.Fatalf("unexpected record delegation: result=%#v req=%#v err=%v", records, fake.lastRecordReq, err)
	}

	result, err := service.SaveDNSPodRecord(context.Background(), DNSPodRecordSaveRequest{
		Domain: "example.com", DomainGrade: " DPG_Enterprise ", SubDomain: "www", RecordType: "a", RecordLine: "默认", Value: "192.0.2.1", TTL: 600,
	})
	if err != nil || result.RecordID != 91 || fake.lastMutation.DomainGrade != "DPG_Enterprise" || fake.lastMutation.RecordType != "A" || fake.lastMutation.RecordLine != "默认" {
		t.Fatalf("unexpected save delegation: result=%#v req=%#v err=%v", result, fake.lastMutation, err)
	}
	if _, err := service.SaveDNSPodRecord(context.Background(), DNSPodRecordSaveRequest{Domain: "example.com", RecordType: "A"}); err == nil {
		t.Fatal("expected empty record value validation error")
	}
	if err := service.SetDNSPodRecordStatus(context.Background(), "example.com", 0, 8, "invalid"); err == nil {
		t.Fatal("expected invalid status validation error")
	}
}

func TestDBServiceDNSPodTokenModeUsesLegacyClientAndMasksToken(t *testing.T) {
	root := useDNSPodConfigRoot(t)
	t.Setenv("DNSPOD_SECRET_ID", "")
	t.Setenv("DNSPOD_SECRET_KEY", "")
	t.Setenv("DNSPOD_API_TOKEN", "")
	fake := &fakeDNSPodAPI{domains: dnspod.DescribeDomainListResult{Total: 1}}
	gotToken := ""
	service := &DBService{dnspodLegacyClientFactory: func(apiToken string) dnspodAPI {
		gotToken = apiToken
		return fake
	}}

	status, err := service.SaveDNSPodConfig(context.Background(), DNSPodConfigSaveRequest{
		AuthType: dnspod.AuthTypeToken,
		APIToken: "730060,token-secret",
		Edition:  dnspod.EditionInternational,
		Verify:   true,
	})
	if err != nil {
		t.Fatalf("SaveDNSPodConfig token mode: %v", err)
	}
	if !status.Configured || status.AuthType != dnspod.AuthTypeToken || status.CredentialMasked != "730060,****" || gotToken != "730060,token-secret" {
		t.Fatalf("unexpected token status=%#v gotToken=%q", status, gotToken)
	}
	rawStatus, _ := json.Marshal(status)
	if strings.Contains(string(rawStatus), "token-secret") {
		t.Fatalf("status leaked token: %s", rawStatus)
	}
	rawConfig, err := os.ReadFile(filepath.Join(root, "config", "admin.json"))
	if err != nil || !strings.Contains(string(rawConfig), "dnspod_api_token") {
		t.Fatalf("token was not persisted: raw=%s err=%v", rawConfig, err)
	}

	if _, err := service.ListDNSPodDomains(context.Background(), DNSPodDomainListRequest{Current: 1, PageSize: 20}); err != nil {
		t.Fatalf("ListDNSPodDomains token mode: %v", err)
	}
	if gotToken != "730060,token-secret" {
		t.Fatalf("legacy client did not receive persisted token, got %q", gotToken)
	}
}

func TestDBServiceDNSPodEnvironmentOverridesAreMergedPerField(t *testing.T) {
	useDNSPodConfigRoot(t)
	t.Setenv("DNSPOD_SECRET_ID", "")
	t.Setenv("DNSPOD_SECRET_KEY", "")
	t.Setenv("DNSPOD_API_TOKEN", "")
	t.Setenv("DNSPOD_AUTH_TYPE", "")
	t.Setenv("DNSPOD_EDITION", "")
	service := &DBService{}
	if _, err := service.SaveDNSPodConfig(context.Background(), DNSPodConfigSaveRequest{
		SecretID: "stored-id", SecretKey: "stored-key", AuthType: dnspod.AuthTypeTC3, Edition: dnspod.EditionInternational,
	}); err != nil {
		t.Fatalf("save stored credentials: %v", err)
	}

	t.Setenv("DNSPOD_SECRET_ID", "env-id")
	t.Setenv("DNSPOD_EDITION", dnspod.EditionChina)
	var gotID, gotKey, gotEdition string
	service.dnspodClientFactory = func(secretID, secretKey, edition string) dnspodAPI {
		gotID, gotKey, gotEdition = secretID, secretKey, edition
		return &fakeDNSPodAPI{}
	}
	if _, err := service.ListDNSPodDomains(context.Background(), DNSPodDomainListRequest{Current: 1, PageSize: 1}); err != nil {
		t.Fatalf("list with partial environment overrides: %v", err)
	}
	if gotID != "env-id" || gotKey != "stored-key" || gotEdition != dnspod.EditionChina {
		t.Fatalf("environment overrides were not merged: id=%q key=%q edition=%q", gotID, gotKey, gotEdition)
	}
	status, err := service.GetDNSPodConfig(context.Background())
	if err != nil || status.Source != "env" {
		t.Fatalf("expected environment source, got status=%#v err=%v", status, err)
	}
	if got := strings.Join(status.EnvironmentOverrides, ","); got != "DNSPOD_SECRET_ID,DNSPOD_EDITION" {
		t.Fatalf("unexpected environment override names: %q", got)
	}
	if _, err := service.SaveDNSPodConfig(context.Background(), DNSPodConfigSaveRequest{
		SecretID: "new-id", SecretKey: "new-key", AuthType: dnspod.AuthTypeTC3,
	}); err == nil || !strings.Contains(err.Error(), "DNSPOD_SECRET_ID") || !strings.Contains(err.Error(), "后台保存不会生效") {
		t.Fatalf("expected actionable environment override save error, got %v", err)
	}
}

func TestDBServiceDNSPodSwitchingAuthRemovesInactiveCredentials(t *testing.T) {
	root := useDNSPodConfigRoot(t)
	t.Setenv("DNSPOD_SECRET_ID", "")
	t.Setenv("DNSPOD_SECRET_KEY", "")
	t.Setenv("DNSPOD_API_TOKEN", "")
	t.Setenv("DNSPOD_AUTH_TYPE", "")
	t.Setenv("DNSPOD_EDITION", "")
	service := &DBService{}
	if _, err := service.SaveDNSPodConfig(context.Background(), DNSPodConfigSaveRequest{
		SecretID: "old-id", SecretKey: "old-key", AuthType: dnspod.AuthTypeTC3,
	}); err != nil {
		t.Fatalf("save TC3 credentials: %v", err)
	}
	if _, err := service.SaveDNSPodConfig(context.Background(), DNSPodConfigSaveRequest{
		APIToken: "730060,new-token", AuthType: dnspod.AuthTypeToken,
	}); err != nil {
		t.Fatalf("switch to token credentials: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "config", "admin.json"))
	if err != nil {
		t.Fatalf("read admin config: %v", err)
	}
	var values map[string]any
	if err := json.Unmarshal(raw, &values); err != nil {
		t.Fatalf("decode admin config: %v", err)
	}
	if _, exists := values[dnspodSecretIDKey]; exists {
		t.Fatalf("inactive SecretId was retained: %s", raw)
	}
	if _, exists := values[dnspodSecretKeyKey]; exists {
		t.Fatalf("inactive SecretKey was retained: %s", raw)
	}
	if values[dnspodAPITokenKey] != "730060,new-token" {
		t.Fatalf("active token missing: %s", raw)
	}
}
