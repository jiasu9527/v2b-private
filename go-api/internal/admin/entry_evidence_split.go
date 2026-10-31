package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

func (s *DBService) ExecuteEntryEvidenceSplit(ctx context.Context, actor int64, req EntryEvidenceSplitRequest) (EntryEvidenceCase, error) {
	var c EntryEvidenceCase
	if err := s.prepareEntryEvidence(ctx, actor, false); err != nil {
		return c, err
	}
	req, err := normalizeEvidenceSplit(req)
	if err != nil {
		return c, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return c, err
	}
	defer tx.Rollback()
	if err = authorizeEntryEvidence(ctx, tx, actor, false); err != nil {
		return c, err
	}
	c, err = readEvidenceCase(ctx, tx, req.CaseID, true)
	if err != nil {
		return c, err
	}
	if c.Status == "executed" {
		if c.Execution == nil || !evidenceSplitSameRequest(req, c.Execution.Request) {
			return c, ErrEntryEvidenceConflict
		}
		return c, tx.Commit()
	}
	if c.Revision != req.Revision {
		return c, ErrEntryEvidenceConflict
	}
	items, ids, err := s.validateEvidenceCaseTx(ctx, tx, c)
	if err != nil {
		return c, err
	}
	now := time.Now().Unix()
	hostA, hostB := req.EntryHostA, req.EntryHostB
	if req.UseBackupIPPool {
		ips, e := claimHealthyEntryEvidenceBackupIPs(ctx, tx, 2, c.Query.Port, now)
		if e != nil {
			return c, e
		}
		if len(ips) != 2 {
			return c, ErrClientEntryBackupIPInsufficient
		}
		if ips[0].Port != c.Query.Port || ips[1].Port != c.Query.Port {
			return c, fmt.Errorf("%w：备用IP检测端口与当前已下发端口不一致，未领取、未二分", ErrEntryEvidenceInvalid)
		}
		hostA, hostB = ips[0].IP, ips[1].IP
	}
	if strings.EqualFold(hostA, c.Target.EntryHost) || strings.EqualFold(hostB, c.Target.EntryHost) || strings.EqualFold(hostA, c.Query.Host) || strings.EqualFold(hostB, c.Query.Host) || strings.EqualFold(hostA, hostB) {
		return c, fmt.Errorf("%w：两个新入口不能相同或等于原入口", ErrEntryEvidenceInvalid)
	}
	// Changes use the same visible-order transaction lock as existing editors.
	// New branches occupy the source position; shifting later rows preserves order.
	policyID := c.Target.PolicyID
	parent := (*int64)(nil)
	pathA, pathB, pathR := "A", "B", "R"
	sortAt := c.Target.Sort
	resolve := c.Target.ResolveEntryHost
	if c.Target.Kind == "policy" {
		if err = shiftClientEntryVisibleSortsAfter(ctx, tx, sortAt-1, 2*clientEntryRuleSortStep); err != nil {
			return c, err
		}
		conditionRaw, _ := json.Marshal(c.Target.Conditions)
		if err = tx.QueryRowContext(ctx, `INSERT INTO v2_client_entry_user_policy(name,sort,mode,action,conditions,entry_host,resolve_entry_host,extra_nodes,extra_nodes_position,enabled,remarks,created_at,updated_at)
   SELECT $2,$3,'split','override',$4,'',resolve_entry_host,'[]','after',enabled,$5,$6,$6 FROM v2_client_entry_user_policy WHERE id=$1 RETURNING id`, c.Target.PolicyID, fmt.Sprintf("排查 #%d · %s", c.ID, c.Target.Name), sortAt, string(conditionRaw), fmt.Sprintf("冻结证据 #%d；来源规则 #%d 保留", c.ID, c.Target.PolicyID), now).Scan(&policyID); err != nil {
			return c, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO v2_client_entry_user_policy_member(policy_id,server_type,server_id,sort,created_at,updated_at) SELECT $2,server_type,server_id,sort,$3,$3 FROM v2_client_entry_user_policy_member WHERE policy_id=$1`, c.Target.PolicyID, policyID, now); err != nil {
			return c, err
		}
	} else {
		var path string
		var inherited int64
		if err = tx.QueryRowContext(ctx, `SELECT g.path,COALESCE(g.resolve_entry_host,p.resolve_entry_host) FROM v2_client_entry_user_policy_split_group g JOIN v2_client_entry_user_policy p ON p.id=g.policy_id WHERE g.id=$1 AND g.policy_id=$2 FOR UPDATE OF g`, c.Target.TargetID, c.Target.PolicyID).Scan(&path, &inherited); err != nil {
			return c, err
		}
		parentID := c.Target.TargetID
		parent = &parentID
		resolve = &inherited
		pathA, pathB, pathR = path+".1", path+".2", path+".R"
		if len([]rune(pathA)) > 255 || len([]rune(pathR)) > 255 {
			return c, fmt.Errorf("%w：二分层级过深", ErrEntryEvidenceInvalid)
		}
		delta := clientEntryRuleSortStep
		if c.ResidualCount > 0 {
			delta *= 2
		}
		if err = shiftClientEntryVisibleSortsAfter(ctx, tx, sortAt, delta); err != nil {
			return c, err
		}
	}
	groupA, err := insertClientEntryUserPolicySplitGroup(ctx, tx, policyID, parent, pathA, pathA, hostA, clientEntryRuleSortStep, sortAt, now, resolve)
	if err != nil {
		return c, err
	}
	groupB, err := insertClientEntryUserPolicySplitGroup(ctx, tx, policyID, parent, pathB, pathB, hostB, 2*clientEntryRuleSortStep, sortAt+clientEntryRuleSortStep, now, resolve)
	if err != nil {
		return c, err
	}
	groupR := int64(0)
	if c.Target.Kind == "split_group" && c.ResidualCount > 0 {
		groupR, err = insertClientEntryUserPolicySplitGroup(ctx, tx, policyID, parent, pathR, pathR, c.Target.EntryHost, 3*clientEntryRuleSortStep, sortAt+2*clientEntryRuleSortStep, now, resolve)
		if err != nil {
			return c, err
		}
	}
	selectedA, selectedB := []int64{}, []int64{}
	for _, item := range items {
		if item.Branch != "A" && item.Branch != "B" {
			return c, ErrEntryEvidenceConflict
		}
		if item.Branch == "A" {
			selectedA = append(selectedA, item.UserID)
		} else {
			selectedB = append(selectedB, item.UserID)
		}
	}
	if len(selectedA) == 0 || len(selectedB) == 0 {
		return c, ErrEntryEvidenceConflict
	}
	if c.Target.Kind == "policy" {
		result, insertErr := tx.ExecContext(ctx, `INSERT INTO v2_client_entry_user_policy_split_assignment(policy_id,user_id,group_id,created_at,updated_at)
 SELECT $1,user_id,CASE WHEN branch='A' THEN $3::BIGINT ELSE $4::BIGINT END,$5,$5
 FROM v2_entry_evidence_case_candidate WHERE case_id=$2 AND selected=TRUE AND branch IN ('A','B')`, policyID, c.ID, groupA, groupB, now)
		if insertErr != nil {
			return c, insertErr
		}
		inserted, countErr := result.RowsAffected()
		if countErr != nil || inserted != c.SelectedCount {
			return c, ErrEntryEvidenceConflict
		}
	} else {
		if c.ResidualCount > 0 && groupR == 0 {
			return c, ErrEntryEvidenceConflict
		}
		result, updateErr := tx.ExecContext(ctx, `UPDATE v2_client_entry_user_policy_split_assignment
 SET group_id=CASE WHEN user_id=ANY($4::BIGINT[]) THEN $5::BIGINT WHEN user_id=ANY($6::BIGINT[]) THEN $7::BIGINT ELSE $8::BIGINT END,updated_at=$9
 WHERE policy_id=$1 AND group_id=$2 AND user_id=ANY($3::BIGINT[])`, policyID, c.Target.TargetID, ids, selectedA, groupA, selectedB, groupB, groupR, now)
		if updateErr != nil {
			return c, updateErr
		}
		moved, countErr := result.RowsAffected()
		if countErr != nil || moved != c.SourceUserCount {
			return c, ErrEntryEvidenceConflict
		}
		if _, err = tx.ExecContext(ctx, `UPDATE v2_client_entry_user_policy_split_group SET entry_host='',global_sort=NULL,updated_at=$2 WHERE id=$1`, c.Target.TargetID, now); err != nil {
			return c, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE v2_client_entry_user_policy SET updated_at=$2 WHERE id=$1`, policyID, now); err != nil {
			return c, err
		}
	}
	if err = syncClientEntrySplitPolicySorts(ctx, tx); err != nil {
		return c, err
	}
	execution := EntryEvidenceExecution{PolicyID: policyID, GroupAID: groupA, GroupBID: groupB, GroupRID: groupR, ExecutedAt: now, ExecutedBy: actor, Request: req, EntryHostA: hostA, EntryHostB: hostB}
	if c.Target.Kind == "policy" {
		execution.MonitoringStatus, execution.MonitoringWarning, err = inheritEntryEvidenceMonitor(ctx, tx, c, execution, now)
		if err != nil {
			return c, err
		}
	}
	executionRaw, _ := json.Marshal(execution)
	result, err := tx.ExecContext(ctx, `UPDATE v2_entry_evidence_case SET status='executed',revision=revision+1,execution=$2,updated_at=$3 WHERE id=$1 AND status='frozen' AND revision=$4`, c.ID, string(executionRaw), now, req.Revision)
	if err != nil {
		return c, err
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return c, ErrEntryEvidenceConflict
	}
	if err = tx.Commit(); err != nil {
		return c, err
	}
	s.markClientEntryMonitorTargetsDirty()
	c.Execution = &execution
	c.Status = "executed"
	c.Revision++
	c.UpdatedAt = now
	c.Preview = evidencePreviewForCase(c, req)
	for i := range c.Preview.Groups {
		g := &c.Preview.Groups[i]
		switch g.Branch {
		case "A":
			g.EntryHost = hostA
			g.PolicyID = policyID
			g.GroupID = groupA
		case "B":
			g.EntryHost = hostB
			g.PolicyID = policyID
			g.GroupID = groupB
		case "R":
			g.GroupID = groupR
		}
	}
	return c, nil
}
