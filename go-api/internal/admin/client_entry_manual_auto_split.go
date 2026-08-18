package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	// ErrClientEntryManualAutoSplitUnavailable is returned when an option shown
	// by Telegram has recovered or changed before the confirmation callback.
	ErrClientEntryManualAutoSplitUnavailable = errors.New("所选入口当前不是可手动二分的故障固定名单叶子，请刷新后重试")
	ErrClientEntryManualAutoSplitForbidden   = errors.New("无权执行用户入口手动二分")
)

// ListClientEntryManualAutoSplitOptions returns only fixed-list leaves whose
// current target is confirmed down by every online probe. A historical down
// event is not sufficient: every result must still be fresh and have at least
// the configured consecutive-failure threshold. Existing pending operations
// are omitted so Telegram cannot enqueue the same incident repeatedly.
func (s *DBService) ListClientEntryManualAutoSplitOptions(ctx context.Context) ([]ClientEntryManualAutoSplitOption, error) {
	if s == nil || s.db == nil {
		return nil, ErrUnavailable
	}
	if err := s.ensureDNSFailoverSchema(ctx); err != nil {
		return nil, err
	}
	if err := s.refreshClientEntryMonitorTargetsIfDue(ctx); err != nil {
		return nil, fmt.Errorf("refresh manual client entry split targets: %w", err)
	}
	now := time.Now().Unix()
	onlineCutoff := saturatingUnixAdd(now, -time.Duration(defaultProbeOfflineSec)*time.Second)
	rows, err := s.db.QueryContext(ctx, `WITH online_probe AS (
	SELECT probe.id
	FROM v2_dns_probe probe
	WHERE probe.enabled = 1
	  AND probe.last_heartbeat_at IS NOT NULL
	  AND probe.last_heartbeat_at BETWEEN $1 AND $2
)
SELECT target.id, policy.id, split_group.id, policy.name, split_group.name,
       target.host, target.port, members.user_count,
       COUNT(online_probe.id) AS online_probe_count,
       COUNT(state.probe_id) FILTER (
		WHERE state.last_success = 0
		  AND state.consecutive_failure >= $3
		  AND state.last_reported_at IS NOT NULL
		  AND state.last_reported_at BETWEEN
		      $2 - 2 * (monitor.check_interval_sec::BIGINT + ((monitor.tcp_timeout_ms::BIGINT + 999) / 1000))
		      AND $2
	   ) AS failed_probe_count,
       target.auto_split_enabled
FROM v2_client_entry_monitor_target target
JOIN v2_client_entry_monitor monitor ON monitor.id = target.monitor_id AND monitor.enabled = 1
JOIN v2_client_entry_user_policy policy ON policy.id = monitor.policy_id
	AND policy.enabled = 1 AND policy.mode = 'split'
JOIN v2_client_entry_user_policy_split_group split_group
	ON split_group.policy_id = policy.id
	AND target.source_key = 'policy:' || policy.id::TEXT || ':split-group:' || split_group.id::TEXT
	AND LOWER(BTRIM(target.host)) = LOWER(BTRIM(split_group.entry_host))
JOIN LATERAL (
	SELECT COUNT(*)::BIGINT AS user_count
	FROM v2_client_entry_user_policy_split_assignment assignment
	WHERE assignment.policy_id = policy.id AND assignment.group_id = split_group.id
) members ON members.user_count >= 2
CROSS JOIN online_probe
LEFT JOIN v2_client_entry_monitor_state state
	ON state.target_id = target.id AND state.probe_id = online_probe.id
WHERE BTRIM(split_group.entry_host) <> ''
  AND split_group.global_sort IS NOT NULL AND split_group.global_sort > 0
  AND NOT EXISTS (
	SELECT 1 FROM v2_client_entry_user_policy_split_group child
	WHERE child.parent_id = split_group.id
  )
  AND NOT EXISTS (
	SELECT 1 FROM v2_client_entry_auto_split_operation operation
	WHERE operation.policy_id = policy.id AND operation.source_group_id = split_group.id
	  AND operation.status = 'pending'
  )
GROUP BY target.id, policy.id, split_group.id, policy.name, split_group.name,
         target.host, target.port, members.user_count, target.auto_split_enabled,
         policy.sort, split_group.global_sort, monitor.check_interval_sec, monitor.tcp_timeout_ms
HAVING COUNT(online_probe.id) > 0
   AND COUNT(state.probe_id) FILTER (
		WHERE state.last_success = 0
		  AND state.consecutive_failure >= $3
		  AND state.last_reported_at IS NOT NULL
		  AND state.last_reported_at BETWEEN
		      $2 - 2 * (monitor.check_interval_sec::BIGINT + ((monitor.tcp_timeout_ms::BIGINT + 999) / 1000))
		      AND $2
	   ) = COUNT(online_probe.id)
ORDER BY split_group.global_sort ASC, policy.sort ASC NULLS LAST, policy.id ASC, split_group.id ASC`,
		onlineCutoff, now, clientEntryMonitorFailureThreshold)
	if err != nil {
		return nil, fmt.Errorf("query manual client entry split options: %w", err)
	}
	defer rows.Close()
	options := make([]ClientEntryManualAutoSplitOption, 0)
	for rows.Next() {
		var option ClientEntryManualAutoSplitOption
		var autoSplitEnabled int64
		if err := rows.Scan(&option.TargetID, &option.PolicyID, &option.GroupID,
			&option.PolicyName, &option.GroupName, &option.Host, &option.Port, &option.UserCount,
			&option.OnlineProbeCount, &option.FailedProbeCount, &autoSplitEnabled); err != nil {
			return nil, fmt.Errorf("scan manual client entry split option: %w", err)
		}
		option.PolicyName = strings.TrimSpace(option.PolicyName)
		option.GroupName = strings.TrimSpace(option.GroupName)
		option.Host = strings.TrimSpace(option.Host)
		option.AutoSplitEnabled = autoSplitEnabled == 1
		options = append(options, option)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate manual client entry split options: %w", err)
	}
	return options, nil
}

// RequestClientEntryManualAutoSplit transactionally revalidates the selected
// target and records a durable operation. It deliberately does not require the
// leaf's auto_split_enabled flag: the explicit operator confirmation is the
// opt-in, while all probe-health and fixed-list safety checks remain mandatory.
func (s *DBService) RequestClientEntryManualAutoSplit(ctx context.Context, targetID, requestedByUserID int64) (int64, error) {
	if s == nil || s.db == nil {
		return 0, ErrUnavailable
	}
	if targetID <= 0 {
		return 0, ErrClientEntryManualAutoSplitUnavailable
	}
	if requestedByUserID <= 0 {
		return 0, ErrClientEntryManualAutoSplitForbidden
	}
	if err := s.ensureDNSFailoverSchema(ctx); err != nil {
		return 0, err
	}
	now := time.Now().Unix()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin manual client entry split: %w", err)
	}
	defer tx.Rollback()

	var operatorID int64
	if err := tx.QueryRowContext(ctx, `SELECT id
FROM v2_user
WHERE id = $1 AND banned = 0 AND (is_admin = 1 OR is_staff = 1)
FOR SHARE`, requestedByUserID).Scan(&operatorID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrClientEntryManualAutoSplitForbidden
		}
		return 0, fmt.Errorf("authorize manual client entry split: %w", err)
	}

	// A Telegram webhook may be delivered again after the database commit but
	// before editMessageText succeeds. Return the durable pending operation as
	// an idempotent success instead of revalidating a now-hidden picker option.
	// This is deliberately an unlocked read: the worker's canonical order is
	// target -> operation, so taking an operation lock here would deadlock with
	// the target lock acquired below.
	var existingOperationID int64
	err = tx.QueryRowContext(ctx, `SELECT id
FROM v2_client_entry_auto_split_operation
WHERE target_id = $1 AND status = 'pending'
ORDER BY id ASC
LIMIT 1`, targetID).Scan(&existingOperationID)
	if err == nil {
		if err := tx.Commit(); err != nil {
			return 0, fmt.Errorf("commit existing manual client entry split: %w", err)
		}
		s.wakeClientEntryMonitorEvaluation(ctx)
		return existingOperationID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("lookup pending manual client entry split: %w", err)
	}

	// Peek without a row lock so we can preserve the canonical lock order used
	// by probe ingestion and the automatic worker: probe -> monitor -> target ->
	// operation. The locked target query below revalidates this relationship.
	var monitorID int64
	if err := tx.QueryRowContext(ctx, `SELECT monitor_id
FROM v2_client_entry_monitor_target WHERE id = $1`, targetID).Scan(&monitorID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrClientEntryManualAutoSplitUnavailable
		}
		return 0, fmt.Errorf("locate manual client entry split target: %w", err)
	}
	probeIDs, err := lockOnlineClientEntryAutoSplitProbes(ctx, tx, now)
	if err != nil {
		return 0, err
	}
	if len(probeIDs) == 0 {
		return 0, ErrClientEntryManualAutoSplitUnavailable
	}

	var lockedMonitorID int64
	if err := tx.QueryRowContext(ctx, `SELECT id
FROM v2_client_entry_monitor
WHERE id = $1
FOR KEY SHARE`, monitorID).Scan(&lockedMonitorID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrClientEntryManualAutoSplitUnavailable
		}
		return 0, fmt.Errorf("lock monitor for manual client entry split: %w", err)
	}

	var sourceKey, targetHost string
	var generation, monitorPolicyID, monitorEnabled, checkIntervalSec, tcpTimeoutMS int64
	if err := tx.QueryRowContext(ctx, `SELECT target.source_key, target.host, target.generation,
monitor.policy_id, monitor.enabled, monitor.check_interval_sec, monitor.tcp_timeout_ms
FROM v2_client_entry_monitor_target target
JOIN v2_client_entry_monitor monitor ON monitor.id = target.monitor_id
WHERE target.id = $1 AND target.monitor_id = $2
FOR UPDATE OF target`, targetID, monitorID).Scan(&sourceKey, &targetHost, &generation,
		&monitorPolicyID, &monitorEnabled, &checkIntervalSec, &tcpTimeoutMS); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrClientEntryManualAutoSplitUnavailable
		}
		return 0, fmt.Errorf("lock manual client entry split target: %w", err)
	}
	if monitorEnabled != 1 || generation <= 0 {
		return 0, ErrClientEntryManualAutoSplitUnavailable
	}
	policyID, groupID, ok := parseClientEntryMonitorSplitGroupSourceKey(sourceKey)
	if !ok || policyID != monitorPolicyID {
		return 0, ErrClientEntryManualAutoSplitUnavailable
	}

	var policyName, groupName, entryHost string
	var globalSort sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT policy.name, split_group.name,
split_group.entry_host, split_group.global_sort
FROM v2_client_entry_user_policy_split_group split_group
JOIN v2_client_entry_user_policy policy ON policy.id = split_group.policy_id
WHERE split_group.id = $1 AND split_group.policy_id = $2
  AND policy.mode = 'split' AND policy.enabled = 1
  AND NOT EXISTS (
	SELECT 1 FROM v2_client_entry_user_policy_split_group child
	WHERE child.parent_id = split_group.id
  )
FOR KEY SHARE OF split_group, policy`, groupID, policyID).Scan(
		&policyName, &groupName, &entryHost, &globalSort); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrClientEntryManualAutoSplitUnavailable
		}
		return 0, fmt.Errorf("validate manual client entry split leaf: %w", err)
	}
	if !globalSort.Valid || globalSort.Int64 <= 0 || strings.TrimSpace(entryHost) == "" ||
		!strings.EqualFold(strings.TrimSpace(entryHost), strings.TrimSpace(targetHost)) {
		return 0, ErrClientEntryManualAutoSplitUnavailable
	}
	userCount, err := lockClientEntryAutoSplitAssignments(ctx, tx, policyID, groupID)
	if err != nil {
		return 0, err
	}
	if userCount < 2 {
		return 0, ErrClientEntryManualAutoSplitUnavailable
	}

	states, err := loadClientEntryAutoSplitStatesForProbes(ctx, tx, targetID, probeIDs)
	if err != nil {
		return 0, err
	}
	for _, state := range states {
		if !state.LastSuccess.Valid || state.LastSuccess.Int64 != 0 ||
			state.ConsecutiveFailure < clientEntryMonitorFailureThreshold ||
			!clientEntryMonitorStateFresh(state.LastReportedAt, now, checkIntervalSec, tcpTimeoutMS) {
			return 0, ErrClientEntryManualAutoSplitUnavailable
		}
	}

	var operationID int64
	err = tx.QueryRowContext(ctx, `INSERT INTO v2_client_entry_auto_split_operation AS current_operation
(monitor_id, target_id, target_generation, policy_id, source_group_id, source_host,
 trigger_probe_id, trigger_result_inbox_id, status, attempts, next_attempt_at,
 last_error, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, NULL, NULL, 'pending', 0, $7, '', $7, $7)
ON CONFLICT (policy_id, source_group_id) WHERE status = 'pending' DO UPDATE SET
updated_at = current_operation.updated_at
RETURNING current_operation.id`, monitorID, targetID, generation, policyID, groupID, strings.TrimSpace(targetHost), now).Scan(&operationID)
	if err != nil {
		return 0, fmt.Errorf("enqueue manual client entry split: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit manual client entry split: %w", err)
	}
	s.wakeClientEntryMonitorEvaluation(ctx)
	return operationID, nil
}

func lockOnlineClientEntryAutoSplitProbes(ctx context.Context, tx *sql.Tx, now int64) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT probe.id
FROM v2_dns_probe probe
WHERE probe.enabled = 1
  AND probe.last_heartbeat_at IS NOT NULL
  AND probe.last_heartbeat_at BETWEEN $1 AND $2
ORDER BY probe.id
FOR SHARE OF probe`, saturatingUnixAdd(now, -time.Duration(defaultProbeOfflineSec)*time.Second), now)
	if err != nil {
		return nil, fmt.Errorf("load online probes for client entry split: %w", err)
	}
	defer rows.Close()
	probeIDs := make([]int64, 0)
	for rows.Next() {
		var probeID int64
		if err := rows.Scan(&probeID); err != nil {
			return nil, fmt.Errorf("scan online probe for client entry split: %w", err)
		}
		probeIDs = append(probeIDs, probeID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate online probes for client entry split: %w", err)
	}
	return probeIDs, nil
}

func loadClientEntryAutoSplitStatesForProbes(ctx context.Context, tx *sql.Tx, targetID int64, probeIDs []int64) ([]clientEntryAutoSplitSourceState, error) {
	if len(probeIDs) == 0 {
		return []clientEntryAutoSplitSourceState{}, nil
	}
	placeholders := make([]string, len(probeIDs))
	args := make([]any, 0, len(probeIDs)+1)
	args = append(args, targetID)
	for index, probeID := range probeIDs {
		placeholders[index] = fmt.Sprintf("$%d", index+2)
		args = append(args, probeID)
	}
	rows, err := tx.QueryContext(ctx, `SELECT probe_id, last_success,
consecutive_failure, last_reported_at
FROM v2_client_entry_monitor_state
WHERE target_id = $1 AND probe_id IN (`+strings.Join(placeholders, ",")+`)
ORDER BY probe_id`, args...)
	if err != nil {
		return nil, fmt.Errorf("load client entry split source states: %w", err)
	}
	defer rows.Close()
	stateByProbe := make(map[int64]clientEntryAutoSplitSourceState, len(probeIDs))
	for rows.Next() {
		var state clientEntryAutoSplitSourceState
		if err := rows.Scan(&state.ProbeID, &state.LastSuccess, &state.ConsecutiveFailure, &state.LastReportedAt); err != nil {
			return nil, fmt.Errorf("scan client entry split source state: %w", err)
		}
		stateByProbe[state.ProbeID] = state
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate client entry split source states: %w", err)
	}
	states := make([]clientEntryAutoSplitSourceState, 0, len(probeIDs))
	for _, probeID := range probeIDs {
		state, exists := stateByProbe[probeID]
		if !exists {
			state = clientEntryAutoSplitSourceState{ProbeID: probeID}
		}
		states = append(states, state)
	}
	return states, nil
}
