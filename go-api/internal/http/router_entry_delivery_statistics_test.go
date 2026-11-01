package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"forest/go-api/internal/aiops"
	"forest/go-api/internal/config"
	"forest/go-api/internal/entryevidence"
	"forest/go-api/internal/session"
)

type deliveryStatisticsHTTPStore struct {
	evidenceHTTPStore
	err error
}

func (s *deliveryStatisticsHTTPStore) Statistics(_ context.Context, f entryevidence.Filter) (entryevidence.DeliveryStatistics, error) {
	s.calls++
	s.filter = f
	return entryevidence.DeliveryStatistics{Users: 3, Requests: 17, FirstAt: 100, LastAt: 200}, s.err
}
func (s *deliveryStatisticsHTTPStore) Users(_ context.Context, f entryevidence.Filter) (entryevidence.DeliveryUsersPage, error) {
	s.calls++
	s.filter = f
	return entryevidence.DeliveryUsersPage{Total: 3, Limit: f.Limit, Offset: f.Offset}, s.err
}
func (s *deliveryStatisticsHTTPStore) Versions(_ context.Context, f entryevidence.Filter) (entryevidence.DeliveryVersionsPage, error) {
	s.calls++
	s.filter = f
	return entryevidence.DeliveryVersionsPage{Total: 2, Limit: f.Limit, Offset: f.Offset}, s.err
}

func TestEntryDeliveryStatisticsHTTPReadOnlyAndMetadata(t *testing.T) {
	for _, action := range []string{"delivery-stats", "delivery-users", "delivery-versions"} {
		for _, tc := range []struct {
			name, method string
			actor        *session.Identity
			want         int
		}{
			{"admin", "GET", &session.Identity{ID: 1, IsAdmin: 1}, 200},
			{"staff", "GET", &session.Identity{ID: 2, IsStaff: 1}, 200},
			{"ordinary", "GET", &session.Identity{ID: 3}, 403},
			{"post", "POST", &session.Identity{ID: 1, IsAdmin: 1}, 405},
		} {
			t.Run(action+"/"+tc.name, func(t *testing.T) {
				q := url.Values{"policy_id": {"7"}, "group_id": {"9"}, "collection_id": {"3"}, "provider_id": {"4"}, "server_type": {"vmess"}, "server_id": {"-7001"}, "host": {"192.0.2.1"}, "port": {"54101"}, "ua": {"Forest"}, "account": {"72"}, "from": {"100"}, "to": {"201"}, "rule_revision": {strings.Repeat("a", 64)}, "version": {strings.Repeat("b", 32)}, "transport": {"subscription"}, "complete_only": {"false"}, "current": {"2"}, "page_size": {"20"}}
				r := httptest.NewRequest(tc.method, "http://fixture/?auth_data=fixture&"+q.Encode(), nil)
				w := httptest.NewRecorder()
				store := &deliveryStatisticsHTTPStore{}
				handleEntryEvidence(w, r, strictAppleIDSession{identity: tc.actor}, nil, store, action)
				if w.Code != tc.want {
					t.Fatal(w.Code, w.Body.String())
				}
				if tc.want != 200 {
					if store.calls != 0 {
						t.Fatal("unauthorized call reached statistics")
					}
					return
				}
				if !store.filter.CompleteOnly || store.filter.CollectionID != 3 || store.filter.ProviderID != 4 || store.filter.ServerID != -7001 || store.filter.UserID != 72 || store.filter.Version != strings.Repeat("b", 32) || store.filter.Offset != 20 {
					t.Fatal(store.filter)
				}
				if w.Header().Get("Cache-Control") == "" || w.Header().Get("Vary") != "Authorization" {
					t.Fatal("private statistics cacheable")
				}
				var response struct {
					Data map[string]json.RawMessage `json:"data"`
				}
				if json.Unmarshal(w.Body.Bytes(), &response) != nil {
					t.Fatal(w.Body.String())
				}
				for _, key := range []string{"keep_days", "available_from", "available_to", "warnings", "query"} {
					if response.Data[key] == nil {
						t.Fatal("missing coverage metadata", key)
					}
				}
				if action == "delivery-stats" && string(response.Data["users"]) != "3" {
					t.Fatal(w.Body.String())
				}
			})
		}
	}
}

func TestEntryDeliveryStatisticsHTTPFailureAndFilterValidation(t *testing.T) {
	for _, action := range []string{"delivery-stats", "delivery-users", "delivery-versions"} {
		for _, unavailable := range []bool{false, true} {
			var store entryEvidenceReader = &deliveryStatisticsHTTPStore{err: errors.New("PRIVATE-SENTINEL query")}
			want := 500
			if unavailable {
				store = &evidenceHTTPStore{}
				want = 503
			}
			w := httptest.NewRecorder()
			handleEntryEvidence(w, httptest.NewRequest("GET", "/?auth_data=fixture", nil), strictAppleIDSession{identity: &session.Identity{ID: 1, IsAdmin: 1}}, nil, store, action)
			if w.Code != want || strings.Contains(w.Body.String(), "PRIVATE-SENTINEL") {
				t.Fatal(w.Code, w.Body.String())
			}
		}
	}
	for _, q := range []url.Values{
		{"version": {"one", "two"}}, {"rule_revision": {"one", "two"}}, {"host": {"one", "two"}},
		{"collection_id": {"-1"}}, {"provider_id": {"bad"}}, {"server_id": {"0"}}, {"server_id": {"-1", "1"}},
		{"kind": {"policy"}, "target_id": {"7"}, "policy_id": {"8"}},
		{"kind": {"split_group"}, "target_id": {"7"}, "group_id": {"8"}},
		{"kind": {"policy"}, "target_id": {"7"}, "policy_id": {"0"}},
	} {
		if _, err := parseEntryDeliveryFilter(q); err == nil {
			t.Fatal("ambiguous/invalid filter accepted", q)
		}
	}
	for _, q := range []url.Values{
		{"kind": {"policy"}, "target_id": {"7"}, "policy_id": {"7"}, "group_id": {"9"}},
		{"kind": {"split_group"}, "target_id": {"9"}, "group_id": {"9"}, "policy_id": {"7"}},
		{"kind": {"policy"}, "target_id": {"7"}, "group_id": {"9"}},
		{"kind": {"split_group"}, "target_id": {"9"}, "policy_id": {"7"}},
	} {
		filter, err := parseEntryDeliveryFilter(q)
		if err != nil || filter.PolicyID != 7 || filter.GroupID != 9 {
			t.Fatal("compatible aliases or distinct node dimensions rejected", q, filter, err)
		}
	}
}

func TestEntryDeliveryStatisticsAIDirectoryContract(t *testing.T) {
	p := newPanelOperations(config.Config{}, &routerState{})
	staff := aiops.PanelOperationActor{ID: 1}
	for _, name := range []string{"delivery_stats", "delivery_users", "delivery_versions"} {
		op, ok := p.operations["entry_evidence."+name]
		if !ok || op.Mutating || op.AdminOnly || op.Method != http.MethodGet {
			t.Fatal("missing readonly operation", name)
		}
		args := []byte(`{"server_id":-7001,"port":"2000-2009","collection_id":3,"version":"` + strings.Repeat("a", 32) + `","from":100,"to":200}`)
		if err := p.Validate(context.Background(), staff, op.Name, args); err != nil {
			t.Fatal(name, err)
		}
		if err := p.Validate(context.Background(), staff, op.Name, []byte(`{"token":"must-not-be-read"}`)); err == nil {
			t.Fatal("credential field accepted", name)
		}
		for _, invalid := range []string{
			`{"page_size":0}`, `{"page_size":101}`, `{"current":0}`, `{"current":10001}`,
			`{"policy_id":-1}`, `{"group_id":0}`, `{"provider_id":-1}`, `{"collection_id":0}`,
			`{"from":-1}`, `{"to":-1}`, `{"server_type":"unknown"}`, `{"transport":"clash"}`,
			`{"rule_revision":"short"}`, `{"version":"short"}`,
			`{"version":"` + strings.Repeat("a", 33) + `"}`,
			`{"host":"` + strings.Repeat("a", 256) + `"}`,
			`{"ua":"` + strings.Repeat("a", 1025) + `"}`,
		} {
			if err := p.Validate(context.Background(), staff, op.Name, []byte(invalid)); err == nil {
				t.Fatal("invalid declaration value accepted", name, invalid)
			}
		}
		for _, valid := range []string{`{}`, `{"server_id":-7001}`, `{"server_type":"vless","current":1,"page_size":100}`, `{"rule_revision":"` + strings.Repeat("a", 64) + `"}`} {
			if err := p.Validate(context.Background(), staff, op.Name, []byte(valid)); err != nil {
				t.Fatal("valid optional filter rejected", name, valid, err)
			}
		}
	}
}
