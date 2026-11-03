package admin

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func expectReceiptPolicies(mock sqlmock.Sqlmock, populated bool) {
	rows := sqlmock.NewRows([]string{"id", "name", "sort", "mode", "snapshot_from", "snapshot_to", "action", "conditions", "entry_host", "resolve_entry_host", "extra_nodes", "extra_nodes_position", "enabled", "remarks", "created_at", "updated_at"})
	if populated {
		rows.AddRow(int64(12), "entry", int64(10), "standard", nil, nil, "override", `[]`, "entry.example.test", int64(0), `[]`, "after", int64(1), "", int64(100), int64(200))
	}
	mock.ExpectQuery(`(?s)SELECT p.id, p.name, p.sort, p.mode,.*FROM v2_client_entry_user_policy p`).WillReturnRows(rows)
	if populated {
		mock.ExpectQuery(`(?s)SELECT policy_id, server_type, server_id, sort.*FROM v2_client_entry_user_policy_member`).WithArgs(int64(12)).
			WillReturnRows(sqlmock.NewRows([]string{"policy_id", "server_type", "server_id", "sort"}))
	}
}

func TestSaveClientEntryMonitorReceiptCommitsWithoutOverviewRead(t *testing.T) {
	for _, populated := range []bool{false, true} {
		name := "clear_all"
		if populated {
			name = "save_one"
		}
		t.Run(name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			service := &DBService{db: db, dnsFailoverSchemaOK: true}
			readyClientEntrySchemaForPolicyTest(service)
			expectReceiptPolicies(mock, populated)
			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT revision FROM v2_client_entry_monitor_config WHERE id = 1 FOR UPDATE`).
				WillReturnRows(sqlmock.NewRows([]string{"revision"}).AddRow(int64(7)))
			request := ClientEntryMonitorSaveRequest{Revision: 7, Items: []ClientEntryMonitorSaveItem{}}
			want := ClientEntryMonitorSaveReceipt{Committed: true, Revision: 8, DeletedPolicyCount: 2}
			if populated {
				request.Items = append(request.Items, ClientEntryMonitorSaveItem{PolicyID: 12})
				mock.ExpectQuery(`(?s)SELECT enabled FROM v2_client_entry_monitor.*WHERE policy_id = \$1 FOR UPDATE`).WithArgs(int64(12)).
					WillReturnRows(sqlmock.NewRows([]string{"enabled"}))
				mock.ExpectQuery(`(?s)INSERT INTO v2_client_entry_monitor.*RETURNING id`).
					WithArgs(int64(12), int64(0), int64(30), int64(5000), int64(3), int64(2), sqlmock.AnyArg()).
					WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(3)))
				mock.ExpectQuery(`(?s)SELECT id, host, port, auto_split_enabled.*FROM v2_client_entry_monitor_target`).
					WithArgs(int64(3), "policy:12").WillReturnRows(sqlmock.NewRows([]string{"id", "host", "port", "auto_split_enabled"}))
				mock.ExpectExec(`(?s)INSERT INTO v2_client_entry_monitor_target AS current_target.*`).
					WithArgs(int64(3), "policy:12", "entry · 独立入口", "entry.example.test", int64(54101), int64(10), int64(0), sqlmock.AnyArg(), int64(0)).
					WillReturnResult(sqlmock.NewResult(1, 1))
				mock.ExpectExec(`(?s)DELETE FROM v2_client_entry_monitor_target.*WHERE monitor_id = \$1 AND source_key NOT IN \(\$2\)`).
					WithArgs(int64(3), "policy:12").WillReturnResult(sqlmock.NewResult(0, 0))
				mock.ExpectExec(`DELETE FROM v2_client_entry_monitor WHERE policy_id NOT IN \(\$1\)`).
					WithArgs(int64(12)).WillReturnResult(sqlmock.NewResult(0, 2))
				want.SavedPolicyCount, want.SavedTargetCount = 1, 1
			} else {
				mock.ExpectExec(`DELETE FROM v2_client_entry_monitor$`).WillReturnResult(sqlmock.NewResult(0, 2))
			}
			mock.ExpectQuery(`(?s)UPDATE v2_client_entry_monitor_config.*RETURNING revision`).WithArgs(sqlmock.AnyArg()).
				WillReturnRows(sqlmock.NewRows([]string{"revision"}).AddRow(int64(8)))
			mock.ExpectCommit()
			// No expectations after Commit: an overview refresh/query is an error.
			got, err := service.SaveClientEntryMonitorReceipt(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("receipt = %#v, want %#v", got, want)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSaveClientEntryMonitorReceiptDoesNotClaimCommitOnConflictOrFailure(t *testing.T) {
	for _, stage := range []string{"conflict", "revision", "commit"} {
		t.Run(stage, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			service := &DBService{db: db, dnsFailoverSchemaOK: true}
			readyClientEntrySchemaForPolicyTest(service)
			expectReceiptPolicies(mock, false)
			mock.ExpectBegin()
			current := int64(7)
			if stage == "conflict" {
				current = 8
			}
			mock.ExpectQuery(`SELECT revision FROM v2_client_entry_monitor_config WHERE id = 1 FOR UPDATE`).
				WillReturnRows(sqlmock.NewRows([]string{"revision"}).AddRow(current))
			if stage == "conflict" {
				mock.ExpectRollback()
			} else {
				mock.ExpectExec(`DELETE FROM v2_client_entry_monitor$`).WillReturnResult(sqlmock.NewResult(0, 2))
				revision := mock.ExpectQuery(`(?s)UPDATE v2_client_entry_monitor_config.*RETURNING revision`).WithArgs(sqlmock.AnyArg())
				if stage == "revision" {
					revision.WillReturnError(errors.New("revision write failure"))
					mock.ExpectRollback()
				} else {
					revision.WillReturnRows(sqlmock.NewRows([]string{"revision"}).AddRow(int64(8)))
					mock.ExpectCommit().WillReturnError(errors.New("commit connection failure"))
				}
			}
			got, err := service.SaveClientEntryMonitorReceipt(context.Background(), ClientEntryMonitorSaveRequest{Revision: 7, Items: []ClientEntryMonitorSaveItem{}})
			if err == nil || got.Committed || got.Revision != 0 {
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
