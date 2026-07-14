package admin

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
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
	ErrDNSProbeUnauthorized   = errors.New("dns probe unauthorized")
	ErrDNSProbeInvalidRequest = errors.New("invalid dns probe request")
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
	CheckHost        string `json:"check_host"`
	CheckPort        int64  `json:"check_port"`
	TCPTimeoutMS     int64  `json:"tcp_timeout_ms"`
	CheckIntervalSec int64  `json:"check_interval_sec"`
}

type DNSProbeResult struct {
	ResultID   string `json:"result_id"`
	TargetID   int64  `json:"target_id"`
	Success    *bool  `json:"success"`
	LatencyMS  *int64 `json:"latency_ms"`
	Error      string `json:"error"`
	ResolvedIP string `json:"resolved_ip"`
}

type DNSProbeResultsRequest struct {
	Results []DNSProbeResult `json:"results"`
}

type DNSProbeReportResult struct {
	Accepted     int     `json:"accepted"`
	Duplicates   int     `json:"duplicates"`
	PrewarmCount int64   `json:"prewarm_count"`
	GroupIDs     []int64 `json:"group_ids"`
}

type DNSProbeService interface {
	AuthenticateDNSProbe(ctx context.Context, rawSecret string) (DNSProbeIdentity, error)
	HeartbeatDNSProbe(ctx context.Context, probeID int64, request DNSProbeHeartbeatRequest) (DNSProbeHeartbeatResult, error)
	ListDNSProbeTasks(ctx context.Context, probeID int64) ([]DNSProbeTask, error)
	ReportDNSProbeResults(ctx context.Context, probeID int64, request DNSProbeResultsRequest) (DNSProbeReportResult, error)
}

type DNSFailoverEvaluationRequester interface {
	// RequestDNSFailoverEvaluation must hand the IDs to a durable/retryable
	// mechanism. Result ingestion calls it only after commit and deliberately
	// does not turn requester failures into probe retries.
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

	rows, err := s.db.QueryContext(ctx, `SELECT id, token_hash FROM v2_dns_probe WHERE enabled = 1 ORDER BY id ASC`)
	if err != nil {
		return DNSProbeIdentity{}, fmt.Errorf("authenticate DNS probe: %w", err)
	}
	defer rows.Close()

	want := sha256.Sum256([]byte(rawSecret))
	var matchedID int64
	for rows.Next() {
		var (
			probeID  int64
			tokenHex string
		)
		if err := rows.Scan(&probeID, &tokenHex); err != nil {
			return DNSProbeIdentity{}, fmt.Errorf("authenticate DNS probe: %w", err)
		}
		stored, err := hex.DecodeString(tokenHex)
		if err != nil || len(stored) != sha256.Size {
			continue
		}
		if subtle.ConstantTimeCompare(want[:], stored) == 1 {
			matchedID = probeID
		}
	}
	if err := rows.Err(); err != nil {
		return DNSProbeIdentity{}, fmt.Errorf("authenticate DNS probe: %w", err)
	}
	if matchedID <= 0 {
		return DNSProbeIdentity{}, ErrDNSProbeUnauthorized
	}
	return DNSProbeIdentity{ID: matchedID}, nil
}

func (s *DBService) HeartbeatDNSProbe(ctx context.Context, probeID int64, request DNSProbeHeartbeatRequest) (DNSProbeHeartbeatResult, error) {
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

	offlineSec := defaultProbeOfflineSec
	var configuredOffline sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT MIN(g.probe_offline_sec)
FROM v2_dns_failover_group_probe gp
JOIN v2_dns_failover_group g ON g.id = gp.group_id
WHERE gp.probe_id = $1 AND g.enabled = 1`, probeID).Scan(&configuredOffline)
	if err != nil {
		return DNSProbeHeartbeatResult{}, fmt.Errorf("load DNS probe offline threshold: %w", err)
	}
	if configuredOffline.Valid && configuredOffline.Int64 > 0 {
		offlineSec = configuredOffline.Int64
	}

	now := time.Now().Unix()
	reconnected := lastHeartbeat.Valid && now-lastHeartbeat.Int64 > offlineSec
	if reconnected {
		if _, err := tx.ExecContext(ctx, `UPDATE v2_dns_probe SET version = $2, arch = $3, public_ip = $4, last_heartbeat_at = $5, prewarm_count = 0, updated_at = $5 WHERE id = $1`, probeID, request.Version, request.Arch, request.PublicIP, now); err != nil {
			return DNSProbeHeartbeatResult{}, fmt.Errorf("update reconnected DNS probe heartbeat: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE v2_dns_probe_target_state SET warmed_up = 0, consecutive_success = 0, consecutive_failure = 0, updated_at = $2 WHERE probe_id = $1`, probeID, now); err != nil {
			return DNSProbeHeartbeatResult{}, fmt.Errorf("reset reconnected DNS probe state: %w", err)
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

	var enabled, prewarmCount int64
	err = tx.QueryRowContext(ctx, `SELECT enabled, prewarm_count FROM v2_dns_probe WHERE id = $1 FOR UPDATE`, probeID).Scan(&enabled, &prewarmCount)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && enabled != 1) {
		return DNSProbeReportResult{}, ErrDNSProbeUnauthorized
	}
	if err != nil {
		return DNSProbeReportResult{}, fmt.Errorf("load DNS probe report state: %w", err)
	}
	if prewarmCount < 0 {
		prewarmCount = 0
	}
	if prewarmCount > 3 {
		prewarmCount = 3
	}

	rows, err := tx.QueryContext(ctx, `SELECT t.id, t.group_id
FROM v2_dns_failover_group_probe gp
JOIN v2_dns_failover_group g ON g.id = gp.group_id
JOIN v2_dns_failover_target t ON t.group_id = g.id
WHERE gp.probe_id = $1 AND g.enabled = 1 AND t.enabled = 1
ORDER BY t.id ASC
FOR SHARE OF gp, g, t`, probeID)
	if err != nil {
		return DNSProbeReportResult{}, fmt.Errorf("load allowed DNS probe targets: %w", err)
	}
	allowedTargets := make(map[int64]int64)
	for rows.Next() {
		var targetID, groupID int64
		if err := rows.Scan(&targetID, &groupID); err != nil {
			_ = rows.Close()
			return DNSProbeReportResult{}, fmt.Errorf("load allowed DNS probe targets: %w", err)
		}
		allowedTargets[targetID] = groupID
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return DNSProbeReportResult{}, fmt.Errorf("load allowed DNS probe targets: %w", err)
	}
	if err := rows.Close(); err != nil {
		return DNSProbeReportResult{}, fmt.Errorf("close allowed DNS probe targets: %w", err)
	}
	for index, result := range request.Results {
		if _, ok := allowedTargets[result.TargetID]; !ok {
			return DNSProbeReportResult{}, fmt.Errorf("%w: result %d: target_id is invalid", ErrDNSProbeInvalidRequest, index)
		}
	}

	now := time.Now().Unix()
	resultSummary := DNSProbeReportResult{PrewarmCount: prewarmCount, GroupIDs: make([]int64, 0)}
	seenResultIDs := make(map[string]struct{}, len(request.Results))
	affectedGroups := make(map[int64]struct{})
	for _, result := range request.Results {
		if _, seen := seenResultIDs[result.ResultID]; seen {
			resultSummary.Duplicates++
			continue
		}
		seenResultIDs[result.ResultID] = struct{}{}

		var inboxID int64
		err := tx.QueryRowContext(ctx, `INSERT INTO v2_dns_probe_result_inbox (probe_id, target_id, result_id, created_at) VALUES ($1, $2, $3, $4) ON CONFLICT (probe_id, result_id) DO NOTHING RETURNING id`, probeID, result.TargetID, result.ResultID, now).Scan(&inboxID)
		if errors.Is(err, sql.ErrNoRows) {
			resultSummary.Duplicates++
			continue
		}
		if err != nil {
			return DNSProbeReportResult{}, fmt.Errorf("insert DNS probe result inbox: %w", err)
		}

		success := int64(0)
		successStreak := int64(0)
		failureStreak := int64(1)
		if *result.Success {
			success = 1
			successStreak = 1
			failureStreak = 0
		}
		var latency any
		if result.LatencyMS != nil {
			latency = *result.LatencyMS
		}
		warmedUp := int64(0)
		if prewarmCount >= 3 {
			warmedUp = 1
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO v2_dns_probe_target_state (
probe_id, target_id, last_success, last_latency_ms, last_error, last_resolved_ip,
consecutive_success, consecutive_failure, last_reported_at, warmed_up, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $9, $9)
ON CONFLICT (probe_id, target_id) DO UPDATE SET
last_success = EXCLUDED.last_success,
last_latency_ms = EXCLUDED.last_latency_ms,
last_error = EXCLUDED.last_error,
last_resolved_ip = EXCLUDED.last_resolved_ip,
consecutive_success = CASE WHEN EXCLUDED.last_success = 1 THEN v2_dns_probe_target_state.consecutive_success + 1 ELSE 0 END,
consecutive_failure = CASE WHEN EXCLUDED.last_success = 0 THEN v2_dns_probe_target_state.consecutive_failure + 1 ELSE 0 END,
last_reported_at = EXCLUDED.last_reported_at,
warmed_up = CASE WHEN EXCLUDED.warmed_up = 1 THEN 1 ELSE v2_dns_probe_target_state.warmed_up END,
updated_at = EXCLUDED.updated_at`, probeID, result.TargetID, success, latency, result.Error, result.ResolvedIP, successStreak, failureStreak, now, warmedUp)
		if err != nil {
			return DNSProbeReportResult{}, fmt.Errorf("upsert DNS probe target state: %w", err)
		}
		resultSummary.Accepted++
		affectedGroups[allowedTargets[result.TargetID]] = struct{}{}
	}

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

	if err := tx.Commit(); err != nil {
		return DNSProbeReportResult{}, fmt.Errorf("commit DNS probe result report: %w", err)
	}
	committed = true
	if s.dnsFailoverEvaluator != nil && len(resultSummary.GroupIDs) > 0 {
		_ = s.dnsFailoverEvaluator.RequestDNSFailoverEvaluation(ctx, append([]int64(nil), resultSummary.GroupIDs...))
	}
	return resultSummary, nil
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
		if result.Success == nil {
			return fmt.Errorf("%w: result %d: success is required", ErrDNSProbeInvalidRequest, index)
		}
		if *result.Success && result.LatencyMS == nil {
			return fmt.Errorf("%w: result %d: successful result requires latency_ms", ErrDNSProbeInvalidRequest, index)
		}
		if result.LatencyMS != nil && (*result.LatencyMS < 0 || *result.LatencyMS > maxDNSProbeLatencyMS) {
			return fmt.Errorf("%w: result %d: latency_ms is invalid", ErrDNSProbeInvalidRequest, index)
		}
		if err := validateDNSFailoverText("error", result.Error); err != nil {
			return fmt.Errorf("%w: result %d: %s", ErrDNSProbeInvalidRequest, index, err)
		}
		if len([]rune(result.Error)) > maxDNSProbeResultError {
			return fmt.Errorf("%w: result %d: error is too long", ErrDNSProbeInvalidRequest, index)
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
