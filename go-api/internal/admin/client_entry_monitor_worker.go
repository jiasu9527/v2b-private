package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const clientEntryMonitorNotificationLockKey int64 = -7_642_309_018

type clientEntryMonitorChatNotifier interface {
	NotifyChat(context.Context, int64, string) error
}

type clientEntryMonitorImageNotifier interface {
	NotifyChatImage(context.Context, int64, []byte, string) error
}

type clientEntryMonitorProgressNotifier interface {
	EditChatMessage(context.Context, int64, int64, string) error
}

type clientEntryMonitorEventNotifier interface {
	ListAdminChats(context.Context, bool) ([]int64, error)
	NotifyChat(context.Context, int64, string) error
}

type clientEntryMonitorEventDelivery struct {
	ChatID        int64
	DeliveredAt   sql.NullInt64
	Attempts      int
	NextAttemptAt int64
	LastError     string
}

func (s *DBService) drainPendingClientEntryMonitorNotifications(ctx context.Context, limit int) error {
	if s == nil || s.db == nil || limit <= 0 {
		return nil
	}
	now := time.Now().Unix()
	if _, err := s.db.ExecContext(ctx, `UPDATE v2_client_entry_monitor_run
SET status = 'timeout', completed_at = $1, updated_at = $1
WHERE status = 'running' AND started_at < $2`, now, now-int64(clientEntryMonitorRunTimeout/time.Second)); err != nil {
		return fmt.Errorf("expire client entry monitor runs: %w", err)
	}
	// Automatic splitting and Telegram delivery share this worker cycle, but
	// they are independent durable queues. Keep draining notifications when a
	// malformed or temporarily unavailable split operation fails; otherwise one
	// permanently failing operation would suppress every fault/recovery alert
	// and manual-run report on every subsequent cycle.
	var autoSplitErr error
	if err := s.drainPendingClientEntryAutoSplits(ctx, limit); err != nil {
		autoSplitErr = fmt.Errorf("drain client entry automatic splits: %w", err)
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return errors.Join(autoSplitErr, fmt.Errorf("open client entry notification connection: %w", err))
	}
	defer conn.Close()
	var locked bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, clientEntryMonitorNotificationLockKey).Scan(&locked); err != nil {
		return errors.Join(autoSplitErr, fmt.Errorf("lock client entry notification delivery: %w", err))
	}
	if !locked {
		return autoSplitErr
	}
	defer releaseDNSFailoverSessionLock(conn, clientEntryMonitorNotificationLockKey)
	for range limit {
		processed, err := s.notifyNextClientEntryMonitorProgress(ctx, conn)
		if err != nil {
			return errors.Join(autoSplitErr, err)
		}
		if !processed {
			break
		}
	}
	for range limit {
		processed, err := s.notifyNextClientEntryMonitorEvent(ctx, conn)
		if err != nil {
			return errors.Join(autoSplitErr, err)
		}
		if !processed {
			break
		}
	}
	for range limit {
		processed, err := s.notifyNextClientEntryMonitorRun(ctx, conn)
		if err != nil {
			return errors.Join(autoSplitErr, err)
		}
		if !processed {
			break
		}
	}
	return autoSplitErr
}

// notifyNextClientEntryMonitorProgress edits the original rule picker message
// whenever a manual run's observable state changes. It is deliberately
// separate from final report delivery so a temporary edit failure never blocks
// the durable image notification.
func (s *DBService) notifyNextClientEntryMonitorProgress(ctx context.Context, conn *sql.Conn) (bool, error) {
	now := time.Now().Unix()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin client entry progress notification: %w", err)
	}
	var (
		runID       int64
		chatID      int64
		messageID   int64
		requestedBy sql.NullInt64
		attempts    int
	)
	err = tx.QueryRowContext(ctx, `SELECT id, requested_by_user_id, request_chat_id, progress_message_id, progress_attempts
FROM v2_client_entry_monitor_run
WHERE request_chat_id IS NOT NULL
  AND progress_message_id IS NOT NULL
  AND progress_next_attempt_at <= $1
  AND (progress_reported_results IS DISTINCT FROM received_results
       OR progress_reported_status IS DISTINCT FROM status)
ORDER BY id ASC
LIMIT 1
FOR UPDATE SKIP LOCKED`, now).Scan(&runID, &requestedBy, &chatID, &messageID, &attempts)
	if errors.Is(err, sql.ErrNoRows) {
		_ = tx.Rollback()
		return false, nil
	}
	if err != nil {
		_ = tx.Rollback()
		return false, fmt.Errorf("claim client entry progress notification: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE v2_client_entry_monitor_run
SET progress_attempts = progress_attempts + 1, progress_next_attempt_at = $2, updated_at = $3
WHERE id = $1`, runID, saturatingUnixAdd(now, dnsFailoverNotificationLease), now); err != nil {
		_ = tx.Rollback()
		return false, fmt.Errorf("advance client entry progress attempt: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit client entry progress notification claim: %w", err)
	}

	authorized, err := s.clientEntryMonitorRunRequesterAuthorized(ctx, requestedBy, chatID)
	if err != nil {
		return false, fmt.Errorf("revalidate client entry progress requester: %w", err)
	}
	if !authorized {
		if _, err := conn.ExecContext(ctx, `UPDATE v2_client_entry_monitor_run
SET progress_message_id = NULL, progress_attempts = 0, progress_next_attempt_at = 0,
	progress_last_error = '', updated_at = $2
WHERE id = $1`, runID, time.Now().Unix()); err != nil {
			return false, fmt.Errorf("consume unauthorized client entry progress notification: %w", err)
		}
		return true, nil
	}

	run, err := s.loadClientEntryMonitorRun(ctx, runID)
	if err != nil {
		return false, err
	}
	notifier, ok := s.dnsFailoverNotifier.(clientEntryMonitorProgressNotifier)
	if !ok {
		err = ErrDNSFailoverNotifierUnavailable
	} else {
		err = notifier.EditChatMessage(ctx, chatID, messageID, formatClientEntryMonitorProgress(run, run.ExpectedPairs, time.Now()))
	}
	if err != nil {
		finishedAt := time.Now().Unix()
		if isPermanentClientEntryMonitorProgressError(err) {
			if _, updateErr := conn.ExecContext(ctx, `UPDATE v2_client_entry_monitor_run
SET progress_message_id = NULL, progress_attempts = 0, progress_next_attempt_at = 0,
	progress_last_error = $2, updated_at = $3
WHERE id = $1`, runID, truncateDNSFailoverError(err), finishedAt); updateErr != nil {
				return false, fmt.Errorf("consume permanent client entry progress notification failure: %w", updateErr)
			}
			return true, nil
		}
		next := saturatingUnixAdd(finishedAt, dnsFailoverRetryDelay(attempts))
		if _, updateErr := conn.ExecContext(ctx, `UPDATE v2_client_entry_monitor_run
SET progress_next_attempt_at = $2, progress_last_error = $3, updated_at = $4
WHERE id = $1`, runID, next, truncateDNSFailoverError(err), finishedAt); updateErr != nil {
			return false, fmt.Errorf("persist client entry progress notification failure: %w", updateErr)
		}
		return true, nil
	}
	if _, err := conn.ExecContext(ctx, `UPDATE v2_client_entry_monitor_run
SET progress_reported_results = $2, progress_reported_status = $3,
progress_attempts = 0, progress_next_attempt_at = 0, progress_last_error = '', updated_at = $4
WHERE id = $1`, runID, run.ReceivedResults, run.Status, time.Now().Unix()); err != nil {
		return false, fmt.Errorf("mark client entry progress notified: %w", err)
	}
	return true, nil
}

func isPermanentClientEntryMonitorProgressError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(strings.TrimSpace(err.Error()))
	for _, marker := range []string{
		"message to edit not found",
		"message not found",
		"message can't be edited",
		"message cant be edited",
		"message cannot be edited",
		"bot was blocked",
		"bot is blocked",
		"forbidden",
		"chat not found",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func (s *DBService) notifyNextClientEntryMonitorEvent(ctx context.Context, conn *sql.Conn) (bool, error) {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin client entry event notification: %w", err)
	}
	var eventID int64
	var message string
	var attempts int
	now := time.Now().Unix()
	err = tx.QueryRowContext(ctx, `SELECT id, message, notify_attempts
FROM v2_client_entry_monitor_event
WHERE notified_at IS NULL AND notify_next_attempt_at <= $1
ORDER BY id ASC LIMIT 1
FOR UPDATE SKIP LOCKED`, now).Scan(&eventID, &message, &attempts)
	if errors.Is(err, sql.ErrNoRows) {
		_ = tx.Rollback()
		return false, nil
	}
	if err != nil {
		_ = tx.Rollback()
		return false, fmt.Errorf("claim client entry event notification: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE v2_client_entry_monitor_event
SET notify_attempts = notify_attempts + 1, notify_next_attempt_at = $2 WHERE id = $1`, eventID, saturatingUnixAdd(now, dnsFailoverNotificationLease)); err != nil {
		_ = tx.Rollback()
		return false, fmt.Errorf("advance client entry event notification attempt: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit client entry event notification claim: %w", err)
	}
	notifier, ok := s.dnsFailoverNotifier.(clientEntryMonitorEventNotifier)
	if !ok {
		if err := rescheduleClientEntryMonitorEvent(ctx, conn, eventID, attempts, ErrDNSFailoverNotifierUnavailable); err != nil {
			return false, err
		}
		return true, nil
	}
	chatIDs, err := notifier.ListAdminChats(ctx, true)
	if err != nil {
		if persistErr := rescheduleClientEntryMonitorEvent(ctx, conn, eventID, attempts, err); persistErr != nil {
			return false, persistErr
		}
		return true, nil
	}
	chatIDs = uniquePositiveClientEntryMonitorChatIDs(chatIDs)
	deliveries, err := syncClientEntryMonitorEventDeliveries(ctx, conn, eventID, chatIDs, time.Now().Unix())
	if err != nil {
		return false, err
	}

	allDelivered := true
	nextAttemptAt := int64(0)
	deliveryErrors := make([]error, 0)
	for _, chatID := range chatIDs {
		delivery := deliveries[chatID]
		if delivery.DeliveredAt.Valid {
			continue
		}
		now = time.Now().Unix()
		if delivery.NextAttemptAt > now {
			allDelivered = false
			nextAttemptAt = minPositiveInt64(nextAttemptAt, delivery.NextAttemptAt)
			if delivery.LastError != "" {
				deliveryErrors = append(deliveryErrors, errors.New(delivery.LastError))
			}
			continue
		}
		if _, err := conn.ExecContext(ctx, `UPDATE v2_client_entry_monitor_event_delivery
SET attempts = attempts + 1, next_attempt_at = $3, updated_at = $4
WHERE event_id = $1 AND chat_id = $2 AND delivered_at IS NULL`, eventID, chatID,
			saturatingUnixAdd(now, dnsFailoverNotificationLease), now); err != nil {
			return false, fmt.Errorf("claim client entry monitor event recipient: %w", err)
		}
		// Availability transitions should stay immediately readable in the chat.
		// Images are reserved for the final, explicitly requested active run.
		notifyErr := notifier.NotifyChat(ctx, chatID, message)
		finishedAt := time.Now().Unix()
		if notifyErr != nil {
			next := saturatingUnixAdd(finishedAt, dnsFailoverRetryDelay(delivery.Attempts))
			if _, err := conn.ExecContext(ctx, `UPDATE v2_client_entry_monitor_event_delivery
SET next_attempt_at = $3, last_error = $4, updated_at = $5
WHERE event_id = $1 AND chat_id = $2 AND delivered_at IS NULL`, eventID, chatID,
				next, truncateDNSFailoverError(notifyErr), finishedAt); err != nil {
				return false, fmt.Errorf("persist client entry monitor recipient failure: %w", err)
			}
			allDelivered = false
			nextAttemptAt = minPositiveInt64(nextAttemptAt, next)
			deliveryErrors = append(deliveryErrors, notifyErr)
			continue
		}
		if _, err := conn.ExecContext(ctx, `UPDATE v2_client_entry_monitor_event_delivery
SET delivered_at = $3, next_attempt_at = 0, last_error = '', updated_at = $3
WHERE event_id = $1 AND chat_id = $2 AND delivered_at IS NULL`, eventID, chatID, finishedAt); err != nil {
			return false, fmt.Errorf("mark client entry monitor recipient delivered: %w", err)
		}
	}

	if allDelivered {
		if _, err := conn.ExecContext(ctx, `UPDATE v2_client_entry_monitor_event
SET notified_at = $2, notify_next_attempt_at = 0, last_notify_error = ''
WHERE id = $1 AND notified_at IS NULL`, eventID, time.Now().Unix()); err != nil {
			return false, fmt.Errorf("mark client entry monitor event notified: %w", err)
		}
		return true, nil
	}
	if nextAttemptAt <= 0 {
		nextAttemptAt = saturatingUnixAdd(time.Now().Unix(), dnsFailoverRetryDelay(attempts))
	}
	lastError := "部分 Telegram 收件人等待重试"
	if joined := errors.Join(deliveryErrors...); joined != nil {
		lastError = truncateDNSFailoverError(joined)
	}
	if _, err := conn.ExecContext(ctx, `UPDATE v2_client_entry_monitor_event
SET notify_next_attempt_at = $2, last_notify_error = $3
WHERE id = $1 AND notified_at IS NULL`, eventID, nextAttemptAt, lastError); err != nil {
		return false, fmt.Errorf("schedule client entry monitor event retry: %w", err)
	}
	return true, nil
}

func uniquePositiveClientEntryMonitorChatIDs(values []int64) []int64 {
	result := make([]int64, 0, len(values))
	seen := make(map[int64]struct{}, len(values))
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func syncClientEntryMonitorEventDeliveries(ctx context.Context, conn *sql.Conn, eventID int64, chatIDs []int64, now int64) (map[int64]clientEntryMonitorEventDelivery, error) {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin client entry event recipient sync: %w", err)
	}
	defer tx.Rollback()
	for _, chatID := range chatIDs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO v2_client_entry_monitor_event_delivery
(event_id, chat_id, delivered_at, attempts, next_attempt_at, last_error, created_at, updated_at)
VALUES ($1, $2, NULL, 0, $3, '', $3, $3)
ON CONFLICT (event_id, chat_id) DO NOTHING`, eventID, chatID, now); err != nil {
			return nil, fmt.Errorf("create client entry event recipient: %w", err)
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT chat_id, delivered_at, attempts, next_attempt_at, last_error
FROM v2_client_entry_monitor_event_delivery
WHERE event_id = $1
ORDER BY chat_id
FOR UPDATE`, eventID)
	if err != nil {
		return nil, fmt.Errorf("load client entry event recipients: %w", err)
	}
	deliveries := make(map[int64]clientEntryMonitorEventDelivery, len(chatIDs))
	for rows.Next() {
		var delivery clientEntryMonitorEventDelivery
		if err := rows.Scan(&delivery.ChatID, &delivery.DeliveredAt, &delivery.Attempts, &delivery.NextAttemptAt, &delivery.LastError); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan client entry event recipient: %w", err)
		}
		deliveries[delivery.ChatID] = delivery
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate client entry event recipients: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close client entry event recipients: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit client entry event recipient sync: %w", err)
	}
	for _, chatID := range chatIDs {
		if _, exists := deliveries[chatID]; !exists {
			return nil, fmt.Errorf("client entry event recipient %d was not persisted", chatID)
		}
	}
	return deliveries, nil
}

func rescheduleClientEntryMonitorEvent(ctx context.Context, conn *sql.Conn, eventID int64, attempts int, notifyErr error) error {
	if _, err := conn.ExecContext(ctx, `UPDATE v2_client_entry_monitor_event
SET notify_next_attempt_at = $2, last_notify_error = $3
WHERE id = $1 AND notified_at IS NULL`, eventID,
		saturatingUnixAdd(time.Now().Unix(), dnsFailoverRetryDelay(attempts)), truncateDNSFailoverError(notifyErr)); err != nil {
		return fmt.Errorf("persist client entry monitor event notification failure: %w", err)
	}
	return nil
}

func minPositiveInt64(left, right int64) int64 {
	if left <= 0 || (right > 0 && right < left) {
		return right
	}
	return left
}

func (s *DBService) notifyNextClientEntryMonitorRun(ctx context.Context, conn *sql.Conn) (bool, error) {
	now := time.Now().Unix()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin client entry run notification: %w", err)
	}
	var runID, chatID int64
	var requestedBy sql.NullInt64
	var attempts int
	err = tx.QueryRowContext(ctx, `SELECT id, requested_by_user_id, request_chat_id, notify_attempts
	FROM v2_client_entry_monitor_run
WHERE status IN ('completed', 'timeout')
  AND request_chat_id IS NOT NULL
  AND notified_at IS NULL
  AND notify_next_attempt_at <= $1
ORDER BY id ASC LIMIT 1
	FOR UPDATE SKIP LOCKED`, now).Scan(&runID, &requestedBy, &chatID, &attempts)
	if errors.Is(err, sql.ErrNoRows) {
		_ = tx.Rollback()
		return false, nil
	}
	if err != nil {
		_ = tx.Rollback()
		return false, fmt.Errorf("claim client entry run notification: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE v2_client_entry_monitor_run
	SET notify_attempts = notify_attempts + 1, notify_next_attempt_at = $2, updated_at = $3
	WHERE id = $1`, runID, saturatingUnixAdd(now, dnsFailoverNotificationLease), now); err != nil {
		_ = tx.Rollback()
		return false, fmt.Errorf("advance client entry run notification attempt: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit client entry run notification claim: %w", err)
	}
	authorized, err := s.clientEntryMonitorRunRequesterAuthorized(ctx, requestedBy, chatID)
	if err != nil {
		return false, fmt.Errorf("revalidate client entry monitor run requester: %w", err)
	}
	if !authorized {
		if _, err := conn.ExecContext(ctx, `UPDATE v2_client_entry_monitor_run
	SET notified_at = $2, last_notify_error = '', updated_at = $2
	WHERE id = $1 AND notified_at IS NULL`, runID, time.Now().Unix()); err != nil {
			return false, fmt.Errorf("consume unauthorized client entry monitor run notification: %w", err)
		}
		return true, nil
	}
	run, err := s.loadClientEntryMonitorRun(ctx, runID)
	if err != nil {
		return false, err
	}
	notifyErr := ErrDNSFailoverNotifierUnavailable
	if notifier, ok := s.dnsFailoverNotifier.(clientEntryMonitorChatNotifier); ok {
		message := formatClientEntryMonitorRunReport(run)
		if imageNotifier, imageCapable := notifier.(clientEntryMonitorImageNotifier); imageCapable {
			imageBytes, imageCaption, renderErr := renderClientEntryMonitorRunImage(run)
			if renderErr == nil {
				notifyErr = imageNotifier.NotifyChatImage(ctx, chatID, imageBytes, imageCaption)
			} else {
				notifyErr = notifier.NotifyChat(ctx, chatID, message)
			}
		} else {
			notifyErr = notifier.NotifyChat(ctx, chatID, message)
		}
	}
	if notifyErr != nil {
		next := saturatingUnixAdd(time.Now().Unix(), dnsFailoverRetryDelay(attempts))
		if _, err := conn.ExecContext(ctx, `UPDATE v2_client_entry_monitor_run
SET notify_next_attempt_at = $2, last_notify_error = $3, updated_at = $4
WHERE id = $1 AND notified_at IS NULL`, runID, next, truncateDNSFailoverError(notifyErr), time.Now().Unix()); err != nil {
			return false, fmt.Errorf("persist client entry monitor run notification failure: %w", err)
		}
		return true, nil
	}
	if _, err := conn.ExecContext(ctx, `UPDATE v2_client_entry_monitor_run
SET notified_at = $2, last_notify_error = '', updated_at = $2
WHERE id = $1 AND notified_at IS NULL`, runID, time.Now().Unix()); err != nil {
		return false, fmt.Errorf("mark client entry monitor run notified: %w", err)
	}
	return true, nil
}

func (s *DBService) clientEntryMonitorRunRequesterAuthorized(ctx context.Context, requestedBy sql.NullInt64, chatID int64) (bool, error) {
	if !requestedBy.Valid || requestedBy.Int64 <= 0 || chatID <= 0 {
		return false, nil
	}
	var authorized bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS (
	SELECT 1 FROM v2_user
	WHERE id = $1 AND telegram_id = $2 AND banned = 0
	  AND (is_admin = 1 OR is_staff = 1)
)`, requestedBy.Int64, chatID).Scan(&authorized)
	return authorized, err
}

func (s *DBService) loadClientEntryMonitorRun(ctx context.Context, runID int64) (ClientEntryMonitorRun, error) {
	var run ClientEntryMonitorRun
	var rawPolicies []byte
	var rawExpectedPairs []byte
	var progressMessageID sql.NullInt64
	var completed sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT id, policy_ids, expected_pairs, status, expected_results,
	received_results, progress_message_id, progress_reported_results, progress_reported_status,
	progress_next_attempt_at, progress_last_error, COALESCE(started_at, 0), completed_at, created_at
FROM v2_client_entry_monitor_run WHERE id = $1`, runID).Scan(&run.ID, &rawPolicies, &rawExpectedPairs,
		&run.Status, &run.ExpectedResults, &run.ReceivedResults, &progressMessageID, &run.ProgressReportedResults,
		&run.ProgressReportedStatus, &run.ProgressNextAttemptAt, &run.ProgressLastError, &run.StartedAt, &completed, &run.CreatedAt)
	if err != nil {
		return run, fmt.Errorf("load client entry monitor run: %w", err)
	}
	run.PolicyIDs = decodeClientEntryMonitorPolicyIDs(rawPolicies)
	run.ExpectedPairs, err = decodeClientEntryMonitorRunPairs(rawExpectedPairs)
	if err != nil {
		return run, err
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
	rows, err := s.db.QueryContext(ctx, `SELECT result.id, result.policy_id, result.policy_name, result.target_id, result.target_name,
result.host, result.port, result.probe_id, result.probe_name, result.success,
result.latency_ms, result.error, result.resolved_ip, result.reported_at
	FROM v2_client_entry_monitor_run_result result
	WHERE result.run_id = $1
	ORDER BY result.success ASC, result.target_id, result.probe_id
	LIMIT $2`, runID, clientEntryMonitorRunResultListLimit)
	if err != nil {
		return run, fmt.Errorf("load client entry monitor run results: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var result ClientEntryMonitorRunResult
		var success int64
		var latency sql.NullInt64
		if err := rows.Scan(&result.ID, &result.PolicyID, &result.PolicyName, &result.TargetID, &result.TargetName,
			&result.Host, &result.Port, &result.ProbeID, &result.ProbeName, &success,
			&latency, &result.Error, &result.ResolvedIP, &result.ReportedAt); err != nil {
			return run, fmt.Errorf("scan client entry monitor run result: %w", err)
		}
		result.Success = success == 1
		if latency.Valid {
			value := latency.Int64
			result.LatencyMS = &value
		}
		run.Results = append(run.Results, result)
	}
	if err := rows.Err(); err != nil {
		return run, fmt.Errorf("iterate client entry monitor run results: %w", err)
	}
	if err := rows.Close(); err != nil {
		return run, fmt.Errorf("close client entry monitor run results: %w", err)
	}
	visible := int64(len(run.Results))
	if visible > run.TotalResults {
		run.TotalResults = visible
	}
	run.ResultsTruncated = run.TotalResults > visible
	// The helper accepts a slice so list and single-run loading share one query.
	statsRun := []ClientEntryMonitorRun{run}
	if err := s.populateClientEntryMonitorRunResultStats(ctx, statsRun); err != nil {
		return run, err
	}
	run = statsRun[0]
	return run, nil
}
