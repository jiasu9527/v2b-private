package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"forest/go-api/internal/cliententry"
)

const (
	clientEntryProbeTargetOffset int64 = 1 << 62
	// Client-entry probe reports are serialized while they update availability
	// state and decide address-level transitions. Taking one transaction lock
	// before any state row is touched prevents both duplicate address events and
	// the A→B / B→A row-lock deadlock possible with per-address lazy locks.
	// Telegram delivery happens later in the worker and is not covered by it.
	clientEntryMonitorEventLockKey int64 = -7_642_309_020
)

type clientEntryProbeTargetSnapshot struct {
	TargetID         int64
	TargetVersion    int64
	MonitorID        int64
	PolicyID         int64
	PolicyName       string
	TargetName       string
	SourceKey        string
	Host             string
	Port             int64
	ProbeName        string
	AutoSplitEnabled bool
	CheckIntervalSec int64
	TCPTimeoutMS     int64
	FailureThreshold int64
	SuccessThreshold int64
}

func isClientEntryProbeTargetID(targetID int64) bool {
	return targetID >= clientEntryProbeTargetOffset && targetID < clientEntryBackupIPProbeTargetOffset
}

func encodeClientEntryProbeTargetID(targetID int64) int64 {
	if targetID <= 0 || targetID >= clientEntryBackupIPProbeTargetLimit {
		return 0
	}
	return clientEntryProbeTargetOffset + targetID
}

func decodeClientEntryProbeTargetID(targetID int64) (int64, bool) {
	if !isClientEntryProbeTargetID(targetID) {
		return 0, false
	}
	decoded := targetID - clientEntryProbeTargetOffset
	return decoded, decoded > 0 && decoded < clientEntryBackupIPProbeTargetLimit
}

func (s *DBService) listClientEntryProbeTasks(ctx context.Context, probeID int64) ([]DNSProbeTask, error) {
	var runID int64
	var rawExpectedPairs []byte
	err := s.db.QueryRowContext(ctx, `SELECT id, expected_pairs
FROM v2_client_entry_monitor_run
WHERE status = 'running'
ORDER BY id DESC LIMIT 1`).Scan(&runID, &rawExpectedPairs)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("query active client entry monitor run: %w", err)
	}
	runPairs := make(map[[3]int64]struct{})
	if err == nil {
		pairs, decodeErr := decodeClientEntryMonitorRunPairs(rawExpectedPairs)
		if decodeErr != nil {
			return nil, decodeErr
		}
		runPairs = clientEntryMonitorRunPairSet(pairs)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT target.id, target.generation, monitor.id, monitor.policy_id,
target.host, target.port, monitor.tcp_timeout_ms, monitor.check_interval_sec
FROM v2_client_entry_monitor monitor
JOIN v2_client_entry_user_policy policy ON policy.id = monitor.policy_id AND policy.enabled = 1
JOIN v2_client_entry_monitor_target target ON target.monitor_id = monitor.id
WHERE monitor.enabled = 1
  AND EXISTS (SELECT 1 FROM v2_dns_probe probe WHERE probe.id = $1 AND probe.enabled = 1)
ORDER BY monitor.id, target.sort, target.id`, probeID)
	if err != nil {
		return nil, fmt.Errorf("list client entry probe tasks: %w", err)
	}
	defer rows.Close()
	tasks := make([]DNSProbeTask, 0)
	for rows.Next() {
		var targetID, monitorID, policyID int64
		var task DNSProbeTask
		if err := rows.Scan(&targetID, &task.TargetVersion, &monitorID, &policyID, &task.CheckHost, &task.CheckPort, &task.TCPTimeoutMS, &task.CheckIntervalSec); err != nil {
			return nil, fmt.Errorf("scan client entry probe task: %w", err)
		}
		task.TargetID = encodeClientEntryProbeTargetID(targetID)
		task.GroupID = encodeClientEntryProbeTargetID(monitorID)
		if _, selected := runPairs[[3]int64{targetID, probeID, task.TargetVersion}]; selected {
			task.RunID = runID
		}
		if task.TargetID > 0 {
			tasks = append(tasks, task)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate client entry probe tasks: %w", err)
	}
	return tasks, nil
}

func (s *DBService) reportClientEntryProbeResults(ctx context.Context, probeID int64, results []DNSProbeResult) (DNSProbeReportResult, error) {
	summary := DNSProbeReportResult{GroupIDs: make([]int64, 0)}
	if len(results) == 0 {
		return summary, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return summary, fmt.Errorf("begin client entry probe report: %w", err)
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	var enabled int64
	var heartbeat sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT enabled, last_heartbeat_at FROM v2_dns_probe WHERE id = $1 FOR UPDATE`, probeID).Scan(&enabled, &heartbeat); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return summary, ErrDNSProbeUnauthorized
		}
		return summary, fmt.Errorf("load client entry probe heartbeat: %w", err)
	}
	if enabled != 1 {
		return summary, ErrDNSProbeUnauthorized
	}
	if !dnsProbeHeartbeatFresh(heartbeat, now, defaultProbeOfflineSec) {
		return summary, ErrDNSProbeHeartbeatRequired
	}
	// This must happen before the first monitor-state read/write. Otherwise two
	// reports can each lock a different state row before waiting on the shared
	// event lock, then deadlock when their batches contain the rows in opposite
	// order.
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, clientEntryMonitorEventLockKey); err != nil {
		return summary, fmt.Errorf("lock client entry monitor events: %w", err)
	}
	seenResultIDs := make(map[string]struct{}, len(results))
	touchedRuns := make(map[int64]struct{})
	hasEvents := false
	for _, result := range results {
		if _, duplicate := seenResultIDs[result.ResultID]; duplicate {
			summary.Duplicates++
			continue
		}
		seenResultIDs[result.ResultID] = struct{}{}
		targetID, ok := decodeClientEntryProbeTargetID(result.TargetID)
		if !ok {
			summary.Skipped++
			continue
		}
		snapshot, err := loadClientEntryProbeTargetSnapshot(ctx, tx, probeID, targetID)
		if errors.Is(err, sql.ErrNoRows) {
			summary.Skipped++
			continue
		}
		if err != nil {
			return summary, err
		}
		if result.TargetVersion <= 0 || result.TargetVersion != snapshot.TargetVersion {
			summary.Skipped++
			continue
		}
		validRunID, err := validateClientEntryMonitorRunResult(ctx, tx, result.RunID, targetID, probeID, snapshot.TargetVersion)
		if err != nil {
			return summary, err
		}
		var inboxID int64
		err = tx.QueryRowContext(ctx, `INSERT INTO v2_client_entry_monitor_result_inbox
(probe_id, target_id, run_id, result_id, created_at)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (probe_id, result_id) DO NOTHING
RETURNING id`, probeID, targetID, nullablePositiveInt64(validRunID), result.ResultID, now).Scan(&inboxID)
		if errors.Is(err, sql.ErrNoRows) {
			summary.Duplicates++
			continue
		}
		if err != nil {
			return summary, fmt.Errorf("deduplicate client entry probe result: %w", err)
		}
		var previousSuccess, previousReportedAt sql.NullInt64
		var successStreak, failureStreak int64
		err = tx.QueryRowContext(ctx, `SELECT last_success, consecutive_success, consecutive_failure, last_reported_at
FROM v2_client_entry_monitor_state
WHERE target_id = $1 AND probe_id = $2
	FOR UPDATE`, targetID, probeID).Scan(&previousSuccess, &successStreak, &failureStreak, &previousReportedAt)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return summary, fmt.Errorf("load client entry monitor state: %w", err)
		}
		if !clientEntryMonitorStateFresh(previousReportedAt, now, snapshot.CheckIntervalSec, snapshot.TCPTimeoutMS) {
			previousSuccess = sql.NullInt64{}
			successStreak = 0
			failureStreak = 0
		}
		success := result.Success != nil && *result.Success
		if success {
			successStreak = saturatingDNSProbeStreakAdd(successStreak, 1)
			failureStreak = 0
		} else {
			successStreak = 0
			failureStreak = saturatingDNSProbeStreakAdd(failureStreak, 1)
		}
		var latency any
		if success && result.LatencyMS != nil {
			latency = *result.LatencyMS
		}
		errorText := result.Error
		if success {
			errorText = ""
		}
		confirmedSuccess, transition := confirmClientEntryMonitorAvailability(previousSuccess, success,
			successStreak, failureStreak, snapshot.FailureThreshold, snapshot.SuccessThreshold)
		var confirmedSuccessValue any
		if confirmedSuccess.Valid {
			confirmedSuccessValue = confirmedSuccess.Int64
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO v2_client_entry_monitor_state
(target_id, probe_id, last_success, last_latency_ms, last_error, last_resolved_ip,
consecutive_success, consecutive_failure, last_reported_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9)
ON CONFLICT (target_id, probe_id) DO UPDATE SET
last_success = EXCLUDED.last_success, last_latency_ms = EXCLUDED.last_latency_ms,
last_error = EXCLUDED.last_error, last_resolved_ip = EXCLUDED.last_resolved_ip,
consecutive_success = EXCLUDED.consecutive_success,
consecutive_failure = EXCLUDED.consecutive_failure,
last_reported_at = EXCLUDED.last_reported_at, updated_at = EXCLUDED.updated_at`,
			targetID, probeID, confirmedSuccessValue, latency, errorText, result.ResolvedIP,
			successStreak, failureStreak, now)
		if err != nil {
			return summary, fmt.Errorf("save client entry monitor state: %w", err)
		}
		if transition == "recovered" {
			if err := cancelClientEntryAutoSplitOnRecovery(ctx, tx, snapshot, now); err != nil {
				return summary, err
			}
		}
		if transition != "" {
			addressKey := clientEntryMonitorAddressKey(snapshot.Host, snapshot.Port)
			var eventAddressKey any
			if addressKey != "" {
				eventAddressKey = addressKey
			}
			shouldEnqueue := true
			if addressKey != "" {
				otherDown, err := clientEntryMonitorAddressHasOtherDown(ctx, tx, addressKey, targetID, probeID, now, defaultProbeOfflineSec)
				if err != nil {
					return summary, err
				}
				// For a down transition, any other confirmed-down state means the
				// address incident already exists. For recovery, the address is
				// considered recovered only after this is the last down state.
				shouldEnqueue = !otherDown
			}
			if shouldEnqueue {
				details, _ := json.Marshal(map[string]any{
					"policy_id": snapshot.PolicyID, "policy_name": snapshot.PolicyName,
					"target_id": targetID, "target_name": snapshot.TargetName,
					"host": snapshot.Host, "port": snapshot.Port,
					"probe_id": probeID, "probe_name": snapshot.ProbeName,
					"success": success, "latency_ms": result.LatencyMS,
					"error": errorText, "resolved_ip": result.ResolvedIP,
					"consecutive_success": successStreak,
					"consecutive_failure": failureStreak,
					"failure_threshold":   snapshot.FailureThreshold,
					"success_threshold":   snapshot.SuccessThreshold,
				})
				message := formatClientEntryMonitorTransition(snapshot, transition, result, now)
				if _, err := tx.ExecContext(ctx, `INSERT INTO v2_client_entry_monitor_event
(monitor_id, target_id, probe_id, event_type, message, details, address_key, notified_at,
notify_attempts, notify_next_attempt_at, last_notify_error, created_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, NULL, 0, $8, '', $8)`,
					snapshot.MonitorID, targetID, probeID, transition, message, string(details), eventAddressKey, now); err != nil {
					return summary, fmt.Errorf("create client entry monitor event: %w", err)
				}
				hasEvents = true
			}
			if transition == "down" && snapshot.AutoSplitEnabled {
				if err := enqueueClientEntryAutoSplit(ctx, tx, snapshot, probeID, inboxID, now); err != nil {
					return summary, err
				}
			}
		}
		if validRunID > 0 {
			_, err := tx.ExecContext(ctx, `INSERT INTO v2_client_entry_monitor_run_result
	(run_id, policy_id, policy_name, target_id, probe_id, target_name, host, port, probe_name, success,
	latency_ms, error, resolved_ip, reported_at, created_at)
	VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $14)
ON CONFLICT (run_id, target_id, probe_id) DO NOTHING`, validRunID, snapshot.PolicyID, snapshot.PolicyName,
				targetID, probeID, snapshot.TargetName, snapshot.Host, snapshot.Port, snapshot.ProbeName,
				boolToInt64(success), latency, errorText, result.ResolvedIP, now)
			if err != nil {
				return summary, fmt.Errorf("save client entry monitor run result: %w", err)
			}
			touchedRuns[validRunID] = struct{}{}
		}
		summary.Accepted++
	}
	completedRuns := make([]int64, 0)
	for runID := range touchedRuns {
		var expected, received int64
		if err := tx.QueryRowContext(ctx, `SELECT expected_results,
(SELECT COUNT(*) FROM v2_client_entry_monitor_run_result WHERE run_id = $1)
FROM v2_client_entry_monitor_run WHERE id = $1 FOR UPDATE`, runID).Scan(&expected, &received); err != nil {
			return summary, fmt.Errorf("count client entry monitor run results: %w", err)
		}
		status := "running"
		var completedAt any
		if received >= expected {
			status = "completed"
			completedAt = now
			completedRuns = append(completedRuns, runID)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE v2_client_entry_monitor_run
SET received_results = $2, status = $3, completed_at = $4, updated_at = $5
WHERE id = $1`, runID, received, status, completedAt, now); err != nil {
			return summary, fmt.Errorf("update client entry monitor run progress: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return summary, fmt.Errorf("commit client entry probe report: %w", err)
	}
	if (hasEvents || len(completedRuns) > 0) && s.dnsFailoverEvaluator != nil {
		requestDNSFailoverEvaluationWake(ctx, s.dnsFailoverEvaluator, nil)
	}
	return summary, nil
}

// clientEntryMonitorAddressKey returns the canonical host:port identity used
// to coalesce availability notifications from multiple policy groups.  The
// monitor state remains per target/probe; this key is only for the Telegram
// event queue.  Keep the port in the key so the same host on different ports
// is not incorrectly merged, and use JoinHostPort for unambiguous IPv6.
func clientEntryMonitorAddressKey(host string, port int64) string {
	host = strings.TrimSpace(host)
	if host == "" || port < 1 || port > 65535 {
		return ""
	}
	// NormalizeHost intentionally rejects bracketed IPv6 because policy input
	// stores a host without transport syntax.  Historical rows can still carry
	// brackets, so strip one pair before normalization.
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	}
	if normalized, err := cliententry.NormalizeHost(host); err == nil {
		host = normalized
	} else {
		// A malformed legacy target must not be put into a broad, guessed
		// deduplication bucket. Valid targets are normalized when saved; for
		// historical rows with invalid data, leave the key NULL and preserve the
		// event independently.
		return ""
	}
	return net.JoinHostPort(host, strconv.FormatInt(port, 10))
}

// clientEntryMonitorAddressHasOtherDown reports whether another enabled
// target/probe currently confirms the same endpoint as down. The caller has
// already updated the current target's state, so that pair is excluded from
// the query. Keeping the comparison here (rather than relying on event
// history) means a stale/cleaned event cannot suppress a genuinely new outage.
func clientEntryMonitorAddressHasOtherDown(ctx context.Context, tx *sql.Tx, addressKey string,
	targetID, probeID, now, probeOfflineSec int64,
) (bool, error) {
	if tx == nil || strings.TrimSpace(addressKey) == "" || probeOfflineSec <= 0 {
		return false, nil
	}
	var hasOtherDown bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS (
	SELECT 1
	FROM v2_client_entry_monitor_state monitor_state
	JOIN v2_client_entry_monitor_target monitor_target
	  ON monitor_target.id = monitor_state.target_id
	JOIN v2_client_entry_monitor monitor
	  ON monitor.id = monitor_target.monitor_id
	  AND monitor.enabled = 1
	JOIN v2_client_entry_user_policy policy
	  ON policy.id = monitor.policy_id
	 AND policy.enabled = 1
	JOIN v2_dns_probe probe
	  ON probe.id = monitor_state.probe_id
	  AND probe.enabled = 1
	  AND probe.last_heartbeat_at IS NOT NULL
	  AND probe.last_heartbeat_at <= $4::bigint
	  AND probe.last_heartbeat_at >= $4::bigint - $5::bigint
	WHERE monitor_state.last_success = 0
	  AND monitor_state.last_reported_at IS NOT NULL
	  AND monitor_state.last_reported_at <= $4::bigint
	  AND monitor_state.last_reported_at >= $4::bigint -
		((monitor.check_interval_sec::bigint + ((monitor.tcp_timeout_ms::bigint + 999) / 1000)) * 2)
	  AND NOT (monitor_state.target_id = $2::bigint AND monitor_state.probe_id = $3::bigint)
	  AND CASE
		WHEN position(':' IN btrim(monitor_target.host)) > 0
			THEN format('[%s]:%s', trim(BOTH '[]' FROM lower(regexp_replace(btrim(monitor_target.host), '\.$', ''))), monitor_target.port::text)
		ELSE lower(regexp_replace(btrim(monitor_target.host), '\.$', '')) || ':' || monitor_target.port::text
	  END = $1::text
	)`, addressKey, targetID, probeID, now, probeOfflineSec).Scan(&hasOtherDown)
	if err != nil {
		return false, fmt.Errorf("check other down states for client entry address: %w", err)
	}
	return hasOtherDown, nil
}

// confirmClientEntryMonitorAvailability keeps last_success as the confirmed
// availability state instead of the latest raw sample. Failures must reach the
// configured failure threshold before declaring a target down, while a target
// already confirmed down must reach the configured success threshold before
// declaring it recovered. A successful first observation can establish the
// initial healthy baseline immediately because it does not emit a transition.
func confirmClientEntryMonitorAvailability(previous sql.NullInt64, success bool, successStreak, failureStreak,
	failureThreshold, successThreshold int64,
) (sql.NullInt64, string) {
	confirmed := previous
	if success {
		if previous.Valid && previous.Int64 == 0 && successStreak < successThreshold {
			return confirmed, ""
		}
		confirmed = sql.NullInt64{Int64: 1, Valid: true}
		if previous.Valid && previous.Int64 == 0 {
			return confirmed, "recovered"
		}
		return confirmed, ""
	}
	if failureStreak < failureThreshold {
		return confirmed, ""
	}
	confirmed = sql.NullInt64{Int64: 0, Valid: true}
	if !previous.Valid || previous.Int64 == 1 {
		return confirmed, "down"
	}
	return confirmed, ""
}

func loadClientEntryProbeTargetSnapshot(ctx context.Context, tx *sql.Tx, probeID, targetID int64) (clientEntryProbeTargetSnapshot, error) {
	var snapshot clientEntryProbeTargetSnapshot
	var autoSplitEnabled int64
	err := tx.QueryRowContext(ctx, `SELECT target.id, target.generation, monitor.id, monitor.policy_id,
policy.name, target.name, target.source_key, target.host, target.port, probe.name, target.auto_split_enabled,
monitor.check_interval_sec, monitor.tcp_timeout_ms, monitor.failure_threshold, monitor.success_threshold
FROM v2_client_entry_monitor_target target
JOIN v2_client_entry_monitor monitor ON monitor.id = target.monitor_id AND monitor.enabled = 1
JOIN v2_client_entry_user_policy policy ON policy.id = monitor.policy_id AND policy.enabled = 1
JOIN v2_dns_probe probe ON probe.id = $1 AND probe.enabled = 1
WHERE target.id = $2
	FOR SHARE OF target`, probeID, targetID).Scan(
		&snapshot.TargetID, &snapshot.TargetVersion, &snapshot.MonitorID, &snapshot.PolicyID, &snapshot.PolicyName,
		&snapshot.TargetName, &snapshot.SourceKey, &snapshot.Host, &snapshot.Port, &snapshot.ProbeName,
		&autoSplitEnabled, &snapshot.CheckIntervalSec, &snapshot.TCPTimeoutMS,
		&snapshot.FailureThreshold, &snapshot.SuccessThreshold,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return snapshot, err
		}
		return snapshot, fmt.Errorf("load allowed client entry probe target: %w", err)
	}
	snapshot.AutoSplitEnabled = autoSplitEnabled == 1
	return snapshot, nil
}

func validateClientEntryMonitorRunResult(ctx context.Context, tx *sql.Tx, runID, targetID, probeID, targetVersion int64) (int64, error) {
	if runID <= 0 {
		return 0, nil
	}
	var selectedRunID int64
	var status string
	var rawExpectedPairs []byte
	err := tx.QueryRowContext(ctx, `SELECT id, status, expected_pairs
FROM v2_client_entry_monitor_run WHERE id = $1 FOR UPDATE`, runID).Scan(&selectedRunID, &status, &rawExpectedPairs)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && status != "running") {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("validate client entry monitor run result: %w", err)
	}
	pairs, err := decodeClientEntryMonitorRunPairs(rawExpectedPairs)
	if err != nil {
		return 0, err
	}
	for _, pair := range pairs {
		if pair.TargetID == targetID && pair.ProbeID == probeID && pair.TargetVersion == targetVersion {
			return selectedRunID, nil
		}
	}
	return 0, nil
}

func formatClientEntryMonitorTransition(snapshot clientEntryProbeTargetSnapshot, transition string, _ DNSProbeResult, _ int64) string {
	status := "掉线"
	if transition == "recovered" {
		status = "恢复"
	}
	address := clientEntryMonitorAddressKey(snapshot.Host, snapshot.Port)
	if address == "" {
		// Keep a readable fallback for an invalid legacy target.  Valid policy
		// targets always use the canonical key above.
		address = fmt.Sprintf("%s:%d", strings.TrimSpace(snapshot.Host), snapshot.Port)
	}
	return fmt.Sprintf("用户入口%s：%s", status, address)
}

func sortedClientEntryRunIDs(values map[int64]struct{}) []int64 {
	result := make([]int64, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}
