package admin

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// Both locks are acquired inside the conversion transaction: the visible-order
// advisory lock serializes conversions and pool-row locks also exclude claims
// from other IP-pool consumers until the destination groups are committed.
func TestConvertClientEntryUserPolicyToSplitClaimsBackupIPsAtomically(t *testing.T) {
	for _, tc := range []struct {
		name          string
		previewA      string
		previewB      string
		poolCount     int
		userCount     int
		failGroup     bool
		wantError     string
		wantPoolError bool
	}{
		{name: "no preview hosts required", poolCount: 2, userCount: 5},
		{name: "stale preview hosts ignored", previewA: "192.0.2.1", previewB: "192.0.2.2", poolCount: 2, userCount: 5},
		{name: "pool empty after other claim", poolCount: 0, wantError: "备用 IP", wantPoolError: true},
		{name: "only one available", poolCount: 1, wantError: "备用 IP", wantPoolError: true},
		{name: "group write rolls back claim", poolCount: 2, failGroup: true, wantError: "转换固定二分规则失败"},
		{name: "insufficient users rolls back groups and claim", poolCount: 2, userCount: 1, wantError: "现有用户不足 2 人"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			service := &DBService{db: db, dnsFailoverSchemaOK: true}
			readyClientEntrySchemaForPolicyTest(service)
			conditions := `[{"field":"user_id","operator":"between","min":100,"max":200},{"field":"ua","operator":"contains_any","values":["Forest"]}]`
			mock.ExpectBegin()
			expectClientEntryVisibleOrderLock(mock)
			mock.ExpectQuery(`SELECT action, conditions, extra_nodes, sort\s+FROM v2_client_entry_user_policy\s+WHERE id = \$1 AND mode = 'standard'\s+FOR UPDATE`).
				WithArgs(int64(9)).WillReturnRows(sqlmock.NewRows([]string{"action", "conditions", "extra_nodes", "sort"}).AddRow("override", conditions, `[]`, int64(30)))
			poolRows := sqlmock.NewRows([]string{"id", "name", "ip", "port", "enabled", "check_interval_sec", "tcp_timeout_ms", "generation", "sort", "quarantine_until", "created_at", "updated_at"})
			if tc.poolCount >= 1 {
				poolRows.AddRow(501, "备用 A", "192.0.2.51", 54101, 1, 30, 3000, 1, 10, 0, 10, 10)
			}
			if tc.poolCount >= 2 {
				poolRows.AddRow(502, "备用 B", "192.0.2.52", 54101, 1, 30, 3000, 1, 20, 0, 10, 10)
			}
			mock.ExpectQuery(`(?s)FROM v2_client_entry_backup_ip backup.*backup.enabled = 1.*backup.quarantine_until <= \$1.*NOT EXISTS.*v2_client_entry_user_policy policy.*NOT EXISTS.*v2_client_entry_user_policy_split_group.*EXISTS.*v2_dns_probe.*state.last_success = 1.*state.consecutive_success >= \$3.*ORDER BY backup.sort ASC, backup.id ASC.*LIMIT \$4.*FOR UPDATE OF backup SKIP LOCKED`).
				WithArgs(sqlmock.AnyArg(), int64(defaultProbeOfflineSec), int64(clientEntryBackupIPSuccessThreshold), int64(2)).WillReturnRows(poolRows)
			if tc.poolCount == 2 {
				expectShiftClientEntryVisibleSortsAfter(mock, 30, 10)
				groupA := mock.ExpectQuery(`INSERT INTO v2_client_entry_user_policy_split_group`).
					WithArgs(int64(9), nil, "A", "A", "192.0.2.51", int64(10), int64(30), sqlmock.AnyArg(), nil)
				if tc.failGroup {
					groupA.WillReturnError(errors.New("simulated insert failure"))
				} else {
					groupA.WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(101)))
					mock.ExpectQuery(`INSERT INTO v2_client_entry_user_policy_split_group`).
						WithArgs(int64(9), nil, "B", "B", "192.0.2.52", int64(20), int64(40), sqlmock.AnyArg(), nil).
						WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(102)))
					assignmentRows := sqlmock.NewRows([]string{"group_id"})
					for i := 0; i < tc.userCount; i++ {
						groupID := int64(101)
						if i >= (tc.userCount+1)/2 {
							groupID = 102
						}
						assignmentRows.AddRow(groupID)
					}
					mock.ExpectQuery(`(?s)WITH eligible AS \(.*FROM v2_user users.*WHERE users.id BETWEEN \$1 AND \$2.*INSERT INTO v2_client_entry_user_policy_split_assignment.*RETURNING group_id`).
						WithArgs(int64(100), int64(200), int64(9), int64(101), int64(102), sqlmock.AnyArg()).WillReturnRows(assignmentRows)
					if tc.userCount >= 2 {
						expectDetachClientEntryCollectionMembers(mock)
						// Exact SET clause ensures persisted conditions and UA are not rewritten.
						mock.ExpectExec(`UPDATE v2_client_entry_user_policy\s+SET mode = 'split', entry_host = '', extra_nodes = '\[\]', snapshot_from = NULL, snapshot_to = NULL, updated_at = \$2\s+WHERE id = \$1 AND mode = 'standard'`).
							WithArgs(int64(9), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
					}
				}
			}
			if tc.wantError != "" {
				mock.ExpectRollback()
			} else {
				mock.ExpectCommit()
			}
			record, err := service.ConvertClientEntryUserPolicyToSplit(context.Background(), ClientEntryUserPolicySplitConvertRequest{
				PolicyID: 9, EntryHostA: tc.previewA, EntryHostB: tc.previewB, UseBackupIPPool: true,
			})
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("error = %v, want %q", err, tc.wantError)
				}
				if tc.wantPoolError && !errors.Is(err, ErrClientEntryBackupIPInsufficient) {
					t.Fatalf("pool error identity lost: %v", err)
				}
			} else if err != nil || record.Mode != ClientEntryUserPolicyModeSplit || record.SnapshotUserCount != 5 || len(record.Conditions) != 2 || len(record.SplitGroups) != 2 {
				t.Fatalf("converted record = %#v, error = %v", record, err)
			} else if record.SplitGroups[0].EntryHost != "192.0.2.51" || record.SplitGroups[1].EntryHost != "192.0.2.52" || record.SplitGroups[0].UserCount != 3 || record.SplitGroups[1].UserCount != 2 {
				t.Fatalf("pool hosts or fixed membership incorrect: %#v", record.SplitGroups)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
