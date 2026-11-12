package admin

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func expectUpsertSplitPolicy(mock sqlmock.Sqlmock) {
	rows := sqlmock.NewRows([]string{"id", "name", "sort", "mode", "snapshot_from", "snapshot_to", "action", "conditions", "entry_host", "resolve_entry_host", "extra_nodes", "extra_nodes_position", "enabled", "remarks", "created_at", "updated_at"}).
		AddRow(int64(12), "entry", int64(10), "split", nil, nil, "override", `[]`, "", int64(0), `[]`, "after", int64(1), "", int64(100), int64(200))
	mock.ExpectQuery(`(?s)SELECT p.id, p.name, p.sort, p.mode,.*FROM v2_client_entry_user_policy p`).WillReturnRows(rows)
	mock.ExpectQuery(`(?s)SELECT policy_id, server_type, server_id, sort.*FROM v2_client_entry_user_policy_member`).WithArgs(int64(12)).
		WillReturnRows(sqlmock.NewRows([]string{"policy_id", "server_type", "server_id", "sort"}))
	mock.ExpectQuery(`(?s)SELECT split_group.id, split_group.policy_id,.*FROM v2_client_entry_user_policy_split_group`).WithArgs(int64(12)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "policy_id", "parent_id", "name", "path", "entry_host", "sort", "global_sort", "user_count", "is_leaf", "created_at", "updated_at", "resolve_entry_host"}).
			AddRow(int64(71), int64(12), nil, "A", "A", "entry.example.test", int64(10), int64(10), int64(20), true, int64(100), int64(200), nil))
}

func upsertMonitorRequest() ClientEntryMonitorSaveRequest {
	return ClientEntryMonitorSaveRequest{Revision: 7, Items: []ClientEntryMonitorSaveItem{{
		PolicyID: 12, Enabled: true, CheckIntervalSec: 30, TCPTimeoutMS: 5000, FailureThreshold: 4, SuccessThreshold: 2,
		Targets: []ClientEntryMonitorTargetSaveRequest{{SourceKey: "policy:12:split-group:71", Port: 54101, AutoSplitEnabled: true}},
	}}}
}

func expectMonitorTopologyLock(mock sqlmock.Sqlmock, locked bool) {
	mock.ExpectQuery(`SELECT id, policy_id FROM v2_client_entry_monitor ORDER BY id FOR UPDATE`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "policy_id"}))
	mock.ExpectQuery(`SELECT pg_try_advisory_xact_lock\(\$1\)`).WithArgs(clientEntryVisibleOrderLockKey).
		WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(locked))
}

func TestUpsertClientEntryMonitorPreservesOtherPoliciesAndExistingTargetState(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := &DBService{db: db, dnsFailoverSchemaOK: true}
	readyClientEntrySchemaForPolicyTest(s)
	expectUpsertSplitPolicy(mock)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT revision FROM v2_client_entry_monitor_config WHERE id = 1 FOR UPDATE`).
		WillReturnRows(sqlmock.NewRows([]string{"revision"}).AddRow(int64(7)))
	mock.ExpectQuery(`(?s)SELECT id FROM v2_dns_probe.*FOR SHARE`).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(9)))
	expectMonitorTopologyLock(mock, true)
	expectUpsertSplitPolicy(mock)
	mock.ExpectQuery(`(?s)SELECT enabled FROM v2_client_entry_monitor.*WHERE policy_id = \$1 FOR UPDATE`).WithArgs(int64(12)).
		WillReturnRows(sqlmock.NewRows([]string{"enabled"}).AddRow(int64(1)))
	mock.ExpectQuery(`(?s)INSERT INTO v2_client_entry_monitor.*RETURNING id`).
		WithArgs(int64(12), int64(1), int64(30), int64(5000), int64(4), int64(2), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(3)))
	mock.ExpectQuery(`(?s)SELECT id, host, port, auto_split_enabled.*FROM v2_client_entry_monitor_target`).
		WithArgs(int64(3), "policy:12:split-group:71").WillReturnRows(sqlmock.NewRows([]string{"id", "host", "port", "auto_split_enabled"}).
		AddRow(int64(5), "entry.example.test", int64(54101), int64(1)))
	// Changing thresholds must not delete health state, cancel automatic-split
	// incidents, or change a target's existing automatic-split flag.
	mock.ExpectExec(`(?s)INSERT INTO v2_client_entry_monitor_target AS current_target.*`).
		WithArgs(int64(3), "policy:12:split-group:71", "entry · A 组入口", "entry.example.test", int64(54101), int64(10), int64(1), sqlmock.AnyArg(), int64(0)).
		WillReturnResult(sqlmock.NewResult(1, 0))
	mock.ExpectExec(`(?s)DELETE FROM v2_client_entry_monitor_target.*WHERE monitor_id = \$1 AND source_key NOT IN \(\$2\)`).
		WithArgs(int64(3), "policy:12:split-group:71").WillReturnResult(sqlmock.NewResult(0, 0))
	// No DELETE FROM v2_client_entry_monitor: unrelated policies are untouched.
	mock.ExpectQuery(`(?s)UPDATE v2_client_entry_monitor_config.*RETURNING revision`).WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"revision"}).AddRow(int64(8)))
	mock.ExpectCommit()
	got, err := s.UpsertClientEntryMonitorReceipt(context.Background(), upsertMonitorRequest())
	if err != nil || !got.Committed || got.Revision != 8 || got.SavedPolicyCount != 1 || got.SavedTargetCount != 1 || got.DeletedPolicyCount != 0 {
		t.Fatalf("receipt=%#v err=%v", got, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestUpsertClientEntryMonitorConflictAndWriteFailureRollback(t *testing.T) {
	for _, stage := range []string{"conflict", "sql_failure"} {
		t.Run(stage, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			s := &DBService{db: db, dnsFailoverSchemaOK: true}
			readyClientEntrySchemaForPolicyTest(s)
			expectUpsertSplitPolicy(mock)
			mock.ExpectBegin()
			current := int64(7)
			if stage == "conflict" {
				current = 8
			}
			mock.ExpectQuery(`SELECT revision FROM v2_client_entry_monitor_config WHERE id = 1 FOR UPDATE`).
				WillReturnRows(sqlmock.NewRows([]string{"revision"}).AddRow(current))
			if stage == "sql_failure" {
				mock.ExpectQuery(`(?s)SELECT id FROM v2_dns_probe.*FOR SHARE`).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(9)))
				expectMonitorTopologyLock(mock, true)
				expectUpsertSplitPolicy(mock)
				mock.ExpectQuery(`(?s)SELECT enabled FROM v2_client_entry_monitor.*WHERE policy_id = \$1 FOR UPDATE`).WithArgs(int64(12)).
					WillReturnRows(sqlmock.NewRows([]string{"enabled"}))
				mock.ExpectQuery(`(?s)INSERT INTO v2_client_entry_monitor.*RETURNING id`).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(3)))
				mock.ExpectQuery(`(?s)SELECT id, host, port, auto_split_enabled.*FROM v2_client_entry_monitor_target`).
					WithArgs(int64(3), "policy:12:split-group:71").WillReturnError(errors.New("database read failed after monitor write"))
			}
			mock.ExpectRollback()
			got, err := s.UpsertClientEntryMonitorReceipt(context.Background(), upsertMonitorRequest())
			var validation *ClientEntryMonitorValidationError
			if err == nil || got.Committed || errors.As(err, &validation) {
				t.Fatalf("receipt=%#v err=%v", got, err)
			}
			if stage == "conflict" && !errors.Is(err, ErrClientEntryMonitorRevisionConflict) {
				t.Fatal(err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestUpsertClientEntryMonitorRejectsUnsafePartialInputsBeforeWrite(t *testing.T) {
	for _, kind := range []string{"nil", "empty", "too_many", "missing_targets", "duplicate_targets", "unknown_target", "invalid_port", "invalid_interval", "invalid_threshold"} {
		t.Run(kind, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			s := &DBService{db: db, dnsFailoverSchemaOK: true}
			readyClientEntrySchemaForPolicyTest(s)
			req := upsertMonitorRequest()
			switch kind {
			case "nil":
				req.Items = nil
			case "empty":
				req.Items = []ClientEntryMonitorSaveItem{}
			case "too_many":
				req.Items = make([]ClientEntryMonitorSaveItem, 21)
			default:
				expectUpsertSplitPolicy(mock)
				switch kind {
				case "missing_targets":
					req.Items[0].Targets = nil
				case "duplicate_targets":
					req.Items[0].Targets = append(req.Items[0].Targets, req.Items[0].Targets[0])
				case "unknown_target":
					req.Items[0].Targets[0].SourceKey = "PRIVATE_SENTINEL"
				case "invalid_port":
					req.Items[0].Targets[0].Port = 0
				case "invalid_interval":
					req.Items[0].CheckIntervalSec = 1
				case "invalid_threshold":
					req.Items[0].FailureThreshold = 1
				}
			}
			got, err := s.UpsertClientEntryMonitorReceipt(context.Background(), req)
			var validation *ClientEntryMonitorValidationError
			if !errors.As(err, &validation) || got.Committed {
				t.Fatalf("receipt=%#v err=%v", got, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
