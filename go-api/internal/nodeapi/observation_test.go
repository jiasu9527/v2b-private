package nodeapi

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestNormalizeObservationEvent(t *testing.T) {
	now := time.Now().Unix()
	tests := []struct {
		name    string
		event   ObservationEvent
		wantErr bool
	}{
		{"domain", ObservationEvent{EventID: "event_123", UserID: 5, OccurredAt: now, ClientIP: "192.0.2.1", TargetHost: "Example.COM.", TargetPort: 443, Network: "TCP", Result: "Route_Attempt"}, false},
		{"ip", ObservationEvent{EventID: "event_234", UserID: 5, OccurredAt: now, TargetIP: "198.51.100.1", TargetPort: 80, Network: "tcp", Result: "route_attempt"}, false},
		{"unproven_success", ObservationEvent{EventID: "event_245", UserID: 5, OccurredAt: now, TargetIP: "198.51.100.1", TargetPort: 80, Network: "tcp", Result: "connected"}, true},
		{"unproven_bytes", ObservationEvent{EventID: "event_246", UserID: 5, OccurredAt: now, TargetIP: "198.51.100.1", TargetPort: 80, Network: "tcp", Result: "route_attempt", UplinkBytes: 1}, true},
		{"url_not_domain", ObservationEvent{EventID: "event_345", UserID: 5, OccurredAt: now, TargetHost: "example.com/path", TargetPort: 443, Network: "tcp", Result: "route_attempt"}, true},
		{"missing_destination", ObservationEvent{EventID: "event_456", UserID: 5, OccurredAt: now, TargetPort: 443, Network: "tcp", Result: "route_attempt"}, true},
		{"stale", ObservationEvent{EventID: "event_567", UserID: 5, OccurredAt: now - 8*86400, TargetIP: "198.51.100.1", TargetPort: 443, Network: "tcp", Result: "route_attempt"}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := normalizeObservationEvent(&tc.event, now)
			if (err != nil) != tc.wantErr {
				t.Fatalf("normalizeObservationEvent error=%v, wantErr=%v", err, tc.wantErr)
			}
			if tc.name == "domain" && tc.event.TargetHost != "example.com" {
				t.Fatalf("domain not normalized: %q", tc.event.TargetHost)
			}
		})
	}
}

func TestReportObservationRequiresPerNodeTokenAndAllowlistedUser(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc := &DBService{db: db}
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
	token := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	hash := sha256.Sum256([]byte(token))
	row := func() *sqlmock.Rows {
		return sqlmock.NewRows([]string{"enabled", "mode", "token_hash", "allowed_user_ids"}).AddRow(true, ObservationModeAllowlist, hash[:], []byte(`[5]`))
	}
	mock.ExpectQuery(`SELECT enabled,mode,token_hash,allowed_user_ids FROM v2_node_observation_config`).WithArgs(int64(7)).WillReturnRows(row())
	_, err = svc.ReportObservation(context.Background(), ObservationReportRequest{NodeType: "v2node", NodeID: 7, Token: "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", Events: []ObservationEvent{{EventID: "event_123", UserID: 5, OccurredAt: time.Now().Unix(), TargetHost: "example.com", TargetPort: 443, Network: "tcp", Result: "route_attempt"}}})
	if !errors.Is(err, ErrObservationUnauthorized) {
		t.Fatalf("wrong token error=%v", err)
	}
	mock.ExpectQuery(`SELECT enabled,mode,token_hash,allowed_user_ids FROM v2_node_observation_config`).WithArgs(int64(7)).WillReturnRows(row())
	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM v2_server_v2node`).WithArgs(int64(7), ObservationModeAllowlist).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	_, err = svc.ReportObservation(context.Background(), ObservationReportRequest{NodeType: "v2node", NodeID: 7, Token: token, Events: []ObservationEvent{{EventID: "event_123", UserID: 6, OccurredAt: time.Now().Unix(), TargetHost: "example.com", TargetPort: 443, Network: "tcp", Result: "route_attempt"}}})
	if !errors.Is(err, ErrObservationUserNotAllowed) {
		t.Fatalf("unlisted user error=%v", err)
	}
	mock.ExpectQuery(`SELECT enabled,mode,token_hash,allowed_user_ids FROM v2_node_observation_config`).WithArgs(int64(7)).WillReturnRows(row())
	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM v2_server_v2node`).WithArgs(int64(7), ObservationModeAllowlist).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectExec(`INSERT INTO v2_node_observation_event`).WillReturnResult(sqlmock.NewResult(1, 1))
	inserted, err := svc.ReportObservation(context.Background(), ObservationReportRequest{NodeType: "v2node", NodeID: 7, Token: token, Events: []ObservationEvent{{EventID: "event_123", UserID: 5, OccurredAt: time.Now().Unix(), TargetHost: "example.com", TargetPort: 443, Network: "tcp", Result: "route_attempt"}}})
	if err != nil || inserted != 1 {
		t.Fatalf("valid event inserted=%d error=%v", inserted, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestProvisionObservationRejectsSharedNode(t *testing.T) {
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
	mock.ExpectQuery(`SELECT row_to_json\(t\)`).WithArgs(int64(7)).WillReturnRows(
		sqlmock.NewRows([]string{"row_to_json"}).AddRow([]byte(`{"id":7,"group_id":"[1]","route_id":"[]","protocol":"anytls"}`)))
	mock.ExpectQuery(`SELECT client_entry_only=1 FROM v2_server_v2node`).WithArgs(int64(7)).WillReturnRows(
		sqlmock.NewRows([]string{"client_entry_only"}).AddRow(false))
	svc := &DBService{db: db}
	if _, err := svc.ProvisionObservation(context.Background(), "v2node", 7, true, ObservationModeAllowlist, []int64{5}); !errors.Is(err, ErrObservationInvalid) {
		t.Fatalf("shared node accepted: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestProvisionObservationAllModeAllowsSharedNodeWithoutUserList(t *testing.T) {
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
	mock.ExpectQuery(`SELECT row_to_json\(t\)`).WithArgs(int64(7)).WillReturnRows(
		sqlmock.NewRows([]string{"row_to_json"}).AddRow([]byte(`{"id":7,"group_id":"[1]","route_id":"[]","protocol":"anytls"}`)))
	mock.ExpectQuery(`SELECT client_entry_only=1 FROM v2_server_v2node`).WithArgs(int64(7)).WillReturnRows(
		sqlmock.NewRows([]string{"client_entry_only"}).AddRow(false))
	mock.ExpectExec(`INSERT INTO v2_node_observation_config`).WithArgs(int64(7), true, ObservationModeAll, sqlmock.AnyArg(), `[]`, sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(1, 1))
	svc := &DBService{db: db}
	token, err := svc.ProvisionObservation(context.Background(), "v2node", 7, true, ObservationModeAll, []int64{})
	if err != nil || len(token) != 64 {
		t.Fatalf("all mode token=%q err=%v", token, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestReportObservationAllModeDoesNotUseAllowlist(t *testing.T) {
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
	token := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	hash := sha256.Sum256([]byte(token))
	mock.ExpectQuery(`SELECT enabled,mode,token_hash,allowed_user_ids FROM v2_node_observation_config`).WithArgs(int64(7)).WillReturnRows(
		sqlmock.NewRows([]string{"enabled", "mode", "token_hash", "allowed_user_ids"}).AddRow(true, ObservationModeAll, hash[:], []byte(`[]`)))
	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM v2_server_v2node`).WithArgs(int64(7), ObservationModeAll).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM v2_user WHERE id IN`).WithArgs(`[999,1000]`).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectExec(`INSERT INTO v2_node_observation_event`).WillReturnResult(sqlmock.NewResult(1, 2))
	svc := &DBService{db: db}
	inserted, err := svc.ReportObservation(context.Background(), ObservationReportRequest{NodeType: "v2node", NodeID: 7, Token: token, Events: []ObservationEvent{
		{EventID: "event_123", UserID: 999, OccurredAt: time.Now().Unix(), TargetHost: "example.com", TargetPort: 443, Network: "tcp", Result: "route_attempt"},
		{EventID: "event_124", UserID: 1000, OccurredAt: time.Now().Unix(), TargetHost: "example.net", TargetPort: 443, Network: "tcp", Result: "route_attempt"},
	}})
	if err != nil || inserted != 2 {
		t.Fatalf("all mode report inserted=%d err=%v", inserted, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestObservationNodesIncludesUnconfiguredNodesForCoverage(t *testing.T) {
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
	cols := []string{"id", "name", "protocol", "client_entry_only", "show", "configured", "enabled", "mode", "allowed_user_ids", "created_at", "updated_at", "last_event_at"}
	mock.ExpectQuery(`FROM v2_server_v2node n`).WillReturnRows(sqlmock.NewRows(cols).
		AddRow(7, "MPLS A", "anytls", 0, 1, false, false, "", []byte(`[]`), 0, 0, 0).
		AddRow(8, "MPLS B", "vless", 0, 1, true, true, ObservationModeAll, []byte(`[]`), 1, 2, 3))
	svc := &DBService{db: db}
	nodes, err := svc.ObservationNodes(context.Background())
	if err != nil || len(nodes) != 2 {
		t.Fatalf("nodes=%#v err=%v", nodes, err)
	}
	if nodes[0]["configured"] != false || nodes[1]["configured"] != true || nodes[1]["mode"] != ObservationModeAll || nodes[1]["last_event_at"] != int64(3) {
		t.Fatalf("coverage rows wrong: %#v", nodes)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteExpiredObservationUsesBoundedBatch(t *testing.T) {
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
	mock.ExpectExec(`WITH expired AS \([\s\S]*LIMIT 50000[\s\S]*DELETE FROM v2_node_observation_event`).WithArgs(sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 100))
	svc := &DBService{db: db}
	if err := svc.DeleteExpiredObservation(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
