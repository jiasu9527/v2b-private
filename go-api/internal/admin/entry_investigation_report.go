package admin

import (
	"context"
	"database/sql"
	"fmt"
	"forest/go-api/internal/cliententry"
	"net"
	"reflect"
	"strings"
	"time"
)

func investigationBaseReport(now int64) EntryInvestigationReport {
	return EntryInvestigationReport{State: "needs_review", CheckedAt: now, RequiresConfirmation: true, Branches: []EntryInvestigationBranch{}, Warnings: []string{
		"完整响应只证明服务端写入，不证明客户端使用或账号本人造成封锁。",
		"正常分支保留观察，未记录下发不等于未获得入口；不自动排除或封禁用户。",
		"普通日志只覆盖保留期，健康备用IP不代表从未暴露；新IP仍需独立性复核。",
	}, Blockers: []string{}}
}
func investigationHostExposure(ctx context.Context, tx *sql.Tx, host string, from, to, policy, group int64) (prior, foreign int64, err error) {
	// IP exposure through other ports and nodes also matters.
	err = tx.QueryRowContext(ctx, `SELECT count(DISTINCT r.user_id) FILTER(WHERE r.completed_at<$2),
 count(DISTINCT r.user_id) FILTER(WHERE r.completed_at>=$2 AND (COALESCE(n.policy_id,0)<>$4 OR ($5::BIGINT>0 AND COALESCE(n.group_id,0)<>$5)))
 FROM v2_entry_delivery_request r JOIN v2_entry_delivery_node n ON n.request_id=r.id
 WHERE LOWER(n.delivered_host)=LOWER($1) AND r.completed_at<$3 AND r.response_complete AND r.coverage='complete' AND r.status BETWEEN 200 AND 299`, host, from, to, policy, group).Scan(&prior, &foreign)
	return
}
func investigationHostReferences(ctx context.Context, tx *sql.Tx, host string, policy, group int64) (int64, error) {
	var n int64
	err := tx.QueryRowContext(ctx, `SELECT
 (SELECT count(*) FROM v2_client_entry_user_policy WHERE LOWER(btrim(entry_host))=LOWER($1) AND id<>$2)+
 (SELECT count(*) FROM v2_client_entry_user_policy_split_group WHERE LOWER(btrim(entry_host))=LOWER($1) AND id<>$3)+
 (SELECT count(*) FROM v2_client_entry_collection WHERE LOWER(btrim(entry_host))=LOWER($1))+
 (SELECT count(*) FROM v2_client_entry_group_ip WHERE LOWER(btrim(ip))=LOWER($1))+
 (SELECT count(*) FROM v2_support_entry_override o LEFT JOIN v2_client_entry_user_policy p ON p.id=o.target_policy_id LEFT JOIN v2_client_entry_user_policy_split_group g ON g.id=o.target_group_id WHERE o.active=1 AND (o.expires_at IS NULL OR o.expires_at>EXTRACT(EPOCH FROM now())) AND LOWER(btrim(CASE WHEN o.target_group_id IS NOT NULL THEN g.entry_host ELSE p.entry_host END))=LOWER($1))+
 (`+investigationNodeHostCountSQL("$1")+`)`, host, policy, group).Scan(&n)
	return n, err
}

// Only internal allowlisted table names/expressions are accepted here. Original
// node addresses are exposure paths even when no policy references that IP.
func investigationNodeHostCountSQL(hostExpression string) string {
	queries := make([]string, 0, len(serverGroupReferenceTables))
	for _, table := range serverGroupReferenceTables {
		queries = append(queries, `SELECT count(*) AS n FROM `+table+` WHERE LOWER(btrim(host))=LOWER(`+hostExpression+`)`)
	}
	return `SELECT COALESCE(sum(n),0) FROM (` + strings.Join(queries, ` UNION ALL `) + `) direct_hosts`
}
func (s *DBService) entryInvestigationReportTx(ctx context.Context, tx *sql.Tx, t EntryInvestigation, c EntryEvidenceCase, now int64) (EntryInvestigationReport, error) {
	return s.buildEntryInvestigationReportTx(ctx, tx, t, c, now, true)
}

func (s *DBService) buildEntryInvestigationReportTx(ctx context.Context, tx *sql.Tx, t EntryInvestigation, c EntryEvidenceCase, now int64, checkAncestors bool) (EntryInvestigationReport, error) {
	out := investigationBaseReport(now)
	out.EvidenceRisks = append([]string{}, t.Report.EvidenceRisks...)
	out.Blockers = append(out.Blockers, out.EvidenceRisks...)
	risk := func(message string) {
		for _, old := range out.EvidenceRisks {
			if old == message {
				return
			}
		}
		out.EvidenceRisks = append(out.EvidenceRisks, message)
		out.Blockers = append(out.Blockers, message)
	}
	if checkAncestors {
		if reason, err := s.investigationAncestorBlock(ctx, tx, t, false); err != nil {
			return out, err
		} else if reason != "" {
			out.Blockers = append(out.Blockers, reason)
		}
	}
	var days int64
	if err := tx.QueryRowContext(ctx, `SELECT keep_days FROM v2_entry_delivery_settings WHERE id=1`).Scan(&days); err != nil {
		return out, err
	}
	from := c.Query.From
	if c.Execution != nil {
		from = min(from, c.Execution.ExecutedAt)
	}
	if days < 1 || from < now-days*86400 {
		risk("观察窗口超出普通日志保留范围，无法完整复核跨组暴露；保留档案但停止自动缩小范围")
	}
	group := int64(0)
	if c.Target.Kind == "split_group" {
		group = c.Target.TargetID
	}
	prior, foreign, err := investigationHostExposure(ctx, tx, c.Query.Host, c.Query.From, c.Query.To, c.Target.PolicyID, group)
	if err != nil {
		return out, err
	}
	if prior > 0 {
		risk("故障IP存在窗口前历史暴露；已保留该风险，不因原日志清理而解除")
	}
	if foreign > 0 {
		risk("故障IP在同窗口还经其他来源下发，不能只归因当前组；风险已归档")
	}
	if c.Execution == nil {
		refs, err := investigationHostReferences(ctx, tx, c.Query.Host, c.Target.PolicyID, group)
		if err != nil {
			return out, err
		}
		if refs > 0 {
			out.Blockers = append(out.Blockers, "故障IP仍被其他规则、叶子、合集或入口组引用，先核对共享范围")
		}
		var auto bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM v2_client_entry_monitor_target mt JOIN v2_client_entry_monitor m ON m.id=mt.monitor_id WHERE m.policy_id=$1 AND LOWER(mt.host)=LOWER($2) AND m.enabled=1 AND mt.auto_split_enabled=1)`, c.Target.PolicyID, c.Query.Host).Scan(&auto); err != nil {
			return out, err
		}
		if auto {
			out.Blockers = append(out.Blockers, "来源启用了旧自动二分，请先关闭该目标的自动二分，避免与半自动任务竞争")
		}
		out.State = "awaiting_split_confirmation"
		out.Reason = "名单已冻结；AI可核对A/B与R预览后按当前会话权限执行，也可在页面办理；后台观察任务本身不改入口"
		if c.SelectedCount <= t.Settings.StopUsers {
			out.State = "review_candidates"
			out.Reason = "已达到小名单阈值，保留证据交人工复核，不据此认定或封禁"
		}
		if len(out.Blockers) > 0 {
			out.State = "needs_review"
			out.Reason = "存在暴露范围或证据问题，暂不生成执行建议"
		}
		return out, nil
	}
	var rawConditions string
	if err := tx.QueryRowContext(ctx, `SELECT conditions FROM v2_client_entry_user_policy WHERE id=$1`, c.Execution.PolicyID).Scan(&rawConditions); err != nil {
		if err == sql.ErrNoRows {
			out.Blockers = append(out.Blockers, "本轮规则已删除")
		} else {
			return out, err
		}
	} else {
		cond, err := cliententry.DecodeConditions(rawConditions)
		if err != nil {
			return out, err
		}
		if !reflect.DeepEqual(cliententry.RuntimeConditions("split", cond), cliententry.RuntimeConditions("split", c.Target.Conditions)) {
			out.Blockers = append(out.Blockers, "本轮UA条件已被修改")
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT server_type,server_id FROM v2_client_entry_user_policy_member WHERE policy_id=$1`, c.Execution.PolicyID)
	if err != nil {
		return out, err
	}
	actual := map[AIEntryRuleMember]bool{}
	for rows.Next() {
		var m AIEntryRuleMember
		if err = rows.Scan(&m.ServerType, &m.ServerID); err != nil {
			rows.Close()
			return out, err
		}
		actual[m] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if len(actual) != len(c.Target.Members) {
		out.Blockers = append(out.Blockers, "本轮节点范围已被修改")
	} else {
		for _, m := range c.Target.Members {
			if !actual[m] {
				out.Blockers = append(out.Blockers, "本轮节点范围已被修改")
				break
			}
		}
	}
	out.ExecutedAt = c.Execution.ExecutedAt
	out.ObservedSeconds = max(0, now-out.ExecutedAt)
	var gap bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM v2_entry_evidence_observation_state WHERE case_id=$1 AND coverage_gap) OR EXISTS(SELECT 1 FROM v2_entry_evidence_probe_event_state WHERE case_id=$1 AND coverage_gap)`, c.ID).Scan(&gap); err != nil {
		return out, err
	}
	if gap {
		risk("本轮下发或探针归档存在缺口")
	}
	failures := investigationLatestFailures(t.Failures)
	bothDelivered := true
	latestFirst := int64(0)
	anyDown := false
	for _, b := range []struct {
		name  string
		id    int64
		host  string
		count int64
	}{{"A", c.Execution.GroupAID, c.Execution.EntryHostA, (c.SelectedCount + 1) / 2}, {"B", c.Execution.GroupBID, c.Execution.EntryHostB, c.SelectedCount / 2}} {
		branch := EntryInvestigationBranch{Branch: b.name, GroupID: b.id, Host: b.host, AssignedUsers: b.count, Health: "unmonitored"}
		if f, ok := failures[b.name]; ok && t.NextCaseID > 0 {
			// The continued branch can now be an internal tree node. Keep its
			// archived round numbers; re-evaluate the untouched peer below.
			// Still reject late-arriving historical contamination. Valid descendant
			// splits change topology, but never justify outsiders before the cutoff.
			var outsider bool
			err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM v2_entry_delivery_request r JOIN v2_entry_delivery_node n ON n.request_id=r.id WHERE r.completed_at>=$1 AND r.completed_at<$2 AND r.response_complete AND r.coverage='complete' AND r.status BETWEEN 200 AND 299 AND LOWER(n.delivered_host)=LOWER($3) AND NOT EXISTS(SELECT 1 FROM v2_entry_evidence_case_candidate cc WHERE cc.case_id=$4 AND cc.selected AND cc.branch=$5 AND cc.user_id=r.user_id))`, out.ExecutedAt, f.BlockedAt, b.host, c.ID, b.name).Scan(&outsider)
			if err != nil {
				return out, err
			}
			pre, foreign, err := investigationHostExposure(ctx, tx, b.host, out.ExecutedAt, f.BlockedAt, c.Execution.PolicyID, b.id)
			if err != nil {
				return out, err
			}
			if outsider || pre > 0 || foreign > 0 {
				risk(b.name + "已继续分支发现异常前历史或跨来源暴露，后续轮次需复核")
			}
			for _, old := range t.Report.Branches {
				if old.Branch == b.name {
					branch = old
				}
			}
			branch.BlockedAt = f.BlockedAt
			branch.Health = "archived"
			out.Branches = append(out.Branches, branch)
			continue
		}
		var actualHost string
		var enabled int64
		var leaf bool
		err = tx.QueryRowContext(ctx, `SELECT g.entry_host,p.enabled,NOT EXISTS(SELECT 1 FROM v2_client_entry_user_policy_split_group child WHERE child.parent_id=g.id) FROM v2_client_entry_user_policy_split_group g JOIN v2_client_entry_user_policy p ON p.id=g.policy_id WHERE g.id=$1 AND p.id=$2`, b.id, c.Execution.PolicyID).Scan(&actualHost, &enabled, &leaf)
		if err != nil && err != sql.ErrNoRows {
			return out, err
		}
		if err == sql.ErrNoRows || !leaf || enabled != 1 || !strings.EqualFold(actualHost, b.host) {
			out.Blockers = append(out.Blockers, b.name+"组入口或拓扑已被外部修改")
		}
		var membershipChanged bool
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(
         (SELECT user_id FROM v2_client_entry_user_policy_split_assignment WHERE policy_id=$1 AND group_id=$2 EXCEPT SELECT user_id FROM v2_entry_evidence_case_candidate WHERE case_id=$3 AND selected AND branch=$4)
         UNION ALL
         (SELECT user_id FROM v2_entry_evidence_case_candidate WHERE case_id=$3 AND selected AND branch=$4 EXCEPT SELECT user_id FROM v2_client_entry_user_policy_split_assignment WHERE policy_id=$1 AND group_id=$2))`, c.Execution.PolicyID, b.id, c.ID, b.name).Scan(&membershipChanged)
		if err != nil {
			return out, err
		}
		if membershipChanged {
			out.Blockers = append(out.Blockers, b.name+"组名单已被修改，不能沿用旧轮次缩小范围")
		}
		pre, other, err := investigationHostExposure(ctx, tx, b.host, out.ExecutedAt, now+1, c.Execution.PolicyID, b.id)
		if err != nil {
			return out, err
		}
		if pre > 0 {
			risk(b.name + "入口存在本轮前历史暴露；风险已归档")
		}
		if other > 0 {
			risk(b.name + "入口被其他来源下发；风险已归档")
		}
		var outsider bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM v2_entry_delivery_request r JOIN v2_entry_delivery_node n ON n.request_id=r.id WHERE r.completed_at>=$1 AND r.completed_at<=$2 AND r.response_complete AND r.coverage='complete' AND r.status BETWEEN 200 AND 299 AND LOWER(n.delivered_host)=LOWER($3) AND NOT EXISTS(SELECT 1 FROM v2_entry_evidence_case_candidate cc WHERE cc.case_id=$4 AND cc.selected AND cc.branch=$5 AND cc.user_id=r.user_id))`, out.ExecutedAt, now, b.host, c.ID, b.name).Scan(&outsider); err != nil {
			return out, err
		}
		if outsider {
			risk(b.name + "入口被冻名单以外用户获得；风险已归档")
		}
		refs, err := investigationHostReferences(ctx, tx, b.host, c.Execution.PolicyID, b.id)
		if err != nil {
			return out, err
		}
		if refs > 0 {
			out.Blockers = append(out.Blockers, b.name+"入口存在其他配置引用")
		}
		cutoff := now + 1
		if f, ok := failures[b.name]; ok {
			cutoff = f.BlockedAt
			branch.BlockedAt = f.BlockedAt
		}
		var first int64
		err = tx.QueryRowContext(ctx, `SELECT count(DISTINCT r.user_id),count(DISTINCT r.user_id) FILTER(WHERE r.completed_at<$6),COALESCE(min(r.completed_at),0)
 FROM v2_entry_delivery_request r JOIN v2_entry_evidence_case_candidate cc ON cc.case_id=$1 AND cc.user_id=r.user_id AND cc.selected AND cc.branch=$2
 WHERE r.completed_at>=$3 AND r.completed_at<=$7 AND r.response_complete AND r.coverage='complete' AND r.status BETWEEN 200 AND 299
 AND EXISTS(SELECT 1 FROM v2_entry_delivery_node n WHERE n.request_id=r.id AND n.policy_id=$4 AND n.group_id=$5 AND LOWER(n.delivered_host)=LOWER($8) AND n.port=$9)`, c.ID, b.name, out.ExecutedAt, c.Execution.PolicyID, b.id, cutoff, now, b.host, fmt.Sprint(c.Query.Port)).Scan(&branch.AcquiredUsers, &branch.PreFailureUsers, &first)
		if err != nil {
			return out, err
		}
		branch.UnrecordedUsers = max(0, branch.AssignedUsers-branch.AcquiredUsers)
		if first == 0 {
			bothDelivered = false
		}
		latestFirst = max(latestFirst, first)
		var target, interval, timeout, failureThreshold, successThreshold int64
		var auto bool
		source := fmt.Sprintf("policy:%d:split-group:%d", c.Execution.PolicyID, b.id)
		err = tx.QueryRowContext(ctx, `SELECT mt.id,m.check_interval_sec,m.tcp_timeout_ms,m.failure_threshold,m.success_threshold,mt.auto_split_enabled<>0 FROM v2_client_entry_monitor_target mt JOIN v2_client_entry_monitor m ON m.id=mt.monitor_id WHERE m.enabled=1 AND m.policy_id=$1 AND mt.source_key=$2 AND LOWER(mt.host)=LOWER($3) AND mt.port=$4`, c.Execution.PolicyID, source, b.host, c.Query.Port).Scan(&target, &interval, &timeout, &failureThreshold, &successThreshold, &auto)
		if err != nil && err != sql.ErrNoRows {
			return out, err
		}
		if err == nil {
			if auto {
				out.Blockers = append(out.Blockers, b.name+"开启了旧自动二分，与半自动排查冲突")
			}
			var online, healthy, failed int64
			err = tx.QueryRowContext(ctx, `SELECT count(*),count(*) FILTER(WHERE st.last_success=1 AND st.consecutive_success>=$4 AND st.last_reported_at BETWEEN $2::BIGINT-$3::BIGINT AND $2),count(*) FILTER(WHERE st.last_success=0 AND st.consecutive_failure>=$5 AND st.last_reported_at BETWEEN $2::BIGINT-$3::BIGINT AND $2),COALESCE(max(st.last_reported_at),0)
 FROM v2_dns_probe pr LEFT JOIN v2_client_entry_monitor_state st ON st.probe_id=pr.id AND st.target_id=$1 WHERE pr.enabled=1 AND pr.last_heartbeat_at BETWEEN $2-90 AND $2`, target, now, min(int64(90), 2*(interval+(timeout+999)/1000)), successThreshold, failureThreshold).Scan(&online, &healthy, &failed, &branch.LastReportedAt)
			if err != nil {
				return out, err
			}
			branch.Health = "insufficient"
			if online > 0 && healthy == online {
				branch.Health = "healthy"
			} else if online > 0 && failed == online {
				branch.Health = "down"
				anyDown = true
			}
		}
		if t.NextCaseID > 0 {
			var pastFailure bool
			err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM v2_entry_evidence_probe_event WHERE case_id=$1 AND branch=$2 AND event_type='down' AND happened_at>=$3)`, c.ID, b.name, out.ExecutedAt).Scan(&pastFailure)
			if err != nil {
				return out, err
			}
			if pastFailure || branch.Health == "down" {
				risk(b.name + "保留观察分支曾出现检测异常，不能因最新检测恢复就自动解除矛盾；请人工复核")
			}
		}
		out.Branches = append(out.Branches, branch)
	}
	if t.NextCaseID > 0 {
		out.State, out.Reason = "continued", "下一轮名单已冻结，原正常分支仍保留观察；不会自动排除用户"
		if len(failures) > 1 {
			out.State, out.Reason = "both_failed", "原正常分支也被确认异常，后续轮次停止推进，请复核共同因素"
		} else {
			for _, b := range out.Branches {
				if _, failed := failures[b.Branch]; !failed && b.Health != "healthy" {
					out.State, out.Reason = "needs_review", "保留观察分支异常或缺少新鲜健康检测，后续轮次暂停推进"
				}
			}
		}
		if len(out.Blockers) > 0 {
			out.State, out.Reason = "needs_review", "存在共享暴露、证据缺口或范围变更，后续轮次暂停推进"
		}
		return out, nil
	}
	return decideEntryInvestigation(t, c, out, bothDelivered, latestFirst, anyDown, now), nil
}
func decideEntryInvestigation(t EntryInvestigation, c EntryEvidenceCase, out EntryInvestigationReport, bothDelivered bool, latestFirst int64, anyDown bool, now int64) EntryInvestigationReport {
	if len(out.Blockers) > 0 {
		out.State = "needs_review"
		out.Reason = "存在证据缺口、共享暴露或外部配置变更，不能自动缩小范围"
		return out
	}
	failures := investigationLatestFailures(t.Failures)
	if len(failures) > 1 {
		out.State = "both_failed"
		out.Reason = "A/B都被确认异常，可能多来源泄露或共同网络因素；保留两边，不选择单一嫌疑组"
		return out
	}
	if len(failures) == 1 {
		var f EntryInvestigationFailure
		for _, value := range failures {
			f = value
		}
		if f.ControlStatus != "healthy" {
			out.State = "needs_review"
			out.Reason = "对照入口异常或尚未核对，不能用这一轮单独缩小范围"
			return out
		}
		var failed, other EntryInvestigationBranch
		for _, b := range out.Branches {
			if b.Branch == f.Branch {
				failed = b
			} else {
				other = b
			}
		}
		if failed.PreFailureUsers == 0 {
			out.State = "needs_review"
			out.Reason = "异常前没有完整下发证据，先排查历史暴露或网络原因，不能归咎本轮用户"
			return out
		}
		if failed.Health != "down" {
			out.State = "needs_review"
			out.Reason = "人工确认与当前探测不一致或检测不足，需复核，不重复换入口"
			return out
		}
		if other.Health != "healthy" {
			out.State = "needs_review"
			out.Reason = "另一组尚无完整新鲜健康检测，不能仅凭单边异常继续缩小"
			return out
		}
		if !bothDelivered {
			out.State = "waiting_delivery"
			out.Reason = "两组尚未都有实际下发记录，继续等待；未更新用户不作无嫌疑结论"
			return out
		}
		if now-max(out.ExecutedAt, latestFirst) < t.Settings.MinObserveSeconds {
			out.State = "observing"
			out.Reason = "从两组开始实际下发起的观察时间尚不足，保留正常分支继续观察"
			return out
		}
		if failed.PreFailureUsers <= t.Settings.StopUsers {
			out.State = "review_candidates"
			out.Reason = "已缩小到小名单，需独立入口重复验证及人工复核，不自动封号"
			return out
		}
		if t.Round >= t.Settings.MaxRounds {
			out.State = "round_limit"
			out.Reason = "已达到本任务轮数上限，停止消耗新IP并交人工复核"
			return out
		}
		out.State = "ready_next"
		out.Reason = "单组人工确认异常，对照及另一组检测正常；可冻结异常前实际下发用户作为下一轮，正常组继续观察"
		out.NextQuery = &EntryEvidenceQueryRequest{Kind: "split_group", TargetID: failed.GroupID, Host: failed.Host, Port: c.Query.Port, From: out.ExecutedAt, To: f.BlockedAt, Current: 1, PageSize: 20}
		return out
	}
	if anyDown {
		out.State = "awaiting_block_confirmation"
		out.Reason = "检测到入口异常，但TCP失败不能自动认定被墙；请核实并登记异常时间及对照结果"
		return out
	}
	if !bothDelivered {
		out.State = "waiting_delivery"
		out.Reason = "等待A/B实际下发，分配人数不等于获得入口人数"
		return out
	}
	out.State = "observing"
	out.Reason = "继续观察，不因暂时正常排除用户；后台不会自行换IP或二分"
	return out
}
func (s *DBService) validateEntryInvestigationSplit(ctx context.Context, tx *sql.Tx, c EntryEvidenceCase, now int64) error {
	t, err := readEntryInvestigation(ctx, tx, c.ID, false)
	if err == ErrEntryEvidenceNotFound {
		return nil
	}
	if err != nil {
		return err
	}
	if reason, err := s.investigationAncestorBlock(ctx, tx, t, true); err != nil {
		return err
	} else if reason != "" {
		return fmt.Errorf("%w：%s", ErrEntryEvidenceConflict, reason)
	}
	report, err := s.entryInvestigationReportTx(ctx, tx, t, c, now)
	if err != nil {
		return err
	}
	if report.State != "awaiting_split_confirmation" {
		return fmt.Errorf("%w：排查任务需先复核：%s", ErrEntryEvidenceInvalid, report.Reason)
	}
	return nil
}
func validateInvestigationEntryHosts(ctx context.Context, tx *sql.Tx, c EntryEvidenceCase, a, b string, now int64) error {
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM v2_entry_investigation WHERE case_id=$1)`, c.ID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return nil
	}
	for _, host := range []string{a, b} {
		var archived bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM v2_entry_evidence_case WHERE LOWER(execution->>'entry_host_a')=LOWER($1) OR LOWER(execution->>'entry_host_b')=LOWER($1))`, host).Scan(&archived); err != nil {
			return err
		}
		if archived {
			return fmt.Errorf("%w：入口已被历史排查轮次使用", ErrEntryEvidenceInvalid)
		}

		if net.ParseIP(host) == nil {
			return fmt.Errorf("%w：排查分流需使用独立IP而非动态域名", ErrEntryEvidenceInvalid)
		}
		prior, other, err := investigationHostExposure(ctx, tx, host, now, now+1, 0, 0)
		if err != nil {
			return err
		}
		if prior+other > 0 {
			return fmt.Errorf("%w：新入口在保留期内已有下发记录，请换独立入口", ErrEntryEvidenceInvalid)
		}
		refs, err := investigationHostReferences(ctx, tx, host, 0, 0)
		if err != nil {
			return err
		}
		if refs > 0 {
			return fmt.Errorf("%w：新入口已被其他配置引用", ErrEntryEvidenceInvalid)
		}
		var healthy bool
		err = tx.QueryRowContext(ctx, `WITH online AS (
 SELECT id FROM v2_dns_probe WHERE enabled=1 AND last_heartbeat_at BETWEEN $3::BIGINT-90 AND $3
 ) SELECT EXISTS(SELECT 1 FROM online) AND NOT EXISTS(SELECT 1 FROM online pr WHERE NOT (
 EXISTS(SELECT 1 FROM v2_client_entry_backup_ip b JOIN v2_client_entry_backup_ip_state st ON st.backup_ip_id=b.id
 WHERE LOWER(b.ip)=LOWER($1) AND b.port=$2 AND b.enabled=1 AND b.quarantine_until<=$3 AND st.probe_id=pr.id AND st.last_success=1 AND st.consecutive_success>=$4
 AND st.last_reported_at BETWEEN $3-LEAST(90,2*(b.check_interval_sec+(b.tcp_timeout_ms+999)/1000)) AND $3)
 OR EXISTS(SELECT 1 FROM v2_client_entry_monitor_target mt JOIN v2_client_entry_monitor m ON m.id=mt.monitor_id JOIN v2_client_entry_monitor_state st ON st.target_id=mt.id
 WHERE LOWER(mt.host)=LOWER($1) AND mt.port=$2 AND m.enabled=1 AND st.probe_id=pr.id AND st.last_success=1 AND st.consecutive_success>=m.success_threshold
 AND st.last_reported_at BETWEEN $3-LEAST(90,2*(m.check_interval_sec+(m.tcp_timeout_ms+999)/1000)) AND $3)
 ))`, host, c.Query.Port, now, clientEntryBackupIPSuccessThreshold).Scan(&healthy)
		if err != nil {
			return err
		}
		if !healthy {
			return fmt.Errorf("%w：新入口缺少对应端口的新鲜健康检测，请先加入检测再预览执行", ErrEntryEvidenceInvalid)
		}
	}
	return nil
}

func investigationLatestFailures(all []EntryInvestigationFailure) map[string]EntryInvestigationFailure {
	result := map[string]EntryInvestigationFailure{}
	for _, f := range all {
		result[f.Branch] = f
	}
	return result
}
func (s *DBService) investigationAncestorBlock(ctx context.Context, tx *sql.Tx, t EntryInvestigation, lock bool) (string, error) {
	id := t.CaseID
	seen := map[int64]bool{}
	for depth := 0; depth < 30; depth++ {
		c, err := readEvidenceCase(ctx, tx, id, false)
		if err != nil {
			return "", err
		}
		if c.ParentCaseID == 0 {
			return "", nil
		}
		id = c.ParentCaseID
		if seen[id] {
			return "排查轮次链异常", nil
		}
		seen[id] = true
		ancestor, err := readEntryInvestigation(ctx, tx, id, false)
		if err != nil {
			return "上游排查任务缺失，无法继续", nil
		}
		if lock {
			if _, err = tx.ExecContext(ctx, `SELECT case_id FROM v2_entry_investigation WHERE case_id=$1 FOR SHARE NOWAIT`, id); err != nil {
				// A next-round writer can already own this row while waiting for
				// topology. Do not invert that order and deadlock the two turns.
				return "", fmt.Errorf("%w：上游轮次正在更新，请刷新后重试", ErrEntryEvidenceConflict)
			}
			ancestor, err = readEntryInvestigation(ctx, tx, id, false)
			if err != nil {
				return "", err
			}
		}
		fs := investigationLatestFailures(ancestor.Failures)
		if len(fs) != 1 {
			return "上游轮次不再是单分支确认异常，后续轮次需人工复核", nil
		}
		for _, f := range fs {
			if f.ControlStatus != "healthy" {
				return "上游轮次对照未确认正常，后续轮次暂停", nil
			}
		}
		pc, err := readEvidenceCase(ctx, tx, id, false)
		if err != nil {
			return "", err
		}
		if pc.Execution == nil || ancestor.NextCaseID != c.ID {
			return "上游轮次与当前档案关联不一致", nil
		}
		for branch, f := range fs {
			group, host := pc.Execution.GroupAID, pc.Execution.EntryHostA
			if branch == "B" {
				group, host = pc.Execution.GroupBID, pc.Execution.EntryHostB
			}
			if c.Query.TargetID != group || c.Query.Host != host || c.Query.From != pc.Execution.ExecutedAt || c.Query.To != f.BlockedAt {
				return "上游封锁时间或分支已修正，本轮冻结窗口需人工复核", nil
			}
		}
		// Reuse the full retained-branch validation, but traverse ancestors only
		// in this bounded loop (not recursively). Missing/stale monitoring,
		// changed scope and newly shared exposure all fail closed.
		r, err := s.buildEntryInvestigationReportTx(ctx, tx, ancestor, pc, time.Now().Unix(), false)
		if err != nil {
			return "", err
		}
		if r.State != "continued" || len(r.Blockers) > 0 {
			return "上游保留观察分支需复核：" + r.Reason, nil
		}
	}
	return "排查轮次链超过上限", nil
}
