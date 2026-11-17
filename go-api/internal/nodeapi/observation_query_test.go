package nodeapi

import (
	"context"
	"database/sql/driver"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestObservationSummaryCountsDistinctUsersForDomainsAndIPs(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, pattern := range []string{
		`CREATE TABLE IF NOT EXISTS v2_node_observation_config`,
		`ALTER TABLE v2_node_observation_config ADD COLUMN IF NOT EXISTS mode`,
		`CREATE TABLE IF NOT EXISTS v2_node_observation_event`,
		`CREATE INDEX IF NOT EXISTS idx_v2_node_observation_event_time`,
		`CREATE INDEX IF NOT EXISTS idx_v2_node_observation_event_user_time`,
		`CREATE INDEX IF NOT EXISTS idx_v2_node_observation_event_node_time`,
	} {
		mock.ExpectExec(pattern).WillReturnResult(sqlmock.NewResult(0, 0))
	}
	now := time.Now().Unix()
	args := []driver.Value{now - 60, now, "v2node", int64(7)}
	mock.ExpectQuery(`SELECT COUNT\(\*\),COUNT\(DISTINCT e.user_id\)`).WithArgs(args...).WillReturnRows(
		sqlmock.NewRows([]string{"events", "users", "known", "unknown"}).AddRow(4, 2, 3, 1))
	mock.ExpectQuery(`SELECT e.target_host,COUNT\(DISTINCT e.user_id\)`).WithArgs(args...).WillReturnRows(
		sqlmock.NewRows([]string{"domain", "users", "events"}).AddRow("example.test", 2, 3))
	mock.ExpectQuery(`SELECT e.target_ip,COUNT\(DISTINCT e.user_id\)`).WithArgs(args...).WillReturnRows(
		sqlmock.NewRows([]string{"ip", "users", "events"}).AddRow("192.0.2.1", 2, 4))
	svc := &DBService{db: db}
	result, err := svc.ObservationSummary(context.Background(), ObservationFilter{NodeType: "v2node", NodeID: 7, From: now - 60, To: now})
	if err != nil {
		t.Fatal(err)
	}
	if result["events"] != int64(4) || result["users"] != int64(2) {
		t.Fatalf("wrong summary counts: %#v", result)
	}
	if len(result["top_domains"].([]map[string]any)) != 1 || len(result["top_ips"].([]map[string]any)) != 1 {
		t.Fatalf("missing target summary: %#v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestObservationFilterAllowsPickerBoundaryButRejectsLongerWindow(t *testing.T) {
	now := time.Now().Unix()
	if _, err := normalizeObservationFilter(ObservationFilter{}); err != nil {
		t.Fatalf("default seven-day window rejected: %v", err)
	}
	if _, err := normalizeObservationFilter(ObservationFilter{From: now - observationKeepDays*86400 - 20, To: now - 20}); err != nil {
		t.Fatalf("exact seven-day picker window rejected: %v", err)
	}
	if _, err := normalizeObservationFilter(ObservationFilter{From: now - observationKeepDays*86400 - 20, To: now}); err == nil {
		t.Fatal("overlong seven-day window accepted")
	}
}
