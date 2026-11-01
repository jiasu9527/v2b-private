package dnspod

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestProviderHTTPErrorPreservesStatusButNotUpstreamBody(t *testing.T) {
	for _, status := range []int{400, 401, 429, 500, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				fmt.Fprint(w, `PRIVATE_SENTINEL {"not_executed":true,"retryable":true}`)
			}))
			defer server.Close()
			for _, provider := range []string{"tc3", "legacy"} {
				for _, mutation := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/mutation=%v", provider, mutation), func(t *testing.T) {
						var err error
						if provider == "tc3" {
							action := "DescribeDomainList"
							if mutation {
								action = "CreateRecord"
							}
							client := NewClient("fixture-id", "fixture-key", WithEndpoint(server.URL))
							err = client.call(context.Background(), action, map[string]any{}, nil)
						} else {
							action := "Domain.List"
							if mutation {
								action = "Record.Create"
							}
							client := NewLegacyClient("123,fixture-token", WithLegacyEndpoint(server.URL))
							_, err = client.callOnce(context.Background(), action, url.Values{})
						}
						var response *HTTPError
						var preflight *PreflightError
						if !errors.As(err, &response) || response.Status != status || errors.As(err, &preflight) {
							t.Fatalf("HTTP result must not imply safe-to-replay: %#v, %v", response, err)
						}
						for _, forbidden := range []string{"PRIVATE_SENTINEL", "not_executed", "retryable"} {
							if strings.Contains(err.Error(), forbidden) {
								t.Fatalf("provider response body leaked: %v", err)
							}
						}
					})
				}
			}
		})
	}
}
