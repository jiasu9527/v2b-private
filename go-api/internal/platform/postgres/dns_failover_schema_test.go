package postgres

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestEnsureDNSFailoverSchemaCreatesTablesAndIndexesIdempotently(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	for range 2 {
		expectDNSFailoverSchema(mock)
	}

	for range 2 {
		if err := EnsureDNSFailoverSchema(context.Background(), db); err != nil {
			t.Fatalf("EnsureDNSFailoverSchema: %v", err)
		}
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func expectDNSFailoverSchema(mock sqlmock.Sqlmock) {
	for _, table := range []string{
		"v2_dns_probe",
		"v2_dns_failover_group",
		"v2_dns_failover_target",
		"v2_dns_failover_group_probe",
		"v2_dns_probe_target_state",
		"v2_dns_failover_event",
	} {
		mock.ExpectExec(`CREATE TABLE IF NOT EXISTS ` + table).
			WillReturnResult(sqlmock.NewResult(0, 0))
	}

	for _, index := range []string{
		"idx_v2_dns_probe_enabled_heartbeat",
		"idx_v2_dns_failover_group_enabled",
		"idx_v2_dns_failover_target_group_sort",
		"idx_v2_dns_failover_group_probe_probe",
		"idx_v2_dns_probe_target_state_target",
		"idx_v2_dns_failover_event_group_created",
	} {
		mock.ExpectExec(`CREATE INDEX IF NOT EXISTS ` + index).
			WillReturnResult(sqlmock.NewResult(0, 0))
	}
}
