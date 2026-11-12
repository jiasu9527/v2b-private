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
	"time"
)

var ErrClientEntryGroupAuditInvalid = errors.New("入口分组审计参数无效：使用真实叶子组ID、有效时间窗口和分页参数")
var ErrClientEntryGroupAuditConflict = errors.New("入口分组审计结果已变化，请重新读取第一页；翻页须保留from、to和revision")

type ClientEntryGroupAuditRequest struct {
	GroupIDs            []int64 `json:"group_ids"`
	ReplacementGroupIDs []int64 `json:"replacement_group_ids"`
	From                int64   `json:"from"`
	To                  int64   `json:"to"`
	Current             int     `json:"current"`
	PageSize            int     `json:"page_size"`
	ExpectedRevision    string  `json:"expected_revision"`
}

// Membership coverage is intentionally separate from route effectiveness.
// Conditions, priority and node coverage are checked by retirement preview.
type ClientEntryGroupAuditCounts struct {
	AssignedUsers              int64 `json:"assigned_users"`
	ExistingUsers              int64 `json:"existing_users"`
	DeletedUsers               int64 `json:"deleted_users"`
	ReplacementCoveredUsers    int64 `json:"replacement_covered_users"`
	RemainingUsers             int64 `json:"remaining_users"`
	DeliveredUsers             int64 `json:"delivered_users"`
	ExistingDeliveredUsers     int64 `json:"existing_delivered_users"`
	DeletedDeliveredUsers      int64 `json:"deleted_delivered_users"`
	CurrentEntryDeliveredUsers int64 `json:"current_entry_delivered_users"`
	DeliveredRequests          int64 `json:"delivered_requests"`
	FirstDeliveryAt            int64 `json:"first_delivery_at"`
	LastDeliveryAt             int64 `json:"last_delivery_at"`
}

type ClientEntryGroupAuditItem struct {
	ClientEntryGroupAuditCounts
	Kind                        string `json:"kind"`
	GroupID                     int64  `json:"group_id"`
	PolicyID                    int64  `json:"policy_id"`
	Name                        string `json:"name"`
	PolicyName                  string `json:"policy_name"`
	EntryHost                   string `json:"entry_host"`
	ConfiguredHost              string `json:"configured_host"`
	CollectionID                int64  `json:"collection_id"`
	Enabled                     int64  `json:"enabled"`
	Action                      string `json:"action"`
	Sort                        int64  `json:"sort"`
	CreatedAt                   int64  `json:"created_at"`
	DistinctObservedSourceHosts int64  `json:"distinct_observed_source_hosts"`
	OtherSourceHostObserved     bool   `json:"other_source_host_observed"`
	HostHistoryStatus           string `json:"host_history_status"`
}

type ClientEntryGroupAuditReplacement struct {
	GroupID       int64  `json:"group_id"`
	PolicyID      int64  `json:"policy_id"`
	Name          string `json:"name"`
	Enabled       int64  `json:"enabled"`
	Action        string `json:"action"`
	ExistingUsers int64  `json:"existing_users"`
}

type ClientEntryGroupAuditWindow struct {
	From                     int64  `json:"from"`
	To                       int64  `json:"to"`
	KeepDays                 int64  `json:"keep_days"`
	RetentionFrom            int64  `json:"retention_from"`
	EarliestRecordAt         int64  `json:"earliest_record_at"`
	RequestedBeforeRetention bool   `json:"requested_before_retention"`
	CoverageCertain          bool   `json:"coverage_certain"`
	Note                     string `json:"note"`
}

type ClientEntryGroupAuditResult struct {
	Items             []ClientEntryGroupAuditItem        `json:"items"`
	Total             int                                `json:"total"`
	Current           int                                `json:"current"`
	PageSize          int                                `json:"page_size"`
	Revision          string                             `json:"revision"`
	Summary           ClientEntryGroupAuditCounts        `json:"summary"`
	ReplacementGroups []ClientEntryGroupAuditReplacement `json:"replacement_groups"`
	Window            ClientEntryGroupAuditWindow        `json:"window"`
	CoverageNote      string                             `json:"coverage_note"`
}

func normalizeClientEntryGroupAudit(req ClientEntryGroupAuditRequest, now int64) (ClientEntryGroupAuditRequest, error) {
	validateIDs := func(ids []int64) ([]int64, error) {
		if len(ids) > 100 {
			return nil, ErrClientEntryGroupAuditInvalid
		}
		out := append([]int64{}, ids...)
		sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
		for i, id := range out {
			if id < 1 || (i > 0 && id == out[i-1]) {
				return nil, ErrClientEntryGroupAuditInvalid
			}
		}
		return out, nil
	}
	var err error
	if req.GroupIDs, err = validateIDs(req.GroupIDs); err != nil {
		return req, err
	}
	if req.ReplacementGroupIDs, err = validateIDs(req.ReplacementGroupIDs); err != nil {
		return req, err
	}
	for _, old := range req.GroupIDs {
		for _, replacement := range req.ReplacementGroupIDs {
			if old == replacement {
				return req, ErrClientEntryGroupAuditInvalid
			}
		}
	}
	if req.Current == 0 {
		req.Current = 1
	}
	if req.PageSize == 0 {
		req.PageSize = 20
	}
	if req.Current < 1 || req.Current > 10000 || req.PageSize < 1 || req.PageSize > 50 {
		return req, ErrClientEntryGroupAuditInvalid
	}
	if req.ExpectedRevision != "" {
		hash, err := hex.DecodeString(req.ExpectedRevision)
		if err != nil || len(hash) != 32 || hex.EncodeToString(hash) != req.ExpectedRevision || req.From == 0 || req.To == 0 {
			return req, ErrClientEntryGroupAuditInvalid
		}
	} else if req.Current > 1 {
		return req, ErrClientEntryGroupAuditConflict
	}
	if req.From == 0 && req.To == 0 {
		req.From, req.To = now-7*86400, now
	}
	if req.From <= 0 || req.To <= req.From || req.To > now+1 || req.To-req.From > 365*86400 {
		return req, ErrClientEntryGroupAuditInvalid
	}
	return req, nil
}

// The query aggregates assignments and successful final responses independently,
// avoiding row multiplication when a subscription has several nodes in a group.
// It never infers a past delivery from the current group host or user membership.
const clientEntryGroupAuditSQL = `WITH leaves AS MATERIALIZED (
 SELECT g.id AS group_id,p.id AS policy_id,g.name,p.name AS policy_name,
 g.entry_host AS configured_host,COALESCE(c.entry_host,g.entry_host) AS entry_host,
 COALESCE(c.id,0) AS collection_id,p.enabled,p.action,COALESCE(g.global_sort,p.sort) AS sort,g.created_at,
 p.updated_at AS policy_updated_at,g.updated_at AS group_updated_at,md5(p.conditions) AS conditions_hash,
 COALESCE(c.version,0) AS collection_version
 FROM v2_client_entry_user_policy_split_group g JOIN v2_client_entry_user_policy p ON p.id=g.policy_id
 LEFT JOIN v2_client_entry_collection_member cm ON cm.split_group_id=g.id
 LEFT JOIN v2_client_entry_collection c ON c.id=cm.collection_id
 WHERE p.mode='split' AND NOT EXISTS(SELECT 1 FROM v2_client_entry_user_policy_split_group child WHERE child.parent_id=g.id)
), selected AS MATERIALIZED (
 SELECT * FROM leaves WHERE (cardinality($1::bigint[])=0 AND NOT(group_id=ANY($2::bigint[]))) OR group_id=ANY($1::bigint[])
), replacements AS MATERIALIZED (SELECT * FROM leaves WHERE group_id=ANY($2::bigint[])),
 replacement_users AS MATERIALIZED (
 SELECT DISTINCT a.user_id FROM v2_client_entry_user_policy_split_assignment a
 JOIN replacements g ON g.group_id=a.group_id AND g.policy_id=a.policy_id JOIN v2_user u ON u.id=a.user_id
), assignments AS MATERIALIZED (
 SELECT a.group_id,a.policy_id,a.user_id,u.id IS NOT NULL AS user_exists,r.user_id IS NOT NULL AS covered
 FROM v2_client_entry_user_policy_split_assignment a JOIN selected g ON g.group_id=a.group_id AND g.policy_id=a.policy_id
 LEFT JOIN v2_user u ON u.id=a.user_id LEFT JOIN replacement_users r ON r.user_id=a.user_id
), assignment_counts AS (
 SELECT group_id,COUNT(DISTINCT user_id) AS assigned_users,COUNT(DISTINCT user_id) FILTER(WHERE user_exists) AS existing_users,
 COUNT(DISTINCT user_id) FILTER(WHERE NOT user_exists) AS deleted_users,
 COUNT(DISTINCT user_id) FILTER(WHERE user_exists AND covered) AS replacement_covered_users,
 COUNT(DISTINCT user_id) FILTER(WHERE user_exists AND NOT covered) AS remaining_users
 FROM assignments GROUP BY group_id
), deliveries AS MATERIALIZED (
 SELECT DISTINCT n.group_id,r.id AS request_id,r.user_id,r.completed_at,u.id IS NOT NULL AS user_exists,
 LOWER(n.delivered_host)=LOWER(g.entry_host) OR (n.resolved AND LOWER(n.source_host)=LOWER(g.entry_host)) AS current_host,
 LOWER(CASE WHEN n.source_host<>'' THEN n.source_host WHEN NOT n.resolved THEN n.delivered_host ELSE '' END) AS observed_source_host,
 LOWER(g.entry_host) AS current_source_host
 FROM v2_entry_delivery_request r JOIN v2_entry_delivery_node n ON n.request_id=r.id
 JOIN selected g ON g.group_id=n.group_id AND g.policy_id=n.policy_id LEFT JOIN v2_user u ON u.id=r.user_id
 WHERE r.completed_at >= $3 AND r.completed_at < $4 AND r.response_complete AND r.coverage='complete'
 AND r.status BETWEEN 200 AND 299 AND r.write_error='' AND n.delivered_host<>'' AND n.port<>''
 AND n.server_type IN ('shadowsocks','vmess','vless','trojan','tuic','hysteria','hysteria2','anytls','v2node','juicity','mieru','socks','http')
 AND n.source='split'
), delivery_counts AS (
 SELECT group_id,COUNT(DISTINCT user_id) AS delivered_users,COUNT(DISTINCT user_id) FILTER(WHERE user_exists) AS existing_delivered_users,
 COUNT(DISTINCT user_id) FILTER(WHERE NOT user_exists) AS deleted_delivered_users,
 COUNT(DISTINCT user_id) FILTER(WHERE current_host) AS current_entry_delivered_users,COUNT(DISTINCT request_id) AS delivered_requests,
 MIN(completed_at) AS first_delivery_at,MAX(completed_at) AS last_delivery_at,
 COUNT(DISTINCT observed_source_host) FILTER(WHERE observed_source_host<>'') AS distinct_observed_source_hosts,
 COALESCE(bool_or(observed_source_host<>'' AND observed_source_host<>current_source_host),false) AS other_source_host_observed
 FROM deliveries GROUP BY group_id
), items AS (
 SELECT g.*,COALESCE(a.assigned_users,0) AS assigned_users,COALESCE(a.existing_users,0) AS existing_users,
 COALESCE(a.deleted_users,0) AS deleted_users,COALESCE(a.replacement_covered_users,0) AS replacement_covered_users,
 COALESCE(a.remaining_users,0) AS remaining_users,COALESCE(d.delivered_users,0) AS delivered_users,
 COALESCE(d.existing_delivered_users,0) AS existing_delivered_users,COALESCE(d.deleted_delivered_users,0) AS deleted_delivered_users,
 COALESCE(d.current_entry_delivered_users,0) AS current_entry_delivered_users,COALESCE(d.delivered_requests,0) AS delivered_requests,
 COALESCE(d.first_delivery_at,0) AS first_delivery_at,COALESCE(d.last_delivery_at,0) AS last_delivery_at,
 COALESCE(d.distinct_observed_source_hosts,0) AS distinct_observed_source_hosts,
 COALESCE(d.other_source_host_observed,false) AS other_source_host_observed
 FROM selected g LEFT JOIN assignment_counts a USING(group_id) LEFT JOIN delivery_counts d USING(group_id)
)
SELECT jsonb_build_object(
 'items',COALESCE((SELECT jsonb_agg(i ORDER BY i.sort,i.policy_id,i.group_id) FROM items i),'[]'::jsonb),
 'replacement_groups',COALESCE((SELECT jsonb_agg(jsonb_build_object('group_id',g.group_id,'policy_id',g.policy_id,'name',g.name,'enabled',g.enabled,'action',g.action,
 'existing_users',(SELECT COUNT(DISTINCT a.user_id) FROM v2_client_entry_user_policy_split_assignment a JOIN v2_user u ON u.id=a.user_id WHERE a.group_id=g.group_id AND a.policy_id=g.policy_id)) ORDER BY g.group_id) FROM replacements g),'[]'::jsonb),
 'assignment_digest',(SELECT md5(COALESCE(string_agg(group_id||':'||user_id||':'||user_exists||':'||covered,',' ORDER BY group_id,user_id),'')) FROM assignments),
 'replacement_digest',(SELECT md5(COALESCE(string_agg(user_id::text,',' ORDER BY user_id),'')) FROM replacement_users),
 'summary',(SELECT jsonb_build_object('assigned_users',COUNT(DISTINCT user_id),'existing_users',COUNT(DISTINCT user_id) FILTER(WHERE user_exists),
 'deleted_users',COUNT(DISTINCT user_id) FILTER(WHERE NOT user_exists),'replacement_covered_users',COUNT(DISTINCT user_id) FILTER(WHERE user_exists AND covered),
 'remaining_users',COUNT(DISTINCT user_id) FILTER(WHERE user_exists AND NOT covered)) FROM assignments)
 || (SELECT jsonb_build_object('delivered_users',COUNT(DISTINCT user_id),'existing_delivered_users',COUNT(DISTINCT user_id) FILTER(WHERE user_exists),
 'deleted_delivered_users',COUNT(DISTINCT user_id) FILTER(WHERE NOT user_exists),'current_entry_delivered_users',COUNT(DISTINCT user_id) FILTER(WHERE current_host),
 'delivered_requests',COUNT(DISTINCT request_id),'first_delivery_at',COALESCE(MIN(completed_at),0),'last_delivery_at',COALESCE(MAX(completed_at),0)) FROM deliveries),
 'keep_days',(SELECT keep_days FROM v2_entry_delivery_settings WHERE id=1),
 'earliest_record_at',(SELECT COALESCE(MIN(completed_at),0) FROM v2_entry_delivery_request)
)`

func (s *DBService) AuditClientEntryGroups(ctx context.Context, request ClientEntryGroupAuditRequest) (ClientEntryGroupAuditResult, error) {
	out := ClientEntryGroupAuditResult{}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	now := time.Now().Unix()
	req, err := normalizeClientEntryGroupAudit(request, now)
	if err != nil {
		return out, err
	}
	if s == nil || s.db == nil {
		return out, ErrUnavailable
	}
	// No ensureSchema or cache writes: this tool also works on a read-only
	// connection while an earlier operation is being reconciled.
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	var raw []byte
	if err = tx.QueryRowContext(ctx, clientEntryGroupAuditSQL, req.GroupIDs, req.ReplacementGroupIDs, req.From, req.To).Scan(&raw); err != nil {
		return out, fmt.Errorf("读取入口分组覆盖审计失败: %w", err)
	}
	var all struct {
		Items             []ClientEntryGroupAuditItem        `json:"items"`
		Summary           ClientEntryGroupAuditCounts        `json:"summary"`
		ReplacementGroups []ClientEntryGroupAuditReplacement `json:"replacement_groups"`
		KeepDays          int64                              `json:"keep_days"`
		EarliestRecordAt  int64                              `json:"earliest_record_at"`
	}
	if err = json.Unmarshal(raw, &all); err != nil {
		return out, err
	}
	if (len(req.GroupIDs) > 0 && len(all.Items) != len(req.GroupIDs)) || len(all.ReplacementGroups) != len(req.ReplacementGroupIDs) {
		return out, fmt.Errorf("%w：指定组不存在、已非叶子或不属于固定名单规则", ErrClientEntryGroupAuditInvalid)
	}
	if all.KeepDays < 1 || all.KeepDays > 365 {
		return out, errors.New("订阅下发保留设置不可用，不能推断日志覆盖范围")
	}
	input, _ := json.Marshal([]any{"entry-group-audit-v1", req.GroupIDs, req.ReplacementGroupIDs, req.From, req.To, json.RawMessage(raw)})
	digest := sha256.Sum256(input)
	revision := hex.EncodeToString(digest[:])
	if req.ExpectedRevision != "" && req.ExpectedRevision != revision {
		return out, ErrClientEntryGroupAuditConflict
	}
	if err = tx.Commit(); err != nil {
		return out, err
	}
	for i := range all.Items {
		item := &all.Items[i]
		item.Kind = "split_group"
		switch {
		case item.OtherSourceHostObserved || item.DistinctObservedSourceHosts > 1:
			item.HostHistoryStatus = "different_host_observed"
		case item.DistinctObservedSourceHosts > 0:
			item.HostHistoryStatus = "no_change_observed_in_window"
		default:
			item.HostHistoryStatus = "no_delivery_evidence"
		}
	}
	start := min((req.Current-1)*req.PageSize, len(all.Items))
	end := min(start+req.PageSize, len(all.Items))
	out = ClientEntryGroupAuditResult{
		Items: all.Items[start:end], Total: len(all.Items), Current: req.Current, PageSize: req.PageSize, Revision: revision,
		Summary: all.Summary, ReplacementGroups: all.ReplacementGroups,
		Window: ClientEntryGroupAuditWindow{From: req.From, To: req.To, KeepDays: all.KeepDays, RetentionFrom: now - all.KeepDays*86400,
			EarliestRecordAt: all.EarliestRecordAt, RequestedBeforeRetention: req.From < now-all.KeepDays*86400,
			CoverageCertain: false, Note: "人数仅来自现存的完整成功响应记录；保留期和最早记录不能证明期间无日志缺口，未记录不代表未下发。地址历史仅描述查询窗口，不能据此认定从未换IP。"},
		CoverageNote: "summary按所选全部叶子范围取用户并集，不能累加各页或各组人数。只统计split来源的叶子入口，不包含附加节点。replacement_covered_users仅为现存账号与替代组固定名单的交集，包含停用替代组，不证明节点、UA、优先级已生效或可以删除；清理前使用entry_groups.retire_preview校验。下发记录不证明客户端实际连接。",
	}
	return out, nil
}
