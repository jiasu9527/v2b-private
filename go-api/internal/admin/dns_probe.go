package admin

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/netip"
	"sort"
	"strings"
	"time"
)

const (
	maxDNSProbeSecretLength = 512
	defaultProbeOfflineSec  = int64(90)
	maxDNSProbeResultBatch  = 500
	maxDNSProbeLatencyMS    = int64(3_600_000)
	maxDNSProbeResultError  = 1024
)

var (
	ErrDNSProbeUnauthorized      = errors.New("dns probe unauthorized")
	ErrDNSProbeInvalidRequest    = errors.New("invalid dns probe request")
	ErrDNSProbeHeartbeatRequired = errors.New("dns probe heartbeat required")
)

type DNSProbeIdentity struct {
	ID int64
}

type DNSProbeHeartbeatRequest struct {
	Version  string `json:"version"`
	Arch     string `json:"arch"`
	PublicIP string `json:"-"`
}

type DNSProbeHeartbeatResult struct {
	PrewarmCount int64 `json:"prewarm_count"`
	Reconnected  bool  `json:"reconnected"`
}

type DNSProbeTask struct {
	TargetID         int64  `json:"target_id"`
	GroupID          int64  `json:"group_id"`
	RunID            int64  `json:"run_id,omitempty"`
	TargetVersion    int64  `json:"target_version,omitempty"`
	CheckHost        string `json:"check_host"`
	CheckPort        int64  `json:"check_port"`
	TCPTimeoutMS     int64  `json:"tcp_timeout_ms"`
	CheckIntervalSec int64  `json:"check_interval_sec"`
}

type DNSProbeResult struct {
	ResultID      string `json:"result_id"`
	TargetID      int64  `json:"target_id"`
	RunID         int64  `json:"run_id,omitempty"`
	TargetVersion int64  `json:"target_version,omitempty"`
	Success       *bool  `json:"success"`
	LatencyMS     *int64 `json:"latency_ms"`
	Error         string `json:"error"`
	ResolvedIP    string `json:"resolved_ip"`
}

type DNSProbeResultsRequest struct {
	Results []DNSProbeResult `json:"results"`
}

type DNSProbeReportResult struct {
	Accepted     int     `json:"accepted"`
	Duplicates   int     `json:"duplicates"`
	Skipped      int     `json:"skipped"`
	PrewarmCount int64   `json:"prewarm_count"`
	GroupIDs     []int64 `json:"group_ids"`
}

type dnsProbeStateValues struct {
	Success       int64
	Latency       any
	Error         string
	SuccessStreak int64
	FailureStreak int64
}

type dnsProbeStateBatchRow struct {
	TargetID       int64  `json:"target_id"`
	LastSuccess    int64  `json:"last_success"`
	LatencyMS      *int64 `json:"latency_ms"`
	LastError      string `json:"last_error"`
	ResolvedIP     string `json:"resolved_ip"`
	InitialSuccess int64  `json:"initial_success"`
	InitialFailure int64  `json:"initial_failure"`
}

type dnsProbeAllowedTargetState struct {
	GroupID          int64
	CheckIntervalSec int64
	TCPTimeoutMS     int64
	ProbeOfflineSec  int64
	LastSuccess      sql.NullInt64
	SuccessStreak    int64
	FailureStreak    int64
	LastReportedAt   sql.NullInt64
}

type dnsFailoverOutboxBatchRow struct {
	GroupID int64 `json:"group_id"`
}

type DNSProbeService interface {
	AuthenticateDNSProbe(ctx context.Context, rawSecret string) (DNSProbeIdentity, error)
	HeartbeatDNSProbe(ctx context.Context, probeID int64, request DNSProbeHeartbeatRequest) (DNSProbeHeartbeatResult, error)
	ListDNSProbeTasks(ctx context.Context, probeID int64) ([]DNSProbeTask, error)
	ReportDNSProbeResults(ctx context.Context, probeID int64, request DNSProbeResultsRequest) (DNSProbeReportResult, error)
}

type DNSFailoverEvaluationRequester interface {
	// RequestDNSFailoverEvaluation is a best-effort post-commit wake hint. The
	// transactionally persisted evaluation outbox is the durable source of work,
	// so requester failures must not turn into probe retries.
	RequestDNSFailoverEvaluation(ctx context.Context, groupIDs []int64) error
}

var _ DNSProbeService = (*DBService)(nil)

func (s *DBService) WithDNSFailoverEvaluationRequester(requester DNSFailoverEvaluationRequester) *DBService {
	s.dnsFailoverEvaluator = requester
	return s
}

func (s *DBService) AuthenticateDNSProbe(ctx context.Context, rawSecret string) (DNSProbeIdentity, error) {
	if rawSecret == "" || len(rawSecret) > maxDNSProbeSecretLength {
		return DNSProbeIdentity{}, ErrDNSProbeUnauthorized
	}
	if err := s.ensureDNSFailoverSchema(ctx); err != nil {
		return DNSProbeIdentity{}, err
	}

	want := sha256.Sum256([]byte(rawSecret))
	wantHex := hex.EncodeToString(want[:])
	var (
		probeID  int64
		tokenHex string
	)
	err := s.db.QueryRowContext(ctx, `SELECT id, token_hash FROM v2_dns_probe WHERE token_hash = $1 AND enabled = 1 LIMIT 1`, wantHex).Scan(&probeID, &tokenHex)
	rowExists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return DNSProbeIdentity{}, fmt.Errorf("authenticate DNS probe: %w", err)
	}

	dummy := [sha256.Size]byte{}
	candidate := dummy[:]
	decoded, decodeErr := hex.DecodeString(tokenHex)
	validHash := decodeErr == nil && len(decoded) == sha256.Size
	if rowExists && validHash {
		candidate = decoded
	}
	matched := subtle.ConstantTimeCompare(want[:], candidate)
	if !rowExists || !validHash || matched != 1 || probeID <= 0 {
		return DNSProbeIdentity{}, ErrDNSProbeUnauthorized
	}
	return DNSProbeIdentity{ID: probeID}, nil
}

func (s *DBService) HeartbeatDNSProbe(ctx context.Context, probeID int64, request DNSProbeHeartbeatRequest) (DNSProbeHeartbeatResult, error) {
	requestNow := time.Now().Unix()
	if err := normalizeDNSProbeHeartbeatRequest(&request); err != nil {
		return DNSProbeHeartbeatResult{}, err
	}
	if probeID <= 0 {
		return DNSProbeHeartbeatResult{}, ErrDNSProbeUnauthorized
	}
	if err := s.ensureDNSFailoverSchema(ctx); err != nil {
		return DNSProbeHeartbeatResult{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DNSProbeHeartbeatResult{}, fmt.Errorf("begin DNS probe heartbeat: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	var (
		enabled       int64
		lastHeartbeat sql.NullInt64
		prewarmCount  int64
	)
	err = tx.QueryRowContext(ctx, `SELECT enabled, last_heartbeat_at, prewarm_count FROM v2_dns_probe WHERE id = $1 FOR UPDATE`, probeID).
		Scan(&enabled, &lastHeartbeat, &prewarmCount)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && enabled != 1) {
		return DNSProbeHeartbeatResult{}, ErrDNSProbeUnauthorized
	}
	if err != nil {
		return DNSProbeHeartbeatResult{}, fmt.Errorf("load DNS probe heartbeat state: %w", err)
	}

	var configuredOffline sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT MIN(g.probe_offline_sec)
FROM v2_dns_failover_group_probe gp
JOIN v2_dns_failover_group g ON g.id = gp.group_id
WHERE gp.probe_id = $1 AND g.enabled = 1`, probeID).Scan(&configuredOffline)
	if err != nil {
		return DNSProbeHeartbeatResult{}, fmt.Errorf("load DNS probe offline threshold: %w", err)
	}
	offlineSec := dnsProbeOfflineThreshold(configuredOffline)

	now := requestNow
	needsReset := !dnsProbeHeartbeatFresh(lastHeartbeat, now, offlineSec)
	reconnected := lastHeartbeat.Valid && lastHeartbeat.Int64 >= 0 && lastHeartbeat.Int64 <= now && needsReset
	if needsReset {
		if _, err := tx.ExecContext(ctx, `UPDATE v2_dns_probe SET version = $2, arch = $3, public_ip = $4, last_heartbeat_at = $5, prewarm_count = 0, updated_at = $5 WHERE id = $1`, probeID, request.Version, request.Arch, request.PublicIP, now); err != nil {
			return DNSProbeHeartbeatResult{}, fmt.Errorf("update reset DNS probe heartbeat: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE v2_dns_probe_target_state SET warmed_up = 0, consecutive_success = 0, consecutive_failure = 0, updated_at = $2 WHERE probe_id = $1`, probeID, now); err != nil {
			return DNSProbeHeartbeatResult{}, fmt.Errorf("reset DNS probe state: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM v2_client_entry_monitor_state WHERE probe_id = $1`, probeID); err != nil {
			return DNSProbeHeartbeatResult{}, fmt.Errorf("reset client entry monitor state: %w", err)
		}
		prewarmCount = 0
	} else {
		if _, err := tx.ExecContext(ctx, `UPDATE v2_dns_probe SET version = $2, arch = $3, public_ip = $4, last_heartbeat_at = $5, updated_at = $5 WHERE id = $1`, probeID, request.Version, request.Arch, request.PublicIP, now); err != nil {
			return DNSProbeHeartbeatResult{}, fmt.Errorf("update DNS probe heartbeat: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return DNSProbeHeartbeatResult{}, fmt.Errorf("commit DNS probe heartbeat: %w", err)
	}
	committed = true
	return DNSProbeHeartbeatResult{PrewarmCount: prewarmCount, Reconnected: reconnected}, nil
}

func normalizeDNSProbeHeartbeatRequest(request *DNSProbeHeartbeatRequest) error {
	if request == nil {
		return fmt.Errorf("%w: heartbeat is required", ErrDNSProbeInvalidRequest)
	}
	for _, field := range []struct {
		name   string
		value  *string
		length int
	}{
		{name: "version", value: &request.Version, length: 64},
		{name: "arch", value: &request.Arch, length: 32},
	} {
		if err := validateDNSFailoverIdentifierText(field.name, *field.value); err != nil {
			return fmt.Errorf("%w: %s", ErrDNSProbeInvalidRequest, err)
		}
		*field.value = strings.TrimSpace(*field.value)
		if *field.value == "" || len([]rune(*field.value)) > field.length {
			return fmt.Errorf("%w: %s length is invalid", ErrDNSProbeInvalidRequest, field.name)
		}
	}

	addr, err := netip.ParseAddr(strings.TrimSpace(request.PublicIP))
	if err != nil || addr.Zone() != "" {
		return fmt.Errorf("%w: public_ip is invalid", ErrDNSProbeInvalidRequest)
	}
	request.PublicIP = addr.Unmap().String()
	return nil
}

func (s *DBService) ListDNSProbeTasks(ctx context.Context, probeID int64) ([]DNSProbeTask, error) {
	if probeID <= 0 {
		return nil, ErrDNSProbeUnauthorized
	}
	if err := s.ensureDNSFailoverSchema(ctx); err != nil {
		return nil, err
	}

	rows, err := s.db.QueryContext(ctx, `SELECT t.id, g.id, t.check_host, t.check_port, g.tcp_timeout_ms, g.check_interval_sec
FROM v2_dns_failover_group_probe gp
JOIN v2_dns_probe p ON p.id = gp.probe_id AND p.enabled = 1
JOIN v2_dns_failover_group g ON g.id = gp.group_id
JOIN v2_dns_failover_target t ON t.group_id = g.id
WHERE gp.probe_id = $1 AND g.enabled = 1 AND t.enabled = 1
ORDER BY g.id ASC, t.sort ASC, t.id ASC`, probeID)
	if err != nil {
		return nil, fmt.Errorf("list DNS probe tasks: %w", err)
	}
	defer rows.Close()

	tasks := make([]DNSProbeTask, 0)
	for rows.Next() {
		var task DNSProbeTask
		if err := rows.Scan(&task.TargetID, &task.GroupID, &task.CheckHost, &task.CheckPort, &task.TCPTimeoutMS, &task.CheckIntervalSec); err != nil {
			return nil, fmt.Errorf("list DNS probe tasks: %w", err)
		}
		tasks = append(tasks, task)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list DNS probe tasks: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close DNS probe tasks: %w", err)
	}
	if err := s.refreshClientEntryMonitorTargetsIfDue(ctx); err != nil {
		return nil, fmt.Errorf("refresh client entry probe tasks: %w", err)
	}
	entryTasks, err := s.listClientEntryProbeTasks(ctx, probeID)
	if err != nil {
		return nil, err
	}
	tasks = append(tasks, entryTasks...)
	return tasks, nil
}

func (s *DBService) ReportDNSProbeResults(ctx context.Context, probeID int64, request DNSProbeResultsRequest) (DNSProbeReportResult, error) {
	if probeID <= 0 {
		return DNSProbeReportResult{}, ErrDNSProbeUnauthorized
	}
	if err := normalizeDNSProbeResultsRequest(&request); err != nil {
		return DNSProbeReportResult{}, err
	}
	if err := s.ensureDNSFailoverSchema(ctx); err != nil {
		return DNSProbeReportResult{}, err
	}
	dnsResults := make([]DNSProbeResult, 0, len(request.Results))
	entryResults := make([]DNSProbeResult, 0, len(request.Results))
	for _, result := range request.Results {
		if isClientEntryProbeTargetID(result.TargetID) {
			entryResults = append(entryResults, result)
		} else {
			dnsResults = append(dnsResults, result)
		}
	}
	entrySummary := DNSProbeReportResult{GroupIDs: make([]int64, 0)}
	if len(entryResults) > 0 {
		var err error
		entrySummary, err = s.reportClientEntryProbeResults(ctx, probeID, entryResults)
		if err != nil {
			return DNSProbeReportResult{}, err
		}
	}
	if len(dnsResults) == 0 {
		return entrySummary, nil
	}
	dnsSummary, err := s.reportDNSFailoverProbeResults(ctx, probeID, DNSProbeResultsRequest{Results: dnsResults})
	if err != nil {
		return DNSProbeReportResult{}, err
	}
	dnsSummary.Accepted += entrySummary.Accepted
	dnsSummary.Duplicates += entrySummary.Duplicates
	dnsSummary.Skipped += entrySummary.Skipped
	return dnsSummary, nil
}

func (s *DBService) reportDNSFailoverProbeResults(ctx context.Context, probeID int64, request DNSProbeResultsRequest) (DNSProbeReportResult, error) {
	requestNow := time.Now().Unix()
	if probeID <= 0 {
		return DNSProbeReportResult{}, ErrDNSProbeUnauthorized
	}
	if err := normalizeDNSProbeResultsRequest(&request); err != nil {
		return DNSProbeReportResult{}, err
	}
	if err := s.ensureDNSFailoverSchema(ctx); err != nil {
		return DNSProbeReportResult{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DNSProbeReportResult{}, fmt.Errorf("begin DNS probe result report: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	var (
		enabled       int64
		lastHeartbeat sql.NullInt64
		prewarmCount  int64
	)
	err = tx.QueryRowContext(ctx, `SELECT enabled, last_heartbeat_at, prewarm_count FROM v2_dns_probe WHERE id = $1 FOR UPDATE`, probeID).Scan(&enabled, &lastHeartbeat, &prewarmCount)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && enabled != 1) {
		return DNSProbeReportResult{}, ErrDNSProbeUnauthorized
	}
	if err != nil {
		return DNSProbeReportResult{}, fmt.Errorf("load DNS probe report state: %w", err)
	}

	var configuredOffline sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT MIN(g.probe_offline_sec)
FROM v2_dns_failover_group_probe gp
JOIN v2_dns_failover_group g ON g.id = gp.group_id
WHERE gp.probe_id = $1 AND g.enabled = 1`, probeID).Scan(&configuredOffline)
	if err != nil {
		return DNSProbeReportResult{}, fmt.Errorf("load DNS probe offline threshold: %w", err)
	}
	offlineSec := dnsProbeOfflineThreshold(configuredOffline)
	now := requestNow
	if !dnsProbeHeartbeatFresh(lastHeartbeat, now, offlineSec) {
		return DNSProbeReportResult{}, ErrDNSProbeHeartbeatRequired
	}
	if prewarmCount < 0 {
		prewarmCount = 0
	}
	if prewarmCount > 3 {
		prewarmCount = 3
	}

	requestJSON, err := marshalDNSProbeBatch(request.Results)
	if err != nil {
		return DNSProbeReportResult{}, fmt.Errorf("encode DNS probe result IDs: %w", err)
	}
	rows, err := tx.QueryContext(ctx, `SELECT i.result_id
FROM v2_dns_probe_result_inbox i
JOIN jsonb_to_recordset($2::jsonb) AS requested(result_id text)
ON requested.result_id = i.result_id
WHERE i.probe_id = $1
GROUP BY i.result_id`, probeID, requestJSON)
	if err != nil {
		return DNSProbeReportResult{}, fmt.Errorf("load existing DNS probe result IDs: %w", err)
	}
	existingResultIDs := make(map[string]struct{})
	for rows.Next() {
		var resultID string
		if err := rows.Scan(&resultID); err != nil {
			_ = rows.Close()
			return DNSProbeReportResult{}, fmt.Errorf("load existing DNS probe result IDs: %w", err)
		}
		existingResultIDs[resultID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return DNSProbeReportResult{}, fmt.Errorf("load existing DNS probe result IDs: %w", err)
	}
	if err := rows.Close(); err != nil {
		return DNSProbeReportResult{}, fmt.Errorf("close existing DNS probe result IDs: %w", err)
	}

	resultSummary := DNSProbeReportResult{PrewarmCount: prewarmCount, GroupIDs: make([]int64, 0)}
	newResults := make([]DNSProbeResult, 0, len(request.Results))
	seenNewResultIDs := make(map[string]struct{}, len(request.Results))
	for _, result := range request.Results {
		if _, duplicate := existingResultIDs[result.ResultID]; duplicate {
			resultSummary.Duplicates++
			continue
		}
		if _, duplicate := seenNewResultIDs[result.ResultID]; duplicate {
			resultSummary.Duplicates++
			continue
		}
		seenNewResultIDs[result.ResultID] = struct{}{}
		newResults = append(newResults, result)
	}
	if len(newResults) == 0 {
		if err := tx.Commit(); err != nil {
			return DNSProbeReportResult{}, fmt.Errorf("commit duplicate DNS probe result report: %w", err)
		}
		committed = true
		return resultSummary, nil
	}

	newJSON, err := marshalDNSProbeBatch(newResults)
	if err != nil {
		return DNSProbeReportResult{}, fmt.Errorf("encode new DNS probe results: %w", err)
	}
	rows, err = tx.QueryContext(ctx, `SELECT requested.target_id, t.group_id,
g.check_interval_sec, g.tcp_timeout_ms, g.probe_offline_sec,
s.last_success, COALESCE(s.consecutive_success, 0), COALESCE(s.consecutive_failure, 0), s.last_reported_at
FROM (
  SELECT DISTINCT target_id
  FROM jsonb_to_recordset($2::jsonb) AS submitted(target_id bigint)
) AS requested
JOIN v2_dns_failover_target t ON t.id = requested.target_id
JOIN v2_dns_failover_group g ON g.id = t.group_id
JOIN v2_dns_failover_group_probe gp ON gp.group_id = g.id
LEFT JOIN v2_dns_probe_target_state s ON s.probe_id = $1 AND s.target_id = t.id
WHERE gp.probe_id = $1 AND g.enabled = 1 AND t.enabled = 1
ORDER BY requested.target_id ASC
FOR SHARE OF gp, g, t`, probeID, newJSON)
	if err != nil {
		return DNSProbeReportResult{}, fmt.Errorf("load allowed DNS probe targets: %w", err)
	}
	allowedTargets := make(map[int64]dnsProbeAllowedTargetState, len(newResults))
	for rows.Next() {
		var (
			targetID int64
			state    dnsProbeAllowedTargetState
		)
		if err := rows.Scan(
			&targetID,
			&state.GroupID,
			&state.CheckIntervalSec,
			&state.TCPTimeoutMS,
			&state.ProbeOfflineSec,
			&state.LastSuccess,
			&state.SuccessStreak,
			&state.FailureStreak,
			&state.LastReportedAt,
		); err != nil {
			_ = rows.Close()
			return DNSProbeReportResult{}, fmt.Errorf("load allowed DNS probe targets: %w", err)
		}
		allowedTargets[targetID] = state
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return DNSProbeReportResult{}, fmt.Errorf("load allowed DNS probe targets: %w", err)
	}
	if err := rows.Close(); err != nil {
		return DNSProbeReportResult{}, fmt.Errorf("close allowed DNS probe targets: %w", err)
	}
	validResults := make([]DNSProbeResult, 0, len(newResults))
	for _, result := range newResults {
		if _, ok := allowedTargets[result.TargetID]; !ok {
			resultSummary.Skipped++
			continue
		}
		validResults = append(validResults, result)
	}
	if len(validResults) == 0 {
		if err := tx.Commit(); err != nil {
			return DNSProbeReportResult{}, fmt.Errorf("commit skipped DNS probe result report: %w", err)
		}
		committed = true
		return resultSummary, nil
	}
	validJSON, err := marshalDNSProbeBatch(validResults)
	if err != nil {
		return DNSProbeReportResult{}, fmt.Errorf("encode valid DNS probe results: %w", err)
	}

	rows, err = tx.QueryContext(ctx, `INSERT INTO v2_dns_probe_result_inbox (probe_id, target_id, result_id, created_at)
SELECT $1, requested.target_id, requested.result_id, $3
FROM jsonb_to_recordset($2::jsonb) AS requested(result_id text, target_id bigint)
ON CONFLICT (probe_id, result_id) DO NOTHING
RETURNING result_id, target_id`, probeID, validJSON, now)
	if err != nil {
		return DNSProbeReportResult{}, fmt.Errorf("batch insert DNS probe result inbox: %w", err)
	}
	acceptedIDs := make(map[string]int64, len(newResults))
	for rows.Next() {
		var resultID string
		var targetID int64
		if err := rows.Scan(&resultID, &targetID); err != nil {
			_ = rows.Close()
			return DNSProbeReportResult{}, fmt.Errorf("read accepted DNS probe result inbox: %w", err)
		}
		acceptedIDs[resultID] = targetID
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return DNSProbeReportResult{}, fmt.Errorf("read accepted DNS probe result inbox: %w", err)
	}
	if err := rows.Close(); err != nil {
		return DNSProbeReportResult{}, fmt.Errorf("close accepted DNS probe result inbox: %w", err)
	}

	acceptedResults := make([]DNSProbeResult, 0, len(acceptedIDs))
	stateRows := make([]dnsProbeStateBatchRow, 0, len(acceptedIDs))
	stateIndexes := make(map[int64]int, len(acceptedIDs))
	stateTransitions := make(map[int64]bool, len(acceptedIDs))
	logRows := make([]dnsFailoverLogEntry, 0, len(acceptedIDs))
	affectedGroups := make(map[int64]struct{})
	for _, result := range validResults {
		if _, accepted := acceptedIDs[result.ResultID]; !accepted {
			resultSummary.Duplicates++
			continue
		}
		stateValues := dnsProbeStateWriteValues(result)
		latency := result.LatencyMS
		if stateValues.Success == 0 {
			latency = nil
		}
		if index, exists := stateIndexes[result.TargetID]; exists {
			row := &stateRows[index]
			if row.LastSuccess != stateValues.Success {
				stateTransitions[result.TargetID] = true
				row.InitialSuccess = stateValues.SuccessStreak
				row.InitialFailure = stateValues.FailureStreak
			} else if stateValues.Success == 1 {
				row.InitialSuccess = saturatingDNSProbeStreakAdd(row.InitialSuccess, 1)
				row.InitialFailure = 0
			} else {
				row.InitialSuccess = 0
				row.InitialFailure = saturatingDNSProbeStreakAdd(row.InitialFailure, 1)
			}
			row.LastSuccess = stateValues.Success
			row.LatencyMS = latency
			row.LastError = stateValues.Error
			row.ResolvedIP = result.ResolvedIP
		} else {
			stateIndexes[result.TargetID] = len(stateRows)
			stateRows = append(stateRows, dnsProbeStateBatchRow{
				TargetID:       result.TargetID,
				LastSuccess:    stateValues.Success,
				LatencyMS:      latency,
				LastError:      stateValues.Error,
				ResolvedIP:     result.ResolvedIP,
				InitialSuccess: stateValues.SuccessStreak,
				InitialFailure: stateValues.FailureStreak,
			})
		}
		targetID := result.TargetID
		success := result.Success != nil && *result.Success
		level := "warning"
		outcome := "failure"
		message := "probe check failed"
		if success {
			level = "info"
			outcome = "success"
			message = "probe check succeeded"
		}
		logRows = append(logRows, dnsFailoverLogEntry{
			GroupID:  allowedTargets[result.TargetID].GroupID,
			ProbeID:  &probeID,
			TargetID: &targetID,
			Stage:    "probe_result",
			Level:    level,
			Outcome:  outcome,
			Message:  message,
			Details: map[string]any{
				"result_id":   result.ResultID,
				"success":     success,
				"latency_ms":  latency,
				"error":       stateValues.Error,
				"resolved_ip": result.ResolvedIP,
			},
			CreatedAt: now,
		})
		acceptedResults = append(acceptedResults, result)
		affectedGroups[allowedTargets[result.TargetID].GroupID] = struct{}{}
	}

	if len(acceptedResults) > 0 {
		for index := range stateRows {
			row := &stateRows[index]
			current := allowedTargets[row.TargetID]
			if stateTransitions[row.TargetID] || !current.LastSuccess.Valid || current.LastSuccess.Int64 != row.LastSuccess ||
				!dnsFailoverProbeStateFresh(current.LastReportedAt, now, current.CheckIntervalSec, current.TCPTimeoutMS, current.ProbeOfflineSec) {
				continue
			}
			if row.LastSuccess == 1 {
				row.InitialSuccess = saturatingDNSProbeStreakAdd(current.SuccessStreak, row.InitialSuccess)
				row.InitialFailure = 0
			} else {
				row.InitialSuccess = 0
				row.InitialFailure = saturatingDNSProbeStreakAdd(current.FailureStreak, row.InitialFailure)
			}
		}
		stateJSON, err := marshalDNSProbeBatch(stateRows)
		if err != nil {
			return DNSProbeReportResult{}, fmt.Errorf("encode DNS probe state batch: %w", err)
		}
		warmedUp := int64(0)
		if prewarmCount >= 3 {
			warmedUp = 1
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO v2_dns_probe_target_state (
probe_id, target_id, last_success, last_latency_ms, last_error, last_resolved_ip,
consecutive_success, consecutive_failure, last_reported_at, warmed_up, created_at, updated_at
) SELECT $1, requested.target_id, requested.last_success, requested.latency_ms, requested.last_error, requested.resolved_ip,
requested.initial_success, requested.initial_failure, $3, $4, $3, $3
FROM jsonb_to_recordset($2::jsonb) AS requested(
target_id bigint, last_success smallint, latency_ms integer, last_error text, resolved_ip text,
initial_success integer, initial_failure integer
)
ON CONFLICT (probe_id, target_id) DO UPDATE SET
last_success = EXCLUDED.last_success,
last_latency_ms = EXCLUDED.last_latency_ms,
last_error = EXCLUDED.last_error,
last_resolved_ip = EXCLUDED.last_resolved_ip,
consecutive_success = EXCLUDED.consecutive_success,
consecutive_failure = EXCLUDED.consecutive_failure,
last_reported_at = EXCLUDED.last_reported_at,
warmed_up = CASE WHEN EXCLUDED.warmed_up = 1 THEN 1 ELSE v2_dns_probe_target_state.warmed_up END,
updated_at = EXCLUDED.updated_at`, probeID, stateJSON, now, warmedUp)
		if err != nil {
			return DNSProbeReportResult{}, fmt.Errorf("batch upsert DNS probe target state: %w", err)
		}

	}
	resultSummary.Accepted = len(acceptedResults)

	if resultSummary.Accepted > 0 && prewarmCount < 3 {
		prewarmCount++
		if _, err := tx.ExecContext(ctx, `UPDATE v2_dns_probe SET prewarm_count = $2, updated_at = $3 WHERE id = $1`, probeID, prewarmCount, now); err != nil {
			return DNSProbeReportResult{}, fmt.Errorf("advance DNS probe prewarm state: %w", err)
		}
		if prewarmCount == 3 {
			if _, err := tx.ExecContext(ctx, `UPDATE v2_dns_probe_target_state SET warmed_up = 1, updated_at = $2 WHERE probe_id = $1 AND warmed_up = 0`, probeID, now); err != nil {
				return DNSProbeReportResult{}, fmt.Errorf("warm DNS probe target states: %w", err)
			}
		}
	}
	resultSummary.PrewarmCount = prewarmCount
	for groupID := range affectedGroups {
		resultSummary.GroupIDs = append(resultSummary.GroupIDs, groupID)
	}
	sort.Slice(resultSummary.GroupIDs, func(i, j int) bool { return resultSummary.GroupIDs[i] < resultSummary.GroupIDs[j] })
	if len(resultSummary.GroupIDs) > 0 {
		outboxRows := make([]dnsFailoverOutboxBatchRow, 0, len(resultSummary.GroupIDs))
		for _, groupID := range resultSummary.GroupIDs {
			outboxRows = append(outboxRows, dnsFailoverOutboxBatchRow{GroupID: groupID})
		}
		outboxJSON, err := marshalDNSProbeBatch(outboxRows)
		if err != nil {
			return DNSProbeReportResult{}, fmt.Errorf("encode DNS failover evaluation outbox: %w", err)
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO v2_dns_failover_eval_outbox (
group_id, operation, target_id, source_target_id, requested_at, attempts, next_attempt_at, last_error, created_at, updated_at
) SELECT requested.group_id, 'evaluate', NULL, NULL, $2, 0, $2, '', $2, $2
FROM jsonb_to_recordset($1::jsonb) AS requested(group_id bigint)
ON CONFLICT (group_id) DO UPDATE SET
requested_at = EXCLUDED.requested_at,
attempts = 0,
next_attempt_at = EXCLUDED.next_attempt_at,
last_error = '',
updated_at = EXCLUDED.updated_at
WHERE v2_dns_failover_eval_outbox.operation = 'evaluate'
  AND NOT EXISTS (
    SELECT 1 FROM v2_dns_failover_saga saga
    WHERE saga.group_id = v2_dns_failover_eval_outbox.group_id
  )`, outboxJSON, now)
		if err != nil {
			return DNSProbeReportResult{}, fmt.Errorf("persist DNS failover evaluation outbox: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return DNSProbeReportResult{}, fmt.Errorf("commit DNS probe result report: %w", err)
	}
	committed = true
	for _, groupID := range resultSummary.GroupIDs {
		logRows = append(logRows, dnsFailoverLogEntry{
			GroupID: groupID, Stage: "outbox", Level: "info", Outcome: "queued",
			Message: "evaluation queued after probe report",
			Details: map[string]any{
				"source": "probe_report", "probe_id": probeID,
			},
			CreatedAt: now,
		})
	}
	s.writeDNSFailoverLogsBestEffort(ctx, logRows...)
	if s.dnsFailoverEvaluator != nil && len(resultSummary.GroupIDs) > 0 {
		requestDNSFailoverEvaluationWake(ctx, s.dnsFailoverEvaluator, resultSummary.GroupIDs)
	}
	return resultSummary, nil
}

func marshalDNSProbeBatch(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func dnsProbeHeartbeatFresh(lastHeartbeat sql.NullInt64, now, offlineSec int64) bool {
	if !lastHeartbeat.Valid || offlineSec <= 0 || lastHeartbeat.Int64 < 0 || lastHeartbeat.Int64 > now {
		return false
	}
	const minInt64 = int64(-1 << 63)
	cutoff := minInt64
	if now >= minInt64+offlineSec {
		cutoff = now - offlineSec
	}
	return lastHeartbeat.Int64 >= cutoff
}

func requestDNSFailoverEvaluationWake(ctx context.Context, requester DNSFailoverEvaluationRequester, groupIDs []int64) {
	wakeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf("DNS failover evaluation wake panic: %v", recovered)
		}
	}()
	if err := requester.RequestDNSFailoverEvaluation(wakeCtx, append([]int64(nil), groupIDs...)); err != nil {
		log.Printf("DNS failover evaluation wake failed: %v", err)
	}
}

func dnsProbeStateWriteValues(result DNSProbeResult) dnsProbeStateValues {
	if result.Success != nil && *result.Success {
		var latency any
		if result.LatencyMS != nil {
			latency = *result.LatencyMS
		}
		return dnsProbeStateValues{
			Success:       1,
			Latency:       latency,
			Error:         "",
			SuccessStreak: 1,
			FailureStreak: 0,
		}
	}
	return dnsProbeStateValues{
		Success:       0,
		Latency:       nil,
		Error:         result.Error,
		SuccessStreak: 0,
		FailureStreak: 1,
	}
}

const maxDNSProbeStreak = int64(2147483647)

func saturatingDNSProbeStreakAdd(current, increment int64) int64 {
	if current < 0 {
		current = 0
	}
	if increment <= 0 || current >= maxDNSProbeStreak {
		if current > maxDNSProbeStreak {
			return maxDNSProbeStreak
		}
		return current
	}
	if increment > maxDNSProbeStreak-current {
		return maxDNSProbeStreak
	}
	return current + increment
}

func dnsProbeOfflineThreshold(configured sql.NullInt64) int64 {
	if configured.Valid && configured.Int64 > 0 {
		return configured.Int64
	}
	return defaultProbeOfflineSec
}

func normalizeDNSProbeResultsRequest(request *DNSProbeResultsRequest) error {
	if request == nil || request.Results == nil {
		return fmt.Errorf("%w: results array is required", ErrDNSProbeInvalidRequest)
	}
	if len(request.Results) > maxDNSProbeResultBatch {
		return fmt.Errorf("%w: results batch exceeds %d", ErrDNSProbeInvalidRequest, maxDNSProbeResultBatch)
	}
	for index := range request.Results {
		result := &request.Results[index]
		if err := validateDNSFailoverIdentifierText("result_id", result.ResultID); err != nil {
			return fmt.Errorf("%w: result %d: %s", ErrDNSProbeInvalidRequest, index, err)
		}
		if result.ResultID == "" || strings.TrimSpace(result.ResultID) != result.ResultID || len([]rune(result.ResultID)) > 128 {
			return fmt.Errorf("%w: result %d: result_id is invalid", ErrDNSProbeInvalidRequest, index)
		}
		if result.TargetID <= 0 {
			return fmt.Errorf("%w: result %d: target_id is invalid", ErrDNSProbeInvalidRequest, index)
		}
		if result.RunID < 0 {
			return fmt.Errorf("%w: result %d: run_id is invalid", ErrDNSProbeInvalidRequest, index)
		}
		if result.TargetVersion < 0 {
			return fmt.Errorf("%w: result %d: target_version is invalid", ErrDNSProbeInvalidRequest, index)
		}
		if result.Success == nil {
			return fmt.Errorf("%w: result %d: success is required", ErrDNSProbeInvalidRequest, index)
		}
		if result.LatencyMS != nil && (*result.LatencyMS < 0 || *result.LatencyMS > maxDNSProbeLatencyMS) {
			return fmt.Errorf("%w: result %d: latency_ms is invalid", ErrDNSProbeInvalidRequest, index)
		}
		if err := validateDNSFailoverIdentifierText("error", result.Error); err != nil {
			return fmt.Errorf("%w: result %d: %s", ErrDNSProbeInvalidRequest, index, err)
		}
		result.Error = strings.TrimSpace(result.Error)
		if len([]rune(result.Error)) > maxDNSProbeResultError {
			return fmt.Errorf("%w: result %d: error is too long", ErrDNSProbeInvalidRequest, index)
		}
		if *result.Success {
			if result.LatencyMS == nil {
				return fmt.Errorf("%w: result %d: successful result requires latency_ms", ErrDNSProbeInvalidRequest, index)
			}
			if result.Error != "" {
				return fmt.Errorf("%w: result %d: successful result cannot include error", ErrDNSProbeInvalidRequest, index)
			}
		} else {
			if result.LatencyMS != nil {
				return fmt.Errorf("%w: result %d: failed result cannot include latency_ms", ErrDNSProbeInvalidRequest, index)
			}
			if result.Error == "" {
				return fmt.Errorf("%w: result %d: failed result requires error", ErrDNSProbeInvalidRequest, index)
			}
		}
		result.ResolvedIP = strings.TrimSpace(result.ResolvedIP)
		if result.ResolvedIP != "" {
			addr, err := netip.ParseAddr(result.ResolvedIP)
			if err != nil || addr.Zone() != "" {
				return fmt.Errorf("%w: result %d: resolved_ip is invalid", ErrDNSProbeInvalidRequest, index)
			}
			result.ResolvedIP = addr.Unmap().String()
		}
	}
	return nil
}
