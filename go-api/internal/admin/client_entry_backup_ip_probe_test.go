package admin

import (
	"context"
	"testing"
	"time"

	"forest/go-api/internal/config"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestClientEntryBackupIPProbeTargetNamespaceDoesNotOverlapEntryTargets(t *testing.T) {
	for _, id := range []int64{1, 42, clientEntryBackupIPProbeTargetLimit - 1} {
		encoded := encodeClientEntryBackupIPProbeTargetID(id)
		decoded, ok := decodeClientEntryBackupIPProbeTargetID(encoded)
		if !ok || decoded != id || !isClientEntryBackupIPProbeTargetID(encoded) || isClientEntryProbeTargetID(encoded) {
			t.Fatalf("backup id %d encoded=%d decoded=%d ok=%v", id, encoded, decoded, ok)
		}
	}
	entryEncoded := encodeClientEntryProbeTargetID(42)
	if entryEncoded == 0 || isClientEntryBackupIPProbeTargetID(entryEncoded) {
		t.Fatalf("ordinary entry target leaked into backup namespace: %d", entryEncoded)
	}
}

func TestListClientEntryBackupIPProbeTasksReturnsEveryEnabledPoolAddress(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := NewDBService(config.Config{}, db)
	mock.ExpectQuery(`(?s)SELECT backup.id, backup.generation, backup.ip, backup.port.*WHERE backup.enabled = 1.*probe.id = \$1.*ORDER BY backup.sort ASC, backup.id ASC`).
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "generation", "ip", "port", "tcp_timeout_ms", "check_interval_sec"}).
			AddRow(3, 2, "192.0.2.3", 443, 3000, 30).
			AddRow(9, 4, "2001:db8::9", 8443, 5000, 60))
	tasks, err := service.listClientEntryBackupIPProbeTasks(context.Background(), 7)
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(tasks) != 2 || tasks[0].TargetID != encodeClientEntryBackupIPProbeTargetID(3) || tasks[1].CheckHost != "2001:db8::9" || tasks[1].TargetVersion != 4 {
		t.Fatalf("tasks = %#v", tasks)
	}
	if tasks[0].GroupID != tasks[0].TargetID || tasks[1].CheckPort != 8443 {
		t.Fatalf("task framing = %#v", tasks)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestReportClientEntryBackupIPProbeResultAdvancesSuccessStreak(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := NewDBService(config.Config{}, db)
	now := time.Now().Unix()
	backupID := int64(5)
	probeID := int64(7)
	latency := int64(18)
	success := true
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT enabled, last_heartbeat_at.*v2_dns_probe WHERE id = \$1 FOR UPDATE`).
		WithArgs(probeID).
		WillReturnRows(sqlmock.NewRows([]string{"enabled", "last_heartbeat_at"}).AddRow(1, now))
	mock.ExpectQuery(`(?s)SELECT backup.id, backup.generation.*WHERE backup.id = \$2.*FOR SHARE OF backup`).
		WithArgs(probeID, backupID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "generation", "check_interval_sec", "tcp_timeout_ms"}).
			AddRow(backupID, 3, 30, 3000))
	mock.ExpectQuery(`INSERT INTO v2_client_entry_backup_ip_result_inbox`).
		WithArgs(probeID, backupID, "result-1", sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(11))
	mock.ExpectQuery(`SELECT consecutive_success, consecutive_failure, last_reported_at`).
		WithArgs(backupID, probeID).
		WillReturnRows(sqlmock.NewRows([]string{"consecutive_success", "consecutive_failure", "last_reported_at"}).AddRow(1, 0, now))
	mock.ExpectExec(`INSERT INTO v2_client_entry_backup_ip_state`).
		WithArgs(backupID, probeID, int64(1), latency, "", int64(2), int64(0), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	summary, err := service.reportClientEntryBackupIPProbeResults(context.Background(), probeID, []DNSProbeResult{{
		ResultID: "result-1", TargetID: encodeClientEntryBackupIPProbeTargetID(backupID),
		TargetVersion: 3, Success: &success, LatencyMS: &latency,
	}})
	if err != nil || summary.Accepted != 1 || summary.Duplicates != 0 || summary.Skipped != 0 {
		t.Fatalf("summary=%#v err=%v", summary, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestReportClientEntryBackupIPProbeResultResetsStaleSuccessStreak(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := NewDBService(config.Config{}, db)
	now := time.Now().Unix()
	backupID := int64(5)
	probeID := int64(7)
	latency := int64(18)
	success := true
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT enabled, last_heartbeat_at.*v2_dns_probe WHERE id = \$1 FOR UPDATE`).
		WithArgs(probeID).
		WillReturnRows(sqlmock.NewRows([]string{"enabled", "last_heartbeat_at"}).AddRow(1, now))
	mock.ExpectQuery(`(?s)SELECT backup.id, backup.generation.*WHERE backup.id = \$2.*FOR SHARE OF backup`).
		WithArgs(probeID, backupID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "generation", "check_interval_sec", "tcp_timeout_ms"}).
			AddRow(backupID, 3, 30, 3000))
	mock.ExpectQuery(`INSERT INTO v2_client_entry_backup_ip_result_inbox`).
		WithArgs(probeID, backupID, "result-after-gap", sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(12))
	mock.ExpectQuery(`SELECT consecutive_success, consecutive_failure, last_reported_at`).
		WithArgs(backupID, probeID).
		WillReturnRows(sqlmock.NewRows([]string{"consecutive_success", "consecutive_failure", "last_reported_at"}).
			AddRow(1, 0, now-1000))
	mock.ExpectExec(`INSERT INTO v2_client_entry_backup_ip_state`).
		WithArgs(backupID, probeID, int64(1), latency, "", int64(1), int64(0), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	summary, err := service.reportClientEntryBackupIPProbeResults(context.Background(), probeID, []DNSProbeResult{{
		ResultID: "result-after-gap", TargetID: encodeClientEntryBackupIPProbeTargetID(backupID),
		TargetVersion: 3, Success: &success, LatencyMS: &latency,
	}})
	if err != nil || summary.Accepted != 1 {
		t.Fatalf("summary=%#v err=%v", summary, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}
