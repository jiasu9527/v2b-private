package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const clientEntryProbeTargetOffset int64 = 1 << 62

type clientEntryProbeTargetSnapshot struct {
	TargetID      int64
	TargetVersion int64
	MonitorID     int64
	PolicyID      int64
	PolicyName    string
	TargetName    string
	Host          string
	Port          int64
	ProbeName     string
}

func isClientEntryProbeTargetID(targetID int64) bool {
	return targetID >= clientEntryProbeTargetOffset
}

func encodeClientEntryProbeTargetID(targetID int64) int64 {
	if targetID <= 0 || targetID >= clientEntryProbeTargetOffset {
		return 0
	}
	return clientEntryProbeTargetOffset + targetID
}

func decodeClientEntryProbeTargetID(targetID int64) (int64, bool) {
	if !isClientEntryProbeTargetID(targetID) {
		return 0, false
	}
	decoded := targetID - clientEntryProbeTargetOffset
	return decoded, decoded > 0
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
		var previousSuccess sql.NullInt64
		var successStreak, failureStreak int64
		err = tx.QueryRowContext(ctx, `SELECT last_success, consecutive_success, consecutive_failure
FROM v2_client_entry_monitor_state
WHERE target_id = $1 AND probe_id = $2
FOR UPDATE`, targetID, probeID).Scan(&previousSuccess, &successStreak, &failureStreak)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return summary, fmt.Errorf("load client entry monitor state: %w", err)
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
			targetID, probeID, boolToInt64(success), latency, errorText, result.ResolvedIP,
			successStreak, failureStreak, now)
		if err != nil {
			return summary, fmt.Errorf("save client entry monitor state: %w", err)
		}
		transition := ""
		if !success && (!previousSuccess.Valid || previousSuccess.Int64 == 1) {
			transition = "down"
		} else if success && previousSuccess.Valid && previousSuccess.Int64 == 0 {
			transition = "recovered"
		}
		if transition != "" {
			details, _ := json.Marshal(map[string]any{
				"policy_id": snapshot.PolicyID, "policy_name": snapshot.PolicyName,
				"target_id": targetID, "target_name": snapshot.TargetName,
				"host": snapshot.Host, "port": snapshot.Port,
				"probe_id": probeID, "probe_name": snapshot.ProbeName,
				"success": success, "latency_ms": result.LatencyMS,
				"error": errorText, "resolved_ip": result.ResolvedIP,
			})
			message := formatClientEntryMonitorTransition(snapshot, transition, result, now)
			if _, err := tx.ExecContext(ctx, `INSERT INTO v2_client_entry_monitor_event
(monitor_id, target_id, probe_id, event_type, message, details, notified_at,
notify_attempts, notify_next_attempt_at, last_notify_error, created_at)
VALUES ($1, $2, $3, $4, $5, $6, NULL, 0, $7, '', $7)`, snapshot.MonitorID, targetID, probeID, transition, message, string(details), now); err != nil {
				return summary, fmt.Errorf("create client entry monitor event: %w", err)
			}
			hasEvents = true
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

func loadClientEntryProbeTargetSnapshot(ctx context.Context, tx *sql.Tx, probeID, targetID int64) (clientEntryProbeTargetSnapshot, error) {
	var snapshot clientEntryProbeTargetSnapshot
	err := tx.QueryRowContext(ctx, `SELECT target.id, target.generation, monitor.id, monitor.policy_id,
policy.name, target.name, target.host, target.port, probe.name
FROM v2_client_entry_monitor_target target
JOIN v2_client_entry_monitor monitor ON monitor.id = target.monitor_id AND monitor.enabled = 1
JOIN v2_client_entry_user_policy policy ON policy.id = monitor.policy_id AND policy.enabled = 1
JOIN v2_dns_probe probe ON probe.id = $1 AND probe.enabled = 1
WHERE target.id = $2
FOR SHARE OF target, monitor, policy, probe`, probeID, targetID).Scan(
		&snapshot.TargetID, &snapshot.TargetVersion, &snapshot.MonitorID, &snapshot.PolicyID, &snapshot.PolicyName,
		&snapshot.TargetName, &snapshot.Host, &snapshot.Port, &snapshot.ProbeName,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return snapshot, err
		}
		return snapshot, fmt.Errorf("load allowed client entry probe target: %w", err)
	}
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

func formatClientEntryMonitorTransition(snapshot clientEntryProbeTargetSnapshot, transition string, result DNSProbeResult, now int64) string {
	status := "掉线"
	detail := strings.TrimSpace(result.Error)
	if transition == "recovered" {
		status = "恢复"
		if result.LatencyMS != nil {
			detail = fmt.Sprintf("延迟 %d ms", *result.LatencyMS)
		}
	}
	if detail == "" {
		detail = "无详情"
	}
	return fmt.Sprintf("用户入口%s\n规则：%s\n地址：%s:%d\n探针：%s\n详情：%s\n时间：%s",
		status, snapshot.PolicyName, snapshot.Host, snapshot.Port, snapshot.ProbeName,
		detail, time.Unix(now, 0).Format("2006-01-02 15:04:05"))
}

func sortedClientEntryRunIDs(values map[int64]struct{}) []int64 {
	result := make([]int64, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}
