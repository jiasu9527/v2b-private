package nodeapi

import (
	"context"
	"database/sql/driver"
	"errors"
	"reflect"
	"strings"
	"testing"

	"forest/go-api/internal/config"
	"forest/go-api/internal/platform/ipgeo"

	"github.com/DATA-DOG/go-sqlmock"
)

type locationTestResolver struct {
	locations map[string]ipgeo.Location
	calls     []string
	onResolve func()
}

func (r *locationTestResolver) Resolve(ip string) ipgeo.Location {
	r.calls = append(r.calls, ip)
	if r.onResolve != nil {
		r.onResolve()
	}
	if location, ok := r.locations[ip]; ok {
		return location
	}
	return ipgeo.Location{UnknownReason: "not_found"}
}

func (*locationTestResolver) Status() ipgeo.GeoStatus {
	return ipgeo.GeoStatus{Status: "ready", IPv4: true, IPv6: true}
}

// Construct a single-node observation batch for the focused storage tests.
func (s *DBService) recordOnlineLocations(ctx context.Context, users map[int64][]string, now int64) error {
	if s.onlineGeo == nil || len(users) == 0 {
		return nil
	}
	observations := make(map[int64]onlineLocationObservation, len(users))
	for id, ips := range users {
		if err := ctx.Err(); err != nil {
			return err
		}
		if id > 0 && len(ips) > 0 {
			observations[id] = onlineLocationObservation{Location: selectOnlineLocation(s.onlineGeo, ips), SeenAt: now}
		}
	}
	return s.saveOnlineLocationObservations(ctx, observations)
}

func selectOnlineLocation(resolver ipgeo.Resolver, rawIPs []string) ipgeo.Location {
	observed := make(map[string]int64, len(rawIPs))
	for _, raw := range rawIPs {
		observed[raw] = 1
	}
	return selectOnlineLocationObservation(resolver, observed).Location
}

func TestSelectOnlineLocationUsesOneKnownLocationAndDeduplicatesDevices(t *testing.T) {
	resolver := &locationTestResolver{locations: map[string]ipgeo.Location{
		"113.92.157.29": {Known: true, CountryCode: "CN", ProvinceCode: "440000"},
		"8.8.8.8":       {Known: true, CountryCode: "US"},
	}}
	input := []string{"8.8.8.8_phone", "1.1.1.1_ios", "113.92.157.29_android", "1.1.1.1_android", " 1.1.1.1 ", "113.92.157.29_ios"}
	before := append([]string(nil), input...)
	got := selectOnlineLocation(resolver, input)
	if !got.Known || got.CountryCode != "CN" || got.ProvinceCode != "440000" {
		t.Fatalf("selected location=%+v", got)
	}
	if !reflect.DeepEqual(resolver.calls, []string{"1.1.1.1", "113.92.157.29", "8.8.8.8"}) {
		t.Fatalf("expected normalized unique lookups to choose the most precise same-time location, got %v", resolver.calls)
	}
	if !reflect.DeepEqual(input, before) {
		t.Fatalf("report input was mutated: %v", input)
	}
}

func TestSelectOnlineLocationNormalizesSocketAddressesAndPrefersCity(t *testing.T) {
	resolver := &locationTestResolver{locations: map[string]ipgeo.Location{
		"1.1.1.1":       {Known: true, CountryCode: "CN"},
		"113.92.157.29": {Known: true, CountryCode: "CN", ProvinceCode: "440000", CityName: "深圳市"},
	}}
	location := selectOnlineLocation(resolver, []string{"1.1.1.1_phone", "113.92.157.29:53151_android", "[::ffff:113.92.157.29]:53_ios", "113.92.157.29"})
	if location.CityName != "深圳市" || !reflect.DeepEqual(resolver.calls, []string{"1.1.1.1", "113.92.157.29"}) {
		t.Fatalf("socket normalization/precision selection failed: %+v calls=%v", location, resolver.calls)
	}
}

func TestOnlineLocationFromAliveStateKeepsObservationTimeAndNewestEvidence(t *testing.T) {
	const now = int64(1700000000)
	resolver := &locationTestResolver{locations: map[string]ipgeo.Location{
		"1.1.1.1":       {Known: true, CountryCode: "AU"},
		"113.92.157.29": {Known: true, CountryCode: "CN", ProvinceCode: "440000", CityName: "深圳市"},
	}}
	state := map[string]any{
		"alive_ip": int64(4),
		"current":  map[string]any{"aliveips": []string{"10.0.0.1"}, "lastupdateAt": now},
		"other":    map[string]any{"aliveips": []string{"113.92.157.29:50000_android"}, "lastupdateAt": now - 50},
		"stale":    map[string]any{"aliveips": []string{"1.1.1.1"}, "lastupdateAt": now - 101},
		"future":   map[string]any{"aliveips": []string{"1.1.1.1"}, "lastupdateAt": now + 1},
	}
	observation, ok := onlineLocationFromAliveState(resolver, state, now)
	if !ok || observation.Location.CityName != "深圳市" || observation.SeenAt != now-50 {
		t.Fatalf("valid other-node evidence not recovered at its original time: %+v %v", observation, ok)
	}
	state["current"] = map[string]any{"aliveips": []string{"1.1.1.1"}, "lastupdateAt": now}
	observation, ok = onlineLocationFromAliveState(resolver, state, now)
	if !ok || observation.Location.CountryCode != "AU" || observation.Location.CityName != "" || observation.SeenAt != now {
		t.Fatalf("older city evidence overrode a newly observed country: %+v %v", observation, ok)
	}
	delete(state, "current")
	delete(state, "other")
	if observation, ok = onlineLocationFromAliveState(resolver, state, now); ok {
		t.Fatalf("stale/future observations were revived: %+v", observation)
	}
}

func TestSelectOnlineLocationPreservesUsefulUnknownReason(t *testing.T) {
	resolver := &locationTestResolver{locations: map[string]ipgeo.Location{
		"broken":   {UnknownReason: "invalid_ip"},
		"10.0.0.1": {UnknownReason: "non_public_ip"},
		"8.8.8.8":  {UnknownReason: "database_unavailable"},
	}}
	got := selectOnlineLocation(resolver, []string{"broken", "10.0.0.1", "8.8.8.8"})
	if got.Known || got.UnknownReason != "database_unavailable" {
		t.Fatalf("result=%+v", got)
	}
}

func TestRecordOnlineLocationsSkipsEmptyReportsAndInvalidUsers(t *testing.T) {
	resolver := &locationTestResolver{}
	s := (&DBService{}).WithOnlineGeoResolver(resolver)
	for _, users := range []map[int64][]string{nil, {}, {1: nil, 2: {}, 0: {"8.8.8.8"}, -1: {"8.8.8.8"}}} {
		if err := s.recordOnlineLocations(context.Background(), users, 1700000000); err != nil {
			t.Fatal(err)
		}
	}
	if len(resolver.calls) != 0 {
		t.Fatalf("empty or invalid users were looked up: %v", resolver.calls)
	}
	if err := (&DBService{}).recordOnlineLocations(context.Background(), map[int64][]string{1: {"8.8.8.8"}}, 1700000000); err != nil {
		t.Fatal(err)
	}
}

func TestRecordOnlineLocationsBatchesUsersAndStoresNoRawIP(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	location := ipgeo.Location{Known: true, CountryCode: "CN", CountryName: "中国", ProvinceCode: "440000", ProvinceName: "广东", CityName: "深圳市"}
	resolver := &locationTestResolver{locations: map[string]ipgeo.Location{"113.92.157.29": location}}
	s := (&DBService{db: db}).WithOnlineGeoResolver(resolver)
	const now = int64(1700000000)
	users := make(map[int64][]string)
	for id := int64(1); id <= 501; id++ {
		users[id] = []string{"113.92.157.29_android", "113.92.157.29_ios"}
	}
	for start := int64(1); start <= 501; start += 250 {
		end := min(start+250, int64(502))
		args := make([]driver.Value, 0, (end-start)*8)
		for id := start; id < end; id++ {
			// Exact arguments assert one aggregate row per user; no original IP or
			// device label may be passed into this analytics table.
			args = append(args, id, "CN", "中国", "440000", "广东", "深圳市", "", now)
		}
		mock.ExpectExec(`(?s)WITH incoming\(user_id,country_code,country_name,province_code,province_name,city_name,unknown_reason,last_seen_at\).*INSERT INTO v2_user_online_location.*JOIN v2_user.*ON CONFLICT \(user_id\) DO UPDATE`).
			WithArgs(args...).WillReturnResult(sqlmock.NewResult(0, end-start))
	}
	if err := s.recordOnlineLocations(context.Background(), users, now); err != nil {
		t.Fatal(err)
	}
	if len(resolver.calls) != 501 {
		t.Fatalf("got %d lookups, want one per user", len(resolver.calls))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRecordOnlineLocationsStopsResolvingWhenCancelled(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resolver := &locationTestResolver{onResolve: cancel}
	s := (&DBService{db: db}).WithOnlineGeoResolver(resolver)
	err = s.recordOnlineLocations(ctx, map[int64][]string{1: {"8.8.8.8"}, 2: {"1.1.1.1"}}, 1700000000)
	if !errors.Is(err, context.Canceled) || len(resolver.calls) != 1 {
		t.Fatalf("canceled collection continued: error=%v lookups=%d", err, len(resolver.calls))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestReportAliveAcceptsReportWhenLocationStorageFails(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	resolver := &locationTestResolver{locations: map[string]ipgeo.Location{
		"113.92.157.29": {Known: true, CountryCode: "CN", CountryName: "中国", ProvinceCode: "440000", ProvinceName: "广东", CityName: "深圳市"},
	}}
	s := NewDBService(config.Config{DeviceLimitMode: 1}, db, nil).WithOnlineGeoResolver(resolver)
	mock.ExpectQuery(`SELECT v, expire_at FROM v2_runtime_kv WHERE k = \$1 LIMIT 1`).
		WithArgs("ALIVE_IP_USER_9").WillReturnRows(sqlmock.NewRows([]string{"v", "expire_at"}))
	mock.ExpectExec(`INSERT INTO v2_runtime_kv \(k, v, expire_at, created_at, updated_at\)`).
		WithArgs("ALIVE_IP_USER_9", aliveStateMatcher{wantAliveIP: 1}, sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`(?s)WITH incoming.*INSERT INTO v2_user_online_location`).
		WithArgs(int64(9), "CN", "中国", "440000", "广东", "深圳市", "", sqlmock.AnyArg()).
		WillReturnError(errors.New("analytics table unavailable"))
	err = s.ReportAlive(context.Background(), AliveReportRequest{NodeID: 2, NodeType: "v2node", Users: map[int64][]string{9: {"113.92.157.29_android", "113.92.157.29_ios"}}})
	if err != nil {
		t.Fatalf("valid node report rejected due to optional analytics: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	for _, input := range resolver.calls {
		if strings.Contains(input, "_") {
			t.Fatalf("device label leaked to resolver: %q", input)
		}
	}
}
