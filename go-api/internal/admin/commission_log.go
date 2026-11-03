package admin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	platformpostgres "forest/go-api/internal/platform/postgres"
)

var (
	ErrCommissionLogValidation = errors.New("佣金记录参数无效")
	ErrCommissionLogConflict   = errors.New("佣金记录或预览已变化，请重新预览")
	ErrCommissionLogForbidden  = errors.New("没有佣金记录操作权限")
	ErrCommissionLogNotFound   = errors.New("佣金记录预览不存在")
)

const commissionLogMaxBatch = 10000
const commissionLogPreviewLifetime = 10 * time.Minute

type CommissionLogFilter struct {
	ID               *int64  `json:"id,omitempty"`
	IDs              []int64 `json:"ids,omitempty"`
	InviteUserID     *int64  `json:"invite_user_id,omitempty"`
	UserID           *int64  `json:"user_id,omitempty"`
	TradeNo          string  `json:"trade_no,omitempty"`
	CreatedFrom      *int64  `json:"created_from,omitempty"`
	CreatedTo        *int64  `json:"created_to,omitempty"`
	CommissionStatus *int64  `json:"commission_status,omitempty"`
	Visibility       string  `json:"visibility,omitempty"`
}
type CommissionLogListRequest struct {
	Filter   CommissionLogFilter `json:"filter"`
	Current  int64               `json:"current"`
	PageSize int64               `json:"page_size"`
}
type CommissionLogRecord struct {
	ID                int64  `json:"id"`
	InviteUserID      int64  `json:"invite_user_id"`
	UserID            int64  `json:"user_id"`
	TradeNo           string `json:"trade_no"`
	OrderAmount       int64  `json:"order_amount"`
	GetAmount         int64  `json:"get_amount"`
	CreatedAt         int64  `json:"created_at"`
	UpdatedAt         int64  `json:"updated_at"`
	CommissionStatus  *int64 `json:"commission_status"`
	DeletedAt         *int64 `json:"deleted_at"`
	DeletedBy         *int64 `json:"deleted_by"`
	DeleteReason      string `json:"delete_reason"`
	VisibilityVersion int64  `json:"visibility_version"`
}
type CommissionLogListResult struct {
	Data           []CommissionLogRecord `json:"data"`
	Total          int64                 `json:"total"`
	TotalAmount    int64                 `json:"total_amount"`
	Current        int64                 `json:"current"`
	PageSize       int64                 `json:"page_size"`
	AppliedFilters CommissionLogFilter   `json:"applied_filters"`
}
type CommissionLogPreviewRequest struct {
	Filter     CommissionLogFilter `json:"filter"`
	Action     string              `json:"action"`
	Reason     string              `json:"reason"`
	ConfirmAll bool                `json:"confirm_all"`
}
type CommissionLogPreviewResult struct {
	PreviewHash    string                `json:"preview_hash"`
	Count          int64                 `json:"count"`
	TotalAmount    int64                 `json:"total_amount"`
	Action         string                `json:"action"`
	ExpiresAt      int64                 `json:"expires_at"`
	AllRecords     bool                  `json:"all_records"`
	AppliedFilters CommissionLogFilter   `json:"applied_filters"`
	Sample         []CommissionLogRecord `json:"sample"`
	BalanceChanged bool                  `json:"balance_changed"`
}
type CommissionLogApplyRequest struct {
	PreviewHash    string `json:"preview_hash"`
	IdempotencyKey string `json:"idempotency_key"`
	Confirm        bool   `json:"confirm"`
	ConfirmAll     bool   `json:"confirm_all"`
	TargetID       *int64 `json:"id,omitempty"`
	ExpectedAction string `json:"-"`
}
type CommissionLogApplyResult struct {
	OperationID    int64  `json:"operation_id"`
	Action         string `json:"action"`
	Count          int64  `json:"count"`
	TotalAmount    int64  `json:"total_amount"`
	Committed      bool   `json:"committed"`
	BalanceChanged bool   `json:"balance_changed"`
	AppliedAt      int64  `json:"applied_at"`
	Replayed       bool   `json:"replayed"`
}
type CommissionLogAdminService interface {
	ListCommissionLogs(context.Context, CommissionLogListRequest) (CommissionLogListResult, error)
	PreviewCommissionLogs(context.Context, int64, CommissionLogPreviewRequest) (CommissionLogPreviewResult, error)
	ApplyCommissionLogs(context.Context, int64, CommissionLogApplyRequest) (CommissionLogApplyResult, error)
}

func (s *DBService) ensureCommissionLogSchema(ctx context.Context) error {
	if s == nil || s.db == nil {
		return ErrUnavailable
	}
	s.commissionLogSchemaMu.Lock()
	defer s.commissionLogSchemaMu.Unlock()
	if s.commissionLogSchemaOK {
		return nil
	}
	if err := platformpostgres.EnsureCommissionLogSchema(ctx, s.db); err != nil {
		return err
	}
	s.commissionLogSchemaOK = true
	return nil
}
func (s *DBService) EnsureCommissionLogSchema(ctx context.Context) error {
	return s.ensureCommissionLogSchema(ctx)
}

func normalizeCommissionLogFilter(f CommissionLogFilter) (CommissionLogFilter, error) {
	invalid := func() (CommissionLogFilter, error) { return f, ErrCommissionLogValidation }
	for _, id := range []*int64{f.ID, f.InviteUserID, f.UserID} {
		if id != nil && (*id < 1 || *id > math.MaxInt32) {
			return invalid()
		}
	}
	if f.ID != nil && len(f.IDs) > 0 || len(f.IDs) > commissionLogMaxBatch {
		return invalid()
	}
	if len(f.IDs) > 0 {
		ids := append([]int64(nil), f.IDs...)
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		f.IDs = make([]int64, 0, len(ids))
		for _, id := range ids {
			if id < 1 || id > math.MaxInt32 {
				return invalid()
			}
			if len(f.IDs) == 0 || f.IDs[len(f.IDs)-1] != id {
				f.IDs = append(f.IDs, id)
			}
		}
	}
	f.TradeNo = strings.TrimSpace(f.TradeNo)
	if len(f.TradeNo) > 36 || strings.ContainsAny(f.TradeNo, "\x00\r\n") {
		return invalid()
	}
	for _, ts := range []*int64{f.CreatedFrom, f.CreatedTo} {
		if ts != nil && (*ts < 0 || *ts > math.MaxInt32) {
			return invalid()
		}
	}
	if f.CreatedFrom != nil && f.CreatedTo != nil && *f.CreatedFrom >= *f.CreatedTo {
		return invalid()
	}
	if f.CommissionStatus != nil && (*f.CommissionStatus < 0 || *f.CommissionStatus > 3) {
		return invalid()
	}
	if f.Visibility == "" {
		f.Visibility = "active"
	}
	if f.Visibility != "active" && f.Visibility != "deleted" && f.Visibility != "all" {
		return invalid()
	}
	return f, nil
}
func commissionLogFilterAll(f CommissionLogFilter) bool {
	return f.ID == nil && len(f.IDs) == 0 && f.InviteUserID == nil && f.UserID == nil && f.TradeNo == "" && f.CreatedFrom == nil && f.CreatedTo == nil && f.CommissionStatus == nil
}
func commissionLogWhere(f CommissionLogFilter) (string, []any) {
	clauses := []string{}
	args := []any{}
	add := func(column, op string, value any) {
		args = append(args, value)
		clauses = append(clauses, fmt.Sprintf("%s %s $%d", column, op, len(args)))
	}
	if f.ID != nil {
		add("l.id", "=", *f.ID)
	}
	if len(f.IDs) > 0 {
		placeholders := []string{}
		for _, id := range f.IDs {
			args = append(args, id)
			placeholders = append(placeholders, fmt.Sprintf("$%d", len(args)))
		}
		clauses = append(clauses, "l.id IN ("+strings.Join(placeholders, ",")+")")
	}
	if f.InviteUserID != nil {
		add("l.invite_user_id", "=", *f.InviteUserID)
	}
	if f.UserID != nil {
		add("l.user_id", "=", *f.UserID)
	}
	if f.TradeNo != "" {
		add("l.trade_no", "=", f.TradeNo)
	}
	if f.CreatedFrom != nil {
		add("l.created_at", ">=", *f.CreatedFrom)
	}
	if f.CreatedTo != nil {
		add("l.created_at", "<", *f.CreatedTo)
	}
	if f.CommissionStatus != nil {
		add("o.commission_status", "=", *f.CommissionStatus)
	}
	if f.Visibility == "active" {
		clauses = append(clauses, "l.deleted_at IS NULL")
	}
	if f.Visibility == "deleted" {
		clauses = append(clauses, "l.deleted_at IS NOT NULL")
	}
	if len(clauses) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

const commissionLogFrom = ` FROM v2_commission_log l LEFT JOIN v2_order o ON o.trade_no=l.trade_no`
const commissionLogColumns = `l.id,l.invite_user_id,l.user_id,TRIM(l.trade_no),l.order_amount,l.get_amount,l.created_at,l.updated_at,o.commission_status,l.deleted_at,l.deleted_by,l.delete_reason,l.visibility_version`

func queryCommissionLogRows(ctx context.Context, tx *sql.Tx, where string, args []any, limit, offset int64, lock bool) ([]CommissionLogRecord, error) {
	params := append([]any(nil), args...)
	params = append(params, limit, offset)
	query := `SELECT ` + commissionLogColumns + commissionLogFrom + where + fmt.Sprintf(` ORDER BY l.id ASC LIMIT $%d OFFSET $%d`, len(params)-1, len(params))
	if lock {
		query += ` FOR UPDATE OF l`
	}
	rows, err := tx.QueryContext(ctx, query, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CommissionLogRecord{}
	for rows.Next() {
		var r CommissionLogRecord
		if err = rows.Scan(&r.ID, &r.InviteUserID, &r.UserID, &r.TradeNo, &r.OrderAmount, &r.GetAmount, &r.CreatedAt, &r.UpdatedAt, &r.CommissionStatus, &r.DeletedAt, &r.DeletedBy, &r.DeleteReason, &r.VisibilityVersion); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func commissionLogDigest(v any) string {
	raw, _ := json.Marshal(v)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func commissionLogTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
func requireCommissionLogOperator(ctx context.Context, tx *sql.Tx, actor int64) error {
	return checkCommissionLogOperator(ctx, tx, actor, true)
}

func checkCommissionLogOperator(ctx context.Context, tx *sql.Tx, actor int64, lock bool) error {
	if actor < 1 {
		return ErrCommissionLogForbidden
	}
	var id int64
	query := `SELECT id FROM v2_user WHERE id=$1 AND banned=0 AND (is_admin=1 OR is_staff=1)`
	if lock {
		query += ` FOR SHARE`
	}
	err := tx.QueryRowContext(ctx, query, actor).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrCommissionLogForbidden
	}
	return err
}

func (s *DBService) ListCommissionLogs(ctx context.Context, req CommissionLogListRequest) (CommissionLogListResult, error) {
	f, err := normalizeCommissionLogFilter(req.Filter)
	if err != nil {
		return CommissionLogListResult{}, err
	}
	if req.Current == 0 {
		req.Current = 1
	}
	if req.PageSize == 0 {
		req.PageSize = 20
	}
	if req.Current < 1 || req.Current > 10000000 || req.PageSize < 1 || req.PageSize > 100 {
		return CommissionLogListResult{}, ErrCommissionLogValidation
	}
	if err = s.ensureCommissionLogSchema(ctx); err != nil {
		return CommissionLogListResult{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return CommissionLogListResult{}, err
	}
	defer tx.Rollback()
	out := CommissionLogListResult{Current: req.Current, PageSize: req.PageSize, AppliedFilters: f}
	where, args := commissionLogWhere(f)
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(l.get_amount),0)`+commissionLogFrom+where, args...).Scan(&out.Total, &out.TotalAmount); err != nil {
		return CommissionLogListResult{}, err
	}
	out.Data, err = queryCommissionLogRows(ctx, tx, where, args, req.PageSize, (req.Current-1)*req.PageSize, false)
	if err != nil {
		return CommissionLogListResult{}, err
	}
	if err = tx.Commit(); err != nil {
		return CommissionLogListResult{}, err
	}
	return out, nil
}

func (s *DBService) PreviewCommissionLogs(ctx context.Context, actor int64, req CommissionLogPreviewRequest) (CommissionLogPreviewResult, error) {
	if req.Action != "delete" && req.Action != "restore" {
		return CommissionLogPreviewResult{}, ErrCommissionLogValidation
	}
	if req.Action == "restore" && req.Filter.Visibility == "" {
		req.Filter.Visibility = "deleted"
	}
	f, err := normalizeCommissionLogFilter(req.Filter)
	if err != nil {
		return CommissionLogPreviewResult{}, err
	}
	if (req.Action == "delete" && f.Visibility != "active") || (req.Action == "restore" && f.Visibility != "deleted") {
		return CommissionLogPreviewResult{}, fmt.Errorf("%w: 删除仅接受active，恢复仅接受deleted", ErrCommissionLogValidation)
	}
	req.Reason = strings.TrimSpace(req.Reason)
	if req.Reason == "" || utf8.RuneCountInString(req.Reason) > 500 || strings.ContainsRune(req.Reason, 0) {
		return CommissionLogPreviewResult{}, ErrCommissionLogValidation
	}
	all := commissionLogFilterAll(f)
	if all && !req.ConfirmAll {
		return CommissionLogPreviewResult{}, fmt.Errorf("%w: 无目标筛选需confirm_all=true", ErrCommissionLogValidation)
	}
	if err = s.ensureCommissionLogSchema(ctx); err != nil {
		return CommissionLogPreviewResult{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return CommissionLogPreviewResult{}, err
	}
	defer tx.Rollback()
	if err = requireCommissionLogOperator(ctx, tx, actor); err != nil {
		return CommissionLogPreviewResult{}, err
	}
	where, args := commissionLogWhere(f)
	logs, err := queryCommissionLogRows(ctx, tx, where, args, commissionLogMaxBatch+1, 0, false)
	if err != nil {
		return CommissionLogPreviewResult{}, err
	}
	if len(logs) == 0 {
		return CommissionLogPreviewResult{}, fmt.Errorf("%w: 没有符合条件的佣金记录", ErrCommissionLogValidation)
	}
	if len(logs) > commissionLogMaxBatch {
		return CommissionLogPreviewResult{}, fmt.Errorf("%w: 单批最多%d条，请缩小筛选范围", ErrCommissionLogValidation, commissionLogMaxBatch)
	}
	total := int64(0)
	for _, r := range logs {
		total += r.GetAmount
	}
	tokenBytes := make([]byte, 32)
	if _, err = rand.Read(tokenBytes); err != nil {
		return CommissionLogPreviewResult{}, err
	}
	token := hex.EncodeToString(tokenBytes)
	filterRaw, _ := json.Marshal(f)
	snapshotRaw, _ := json.Marshal(logs)
	now := time.Now().Unix()
	expires := now + int64(commissionLogPreviewLifetime/time.Second)
	if _, err = tx.ExecContext(ctx, `DELETE FROM v2_commission_log_preview WHERE expires_at<$1 AND applied_operation_id IS NULL`, now); err != nil {
		return CommissionLogPreviewResult{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO v2_commission_log_preview(token_hash,actor_id,action,filters,snapshot,snapshot_hash,reason,all_records,record_count,total_amount,created_at,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, commissionLogTokenHash(token), actor, req.Action, string(filterRaw), string(snapshotRaw), commissionLogDigest(logs), req.Reason, all, len(logs), total, now, expires); err != nil {
		return CommissionLogPreviewResult{}, err
	}
	if err = tx.Commit(); err != nil {
		return CommissionLogPreviewResult{}, err
	}
	sample := logs
	if len(sample) > 20 {
		sample = sample[:20]
	}
	return CommissionLogPreviewResult{PreviewHash: token, Count: int64(len(logs)), TotalAmount: total, Action: req.Action, ExpiresAt: expires, AllRecords: all, AppliedFilters: f, Sample: sample}, nil
}

func (s *DBService) ApplyCommissionLogs(ctx context.Context, actor int64, req CommissionLogApplyRequest) (CommissionLogApplyResult, error) {
	decoded, err := hex.DecodeString(req.PreviewHash)
	if !req.Confirm || err != nil || len(decoded) != 32 || req.PreviewHash != strings.ToLower(req.PreviewHash) || len(req.IdempotencyKey) < 1 || len(req.IdempotencyKey) > 128 || strings.TrimSpace(req.IdempotencyKey) != req.IdempotencyKey || strings.ContainsAny(req.IdempotencyKey, "\x00\r\n") || req.TargetID != nil && *req.TargetID < 1 || req.ExpectedAction != "" && req.ExpectedAction != "delete" && req.ExpectedAction != "restore" {
		return CommissionLogApplyResult{}, ErrCommissionLogValidation
	}
	if err = s.ensureCommissionLogSchema(ctx); err != nil {
		return CommissionLogApplyResult{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CommissionLogApplyResult{}, err
	}
	defer tx.Rollback()
	if err = checkCommissionLogOperator(ctx, tx, actor, false); err != nil {
		return CommissionLogApplyResult{}, err
	}
	// Serialize the same idempotency key across independent backend workers. A
	// hash collision only over-serializes unrelated keys and cannot change data.
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, fmt.Sprintf("commission-log:%d:%s", actor, req.IdempotencyKey)); err != nil {
		return CommissionLogApplyResult{}, err
	}
	requestHash := commissionLogDigest(struct {
		Request        CommissionLogApplyRequest
		ExpectedAction string
	}{req, req.ExpectedAction})
	var previousHash string
	var previousReceipt []byte
	err = tx.QueryRowContext(ctx, `SELECT request_hash,receipt FROM v2_commission_log_operation WHERE actor_id=$1 AND idempotency_key=$2`, actor, req.IdempotencyKey).Scan(&previousHash, &previousReceipt)
	if err == nil {
		if previousHash != requestHash {
			return CommissionLogApplyResult{}, ErrCommissionLogConflict
		}
		var out CommissionLogApplyResult
		if json.Unmarshal(previousReceipt, &out) != nil || !out.Committed {
			return CommissionLogApplyResult{}, ErrUnavailable
		}
		out.Replayed = true
		// Replays need no financial locks, but authorization must remain valid
		// through the receipt read just as it does through a new mutation.
		if err = requireCommissionLogOperator(ctx, tx, actor); err != nil {
			return CommissionLogApplyResult{}, err
		}
		if err = tx.Commit(); err != nil {
			return CommissionLogApplyResult{}, err
		}
		return out, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return CommissionLogApplyResult{}, err
	}
	var previewID, expires, count, total int64
	var action, reason, snapshotHash string
	var all bool
	var applied sql.NullInt64
	var snapshotRaw []byte
	err = tx.QueryRowContext(ctx, `SELECT id,action,snapshot,snapshot_hash,reason,all_records,record_count,total_amount,expires_at,applied_operation_id FROM v2_commission_log_preview WHERE token_hash=$1 AND actor_id=$2 FOR UPDATE`, commissionLogTokenHash(req.PreviewHash), actor).Scan(&previewID, &action, &snapshotRaw, &snapshotHash, &reason, &all, &count, &total, &expires, &applied)
	if errors.Is(err, sql.ErrNoRows) {
		return CommissionLogApplyResult{}, ErrCommissionLogNotFound
	}
	if err != nil {
		return CommissionLogApplyResult{}, err
	}
	if applied.Valid || expires <= time.Now().Unix() {
		return CommissionLogApplyResult{}, ErrCommissionLogConflict
	}
	if req.ExpectedAction != "" && req.ExpectedAction != action {
		return CommissionLogApplyResult{}, ErrCommissionLogConflict
	}
	if all && !req.ConfirmAll {
		return CommissionLogApplyResult{}, fmt.Errorf("%w: 全部记录操作需再次confirm_all=true", ErrCommissionLogValidation)
	}
	var frozen []CommissionLogRecord
	if json.Unmarshal(snapshotRaw, &frozen) != nil || len(frozen) < 1 || len(frozen) > commissionLogMaxBatch || int64(len(frozen)) != count || commissionLogDigest(frozen) != snapshotHash {
		return CommissionLogApplyResult{}, ErrUnavailable
	}
	if req.TargetID != nil && (len(frozen) != 1 || frozen[0].ID != *req.TargetID) {
		return CommissionLogApplyResult{}, ErrCommissionLogConflict
	}
	ids := make([]int64, 0, len(frozen))
	trades := []string{}
	seenTrades := map[string]bool{}
	for _, r := range frozen {
		ids = append(ids, r.ID)
		if !seenTrades[r.TradeNo] {
			trades = append(trades, r.TradeNo)
			seenTrades[r.TradeNo] = true
		}
	}
	sort.Strings(trades)
	// Follow the financial writers' order->log locking order. Order status is
	// part of the preview; freeze it while checking, without updating it.
	placeholders := []string{}
	args := []any{}
	for _, trade := range trades {
		args = append(args, trade)
		placeholders = append(placeholders, fmt.Sprintf("$%d", len(args)))
	}
	rows, err := tx.QueryContext(ctx, `SELECT trade_no FROM v2_order WHERE trade_no IN (`+strings.Join(placeholders, ",")+`) ORDER BY trade_no FOR SHARE`, args...)
	if err != nil {
		return CommissionLogApplyResult{}, err
	}
	for rows.Next() {
		var trade string
		if err = rows.Scan(&trade); err != nil {
			rows.Close()
			return CommissionLogApplyResult{}, err
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return CommissionLogApplyResult{}, err
	}
	// Financial writers lock order -> user -> commission log. The operator
	// may also be the inviter/buyer; taking their user lock before the order
	// would deadlock with a concurrent settlement or refund. Recheck authority
	// now and hold it until commit, after the order locks and before log locks.
	if err = requireCommissionLogOperator(ctx, tx, actor); err != nil {
		return CommissionLogApplyResult{}, err
	}
	where, args := commissionLogWhere(CommissionLogFilter{IDs: ids, Visibility: "all"})
	current, err := queryCommissionLogRows(ctx, tx, where, args, commissionLogMaxBatch+1, 0, true)
	if err != nil {
		return CommissionLogApplyResult{}, err
	}
	if commissionLogDigest(current) != snapshotHash {
		return CommissionLogApplyResult{}, ErrCommissionLogConflict
	}
	now := time.Now().Unix()
	placeholders = placeholders[:0]
	updateArgs := []any{now, actor, reason}
	for _, id := range ids {
		updateArgs = append(updateArgs, id)
		placeholders = append(placeholders, fmt.Sprintf("$%d", len(updateArgs)))
	}
	assignment := `deleted_at=$1,deleted_by=$2,delete_reason=$3,visibility_version=visibility_version+1`
	if action == "restore" {
		assignment = `deleted_at=NULL,deleted_by=NULL,delete_reason='',visibility_version=visibility_version+1`
		updateArgs = updateArgs[3:]
		placeholders = placeholders[:0]
		for i := range ids {
			placeholders = append(placeholders, fmt.Sprintf("$%d", i+1))
		}
	}
	if action != "delete" && action != "restore" {
		return CommissionLogApplyResult{}, ErrUnavailable
	}
	result, err := tx.ExecContext(ctx, `UPDATE v2_commission_log SET `+assignment+` WHERE id IN (`+strings.Join(placeholders, ",")+`)`, updateArgs...)
	if err != nil {
		return CommissionLogApplyResult{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return CommissionLogApplyResult{}, err
	}
	if changed != count {
		return CommissionLogApplyResult{}, ErrCommissionLogConflict
	}
	out := CommissionLogApplyResult{Action: action, Count: count, TotalAmount: total, Committed: true, AppliedAt: now}
	if err = tx.QueryRowContext(ctx, `INSERT INTO v2_commission_log_operation(actor_id,idempotency_key,request_hash,preview_id,action,reason,record_count,total_amount,snapshot,applied_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING id`, actor, req.IdempotencyKey, requestHash, previewID, action, reason, count, total, string(snapshotRaw), now).Scan(&out.OperationID); err != nil {
		return CommissionLogApplyResult{}, err
	}
	receiptRaw, _ := json.Marshal(out)
	if _, err = tx.ExecContext(ctx, `UPDATE v2_commission_log_operation SET receipt=$2 WHERE id=$1`, out.OperationID, string(receiptRaw)); err != nil {
		return CommissionLogApplyResult{}, err
	}
	// The operation owns the immutable audit snapshot after commit. Discard
	// the duplicate preview body; a consumed preview can never execute again.
	if _, err = tx.ExecContext(ctx, `UPDATE v2_commission_log_preview SET applied_operation_id=$2,snapshot='[]'::jsonb WHERE id=$1`, previewID, out.OperationID); err != nil {
		return CommissionLogApplyResult{}, err
	}
	if err = tx.Commit(); err != nil {
		return CommissionLogApplyResult{}, err
	}
	return out, nil
}
