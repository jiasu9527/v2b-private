package postgres

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestObservationDayAlwaysUsesUTCBoundary(t *testing.T) {
	for _, input := range []string{
		"2026-11-17T23:59:59-08:00",
		"2026-11-17T00:00:01+08:00",
		"2026-03-08T01:59:59-08:00",
		"2026-03-08T03:00:00-07:00",
		"2026-11-01T01:59:59-07:00",
		"2026-11-01T01:00:00-08:00",
	} {
		instant, err := time.Parse(time.RFC3339, input)
		if err != nil {
			t.Fatal(err)
		}
		day := observationUTCDay(instant)
		if day.Location() != time.UTC || day.Hour() != 0 || day.Minute() != 0 || day.Second() != 0 || day.Nanosecond() != 0 {
			t.Errorf("%s produced a non-UTC midnight: %s", input, day)
		}
		if day.Format("2006-01-02") != instant.UTC().Format("2006-01-02") {
			t.Errorf("%s produced the wrong UTC day: %s", input, day)
		}
		if day.Unix() > instant.Unix() || day.AddDate(0, 0, 1).Unix() <= instant.Unix() {
			t.Errorf("%s is outside the computed UTC day %s", input, day)
		}
		if day.AddDate(0, 0, 1).Sub(day) != 24*time.Hour {
			t.Error("UTC partition day changed length across DST")
		}
	}
}

func TestEnsureObservationPartitionsPrecreatesNineUTCDays(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now, err := time.Parse(time.RFC3339, "2026-11-17T23:59:59-08:00")
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`SET LOCAL lock_timeout='2s'`)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta(`SELECT pg_advisory_xact_lock(582791,0)`)).WillReturnResult(sqlmock.NewResult(0, 0))
	today := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	for offset := -7; offset <= 1; offset++ {
		day := today.AddDate(0, 0, offset)
		query := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s%s PARTITION OF v2_node_observation_compact FOR VALUES FROM (%d) TO (%d)`, observationPartitionPrefix, day.Format("20060102"), day.Unix(), day.AddDate(0, 0, 1).Unix())
		mock.ExpectExec(regexp.QuoteMeta(query)).WillReturnResult(sqlmock.NewResult(0, 0))
	}
	mock.ExpectCommit()
	if err := EnsureObservationPartitions(context.Background(), db, now); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureObservationPartitionsRollsBackBusyDDL(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`SET LOCAL lock_timeout='2s'`)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta(`SELECT pg_advisory_xact_lock(582791,0)`)).WillReturnError(errors.New("lock timeout"))
	mock.ExpectRollback()
	if err := EnsureObservationPartitions(context.Background(), db, time.Now()); err == nil {
		t.Fatal("expected busy partition DDL to return an error")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteExpiredObservationPartitionsDropsOnlyQualifiedValidatedDay(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	day := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	name := observationPartitionPrefix + day.Format("20060102")
	bound := fmt.Sprintf("FOR VALUES FROM ('%d') TO ('%d')", day.Unix(), day.AddDate(0, 0, 1).Unix())
	cutoff := day.AddDate(0, 0, 1).Unix()
	mock.ExpectQuery(`SELECT namespace.nspname,child.relname,pg_get_expr`).WillReturnRows(sqlmock.NewRows([]string{"nspname", "relname", "bound"}).
		AddRow(`audit"archive`, name, bound).
		AddRow("public", observationPartitionPrefix+"20261111", fmt.Sprintf("FOR VALUES FROM ('%d') TO ('%d')", cutoff, cutoff+86400)).
		AddRow("public", "unrelated_20261110", bound).
		AddRow("public", observationPartitionPrefix+"20261109", bound).
		AddRow("public", observationPartitionPrefix+"20261108", "DEFAULT"))
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`SET LOCAL lock_timeout='2s'`)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta(`SELECT pg_advisory_xact_lock(582791,0)`)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta(`DROP TABLE IF EXISTS "audit""archive"."` + name + `"`)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT MAX(occurred_at) FROM v2_node_observation_event`)).WillReturnRows(sqlmock.NewRows([]string{"max"}).AddRow(cutoff))
	if err := DeleteExpiredObservationPartitions(context.Background(), db, cutoff); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteExpiredObservationLegacyHeapRequiresLockedRecheck(t *testing.T) {
	cutoff := int64(1794441600)
	for _, test := range []struct {
		name      string
		before    any
		after     any
		wantLock  bool
		wantTrunc bool
	}{
		{"empty heap", nil, nil, false, false},
		{"recent heap", cutoff, nil, false, false},
		{"fully expired heap", cutoff - 1, cutoff - 1, true, true},
		{"concurrent recent row", cutoff - 1, cutoff, true, false},
		{"concurrent truncate", cutoff - 1, nil, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			mock.ExpectQuery(`SELECT namespace.nspname,child.relname,pg_get_expr`).WillReturnRows(sqlmock.NewRows([]string{"nspname", "relname", "bound"}))
			mock.ExpectQuery(regexp.QuoteMeta(`SELECT MAX(occurred_at) FROM v2_node_observation_event`)).WillReturnRows(sqlmock.NewRows([]string{"max"}).AddRow(test.before))
			if test.wantLock {
				mock.ExpectBegin()
				mock.ExpectExec(regexp.QuoteMeta(`SET LOCAL lock_timeout='2s'`)).WillReturnResult(sqlmock.NewResult(0, 0))
				mock.ExpectExec(regexp.QuoteMeta(`LOCK TABLE v2_node_observation_event IN ACCESS EXCLUSIVE MODE`)).WillReturnResult(sqlmock.NewResult(0, 0))
				mock.ExpectQuery(regexp.QuoteMeta(`SELECT MAX(occurred_at) FROM v2_node_observation_event`)).WillReturnRows(sqlmock.NewRows([]string{"max"}).AddRow(test.after))
				if test.wantTrunc {
					mock.ExpectExec(regexp.QuoteMeta(`TRUNCATE TABLE v2_node_observation_event`)).WillReturnResult(sqlmock.NewResult(0, 0))
				}
				mock.ExpectCommit()
			}
			if err := DeleteExpiredObservationPartitions(context.Background(), db, cutoff); err != nil {
				t.Fatal(err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDeleteExpiredObservationPartitionsRollsBackBusyDrop(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	day := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	name := observationPartitionPrefix + day.Format("20060102")
	bound := fmt.Sprintf("FOR VALUES FROM ('%d') TO ('%d')", day.Unix(), day.AddDate(0, 0, 1).Unix())
	mock.ExpectQuery(`SELECT namespace.nspname,child.relname,pg_get_expr`).WillReturnRows(sqlmock.NewRows([]string{"nspname", "relname", "bound"}).AddRow("public", name, bound))
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`SET LOCAL lock_timeout='2s'`)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta(`SELECT pg_advisory_xact_lock(582791,0)`)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta(`DROP TABLE IF EXISTS "public"."` + name + `"`)).WillReturnError(errors.New("lock timeout"))
	mock.ExpectRollback()
	if err := DeleteExpiredObservationPartitions(context.Background(), db, day.AddDate(0, 0, 1).Unix()); err == nil {
		t.Fatal("expected busy partition drop to return an error")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestObservationPartitionMaintenanceRejectsNilDB(t *testing.T) {
	if err := EnsureObservationPartitions(context.Background(), nil, time.Now()); err == nil {
		t.Fatal("expected nil database error")
	}
	if err := DeleteExpiredObservationPartitions(context.Background(), nil, time.Now().Unix()); err == nil {
		t.Fatal("expected nil database error")
	}
}

func TestExpiredObservationPartitionRequiresWholeValidatedDay(t *testing.T) {
	day := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	name := observationPartitionPrefix + day.Format("20060102")
	from, to := day.Unix(), day.AddDate(0, 0, 1).Unix()
	bound := fmt.Sprintf("FOR VALUES FROM ('%d') TO ('%d')", from, to)
	tests := []struct {
		name   string
		table  string
		bound  string
		cutoff int64
		want   bool
	}{
		{"exact upper boundary", name, bound, to, true},
		{"fully expired", name, bound, to + 86400, true},
		{"last second still retained", name, bound, to - 1, false},
		{"partially expired", name, bound, from + 43200, false},
		{"future partition", name, bound, from - 1, false},
		{"unquoted catalog bounds", name, fmt.Sprintf("FOR VALUES FROM (%d) TO (%d)", from, to), to, true},
		{"wrong lower boundary", name, fmt.Sprintf("FOR VALUES FROM ('%d') TO ('%d')", from-1, to), to, false},
		{"wrong upper boundary", name, fmt.Sprintf("FOR VALUES FROM ('%d') TO ('%d')", from, to-1), to, false},
		{"wrong date in name", observationPartitionPrefix + "20261116", bound, to, false},
		{"invalid date in name", observationPartitionPrefix + "20260230", bound, to, false},
		{"other table", "other_20261117", bound, to, false},
		{"injection suffix", name + ";DROP TABLE x", bound, to, false},
		{"default partition", name, "DEFAULT", to, false},
		{"unbounded partition", name, "FOR VALUES FROM (MINVALUE) TO (MAXVALUE)", to, false},
		{"numeric overflow", name, "FOR VALUES FROM ('999999999999999999999') TO ('999999999999999999999')", to, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := expiredObservationPartition(test.table, test.bound, test.cutoff); got != test.want {
				t.Errorf("expiredObservationPartition(%q, %q, %d)=%v, want %v", test.table, test.bound, test.cutoff, got, test.want)
			}
		})
	}
}
