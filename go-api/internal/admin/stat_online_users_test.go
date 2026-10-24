package admin

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func onlineUserTestRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"total", "id", "email", "last_seen_at", "location_seen_at", "country_code", "country_name", "province_code", "province_name", "city_name", "unknown_reason"})
}

func TestOnlineUsersReturnsPagedSafeAccountFields(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const now int64 = 1792830000
	mock.ExpectQuery(regexp.QuoteMeta(onlineUsersQuery)).WithArgs(now-600, now, "city", "", "440000", "深圳", "", "USER@example.test", int64(0), int64(20), int64(20)).
		WillReturnRows(onlineUserTestRows().AddRow(25, 9, "user@example.test", now-1, now-5, "CN", "中国", "440000", "广东", "深圳", ""))
	result, err := (&DBService{db: db}).getOnlineUsersAt(context.Background(), OnlineUsersRequest{Scope: "city", ProvinceCode: "440000", City: "深圳市", Search: "USER@example.test", Page: 2, PageSize: 20}, now)
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 25 || result.Page != 2 || result.PageSize != 20 || result.GeneratedAt != now || result.OnlineWindowSeconds != 600 || len(result.Users) != 1 {
		t.Fatalf("unexpected users: %+v", result)
	}
	if user := result.Users[0]; user.ID != 9 || user.Email != "user@example.test" || user.LastSeenAt != now-1 || user.LocationSeenAt != now-5 || user.CityName != "深圳" {
		t.Fatalf("unexpected account fields: %+v", user)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"password", "token", "uuid", "ip"} {
		if strings.Contains(string(raw), `"`+secret+`"`) {
			t.Fatalf("unnecessary secret field %s in response", secret)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestOnlineUsersEmptyPageRetainsTotal(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const now int64 = 1792830000
	mock.ExpectQuery(regexp.QuoteMeta(onlineUsersQuery)).WithArgs(now-600, now, "unknown", "", "", "", "location_stale", "", int64(0), int64(20), int64(40)).
		WillReturnRows(onlineUserTestRows().AddRow(12, 0, "", 0, 0, "", "", "", "", "", ""))
	result, err := (&DBService{db: db}).getOnlineUsersAt(context.Background(), OnlineUsersRequest{Scope: "unknown", Reason: "location_stale", Page: 3}, now)
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 12 || result.Page != 3 || result.Users == nil || len(result.Users) != 0 {
		t.Fatalf("empty page must retain count, without placeholder user: %+v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestOnlineUsersValidation(t *testing.T) {
	for _, req := range []OnlineUsersRequest{
		{Scope: "invalid"}, {Scope: "country"}, {Scope: "country", CountryCode: "ZZ"}, {Scope: "province", ProvinceCode: "44000x"},
		{Scope: "city", City: "深圳"}, {Scope: "unknown", Reason: "invented"}, {Scope: "all", Reason: "not_reported"},
		{Page: -1}, {PageSize: -1}, {PageSize: 101}, {Page: math.MaxInt64, PageSize: 20},
		{Search: strings.Repeat("a", 256)}, {City: strings.Repeat("深", 129)},
	} {
		if _, err := NormalizeOnlineUsersRequest(req); !errors.Is(err, ErrOnlineUsersRequest) {
			t.Errorf("expected validation error for %+v, got %v", req, err)
		}
	}
	for _, req := range []OnlineUsersRequest{
		{}, {Scope: "country", CountryCode: "cn"}, {Scope: "province", ProvinceCode: ""},
		{Scope: "city", ProvinceCode: "440000", City: ""}, {Scope: "city", ProvinceCode: "", City: ""},
		{Scope: "unknown", Reason: "invalid_ip"}, {Page: math.MaxInt64, PageSize: 1},
	} {
		if normalized, err := NormalizeOnlineUsersRequest(req); err != nil || normalized.Page < 1 || normalized.PageSize < 1 {
			t.Errorf("valid request rejected: %+v normalized=%+v err=%v", req, normalized, err)
		}
	}
}

func TestOnlineUsersDatabaseFailures(t *testing.T) {
	for _, stage := range []string{"query", "scan", "iterate"} {
		t.Run(stage, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			expected := mock.ExpectQuery(regexp.QuoteMeta(onlineUsersQuery))
			switch stage {
			case "query":
				expected.WillReturnError(errors.New("database unavailable"))
			case "scan":
				expected.WillReturnRows(onlineUserTestRows().AddRow("bad-count", 0, "", 0, 0, "", "", "", "", "", ""))
			case "iterate":
				expected.WillReturnRows(onlineUserTestRows().AddRow(0, 0, "", 0, 0, "", "", "", "", "", "").RowError(0, errors.New("stream failed")))
			}
			if _, err := (&DBService{db: db}).getOnlineUsersAt(context.Background(), OnlineUsersRequest{}, 1792830000); err == nil {
				t.Fatal("database failure returned successful empty users")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, service := range []*DBService{nil, {}} {
		if _, err := service.GetOnlineUsers(context.Background(), OnlineUsersRequest{}); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("nil database returned %v", err)
		}
	}
}

func TestOnlineLocationRefreshBestEffortBeforeQueries(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	calls := 0
	service := (&DBService{db: db}).WithOnlineLocationRefresh(func(context.Context) error {
		calls++
		return errors.New("optional node cache unavailable")
	})
	mock.ExpectQuery(regexp.QuoteMeta(onlineStatsQuery)).WillReturnRows(onlineTestRows().AddRow("summary", "", "", "", "", "", "", 0, 0, 0, 0))
	if _, err := service.GetOnlineStats(context.Background()); err != nil || calls != 1 {
		t.Fatalf("refresh should run before stats without suppressing successful query: calls=%d err=%v", calls, err)
	}
	mock.ExpectQuery(regexp.QuoteMeta(onlineUsersQuery)).WillReturnRows(onlineUserTestRows().AddRow(0, 0, "", 0, 0, "", "", "", "", "", ""))
	if _, err := service.GetOnlineUsers(context.Background(), OnlineUsersRequest{}); err != nil || calls != 2 {
		t.Fatalf("refresh should run before users without suppressing successful query: calls=%d err=%v", calls, err)
	}
	if _, err := service.GetOnlineUsers(context.Background(), OnlineUsersRequest{Scope: "bad"}); !errors.Is(err, ErrOnlineUsersRequest) || calls != 2 {
		t.Fatal("invalid request must not trigger cache work")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
