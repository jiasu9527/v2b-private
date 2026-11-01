package aiops

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

const operationMaxAttempts = 3

// Only use this primitive for reviewed reads, idempotent writes or a transaction
// the database explicitly rejected. Network errors alone cannot authorize replay.
func retryOperation(ctx context.Context, retryable func(context.Context, error) bool, pause time.Duration, invoke func(context.Context) error) error {
	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := invoke(ctx)
		if err == nil || attempt >= operationMaxAttempts || !retryable(ctx, err) {
			return err
		}
		timer := time.NewTimer(time.Duration(attempt) * pause)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// Domain errors such as disabled AI, too many pending actions and stale
// revisions are not temporary I/O errors, even if a read tool maps them so.
func transientOperationIO(ctx context.Context, err error) bool {
	if errors.Is(err, ErrBusy) || errors.Is(err, ErrUnavailable) {
		return false
	}
	return retryableReadToolError(ctx, err)
}

// Permission/status checks keep the existing three-second fail-closed budget.
// Denial or cancellation is immediate; only transport/DB errors get a retry.
func retryStateRead(ctx context.Context, invoke func(context.Context) error) error {
	check, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return retryOperation(check, transientOperationIO, 100*time.Millisecond, invoke)
}

type actionApprovalLockBusy struct{}

func (*actionApprovalLockBusy) Error() string { return ErrBusy.Error() }
func (*actionApprovalLockBusy) Unwrap() error { return ErrBusy }

func retryableApprovalTransaction(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	var busy *actionApprovalLockBusy
	if errors.As(err, &busy) {
		return true
	}
	var pgerr *pgconn.PgError
	return errors.As(err, &pgerr) && (pgerr.Code == "40001" || pgerr.Code == "40P01")
}

func (s *Service) changeActionAuthorized(ctx context.Context, actor, session, id int64, approve bool, turn int64) (a Action, err error) {
	err = retryOperation(ctx, retryableApprovalTransaction, time.Second, func(ctx context.Context) error {
		// Each attempt locks and validates the same action, role and snapshot.
		// No new action or business object is created to retry an approval.
		var attemptErr error
		a, attemptErr = s.changeActionAuthorizedOnce(ctx, actor, session, id, approve, turn)
		return attemptErr
	})
	return a, err
}

func (s *Service) requireToolTurnActive(ctx context.Context, actor, session, turn int64, full bool) error {
	if full {
		return s.requireFullAccess(ctx, actor, session, turn)
	}
	return retryStateRead(ctx, func(check context.Context) error {
		var alive bool
		err := s.db.QueryRowContext(check, `SELECT EXISTS(SELECT 1 FROM v2_ai_turn t JOIN v2_ai_session s ON s.id=t.session_id JOIN v2_user u ON u.id=t.actor_id JOIN v2_ai_config c ON c.id=1 WHERE t.id=$1 AND t.session_id=$2 AND t.actor_id=$3 AND t.status='running' AND NOT t.stop_requested AND t.expires_at>$4 AND (u.is_admin=1 OR (u.is_staff=1 AND s.created_by=u.id)) AND u.banned=0 AND c.enabled)`, turn, session, actor, time.Now().Unix()).Scan(&alive)
		if err == nil && !alive {
			return ErrForbidden
		}
		return err
	})
}

func (s *Service) retryPanelActor(ctx context.Context, id int64) (actor PanelOperationActor, err error) {
	err = retryStateRead(ctx, func(check context.Context) error {
		var readErr error
		actor, readErr = panelActor(check, s.db, id, false)
		return readErr
	})
	return actor, err
}

func (s *Service) retryPanelAdminSession(ctx context.Context, session int64) error {
	return retryStateRead(ctx, func(check context.Context) error {
		return panelAdminSession(check, s.db, session, false)
	})
}

// Retrying this UPDATE repeats only the receipt, not the business operation.
// UPDATE-only and the running guard prevent resurrecting a deleted session or
// overwriting a completion persisted after a lost database acknowledgement.
func (s *Service) persistActionCompletion(a Action, status, output, problem string, exitCode *int) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	now := time.Now().Unix()
	return retryOperation(ctx, transientOperationIO, time.Second, func(ctx context.Context) error {
		_, err := s.db.ExecContext(ctx, `UPDATE v2_ai_action SET status=$2,output=$3,error=$4,completed_at=$5,updated_at=$5,exit_code=$6 WHERE id=$1 AND status='running'`, a.ID, status, output, problem, now, exitCode)
		return err
	})
}

// Both detection entrypoints use a durable, transaction-locked request key
// derived from this action ID. A lost response can safely resolve to that same
// run; this helper must not be used for ordinary HTTP or SSH mutations.
func (s *Service) startIdempotentDetection(ctx context.Context, a Action, start func(context.Context) (int64, error)) (id int64, err error) {
	err = retryOperation(ctx, transientOperationIO, time.Second, func(check context.Context) error {
		if err := s.requireSSHActionActive(check, a); err != nil {
			return err
		}
		var startErr error
		id, startErr = start(check)
		return startErr
	})
	return id, err
}
