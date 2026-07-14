package admin

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"forest/go-api/internal/dnspod"
)

const (
	dnsFailoverQueueName                 = "dns_failover"
	dnsFailoverQueueJobName              = "dns-failover-drain"
	dnsFailoverDefaultTick               = time.Second
	dnsFailoverRetryBase                 = 5 * time.Second
	dnsFailoverRetryMaximum              = 5 * time.Minute
	dnsFailoverDNSPodTimeout             = 10 * time.Second
	dnsFailoverRecoveryTimeout           = 3 * time.Second
	dnsFailoverRecoveryAttempts          = 3
	dnsFailoverEvaluationBatch           = 16
	dnsFailoverNotificationBatch         = 32
	dnsFailoverMaxErrorLength            = 1024
	dnsFailoverOperationEvaluate         = "evaluate"
	dnsFailoverOperationManual           = "manual"
	dnsFailoverOperationReconcile        = "reconcile"
	dnsFailoverNotificationLockKey int64 = -7_642_309_017
	dnsFailoverNotificationLease         = 30 * time.Second
)

type dnsFailoverOutboxRow struct {
	ID             int64
	GroupID        int64
	Operation      string
	TargetID       sql.NullInt64
	SourceTargetID sql.NullInt64
	RequestedAt    int64
	Attempts       int
	LastError      string
}

type dnsFailoverWorkerSnapshot struct {
	Rule                DNSFailoverRuleRecord
	Targets             []DNSFailoverTargetRecord
	Probes              []dnsFailoverProbeSnapshot
	ProbeIDs            []int64
	States              []dnsFailoverProbeTargetSnapshot
	StateFacts          []map[string]any
	ActiveIncidentType  string
	ActiveIncidentSince sql.NullInt64
}

type DNSFailoverNotifier interface {
	NotifyAdmins(context.Context, string, bool) error
}

var ErrDNSFailoverNotifierUnavailable = errors.New("DNS failover notifier unavailable")
var ErrDNSFailoverAutomationStopped = errors.New("DNS failover automation stopped")

var _ DNSFailoverEvaluationRequester = (*DBService)(nil)

func (s *DBService) WithDNSFailoverNotifier(notifier DNSFailoverNotifier) *DBService {
	if s != nil {
		s.dnsFailoverNotifier = notifier
	}
	return s
}

// RequestDNSFailoverEvaluation is deliberately only a wake hint. Probe result
// ingestion has already persisted the affected group in the evaluation outbox.
func (s *DBService) RequestDNSFailoverEvaluation(_ context.Context, _ []int64) error {
	if s == nil {
		return errors.New("DNS failover queue unavailable")
	}

	s.dnsFailoverWakeMu.Lock()
	if s.dnsFailoverStopping {
		s.dnsFailoverWakeMu.Unlock()
		return ErrDNSFailoverAutomationStopped
	}
	if s.jobs == nil {
		s.dnsFailoverWakeMu.Unlock()
		return errors.New("DNS failover queue unavailable")
	}
	if s.dnsFailoverWakeQueued {
		s.dnsFailoverWakeMu.Unlock()
		return nil
	}
	s.dnsFailoverWakeQueued = true
	s.dnsFailoverJobWG.Add(1)
	workerCtx := s.dnsFailoverWorkerCtx
	s.dnsFailoverWakeMu.Unlock()

	err := s.jobs.Enqueue(dnsFailoverQueueName, dnsFailoverQueueJobName, func(jobCtx context.Context) error {
		defer s.dnsFailoverJobWG.Done()
		defer func() {
			s.dnsFailoverWakeMu.Lock()
			s.dnsFailoverWakeQueued = false
			s.dnsFailoverWakeMu.Unlock()
		}()
		cycleCtx := jobCtx
		if workerCtx != nil {
			var cancel context.CancelFunc
			cycleCtx, cancel = context.WithCancel(workerCtx)
			stopQueueCancellation := context.AfterFunc(jobCtx, cancel)
			defer stopQueueCancellation()
			defer cancel()
		}
		return s.runDNSFailoverCycle(cycleCtx)
	})
	if err != nil {
		s.dnsFailoverJobWG.Done()
		s.dnsFailoverWakeMu.Lock()
		s.dnsFailoverWakeQueued = false
		s.dnsFailoverWakeMu.Unlock()
		return err
	}
	return nil
}

func (s *DBService) StartDNSFailoverAutomation(ctx context.Context) {
	if s == nil || ctx == nil {
		return
	}
	s.dnsFailoverStartOnce.Do(func() {
		workerCtx, cancel := context.WithCancel(ctx)
		s.dnsFailoverWakeMu.Lock()
		s.dnsFailoverStopping = false
		s.dnsFailoverWorkerCtx = workerCtx
		s.dnsFailoverWorkerCancel = cancel
		s.dnsFailoverWakeMu.Unlock()
		interval := s.dnsFailoverTickInterval
		if interval <= 0 {
			interval = dnsFailoverDefaultTick
		}
		s.dnsFailoverWorkerWG.Add(1)
		go func() {
			defer s.dnsFailoverWorkerWG.Done()
			runCycle := func() {
				if err := s.runDNSFailoverCycle(workerCtx); err != nil && workerCtx.Err() == nil {
					s.logDNSFailoverWorkerError("DNS failover automation cycle failed: %v", err)
				}
			}
			runCycle()
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-workerCtx.Done():
					return
				case <-ticker.C:
					runCycle()
				}
			}
		}()
	})
}

func (s *DBService) StopDNSFailoverAutomation(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.dnsFailoverWakeMu.Lock()
	s.dnsFailoverStopping = true
	cancel := s.dnsFailoverWorkerCancel
	s.dnsFailoverWorkerCtx = nil
	s.dnsFailoverWorkerCancel = nil
	s.dnsFailoverWakeMu.Unlock()
	if cancel != nil {
		cancel()
	}
	done := make(chan struct{})
	go func() {
		s.dnsFailoverWorkerWG.Wait()
		s.dnsFailoverJobWG.Wait()
		close(done)
	}()
	if ctx == nil {
		<-done
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("stop DNS failover automation: %w", ctx.Err())
	}
}

func (s *DBService) logDNSFailoverWorkerError(format string, args ...any) {
	if s.dnsFailoverLogf != nil {
		s.dnsFailoverLogf(format, args...)
		return
	}
	log.Printf(format, args...)
}

func (s *DBService) runDNSFailoverCycle(ctx context.Context) error {
	if s.dnsFailoverCycle != nil {
		return s.dnsFailoverCycle(ctx)
	}
	if s.db == nil {
		return ErrUnavailable
	}
	if err := s.ensureDNSFailoverSchema(ctx); err != nil {
		return err
	}
	if err := s.drainDNSFailoverSagaOutbox(ctx, dnsFailoverEvaluationBatch); err != nil {
		return err
	}
	now := time.Now().Unix()
	if err := s.seedDueDNSFailoverEvaluations(ctx, now); err != nil {
		return err
	}
	if err := s.drainDNSFailoverEvaluationOutbox(ctx, dnsFailoverEvaluationBatch); err != nil {
		return err
	}
	return s.drainPendingDNSFailoverNotifications(ctx, dnsFailoverNotificationBatch)
}

func (s *DBService) drainDNSFailoverSagaOutbox(ctx context.Context, limit int) error {
	if s == nil || s.db == nil || limit <= 0 {
		return nil
	}
	for range limit {
		processed, err := s.processNextDNSFailoverSaga(ctx)
		if err != nil {
			return err
		}
		if !processed {
			return nil
		}
	}
	return nil
}

func (s *DBService) seedDueDNSFailoverEvaluations(ctx context.Context, now int64) error {
	if s == nil || s.db == nil {
		return ErrUnavailable
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO v2_dns_failover_eval_outbox (
group_id, operation, target_id, source_target_id, requested_at, attempts, next_attempt_at, last_error, created_at, updated_at
)
SELECT g.id, 'evaluate', NULL, NULL, $1, 0, $1, '', $1, $1
FROM v2_dns_failover_group g
WHERE g.enabled = 1
  AND (g.last_evaluated_at IS NULL OR g.last_evaluated_at <= $1 - g.check_interval_sec)
ON CONFLICT (group_id) DO NOTHING`, now)
	if err != nil {
		return fmt.Errorf("seed due DNS failover evaluations: %w", err)
	}
	return nil
}

func (s *DBService) drainDNSFailoverEvaluationOutbox(ctx context.Context, limit int) error {
	if s == nil || s.db == nil || limit <= 0 {
		return nil
	}
	for range limit {
		processed, err := s.processNextDNSFailoverEvaluation(ctx)
		if err != nil {
			return err
		}
		if !processed {
			return nil
		}
	}
	return nil
}

func loadDNSFailoverWorkerSnapshot(ctx context.Context, tx *sql.Tx, groupID, now int64) (dnsFailoverWorkerSnapshot, error) {
	var (
		snapshot      dnsFailoverWorkerSnapshot
		weight        sql.NullInt64
		currentTarget sql.NullInt64
		enabled       int64
		autoFailback  int64
		lastSwitch    sql.NullInt64
	)
	err := tx.QueryRowContext(ctx, `SELECT id, name, domain_id, domain, record_id, subdomain, record_line_id, record_line_name,
ttl, mx, weight, current_target_id, enabled, auto_failback, check_interval_sec, tcp_timeout_ms, failure_threshold,
success_threshold, single_probe_failure_threshold, single_probe_success_threshold, probe_offline_sec, cooldown_sec,
last_switch_at, last_switch_reason, active_incident_type, active_incident_since, created_at, updated_at
FROM v2_dns_failover_group
WHERE id = $1
FOR UPDATE`, groupID).Scan(
		&snapshot.Rule.ID, &snapshot.Rule.Name, &snapshot.Rule.DomainID, &snapshot.Rule.Domain, &snapshot.Rule.RecordID,
		&snapshot.Rule.Subdomain, &snapshot.Rule.RecordLineID, &snapshot.Rule.RecordLineName, &snapshot.Rule.TTL, &snapshot.Rule.MX,
		&weight, &currentTarget, &enabled, &autoFailback, &snapshot.Rule.CheckIntervalSec, &snapshot.Rule.TCPTimeoutMS,
		&snapshot.Rule.FailureThreshold, &snapshot.Rule.SuccessThreshold, &snapshot.Rule.SingleProbeFailureThreshold,
		&snapshot.Rule.SingleProbeSuccessThreshold, &snapshot.Rule.ProbeOfflineSec, &snapshot.Rule.CooldownSec, &lastSwitch,
		&snapshot.Rule.LastSwitchReason, &snapshot.ActiveIncidentType, &snapshot.ActiveIncidentSince, &snapshot.Rule.CreatedAt, &snapshot.Rule.UpdatedAt,
	)
	if err != nil {
		return dnsFailoverWorkerSnapshot{}, fmt.Errorf("lock DNS failover group: %w", err)
	}
	snapshot.Rule.Weight = dnsNullInt64Pointer(weight)
	snapshot.Rule.CurrentTargetID = dnsNullInt64Pointer(currentTarget)
	snapshot.Rule.Enabled = enabled != 0
	snapshot.Rule.AutoFailback = autoFailback != 0
	snapshot.Rule.LastSwitchAt = dnsNullInt64Pointer(lastSwitch)

	targetRows, err := tx.QueryContext(ctx, `SELECT id, group_id, sort, name, dns_type, dns_value, check_host, check_port, enabled, created_at, updated_at
FROM v2_dns_failover_target
WHERE group_id = $1
ORDER BY sort ASC, id ASC
FOR UPDATE`, groupID)
	if err != nil {
		return dnsFailoverWorkerSnapshot{}, fmt.Errorf("load DNS failover targets: %w", err)
	}
	for targetRows.Next() {
		var target DNSFailoverTargetRecord
		var targetEnabled int64
		if err := targetRows.Scan(&target.ID, &target.GroupID, &target.Sort, &target.Name, &target.DNSType, &target.DNSValue,
			&target.CheckHost, &target.CheckPort, &targetEnabled, &target.CreatedAt, &target.UpdatedAt); err != nil {
			targetRows.Close()
			return dnsFailoverWorkerSnapshot{}, fmt.Errorf("scan DNS failover target: %w", err)
		}
		target.Enabled = targetEnabled != 0
		snapshot.Targets = append(snapshot.Targets, target)
	}
	if err := targetRows.Close(); err != nil {
		return dnsFailoverWorkerSnapshot{}, fmt.Errorf("close DNS failover targets: %w", err)
	}
	if err := targetRows.Err(); err != nil {
		return dnsFailoverWorkerSnapshot{}, fmt.Errorf("iterate DNS failover targets: %w", err)
	}

	probeRows, err := tx.QueryContext(ctx, `SELECT p.id, p.last_heartbeat_at, p.prewarm_count
FROM v2_dns_failover_group_probe gp
JOIN v2_dns_probe p ON p.id = gp.probe_id AND p.enabled = 1
WHERE gp.group_id = $1
ORDER BY p.id ASC`, groupID)
	if err != nil {
		return dnsFailoverWorkerSnapshot{}, fmt.Errorf("load DNS failover probes: %w", err)
	}
	for probeRows.Next() {
		var probeID, prewarm int64
		var heartbeat sql.NullInt64
		if err := probeRows.Scan(&probeID, &heartbeat, &prewarm); err != nil {
			probeRows.Close()
			return dnsFailoverWorkerSnapshot{}, fmt.Errorf("scan DNS failover probe: %w", err)
		}
		snapshot.ProbeIDs = append(snapshot.ProbeIDs, probeID)
		snapshot.Probes = append(snapshot.Probes, dnsFailoverProbeSnapshot{ID: probeID, Online: dnsFailoverProbeOnline(heartbeat, prewarm, now, snapshot.Rule.ProbeOfflineSec)})
	}
	if err := probeRows.Close(); err != nil {
		return dnsFailoverWorkerSnapshot{}, fmt.Errorf("close DNS failover probes: %w", err)
	}
	if err := probeRows.Err(); err != nil {
		return dnsFailoverWorkerSnapshot{}, fmt.Errorf("iterate DNS failover probes: %w", err)
	}

	stateRows, err := tx.QueryContext(ctx, `SELECT s.probe_id, s.target_id, s.consecutive_success, s.consecutive_failure, s.last_resolved_ip, s.last_reported_at
FROM v2_dns_probe_target_state s
JOIN v2_dns_failover_target t ON t.id = s.target_id
JOIN v2_dns_failover_group_probe gp ON gp.probe_id = s.probe_id AND gp.group_id = t.group_id
WHERE s.warmed_up = 1 AND t.group_id = $1
ORDER BY s.probe_id ASC, s.target_id ASC`, groupID)
	if err != nil {
		return dnsFailoverWorkerSnapshot{}, fmt.Errorf("load DNS failover states: %w", err)
	}
	for stateRows.Next() {
		var probeID, targetID, success, failure int64
		var resolvedIP string
		var lastReportedAt sql.NullInt64
		if err := stateRows.Scan(&probeID, &targetID, &success, &failure, &resolvedIP, &lastReportedAt); err != nil {
			stateRows.Close()
			return dnsFailoverWorkerSnapshot{}, fmt.Errorf("scan DNS failover state: %w", err)
		}
		fresh := dnsFailoverProbeStateFresh(lastReportedAt, now, snapshot.Rule.CheckIntervalSec, snapshot.Rule.TCPTimeoutMS, snapshot.Rule.ProbeOfflineSec)
		var reportedAt any
		if lastReportedAt.Valid {
			reportedAt = lastReportedAt.Int64
		}
		snapshot.StateFacts = append(snapshot.StateFacts, map[string]any{
			"probe_id": probeID, "target_id": targetID, "success_streak": success, "failure_streak": failure, "resolved_ip": resolvedIP,
			"last_reported_at": reportedAt, "stale": !fresh,
		})
		if fresh {
			snapshot.States = append(snapshot.States, dnsFailoverProbeTargetSnapshot{
				ProbeID: probeID, TargetID: targetID, SuccessStreak: safeInt(success), FailureStreak: safeInt(failure),
			})
		}
	}
	if err := stateRows.Close(); err != nil {
		return dnsFailoverWorkerSnapshot{}, fmt.Errorf("close DNS failover states: %w", err)
	}
	if err := stateRows.Err(); err != nil {
		return dnsFailoverWorkerSnapshot{}, fmt.Errorf("iterate DNS failover states: %w", err)
	}
	snapshot.Rule.Targets = snapshot.Targets
	snapshot.Rule.ProbeIDs = snapshot.ProbeIDs
	return snapshot, nil
}

func buildDNSFailoverDecisionInput(snapshot dnsFailoverWorkerSnapshot, now int64) dnsFailoverDecisionInput {
	targets := make([]dnsFailoverTargetSnapshot, 0, len(snapshot.Targets))
	for _, target := range snapshot.Targets {
		targets = append(targets, dnsFailoverTargetSnapshot{ID: target.ID, Sort: safeInt(target.Sort), Enabled: target.Enabled})
	}
	var currentTargetID int64
	if snapshot.Rule.CurrentTargetID != nil {
		currentTargetID = *snapshot.Rule.CurrentTargetID
	}
	var lastSwitch time.Time
	if snapshot.Rule.LastSwitchAt != nil {
		lastSwitch = time.Unix(*snapshot.Rule.LastSwitchAt, 0)
	}
	return dnsFailoverDecisionInput{
		Now: time.Unix(now, 0), CurrentTargetID: currentTargetID, AutoFailback: snapshot.Rule.AutoFailback,
		LastSwitchAt: lastSwitch, Cooldown: safeSecondsDuration(snapshot.Rule.CooldownSec),
		FailureThreshold: safeInt(snapshot.Rule.FailureThreshold), SuccessThreshold: safeInt(snapshot.Rule.SuccessThreshold),
		SingleProbeFailureThreshold: safeInt(snapshot.Rule.SingleProbeFailureThreshold), SingleProbeSuccessThreshold: safeInt(snapshot.Rule.SingleProbeSuccessThreshold),
		Targets: targets, Probes: snapshot.Probes, States: snapshot.States,
	}
}

func enabledDNSFailoverTarget(targets []DNSFailoverTargetRecord, targetID *int64) (DNSFailoverTargetRecord, bool) {
	if targetID == nil || *targetID <= 0 {
		return DNSFailoverTargetRecord{}, false
	}
	for _, target := range targets {
		if target.ID == *targetID && target.Enabled {
			return target, true
		}
	}
	return DNSFailoverTargetRecord{}, false
}

func dnsFailoverTargetByID(targets []DNSFailoverTargetRecord, targetID int64) (DNSFailoverTargetRecord, bool) {
	for _, target := range targets {
		if target.ID == targetID {
			return target, true
		}
	}
	return DNSFailoverTargetRecord{}, false
}

func dnsFailoverTargetDetails(target DNSFailoverTargetRecord) map[string]any {
	return map[string]any{
		"id": target.ID, "name": target.Name, "dns_type": target.DNSType, "dns_value": target.DNSValue,
	}
}

func recordDNSFailoverIncident(ctx context.Context, tx *sql.Tx, snapshot dnsFailoverWorkerSnapshot, eventType string, target *DNSFailoverTargetRecord, message string, details map[string]any, now int64) error {
	eventType = strings.TrimSpace(eventType)
	if eventType == "" || snapshot.ActiveIncidentType == eventType {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE v2_dns_failover_group
SET active_incident_type = $2, active_incident_since = $3
WHERE id = $1`, snapshot.Rule.ID, eventType, now); err != nil {
		return fmt.Errorf("set DNS failover active incident: %w", err)
	}
	var targetID *int64
	if target != nil {
		targetID = &target.ID
	}
	return insertDNSFailoverEvent(ctx, tx, snapshot.Rule.ID, targetID, eventType, message, details, now)
}

func clearDNSFailoverIncident(ctx context.Context, tx *sql.Tx, snapshot dnsFailoverWorkerSnapshot, target *DNSFailoverTargetRecord, message string, details map[string]any, now int64) error {
	if snapshot.ActiveIncidentType == "" {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE v2_dns_failover_group
SET active_incident_type = '', active_incident_since = NULL
WHERE id = $1 AND active_incident_type = $2`, snapshot.Rule.ID, snapshot.ActiveIncidentType); err != nil {
		return fmt.Errorf("clear DNS failover active incident: %w", err)
	}
	var targetID *int64
	if target != nil {
		targetID = &target.ID
	}
	return insertDNSFailoverEvent(ctx, tx, snapshot.Rule.ID, targetID, "recovered", message, details, now)
}

func dnsFailoverCurrentTargetHealthy(snapshot dnsFailoverWorkerSnapshot, targetID int64) bool {
	available := dnsFailoverDecisionAvailableProbeIDList(snapshot.Probes, snapshot.States, targetID)
	if len(available) == 0 {
		return false
	}
	threshold := safeInt(snapshot.Rule.SuccessThreshold)
	if len(available) == 1 {
		threshold = safeInt(snapshot.Rule.SingleProbeSuccessThreshold)
	}
	states := make(map[int64]dnsFailoverProbeTargetSnapshot, len(snapshot.States))
	for _, state := range snapshot.States {
		if state.TargetID == targetID {
			states[state.ProbeID] = state
		}
	}
	for _, probeID := range available {
		state, ok := states[probeID]
		if !ok || state.SuccessStreak < threshold {
			return false
		}
	}
	return true
}

func insertDNSFailoverEvent(ctx context.Context, tx *sql.Tx, groupID int64, targetID *int64, eventType, message string, details map[string]any, now int64) error {
	rawDetails, err := json.Marshal(details)
	if err != nil {
		return fmt.Errorf("encode DNS failover event details: %w", err)
	}
	dedupeKey := strconv.FormatInt(groupID, 10) + ":" + eventType + ":" + strconv.FormatInt(now, 10)
	_, err = tx.ExecContext(ctx, `INSERT INTO v2_dns_failover_event (
group_id, probe_id, target_id, event_type, message, details, dedupe_key, notified_at, created_at
) VALUES ($1, NULL, $2, $3, $4, $5, $6, NULL, $7)`, groupID, nullableInt64(targetID), eventType, message, string(rawDetails), dedupeKey, now)
	if err != nil {
		return fmt.Errorf("insert DNS failover event: %w", err)
	}
	return nil
}

func ackDNSFailoverEvaluation(ctx context.Context, tx *sql.Tx, outbox dnsFailoverOutboxRow, now int64) error {
	if _, err := tx.ExecContext(ctx, `UPDATE v2_dns_failover_group SET last_evaluated_at = $2, updated_at = $2 WHERE id = $1`, outbox.GroupID, now); err != nil {
		return fmt.Errorf("mark DNS failover evaluated: %w", err)
	}
	return deleteDNSFailoverOutboxVersion(ctx, tx, outbox)
}

func deleteDNSFailoverOutboxVersion(ctx context.Context, tx *sql.Tx, outbox dnsFailoverOutboxRow) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM v2_dns_failover_eval_outbox WHERE id = $1 AND requested_at = $2`, outbox.ID, outbox.RequestedAt); err != nil {
		return fmt.Errorf("ack DNS failover outbox: %w", err)
	}
	return nil
}

func (s *DBService) dnsFailoverClient() (dnspodAPI, error) {
	if s.dnsFailoverAPI != nil {
		return s.dnsFailoverAPI, nil
	}
	return s.dnspodClient()
}

func dnsFailoverEventDetails(snapshot dnsFailoverWorkerSnapshot, oldTarget, newTarget *DNSFailoverTargetRecord, reason, errorText, requestID string, now int64) map[string]any {
	decisionCurrentTargetID := int64(0)
	if oldTarget != nil {
		decisionCurrentTargetID = oldTarget.ID
	} else if snapshot.Rule.CurrentTargetID != nil {
		decisionCurrentTargetID = *snapshot.Rule.CurrentTargetID
	}
	decisionProbeIDs := dnsFailoverDecisionAvailableProbeIDList(snapshot.Probes, snapshot.States, decisionCurrentTargetID)
	details := map[string]any{
		"reason": reason, "probe_ids": decisionProbeIDs, "bound_probe_ids": append([]int64(nil), snapshot.ProbeIDs...), "states": snapshot.StateFacts,
		"request_id": requestID, "error": errorText, "time": time.Unix(now, 0).UTC().Format(time.RFC3339),
	}
	if oldTarget != nil {
		details["old_target"] = map[string]any{"id": oldTarget.ID, "name": oldTarget.Name, "dns_type": oldTarget.DNSType, "dns_value": oldTarget.DNSValue}
	}
	if newTarget != nil {
		details["new_target"] = map[string]any{"id": newTarget.ID, "name": newTarget.Name, "dns_type": newTarget.DNSType, "dns_value": newTarget.DNSValue}
	}
	return details
}

func formatDNSFailoverIncidentNotification(rule DNSFailoverRuleRecord, eventType, reason string, probeIDs []int64, now int64) string {
	probeParts := make([]string, 0, len(probeIDs))
	for _, probeID := range probeIDs {
		probeParts = append(probeParts, strconv.FormatInt(probeID, 10))
	}
	fqdn := strings.TrimSpace(rule.Domain)
	if subdomain := strings.TrimSpace(rule.Subdomain); subdomain != "" && subdomain != "@" {
		fqdn = subdomain + "." + fqdn
	}
	currentTarget := "未配置"
	checkPort := "-"
	if target, ok := enabledDNSFailoverTarget(rule.Targets, rule.CurrentTargetID); ok {
		currentTarget = fmt.Sprintf("%s [%s %s]", target.Name, target.DNSType, target.DNSValue)
		checkPort = strconv.FormatInt(target.CheckPort, 10)
	}
	return fmt.Sprintf("【DNS 故障转移】规则：%s（#%d）\n记录：%s\n当前：%s\n检测端口：%s\n状态：%s\n探针：%s\n原因：%s\n时间：%s",
		rule.Name, rule.ID, fqdn, currentTarget, checkPort, eventType, strings.Join(probeParts, ", "), reason, time.Unix(now, 0).UTC().Format(time.RFC3339))
}

func onlineDNSFailoverProbeIDs(probes []dnsFailoverProbeSnapshot) []int64 {
	ids := make([]int64, 0, len(probes))
	for _, probe := range probes {
		if probe.Online {
			ids = append(ids, probe.ID)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func safeInt(value int64) int {
	if value > int64(^uint(0)>>1) {
		return int(^uint(0) >> 1)
	}
	if strconv.IntSize == 32 && value < math.MinInt32 {
		return math.MinInt32
	}
	return int(value)
}

func safeSecondsDuration(seconds int64) time.Duration {
	if seconds <= 0 {
		return 0
	}
	if seconds > int64(math.MaxInt64/time.Second) {
		return time.Duration(math.MaxInt64)
	}
	return time.Duration(seconds) * time.Second
}

func saturatingUnixAdd(now int64, delay time.Duration) int64 {
	seconds := int64(delay / time.Second)
	if seconds > 0 && now > math.MaxInt64-seconds {
		return math.MaxInt64
	}
	return now + seconds
}

func truncateDNSFailoverError(err error) string {
	if err == nil {
		return ""
	}
	value := []rune(strings.TrimSpace(err.Error()))
	if len(value) > dnsFailoverMaxErrorLength {
		value = value[:dnsFailoverMaxErrorLength]
	}
	return string(value)
}

func (s *DBService) ManualSwitchDNSFailoverTarget(ctx context.Context, groupID, targetID int64) error {
	if groupID <= 0 || targetID <= 0 {
		return errors.New("故障转移规则或目标 ID 无效")
	}
	if err := s.ensureDNSFailoverSchema(ctx); err != nil {
		return err
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("open manual DNS failover connection: %w", err)
	}
	defer conn.Close()
	var locked bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, groupID).Scan(&locked); err != nil {
		return fmt.Errorf("lock manual DNS failover group: %w", err)
	}
	if !locked {
		return errors.New("DNS 状态正在恢复或执行，请稍后再手动切换")
	}
	defer releaseDNSFailoverSessionLock(conn, groupID)

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin manual DNS failover request: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	var sagaExists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM v2_dns_failover_saga WHERE group_id = $1 AND phase = 'prepared')`, groupID).Scan(&sagaExists); err != nil {
		return fmt.Errorf("check DNS failover recovery state: %w", err)
	}
	if sagaExists {
		return errors.New("DNS 状态正在恢复，请稍后再手动切换")
	}
	now := time.Now().Unix()
	var outboxID, requestedAt int64
	err = tx.QueryRowContext(ctx, `INSERT INTO v2_dns_failover_eval_outbox (
group_id, operation, target_id, source_target_id, requested_at, attempts, next_attempt_at, last_error, created_at, updated_at
) VALUES ($1, 'manual', $2, NULL, $3, 0, $3, '', $3, $3)
ON CONFLICT (group_id) DO UPDATE SET
operation = 'manual', target_id = EXCLUDED.target_id, source_target_id = NULL,
requested_at = EXCLUDED.requested_at, attempts = 0, next_attempt_at = EXCLUDED.next_attempt_at,
last_error = '', updated_at = EXCLUDED.updated_at
WHERE v2_dns_failover_eval_outbox.operation <> 'reconcile'
RETURNING id, requested_at`, groupID, targetID, now).Scan(&outboxID, &requestedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("DNS 状态正在恢复，请稍后再手动切换")
	}
	if err != nil {
		return fmt.Errorf("persist manual DNS failover operation: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit manual DNS failover request: %w", err)
	}
	committed = true
	_, err = s.processClaimedDNSFailoverEvaluation(ctx, conn, outboxID, now)
	return err
}

func buildDNSFailoverMutation(rule DNSFailoverRuleRecord, target DNSFailoverTargetRecord) dnspod.RecordMutationRequest {
	return dnspod.RecordMutationRequest{
		Domain:       rule.Domain,
		DomainID:     rule.DomainID,
		RecordID:     rule.RecordID,
		SubDomain:    rule.Subdomain,
		RecordType:   target.DNSType,
		RecordLine:   rule.RecordLineName,
		RecordLineID: rule.RecordLineID,
		Value:        target.DNSValue,
		TTL:          rule.TTL,
		MX:           rule.MX,
		Weight:       rule.Weight,
	}
}

func dnsFailoverProbeOnline(lastHeartbeat sql.NullInt64, prewarmCount, now, offlineSec int64) bool {
	return prewarmCount >= 3 && dnsProbeHeartbeatFresh(lastHeartbeat, now, offlineSec)
}

func isDNSFailoverIncident(eventType string) bool {
	switch eventType {
	case "all_probes_offline", "probe_disagreement", "no_healthy_target", "config_error":
		return true
	default:
		return false
	}
}

func dnsFailoverRetryDelay(attempts int) time.Duration {
	if attempts < 0 {
		attempts = 0
	}
	if attempts >= 6 {
		return dnsFailoverRetryMaximum
	}
	multiplier := int64(1) << attempts
	seconds := int64(dnsFailoverRetryBase/time.Second) * multiplier
	if seconds > int64(dnsFailoverRetryMaximum/time.Second) || seconds > math.MaxInt64/int64(time.Second) {
		return dnsFailoverRetryMaximum
	}
	return time.Duration(seconds) * time.Second
}

func (s *DBService) drainPendingDNSFailoverNotifications(ctx context.Context, limit int) error {
	if s == nil || s.db == nil || limit <= 0 {
		return nil
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("open DNS failover notification connection: %w", err)
	}
	defer conn.Close()
	var locked bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, dnsFailoverNotificationLockKey).Scan(&locked); err != nil {
		return fmt.Errorf("lock DNS failover notification delivery: %w", err)
	}
	if !locked {
		return nil
	}
	defer releaseDNSFailoverSessionLock(conn, dnsFailoverNotificationLockKey)
	for range limit {
		now := time.Now().Unix()
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin DNS failover notification transaction: %w", err)
		}
		var eventID int64
		var message string
		var attempts int
		err = tx.QueryRowContext(ctx, `SELECT id, message, notify_attempts
FROM v2_dns_failover_event e
WHERE e.notified_at IS NULL
  AND e.notify_next_attempt_at <= $1
  AND (e.notify_claim_token = '' OR e.notify_claimed_at IS NULL OR e.notify_claimed_at <= $2)
  AND NOT EXISTS (
    SELECT 1 FROM v2_dns_failover_event older
    WHERE older.notified_at IS NULL AND older.id < e.id
  )
ORDER BY id ASC
LIMIT 1
FOR UPDATE SKIP LOCKED`, now, saturatingUnixAdd(now, -dnsFailoverNotificationLease)).Scan(&eventID, &message, &attempts)
		if errors.Is(err, sql.ErrNoRows) {
			_ = tx.Rollback()
			return nil
		}
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("claim DNS failover notification: %w", err)
		}
		token, err := newDNSFailoverNotificationClaimToken()
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		claimResult, err := tx.ExecContext(ctx, `UPDATE v2_dns_failover_event
SET notify_claim_token = $2, notify_claimed_at = $3, notify_attempts = notify_attempts + 1, notify_next_attempt_at = $4
WHERE id = $1 AND notified_at IS NULL`, eventID, token, now, saturatingUnixAdd(now, dnsFailoverNotificationLease))
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("claim DNS failover notification: %w", err)
		}
		claimed, err := claimResult.RowsAffected()
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("read DNS failover notification claim result: %w", err)
		}
		if claimed != 1 {
			_ = tx.Rollback()
			return fmt.Errorf("claim DNS failover notification: expected one updated row, got %d", claimed)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit DNS failover notification claim: %w", err)
		}

		notifyErr := ErrDNSFailoverNotifierUnavailable
		if s.dnsFailoverNotifier != nil {
			notifyErr = s.dnsFailoverNotifier.NotifyAdmins(ctx, message, true)
		}
		if notifyErr != nil {
			if err := releaseDNSFailoverNotificationClaim(ctx, conn, eventID, token, attempts+1, notifyErr); err != nil {
				return errors.Join(fmt.Errorf("notify DNS failover event: %w", notifyErr), err)
			}
			return fmt.Errorf("notify DNS failover event: %w", notifyErr)
		}

		ackTx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin DNS failover notification ack: %w", err)
		}
		ackNow := time.Now().Unix()
		ackResult, err := ackTx.ExecContext(ctx, `UPDATE v2_dns_failover_event
SET notified_at = $3, notify_claim_token = '', notify_claimed_at = NULL
WHERE id = $1 AND notify_claim_token = $2 AND notified_at IS NULL`, eventID, token, ackNow)
		if err != nil {
			_ = ackTx.Rollback()
			return fmt.Errorf("mark DNS failover event notified: %w", err)
		}
		acked, err := ackResult.RowsAffected()
		if err != nil {
			_ = ackTx.Rollback()
			return fmt.Errorf("read DNS failover notification ack result: %w", err)
		}
		if acked != 1 {
			_ = ackTx.Rollback()
			return fmt.Errorf("mark DNS failover event notified: expected one updated row, got %d", acked)
		}
		if err := ackTx.Commit(); err != nil {
			return fmt.Errorf("commit DNS failover notification ack: %w", err)
		}
	}
	return nil
}

func newDNSFailoverNotificationClaimToken() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("create DNS failover notification claim token: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

func releaseDNSFailoverNotificationClaim(ctx context.Context, conn *sql.Conn, eventID int64, token string, attempts int, notifyErr error) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin DNS failover notification retry: %w", err)
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	if _, err := tx.ExecContext(ctx, `UPDATE v2_dns_failover_event
SET notify_claim_token = '', notify_claimed_at = NULL, notify_next_attempt_at = $3
WHERE id = $1 AND notify_claim_token = $2 AND notified_at IS NULL`, eventID, token, saturatingUnixAdd(now, dnsFailoverRetryDelay(attempts-1))); err != nil {
		return fmt.Errorf("schedule DNS failover notification retry after %v: %w", notifyErr, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit DNS failover notification retry after %v: %w", notifyErr, err)
	}
	return nil
}

func formatDNSFailoverSwitchNotification(rule DNSFailoverRuleRecord, oldTarget, newTarget DNSFailoverTargetRecord, reason string, probeIDs []int64, singleProbe bool, now time.Time) string {
	probeParts := make([]string, 0, len(probeIDs))
	for _, probeID := range probeIDs {
		probeParts = append(probeParts, fmt.Sprint(probeID))
	}
	probeMode := "多探针一致"
	if singleProbe {
		probeMode = "单探针降级"
	}
	fqdn := strings.TrimSpace(rule.Domain)
	if subdomain := strings.TrimSpace(rule.Subdomain); subdomain != "" && subdomain != "@" {
		fqdn = subdomain + "." + fqdn
	}
	return fmt.Sprintf("【DNS 故障转移】规则：%s（#%d）\n记录：%s\n切换：%s [%s %s] → %s [%s %s]\n检测端口：%d\n探针：%s（%s）\n原因：%s\n时间：%s",
		rule.Name, rule.ID, fqdn,
		oldTarget.Name, oldTarget.DNSType, oldTarget.DNSValue,
		newTarget.Name, newTarget.DNSType, newTarget.DNSValue,
		newTarget.CheckPort, strings.Join(probeParts, ", "), probeMode, reason,
		now.UTC().Format(time.RFC3339),
	)
}
