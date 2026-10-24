package nodeapi

import (
	"context"
	"database/sql/driver"
	"fmt"
	"regexp"
	"testing"
	"time"

	"forest/go-api/internal/config"
	"forest/go-api/internal/platform/ipgeo"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestReportAliveUsesOtherLiveNodesWithoutRenewingTheirObservation(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	seenAt := time.Now().Unix() - 50
	resolver := &locationTestResolver{locations: map[string]ipgeo.Location{
		"113.92.157.29": {Known: true, CountryCode: "CN", CountryName: "中国", ProvinceCode: "440000", ProvinceName: "广东", CityName: "深圳市"},
	}}
	s := NewDBService(config.Config{DeviceLimitMode: 1}, db, nil).WithOnlineGeoResolver(resolver)
	cache := fmt.Sprintf(`{"v2node1":{"aliveips":["113.92.157.29:58231_android"],"lastupdateAt":%d},"alive_ip":1}`, seenAt)
	mock.ExpectQuery(`SELECT v, expire_at FROM v2_runtime_kv WHERE k = \$1 LIMIT 1`).
		WithArgs("ALIVE_IP_USER_9").WillReturnRows(sqlmock.NewRows([]string{"v", "expire_at"}).AddRow(cache, seenAt+120))
	mock.ExpectExec(`INSERT INTO v2_runtime_kv \(k, v, expire_at, created_at, updated_at\)`).
		WithArgs("ALIVE_IP_USER_9", aliveStateMatcher{wantAliveIP: 2}, sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`(?s)WITH incoming.*INSERT INTO v2_user_online_location`).
		WithArgs(int64(9), "CN", "中国", "440000", "广东", "深圳市", "", seenAt).
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := s.ReportAlive(context.Background(), AliveReportRequest{NodeID: 2, NodeType: "v2node", Users: map[int64][]string{9: {"10.0.0.1"}}}); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileOnlineLocationsRecoversOnlyFreshObservationsAndLimitsFrequency(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const now = int64(1700000000)
	resolver := &locationTestResolver{locations: map[string]ipgeo.Location{
		"113.92.157.29": {Known: true, CountryCode: "CN", CountryName: "中国", ProvinceCode: "440000", ProvinceName: "广东", CityName: "深圳市"},
	}}
	s := NewDBService(config.Config{}, db, nil).WithOnlineGeoResolver(resolver)
	state := func(at int64) string {
		return fmt.Sprintf(`{"vmess1":{"aliveips":["[::ffff:113.92.157.29]:58000_phone"],"lastupdateAt":%d},"alive_ip":1}`, at)
	}
	mock.ExpectQuery(regexp.QuoteMeta(onlineLocationRefreshQuery)).
		WithArgs(now-600, now, int64(0), onlineLocationRefreshBatch).
		WillReturnRows(sqlmock.NewRows([]string{"id", "v"}).
			AddRow(1, state(now-50)).AddRow(2, state(now-100)).
			AddRow(3, state(now-101)).AddRow(4, state(now+1)).AddRow(5, "{broken").
			AddRow(6, `{"vmess1":{"aliveips":[],"lastupdateAt":1700000000}}`))
	var args []driver.Value
	for index, at := range []int64{now - 50, now - 100} {
		args = append(args, int64(index+1), "CN", "中国", "440000", "广东", "深圳市", "", at)
	}
	mock.ExpectExec(`(?s)WITH incoming.*INSERT INTO v2_user_online_location`).
		WithArgs(args...).WillReturnResult(sqlmock.NewResult(0, 2))
	if err := s.reconcileOnlineLocationsAt(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if err := s.reconcileOnlineLocationsAt(context.Background(), now+29); err != nil {
		t.Fatal(err)
	}
	if len(resolver.calls) != 2 {
		t.Fatalf("stale/future/empty cache was resolved or throttling failed: %v", resolver.calls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileOnlineLocationsRotatesBoundedBatchesPastMalformedUsers(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := NewDBService(config.Config{}, db, nil).WithOnlineGeoResolver(&locationTestResolver{})
	const now = int64(1700000000)
	rows := sqlmock.NewRows([]string{"id", "v"})
	for id := 1; id <= onlineLocationRefreshBatch; id++ {
		rows.AddRow(id, "{broken")
	}
	mock.ExpectQuery(regexp.QuoteMeta(onlineLocationRefreshQuery)).
		WithArgs(now-600, now, int64(0), onlineLocationRefreshBatch).WillReturnRows(rows)
	if err := s.reconcileOnlineLocationsAt(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery(regexp.QuoteMeta(onlineLocationRefreshQuery)).
		WithArgs(now+30-600, now+30, int64(onlineLocationRefreshBatch), onlineLocationRefreshBatch).
		WillReturnRows(sqlmock.NewRows([]string{"id", "v"}).AddRow(501, "{}"))
	if err := s.reconcileOnlineLocationsAt(context.Background(), now+30); err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery(regexp.QuoteMeta(onlineLocationRefreshQuery)).
		WithArgs(now+60-600, now+60, int64(0), onlineLocationRefreshBatch).
		WillReturnRows(sqlmock.NewRows([]string{"id", "v"}))
	if err := s.reconcileOnlineLocationsAt(context.Background(), now+60); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileOnlineLocationsDoesNotBlockConcurrentRefresh(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := NewDBService(config.Config{}, db, nil).WithOnlineGeoResolver(&locationTestResolver{})
	s.onlineGeoRefreshMu.Lock()
	err = s.reconcileOnlineLocationsAt(context.Background(), 1700000000)
	s.onlineGeoRefreshMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
