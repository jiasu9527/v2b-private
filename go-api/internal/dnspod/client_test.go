package dnspod

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClientSignsTC3RequestAndDecodesResponse(t *testing.T) {
	var gotAction string
	var gotVersion string
	var gotAuthorization string
	var gotPayload map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAction = r.Header.Get("X-TC-Action")
		gotVersion = r.Header.Get("X-TC-Version")
		gotAuthorization = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotPayload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Response":{"DomainCountInfo":{"DomainTotal":1},"DomainList":[{"DomainId":7,"Name":"example.com","Status":"ENABLE","RecordCount":3}],"RequestId":"req-1"}}`))
	}))
	defer server.Close()

	client := NewClient("AKIDEXAMPLE", "SECRETKEYEXAMPLE",
		WithEndpoint(server.URL),
		WithClock(func() time.Time { return time.Unix(1551113065, 0).UTC() }),
	)
	result, err := client.DescribeDomainList(context.Background(), DescribeDomainListRequest{Offset: 0, Limit: 20})
	if err != nil {
		t.Fatalf("DescribeDomainList: %v", err)
	}
	if gotAction != "DescribeDomainList" || gotVersion != APIVersion {
		t.Fatalf("unexpected action/version: %q %q", gotAction, gotVersion)
	}
	if gotPayload["Limit"] != float64(20) || gotPayload["Offset"] != float64(0) {
		t.Fatalf("unexpected payload: %#v", gotPayload)
	}
	if !strings.HasPrefix(gotAuthorization, "TC3-HMAC-SHA256 Credential=AKIDEXAMPLE/2019-02-25/dnspod/tc3_request, SignedHeaders=content-type;host, Signature=") {
		t.Fatalf("unexpected authorization: %q", gotAuthorization)
	}
	if result.Total != 1 || len(result.Domains) != 1 || result.Domains[0].Name != "example.com" || result.RequestID != "req-1" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestClientReturnsDNSPodAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"Response":{"Error":{"Code":"AuthFailure.SecretIdNotFound","Message":"SecretId 不存在"},"RequestId":"req-error"}}`))
	}))
	defer server.Close()

	client := NewClient("bad-id", "bad-key", WithEndpoint(server.URL))
	_, err := client.DescribeDomainList(context.Background(), DescribeDomainListRequest{Limit: 20})
	if err == nil {
		t.Fatal("expected API error")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("expected *APIError, got %T: %v", err, err)
	}
	if apiErr.Code != "AuthFailure.SecretIdNotFound" || apiErr.RequestID != "req-error" || !strings.Contains(apiErr.Error(), "SecretId 不存在") {
		t.Fatalf("unexpected API error: %#v", apiErr)
	}
}

func TestClientTranslatesEnglishCredentialErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"Response":{"Error":{"Code":"AuthFailure.SecretIdNotFound","Message":"The SecretId is not found, please ensure that your SecretId is correct."},"RequestId":"req-english"}}`))
	}))
	defer server.Close()

	client := NewClient("bad-id", "bad-key", WithEndpoint(server.URL))
	_, err := client.DescribeDomainList(context.Background(), DescribeDomainListRequest{Limit: 1})
	if err == nil {
		t.Fatal("expected API error")
	}
	message := err.Error()
	if !strings.Contains(message, "DNSPod SecretId 不存在") ||
		!strings.Contains(message, "腾讯云 API 密钥") ||
		!strings.Contains(message, "AuthFailure.SecretIdNotFound") ||
		!strings.Contains(message, "req-english") {
		t.Fatalf("expected actionable Chinese DNSPod error, got %q", message)
	}
}

func TestClientHonorsCancelledContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := NewClient("id", "key", WithEndpoint(server.URL))
	_, err := client.DescribeDomainList(ctx, DescribeDomainListRequest{Limit: 20})
	if err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("expected context cancellation, got %v", err)
	}
}
