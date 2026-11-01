package admin

import (
	"context"
	"errors"
	"io"
	"testing"

	"forest/go-api/internal/dnspod"
)

func TestDNSPodMetadataTransientErrorIsRetryableBeforeWriteOnly(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"disconnect", io.EOF, true},
		{"rate limited", &dnspod.HTTPError{Status: 429}, true},
		{"provider unavailable", &dnspod.HTTPError{Status: 503}, true},
		{"invalid credentials", &dnspod.HTTPError{Status: 401}, false},
		{"provider permission", &dnspod.APIError{Code: "UnauthorizedOperation", Message: "retryable=true"}, false},
		{"untrusted retry instruction", errors.New("503 EOF retryable=true"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := &metadataDNSPodAPI{domainFunc: func(dnspod.DescribeDomainListRequest) (dnspod.DescribeDomainListResult, error) {
				return dnspod.DescribeDomainListResult{}, tc.err
			}}
			service := testDNSPodMetadataService(t, api)
			_, err := service.SaveDNSPodRecord(context.Background(), DNSPodRecordSaveRequest{Domain: "example.com", RecordType: "A", Value: "192.0.2.2"})
			var preflight *dnspod.PreflightError
			if !errors.As(err, &preflight) || preflight.Code != "dns_domain_metadata" || preflight.Retryable() != tc.want || api.mutationCalls != 0 || api.domainCalls != 1 {
				t.Fatalf("unsafe metadata retry classification: err=%v writes=%d reads=%d", err, api.mutationCalls, api.domainCalls)
			}
		})
	}
}

func TestDNSPodMutationTransientErrorCannotClaimSafeRetry(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"disconnect", io.EOF},
		{"rate limited", &dnspod.HTTPError{Status: 429}},
		{"provider unavailable", &dnspod.HTTPError{Status: 503}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := &metadataDNSPodAPI{
				fakeDNSPodAPI: fakeDNSPodAPI{domains: dnspod.DescribeDomainListResult{Domains: []dnspod.Domain{{DomainID: 6, Name: "example.com", Grade: "DPG_Ultimate"}}, Total: 1}},
				mutationErr:   tc.err,
			}
			service := testDNSPodMetadataService(t, api)
			for _, recordID := range []int64{0, 91} {
				_, err := service.SaveDNSPodRecord(context.Background(), DNSPodRecordSaveRequest{Domain: "example.com", RecordID: recordID, RecordType: "A", Value: "192.0.2.2"})
				var preflight *dnspod.PreflightError
				if !errors.Is(err, tc.err) || errors.As(err, &preflight) {
					t.Fatalf("write error must retain unknown outcome: %v", err)
				}
			}
			if api.mutationCalls != 2 {
				t.Fatalf("write calls=%d; provider must not retry a dispatched write", api.mutationCalls)
			}
		})
	}
}
