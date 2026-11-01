package admin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"forest/go-api/internal/dnspod"
)

type metadataDNSPodAPI struct {
	fakeDNSPodAPI
	domainFunc    func(dnspod.DescribeDomainListRequest) (dnspod.DescribeDomainListResult, error)
	domainCalls   int
	mutationCalls int
	mutationErr   error
	lastLineReq   dnspod.DescribeRecordLineListRequest
}

func (f *metadataDNSPodAPI) DescribeDomainList(_ context.Context, req dnspod.DescribeDomainListRequest) (dnspod.DescribeDomainListResult, error) {
	f.domainCalls++
	if f.domainFunc != nil {
		return f.domainFunc(req)
	}
	return f.domains, nil
}

func (f *metadataDNSPodAPI) DescribeRecordLineList(_ context.Context, req dnspod.DescribeRecordLineListRequest) (dnspod.DescribeRecordLineListResult, error) {
	f.lastLineReq = req
	return f.lines, nil
}

func (f *metadataDNSPodAPI) CreateRecord(_ context.Context, req dnspod.RecordMutationRequest) (dnspod.RecordMutationResult, error) {
	f.mutationCalls++
	f.lastMutation = req
	return dnspod.RecordMutationResult{RecordID: 91}, f.mutationErr
}

func (f *metadataDNSPodAPI) ModifyRecord(ctx context.Context, req dnspod.RecordMutationRequest) (dnspod.RecordMutationResult, error) {
	return f.CreateRecord(ctx, req)
}

func testDNSPodMetadataService(t *testing.T, api dnspodAPI) *DBService {
	t.Helper()
	useDNSPodConfigRoot(t)
	t.Setenv("DNSPOD_SECRET_ID", "test-id")
	t.Setenv("DNSPOD_SECRET_KEY", "test-key")
	t.Setenv("DNSPOD_API_TOKEN", "")
	t.Setenv("DNSPOD_AUTH_TYPE", dnspod.AuthTypeTC3)
	t.Setenv("DNSPOD_EDITION", dnspod.EditionInternational)
	return &DBService{dnspodClientFactory: func(_, _, _ string) dnspodAPI { return api }}
}

func TestDNSPodDomainMetadataExactMatchAndPagination(t *testing.T) {
	for _, tc := range []struct {
		name   string
		domain dnspod.Domain
	}{
		{"normalized name", dnspod.Domain{DomainID: 6, Name: "EXAMPLE.COM.", Grade: " DPG_Ultimate "}},
		{"punycode", dnspod.Domain{DomainID: 6, Name: "另一个显示名称", Punycode: "example.com", Grade: "DPG_Ultimate"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var offsets []int64
			api := &metadataDNSPodAPI{domainFunc: func(req dnspod.DescribeDomainListRequest) (dnspod.DescribeDomainListResult, error) {
				offsets = append(offsets, req.Offset)
				if req.Keyword != "example.com" || req.Limit != 100 {
					t.Fatalf("unexpected lookup: %#v", req)
				}
				if req.Offset == 0 {
					return dnspod.DescribeDomainListResult{Domains: []dnspod.Domain{{DomainID: 5, Name: "notexample.com"}}, Total: 2}, nil
				}
				return dnspod.DescribeDomainListResult{Domains: []dnspod.Domain{tc.domain}, Total: 2}, nil
			}}
			got, err := resolveDNSPodDomain(context.Background(), api, " Example.COM. ", 0, true)
			if err != nil || got.DomainID != 6 || got.Grade != "DPG_Ultimate" || !reflect.DeepEqual(offsets, []int64{0, 1}) {
				t.Fatalf("lookup: got=%#v offsets=%v err=%v", got, offsets, err)
			}
		})
	}
}

func TestDNSPodDomainMetadataRefusesIncompleteOrConflictingIdentity(t *testing.T) {
	for _, tc := range []struct {
		name    string
		id      int64
		domains []dnspod.Domain
	}{
		{"not found", 0, []dnspod.Domain{{DomainID: 6, Name: "notexample.com", Grade: "DPG_Free"}}},
		{"mismatched ID", 7, []dnspod.Domain{{DomainID: 6, Name: "example.com", Grade: "DPG_Ultimate"}}},
		{"missing ID", 0, []dnspod.Domain{{Name: "example.com", Grade: "DPG_Ultimate"}}},
		{"missing grade", 0, []dnspod.Domain{{DomainID: 6, Name: "example.com"}}},
		{"ambiguous identity", 0, []dnspod.Domain{{DomainID: 6, Name: "example.com", Grade: "DPG_Ultimate"}, {DomainID: 7, Name: "example.com", Grade: "DPG_Ultimate"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := &metadataDNSPodAPI{fakeDNSPodAPI: fakeDNSPodAPI{domains: dnspod.DescribeDomainListResult{Domains: tc.domains, Total: int64(len(tc.domains))}}}
			service := testDNSPodMetadataService(t, api)
			_, err := service.SaveDNSPodRecord(context.Background(), DNSPodRecordSaveRequest{Domain: "example.com", DomainID: tc.id, DomainGrade: "DPG_Free", RecordType: "A", Value: "192.0.2.2"})
			var preflight *dnspod.PreflightError
			if !errors.As(err, &preflight) || preflight.Code != "dns_domain_metadata" || api.mutationCalls != 0 {
				t.Fatalf("unsafe metadata result: err=%v writes=%d", err, api.mutationCalls)
			}
		})
	}
}

func TestDNSPodDomainMetadataPaginationLimitFailsClosed(t *testing.T) {
	api := &metadataDNSPodAPI{domainFunc: func(req dnspod.DescribeDomainListRequest) (dnspod.DescribeDomainListResult, error) {
		return dnspod.DescribeDomainListResult{Domains: []dnspod.Domain{{DomainID: 6, Name: "example.com", Grade: "DPG_Ultimate"}}, Total: 101}, nil
	}}
	_, err := resolveDNSPodDomain(context.Background(), api, "example.com", 0, true)
	var preflight *dnspod.PreflightError
	if !errors.As(err, &preflight) || preflight.Code != "dns_domain_metadata" || api.domainCalls != 100 {
		t.Fatalf("pagination must fail closed: calls=%d err=%v", api.domainCalls, err)
	}
}

func TestDNSPodMetadataFillsRecordAndLineQueriesAndMutation(t *testing.T) {
	api := &metadataDNSPodAPI{fakeDNSPodAPI: fakeDNSPodAPI{domains: dnspod.DescribeDomainListResult{Domains: []dnspod.Domain{{DomainID: 6, Name: "example.com", Grade: "DPG_Ultimate"}}, Total: 1}}}
	service := testDNSPodMetadataService(t, api)
	if _, err := service.ListDNSPodRecords(context.Background(), DNSPodRecordListRequest{Domain: "EXAMPLE.COM.", Keyword: "req22", PageSize: 50}); err != nil || api.lastRecordReq.DomainID != 6 || api.lastRecordReq.Keyword != "req22" {
		t.Fatalf("records: req=%#v err=%v", api.lastRecordReq, err)
	}
	if _, err := service.ListDNSPodRecordLines(context.Background(), "example.com", 0, "DPG_Free", "a"); err != nil || api.lastLineReq.DomainID != 6 || api.lastLineReq.DomainGrade != "DPG_Ultimate" {
		t.Fatalf("lines: req=%#v err=%v", api.lastLineReq, err)
	}
	for _, id := range []int64{0, 6} {
		if _, err := service.SaveDNSPodRecord(context.Background(), DNSPodRecordSaveRequest{Domain: "example.com", DomainID: id, DomainGrade: "DPG_Free", RecordType: "A", Value: "192.0.2.2"}); err != nil || api.lastMutation.DomainID != 6 || api.lastMutation.DomainGrade != "DPG_Ultimate" {
			t.Fatalf("save: req=%#v err=%v", api.lastMutation, err)
		}
	}
	if err := service.DeleteDNSPodRecord(context.Background(), "example.com", 0, 91); err != nil || api.lastDelete.DomainID != 6 {
		t.Fatalf("delete: req=%#v err=%v", api.lastDelete, err)
	}
	if err := service.SetDNSPodRecordStatus(context.Background(), "example.com", 0, 91, "DISABLE"); err != nil || api.lastStatus.DomainID != 6 {
		t.Fatalf("status: req=%#v err=%v", api.lastStatus, err)
	}
}

func TestDNSPodMutationErrorsAreNotPreflight(t *testing.T) {
	want := errors.New("provider write result unknown")
	api := &metadataDNSPodAPI{fakeDNSPodAPI: fakeDNSPodAPI{domains: dnspod.DescribeDomainListResult{Domains: []dnspod.Domain{{DomainID: 6, Name: "example.com", Grade: "DPG_Ultimate"}}, Total: 1}}, mutationErr: want}
	service := testDNSPodMetadataService(t, api)
	for _, recordID := range []int64{0, 91} {
		_, err := service.SaveDNSPodRecord(context.Background(), DNSPodRecordSaveRequest{Domain: "example.com", RecordID: recordID, RecordType: "A", Value: "192.0.2.2"})
		var preflight *dnspod.PreflightError
		if err != want || errors.As(err, &preflight) {
			t.Fatalf("write error must retain unknown outcome: %v", err)
		}
	}
	if api.mutationCalls != 2 {
		t.Fatalf("writes = %d", api.mutationCalls)
	}
}

func TestDNSPodMissingCredentialsIsPreflight(t *testing.T) {
	service := testDNSPodMetadataService(t, nil)
	t.Setenv("DNSPOD_SECRET_ID", "")
	t.Setenv("DNSPOD_SECRET_KEY", "")
	_, err := service.SaveDNSPodRecord(context.Background(), DNSPodRecordSaveRequest{Domain: "example.com", RecordType: "A", Value: "192.0.2.2"})
	var preflight *dnspod.PreflightError
	if !errors.As(err, &preflight) || preflight.Code != "dns_preflight_failed" {
		t.Fatalf("missing configuration: %v", err)
	}
}

func TestDNSPodLegacyMetadataToProviderEndToEnd(t *testing.T) {
	for _, tc := range []struct {
		name        string
		failurePath string
		failureMode string
		wantCode    string
		wantWrites  int
		domainID    int64
	}{
		{name: "missing ID and grade Global create", wantWrites: 1},
		{name: "provided ID missing grade Global create", wantWrites: 1, domainID: 6440564},
		{name: "metadata transport failure", failurePath: "/Domain.List", failureMode: "disconnect", wantCode: "dns_domain_metadata"},
		{name: "line precheck failure", failurePath: "/Record.Line", failureMode: "http500", wantCode: "dns_record_line_unavailable"},
		{name: "unknown line not sent", failurePath: "/Record.Line", failureMode: "missing_line", wantCode: "dns_record_line_unavailable"},
		{name: "write provider rejection remains unknown", failurePath: "/Record.Create", failureMode: "provider_error", wantWrites: 1},
		{name: "write HTTP500 remains unknown", failurePath: "/Record.Create", failureMode: "http500", wantWrites: 1},
		{name: "write transport failure remains unknown", failurePath: "/Record.Create", failureMode: "disconnect", wantWrites: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var paths []string
			writes := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				paths = append(paths, r.URL.Path)
				if r.URL.Path == "/Record.Create" {
					writes++
				}
				mu.Unlock()
				if err := r.ParseForm(); err != nil {
					t.Error(err)
					return
				}
				if r.URL.Path == tc.failurePath {
					switch tc.failureMode {
					case "disconnect":
						conn, _, err := w.(http.Hijacker).Hijack()
						if err != nil {
							t.Error(err)
							return
						}
						_ = conn.Close()
						return
					case "http500":
						http.Error(w, "provider-sensitive-detail", http.StatusInternalServerError)
						return
					case "provider_error":
						fmt.Fprint(w, `{"status":{"code":"26","message":"provider-sensitive-detail"}}`)
						return
					case "missing_line":
						fmt.Fprint(w, `{"status":{"code":"1"},"lines":["Default"],"line_ids":{"Default":0}}`)
						return
					}
				}
				switch r.URL.Path {
				case "/Domain.List":
					if r.Form.Get("keyword") != "example.com" {
						t.Errorf("domain lookup keyword = %q", r.Form.Get("keyword"))
					}
					fmt.Fprint(w, `{"status":{"code":"1"},"info":{"domain_total":"1"},"domains":[{"id":6440564,"name":"example.com","grade":"DPG_Ultimate"}]}`)
				case "/Record.List":
					if r.Form.Get("domain_id") != "6440564" {
						t.Errorf("record lookup did not resolve ID: %v", r.Form)
					}
					fmt.Fprint(w, `{"status":{"code":"1"},"info":{"record_total":"0"},"records":[]}`)
				case "/Record.Line":
					if r.Form.Get("domain_grade") != "DPG_Ultimate" || r.Form.Get("domain_id") != "6440564" {
						t.Errorf("line metadata = %v", r.Form)
					}
					fmt.Fprint(w, `{"status":{"code":"1"},"lines":["Default","Global"],"line_ids":{"Default":0,"Global":"3=0"}}`)
				case "/Record.Create":
					if r.Form.Get("domain_id") != "6440564" || r.Form.Get("record_line") != "Global" || r.Form.Get("sub_domain") != "req7k9m2" {
						t.Errorf("create metadata = %v", r.Form)
					}
					fmt.Fprint(w, `{"status":{"code":"1"},"record":{"id":"91"}}`)
				default:
					t.Errorf("unexpected request: %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			service := testDNSPodMetadataService(t, nil)
			t.Setenv("DNSPOD_AUTH_TYPE", dnspod.AuthTypeToken)
			t.Setenv("DNSPOD_API_TOKEN", "1,test-token")
			service.dnspodLegacyClientFactory = func(token string) dnspodAPI {
				return dnspod.NewLegacyClient(token, dnspod.WithLegacyEndpoint(server.URL), dnspod.WithLegacyHTTPClient(server.Client()))
			}
			if tc.failurePath == "" {
				if _, err := service.ListDNSPodRecords(context.Background(), DNSPodRecordListRequest{Domain: "example.com", Keyword: "req22"}); err != nil {
					t.Fatalf("missing-ID record query: %v", err)
				}
			}
			result, err := service.SaveDNSPodRecord(context.Background(), DNSPodRecordSaveRequest{Domain: "example.com", DomainID: tc.domainID, SubDomain: "req7k9m2", RecordType: "A", RecordLine: "Global", RecordLineID: "3=0", Value: "192.0.2.2", TTL: 600})
			var preflight *dnspod.PreflightError
			if tc.wantCode != "" {
				if !errors.As(err, &preflight) || preflight.Code != tc.wantCode || strings.Contains(err.Error(), "provider-sensitive-detail") {
					t.Fatalf("preflight err=%v", err)
				}
			} else if tc.failurePath != "" {
				if err == nil || errors.As(err, &preflight) {
					t.Fatalf("write result must remain unknown: %v", err)
				}
			} else if err != nil || result.RecordID != 91 {
				t.Fatalf("create result=%#v err=%v", result, err)
			}
			mu.Lock()
			defer mu.Unlock()
			if writes != tc.wantWrites {
				t.Fatalf("writes=%d want=%d paths=%v", writes, tc.wantWrites, paths)
			}
			if tc.failurePath == "" && !reflect.DeepEqual(paths, []string{"/Domain.List", "/Record.List", "/Domain.List", "/Record.Line", "/Record.Create"}) {
				t.Fatalf("unexpected flow: %v", paths)
			}
		})
	}
}
