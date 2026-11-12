package admin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestReadClientEntryMonitorConfigReadsOnlySafeConfiguration(t *testing.T) {
	for _, stage := range []string{"configured", "unconfigured", "changed_revision"} {
		t.Run(stage, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			// No schema readiness flags: this endpoint must remain SELECT-only
			// even for the first read and must not load probe/health state.
			s := &DBService{db: db}
			mock.ExpectQuery(`SELECT revision FROM v2_client_entry_monitor_config WHERE id = 1$`).
				WillReturnRows(sqlmock.NewRows([]string{"revision"}).AddRow(int64(7)))
			expectUpsertSplitPolicy(mock)
			rows := sqlmock.NewRows([]string{"policy_id", "enabled", "check_interval_sec", "tcp_timeout_ms", "failure_threshold", "success_threshold", "source_key", "port", "auto_split_enabled"})
			if stage != "unconfigured" {
				rows.AddRow(int64(12), int64(1), int64(60), int64(4000), int64(4), int64(3), "policy:12:split-group:71", int64(8443), int64(1))
			}
			mock.ExpectQuery(`(?s)^SELECT m.policy_id, m.enabled, m.check_interval_sec, m.tcp_timeout_ms,.*WHERE m.policy_id IN \(\$1\).*`).WithArgs(int64(12)).WillReturnRows(rows)
			revision := int64(7)
			if stage == "changed_revision" {
				revision = 8
			}
			mock.ExpectQuery(`SELECT revision FROM v2_client_entry_monitor_config WHERE id = 1$`).
				WillReturnRows(sqlmock.NewRows([]string{"revision"}).AddRow(revision))
			got, err := s.ReadClientEntryMonitorConfig(context.Background(), 12, 20, 0)
			if stage == "changed_revision" {
				if !errors.Is(err, ErrClientEntryMonitorRevisionConflict) || len(got.Items) != 0 {
					t.Fatalf("mixed revision page=%#v err=%v", got, err)
				}
			} else {
				if err != nil || got.Total != 1 || got.Revision != 7 || len(got.Items) != 1 || got.Truncated {
					t.Fatalf("page=%#v err=%v", got, err)
				}
				item := got.Items[0]
				if stage == "configured" {
					if !item.Configured || !item.Enabled || item.CheckIntervalSec != 60 || item.TCPTimeoutMS != 4000 || item.FailureThreshold != 4 || item.SuccessThreshold != 3 || item.Targets[0].Port != 8443 || !item.Targets[0].AutoSplitEnabled {
						t.Fatalf("saved settings lost: %#v", item)
					}
				} else if item.Configured || item.Enabled || item.CheckIntervalSec != 30 || item.TCPTimeoutMS != 5000 || item.Targets[0].Port != 54101 || item.Targets[0].AutoSplitEnabled {
					t.Fatalf("unconfigured defaults: %#v", item)
				}
				raw, _ := json.Marshal(got)
				for _, field := range []string{`"states"`, `"probes"`, `"conditions"`, `"extra_nodes"`, `"host"`} {
					if strings.Contains(string(raw), field) {
						t.Fatalf("unneeded field %s in compact configuration: %s", field, raw)
					}
				}
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestClientEntryMonitorConfigCandidatePaginationKeepsCompleteTargets(t *testing.T) {
	targets := []ClientEntryMonitorCandidateTarget{{SourceKey: "policy:12:split-group:71", SuggestedPort: 54101}, {SourceKey: "policy:12:split-group:72", SuggestedPort: 54101}}
	policies := []ClientEntryMonitorPolicy{
		{ID: 9, Action: "hide", Targets: targets},
		{ID: 10, Action: "override"},
		{ID: 11, Action: "override", Targets: targets},
		{ID: 12, Action: "override", Targets: targets},
		{ID: 13, Action: "override", Targets: targets},
	}
	got := clientEntryMonitorConfigCandidates(policies, 0, 1, 1)
	if got.Total != 3 || len(got.Items) != 1 || got.Items[0].PolicyID != 12 || len(got.Items[0].Targets) != 2 || !got.Truncated {
		t.Fatalf("page=%#v", got)
	}
	got = clientEntryMonitorConfigCandidates(policies, 12, 20, 0)
	if got.Total != 1 || len(got.Items) != 1 || got.Items[0].PolicyID != 12 || got.Truncated {
		t.Fatalf("filtered page=%#v", got)
	}
	got = clientEntryMonitorConfigCandidates(policies, 0, 1, 20)
	if got.Total != 3 || len(got.Items) != 0 || got.Truncated {
		t.Fatalf("past-end page=%#v", got)
	}
}
