package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"forest/go-api/internal/appleid"
	"forest/go-api/internal/config"
	"forest/go-api/internal/session"
)

func (f *fakeAppleIDService) AdminFinanceStats(context.Context, appleid.AdminFinanceRequest) (appleid.AdminFinanceStatsResult, error) {
	return appleid.AdminFinanceStatsResult{}, f.adminErr
}
func (f *fakeAppleIDService) AdminFinanceTransactions(context.Context, appleid.AdminFinanceRequest) (appleid.AdminFinanceTransactionsResult, error) {
	return appleid.AdminFinanceTransactionsResult{}, f.adminErr
}

type fakeAppleFinanceService struct {
	fakeAppleIDService
	calls int
	last  appleid.AdminFinanceRequest
}

func (f *fakeAppleFinanceService) AdminFinanceStats(_ context.Context, req appleid.AdminFinanceRequest) (appleid.AdminFinanceStatsResult, error) {
	f.calls++
	f.last = req
	return appleid.AdminFinanceStatsResult{StartDate: "2026-09-01", EndDate: "2026-09-30"}, f.adminErr
}
func (f *fakeAppleFinanceService) AdminFinanceTransactions(_ context.Context, req appleid.AdminFinanceRequest) (appleid.AdminFinanceTransactionsResult, error) {
	f.calls++
	f.last = req
	return appleid.AdminFinanceTransactionsResult{Data: []appleid.AdminFinanceTransaction{{ID: "payment:41", OrderID: 41, TradeNo: "apple-41", EventType: "payment", Amount: 2010, OccurredAt: 1790838000}}, Total: 1}, f.adminErr
}
func appleFinanceRequest(service *fakeAppleFinanceService, identity *session.Identity, method, endpoint, query string) *httptest.ResponseRecorder {
	router := NewRouter(config.Config{AdminPath: "localadmin"}, WithSessionService(strictAppleIDSession{identity: identity}), WithAppleIDServices(service, service))
	req := httptest.NewRequest(method, "/api/v1/localadmin/apple-id/finance/"+endpoint+"?auth_data=fixture-token"+query, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}
func TestAppleIDFinanceRequiresAdmin(t *testing.T) {
	for _, endpoint := range []string{"stats", "transactions"} {
		for _, identity := range []*session.Identity{nil, {ID: 7}} {
			service := &fakeAppleFinanceService{}
			rec := appleFinanceRequest(service, identity, http.MethodGet, endpoint, "")
			if rec.Code != http.StatusForbidden || service.calls != 0 {
				t.Fatalf("%s: status=%d calls=%d", endpoint, rec.Code, service.calls)
			}
			assertAppleIDNoStore(t, rec)
		}
	}
}
func TestAppleIDFinanceRejectsNonGET(t *testing.T) {
	for _, endpoint := range []string{"stats", "transactions"} {
		for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodHead} {
			service := &fakeAppleFinanceService{}
			rec := appleFinanceRequest(service, &session.Identity{ID: 77, IsAdmin: 1}, method, endpoint, "")
			if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != http.MethodGet || service.calls != 0 {
				t.Fatalf("%s %s: status=%d allow=%q calls=%d", method, endpoint, rec.Code, rec.Header().Get("Allow"), service.calls)
			}
		}
	}
}
func TestAppleIDFinanceParsesFiltersAndResponse(t *testing.T) {
	product := int64(3)
	want := appleid.AdminFinanceRequest{StartDate: "2026-09-01", EndDate: "2026-09-30", ProductID: &product, Current: 2, PageSize: 50}
	for _, endpoint := range []string{"stats", "transactions"} {
		service := &fakeAppleFinanceService{}
		rec := appleFinanceRequest(service, &session.Identity{ID: 77, IsAdmin: 1}, http.MethodGet, endpoint, "&start_date=2026-09-01&end_date=2026-09-30&product_id=3&current=2&page_size=50")
		if rec.Code != http.StatusOK || service.calls != 1 || !reflect.DeepEqual(service.last, want) {
			t.Fatalf("%s: status=%d calls=%d req=%+v", endpoint, rec.Code, service.calls, service.last)
		}
		assertAppleIDNoStore(t, rec)
		if endpoint == "transactions" {
			var result struct {
				Data  []appleid.AdminFinanceTransaction `json:"data"`
				Total int64                             `json:"total"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.Total != 1 || len(result.Data) != 1 || result.Data[0].Amount != 2010 || result.Data[0].EventType != "payment" {
				t.Fatalf("unexpected response: %s", rec.Body.String())
			}
		} else {
			var result struct {
				Data appleid.AdminFinanceStatsResult `json:"data"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.Data.StartDate != want.StartDate || result.Data.EndDate != want.EndDate {
				t.Fatalf("unexpected response: %s", rec.Body.String())
			}
		}
	}
}
func TestAppleIDFinanceRejectsMalformedFilters(t *testing.T) {
	for _, endpoint := range []string{"stats", "transactions"} {
		for _, query := range []string{"&product_id=bad", "&product_id=0", "&product_id=-1", "&current=-1", "&current=1.5", "&page_size=abc", "&page_size=-1", "&current=9223372036854775808"} {
			service := &fakeAppleFinanceService{}
			rec := appleFinanceRequest(service, &session.Identity{ID: 77, IsAdmin: 1}, http.MethodGet, endpoint, query)
			if rec.Code != http.StatusBadRequest || service.calls != 0 {
				t.Fatalf("%s%s: status=%d calls=%d", endpoint, query, rec.Code, service.calls)
			}
		}
	}
}
func TestAppleIDFinanceMapsServiceErrors(t *testing.T) {
	for _, endpoint := range []string{"stats", "transactions"} {
		for _, tt := range []struct {
			err  error
			code int
		}{{appleid.ErrInvalidParameter, 400}, {appleid.ErrUnavailable, 503}, {errors.New("query failed"), 500}} {
			service := &fakeAppleFinanceService{fakeAppleIDService: fakeAppleIDService{adminErr: tt.err}}
			rec := appleFinanceRequest(service, &session.Identity{ID: 77, IsAdmin: 1}, http.MethodGet, endpoint, "")
			if rec.Code != tt.code || service.calls != 1 {
				t.Fatalf("%s: status=%d want=%d calls=%d", endpoint, rec.Code, tt.code, service.calls)
			}
		}
	}
}
