package admin

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"forest/go-api/internal/cliententry"
)

type ClientEntryMonitorConfigService interface {
	ReadClientEntryMonitorConfig(context.Context, int64, int, int) (ClientEntryMonitorConfigPage, error)
}

type ClientEntryMonitorConfigItem struct {
	ClientEntryMonitorSaveItem
	Name       string `json:"name"`
	Configured bool   `json:"configured"`
}

type ClientEntryMonitorConfigPage struct {
	Revision  int64                          `json:"revision"`
	Items     []ClientEntryMonitorConfigItem `json:"items"`
	Total     int                            `json:"total"`
	Limit     int                            `json:"limit"`
	Offset    int                            `json:"offset"`
	Truncated bool                           `json:"truncated"`
}

// ReadClientEntryMonitorConfig reads configuration and current candidate target
// identities only. It never initializes schema, refreshes targets, or loads
// probe states. The returned revision must be supplied to a subsequent save.
func (s *DBService) ReadClientEntryMonitorConfig(ctx context.Context, policyID int64, limit, offset int) (ClientEntryMonitorConfigPage, error) {
	if s == nil || s.db == nil {
		return ClientEntryMonitorConfigPage{}, ErrUnavailable
	}
	if limit == 0 {
		limit = 20
	}
	if policyID < 0 || limit < 1 || limit > 20 || offset < 0 || offset > 1000000 {
		return ClientEntryMonitorConfigPage{}, clientEntryMonitorValidation("检测配置查询参数无效：policy_id不能为负，limit须为1至20，offset须为0至1000000")
	}
	revision, err := s.loadClientEntryMonitorRevision(ctx)
	if err != nil {
		return ClientEntryMonitorConfigPage{}, err
	}
	policies, err := s.listClientEntryUserPolicies(ctx, false)
	if err != nil {
		return ClientEntryMonitorConfigPage{}, err
	}
	nodes, err := s.loadClientEntryMonitorNodes(ctx, policies)
	if err != nil {
		return ClientEntryMonitorConfigPage{}, err
	}
	page := clientEntryMonitorConfigCandidates(buildClientEntryMonitorPolicies(policies, nodes), policyID, limit, offset)
	page.Revision = revision
	if len(page.Items) > 0 {
		if err := s.populateClientEntryMonitorConfig(ctx, page.Items); err != nil {
			return ClientEntryMonitorConfigPage{}, err
		}
	}
	current, err := s.loadClientEntryMonitorRevision(ctx)
	if err != nil {
		return ClientEntryMonitorConfigPage{}, err
	}
	if current != revision {
		return ClientEntryMonitorConfigPage{}, ErrClientEntryMonitorRevisionConflict
	}
	return page, nil
}

func clientEntryMonitorConfigCandidates(policies []ClientEntryMonitorPolicy, policyID int64, limit, offset int) ClientEntryMonitorConfigPage {
	page := ClientEntryMonitorConfigPage{Items: []ClientEntryMonitorConfigItem{}, Limit: limit, Offset: offset}
	for _, policy := range policies {
		if policyID > 0 && policy.ID != policyID || policy.Action == cliententry.ActionHide || len(policy.Targets) == 0 {
			continue
		}
		position := page.Total
		page.Total++
		if position < offset || len(page.Items) >= limit {
			continue
		}
		item := ClientEntryMonitorConfigItem{Name: policy.Name, ClientEntryMonitorSaveItem: ClientEntryMonitorSaveItem{
			PolicyID: policy.ID, CheckIntervalSec: defaultClientEntryMonitorIntervalSec, TCPTimeoutMS: defaultClientEntryMonitorTimeoutMS,
			FailureThreshold: defaultClientEntryMonitorFailureThreshold, SuccessThreshold: defaultClientEntryMonitorSuccessThreshold,
			Targets: make([]ClientEntryMonitorTargetSaveRequest, 0, len(policy.Targets)),
		}}
		for _, target := range policy.Targets {
			item.Targets = append(item.Targets, ClientEntryMonitorTargetSaveRequest{SourceKey: target.SourceKey, Port: target.SuggestedPort})
		}
		page.Items = append(page.Items, item)
	}
	page.Truncated = offset+len(page.Items) < page.Total
	return page
}

func (s *DBService) populateClientEntryMonitorConfig(ctx context.Context, items []ClientEntryMonitorConfigItem) error {
	args := make([]any, len(items))
	placeholders := make([]string, len(items))
	indexByPolicy := make(map[int64]int, len(items))
	for i, item := range items {
		args[i], placeholders[i], indexByPolicy[item.PolicyID] = item.PolicyID, fmt.Sprintf("$%d", i+1), i
	}
	rows, err := s.db.QueryContext(ctx, `SELECT m.policy_id, m.enabled, m.check_interval_sec, m.tcp_timeout_ms,
m.failure_threshold, m.success_threshold, t.source_key, t.port, t.auto_split_enabled
FROM v2_client_entry_monitor m
LEFT JOIN v2_client_entry_monitor_target t ON t.monitor_id=m.id
WHERE m.policy_id IN (`+strings.Join(placeholders, ",")+`)
ORDER BY m.policy_id,t.sort,t.id`, args...)
	if err != nil {
		return fmt.Errorf("read compact client entry monitor configuration: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var policyID, enabled, interval, timeout, failure, success int64
		var source sql.NullString
		var port, autoSplit sql.NullInt64
		if err := rows.Scan(&policyID, &enabled, &interval, &timeout, &failure, &success, &source, &port, &autoSplit); err != nil {
			return fmt.Errorf("scan compact client entry monitor configuration: %w", err)
		}
		index, ok := indexByPolicy[policyID]
		if !ok {
			continue
		}
		item := &items[index]
		item.Configured, item.Enabled = true, enabled == 1
		item.CheckIntervalSec, item.TCPTimeoutMS = interval, timeout
		item.FailureThreshold, item.SuccessThreshold = failure, success
		for j := range item.Targets {
			target := &item.Targets[j]
			if source.Valid && target.SourceKey == source.String {
				if port.Valid {
					target.Port = port.Int64
				}
				target.AutoSplitEnabled = autoSplit.Valid && autoSplit.Int64 == 1
			}
		}
	}
	return rows.Err()
}
