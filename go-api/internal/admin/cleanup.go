package admin

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"time"
)

const (
	cleanupLastCheckKey               = "MAINTENANCE_CLEANUP_LAST_AT_"
	dnsFailoverLogKeepDays      int64 = 3
	dnsProbeResultInboxKeepDays int64 = 3
	dnsFailoverEventKeepDays    int64 = 3
	clientEntryMonitorKeepDays  int64 = 3
	dnsFailoverCleanupBatchSize       = 5000
)

type dnsFailoverRetentionExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func deleteDNSFailoverRetentionRows(ctx context.Context, execer dnsFailoverRetentionExecer, query string, cutoff, batchSize int64) error {
	if batchSize <= 0 {
		return fmt.Errorf("DNS failover cleanup batch size must be positive")
	}
	for {
		result, err := execer.ExecContext(ctx, query, cutoff, batchSize)
		if err != nil {
			return err
		}
		deleted, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("read DNS failover cleanup result: %w", err)
		}
		if deleted < batchSize {
			return nil
		}
	}
}

func (s *DBService) CleanupRetention(ctx context.Context) error {
	if s.db == nil {
		return ErrUnavailable
	}

	now := time.Now()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).Unix()

	lastRun, err := s.getInt64KV(ctx, cleanupLastCheckKey)
	if err != nil {
		return fmt.Errorf("query cleanup last runtime: %w", err)
	}
	if lastRun != nil && *lastRun >= todayStart {
		return nil
	}

	cfg := s.currentConfig()
	nowUnix := now.Unix()

	if cfg.MailLogKeepDays > 0 {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM v2_mail_log WHERE created_at < $1`, nowUnix-(cfg.MailLogKeepDays*86400)); err != nil {
			return fmt.Errorf("cleanup mail log: %w", err)
		}
	}
	if cfg.LogKeepDays > 0 {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM v2_log WHERE created_at < $1`, nowUnix-(cfg.LogKeepDays*86400)); err != nil {
			return fmt.Errorf("cleanup system log: %w", err)
		}
	}
	if cfg.StatUserKeepDays > 0 {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM v2_stat_user WHERE record_at < $1`, todayStart-(cfg.StatUserKeepDays*86400)); err != nil {
			return fmt.Errorf("cleanup user traffic stat: %w", err)
		}
	}
	if cfg.StatServerKeepDays > 0 {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM v2_stat_server WHERE record_at < $1`, todayStart-(cfg.StatServerKeepDays*86400)); err != nil {
			return fmt.Errorf("cleanup server traffic stat: %w", err)
		}
	}

	if _, err := s.db.ExecContext(ctx, `DELETE FROM v2_auth_session WHERE expire_at > 0 AND expire_at <= $1`, nowUnix); err != nil {
		return fmt.Errorf("cleanup expired auth session: %w", err)
	}
	if cfg.AuthSessionKeepDays > 0 {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM v2_auth_session WHERE updated_at < $1`, nowUnix-(cfg.AuthSessionKeepDays*86400)); err != nil {
			return fmt.Errorf("cleanup auth session history: %w", err)
		}
	}

	if _, err := s.db.ExecContext(ctx, `DELETE FROM v2_runtime_kv WHERE expire_at > 0 AND expire_at <= $1`, nowUnix); err != nil {
		return fmt.Errorf("cleanup expired runtime kv: %w", err)
	}
	if cfg.RuntimeKVKeepDays > 0 {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM v2_runtime_kv WHERE expire_at = 0 AND updated_at < $1`, nowUnix-(cfg.RuntimeKVKeepDays*86400)); err != nil {
			return fmt.Errorf("cleanup runtime kv history: %w", err)
		}
	}

	if cfg.FailedJobsKeepDays > 0 {
		cutoff := now.Add(-time.Duration(cfg.FailedJobsKeepDays) * 24 * time.Hour)
		if _, err := s.db.ExecContext(ctx, `DELETE FROM failed_jobs WHERE failed_at < $1`, cutoff); err != nil {
			return fmt.Errorf("cleanup failed jobs: %w", err)
		}
	}

	if err := deleteDNSFailoverRetentionRows(ctx, s.db, `WITH doomed AS (
SELECT id FROM v2_dns_failover_log WHERE created_at < $1 ORDER BY id LIMIT $2
)
DELETE FROM v2_dns_failover_log WHERE id IN (SELECT id FROM doomed)`, nowUnix-(dnsFailoverLogKeepDays*86400), dnsFailoverCleanupBatchSize); err != nil {
		return fmt.Errorf("cleanup DNS failover diagnostic log: %w", err)
	}
	if err := deleteDNSFailoverRetentionRows(ctx, s.db, `WITH doomed AS (
SELECT id FROM v2_dns_probe_result_inbox WHERE created_at < $1 ORDER BY id LIMIT $2
)
DELETE FROM v2_dns_probe_result_inbox WHERE id IN (SELECT id FROM doomed)`, nowUnix-(dnsProbeResultInboxKeepDays*86400), dnsFailoverCleanupBatchSize); err != nil {
		return fmt.Errorf("cleanup DNS probe result inbox: %w", err)
	}
	if err := deleteDNSFailoverRetentionRows(ctx, s.db, `WITH doomed AS (
SELECT id FROM v2_dns_failover_event WHERE notified_at IS NOT NULL AND created_at < $1 ORDER BY id LIMIT $2
)
DELETE FROM v2_dns_failover_event WHERE id IN (SELECT id FROM doomed)`, nowUnix-(dnsFailoverEventKeepDays*86400), dnsFailoverCleanupBatchSize); err != nil {
		return fmt.Errorf("cleanup notified DNS failover event: %w", err)
	}
	clientEntryCutoff := nowUnix - (clientEntryMonitorKeepDays * 86400)
	if err := deleteDNSFailoverRetentionRows(ctx, s.db, `WITH doomed AS (
SELECT id FROM v2_client_entry_monitor_event WHERE created_at < $1 ORDER BY id LIMIT $2
)
DELETE FROM v2_client_entry_monitor_event WHERE id IN (SELECT id FROM doomed)`, clientEntryCutoff, dnsFailoverCleanupBatchSize); err != nil {
		return fmt.Errorf("cleanup client entry monitor event: %w", err)
	}
	if err := deleteDNSFailoverRetentionRows(ctx, s.db, `WITH doomed AS (
SELECT id FROM v2_client_entry_monitor_result_inbox WHERE created_at < $1 ORDER BY id LIMIT $2
)
DELETE FROM v2_client_entry_monitor_result_inbox WHERE id IN (SELECT id FROM doomed)`, clientEntryCutoff, dnsFailoverCleanupBatchSize); err != nil {
		return fmt.Errorf("cleanup client entry monitor result inbox: %w", err)
	}
	if err := deleteDNSFailoverRetentionRows(ctx, s.db, `WITH doomed AS (
SELECT id FROM v2_client_entry_monitor_run_result WHERE created_at < $1 ORDER BY id LIMIT $2
)
DELETE FROM v2_client_entry_monitor_run_result WHERE id IN (SELECT id FROM doomed)`, clientEntryCutoff, dnsFailoverCleanupBatchSize); err != nil {
		return fmt.Errorf("cleanup client entry monitor run result: %w", err)
	}
	if err := deleteDNSFailoverRetentionRows(ctx, s.db, `WITH doomed AS (
SELECT id FROM v2_client_entry_monitor_run WHERE status <> 'running' AND created_at < $1 ORDER BY id LIMIT $2
)
DELETE FROM v2_client_entry_monitor_run WHERE id IN (SELECT id FROM doomed)`, clientEntryCutoff, dnsFailoverCleanupBatchSize); err != nil {
		return fmt.Errorf("cleanup client entry monitor run: %w", err)
	}

	if _, err := s.db.ExecContext(ctx, `INSERT INTO v2_runtime_kv (k, v, expire_at, created_at, updated_at)
VALUES ($1, $2, 0, $3, $3)
ON CONFLICT (k) DO UPDATE SET v = EXCLUDED.v, expire_at = EXCLUDED.expire_at, updated_at = EXCLUDED.updated_at`,
		cleanupLastCheckKey, strconv.FormatInt(todayStart, 10), nowUnix,
	); err != nil {
		return fmt.Errorf("save cleanup last runtime: %w", err)
	}

	return nil
}
