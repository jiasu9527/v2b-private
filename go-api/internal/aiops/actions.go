package aiops

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"forest/go-api/internal/cliententry"
	"github.com/jackc/pgx/v5/pgconn"
)

const actionLifetime = 15 * time.Minute
const executionLifetime = 120 * time.Second

var nodeTables = map[string]string{"shadowsocks": "v2_server_shadowsocks", "vmess": "v2_server_vmess", "vless": "v2_server_vless", "trojan": "v2_server_trojan", "tuic": "v2_server_tuic", "hysteria": "v2_server_hysteria", "anytls": "v2_server_anytls", "v2node": "v2_server_v2node"}

type actionSpec struct {
	Kind       string
	TargetID   int64
	TargetType string
	Payload    json.RawMessage
	Reason     string
}

func hashOperation(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
func scanAction(row interface{ Scan(...any) error }) (Action, error) {
	var a Action
	err := row.Scan(&a.ID, &a.SessionID, &a.ActorID, &a.ApprovedBy, &a.RejectedBy, &a.ToolCallID, &a.Kind, &a.TargetID, &a.TargetType, &a.ServerLabel, &a.Payload, &a.Reason, &a.Status, &a.ExpiresAt, &a.ClaimedAt, &a.CompletedAt, &a.Output, &a.Error, &a.CreatedAt, &a.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return a, err
}
func (s *Service) ListActions(ctx context.Context, actorID, sessionID int64) ([]Action, error) {
	if err := s.ensureSchema(ctx); err != nil {
		return nil, err
	}
	if err := s.requireAdmin(ctx, actorID); err != nil {
		return nil, err
	}
	if sessionID <= 0 {
		return nil, ErrValidation
	}
	if err := s.reconcileExpiredActions(ctx); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+actionColumns+` FROM v2_ai_action WHERE session_id=$1 ORDER BY id DESC LIMIT 100`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Action{}
	for rows.Next() {
		a, err := scanAction(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Called by both list and management paths: a restart must not leave an old
// running record permanently blocking reset or server maintenance.
func (s *Service) reconcileExpiredActions(ctx context.Context) error {
	now := time.Now().Unix()
	_, err := s.db.ExecContext(ctx, `UPDATE v2_ai_action SET status=CASE WHEN status='pending' THEN 'expired' ELSE 'unknown' END,error=CASE WHEN status='running' THEN '执行连接已中断或进程已重启，结果未知，可能已部分执行；禁止自动重试' ELSE error END,completed_at=$1,updated_at=$1 WHERE (status='pending' AND expires_at<=$1) OR(status='running' AND claimed_at<$1-`+actionStaleSecondsSQL+`)`, now)
	return err
}
func (s *Service) proposeAction(ctx context.Context, actorID, sessionID int64, callID string, spec actionSpec) (Action, error) {
	var empty Action
	if actorID <= 0 || sessionID <= 0 || len(callID) < 1 || len(callID) > 255 || len(spec.Payload) > 32768 || strings.TrimSpace(spec.Reason) == "" || len([]rune(spec.Reason)) > 500 {
		return empty, ErrValidation
	}
	if err := s.ensureSchema(ctx); err != nil {
		return empty, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	if err = lockAIState(ctx, tx); err != nil {
		return empty, err
	}
	if err = s.requireAdminTx(ctx, tx, actorID); err != nil {
		return empty, err
	}
	if err = s.requireSessionTx(ctx, tx, sessionID); err != nil {
		return empty, err
	}
	reqBytes, _ := json.Marshal(spec)
	requestHash := hashOperation(string(reqBytes))
	var existingHash string
	err = tx.QueryRowContext(ctx, `SELECT request_hash FROM v2_ai_action WHERE session_id=$1 AND tool_call_id=$2`, sessionID, callID).Scan(&existingHash)
	if err == nil {
		if existingHash != requestHash {
			return empty, ErrConflict
		}
		a, err := scanAction(tx.QueryRowContext(ctx, `SELECT `+actionColumns+` FROM v2_ai_action WHERE session_id=$1 AND tool_call_id=$2`, sessionID, callID))
		if err != nil {
			return empty, err
		}
		return a, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return empty, err
	}
	var pending int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_ai_action WHERE session_id=$1 AND status='pending' AND expires_at>$2`, sessionID, time.Now().Unix()).Scan(&pending); err != nil {
		return empty, err
	}
	if pending >= 20 {
		return empty, ErrBusy
	}
	payload, snapshot, label, err := s.captureActionTarget(ctx, tx, spec)
	if err != nil {
		return empty, err
	}
	now := time.Now().Unix()
	a, err := scanAction(tx.QueryRowContext(ctx, `INSERT INTO v2_ai_action(session_id,actor_id,tool_call_id,kind,target_id,target_type,server_label,payload,snapshot_hash,request_hash,reason,status,expires_at,created_at,updated_at)VALUES($1,$2,$3,$4,$5,$6,$7,$8::JSONB,$9,$10,$11,'pending',$12,$13,$13) RETURNING `+actionColumns, sessionID, actorID, callID, spec.Kind, spec.TargetID, spec.TargetType, label, string(payload), snapshot, requestHash, spec.Reason, now+int64(actionLifetime/time.Second), now))
	if err != nil {
		return empty, err
	}
	return a, tx.Commit()
}

type sshPayload struct {
	ServerID       int64  `json:"server_id"`
	Command        string `json:"command"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	Host           string `json:"host"`
	Port           int    `json:"port"`
	Username       string `json:"username"`
	HostKeySHA256  string `json:"host_key_sha256"`
}
type entryPayload struct {
	Kind       string `json:"kind"`
	TargetID   int64  `json:"target_id"`
	BeforeHost string `json:"before_host"`
	Host       string `json:"host"`
}
type nodePatch struct {
	Host *string `json:"host,omitempty"`
	Port *int    `json:"port,omitempty"`
	Show *int    `json:"show,omitempty"`
}
type nodePayload struct {
	NodeType string         `json:"node_type"`
	NodeID   int64          `json:"node_id"`
	Before   map[string]any `json:"before"`
	Changes  nodePatch      `json:"changes"`
}
type monitorPayload struct {
	PolicyIDs []int64         `json:"policy_ids"`
	Targets   []monitorTarget `json:"targets,omitempty"`
}

type monitorTarget struct {
	PolicyID         int64  `json:"policy_id"`
	Host             string `json:"host"`
	Port             int    `json:"port"`
	AutoSplitEnabled bool   `json:"auto_split_enabled"`
}

func (s *Service) captureActionTarget(ctx context.Context, tx *sql.Tx, spec actionSpec) (json.RawMessage, string, string, error) {
	switch spec.Kind {
	case "user_update":
		return s.captureUserAction(ctx, tx, spec)
	case "ticket_update", "ticket_settings":
		return s.captureTicketAction(ctx, tx, spec)
	case "entry_probe_run":
		return s.captureProbeAction(ctx, tx, spec)
	case "entry_rule_update":
		return s.captureEntryRuleAction(ctx, tx, spec)
	case "node_update":
		return s.captureNodeUpdateAction(ctx, tx, spec)
	case "ssh_command":
		var p sshPayload
		if json.Unmarshal(spec.Payload, &p) != nil || p.ServerID <= 0 || len(p.Command) == 0 || len(p.Command) > 16000 || strings.ContainsRune(p.Command, 0) {
			return nil, "", "", ErrValidation
		}
		timeoutSeconds, err := normalizeSSHTimeout(p.TimeoutSeconds)
		if err != nil {
			return nil, "", "", err
		}
		p.TimeoutSeconds = timeoutSeconds
		v, err := s.loadSSHServer(ctx, tx, p.ServerID, true)
		if err != nil {
			return nil, "", "", err
		}
		p.Host = v.Host
		p.Port = v.Port
		p.Username = v.Username
		p.HostKeySHA256 = v.HostKeySHA256
		raw, _ := json.Marshal(p)
		snapshot, _ := json.Marshal(v)
		return raw, hashOperation(string(snapshot)), v.Name, nil
	case "entry_host":
		var p entryPayload
		if json.Unmarshal(spec.Payload, &p) != nil || (p.Kind != "policy" && p.Kind != "split_group") || p.TargetID <= 0 {
			return nil, "", "", ErrValidation
		}
		host, err := cliententry.NormalizeHost(p.Host)
		if err != nil {
			return nil, "", "", ErrValidation
		}
		p.Host = host
		before, hash, err := loadEntrySnapshot(ctx, tx, p.Kind, p.TargetID)
		if err != nil {
			return nil, "", "", err
		}
		if before == host {
			return nil, "", "", ErrValidation
		}
		p.BeforeHost = before
		raw, _ := json.Marshal(p)
		return raw, hash, "", nil
	case "node_change":
		var p nodePayload
		if json.Unmarshal(spec.Payload, &p) != nil || p.NodeID <= 0 || nodeTables[p.NodeType] == "" {
			return nil, "", "", ErrValidation
		}
		if p.Changes.Host == nil && p.Changes.Port == nil && p.Changes.Show == nil {
			return nil, "", "", ErrValidation
		}
		if p.Changes.Host != nil {
			host, err := cliententry.NormalizeHost(*p.Changes.Host)
			if err != nil {
				return nil, "", "", ErrValidation
			}
			p.Changes.Host = &host
		}
		if p.Changes.Port != nil && (*p.Changes.Port < 1 || *p.Changes.Port > 65535) {
			return nil, "", "", ErrValidation
		}
		if p.Changes.Show != nil && *p.Changes.Show != 0 && *p.Changes.Show != 1 {
			return nil, "", "", ErrValidation
		}
		before, hash, err := loadNodeSnapshot(ctx, tx, p.NodeType, p.NodeID)
		if err != nil {
			return nil, "", "", err
		}
		p.Before = before
		raw, _ := json.Marshal(p)
		return raw, hash, "", nil
	case "monitor_run":
		var p monitorPayload
		if json.Unmarshal(spec.Payload, &p) != nil || len(p.PolicyIDs) < 1 || len(p.PolicyIDs) > 10 {
			return nil, "", "", ErrValidation
		}
		seen := map[int64]bool{}
		for _, id := range p.PolicyIDs {
			if id <= 0 || seen[id] {
				return nil, "", "", ErrValidation
			}
			seen[id] = true
		}
		targets, snapshot, err := loadMonitorSnapshot(ctx, tx, p.PolicyIDs)
		if err != nil {
			return nil, "", "", err
		}
		p.Targets = targets
		raw, _ := json.Marshal(p)
		return raw, snapshot, "", nil
	default:
		return nil, "", "", ErrValidation
	}
}

// Snapshot configuration, not noisy probe observations. An operator changing a
// host, port, automatic-split setting, policy, or probe enablement invalidates
// the card; the model cannot silently reuse a confirmation for new targets.
func loadMonitorSnapshot(ctx context.Context, tx *sql.Tx, ids []int64) ([]monitorTarget, string, error) {
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(2026081001)); err != nil {
		return nil, "", err
	}
	var revision int64
	if err := tx.QueryRowContext(ctx, `SELECT revision FROM v2_client_entry_monitor_config WHERE id=1 FOR SHARE`).Scan(&revision); err != nil {
		return nil, "", err
	}
	snapshots := []string{strconv.FormatInt(revision, 10)}
	targets := []monitorTarget{}
	for _, id := range ids {
		var policy, monitor, leaves, rawTargets string
		err := tx.QueryRowContext(ctx, `SELECT to_jsonb(p)::TEXT,to_jsonb(m)::TEXT,
COALESCE((SELECT jsonb_agg(to_jsonb(g) ORDER BY g.id) FROM v2_client_entry_user_policy_split_group g WHERE g.policy_id=p.id),'[]'::JSONB)::TEXT,
COALESCE((SELECT jsonb_agg(to_jsonb(t) ORDER BY t.id) FROM v2_client_entry_monitor_target t WHERE t.monitor_id=m.id),'[]'::JSONB)::TEXT
FROM v2_client_entry_user_policy p JOIN v2_client_entry_monitor m ON m.policy_id=p.id WHERE p.id=$1 AND p.enabled=1 AND m.enabled=1`, id).Scan(&policy, &monitor, &leaves, &rawTargets)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, "", ErrConflict
		}
		if err != nil {
			return nil, "", err
		}
		var rows []struct {
			Host             string `json:"host"`
			Port             int    `json:"port"`
			AutoSplitEnabled int    `json:"auto_split_enabled"`
		}
		if json.Unmarshal([]byte(rawTargets), &rows) != nil || len(rows) == 0 || len(targets)+len(rows) > 100 {
			return nil, "", ErrValidation
		}
		for _, row := range rows {
			targets = append(targets, monitorTarget{PolicyID: id, Host: row.Host, Port: row.Port, AutoSplitEnabled: row.AutoSplitEnabled != 0})
		}
		snapshots = append(snapshots, policy, monitor, leaves, rawTargets)
	}
	var probes string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(jsonb_agg(jsonb_build_object('id',id,'name',name) ORDER BY id),'[]'::JSONB)::TEXT FROM v2_dns_probe WHERE enabled=1`).Scan(&probes); err != nil {
		return nil, "", err
	}
	if probes == "[]" {
		return nil, "", ErrConflict
	}
	snapshots = append(snapshots, probes)
	raw, _ := json.Marshal(snapshots)
	return targets, hashOperation(string(raw)), nil
}
func loadEntrySnapshot(ctx context.Context, tx *sql.Tx, kind string, id int64) (string, string, error) {
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(2026081001)); err != nil {
		return "", "", err
	}
	policyID := id
	if kind == "split_group" {
		if err := tx.QueryRowContext(ctx, `SELECT policy_id FROM v2_client_entry_user_policy_split_group WHERE id=$1`, id).Scan(&policyID); errors.Is(err, sql.ErrNoRows) {
			return "", "", ErrNotFound
		} else if err != nil {
			return "", "", err
		}
	}
	var raw, mode, action, host string
	if err := tx.QueryRowContext(ctx, `SELECT to_jsonb(p)::TEXT,mode,action,entry_host FROM v2_client_entry_user_policy p WHERE id=$1 FOR UPDATE`, policyID).Scan(&raw, &mode, &action, &host); errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrNotFound
	} else if err != nil {
		return "", "", err
	}
	if action != "override" || (kind == "policy" && mode != "standard") || (kind == "split_group" && mode != "split") {
		return "", "", ErrValidation
	}
	column := "policy_id"
	if kind == "split_group" {
		column = "split_group_id"
	}
	var grouped bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM v2_client_entry_collection_member WHERE `+column+`=$1)`, id).Scan(&grouped); err != nil {
		return "", "", err
	}
	if grouped {
		return "", "", ErrConflict
	}
	leaf, assign := "", ""
	if kind == "split_group" {
		var isLeaf bool
		if err := tx.QueryRowContext(ctx, `SELECT to_jsonb(g)::TEXT,g.entry_host,NOT EXISTS(SELECT 1 FROM v2_client_entry_user_policy_split_group child WHERE child.parent_id=g.id) FROM v2_client_entry_user_policy_split_group g WHERE g.id=$1 AND g.policy_id=$2 FOR UPDATE`, id, policyID).Scan(&leaf, &host, &isLeaf); err != nil {
			return "", "", err
		}
		if !isLeaf {
			return "", "", ErrConflict
		}
		if err := tx.QueryRowContext(ctx, `SELECT md5(COALESCE(string_agg(user_id::TEXT,',' ORDER BY user_id),'')) FROM v2_client_entry_user_policy_split_assignment WHERE policy_id=$1 AND group_id=$2`, policyID, id).Scan(&assign); err != nil {
			return "", "", err
		}
	}
	var members string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(member) ORDER BY member.server_type,member.server_id,member.id),'[]'::JSONB)::TEXT FROM v2_client_entry_user_policy_member member WHERE policy_id=$1`, policyID).Scan(&members); err != nil {
		return "", "", err
	}
	encoded, _ := json.Marshal([]string{raw, leaf, assign, members})
	return host, hashOperation(string(encoded)), nil
}
func loadNodeSnapshot(ctx context.Context, tx *sql.Tx, kind string, id int64) (map[string]any, string, error) {
	table := nodeTables[kind]
	if table == "" {
		return nil, "", ErrValidation
	}
	var raw, host, port string
	var show int
	if err := tx.QueryRowContext(ctx, `SELECT to_jsonb(n)::TEXT,host,port::TEXT,"show" FROM `+table+` n WHERE id=$1 FOR UPDATE`, id).Scan(&raw, &host, &port, &show); errors.Is(err, sql.ErrNoRows) {
		return nil, "", ErrNotFound
	} else if err != nil {
		return nil, "", err
	}
	return map[string]any{"host": host, "port": port, "show": show}, hashOperation(raw), nil
}
func (s *Service) RejectAction(ctx context.Context, actorID, sessionID, id int64) (Action, error) {
	return s.changeAction(ctx, actorID, sessionID, id, false)
}
func (s *Service) ApproveAction(ctx context.Context, actorID, sessionID, id int64) (Action, error) {
	return s.changeAction(ctx, actorID, sessionID, id, true)
}

func requireActionSessionTx(ctx context.Context, tx *sql.Tx, sessionID int64) error {
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM v2_ai_session WHERE id=$1 FOR UPDATE NOWAIT`, sessionID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	var pgerr *pgconn.PgError
	if errors.As(err, &pgerr) && pgerr.Code == "55P03" {
		return ErrBusy
	}
	return err
}
func (s *Service) changeAction(ctx context.Context, actorID, sessionID, id int64, approve bool) (Action, error) {
	var empty Action
	if id <= 0 || sessionID <= 0 {
		return empty, ErrValidation
	}
	if err := s.ensureSchema(ctx); err != nil {
		return empty, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	if err = lockAIState(ctx, tx); err != nil {
		return empty, err
	}
	if err = s.requireAdminTx(ctx, tx, actorID); err != nil {
		return empty, err
	}
	if err = requireActionSessionTx(ctx, tx, sessionID); err != nil {
		return empty, err
	}
	a, err := scanAction(tx.QueryRowContext(ctx, `SELECT `+actionColumns+` FROM v2_ai_action WHERE id=$1 AND session_id=$2 FOR UPDATE`, id, sessionID))
	if err != nil {
		return empty, err
	}
	if a.Status != "pending" {
		return a, ErrConflict
	}
	now := time.Now().Unix()
	if a.ExpiresAt <= now {
		return a, ErrConflict
	}
	if !approve {
		if _, err = tx.ExecContext(ctx, `UPDATE v2_ai_action SET status='rejected',completed_at=$2,updated_at=$2,rejected_by=$3 WHERE id=$1`, id, now, actorID); err != nil {
			return empty, err
		}
		a.Status = "rejected"
		a.CompletedAt = now
		a.UpdatedAt = now
		a.RejectedBy = actorID
		return a, tx.Commit()
	}
	// Long SSH commands no longer hold the session row lock while executing.
	// Keep approvals serialized explicitly while allowing the model to save
	// its final reply and the browser to receive ongoing output.
	var actionRunning bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM v2_ai_action WHERE session_id=$1 AND status='running')`, sessionID).Scan(&actionRunning); err != nil {
		return empty, err
	}
	if actionRunning {
		return empty, ErrBusy
	}
	// Disabling AI prevents new approvals, not only new model requests. Lock the
	// config row until the action is claimed so a concurrent disable is ordered.
	var enabled bool
	if err = tx.QueryRowContext(ctx, `SELECT enabled FROM v2_ai_config WHERE id=1 FOR SHARE`).Scan(&enabled); errors.Is(err, sql.ErrNoRows) {
		return empty, ErrUnavailable
	} else if err != nil {
		return empty, err
	}
	if !enabled {
		return empty, ErrUnavailable
	}
	var snapshot string
	if err = tx.QueryRowContext(ctx, `SELECT snapshot_hash FROM v2_ai_action WHERE id=$1`, id).Scan(&snapshot); err != nil {
		return empty, err
	}
	spec := actionSpec{Kind: a.Kind, TargetID: a.TargetID, TargetType: a.TargetType, Payload: a.Payload, Reason: a.Reason}
	_, current, _, err := s.captureActionTarget(ctx, tx, spec)
	if err != nil {
		return empty, err
	}
	if snapshot != current {
		return a, ErrConflict
	}
	if a.Kind == "entry_host" || a.Kind == "node_change" || a.Kind == "node_update" || a.Kind == "entry_rule_update" || a.Kind == "user_update" || a.Kind == "ticket_update" {
		a.ApprovedBy = actorID
		if err = applyPanelAction(ctx, tx, a); err != nil {
			return empty, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE v2_ai_action SET status='succeeded',claimed_at=$2,completed_at=$2,updated_at=$2,approved_by=$3,output='已执行经人工批准的配置变更' WHERE id=$1`, id, now, actorID); err != nil {
			return empty, err
		}
		if err = tx.Commit(); err != nil {
			return empty, err
		}
		a.Status = "succeeded"
		a.ClaimedAt = now
		a.CompletedAt = now
		a.UpdatedAt = now
		a.ApprovedBy = actorID
		a.Output = "已执行经人工批准的配置变更"
		if invalidator, ok := s.panel.(interface{ InvalidateAIEntryTargets() }); ok {
			if a.Kind == "entry_host" || a.Kind == "node_change" || a.Kind == "node_update" || a.Kind == "entry_rule_update" {
				invalidator.InvalidateAIEntryTargets()
			}
		}
		if a.Kind == "user_update" {
			if invalidator, ok := s.panel.(interface{ InvalidateAIUserSessions(int64) }); ok {
				invalidator.InvalidateAIUserSessions(a.TargetID)
			}
		}
		if a.Kind == "ticket_update" {
			s.afterTicketAction(ctx, a)
		}
		return a, nil
	}
	// Register under the service lifecycle lock before committing the claim. The
	// Close method cancels and waits, and identical actions cannot be run twice.
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return empty, ErrUnavailable
	}
	workerCtx, cancel := context.WithTimeout(context.Background(), actionExecutionLifetime(a))
	workerKey := -a.ID
	if _, exists := s.workers[workerKey]; exists {
		s.mu.Unlock()
		cancel()
		return empty, ErrConflict
	}
	s.workers[workerKey] = cancel
	s.wg.Add(1)
	if _, err = tx.ExecContext(ctx, `UPDATE v2_ai_action SET status='running',claimed_at=$2,updated_at=$2,approved_by=$3 WHERE id=$1 AND status='pending'`, id, now, actorID); err == nil {
		err = tx.Commit()
	}
	if err != nil {
		delete(s.workers, workerKey)
		s.wg.Done()
		s.mu.Unlock()
		cancel()
		return empty, err
	}
	s.mu.Unlock()
	a.Status = "running"
	a.ClaimedAt = now
	a.UpdatedAt = now
	a.ApprovedBy = actorID
	go s.executeAction(workerCtx, cancel, a, snapshot)
	return a, nil
}
func applyPanelAction(ctx context.Context, tx *sql.Tx, a Action) error {
	if a.Kind == "entry_rule_update" {
		return applyEntryRuleAction(ctx, tx, a)
	}
	if a.Kind == "node_update" {
		return applyNodeUpdateAction(ctx, tx, a)
	}
	if a.Kind == "user_update" {
		return applyUserAction(ctx, tx, a)
	}
	if a.Kind == "ticket_update" {
		return applyTicketAction(ctx, tx, a)
	}
	if a.Kind == "entry_host" {
		var p entryPayload
		if json.Unmarshal(a.Payload, &p) != nil {
			return ErrValidation
		}
		table := "v2_client_entry_user_policy"
		if p.Kind == "split_group" {
			table = "v2_client_entry_user_policy_split_group"
		}
		_, err := tx.ExecContext(ctx, `UPDATE `+table+` SET entry_host=$2,updated_at=$3 WHERE id=$1`, p.TargetID, p.Host, time.Now().Unix())
		return err
	}
	var p nodePayload
	if json.Unmarshal(a.Payload, &p) != nil {
		return ErrValidation
	}
	table := nodeTables[p.NodeType]
	if table == "" {
		return ErrValidation
	}
	sets := []string{}
	args := []any{p.NodeID}
	if p.Changes.Host != nil {
		args = append(args, *p.Changes.Host)
		sets = append(sets, fmt.Sprintf("host=$%d", len(args)))
	}
	if p.Changes.Port != nil {
		if p.NodeType == "vless" {
			args = append(args, *p.Changes.Port)
		} else {
			args = append(args, strconv.Itoa(*p.Changes.Port))
		}
		sets = append(sets, fmt.Sprintf("port=$%d", len(args)))
	}
	if p.Changes.Show != nil {
		args = append(args, *p.Changes.Show)
		sets = append(sets, fmt.Sprintf(`"show"=$%d`, len(args)))
	}
	if len(sets) == 0 {
		return ErrValidation
	}
	args = append(args, time.Now().Unix())
	sets = append(sets, fmt.Sprintf("updated_at=$%d", len(args)))
	_, err := tx.ExecContext(ctx, `UPDATE `+table+` SET `+strings.Join(sets, ",")+` WHERE id=$1`, args...)
	return err
}
func (s *Service) executeAction(ctx context.Context, cancel context.CancelFunc, a Action, snapshot string) {
	defer func() { cancel(); s.mu.Lock(); delete(s.workers, -a.ID); s.mu.Unlock(); s.wg.Done() }()
	status, output, problem := "failed", "", "操作执行失败，可能已部分执行；请人工检查"
	if a.Kind == "ticket_settings" {
		status, output, problem = s.executeTicketSettings(ctx, a, snapshot)
	}
	if a.Kind == "entry_probe_run" {
		status, output, problem = s.executeProbeAction(ctx, a, snapshot)
	}
	if a.Kind == "ssh_command" {
		var p sshPayload
		if json.Unmarshal(a.Payload, &p) == nil {
			// Hold a SHARE lock across connection/execution. Credential edits/deletion
			// wait, so a deleted or replaced server is never used by a late worker.
			tx, err := s.db.BeginTx(ctx, nil)
			if err == nil {
				defer tx.Rollback()
				if err = lockAIState(ctx, tx); err == nil {
					// Cleanup requires the exclusive AI state lock, so this shared
					// lock already preserves the session. Do not hold its row lock
					// for a long installation: the model must still finish its reply.
					var sessionID int64
					err = tx.QueryRowContext(ctx, `SELECT id FROM v2_ai_session WHERE id=$1`, a.SessionID).Scan(&sessionID)
				}
				if err == nil {
					err = s.requireAdminTx(ctx, tx, a.ApprovedBy)
				}
				var v sshSecretServer
				if err == nil {
					v, err = s.loadSSHServer(ctx, tx, p.ServerID, true)
				}
				if err == nil {
					raw, _ := json.Marshal(v)
					if hashOperation(string(raw)) != snapshot {
						err = ErrConflict
					}
				}
				if err == nil {
					result := s.runSSHWithProgress(ctx, v, p.Command, func(output string) { s.persistSSHProgress(ctx, a.ID, output) })
					output = result.Output
					if result.Err == nil {
						status = "succeeded"
						problem = ""
					} else {
						problem = result.Err.Error()
						if result.Uncertain {
							status = "unknown"
						}
					}
				} else {
					problem = "服务器或会话配置已变化，未发送命令"
				}
			}
		}
	}
	if a.Kind == "monitor_run" {
		var p monitorPayload
		if json.Unmarshal(a.Payload, &p) == nil && s.monitorActionStillCurrent(ctx, p.PolicyIDs, snapshot) {
			if runner, ok := s.panel.(interface {
				StartClientEntryMonitorRunForPoliciesWithMessage(context.Context, []int64, int64, int64, int64, string) (int64, error)
			}); ok {
				id, err := runner.StartClientEntryMonitorRunForPoliciesWithMessage(ctx, p.PolicyIDs, a.ApprovedBy, 0, 0, fmt.Sprintf("ai-action:%d", a.ID))
				if err == nil {
					status = "succeeded"
					output = fmt.Sprintf("检测任务已受理，run_id=%d；检测结果继续遵循后台自动化配置", id)
					problem = ""
				} else {
					status = "unknown"
					problem = "检测发起结果未知，请先核对现有检测任务，禁止自动重试"
				}
			}
		} else {
			problem = "检测配置已变化，未发起检测，请重新提议并审批"
		}
	}
	// UPDATE only: session reset/deletion cannot be undone by a late completion.
	finishCtx, finishCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer finishCancel()
	_, _ = s.db.ExecContext(finishCtx, `UPDATE v2_ai_action SET status=$2,output=$3,error=$4,completed_at=$5,updated_at=$5 WHERE id=$1 AND status='running'`, a.ID, status, output, problem, time.Now().Unix())
}

func (s *Service) monitorActionStillCurrent(ctx context.Context, ids []int64, expected string) bool {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false
	}
	defer tx.Rollback()
	_, actual, err := loadMonitorSnapshot(ctx, tx, ids)
	return err == nil && actual == expected
}
