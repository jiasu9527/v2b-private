package dnspod

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLegacyClientListsDomainsWithLoginToken(t *testing.T) {
	var gotPath, gotToken, gotUserAgent string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotUserAgent = r.UserAgent()
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		gotToken = r.Form.Get("login_token")
		_, _ = w.Write([]byte(`{"status":{"code":"1","message":"Action completed successful"},"info":{"domain_total":"1"},"domains":[{"id":6,"name":"dnspod.com","grade":"DP_Free","grade_title":"Free","status":"enable","records":"3","updated_on":"2026-01-01 00:00:00"}]}`))
	}))
	defer server.Close()

	client := NewLegacyClient("730060,token-value", WithLegacyEndpoint(server.URL))
	result, err := client.DescribeDomainList(context.Background(), DescribeDomainListRequest{Offset: 0, Limit: 20})
	if err != nil {
		t.Fatalf("DescribeDomainList: %v", err)
	}
	if gotPath != "/Domain.List" || gotToken != "730060,token-value" || !strings.Contains(gotUserAgent, "ForestDNSManager/") {
		t.Fatalf("unexpected legacy request path=%q token=%q ua=%q", gotPath, gotToken, gotUserAgent)
	}
	if result.Total != 1 || len(result.Domains) != 1 || result.Domains[0].DomainID != 6 || result.Domains[0].Status != "ENABLE" || result.Domains[0].RecordCount != 3 {
		t.Fatalf("unexpected legacy domain result: %#v", result)
	}
}

func TestLegacyClientMapsRecordsTypesAndLines(t *testing.T) {
	var gotTypeGrade, gotLineGrade string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Record.List":
			_, _ = w.Write([]byte(`{"status":{"code":"1","message":"ok"},"info":{"record_total":"1"},"records":[{"id":"8","name":"www","type":"A","value":"192.0.2.1","line":"Default","line_id":"default","ttl":"600","mx":"0","enabled":"1","status":"enabled","updated_on":"2026-01-01 00:00:00"}]}`))
		case "/Record.Type":
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse Record.Type form: %v", err)
			}
			gotTypeGrade = r.Form.Get("domain_grade")
			_, _ = w.Write([]byte(`{"status":{"code":"1","message":"ok"},"types":["A","CNAME","TXT"]}`))
		case "/Record.Line":
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse Record.Line form: %v", err)
			}
			gotLineGrade = r.Form.Get("domain_grade")
			_, _ = w.Write([]byte(`{"status":{"code":"1","message":"ok"},"lines":{"default":{"name":"Default","sub_area":{"default":"Default"}},"asia":{"name":"Asia","sub_area":{"JP":"Japan","SG":"Singapore"}}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewLegacyClient("1,token", WithLegacyEndpoint(server.URL))
	records, err := client.DescribeRecordList(context.Background(), DescribeRecordListRequest{DomainID: 6, Limit: 20})
	if err != nil || records.Total != 1 || len(records.Records) != 1 || records.Records[0].Status != "ENABLE" {
		t.Fatalf("unexpected records=%#v err=%v", records, err)
	}
	types, err := client.DescribeRecordType(context.Background(), DescribeRecordTypeRequest{DomainGrade: "DP_Pro"})
	if err != nil || len(types.Types) != 3 || types.Types[1] != "CNAME" {
		t.Fatalf("unexpected types=%#v err=%v", types, err)
	}
	lines, err := client.DescribeRecordLineList(context.Background(), DescribeRecordLineListRequest{Domain: "dnspod.com", DomainGrade: "DP_Pro"})
	if err != nil || len(lines.Lines) != 2 || lines.Lines[0].LineID != "asia" || len(lines.Lines[0].SubGroup) != 2 || lines.Lines[0].SubGroup[0].LineID != "JP" {
		t.Fatalf("unexpected lines=%#v err=%v", lines, err)
	}
	if gotTypeGrade != "DP_Free" || gotLineGrade != "DP_Free" {
		t.Fatalf("legacy type/line APIs only support DP_Free, got type=%q line=%q", gotTypeGrade, gotLineGrade)
	}
}

func TestLegacyClientRequiresDomainIDForRecordList(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		_, _ = w.Write([]byte(`{"status":{"code":"1","message":"ok"},"records":[]}`))
	}))
	defer server.Close()

	client := NewLegacyClient("1,token", WithLegacyEndpoint(server.URL))
	_, err := client.DescribeRecordList(context.Background(), DescribeRecordListRequest{Domain: "example.com", Limit: 20})
	if err == nil || !strings.Contains(err.Error(), "域名 ID") {
		t.Fatalf("expected actionable domain ID error, got %v", err)
	}
	if called {
		t.Fatal("legacy Record.List must reject a missing domain ID before sending the request")
	}
}

func TestLegacyClientProvidesDefaultLineWhenProviderReturnsNoLines(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":{"code":"1","message":"ok"},"lines":{}}`))
	}))
	defer server.Close()

	client := NewLegacyClient("1,token", WithLegacyEndpoint(server.URL))
	result, err := client.DescribeRecordLineList(context.Background(), DescribeRecordLineListRequest{Domain: "example.com", DomainGrade: "DP_Free"})
	if err != nil {
		t.Fatalf("DescribeRecordLineList: %v", err)
	}
	if len(result.Lines) != 1 || result.Lines[0].LineID != "default" || result.Lines[0].LineName != "Default" {
		t.Fatalf("expected default line fallback, got %#v", result.Lines)
	}
}

func TestLegacyClientCreatesRecordAndReturnsActionableError(t *testing.T) {
	call := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call++
		if call == 1 {
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse form: %v", err)
			}
			if r.URL.Path != "/Record.Create" || r.Form.Get("domain_id") != "6" || r.Form.Get("record_line") != "default" {
				t.Fatalf("unexpected create request path=%q form=%v", r.URL.Path, r.Form)
			}
			_, _ = w.Write([]byte(`{"status":{"code":"1","message":"ok"},"record":{"id":"91","name":"www","status":"enable"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":{"code":"-1","message":"Login token error"}}`))
	}))
	defer server.Close()

	client := NewLegacyClient("1,token", WithLegacyEndpoint(server.URL))
	created, err := client.CreateRecord(context.Background(), RecordMutationRequest{DomainID: 6, SubDomain: "www", RecordType: "A", RecordLineID: "default", Value: "192.0.2.2", TTL: 600})
	if err != nil || created.RecordID != 91 {
		t.Fatalf("unexpected create result=%#v err=%v", created, err)
	}
	_, err = client.DescribeDomainList(context.Background(), DescribeDomainListRequest{Limit: 1})
	if err == nil || !strings.Contains(err.Error(), "DNSPod API Token") || !strings.Contains(err.Error(), "-1") {
		t.Fatalf("expected actionable token error, got %v", err)
	}
}
