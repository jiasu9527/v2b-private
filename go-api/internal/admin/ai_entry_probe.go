package admin

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

var ErrAIEntryProbeSelection = errors.New("invalid AI entry probe selection")
var ErrAIEntryProbeConflict = errors.New("AI entry probe targets changed")

// This narrow adapter uses the normal monitor run and probe-report pipeline.
// Only the immutable expected target/probe pairs are narrowed for AI requests.
type AIEntryProbeSelection struct {
	PolicyIDs []int64 `json:"policy_ids,omitempty"`
	TargetIDs []int64 `json:"target_ids,omitempty"`
	ProbeIDs  []int64 `json:"probe_ids,omitempty"`
}
type AIEntryProbeTarget struct {
	TargetID         int64  `json:"target_id"`
	Generation       int64  `json:"generation"`
	PolicyID         int64  `json:"policy_id"`
	PolicyName       string `json:"policy_name"`
	SourceKey        string `json:"source_key"`
	Name             string `json:"name"`
	Host             string `json:"host"`
	Port             int64  `json:"port"`
	AutoSplitEnabled bool   `json:"auto_split_enabled"`
}
type AIEntryProbeAgent struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}
type AIEntryProbeSnapshot struct {
	PolicyIDs   []int64              `json:"policy_ids"`
	Targets     []AIEntryProbeTarget `json:"targets"`
	Probes      []AIEntryProbeAgent  `json:"probes"`
	Fingerprint string               `json:"-"`
}

func validateAIProbeIDs(ids []int64) bool {
	if len(ids) > 50 {
		return false
	}
	seen := make(map[int64]bool)
	for _, id := range ids {
		if id <= 0 || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

// Capture and run use the same policy mutation lock. Recheck under that lock
// during insertion prevents approval of one target from dispatching another.
func (s *DBService) CaptureAIEntryProbeSelection(ctx context.Context, tx *sql.Tx, selection AIEntryProbeSelection) (AIEntryProbeSnapshot, error) {
	var out AIEntryProbeSnapshot
	if len(selection.PolicyIDs)+len(selection.TargetIDs) == 0 || !validateAIProbeIDs(selection.PolicyIDs) || !validateAIProbeIDs(selection.TargetIDs) || !validateAIProbeIDs(selection.ProbeIDs) {
		return out, ErrAIEntryProbeSelection
	}
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(2026081001)); err != nil {
		return out, err
	}
	var revision int64
	if err := tx.QueryRowContext(ctx, `SELECT revision FROM v2_client_entry_monitor_config WHERE id=1 FOR SHARE`).Scan(&revision); err != nil {
		return out, err
	}
	clauses := []string{"p.enabled=1", "m.enabled=1"}
	args := []any{}
	addIDs := func(column string, ids []int64) {
		if len(ids) == 0 {
			return
		}
		marks := []string{}
		for _, id := range ids {
			args = append(args, id)
			marks = append(marks, fmt.Sprintf("$%d", len(args)))
		}
		clauses = append(clauses, column+" IN ("+strings.Join(marks, ",")+")")
	}
	addIDs("p.id", selection.PolicyIDs)
	addIDs("t.id", selection.TargetIDs)
	rows, err := tx.QueryContext(ctx, `SELECT t.id,t.generation,p.id,p.name,t.source_key,t.name,t.host,t.port,t.auto_split_enabled,to_jsonb(p)::TEXT,to_jsonb(m)::TEXT
FROM v2_client_entry_monitor_target t JOIN v2_client_entry_monitor m ON m.id=t.monitor_id JOIN v2_client_entry_user_policy p ON p.id=m.policy_id
WHERE `+strings.Join(clauses, " AND ")+` ORDER BY t.id LIMIT 51 FOR SHARE OF p,m,t`, args...)
	if err != nil {
		return out, err
	}
	config := []string{}
	seenTargets, seenPolicies := map[int64]bool{}, map[int64]bool{}
	for rows.Next() {
		var item AIEntryProbeTarget
		var enabled int64
		var policy, monitor string
		if err = rows.Scan(&item.TargetID, &item.Generation, &item.PolicyID, &item.PolicyName, &item.SourceKey, &item.Name, &item.Host, &item.Port, &enabled, &policy, &monitor); err != nil {
			rows.Close()
			return out, err
		}
		item.AutoSplitEnabled = enabled != 0
		out.Targets = append(out.Targets, item)
		config = append(config, policy, monitor)
		seenTargets[item.TargetID] = true
		seenPolicies[item.PolicyID] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if len(out.Targets) == 0 || len(out.Targets) > 50 {
		return out, ErrAIEntryProbeSelection
	}
	for _, id := range selection.TargetIDs {
		if !seenTargets[id] {
			return out, ErrAIEntryProbeConflict
		}
	}
	for _, id := range selection.PolicyIDs {
		if !seenPolicies[id] {
			return out, ErrAIEntryProbeConflict
		}
	}
	for id := range seenPolicies {
		out.PolicyIDs = append(out.PolicyIDs, id)
	}
	sort.Slice(out.PolicyIDs, func(i, j int) bool { return out.PolicyIDs[i] < out.PolicyIDs[j] })
	clauses = []string{"enabled=1"}
	args = nil
	addIDs("id", selection.ProbeIDs)
	rows, err = tx.QueryContext(ctx, `SELECT id,name FROM v2_dns_probe WHERE `+strings.Join(clauses, " AND ")+` ORDER BY id LIMIT 51 FOR SHARE`, args...)
	if err != nil {
		return out, err
	}
	seenProbes := map[int64]bool{}
	for rows.Next() {
		var p AIEntryProbeAgent
		if err = rows.Scan(&p.ID, &p.Name); err != nil {
			rows.Close()
			return out, err
		}
		out.Probes = append(out.Probes, p)
		seenProbes[p.ID] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if len(out.Probes) == 0 || len(out.Probes) > 50 {
		return out, ErrAIEntryProbeSelection
	}
	for _, id := range selection.ProbeIDs {
		if !seenProbes[id] {
			return out, ErrAIEntryProbeConflict
		}
	}
	raw, err := json.Marshal(struct {
		Revision int64
		Config   []string
		Snapshot AIEntryProbeSnapshot
	}{revision, config, out})
	if err != nil {
		return out, err
	}
	sum := sha256.Sum256(raw)
	out.Fingerprint = hex.EncodeToString(sum[:])
	return out, nil
}

func (s *DBService) StartAIEntryProbeRun(ctx context.Context, selection AIEntryProbeSelection, actorID int64, requestKey, expectedSnapshot string) (int64, error) {
	if actorID <= 0 || requestKey == "" || expectedSnapshot == "" {
		return 0, ErrAIEntryProbeSelection
	}
	return s.startClientEntryMonitorRun(ctx, nil, actorID, 0, 0, requestKey, &selection, expectedSnapshot)
}
