package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"forest/go-api/internal/dnspod"
)

const (
	dnsFailoverQueueName         = "dns_failover"
	dnsFailoverQueueJobName      = "dns-failover-drain"
	dnsFailoverDefaultTick       = time.Second
	dnsFailoverRetryBase         = 5 * time.Second
	dnsFailoverRetryMaximum      = 5 * time.Minute
	dnsFailoverDNSPodTimeout     = 10 * time.Second
	dnsFailoverEvaluationBatch   = 16
	dnsFailoverNotificationBatch = 32
	dnsFailoverMaxErrorLength    = 1024
)

type dnsFailoverOutboxRow struct {
	ID          int64
	GroupID     int64
	RequestedAt int64
	Attempts    int
}

type dnsFailoverWorkerSnapshot struct {
	Rule       DNSFailoverRuleRecord
	Targets    []DNSFailoverTargetRecord
	Probes     []dnsFailoverProbeSnapshot
	ProbeIDs   []int64
	States     []dnsFailoverProbeTargetSnapshot
	StateFacts []map[string]any
}

type DNSFailoverNotifier interface {
	NotifyAdmins(context.Context, string, bool) error
}

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
	if s == nil || s.jobs == nil {
		return errors.New("DNS failover queue unavailable")
	}

	s.dnsFailoverWakeMu.Lock()
	if s.dnsFailoverWakeQueued {
		s.dnsFailoverWakeMu.Unlock()
		return nil
	}
	s.dnsFailoverWakeQueued = true
	s.dnsFailoverWakeMu.Unlock()

	err := s.jobs.Enqueue(dnsFailoverQueueName, dnsFailoverQueueJobName, func(jobCtx context.Context) error {
		defer func() {
			s.dnsFailoverWakeMu.Lock()
			s.dnsFailoverWakeQueued = false
			s.dnsFailoverWakeMu.Unlock()
		}()
		return s.runDNSFailoverCycle(jobCtx)
	})
	if err != nil {
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
		interval := s.dnsFailoverTickInterval
		if interval <= 0 {
			interval = dnsFailoverDefaultTick
		}
		s.dnsFailoverWorkerWG.Add(1)
		go func() {
			defer s.dnsFailoverWorkerWG.Done()
			_ = s.runDNSFailoverCycle(ctx)
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					_ = s.runDNSFailoverCycle(ctx)
				}
			}
		}()
	})
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
	now := time.Now().Unix()
	if err := s.seedDueDNSFailoverEvaluations(ctx, now); err != nil {
		return err
	}
	if err := s.drainDNSFailoverEvaluationOutbox(ctx, dnsFailoverEvaluationBatch); err != nil {
		return err
	}
	return s.drainPendingDNSFailoverNotifications(ctx, dnsFailoverNotificationBatch)
}

func (s *DBService) seedDueDNSFailoverEvaluations(ctx context.Context, now int64) error {
	if s == nil || s.db == nil {
		return ErrUnavailable
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO v2_dns_failover_eval_outbox (
group_id, requested_at, attempts, next_attempt_at, last_error, created_at, updated_at
)
SELECT g.id, $1, 0, $1, '', $1, $1
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

func (s *DBService) processNextDNSFailoverEvaluation(ctx context.Context) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin DNS failover evaluation: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	now := time.Now().Unix()
	var outbox dnsFailoverOutboxRow
	err = tx.QueryRowContext(ctx, `SELECT id, group_id
FROM v2_dns_failover_eval_outbox
WHERE next_attempt_at <= $1
ORDER BY next_attempt_at ASC, requested_at ASC
LIMIT 1`, now).Scan(&outbox.ID, &outbox.GroupID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("select DNS failover evaluation candidate: %w", err)
	}
	var advisoryLocked bool
	if err := tx.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock($1)`, outbox.GroupID).Scan(&advisoryLocked); err != nil {
		return false, fmt.Errorf("lock DNS failover evaluation group: %w", err)
	}
	if !advisoryLocked {
		return false, nil
	}
	err = tx.QueryRowContext(ctx, `SELECT id, group_id, requested_at, attempts
FROM v2_dns_failover_eval_outbox
WHERE id = $1 AND group_id = $2 AND next_attempt_at <= $3
FOR UPDATE SKIP LOCKED`, outbox.ID, outbox.GroupID, now).Scan(&outbox.ID, &outbox.GroupID, &outbox.RequestedAt, &outbox.Attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("claim DNS failover evaluation: %w", err)
	}

	snapshot, err := loadDNSFailoverWorkerSnapshot(ctx, tx, outbox.GroupID, now)
	if err != nil {
		return false, err
	}
	if !snapshot.Rule.Enabled {
		if err := ackDNSFailoverEvaluation(ctx, tx, outbox, now); err != nil {
			return false, err
		}
		if err := tx.Commit(); err != nil {
			return false, fmt.Errorf("commit disabled DNS failover evaluation: %w", err)
		}
		committed = true
		return true, nil
	}

	lastEventType, err := latestDNSFailoverStateEvent(ctx, tx, outbox.GroupID)
	if err != nil {
		return false, err
	}
	currentTarget, currentValid := enabledDNSFailoverTarget(snapshot.Targets, snapshot.Rule.CurrentTargetID)
	if !currentValid {
		details := dnsFailoverEventDetails(snapshot, nil, nil, "config_error", "current target is missing or disabled", "", now)
		if _, err := appendDNSFailoverTransitionEvent(ctx, tx, snapshot.Rule, lastEventType, "config_error", nil,
			formatDNSFailoverIncidentNotification(snapshot.Rule, "config_error", "当前目标不存在、已禁用或不属于该规则", snapshot.ProbeIDs, now), details, now); err != nil {
			return false, err
		}
		if err := ackDNSFailoverEvaluation(ctx, tx, outbox, now); err != nil {
			return false, err
		}
		if err := tx.Commit(); err != nil {
			return false, fmt.Errorf("commit DNS failover configuration event: %w", err)
		}
		committed = true
		return true, nil
	}

	decision := decideDNSFailover(buildDNSFailoverDecisionInput(snapshot, now))
	if decision.Action == dnsFailoverActionNone {
		desiredEvent := ""
		if isDNSFailoverIncident(decision.Reason) {
			desiredEvent = decision.Reason
		}
		details := dnsFailoverEventDetails(snapshot, &currentTarget, nil, decision.Reason, "", "", now)
		transition := dnsFailoverIncidentTransition(lastEventType, desiredEvent)
		messageReason := decision.Reason
		if transition == "recovered" {
			messageReason = "检测状态已恢复"
		}
		message := formatDNSFailoverIncidentNotification(snapshot.Rule, transition, messageReason, snapshot.ProbeIDs, now)
		if _, err := appendDNSFailoverTransitionEvent(ctx, tx, snapshot.Rule, lastEventType, desiredEvent, &currentTarget, message, details, now); err != nil {
			return false, err
		}
		if err := ackDNSFailoverEvaluation(ctx, tx, outbox, now); err != nil {
			return false, err
		}
		if err := tx.Commit(); err != nil {
			return false, fmt.Errorf("commit DNS failover evaluation: %w", err)
		}
		committed = true
		return true, nil
	}

	newTarget, ok := enabledDNSFailoverTarget(snapshot.Targets, &decision.TargetID)
	if !ok {
		return false, fmt.Errorf("DNS failover target %d is missing or disabled", decision.TargetID)
	}
	request := buildDNSFailoverMutation(snapshot.Rule, newTarget)
	client, err := s.dnsFailoverClient()
	if err != nil {
		return false, s.persistDNSFailoverMutationFailure(ctx, tx, outbox, snapshot, lastEventType, currentTarget, newTarget, dnspod.RecordMutationResult{}, err, now, &committed)
	}
	dnsCtx, cancel := context.WithTimeout(ctx, dnsFailoverDNSPodTimeout)
	result, mutationErr := client.ModifyRecord(dnsCtx, request)
	cancel()
	if mutationErr != nil {
		return true, s.persistDNSFailoverMutationFailure(ctx, tx, outbox, snapshot, lastEventType, currentTarget, newTarget, result, mutationErr, now, &committed)
	}
	recoverPersistenceFailure := func(persistenceErr error) (bool, error) {
		_ = tx.Rollback()
		return false, s.recoverDNSFailoverAfterPersistenceFailure(ctx, client, outbox, snapshot, currentTarget, newTarget, result, persistenceErr, now)
	}
	if isDNSFailoverIncident(lastEventType) {
		recoveredDetails := dnsFailoverEventDetails(snapshot, &currentTarget, &newTarget, "recovered", "", result.RequestID, now)
		if _, err := appendDNSFailoverTransitionEvent(ctx, tx, snapshot.Rule, lastEventType, "", &currentTarget,
			formatDNSFailoverIncidentNotification(snapshot.Rule, "recovered", "检测状态已恢复", snapshot.ProbeIDs, now), recoveredDetails, now); err != nil {
			return recoverPersistenceFailure(err)
		}
	}

	if _, err := tx.ExecContext(ctx, `UPDATE v2_dns_failover_group
SET current_target_id = $2, last_switch_at = $3, last_switch_reason = $4, last_evaluated_at = $3, updated_at = $3
WHERE id = $1`, snapshot.Rule.ID, newTarget.ID, now, decision.Reason); err != nil {
		return recoverPersistenceFailure(fmt.Errorf("update DNS failover active target: %w", err))
	}
	eventType := "failover"
	if decision.Action == dnsFailoverActionFailback {
		eventType = "failback"
	}
	details := dnsFailoverEventDetails(snapshot, &currentTarget, &newTarget, decision.Reason, "", result.RequestID, now)
	onlineProbeIDs := onlineDNSFailoverProbeIDs(snapshot.Probes)
	message := formatDNSFailoverSwitchNotification(snapshot.Rule, currentTarget, newTarget, decision.Reason, onlineProbeIDs, len(onlineProbeIDs) == 1, time.Unix(now, 0))
	if err := insertDNSFailoverEvent(ctx, tx, snapshot.Rule.ID, &newTarget.ID, eventType, message, details, now); err != nil {
		return recoverPersistenceFailure(err)
	}
	if err := deleteDNSFailoverOutboxVersion(ctx, tx, outbox); err != nil {
		return recoverPersistenceFailure(err)
	}
	if err := tx.Commit(); err != nil {
		return false, s.recoverDNSFailoverAfterPersistenceFailure(ctx, client, outbox, snapshot, currentTarget, newTarget, result, fmt.Errorf("commit DNS failover switch: %w", err), now)
	}
	committed = true
	return true, nil
}

func (s *DBService) recoverDNSFailoverAfterPersistenceFailure(ctx context.Context, client dnspodAPI, outbox dnsFailoverOutboxRow, snapshot dnsFailoverWorkerSnapshot, oldTarget, newTarget DNSFailoverTargetRecord, switchResult dnspod.RecordMutationResult, persistenceErr error, now int64) error {
	rollbackCtx, cancelRollback := context.WithTimeout(context.WithoutCancel(ctx), dnsFailoverDNSPodTimeout)
	rollbackResult, compensationErr := client.ModifyRecord(rollbackCtx, buildDNSFailoverMutation(snapshot.Rule, oldTarget))
	cancelRollback()

	recoveryErr := persistenceErr
	if compensationErr != nil {
		recoveryErr = errors.Join(recoveryErr, fmt.Errorf("rollback DNS failed: %w", compensationErr))
	}
	errorText := truncateDNSFailoverError(recoveryErr)

	persistCtx, cancelPersist := context.WithTimeout(context.WithoutCancel(ctx), dnsFailoverDNSPodTimeout)
	defer cancelPersist()
	tx, err := s.db.BeginTx(persistCtx, nil)
	if err != nil {
		return errors.Join(recoveryErr, fmt.Errorf("begin DNS failover recovery transaction: %w", err))
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(persistCtx, `SELECT pg_advisory_xact_lock($1)`, snapshot.Rule.ID); err != nil {
		return errors.Join(recoveryErr, fmt.Errorf("lock DNS failover recovery group: %w", err))
	}
	lastEventType, err := latestDNSFailoverStateEvent(persistCtx, tx, snapshot.Rule.ID)
	if err != nil {
		return errors.Join(recoveryErr, err)
	}
	details := dnsFailoverEventDetails(snapshot, &oldTarget, &newTarget, "dnspod_error", errorText, switchResult.RequestID, now)
	details["switch_request_id"] = switchResult.RequestID
	details["rollback_request_id"] = rollbackResult.RequestID
	if compensationErr != nil {
		details["rollback_error"] = compensationErr.Error()
	} else {
		details["rollback"] = "succeeded"
	}
	message := formatDNSFailoverIncidentNotification(snapshot.Rule, "dnspod_error", errorText, snapshot.ProbeIDs, now)
	if _, err := appendDNSFailoverTransitionEvent(persistCtx, tx, snapshot.Rule, lastEventType, "dnspod_error", &newTarget, message, details, now); err != nil {
		return errors.Join(recoveryErr, err)
	}
	nextAttemptAt := saturatingUnixAdd(now, dnsFailoverRetryDelay(outbox.Attempts))
	_, err = tx.ExecContext(persistCtx, `INSERT INTO v2_dns_failover_eval_outbox (
group_id, requested_at, attempts, next_attempt_at, last_error, created_at, updated_at
) VALUES ($1, $2, 1, $3, $4, $2, $2)
ON CONFLICT (group_id) DO UPDATE SET
requested_at = GREATEST(v2_dns_failover_eval_outbox.requested_at, EXCLUDED.requested_at),
attempts = v2_dns_failover_eval_outbox.attempts + 1,
next_attempt_at = EXCLUDED.next_attempt_at,
last_error = EXCLUDED.last_error,
updated_at = EXCLUDED.updated_at`, snapshot.Rule.ID, now, nextAttemptAt, errorText)
	if err != nil {
		return errors.Join(recoveryErr, fmt.Errorf("persist DNS failover recovery outbox: %w", err))
	}
	if err := tx.Commit(); err != nil {
		return errors.Join(recoveryErr, fmt.Errorf("commit DNS failover recovery transaction: %w", err))
	}
	return recoveryErr
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
last_switch_at, last_switch_reason, created_at, updated_at
FROM v2_dns_failover_group
WHERE id = $1
FOR UPDATE`, groupID).Scan(
		&snapshot.Rule.ID, &snapshot.Rule.Name, &snapshot.Rule.DomainID, &snapshot.Rule.Domain, &snapshot.Rule.RecordID,
		&snapshot.Rule.Subdomain, &snapshot.Rule.RecordLineID, &snapshot.Rule.RecordLineName, &snapshot.Rule.TTL, &snapshot.Rule.MX,
		&weight, &currentTarget, &enabled, &autoFailback, &snapshot.Rule.CheckIntervalSec, &snapshot.Rule.TCPTimeoutMS,
		&snapshot.Rule.FailureThreshold, &snapshot.Rule.SuccessThreshold, &snapshot.Rule.SingleProbeFailureThreshold,
		&snapshot.Rule.SingleProbeSuccessThreshold, &snapshot.Rule.ProbeOfflineSec, &snapshot.Rule.CooldownSec, &lastSwitch,
		&snapshot.Rule.LastSwitchReason, &snapshot.Rule.CreatedAt, &snapshot.Rule.UpdatedAt,
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

	stateRows, err := tx.QueryContext(ctx, `SELECT s.probe_id, s.target_id, s.consecutive_success, s.consecutive_failure, s.last_resolved_ip
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
		if err := stateRows.Scan(&probeID, &targetID, &success, &failure, &resolvedIP); err != nil {
			stateRows.Close()
			return dnsFailoverWorkerSnapshot{}, fmt.Errorf("scan DNS failover state: %w", err)
		}
		snapshot.States = append(snapshot.States, dnsFailoverProbeTargetSnapshot{
			ProbeID: probeID, TargetID: targetID, SuccessStreak: safeInt(success), FailureStreak: safeInt(failure),
		})
		snapshot.StateFacts = append(snapshot.StateFacts, map[string]any{
			"probe_id": probeID, "target_id": targetID, "success_streak": success, "failure_streak": failure, "resolved_ip": resolvedIP,
		})
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

func latestDNSFailoverStateEvent(ctx context.Context, tx *sql.Tx, groupID int64) (string, error) {
	var eventType string
	err := tx.QueryRowContext(ctx, `SELECT event_type
FROM v2_dns_failover_event
WHERE group_id = $1
  AND event_type IN ('all_probes_offline', 'probe_disagreement', 'no_healthy_target', 'dnspod_error', 'config_error', 'recovered')
ORDER BY created_at DESC, id DESC
LIMIT 1
FOR UPDATE`, groupID).Scan(&eventType)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("load DNS failover incident state: %w", err)
	}
	return eventType, nil
}

func appendDNSFailoverTransitionEvent(ctx context.Context, tx *sql.Tx, rule DNSFailoverRuleRecord, lastType, desiredType string, target *DNSFailoverTargetRecord, message string, details map[string]any, now int64) (string, error) {
	eventType := dnsFailoverIncidentTransition(lastType, desiredType)
	if eventType == "" {
		return lastType, nil
	}
	if eventType == "recovered" && strings.TrimSpace(message) == "" {
		message = formatDNSFailoverIncidentNotification(rule, eventType, "检测状态已恢复", rule.ProbeIDs, now)
	}
	var targetID *int64
	if target != nil {
		targetID = &target.ID
	}
	if err := insertDNSFailoverEvent(ctx, tx, rule.ID, targetID, eventType, message, details, now); err != nil {
		return lastType, err
	}
	return eventType, nil
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

func (s *DBService) persistDNSFailoverMutationFailure(ctx context.Context, tx *sql.Tx, outbox dnsFailoverOutboxRow, snapshot dnsFailoverWorkerSnapshot, lastEventType string, oldTarget, newTarget DNSFailoverTargetRecord, result dnspod.RecordMutationResult, mutationErr error, now int64, committed *bool) error {
	errorText := truncateDNSFailoverError(mutationErr)
	details := dnsFailoverEventDetails(snapshot, &oldTarget, &newTarget, "dnspod_error", errorText, result.RequestID, now)
	message := formatDNSFailoverIncidentNotification(snapshot.Rule, "dnspod_error", errorText, snapshot.ProbeIDs, now)
	if _, err := appendDNSFailoverTransitionEvent(ctx, tx, snapshot.Rule, lastEventType, "dnspod_error", &newTarget, message, details, now); err != nil {
		return err
	}
	nextAttemptAt := saturatingUnixAdd(now, dnsFailoverRetryDelay(outbox.Attempts))
	_, err := tx.ExecContext(ctx, `UPDATE v2_dns_failover_eval_outbox
SET attempts = $2, next_attempt_at = $3, last_error = $4, updated_at = $5
WHERE id = $1 AND requested_at = $6`, outbox.ID, outbox.Attempts+1, nextAttemptAt, errorText, now, outbox.RequestedAt)
	if err != nil {
		return fmt.Errorf("schedule DNS failover retry: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit DNSPod failure event: %w", err)
	}
	*committed = true
	return nil
}

func (s *DBService) dnsFailoverClient() (dnspodAPI, error) {
	if s.dnsFailoverAPI != nil {
		return s.dnsFailoverAPI, nil
	}
	return s.dnspodClient()
}

func dnsFailoverEventDetails(snapshot dnsFailoverWorkerSnapshot, oldTarget, newTarget *DNSFailoverTargetRecord, reason, errorText, requestID string, now int64) map[string]any {
	details := map[string]any{
		"reason": reason, "probe_ids": append([]int64(nil), snapshot.ProbeIDs...), "states": snapshot.StateFacts,
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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin manual DNS failover switch: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, groupID); err != nil {
		return fmt.Errorf("lock manual DNS failover group: %w", err)
	}

	now := time.Now().Unix()
	snapshot, err := loadDNSFailoverRuleTargetsForUpdate(ctx, tx, groupID)
	if err != nil {
		return err
	}
	oldTarget, oldValid := enabledDNSFailoverTarget(snapshot.Targets, snapshot.Rule.CurrentTargetID)
	if !oldValid {
		return errors.New("当前故障转移目标不存在、已禁用或不属于该规则")
	}
	newTarget, targetValid := enabledDNSFailoverTarget(snapshot.Targets, &targetID)
	if !targetValid {
		return errors.New("手动切换目标不存在、已禁用或不属于该规则")
	}
	if oldTarget.ID == newTarget.ID {
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit idempotent manual DNS failover switch: %w", err)
		}
		committed = true
		return nil
	}

	lastEventType, err := latestDNSFailoverStateEvent(ctx, tx, groupID)
	if err != nil {
		return err
	}
	client, err := s.dnsFailoverClient()
	var mutationResult dnspod.RecordMutationResult
	if err == nil {
		dnsCtx, cancel := context.WithTimeout(ctx, dnsFailoverDNSPodTimeout)
		mutationResult, err = client.ModifyRecord(dnsCtx, buildDNSFailoverMutation(snapshot.Rule, newTarget))
		cancel()
		if err == nil {
			recoverManualPersistenceFailure := func(persistenceErr error) error {
				_ = tx.Rollback()
				return s.recoverDNSFailoverAfterPersistenceFailure(ctx, client, dnsFailoverOutboxRow{GroupID: groupID, RequestedAt: now}, snapshot, oldTarget, newTarget, mutationResult, persistenceErr, now)
			}
			if isDNSFailoverIncident(lastEventType) {
				recoveredDetails := dnsFailoverEventDetails(snapshot, &oldTarget, &newTarget, "recovered", "", mutationResult.RequestID, now)
				if _, transitionErr := appendDNSFailoverTransitionEvent(ctx, tx, snapshot.Rule, lastEventType, "", &oldTarget,
					formatDNSFailoverIncidentNotification(snapshot.Rule, "recovered", "检测状态已恢复", snapshot.ProbeIDs, now), recoveredDetails, now); transitionErr != nil {
					return recoverManualPersistenceFailure(transitionErr)
				}
			}
			if _, updateErr := tx.ExecContext(ctx, `UPDATE v2_dns_failover_group
SET current_target_id = $2, last_switch_at = $3, last_switch_reason = 'manual', last_evaluated_at = $3, updated_at = $3
WHERE id = $1`, groupID, targetID, now); updateErr != nil {
				return recoverManualPersistenceFailure(fmt.Errorf("update manual DNS failover target: %w", updateErr))
			}
			details := dnsFailoverEventDetails(snapshot, &oldTarget, &newTarget, "manual", "", mutationResult.RequestID, now)
			message := formatDNSFailoverSwitchNotification(snapshot.Rule, oldTarget, newTarget, "manual", snapshot.ProbeIDs, false, time.Unix(now, 0))
			if insertErr := insertDNSFailoverEvent(ctx, tx, groupID, &newTarget.ID, "manual_switch", message, details, now); insertErr != nil {
				return recoverManualPersistenceFailure(insertErr)
			}
			if commitErr := tx.Commit(); commitErr != nil {
				return s.recoverDNSFailoverAfterPersistenceFailure(ctx, client, dnsFailoverOutboxRow{GroupID: groupID, RequestedAt: now}, snapshot, oldTarget, newTarget, mutationResult, fmt.Errorf("commit manual DNS failover switch: %w", commitErr), now)
			}
			committed = true
			return nil
		}
	}

	errorText := truncateDNSFailoverError(err)
	details := dnsFailoverEventDetails(snapshot, &oldTarget, &newTarget, "dnspod_error", errorText, mutationResult.RequestID, now)
	details["operation"] = "manual_switch"
	details["retry_semantics"] = "reevaluate_group_health"
	message := formatDNSFailoverIncidentNotification(snapshot.Rule, "dnspod_error", errorText, snapshot.ProbeIDs, now)
	if _, eventErr := appendDNSFailoverTransitionEvent(ctx, tx, snapshot.Rule, lastEventType, "dnspod_error", &newTarget, message, details, now); eventErr != nil {
		return eventErr
	}
	nextAttemptAt := saturatingUnixAdd(now, dnsFailoverRetryDelay(0))
	_, outboxErr := tx.ExecContext(ctx, `INSERT INTO v2_dns_failover_eval_outbox (
group_id, requested_at, attempts, next_attempt_at, last_error, created_at, updated_at
) VALUES ($1, $2, 1, $3, $4, $2, $2)
ON CONFLICT (group_id) DO UPDATE SET
requested_at = GREATEST(v2_dns_failover_eval_outbox.requested_at, EXCLUDED.requested_at),
attempts = v2_dns_failover_eval_outbox.attempts + 1,
next_attempt_at = EXCLUDED.next_attempt_at,
last_error = EXCLUDED.last_error,
updated_at = EXCLUDED.updated_at`, groupID, now, nextAttemptAt, errorText)
	if outboxErr != nil {
		return fmt.Errorf("persist manual DNS failover retry: %w", outboxErr)
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return errors.Join(err, fmt.Errorf("commit manual DNS failure event: %w", commitErr))
	}
	committed = true
	if s.jobs != nil {
		_ = s.RequestDNSFailoverEvaluation(context.WithoutCancel(ctx), []int64{groupID})
	}
	return err
}

func loadDNSFailoverRuleTargetsForUpdate(ctx context.Context, tx *sql.Tx, groupID int64) (dnsFailoverWorkerSnapshot, error) {
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
last_switch_at, last_switch_reason, created_at, updated_at
FROM v2_dns_failover_group
WHERE id = $1
FOR UPDATE`, groupID).Scan(
		&snapshot.Rule.ID, &snapshot.Rule.Name, &snapshot.Rule.DomainID, &snapshot.Rule.Domain, &snapshot.Rule.RecordID,
		&snapshot.Rule.Subdomain, &snapshot.Rule.RecordLineID, &snapshot.Rule.RecordLineName, &snapshot.Rule.TTL, &snapshot.Rule.MX,
		&weight, &currentTarget, &enabled, &autoFailback, &snapshot.Rule.CheckIntervalSec, &snapshot.Rule.TCPTimeoutMS,
		&snapshot.Rule.FailureThreshold, &snapshot.Rule.SuccessThreshold, &snapshot.Rule.SingleProbeFailureThreshold,
		&snapshot.Rule.SingleProbeSuccessThreshold, &snapshot.Rule.ProbeOfflineSec, &snapshot.Rule.CooldownSec, &lastSwitch,
		&snapshot.Rule.LastSwitchReason, &snapshot.Rule.CreatedAt, &snapshot.Rule.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return dnsFailoverWorkerSnapshot{}, errors.New("故障转移规则不存在")
		}
		return dnsFailoverWorkerSnapshot{}, fmt.Errorf("lock DNS failover group: %w", err)
	}
	snapshot.Rule.Weight = dnsNullInt64Pointer(weight)
	snapshot.Rule.CurrentTargetID = dnsNullInt64Pointer(currentTarget)
	snapshot.Rule.Enabled = enabled != 0
	snapshot.Rule.AutoFailback = autoFailback != 0
	snapshot.Rule.LastSwitchAt = dnsNullInt64Pointer(lastSwitch)

	rows, err := tx.QueryContext(ctx, `SELECT id, group_id, sort, name, dns_type, dns_value, check_host, check_port, enabled, created_at, updated_at
FROM v2_dns_failover_target
WHERE group_id = $1
ORDER BY sort ASC, id ASC
FOR UPDATE`, groupID)
	if err != nil {
		return dnsFailoverWorkerSnapshot{}, fmt.Errorf("lock DNS failover targets: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var target DNSFailoverTargetRecord
		var targetEnabled int64
		if err := rows.Scan(&target.ID, &target.GroupID, &target.Sort, &target.Name, &target.DNSType, &target.DNSValue,
			&target.CheckHost, &target.CheckPort, &targetEnabled, &target.CreatedAt, &target.UpdatedAt); err != nil {
			return dnsFailoverWorkerSnapshot{}, fmt.Errorf("scan DNS failover target: %w", err)
		}
		target.Enabled = targetEnabled != 0
		snapshot.Targets = append(snapshot.Targets, target)
	}
	if err := rows.Err(); err != nil {
		return dnsFailoverWorkerSnapshot{}, fmt.Errorf("iterate DNS failover targets: %w", err)
	}
	snapshot.Rule.Targets = snapshot.Targets
	return snapshot, nil
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

func dnsFailoverIncidentTransition(lastType, desiredType string) string {
	lastType = strings.TrimSpace(lastType)
	desiredType = strings.TrimSpace(desiredType)
	if desiredType != "" {
		if lastType == desiredType {
			return ""
		}
		return desiredType
	}
	if isDNSFailoverIncident(lastType) {
		return "recovered"
	}
	return ""
}

func isDNSFailoverIncident(eventType string) bool {
	switch eventType {
	case "all_probes_offline", "probe_disagreement", "no_healthy_target", "dnspod_error", "config_error":
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
	for range limit {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin DNS failover notification transaction: %w", err)
		}
		var eventID int64
		var message string
		err = tx.QueryRowContext(ctx, `SELECT id, message
FROM v2_dns_failover_event
WHERE notified_at IS NULL
ORDER BY created_at ASC, id ASC
LIMIT 1
FOR UPDATE SKIP LOCKED`).Scan(&eventID, &message)
		if errors.Is(err, sql.ErrNoRows) {
			_ = tx.Rollback()
			return nil
		}
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("claim DNS failover notification: %w", err)
		}
		if s.dnsFailoverNotifier != nil {
			if err := s.dnsFailoverNotifier.NotifyAdmins(ctx, message, true); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("notify DNS failover event: %w", err)
			}
		}
		now := time.Now().Unix()
		if _, err := tx.ExecContext(ctx, `UPDATE v2_dns_failover_event SET notified_at = $2 WHERE id = $1 AND notified_at IS NULL`, eventID, now); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("mark DNS failover event notified: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit DNS failover notification: %w", err)
		}
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
