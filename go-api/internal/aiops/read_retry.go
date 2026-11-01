package aiops

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

const readToolMaxAttempts = 3

type readToolRetryExhausted struct{ attempts int }

// safeUnavailableError keeps the public, redacted error category while
// carrying the server-side classification needed by the read retry loop.
// The wrapped error is deliberately only ErrUnavailable: callers and model
// output must not receive database/transport details.
type safeUnavailableError struct{ retryable bool }

func (e *safeUnavailableError) Error() string { return ErrUnavailable.Error() }
func (e *safeUnavailableError) Unwrap() error { return ErrUnavailable }

func (e *readToolRetryExhausted) Error() string {
	return fmt.Sprintf("只读查询已自动尝试 %d 次仍未成功；请缩小查询范围或检查服务状态，不要声称已取得结果，也不要原样连续重复查询", e.attempts)
}

// Only previously reviewed reads can repeat. A tool name which sounds like a
// query, a model's claim, and Mutating=false alone do not establish this safety.
func (s *Service) callToolWithRetry(ctx context.Context, actorID, sessionID int64, call ToolCall, guard func(context.Context) error) (ToolOutcome, error) {
	return retryReadTool(ctx, func() bool { return s.panelRecoveryReadOnlyCall(call) }, guard, func() (ToolOutcome, error) {
		return s.callTool(ctx, actorID, sessionID, call)
	}, readToolRetryDelay)
}

func retryReadTool(ctx context.Context, approvedRead func() bool, guard func(context.Context) error, invoke func() (ToolOutcome, error), delay func(context.Context, int) error) (ToolOutcome, error) {
	read := approvedRead()
	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return ToolOutcome{}, err
		}
		if guard != nil {
			if err := guard(ctx); err != nil {
				return ToolOutcome{}, err
			}
		}
		if attempt > 1 && !approvedRead() {
			// The operation directory can change between attempts. Never replay
			// a formerly read-only call after it gains business side effects.
			return ToolOutcome{}, ErrConflict
		}
		result, err := invoke()
		if err == nil || !read || result.Pending || result.ActionID > 0 || !retryableReadToolError(ctx, err) {
			return result, err
		}
		if attempt >= readToolMaxAttempts {
			return result, &readToolRetryExhausted{attempts: attempt}
		}
		if err := delay(ctx, attempt); err != nil {
			return ToolOutcome{}, err
		}
	}
}

func readToolRetryDelay(ctx context.Context, attempt int) error {
	timer := time.NewTimer(time.Duration(attempt) * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func retryableReadToolError(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, ErrForbidden) || errors.Is(err, ErrValidation) || errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) {
		return false
	}
	var panelError *PanelOperationError
	if errors.As(err, &panelError) {
		if panelError.NotExecuted {
			// A DNS metadata lookup may be surfaced as 422 by the adapter.
			// Trust its typed preflight classification, not that HTTP status.
			return panelError.Retryable && oneOf(panelError.Code, "dns_domain_metadata", "dns_record_line_unavailable", "dns_preflight_failed")
		}
		switch panelError.Status {
		case 408, 429, 500, 502, 503, 504:
			return true
		default:
			return false
		}
	}
	var safe *safeUnavailableError
	if errors.As(err, &safe) {
		return safe.retryable
	}
	if errors.Is(err, ErrUnavailable) || errors.Is(err, ErrBusy) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, driver.ErrBadConn) || errors.Is(err, sql.ErrConnDone) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) {
		return true
	}
	var pgError *pgconn.PgError
	if errors.As(err, &pgError) {
		switch pgError.Code {
		case "08000", "08001", "08003", "08004", "08006", "08007", "08P01", "40001", "40P01", "55P03", "53300", "57P01", "57P02", "57P03":
			return true
		default:
			return false
		}
	}
	var network net.Error
	if errors.As(err, &network) && (network.Timeout() || network.Temporary()) {
		return true
	}
	return errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNABORTED) || errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ETIMEDOUT) || errors.Is(err, syscall.ENETUNREACH) || errors.Is(err, syscall.EHOSTUNREACH)
}
