package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

func readEvidenceCase(ctx context.Context, db evidenceQueryer, id int64, lock bool) (EntryEvidenceCase, error) {
	var c EntryEvidenceCase
	var raw, execution []byte
	var cid, rev, creator, created, updated, parent int64
	var status, topology, observation, conclusion string
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	err := db.QueryRowContext(ctx, `SELECT id,revision,created_by,created_at,updated_at,parent_case_id,status,snapshot,topology_revision,observation,conclusion,execution FROM v2_entry_evidence_case WHERE id=$1`+suffix, id).Scan(&cid, &rev, &creator, &created, &updated, &parent, &status, &raw, &topology, &observation, &conclusion, &execution)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrEntryEvidenceNotFound
	}
	if err != nil {
		return c, err
	}
	if json.Unmarshal(raw, &c) != nil {
		return c, ErrEntryEvidenceInvalid
	}
	c.ID = cid
	c.Revision = rev
	c.CreatedBy = creator
	c.CreatedAt = created
	c.UpdatedAt = updated
	c.ParentCaseID = parent
	c.Status = status
	c.TopologyRevision = topology
	c.Observation = observation
	c.Conclusion = conclusion
	if len(execution) > 0 && string(execution) != "null" {
		if json.Unmarshal(execution, &c.Execution) != nil {
			return c, ErrEntryEvidenceInvalid
		}
	}
	req := EntryEvidenceSplitRequest{}
	if c.Execution != nil {
		req = c.Execution.Request
	}
	c.Preview = evidencePreviewForCase(c, req)
	if c.Execution != nil {
		for i := range c.Preview.Groups {
			g := &c.Preview.Groups[i]
			switch g.Branch {
			case "A":
				g.EntryHost = c.Execution.EntryHostA
				g.GroupID = c.Execution.GroupAID
				g.PolicyID = c.Execution.PolicyID
			case "B":
				g.EntryHost = c.Execution.EntryHostB
				g.GroupID = c.Execution.GroupBID
				g.PolicyID = c.Execution.PolicyID
			case "R":
				if c.Target.Kind == "split_group" {
					g.GroupID = c.Execution.GroupRID
				}
			}
		}
	}
	return c, nil
}
func (s *DBService) GetEntryEvidenceCase(ctx context.Context, actor, id int64) (EntryEvidenceCase, error) {
	if err := s.prepareEntryEvidence(ctx, actor, false); err != nil {
		return EntryEvidenceCase{}, err
	}
	c, err := readEvidenceCase(ctx, s.db, id, false)
	if err == nil && c.Execution != nil {
		if refreshErr := s.refreshEntryEvidenceCaseObservations(ctx, &c); refreshErr != nil {
			c.ObservationWarning = "本轮下发覆盖统计刷新失败，不能把缺失统计当作无人获得入口"
		}
	}
	return c, err
}
func (s *DBService) ListEntryEvidenceCases(ctx context.Context, actor int64, req EntryEvidenceListRequest) (EntryEvidenceCasesPage, error) {
	current, size := normalizeEvidencePage(req.Current, req.PageSize)
	out := EntryEvidenceCasesPage{Items: []EntryEvidenceCase{}, Current: current, PageSize: size}
	if err := s.prepareEntryEvidence(ctx, actor, false); err != nil {
		return out, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_entry_evidence_case`).Scan(&out.Total); err != nil {
		return out, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM v2_entry_evidence_case ORDER BY id DESC LIMIT $1 OFFSET $2`, size, (current-1)*size)
	if err != nil {
		return out, err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return out, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	for _, id := range ids {
		c, err := readEvidenceCase(ctx, s.db, id, false)
		if errors.Is(err, ErrEntryEvidenceNotFound) {
			continue
		}
		if err != nil {
			return out, err
		}
		out.Items = append(out.Items, c)
	}
	return out, nil
}
func readEvidenceCandidates(ctx context.Context, db evidenceQueryer, id int64, selected *bool) ([]EntryEvidenceCandidate, error) {
	rows, err := db.QueryContext(ctx, `SELECT evidence FROM v2_entry_evidence_case_candidate WHERE case_id=$1 AND ($2::BOOLEAN IS NULL OR selected=$2) ORDER BY user_id`, id, selected)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EntryEvidenceCandidate{}
	for rows.Next() {
		var raw []byte
		var item EntryEvidenceCandidate
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if json.Unmarshal(raw, &item) != nil {
			return nil, ErrEntryEvidenceInvalid
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
func (s *DBService) ListEntryEvidenceCandidates(ctx context.Context, actor int64, req EntryEvidenceCandidatesRequest) (EntryEvidenceCandidatesPage, error) {
	current, size := normalizeEvidencePage(req.Current, req.PageSize)
	out := EntryEvidenceCandidatesPage{Items: []EntryEvidenceCandidate{}, Current: current, PageSize: size}
	if err := s.prepareEntryEvidence(ctx, actor, false); err != nil {
		return out, err
	}
	if _, err := readEvidenceCase(ctx, s.db, req.CaseID, false); err != nil {
		return out, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_entry_evidence_case_candidate WHERE case_id=$1 AND ($2::BOOLEAN IS NULL OR selected=$2)`, req.CaseID, req.Selected).Scan(&out.Total); err != nil {
		return out, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT evidence FROM v2_entry_evidence_case_candidate WHERE case_id=$1 AND ($2::BOOLEAN IS NULL OR selected=$2) ORDER BY user_id LIMIT $3 OFFSET $4`, req.CaseID, req.Selected, size, (current-1)*size)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		var item EntryEvidenceCandidate
		if err = rows.Scan(&raw); err != nil {
			return out, err
		}
		if json.Unmarshal(raw, &item) != nil {
			return out, ErrEntryEvidenceInvalid
		}
		out.Items = append(out.Items, item)
	}
	return out, rows.Err()
}
func (s *DBService) UpdateEntryEvidenceObservation(ctx context.Context, actor int64, req EntryEvidenceObservationRequest) (EntryEvidenceCase, error) {
	var c EntryEvidenceCase
	if err := s.prepareEntryEvidence(ctx, actor, false); err != nil {
		return c, err
	}
	if req.CaseID <= 0 || req.Revision <= 0 || utf8.RuneCountInString(req.Observation) > 8000 || utf8.RuneCountInString(req.Conclusion) > 2000 || strings.ContainsRune(req.Observation+req.Conclusion, 0) {
		return c, ErrEntryEvidenceInvalid
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
	if c.Revision != req.Revision {
		return c, ErrEntryEvidenceConflict
	}
	now := time.Now().Unix()
	if _, err = tx.ExecContext(ctx, `UPDATE v2_entry_evidence_case SET observation=$2,conclusion=$3,revision=revision+1,updated_at=$4 WHERE id=$1`, c.ID, req.Observation, req.Conclusion, now); err != nil {
		return c, err
	}
	c.Revision++
	c.Observation = req.Observation
	c.Conclusion = req.Conclusion
	c.UpdatedAt = now
	c.Preview.Revision = c.Revision
	return c, tx.Commit()
}
func (s *DBService) DeleteEntryEvidenceCase(ctx context.Context, actor, id int64) error {
	if err := s.prepareEntryEvidence(ctx, actor, true); err != nil {
		return err
	}
	if id <= 0 {
		return ErrEntryEvidenceInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = authorizeEntryEvidence(ctx, tx, actor, true); err != nil {
		return err
	}
	if _, err = readEvidenceCase(ctx, tx, id, true); err != nil {
		return err
	}
	// Deleting a dossier never reverses the routing changes it recorded.
	result, err := tx.ExecContext(ctx, `DELETE FROM v2_entry_evidence_case WHERE id=$1`, id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrEntryEvidenceNotFound
	}
	return tx.Commit()
}
func normalizeEvidenceSplit(req EntryEvidenceSplitRequest) (EntryEvidenceSplitRequest, error) {
	if req.CaseID <= 0 || req.Revision <= 0 {
		return req, ErrEntryEvidenceInvalid
	}
	if req.UseBackupIPPool {
		if strings.TrimSpace(req.EntryHostA) != "" || strings.TrimSpace(req.EntryHostB) != "" {
			return req, fmt.Errorf("%w：使用IP池时不要同时指定手动地址", ErrEntryEvidenceInvalid)
		}
		return req, nil
	}
	a, b, err := normalizeClientEntryUserPolicySplitHosts(req.EntryHostA, req.EntryHostB)
	if err != nil {
		return req, fmt.Errorf("%w：%v", ErrEntryEvidenceInvalid, err)
	}
	req.EntryHostA = a
	req.EntryHostB = b
	return req, nil
}
func evidenceSplitSameRequest(a, b EntryEvidenceSplitRequest) bool {
	// Revision is a CAS for new execution, not an idempotency discriminator.
	return a.CaseID == b.CaseID && a.UseBackupIPPool == b.UseBackupIPPool && (a.UseBackupIPPool || a.EntryHostA == b.EntryHostA && a.EntryHostB == b.EntryHostB)
}
func (s *DBService) validateEvidenceCaseTx(ctx context.Context, tx *sql.Tx, c EntryEvidenceCase) ([]EntryEvidenceCandidate, []int64, error) {
	if c.SelectedCount < 2 {
		return nil, nil, fmt.Errorf("%w：二分至少需要2名选中用户；单人档案仍可记录观察与结论", ErrEntryEvidenceInvalid)
	}
	rule, err := LoadAIEntryRule(ctx, tx, c.Target.Kind, c.Target.TargetID)
	if err != nil {
		return nil, nil, err
	}
	if rule.Revision != c.Target.Revision {
		return nil, nil, ErrEntryEvidenceConflict
	}
	topology, err := evidenceTopologyRevision(ctx, tx)
	if err != nil {
		return nil, nil, err
	}
	if topology != c.TopologyRevision {
		return nil, nil, fmt.Errorf("%w：规则、节点范围、合集或优先级已变更", ErrEntryEvidenceConflict)
	}
	var extra string
	if err = tx.QueryRowContext(ctx, `SELECT extra_nodes FROM v2_client_entry_user_policy WHERE id=$1`, rule.PolicyID).Scan(&extra); err != nil {
		return nil, nil, err
	}
	if err = evidenceRuleSafety(rule, extra); err != nil {
		return nil, nil, err
	}
	ids, err := evidenceSourceUsers(ctx, tx, rule)
	if err != nil {
		return nil, nil, err
	}
	if int64(len(ids)) != c.SourceUserCount || evidenceIDsHash(ids) != c.SourceUsersRevision {
		return nil, nil, fmt.Errorf("%w：来源用户全集已变化", ErrEntryEvidenceConflict)
	}
	yes := true
	items, err := readEvidenceCandidates(ctx, tx, c.ID, &yes)
	if err != nil {
		return nil, nil, err
	}
	if int64(len(items)) != c.SelectedCount || len(items) < 2 {
		return nil, nil, ErrEntryEvidenceConflict
	}
	if err = evidenceCandidateCoverage(rule, ids, items); err != nil {
		return nil, nil, err
	}
	if err = evidenceCurrentCoverage(ctx, tx, rule, items); err != nil {
		return nil, nil, err
	}
	var sameSort int64
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM (SELECT sort::BIGINT AS pos FROM v2_client_entry_user_policy WHERE mode<>'split' UNION ALL SELECT global_sort FROM v2_client_entry_user_policy_split_group WHERE global_sort IS NOT NULL) p WHERE pos=$1`, rule.Sort).Scan(&sameSort)
	if err != nil {
		return nil, nil, err
	}
	if sameSort != 1 {
		return nil, nil, fmt.Errorf("%w：原位置与其他规则同优先级，不能安全插入；请先整理全局排序", ErrEntryEvidenceConflict)
	}
	return items, ids, nil
}
func (s *DBService) PreviewEntryEvidenceSplit(ctx context.Context, actor int64, req EntryEvidenceSplitRequest) (EntryEvidenceSplitPreview, error) {
	var out EntryEvidenceSplitPreview
	if err := s.prepareEntryEvidence(ctx, actor, false); err != nil {
		return out, err
	}
	req, err := normalizeEvidenceSplit(req)
	if err != nil {
		return out, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	c, err := readEvidenceCase(ctx, tx, req.CaseID, true)
	if err != nil {
		return out, err
	}
	out = evidencePreviewForCase(c, req)
	if c.Status == "executed" {
		out.Supported = false
		out.UnsupportedReason = "此冻结档案已执行，请查看执行结果；再次二分需建立子轮次档案"
		return out, tx.Commit()
	}
	if c.Revision != req.Revision {
		return out, ErrEntryEvidenceConflict
	}
	if _, _, err = s.validateEvidenceCaseTx(ctx, tx, c); err != nil {
		out.Supported = false
		out.UnsupportedReason = err.Error()
		return out, nil
	}
	if !req.UseBackupIPPool && (strings.EqualFold(req.EntryHostA, c.Target.EntryHost) || strings.EqualFold(req.EntryHostB, c.Target.EntryHost) || strings.EqualFold(req.EntryHostA, c.Query.Host) || strings.EqualFold(req.EntryHostB, c.Query.Host)) {
		out.Supported = false
		out.UnsupportedReason = "A/B入口必须不同于原入口，不能把原入口当作新的排查分支"
	}
	return out, tx.Commit()
}
