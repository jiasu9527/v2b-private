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
	TargetID      int64 `json:"target_id"`
	ProbeID       int64 `json:"probe_id"`
	TargetVersion int64 `json:"target_version"`
}

func (s *DBService) StartClientEntryMonitorRun(ctx context.Context, userID, chatID int64, requestKey string) (int64, error) {
	return s.startClientEntryMonitorRun(ctx, nil, userID, chatID, requestKey)
}

func (s *DBService) StartClientEntryMonitorRunForPolicies(ctx context.Context, policyIDs []int64, userID, chatID int64) (int64, error) {
	return s.startClientEntryMonitorRun(ctx, policyIDs, userID, chatID, "")
}

func (s *DBService) startClientEntryMonitorRun(ctx context.Context, policyIDs []int64, userID, chatID int64, requestKey string) (int64, error) {
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
				return 0, errors.New("所选用户入口检测规则未启用或没有可用探针")
			}
		}
	}
	if len(policyIDs) == 0 {
		return 0, errors.New("暂无已启用的用户入口检测规则")
	}
	placeholders, args := clientEntryMonitorIDPlaceholders(policyIDs, 1)
	rows, err := tx.QueryContext(ctx, `SELECT target.id, binding.probe_id, target.generation
	FROM v2_client_entry_monitor m
	JOIN v2_client_entry_user_policy policy ON policy.id = m.policy_id AND policy.enabled = 1
	JOIN v2_client_entry_monitor_target target ON target.monitor_id = m.id
	JOIN v2_client_entry_monitor_probe binding ON binding.monitor_id = m.id
	JOIN v2_dns_probe probe ON probe.id = binding.probe_id AND probe.enabled = 1
	WHERE m.enabled = 1 AND m.policy_id IN (`+strings.Join(placeholders, ",")+`)
	ORDER BY target.id, binding.probe_id`, args...)
	if err != nil {
		return 0, fmt.Errorf("snapshot client entry monitor run tasks: %w", err)
	}
	expectedPairs := make([]clientEntryMonitorRunPair, 0)
	for rows.Next() {
		var pair clientEntryMonitorRunPair
		if err := rows.Scan(&pair.TargetID, &pair.ProbeID, &pair.TargetVersion); err != nil {
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
	(requested_by_user_id, request_chat_id, request_key, policy_ids, expected_pairs, status, expected_results,
	received_results, started_at, completed_at, notified_at, notify_attempts,
	notify_next_attempt_at, last_notify_error, created_at, updated_at)
	VALUES ($1, $2, $3, $4, $5, $6, $7, 0, $8, $9, NULL, 0, $8, '', $8, $8)
	RETURNING id`, nullablePositiveInt64(userID), nullablePositiveInt64(chatID), nullableClientEntryMonitorRequestKey(requestKey),
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
JOIN v2_client_entry_monitor_probe binding ON binding.monitor_id = m.id
JOIN v2_dns_probe probe ON probe.id = binding.probe_id AND probe.enabled = 1
WHERE m.enabled = 1
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
	rows, err := s.db.QueryContext(ctx, `SELECT id, policy_ids, status, expected_results,
	received_results, COALESCE(started_at, 0), completed_at, created_at
FROM v2_client_entry_monitor_run
ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("query client entry monitor runs: %w", err)
	}
	runs := make([]ClientEntryMonitorRun, 0)
	indexByID := make(map[int64]int)
	for rows.Next() {
		var run ClientEntryMonitorRun
		var rawPolicies []byte
		var completed sql.NullInt64
		if err := rows.Scan(&run.ID, &rawPolicies, &run.Status, &run.ExpectedResults,
			&run.ReceivedResults, &run.StartedAt, &completed, &run.CreatedAt); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan client entry monitor run: %w", err)
		}
		run.PolicyIDs = decodeClientEntryMonitorPolicyIDs(rawPolicies)
		run.TotalResults = run.ReceivedResults
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
	SELECT result.id, result.run_id, result.target_id,
	result.target_name, result.host, result.port, result.probe_id, result.probe_name, result.success,
	result.latency_ms, result.error, result.resolved_ip, result.reported_at,
	ROW_NUMBER() OVER (PARTITION BY result.run_id ORDER BY result.target_id, result.probe_id) AS result_rank
	FROM v2_client_entry_monitor_run_result result
	WHERE result.run_id IN (`+strings.Join(placeholders, ",")+`)
)
	SELECT id, run_id, target_id, target_name, host, port, probe_id, probe_name, success,
	latency_ms, error, resolved_ip, reported_at
	FROM ranked_results
	WHERE result_rank <= `+resultLimitPlaceholder+`
	ORDER BY run_id DESC, target_id, probe_id`, args...)
	if err != nil {
		return nil, fmt.Errorf("query client entry monitor run results: %w", err)
	}
	for rows.Next() {
		var runID int64
		var result ClientEntryMonitorRunResult
		var success int64
		var latency sql.NullInt64
		if err := rows.Scan(&result.ID, &runID, &result.TargetID, &result.TargetName,
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
	return runs, nil
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
	var builder strings.Builder
	builder.WriteString("用户入口检测近期状态\n")
	count := 0
	for _, monitor := range overview.Items {
		for _, target := range monitor.Targets {
			for _, state := range target.States {
				if count >= 50 {
					builder.WriteString("\n结果较多，其余请在后台查看。")
					return builder.String(), nil
				}
				status := "未检测"
				if state.Stale {
					status = "已过期"
				} else if state.LastSuccess != nil && *state.LastSuccess {
					status = "正常"
				} else if state.LastSuccess != nil {
					status = "异常"
				}
				reportedAt := "无上报时间"
				if state.LastReportedAt != nil {
					reportedAt = formatClientEntryMonitorReportedAt(*state.LastReportedAt)
				}
				fmt.Fprintf(&builder, "\n%s · %s:%d · %s · %s · 上报：%s", monitor.PolicyName, target.Host, target.Port, state.ProbeName, status, reportedAt)
				count++
			}
		}
	}
	if count == 0 {
		builder.WriteString("\n暂无检测结果。")
	}
	return builder.String(), nil
}

func formatClientEntryMonitorRunReport(run ClientEntryMonitorRun) string {
	var builder strings.Builder
	totalResults := run.TotalResults
	if totalResults < run.ReceivedResults {
		totalResults = run.ReceivedResults
	}
	if totalResults < int64(len(run.Results)) {
		totalResults = int64(len(run.Results))
	}
	normal, abnormal := 0, 0
	for _, result := range run.Results {
		if result.Success {
			normal++
		} else {
			abnormal++
		}
	}
	missing := run.ExpectedResults - run.ReceivedResults
	if missing < 0 {
		missing = 0
	}
	resultSummary := fmt.Sprintf("正常：%d · 异常：%d · 未返回：%d", normal, abnormal, missing)
	if run.ResultsTruncated {
		resultSummary = fmt.Sprintf("前 %d 条结果：%s", len(run.Results), resultSummary)
	}
	fmt.Fprintf(&builder, "用户入口一键检测 #%d\n状态：%s\n进度：%d/%d\n%s",
		run.ID, clientEntryMonitorRunStatusText(run.Status), run.ReceivedResults, run.ExpectedResults, resultSummary)
	const resultLimit = 50
	visibleResults := run.Results
	if len(visibleResults) > resultLimit {
		visibleResults = visibleResults[:resultLimit]
	}
	for _, result := range visibleResults {
		status := "异常"
		detail := strings.TrimSpace(result.Error)
		if result.Success {
			status = "正常"
			if result.LatencyMS != nil {
				detail = fmt.Sprintf("%d ms", *result.LatencyMS)
			}
		}
		if detail == "" {
			detail = "无详情"
		}
		fmt.Fprintf(&builder, "\n%s · %s:%d · %s · %s（%s） · 上报：%s", result.TargetName, result.Host, result.Port, result.ProbeName, status, detail, formatClientEntryMonitorReportedAt(result.ReportedAt))
	}
	if len(run.Results) == 0 {
		builder.WriteString("\n暂无可用检测结果。")
	} else if totalResults > int64(len(visibleResults)) {
		fmt.Fprintf(&builder, "\n共 %d 条结果，其余请在后台查看。", totalResults)
	}
	return builder.String()
}

func formatClientEntryMonitorReportedAt(value int64) string {
	if value <= 0 {
		return "未知"
	}
	return time.Unix(value, 0).Format("2006-01-02 15:04:05 MST")
}

func clientEntryMonitorRunStatusText(status string) string {
	switch status {
	case "running":
		return "检测中"
	case "completed":
		return "已完成"
	case "timeout":
		return "已超时"
	default:
		return status
	}
}
