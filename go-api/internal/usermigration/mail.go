package usermigration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"forest/go-api/internal/queue"
)

// MailDispatcher prepares the existing administrator bulk-mail implementation.
// It must not send or enqueue anything before the returned job is invoked.
type MailDispatcher interface {
	MigrationMailSettings() (intervalSeconds int64, configured bool, err error)
	PrepareMigrationMail(context.Context, []string, string, string, func(context.Context, bool) error) (int64, queue.JobFunc, error)
}

type MailRequest struct {
	PreviewID string `json:"preview_id"`
	RequestID string `json:"request_id"`
	Subject   string `json:"subject"`
	Content   string `json:"content"`
	Confirm   bool   `json:"confirm"`
}

type MailTask struct {
	RequestID       string `json:"request_id"`
	PreviewID       string `json:"preview_id"`
	Status          string `json:"status"`
	Recipients      int    `json:"recipients"`
	Sent            int    `json:"sent"`
	Failed          int    `json:"failed"`
	IntervalSeconds int64  `json:"interval_seconds"`
	CreatedAt       int64  `json:"created_at"`
	UpdatedAt       int64  `json:"updated_at"`
}

type MailInfo struct {
	PreviewID         string    `json:"preview_id"`
	Imported          int       `json:"imported"`
	Recipients        int       `json:"recipients"`
	TrackingAvailable bool      `json:"tracking_available"`
	Configured        bool      `json:"configured"`
	QueueAvailable    bool      `json:"queue_available"`
	IntervalSeconds   int64     `json:"interval_seconds"`
	Task              *MailTask `json:"task"`
}

func (s *DBService) WithMailDispatcher(dispatcher MailDispatcher, jobs queue.Enqueuer) *DBService {
	s.mailDispatcher, s.jobs = dispatcher, jobs
	return s
}

const mailTaskColumns = `request_id,preview_id,status,recipients,sent,failed,interval_seconds,created_at,updated_at`

// Use only exact imported IDs; current email changes are respected. Privileged
// users and deleted users are excluded, and shared current emails are deduped.
const mailRecipientsQuery = `SELECT DISTINCT lower(btrim(u.email)) AS email FROM v2_user_migration_member m JOIN v2_user u ON u.id=m.user_id WHERE m.preview_id=$1 AND COALESCE(u.is_admin,0)=0 AND COALESCE(u.is_staff,0)=0 AND btrim(u.email)<>''`

type mailRowScanner interface{ Scan(...any) error }

func scanMailTask(row mailRowScanner) (MailTask, error) {
	var task MailTask
	err := row.Scan(&task.RequestID, &task.PreviewID, &task.Status, &task.Recipients, &task.Sent, &task.Failed, &task.IntervalSeconds, &task.CreatedAt, &task.UpdatedAt)
	return task, err
}

func (s *DBService) mailQueueAvailable() bool { return s.jobs != nil && s.jobs.Snapshot().Running }

func (s *DBService) MailInfo(ctx context.Context, actorID int64, previewID string) (MailInfo, error) {
	if actorID <= 0 || !validPreviewID(previewID) {
		return MailInfo{}, ErrInvalidParameter
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := s.ensureSchema(ctx); err != nil {
		return MailInfo{}, err
	}
	receipt, err := readReceipt(ctx, s.db, actorID, previewID)
	if err != nil {
		return MailInfo{}, err
	}
	out := MailInfo{PreviewID: previewID, Imported: receipt.Imported, TrackingAvailable: receipt.MailTrackingAvailable, QueueAvailable: s.mailQueueAvailable()}
	if receipt.MailTrackingAvailable {
		if err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM (`+mailRecipientsQuery+`) recipients`, previewID).Scan(&out.Recipients); err != nil {
			return MailInfo{}, ErrTargetRead
		}
	}
	if s.mailDispatcher != nil {
		out.IntervalSeconds, out.Configured, err = s.mailDispatcher.MigrationMailSettings()
		if err != nil {
			return MailInfo{}, ErrMailUnavailable
		}
	}
	task, err := scanMailTask(s.db.QueryRowContext(ctx, `SELECT `+mailTaskColumns+` FROM v2_user_migration_mail WHERE preview_id=$1 AND actor_id=$2 ORDER BY id DESC LIMIT 1`, previewID, actorID))
	if err == nil {
		out.Task = &task
	} else if !errors.Is(err, sql.ErrNoRows) {
		return MailInfo{}, ErrTargetRead
	}
	return out, nil
}

func normalizeMailRequest(actorID int64, req MailRequest) (MailRequest, string, error) {
	if strings.ContainsAny(req.Subject, "\r\n\x00") {
		return req, "", ErrInvalidParameter
	}
	req.Subject, req.Content = strings.TrimSpace(req.Subject), strings.TrimSpace(req.Content)
	if actorID <= 0 || !req.Confirm || !validPreviewID(req.PreviewID) || !validPreviewID(req.RequestID) || req.Subject == "" || req.Content == "" || !utf8.ValidString(req.Subject) || !utf8.ValidString(req.Content) || utf8.RuneCountInString(req.Subject) > 200 || len(req.Content) > 100000 || strings.ContainsAny(req.Subject, "\r\n\x00") {
		return req, "", ErrInvalidParameter
	}
	raw, _ := json.Marshal([]string{req.PreviewID, req.Subject, req.Content})
	digest := sha256.Sum256(raw)
	return req, hex.EncodeToString(digest[:]), nil
}

func (s *DBService) SendMail(ctx context.Context, actorID int64, req MailRequest) (MailTask, error) {
	req, hash, err := normalizeMailRequest(actorID, req)
	if err != nil {
		return MailTask{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err = s.ensureSchema(ctx); err != nil {
		return MailTask{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return MailTask{}, ErrTargetWrite
	}
	defer tx.Rollback()
	// The receipt row serializes all submissions to this batch, including from
	// separate API processes. The partial unique index is a second invariant.
	var raw string
	err = tx.QueryRowContext(ctx, `SELECT receipt_json FROM v2_user_migration_receipt WHERE preview_id=$1 AND actor_id=$2 FOR UPDATE`, req.PreviewID, actorID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return MailTask{}, ErrReceiptNotFound
	}
	if err != nil {
		return MailTask{}, ErrTargetRead
	}
	var receipt Receipt
	if json.Unmarshal([]byte(raw), &receipt) != nil || receipt.PreviewID != req.PreviewID || receipt.Status != "completed" {
		return MailTask{}, ErrTargetRead
	}
	if !receipt.MailTrackingAvailable {
		return MailTask{}, ErrMailTrackingUnavailable
	}
	var previousHash string
	task := MailTask{}
	err = tx.QueryRowContext(ctx, `SELECT `+mailTaskColumns+`,content_hash FROM v2_user_migration_mail WHERE request_id=$1 AND preview_id=$2 AND actor_id=$3`, req.RequestID, req.PreviewID, actorID).Scan(&task.RequestID, &task.PreviewID, &task.Status, &task.Recipients, &task.Sent, &task.Failed, &task.IntervalSeconds, &task.CreatedAt, &task.UpdatedAt, &previousHash)
	if err == nil {
		if previousHash != hash {
			return MailTask{}, ErrMailConflict
		}
		return task, nil // Never requeue even a queue_failed or interrupted request.
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return MailTask{}, ErrTargetRead
	}
	var active bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM v2_user_migration_mail WHERE preview_id=$1 AND status IN ('queued','running'))`, req.PreviewID).Scan(&active); err != nil {
		return MailTask{}, ErrTargetRead
	}
	if active {
		return MailTask{}, ErrMailBusy
	}
	if !s.mailQueueAvailable() {
		return MailTask{}, ErrMailQueueUnavailable
	}
	if s.mailDispatcher == nil {
		return MailTask{}, ErrMailUnavailable
	}
	rows, err := tx.QueryContext(ctx, mailRecipientsQuery+` ORDER BY email`, req.PreviewID)
	if err != nil {
		return MailTask{}, ErrTargetRead
	}
	emails := []string{}
	for rows.Next() {
		var email string
		if err = rows.Scan(&email); err != nil {
			rows.Close()
			return MailTask{}, ErrTargetRead
		}
		emails = append(emails, email)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return MailTask{}, ErrTargetRead
	}
	if len(emails) == 0 {
		return MailTask{}, ErrMailNoRecipients
	}
	interval, run, err := s.mailDispatcher.PrepareMigrationMail(ctx, emails, req.Subject, req.Content, func(_ context.Context, success bool) error { return s.recordMailResult(req.RequestID, success) })
	if err != nil || run == nil || interval < 0 {
		return MailTask{}, ErrMailUnavailable
	}
	now := s.now().Unix()
	task = MailTask{RequestID: req.RequestID, PreviewID: req.PreviewID, Status: "queued", Recipients: len(emails), IntervalSeconds: interval, CreatedAt: now, UpdatedAt: now}
	result, err := tx.ExecContext(ctx, `INSERT INTO v2_user_migration_mail (request_id,preview_id,actor_id,content_hash,status,recipients,sent,failed,interval_seconds,created_at,updated_at) VALUES ($1,$2,$3,$4,'queued',$5,0,0,$6,$7,$7) ON CONFLICT (request_id) DO NOTHING`, req.RequestID, req.PreviewID, actorID, hash, len(emails), interval, now)
	if err != nil {
		return MailTask{}, ErrTargetWrite
	}
	n, err := result.RowsAffected()
	if err != nil {
		return MailTask{}, ErrTargetWrite
	}
	if n != 1 {
		return MailTask{}, ErrMailConflict
	}
	if err = tx.Commit(); err != nil {
		return MailTask{}, ErrCommitUnknown
	}
	// This queue is intentionally process-local (same as ordinary bulk mail).
	// Crash between commit and enqueue can leave queued state. Never auto replay:
	// SMTP cannot provide exactly-once delivery after an ambiguous interruption.
	enqueue := s.jobs.Enqueue
	if nonblocking, ok := s.jobs.(interface {
		TryEnqueue(string, string, queue.JobFunc) error
	}); ok {
		enqueue = nonblocking.TryEnqueue
	}
	if err = enqueue("send_email_mass", "migration:"+req.RequestID, func(jobCtx context.Context) error { return s.runMailTask(jobCtx, req.RequestID, run) }); err != nil {
		markCtx, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		result, markErr := s.db.ExecContext(markCtx, `UPDATE v2_user_migration_mail SET status='queue_failed',updated_at=$2 WHERE request_id=$1 AND status='queued'`, req.RequestID, s.now().Unix())
		if markErr != nil {
			return MailTask{}, ErrTargetWrite
		}
		if n, markErr := result.RowsAffected(); markErr != nil || n != 1 {
			return MailTask{}, ErrTargetWrite
		}
		task.Status = "queue_failed"
		task.UpdatedAt = s.now().Unix()
	}
	return task, nil
}

func (s *DBService) recordMailResult(requestID string, success bool) error {
	// SMTP may finish after its worker context was cancelled. Account for that
	// attempted send using a bounded independent context, then stop the batch.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sent, failed := 0, 1
	if success {
		sent, failed = 1, 0
	}
	result, err := s.db.ExecContext(ctx, `UPDATE v2_user_migration_mail SET sent=sent+$2,failed=failed+$3,updated_at=$4 WHERE request_id=$1 AND status='running' AND sent+failed<recipients`, requestID, sent, failed, s.now().Unix())
	if err != nil {
		return ErrTargetWrite
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return ErrTargetWrite
	}
	return nil
}

func (s *DBService) runMailTask(ctx context.Context, requestID string, run queue.JobFunc) error {
	markCtx, done := context.WithTimeout(context.Background(), 5*time.Second)
	result, err := s.db.ExecContext(markCtx, `UPDATE v2_user_migration_mail SET status='running',updated_at=$2 WHERE request_id=$1 AND status='queued'`, requestID, s.now().Unix())
	done()
	if err != nil {
		return ErrTargetWrite
	}
	n, err := result.RowsAffected()
	if err != nil {
		return ErrTargetWrite
	}
	if n != 1 {
		return nil
	}
	runErr := run(ctx)
	finishCtx, finish := context.WithTimeout(context.Background(), 5*time.Second)
	defer finish()
	_, err = s.db.ExecContext(finishCtx, `UPDATE v2_user_migration_mail SET status=CASE WHEN sent+failed<recipients THEN 'interrupted' WHEN failed>0 THEN 'partial_failed' ELSE 'completed' END,updated_at=$2 WHERE request_id=$1 AND status='running'`, requestID, s.now().Unix())
	if err != nil {
		return ErrTargetWrite
	}
	if runErr != nil {
		return errors.New("migration mail job did not complete successfully")
	}
	return nil
}
