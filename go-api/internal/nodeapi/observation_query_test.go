package nodeapi

import (
	"context"
	"database/sql/driver"
	"strings"
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
	now := time.Now().Unix()
	args := []driver.Value{now - 60, now, "v2node", int64(7)}
	mock.ExpectBegin()
	mock.ExpectExec(`LOCK TABLE v2_node_observation_event,v2_node_observation_compact IN ACCESS SHARE MODE`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT e.user_id,e.target_host,e.target_ip,COUNT`).WithArgs(args...).WillReturnRows(
		sqlmock.NewRows([]string{"user_id", "host", "ip", "count"}).
			AddRow(1, "example.test", "192.0.2.1", 2).
			AddRow(2, "example.test", "192.0.2.1", 1).
			AddRow(2, "", "192.0.2.1", 1))
	mock.ExpectQuery(`FROM v2_node_observation_archive`).WithArgs(now-60, now, int64(7), observationArchiveQueryBatch).
		WillReturnRows(archiveQueryTestRows())
	mock.ExpectCommit()
	svc := observationTestService(db)
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

func TestObservationFilterAccountAndTargetAreExactAndParameterized(t *testing.T) {
	filter, err := normalizeObservationFilter(ObservationFilter{Account: "  USER@Example.COM ", Target: "Example.COM."})
	if err != nil {
		t.Fatal(err)
	}
	where, args := observationWhere(filter)
	if !strings.Contains(where, "lower(email)=lower($3)") || !strings.Contains(where, "e.target_host = $4") {
		t.Fatalf("missing exact account/target conditions: %s", where)
	}
	if len(args) != 4 || args[2] != "USER@Example.COM" || args[3] != "example.com" {
		t.Fatalf("unexpected query args: %#v", args)
	}
	filter, err = normalizeObservationFilter(ObservationFilter{Account: "42", Target: "2001:DB8::1"})
	if err != nil || filter.UserID != 42 || filter.Account != "" || filter.Target != "2001:db8::1" {
		t.Fatalf("numeric account/IPv6 normalization: %+v %v", filter, err)
	}
	for _, bad := range []ObservationFilter{{Account: "0"}, {Account: "not-an-email"}, {Target: "example.com/path"}, {Target: "x' OR 1=1 --"}} {
		if _, err := normalizeObservationFilter(bad); err == nil {
			t.Fatalf("invalid filter accepted: %+v", bad)
		}
	}
}

func TestObservationEventsDoesNotCountBeforeReturningOnePage(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now().Unix()
	columns := []string{"node_type", "node_id", "event_id", "user_id", "email", "occurred_at", "client_ip", "target_host", "target_ip", "target_port", "network", "result", "uplink_bytes", "downlink_bytes"}
	rows := sqlmock.NewRows(columns).
		AddRow("v2node", 7, "event_003", 9, "user@example.test", now-1, "", "example.test", "", 443, "tcp", "route_attempt", 0, 0).
		AddRow("v2node", 7, "event_002", 9, "user@example.test", now-1, "", "example.test", "", 443, "tcp", "route_attempt", 0, 0).
		AddRow("v2node", 7, "event_001", 9, "user@example.test", now-1, "", "example.test", "", 443, "tcp", "route_attempt", 0, 0)
	mock.ExpectBegin()
	mock.ExpectExec(`LOCK TABLE v2_node_observation_event,v2_node_observation_compact IN ACCESS SHARE MODE`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT e.node_type,e.node_id,e.event_id`).WithArgs(now-60, now, 3, 0).WillReturnRows(rows)
	mock.ExpectQuery(`FROM v2_node_observation_archive`).WithArgs(now-60, now, observationArchiveQueryBatch).
		WillReturnRows(archiveQueryTestRows())
	mock.ExpectCommit()
	svc := observationTestService(db)
	f := ObservationFilter{From: now - 60, To: now, PageSize: 2}
	page, err := svc.ObservationEvents(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if len(page["items"].([]map[string]any)) != 2 || page["total_exact"] != false || page["has_more"] != true || page["total"] != int64(3) {
		t.Fatalf("invalid bounded page: %#v", page)
	}
	f.Cursor = page["next_cursor"].(string)
	cursor, err := decodeObservationCursor(f)
	if err != nil || cursor.Seen != 2 || cursor.EventID != "event_002" {
		t.Fatalf("invalid cursor: %+v %v", cursor, err)
	}
	f.UserID = 99
	if _, err := decodeObservationCursor(f); err == nil {
		t.Fatal("cursor accepted for different filters")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestObservationCursorRejectsInvalidAndExpiredScope(t *testing.T) {
	now := time.Now().Unix()
	for _, f := range []ObservationFilter{{From: now - 60, To: now, Cursor: "bad-cursor"}, {From: now - 60, To: now, Cursor: strings.Repeat("a", 2049)}, {Page: 102, PageSize: 100}} {
		normal, err := normalizeObservationFilter(f)
		if err == nil {
			_, err = decodeObservationCursor(normal)
		}
		if err == nil {
			t.Fatalf("invalid pagination accepted: %+v", f)
		}
	}
	f, err := normalizeObservationFilter(ObservationFilter{})
	if err != nil || f.To-f.From != 15*60 {
		t.Fatalf("default should be bounded to fifteen minutes: %+v %v", f, err)
	}
}

func TestObservationSummaryFailsFastWhenAnotherSummaryIsRunning(t *testing.T) {
	observationSummarySlots <- struct{}{}
	defer func() { <-observationSummarySlots }()
	_, err := (&DBService{}).ObservationSummary(context.Background(), ObservationFilter{})
	if err != ErrObservationQueryBusy {
		t.Fatalf("busy summary should not queue database work: %v", err)
	}
}

func TestObservationOldPickerRemainsUsableWithoutExposingExpiredRows(t *testing.T) {
	now := time.Now().Unix()
	f, err := normalizeObservationFilter(ObservationFilter{From: now - 7*86400 - 3600, To: now - 3600})
	if err != nil {
		t.Fatalf("picker should survive time spent reading: %v", err)
	}
	_, args := observationWhere(f)
	if args[0].(int64) < now-7*86400 {
		t.Fatalf("query exposed physically retained old records: %#v", args)
	}
	f.Target = "192.0.2.1"
	where, _ := observationWhere(f)
	if !strings.Contains(where, "e.target_ip = $3") || strings.Contains(where, " OR ") {
		t.Fatalf("exact IP should use only its index: %s", where)
	}
}

func TestObservationSchemaFailureCanRetry(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc := &DBService{db: db}
	for i := 0; i < 2; i++ {
		mock.ExpectExec(`CREATE TABLE IF NOT EXISTS v2_node_observation_config`).WillReturnError(context.DeadlineExceeded)
		if err := svc.ensureObservationSchema(context.Background()); err == nil {
			t.Fatal("expected transient schema failure")
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
