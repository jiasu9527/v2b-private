package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestObservationArchiveSchemaIsAdditiveAndIdempotent(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for attempt := 0; attempt < 2; attempt++ {
		for _, statement := range []string{
			`CREATE TABLE IF NOT EXISTS v2_node_observation_archive`,
			`ALTER TABLE v2_node_observation_archive ALTER COLUMN payload SET STORAGE EXTERNAL`,
			`CREATE INDEX IF NOT EXISTS idx_observation_archive_user_time`,
			`CREATE INDEX IF NOT EXISTS idx_observation_archive_time`,
			`CREATE INDEX IF NOT EXISTS idx_observation_archive_last_time`,
			`CREATE INDEX IF NOT EXISTS idx_observation_archive_nodes`,
			`CREATE INDEX IF NOT EXISTS idx_observation_archive_hosts`,
			`CREATE INDEX IF NOT EXISTS idx_observation_archive_ips`,
		} {
			mock.ExpectExec(statement).WillReturnResult(sqlmock.NewResult(0, 0))
		}
		if err := EnsureObservationArchiveSchema(context.Background(), db); err != nil {
			t.Fatal(err)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestObservationArchiveSchemaRejectsNilAndStopsOnError(t *testing.T) {
	if err := EnsureObservationArchiveSchema(context.Background(), nil); err == nil {
		t.Fatal("nil database accepted")
	}
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectExec(`CREATE TABLE IF NOT EXISTS v2_node_observation_archive`).WillReturnError(errors.New("temporary database error"))
	if err := EnsureObservationArchiveSchema(context.Background(), db); err == nil {
		t.Fatal("database schema error was hidden")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
