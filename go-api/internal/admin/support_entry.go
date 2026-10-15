package admin

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"forest/go-api/internal/cliententry"
	usersvc "forest/go-api/internal/user"
)

var (
	ErrSupportEntryInvalid  = errors.New("用户入口操作参数无效")
	ErrSupportEntryConflict = errors.New("用户入口状态已变化，请刷新后重试")
	ErrSupportEntryNotFound = errors.New("未找到可操作的用户")
)

type SupportEntryTarget = usersvc.SupportEntryTarget
type SupportUserResult struct {
	CanRestore bool                        `json:"can_restore"`
	Found      bool                        `json:"found"`
	User       *usersvc.SupportEntryUser   `json:"user"`
	Scopes     []usersvc.SupportEntryScope `json:"scopes"`
	UA         string                      `json:"ua"`
}
type SupportEntryChangeRequest struct {
	UserID         int64  `json:"user_id"`
	SourcePolicyID int64  `json:"source_policy_id"`
	TargetKey      string `json:"target_key"`
	Restore        bool   `json:"restore"`
	Reason         string `json:"reason"`
	ExpiresAt      int64  `json:"expires_at"`
	Version        int64  `json:"version"`
	RequestID      string `json:"request_id"`
	UA             string `json:"ua"`
}
type SupportOperationState struct {
	TargetKey   string `json:"target_key"`
	TargetName  string `json:"target_name"`
	EntryHost   string `json:"entry_host"`
	Active      bool   `json:"active"`
	ExpiresAt   int64  `json:"expires_at"`
	Version     int64  `json:"version"`
	SourceName  string `json:"source_name"`
	RequestHash string `json:"request_hash,omitempty"`
}
type SupportOperation struct {
	ID             int64                 `json:"id"`
	ActorID        int64                 `json:"actor_id"`
	TargetUserID   int64                 `json:"target_user_id"`
	SourcePolicyID int64                 `json:"source_policy_id"`
	Reason         string                `json:"reason"`
	BeforeState    SupportOperationState `json:"before_state"`
	AfterState     SupportOperationState `json:"after_state"`
	CreatedAt      int64                 `json:"created_at"`
}
type SupportOperationResult struct {
	Items    []SupportOperation `json:"items"`
	Total    int64              `json:"total"`
	Page     int64              `json:"page"`
	PageSize int64              `json:"page_size"`
}

var supportRequestIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,80}$`)

func (s *DBService) listSupportTargets(ctx context.Context) ([]SupportEntryTarget, error) {
	if err := s.ensureClientEntrySchema(ctx); err != nil {
		return nil, err
	}
	return usersvc.LoadSupportEntryTargets(ctx, s.db)
}
func (s *DBService) ListSupportEntryTargets(ctx context.Context) ([]SupportEntryTarget, error) {
	return s.listSupportTargets(ctx)
}

func parseSupportLookup(value string) (int64, string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 254 {
		return 0, "", ErrSupportEntryInvalid
	}
	numeric := true
	for _, r := range value {
		if r < '0' || r > '9' {
			numeric = false
			break
		}
	}
	if numeric {
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil || id <= 0 {
			return 0, "", ErrSupportEntryInvalid
		}
		return id, "", nil
	}
	address, err := mail.ParseAddress(value)
	if err != nil || address.Address != value || !strings.Contains(value, "@") {
		return 0, "", fmt.Errorf("%w: 请填写完整邮箱或用户 ID，不支持模糊查询", ErrSupportEntryInvalid)
	}
	if _, err := cliententry.NormalizeHost(value[strings.LastIndex(value, "@")+1:]); err != nil {
		return 0, "", fmt.Errorf("%w: 邮箱域名无效", ErrSupportEntryInvalid)
	}
	return 0, strings.ToLower(value), nil
}
func supportUserResult(preview usersvc.SupportEntryPreview) SupportUserResult {
	return SupportUserResult{Found: true, User: &preview.User, Scopes: preview.Scopes, UA: preview.UA, CanRestore: true}
}
func (s *DBService) LookupSupportUser(ctx context.Context, actorID int64, query, ua string) (SupportUserResult, error) {
	empty := SupportUserResult{Scopes: []usersvc.SupportEntryScope{}, UA: ua}
	_, err := s.requireSupportStaff(ctx, actorID)
	if err != nil {
		return empty, err
	}
	empty.CanRestore = true
	id, email, err := parseSupportLookup(query)
	if err != nil {
		return empty, err
	}
	if len(ua) > 1024 {
		return empty, ErrSupportEntryInvalid
	}
	if err := s.ensureClientEntrySchema(ctx); err != nil {
		return empty, err
	}
	where := "u.id = $1"
	var value any = id
	if email != "" {
		where = "LOWER(u.email) = $1"
		value = email
	}
	rows, err := s.db.QueryContext(ctx, `SELECT u.id FROM v2_user u WHERE `+where+`
 AND u.is_admin = 0 AND u.is_staff = 0
 ORDER BY u.id LIMIT 2`, value)
	if err != nil {
		return empty, err
	}
	ids := []int64{}
	for rows.Next() {
		var foundID int64
		if err := rows.Scan(&foundID); err != nil {
			rows.Close()
			return empty, err
		}
		ids = append(ids, foundID)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return empty, err
	}
	if len(ids) == 0 {
		return empty, nil
	}
	if len(ids) > 1 {
		return empty, fmt.Errorf("%w: 邮箱匹配不唯一，请使用用户 ID", ErrSupportEntryInvalid)
	}
	preview, err := usersvc.PreviewSupportEntries(ctx, s.db, ids[0], ua, time.Now().Unix())
	if err != nil {
		return empty, err
	}
	return supportUserResult(preview), nil
}

func normalizeSupportChange(req SupportEntryChangeRequest, now int64) (SupportEntryChangeRequest, error) {
	req.TargetKey = strings.TrimSpace(req.TargetKey)
	req.Reason = strings.TrimSpace(req.Reason)
	req.RequestID = strings.TrimSpace(req.RequestID)
	if req.UserID <= 0 || req.SourcePolicyID <= 0 || req.Version < 0 || !supportRequestIDPattern.MatchString(req.RequestID) || req.Reason == "" || utf8.RuneCountInString(req.Reason) > 500 || len(req.UA) > 1024 || len(req.TargetKey) > 255 || req.ExpiresAt < 0 {
		return req, ErrSupportEntryInvalid
	}
	if req.Restore {
		if req.TargetKey != "" || req.ExpiresAt != 0 {
			return req, fmt.Errorf("%w: 恢复自动分配不接受目标或有效期", ErrSupportEntryInvalid)
		}
	} else if req.TargetKey == "" {
		return req, ErrSupportEntryInvalid
	}
	// Past expiry is checked after idempotency lookup: a delayed retry of an
	// already successful temporary override must not execute again or fail early.
	_ = now
	return req, nil
}
func supportChangeHash(req SupportEntryChangeRequest) string {
	req.RequestID = ""
	raw, _ := json.Marshal(req)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// Lock the existing employee row while mutating an entry. User role changes
// update this same row, serializing revocation with an in-flight operation.
func lockSupportActor(ctx context.Context, tx *sql.Tx, actorID int64) (SupportStaffRecord, error) {
	var staff SupportStaffRecord
	err := tx.QueryRowContext(ctx, `SELECT id,email FROM v2_user WHERE id = $1 AND banned = 0 AND is_admin = 0 AND is_staff = 1 FOR SHARE`, actorID).Scan(&staff.UserID, &staff.Email)
	if errors.Is(err, sql.ErrNoRows) {
		return staff, ErrSupportForbidden
	}
	if err != nil {
		return staff, err
	}
	return staff, nil
}
func lockSupportTargetUser(ctx context.Context, tx *sql.Tx, userID int64) error {
	var id, isAdmin, isStaff int64
	err := tx.QueryRowContext(ctx, `SELECT id,is_admin,is_staff FROM v2_user WHERE id = $1 AND is_admin = 0 AND is_staff = 0 FOR UPDATE`, userID).Scan(&id, &isAdmin, &isStaff)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrSupportEntryNotFound
	}
	if err != nil {
		return err
	}
	if isAdmin != 0 || isStaff != 0 {
		return ErrSupportForbidden
	}
	return nil
}
func supportScopeState(scope usersvc.SupportEntryScope) SupportOperationState {
	state := SupportOperationState{Version: scope.Version, SourceName: scope.Name}
	if scope.Override != nil {
		record := scope.Override
		state.TargetKey = record.TargetKey
		state.TargetName = record.TargetName
		state.EntryHost = record.EntryHost
		state.Active = record.Active
		state.ExpiresAt = record.ExpiresAt
	}
	return state
}
func (s *DBService) ChangeSupportEntry(ctx context.Context, actorID int64, req SupportEntryChangeRequest) (SupportUserResult, error) {
	empty := SupportUserResult{Scopes: []usersvc.SupportEntryScope{}}
	if actorID <= 0 {
		return empty, ErrSupportForbidden
	}
	if _, err := s.requireSupportStaff(ctx, actorID); err != nil {
		return empty, err
	}
	now := time.Now().Unix()
	req, err := normalizeSupportChange(req, now)
	if err != nil {
		return empty, err
	}
	if err = s.ensureClientEntrySchema(ctx); err != nil {
		return empty, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	_, err = lockSupportActor(ctx, tx, actorID)
	if err != nil {
		return empty, err
	}
	if req.UserID == actorID {
		return empty, ErrSupportForbidden
	}
	// Serialize one actor/request key even when concurrent requests name different
	// targets. The unique log constraint is the second line of defense.
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,2026101502))`, fmt.Sprintf("%d:%s", actorID, req.RequestID)); err != nil {
		return empty, err
	}
	if err = lockSupportTargetUser(ctx, tx, req.UserID); err != nil {
		return empty, err
	}
	fingerprint := supportChangeHash(req)
	var saved string
	err = tx.QueryRowContext(ctx, `SELECT after_state::TEXT FROM v2_support_operation WHERE actor_id = $1 AND request_id = $2`, actorID, req.RequestID).Scan(&saved)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return empty, err
	}
	if err == nil {
		var state SupportOperationState
		if json.Unmarshal([]byte(saved), &state) != nil || state.RequestHash != fingerprint {
			return empty, fmt.Errorf("%w: request_id 已被其他请求使用", ErrSupportEntryConflict)
		}
		preview, err := usersvc.PreviewSupportEntries(ctx, tx, req.UserID, req.UA, now)
		if err != nil {
			return empty, err
		}
		if err = tx.Commit(); err != nil {
			return empty, err
		}
		return supportUserResult(preview), nil
	}
	if req.ExpiresAt > 0 && req.ExpiresAt <= now {
		return empty, fmt.Errorf("%w: 临时指定有效期必须晚于当前时间", ErrSupportEntryInvalid)
	}
	preview, err := usersvc.PreviewSupportEntries(ctx, tx, req.UserID, req.UA, now)
	if err != nil {
		return empty, err
	}
	index := -1
	for i := range preview.Scopes {
		if preview.Scopes[i].SourcePolicyID == req.SourcePolicyID {
			index = i
			break
		}
	}
	if index < 0 {
		return empty, fmt.Errorf("%w: 用户在当前客户端条件下未命中该规则", ErrSupportEntryInvalid)
	}
	scope := &preview.Scopes[index]
	if scope.Version != req.Version {
		return empty, ErrSupportEntryConflict
	}
	before := supportScopeState(*scope)
	after := SupportOperationState{SourceName: scope.Name, Version: scope.Version + 1, RequestHash: fingerprint}
	var target SupportEntryTarget
	if req.Restore {
		if scope.Override == nil || !scope.Override.Active {
			return empty, fmt.Errorf("%w: 当前已经按规则自动分配", ErrSupportEntryInvalid)
		}
	} else {
		if !scope.Applicable {
			return empty, fmt.Errorf("%w: 当前条件下该规则未生效，只能恢复自动分配", ErrSupportEntryInvalid)
		}
		for _, candidate := range scope.Targets {
			if candidate.Key == req.TargetKey {
				target = candidate
				break
			}
		}
		if target.Key == "" {
			return empty, fmt.Errorf("%w: 目标入口已停用或节点范围不兼容", ErrSupportEntryInvalid)
		}
		if scope.Override != nil && scope.Override.Active && scope.Override.TargetKey == req.TargetKey && scope.Override.ExpiresAt == req.ExpiresAt {
			return empty, fmt.Errorf("%w: 已是该入口，无需重复设置", ErrSupportEntryInvalid)
		}
		after.TargetKey = target.Key
		after.TargetName = target.Name
		after.EntryHost = target.EntryHost
		after.Active = true
		after.ExpiresAt = req.ExpiresAt
	}
	var targetPolicy, targetGroup, expires any
	if !req.Restore {
		targetPolicy = target.PolicyID
		if target.GroupID > 0 {
			targetGroup = target.GroupID
		}
		if req.ExpiresAt > 0 {
			expires = req.ExpiresAt
		}
	}
	active := int64(0)
	if after.Active {
		active = 1
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO v2_support_entry_override (user_id,source_policy_id,target_policy_id,target_group_id,target_key,active,expires_at,version,updated_by,updated_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
ON CONFLICT (user_id,source_policy_id) DO UPDATE SET target_policy_id=EXCLUDED.target_policy_id,target_group_id=EXCLUDED.target_group_id,target_key=EXCLUDED.target_key,active=EXCLUDED.active,expires_at=EXCLUDED.expires_at,version=EXCLUDED.version,updated_by=EXCLUDED.updated_by,updated_at=EXCLUDED.updated_at`, req.UserID, req.SourcePolicyID, targetPolicy, targetGroup, after.TargetKey, active, expires, after.Version, actorID, now)
	if err != nil {
		return empty, err
	}
	beforeRaw, _ := json.Marshal(before)
	afterRaw, _ := json.Marshal(after)
	_, err = tx.ExecContext(ctx, `INSERT INTO v2_support_operation (actor_id,target_user_id,source_policy_id,request_id,reason,before_state,after_state,created_at)
VALUES ($1,$2,$3,$4,$5,$6::JSONB,$7::JSONB,$8)`, actorID, req.UserID, req.SourcePolicyID, req.RequestID, req.Reason, string(beforeRaw), string(afterRaw), now)
	if err != nil {
		return empty, err
	}
	if err = tx.Commit(); err != nil {
		return empty, err
	}
	scope.Version = after.Version
	status := "restored"
	if after.Active {
		status = "active"
	}
	scope.Override = &usersvc.SupportEntryOverride{SourcePolicyID: req.SourcePolicyID, TargetPolicyID: target.PolicyID, TargetGroupID: target.GroupID, TargetKey: after.TargetKey, TargetName: after.TargetName, EntryHost: after.EntryHost, Active: after.Active, Status: status, ExpiresAt: after.ExpiresAt, Version: after.Version}
	return supportUserResult(preview), nil
}
func (s *DBService) ListSupportOperations(ctx context.Context, actorID, page int64) (SupportOperationResult, error) {
	result := SupportOperationResult{Items: []SupportOperation{}, Page: page, PageSize: 20}
	if actorID < 0 {
		return result, ErrSupportForbidden
	}
	if actorID > 0 {
		if _, err := s.requireSupportStaff(ctx, actorID); err != nil {
			return result, err
		}
	} else if err := s.ensureSupportSchema(ctx); err != nil {
		return result, err
	}
	if page <= 0 {
		page = 1
	}
	if page > 100000 {
		return result, ErrSupportEntryInvalid
	}
	result.Page = page
	where := ""
	args := []any{}
	if actorID > 0 {
		where = " WHERE actor_id = $1"
		args = append(args, actorID)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_support_operation`+where, args...).Scan(&result.Total); err != nil {
		return result, err
	}
	args = append(args, (page-1)*20)
	rows, err := s.db.QueryContext(ctx, `SELECT id,actor_id,target_user_id,source_policy_id,reason,before_state::TEXT,after_state::TEXT,created_at FROM v2_support_operation`+where+fmt.Sprintf(" ORDER BY id DESC LIMIT 20 OFFSET $%d", len(args)), args...)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var item SupportOperation
		var before, after string
		if err := rows.Scan(&item.ID, &item.ActorID, &item.TargetUserID, &item.SourcePolicyID, &item.Reason, &before, &after, &item.CreatedAt); err != nil {
			return result, err
		}
		if err := json.Unmarshal([]byte(before), &item.BeforeState); err != nil {
			return result, err
		}
		if err := json.Unmarshal([]byte(after), &item.AfterState); err != nil {
			return result, err
		}
		item.BeforeState.RequestHash = ""
		item.AfterState.RequestHash = ""
		result.Items = append(result.Items, item)
	}
	return result, rows.Err()
}
