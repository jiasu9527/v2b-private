package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestOnlineLocationSchemaUsesAtomicDDL(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit", true: "rollback"}[fail], func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			mock.ExpectBegin()
			mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WillReturnResult(sqlmock.NewResult(0, 0))
			mock.ExpectExec(`CREATE TABLE IF NOT EXISTS v2_user_online_location`).WillReturnResult(sqlmock.NewResult(0, 0))
			mock.ExpectExec(`ALTER TABLE v2_user_online_location ADD COLUMN IF NOT EXISTS city_name`).WillReturnResult(sqlmock.NewResult(0, 0))
			index := mock.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_v2_user_online_location_last_seen`)
			if fail {
				index.WillReturnError(errors.New("index unavailable"))
				mock.ExpectRollback()
			} else {
				index.WillReturnResult(sqlmock.NewResult(0, 0))
				mock.ExpectCommit()
			}
			err = EnsureOnlineLocationSchema(context.Background(), db)
			if (err != nil) != fail {
				t.Fatalf("EnsureOnlineLocationSchema error = %v; want failure %v", err, fail)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
