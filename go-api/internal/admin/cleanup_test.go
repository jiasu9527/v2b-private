package admin

import (
	"context"
	"database/sql/driver"
	"regexp"
	"strconv"
	"testing"
	"time"

	"forest/go-api/internal/config"

	"github.com/DATA-DOG/go-sqlmock"
)

type cleanupCutoffDays int64

func (days cleanupCutoffDays) Match(value driver.Value) bool {
	cutoff, ok := value.(int64)
	if !ok {
		return false
	}
	want := time.Now().Unix() - int64(days)*86400
	delta := cutoff - want
	return delta >= -2 && delta <= 2
}

type cleanupCutoffHours int64

func (hours cleanupCutoffHours) Match(value driver.Value) bool {
	cutoff, ok := value.(int64)
	if !ok {
		return false
	}
	want := time.Now().Unix() - int64(hours)*3600
	delta := cutoff - want
	return delta >= -2 && delta <= 2
}

func cleanupDayStart(now time.Time) int64 {
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).Unix()
}

func TestCleanupRetentionSkipsWhenAlreadyRanToday(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	service := &DBService{db: db, cfg: config.Config{}}
	todayStart := cleanupDayStart(time.Now())

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT v, expire_at FROM v2_runtime_kv WHERE k = $1 LIMIT 1`)).
		WithArgs(cleanupLastCheckKey).
		WillReturnRows(sqlmock.NewRows([]string{"v", "expire_at"}).AddRow(strconv.FormatInt(todayStart, 10), int64(0)))

	if err := service.CleanupRetention(context.Background()); err != nil {
		t.Fatalf("cleanup retention: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestCleanupRetentionDeletesConfiguredHistoryAndExpiredRows(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	service := &DBService{db: db, cfg: config.Config{
		MailLogKeepDays:     15,
		LogKeepDays:         15,
		StatUserKeepDays:    90,
		StatServerKeepDays:  180,
		AuthSessionKeepDays: 30,
		RuntimeKVKeepDays:   7,
		FailedJobsKeepDays:  15,
	}}
	todayStart := cleanupDayStart(time.Now())

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT v, expire_at FROM v2_runtime_kv WHERE k = $1 LIMIT 1`)).
		WithArgs(cleanupLastCheckKey).
		WillReturnRows(sqlmock.NewRows([]string{"v", "expire_at"}))
	mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM v2_mail_log WHERE created_at < $1`)).
		WithArgs(sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 12))
	mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM v2_log WHERE created_at < $1`)).
		WithArgs(sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 3))
	mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM v2_stat_user WHERE record_at < $1`)).
		WithArgs(sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 30))
	mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM v2_stat_server WHERE record_at < $1`)).
		WithArgs(sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 8))
	mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM v2_auth_session WHERE expire_at > 0 AND expire_at <= $1`)).
		WithArgs(sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM v2_auth_session WHERE updated_at < $1`)).
		WithArgs(sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 4))
	mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM v2_runtime_kv WHERE expire_at > 0 AND expire_at <= $1`)).
		WithArgs(sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 5))
	mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM v2_runtime_kv WHERE expire_at = 0 AND updated_at < $1`)).
		WithArgs(sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM failed_jobs WHERE failed_at < $1`)).
		WithArgs(sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(`(?s)WITH doomed AS \(\s*SELECT id FROM v2_dns_failover_log WHERE created_at < \$1 ORDER BY id LIMIT \$2\s*\).*DELETE FROM v2_dns_failover_log`).
		WithArgs(cleanupCutoffDays(3), int64(dnsFailoverCleanupBatchSize)).
		WillReturnResult(sqlmock.NewResult(0, 70))
	mock.ExpectExec(`(?s)WITH doomed AS \(\s*SELECT id FROM v2_dns_probe_result_inbox WHERE created_at < \$1 ORDER BY id LIMIT \$2\s*\).*DELETE FROM v2_dns_probe_result_inbox`).
		WithArgs(cleanupCutoffDays(3), int64(dnsFailoverCleanupBatchSize)).
		WillReturnResult(sqlmock.NewResult(0, 20))
	mock.ExpectExec(`(?s)WITH doomed AS \(\s*SELECT id FROM v2_dns_failover_event WHERE notified_at IS NOT NULL AND created_at < \$1 ORDER BY id LIMIT \$2\s*\).*DELETE FROM v2_dns_failover_event`).
		WithArgs(cleanupCutoffDays(3), int64(dnsFailoverCleanupBatchSize)).
		WillReturnResult(sqlmock.NewResult(0, 4))
	expectClientEntryMonitorCleanup(mock)
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO v2_runtime_kv (k, v, expire_at, created_at, updated_at)
VALUES ($1, $2, 0, $3, $3)
ON CONFLICT (k) DO UPDATE SET v = EXCLUDED.v, expire_at = EXCLUDED.expire_at, updated_at = EXCLUDED.updated_at`)).
		WithArgs(cleanupLastCheckKey, strconv.FormatInt(todayStart, 10), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := service.CleanupRetention(context.Background()); err != nil {
		t.Fatalf("cleanup retention: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestCleanupRetentionRunsAgainNextDayAndDeletesExpirableRowsWithoutKeepDays(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	service := &DBService{db: db, cfg: config.Config{}}
	now := time.Now()
	todayStart := cleanupDayStart(now)
	yesterdayStart := time.Date(now.Year(), now.Month(), now.Day()-1, 0, 0, 0, 0, now.Location()).Unix()

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT v, expire_at FROM v2_runtime_kv WHERE k = $1 LIMIT 1`)).
		WithArgs(cleanupLastCheckKey).
		WillReturnRows(sqlmock.NewRows([]string{"v", "expire_at"}).AddRow(strconv.FormatInt(yesterdayStart, 10), int64(0)))
	mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM v2_auth_session WHERE expire_at > 0 AND expire_at <= $1`)).
		WithArgs(sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM v2_runtime_kv WHERE expire_at > 0 AND expire_at <= $1`)).
		WithArgs(sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 5))
	mock.ExpectExec(`(?s)WITH doomed AS \(\s*SELECT id FROM v2_dns_failover_log WHERE created_at < \$1 ORDER BY id LIMIT \$2\s*\).*DELETE FROM v2_dns_failover_log`).
		WithArgs(cleanupCutoffDays(3), int64(dnsFailoverCleanupBatchSize)).
		WillReturnResult(sqlmock.NewResult(0, 70))
	mock.ExpectExec(`(?s)WITH doomed AS \(\s*SELECT id FROM v2_dns_probe_result_inbox WHERE created_at < \$1 ORDER BY id LIMIT \$2\s*\).*DELETE FROM v2_dns_probe_result_inbox`).
		WithArgs(cleanupCutoffDays(3), int64(dnsFailoverCleanupBatchSize)).
		WillReturnResult(sqlmock.NewResult(0, 20))
	mock.ExpectExec(`(?s)WITH doomed AS \(\s*SELECT id FROM v2_dns_failover_event WHERE notified_at IS NOT NULL AND created_at < \$1 ORDER BY id LIMIT \$2\s*\).*DELETE FROM v2_dns_failover_event`).
		WithArgs(cleanupCutoffDays(3), int64(dnsFailoverCleanupBatchSize)).
		WillReturnResult(sqlmock.NewResult(0, 4))
	expectClientEntryMonitorCleanup(mock)
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO v2_runtime_kv (k, v, expire_at, created_at, updated_at)
VALUES ($1, $2, 0, $3, $3)
ON CONFLICT (k) DO UPDATE SET v = EXCLUDED.v, expire_at = EXCLUDED.expire_at, updated_at = EXCLUDED.updated_at`)).
		WithArgs(cleanupLastCheckKey, strconv.FormatInt(todayStart, 10), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := service.CleanupRetention(context.Background()); err != nil {
		t.Fatalf("cleanup retention: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func expectClientEntryMonitorCleanup(mock sqlmock.Sqlmock) {
	for _, table := range []string{
		"v2_client_entry_monitor_event",
		"v2_client_entry_monitor_result_inbox",
		"v2_client_entry_monitor_run_result",
	} {
		mock.ExpectExec(`(?s)WITH doomed AS \(\s*SELECT id FROM `+table+` WHERE created_at < \$1 ORDER BY id LIMIT \$2\s*\).*DELETE FROM `+table).
			WithArgs(cleanupCutoffHours(24), int64(dnsFailoverCleanupBatchSize)).
			WillReturnResult(sqlmock.NewResult(0, 4))
	}
	mock.ExpectExec(`(?s)WITH doomed AS \(\s*SELECT id FROM v2_client_entry_monitor_run WHERE status <> 'running' AND created_at < \$1 ORDER BY id LIMIT \$2\s*\).*DELETE FROM v2_client_entry_monitor_run`).
		WithArgs(cleanupCutoffHours(24), int64(dnsFailoverCleanupBatchSize)).
		WillReturnResult(sqlmock.NewResult(0, 4))
}

func TestDeleteDNSFailoverRetentionRowsRunsBoundedBatchesUntilDrained(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	query := `WITH doomed AS (SELECT id FROM sample WHERE created_at < $1 ORDER BY id LIMIT $2)
DELETE FROM sample WHERE id IN (SELECT id FROM doomed)`
	mock.ExpectExec(regexp.QuoteMeta(query)).WithArgs(int64(100), int64(5000)).WillReturnResult(sqlmock.NewResult(0, 5000))
	mock.ExpectExec(regexp.QuoteMeta(query)).WithArgs(int64(100), int64(5000)).WillReturnResult(sqlmock.NewResult(0, 3))

	if err := deleteDNSFailoverRetentionRows(context.Background(), db, query, 100, 5000); err != nil {
		t.Fatalf("deleteDNSFailoverRetentionRows: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}
