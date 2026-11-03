package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/aiops"
	"forest/go-api/internal/config"
	"forest/go-api/internal/session"
	"github.com/DATA-DOG/go-sqlmock"
)

func TestAdminOrderFilterInputToSQLAndEcho(t *testing.T) {
	filter := `[{"key":"invite_user_id","condition":"=","value":61624},{"key":"created_at","condition":"<","value":"1790812800"}]`
	bracket := url.Values{"filter[0][key]": {"invite_user_id"}, "filter[0][condition]": {"="}, "filter[0][value]": {"61624"}, "filter[1][key]": {"created_at"}, "filter[1][condition]": {"<"}, "filter[1][value]": {"1790812800"}}
	for _, format := range []string{"query_json", "query_brackets", "body_json"} {
		t.Run(format, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			service := admin.NewDBService(config.Config{}, db)
			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT COUNT\(\*\) FROM v2_order o WHERE o.invite_user_id = \$1 AND o.created_at < \$2`).WithArgs("61624", "1790812800").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
			mock.ExpectQuery(`(?s)SELECT COALESCE\(json_agg\(row_to_json\(t\)\).*WHERE o.invite_user_id = \$1 AND o.created_at < \$2.*LIMIT \$3 OFFSET \$4`).WithArgs("61624", "1790812800", int64(10), int64(0)).WillReturnRows(sqlmock.NewRows([]string{"json"}).AddRow(`[ {"id":7,"invite_user_id":61624,"created_at":1700000000} ]`))
			mock.ExpectCommit()
			req := httptest.NewRequest(http.MethodGet, "/order/fetch?filter="+url.QueryEscape(filter), nil)
			if format == "query_brackets" {
				req = httptest.NewRequest(http.MethodGet, "/order/fetch?"+bracket.Encode(), nil)
			}
			if format == "body_json" {
				req = httptest.NewRequest(http.MethodPost, "/order/fetch", strings.NewReader(`{"filter":`+filter+`}`))
				req.Header.Set("Content-Type", "application/json")
			}
			req.Header.Set("Authorization", "fixture")
			rec := httptest.NewRecorder()
			handleAdminOrderFetch(rec, req, strictAppleIDSession{identity: &session.Identity{ID: 1, IsAdmin: 1}}, service)
			if rec.Code != 200 {
				t.Fatal(rec.Code, rec.Body.String())
			}
			var got struct {
				Total   int64               `json:"total"`
				Applied []admin.OrderFilter `json:"applied_filters"`
				Data    []map[string]any    `json:"data"`
			}
			if err = json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Total != 1 || len(got.Applied) != 2 || got.Applied[0].Value != "61624" || got.Applied[1].Key != "created_at" || len(got.Data) != 1 {
				t.Fatal(got, err)
			}
			if err = mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type strictFilterUserFixture struct {
	admin.Service
	called  bool
	filters []admin.UserFilter
}

func (s *strictFilterUserFixture) ListUsers(_ context.Context, req admin.UserFetchRequest) (admin.UserListResult, error) {
	s.called = true
	s.filters = req.Filters
	return admin.UserListResult{Data: []map[string]any{}, Total: 0}, nil
}
func (s *strictFilterUserFixture) DeleteUsers(_ context.Context, filters []admin.UserFilter) (bool, error) {
	s.called = true
	s.filters = filters
	return true, nil
}

func TestAdminUserTimeFiltersAndMalformedPredicatesFailBeforeDispatch(t *testing.T) {
	ss := strictAppleIDSession{identity: &session.Identity{ID: 1, IsAdmin: 1}}
	cases := []struct {
		raw  string
		want int
	}{
		{`[{"key":"created_at","condition":"<","value":"1790812800"}]`, 200},
		{`[{"key":"unknown","condition":"=","value":"1"}]`, 400},
		{`[{"key":"created_at","condition":"<","value":"not-a-number"}]`, 400},
		{`[{"key":"created_at","condition":"<"}]`, 400},
		{`[{"key":"created_at","condition":"<","value":null}]`, 400},
		{`[{"key":"email","condition":"=","value":null}]`, 400},
		{`[{"key":"created_at","condition":"<","value":1,"extra":true}]`, 400},
		{`[{"key":"created_at","condition":"<","value":{}}]`, 400},
		{`null`, 400}, {`{}`, 400}, {`[map[key:created_at value:1]]`, 400}, {`[] trailing`, 400},
	}
	for i, tc := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			service := &strictFilterUserFixture{}
			req := httptest.NewRequest(http.MethodGet, "/user/fetch?filter="+url.QueryEscape(tc.raw), nil)
			req.Header.Set("Authorization", "fixture")
			rec := httptest.NewRecorder()
			handleAdminUserFetch(rec, req, ss, service)
			if rec.Code != tc.want || service.called != (tc.want == 200) {
				t.Fatal(rec.Code, rec.Body.String(), service.called)
			}
			if tc.want == 200 && (len(service.filters) != 1 || service.filters[0].Key != "created_at") {
				t.Fatal(service.filters)
			}
		})
	}
	for _, query := range []string{"filter[x][key]=id", "filter[-1][key]=id", "filter[0][key]=id&filter[0][condition]=%3D", "filter[0][key]=id&filter[0][key]=email&filter[0][condition]=%3D&filter[0][value]=1", "invite_user_id=61624", "filter="} {
		for _, mutating := range []bool{false, true} {
			service := &strictFilterUserFixture{}
			req := httptest.NewRequest(http.MethodPost, "/user/"+"?"+query, nil)
			req.Header.Set("Authorization", "fixture")
			rec := httptest.NewRecorder()
			if mutating {
				handleAdminUserAllDel(rec, req, ss, service)
			} else {
				handleAdminUserFetch(rec, req, ss, service)
			}
			if rec.Code != 400 || service.called {
				t.Fatalf("unsafe query %q mutation=%t: %d %s", query, mutating, rec.Code, rec.Body.String())
			}
		}
	}
}

func TestAdminFilterDuplicateViewsMustAgree(t *testing.T) {
	good := map[string]string{"filter": `[{"key":"id","condition":"=","value":1}]`, "filter[0][key]": "id", "filter[0][condition]": "=", "filter[0][value]": "1"}
	if got, err := userFilters(good); err != nil || len(got) != 1 {
		t.Fatal(got, err)
	}
	good["filter[0][value]"] = "2"
	if _, err := userFilters(good); !errors.Is(err, admin.ErrInvalidFilter) {
		t.Fatal("conflicting filter representations accepted", err)
	}
}

func TestAIOrderListFilterArrayDispatchesToStrictSQL(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM v2_order o WHERE o.invite_user_id = \$1 AND o.created_at < \$2`).WithArgs("61624", "1790812800").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`(?s)SELECT COALESCE\(json_agg\(row_to_json\(t\)\).*WHERE o.invite_user_id = \$1 AND o.created_at < \$2`).WithArgs("61624", "1790812800", int64(10), int64(0)).WillReturnRows(sqlmock.NewRows([]string{"json"}).AddRow(`[{"id":1,"invite_user_id":61624,"created_at":1700000000}]`))
	mock.ExpectCommit()
	p := newPanelOperations(config.Config{AdminPath: "control"}, &routerState{admin: admin.NewDBService(config.Config{}, db)})
	raw, err := p.Execute(context.Background(), aiops.PanelOperationActor{ID: 1, IsAdmin: true}, "orders.list", json.RawMessage(`{"filter":[{"key":"invite_user_id","condition":"=","value":"61624"},{"key":"created_at","condition":"<","value":"1790812800"}]}`))
	if err != nil || !strings.Contains(string(raw), `"applied_filters"`) || !strings.Contains(string(raw), `"total":1`) {
		t.Fatal(string(raw), err)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	// Unknown filter fields are a safe 400, never a fallback to the entire table.
	_, err = p.Execute(context.Background(), aiops.PanelOperationActor{ID: 1, IsAdmin: true}, "orders.list", json.RawMessage(`{"filter":[{"key":"unknown","condition":"=","value":"1"}]}`))
	var pe *aiops.PanelOperationError
	if !errors.As(err, &pe) || pe.Status != 400 {
		t.Fatal(err)
	}
}
