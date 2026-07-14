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
		Domain: "example.com", SubDomain: "www", RecordType: "a", RecordLine: "默认", Value: "192.0.2.1", TTL: 600,
	})
	if err != nil || result.RecordID != 91 || fake.lastMutation.RecordType != "A" || fake.lastMutation.RecordLine != "默认" {
		t.Fatalf("unexpected save delegation: result=%#v req=%#v err=%v", result, fake.lastMutation, err)
	}
	if _, err := service.SaveDNSPodRecord(context.Background(), DNSPodRecordSaveRequest{Domain: "example.com", RecordType: "A"}); err == nil {
		t.Fatal("expected empty record value validation error")
	}
	if err := service.SetDNSPodRecordStatus(context.Background(), "example.com", 8, "invalid"); err == nil {
		t.Fatal("expected invalid status validation error")
	}
}
