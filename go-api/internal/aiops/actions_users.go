package aiops

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"forest/go-api/internal/admin"
)

// Only explicit account settings are writable. Roles and credentials never
// enter this payload; usage counters are reset only by the named operation.
type userUpdatePayload struct {
	UserID                 int64                      `json:"user_id"`
	Changes                map[string]json.RawMessage `json:"changes"`
	ResetUsage             bool                       `json:"reset_usage"`
	Before                 map[string]json.RawMessage `json:"before,omitempty"`
	EffectiveChanges       map[string]json.RawMessage `json:"effective_changes,omitempty"`
	BeforeRemarksTruncated bool                       `json:"before_remarks_truncated,omitempty"`
}

const aiMaxSafeInteger = int64(9007199254740991)

var userEditableFields = map[string]bool{"plan_id": true, "group_id": true, "transfer_enable": true, "expired_at": true, "device_limit": true, "speed_limit": true, "banned": true, "balance": true, "commission_balance": true, "discount": true, "commission_rate": true, "commission_type": true, "remarks": true}

const userSettingsProjection = `jsonb_build_object('user_id',id,'email',email,'plan_id',plan_id,'group_id',group_id,'transfer_enable',transfer_enable,'expired_at',expired_at,'device_limit',device_limit,'speed_limit',speed_limit,'banned',banned,'balance',balance,'commission_balance',commission_balance,'discount',discount,'commission_rate',commission_rate,'commission_type',commission_type,'remarks',remarks)`

func normalizeUserChanges(changes map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	out := map[string]json.RawMessage{}
	for key, raw := range changes {
		if !userEditableFields[key] {
			return nil, ErrValidation
		}
		if key == "remarks" {
			var value string
			if strings.TrimSpace(string(raw)) == "null" {
				out[key] = json.RawMessage(`null`)
				continue
			}
			if json.Unmarshal(raw, &value) != nil || !utf8.ValidString(value) || len(value) > 4000 || strings.ContainsRune(value, 0) {
				return nil, ErrValidation
			}
			out[key], _ = json.Marshal(strings.TrimSpace(value))
			continue
		}
		nullable := oneOf(key, "plan_id", "group_id", "expired_at", "device_limit", "speed_limit", "discount", "commission_rate")
		if strings.TrimSpace(string(raw)) == "null" {
			if !nullable {
				return nil, ErrValidation
			}
			out[key] = json.RawMessage(`null`)
			continue
		}
		var value int64
		if json.Unmarshal(raw, &value) != nil || value < 0 {
			return nil, ErrValidation
		}
		max := int64(2147483647)
		switch key {
		case "plan_id", "group_id":
			if value == 0 {
				return nil, ErrValidation
			}
		case "transfer_enable":
			max = aiMaxSafeInteger
		case "expired_at":
			max = 253402300799
			if value == 0 {
				return nil, ErrValidation
			}
		case "banned":
			max = 1
		case "discount", "commission_rate":
			max = 100
		case "commission_type":
			max = 2
		}
		if value > max {
			return nil, ErrValidation
		}
		out[key], _ = json.Marshal(value)
	}
	return out, nil
}
func rawInt64(raw json.RawMessage) int64 { var v int64; _ = json.Unmarshal(raw, &v); return v }
func loadAIUserSettings(ctx context.Context, tx *sql.Tx, id int64) (map[string]json.RawMessage, error) {
	var raw []byte
	err := tx.QueryRowContext(ctx, `SELECT `+userSettingsProjection+` FROM v2_user WHERE id=$1 AND is_admin=0 AND is_staff=0 FOR UPDATE`, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var out map[string]json.RawMessage
	if json.Unmarshal(raw, &out) != nil {
		return nil, ErrUnavailable
	}
	return out, nil
}
func (s *Service) captureUserAction(ctx context.Context, tx *sql.Tx, spec actionSpec) (json.RawMessage, string, string, error) {
	var p userUpdatePayload
	if spec.Kind != "user_update" || json.Unmarshal(spec.Payload, &p) != nil || p.UserID <= 0 || p.UserID != spec.TargetID {
		return nil, "", "", ErrValidation
	}
	changes, err := normalizeUserChanges(p.Changes)
	if err != nil || (len(changes) == 0 && !p.ResetUsage) {
		return nil, "", "", ErrValidation
	}
	before, err := loadAIUserSettings(ctx, tx, p.UserID)
	if err != nil {
		return nil, "", "", err
	}
	effective := map[string]json.RawMessage{}
	for k, v := range changes {
		effective[k] = v
	}
	var planSnapshot json.RawMessage
	if planID := rawInt64(changes["plan_id"]); planID > 0 {
		var groupID, quota int64
		var device, speed sql.NullInt64
		var name string
		err = tx.QueryRowContext(ctx, `SELECT group_id,transfer_enable,device_limit,speed_limit,name FROM v2_plan WHERE id=$1 FOR SHARE`, planID).Scan(&groupID, &quota, &device, &speed, &name)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, "", "", ErrNotFound
		}
		if err != nil {
			return nil, "", "", err
		}
		quota = admin.AIPlanTrafficBytes(quota)
		if quota < 0 || quota > aiMaxSafeInteger {
			return nil, "", "", ErrValidation
		}
		defaultValues := map[string]any{"group_id": groupID, "transfer_enable": quota, "device_limit": nil, "speed_limit": nil}
		if device.Valid {
			defaultValues["device_limit"] = device.Int64
		}
		if speed.Valid {
			defaultValues["speed_limit"] = speed.Int64
		}
		for k, v := range defaultValues {
			if _, ok := effective[k]; !ok {
				effective[k], _ = json.Marshal(v)
			}
		}
		planSnapshot, _ = json.Marshal(map[string]any{"id": planID, "name": name, "defaults": defaultValues})
	}
	if groupID := rawInt64(effective["group_id"]); groupID > 0 {
		var exists int64
		err = tx.QueryRowContext(ctx, `SELECT id FROM v2_server_group WHERE id=$1 FOR SHARE`, groupID).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, "", "", ErrNotFound
		}
		if err != nil {
			return nil, "", "", err
		}
	}
	effective, err = normalizeUserChanges(effective)
	if err != nil {
		return nil, "", "", err
	}
	snapshot, _ := json.Marshal(map[string]any{"user": before, "plan": planSnapshot})
	var oldRemarks string
	if json.Unmarshal(before["remarks"], &oldRemarks) == nil && utf8.RuneCountInString(oldRemarks) > 500 {
		before["remarks"], _ = json.Marshal(clip(oldRemarks, 500))
		p.BeforeRemarksTruncated = true
	}
	p.Changes = changes
	p.Before = before
	p.EffectiveChanges = effective
	raw, _ := json.Marshal(p)
	var email string
	_ = json.Unmarshal(before["email"], &email)
	return raw, hashOperation(string(snapshot)), email, nil
}
func applyUserAction(ctx context.Context, tx *sql.Tx, a Action) error {
	var p userUpdatePayload
	if a.Kind != "user_update" || json.Unmarshal(a.Payload, &p) != nil || p.UserID <= 0 || p.UserID != a.TargetID {
		return ErrValidation
	}
	changes, err := normalizeUserChanges(p.EffectiveChanges)
	if err != nil || (len(changes) == 0 && !p.ResetUsage) {
		return ErrValidation
	}
	keys := make([]string, 0, len(changes))
	for key := range changes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	sets := []string{}
	args := []any{p.UserID}
	for _, key := range keys {
		var value any
		if string(changes[key]) != "null" {
			if key == "remarks" {
				var v string
				_ = json.Unmarshal(changes[key], &v)
				value = v
			} else {
				value = rawInt64(changes[key])
			}
		}
		args = append(args, value)
		sets = append(sets, fmt.Sprintf("%s=$%d", key, len(args)))
	}
	if p.ResetUsage {
		sets = append(sets, "u=0", "d=0")
	}
	args = append(args, time.Now().Unix())
	sets = append(sets, fmt.Sprintf("updated_at=$%d", len(args)))
	result, err := tx.ExecContext(ctx, `UPDATE v2_user SET `+strings.Join(sets, ",")+` WHERE id=$1 AND is_admin=0 AND is_staff=0`, args...)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	if rawInt64(changes["banned"]) == 1 {
		_, err = tx.ExecContext(ctx, `DELETE FROM v2_auth_session WHERE user_id=$1`, p.UserID)
	}
	return err
}
