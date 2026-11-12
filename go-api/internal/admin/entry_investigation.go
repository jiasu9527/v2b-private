package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
	"unicode/utf8"
)

func normalizeInvestigationStart(r EntryInvestigationStartRequest, now int64) (EntryInvestigationStartRequest, error) {
	r.Note = strings.TrimSpace(r.Note)
	if r.MinObserveSeconds == 0 {
		r.MinObserveSeconds = 86400
	}
	if r.MaxRounds == 0 {
		r.MaxRounds = 10
	}
	if r.StopUsers == 0 {
		r.StopUsers = 8
	}
	if r.CaseID <= 0 || r.Revision <= 0 || r.BlockedAt <= 0 || r.BlockedAt > now || r.Note == "" || utf8.RuneCountInString(r.Note) > 2000 || strings.ContainsRune(r.Note, 0) || r.MinObserveSeconds < 300 || r.MinObserveSeconds > 7*86400 || r.MaxRounds < 1 || r.MaxRounds > 30 || r.StopUsers < 2 || r.StopUsers > 100 {
		return r, ErrEntryEvidenceInvalid
	}
	return r, nil
}
func readEntryInvestigation(ctx context.Context, q evidenceQueryer, id int64, lock bool) (EntryInvestigation, error) {
	var t EntryInvestigation
	var settings, failures, report []byte
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	err := q.QueryRowContext(ctx, `SELECT case_id,root_case_id,round,revision,created_by,created_at,updated_at,settings,failures,report,COALESCE(next_case_id,0) FROM v2_entry_investigation WHERE case_id=$1`+suffix, id).Scan(&t.CaseID, &t.RootCaseID, &t.Round, &t.Revision, &t.CreatedBy, &t.CreatedAt, &t.UpdatedAt, &settings, &failures, &report, &t.NextCaseID)
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrEntryEvidenceNotFound
	}
	if err != nil {
		return t, err
	}
	for _, x := range []struct {
		raw []byte
		dst any
	}{{settings, &t.Settings}, {failures, &t.Failures}, {report, &t.Report}} {
		if err = json.Unmarshal(x.raw, x.dst); err != nil {
			return t, ErrEntryEvidenceInvalid
		}
	}
	return t, nil
}
func insertEntryInvestigation(ctx context.Context, tx *sql.Tx, t EntryInvestigation) error {
	settings, err := json.Marshal(t.Settings)
	if err != nil {
		return err
	}
	report, err := json.Marshal(t.Report)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO v2_entry_investigation(case_id,root_case_id,round,created_by,created_at,updated_at,settings,report,checked_at) VALUES($1,$2,$3,$4,$5,$5,$6,$7,$5)`, t.CaseID, t.RootCaseID, t.Round, t.CreatedBy, t.CreatedAt, string(settings), string(report))
	return err
}
func (s *DBService) StartEntryInvestigation(ctx context.Context, actor int64, req EntryInvestigationStartRequest) (EntryInvestigation, error) {
	var out EntryInvestigation
	if err := s.prepareEntryEvidence(ctx, actor, false); err != nil {
		return out, err
	}
	req, err := normalizeInvestigationStart(req, time.Now().Unix())
	if err != nil {
		return out, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if err = authorizeEntryEvidence(ctx, tx, actor, false); err != nil {
		return out, err
	}
	c, err := readEvidenceCase(ctx, tx, req.CaseID, true)
	if err != nil {
		return out, err
	}
	existing, err := readEntryInvestigation(ctx, tx, c.ID, false)
	if err == nil {
		if existing.Settings != req {
			return out, ErrEntryEvidenceConflict
		}
		return existing, tx.Commit()
	}
	if !errors.Is(err, ErrEntryEvidenceNotFound) {
		return out, err
	}
	if c.Status != "frozen" || c.Execution != nil || c.Revision != req.Revision || c.Query.To != req.BlockedAt || net.ParseIP(c.Query.Host) == nil || !strings.EqualFold(c.Query.Host, c.Target.EntryHost) {
		return out, fmt.Errorf("%w：请使用故障前截止、目标为实际IP的未执行冻结档案；blocked_at必须等于查询to", ErrEntryEvidenceInvalid)
	}
	// A child must be created through next-round so inherited limits and lineage
	// cannot be accidentally reset by starting an independent task on its dossier.
	if c.ParentCaseID != 0 {
		return out, fmt.Errorf("%w：子档案请从上一轮任务生成", ErrEntryEvidenceInvalid)
	}
	if _, _, err = s.validateEvidenceCaseTx(ctx, tx, c); err != nil {
		return out, err
	}
	now := time.Now().Unix()
	out = EntryInvestigation{CaseID: c.ID, RootCaseID: c.ID, Round: 1, Revision: 1, CreatedBy: actor, CreatedAt: now, UpdatedAt: now, Settings: req, Failures: []EntryInvestigationFailure{}}
	out.Report, err = s.entryInvestigationReportTx(ctx, tx, out, c, now)
	if err != nil {
		return out, err
	}
	if err = insertEntryInvestigation(ctx, tx, out); err != nil {
		return out, err
	}
	return out, tx.Commit()
}

// GET only reads persisted reports. It is safe during uncertain prior writes.
func (s *DBService) GetEntryInvestigation(ctx context.Context, actor, id int64) (EntryInvestigation, error) {
	if err := s.prepareEntryEvidence(ctx, actor, false); err != nil {
		return EntryInvestigation{}, err
	}
	if id <= 0 {
		return EntryInvestigation{}, ErrEntryEvidenceInvalid
	}
	return readEntryInvestigation(ctx, s.db, id, false)
}
func (s *DBService) ListEntryInvestigations(ctx context.Context, actor int64, req EntryEvidenceListRequest) (EntryInvestigationsPage, error) {
	current, size := normalizeEvidencePage(req.Current, req.PageSize)
	out := EntryInvestigationsPage{Items: []EntryInvestigation{}, Current: current, PageSize: size}
	if err := s.prepareEntryEvidence(ctx, actor, false); err != nil {
		return out, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM v2_entry_investigation`).Scan(&out.Total); err != nil {
		return out, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT case_id FROM v2_entry_investigation ORDER BY case_id DESC LIMIT $1 OFFSET $2`, size, (current-1)*size)
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
		t, err := readEntryInvestigation(ctx, tx, id, false)
		if err != nil {
			return out, err
		}
		out.Items = append(out.Items, t)
	}
	return out, tx.Commit()
}
func saveEntryInvestigationReport(ctx context.Context, tx *sql.Tx, t *EntryInvestigation, report EntryInvestigationReport) error {
	// Polling must not invalidate a human confirmation for a mere clock tick.
	// CAS binds operator decisions; live evidence is always revalidated on next.
	raw, err := json.Marshal(report)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE v2_entry_investigation SET report=$2,checked_at=$3 WHERE case_id=$1`, t.CaseID, string(raw), report.CheckedAt)
	if err == nil {
		t.Report = report
	}
	return err
}
func (s *DBService) refreshEntryInvestigation(ctx context.Context, id, actor int64) (EntryInvestigation, error) {
	var out EntryInvestigation
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if actor > 0 {
		if err = authorizeEntryEvidence(ctx, tx, actor, false); err != nil {
			return out, err
		}
	}
	out, err = readEntryInvestigation(ctx, tx, id, true)
	if err != nil {
		return out, err
	}
	c, err := readEvidenceCase(ctx, tx, id, false)
	if err != nil {
		return out, err
	}
	report, err := s.entryInvestigationReportTx(ctx, tx, out, c, time.Now().Unix())
	if err != nil {
		return out, err
	}
	if err = saveEntryInvestigationReport(ctx, tx, &out, report); err != nil {
		return out, err
	}
	return out, tx.Commit()
}
func (s *DBService) RefreshEntryInvestigation(ctx context.Context, actor, id int64) (EntryInvestigation, error) {
	if err := s.prepareEntryEvidence(ctx, actor, false); err != nil {
		return EntryInvestigation{}, err
	}
	if id <= 0 {
		return EntryInvestigation{}, ErrEntryEvidenceInvalid
	}
	return s.refreshEntryInvestigation(ctx, id, actor)
}
func (s *DBService) ConfirmEntryInvestigationFailure(ctx context.Context, actor int64, req EntryInvestigationConfirmRequest) (EntryInvestigation, error) {
	var out EntryInvestigation
	if err := s.prepareEntryEvidence(ctx, actor, false); err != nil {
		return out, err
	}
	now := time.Now().Unix()
	req.Note = strings.TrimSpace(req.Note)
	if req.CaseID <= 0 || req.Revision <= 0 || (req.Branch != "A" && req.Branch != "B") || req.BlockedAt <= 0 || req.BlockedAt > now || req.Note == "" || utf8.RuneCountInString(req.Note) > 2000 || strings.ContainsRune(req.Note, 0) || (req.ControlStatus != "healthy" && req.ControlStatus != "abnormal" && req.ControlStatus != "unknown") {
		return out, ErrEntryEvidenceInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if err = authorizeEntryEvidence(ctx, tx, actor, false); err != nil {
		return out, err
	}
	out, err = readEntryInvestigation(ctx, tx, req.CaseID, true)
	if err != nil {
		return out, err
	}
	// A replay of the same confirmation is safe even if its revision is old.
	for _, f := range investigationLatestFailures(out.Failures) {
		if f.Branch == req.Branch {
			if f.BlockedAt == req.BlockedAt && f.Note == req.Note && f.ControlStatus == req.ControlStatus {
				return out, tx.Commit()
			}
			// Corrections append an audit entry; earlier human statements remain preserved.
		}
	}
	if out.Revision != req.Revision || len(out.Failures) >= 100 {
		return out, ErrEntryEvidenceConflict
	}
	c, err := readEvidenceCase(ctx, tx, req.CaseID, false)
	if err != nil {
		return out, err
	}
	if c.Execution == nil || req.BlockedAt <= c.Execution.ExecutedAt {
		return out, ErrEntryEvidenceInvalid
	}
	out.Failures = append(out.Failures, EntryInvestigationFailure{Branch: req.Branch, BlockedAt: req.BlockedAt, ConfirmedAt: now, ConfirmedBy: actor, Note: req.Note, ControlStatus: req.ControlStatus})
	report, err := s.entryInvestigationReportTx(ctx, tx, out, c, now)
	if err != nil {
		return out, err
	}
	raw, _ := json.Marshal(out.Failures)
	_, err = tx.ExecContext(ctx, `UPDATE v2_entry_investigation SET failures=$2,revision=revision+1,updated_at=$3 WHERE case_id=$1`, out.CaseID, string(raw), now)
	if err != nil {
		return out, err
	}
	out.Revision++
	out.UpdatedAt = now
	if err = saveEntryInvestigationReport(ctx, tx, &out, report); err != nil {
		return out, err
	}
	return out, tx.Commit()
}
func (s *DBService) NextEntryInvestigationRound(ctx context.Context, actor int64, req EntryInvestigationNextRequest) (EntryInvestigation, error) {
	var out EntryInvestigation
	if err := s.prepareEntryEvidence(ctx, actor, false); err != nil {
		return out, err
	}
	if req.CaseID <= 0 || req.Revision <= 0 {
		return out, ErrEntryEvidenceInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if err = authorizeEntryEvidence(ctx, tx, actor, false); err != nil {
		return out, err
	}
	parent, err := readEntryInvestigation(ctx, tx, req.CaseID, true)
	if err != nil {
		return out, err
	}
	if parent.NextCaseID > 0 {
		out, err = readEntryInvestigation(ctx, tx, parent.NextCaseID, false)
		if err != nil {
			return out, err
		}
		return out, tx.Commit()
	}
	if parent.Revision != req.Revision {
		return out, ErrEntryEvidenceConflict
	}
	// Editors use this same lock. Hold it before report checks, not only when
	// previewing, so scope cannot change between validation and freezing.
	if err = lockClientEntryVisibleOrder(ctx, tx); err != nil {
		return out, err
	}
	c, err := readEvidenceCase(ctx, tx, parent.CaseID, false)
	if err != nil {
		return out, err
	}
	if reason, e := s.investigationAncestorBlock(ctx, tx, parent, true); e != nil {
		return out, e
	} else if reason != "" {
		return out, fmt.Errorf("%w：%s", ErrEntryEvidenceConflict, reason)
	}
	now := time.Now().Unix()
	report, err := s.entryInvestigationReportTx(ctx, tx, parent, c, now)
	if err != nil {
		return out, err
	}
	if report.State != "ready_next" || report.NextQuery == nil {
		return out, fmt.Errorf("%w：%s", ErrEntryEvidenceConflict, report.Reason)
	}
	q, err := normalizeEvidenceQuery(*report.NextQuery)
	if err != nil {
		return out, err
	}
	// Preview + freeze share the topology lock and one transaction. No thousands
	// of IDs pass through the model and no orphan case survives a failed task link.
	preview, candidates, _, err := s.evidencePreviewTx(ctx, tx, q)
	if err != nil {
		return out, err
	}
	if !preview.FreezeAllowed || preview.Total <= parent.Settings.StopUsers {
		return out, fmt.Errorf("%w：候选不足或范围已变化，请人工复核", ErrEntryEvidenceConflict)
	}
	branch := "A"
	if c.Execution == nil {
		return out, ErrEntryEvidenceConflict
	}
	if q.TargetID == c.Execution.GroupBID {
		branch = "B"
	}
	parentCandidates, err := readEvidenceCandidates(ctx, tx, c.ID, nil)
	if err != nil {
		return out, err
	}
	allowed := map[int64]bool{}
	for _, item := range parentCandidates {
		if item.Selected && item.Branch == branch {
			allowed[item.UserID] = true
		}
	}
	for _, item := range candidates {
		if !allowed[item.UserID] {
			return out, fmt.Errorf("%w：下一轮出现原冻结分支以外用户，需人工复核", ErrEntryEvidenceConflict)
		}
	}
	child, err := s.createEntryEvidenceCaseTx(ctx, tx, actor, CreateEntryEvidenceCaseRequest{EntryEvidenceQueryRequest: q, Revision: preview.Target.Revision, ExpectedCandidatesHash: preview.CandidatesHash, Title: fmt.Sprintf("排查 #%d · 第 %d 轮", parent.RootCaseID, parent.Round+1), ParentCaseID: c.ID}, q)
	if err != nil {
		return out, err
	}
	settings := parent.Settings
	settings.CaseID = child.ID
	settings.Revision = child.Revision
	settings.BlockedAt = q.To
	settings.Note = "沿用上轮人工确认的异常及观察证据"
	out = EntryInvestigation{CaseID: child.ID, RootCaseID: parent.RootCaseID, Round: parent.Round + 1, Revision: 1, CreatedBy: actor, CreatedAt: now, UpdatedAt: now, Settings: settings, Failures: []EntryInvestigationFailure{}}
	report.State = "continued"
	report.Reason = "下一轮名单已冻结，尚未修改入口；原另一分支仍保留观察"
	report.NextQuery = nil
	parentRaw, _ := json.Marshal(report)
	_, err = tx.ExecContext(ctx, `UPDATE v2_entry_investigation SET next_case_id=$2,revision=revision+1,updated_at=$3,checked_at=$3,report=$4 WHERE case_id=$1`, parent.CaseID, child.ID, now, string(parentRaw))
	if err != nil {
		return out, err
	}
	out.Report, err = s.entryInvestigationReportTx(ctx, tx, out, child, now)
	if err != nil {
		return out, err
	}
	if err = insertEntryInvestigation(ctx, tx, out); err != nil {
		return out, err
	}
	return out, tx.Commit()
}

// Bounded durable maintenance, independent of chat/model lifetime. Reads source
// evidence and writes report metadata only; it cannot split, ban or rotate IPs.
func (s *DBService) RefreshEntryInvestigations(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT case_id FROM v2_entry_investigation ORDER BY checked_at,case_id LIMIT 20`)
	if err != nil {
		return err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var combined error
	for _, id := range ids {
		started := time.Now().Unix()
		if _, err = s.refreshEntryInvestigation(ctx, id, 0); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			combined = errors.Join(combined, err)
			// A broken task must not starve others or preserve stale ready_next.
			_, _ = s.db.ExecContext(ctx, `UPDATE v2_entry_investigation SET checked_at=$2,report=(report - 'next_query') || jsonb_build_object('state','needs_review','reason','证据刷新失败，请复核后台；不能沿用旧的下一轮建议','checked_at',$2::BIGINT,'requires_confirmation',true) WHERE case_id=$1 AND checked_at<=$2`, id, started)
		}
	}
	return combined
}
