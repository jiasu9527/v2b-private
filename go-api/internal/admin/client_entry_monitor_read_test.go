package admin

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func expectReadMonitorRevision(mock sqlmock.Sqlmock, revision int64) {
	mock.ExpectQuery(`^SELECT revision FROM v2_client_entry_monitor_config WHERE id = 1$`).
		WillReturnRows(sqlmock.NewRows([]string{"revision"}).AddRow(revision))
}

func expectReadMonitorEmptyPolicies(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(`(?s)^SELECT p.id, p.name, p.sort, p.mode,.*FROM v2_client_entry_user_policy p.*ORDER BY p.sort ASC NULLS LAST, p.id ASC$`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "sort", "mode", "snapshot_from", "snapshot_to", "action", "conditions", "entry_host", "resolve_entry_host", "extra_nodes", "extra_nodes_position", "enabled", "remarks", "created_at", "updated_at"}))
}

func expectReadMonitorRecords(mock sqlmock.Sqlmock) *sqlmock.ExpectedQuery {
	return mock.ExpectQuery(`(?s)^SELECT m.id, m.policy_id, p.name, p.action, m.enabled,.*FROM v2_client_entry_monitor m.*ORDER BY p.sort ASC NULLS LAST, p.id ASC$`)
}

func emptyReadMonitorRecords() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id", "policy_id", "name", "action", "enabled", "check_interval_sec", "tcp_timeout_ms", "failure_threshold", "success_threshold", "created_at", "updated_at"})
}

func expectReadMonitorProbes(mock sqlmock.Sqlmock) *sqlmock.ExpectedQuery {
	// Selecting token_plaintext, or using ListDNSProbes, fails this expectation.
	return mock.ExpectQuery(`^SELECT id, name, last_heartbeat_at\s+FROM v2_dns_probe WHERE enabled = 1 ORDER BY id$`)
}

func TestReadClientEntryMonitorsEmptyReadDoesNotInitializeOrRefresh(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// No schema-ready flags: even the first request may issue SELECTs only.
	service := &DBService{db: db}
	expectReadMonitorRevision(mock, 71)
	expectReadMonitorEmptyPolicies(mock)
	expectReadMonitorRecords(mock).WillReturnRows(emptyReadMonitorRecords())
	expectReadMonitorProbes(mock).WillReturnRows(sqlmock.NewRows([]string{"id", "name", "last_heartbeat_at"}))
	expectReadMonitorRevision(mock, 71)
	result, err := service.ReadClientEntryMonitors(context.Background())
	if err != nil || result.Revision != 71 || result.Items == nil || result.Policies == nil || result.Probes == nil || len(result.Items) != 0 {
		t.Fatalf("snapshot = %#v, %v", result, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestReadClientEntryMonitorsPreservesSavedTargetAndObservation(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service := &DBService{db: db}
	now := time.Now().Unix()
	expectReadMonitorRevision(mock, 71)
	expectUpsertSplitPolicy(mock)
	expectReadMonitorRecords(mock).WillReturnRows(emptyReadMonitorRecords().
		AddRow(int64(3), int64(12), "entry", "override", int64(1), int64(30), int64(5000), int64(4), int64(2), int64(1), int64(1)))
	mock.ExpectQuery(`(?s)^SELECT t.id, t.monitor_id, t.source_key, t.name, t.host, t.port, t.sort, t.auto_split_enabled.*FROM v2_client_entry_monitor_target t.*`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "monitor_id", "source_key", "name", "host", "port", "sort", "auto_split_enabled"}).
			AddRow(int64(13615664), int64(3), "policy:12:split-group:71", "A", "saved-old.example.test", int64(54101), int64(10), int64(0)))
	mock.ExpectQuery(`(?s)^SELECT s.target_id, s.probe_id, p.name, s.last_success,.*FROM v2_client_entry_monitor_state s.*`).
		WillReturnRows(sqlmock.NewRows([]string{"target_id", "probe_id", "name", "last_success", "last_latency_ms", "last_error", "last_resolved_ip", "consecutive_success", "consecutive_failure", "last_reported_at"}).
			AddRow(int64(13615664), int64(9), "probe", int64(1), int64(53), "", "192.0.2.9", int64(2), int64(0), now-5))
	expectReadMonitorProbes(mock).WillReturnRows(sqlmock.NewRows([]string{"id", "name", "last_heartbeat_at"}).
		AddRow(int64(9), "probe", now).AddRow(int64(10), "new probe", nil))
	expectReadMonitorRevision(mock, 71)
	result, err := service.ReadClientEntryMonitors(context.Background())
	if err != nil || len(result.Items) != 1 || len(result.Items[0].Targets) != 1 || len(result.Policies) != 1 || len(result.Probes) != 2 {
		t.Fatalf("snapshot = %#v, %v", result, err)
	}
	target := result.Items[0].Targets[0]
	if target.ID != 13615664 || target.Host != "saved-old.example.test" || target.Host == result.Policies[0].Targets[0].Host || len(target.States) != 1 || target.States[0].LastSuccess == nil || !*target.States[0].LastSuccess || target.States[0].Stale {
		t.Fatalf("persisted target silently refreshed or observation lost: %#v", target)
	}
	for _, probe := range result.Probes {
		if !probe.Enabled || probe.Secret != "" || probe.PublicIP != "" {
			t.Fatalf("unexpected probe data: %#v", probe)
		}
	}
	if result.Probes[0].LastHeartbeatAt == nil || *result.Probes[0].LastHeartbeatAt != now || result.Probes[1].LastHeartbeatAt != nil {
		t.Fatal("probe heartbeat nullability lost")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestReadClientEntryMonitorsRevisionConflictAndReadFailure(t *testing.T) {
	for _, stage := range []string{"revision", "policies", "records", "probes", "final_revision", "conflict"} {
		t.Run(stage, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			service := &DBService{db: db}
			failure := errors.New("fixture read failure")
			if stage == "revision" {
				mock.ExpectQuery(`^SELECT revision FROM v2_client_entry_monitor_config WHERE id = 1$`).WillReturnError(failure)
			} else {
				expectReadMonitorRevision(mock, 71)
				if stage == "policies" {
					mock.ExpectQuery(`(?s)^SELECT p.id, p.name,.*FROM v2_client_entry_user_policy p.*`).WillReturnError(failure)
				} else {
					expectReadMonitorEmptyPolicies(mock)
					if stage == "records" {
						expectReadMonitorRecords(mock).WillReturnError(failure)
					} else {
						expectReadMonitorRecords(mock).WillReturnRows(emptyReadMonitorRecords())
						if stage == "probes" {
							expectReadMonitorProbes(mock).WillReturnError(failure)
						} else {
							expectReadMonitorProbes(mock).WillReturnRows(sqlmock.NewRows([]string{"id", "name", "last_heartbeat_at"}))
							if stage == "final_revision" {
								mock.ExpectQuery(`^SELECT revision FROM v2_client_entry_monitor_config WHERE id = 1$`).WillReturnError(failure)
							} else {
								expectReadMonitorRevision(mock, 72)
							}
						}
					}
				}
			}
			result, err := service.ReadClientEntryMonitors(context.Background())
			if stage == "conflict" {
				failure = ErrClientEntryMonitorRevisionConflict
			}
			if !errors.Is(err, failure) || result.Revision != 0 || len(result.Items) != 0 {
				t.Fatalf("partial snapshot returned: %#v, %v", result, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, service := range []*DBService{nil, {}} {
		if _, err := service.ReadClientEntryMonitors(context.Background()); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("unavailable service: %v", err)
		}
	}
}
