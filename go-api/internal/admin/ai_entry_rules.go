package admin

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"forest/go-api/internal/cliententry"
)

var ErrAIEntryRuleConflict = errors.New("入口规则已变化或入口由合集管理")
var ErrAIEntryRuleInvalid = errors.New("入口规则参数无效")

type AIEntryRuleMember struct {
	ServerType string `json:"server_type"`
	ServerID   int64  `json:"server_id"`
}

// Details contain editable rule data only. Extra subscription links, which may
// contain passwords, participate in the revision but are never projected.
type AIEntryRuleSnapshot struct {
	Kind              string                  `json:"kind"`
	TargetID          int64                   `json:"target_id"`
	PolicyID          int64                   `json:"policy_id"`
	Mode              string                  `json:"mode"`
	Name              string                  `json:"name"`
	Sort              int64                   `json:"sort"`
	Action            string                  `json:"action"`
	Conditions        []cliententry.Condition `json:"conditions"`
	Members           []AIEntryRuleMember     `json:"members"`
	EntryHost         string                  `json:"entry_host"`
	ResolveEntryHost  *int64                  `json:"resolve_entry_host"`
	Enabled           int64                   `json:"enabled"`
	Remarks           string                  `json:"remarks"`
	Revision          string                  `json:"revision"`
	CollectionID      *int64                  `json:"collection_id"`
	SharedFields      []string                `json:"shared_fields"`
	collectionHost    string
	collectionResolve *int64
}

// LoadAIEntryRule uses the same topology lock as the existing editors. Both
// proposal creation and execution use this inside their caller's transaction.
func LoadAIEntryRule(ctx context.Context, tx *sql.Tx, kind string, id int64) (AIEntryRuleSnapshot, error) {
	v := AIEntryRuleSnapshot{Kind: kind, TargetID: id, PolicyID: id, Members: []AIEntryRuleMember{}, SharedFields: []string{}}
	if id <= 0 || (kind != "policy" && kind != "split_group") {
		return v, ErrAIEntryRuleInvalid
	}
	if err := lockClientEntryVisibleOrder(ctx, tx); err != nil {
		return v, err
	}
	if kind == "split_group" {
		if err := tx.QueryRowContext(ctx, `SELECT policy_id FROM v2_client_entry_user_policy_split_group WHERE id=$1`, id).Scan(&v.PolicyID); err != nil {
			return v, err
		}
	}
	var rawPolicy, conditions string
	var resolve int64
	err := tx.QueryRowContext(ctx, `SELECT to_jsonb(p)::TEXT,mode,name,sort,action,conditions,entry_host,resolve_entry_host,enabled,remarks FROM v2_client_entry_user_policy p WHERE id=$1 FOR UPDATE`, v.PolicyID).Scan(&rawPolicy, &v.Mode, &v.Name, &v.Sort, &v.Action, &conditions, &v.EntryHost, &resolve, &v.Enabled, &v.Remarks)
	if err != nil {
		return v, err
	}
	v.ResolveEntryHost = &resolve
	if v.Conditions, err = cliententry.DecodeConditions(conditions); err != nil {
		return v, err
	}
	parts := []string{rawPolicy}
	if kind == "split_group" {
		if v.Mode != "split" {
			return v, ErrAIEntryRuleConflict
		}
		var rawLeaf, assignments string
		var leaf bool
		var leafResolve sql.NullInt64
		err = tx.QueryRowContext(ctx, `SELECT to_jsonb(g)::TEXT,name,COALESCE(global_sort,sort),entry_host,resolve_entry_host,NOT EXISTS(SELECT 1 FROM v2_client_entry_user_policy_split_group child WHERE child.parent_id=g.id) FROM v2_client_entry_user_policy_split_group g WHERE id=$1 AND policy_id=$2 FOR UPDATE`, id, v.PolicyID).Scan(&rawLeaf, &v.Name, &v.Sort, &v.EntryHost, &leafResolve, &leaf)
		if err != nil {
			return v, err
		}
		if !leaf {
			return v, ErrAIEntryRuleConflict
		}
		v.ResolveEntryHost = nil
		if leafResolve.Valid {
			value := leafResolve.Int64
			v.ResolveEntryHost = &value
		}
		if err = tx.QueryRowContext(ctx, `SELECT md5(COALESCE(string_agg(user_id::TEXT,',' ORDER BY user_id),'')) FROM v2_client_entry_user_policy_split_assignment WHERE policy_id=$1 AND group_id=$2`, v.PolicyID, id).Scan(&assignments); err != nil {
			return v, err
		}
		parts = append(parts, rawLeaf, assignments)
		v.SharedFields = []string{"enabled", "remarks", "members"}
	} else if v.Mode != "standard" {
		return v, ErrAIEntryRuleConflict
	}
	rows, err := tx.QueryContext(ctx, `SELECT server_type,server_id FROM v2_client_entry_user_policy_member WHERE policy_id=$1 ORDER BY sort ASC NULLS LAST,id ASC`, v.PolicyID)
	if err != nil {
		return v, err
	}
	for rows.Next() {
		var m AIEntryRuleMember
		if err = rows.Scan(&m.ServerType, &m.ServerID); err != nil {
			rows.Close()
			return v, err
		}
		v.Members = append(v.Members, m)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return v, err
	}
	membersRaw, _ := json.Marshal(v.Members)
	parts = append(parts, string(membersRaw))
	column := "policy_id"
	if kind == "split_group" {
		column = "split_group_id"
	}
	var collectionID sql.NullInt64
	var collectionResolve sql.NullInt64
	var collectionRaw string
	err = tx.QueryRowContext(ctx, `SELECT c.id,c.entry_host,c.resolve_entry_host,to_jsonb(c)::TEXT FROM v2_client_entry_collection_member m JOIN v2_client_entry_collection c ON c.id=m.collection_id WHERE m.`+column+`=$1`, id).Scan(&collectionID, &v.collectionHost, &collectionResolve, &collectionRaw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return v, err
	}
	if err == nil {
		value := collectionID.Int64
		v.CollectionID = &value
		if collectionResolve.Valid {
			r := collectionResolve.Int64
			v.collectionResolve = &r
		}
		parts = append(parts, collectionRaw)
	}
	raw, _ := json.Marshal(parts)
	sum := sha256.Sum256(raw)
	v.Revision = hex.EncodeToString(sum[:])
	return v, nil
}

func aiRuleDecode(raw json.RawMessage, out any) error {
	if len(raw) == 0 || string(raw) == "null" {
		return ErrAIEntryRuleInvalid
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil || d.Decode(new(any)) != io.EOF {
		return ErrAIEntryRuleInvalid
	}
	return nil
}

// PrepareAIEntryRuleChanges reuses the normal editor's condition, host, user,
// and node validation. Only supplied fields are returned, so an abbreviated AI
// read can never silently erase the original member list or conditions.
func PrepareAIEntryRuleChanges(ctx context.Context, tx *sql.Tx, before AIEntryRuleSnapshot, changes map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	if len(changes) == 0 || len(changes) > 11 {
		return nil, ErrAIEntryRuleInvalid
	}
	req := ClientEntryUserPolicySaveRequest{Name: before.Name, Action: before.Action, Conditions: before.Conditions, EntryHost: before.EntryHost, ResolveEntryHost: before.ResolveEntryHost, Enabled: &before.Enabled, Remarks: before.Remarks}
	for _, m := range before.Members {
		req.Members = append(req.Members, ClientEntryGroupMemberSaveRequest{ServerType: m.ServerType, ServerID: m.ServerID})
	}
	nextSort := before.Sort
	for key, raw := range changes {
		switch key {
		case "name":
			if aiRuleDecode(raw, &req.Name) != nil {
				return nil, ErrAIEntryRuleInvalid
			}
		case "entry_host":
			if aiRuleDecode(raw, &req.EntryHost) != nil {
				return nil, ErrAIEntryRuleInvalid
			}
		case "remarks":
			if aiRuleDecode(raw, &req.Remarks) != nil {
				return nil, ErrAIEntryRuleInvalid
			}
		case "action":
			if before.Kind != "policy" || aiRuleDecode(raw, &req.Action) != nil {
				return nil, ErrAIEntryRuleInvalid
			}
		case "conditions":
			if before.Kind != "policy" || aiRuleDecode(raw, &req.Conditions) != nil || len(req.Conditions) > 100 {
				return nil, ErrAIEntryRuleInvalid
			}
		case "enabled":
			var n int64
			if aiRuleDecode(raw, &n) != nil || n < 0 || n > 1 {
				return nil, ErrAIEntryRuleInvalid
			}
			req.Enabled = &n
		case "resolve_entry_host":
			var n int64
			if aiRuleDecode(raw, &n) != nil || n < 0 || n > 1 {
				return nil, ErrAIEntryRuleInvalid
			}
			req.ResolveEntryHost = &n
		case "sort":
			if aiRuleDecode(raw, &nextSort) != nil || nextSort < 0 || nextSort > 2147483647 {
				return nil, ErrAIEntryRuleInvalid
			}
		case "members":
			var members []AIEntryRuleMember
			if aiRuleDecode(raw, &members) != nil || len(members) < 1 || len(members) > 500 {
				return nil, ErrAIEntryRuleInvalid
			}
			req.Members = nil
			for _, m := range members {
				req.Members = append(req.Members, ClientEntryGroupMemberSaveRequest{ServerType: m.ServerType, ServerID: m.ServerID})
			}
		default:
			return nil, ErrAIEntryRuleInvalid
		}
	}
	for _, value := range []string{req.Name, req.Remarks, req.EntryHost} {
		if strings.ContainsRune(value, 0) {
			return nil, ErrAIEntryRuleInvalid
		}
	}
	// Moving from override to original/hide necessarily clears the address. It is
	// an explicit effective change and appears on the approval card.
	if _, ok := changes["action"]; ok {
		action, err := cliententry.NormalizeAction(req.Action)
		if err != nil {
			return nil, ErrAIEntryRuleInvalid
		}
		if action != cliententry.ActionOverride {
			if _, explicit := changes["entry_host"]; !explicit {
				req.EntryHost = ""
			}
			zero := int64(0)
			req.ResolveEntryHost = &zero
		}
	}
	prepared, err := normalizeClientEntryUserPolicySaveRequest(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAIEntryRuleInvalid, err)
	}
	if err = validateClientEntryRuleUsers(ctx, tx, prepared.Conditions); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAIEntryRuleInvalid, err)
	}
	if err = validateClientEntryRuleMembers(ctx, tx, prepared.Members); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAIEntryRuleInvalid, err)
	}
	if before.CollectionID != nil && (prepared.EntryHost != before.collectionHost || prepared.Action != cliententry.ActionOverride || (before.collectionResolve != nil && req.ResolveEntryHost != nil && prepared.ResolveEntryHost != *before.collectionResolve)) {
		return nil, ErrAIEntryRuleConflict
	}
	values := map[string]any{"name": prepared.Name, "action": prepared.Action, "conditions": prepared.Conditions, "entry_host": prepared.EntryHost, "resolve_entry_host": prepared.ResolveEntryHost, "enabled": prepared.Enabled, "remarks": prepared.Remarks, "sort": nextSort}
	members := []AIEntryRuleMember{}
	for _, m := range prepared.Members {
		members = append(members, AIEntryRuleMember{ServerType: m.ServerType, ServerID: m.ServerID})
	}
	values["members"] = members
	out := map[string]json.RawMessage{}
	for key := range changes {
		out[key], _ = json.Marshal(values[key])
	}
	if _, ok := changes["action"]; ok && prepared.Action != cliententry.ActionOverride {
		out["entry_host"], _ = json.Marshal("")
		out["resolve_entry_host"] = json.RawMessage("0")
	}
	return out, nil
}

func ApplyAIEntryRuleChanges(ctx context.Context, tx *sql.Tx, before AIEntryRuleSnapshot, effective map[string]json.RawMessage) error {
	if len(effective) == 0 {
		return ErrAIEntryRuleInvalid
	}
	now := time.Now().Unix()
	policySets, leafSets := []string{}, []string{}
	policyArgs, leafArgs := []any{before.PolicyID}, []any{before.TargetID}
	keys := make([]string, 0, len(effective))
	for key := range effective {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		raw := effective[key]
		if key == "members" {
			continue
		}
		var value any
		switch key {
		case "conditions":
			value = string(raw)
		case "name", "action", "entry_host", "remarks":
			var x string
			if aiRuleDecode(raw, &x) != nil {
				return ErrAIEntryRuleInvalid
			}
			value = x
		case "sort", "enabled", "resolve_entry_host":
			var x int64
			if aiRuleDecode(raw, &x) != nil {
				return ErrAIEntryRuleInvalid
			}
			value = x
		default:
			return ErrAIEntryRuleInvalid
		}
		if before.Kind == "split_group" && (key == "name" || key == "entry_host" || key == "sort" || key == "resolve_entry_host") {
			column := key
			if column == "sort" {
				column = "global_sort"
			}
			leafArgs = append(leafArgs, value)
			leafSets = append(leafSets, fmt.Sprintf("%s=$%d", column, len(leafArgs)))
		} else {
			policyArgs = append(policyArgs, value)
			policySets = append(policySets, fmt.Sprintf("%s=$%d", key, len(policyArgs)))
		}
	}
	policyArgs = append(policyArgs, now)
	policySets = append(policySets, fmt.Sprintf("updated_at=$%d", len(policyArgs)))
	result, err := tx.ExecContext(ctx, `UPDATE v2_client_entry_user_policy SET `+strings.Join(policySets, ",")+` WHERE id=$1`, policyArgs...)
	if err != nil {
		return err
	}
	if err = requireClientEntryRuleAffected(result, "规则"); err != nil {
		return err
	}
	if len(leafSets) > 0 {
		leafArgs = append(leafArgs, now)
		leafSets = append(leafSets, fmt.Sprintf("updated_at=$%d", len(leafArgs)))
		result, err = tx.ExecContext(ctx, `UPDATE v2_client_entry_user_policy_split_group SET `+strings.Join(leafSets, ",")+` WHERE id=$1`, leafArgs...)
		if err != nil {
			return err
		}
		if err = requireClientEntryRuleAffected(result, "二分组"); err != nil {
			return err
		}
	}
	if raw, ok := effective["members"]; ok {
		var members []AIEntryRuleMember
		if aiRuleDecode(raw, &members) != nil {
			return ErrAIEntryRuleInvalid
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM v2_client_entry_user_policy_member WHERE policy_id=$1`, before.PolicyID); err != nil {
			return err
		}
		for i, m := range members {
			if _, err = tx.ExecContext(ctx, `INSERT INTO v2_client_entry_user_policy_member(policy_id,server_type,server_id,sort,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$5)`, before.PolicyID, m.ServerType, m.ServerID, int64(i+1)*clientEntryRuleSortStep, now); err != nil {
				return err
			}
		}
	}
	if before.Kind == "split_group" {
		if _, ok := effective["sort"]; ok {
			return syncClientEntrySplitPolicySorts(ctx, tx)
		}
	}
	return nil
}
