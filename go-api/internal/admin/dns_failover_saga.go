package admin

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"forest/go-api/internal/dnspod"
)

const dnsFailoverClaimLease = 30 * time.Second

type dnsFailoverMutationSnapshot struct {
	Domain       string `json:"domain"`
	DomainID     int64  `json:"domain_id"`
	RecordID     int64  `json:"record_id"`
	SubDomain    string `json:"subdomain"`
	RecordType   string `json:"record_type"`
	RecordLine   string `json:"record_line"`
	RecordLineID string `json:"record_line_id"`
	Value        string `json:"value"`
	TTL          int64  `json:"ttl"`
	MX           int64  `json:"mx"`
	Weight       *int64 `json:"weight"`
}

type dnsFailoverSagaRecord struct {
	GroupID             int64
	Phase               string
	OriginalOperation   string
	OriginalTargetID    sql.NullInt64
	OriginalRequestedAt int64
	Reason              string
	DesiredTargetID     int64
	RollbackTargetID    int64
	DesiredMutation     dnsFailoverMutationSnapshot
	RollbackMutation    dnsFailoverMutationSnapshot
	Attempts            int
	NextAttemptAt       int64
	LastError           string
	CreatedAt           int64
}

func freezeDNSFailoverMutation(request dnspod.RecordMutationRequest) dnsFailoverMutationSnapshot {
	return dnsFailoverMutationSnapshot{
		Domain: request.Domain, DomainID: request.DomainID, RecordID: request.RecordID, SubDomain: request.SubDomain,
		RecordType: request.RecordType, RecordLine: request.RecordLine, RecordLineID: request.RecordLineID,
		Value: request.Value, TTL: request.TTL, MX: request.MX, Weight: request.Weight,
	}
}

func (snapshot dnsFailoverMutationSnapshot) request() dnspod.RecordMutationRequest {
	return dnspod.RecordMutationRequest{
		Domain: snapshot.Domain, DomainID: snapshot.DomainID, RecordID: snapshot.RecordID, SubDomain: snapshot.SubDomain,
		RecordType: snapshot.RecordType, RecordLine: snapshot.RecordLine, RecordLineID: snapshot.RecordLineID,
		Value: snapshot.Value, TTL: snapshot.TTL, MX: snapshot.MX, Weight: snapshot.Weight,
	}
}

func releaseDNSFailoverSessionLock(conn *sql.Conn, key int64) {
	if conn == nil {
		return
	}
	releaseCtx, cancel := context.WithTimeout(context.Background(), dnsFailoverRecoveryTimeout)
	defer cancel()
	var unlocked bool
	if err := conn.QueryRowContext(releaseCtx, `SELECT pg_advisory_unlock($1)`, key).Scan(&unlocked); err == nil && unlocked {
		return
	}
	_ = conn.Raw(func(any) error { return driver.ErrBadConn })
}

func scanDNSFailoverOutbox(row *sql.Row, outbox *dnsFailoverOutboxRow) error {
	return row.Scan(&outbox.ID, &outbox.GroupID, &outbox.Operation, &outbox.TargetID, &outbox.SourceTargetID,
		&outbox.RequestedAt, &outbox.Attempts, &outbox.LastError)
}

func scanDNSFailoverSaga(row *sql.Row, saga *dnsFailoverSagaRecord) error {
	var desiredJSON, rollbackJSON string
	if err := row.Scan(&saga.GroupID, &saga.Phase, &saga.OriginalOperation, &saga.OriginalTargetID,
		&saga.OriginalRequestedAt, &saga.Reason, &saga.DesiredTargetID, &saga.RollbackTargetID,
		&desiredJSON, &rollbackJSON, &saga.Attempts, &saga.NextAttemptAt, &saga.LastError, &saga.CreatedAt); err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(desiredJSON), &saga.DesiredMutation); err != nil {
		return fmt.Errorf("decode desired DNS mutation snapshot: %w", err)
	}
	if err := json.Unmarshal([]byte(rollbackJSON), &saga.RollbackMutation); err != nil {
		return fmt.Errorf("decode rollback DNS mutation snapshot: %w", err)
	}
	return nil
}

func (s *DBService) processNextDNSFailoverSaga(ctx context.Context) (bool, error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return false, fmt.Errorf("open DNS failover saga connection: %w", err)
	}
	defer conn.Close()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin DNS failover saga claim: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	now := time.Now().Unix()
	var saga dnsFailoverSagaRecord
	err = scanDNSFailoverSaga(tx.QueryRowContext(ctx, `SELECT group_id, phase, original_operation, original_target_id,
original_requested_at, reason, desired_target_id, rollback_target_id, desired_mutation, rollback_mutation,
attempts, next_attempt_at, last_error, created_at
FROM v2_dns_failover_saga
WHERE phase = 'prepared' AND next_attempt_at <= $1
ORDER BY next_attempt_at ASC, created_at ASC, group_id ASC
LIMIT 1
FOR UPDATE SKIP LOCKED`, now), &saga)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("claim prepared DNS failover saga: %w", err)
	}
	var locked bool
	if err := tx.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, saga.GroupID).Scan(&locked); err != nil {
		return false, fmt.Errorf("acquire DNS failover saga session lock: %w", err)
	}
	if !locked {
		if _, err := tx.ExecContext(ctx, `UPDATE v2_dns_failover_saga
SET next_attempt_at = $2, updated_at = $3
WHERE group_id = $1 AND phase = 'prepared'`, saga.GroupID, saturatingUnixAdd(now, time.Second), now); err != nil {
			return false, fmt.Errorf("defer busy DNS failover saga: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return false, fmt.Errorf("commit busy DNS failover saga defer: %w", err)
		}
		committed = true
		return true, nil
	}
	defer releaseDNSFailoverSessionLock(conn, saga.GroupID)
	if _, err := tx.ExecContext(ctx, `UPDATE v2_dns_failover_saga
SET next_attempt_at = $2, updated_at = $3
WHERE group_id = $1 AND phase = 'prepared'`, saga.GroupID, saturatingUnixAdd(now, dnsFailoverClaimLease), now); err != nil {
		return false, fmt.Errorf("lease prepared DNS failover saga: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit prepared DNS failover saga lease: %w", err)
	}
	committed = true
	claimedTargetID := saga.DesiredTargetID
	s.writeDNSFailoverLogsBestEffort(ctx, dnsFailoverLogEntry{
		GroupID: saga.GroupID, TargetID: &claimedTargetID, Stage: "saga", Level: "info", Outcome: "claimed",
		Message: "prepared DNS switch saga claimed for recovery", Details: map[string]any{
			"operation": saga.OriginalOperation, "attempt": saga.Attempts, "last_error": dnsFailoverSafeDiagnosticText(saga.LastError),
			"reason": saga.Reason, "desired_target_id": saga.DesiredTargetID, "rollback_target_id": saga.RollbackTargetID,
			"lease_until": saturatingUnixAdd(now, dnsFailoverClaimLease),
		}, CreatedAt: now,
	})
	cause := errors.New("recover prepared DNS failover saga")
	if saga.LastError != "" {
		cause = errors.New(saga.LastError)
	}
	return true, s.recoverPreparedDNSFailoverSaga(ctx, conn, saga, cause, now)
}

func (s *DBService) processNextDNSFailoverEvaluation(ctx context.Context) (bool, error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return false, fmt.Errorf("open DNS failover evaluation connection: %w", err)
	}
	defer conn.Close()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin DNS failover evaluation claim: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	now := time.Now().Unix()
	var outbox dnsFailoverOutboxRow
	err = scanDNSFailoverOutbox(tx.QueryRowContext(ctx, `SELECT id, group_id, operation, target_id, source_target_id, requested_at, attempts, last_error
FROM v2_dns_failover_eval_outbox o
WHERE next_attempt_at <= $1
  AND NOT EXISTS (SELECT 1 FROM v2_dns_failover_saga s WHERE s.group_id = o.group_id)
ORDER BY next_attempt_at ASC, requested_at ASC
LIMIT 1
FOR UPDATE SKIP LOCKED`, now), &outbox)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("select DNS failover evaluation candidate: %w", err)
	}
	var locked bool
	if err := tx.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, outbox.GroupID).Scan(&locked); err != nil {
		return false, fmt.Errorf("acquire DNS failover session lock: %w", err)
	}
	if !locked {
		if _, err := tx.ExecContext(ctx, `UPDATE v2_dns_failover_eval_outbox
SET next_attempt_at = $2, updated_at = $3
WHERE id = $1 AND requested_at = $4`, outbox.ID, saturatingUnixAdd(now, time.Second), now, outbox.RequestedAt); err != nil {
			return false, fmt.Errorf("defer busy DNS failover group: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return false, fmt.Errorf("commit busy DNS failover defer: %w", err)
		}
		committed = true
		return true, nil
	}
	defer releaseDNSFailoverSessionLock(conn, outbox.GroupID)
	if _, err := tx.ExecContext(ctx, `UPDATE v2_dns_failover_eval_outbox
SET next_attempt_at = $2, updated_at = $3
WHERE id = $1 AND requested_at = $4`, outbox.ID, saturatingUnixAdd(now, dnsFailoverClaimLease), now, outbox.RequestedAt); err != nil {
		return false, fmt.Errorf("lease DNS failover evaluation: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit DNS failover evaluation lease: %w", err)
	}
	committed = true
	s.writeDNSFailoverLogsBestEffort(ctx, dnsFailoverOutboxTrace(outbox, "claimed", "info", "evaluation outbox claimed", now, map[string]any{
		"lease_until": saturatingUnixAdd(now, dnsFailoverClaimLease),
	}))
	return s.processClaimedDNSFailoverEvaluation(ctx, conn, outbox.ID, now)
}

func (s *DBService) processClaimedDNSFailoverEvaluation(ctx context.Context, conn *sql.Conn, outboxID, now int64) (bool, error) {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin claimed DNS failover evaluation: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	var outbox dnsFailoverOutboxRow
	err = scanDNSFailoverOutbox(tx.QueryRowContext(ctx, `SELECT id, group_id, operation, target_id, source_target_id, requested_at, attempts, last_error
FROM v2_dns_failover_eval_outbox
WHERE id = $1
FOR UPDATE`, outboxID), &outbox)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return false, err
		}
		committed = true
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("reload claimed DNS failover evaluation: %w", err)
	}
	snapshot, err := loadDNSFailoverWorkerSnapshot(ctx, tx, outbox.GroupID, now)
	if err != nil {
		return false, err
	}
	currentTarget, currentValid := enabledDNSFailoverTarget(snapshot.Targets, snapshot.Rule.CurrentTargetID)
	if !currentValid {
		configErr := errors.New("current target is missing or disabled")
		details := dnsFailoverEventDetails(snapshot, nil, nil, "config_error", configErr.Error(), "", now)
		if err := recordDNSFailoverIncident(ctx, tx, snapshot, "config_error", nil,
			formatDNSFailoverIncidentNotification(snapshot.Rule, "config_error", "当前目标不存在、已禁用或不属于该规则", snapshot.ProbeIDs, now), details, now); err != nil {
			return false, err
		}
		if outbox.Operation == dnsFailoverOperationManual || outbox.Operation == dnsFailoverOperationReconcile {
			if err := persistDNSFailoverRetryWithoutCommit(ctx, tx, outbox, configErr, now); err != nil {
				return false, err
			}
		} else if err := ackDNSFailoverEvaluation(ctx, tx, outbox, now); err != nil {
			return false, err
		}
		if err := tx.Commit(); err != nil {
			return false, err
		}
		committed = true
		return true, nil
	}

	var newTarget DNSFailoverTargetRecord
	var evaluationDecision *dnsFailoverDecision
	eventType := "manual_switch"
	reason := "manual"
	switch outbox.Operation {
	case dnsFailoverOperationManual:
		if !outbox.TargetID.Valid {
			return false, errors.New("manual DNS failover operation has no target")
		}
		var ok bool
		newTarget, ok = enabledDNSFailoverTarget(snapshot.Targets, &outbox.TargetID.Int64)
		if !ok {
			retryErr := fmt.Errorf("manual DNS failover target %d is missing or disabled", outbox.TargetID.Int64)
			if err := persistDNSFailoverForcedConfigurationFailureWithoutCommit(ctx, tx, outbox, snapshot, outbox.TargetID.Int64, retryErr, now); err != nil {
				return false, err
			}
			if err := tx.Commit(); err != nil {
				return false, err
			}
			committed = true
			return true, nil
		}
		if newTarget.ID == currentTarget.ID {
			if err := deleteDNSFailoverOutboxVersion(ctx, tx, outbox); err != nil {
				return false, err
			}
			if err := tx.Commit(); err != nil {
				return false, fmt.Errorf("commit idempotent manual DNS failover switch: %w", err)
			}
			committed = true
			return true, nil
		}
	case dnsFailoverOperationReconcile:
		// Compatibility for rows created before durable sagas existed. Freeze the
		// current database target first, then recover through the normal saga path.
		newTarget = currentTarget
		eventType = "dns_state_reconciled"
		reason = "legacy_reconcile"
	case "", dnsFailoverOperationEvaluate:
		if !snapshot.Rule.Enabled {
			if err := ackDNSFailoverEvaluation(ctx, tx, outbox, now); err != nil {
				return false, err
			}
			if err := tx.Commit(); err != nil {
				return false, err
			}
			committed = true
			return true, nil
		}
		decision := decideDNSFailover(buildDNSFailoverDecisionInput(snapshot, now))
		evaluationDecision = &decision
		if decision.Action == dnsFailoverActionNone {
			if err := applyDNSFailoverNoAction(ctx, tx, outbox, snapshot, currentTarget, decision, now); err != nil {
				return false, err
			}
			if err := tx.Commit(); err != nil {
				return false, fmt.Errorf("commit DNS failover evaluation: %w", err)
			}
			committed = true
			logs := dnsFailoverEvaluationTraceEntries(snapshot, outbox, decision, now)
			logs = append(logs, dnsFailoverOutboxTrace(outbox, "acked", "info", "evaluation completed without a switch", now, map[string]any{
				"decision_action": string(decision.Action), "decision_reason": decision.Reason,
			}))
			s.writeDNSFailoverLogsBestEffort(ctx, logs...)
			return true, nil
		}
		var ok bool
		newTarget, ok = enabledDNSFailoverTarget(snapshot.Targets, &decision.TargetID)
		if !ok {
			return false, fmt.Errorf("DNS failover target %d is missing or disabled", decision.TargetID)
		}
		reason = decision.Reason
		eventType = "failover"
		if decision.Action == dnsFailoverActionFailback {
			eventType = "failback"
		}
	default:
		return false, fmt.Errorf("unsupported DNS failover outbox operation %q", outbox.Operation)
	}

	desired := freezeDNSFailoverMutation(buildDNSFailoverMutation(snapshot.Rule, newTarget))
	rollback := freezeDNSFailoverMutation(buildDNSFailoverMutation(snapshot.Rule, currentTarget))
	desiredJSON, err := json.Marshal(desired)
	if err != nil {
		return false, fmt.Errorf("encode desired DNS mutation: %w", err)
	}
	rollbackJSON, err := json.Marshal(rollback)
	if err != nil {
		return false, fmt.Errorf("encode rollback DNS mutation: %w", err)
	}
	originalOperation := outbox.Operation
	var originalTarget any
	if originalOperation == "" || originalOperation == dnsFailoverOperationEvaluate || originalOperation == dnsFailoverOperationReconcile {
		originalOperation = dnsFailoverOperationEvaluate
	} else {
		originalTarget = outbox.TargetID.Int64
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO v2_dns_failover_saga (
group_id, phase, original_operation, original_target_id, original_requested_at, reason,
desired_target_id, rollback_target_id, desired_mutation, rollback_mutation,
attempts, next_attempt_at, last_error, created_at, updated_at
) VALUES ($1, 'prepared', $2, $3, $4, $5, $6, $7, $8, $9, 0, $10, '', $10, $10)`,
		outbox.GroupID, originalOperation, originalTarget, outbox.RequestedAt, reason, newTarget.ID, currentTarget.ID,
		string(desiredJSON), string(rollbackJSON), now); err != nil {
		return false, fmt.Errorf("prepare durable DNS failover saga: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit prepared DNS failover saga: %w", err)
	}
	committed = true
	preparedLogs := make([]dnsFailoverLogEntry, 0, 3)
	if evaluationDecision != nil {
		preparedLogs = append(preparedLogs, dnsFailoverEvaluationTraceEntries(snapshot, outbox, *evaluationDecision, now)...)
	}
	preparedTargetID := newTarget.ID
	preparedLogs = append(preparedLogs, dnsFailoverLogEntry{
		GroupID: outbox.GroupID, TargetID: &preparedTargetID, Stage: "saga", Level: "info", Outcome: "prepared",
		Message: "durable DNS switch saga prepared", Details: map[string]any{
			"outbox_id": outbox.ID, "operation": originalOperation, "requested_at": outbox.RequestedAt,
			"reason": reason, "desired_target_id": newTarget.ID, "rollback_target_id": currentTarget.ID,
		}, CreatedAt: now,
	})
	s.writeDNSFailoverLogsBestEffort(ctx, preparedLogs...)
	saga := dnsFailoverSagaRecord{
		GroupID: outbox.GroupID, Phase: "prepared", OriginalOperation: originalOperation,
		OriginalTargetID: outbox.TargetID, OriginalRequestedAt: outbox.RequestedAt, Reason: reason,
		DesiredTargetID: newTarget.ID, RollbackTargetID: currentTarget.ID,
		DesiredMutation: desired, RollbackMutation: rollback, CreatedAt: now,
	}
	if outbox.Operation == dnsFailoverOperationReconcile {
		return true, s.recoverPreparedDNSFailoverSaga(ctx, conn, saga, errors.New("recover legacy reconciliation"), now)
	}
	return true, s.executePreparedDNSFailoverSaga(ctx, conn, saga, outbox, snapshot, currentTarget, newTarget, eventType, now)
}

func applyDNSFailoverNoAction(ctx context.Context, tx *sql.Tx, outbox dnsFailoverOutboxRow, snapshot dnsFailoverWorkerSnapshot, currentTarget DNSFailoverTargetRecord, decision dnsFailoverDecision, now int64) error {
	desiredEvent := ""
	if isDNSFailoverIncident(decision.Reason) {
		desiredEvent = decision.Reason
	}
	details := dnsFailoverEventDetails(snapshot, &currentTarget, nil, decision.Reason, "", "", now)
	if desiredEvent != "" {
		message := formatDNSFailoverIncidentNotification(snapshot.Rule, desiredEvent, decision.Reason, snapshot.ProbeIDs, now)
		if err := recordDNSFailoverIncident(ctx, tx, snapshot, desiredEvent, &currentTarget, message, details, now); err != nil {
			return err
		}
	} else if decision.Reason == dnsFailoverReasonCurrentHealthy && dnsFailoverCurrentTargetHealthy(snapshot, currentTarget.ID) {
		message := formatDNSFailoverIncidentNotification(snapshot.Rule, "recovered", "检测状态已恢复", snapshot.ProbeIDs, now)
		if err := clearDNSFailoverIncident(ctx, tx, snapshot, &currentTarget, message, details, now); err != nil {
			return err
		}
	}
	return ackDNSFailoverEvaluation(ctx, tx, outbox, now)
}

func persistDNSFailoverRetryWithoutCommit(ctx context.Context, tx *sql.Tx, outbox dnsFailoverOutboxRow, retryErr error, now int64) error {
	errorText := truncateDNSFailoverError(retryErr)
	nextAttemptAt := saturatingUnixAdd(now, dnsFailoverRetryDelay(outbox.Attempts))
	_, err := tx.ExecContext(ctx, `UPDATE v2_dns_failover_eval_outbox
SET attempts = $2, next_attempt_at = $3, last_error = $4, updated_at = $5
WHERE id = $1 AND requested_at = $6`, outbox.ID, outbox.Attempts+1, nextAttemptAt, errorText, now, outbox.RequestedAt)
	return err
}

func persistDNSFailoverForcedConfigurationFailureWithoutCommit(ctx context.Context, tx *sql.Tx, outbox dnsFailoverOutboxRow, snapshot dnsFailoverWorkerSnapshot, targetID int64, retryErr error, now int64) error {
	var target *DNSFailoverTargetRecord
	if found, ok := dnsFailoverTargetByID(snapshot.Targets, targetID); ok {
		target = &found
	}
	details := dnsFailoverEventDetails(snapshot, nil, target, "config_error", retryErr.Error(), "", now)
	message := formatDNSFailoverIncidentNotification(snapshot.Rule, "config_error", retryErr.Error(), snapshot.ProbeIDs, now)
	if err := recordDNSFailoverIncident(ctx, tx, snapshot, "config_error", target, message, details, now); err != nil {
		return err
	}
	return persistDNSFailoverRetryWithoutCommit(ctx, tx, outbox, retryErr, now)
}

func (s *DBService) executePreparedDNSFailoverSaga(ctx context.Context, conn *sql.Conn, saga dnsFailoverSagaRecord, outbox dnsFailoverOutboxRow, snapshot dnsFailoverWorkerSnapshot, oldTarget, newTarget DNSFailoverTargetRecord, eventType string, now int64) error {
	client, err := s.dnsFailoverClient()
	if err != nil {
		s.logDNSFailoverProviderFailure(ctx, saga, "desired", saga.DesiredMutation, err, now)
		recoveryErr := s.recoverPreparedDNSFailoverSaga(ctx, conn, saga, err, now)
		return errors.Join(err, recoveryErr)
	}
	desiredTargetID := saga.DesiredTargetID
	s.writeDNSFailoverLogsBestEffort(ctx, dnsFailoverLogEntry{
		GroupID: saga.GroupID, TargetID: &desiredTargetID, Stage: "dns_provider", Level: "info", Outcome: "request_started",
		Message: "sending desired DNS mutation", Details: dnsFailoverProviderMutationDetails("desired", saga.DesiredMutation), CreatedAt: now,
	})
	dnsCtx, cancel := context.WithTimeout(ctx, dnsFailoverDNSPodTimeout)
	result, mutationErr := client.ModifyRecord(dnsCtx, saga.DesiredMutation.request())
	cancel()
	if mutationErr != nil {
		s.logDNSFailoverProviderFailure(ctx, saga, "desired", saga.DesiredMutation, mutationErr, now)
		recoveryErr := s.recoverPreparedDNSFailoverSaga(ctx, conn, saga, mutationErr, now)
		return errors.Join(mutationErr, recoveryErr)
	}
	providerDetails := dnsFailoverProviderMutationDetails("desired", saga.DesiredMutation)
	providerDetails["request_id"] = result.RequestID
	s.writeDNSFailoverLogsBestEffort(ctx, dnsFailoverLogEntry{
		GroupID: saga.GroupID, TargetID: &desiredTargetID, Stage: "dns_provider", Level: "info", Outcome: "success",
		Message: "desired DNS mutation succeeded", Details: providerDetails, CreatedAt: now,
	})
	finalizeErr := func() error {
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		committed := false
		defer func() {
			if !committed {
				_ = tx.Rollback()
			}
		}()
		if err := clearDNSFailoverDNSIncidentAfterDesired(ctx, tx, saga.GroupID); err != nil {
			return err
		}
		if saga.OriginalOperation == dnsFailoverOperationEvaluate && snapshot.ActiveIncidentType != "" {
			details := dnsFailoverEventDetails(snapshot, &oldTarget, &newTarget, "recovered", "", result.RequestID, now)
			if err := clearDNSFailoverIncident(ctx, tx, snapshot, &oldTarget,
				formatDNSFailoverIncidentNotification(snapshot.Rule, "recovered", "检测状态已恢复", snapshot.ProbeIDs, now), details, now); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE v2_dns_failover_group
SET current_target_id = $2, last_switch_at = $3, last_switch_reason = $4, last_evaluated_at = $3, updated_at = $3
WHERE id = $1`, saga.GroupID, saga.DesiredTargetID, now, saga.Reason); err != nil {
			return err
		}
		details := dnsFailoverEventDetails(snapshot, &oldTarget, &newTarget, saga.Reason, "", result.RequestID, now)
		decisionProbeIDs := dnsFailoverDecisionAvailableProbeIDList(snapshot.Probes, snapshot.States, oldTarget.ID)
		message := formatDNSFailoverSwitchNotification(snapshot.Rule, oldTarget, newTarget, saga.Reason, decisionProbeIDs, len(decisionProbeIDs) == 1, time.Unix(now, 0))
		if err := insertDNSFailoverEvent(ctx, tx, saga.GroupID, &newTarget.ID, eventType, message, details, now); err != nil {
			return err
		}
		if err := deleteDNSFailoverOutboxVersion(ctx, tx, outbox); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM v2_dns_failover_saga WHERE group_id = $1 AND phase = 'prepared'`, saga.GroupID); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		committed = true
		return nil
	}()
	if finalizeErr == nil {
		s.writeDNSFailoverLogsBestEffort(ctx,
			dnsFailoverLogEntry{
				GroupID: saga.GroupID, TargetID: &desiredTargetID, Stage: "switch", Level: "info", Outcome: "succeeded",
				Message: "DNS failover switch completed", Details: map[string]any{
					"from_target_id": oldTarget.ID, "to_target_id": newTarget.ID, "reason": saga.Reason, "request_id": result.RequestID,
				}, CreatedAt: now,
			},
			dnsFailoverLogEntry{
				GroupID: saga.GroupID, TargetID: &desiredTargetID, Stage: "saga", Level: "info", Outcome: "finalized",
				Message: "prepared switch saga finalized", Details: map[string]any{
					"rollback_target_id": saga.RollbackTargetID, "desired_target_id": saga.DesiredTargetID,
				}, CreatedAt: now,
			},
			dnsFailoverLogEntry{
				GroupID: saga.GroupID, TargetID: &desiredTargetID, Stage: "outbox", Level: "info", Outcome: "acked",
				Message: "evaluation outbox acknowledged", Details: map[string]any{
					"outbox_id": outbox.ID, "requested_at": outbox.RequestedAt, "operation": outbox.Operation,
				}, CreatedAt: now,
			},
		)
		return nil
	}
	// A failed COMMIT is ambiguous. Only compensate when the durable prepared
	// row can still be observed; absence means the finalize transaction won.
	var sagaStillPrepared bool
	checkCtx, cancelCheck := context.WithTimeout(context.WithoutCancel(ctx), dnsFailoverRecoveryTimeout)
	checkErr := conn.QueryRowContext(checkCtx, `SELECT EXISTS (SELECT 1 FROM v2_dns_failover_saga WHERE group_id = $1 AND phase = 'prepared')`, saga.GroupID).Scan(&sagaStillPrepared)
	cancelCheck()
	if checkErr != nil || !sagaStillPrepared {
		return fmt.Errorf("finalize DNS failover saga: %w", finalizeErr)
	}
	recoveryErr := s.recoverPreparedDNSFailoverSaga(ctx, conn, saga, fmt.Errorf("finalize DNS failover saga: %w", finalizeErr), now)
	return errors.Join(fmt.Errorf("finalize DNS failover saga: %w", finalizeErr), recoveryErr)
}

func (s *DBService) logDNSFailoverProviderFailure(ctx context.Context, saga dnsFailoverSagaRecord, operation string, mutation dnsFailoverMutationSnapshot, providerErr error, now int64) {
	targetID := saga.DesiredTargetID
	if operation == "rollback" {
		targetID = saga.RollbackTargetID
	}
	safeError := dnsFailoverSafeDiagnosticError(providerErr)
	details := dnsFailoverProviderMutationDetails(operation, mutation)
	details["error"] = safeError
	s.writeDNSFailoverLogsBestEffort(ctx,
		dnsFailoverLogEntry{
			GroupID: saga.GroupID, TargetID: &targetID, Stage: "dns_provider", Level: "error", Outcome: "error",
			Message: operation + " DNS mutation failed", Details: details, CreatedAt: now,
		},
		dnsFailoverLogEntry{
			GroupID: saga.GroupID, TargetID: &targetID, Stage: "switch", Level: "error", Outcome: "failed",
			Message: "DNS failover switch failed", Details: map[string]any{
				"operation": operation, "error": safeError, "reason": saga.Reason,
				"desired_target_id": saga.DesiredTargetID, "rollback_target_id": saga.RollbackTargetID,
			}, CreatedAt: now,
		},
	)
}

func dnsFailoverProviderMutationDetails(operation string, mutation dnsFailoverMutationSnapshot) map[string]any {
	return map[string]any{
		"operation": operation, "domain": mutation.Domain, "domain_id": mutation.DomainID,
		"record_id": mutation.RecordID, "subdomain": mutation.SubDomain, "record_type": mutation.RecordType,
		"record_line_id": mutation.RecordLineID, "value": mutation.Value, "ttl": mutation.TTL,
		"mx": mutation.MX, "weight": mutation.Weight,
	}
}

func clearDNSFailoverDNSIncidentAfterDesired(ctx context.Context, tx *sql.Tx, groupID int64) error {
	var active string
	if err := tx.QueryRowContext(ctx, `SELECT active_dns_incident_type FROM v2_dns_failover_group WHERE id = $1 FOR UPDATE`, groupID).Scan(&active); err != nil {
		return err
	}
	if active == "dns_state_diverged" {
		return errors.New("DNS state is still diverged; prepared switch cannot finalize")
	}
	if active == "" {
		return nil
	}
	_, err := tx.ExecContext(ctx, `UPDATE v2_dns_failover_group SET active_dns_incident_type = '', active_dns_incident_since = NULL WHERE id = $1`, groupID)
	return err
}

func (s *DBService) recoverPreparedDNSFailoverSaga(ctx context.Context, conn *sql.Conn, saga dnsFailoverSagaRecord, cause error, now int64) error {
	rollbackTargetID := saga.RollbackTargetID
	s.writeDNSFailoverLogsBestEffort(ctx, dnsFailoverLogEntry{
		GroupID: saga.GroupID, TargetID: &rollbackTargetID, Stage: "dns_provider", Level: "warning", Outcome: "request_started",
		Message: "sending rollback DNS mutation", Details: dnsFailoverProviderMutationDetails("rollback", saga.RollbackMutation), CreatedAt: now,
	})
	client, err := s.dnsFailoverClient()
	var result dnspod.RecordMutationResult
	if err == nil {
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dnsFailoverDNSPodTimeout)
		result, err = client.ModifyRecord(rollbackCtx, saga.RollbackMutation.request())
		cancel()
	}
	if err != nil {
		s.logDNSFailoverProviderFailure(ctx, saga, "rollback", saga.RollbackMutation, err, now)
		return s.persistDNSFailoverSagaRollbackFailure(ctx, conn, saga, errors.Join(cause, fmt.Errorf("rollback DNS failed: %w", err)), now)
	}
	providerDetails := dnsFailoverProviderMutationDetails("rollback", saga.RollbackMutation)
	providerDetails["request_id"] = result.RequestID
	s.writeDNSFailoverLogsBestEffort(ctx, dnsFailoverLogEntry{
		GroupID: saga.GroupID, TargetID: &rollbackTargetID, Stage: "dns_provider", Level: "info", Outcome: "success",
		Message: "rollback DNS mutation succeeded", Details: providerDetails, CreatedAt: now,
	})
	return s.finalizeDNSFailoverSagaRecovery(ctx, conn, saga, cause, now)
}

func (s *DBService) persistDNSFailoverSagaRollbackFailure(ctx context.Context, conn *sql.Conn, saga dnsFailoverSagaRecord, rollbackErr error, now int64) error {
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dnsFailoverRecoveryTimeout)
	defer cancel()
	tx, err := conn.BeginTx(persistCtx, nil)
	if err != nil {
		return rollbackErr
	}
	defer tx.Rollback()
	errorText := truncateDNSFailoverError(rollbackErr)
	nextAttemptAt := saturatingUnixAdd(now, dnsFailoverRetryDelay(saga.Attempts))
	if _, err := tx.ExecContext(persistCtx, `UPDATE v2_dns_failover_saga
SET attempts = attempts + 1, next_attempt_at = $2, last_error = $3, updated_at = $4
WHERE group_id = $1 AND phase = 'prepared'`, saga.GroupID, nextAttemptAt, errorText, now); err != nil {
		return errors.Join(rollbackErr, err)
	}
	if err := setDNSFailoverDNSIncident(persistCtx, tx, saga, "dns_state_diverged", errorText, now); err != nil {
		return errors.Join(rollbackErr, err)
	}
	if err := tx.Commit(); err != nil {
		return errors.Join(rollbackErr, err)
	}
	targetID := saga.DesiredTargetID
	s.writeDNSFailoverLogsBestEffort(ctx, dnsFailoverLogEntry{
		GroupID: saga.GroupID, TargetID: &targetID, Stage: "saga", Level: "error", Outcome: "retry_scheduled",
		Message: "prepared saga rollback failed and was scheduled for retry", Details: map[string]any{
			"attempt": saga.Attempts + 1, "next_attempt_at": nextAttemptAt,
			"error": dnsFailoverSafeDiagnosticError(rollbackErr), "desired_target_id": saga.DesiredTargetID,
			"rollback_target_id": saga.RollbackTargetID,
		}, CreatedAt: now,
	})
	return rollbackErr
}

func (s *DBService) finalizeDNSFailoverSagaRecovery(ctx context.Context, conn *sql.Conn, saga dnsFailoverSagaRecord, cause error, now int64) error {
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dnsFailoverRecoveryTimeout)
	defer cancel()
	tx, err := conn.BeginTx(persistCtx, nil)
	if err != nil {
		return errors.Join(cause, err)
	}
	defer tx.Rollback()
	if saga.OriginalOperation == dnsFailoverOperationManual {
		if _, err := tx.ExecContext(persistCtx, `INSERT INTO v2_dns_failover_eval_outbox (
group_id, operation, target_id, source_target_id, requested_at, attempts, next_attempt_at, last_error, created_at, updated_at
) VALUES ($1, 'manual', $2, $3, $4, 1, $5, $6, $4, $4)
ON CONFLICT (group_id) DO UPDATE SET operation = 'manual', target_id = EXCLUDED.target_id,
source_target_id = EXCLUDED.source_target_id, attempts = v2_dns_failover_eval_outbox.attempts + 1,
next_attempt_at = EXCLUDED.next_attempt_at, last_error = EXCLUDED.last_error, updated_at = EXCLUDED.updated_at`,
			saga.GroupID, saga.OriginalTargetID.Int64, saga.RollbackTargetID, saga.OriginalRequestedAt,
			saturatingUnixAdd(now, dnsFailoverRetryDelay(saga.Attempts)), truncateDNSFailoverError(cause)); err != nil {
			return errors.Join(cause, err)
		}
	} else {
		if _, err := tx.ExecContext(persistCtx, `INSERT INTO v2_dns_failover_eval_outbox (
group_id, operation, target_id, source_target_id, requested_at, attempts, next_attempt_at, last_error, created_at, updated_at
) VALUES ($1, 'evaluate', NULL, NULL, $2, 1, $3, $4, $2, $2)
ON CONFLICT (group_id) DO UPDATE SET operation = 'evaluate', target_id = NULL, source_target_id = NULL,
requested_at = GREATEST(v2_dns_failover_eval_outbox.requested_at, EXCLUDED.requested_at),
attempts = v2_dns_failover_eval_outbox.attempts + 1, next_attempt_at = EXCLUDED.next_attempt_at,
last_error = EXCLUDED.last_error, updated_at = EXCLUDED.updated_at
WHERE v2_dns_failover_eval_outbox.operation IN ('evaluate', 'reconcile')`, saga.GroupID, saga.OriginalRequestedAt,
			saturatingUnixAdd(now, dnsFailoverRetryDelay(saga.Attempts)), truncateDNSFailoverError(cause)); err != nil {
			return errors.Join(cause, err)
		}
	}
	if err := completeDNSFailoverDNSIncidentAfterRollback(persistCtx, tx, saga, cause, now); err != nil {
		return errors.Join(cause, err)
	}
	if _, err := tx.ExecContext(persistCtx, `DELETE FROM v2_dns_failover_saga WHERE group_id = $1 AND phase = 'prepared'`, saga.GroupID); err != nil {
		return errors.Join(cause, err)
	}
	if err := tx.Commit(); err != nil {
		return errors.Join(cause, err)
	}
	desiredTargetID := saga.DesiredTargetID
	safeCause := dnsFailoverSafeDiagnosticError(cause)
	nextAttemptAt := saturatingUnixAdd(now, dnsFailoverRetryDelay(saga.Attempts))
	var retryTargetID *int64
	if saga.OriginalOperation == dnsFailoverOperationManual && saga.OriginalTargetID.Valid {
		targetID := saga.OriginalTargetID.Int64
		retryTargetID = &targetID
	}
	s.writeDNSFailoverLogsBestEffort(ctx,
		dnsFailoverLogEntry{
			GroupID: saga.GroupID, TargetID: &desiredTargetID, Stage: "switch", Level: "error", Outcome: "rolled_back",
			Message: "DNS switch was rolled back", Details: map[string]any{
				"reason": saga.Reason, "cause": safeCause,
				"desired_target_id": saga.DesiredTargetID, "rollback_target_id": saga.RollbackTargetID,
			}, CreatedAt: now,
		},
		dnsFailoverLogEntry{
			GroupID: saga.GroupID, TargetID: &desiredTargetID, Stage: "saga", Level: "warning", Outcome: "recovered",
			Message: "prepared DNS switch saga recovered", Details: map[string]any{
				"cause": safeCause, "desired_target_id": saga.DesiredTargetID,
				"rollback_target_id": saga.RollbackTargetID,
			}, CreatedAt: now,
		},
		dnsFailoverLogEntry{
			GroupID: saga.GroupID, TargetID: retryTargetID, Stage: "outbox", Level: "warning", Outcome: "retry_scheduled",
			Message: "DNS failover operation scheduled for retry", Details: map[string]any{
				"operation": saga.OriginalOperation, "attempt": saga.Attempts + 1,
				"requested_at": saga.OriginalRequestedAt, "next_attempt_at": nextAttemptAt,
				"last_error": safeCause,
			}, CreatedAt: now,
		},
	)
	return nil
}

func setDNSFailoverDNSIncident(ctx context.Context, tx *sql.Tx, saga dnsFailoverSagaRecord, eventType, errorText string, now int64) error {
	var active string
	if err := tx.QueryRowContext(ctx, `SELECT active_dns_incident_type FROM v2_dns_failover_group WHERE id = $1 FOR UPDATE`, saga.GroupID).Scan(&active); err != nil {
		return err
	}
	if active == eventType {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE v2_dns_failover_group SET active_dns_incident_type = $2, active_dns_incident_since = $3 WHERE id = $1`, saga.GroupID, eventType, now); err != nil {
		return err
	}
	details := map[string]any{
		"desired_target":  map[string]any{"id": saga.DesiredTargetID, "dns_type": saga.DesiredMutation.RecordType, "dns_value": saga.DesiredMutation.Value},
		"actual_target":   map[string]any{"id": saga.DesiredTargetID, "dns_type": saga.DesiredMutation.RecordType, "dns_value": saga.DesiredMutation.Value},
		"rollback_target": map[string]any{"id": saga.RollbackTargetID, "dns_type": saga.RollbackMutation.RecordType, "dns_value": saga.RollbackMutation.Value},
		"rollback_error":  errorText, "time": time.Unix(now, 0).UTC().Format(time.RFC3339),
	}
	message := fmt.Sprintf("【DNS 故障转移】记录：%s.%s\n状态：%s\n原因：%s\n时间：%s", saga.RollbackMutation.SubDomain, saga.RollbackMutation.Domain, eventType, errorText, time.Unix(now, 0).UTC().Format(time.RFC3339))
	return insertDNSFailoverEvent(ctx, tx, saga.GroupID, &saga.DesiredTargetID, eventType, message, details, now)
}

func completeDNSFailoverDNSIncidentAfterRollback(ctx context.Context, tx *sql.Tx, saga dnsFailoverSagaRecord, cause error, now int64) error {
	var active string
	if err := tx.QueryRowContext(ctx, `SELECT active_dns_incident_type FROM v2_dns_failover_group WHERE id = $1 FOR UPDATE`, saga.GroupID).Scan(&active); err != nil {
		return err
	}
	if active == "dnspod_error" {
		return nil
	}
	if active == "dns_state_diverged" {
		if _, err := tx.ExecContext(ctx, `UPDATE v2_dns_failover_group SET active_dns_incident_type = '', active_dns_incident_since = NULL WHERE id = $1`, saga.GroupID); err != nil {
			return err
		}
		details := map[string]any{
			"rollback_target": map[string]any{"id": saga.RollbackTargetID, "dns_type": saga.RollbackMutation.RecordType, "dns_value": saga.RollbackMutation.Value},
			"time":            time.Unix(now, 0).UTC().Format(time.RFC3339),
		}
		message := fmt.Sprintf("【DNS 故障转移】记录：%s.%s\n状态：dns_state_reconciled\n时间：%s", saga.RollbackMutation.SubDomain, saga.RollbackMutation.Domain, time.Unix(now, 0).UTC().Format(time.RFC3339))
		return insertDNSFailoverEvent(ctx, tx, saga.GroupID, &saga.RollbackTargetID, "dns_state_reconciled", message, details, now)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE v2_dns_failover_group SET active_dns_incident_type = $2, active_dns_incident_since = $3 WHERE id = $1`, saga.GroupID, "dnspod_error", now); err != nil {
		return err
	}
	details := map[string]any{
		"desired_target":  map[string]any{"id": saga.DesiredTargetID, "dns_type": saga.DesiredMutation.RecordType, "dns_value": saga.DesiredMutation.Value},
		"rollback_target": map[string]any{"id": saga.RollbackTargetID, "dns_type": saga.RollbackMutation.RecordType, "dns_value": saga.RollbackMutation.Value},
		"error":           truncateDNSFailoverError(cause),
		"time":            time.Unix(now, 0).UTC().Format(time.RFC3339),
	}
	message := fmt.Sprintf("【DNS 故障转移】记录：%s.%s\n状态：dnspod_error\n原因：%s\n时间：%s", saga.RollbackMutation.SubDomain, saga.RollbackMutation.Domain, truncateDNSFailoverError(cause), time.Unix(now, 0).UTC().Format(time.RFC3339))
	return insertDNSFailoverEvent(ctx, tx, saga.GroupID, &saga.DesiredTargetID, "dnspod_error", message, details, now)
}
