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

const (
	clientEntryMonitorRunTimeout               = 5 * time.Minute
	clientEntryMonitorRunMaxList               = int64(100)
	clientEntryMonitorRunResultListLimit       = int64(200)
	clientEntryMonitorRunLockKey         int64 = -7_642_309_019
	clientEntryMonitorRequestKeyMaxLen         = 255
)

type clientEntryMonitorRunPair struct {
	TargetID      int64  `json:"target_id"`
	ProbeID       int64  `json:"probe_id"`
	TargetVersion int64  `json:"target_version"`
	PolicyID      int64  `json:"policy_id"`
	PolicyName    string `json:"policy_name"`
	TargetName    string `json:"target_name"`
	Host          string `json:"host"`
	Port          int64  `json:"port"`
	ProbeName     string `json:"probe_name"`
}

func (s *DBService) StartClientEntryMonitorRun(ctx context.Context, userID, chatID int64, requestKey string) (int64, error) {
	return s.StartClientEntryMonitorRunForPoliciesWithMessage(ctx, nil, userID, chatID, 0, requestKey)
}

func (s *DBService) StartClientEntryMonitorRunForPolicies(ctx context.Context, policyIDs []int64, userID, chatID int64) (int64, error) {
	return s.StartClientEntryMonitorRunForPoliciesWithMessage(ctx, policyIDs, userID, chatID, 0, "")
}

// ListClientEntryMonitorRunOptions returns the enabled policy groups that have
// at least one persisted monitor target and an enabled probe to execute them.
func (s *DBService) ListClientEntryMonitorRunOptions(ctx context.Context) ([]ClientEntryMonitorRunOption, error) {
	if s == nil || s.db == nil {
		return nil, ErrUnavailable
	}
	if err := s.ensureDNSFailoverSchema(ctx); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT policy.id, policy.name, COUNT(target.id)
FROM v2_client_entry_user_policy policy
JOIN v2_client_entry_monitor monitor ON monitor.policy_id = policy.id AND monitor.enabled = 1
JOIN v2_client_entry_monitor_target target ON target.monitor_id = monitor.id
WHERE policy.enabled = 1
  AND EXISTS (SELECT 1 FROM v2_dns_probe probe WHERE probe.enabled = 1)
GROUP BY policy.id, policy.name, policy.sort
ORDER BY policy.sort ASC NULLS LAST, policy.id ASC`)
	if err != nil {
		return nil, fmt.Errorf("query client entry monitor run options: %w", err)
	}
	defer rows.Close()
	options := make([]ClientEntryMonitorRunOption, 0)
	for rows.Next() {
		var option ClientEntryMonitorRunOption
		if err := rows.Scan(&option.PolicyID, &option.Name, &option.TargetCount); err != nil {
			return nil, fmt.Errorf("scan client entry monitor run option: %w", err)
		}
		option.Name = strings.TrimSpace(option.Name)
		options = append(options, option)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate client entry monitor run options: %w", err)
	}
	return options, nil
}

// StartClientEntryMonitorRunForPoliciesWithMessage starts an idempotent manual
// run for the selected policy groups. Every enabled probe participates. When a
// Telegram progress message is available, its ID is retained for later edits.
func (s *DBService) StartClientEntryMonitorRunForPoliciesWithMessage(ctx context.Context, policyIDs []int64, userID, chatID, messageID int64, requestKey string) (int64, error) {
	if s == nil || s.db == nil {
		return 0, ErrUnavailable
	}
	requestKey = strings.TrimSpace(requestKey)
	if len(requestKey) > clientEntryMonitorRequestKeyMaxLen {
		return 0, errors.New("入口检测请求标识过长")
	}
	if err := s.ensureDNSFailoverSchema(ctx); err != nil {
		return 0, err
	}
	s.markClientEntryMonitorTargetsDirty()
	if err := s.refreshClientEntryMonitorTargetsIfDue(ctx); err != nil {
		return 0, err
	}
	policyIDs, err := normalizeClientEntryMonitorRunPolicyIDs(policyIDs)
	if err != nil {
		return 0, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin client entry monitor run: %w", err)
	}
	defer tx.Rollback()
	existingRunID, exists, err := lockClientEntryMonitorRunStart(ctx, tx, requestKey)
	if err != nil {
		return 0, err
	}
	if exists {
		// A Telegram webhook can be retried after the run has reached a terminal
		// state. Requeue its stored menu message so the durable worker restores
		// the real status instead of leaving a transient "started" edit behind.
		if _, err := tx.ExecContext(ctx, `UPDATE v2_client_entry_monitor_run
SET progress_reported_results = -1, progress_reported_status = '',
progress_attempts = 0, progress_next_attempt_at = $2, progress_last_error = '', updated_at = $2
WHERE id = $1 AND progress_message_id IS NOT NULL`, existingRunID, time.Now().Unix()); err != nil {
			return 0, fmt.Errorf("requeue existing client entry monitor progress: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return 0, fmt.Errorf("commit existing client entry monitor run: %w", err)
		}
		s.wakeClientEntryMonitorEvaluation(ctx)
		return existingRunID, nil
	}
	now := time.Now().Unix()
	cutoff := now - int64(clientEntryMonitorRunTimeout/time.Second)
	if _, err := tx.ExecContext(ctx, `UPDATE v2_client_entry_monitor_run
SET status = 'timeout', completed_at = $1, updated_at = $1
WHERE status = 'running' AND started_at < $2`, now, cutoff); err != nil {
		return 0, fmt.Errorf("expire stale client entry monitor runs: %w", err)
	}
	var activeRunID int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM v2_client_entry_monitor_run WHERE status = 'running' ORDER BY id DESC LIMIT 1 FOR UPDATE`).Scan(&activeRunID)
	if err == nil {
		return 0, errors.New("用户入口检测正在进行，请稍后查看结果")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("query active client entry monitor run: %w", err)
	}
	available, err := loadEnabledClientEntryMonitorPolicyIDs(ctx, tx)
	if err != nil {
		return 0, err
	}
	if len(policyIDs) == 0 {
		policyIDs = available
	} else {
		availableSet := make(map[int64]struct{}, len(available))
		for _, policyID := range available {
			availableSet[policyID] = struct{}{}
		}
		for _, policyID := range policyIDs {
			if _, ok := availableSet[policyID]; !ok {
				return 0, errors.New("所选用户入口检测规则未启用或当前没有已启用探针")
			}
		}
	}
	if len(policyIDs) == 0 {
		return 0, errors.New("暂无已启用的用户入口检测规则")
	}
	placeholders, args := clientEntryMonitorIDPlaceholders(policyIDs, 1)
	rows, err := tx.QueryContext(ctx, `SELECT target.id, probe.id, target.generation,
	policy.id, policy.name, target.name, target.host, target.port, probe.name
	FROM v2_client_entry_monitor m
	JOIN v2_client_entry_user_policy policy ON policy.id = m.policy_id AND policy.enabled = 1
	JOIN v2_client_entry_monitor_target target ON target.monitor_id = m.id
	JOIN v2_dns_probe probe ON probe.enabled = 1
	WHERE m.enabled = 1 AND m.policy_id IN (`+strings.Join(placeholders, ",")+`)
	ORDER BY target.id, probe.id`, args...)
	if err != nil {
		return 0, fmt.Errorf("snapshot client entry monitor run tasks: %w", err)
	}
	expectedPairs := make([]clientEntryMonitorRunPair, 0)
	for rows.Next() {
		var pair clientEntryMonitorRunPair
		if err := rows.Scan(&pair.TargetID, &pair.ProbeID, &pair.TargetVersion,
			&pair.PolicyID, &pair.PolicyName, &pair.TargetName, &pair.Host, &pair.Port, &pair.ProbeName); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("scan client entry monitor run task: %w", err)
		}
		expectedPairs = append(expectedPairs, pair)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, fmt.Errorf("iterate client entry monitor run tasks: %w", err)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("close client entry monitor run tasks: %w", err)
	}
	expected := int64(len(expectedPairs))
	rawPolicies, err := json.Marshal(policyIDs)
	if err != nil {
		return 0, fmt.Errorf("encode client entry monitor run policies: %w", err)
	}
	rawExpectedPairs, err := json.Marshal(expectedPairs)
	if err != nil {
		return 0, fmt.Errorf("encode client entry monitor run tasks: %w", err)
	}
	status := "running"
	var completedAt any
	if expected == 0 {
		status = "completed"
		completedAt = now
	}
	var runID int64
	err = tx.QueryRowContext(ctx, `INSERT INTO v2_client_entry_monitor_run
	(requested_by_user_id, request_chat_id, request_key, progress_message_id, policy_ids, expected_pairs, status, expected_results,
	received_results, progress_reported_results, progress_reported_status, progress_next_attempt_at, progress_last_error,
	started_at, completed_at, notified_at, notify_attempts, notify_next_attempt_at, last_notify_error, created_at, updated_at)
	VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 0, -1, '', $9, '', $9, $10, NULL, 0, $9, '', $9, $9)
	RETURNING id`, nullablePositiveInt64(userID), nullablePositiveInt64(chatID), nullableClientEntryMonitorRequestKey(requestKey), nullablePositiveInt64(messageID),
		string(rawPolicies), string(rawExpectedPairs), status, expected, now, completedAt).Scan(&runID)
	if err != nil {
		return 0, fmt.Errorf("create client entry monitor run: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit client entry monitor run: %w", err)
	}
	s.wakeClientEntryMonitorEvaluation(ctx)
	return runID, nil
}

func lockClientEntryMonitorRunStart(ctx context.Context, tx *sql.Tx, requestKey string) (int64, bool, error) {
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, clientEntryMonitorRunLockKey); err != nil {
		return 0, false, fmt.Errorf("lock client entry monitor run start: %w", err)
	}
	if requestKey == "" {
		return 0, false, nil
	}
	var runID int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM v2_client_entry_monitor_run WHERE request_key = $1`, requestKey).Scan(&runID)
	if err == nil {
		return runID, true, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return 0, false, fmt.Errorf("query existing client entry monitor run: %w", err)
}

func nullableClientEntryMonitorRequestKey(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func decodeClientEntryMonitorRunPairs(raw []byte) ([]clientEntryMonitorRunPair, error) {
	pairs := make([]clientEntryMonitorRunPair, 0)
	if len(raw) == 0 {
		return pairs, nil
	}
	if err := json.Unmarshal(raw, &pairs); err != nil {
		return nil, fmt.Errorf("decode client entry monitor run tasks: %w", err)
	}
	for _, pair := range pairs {
		if pair.TargetID <= 0 || pair.ProbeID <= 0 || pair.TargetVersion <= 0 {
			return nil, errors.New("client entry monitor run task snapshot is invalid")
		}
	}
	return pairs, nil
}

func clientEntryMonitorRunPairSet(pairs []clientEntryMonitorRunPair) map[[3]int64]struct{} {
	result := make(map[[3]int64]struct{}, len(pairs))
	for _, pair := range pairs {
		result[[3]int64{pair.TargetID, pair.ProbeID, pair.TargetVersion}] = struct{}{}
	}
	return result
}

func (s *DBService) wakeClientEntryMonitorEvaluation(ctx context.Context) {
	if s.dnsFailoverEvaluator != nil {
		requestDNSFailoverEvaluationWake(ctx, s.dnsFailoverEvaluator, nil)
	}
}

func normalizeClientEntryMonitorRunPolicyIDs(policyIDs []int64) ([]int64, error) {
	if len(policyIDs) > clientEntryMonitorRunMaxPolicies {
		return nil, errors.New("入口检测规则数量过多")
	}
	seen := make(map[int64]struct{}, len(policyIDs))
	result := make([]int64, 0, len(policyIDs))
	for _, policyID := range policyIDs {
		if policyID <= 0 {
			return nil, errors.New("入口检测规则无效")
		}
		if _, exists := seen[policyID]; exists {
			continue
		}
		seen[policyID] = struct{}{}
		result = append(result, policyID)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result, nil
}

func loadEnabledClientEntryMonitorPolicyIDs(ctx context.Context, tx *sql.Tx) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT m.policy_id
FROM v2_client_entry_monitor m
JOIN v2_client_entry_user_policy policy ON policy.id = m.policy_id AND policy.enabled = 1
JOIN v2_client_entry_monitor_target target ON target.monitor_id = m.id
WHERE m.enabled = 1
  AND EXISTS (SELECT 1 FROM v2_dns_probe probe WHERE probe.enabled = 1)
ORDER BY m.policy_id`)
	if err != nil {
		return nil, fmt.Errorf("query enabled client entry monitor policies: %w", err)
	}
	defer rows.Close()
	result := make([]int64, 0)
	for rows.Next() {
		var policyID int64
		if err := rows.Scan(&policyID); err != nil {
			return nil, fmt.Errorf("scan enabled client entry monitor policy: %w", err)
		}
		result = append(result, policyID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate enabled client entry monitor policies: %w", err)
	}
	return result, nil
}

func clientEntryMonitorIDPlaceholders(ids []int64, start int) ([]string, []any) {
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for index, id := range ids {
		placeholders[index] = fmt.Sprintf("$%d", start+index)
		args[index] = id
	}
	return placeholders, args
}

func nullablePositiveInt64(value int64) any {
	if value <= 0 {
		return nil
	}
	return value
}

func (s *DBService) ListClientEntryMonitorRuns(ctx context.Context, limit int64) ([]ClientEntryMonitorRun, error) {
	if s == nil || s.db == nil {
		return nil, ErrUnavailable
	}
	if err := s.ensureDNSFailoverSchema(ctx); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > clientEntryMonitorRunMaxList {
		limit = clientEntryMonitorRunMaxList
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, policy_ids, expected_pairs, status, expected_results,
	received_results, progress_message_id, progress_reported_results, progress_reported_status,
	progress_next_attempt_at, progress_last_error, COALESCE(started_at, 0), completed_at, created_at
FROM v2_client_entry_monitor_run
WHERE status = 'running' OR created_at >= $1
ORDER BY id DESC LIMIT $2`, time.Now().Add(-clientEntryMonitorRetention).Unix(), limit)
	if err != nil {
		return nil, fmt.Errorf("query client entry monitor runs: %w", err)
	}
	runs := make([]ClientEntryMonitorRun, 0)
	indexByID := make(map[int64]int)
	for rows.Next() {
		var run ClientEntryMonitorRun
		var rawPolicies []byte
		var rawExpectedPairs []byte
		var progressMessageID sql.NullInt64
		var completed sql.NullInt64
		if err := rows.Scan(&run.ID, &rawPolicies, &rawExpectedPairs, &run.Status, &run.ExpectedResults,
			&run.ReceivedResults, &progressMessageID, &run.ProgressReportedResults, &run.ProgressReportedStatus,
			&run.ProgressNextAttemptAt, &run.ProgressLastError, &run.StartedAt, &completed, &run.CreatedAt); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan client entry monitor run: %w", err)
		}
		run.PolicyIDs = decodeClientEntryMonitorPolicyIDs(rawPolicies)
		run.ExpectedPairs, err = decodeClientEntryMonitorRunPairs(rawExpectedPairs)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		run.TotalResults = run.ReceivedResults
		if progressMessageID.Valid {
			value := progressMessageID.Int64
			run.ProgressMessageID = &value
		}
		if completed.Valid {
			value := completed.Int64
			run.CompletedAt = &value
		}
		run.Results = make([]ClientEntryMonitorRunResult, 0)
		indexByID[run.ID] = len(runs)
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate client entry monitor runs: %w", err)
	}
	_ = rows.Close()
	if len(runs) == 0 {
		return runs, nil
	}
	runIDs := make([]int64, 0, len(runs))
	for _, run := range runs {
		runIDs = append(runIDs, run.ID)
	}
	placeholders, args := clientEntryMonitorIDPlaceholders(runIDs, 1)
	resultLimitPlaceholder := fmt.Sprintf("$%d", len(args)+1)
	args = append(args, clientEntryMonitorRunResultListLimit)
	rows, err = s.db.QueryContext(ctx, `WITH ranked_results AS (
	SELECT result.id, result.run_id, result.policy_id, result.policy_name, result.target_id,
	result.target_name, result.host, result.port, result.probe_id, result.probe_name, result.success,
	result.latency_ms, result.error, result.resolved_ip, result.reported_at,
	ROW_NUMBER() OVER (PARTITION BY result.run_id ORDER BY result.success ASC, result.target_id, result.probe_id) AS result_rank
	FROM v2_client_entry_monitor_run_result result
	WHERE result.run_id IN (`+strings.Join(placeholders, ",")+`)
)
	SELECT id, run_id, policy_id, policy_name, target_id, target_name, host, port, probe_id, probe_name, success,
	latency_ms, error, resolved_ip, reported_at
	FROM ranked_results
	WHERE result_rank <= `+resultLimitPlaceholder+`
	ORDER BY run_id DESC, success ASC, target_id, probe_id`, args...)
	if err != nil {
		return nil, fmt.Errorf("query client entry monitor run results: %w", err)
	}
	for rows.Next() {
		var runID int64
		var result ClientEntryMonitorRunResult
		var success int64
		var latency sql.NullInt64
		if err := rows.Scan(&result.ID, &runID, &result.PolicyID, &result.PolicyName, &result.TargetID, &result.TargetName,
			&result.Host, &result.Port, &result.ProbeID, &result.ProbeName, &success,
			&latency, &result.Error, &result.ResolvedIP, &result.ReportedAt); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan client entry monitor run result: %w", err)
		}
		result.Success = success == 1
		if latency.Valid {
			value := latency.Int64
			result.LatencyMS = &value
		}
		if index, ok := indexByID[runID]; ok {
			runs[index].Results = append(runs[index].Results, result)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate client entry monitor run results: %w", err)
	}
	_ = rows.Close()
	for index := range runs {
		visible := int64(len(runs[index].Results))
		if visible > runs[index].TotalResults {
			runs[index].TotalResults = visible
		}
		runs[index].ResultsTruncated = runs[index].TotalResults > visible
	}
	if err := s.populateClientEntryMonitorRunResultStats(ctx, runs); err != nil {
		return nil, err
	}
	return runs, nil
}

// ClearClientEntryMonitorRuns removes completed on-demand check history. An
// active run is deliberately retained so clearing the table cannot interrupt
// probe reporting or the final result calculation. Run results are removed by
// the run_result foreign key's ON DELETE CASCADE rule.
func (s *DBService) ClearClientEntryMonitorRuns(ctx context.Context) (int64, error) {
	if s == nil || s.db == nil {
		return 0, ErrUnavailable
	}
	if err := s.ensureDNSFailoverSchema(ctx); err != nil {
		return 0, err
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM v2_client_entry_monitor_run WHERE status <> 'running'`)
	if err != nil {
		return 0, fmt.Errorf("clear client entry monitor runs: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count cleared client entry monitor runs: %w", err)
	}
	return deleted, nil
}

func (s *DBService) RecentClientEntryMonitorReport(ctx context.Context) (string, error) {
	runs, err := s.ListClientEntryMonitorRuns(ctx, 1)
	if err != nil {
		return "", err
	}
	if len(runs) > 0 {
		return formatClientEntryMonitorRunReport(runs[0]), nil
	}
	overview, err := s.ListClientEntryMonitors(ctx)
	if err != nil {
		return "", err
	}
	return formatClientEntryMonitorOverviewReport(overview), nil
}
