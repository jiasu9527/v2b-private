package admin

import (
	"context"
	"fmt"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func expectClientEntryMonitorPortPolicy(mock sqlmock.Sqlmock, action, rawPort string) {
	rows := sqlmock.NewRows([]string{"id", "name", "sort", "mode", "snapshot_from", "snapshot_to", "action", "conditions", "entry_host", "resolve_entry_host", "extra_nodes", "extra_nodes_position", "enabled", "remarks", "created_at", "updated_at"}).
		AddRow(int64(7), "入口", int64(10), "standard", nil, nil, action, `[]`, "entry.example.com", int64(0), `[]`, "after", int64(1), "", int64(1), int64(1))
	mock.ExpectQuery(`SELECT p.id, p.name, p.sort, p.mode, p.snapshot_from, p.snapshot_to`).WillReturnRows(rows)
	members := sqlmock.NewRows([]string{"policy_id", "server_type", "server_id", "sort"})
	if action == "original" {
		members.AddRow(int64(7), "vmess", int64(11), int64(10))
	}
	mock.ExpectQuery(`SELECT policy_id, server_type, server_id, sort\s+FROM v2_client_entry_user_policy_member`).
		WithArgs(int64(7)).WillReturnRows(members)
	if action == "original" {
		mock.ExpectQuery(`(?s)SELECT id, COALESCE\(name, ''\).*FROM "v2_server_vmess"\s+WHERE id IN \(\$1\)`).
			WithArgs(int64(11)).WillReturnRows(sqlmock.NewRows([]string{"id", "name", "host", "port"}).AddRow(int64(11), "节点", "node.example.com", rawPort))
	}
}

func TestClientEntryMonitorDefaultPortAndExplicitNodePorts(t *testing.T) {
	for _, test := range []struct {
		name, action, rawPort string
		want                  int64
	}{
		{name: "independent entry", action: "override", want: 54101},
		{name: "original missing port", action: "original", rawPort: "", want: 54101},
		{name: "original zero port", action: "original", rawPort: "0", want: 54101},
		{name: "original malformed port", action: "original", rawPort: "not-a-port", want: 54101},
		{name: "original negative port", action: "original", rawPort: "-1", want: 54101},
		{name: "original excessive port", action: "original", rawPort: "65536", want: 54101},
		{name: "explicit HTTPS node", action: "original", rawPort: "443", want: 443},
		{name: "explicit custom node", action: "original", rawPort: "8443", want: 8443},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			service := &DBService{db: db}
			readyClientEntrySchemaForPolicyTest(service)
			expectClientEntryMonitorPortPolicy(mock, test.action, test.rawPort)
			policies, err := service.resolveClientEntryMonitorPolicies(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(policies) != 1 || len(policies[0].Targets) != 1 || policies[0].Targets[0].SuggestedPort != test.want {
				t.Fatalf("port want %d, policies: %#v", test.want, policies)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestClientEntryMonitorRefreshKeepsSavedPortWhenDefaultChanges(t *testing.T) {
	for _, savedPort := range []int64{443, 8443} {
		t.Run(fmt.Sprint(savedPort), func(t *testing.T) {
			// Exact SQL matching protects the ON CONFLICT clause: a changed default
			// may initialize a new target, but must not update an existing port.
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			service := &DBService{db: db}
			readyClientEntrySchemaForPolicyTest(service)
			expectClientEntryMonitorPortPolicy(mock, "override", "")
			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT id, policy_id FROM v2_client_entry_monitor ORDER BY id FOR UPDATE`).
				WillReturnRows(sqlmock.NewRows([]string{"id", "policy_id"}).AddRow(int64(3), int64(7)))
			mock.ExpectQuery(`(?s)SELECT id, host, port, auto_split_enabled.*FROM v2_client_entry_monitor_target.*FOR UPDATE`).
				WithArgs(int64(3), "policy:7").WillReturnRows(sqlmock.NewRows([]string{"id", "host", "port", "auto_split_enabled"}).AddRow(int64(9), "entry.example.com", savedPort, int64(0)))
			query := `INSERT INTO v2_client_entry_monitor_target AS current_target
(monitor_id, source_key, name, host, port, sort, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $7)
ON CONFLICT (monitor_id, source_key) DO UPDATE SET
name = EXCLUDED.name, host = EXCLUDED.host, sort = EXCLUDED.sort,
generation = CASE WHEN current_target.host IS DISTINCT FROM EXCLUDED.host
  THEN current_target.generation + 1 ELSE current_target.generation END,
updated_at = EXCLUDED.updated_at
WHERE current_target.name IS DISTINCT FROM EXCLUDED.name
   OR current_target.host IS DISTINCT FROM EXCLUDED.host
   OR current_target.sort IS DISTINCT FROM EXCLUDED.sort`
			mock.ExpectExec(regexp.QuoteMeta(query)).WithArgs(int64(3), "policy:7", "入口 · 独立入口", "entry.example.com", int64(54101), int64(10), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 0))
			mock.ExpectExec(`DELETE FROM v2_client_entry_monitor_target\s+WHERE monitor_id = \$1 AND source_key NOT IN \(\$2\)`).
				WithArgs(int64(3), "policy:7").WillReturnResult(sqlmock.NewResult(0, 0))
			mock.ExpectCommit()
			if err := service.refreshClientEntryMonitorTargets(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
