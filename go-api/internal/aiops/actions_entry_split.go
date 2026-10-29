package aiops

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"forest/go-api/internal/admin"
)

type entrySplitActionPayload struct {
	TargetID  int64                             `json:"target_id"`
	Revision  string                            `json:"revision"`
	Enabled   *bool                             `json:"enabled,omitempty"`
	Target    *admin.AIEntrySplitTarget         `json:"target,omitempty"`
	Selection *admin.AIEntryDirectSplit         `json:"selection,omitempty"`
	Direct    *admin.AIEntryDirectSplitSnapshot `json:"direct,omitempty"`
	Effects   string                            `json:"effects"`
}

func (s *Service) captureEntrySplitAction(ctx context.Context, tx *sql.Tx, spec actionSpec) (json.RawMessage, string, string, error) {
	var p entrySplitActionPayload
	if json.Unmarshal(spec.Payload, &p) != nil || p.TargetID <= 0 || p.TargetID != spec.TargetID || len(p.Revision) != 64 {
		return nil, "", "", ErrValidation
	}
	if spec.Kind == "entry_split" {
		if p.Selection == nil || p.Selection.TargetID != p.TargetID || p.Selection.Kind != spec.TargetType || p.Selection.Revision != p.Revision {
			return nil, "", "", ErrValidation
		}
		snapshot, err := admin.CaptureAIEntryDirectSplit(ctx, tx, *p.Selection)
		if err != nil {
			return nil, "", "", err
		}
		p.Selection = &snapshot.Selection
		p.Direct = &snapshot
		p.Target = nil
		p.Effects = "将当前名单均分成两个子组，保留UA条件及原规则；本次明确手动执行不依赖自动二分开关。新叶子沿用后台直接二分行为，自动二分默认关闭，不继承父叶子的开启状态。"
		if snapshot.CollectionID != nil {
			p.Effects += "原目标会从入口合集中移出。"
		}
		raw, _ := json.Marshal(p)
		return raw, snapshot.Fingerprint, snapshot.Name, nil
	}
	if spec.TargetType != "monitor_target" || (spec.Kind != "entry_manual_split" && spec.Kind != "entry_auto_split") || (spec.Kind == "entry_auto_split" && p.Enabled == nil) {
		return nil, "", "", ErrValidation
	}
	target, err := admin.CaptureAIEntrySplitTarget(ctx, tx, p.TargetID, spec.Kind == "entry_auto_split")
	if err != nil {
		return nil, "", "", err
	}
	if target.Revision != p.Revision {
		return nil, "", "", ErrConflict
	}
	if spec.Kind == "entry_manual_split" && target.UserCount < 2 {
		return nil, "", "", fmt.Errorf("%w：该叶子不足 2 名用户，无法手动二分", admin.ErrAIEntrySplitInvalid)
	}
	p.Target = &target
	p.Direct = nil
	p.Selection = nil
	if spec.Kind == "entry_manual_split" {
		p.Effects = "本次手动二分不依赖自动开关，不开启永久自动化；队列确认故障后原子领取2个健康备用IP。受理后需查询operation_id，缺IP会等待。"
	} else {
		p.Effects = "只改变该目标自动二分开关；清除该目标旧检测状态、取消旧pending事故；开启后重新累计失败。本动作不会立即二分。"
	}
	raw, _ := json.Marshal(p)
	return raw, target.Revision, target.Name, nil
}

func applyEntrySplitAction(ctx context.Context, tx *sql.Tx, a Action) (string, error) {
	var p entrySplitActionPayload
	if json.Unmarshal(a.Payload, &p) != nil || p.TargetID != a.TargetID {
		return "", ErrValidation
	}
	value := map[string]any{"status": "succeeded", "kind": a.Kind}
	switch a.Kind {
	case "entry_manual_split":
		if p.Target == nil {
			return "", ErrValidation
		}
		id, err := admin.RequestAIEntryManualSplitTx(ctx, tx, *p.Target)
		if err != nil {
			return "", err
		}
		value["operation_id"] = id
		value["operation_status"] = "pending"
		value["split_completed"] = false
		value["message"] = "手动二分已受理，未更改自动二分开关；请用entry_split_operation查询最终结果。可用备用IP不足2个时保留队列等待，不代表已经二分。"
	case "entry_auto_split":
		if p.Target == nil || p.Enabled == nil {
			return "", ErrValidation
		}
		if err := admin.ApplyAIEntryAutoSplitTx(ctx, tx, *p.Target, *p.Enabled); err != nil {
			return "", err
		}
		value["target_id"] = p.TargetID
		value["enabled"] = *p.Enabled
		value["message"] = "已更新自动二分开关，未执行立即二分；检测状态按后台规则重新开始。"
	case "entry_split":
		if p.Selection == nil || p.Direct == nil {
			return "", ErrValidation
		}
		result, err := admin.ApplyAIEntryDirectSplitTx(ctx, tx, *p.Direct)
		if err != nil {
			return "", err
		}
		rows, err := tx.QueryContext(ctx, `SELECT id,name,entry_host FROM v2_client_entry_user_policy_split_group WHERE policy_id=$1 AND (($2::BIGINT=0 AND parent_id IS NULL) OR parent_id=$2) ORDER BY id`, result.ID, func() int64 {
			if p.Selection.Kind == "split_group" {
				return p.TargetID
			}
			return 0
		}())
		if err != nil {
			return "", err
		}
		groups := []map[string]any{}
		for rows.Next() {
			var id int64
			var name, host string
			if err = rows.Scan(&id, &name, &host); err != nil {
				rows.Close()
				return "", err
			}
			groups = append(groups, map[string]any{"group_id": id, "name": name, "entry_host": host})
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return "", err
		}
		value["policy_id"] = result.ID
		value["groups"] = groups
		value["split_completed"] = true
		value["user_count"] = p.Direct.UserCount
		value["message"] = "已按明确指令完成二分，原有UA条件保留；未依赖或顺带开启自动二分。"
	default:
		return "", ErrValidation
	}
	raw, _ := json.Marshal(value)
	return string(raw), nil
}
