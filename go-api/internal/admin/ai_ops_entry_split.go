package admin

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"forest/go-api/internal/cliententry"
	"forest/go-api/internal/subscribelink"
)

var ErrAIEntrySplitInvalid = errors.New("入口二分请求不可用")
var ErrAIEntrySplitConflict = errors.New("入口二分目标已变化，请刷新后重新选择")

func (s *DBService) PrepareAIEntrySplit(ctx context.Context) error {
	if s == nil || s.db == nil {
		return ErrUnavailable
	}
	if err := s.ensureClientEntrySchema(ctx); err != nil {
		return err
	}
	return s.ensureDNSFailoverSchema(ctx)
}

// Public snapshot deliberately excludes user identities and subscription data.
type AIEntrySplitTarget struct {
	TargetID         int64  `json:"target_id"`
	MonitorID        int64  `json:"monitor_id"`
	PolicyID         int64  `json:"policy_id"`
	GroupID          int64  `json:"group_id"`
	Name             string `json:"name"`
	Host             string `json:"host"`
	Port             int64  `json:"port"`
	Generation       int64  `json:"generation"`
	AutoSplitEnabled bool   `json:"auto_split_enabled"`
	MonitorEnabled   bool   `json:"monitor_enabled"`
	PolicyEnabled    bool   `json:"policy_enabled"`
	UserCount        int64  `json:"user_count"`
	Revision         string `json:"revision"`
	SourceKey        string `json:"source_key"`
	MonitorRevision  int64  `json:"-"`
}

type AIEntryDirectSplit struct {
	Kind            string `json:"kind"`
	TargetID        int64  `json:"target_id"`
	Revision        string `json:"revision"`
	UseBackupIPPool bool   `json:"use_backup_ip_pool"`
	EntryHostA      string `json:"entry_host_a,omitempty"`
	EntryHostB      string `json:"entry_host_b,omitempty"`
}

type AIEntryDirectSplitSnapshot struct {
	Selection     AIEntryDirectSplit `json:"selection"`
	PolicyID      int64              `json:"policy_id"`
	Name          string             `json:"name"`
	SourceHost    string             `json:"source_host"`
	UserCount     int64              `json:"user_count"`
	CollectionID  *int64             `json:"collection_id,omitempty"`
	UsersRevision string             `json:"users_revision"`
	Fingerprint   string             `json:"-"`
}

type AIEntrySplitOperation struct {
	ID            int64  `json:"operation_id"`
	TargetID      int64  `json:"target_id"`
	PolicyID      int64  `json:"policy_id"`
	SourceGroupID int64  `json:"source_group_id"`
	SourceHost    string `json:"source_host"`
	Status        string `json:"status"`
	Attempts      int64  `json:"attempts"`
	NextAttemptAt int64  `json:"next_attempt_at"`
	LastError     string `json:"last_error"`
	ChildGroupAID int64  `json:"child_group_a_id"`
	ChildGroupBID int64  `json:"child_group_b_id"`
	CreatedAt     int64  `json:"created_at"`
	CompletedAt   int64  `json:"completed_at"`
	Manual        bool   `json:"manual"`
}

func aiEntrySplitHash(value any) string {
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// Use worker-compatible order: probes -> monitor -> target -> topology. Never
// take the topology lock first and then wait on a target held by the worker.
func CaptureAIEntrySplitTarget(ctx context.Context, tx *sql.Tx, targetID int64, lockMonitorRevision bool) (AIEntrySplitTarget, error) {
	var out AIEntrySplitTarget
	if targetID <= 0 {
		return out, ErrAIEntrySplitInvalid
	}
	if lockMonitorRevision {
		if err := tx.QueryRowContext(ctx, `SELECT revision FROM v2_client_entry_monitor_config WHERE id=1 FOR UPDATE`).Scan(&out.MonitorRevision); err != nil {
			return out, err
		}
	}
	if _, err := lockOnlineClientEntryAutoSplitProbes(ctx, tx, time.Now().Unix()); err != nil {
		return out, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT monitor_id FROM v2_client_entry_monitor_target WHERE id=$1`, targetID).Scan(&out.MonitorID); err != nil {
		return out, err
	}
	var rawMonitor string
	var monitorEnabled int64
	if err := tx.QueryRowContext(ctx, `SELECT policy_id,enabled,to_jsonb(m)::TEXT FROM v2_client_entry_monitor m WHERE id=$1 FOR KEY SHARE`, out.MonitorID).Scan(&out.PolicyID, &monitorEnabled, &rawMonitor); err != nil {
		return out, err
	}
	var automatic int64
	if err := tx.QueryRowContext(ctx, `SELECT id,source_key,host,port,generation,auto_split_enabled FROM v2_client_entry_monitor_target WHERE id=$1 AND monitor_id=$2 FOR UPDATE`, targetID, out.MonitorID).Scan(&out.TargetID, &out.SourceKey, &out.Host, &out.Port, &out.Generation, &automatic); err != nil {
		return out, err
	}
	policyID, groupID, ok := parseClientEntryMonitorSplitGroupSourceKey(out.SourceKey)
	if !ok || policyID != out.PolicyID {
		return out, fmt.Errorf("%w：仅固定二分叶子支持该操作；标准规则请用指定规则二分工具", ErrAIEntrySplitInvalid)
	}
	out.GroupID = groupID
	rule, err := LoadAIEntryRule(ctx, tx, "split_group", groupID)
	if err != nil {
		return out, err
	}
	if rule.EntryHost != out.Host || rule.PolicyID != out.PolicyID {
		return out, ErrAIEntrySplitConflict
	}
	out.Name, out.AutoSplitEnabled, out.MonitorEnabled, out.PolicyEnabled = rule.Name, automatic == 1, monitorEnabled == 1, rule.Enabled == 1
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_client_entry_user_policy_split_assignment WHERE policy_id=$1 AND group_id=$2`, out.PolicyID, out.GroupID).Scan(&out.UserCount); err != nil {
		return out, err
	}
	out.Revision = aiEntrySplitHash([]any{rule.Revision, out.TargetID, out.Generation, out.Host, out.Port, out.AutoSplitEnabled, rawMonitor})
	return out, nil
}

func (s *DBService) ReadAIEntrySplitTarget(ctx context.Context, targetID int64) (AIEntrySplitTarget, error) {
	var out AIEntrySplitTarget
	if err := s.ensureDNSFailoverSchema(ctx); err != nil {
		return out, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	out, err = CaptureAIEntrySplitTarget(ctx, tx, targetID, false)
	if err != nil {
		return out, err
	}
	return out, tx.Commit()
}

// The AI action transaction owns operator/session authorization, CAS and commit.
// This is the same durable manual request used by the administrator picker.
func RequestAIEntryManualSplitTx(ctx context.Context, tx *sql.Tx, target AIEntrySplitTarget) (int64, error) {
	if target.UserCount < 2 {
		return 0, fmt.Errorf("%w：该固定名单叶子不足 2 名用户，无法二分", ErrAIEntrySplitInvalid)
	}
	id, err := requestClientEntryManualAutoSplitTx(ctx, tx, target.TargetID, time.Now().Unix())
	if errors.Is(err, ErrClientEntryManualAutoSplitUnavailable) {
		return 0, fmt.Errorf("%w：当前未满足所有在线探针的新鲜连续失败阈值，或入口已恢复；自动二分开关不会阻止明确手动二分", ErrAIEntrySplitInvalid)
	}
	return id, err
}

func ApplyAIEntryAutoSplitTx(ctx context.Context, tx *sql.Tx, target AIEntrySplitTarget, enabled bool) error {
	if target.AutoSplitEnabled == enabled {
		return nil
	}
	if enabled && target.UserCount < 2 {
		return fmt.Errorf("%w：该固定名单叶子不足 2 名用户，无法启用自动二分", ErrAIEntrySplitInvalid)
	}
	if enabled && (!target.MonitorEnabled || !target.PolicyEnabled) {
		return fmt.Errorf("%w：请先启用该规则及入口检测，再开启自动二分", ErrAIEntrySplitInvalid)
	}
	if err := resetClientEntryMonitorTargetState(ctx, tx, target.MonitorID, target.SourceKey, target.Host, nil, &enabled, false); err != nil {
		return err
	}
	now := time.Now().Unix()
	if _, err := tx.ExecContext(ctx, `UPDATE v2_client_entry_monitor_target SET auto_split_enabled=$2::SMALLINT,generation=generation+CASE WHEN $2::SMALLINT=1 THEN 1 ELSE 0 END,updated_at=$3 WHERE id=$1`, target.TargetID, boolToInt64(enabled), now); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE v2_client_entry_monitor_config SET revision=revision+1,updated_at=$1 WHERE id=1`, now)
	return err
}

func CaptureAIEntryDirectSplit(ctx context.Context, tx *sql.Tx, selection AIEntryDirectSplit) (AIEntryDirectSplitSnapshot, error) {
	var out AIEntryDirectSplitSnapshot
	if selection.TargetID <= 0 || len(selection.Revision) != 64 || (selection.Kind != "policy" && selection.Kind != "split_group") {
		return out, ErrAIEntrySplitInvalid
	}
	if selection.UseBackupIPPool {
		if selection.EntryHostA != "" || selection.EntryHostB != "" {
			return out, fmt.Errorf("%w：使用备用 IP 池时不要同时指定两个入口地址", ErrAIEntrySplitInvalid)
		}
	} else {
		a, b, err := normalizeClientEntryUserPolicySplitHosts(selection.EntryHostA, selection.EntryHostB)
		if err != nil {
			return out, fmt.Errorf("%w：请提供两个不同的有效入口地址，或使用备用 IP 池", ErrAIEntrySplitInvalid)
		}
		selection.EntryHostA, selection.EntryHostB = a, b
	}
	rule, err := LoadAIEntryRule(ctx, tx, selection.Kind, selection.TargetID)
	if err != nil {
		return out, err
	}
	if rule.Revision != selection.Revision {
		return out, ErrAIEntrySplitConflict
	}
	out = AIEntryDirectSplitSnapshot{Selection: selection, PolicyID: rule.PolicyID, Name: rule.Name, SourceHost: rule.EntryHost, CollectionID: rule.CollectionID}
	var userDigest string
	if selection.Kind == "policy" {
		if rule.Action != cliententry.ActionOverride {
			return out, fmt.Errorf("%w：仅覆盖入口的标准规则可转换为二分", ErrAIEntrySplitInvalid)
		}
		var idRange *cliententry.Condition
		for i := range rule.Conditions {
			condition := &rule.Conditions[i]
			if condition.Field == "ua" {
				continue
			}
			if condition.Field != "user_id" || condition.Operator != "between" || idRange != nil {
				return out, fmt.Errorf("%w：标准规则必须包含一个用户 ID 范围，可附加 UA 条件；其他条件不能直接转换", ErrAIEntrySplitInvalid)
			}
			idRange = condition
		}
		if idRange == nil {
			return out, fmt.Errorf("%w：标准规则未设置用户 ID 范围，不能直接转换", ErrAIEntrySplitInvalid)
		}
		minimum, e1 := clientEntryRuleRawInt64(idRange.Min)
		maximum, e2 := clientEntryRuleRawInt64(idRange.Max)
		if e1 != nil || e2 != nil || minimum <= 0 || maximum < minimum {
			return out, ErrAIEntrySplitInvalid
		}
		var extraRaw string
		if err := tx.QueryRowContext(ctx, `SELECT extra_nodes FROM v2_client_entry_user_policy WHERE id=$1`, rule.PolicyID).Scan(&extraRaw); err != nil {
			return out, err
		}
		extra, err := subscribelink.DecodeList(extraRaw)
		if err != nil || len(extra) != 0 {
			return out, fmt.Errorf("%w：包含额外下发节点的标准规则不能直接转换", ErrAIEntrySplitInvalid)
		}
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),md5(COALESCE(string_agg(id::TEXT,',' ORDER BY id),'')) FROM v2_user WHERE id BETWEEN $1 AND $2`, minimum, maximum).Scan(&out.UserCount, &userDigest); err != nil {
			return out, err
		}
	} else {
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),md5(COALESCE(string_agg(user_id::TEXT,',' ORDER BY user_id),'')) FROM v2_client_entry_user_policy_split_assignment WHERE policy_id=$1 AND group_id=$2`, rule.PolicyID, selection.TargetID).Scan(&out.UserCount, &userDigest); err != nil {
			return out, err
		}
	}
	if out.UserCount < 2 {
		return out, fmt.Errorf("%w：所选规则或叶子当前不足 2 名用户，无法二分", ErrAIEntrySplitInvalid)
	}
	out.UsersRevision = userDigest
	out.Fingerprint = aiEntrySplitHash([]any{out, userDigest})
	return out, nil
}

func ApplyAIEntryDirectSplitTx(ctx context.Context, tx *sql.Tx, snapshot AIEntryDirectSplitSnapshot) (ClientEntryUserPolicyRecord, error) {
	selection := snapshot.Selection
	var result ClientEntryUserPolicyRecord
	var err error
	if selection.Kind == "policy" {
		result, err = convertClientEntryUserPolicyToSplitTx(ctx, tx, ClientEntryUserPolicySplitConvertRequest{PolicyID: snapshot.PolicyID, EntryHostA: selection.EntryHostA, EntryHostB: selection.EntryHostB, UseBackupIPPool: selection.UseBackupIPPool}, selection.EntryHostA, selection.EntryHostB)
	} else {
		result, err = splitClientEntryUserPolicyGroupTx(ctx, tx, ClientEntryUserPolicyGroupSplitRequest{PolicyID: snapshot.PolicyID, GroupID: selection.TargetID, EntryHostA: selection.EntryHostA, EntryHostB: selection.EntryHostB, UseBackupIPPool: selection.UseBackupIPPool}, selection.EntryHostA, selection.EntryHostB, time.Now().Unix())
	}
	if errors.Is(err, ErrClientEntryBackupIPInsufficient) {
		return result, fmt.Errorf("%w：可用且健康的备用 IP 不足 2 个，未二分、未领取 IP", ErrAIEntrySplitInvalid)
	}
	if err != nil {
		return result, err
	}
	if selection.Kind == "policy" {
		var assigned int64
		var digest string
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),md5(COALESCE(string_agg(user_id::TEXT,',' ORDER BY user_id),'')) FROM v2_client_entry_user_policy_split_assignment WHERE policy_id=$1`, snapshot.PolicyID).Scan(&assigned, &digest); err != nil {
			return result, err
		}
		if assigned != snapshot.UserCount || digest != snapshot.UsersRevision {
			return result, ErrAIEntrySplitConflict
		}
	} else {
		var assigned int64
		var digest string
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),md5(COALESCE(string_agg(a.user_id::TEXT,',' ORDER BY a.user_id),'')) FROM v2_client_entry_user_policy_split_assignment a JOIN v2_client_entry_user_policy_split_group g ON g.id=a.group_id AND g.policy_id=a.policy_id WHERE a.policy_id=$1 AND g.parent_id=$2`, snapshot.PolicyID, selection.TargetID).Scan(&assigned, &digest); err != nil {
			return result, err
		}
		if assigned != snapshot.UserCount || digest != snapshot.UsersRevision {
			return result, ErrAIEntrySplitConflict
		}
	}
	return result, nil
}

func (s *DBService) GetAIEntrySplitOperation(ctx context.Context, id int64) (AIEntrySplitOperation, error) {
	var out AIEntrySplitOperation
	if id <= 0 {
		return out, ErrAIEntrySplitInvalid
	}
	if err := s.ensureDNSFailoverSchema(ctx); err != nil {
		return out, err
	}
	err := s.db.QueryRowContext(ctx, `SELECT id,COALESCE(target_id,0),policy_id,source_group_id,source_host,status,attempts,next_attempt_at,last_error,COALESCE(child_group_a_id,0),COALESCE(child_group_b_id,0),created_at,COALESCE(completed_at,0),trigger_probe_id IS NULL FROM v2_client_entry_auto_split_operation WHERE id=$1`, id).Scan(&out.ID, &out.TargetID, &out.PolicyID, &out.SourceGroupID, &out.SourceHost, &out.Status, &out.Attempts, &out.NextAttemptAt, &out.LastError, &out.ChildGroupAID, &out.ChildGroupBID, &out.CreatedAt, &out.CompletedAt, &out.Manual)
	out.LastError = strings.TrimSpace(out.LastError)
	return out, err
}

func (s *DBService) WakeAIEntrySplits(ctx context.Context) {
	s.markClientEntryMonitorTargetsDirty()
	s.wakeClientEntryMonitorEvaluation(ctx)
}
