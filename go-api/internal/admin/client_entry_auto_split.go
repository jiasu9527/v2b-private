package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	clientEntryAutoSplitRetryDelay     = 15 * time.Second
	clientEntryAutoSplitQuarantineTime = 30 * time.Minute

	clientEntryAutoSplitBackupIPInsufficientReason = "可用备用 IP 不足 2 个"
	clientEntryAutoSplitBackupIPShortageEventType  = "auto_split_ip_shortage"
)

type clientEntryAutoSplitOperation struct {
	ID               int64
	MonitorID        int64
	TargetID         int64
	TargetGeneration int64
	PolicyID         int64
	SourceGroupID    int64
	SourceHost       string
	Attempts         int64
	CreatedAt        int64
	LastError        string
}

type clientEntryAutoSplitSourceState struct {
	ProbeID            int64
	LastSuccess        sql.NullInt64
	ConsecutiveFailure int64
	LastReportedAt     sql.NullInt64
}

// parseClientEntryMonitorSplitGroupSourceKey only accepts the canonical key
// emitted by resolveClientEntryMonitorPolicies.  Do not infer a group from a
// display name: names are editable and are not an authorization boundary.
func parseClientEntryMonitorSplitGroupSourceKey(sourceKey string) (policyID, groupID int64, ok bool) {
	parts := strings.Split(strings.TrimSpace(sourceKey), ":")
	if len(parts) != 4 || parts[0] != "policy" || parts[2] != "split-group" {
		return 0, 0, false
	}
	policyID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || policyID <= 0 {
		return 0, 0, false
	}
	groupID, err = strconv.ParseInt(parts[3], 10, 64)
	if err != nil || groupID <= 0 {
		return 0, 0, false
	}
	return policyID, groupID, true
}

// enqueueClientEntryAutoSplit records a durable, idempotent request in the
// same transaction as the confirmed-down state.  A partial unique index keeps
// at most one pending operation per leaf even when several probes report the
// second timeout at the same time.
func enqueueClientEntryAutoSplit(ctx context.Context, tx *sql.Tx, snapshot clientEntryProbeTargetSnapshot, probeID, resultInboxID, now int64) error {
	if tx == nil || !snapshot.AutoSplitEnabled {
		return nil
	}
	policyID, groupID, ok := parseClientEntryMonitorSplitGroupSourceKey(snapshot.SourceKey)
	if !ok || policyID != snapshot.PolicyID {
		return nil
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO v2_client_entry_auto_split_operation AS current_operation
(monitor_id, target_id, target_generation, policy_id, source_group_id, source_host,
 trigger_probe_id, trigger_result_inbox_id, status, attempts, next_attempt_at,
 last_error, created_at, updated_at)
SELECT $1, $2, $3, $4, $5, $6, $7, $8, 'pending', 0, $9, '', $9, $9
WHERE EXISTS (
	SELECT 1
	FROM v2_client_entry_user_policy_split_group split_group
	JOIN v2_client_entry_user_policy policy ON policy.id = split_group.policy_id
	WHERE split_group.id = $5 AND split_group.policy_id = $4
	  AND policy.mode = 'split' AND policy.enabled = 1
	  AND NOT EXISTS (
		SELECT 1 FROM v2_client_entry_user_policy_split_group child
		WHERE child.parent_id = split_group.id
	  )
)
ON CONFLICT (policy_id, source_group_id) WHERE status = 'pending' DO UPDATE SET
monitor_id = EXCLUDED.monitor_id,
target_id = EXCLUDED.target_id,
target_generation = EXCLUDED.target_generation,
source_host = EXCLUDED.source_host,
trigger_probe_id = EXCLUDED.trigger_probe_id,
trigger_result_inbox_id = EXCLUDED.trigger_result_inbox_id,
attempts = 0,
next_attempt_at = EXCLUDED.next_attempt_at,
last_error = '',
backup_ip_a_id = NULL,
backup_ip_b_id = NULL,
child_group_a_id = NULL,
child_group_b_id = NULL,
completed_at = NULL,
created_at = EXCLUDED.created_at,
updated_at = EXCLUDED.updated_at
WHERE current_operation.monitor_id IS DISTINCT FROM EXCLUDED.monitor_id
   OR current_operation.target_id IS DISTINCT FROM EXCLUDED.target_id
   OR current_operation.target_generation IS DISTINCT FROM EXCLUDED.target_generation
   OR current_operation.source_host IS DISTINCT FROM EXCLUDED.source_host`, snapshot.MonitorID, snapshot.TargetID, snapshot.TargetVersion,
		policyID, groupID, snapshot.Host, probeID, resultInboxID, now)
	if err != nil {
		return fmt.Errorf("enqueue client entry automatic split: %w", err)
	}
	return nil
}

// cancelClientEntryAutoSplitOnSuccess closes an incident as soon as any probe
// produces a successful raw sample.  Automatic splitting requires every
// currently-online probe to be confirmed down; an operation from an older
// partial outage must never fire later against an unrelated outage.
func cancelClientEntryAutoSplitOnSuccess(ctx context.Context, tx *sql.Tx, snapshot clientEntryProbeTargetSnapshot, now int64) error {
	if tx == nil || !snapshot.AutoSplitEnabled {
		return nil
	}
	_, err := tx.ExecContext(ctx, `UPDATE v2_client_entry_auto_split_operation
SET status = 'cancelled', last_error = '入口已有探针恢复，取消自动二分',
    completed_at = $3, updated_at = $3
WHERE target_id = $1 AND target_generation = $2 AND status = 'pending'`,
		snapshot.TargetID, snapshot.TargetVersion, now)
	if err != nil {
		return fmt.Errorf("cancel recovered client entry automatic split: %w", err)
	}
	return nil
}

func (s *DBService) drainPendingClientEntryAutoSplits(ctx context.Context, limit int) error {
	if s == nil || s.db == nil || limit <= 0 {
		return nil
	}
	for range limit {
		processed, changed, err := s.processNextClientEntryAutoSplit(ctx)
		if err != nil {
			return err
		}
		if changed {
			s.markClientEntryMonitorTargetsDirty()
		}
		if !processed {
			return nil
		}
	}
	return nil
}

// processNextClientEntryAutoSplit performs the health revalidation, reserves
// two backup IP rows and moves the fixed assignments in one database
// transaction.  FOR UPDATE SKIP LOCKED on both the operation and backup rows
// makes concurrent workers safe without a process-local mutex.
func (s *DBService) processNextClientEntryAutoSplit(ctx context.Context) (processed, changed bool, err error) {
	now := time.Now().Unix()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, false, fmt.Errorf("begin client entry automatic split: %w", err)
	}
	defer tx.Rollback()

	// Do not lock the operation row first. Probe result ingestion takes locks in
	// probe -> target -> operation order; reversing that order here would let a
	// recovery report and this worker deadlock each other. Peek first, lock and
	// revalidate the probe/target snapshot, then claim the operation last.
	operation, err := peekNextClientEntryAutoSplitOperation(ctx, tx, now)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}

	checkIntervalSec, tcpTimeoutMS, states, sourceErr := loadClientEntryAutoSplitSourceStates(ctx, tx, operation, now)
	if sourceErr != nil && !errors.Is(sourceErr, sql.ErrNoRows) {
		return true, false, sourceErr
	}
	operation, err = claimClientEntryAutoSplitOperation(ctx, tx, operation, now)
	if errors.Is(err, sql.ErrNoRows) {
		// Another worker claimed or completed the row while this transaction was
		// waiting for the canonical probe/target locks. Roll back the read locks
		// and let the next cycle select another due operation.
		return false, false, nil
	}
	if err != nil {
		return true, false, err
	}
	if errors.Is(sourceErr, sql.ErrNoRows) {
		if err := finishClientEntryAutoSplitOperation(ctx, tx, operation.ID, "cancelled", "入口检测目标已变化", now, 0, 0, 0, 0); err != nil {
			return true, false, err
		}
		return commitClientEntryAutoSplit(ctx, tx, true, false)
	}
	if len(states) == 0 {
		if err := retryClientEntryAutoSplitOperation(ctx, tx, operation.ID, "暂无在线探针，等待重试", now); err != nil {
			return true, false, err
		}
		return commitClientEntryAutoSplit(ctx, tx, true, false)
	}
	for _, state := range states {
		if state.LastSuccess.Valid && state.LastSuccess.Int64 == 0 &&
			state.ConsecutiveFailure >= clientEntryMonitorFailureThreshold &&
			clientEntryMonitorStateFresh(state.LastReportedAt, now, checkIntervalSec, tcpTimeoutMS) {
			continue
		}
		if err := retryClientEntryAutoSplitOperation(ctx, tx, operation.ID,
			"尚未满足所有在线探针连续两次失败", now); err != nil {
			return true, false, err
		}
		return commitClientEntryAutoSplit(ctx, tx, true, false)
	}

	if err := lockClientEntryVisibleOrder(ctx, tx); err != nil {
		return true, false, fmt.Errorf("lock client entry order for automatic split: %w", err)
	}
	var policyName, parentName, parentPath, currentHost string
	var parentGlobalSort sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT policy.name, split_group.name, split_group.path,
split_group.entry_host, split_group.global_sort
FROM v2_client_entry_user_policy_split_group split_group
JOIN v2_client_entry_user_policy policy ON policy.id = split_group.policy_id
WHERE split_group.id = $1 AND split_group.policy_id = $2
  AND policy.mode = 'split' AND policy.enabled = 1
  AND NOT EXISTS (
	SELECT 1 FROM v2_client_entry_user_policy_split_group child WHERE child.parent_id = split_group.id
  )
FOR UPDATE OF split_group, policy`, operation.SourceGroupID, operation.PolicyID).Scan(
		&policyName, &parentName, &parentPath, &currentHost, &parentGlobalSort,
	)
	if errors.Is(err, sql.ErrNoRows) {
		if err := finishClientEntryAutoSplitOperation(ctx, tx, operation.ID, "cancelled", "固定二分叶子已变化", now, 0, 0, 0, 0); err != nil {
			return true, false, err
		}
		return commitClientEntryAutoSplit(ctx, tx, true, false)
	}
	if err != nil {
		return true, false, fmt.Errorf("lock automatic split leaf: %w", err)
	}
	if strings.TrimSpace(currentHost) != strings.TrimSpace(operation.SourceHost) || !parentGlobalSort.Valid || parentGlobalSort.Int64 <= 0 {
		if err := finishClientEntryAutoSplitOperation(ctx, tx, operation.ID, "cancelled", "叶子入口或排序已变化", now, 0, 0, 0, 0); err != nil {
			return true, false, err
		}
		return commitClientEntryAutoSplit(ctx, tx, true, false)
	}

	userCount, err := lockClientEntryAutoSplitAssignments(ctx, tx, operation.PolicyID, operation.SourceGroupID)
	if err != nil {
		return true, false, err
	}
	if userCount < 2 {
		if err := finishClientEntryAutoSplitOperation(ctx, tx, operation.ID, "cancelled", "固定名单用户不足 2 人", now, 0, 0, 0, 0); err != nil {
			return true, false, err
		}
		return commitClientEntryAutoSplit(ctx, tx, true, false)
	}

	backupIPs, err := claimHealthyClientEntryBackupIPs(ctx, tx, 2, now)
	if errors.Is(err, ErrClientEntryBackupIPInsufficient) {
		// last_error is updated in the same transaction as this event.  Keeping the
		// reason on the operation gives us incident-style de-duplication without a
		// second table or schema field: repeated 15-second retries stay quiet, while
		// a later transition from another wait reason can notify again.
		if operation.LastError != clientEntryAutoSplitBackupIPInsufficientReason {
			if eventErr := createClientEntryAutoSplitBackupIPShortageEvent(ctx, tx, operation, policyName, now); eventErr != nil {
				return true, false, eventErr
			}
		}
		if retryErr := retryClientEntryAutoSplitOperation(ctx, tx, operation.ID, clientEntryAutoSplitBackupIPInsufficientReason, now); retryErr != nil {
			return true, false, retryErr
		}
		return commitClientEntryAutoSplit(ctx, tx, true, false)
	}
	if err != nil {
		return true, false, fmt.Errorf("claim backup IPs for automatic split: %w", err)
	}
	if len(backupIPs) != 2 {
		return true, false, fmt.Errorf("backup IP claim returned %d addresses, want 2", len(backupIPs))
	}
	if backupIPs[0].ID <= 0 || backupIPs[1].ID <= 0 || backupIPs[0].ID == backupIPs[1].ID ||
		strings.EqualFold(strings.TrimSpace(backupIPs[0].IP), strings.TrimSpace(backupIPs[1].IP)) {
		return true, false, errors.New("backup IP claim returned duplicate addresses")
	}

	parentPath = strings.TrimSpace(parentPath)
	if parentPath == "" {
		parentPath = strings.TrimSpace(parentName)
	}
	pathA, pathB := parentPath+".1", parentPath+".2"
	if len([]rune(pathA)) > 255 || len([]rune(pathB)) > 255 {
		if err := finishClientEntryAutoSplitOperation(ctx, tx, operation.ID, "cancelled", "二分层级过深", now, 0, 0, 0, 0); err != nil {
			return true, false, err
		}
		return commitClientEntryAutoSplit(ctx, tx, true, false)
	}
	if err := shiftClientEntryVisibleSortsAfter(ctx, tx, parentGlobalSort.Int64, clientEntryRuleSortStep); err != nil {
		return true, false, fmt.Errorf("shift client entry order for automatic split: %w", err)
	}
	globalSortA, globalSortB := parentGlobalSort.Int64, parentGlobalSort.Int64+clientEntryRuleSortStep
	parentID := operation.SourceGroupID
	nameA, nameB := automaticClientEntrySplitChildNames(parentName, pathA, pathB)
	groupA, err := insertClientEntryUserPolicySplitGroup(ctx, tx, operation.PolicyID, &parentID,
		nameA, pathA, backupIPs[0].IP, clientEntryRuleSortStep, globalSortA, now)
	if err != nil {
		return true, false, fmt.Errorf("create automatic split group A: %w", err)
	}
	groupB, err := insertClientEntryUserPolicySplitGroup(ctx, tx, operation.PolicyID, &parentID,
		nameB, pathB, backupIPs[1].IP, 2*clientEntryRuleSortStep, globalSortB, now)
	if err != nil {
		return true, false, fmt.Errorf("create automatic split group B: %w", err)
	}
	half := (userCount + 1) / 2
	result, err := tx.ExecContext(ctx, `WITH ranked AS (
	SELECT user_id, ROW_NUMBER() OVER (ORDER BY user_id ASC) AS position
	FROM v2_client_entry_user_policy_split_assignment
	WHERE policy_id = $1 AND group_id = $2
)
UPDATE v2_client_entry_user_policy_split_assignment assignment
SET group_id = CASE WHEN ranked.position <= $3 THEN $4::BIGINT ELSE $5::BIGINT END,
    updated_at = $6
FROM ranked
WHERE assignment.policy_id = $1 AND assignment.user_id = ranked.user_id`,
		operation.PolicyID, operation.SourceGroupID, half, groupA, groupB, now)
	if err != nil {
		return true, false, fmt.Errorf("move automatic split assignments: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != userCount {
		return true, false, errors.New("automatic split assignments changed concurrently")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE v2_client_entry_user_policy_split_group
SET entry_host = '', global_sort = NULL, updated_at = $2 WHERE id = $1`, operation.SourceGroupID, now); err != nil {
		return true, false, fmt.Errorf("retire automatic split parent: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE v2_client_entry_user_policy
SET updated_at = $2 WHERE id = $1 AND mode = 'split'`, operation.PolicyID, now); err != nil {
		return true, false, fmt.Errorf("touch automatic split policy: %w", err)
	}

	// Seed the two new monitor targets before the normal refresh.  Refresh keeps
	// the configured port and flag on conflict, so recursively-created leaves
	// with at least two users remain protected and use each pool address' own
	// TCP port. A one-user leaf cannot be divided further and is disabled.
	childTargets := []struct {
		groupID          int64
		name             string
		host             string
		port             int64
		sort             int64
		autoSplitEnabled bool
	}{
		{groupA, nameA, backupIPs[0].IP, backupIPs[0].Port, clientEntryRuleSortStep, half >= 2},
		{groupB, nameB, backupIPs[1].IP, backupIPs[1].Port, 2 * clientEntryRuleSortStep, userCount-half >= 2},
	}
	for _, child := range childTargets {
		sourceKey := fmt.Sprintf("policy:%d:split-group:%d", operation.PolicyID, child.groupID)
		if _, err := tx.ExecContext(ctx, `INSERT INTO v2_client_entry_monitor_target
(monitor_id, source_key, name, host, port, sort, auto_split_enabled, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8)
ON CONFLICT (monitor_id, source_key) DO UPDATE SET
name = EXCLUDED.name, host = EXCLUDED.host, port = EXCLUDED.port,
sort = EXCLUDED.sort, auto_split_enabled = EXCLUDED.auto_split_enabled, updated_at = EXCLUDED.updated_at`,
			operation.MonitorID, sourceKey, child.name, child.host, child.port, child.sort,
			boolToInt64(child.autoSplitEnabled), now); err != nil {
			return true, false, fmt.Errorf("seed automatic split monitor target: %w", err)
		}
	}

	quarantineUntil := saturatingUnixAdd(now, clientEntryAutoSplitQuarantineTime)
	if err := quarantineClientEntryBackupIPByHost(ctx, tx, operation.SourceHost, quarantineUntil, now); err != nil {
		return true, false, err
	}
	if err := finishClientEntryAutoSplitOperation(ctx, tx, operation.ID, "succeeded", "", now,
		backupIPs[0].ID, backupIPs[1].ID, groupA, groupB); err != nil {
		return true, false, err
	}
	details, _ := json.Marshal(map[string]any{
		"operation_id": operation.ID, "policy_id": operation.PolicyID,
		"source_group_id": operation.SourceGroupID, "source_host": operation.SourceHost,
		"child_group_a_id": groupA, "child_group_b_id": groupB,
		"backup_ip_a_id": backupIPs[0].ID, "backup_ip_b_id": backupIPs[1].ID,
		"backup_ip_a": backupIPs[0].IP, "backup_ip_b": backupIPs[1].IP,
		"user_count": userCount,
	})
	message := fmt.Sprintf("用户入口已自动二分\n规则：%s\n故障入口：%s\n新入口：%s / %s\n用户：%d 人（%d / %d）",
		strings.TrimSpace(policyName), operation.SourceHost, backupIPs[0].IP, backupIPs[1].IP,
		userCount, half, userCount-half)
	if _, err := tx.ExecContext(ctx, `INSERT INTO v2_client_entry_monitor_event
(monitor_id, target_id, probe_id, event_type, message, details, notified_at,
 notify_attempts, notify_next_attempt_at, last_notify_error, created_at)
VALUES ($1, $2, NULL, 'auto_split', $3, $4, NULL, 0, $5, '', $5)`,
		operation.MonitorID, operation.TargetID, message, string(details), now); err != nil {
		return true, false, fmt.Errorf("create automatic split event: %w", err)
	}
	// The database is shared by every API instance, while the in-memory dirty
	// flag is process-local. Remove the retired target in the same transaction
	// so another instance cannot keep handing probes the failed parent for up to
	// one refresh interval. Historical events retain their message/details and
	// intentionally use ON DELETE SET NULL for target_id.
	if _, err := tx.ExecContext(ctx, `DELETE FROM v2_client_entry_monitor_target
WHERE id = $1 AND monitor_id = $2`, operation.TargetID, operation.MonitorID); err != nil {
		return true, false, fmt.Errorf("remove retired automatic split target: %w", err)
	}
	return commitClientEntryAutoSplit(ctx, tx, true, true)
}

func peekNextClientEntryAutoSplitOperation(ctx context.Context, tx *sql.Tx, now int64) (clientEntryAutoSplitOperation, error) {
	var operation clientEntryAutoSplitOperation
	err := tx.QueryRowContext(ctx, `SELECT id, monitor_id, target_id, target_generation,
policy_id, source_group_id, source_host, attempts, created_at, last_error
FROM v2_client_entry_auto_split_operation
WHERE status = 'pending' AND next_attempt_at <= $1
ORDER BY id ASC
LIMIT 1`, now).Scan(&operation.ID, &operation.MonitorID, &operation.TargetID,
		&operation.TargetGeneration, &operation.PolicyID, &operation.SourceGroupID, &operation.SourceHost,
		&operation.Attempts, &operation.CreatedAt, &operation.LastError)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return operation, err
		}
		return operation, fmt.Errorf("peek client entry automatic split: %w", err)
	}
	return operation, nil
}

func claimClientEntryAutoSplitOperation(ctx context.Context, tx *sql.Tx, expected clientEntryAutoSplitOperation, now int64) (clientEntryAutoSplitOperation, error) {
	var operation clientEntryAutoSplitOperation
	err := tx.QueryRowContext(ctx, `SELECT id, monitor_id, target_id, target_generation,
policy_id, source_group_id, source_host, attempts, created_at, last_error
FROM v2_client_entry_auto_split_operation
WHERE id = $1 AND status = 'pending' AND next_attempt_at <= $2
FOR UPDATE SKIP LOCKED`, expected.ID, now).Scan(&operation.ID, &operation.MonitorID, &operation.TargetID,
		&operation.TargetGeneration, &operation.PolicyID, &operation.SourceGroupID, &operation.SourceHost,
		&operation.Attempts, &operation.CreatedAt, &operation.LastError)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return operation, err
		}
		return operation, fmt.Errorf("claim client entry automatic split: %w", err)
	}
	if operation.MonitorID != expected.MonitorID || operation.TargetID != expected.TargetID ||
		operation.TargetGeneration != expected.TargetGeneration || operation.PolicyID != expected.PolicyID ||
		operation.SourceGroupID != expected.SourceGroupID || operation.SourceHost != expected.SourceHost ||
		operation.CreatedAt != expected.CreatedAt {
		return operation, errors.New("client entry automatic split operation changed while claiming")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE v2_client_entry_auto_split_operation
SET attempts = attempts + 1, next_attempt_at = $2, updated_at = $1
WHERE id = $3 AND status = 'pending'`, now, saturatingUnixAdd(now, clientEntryAutoSplitRetryDelay), operation.ID); err != nil {
		return operation, fmt.Errorf("advance client entry automatic split attempt: %w", err)
	}
	return operation, nil
}

func loadClientEntryAutoSplitSourceStates(ctx context.Context, tx *sql.Tx, operation clientEntryAutoSplitOperation, now int64) (int64, int64, []clientEntryAutoSplitSourceState, error) {
	// Result reporting locks a probe before it takes a shared target lock.  Lock
	// all online probes first as well, otherwise a worker and a probe report can
	// deadlock in opposite probe/target order.
	rows, err := tx.QueryContext(ctx, `SELECT probe.id
FROM v2_dns_probe probe
WHERE probe.enabled = 1
  AND probe.last_heartbeat_at IS NOT NULL
  AND probe.last_heartbeat_at BETWEEN $1 AND $2
ORDER BY probe.id
FOR SHARE OF probe`, saturatingUnixAdd(now, -time.Duration(defaultProbeOfflineSec)*time.Second), now)
	if err != nil {
		return 0, 0, nil, fmt.Errorf("load online probes for automatic split: %w", err)
	}
	probeIDs := make([]int64, 0)
	for rows.Next() {
		var probeID int64
		if err := rows.Scan(&probeID); err != nil {
			_ = rows.Close()
			return 0, 0, nil, fmt.Errorf("scan online probe for automatic split: %w", err)
		}
		probeIDs = append(probeIDs, probeID)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, 0, nil, fmt.Errorf("iterate online probes for automatic split: %w", err)
	}
	if err := rows.Close(); err != nil {
		return 0, 0, nil, fmt.Errorf("close online probes for automatic split: %w", err)
	}

	var checkIntervalSec, tcpTimeoutMS int64
	var sourceKey, host string
	var generation, enabled, autoSplitEnabled int64
	err = tx.QueryRowContext(ctx, `SELECT target.source_key, target.host, target.generation,
target.auto_split_enabled, monitor.enabled, monitor.check_interval_sec, monitor.tcp_timeout_ms
FROM v2_client_entry_monitor_target target
JOIN v2_client_entry_monitor monitor ON monitor.id = target.monitor_id
WHERE target.id = $1 AND target.monitor_id = $2
FOR UPDATE OF target`, operation.TargetID, operation.MonitorID).Scan(
		&sourceKey, &host, &generation, &autoSplitEnabled, &enabled, &checkIntervalSec, &tcpTimeoutMS,
	)
	if err != nil {
		return 0, 0, nil, err
	}
	policyID, groupID, validSource := parseClientEntryMonitorSplitGroupSourceKey(sourceKey)
	if generation != operation.TargetGeneration || enabled != 1 || autoSplitEnabled != 1 ||
		!validSource || policyID != operation.PolicyID || groupID != operation.SourceGroupID ||
		strings.TrimSpace(host) != strings.TrimSpace(operation.SourceHost) {
		return 0, 0, nil, sql.ErrNoRows
	}
	if len(probeIDs) == 0 {
		return checkIntervalSec, tcpTimeoutMS, []clientEntryAutoSplitSourceState{}, nil
	}
	placeholders := make([]string, len(probeIDs))
	args := make([]any, 0, len(probeIDs)+1)
	args = append(args, operation.TargetID)
	for index, probeID := range probeIDs {
		placeholders[index] = fmt.Sprintf("$%d", index+2)
		args = append(args, probeID)
	}
	stateRows, err := tx.QueryContext(ctx, `SELECT probe_id, last_success,
consecutive_failure, last_reported_at
FROM v2_client_entry_monitor_state
WHERE target_id = $1 AND probe_id IN (`+strings.Join(placeholders, ",")+`)
ORDER BY probe_id`, args...)
	if err != nil {
		return 0, 0, nil, fmt.Errorf("load automatic split source states: %w", err)
	}
	defer stateRows.Close()
	stateByProbe := make(map[int64]clientEntryAutoSplitSourceState, len(probeIDs))
	for stateRows.Next() {
		var state clientEntryAutoSplitSourceState
		if err := stateRows.Scan(&state.ProbeID, &state.LastSuccess, &state.ConsecutiveFailure, &state.LastReportedAt); err != nil {
			return 0, 0, nil, fmt.Errorf("scan automatic split source state: %w", err)
		}
		stateByProbe[state.ProbeID] = state
	}
	if err := stateRows.Err(); err != nil {
		return 0, 0, nil, fmt.Errorf("iterate automatic split source states: %w", err)
	}
	states := make([]clientEntryAutoSplitSourceState, 0, len(probeIDs))
	for _, probeID := range probeIDs {
		state, exists := stateByProbe[probeID]
		if !exists {
			state = clientEntryAutoSplitSourceState{ProbeID: probeID}
		}
		states = append(states, state)
	}
	return checkIntervalSec, tcpTimeoutMS, states, nil
}

func lockClientEntryAutoSplitAssignments(ctx context.Context, tx *sql.Tx, policyID, groupID int64) (int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT user_id
FROM v2_client_entry_user_policy_split_assignment
WHERE policy_id = $1 AND group_id = $2
ORDER BY user_id ASC
FOR UPDATE`, policyID, groupID)
	if err != nil {
		return 0, fmt.Errorf("lock automatic split assignments: %w", err)
	}
	defer rows.Close()
	var count int64
	for rows.Next() {
		var userID int64
		if err := rows.Scan(&userID); err != nil {
			return 0, fmt.Errorf("scan automatic split assignment: %w", err)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate automatic split assignments: %w", err)
	}
	return count, nil
}

func automaticClientEntrySplitChildNames(parentName, pathA, pathB string) (string, string) {
	parentName = strings.TrimSpace(parentName)
	if parentName == "" {
		return pathA, pathB
	}
	nameA := parentName + " A"
	nameB := parentName + " B"
	if len([]rune(nameA)) > 255 || len([]rune(nameB)) > 255 {
		return pathA, pathB
	}
	return nameA, nameB
}

func createClientEntryAutoSplitBackupIPShortageEvent(ctx context.Context, tx *sql.Tx,
	operation clientEntryAutoSplitOperation, policyName string, now int64,
) error {
	details, err := json.Marshal(map[string]any{
		"operation_id":    operation.ID,
		"policy_id":       operation.PolicyID,
		"source_group_id": operation.SourceGroupID,
		"source_host":     operation.SourceHost,
	})
	if err != nil {
		return fmt.Errorf("encode automatic split backup IP shortage event: %w", err)
	}
	policyLabel := strings.TrimSpace(policyName)
	if policyLabel == "" {
		policyLabel = fmt.Sprintf("#%d", operation.PolicyID)
	}
	message := fmt.Sprintf("用户入口自动二分等待备用 IP\n规则：%s\n故障入口：%s\n原因：%s\n系统将在备用 IP 可用后自动重试",
		policyLabel, operation.SourceHost, clientEntryAutoSplitBackupIPInsufficientReason)
	if _, err := tx.ExecContext(ctx, `INSERT INTO v2_client_entry_monitor_event
(monitor_id, target_id, probe_id, event_type, message, details, notified_at,
 notify_attempts, notify_next_attempt_at, last_notify_error, created_at)
VALUES ($1, $2, NULL, $3, $4, $5, NULL, 0, $6, '', $6)`,
		operation.MonitorID, operation.TargetID, clientEntryAutoSplitBackupIPShortageEventType,
		message, string(details), now); err != nil {
		return fmt.Errorf("create automatic split backup IP shortage event: %w", err)
	}
	return nil
}

func retryClientEntryAutoSplitOperation(ctx context.Context, tx *sql.Tx, operationID int64, reason string, now int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE v2_client_entry_auto_split_operation
SET last_error = $2, next_attempt_at = $3, updated_at = $4
WHERE id = $1 AND status = 'pending'`, operationID, reason,
		saturatingUnixAdd(now, clientEntryAutoSplitRetryDelay), now)
	if err != nil {
		return fmt.Errorf("retry client entry automatic split: %w", err)
	}
	return nil
}

func finishClientEntryAutoSplitOperation(ctx context.Context, tx *sql.Tx, operationID int64, status, reason string,
	now, backupIPAID, backupIPBID, childGroupAID, childGroupBID int64,
) error {
	var backupA, backupB, childA, childB any
	if backupIPAID > 0 {
		backupA = backupIPAID
	}
	if backupIPBID > 0 {
		backupB = backupIPBID
	}
	if childGroupAID > 0 {
		childA = childGroupAID
	}
	if childGroupBID > 0 {
		childB = childGroupBID
	}
	_, err := tx.ExecContext(ctx, `UPDATE v2_client_entry_auto_split_operation
SET status = $2, backup_ip_a_id = $3, backup_ip_b_id = $4,
    child_group_a_id = $5, child_group_b_id = $6,
    last_error = $7, completed_at = $8, updated_at = $8
WHERE id = $1 AND status = 'pending'`, operationID, status, backupA, backupB, childA, childB, reason, now)
	if err != nil {
		return fmt.Errorf("finish client entry automatic split: %w", err)
	}
	return nil
}

func commitClientEntryAutoSplit(ctx context.Context, tx *sql.Tx, processed, changed bool) (bool, bool, error) {
	if err := tx.Commit(); err != nil {
		return processed, false, fmt.Errorf("commit client entry automatic split: %w", err)
	}
	return processed, changed, nil
}
