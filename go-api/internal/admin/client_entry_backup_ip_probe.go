package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Probe target IDs share one positive BIGINT namespace with DNS failover and
// user-entry monitor targets.  The two highest non-sign bits are used as a
// stable discriminator so a pool row can never be mistaken for a DNS target
// (or for an ordinary user-entry target) when results are reported in a mixed
// batch.
const (
	clientEntryBackupIPProbeTargetOffset int64 = 3 << 61
	clientEntryBackupIPProbeTargetLimit  int64 = 1 << 61
)

func isClientEntryBackupIPProbeTargetID(targetID int64) bool {
	return targetID >= clientEntryBackupIPProbeTargetOffset
}

func encodeClientEntryBackupIPProbeTargetID(backupIPID int64) int64 {
	if backupIPID <= 0 || backupIPID >= clientEntryBackupIPProbeTargetLimit {
		return 0
	}
	return clientEntryBackupIPProbeTargetOffset + backupIPID
}

func decodeClientEntryBackupIPProbeTargetID(targetID int64) (int64, bool) {
	if !isClientEntryBackupIPProbeTargetID(targetID) {
		return 0, false
	}
	decoded := targetID - clientEntryBackupIPProbeTargetOffset
	return decoded, decoded > 0 && decoded < clientEntryBackupIPProbeTargetLimit
}

// listClientEntryBackupIPProbeTasks assigns every enabled pool address to
// every enabled probe.  There is intentionally no per-probe selector: pool
// availability is derived from all currently-online probes.
func (s *DBService) listClientEntryBackupIPProbeTasks(ctx context.Context, probeID int64) ([]DNSProbeTask, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT backup.id, backup.generation, backup.ip, backup.port,
backup.tcp_timeout_ms, backup.check_interval_sec
FROM v2_client_entry_backup_ip backup
WHERE backup.enabled = 1
  AND EXISTS (SELECT 1 FROM v2_dns_probe probe WHERE probe.id = $1 AND probe.enabled = 1)
ORDER BY backup.sort ASC, backup.id ASC`, probeID)
	if err != nil {
		return nil, fmt.Errorf("list client entry backup IP probe tasks: %w", err)
	}
	defer rows.Close()
	tasks := make([]DNSProbeTask, 0)
	for rows.Next() {
		var backupIPID int64
		var task DNSProbeTask
		if err := rows.Scan(&backupIPID, &task.TargetVersion, &task.CheckHost, &task.CheckPort,
			&task.TCPTimeoutMS, &task.CheckIntervalSec); err != nil {
			return nil, fmt.Errorf("scan client entry backup IP probe task: %w", err)
		}
		task.TargetID = encodeClientEntryBackupIPProbeTargetID(backupIPID)
		task.GroupID = task.TargetID
		if task.TargetID > 0 {
			tasks = append(tasks, task)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate client entry backup IP probe tasks: %w", err)
	}
	return tasks, nil
}

type clientEntryBackupIPProbeSnapshot struct {
	ID               int64
	Generation       int64
	CheckIntervalSec int64
	TCPTimeoutMS     int64
}

func loadClientEntryBackupIPProbeSnapshot(ctx context.Context, tx *sql.Tx, probeID, backupIPID int64) (clientEntryBackupIPProbeSnapshot, error) {
	var snapshot clientEntryBackupIPProbeSnapshot
	err := tx.QueryRowContext(ctx, `SELECT backup.id, backup.generation, backup.check_interval_sec, backup.tcp_timeout_ms
FROM v2_client_entry_backup_ip backup
JOIN v2_dns_probe probe ON probe.id = $1 AND probe.enabled = 1
WHERE backup.id = $2 AND backup.enabled = 1
	FOR SHARE OF backup`, probeID, backupIPID).Scan(&snapshot.ID, &snapshot.Generation,
		&snapshot.CheckIntervalSec, &snapshot.TCPTimeoutMS)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return snapshot, fmt.Errorf("load client entry backup IP probe target: %w", err)
	}
	return snapshot, err
}

func (s *DBService) reportClientEntryBackupIPProbeResults(ctx context.Context, probeID int64, results []DNSProbeResult) (DNSProbeReportResult, error) {
	summary := DNSProbeReportResult{GroupIDs: make([]int64, 0)}
	if len(results) == 0 {
		return summary, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return summary, fmt.Errorf("begin client entry backup IP probe report: %w", err)
	}
	defer tx.Rollback()

	now := time.Now().Unix()
	var enabled int64
	var heartbeat sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT enabled, last_heartbeat_at
FROM v2_dns_probe WHERE id = $1 FOR UPDATE`, probeID).Scan(&enabled, &heartbeat); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return summary, ErrDNSProbeUnauthorized
		}
		return summary, fmt.Errorf("load client entry backup IP probe heartbeat: %w", err)
	}
	if enabled != 1 {
		return summary, ErrDNSProbeUnauthorized
	}
	if !dnsProbeHeartbeatFresh(heartbeat, now, defaultProbeOfflineSec) {
		return summary, ErrDNSProbeHeartbeatRequired
	}

	seenResultIDs := make(map[string]struct{}, len(results))
	for _, result := range results {
		if _, duplicate := seenResultIDs[result.ResultID]; duplicate {
			summary.Duplicates++
			continue
		}
		seenResultIDs[result.ResultID] = struct{}{}

		backupIPID, ok := decodeClientEntryBackupIPProbeTargetID(result.TargetID)
		if !ok {
			summary.Skipped++
			continue
		}
		snapshot, err := loadClientEntryBackupIPProbeSnapshot(ctx, tx, probeID, backupIPID)
		if errors.Is(err, sql.ErrNoRows) {
			summary.Skipped++
			continue
		}
		if err != nil {
			return summary, err
		}
		if result.TargetVersion <= 0 || result.TargetVersion != snapshot.Generation {
			summary.Skipped++
			continue
		}

		var inboxID int64
		err = tx.QueryRowContext(ctx, `INSERT INTO v2_client_entry_backup_ip_result_inbox
(probe_id, backup_ip_id, result_id, created_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT (probe_id, result_id) DO NOTHING
RETURNING id`, probeID, backupIPID, result.ResultID, now).Scan(&inboxID)
		if errors.Is(err, sql.ErrNoRows) {
			summary.Duplicates++
			continue
		}
		if err != nil {
			return summary, fmt.Errorf("deduplicate client entry backup IP result: %w", err)
		}

		var successStreak, failureStreak int64
		var previousReportedAt sql.NullInt64
		err = tx.QueryRowContext(ctx, `SELECT consecutive_success, consecutive_failure, last_reported_at
FROM v2_client_entry_backup_ip_state
WHERE backup_ip_id = $1 AND probe_id = $2
	FOR UPDATE`, backupIPID, probeID).Scan(&successStreak, &failureStreak, &previousReportedAt)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return summary, fmt.Errorf("load client entry backup IP state: %w", err)
		}
		if !dnsFailoverProbeStateFresh(previousReportedAt, now, snapshot.CheckIntervalSec,
			snapshot.TCPTimeoutMS, defaultProbeOfflineSec) {
			successStreak = 0
			failureStreak = 0
		}

		success := result.Success != nil && *result.Success
		var latency any
		lastError := result.Error
		if success {
			successStreak = saturatingDNSProbeStreakAdd(successStreak, 1)
			failureStreak = 0
			lastError = ""
			if result.LatencyMS != nil {
				latency = *result.LatencyMS
			}
		} else {
			successStreak = 0
			failureStreak = saturatingDNSProbeStreakAdd(failureStreak, 1)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO v2_client_entry_backup_ip_state
(backup_ip_id, probe_id, last_success, last_latency_ms, last_error,
 consecutive_success, consecutive_failure, last_reported_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8)
ON CONFLICT (backup_ip_id, probe_id) DO UPDATE SET
last_success = EXCLUDED.last_success,
last_latency_ms = EXCLUDED.last_latency_ms,
last_error = EXCLUDED.last_error,
consecutive_success = EXCLUDED.consecutive_success,
consecutive_failure = EXCLUDED.consecutive_failure,
last_reported_at = EXCLUDED.last_reported_at,
updated_at = EXCLUDED.updated_at`, backupIPID, probeID, boolToInt64(success), latency,
			lastError, successStreak, failureStreak, now); err != nil {
			return summary, fmt.Errorf("save client entry backup IP state: %w", err)
		}
		summary.Accepted++
	}
	if err := tx.Commit(); err != nil {
		return summary, fmt.Errorf("commit client entry backup IP probe report: %w", err)
	}
	if summary.Accepted > 0 {
		// A newly healthy pool row may unblock a durable auto-split operation.
		s.wakeClientEntryMonitorEvaluation(ctx)
	}
	return summary, nil
}
