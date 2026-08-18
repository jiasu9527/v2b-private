package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"forest/go-api/internal/cliententry"
)

const (
	defaultClientEntryMonitorIntervalSec int64 = 30
	defaultClientEntryMonitorTimeoutMS   int64 = 3000
	clientEntryMonitorRunMaxPolicies           = 500
	clientEntryMonitorRefreshInterval          = 30 * time.Second
)

var ErrClientEntryMonitorRevisionConflict = errors.New("用户入口检测配置已被其他操作修改，请刷新后重试")

type ClientEntryMonitorCandidateTarget struct {
	SourceKey     string `json:"source_key"`
	Name          string `json:"name"`
	Host          string `json:"host"`
	SuggestedPort int64  `json:"suggested_port"`
}

type ClientEntryMonitorPolicy struct {
	ID        int64                               `json:"id"`
	Name      string                              `json:"name"`
	Mode      string                              `json:"mode"`
	Action    string                              `json:"action"`
	EntryHost string                              `json:"entry_host"`
	Enabled   bool                                `json:"enabled"`
	Targets   []ClientEntryMonitorCandidateTarget `json:"targets"`
}

type ClientEntryMonitorState struct {
	ProbeID            int64  `json:"probe_id"`
	ProbeName          string `json:"probe_name"`
	LastSuccess        *bool  `json:"last_success"`
	LastLatencyMS      *int64 `json:"last_latency_ms"`
	LastError          string `json:"last_error"`
	LastResolvedIP     string `json:"last_resolved_ip"`
	ConsecutiveSuccess int64  `json:"consecutive_success"`
	ConsecutiveFailure int64  `json:"consecutive_failure"`
	LastReportedAt     *int64 `json:"last_reported_at"`
	Stale              bool   `json:"stale"`
}

type ClientEntryMonitorTarget struct {
	ID               int64                     `json:"id"`
	SourceKey        string                    `json:"source_key"`
	Name             string                    `json:"name"`
	Host             string                    `json:"host"`
	Port             int64                     `json:"port"`
	Sort             int64                     `json:"sort"`
	AutoSplitEnabled bool                      `json:"auto_split_enabled"`
	States           []ClientEntryMonitorState `json:"states"`
}

type ClientEntryMonitorRecord struct {
	ID               int64                      `json:"id"`
	PolicyID         int64                      `json:"policy_id"`
	PolicyName       string                     `json:"policy_name"`
	Action           string                     `json:"action"`
	Enabled          bool                       `json:"enabled"`
	CheckIntervalSec int64                      `json:"check_interval_sec"`
	TCPTimeoutMS     int64                      `json:"tcp_timeout_ms"`
	Targets          []ClientEntryMonitorTarget `json:"targets"`
	CreatedAt        int64                      `json:"created_at"`
	UpdatedAt        int64                      `json:"updated_at"`
}

type ClientEntryMonitorOverview struct {
	Revision int64                      `json:"revision"`
	Items    []ClientEntryMonitorRecord `json:"items"`
	Policies []ClientEntryMonitorPolicy `json:"policies"`
	Probes   []DNSProbeRecord           `json:"probes"`
}

type ClientEntryMonitorTargetSaveRequest struct {
	SourceKey        string `json:"source_key"`
	Port             int64  `json:"port"`
	AutoSplitEnabled bool   `json:"auto_split_enabled"`
}

type ClientEntryMonitorSaveItem struct {
	PolicyID         int64                                 `json:"policy_id"`
	Enabled          bool                                  `json:"enabled"`
	CheckIntervalSec int64                                 `json:"check_interval_sec"`
	TCPTimeoutMS     int64                                 `json:"tcp_timeout_ms"`
	Targets          []ClientEntryMonitorTargetSaveRequest `json:"targets"`
}

type ClientEntryMonitorSaveRequest struct {
	Revision int64                        `json:"revision"`
	Items    []ClientEntryMonitorSaveItem `json:"items"`
}

type ClientEntryMonitorRunResult struct {
	ID         int64  `json:"id"`
	PolicyID   int64  `json:"policy_id"`
	PolicyName string `json:"policy_name"`
	TargetID   int64  `json:"target_id"`
	TargetName string `json:"target_name"`
	Host       string `json:"host"`
	Port       int64  `json:"port"`
	ProbeID    int64  `json:"probe_id"`
	ProbeName  string `json:"probe_name"`
	Success    bool   `json:"success"`
	LatencyMS  *int64 `json:"latency_ms"`
	Error      string `json:"error"`
	ResolvedIP string `json:"resolved_ip"`
	ReportedAt int64  `json:"reported_at"`
}

type ClientEntryMonitorRun struct {
	ID                      int64                         `json:"id"`
	PolicyIDs               []int64                       `json:"policy_ids"`
	Status                  string                        `json:"status"`
	ExpectedResults         int64                         `json:"expected_results"`
	ReceivedResults         int64                         `json:"received_results"`
	TotalResults            int64                         `json:"total_results"`
	ResultsTruncated        bool                          `json:"results_truncated"`
	ProgressMessageID       *int64                        `json:"progress_message_id"`
	ProgressReportedResults int64                         `json:"progress_reported_results"`
	ProgressReportedStatus  string                        `json:"progress_reported_status"`
	ProgressNextAttemptAt   int64                         `json:"progress_next_attempt_at"`
	ProgressLastError       string                        `json:"progress_last_error"`
	ExpectedPairs           []clientEntryMonitorRunPair   `json:"-"`
	ResultStatsLoaded       bool                          `json:"-"`
	SuccessfulResults       int64                         `json:"-"`
	FailedResults           int64                         `json:"-"`
	ResultTargetCount       int64                         `json:"-"`
	FailedTargetCount       int64                         `json:"-"`
	ResultProbeCount        int64                         `json:"-"`
	StartedAt               int64                         `json:"started_at"`
	CompletedAt             *int64                        `json:"completed_at"`
	CreatedAt               int64                         `json:"created_at"`
	Results                 []ClientEntryMonitorRunResult `json:"results"`
}

// ClientEntryMonitorRunOption is a policy group available to an operator for
// an on-demand check.
type ClientEntryMonitorRunOption struct {
	PolicyID    int64  `json:"policy_id"`
	Name        string `json:"name"`
	TargetCount int64  `json:"target_count"`
}

type ClientEntryMonitorAdminService interface {
	ListClientEntryMonitors(context.Context) (ClientEntryMonitorOverview, error)
	SaveClientEntryMonitors(context.Context, ClientEntryMonitorSaveRequest) (ClientEntryMonitorOverview, error)
	StartClientEntryMonitorRunForPolicies(context.Context, []int64, int64, int64) (int64, error)
	ListClientEntryMonitorRuns(context.Context, int64) ([]ClientEntryMonitorRun, error)
	ClearClientEntryMonitorRuns(context.Context) (int64, error)
}

type clientEntryMonitorNode struct {
	ServerType string
	ServerID   int64
	Name       string
	Host       string
	Port       int64
}

func (s *DBService) resolveClientEntryMonitorPolicies(ctx context.Context) ([]ClientEntryMonitorPolicy, error) {
	policies, err := s.ListClientEntryUserPolicies(ctx)
	if err != nil {
		return nil, err
	}
	nodes, err := s.loadClientEntryMonitorNodes(ctx, policies)
	if err != nil {
		return nil, err
	}
	result := make([]ClientEntryMonitorPolicy, 0, len(policies))
	for _, policy := range policies {
		item := ClientEntryMonitorPolicy{
			ID:        policy.ID,
			Name:      policy.Name,
			Mode:      policy.Mode,
			Action:    policy.Action,
			EntryHost: policy.EntryHost,
			Enabled:   policy.Enabled == 1,
			Targets:   make([]ClientEntryMonitorCandidateTarget, 0),
		}
		switch policy.Action {
		case cliententry.ActionOverride:
			if policy.Mode == ClientEntryUserPolicyModeSplit {
				for _, group := range policy.SplitGroups {
					host := strings.TrimSpace(group.EntryHost)
					if !group.IsLeaf || host == "" {
						continue
					}
					path := strings.TrimSpace(group.Path)
					if path == "" {
						path = strings.TrimSpace(group.Name)
					}
					if path == "" {
						path = strconv.FormatInt(group.ID, 10)
					}
					name := strings.TrimSpace(group.Name)
					if name == "" || name == path {
						name = strings.TrimSpace(policy.Name)
					}
					if name == "" {
						name = path + " 组"
					}
					item.Targets = append(item.Targets, ClientEntryMonitorCandidateTarget{
						SourceKey:     fmt.Sprintf("policy:%d:split-group:%d", policy.ID, group.ID),
						Name:          truncateClientEntryMonitorReportText(name+" · "+path+" 组入口", 255),
						Host:          host,
						SuggestedPort: 443,
					})
				}
				break
			}
			if host := strings.TrimSpace(policy.EntryHost); host != "" {
				item.Targets = append(item.Targets, ClientEntryMonitorCandidateTarget{
					SourceKey:     fmt.Sprintf("policy:%d", policy.ID),
					Name:          truncateClientEntryMonitorReportText(policy.Name+" · 独立入口", 255),
					Host:          host,
					SuggestedPort: 443,
				})
			}
		case cliententry.ActionOriginal:
			for _, member := range policy.Members {
				key := clientEntryMonitorNodeKey(member.ServerType, member.ServerID)
				node, ok := nodes[key]
				if !ok || strings.TrimSpace(node.Host) == "" {
					continue
				}
				port := node.Port
				if port <= 0 || port > 65535 {
					port = 443
				}
				item.Targets = append(item.Targets, ClientEntryMonitorCandidateTarget{
					SourceKey:     fmt.Sprintf("policy:%d:%s:%d", policy.ID, node.ServerType, node.ServerID),
					Name:          node.Name,
					Host:          node.Host,
					SuggestedPort: port,
				})
			}
		}
		result = append(result, item)
	}
	return result, nil
}

func (s *DBService) loadClientEntryMonitorNodes(ctx context.Context, policies []ClientEntryUserPolicyRecord) (map[string]clientEntryMonitorNode, error) {
	byType := make(map[string]map[int64]struct{})
	for _, policy := range policies {
		if policy.Action != cliententry.ActionOriginal {
			continue
		}
		for _, member := range policy.Members {
			serverType := strings.ToLower(strings.TrimSpace(member.ServerType))
			if _, ok := managedServerTypeTable[serverType]; !ok || member.ServerID <= 0 {
				continue
			}
			if byType[serverType] == nil {
				byType[serverType] = make(map[int64]struct{})
			}
			byType[serverType][member.ServerID] = struct{}{}
		}
	}
	result := make(map[string]clientEntryMonitorNode)
	for serverType, idsSet := range byType {
		ids := make([]int64, 0, len(idsSet))
		for id := range idsSet {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		placeholders := make([]string, len(ids))
		args := make([]any, len(ids))
		for index, id := range ids {
			placeholders[index] = fmt.Sprintf("$%d", index+1)
			args[index] = id
		}
		table := managedServerTypeTable[serverType]
		rows, err := s.db.QueryContext(ctx, `SELECT id, COALESCE(name, ''), COALESCE(host, ''), COALESCE(CAST(port AS text), '')
FROM `+quoteIdentifier(table)+`
WHERE id IN (`+strings.Join(placeholders, ",")+`)`, args...)
		if err != nil {
			return nil, fmt.Errorf("query %s nodes for client entry monitoring: %w", serverType, err)
		}
		for rows.Next() {
			var id int64
			var name, host, rawPort string
			if err := rows.Scan(&id, &name, &host, &rawPort); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("scan %s node for client entry monitoring: %w", serverType, err)
			}
			port, _ := strconv.ParseInt(strings.TrimSpace(rawPort), 10, 64)
			result[clientEntryMonitorNodeKey(serverType, id)] = clientEntryMonitorNode{
				ServerType: serverType,
				ServerID:   id,
				Name:       strings.TrimSpace(name),
				Host:       strings.TrimSpace(host),
				Port:       port,
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("iterate %s nodes for client entry monitoring: %w", serverType, err)
		}
		_ = rows.Close()
	}
	return result, nil
}

func clientEntryMonitorNodeKey(serverType string, serverID int64) string {
	return strings.ToLower(strings.TrimSpace(serverType)) + ":" + strconv.FormatInt(serverID, 10)
}

func (s *DBService) refreshClientEntryMonitorTargets(ctx context.Context) error {
	policies, err := s.resolveClientEntryMonitorPolicies(ctx)
	if err != nil {
		return err
	}
	policyByID := make(map[int64]ClientEntryMonitorPolicy, len(policies))
	for _, policy := range policies {
		policyByID[policy.ID] = policy
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin client entry monitor refresh: %w", err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id, policy_id FROM v2_client_entry_monitor ORDER BY id FOR UPDATE`)
	if err != nil {
		return fmt.Errorf("query client entry monitors for refresh: %w", err)
	}
	type monitorRef struct{ id, policyID int64 }
	monitors := make([]monitorRef, 0)
	for rows.Next() {
		var item monitorRef
		if err := rows.Scan(&item.id, &item.policyID); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan client entry monitor for refresh: %w", err)
		}
		monitors = append(monitors, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("iterate client entry monitors for refresh: %w", err)
	}
	_ = rows.Close()
	now := time.Now().Unix()
	for _, monitor := range monitors {
		policy, ok := policyByID[monitor.policyID]
		if !ok || policy.Action == cliententry.ActionHide || len(policy.Targets) == 0 {
			if _, err := tx.ExecContext(ctx, `UPDATE v2_client_entry_monitor SET enabled = 0, updated_at = $2 WHERE id = $1`, monitor.id, now); err != nil {
				return fmt.Errorf("disable unavailable client entry monitor: %w", err)
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM v2_client_entry_monitor_target WHERE monitor_id = $1`, monitor.id); err != nil {
				return fmt.Errorf("delete unavailable client entry monitor targets: %w", err)
			}
			continue
		}
		keys := make([]string, 0, len(policy.Targets))
		for index, target := range policy.Targets {
			keys = append(keys, target.SourceKey)
			if err := resetClientEntryMonitorTargetState(ctx, tx, monitor.id, target.SourceKey, target.Host, nil, nil, false); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO v2_client_entry_monitor_target AS current_target
(monitor_id, source_key, name, host, port, sort, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $7)
ON CONFLICT (monitor_id, source_key) DO UPDATE SET
name = EXCLUDED.name, host = EXCLUDED.host, sort = EXCLUDED.sort,
generation = CASE WHEN current_target.host IS DISTINCT FROM EXCLUDED.host
  THEN current_target.generation + 1 ELSE current_target.generation END,
updated_at = EXCLUDED.updated_at
WHERE current_target.name IS DISTINCT FROM EXCLUDED.name
   OR current_target.host IS DISTINCT FROM EXCLUDED.host
   OR current_target.sort IS DISTINCT FROM EXCLUDED.sort`,
				monitor.id, target.SourceKey, target.Name, target.Host, target.SuggestedPort, int64(index+1)*10, now); err != nil {
				return fmt.Errorf("refresh client entry monitor target: %w", err)
			}
		}
		if err := deleteClientEntryMonitorTargetsExcept(ctx, tx, monitor.id, keys); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit client entry monitor refresh: %w", err)
	}
	return nil
}

func (s *DBService) markClientEntryMonitorTargetsDirty() {
	if s == nil {
		return
	}
	s.clientEntryMonitorMu.Lock()
	s.clientEntryMonitorDirty = true
	s.clientEntryMonitorMu.Unlock()
}

func (s *DBService) refreshClientEntryMonitorTargetsIfDue(ctx context.Context) error {
	if s == nil || s.db == nil {
		return ErrUnavailable
	}
	now := time.Now()
	s.clientEntryMonitorMu.Lock()
	defer s.clientEntryMonitorMu.Unlock()
	elapsed := now.Sub(s.clientEntryMonitorAt)
	if !s.clientEntryMonitorDirty && !s.clientEntryMonitorAt.IsZero() && elapsed >= 0 && elapsed < clientEntryMonitorRefreshInterval {
		return nil
	}
	if err := s.refreshClientEntryMonitorTargets(ctx); err != nil {
		return err
	}
	s.clientEntryMonitorDirty = false
	s.clientEntryMonitorAt = now
	return nil
}

func deleteClientEntryMonitorTargetsExcept(ctx context.Context, tx *sql.Tx, monitorID int64, keys []string) error {
	if len(keys) == 0 {
		_, err := tx.ExecContext(ctx, `DELETE FROM v2_client_entry_monitor_target WHERE monitor_id = $1`, monitorID)
		return err
	}
	placeholders := make([]string, len(keys))
	args := make([]any, 0, len(keys)+1)
	args = append(args, monitorID)
	for index, key := range keys {
		placeholders[index] = fmt.Sprintf("$%d", index+2)
		args = append(args, key)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM v2_client_entry_monitor_target
WHERE monitor_id = $1 AND source_key NOT IN (`+strings.Join(placeholders, ",")+`)`, args...); err != nil {
		return fmt.Errorf("delete stale client entry monitor targets: %w", err)
	}
	return nil
}

func resetClientEntryMonitorTargetState(ctx context.Context, tx *sql.Tx, monitorID int64, sourceKey, host string,
	port *int64, autoSplitEnabled *bool, force bool,
) error {
	var targetID, currentPort, currentAutoSplitEnabled int64
	var currentHost string
	err := tx.QueryRowContext(ctx, `SELECT id, host, port, auto_split_enabled
FROM v2_client_entry_monitor_target
WHERE monitor_id = $1 AND source_key = $2
FOR UPDATE`, monitorID, sourceKey).Scan(&targetID, &currentHost, &currentPort, &currentAutoSplitEnabled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("lock client entry monitor target endpoint: %w", err)
	}
	changed := force || currentHost != host
	if port != nil && currentPort != *port {
		changed = true
	}
	// Toggling automatic splitting closes the previous incident.  Enabling it
	// must also start from two fresh failed samples; disabling it must cancel a
	// pending allocation immediately rather than waiting for the worker poll.
	if autoSplitEnabled != nil && boolToInt64(*autoSplitEnabled) != currentAutoSplitEnabled {
		changed = true
	}
	if !changed {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM v2_client_entry_monitor_state WHERE target_id = $1`, targetID); err != nil {
		return fmt.Errorf("reset changed client entry monitor target state: %w", err)
	}
	// Close the old incident in the same transaction as the state reset.  The
	// pending-operation unique index is keyed by policy/leaf rather than target
	// generation; leaving an older row pending could make the first confirmed
	// failure for the new generation hit ON CONFLICT and disappear before the
	// worker has a chance to cancel the stale row.
	now := time.Now().Unix()
	if _, err := tx.ExecContext(ctx, `UPDATE v2_client_entry_auto_split_operation
SET status = 'cancelled', last_error = '入口检测配置已变化，取消旧自动二分',
    completed_at = $2, updated_at = $2
WHERE target_id = $1 AND status = 'pending'`, targetID, now); err != nil {
		return fmt.Errorf("cancel stale client entry automatic split: %w", err)
	}
	return nil
}

func (s *DBService) ListClientEntryMonitors(ctx context.Context) (ClientEntryMonitorOverview, error) {
	if s == nil || s.db == nil {
		return ClientEntryMonitorOverview{}, ErrUnavailable
	}
	if err := s.ensureDNSFailoverSchema(ctx); err != nil {
		return ClientEntryMonitorOverview{}, err
	}
	if err := s.refreshClientEntryMonitorTargetsIfDue(ctx); err != nil {
		return ClientEntryMonitorOverview{}, err
	}
	revision, err := s.loadClientEntryMonitorRevision(ctx)
	if err != nil {
		return ClientEntryMonitorOverview{}, err
	}
	policies, err := s.resolveClientEntryMonitorPolicies(ctx)
	if err != nil {
		return ClientEntryMonitorOverview{}, err
	}
	probes, err := s.ListDNSProbes(ctx)
	if err != nil {
		return ClientEntryMonitorOverview{}, err
	}
	items, err := s.loadClientEntryMonitorRecords(ctx)
	if err != nil {
		return ClientEntryMonitorOverview{}, err
	}
	return ClientEntryMonitorOverview{Revision: revision, Items: items, Policies: policies, Probes: probes}, nil
}

func (s *DBService) loadClientEntryMonitorRevision(ctx context.Context) (int64, error) {
	var revision int64
	if err := s.db.QueryRowContext(ctx, `SELECT revision FROM v2_client_entry_monitor_config WHERE id = 1`).Scan(&revision); err != nil {
		return 0, fmt.Errorf("load client entry monitor config revision: %w", err)
	}
	return revision, nil
}

func (s *DBService) loadClientEntryMonitorRecords(ctx context.Context) ([]ClientEntryMonitorRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT m.id, m.policy_id, p.name, p.action, m.enabled,
m.check_interval_sec, m.tcp_timeout_ms, m.created_at, m.updated_at
FROM v2_client_entry_monitor m
JOIN v2_client_entry_user_policy p ON p.id = m.policy_id
ORDER BY p.sort ASC NULLS LAST, p.id ASC`)
	if err != nil {
		return nil, fmt.Errorf("query client entry monitors: %w", err)
	}
	items := make([]ClientEntryMonitorRecord, 0)
	indexByID := make(map[int64]int)
	now := time.Now().Unix()
	for rows.Next() {
		var item ClientEntryMonitorRecord
		var enabled int64
		if err := rows.Scan(&item.ID, &item.PolicyID, &item.PolicyName, &item.Action, &enabled,
			&item.CheckIntervalSec, &item.TCPTimeoutMS, &item.CreatedAt, &item.UpdatedAt); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan client entry monitor: %w", err)
		}
		item.Enabled = enabled == 1
		item.Targets = make([]ClientEntryMonitorTarget, 0)
		indexByID[item.ID] = len(items)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate client entry monitors: %w", err)
	}
	_ = rows.Close()
	if len(items) == 0 {
		return items, nil
	}
	rows, err = s.db.QueryContext(ctx, `SELECT t.id, t.monitor_id, t.source_key, t.name, t.host, t.port, t.sort, t.auto_split_enabled
FROM v2_client_entry_monitor_target t
ORDER BY t.monitor_id, t.sort, t.id`)
	if err != nil {
		return nil, fmt.Errorf("query client entry monitor targets: %w", err)
	}
	targetLocation := make(map[int64][2]int)
	for rows.Next() {
		var target ClientEntryMonitorTarget
		var monitorID int64
		var autoSplitEnabled int64
		if err := rows.Scan(&target.ID, &monitorID, &target.SourceKey, &target.Name, &target.Host, &target.Port, &target.Sort, &autoSplitEnabled); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan client entry monitor target: %w", err)
		}
		target.AutoSplitEnabled = autoSplitEnabled == 1
		itemIndex, ok := indexByID[monitorID]
		if !ok {
			continue
		}
		target.States = make([]ClientEntryMonitorState, 0)
		targetIndex := len(items[itemIndex].Targets)
		items[itemIndex].Targets = append(items[itemIndex].Targets, target)
		targetLocation[target.ID] = [2]int{itemIndex, targetIndex}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate client entry monitor targets: %w", err)
	}
	_ = rows.Close()
	rows, err = s.db.QueryContext(ctx, `SELECT s.target_id, s.probe_id, p.name, s.last_success,
s.last_latency_ms, s.last_error, s.last_resolved_ip, s.consecutive_success,
s.consecutive_failure, s.last_reported_at
	FROM v2_client_entry_monitor_state s
	JOIN v2_dns_probe p ON p.id = s.probe_id AND p.enabled = 1
	JOIN v2_client_entry_monitor_target target ON target.id = s.target_id
	ORDER BY s.target_id, s.probe_id`)
	if err != nil {
		return nil, fmt.Errorf("query client entry monitor states: %w", err)
	}
	for rows.Next() {
		var targetID int64
		var state ClientEntryMonitorState
		var success, latency, reported sql.NullInt64
		if err := rows.Scan(&targetID, &state.ProbeID, &state.ProbeName, &success, &latency,
			&state.LastError, &state.LastResolvedIP, &state.ConsecutiveSuccess,
			&state.ConsecutiveFailure, &reported); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan client entry monitor state: %w", err)
		}
		if success.Valid {
			value := success.Int64 == 1
			state.LastSuccess = &value
		}
		if latency.Valid {
			value := latency.Int64
			state.LastLatencyMS = &value
		}
		if reported.Valid {
			value := reported.Int64
			state.LastReportedAt = &value
		}
		if location, ok := targetLocation[targetID]; ok {
			monitor := items[location[0]]
			state.Stale = !clientEntryMonitorStateFresh(reported, now, monitor.CheckIntervalSec, monitor.TCPTimeoutMS)
			items[location[0]].Targets[location[1]].States = append(items[location[0]].Targets[location[1]].States, state)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate client entry monitor states: %w", err)
	}
	_ = rows.Close()
	return items, nil
}

func clientEntryMonitorStateFresh(lastReportedAt sql.NullInt64, now, checkIntervalSec, tcpTimeoutMS int64) bool {
	return dnsFailoverProbeStateFresh(lastReportedAt, now, checkIntervalSec, tcpTimeoutMS, int64(^uint64(0)>>1))
}

func (s *DBService) SaveClientEntryMonitors(ctx context.Context, request ClientEntryMonitorSaveRequest) (ClientEntryMonitorOverview, error) {
	if s == nil || s.db == nil {
		return ClientEntryMonitorOverview{}, ErrUnavailable
	}
	if err := s.ensureDNSFailoverSchema(ctx); err != nil {
		return ClientEntryMonitorOverview{}, err
	}
	policies, err := s.resolveClientEntryMonitorPolicies(ctx)
	if err != nil {
		return ClientEntryMonitorOverview{}, err
	}
	policyByID := make(map[int64]ClientEntryMonitorPolicy, len(policies))
	for _, policy := range policies {
		policyByID[policy.ID] = policy
	}
	if len(request.Items) > clientEntryMonitorRunMaxPolicies {
		return ClientEntryMonitorOverview{}, errors.New("入口检测规则数量过多")
	}
	seenPolicies := make(map[int64]struct{}, len(request.Items))
	requiresEnabledProbe := false
	for index := range request.Items {
		item := &request.Items[index]
		if _, exists := seenPolicies[item.PolicyID]; exists {
			return ClientEntryMonitorOverview{}, errors.New("入口检测规则重复")
		}
		seenPolicies[item.PolicyID] = struct{}{}
		policy, ok := policyByID[item.PolicyID]
		if !ok || policy.Action == cliententry.ActionHide || len(policy.Targets) == 0 {
			return ClientEntryMonitorOverview{}, errors.New("所选用户入口规则不可检测")
		}
		if item.CheckIntervalSec == 0 {
			item.CheckIntervalSec = defaultClientEntryMonitorIntervalSec
		}
		if item.TCPTimeoutMS == 0 {
			item.TCPTimeoutMS = defaultClientEntryMonitorTimeoutMS
		}
		if item.CheckIntervalSec < 5 || item.CheckIntervalSec > 3600 {
			return ClientEntryMonitorOverview{}, errors.New("检测间隔必须在 5 到 3600 秒之间")
		}
		if item.TCPTimeoutMS < 100 || item.TCPTimeoutMS > 60000 {
			return ClientEntryMonitorOverview{}, errors.New("TCP 超时必须在 100 到 60000 毫秒之间")
		}
		requiresEnabledProbe = requiresEnabledProbe || item.Enabled
		candidateByKey := make(map[string]ClientEntryMonitorCandidateTarget, len(policy.Targets))
		for _, target := range policy.Targets {
			candidateByKey[target.SourceKey] = target
		}
		portByKey := make(map[string]int64, len(item.Targets))
		autoSplitByKey := make(map[string]bool, len(item.Targets))
		for _, target := range item.Targets {
			if _, ok := candidateByKey[target.SourceKey]; !ok {
				return ClientEntryMonitorOverview{}, errors.New("入口检测地址已变化，请刷新后重试")
			}
			if target.Port < 1 || target.Port > 65535 {
				return ClientEntryMonitorOverview{}, errors.New("TCP 端口必须在 1 到 65535 之间")
			}
			if target.AutoSplitEnabled {
				policyID, _, ok := parseClientEntryMonitorSplitGroupSourceKey(target.SourceKey)
				if !ok || policyID != item.PolicyID || policy.Mode != ClientEntryUserPolicyModeSplit {
					return ClientEntryMonitorOverview{}, errors.New("自动二分容灾只能用于固定二分叶子规则")
				}
			}
			portByKey[target.SourceKey] = target.Port
			autoSplitByKey[target.SourceKey] = target.AutoSplitEnabled
		}
		item.Targets = make([]ClientEntryMonitorTargetSaveRequest, 0, len(policy.Targets))
		for _, target := range policy.Targets {
			port := portByKey[target.SourceKey]
			if port == 0 {
				port = target.SuggestedPort
			}
			item.Targets = append(item.Targets, ClientEntryMonitorTargetSaveRequest{
				SourceKey: target.SourceKey, Port: port, AutoSplitEnabled: autoSplitByKey[target.SourceKey],
			})
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ClientEntryMonitorOverview{}, fmt.Errorf("begin saving client entry monitors: %w", err)
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	if err := lockClientEntryMonitorRevision(ctx, tx, request.Revision); err != nil {
		return ClientEntryMonitorOverview{}, err
	}
	if requiresEnabledProbe {
		if err := requireEnabledClientEntryMonitorProbe(ctx, tx); err != nil {
			return ClientEntryMonitorOverview{}, err
		}
	}
	for _, item := range request.Items {
		policy := policyByID[item.PolicyID]
		var previousMonitorEnabled int64
		previousMonitorFound := true
		if err := tx.QueryRowContext(ctx, `SELECT enabled FROM v2_client_entry_monitor
WHERE policy_id = $1 FOR UPDATE`, item.PolicyID).Scan(&previousMonitorEnabled); err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				return ClientEntryMonitorOverview{}, fmt.Errorf("lock client entry monitor before save: %w", err)
			}
			previousMonitorFound = false
		}
		monitorReenabled := previousMonitorFound && previousMonitorEnabled != 1 && item.Enabled
		var monitorID int64
		if err := tx.QueryRowContext(ctx, `INSERT INTO v2_client_entry_monitor
(policy_id, enabled, check_interval_sec, tcp_timeout_ms, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $5)
ON CONFLICT (policy_id) DO UPDATE SET enabled = EXCLUDED.enabled,
check_interval_sec = EXCLUDED.check_interval_sec, tcp_timeout_ms = EXCLUDED.tcp_timeout_ms,
updated_at = EXCLUDED.updated_at
RETURNING id`, item.PolicyID, boolToInt64(item.Enabled), item.CheckIntervalSec, item.TCPTimeoutMS, now).Scan(&monitorID); err != nil {
			return ClientEntryMonitorOverview{}, fmt.Errorf("save client entry monitor: %w", err)
		}
		portByKey := make(map[string]int64, len(item.Targets))
		autoSplitByKey := make(map[string]bool, len(item.Targets))
		for _, target := range item.Targets {
			portByKey[target.SourceKey] = target.Port
			autoSplitByKey[target.SourceKey] = target.AutoSplitEnabled
		}
		keys := make([]string, 0, len(policy.Targets))
		for index, target := range policy.Targets {
			keys = append(keys, target.SourceKey)
			port := portByKey[target.SourceKey]
			autoSplitEnabled := autoSplitByKey[target.SourceKey]
			if err := resetClientEntryMonitorTargetState(ctx, tx, monitorID, target.SourceKey, target.Host,
				&port, &autoSplitEnabled, monitorReenabled); err != nil {
				return ClientEntryMonitorOverview{}, err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO v2_client_entry_monitor_target AS current_target
(monitor_id, source_key, name, host, port, sort, auto_split_enabled, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8)
ON CONFLICT (monitor_id, source_key) DO UPDATE SET name = EXCLUDED.name,
host = EXCLUDED.host, port = EXCLUDED.port, sort = EXCLUDED.sort,
auto_split_enabled = EXCLUDED.auto_split_enabled,
generation = CASE WHEN current_target.host IS DISTINCT FROM EXCLUDED.host
    OR current_target.port IS DISTINCT FROM EXCLUDED.port
	OR (current_target.auto_split_enabled = 0 AND EXCLUDED.auto_split_enabled = 1)
	OR $9 = 1
  THEN current_target.generation + 1 ELSE current_target.generation END,
updated_at = EXCLUDED.updated_at
WHERE current_target.name IS DISTINCT FROM EXCLUDED.name
   OR current_target.host IS DISTINCT FROM EXCLUDED.host
   OR current_target.port IS DISTINCT FROM EXCLUDED.port
   OR current_target.sort IS DISTINCT FROM EXCLUDED.sort
   OR current_target.auto_split_enabled IS DISTINCT FROM EXCLUDED.auto_split_enabled
	OR $9 = 1`,
				monitorID, target.SourceKey, target.Name, target.Host, portByKey[target.SourceKey], int64(index+1)*10,
				boolToInt64(autoSplitByKey[target.SourceKey]), now, boolToInt64(monitorReenabled)); err != nil {
				return ClientEntryMonitorOverview{}, fmt.Errorf("save client entry monitor target: %w", err)
			}
		}
		if err := deleteClientEntryMonitorTargetsExcept(ctx, tx, monitorID, keys); err != nil {
			return ClientEntryMonitorOverview{}, err
		}
	}
	if len(seenPolicies) == 0 {
		if _, err := tx.ExecContext(ctx, `DELETE FROM v2_client_entry_monitor`); err != nil {
			return ClientEntryMonitorOverview{}, fmt.Errorf("delete unselected client entry monitors: %w", err)
		}
	} else {
		ids := make([]int64, 0, len(seenPolicies))
		for policyID := range seenPolicies {
			ids = append(ids, policyID)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		placeholders := make([]string, len(ids))
		args := make([]any, len(ids))
		for index, id := range ids {
			placeholders[index] = fmt.Sprintf("$%d", index+1)
			args[index] = id
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM v2_client_entry_monitor WHERE policy_id NOT IN (`+strings.Join(placeholders, ",")+`)`, args...); err != nil {
			return ClientEntryMonitorOverview{}, fmt.Errorf("delete unselected client entry monitors: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE v2_client_entry_monitor_config
SET revision = revision + 1, updated_at = $1 WHERE id = 1`, now); err != nil {
		return ClientEntryMonitorOverview{}, fmt.Errorf("advance client entry monitor config revision: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return ClientEntryMonitorOverview{}, fmt.Errorf("commit client entry monitor settings: %w", err)
	}
	return s.ListClientEntryMonitors(ctx)
}

func requireEnabledClientEntryMonitorProbe(ctx context.Context, tx *sql.Tx) error {
	var probeID int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM v2_dns_probe
WHERE enabled = 1
ORDER BY id
LIMIT 1
FOR SHARE`).Scan(&probeID)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("启用入口检测前请先启用至少一个探针")
	}
	if err != nil {
		return fmt.Errorf("query enabled client entry monitor probe: %w", err)
	}
	return nil
}

func lockClientEntryMonitorRevision(ctx context.Context, tx *sql.Tx, expected int64) error {
	var current int64
	if err := tx.QueryRowContext(ctx, `SELECT revision FROM v2_client_entry_monitor_config WHERE id = 1 FOR UPDATE`).Scan(&current); err != nil {
		return fmt.Errorf("lock client entry monitor config revision: %w", err)
	}
	if expected != current {
		return ErrClientEntryMonitorRevisionConflict
	}
	return nil
}

func decodeClientEntryMonitorPolicyIDs(raw []byte) []int64 {
	result := make([]int64, 0)
	if len(raw) == 0 {
		return result
	}
	_ = json.Unmarshal(raw, &result)
	return result
}
