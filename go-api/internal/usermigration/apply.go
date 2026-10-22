package usermigration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"forest/go-api/internal/user"
)

func (s *DBService) ensureSchema(ctx context.Context) error {
	if s == nil || s.db == nil {
		return ErrUnavailable
	}
	s.ensureMu.Lock()
	defer s.ensureMu.Unlock()
	if s.ensured {
		return nil
	}
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS v2_user_migration_receipt (
 preview_id VARCHAR(64) PRIMARY KEY,
 actor_id INTEGER NOT NULL,
 receipt_json TEXT NOT NULL,
 created_at BIGINT NOT NULL
 )`,
		`CREATE TABLE IF NOT EXISTS v2_user_migration_member (
 preview_id VARCHAR(64) NOT NULL,
 user_id INTEGER NOT NULL,
 PRIMARY KEY (preview_id, user_id)
 )`,
		`CREATE TABLE IF NOT EXISTS v2_user_migration_mail (
 id BIGSERIAL NOT NULL UNIQUE,
 request_id VARCHAR(64) PRIMARY KEY,
 preview_id VARCHAR(64) NOT NULL,
 actor_id INTEGER NOT NULL,
 content_hash VARCHAR(64) NOT NULL,
 status VARCHAR(24) NOT NULL,
 recipients INTEGER NOT NULL,
 sent INTEGER NOT NULL DEFAULT 0,
 failed INTEGER NOT NULL DEFAULT 0,
 interval_seconds BIGINT NOT NULL,
 created_at BIGINT NOT NULL,
 updated_at BIGINT NOT NULL
 )`,
		`CREATE INDEX IF NOT EXISTS v2_user_migration_mail_batch_idx ON v2_user_migration_mail (preview_id, id DESC)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS v2_user_migration_mail_active_idx ON v2_user_migration_mail (preview_id) WHERE status IN ('queued','running')`,
		`CREATE TABLE IF NOT EXISTS v2_runtime_kv (
 k VARCHAR(191) PRIMARY KEY,
 v TEXT NOT NULL,
 expire_at BIGINT NOT NULL DEFAULT 0,
 created_at BIGINT NOT NULL,
 updated_at BIGINT NOT NULL
 )`,
	} {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return ErrUnavailable
		}
	}
	s.ensured = true
	return nil
}

type rowQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readReceipt(ctx context.Context, q rowQueryer, actorID int64, id string) (Receipt, error) {
	var raw string
	err := q.QueryRowContext(ctx, `SELECT receipt_json FROM v2_user_migration_receipt WHERE preview_id=$1 AND actor_id=$2`, id, actorID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return Receipt{}, ErrReceiptNotFound
	}
	if err != nil {
		return Receipt{}, ErrTargetRead
	}
	var out Receipt
	if json.Unmarshal([]byte(raw), &out) != nil || out.PreviewID != id || out.Status != "completed" {
		return Receipt{}, ErrTargetRead
	}
	return out, nil
}

func (s *DBService) Receipt(ctx context.Context, actorID int64, previewID string) (Receipt, error) {
	if actorID <= 0 || !validPreviewID(previewID) {
		return Receipt{}, ErrInvalidParameter
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := s.ensureSchema(ctx); err != nil {
		return Receipt{}, err
	}
	result, err := readReceipt(ctx, s.db, actorID, previewID)
	if err == nil {
		s.clearResolvedPreview(previewID)
		return result, nil
	}
	if errors.Is(err, ErrReceiptNotFound) {
		s.mu.Lock()
		p := s.previews[previewID]
		state := ""
		if p != nil && p.actorID == actorID {
			state = p.state
		}
		s.mu.Unlock()
		if state == "applying" {
			return Receipt{}, ErrBusy
		}
		if state == "unknown" {
			return Receipt{}, ErrCommitUnknown
		}
	}
	return Receipt{}, err
}
func (s *DBService) clearResolvedPreview(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p := s.previews[id]; p != nil && p.state != "applying" {
		if p.expiryTimer != nil {
			p.expiryTimer.Stop()
		}
		p.users = nil
		delete(s.previews, id)
	}
}

func validPreviewID(id string) bool {
	if len(id) != 64 {
		return false
	}
	for _, r := range id {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func (s *DBService) Apply(ctx context.Context, actorID int64, req ApplyRequest) (Receipt, error) {
	if actorID <= 0 || !req.Confirm || !validPreviewID(req.PreviewID) {
		return Receipt{}, ErrInvalidParameter
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	if err := s.ensureSchema(ctx); err != nil {
		return Receipt{}, err
	}
	if r, err := readReceipt(ctx, s.db, actorID, req.PreviewID); err == nil {
		s.clearResolvedPreview(req.PreviewID)
		return r, nil
	} else if !errors.Is(err, ErrReceiptNotFound) {
		return Receipt{}, err
	}
	if err := s.enter(); err != nil {
		return Receipt{}, err
	}
	defer s.leave()
	s.mu.Lock()
	s.pruneLocked()
	p, ok := s.previews[req.PreviewID]
	if !ok || p.actorID != actorID {
		s.mu.Unlock()
		return Receipt{}, ErrPreviewExpired
	}
	if p.state == "unknown" {
		s.mu.Unlock()
		return Receipt{}, ErrCommitUnknown
	}
	if p.state != "ready" {
		s.mu.Unlock()
		return Receipt{}, ErrBusy
	}
	if !p.result.CanApply {
		s.mu.Unlock()
		return Receipt{}, ErrPreviewBlocked
	}
	p.state = "applying"
	s.mu.Unlock()
	committed := false
	uncertain := false
	defer func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if committed {
			if p.expiryTimer != nil {
				p.expiryTimer.Stop()
			}
			p.users = nil
			delete(s.previews, req.PreviewID)
		} else if uncertain {
			p.state = "unknown"
			p.users = nil
		} else if p.expired || p.result.ExpiresAt <= s.now().Unix() {
			if p.expiryTimer != nil {
				p.expiryTimer.Stop()
			}
			p.users = nil
			delete(s.previews, req.PreviewID)
		} else {
			p.state = "ready"
		}
	}()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return Receipt{}, ErrTargetWrite
	}
	defer tx.Rollback()
	// Ordinary registration does not share an advisory lock. These short locks
	// protect against registrations and plan edits as well as other imports.
	// No source network request occurs inside the target transaction.
	for _, stmt := range []string{
		`SET LOCAL lock_timeout = '5s'`,
		`SET LOCAL statement_timeout = '100s'`,
		`LOCK TABLE v2_user IN SHARE ROW EXCLUSIVE MODE`,
		`LOCK TABLE v2_plan IN SHARE MODE`,
	} {
		if _, err = tx.ExecContext(ctx, stmt); err != nil {
			return Receipt{}, ErrTargetWrite
		}
	}
	if r, e := readReceipt(ctx, tx, actorID, req.PreviewID); e == nil {
		committed = true
		return r, nil
	} else if !errors.Is(e, ErrReceiptNotFound) {
		return Receipt{}, e
	}
	target, err := readTargetState(ctx, tx)
	if err != nil {
		return Receipt{}, err
	}
	if err = verifyTarget(p, target, s.resetMethod()); err != nil {
		return Receipt{}, err
	}
	now := s.now()
	for start := 0; start < len(p.users); start += 500 {
		end := start + 500
		if end > len(p.users) {
			end = len(p.users)
		}
		if err = insertBatch(ctx, tx, req.PreviewID, p.users[start:end], p.resetMethod, now); err != nil {
			return Receipt{}, err
		}
	}
	receipt := Receipt{MailTrackingAvailable: true, PreviewID: req.PreviewID, Status: "completed", Imported: len(p.users), Counts: p.result.Counts, SourceDigest: p.result.SourceDigest, LockingBalancePolicy: p.result.LockingBalancePolicy, RegenerateTokens: p.result.RegenerateTokens, RegenerateUUIDs: p.result.RegenerateUUIDs, CreatedAt: now.Unix(), CompletedAt: s.now().Unix()}
	raw, err := json.Marshal(receipt)
	if err != nil {
		return Receipt{}, ErrTargetWrite
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO v2_user_migration_receipt (preview_id, actor_id, receipt_json, created_at) VALUES ($1,$2,$3,$4)`, req.PreviewID, actorID, string(raw), receipt.CreatedAt); err != nil {
		return Receipt{}, ErrTargetWrite
	}
	if err = tx.Commit(); err != nil {
		// Do not replay COMMIT after a transport error. This receipt is committed
		// atomically with all user rows and remains the source of truth.
		uncertain = true
		checkCtx, checkCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer checkCancel()
		if saved, readErr := readReceipt(checkCtx, s.db, actorID, req.PreviewID); readErr == nil {
			committed = true
			return saved, nil
		}
		return Receipt{}, ErrCommitUnknown
	}
	committed = true
	return receipt, nil
}

func insertBatch(ctx context.Context, tx *sql.Tx, previewID string, users []insertUser, defaultMethod int64, now time.Time) error {
	if len(users) == 0 {
		return nil
	}
	// IDs, referral/Telegram links, privileges and old sessions are not copied.
	// Only plan/group/device/speed come from the selected target plan.
	columns := `email,password,password_algo,password_salt,balance,commission_balance,t,u,d,transfer_enable,banned,is_admin,is_staff,uuid,group_id,plan_id,device_limit,speed_limit,token,expired_at,created_at,updated_at,auto_renewal`
	values := make([]string, 0, len(users))
	args := make([]any, 0, len(users)*23)
	byEmail := map[string]insertUser{}
	for _, u := range users {
		var planID, groupID, device, speed any
		if u.TargetPlan != nil {
			planID = u.TargetPlan.ID
			groupID = u.TargetPlan.GroupID
			device = u.TargetPlan.DeviceLimit
			speed = u.TargetPlan.SpeedLimit
		}
		row := []any{u.Email, u.Password, nil, nil, u.Balance, u.CommissionBalance, u.T, u.U, u.D, u.TransferEnable, u.Banned, 0, 0, u.UUID, groupID, planID, device, speed, u.Token, u.ExpiredAt, u.CreatedAt, now.Unix(), 0}
		placeholders := make([]string, len(row))
		for i := range row {
			placeholders[i] = fmt.Sprintf("$%d", len(args)+i+1)
		}
		args = append(args, row...)
		values = append(values, "("+strings.Join(placeholders, ",")+")")
		byEmail[u.Email] = u
	}
	rows, err := tx.QueryContext(ctx, `INSERT INTO v2_user (`+columns+`) VALUES `+strings.Join(values, ",")+` RETURNING id,email`, args...)
	if err != nil {
		return ErrTargetWrite
	}
	markerValues := []string{}
	markerArgs := []any{}
	memberValues := []string{}
	memberArgs := []any{}
	returned := 0
	for rows.Next() {
		var id int64
		var email string
		if rows.Scan(&id, &email) != nil {
			rows.Close()
			return ErrTargetWrite
		}
		u, ok := byEmail[email]
		if !ok || id <= 0 {
			rows.Close()
			return ErrTargetWrite
		}
		delete(byEmail, email)
		returned++
		memberValues = append(memberValues, fmt.Sprintf("($%d,$%d)", len(memberArgs)+1, len(memberArgs)+2))
		memberArgs = append(memberArgs, previewID, id)
		if u.TargetPlan == nil {
			continue
		}
		method := defaultMethod
		if u.TargetPlan.ResetTrafficMethod != nil {
			method = *u.TargetPlan.ResetTrafficMethod
		}
		expiry := int64(0)
		if u.ExpiredAt != nil {
			expiry = *u.ExpiredAt
		}
		marker, ok := user.TrafficResetMigrationMarker(u.TargetPlan.ID, method, expiry, now)
		if !ok {
			continue
		}
		vals := []any{fmt.Sprintf("TRAFFIC_RESET_CYCLE_USER_%d", id), marker, now.Add(400 * 24 * time.Hour).Unix(), now.Unix(), now.Unix()}
		p := make([]string, len(vals))
		for i := range vals {
			p[i] = fmt.Sprintf("$%d", len(markerArgs)+i+1)
		}
		markerValues = append(markerValues, "("+strings.Join(p, ",")+")")
		markerArgs = append(markerArgs, vals...)
	}
	err = rows.Err()
	rows.Close()
	if err != nil || returned != len(users) {
		return ErrTargetWrite
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO v2_user_migration_member (preview_id,user_id) VALUES `+strings.Join(memberValues, ","), memberArgs...); err != nil {
		return ErrTargetWrite
	}
	if len(markerValues) > 0 {
		if _, err = tx.ExecContext(ctx, `INSERT INTO v2_runtime_kv (k,v,expire_at,created_at,updated_at) VALUES `+strings.Join(markerValues, ",")+` ON CONFLICT (k) DO UPDATE SET v=EXCLUDED.v,expire_at=EXCLUDED.expire_at,updated_at=EXCLUDED.updated_at`, markerArgs...); err != nil {
			return ErrTargetWrite
		}
	}
	return nil
}
