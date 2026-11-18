package postgres

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestObservationSchemaIsAdditiveAndIdempotent(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i := 0; i < 2; i++ {
		for _, pattern := range []string{
			`CREATE TABLE IF NOT EXISTS v2_node_observation_config`,
			`ALTER TABLE v2_node_observation_config ADD COLUMN IF NOT EXISTS mode`,
			`CREATE TABLE IF NOT EXISTS v2_node_observation_capability`,
			`CREATE TABLE IF NOT EXISTS v2_node_observation_event`,
			`CREATE INDEX IF NOT EXISTS idx_v2_node_observation_event_time`,
			`CREATE INDEX IF NOT EXISTS idx_v2_node_observation_event_user_time`,
			`CREATE INDEX IF NOT EXISTS idx_v2_node_observation_event_node_time`,
			`CREATE INDEX IF NOT EXISTS idx_v2_node_observation_event_host_time`,
			`CREATE INDEX IF NOT EXISTS idx_v2_node_observation_event_ip_time`,
			`CREATE TABLE IF NOT EXISTS v2_node_observation_compact`,
			`CREATE INDEX IF NOT EXISTS idx_observation_compact_time`,
			`CREATE INDEX IF NOT EXISTS idx_observation_compact_user_time`,
			`CREATE INDEX IF NOT EXISTS idx_observation_compact_node_time`,
			`CREATE INDEX IF NOT EXISTS idx_observation_compact_host_time`,
			`CREATE INDEX IF NOT EXISTS idx_observation_compact_ip_time`,
			`CREATE OR REPLACE VIEW v2_node_observation_history`,
		} {
			mock.ExpectExec(pattern).WillReturnResult(sqlmock.NewResult(0, 0))
		}
		mock.ExpectBegin()
		mock.ExpectExec(`SET LOCAL lock_timeout='2s'`).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectExec(`SELECT pg_advisory_xact_lock\(582791,0\)`).WillReturnResult(sqlmock.NewResult(0, 0))
		for offset := -7; offset <= 1; offset++ {
			mock.ExpectExec(`CREATE TABLE IF NOT EXISTS v2_node_observation_day_[0-9]{8} PARTITION OF v2_node_observation_compact FOR VALUES FROM \([0-9]+\) TO \([0-9]+\)`).WillReturnResult(sqlmock.NewResult(0, 0))
		}
		mock.ExpectCommit()
		if err := EnsureObservationSchema(context.Background(), db); err != nil {
			t.Fatal(err)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestObservationSchemaRejectsNilDB(t *testing.T) {
	if err := EnsureObservationSchema(context.Background(), nil); err == nil {
		t.Fatal("expected nil database error")
	}
}
