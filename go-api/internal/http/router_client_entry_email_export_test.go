package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/cliententry"
	"forest/go-api/internal/config"
	"forest/go-api/internal/session"
)

type emailExportPolicyService struct {
	fakeAdminService
	fetchCalls int
}

func (f *emailExportPolicyService) ListClientEntryUserPolicies(context.Context) ([]admin.ClientEntryUserPolicyRecord, error) {
	f.fetchCalls++
	return f.clientEntryUserPolicies, f.err
}

// Manual email export uses the existing complete rule response. It must not
// silently become a paged user lookup or replace lists with condition matches.
func TestClientEntryPolicyFetchRetainsCompleteManualEmailListsForLocalExport(t *testing.T) {
	emails := make([]json.RawMessage, 205)
	for index := range emails {
		emails[index], _ = json.Marshal(fmt.Sprintf("manual-%03d@example.test", index))
	}
	policies := make([]admin.ClientEntryUserPolicyRecord, 127)
	for index := range policies {
		policies[index] = admin.ClientEntryUserPolicyRecord{ID: int64(index + 1), Name: fmt.Sprintf("Rule %d", index+1), Mode: "standard", Action: "override", Conditions: []cliententry.Condition{}}
	}
	policies[0].Conditions = []cliententry.Condition{
		{Field: "email", Operator: "in", Values: emails},
		{Field: "plan_id", Operator: "eq", Value: json.RawMessage(`99`)},
		{Field: "email", Operator: "in", Values: []json.RawMessage{json.RawMessage(`"another-list@example.test"`), json.RawMessage(`"not-registered@example.test"`)}},
		{Field: "ua", Operator: "contains_any", Values: []json.RawMessage{json.RawMessage(`"Clash"`)}},
	}
	policies[126].Conditions = []cliententry.Condition{{Field: "email", Operator: "in", Values: []json.RawMessage{}}}
	for _, identity := range []*session.Identity{{ID: 7, IsAdmin: 1}, {ID: 8, IsAdmin: 1, IsStaff: 1}} {
		t.Run(fmt.Sprintf("admin_%d", identity.ID), func(t *testing.T) {
			service := &emailExportPolicyService{fakeAdminService: fakeAdminService{clientEntryUserPolicies: policies}}
			router := NewRouter(config.Config{AdminPath: "localadmin"}, WithSessionService(strictAppleIDSession{identity: identity}), WithAdminService(service))
			req := httptest.NewRequest(http.MethodGet, "/api/v1/localadmin/server/client-entry-user-policy/fetch?auth_data=admin-token&current=2&page_size=1", nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK || service.fetchCalls != 1 {
				t.Fatalf("fetch: status=%d calls=%d body=%s", rec.Code, service.fetchCalls, rec.Body.String())
			}
			assertAppleIDNoStore(t, rec)
			if rec.Header().Get("Vary") != "Authorization" {
				t.Fatal("policy response must vary by authorization")
			}
			var response struct {
				Data []struct {
					ID         int64                        `json:"id"`
					Conditions []map[string]json.RawMessage `json:"conditions"`
				} `json:"data"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if len(response.Data) != 127 || len(response.Data[0].Conditions) != 4 {
				t.Fatalf("rules or conditions were paginated/truncated: %s", rec.Body.String())
			}
			var returned []json.RawMessage
			if err := json.Unmarshal(response.Data[0].Conditions[0]["values"], &returned); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(returned, emails) {
				t.Fatalf("complete manual list changed: got %d emails, want %d", len(returned), len(emails))
			}
			var otherEmails []string
			if err := json.Unmarshal(response.Data[0].Conditions[2]["values"], &otherEmails); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(otherEmails, []string{"another-list@example.test", "not-registered@example.test"}) {
				t.Fatalf("second manual condition filtered or lost: %#v", otherEmails)
			}
			if string(response.Data[0].Conditions[1]["value"]) != "99" || string(response.Data[126].Conditions[0]["values"]) != "[]" {
				t.Fatal("additional condition or explicit empty email list was lost")
			}
		})
	}
}

func TestClientEntryPolicyFetchRejectsStaffAndOrdinaryUsersBeforeReadingManualEmails(t *testing.T) {
	for _, identity := range []*session.Identity{nil, {ID: 41}, {ID: 42, IsStaff: 1}} {
		t.Run(fmt.Sprint(identity), func(t *testing.T) {
			service := &emailExportPolicyService{}
			router := NewRouter(config.Config{AdminPath: "localadmin"}, WithSessionService(strictAppleIDSession{identity: identity}), WithAdminService(service))
			req := httptest.NewRequest(http.MethodGet, "/api/v1/localadmin/server/client-entry-user-policy/fetch?auth_data=fixture-token", nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden || service.fetchCalls != 0 {
				t.Fatalf("manual email list exposed: status=%d fetchCalls=%d body=%s", rec.Code, service.fetchCalls, rec.Body.String())
			}
			assertAppleIDNoStore(t, rec)
			if rec.Header().Get("Vary") != "Authorization" {
				t.Fatal("denied response must vary by authorization")
			}
		})
	}
}
