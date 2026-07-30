package dnspod

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
	types, err := client.DescribeRecordType(context.Background(), DescribeRecordTypeRequest{DomainGrade: "DPG_Enterprise"})
	if err != nil || len(types.Types) != 3 || types.Types[1] != "CNAME" {
		t.Fatalf("unexpected types=%#v err=%v", types, err)
	}
	lines, err := client.DescribeRecordLineList(context.Background(), DescribeRecordLineListRequest{Domain: "dnspod.com", DomainGrade: "DPG_Enterprise"})
	if err != nil || len(lines.Lines) != 2 || lines.Lines[0].LineID != "asia" || len(lines.Lines[0].SubGroup) != 2 || lines.Lines[0].SubGroup[0].LineID != "JP" {
		t.Fatalf("unexpected lines=%#v err=%v", lines, err)
	}
	if gotTypeGrade != "DPG_Enterprise" || gotLineGrade != "DPG_Enterprise" {
		t.Fatalf("legacy type/line APIs must use the selected domain grade, got type=%q line=%q", gotTypeGrade, gotLineGrade)
	}
}

func TestLegacyClientMapsInternationalLineIDsInProviderOrder(t *testing.T) {
	var gotGrade string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse Record.Line form: %v", err)
		}
		gotGrade = r.Form.Get("domain_grade")
		_, _ = w.Write([]byte(`{
			"status":{"code":"1","message":"ok"},
			"line_ids":{"China Unicom":"10=1","Default":0,"Global":"3=0","China Mobile":"10=3"},
			"lines":["Default","Global","China Mobile"],
			"line_groups":[]
		}`))
	}))
	defer server.Close()

	client := NewLegacyClient("1,token", WithLegacyEndpoint(server.URL))
	result, err := client.DescribeRecordLineList(context.Background(), DescribeRecordLineListRequest{
		Domain: "example.com", DomainID: 6, DomainGrade: "DPG_Free", RecordType: "A",
	})
	if err != nil {
		t.Fatalf("DescribeRecordLineList: %v", err)
	}
	if gotGrade != "DPG_Free" {
		t.Fatalf("Record.Line domain_grade = %q, want DPG_Free", gotGrade)
	}
	if len(result.Lines) != 4 {
		t.Fatalf("expected every line_id entry, got %#v", result.Lines)
	}
	want := []RecordLine{
		{LineID: "0", LineName: "Default", Useful: true},
		{LineID: "3=0", LineName: "Global", Useful: true},
		{LineID: "10=3", LineName: "China Mobile", Useful: true},
		{LineID: "10=1", LineName: "China Unicom", Useful: true},
	}
	for index := range want {
		if result.Lines[index].LineID != want[index].LineID || result.Lines[index].LineName != want[index].LineName || !result.Lines[index].Useful {
			t.Fatalf("line %d = %#v, want %#v; all=%#v", index, result.Lines[index], want[index], result.Lines)
		}
	}
}

func TestLegacyClientCanonicalizesDefaultInternationalGrade(t *testing.T) {
	var grades []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		grades = append(grades, r.Form.Get("domain_grade"))
		switch r.URL.Path {
		case "/Record.Type":
			_, _ = w.Write([]byte(`{"status":{"code":"1","message":"ok"},"types":["A"]}`))
		case "/Record.Line":
			_, _ = w.Write([]byte(`{"status":{"code":"1","message":"ok"},"line_ids":{"Default":0},"lines":["Default"]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewLegacyClient("1,token", WithLegacyEndpoint(server.URL))
	if _, err := client.DescribeRecordType(context.Background(), DescribeRecordTypeRequest{DomainGrade: "DP_FREE"}); err != nil {
		t.Fatalf("DescribeRecordType: %v", err)
	}
	if _, err := client.DescribeRecordLineList(context.Background(), DescribeRecordLineListRequest{Domain: "example.com"}); err != nil {
		t.Fatalf("DescribeRecordLineList: %v", err)
	}
	if got, want := strings.Join(grades, ","), "DP_Free,DPG_Free"; got != want {
		t.Fatalf("canonical grades = %q, want %q", got, want)
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

func TestLegacyClientMapsArrayShapedRecordLines(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":{"code":"1","message":"ok"},"lines":[{"line_id":"0=0","name":"Default"},{"line_id":"7=0","name":"China Mobile"},{"line_id":"3=0","name":"China Unicom"}]}`))
	}))
	defer server.Close()

	client := NewLegacyClient("1,token", WithLegacyEndpoint(server.URL))
	result, err := client.DescribeRecordLineList(context.Background(), DescribeRecordLineListRequest{Domain: "example.com", DomainGrade: "DP_Free"})
	if err != nil {
		t.Fatalf("DescribeRecordLineList: %v", err)
	}
	if len(result.Lines) != 3 || result.Lines[0].LineID != "0=0" || result.Lines[1].LineName != "China Mobile" || result.Lines[2].LineID != "3=0" {
		t.Fatalf("unexpected array-shaped lines: %#v", result.Lines)
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

func TestLegacyClientUsesLegacyRecordLineKeyForDefault(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/Record.Modify" {
			t.Fatalf("unexpected lookup before default mutation: %s", r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		if got := r.Form.Get("record_line"); got != "default" {
			t.Fatalf("legacy mutation record_line = %q, want default; form=%v", got, r.Form)
		}
		if got := r.Form.Get("record_line_id"); got != "" {
			t.Fatalf("legacy mutation must not send unsupported record_line_id, got %q; form=%v", got, r.Form)
		}
		_, _ = w.Write([]byte(`{"status":{"code":"1","message":"ok"},"record":{"id":"91"}}`))
	}))
	defer server.Close()

	client := NewLegacyClient("1,token", WithLegacyEndpoint(server.URL))
	if _, err := client.ModifyRecord(context.Background(), RecordMutationRequest{
		DomainID: 6, RecordID: 8, SubDomain: "www", RecordType: "A",
		RecordLine: "默认", RecordLineID: "10=0", Value: "192.0.2.2",
	}); err != nil {
		t.Fatalf("ModifyRecord: %v", err)
	}
}

func TestLegacyClientResolvesModernRecordLineIDBeforeMutation(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/Record.Line":
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse line form: %v", err)
			}
			if got := r.Form.Get("domain_id"); got != "6" {
				t.Fatalf("line lookup domain_id = %q, want 6", got)
			}
			_, _ = w.Write([]byte(`{"status":{"code":"1","message":"ok"},"lines":{"asia":{"name":"Asia","sub_area":{"JP":"Japan"}}}}`))
		case "/Record.Modify":
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse modify form: %v", err)
			}
			if got := r.Form.Get("record_line"); got != "asia" {
				t.Fatalf("resolved record_line = %q, want asia; form=%v", got, r.Form)
			}
			if got := r.Form.Get("record_line_id"); got != "" {
				t.Fatalf("legacy mutation must not send record_line_id, got %q; form=%v", got, r.Form)
			}
			_, _ = w.Write([]byte(`{"status":{"code":"1","message":"ok"},"record":{"id":"91"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewLegacyClient("1,token", WithLegacyEndpoint(server.URL))
	if _, err := client.ModifyRecord(context.Background(), RecordMutationRequest{
		Domain: "example.com", DomainID: 6, RecordID: 8, SubDomain: "www", RecordType: "A",
		RecordLine: "Asia", RecordLineID: "10=0", Value: "192.0.2.2",
	}); err != nil {
		t.Fatalf("ModifyRecord: %v", err)
	}
	if got, want := strings.Join(paths, ","), "/Record.Line,/Record.Modify"; got != want {
		t.Fatalf("request order = %q, want %q", got, want)
	}
}

func TestLegacyClientUsesProviderNameForNumericInternationalLineID(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/Record.Line":
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse line form: %v", err)
			}
			if got := r.Form.Get("domain_grade"); got != "DPG_Enterprise" {
				t.Fatalf("line lookup domain_grade = %q, want DPG_Enterprise", got)
			}
			_, _ = w.Write([]byte(`{"status":{"code":"1","message":"ok"},"line_ids":{"Default":0,"Global":"3=0"},"lines":["Default","Global"],"line_groups":[]}`))
		case "/Record.Modify":
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse modify form: %v", err)
			}
			if got := r.Form.Get("record_line"); got != "Global" {
				t.Fatalf("resolved record_line = %q, want Global; form=%v", got, r.Form)
			}
			if got := r.Form.Get("record_line_id"); got != "" {
				t.Fatalf("legacy mutation must not send record_line_id, got %q; form=%v", got, r.Form)
			}
			_, _ = w.Write([]byte(`{"status":{"code":"1","message":"ok"},"record":{"id":"91"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewLegacyClient("1,token", WithLegacyEndpoint(server.URL))
	if _, err := client.ModifyRecord(context.Background(), RecordMutationRequest{
		Domain: "example.com", DomainID: 6, DomainGrade: "DPG_Enterprise", RecordID: 8, SubDomain: "www", RecordType: "A",
		RecordLine: "Global", RecordLineID: "3=0", Value: "192.0.2.2",
	}); err != nil {
		t.Fatalf("ModifyRecord: %v", err)
	}
	if got, want := strings.Join(paths, ","), "/Record.Line,/Record.Modify"; got != want {
		t.Fatalf("request order = %q, want %q", got, want)
	}
}

func TestLegacyClientCreatesEnterpriseGlobalRecord(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/Record.Line":
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse line form: %v", err)
			}
			if got := r.Form.Get("domain_grade"); got != "DPG_Enterprise" {
				t.Fatalf("line lookup domain_grade = %q, want DPG_Enterprise", got)
			}
			_, _ = w.Write([]byte(`{"status":{"code":"1","message":"ok"},"line_ids":{"Default":0,"Global":"3=0"},"lines":["Default","Global"]}`))
		case "/Record.Create":
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse create form: %v", err)
			}
			if got := r.Form.Get("record_line"); got != "Global" {
				t.Fatalf("created record_line = %q, want Global; form=%v", got, r.Form)
			}
			if got := r.Form.Get("record_line_id"); got != "" {
				t.Fatalf("legacy mutation must not send record_line_id, got %q", got)
			}
			_, _ = w.Write([]byte(`{"status":{"code":"1","message":"ok"},"record":{"id":"92"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewLegacyClient("1,token", WithLegacyEndpoint(server.URL))
	created, err := client.CreateRecord(context.Background(), RecordMutationRequest{
		Domain: "example.com", DomainID: 6, DomainGrade: "DPG_Enterprise", SubDomain: "www", RecordType: "A",
		RecordLine: "Global", RecordLineID: "3=0", Value: "192.0.2.2",
	})
	if err != nil || created.RecordID != 92 {
		t.Fatalf("CreateRecord result=%#v err=%v", created, err)
	}
	if got, want := strings.Join(paths, ","), "/Record.Line,/Record.Create"; got != want {
		t.Fatalf("request order = %q, want %q", got, want)
	}
}

func TestLegacyClientModifiesExistingRecordWithProviderNameWhenLineLookupMisses(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/Record.Line":
			_, _ = w.Write([]byte(`{"status":{"code":"1","message":"ok"},"line_ids":{"Default":0},"lines":["Default"]}`))
		case "/Record.Modify":
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse modify form: %v", err)
			}
			if got := r.Form.Get("record_line"); got != "Global" {
				t.Fatalf("fallback record_line = %q, want Global; form=%v", got, r.Form)
			}
			if got := r.Form.Get("record_line_id"); got != "" {
				t.Fatalf("legacy mutation must not send record_line_id, got %q", got)
			}
			_, _ = w.Write([]byte(`{"status":{"code":"1","message":"ok"},"record":{"id":"8"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewLegacyClient("1,token", WithLegacyEndpoint(server.URL))
	if _, err := client.ModifyRecord(context.Background(), RecordMutationRequest{
		Domain: "example.com", DomainID: 6, RecordID: 8, SubDomain: "www", RecordType: "A",
		RecordLine: "Global", RecordLineID: "3=0", Value: "192.0.2.2",
	}); err != nil {
		t.Fatalf("ModifyRecord: %v", err)
	}
	if got, want := strings.Join(paths, ","), "/Record.Line,/Record.Modify"; got != want {
		t.Fatalf("request order = %q, want %q", got, want)
	}
}

func TestLegacyClientDoesNotUseNumericLineNameAsModifyFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/Record.Line" {
			t.Fatalf("numeric line name must be rejected before mutation, got %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"status":{"code":"1","message":"ok"},"line_ids":{"Default":0},"lines":["Default"]}`))
	}))
	defer server.Close()

	client := NewLegacyClient("1,token", WithLegacyEndpoint(server.URL))
	_, err := client.ModifyRecord(context.Background(), RecordMutationRequest{
		Domain: "example.com", DomainID: 6, RecordID: 8, SubDomain: "www", RecordType: "A",
		RecordLine: "3=0", RecordLineID: "3=0", Value: "192.0.2.2",
	})
	if err == nil || !strings.Contains(err.Error(), "无法识别记录线路") {
		t.Fatalf("expected numeric line rejection, got %v", err)
	}
}

func TestLegacyAPIErrorExplainsIncorrectRecordLine(t *testing.T) {
	err := (&LegacyAPIError{Code: "26", Message: "Incorrect record line"}).Error()
	if !strings.Contains(err, "线路无效") || !strings.Contains(err, "Record.Line") || !strings.Contains(err, "线路名称") || !strings.Contains(err, "default") || !strings.Contains(err, "腾讯云线路 ID") {
		t.Fatalf("incorrect record line error is not actionable: %q", err)
	}
}

func TestLegacyAPIErrorPreservesProviderTokenFailure(t *testing.T) {
	for _, code := range []string{"-1", "10004"} {
		err := (&LegacyAPIError{Code: code, Message: "Token verification failed"}).Error()
		if !strings.Contains(err, "Token verification failed") || !strings.Contains(err, "DNSPOD_API_TOKEN") || !strings.Contains(err, "错误码="+code) {
			t.Fatalf("token error lost provider diagnostics for code %s: %q", code, err)
		}
	}
}

func TestLegacyClientRetriesTransientUnknownProviderError(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls < 3 {
			_, _ = w.Write([]byte(`{"status":{"code":"-1","message":"Unknown error, please retry later."}}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":{"code":"1","message":"ok"},"info":{"domain_total":"2"},"domains":[]}`))
	}))
	defer server.Close()

	client := NewLegacyClient("1,token", WithLegacyEndpoint(server.URL))
	client.retryDelays = []time.Duration{0, 0}
	result, err := client.DescribeDomainList(context.Background(), DescribeDomainListRequest{Limit: 1})
	if err != nil || result.Total != 2 || calls != 3 {
		t.Fatalf("transient DNSPod retry result=%#v calls=%d err=%v", result, calls, err)
	}
}

func TestLegacyClientDoesNotRetryPermanentTokenFailure(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"status":{"code":"10004","message":"Token verification failed"}}`))
	}))
	defer server.Close()

	client := NewLegacyClient("1,token", WithLegacyEndpoint(server.URL))
	client.retryDelays = []time.Duration{0, 0}
	_, err := client.DescribeDomainList(context.Background(), DescribeDomainListRequest{Limit: 1})
	if err == nil || calls != 1 {
		t.Fatalf("permanent token failure should not retry: calls=%d err=%v", calls, err)
	}
}
