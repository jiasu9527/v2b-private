package admin

import (
	"context"
	"database/sql"
	"errors"
)

// One statement sees either the original operation or its compact receipt in
// the same MVCC snapshot, including while an administrator clears the log.
const supportRequestStateQuery = `SELECT after_state::TEXT FROM v2_support_operation WHERE actor_id = $1 AND request_id = $2
UNION ALL
SELECT jsonb_build_object('kind',kind,'request_hash',request_hash)::TEXT FROM v2_support_request_receipt WHERE actor_id = $1 AND request_id = $2
LIMIT 1`

type SupportOperationClearRequest struct {
	ThroughID int64 `json:"through_id"`
	Confirm   bool  `json:"confirm"`
}

type SupportOperationClearResult struct {
	Deleted int64 `json:"deleted"`
}

// ClearSupportOperations only clears the administrator's confirmed snapshot of
// the employee operation history. It does not undo any operation or touch other
// business logs. IDs remain monotonic so retrying the same fence is harmless.
func (s *DBService) ClearSupportOperations(ctx context.Context, adminID int64, req SupportOperationClearRequest) (SupportOperationClearResult, error) {
	var result SupportOperationClearResult
	if adminID <= 0 {
		return result, ErrSupportForbidden
	}
	if !req.Confirm || req.ThroughID < 0 {
		return result, ErrSupportEntryInvalid
	}
	if err := s.ensureSupportSchema(ctx); err != nil {
		return result, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	// Fresh authorization inside the transaction serializes a ban or role
	// revocation against this destructive action, not merely the HTTP check.
	var authorizedID int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM v2_user WHERE id = $1 AND banned = 0 AND is_admin = 1 FOR SHARE`, adminID).Scan(&authorizedID)
	if errors.Is(err, sql.ErrNoRows) {
		return result, ErrSupportForbidden
	}
	if err != nil {
		return result, err
	}
	if req.ThroughID == 0 {
		return result, tx.Commit()
	}
	// This conflicts with inserts and retention deletes, but not readers. The
	// copied receipts and deleted rows must describe exactly the same log set.
	if _, err = tx.ExecContext(ctx, `LOCK TABLE v2_support_operation IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return result, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO v2_support_request_receipt (actor_id,request_id,kind,request_hash,created_at)
SELECT actor_id,request_id,COALESCE(after_state->>'kind',''),COALESCE(after_state->>'request_hash',''),created_at
FROM v2_support_operation WHERE id <= $1
ON CONFLICT (actor_id,request_id) DO NOTHING`, req.ThroughID); err != nil {
		return result, err
	}
	deleted, err := tx.ExecContext(ctx, `DELETE FROM v2_support_operation WHERE id <= $1`, req.ThroughID)
	if err != nil {
		return result, err
	}
	result.Deleted, err = deleted.RowsAffected()
	if err != nil {
		return SupportOperationClearResult{}, err
	}
	if err = tx.Commit(); err != nil {
		return SupportOperationClearResult{}, err
	}
	return result, nil
}
