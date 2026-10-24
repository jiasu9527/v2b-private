package admin

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"

	"forest/go-api/internal/platform/ipgeo"

	"github.com/DATA-DOG/go-sqlmock"
)

func onlineTestRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"kind", "country_code", "country_name", "province_code", "province_name", "unknown_reason", "users", "current_online", "unique_24h", "unique_7d"})
}

func expectOnlineStatsQuery(mock sqlmock.Sqlmock, now int64) *sqlmock.ExpectedQuery {
	return mock.ExpectQuery(regexp.QuoteMeta(onlineStatsQuery)).WithArgs(now-600, now-86400, now-604800, now)
}

func TestOnlineStatsCountsPeopleAndAggregatesLocations(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const now int64 = 1792830000
	rows := onlineTestRows().
		AddRow("region", "CN", "中国", "440000", "广东", "", 4, 0, 0, 0).
		AddRow("region", "CN", "中国", "110000", "北京", "", 2, 0, 0, 0).
		AddRow("region", "CN", "中国", "440000", "广东省", "", 1, 0, 0, 0).
		AddRow("region", "CN", "中国", "", "", "", 1, 0, 0, 0).
		AddRow("region", "HK", "中国香港", "810000", "香港", "", 1, 0, 0, 0).
		AddRow("region", "MO", "中国澳门", "820000", "澳门", "", 1, 0, 0, 0).
		AddRow("region", "TW", "中国台湾", "710000", "台湾", "", 1, 0, 0, 0).
		AddRow("region", "US", "美国", "", "", "", 2, 0, 0, 0).
		AddRow("region", "", "", "", "", "not_reported", 1, 0, 0, 0).
		AddRow("region", "", "", "", "", "location_stale", 1, 0, 0, 0).
		AddRow("region", "", "", "", "", "database_unavailable", 1, 0, 0, 0).
		AddRow("region", "", "", "", "", "non_public_ip", 1, 0, 0, 0).
		// SQL UNION ALL does not promise summary-row ordering.
		AddRow("summary", "", "", "", "", "", 100, 17, 30, 50)
	expectOnlineStatsQuery(mock, now).WillReturnRows(rows)
	service := (&DBService{db: db}).WithOnlineGeoResolver(onlineTestGeoResolver{status: ipgeo.GeoStatus{Status: "ready", IPv4: true, IPv6: true, Source: "ip2region", Version: "test"}})
	result, err := service.getOnlineStatsAt(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if result.GeneratedAt != now || result.OnlineWindowSeconds != 600 || result.CurrentOnline != 17 || result.Unique24h != 30 || result.Unique7d != 50 || result.TotalUsers != 100 || result.OnlineShare != 17 {
		t.Fatalf("summary mismatch: %+v", result)
	}
	if result.GeoLocatedUsers != 13 || result.GeoUnknownUsers != 4 || result.GeoCoverage != 76.47 {
		t.Fatalf("coverage mismatch: %+v", result)
	}
	if len(result.Countries) != 5 || result.Countries[0].Code != "CN" || result.Countries[0].Users != 8 || result.Countries[0].Share != 47.06 || result.Countries[1].Code != "US" || result.Countries[2].Code != "HK" {
		t.Fatalf("country grouping/ranking mismatch: %+v", result.Countries)
	}
	if len(result.ChinaProvinces) != 6 || result.ChinaProvinces[0].Code != "440000" || result.ChinaProvinces[0].Users != 5 || result.ChinaProvinces[0].Share != 29.41 {
		t.Fatalf("province grouping/ranking mismatch: %+v", result.ChinaProvinces)
	}
	var countryTotal, provinceTotal, unknownTotal int64
	for _, region := range result.Countries {
		countryTotal += region.Users
	}
	for _, region := range result.ChinaProvinces {
		provinceTotal += region.Users
		if region.Code == "" && region.Name != "省份未知" {
			t.Fatalf("unknown province missing label: %+v", region)
		}
	}
	for _, reason := range result.UnknownReasons {
		unknownTotal += reason.Users
	}
	if countryTotal != result.GeoLocatedUsers || countryTotal+unknownTotal != result.CurrentOnline || provinceTotal != 11 {
		t.Fatalf("geography totals mismatch: countries=%d provinces=%d unknown=%d", countryTotal, provinceTotal, unknownTotal)
	}
	if result.GeoDatabase.Status != "ready" || !result.GeoDatabase.IPv6 || result.GeoDatabase.Version != "test" {
		t.Fatalf("resolver status mismatch: %+v", result.GeoDatabase)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestOnlineStatsNoLocationStillCountsOnlineUsers(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const now int64 = 1792830000
	expectOnlineStatsQuery(mock, now).WillReturnRows(onlineTestRows().
		AddRow("summary", "", "", "", "", "", 20, 7, 10, 15).
		AddRow("region", "", "", "", "", "not_reported", 4, 0, 0, 0).
		AddRow("region", "", "", "", "", "database_unavailable", 3, 0, 0, 0))
	result, err := (&DBService{db: db}).getOnlineStatsAt(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if result.CurrentOnline != 7 || result.GeoUnknownUsers != 7 || result.GeoLocatedUsers != 0 || result.GeoCoverage != 0 || len(result.Countries) != 0 || result.GeoDatabase.Status != "unavailable" {
		t.Fatalf("missing geolocation must not hide online activity: %+v", result)
	}
	if len(result.UnknownReasons) != 2 || result.UnknownReasons[0].Code != "not_reported" || result.UnknownReasons[0].Users != 4 {
		t.Fatalf("unknown reasons mismatch: %+v", result.UnknownReasons)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestOnlineStatsEmptyUsersReturnsFinitePercentagesAndArrays(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const now int64 = 1792830000
	expectOnlineStatsQuery(mock, now).WillReturnRows(onlineTestRows().AddRow("summary", "", "", "", "", "", 0, 0, 0, 0))
	result, err := (&DBService{db: db}).getOnlineStatsAt(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("empty statistics should serialize without NaN: %v", err)
	}
	for _, field := range []string{`"countries":[]`, `"china_provinces":[]`, `"unknown_reasons":[]`, `"online_share":0`, `"geo_coverage":0`} {
		if !strings.Contains(string(raw), field) {
			t.Errorf("missing %s in %s", field, raw)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestOnlineStatsUnknownAndStaleDataNeverBecomesMapLocations(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const now int64 = 1792830000
	expectOnlineStatsQuery(mock, now).WillReturnRows(onlineTestRows().
		AddRow("summary", "", "", "", "", "", 8, 8, 8, 8).
		AddRow("region", "CN", "中国", "110000", "北京", "location_stale", 1, 0, 0, 0).
		AddRow("region", "CN", "中国", "110000", "北京", "invalid_timestamp", 1, 0, 0, 0).
		AddRow("region", "ZZ", "unknown", "", "", "", 1, 0, 0, 0).
		AddRow("region", "", "", "", "", "unrecognized-value", 1, 0, 0, 0).
		AddRow("region", "", "", "", "", "invalid_ip", 1, 0, 0, 0).
		AddRow("region", "CN", "中国", "invalid", "obsolete province", "", 1, 0, 0, 0).
		AddRow("region", "CN", "中国", "", "", "", 2, 0, 0, 0))
	result, err := (&DBService{db: db}).getOnlineStatsAt(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if result.GeoLocatedUsers != 3 || result.GeoUnknownUsers != 5 || len(result.Countries) != 1 || result.Countries[0].Users != 3 || len(result.ChinaProvinces) != 1 || result.ChinaProvinces[0].Code != "" || result.ChinaProvinces[0].Users != 3 {
		t.Fatalf("invalid locations should remain explicitly unknown: %+v", result)
	}
	if len(result.UnknownReasons) != 4 || result.UnknownReasons[0].Code != "not_found" || result.UnknownReasons[0].Users != 2 {
		t.Fatalf("unknown reason grouping mismatch: %+v", result.UnknownReasons)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestOnlineStatsReportsDatabaseFailures(t *testing.T) {
	for _, stage := range []string{"query", "scan", "iterate", "missing_summary"} {
		t.Run(stage, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			const now int64 = 1792830000
			expected := expectOnlineStatsQuery(mock, now)
			switch stage {
			case "query":
				expected.WillReturnError(errors.New("database down"))
			case "scan":
				expected.WillReturnRows(onlineTestRows().AddRow("summary", "", "", "", "", "", "invalid-count", 0, 0, 0))
			case "iterate":
				expected.WillReturnRows(onlineTestRows().AddRow("summary", "", "", "", "", "", 0, 0, 0, 0).RowError(0, errors.New("stream interrupted")))
			case "missing_summary":
				expected.WillReturnRows(onlineTestRows())
			}
			if _, err := (&DBService{db: db}).getOnlineStatsAt(context.Background(), now); err == nil {
				t.Fatal("database failure must not be presented as zero online users")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestOnlineStatsUnavailableService(t *testing.T) {
	for _, service := range []*DBService{nil, {}} {
		if _, err := service.GetOnlineStats(context.Background()); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("expected unavailable service, got %v", err)
		}
	}
}

type onlineTestGeoResolver struct{ status ipgeo.GeoStatus }

func (onlineTestGeoResolver) Resolve(string) ipgeo.Location { return ipgeo.Location{} }
func (r onlineTestGeoResolver) Status() ipgeo.GeoStatus     { return r.status }
