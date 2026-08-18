package admin

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"forest/go-api/internal/config"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestNormalizeClientEntryBackupIPSaveRequestCanonicalizesAndDefaults(t *testing.T) {
	req, err := normalizeClientEntryBackupIPSaveRequest(ClientEntryBackupIPSaveRequest{
		Name: "  备用一号  ", IP: "2001:0db8::1", Port: 8443,
	}, true)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if req.Name != "备用一号" || req.IP != "2001:db8::1" || req.Port != 8443 {
		t.Fatalf("normalized request = %#v", req)
	}
	if req.Enabled == nil || !*req.Enabled || req.CheckIntervalSec != 30 || req.TCPTimeoutMS != 3000 {
		t.Fatalf("defaults = %#v", req)
	}
	mapped, err := normalizeClientEntryBackupIPSaveRequest(ClientEntryBackupIPSaveRequest{
		IP: "::ffff:192.0.2.1", Port: 443,
	}, true)
	if err != nil || mapped.IP != "192.0.2.1" {
		t.Fatalf("mapped IPv4 normalization = %#v, %v", mapped, err)
	}

	for _, test := range []struct {
		name string
		req  ClientEntryBackupIPSaveRequest
	}{
		{name: "hostname", req: ClientEntryBackupIPSaveRequest{IP: "entry.example.com", Port: 443}},
		{name: "unspecified", req: ClientEntryBackupIPSaveRequest{IP: "0.0.0.0", Port: 443}},
		{name: "port", req: ClientEntryBackupIPSaveRequest{IP: "192.0.2.1", Port: 65536}},
		{name: "interval", req: ClientEntryBackupIPSaveRequest{IP: "192.0.2.1", Port: 443, CheckIntervalSec: 2}},
		{name: "timeout", req: ClientEntryBackupIPSaveRequest{IP: "192.0.2.1", Port: 443, TCPTimeoutMS: 99}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := normalizeClientEntryBackupIPSaveRequest(test.req, true); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestClassifyClientEntryBackupIPRequiresEveryOnlineProbeHealthy(t *testing.T) {
	base := ClientEntryBackupIPRecord{Enabled: true, EnabledProbeCount: 2, OnlineProbeCount: 2, HealthyProbeCount: 2}
	if status, available := classifyClientEntryBackupIP(base, 100); status != "available" || !available {
		t.Fatalf("healthy status=%q available=%v", status, available)
	}
	base.HealthyProbeCount = 1
	base.States = []ClientEntryBackupIPState{{ProbeOnline: true, Stale: false, LastSuccess: boolPtr(false)}}
	if status, available := classifyClientEntryBackupIP(base, 100); status != "unhealthy" || available {
		t.Fatalf("failed status=%q available=%v", status, available)
	}
	base.Used = true
	if status, available := classifyClientEntryBackupIP(base, 100); status != "in_use" || available {
		t.Fatalf("used status=%q available=%v", status, available)
	}
	base.Used = false
	base.QuarantineUntil = 101
	if status, available := classifyClientEntryBackupIP(base, 100); status != "quarantined" || available {
		t.Fatalf("quarantine status=%q available=%v", status, available)
	}
}

func TestListClientEntryBackupIPsReportsHealthAndRuleUsage(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := NewDBService(config.Config{}, db)
	service.dnsFailoverSchemaOK = true
	now := time.Now().Unix()

	mock.ExpectQuery(`SELECT id, name, ip, port, enabled, check_interval_sec`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "ip", "port", "enabled", "check_interval_sec", "tcp_timeout_ms", "generation", "sort", "quarantine_until", "created_at", "updated_at"}).
			AddRow(1, "空闲", "192.0.2.10", 443, 1, 30, 3000, 2, 10, 0, now-100, now-10).
			AddRow(2, "占用", "192.0.2.20", 443, 1, 30, 3000, 1, 20, 0, now-100, now-10))
	mock.ExpectQuery(`SELECT id, name, last_heartbeat_at`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "last_heartbeat_at"}).AddRow(7, "东京", now))
	mock.ExpectQuery(`SELECT state.backup_ip_id, state.probe_id, state.last_success`).
		WillReturnRows(sqlmock.NewRows([]string{"backup_ip_id", "probe_id", "last_success", "last_latency_ms", "last_error", "consecutive_success", "consecutive_failure", "last_reported_at"}).
			AddRow(1, 7, 1, 12, "", 2, 0, now).
			AddRow(2, 7, 1, 15, "", 3, 0, now))
	mock.ExpectQuery(`SELECT policy.id, policy.name, policy.entry_host`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "entry_host"}).AddRow(9, "普通规则", "192.0.2.20"))
	mock.ExpectQuery(`SELECT policy.id, policy.name, split_group.id`).
		WillReturnRows(sqlmock.NewRows([]string{"policy_id", "policy_name", "group_id", "group_name", "path", "entry_host"}))

	result, err := service.ListClientEntryBackupIPs(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(result.Items) != 2 {
		t.Fatalf("items = %#v", result.Items)
	}
	if result.Items[0].Status != "available" || !result.Items[0].Available || result.Items[0].HealthyProbeCount != 1 {
		t.Fatalf("available item = %#v", result.Items[0])
	}
	if result.Items[1].Status != "in_use" || !result.Items[1].Used || len(result.Items[1].Usages) != 1 || result.Items[1].Usages[0].PolicyID != 9 {
		t.Fatalf("used item = %#v", result.Items[1])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestCreateClientEntryBackupIPRejectsCanonicalDuplicate(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := NewDBService(config.Config{}, db)
	service.dnsFailoverSchemaOK = true
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id FROM v2_client_entry_backup_ip`).
		WithArgs("2001:db8::1", int64(0)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(4))
	mock.ExpectRollback()

	_, err = service.CreateClientEntryBackupIP(context.Background(), ClientEntryBackupIPSaveRequest{IP: "2001:0db8::1", Port: 443})
	if !errors.Is(err, ErrClientEntryBackupIPConflict) {
		t.Fatalf("error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestCreateClientEntryBackupIPsReturnsCommittedRowsWithoutFollowUpRead(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := NewDBService(config.Config{}, db)
	service.dnsFailoverSchemaOK = true
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id FROM v2_client_entry_backup_ip`).
		WithArgs("192.0.2.8", int64(0)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectQuery(`INSERT INTO v2_client_entry_backup_ip`).
		WithArgs("备用八", "192.0.2.8", int64(8443), int64(1), int64(30), int64(3000), int64(7), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(8)))
	mock.ExpectCommit()

	items, err := service.CreateClientEntryBackupIPs(context.Background(), []ClientEntryBackupIPSaveRequest{{
		Name: "备用八", IP: "192.0.2.8", Port: 8443, Sort: 7,
	}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(items) != 1 || items[0].ID != 8 || items[0].IP != "192.0.2.8" ||
		items[0].Status != "checking" || items[0].Generation != 1 {
		t.Fatalf("created items = %#v", items)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestClientEntryBackupIPUniqueErrorRecognitionIsNarrow(t *testing.T) {
	if !isClientEntryBackupIPUniqueError(errors.New(`ERROR: duplicate key value violates unique constraint "uniq_v2_client_entry_backup_ip_value"`)) {
		t.Fatal("pool unique violation was not recognized")
	}
	if isClientEntryBackupIPUniqueError(errors.New(`ERROR: duplicate key value violates unique constraint "unrelated"`)) {
		t.Fatal("unrelated unique violation must not be hidden")
	}
}

func TestDeleteClientEntryBackupIPRejectsAddressInUse(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := NewDBService(config.Config{}, db)
	service.dnsFailoverSchemaOK = true
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT ip FROM v2_client_entry_backup_ip`).WithArgs(int64(4)).
		WillReturnRows(sqlmock.NewRows([]string{"ip"}).AddRow("192.0.2.4"))
	mock.ExpectQuery(`(?s)SELECT EXISTS.*v2_client_entry_user_policy.*v2_client_entry_user_policy_split_group`).
		WithArgs("192.0.2.4").WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectRollback()
	deleted, err := service.DeleteClientEntryBackupIP(context.Background(), 4)
	if deleted || !errors.Is(err, ErrClientEntryBackupIPInUse) {
		t.Fatalf("deleted=%v err=%v", deleted, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestUpdateClientEntryBackupIPRejectsChangingAddressInUse(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	service := NewDBService(config.Config{}, db)
	service.dnsFailoverSchemaOK = true
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT ip, port, enabled, check_interval_sec, tcp_timeout_ms`).
		WithArgs(int64(4)).
		WillReturnRows(sqlmock.NewRows([]string{"ip", "port", "enabled", "check_interval_sec", "tcp_timeout_ms"}).
			AddRow("192.0.2.4", 443, 1, 30, 3000))
	mock.ExpectQuery(`SELECT id FROM v2_client_entry_backup_ip`).
		WithArgs("192.0.2.5", int64(4)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectQuery(`(?s)SELECT EXISTS.*v2_client_entry_user_policy.*v2_client_entry_user_policy_split_group`).
		WithArgs("192.0.2.4").WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectRollback()

	_, err = service.UpdateClientEntryBackupIP(context.Background(), 4, ClientEntryBackupIPSaveRequest{
		Name: "正在使用", IP: "192.0.2.5", Port: 443,
	})
	if !errors.Is(err, ErrClientEntryBackupIPInUse) {
		t.Fatalf("error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestClaimHealthyClientEntryBackupIPsLocksAndRequiresExactCount(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	now := int64(1000)
	mock.ExpectQuery(`(?s)FROM v2_client_entry_backup_ip backup.*FOR UPDATE OF backup SKIP LOCKED`).
		WithArgs(now, defaultProbeOfflineSec, clientEntryBackupIPSuccessThreshold, int64(2)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "ip", "port", "enabled", "check_interval_sec", "tcp_timeout_ms", "generation", "sort", "quarantine_until", "created_at", "updated_at"}).
			AddRow(1, "a", "192.0.2.1", 443, 1, 30, 3000, 1, 1, 0, 10, 10).
			AddRow(2, "b", "192.0.2.2", 443, 1, 30, 3000, 1, 2, 0, 10, 10))
	items, err := claimHealthyClientEntryBackupIPs(context.Background(), tx, 2, now)
	if err != nil || len(items) != 2 || items[0].IP != "192.0.2.1" {
		t.Fatalf("claim = %#v, %v", items, err)
	}
	mock.ExpectRollback()
	_ = tx.Rollback()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestQuarantineClientEntryBackupIPOnlyMatchesLiteralIP(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	mock.ExpectBegin()
	tx, _ := db.BeginTx(context.Background(), &sql.TxOptions{})
	mock.ExpectExec(`UPDATE v2_client_entry_backup_ip`).WithArgs("2001:db8::1", int64(200), int64(100)).WillReturnResult(sqlmock.NewResult(0, 1))
	if err := quarantineClientEntryBackupIPByHost(context.Background(), tx, "2001:0db8::1", 200, 100); err != nil {
		t.Fatalf("quarantine: %v", err)
	}
	if err := quarantineClientEntryBackupIPByHost(context.Background(), tx, "entry.example.com", 200, 100); err != nil {
		t.Fatalf("hostname quarantine: %v", err)
	}
	mock.ExpectRollback()
	_ = tx.Rollback()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func boolPtr(value bool) *bool { return &value }
