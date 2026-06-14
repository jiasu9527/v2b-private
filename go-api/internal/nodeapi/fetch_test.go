package nodeapi

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"regexp"
	"testing"

	"forest/go-api/internal/config"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestDBServiceRoutesPreservesRequestedOrder(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	service := &DBService{db: db}
	rows := sqlmock.NewRows([]string{"id", "match", "action", "action_value"}).
		AddRow(int64(1), `["a.example"]`, "route", nil).
		AddRow(int64(2), `["b.example"]`, "route", nil).
		AddRow(int64(3), `["c.example"]`, "route", nil)
	mock.ExpectQuery(`SELECT id, match, action, action_value\s+FROM v2_server_route\s+WHERE id IN \(\$1, \$2, \$3\)\s+ORDER BY id ASC`).
		WithArgs(int64(3), int64(1), int64(2)).
		WillReturnRows(rows)

	result, err := service.Routes(context.Background(), []int64{3, 1, 2})
	if err != nil {
		t.Fatalf("Routes() error = %v", err)
	}
	if len(result) != 3 {
		t.Fatalf("expected 3 routes, got %d", len(result))
	}

	want := []int64{3, 1, 2}
	for i, id := range want {
		if got := result[i]["id"]; got != id {
			t.Fatalf("route index %d id = %#v, want %d", i, got, id)
		}
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestReportSensitiveAccessInsertsEvents(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	service := &DBService{db: db}
	mock.ExpectExec(`CREATE TABLE IF NOT EXISTS v2_sensitive_access_log`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_v2_sensitive_access_log_last_at`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_v2_sensitive_access_log_user_id`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_v2_sensitive_access_log_domain`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO v2_sensitive_access_log (user_id, server_id, server_type, domain, rule, client_ip, count, first_at, last_at, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $10)`)).
		WithArgs(int64(9), int64(7), "v2node", "example.com", "suffix:example.com", "203.0.113.9", int64(3), int64(1700000000), int64(1700000060), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))

	err = service.ReportSensitiveAccess(context.Background(), SensitiveAccessReportRequest{
		NodeID:   7,
		NodeType: "v2node",
		Events: []SensitiveAccessEvent{{
			UserID:   9,
			Domain:   "example.com",
			Rule:     "suffix:example.com",
			ClientIP: "203.0.113.9",
			Count:    3,
			FirstAt:  1700000000,
			LastAt:   1700000060,
		}},
	})
	if err != nil {
		t.Fatalf("ReportSensitiveAccess() error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestSensitiveAccessStatsReturnsEmailRank(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	service := &DBService{db: db}
	mock.ExpectExec(`CREATE TABLE IF NOT EXISTS v2_sensitive_access_log`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_v2_sensitive_access_log_last_at`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_v2_sensitive_access_log_user_id`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_v2_sensitive_access_log_domain`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT l\.id, l\.user_id, COALESCE\(u\.email, ''\) AS email, l\.server_id, l\.server_type, l\.domain, l\.rule, l\.client_ip, l\.count, l\.first_at, l\.last_at`).
		WithArgs(int64(20)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "user_id", "email", "server_id", "server_type", "domain", "rule", "client_ip", "count", "first_at", "last_at"}).
			AddRow(int64(1), int64(9), "user@example.com", int64(7), "v2node", "example.com", "suffix:example.com", "203.0.113.9", int64(3), int64(1700000000), int64(1700000060)))
	mock.ExpectQuery(`SELECT l\.user_id, COALESCE\(u\.email, ''\) AS email, SUM\(l\.count\) AS count, COUNT\(DISTINCT l\.domain\) AS domain_count`).
		WillReturnRows(sqlmock.NewRows([]string{"user_id", "email", "count", "domain_count", "domains", "ip_count", "ips"}).
			AddRow(int64(9), "user@example.com", int64(3), int64(1), "example.com", int64(1), "203.0.113.9"))
	mock.ExpectQuery(`SELECT l\.domain, SUM\(l\.count\) AS count`).
		WillReturnRows(sqlmock.NewRows([]string{"domain", "count"}).
			AddRow("example.com", int64(3)))

	stats, err := service.SensitiveAccessStats(context.Background(), 20)
	if err != nil {
		t.Fatalf("SensitiveAccessStats() error = %v", err)
	}
	topUsers := stats["top_users"].([]map[string]any)
	if len(topUsers) != 1 || topUsers[0]["email"] != "user@example.com" || topUsers[0]["count"] != int64(3) || topUsers[0]["domain_count"] != int64(1) {
		t.Fatalf("unexpected top users: %#v", topUsers)
	}
	if domains, ok := topUsers[0]["domains"].([]string); !ok || len(domains) != 1 || domains[0] != "example.com" {
		t.Fatalf("unexpected top user domains: %#v", topUsers[0]["domains"])
	}
	if topUsers[0]["ip_count"] != int64(1) {
		t.Fatalf("unexpected top user ip count: %#v", topUsers[0])
	}
	if ips, ok := topUsers[0]["ips"].([]string); !ok || len(ips) != 1 || ips[0] != "203.0.113.9" {
		t.Fatalf("unexpected top user ips: %#v", topUsers[0]["ips"])
	}
	recent := stats["recent"].([]map[string]any)
	if len(recent) != 1 || recent[0]["email"] != "user@example.com" {
		t.Fatalf("unexpected recent events: %#v", recent)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

type fakeRuntimeConfig struct {
	cfg config.Config
}

func (f fakeRuntimeConfig) CurrentConfig() config.Config {
	return f.cfg
}

type aliveStateMatcher struct {
	wantAliveIP int64
}

func (m aliveStateMatcher) Match(value driver.Value) bool {
	raw, ok := value.(string)
	if !ok {
		return false
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return false
	}
	return mapAnyInt64(payload["alive_ip"]) == m.wantAliveIP
}

func TestReportAliveUsesRuntimeDeviceLimitMode(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	service := NewDBService(config.Config{DeviceLimitMode: 0}, db, nil).
		WithRuntimeConfig(fakeRuntimeConfig{cfg: config.Config{DeviceLimitMode: 1}})

	mock.ExpectQuery(`SELECT v, expire_at FROM v2_runtime_kv WHERE k = \$1 LIMIT 1`).
		WithArgs("ALIVE_IP_USER_9").
		WillReturnRows(sqlmock.NewRows([]string{"v", "expire_at"}).AddRow(`{
			"v2node1": {"aliveips": ["1.1.1.1_ios"], "lastupdateAt": 4102444800},
			"alive_ip": 1
		}`, int64(0)))
	mock.ExpectExec(`INSERT INTO v2_runtime_kv \(k, v, expire_at, created_at, updated_at\)`).
		WithArgs("ALIVE_IP_USER_9", aliveStateMatcher{wantAliveIP: 1}, sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))

	err = service.ReportAlive(context.Background(), AliveReportRequest{
		NodeID:   2,
		NodeType: "v2node",
		Users: map[int64][]string{
			9: []string{"1.1.1.1_android"},
		},
	})
	if err != nil {
		t.Fatalf("ReportAlive() error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}
