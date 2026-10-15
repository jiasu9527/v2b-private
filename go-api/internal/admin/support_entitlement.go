package admin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	usersvc "forest/go-api/internal/user"
)

const supportMaxTrafficBytes = int64(9007199254740991)
const supportMaxExpiryTimestamp = int64(253402300799)

// SupportEntitlementSnapshot deliberately contains no balance, credentials or
// role flags. Version tracks configuration, not live usage counters: an online
// customer's traffic must not prevent a staff member from repairing an expiry.
type SupportEntitlementSnapshot struct {
	UserID         int64  `json:"user_id"`
	PlanID         *int64 `json:"plan_id"`
	PlanName       string `json:"plan_name"`
	GroupID        *int64 `json:"group_id"`
	TransferEnable int64  `json:"transfer_enable"`
	U              int64  `json:"u"`
	D              int64  `json:"d"`
	ExpiredAt      *int64 `json:"expired_at"`
	DeviceLimit    *int64 `json:"device_limit"`
	SpeedLimit     *int64 `json:"speed_limit"`
	Version        string `json:"version"`
}

type SupportEntitlementPlan struct {
	ID             int64  `json:"id"`
	Name           string `json:"name"`
	TransferEnable int64  `json:"transfer_enable"`
}

type SupportEntitlementChangeRequest struct {
	UserID         int64           `json:"user_id"`
	PlanID         *int64          `json:"plan_id,omitempty"`
	TransferEnable *int64          `json:"transfer_enable,omitempty"`
	ExpiredAt      json.RawMessage `json:"expired_at,omitempty"`
	ResetUsage     bool            `json:"reset_usage"`
	Reason         string          `json:"reason"`
	RequestID      string          `json:"request_id"`
	Expected       string          `json:"expected"`
}

type supportEntitlementOperationState struct {
	Kind string `json:"kind"`
	SupportEntitlementSnapshot
	RequestHash string `json:"request_hash,omitempty"`
}

func supportEntitlementVersion(state SupportEntitlementSnapshot) string {
	// Display names and upload/download counters may change independently of
	// entitlement configuration. Only fields editable by this operation conflict.
	state.PlanName, state.Version = "", ""
	state.U, state.D = 0, 0
	raw, _ := json.Marshal(state)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func LoadSupportEntitlementSnapshot(ctx context.Context, q usersvc.SupportEntryQuerier, userID int64) (SupportEntitlementSnapshot, error) {
	var result SupportEntitlementSnapshot
	var planID, groupID, expiredAt, deviceLimit, speedLimit sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT u.id,u.plan_id,COALESCE(p.name,''),u.group_id,u.transfer_enable,u.u,u.d,u.expired_at,u.device_limit,u.speed_limit
FROM v2_user u LEFT JOIN v2_plan p ON p.id = u.plan_id
WHERE u.id = $1 AND u.is_admin = 0 AND u.is_staff = 0`, userID).Scan(&result.UserID, &planID, &result.PlanName, &groupID, &result.TransferEnable, &result.U, &result.D, &expiredAt, &deviceLimit, &speedLimit)
	if errors.Is(err, sql.ErrNoRows) {
		return result, ErrSupportEntryNotFound
	}
	if err != nil {
		return result, err
	}
	result.PlanID, result.GroupID = nullInt64Ptr(planID), nullInt64Ptr(groupID)
	// Subscription/user APIs treat legacy zero or negative expiry as permanent.
	// Preserve that meaning here rather than rendering an already-expired date.
	if expiredAt.Valid && expiredAt.Int64 <= 0 {
		expiredAt = sql.NullInt64{}
	}
	result.ExpiredAt, result.DeviceLimit, result.SpeedLimit = nullInt64Ptr(expiredAt), nullInt64Ptr(deviceLimit), nullInt64Ptr(speedLimit)
	result.Version = supportEntitlementVersion(result)
	return result, nil
}

// Hidden plans remain selectable for repairing legacy customers; this returns
// only entitlement information, never plan prices or financial operations.
func (s *DBService) GetSupportEntitlementOptions(ctx context.Context, actorID int64) ([]SupportEntitlementPlan, error) {
	result := []SupportEntitlementPlan{}
	if _, err := s.requireSupportStaff(ctx, actorID); err != nil {
		return result, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,transfer_enable FROM v2_plan ORDER BY sort ASC NULLS LAST,id`)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var plan SupportEntitlementPlan
		if err := rows.Scan(&plan.ID, &plan.Name, &plan.TransferEnable); err != nil {
			return result, err
		}
		plan.TransferEnable = planTransferEnableBytes(plan.TransferEnable)
		if plan.ID > 0 && plan.TransferEnable >= 0 && plan.TransferEnable <= supportMaxTrafficBytes {
			result = append(result, plan)
		}
	}
	return result, rows.Err()
}

func normalizeSupportEntitlementChange(req SupportEntitlementChangeRequest) (SupportEntitlementChangeRequest, error) {
	req.Reason, req.RequestID = strings.TrimSpace(req.Reason), strings.TrimSpace(req.RequestID)
	if req.UserID <= 0 || req.Reason == "" || utf8.RuneCountInString(req.Reason) > 500 || !supportRequestIDPattern.MatchString(req.RequestID) || len(req.Expected) != sha256.Size*2 || strings.Trim(req.Expected, "0123456789abcdef") != "" {
		return req, ErrSupportEntryInvalid
	}
	if req.PlanID != nil && *req.PlanID <= 0 {
		return req, fmt.Errorf("%w: 请选择有效套餐", ErrSupportEntryInvalid)
	}
	if req.TransferEnable != nil && (*req.TransferEnable < 0 || *req.TransferEnable > supportMaxTrafficBytes) {
		return req, fmt.Errorf("%w: 总流量配额无效", ErrSupportEntryInvalid)
	}
	if len(req.ExpiredAt) > 0 {
		var expiry *int64
		if err := json.Unmarshal(req.ExpiredAt, &expiry); err != nil || (expiry != nil && (*expiry <= 0 || *expiry > supportMaxExpiryTimestamp)) {
			return req, fmt.Errorf("%w: 到期时间应为时间戳或 null（永久）", ErrSupportEntryInvalid)
		}
		// Canonicalize whitespace so retries bind to the value, not JSON layout.
		req.ExpiredAt, _ = json.Marshal(expiry)
	}
	if req.PlanID == nil && req.TransferEnable == nil && len(req.ExpiredAt) == 0 && !req.ResetUsage {
		return req, fmt.Errorf("%w: 未选择要修改的权益", ErrSupportEntryInvalid)
	}
	return req, nil
}

func supportEntitlementChangeHash(req SupportEntitlementChangeRequest) string {
	req.RequestID = ""
	raw, _ := json.Marshal(struct {
		Kind string `json:"kind"`
		SupportEntitlementChangeRequest
	}{Kind: "entitlement", SupportEntitlementChangeRequest: req})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (s *DBService) ChangeSupportEntitlement(ctx context.Context, actorID int64, req SupportEntitlementChangeRequest) (SupportEntitlementSnapshot, error) {
	var empty SupportEntitlementSnapshot
	if actorID <= 0 {
		return empty, ErrSupportForbidden
	}
	if _, err := s.requireSupportStaff(ctx, actorID); err != nil {
		return empty, err
	}
	req, err := normalizeSupportEntitlementChange(req)
	if err != nil {
		return empty, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	if _, err = lockSupportActor(ctx, tx, actorID); err != nil {
		return empty, err
	}
	if req.UserID == actorID {
		return empty, ErrSupportForbidden
	}
	// Share the lock/key namespace with entry operations: reusing a request ID
	// across the two operations cannot create duplicate or mismatched effects.
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,2026101502))`, fmt.Sprintf("%d:%s", actorID, req.RequestID)); err != nil {
		return empty, err
	}
	if err = lockSupportTargetUser(ctx, tx, req.UserID); err != nil {
		return empty, err
	}
	fingerprint := supportEntitlementChangeHash(req)
	var saved string
	err = tx.QueryRowContext(ctx, supportRequestStateQuery, actorID, req.RequestID).Scan(&saved)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return empty, err
	}
	if err == nil {
		var state supportEntitlementOperationState
		if json.Unmarshal([]byte(saved), &state) != nil || state.Kind != "entitlement" || state.RequestHash != fingerprint {
			return empty, fmt.Errorf("%w: request_id 已被其他请求使用", ErrSupportEntryConflict)
		}
		current, err := LoadSupportEntitlementSnapshot(ctx, tx, req.UserID)
		if err != nil {
			return empty, err
		}
		if err := tx.Commit(); err != nil {
			return empty, err
		}
		return current, nil
	}
	before, err := LoadSupportEntitlementSnapshot(ctx, tx, req.UserID)
	if err != nil {
		return empty, err
	}
	if req.Expected != before.Version {
		return empty, ErrSupportEntryConflict
	}
	after := before
	setParts := []string{}
	args := []any{req.UserID}
	set := func(column string, value any) {
		args = append(args, value)
		setParts = append(setParts, fmt.Sprintf("%s = $%d", column, len(args)))
	}
	if req.PlanID != nil && (before.PlanID == nil || *req.PlanID != *before.PlanID) {
		var plan SupportEntitlementPlan
		var groupID, deviceLimit, speedLimit sql.NullInt64
		err = tx.QueryRowContext(ctx, `SELECT id,name,group_id,transfer_enable,device_limit,speed_limit FROM v2_plan WHERE id = $1 FOR SHARE`, *req.PlanID).Scan(&plan.ID, &plan.Name, &groupID, &plan.TransferEnable, &deviceLimit, &speedLimit)
		if errors.Is(err, sql.ErrNoRows) {
			return empty, fmt.Errorf("%w: 套餐不存在", ErrSupportEntryInvalid)
		}
		if err != nil {
			return empty, err
		}
		plan.TransferEnable = planTransferEnableBytes(plan.TransferEnable)
		if plan.TransferEnable < 0 || plan.TransferEnable > supportMaxTrafficBytes {
			return empty, fmt.Errorf("%w: 套餐配额无效", ErrSupportEntryInvalid)
		}
		after.PlanID, after.PlanName = &plan.ID, plan.Name
		after.GroupID, after.DeviceLimit, after.SpeedLimit = nullInt64Ptr(groupID), nullInt64Ptr(deviceLimit), nullInt64Ptr(speedLimit)
		after.TransferEnable = plan.TransferEnable
		set("plan_id", plan.ID)
		set("group_id", nullableInt64(after.GroupID))
		set("device_limit", nullableInt64(after.DeviceLimit))
		set("speed_limit", nullableInt64(after.SpeedLimit))
	}
	if req.TransferEnable != nil {
		after.TransferEnable = *req.TransferEnable
	}
	if after.TransferEnable != before.TransferEnable {
		set("transfer_enable", after.TransferEnable)
	}
	if len(req.ExpiredAt) > 0 {
		// Decode into a fresh pointer: after is a shallow snapshot copy, so
		// unmarshalling into its existing pointer would also mutate before.
		var expiry *int64
		_ = json.Unmarshal(req.ExpiredAt, &expiry) // validated above
		after.ExpiredAt = expiry
		beforeRaw, _ := json.Marshal(before.ExpiredAt)
		afterRaw, _ := json.Marshal(after.ExpiredAt)
		if !bytes.Equal(beforeRaw, afterRaw) {
			set("expired_at", nullableInt64(after.ExpiredAt))
		}
	}
	if req.ResetUsage && (before.U != 0 || before.D != 0) {
		after.U, after.D = 0, 0
		set("u", int64(0))
		set("d", int64(0))
	}
	if len(setParts) == 0 {
		return empty, fmt.Errorf("%w: 权益未变化，无需重复保存", ErrSupportEntryInvalid)
	}
	now := time.Now().Unix()
	set("updated_at", now)
	// Never rewrite usage unless explicitly resetting it. The user row lock also
	// serializes traffic-accounting increments with an intentional reset.
	result, err := tx.ExecContext(ctx, `UPDATE v2_user SET `+strings.Join(setParts, ", ")+` WHERE id = $1`, args...)
	if err != nil {
		return empty, err
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		if err != nil {
			return empty, err
		}
		return empty, ErrSupportEntryNotFound
	}
	after.Version = supportEntitlementVersion(after)
	beforeRaw, _ := json.Marshal(supportEntitlementOperationState{Kind: "entitlement", SupportEntitlementSnapshot: before})
	afterRaw, _ := json.Marshal(supportEntitlementOperationState{Kind: "entitlement", SupportEntitlementSnapshot: after, RequestHash: fingerprint})
	if _, err = tx.ExecContext(ctx, `INSERT INTO v2_support_operation (actor_id,target_user_id,source_policy_id,request_id,reason,before_state,after_state,created_at)
VALUES ($1,$2,$3,$4,$5,$6::JSONB,$7::JSONB,$8)`, actorID, req.UserID, int64(0), req.RequestID, req.Reason, string(beforeRaw), string(afterRaw), now); err != nil {
		return empty, err
	}
	if err := tx.Commit(); err != nil {
		return empty, err
	}
	return after, nil
}
