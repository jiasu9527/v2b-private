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
	"sync"
	"time"
	"unicode/utf8"

	"forest/go-api/internal/cliententry"
	"forest/go-api/internal/entryevidence"
	platformpostgres "forest/go-api/internal/platform/postgres"
)

type evidenceSchemaState struct {
	once sync.Once
	err  error
}

var evidenceSchemaStates sync.Map

func (s *DBService) prepareEntryEvidence(ctx context.Context, actor int64, adminOnly bool) error {
	if s == nil || s.db == nil {
		return ErrUnavailable
	}
	if err := authorizeEntryEvidence(ctx, s.db, actor, adminOnly); err != nil {
		return err
	}
	state, _ := evidenceSchemaStates.LoadOrStore(s.db, &evidenceSchemaState{})
	v := state.(*evidenceSchemaState)
	v.once.Do(func() {
		if v.err = s.ensureClientEntrySchema(ctx); v.err != nil {
			return
		}
		if v.err = s.ensureDNSFailoverSchema(ctx); v.err != nil {
			return
		}
		if v.err = entryevidence.EnsureSchema(ctx, s.db); v.err != nil {
			return
		}
		v.err = platformpostgres.EnsureEntryEvidenceCasesSchema(ctx, s.db)
	})
	if v.err != nil {
		evidenceSchemaStates.Delete(s.db)
	}
	return v.err
}

type evidenceQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func authorizeEntryEvidence(ctx context.Context, db evidenceQueryer, actor int64, adminOnly bool) error {
	if actor <= 0 {
		return ErrEntryEvidenceForbidden
	}
	var admin, staff int64
	err := db.QueryRowContext(ctx, `SELECT is_admin,is_staff FROM v2_user WHERE id=$1 AND banned=0`, actor).Scan(&admin, &staff)
	if errors.Is(err, sql.ErrNoRows) || err == nil && (admin != 1 && (adminOnly || staff != 1)) {
		return ErrEntryEvidenceForbidden
	}
	return err
}
func normalizeEvidencePage(current, size int) (int, int) {
	if current < 1 {
		current = 1
	}
	if size < 1 {
		size = 20
	}
	if size > 100 {
		size = 100
	}
	if current > 1000000 {
		current = 1000000
	}
	return current, size
}
func normalizeEvidenceQuery(q EntryEvidenceQueryRequest) (EntryEvidenceQueryRequest, error) {
	if q.Kind != "policy" && q.Kind != "split_group" || q.TargetID <= 0 || q.From <= 0 || q.To <= q.From || q.To > time.Now().Unix()+60 || q.To-q.From > 90*86400 || q.Port < 1 || q.Port > 65535 || len(q.UA) > 1000 || len(q.Account) > 254 {
		return q, ErrEntryEvidenceInvalid
	}
	host, err := cliententry.NormalizeHost(q.Host)
	if err != nil {
		return q, ErrEntryEvidenceInvalid
	}
	q.Host = strings.ToLower(host)
	q.UA = strings.TrimSpace(q.UA)
	q.Account = strings.ToLower(strings.TrimSpace(q.Account))
	q.Current, q.PageSize = normalizeEvidencePage(q.Current, q.PageSize)
	return q, nil
}
func evidenceQueryHash(q EntryEvidenceQueryRequest, rule AIEntryRuleSnapshot, candidates []EntryEvidenceCandidate) string {
	q.Current = 0
	q.PageSize = 0
	return aiEntrySplitHash([]any{q, rule.Revision, candidates})
}
func evidenceIDsHash(ids []int64) string { return aiEntrySplitHash(ids) }
func evidenceTopologyRevision(ctx context.Context, tx *sql.Tx) (string, error) {
	var digest string
	err := tx.QueryRowContext(ctx, `SELECT md5(jsonb_build_array(
 (SELECT COALESCE(jsonb_agg(to_jsonb(p) ORDER BY id),'[]'::jsonb) FROM v2_client_entry_user_policy p),
 (SELECT COALESCE(jsonb_agg(to_jsonb(m) ORDER BY id),'[]'::jsonb) FROM v2_client_entry_user_policy_member m),
 (SELECT COALESCE(jsonb_agg(to_jsonb(g) ORDER BY id),'[]'::jsonb) FROM v2_client_entry_user_policy_split_group g),
 (SELECT COALESCE(jsonb_agg(to_jsonb(c) ORDER BY id),'[]'::jsonb) FROM v2_client_entry_collection c),
 (SELECT COALESCE(jsonb_agg(to_jsonb(m) ORDER BY collection_id,policy_id,split_group_id),'[]'::jsonb) FROM v2_client_entry_collection_member m)
 )::TEXT)`).Scan(&digest)
	return digest, err
}
func evidenceRuleSafety(rule AIEntryRuleSnapshot, extra string) error {
	if rule.Enabled != 1 || rule.Action != cliententry.ActionOverride || rule.EntryHost == "" || len(rule.Members) == 0 || rule.CollectionID != nil {
		return fmt.Errorf("%w：仅支持已启用、覆盖入口、有生效节点且不属于合集的规则或叶子", ErrEntryEvidenceInvalid)
	}
	var links []string
	if json.Unmarshal([]byte(extra), &links) != nil || len(links) > 0 {
		return fmt.Errorf("%w：包含额外下发节点的目标暂不支持安全子集二分", ErrEntryEvidenceInvalid)
	}
	if rule.Sort <= 0 || rule.Sort > 2147483647-3*clientEntryRuleSortStep {
		return fmt.Errorf("%w：目标优先级不可安全扩展", ErrEntryEvidenceInvalid)
	}
	if rule.Kind == "policy" {
		n := 0
		for _, c := range rule.Conditions {
			if c.Field == "ua" {
				continue
			}
			if c.Field != "user_id" || c.Operator != "between" {
				return fmt.Errorf("%w：标准规则仅支持一个用户ID范围及UA条件", ErrEntryEvidenceInvalid)
			}
			n++
		}
		if n != 1 {
			return fmt.Errorf("%w：标准规则需要一个用户ID范围", ErrEntryEvidenceInvalid)
		}
	}
	return nil
}
func evidenceSourceUsers(ctx context.Context, tx *sql.Tx, rule AIEntryRuleSnapshot) ([]int64, error) {
	var rows *sql.Rows
	var err error
	if rule.Kind == "policy" {
		var lo, hi int64
		for _, c := range rule.Conditions {
			if c.Field == "user_id" {
				lo, err = clientEntryRuleRawInt64(c.Min)
				if err != nil {
					return nil, evidenceUserLockError(err)
				}
				hi, err = clientEntryRuleRawInt64(c.Max)
				if err != nil {
					return nil, evidenceUserLockError(err)
				}
			}
		}
		rows, err = tx.QueryContext(ctx, `SELECT id FROM v2_user WHERE id BETWEEN $1 AND $2 ORDER BY id FOR SHARE NOWAIT`, lo, hi)
	} else {
		rows, err = tx.QueryContext(ctx, `SELECT user_id FROM v2_client_entry_user_policy_split_assignment WHERE policy_id=$1 AND group_id=$2 ORDER BY user_id FOR UPDATE`, rule.PolicyID, rule.TargetID)
	}
	if err != nil {
		return nil, evidenceUserLockError(err)
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			return nil, evidenceUserLockError(err)
		}
		ids = append(ids, id)
		if len(ids) > 1000000 {
			return nil, fmt.Errorf("%w：目标用户过多，请缩小原规则", ErrEntryEvidenceInvalid)
		}
	}
	return ids, evidenceUserLockError(rows.Err())
}
func loadEvidenceCandidates(ctx context.Context, tx *sql.Tx, q EntryEvidenceQueryRequest, rule AIEntryRuleSnapshot) ([]EntryEvidenceCandidate, error) {
	groupID := int64(0)
	if q.Kind == "split_group" {
		groupID = q.TargetID
	}
	rows, err := tx.QueryContext(ctx, `WITH matched AS (
 SELECT r.user_id,r.email_at_event,r.completed_at,r.id,r.ua,n.server_type,n.server_id,n.rule_revision,n.delivered_host,n.port,n.ordinal,
 ROW_NUMBER() OVER(PARTITION BY r.user_id ORDER BY r.completed_at DESC,r.id,n.ordinal) AS evidence_rank
 FROM v2_entry_delivery_request r JOIN v2_entry_delivery_node n ON n.request_id=r.id
 WHERE r.response_complete=TRUE AND r.coverage='complete' AND r.status BETWEEN 200 AND 299
 AND n.policy_id=$1 AND ($2::BIGINT=0 OR n.group_id=$2)
 AND lower(n.delivered_host)=$3 AND n.port=$4 AND r.completed_at >= $5 AND r.completed_at < $6
 AND ($7='' OR strpos(lower(r.ua),lower($7))>0)
 AND ($8='' OR lower(r.email_at_event)=$8 OR r.user_id::TEXT=$8)
 ) SELECT user_id,(array_agg(email_at_event ORDER BY completed_at DESC,id DESC))[1],MIN(completed_at),MAX(completed_at),COUNT(DISTINCT id),
 jsonb_agg(DISTINCT jsonb_build_object('server_type',server_type,'server_id',server_id)),jsonb_agg(DISTINCT ua),COUNT(*),
 jsonb_agg(jsonb_build_object('request_id',id,'completed_at',completed_at,'ua',ua,'rule_revision',rule_revision,'server_type',server_type,'server_id',server_id,'host',delivered_host,'port',port) ORDER BY evidence_rank) FILTER(WHERE evidence_rank<=20)
 FROM matched GROUP BY user_id ORDER BY user_id LIMIT $9`, rule.PolicyID, groupID, q.Host, strconv.FormatInt(q.Port, 10), q.From, q.To, q.UA, q.Account, entryEvidenceMaxCandidates+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EntryEvidenceCandidate{}
	for rows.Next() {
		var c EntryEvidenceCandidate
		var nodes, uas, refs []byte
		if err = rows.Scan(&c.UserID, &c.Email, &c.FirstCompletedAt, &c.LastCompletedAt, &c.RequestCount, &nodes, &uas, &c.EvidenceTotal, &refs); err != nil {
			return nil, err
		}
		if json.Unmarshal(nodes, &c.Nodes) != nil || json.Unmarshal(uas, &c.UAs) != nil || json.Unmarshal(refs, &c.Evidence) != nil {
			return nil, ErrEntryEvidenceInvalid
		}
		sort.Slice(c.Nodes, func(i, j int) bool {
			if c.Nodes[i].ServerType != c.Nodes[j].ServerType {
				return c.Nodes[i].ServerType < c.Nodes[j].ServerType
			}
			return c.Nodes[i].ServerID < c.Nodes[j].ServerID
		})
		sort.Strings(c.UAs)
		c.EvidenceTruncated = c.EvidenceTotal > int64(len(c.Evidence))
		out = append(out, c)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(out) > entryEvidenceMaxCandidates {
		return nil, fmt.Errorf("%w：候选超过10000人，请收窄时间或账号条件", ErrEntryEvidenceInvalid)
	}
	return out, nil
}
func evidenceCandidateCoverage(rule AIEntryRuleSnapshot, source []int64, items []EntryEvidenceCandidate) error {
	ids := map[int64]bool{}
	for _, id := range source {
		ids[id] = true
	}
	for _, c := range items {
		if !ids[c.UserID] {
			return fmt.Errorf("%w：候选用户%d已不属于当前来源名单", ErrEntryEvidenceConflict, c.UserID)
		}
		if len(c.UAs) == 0 || len(c.UAs) > 64 {
			return fmt.Errorf("%w：用户%d请求UA数量无法完整核验", ErrEntryEvidenceInvalid, c.UserID)
		}
		for _, m := range rule.Members {
			found := false
			for _, n := range c.Nodes {
				if n == m {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("%w：用户%d的已完成下发证据未覆盖来源全部生效节点", ErrEntryEvidenceConflict, c.UserID)
			}
		}
	}
	return nil
}

// Check the actual ordered policies with each evidenced UA. Load shared rules,
// candidate user state and split assignments in three queries, plus one conservative
// support-override guard, rather than one
// full policy scan per candidate. The caller holds the topology transaction lock;
// user and assignment row locks preserve the validation until execution commits.
func evidenceCurrentCoverage(ctx context.Context, tx *sql.Tx, rule AIEntryRuleSnapshot, items []EntryEvidenceCandidate) error {
	return evidenceCurrentCoverageBatched(ctx, tx, rule, items)
}

func (s *DBService) evidencePreviewTx(ctx context.Context, tx *sql.Tx, q EntryEvidenceQueryRequest) (EntryEvidenceCandidatePreview, []EntryEvidenceCandidate, []int64, error) {
	out := EntryEvidenceCandidatePreview{Query: q, Current: q.Current, PageSize: q.PageSize, Items: []EntryEvidenceCandidate{}}
	rule, err := LoadAIEntryRule(ctx, tx, q.Kind, q.TargetID)
	if err != nil {
		return out, nil, nil, err
	}
	out.Target = rule
	var extra string
	if err = tx.QueryRowContext(ctx, `SELECT extra_nodes FROM v2_client_entry_user_policy WHERE id=$1`, rule.PolicyID).Scan(&extra); err != nil {
		return out, nil, nil, err
	}
	items, err := loadEvidenceCandidates(ctx, tx, q, rule)
	if err != nil {
		return out, nil, nil, err
	}
	out.Total = int64(len(items))
	out.CandidatesHash = evidenceQueryHash(q, rule, items)
	start := (q.Current - 1) * q.PageSize
	if start < len(items) {
		end := start + q.PageSize
		if end > len(items) {
			end = len(items)
		}
		out.Items = items[start:end]
	}
	if err = evidenceRuleSafety(rule, extra); err != nil {
		out.UnsupportedReason = err.Error()
		return out, items, nil, nil
	}
	ids, err := evidenceSourceUsers(ctx, tx, rule)
	if err != nil {
		return out, nil, nil, err
	}
	out.SourceUserCount = int64(len(ids))
	if err = evidenceCandidateCoverage(rule, ids, items); err == nil {
		err = evidenceCurrentCoverage(ctx, tx, rule, items)
	}
	if err != nil {
		out.UnsupportedReason = err.Error()
	} else if len(items) == 0 {
		out.UnsupportedReason = "没有可证实已完成下发且当前仍覆盖的用户"
	} else {
		out.FreezeAllowed = true
		out.Supported = len(items) >= 2
		if !out.Supported {
			out.UnsupportedReason = "单人证据可以冻结留档；二分至少需要2名选中用户"
		}
	}
	return out, items, ids, nil
}
func (s *DBService) PreviewEntryEvidenceCandidates(ctx context.Context, actor int64, req EntryEvidenceQueryRequest) (EntryEvidenceCandidatePreview, error) {
	var empty EntryEvidenceCandidatePreview
	if err := s.prepareEntryEvidence(ctx, actor, false); err != nil {
		return empty, err
	}
	q, err := normalizeEvidenceQuery(req)
	if err != nil {
		return empty, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	out, _, _, err := s.evidencePreviewTx(ctx, tx, q)
	if err != nil {
		return out, err
	}
	return out, tx.Commit()
}
func evidenceSelect(items []EntryEvidenceCandidate, selected []int64) ([]EntryEvidenceCandidate, int64, error) {
	selectAll := selected == nil
	set := map[int64]bool{}
	for _, id := range selected {
		if id <= 0 || set[id] {
			return nil, 0, ErrEntryEvidenceInvalid
		}
		set[id] = true
	}
	count := int64(0)
	out := append([]EntryEvidenceCandidate{}, items...)
	for i := range out {
		out[i].Selected = selectAll || set[out[i].UserID]
		if out[i].Selected {
			count++
			delete(set, out[i].UserID)
		}
	}
	if len(set) > 0 || count < 1 {
		return nil, 0, fmt.Errorf("%w：至少选择1名候选用户，且不能包含证据名单以外用户", ErrEntryEvidenceInvalid)
	}
	position := int64(0)
	for i := range out {
		if out[i].Selected {
			position++
			out[i].Branch = "A"
			if position > (count+1)/2 {
				out[i].Branch = "B"
			}
		} else {
			out[i].Branch = "R"
		}
	}
	return out, count, nil
}
func evidencePreviewForCase(c EntryEvidenceCase, req EntryEvidenceSplitRequest) EntryEvidenceSplitPreview {
	mode := "split_leaf"
	warnings := []string{"冻结的是排查证据名单，不是封禁用户；已完成服务端响应不等于客户端实际收到或使用。", "历史候选不代表造成故障，观察结论由操作人核对。", "UA条件和生效节点保留；按用户ID稳定均分，未选用户不加入A/B。"}
	if c.Target.Kind == "policy" {
		mode = "overlay"
		warnings = append(warnings, "新增仅选中用户的固定名单规则，插在原规则之前；原动态规则完整保留，未选及未来新用户仍按原逻辑。新增规则不是原规则的子组。")
		warnings = append(warnings, "原规则已启用检测时尝试继承检测配置；新A/B分支默认关闭自动二分。未启用或继承发生配置争用时需手动启用检测，以执行结果提示为准。")
	} else {
		warnings = append(warnings, "原叶子成为父组，A/B为选中用户；剩余用户迁到保留原入口的R子叶，其他叶子不变。")
	}
	if c.Execution != nil && c.Execution.MonitoringWarning != "" {
		warnings = append(warnings, c.Execution.MonitoringWarning)
	}
	a, b := req.EntryHostA, req.EntryHostB
	if req.UseBackupIPPool {
		a = "执行时原子领取健康IP"
		b = a
	} else {
		warnings = append(warnings, "手动填写的入口仅校验格式与不同地址，不代表已通过探针健康检查；请先完成对应端口检测，再确认执行。")
	}
	groups := []EntryEvidenceGroupPreview{{Branch: "A", UserCount: (c.SelectedCount + 1) / 2, EntryHost: a}, {Branch: "B", UserCount: c.SelectedCount / 2, EntryHost: b}, {Branch: "R", UserCount: c.ResidualCount, EntryHost: c.Target.EntryHost, PolicyID: c.Target.PolicyID, Unchanged: true}}
	out := EntryEvidenceSplitPreview{CaseID: c.ID, Revision: c.Revision, Mode: mode, Groups: groups, Conditions: c.Target.Conditions, Members: c.Target.Members, Warnings: warnings, Supported: c.SelectedCount >= 2}
	if !out.Supported {
		out.UnsupportedReason = "二分至少需要2名选中用户；单人档案仍可记录观察与结论"
	}
	return out
}
func evidenceProbeSnapshot(ctx context.Context, tx *sql.Tx, rule AIEntryRuleSnapshot, now int64) (json.RawMessage, error) {
	source := fmt.Sprintf("policy:%d:entry", rule.PolicyID)
	if rule.Kind == "split_group" {
		source = fmt.Sprintf("policy:%d:split-group:%d", rule.PolicyID, rule.TargetID)
	}
	var raw []byte
	err := tx.QueryRowContext(ctx, `SELECT jsonb_build_object('captured_at',$2::BIGINT,'time_basis','server_received_at','targets',COALESCE((SELECT jsonb_agg(jsonb_build_object('target_id',t.id,'generation',t.generation,'source_key',t.source_key,'host',t.host,'port',t.port,'monitor_id',t.monitor_id,'states',COALESCE((SELECT jsonb_agg(jsonb_build_object('probe_id',s.probe_id,'last_success',s.last_success,'last_reported_at',s.last_reported_at,'consecutive_failure',s.consecutive_failure,'consecutive_success',s.consecutive_success)) FROM v2_client_entry_monitor_state s WHERE s.target_id=t.id),'[]'::jsonb))) FROM v2_client_entry_monitor_target t JOIN v2_client_entry_monitor m ON m.id=t.monitor_id WHERE m.policy_id=$1 AND ($3='' OR t.source_key=$3)),'[]'::jsonb),'events','[]'::jsonb)`, rule.PolicyID, now, func() string {
		if rule.Kind == "policy" {
			return ""
		}
		return source
	}()).Scan(&raw)
	if err != nil {
		return nil, err
	}
	var snapshot map[string]any
	if err = json.Unmarshal(raw, &snapshot); err != nil {
		return nil, err
	}
	events, err := evidenceProbeSnapshotEvents(ctx, tx, rule, source)
	if err != nil {
		return nil, err
	}
	snapshot["events"] = events
	return json.Marshal(snapshot)
}
func (s *DBService) CreateEntryEvidenceCase(ctx context.Context, actor int64, req CreateEntryEvidenceCaseRequest) (EntryEvidenceCase, error) {
	var c EntryEvidenceCase
	if err := s.prepareEntryEvidence(ctx, actor, false); err != nil {
		return c, err
	}
	q, err := normalizeEvidenceQuery(req.EntryEvidenceQueryRequest)
	if err != nil {
		return c, err
	}
	if len(req.Revision) != 64 || len(req.ExpectedCandidatesHash) != 64 || utf8.RuneCountInString(req.Title) > 200 || req.ParentCaseID < 0 {
		return c, ErrEntryEvidenceInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return c, err
	}
	defer tx.Rollback()
	preview, items, ids, err := s.evidencePreviewTx(ctx, tx, q)
	if err != nil {
		return c, err
	}
	if preview.Target.Revision != req.Revision || preview.CandidatesHash != req.ExpectedCandidatesHash {
		return c, ErrEntryEvidenceConflict
	}
	if !preview.FreezeAllowed {
		return c, fmt.Errorf("%w：%s", ErrEntryEvidenceInvalid, preview.UnsupportedReason)
	}
	items, count, err := evidenceSelect(items, req.SelectedUserIDs)
	if err != nil {
		return c, err
	}
	if req.ParentCaseID > 0 {
		parent, readErr := readEvidenceCase(ctx, tx, req.ParentCaseID, false)
		if readErr != nil {
			return c, readErr
		}
		if parent.Status != "executed" || parent.Execution == nil || q.Kind != "split_group" || preview.Target.PolicyID != parent.Execution.PolicyID || (q.TargetID != parent.Execution.GroupAID && q.TargetID != parent.Execution.GroupBID && q.TargetID != parent.Execution.GroupRID) {
			return c, fmt.Errorf("%w：父轮次必须是当前目标的真实A/B/R来源", ErrEntryEvidenceInvalid)
		}
	}
	now := time.Now().Unix()
	topology, err := evidenceTopologyRevision(ctx, tx)
	if err != nil {
		return c, err
	}
	probes, err := evidenceProbeSnapshot(ctx, tx, preview.Target, now)
	if err != nil {
		return c, err
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = preview.Target.Name + " · 入口排查"
	}
	c = EntryEvidenceCase{Title: title, Status: "frozen", Revision: 1, CreatedBy: actor, CreatedAt: now, UpdatedAt: now, ParentCaseID: req.ParentCaseID, Target: preview.Target, Query: q, CandidatesHash: preview.CandidatesHash, TotalCandidates: int64(len(items)), SelectedCount: count, ResidualCount: int64(len(ids)) - count, SourceUserCount: int64(len(ids)), SourceUsersRevision: evidenceIDsHash(ids), TopologyRevision: topology, ProbeSnapshot: probes}
	c.Preview = evidencePreviewForCase(c, EntryEvidenceSplitRequest{})
	raw, _ := json.Marshal(c)
	if err = tx.QueryRowContext(ctx, `INSERT INTO v2_entry_evidence_case(created_by,created_at,updated_at,parent_case_id,snapshot,topology_revision) VALUES($1,$2,$2,$3,$4,$5) RETURNING id`, actor, now, req.ParentCaseID, string(raw), topology).Scan(&c.ID); err != nil {
		return c, err
	}
	// Bound JSON payloads and database round trips rather than issuing one INSERT
	// per user while retaining the routing topology lock.
	for start := 0; start < len(items); start += 500 {
		end := start + 500
		if end > len(items) {
			end = len(items)
		}
		raw, marshalErr := json.Marshal(items[start:end])
		if marshalErr != nil {
			return c, marshalErr
		}
		result, insertErr := tx.ExecContext(ctx, `INSERT INTO v2_entry_evidence_case_candidate(case_id,user_id,selected,branch,evidence)
 SELECT $1,(value->>'user_id')::BIGINT,(value->>'selected')::BOOLEAN,value->>'branch',value FROM jsonb_array_elements($2::JSONB)`, c.ID, string(raw))
		if insertErr != nil {
			return c, insertErr
		}
		inserted, countErr := result.RowsAffected()
		if countErr != nil || inserted != int64(end-start) {
			return c, ErrEntryEvidenceConflict
		}
		if err = ctx.Err(); err != nil {
			return c, err
		}
	}
	c.Preview.CaseID = c.ID
	return c, tx.Commit()
}
