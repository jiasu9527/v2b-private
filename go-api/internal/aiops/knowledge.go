package aiops

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

const maxKnowledgeEntries = 500
const knowledgeColumns = `id,title,summary,content,category,status,pinned,version,source_kind,source_note,source_session_id,source_turn_id,created_by,updated_by,created_at,updated_at`
const knowledgeListColumns = `id,title,summary,'' AS content,category,status,pinned,version,source_kind,source_note,source_session_id,source_turn_id,created_by,updated_by,created_at,updated_at`

func ensureKnowledgeSchema(ctx context.Context, tx *sql.Tx) error {
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS v2_ai_knowledge(id BIGSERIAL PRIMARY KEY,title TEXT NOT NULL,summary TEXT NOT NULL DEFAULT '',content TEXT NOT NULL,category TEXT NOT NULL CHECK(category IN('architecture','server','network','deployment','troubleshooting','other')),status TEXT NOT NULL CHECK(status IN('draft','active','disabled')),pinned BOOLEAN NOT NULL DEFAULT FALSE,version BIGINT NOT NULL DEFAULT 1,source_kind TEXT NOT NULL CHECK(source_kind IN('manual','ai')),source_note TEXT NOT NULL DEFAULT '',source_session_id BIGINT NOT NULL DEFAULT 0,source_turn_id BIGINT NOT NULL DEFAULT 0,created_by BIGINT NOT NULL,updated_by BIGINT NOT NULL,created_at BIGINT NOT NULL,updated_at BIGINT NOT NULL,dedupe_key TEXT NOT NULL DEFAULT '')`,
		`CREATE INDEX IF NOT EXISTS v2_ai_knowledge_status_idx ON v2_ai_knowledge(status,pinned DESC,updated_at DESC,id DESC)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS v2_ai_knowledge_dedupe_idx ON v2_ai_knowledge(dedupe_key) WHERE dedupe_key<>''`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

func scanKnowledge(row interface{ Scan(...any) error }) (KnowledgeEntry, error) {
	var k KnowledgeEntry
	err := row.Scan(&k.ID, &k.Title, &k.Summary, &k.Content, &k.Category, &k.Status, &k.Pinned, &k.Version, &k.SourceKind, &k.SourceNote, &k.SourceSessionID, &k.SourceTurnID, &k.CreatedBy, &k.UpdatedBy, &k.CreatedAt, &k.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return k, err
}

func knowledgeCategory(category string) bool {
	switch category {
	case "architecture", "server", "network", "deployment", "troubleshooting", "other":
		return true
	}
	return false
}
func knowledgeStatus(status string) bool {
	return status == "draft" || status == "active" || status == "disabled"
}
func validKnowledgeText(value string, limit int) bool {
	return utf8.ValidString(value) && utf8.RuneCountInString(value) <= limit && !strings.ContainsRune(value, 0)
}
func normalizeKnowledgeRequest(req KnowledgeSaveRequest) (KnowledgeSaveRequest, error) {
	req.Title, req.Summary, req.Content = strings.TrimSpace(req.Title), strings.TrimSpace(req.Summary), strings.TrimSpace(req.Content)
	if req.ID < 0 || req.Title == "" || req.Content == "" || !validKnowledgeText(req.Title, 120) || !validKnowledgeText(req.Summary, 500) || !validKnowledgeText(req.Content, 12000) || !knowledgeCategory(req.Category) || (req.Status != "" && !knowledgeStatus(req.Status)) {
		return req, ErrValidation
	}
	if req.ID > 0 && (req.ExpectedVersion == nil || *req.ExpectedVersion < 1) {
		return req, ErrValidation
	}
	if req.ID == 0 && req.ExpectedVersion != nil && *req.ExpectedVersion != 0 {
		return req, ErrValidation
	}
	if err := validateKnowledgeContent(req.Title, req.Summary, req.Content); err != nil {
		return req, err
	}
	return req, nil
}
func normalizeKnowledgeList(req KnowledgeListRequest) (KnowledgeListRequest, error) {
	req.Query = strings.TrimSpace(req.Query)
	if req.Current == 0 {
		req.Current = 1
	}
	if req.PageSize == 0 {
		req.PageSize = 20
	}
	if req.Current < 1 || req.Current > 100000 || req.PageSize < 1 || req.PageSize > 100 || !validKnowledgeText(req.Query, 200) || (req.Status != "" && !knowledgeStatus(req.Status)) || (req.Category != "" && !knowledgeCategory(req.Category)) {
		return req, ErrValidation
	}
	return req, nil
}
func knowledgeActorTx(ctx context.Context, tx *sql.Tx, actor int64) (bool, error) {
	var admin bool
	err := tx.QueryRowContext(ctx, `SELECT is_admin=1 FROM v2_user WHERE id=$1 AND banned=0 AND (is_admin=1 OR is_staff=1) FOR SHARE`, actor).Scan(&admin)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrForbidden
	}
	return admin, err
}
func (s *Service) knowledgeTx(ctx context.Context, actor int64, write bool) (*sql.Tx, bool, error) {
	if err := s.requireOperator(ctx, actor); err != nil {
		return nil, false, err
	}
	if err := s.ensureSchema(ctx); err != nil {
		return nil, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	if write {
		if err = lockAIState(ctx, tx); err != nil {
			tx.Rollback()
			return nil, false, err
		}
	}
	admin, err := knowledgeActorTx(ctx, tx, actor)
	if err != nil {
		tx.Rollback()
		return nil, false, err
	}
	if write {
		_, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(2026102904)`)
		if err != nil {
			tx.Rollback()
			return nil, false, err
		}
	}
	return tx, admin, nil
}

func (s *Service) ListKnowledge(ctx context.Context, actor int64, req KnowledgeListRequest) (KnowledgeListResult, error) {
	var out KnowledgeListResult
	req, err := normalizeKnowledgeList(req)
	if err != nil {
		return out, err
	}
	tx, admin, err := s.knowledgeTx(ctx, actor, false)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	out = KnowledgeListResult{Data: []KnowledgeEntry{}, Current: req.Current, PageSize: req.PageSize}
	where := ` WHERE ($1::BOOLEAN OR status='active' OR (status='draft' AND created_by=$2)) AND ($3='' OR status=$3) AND ($4='' OR category=$4) AND ($5='' OR strpos(lower(title),lower($5))>0 OR strpos(lower(summary),lower($5))>0 OR strpos(lower(content),lower($5))>0)`
	args := []any{admin, actor, req.Status, req.Category, req.Query}
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_ai_knowledge`+where, args...).Scan(&out.Total); err != nil {
		return out, err
	}
	args = append(args, req.PageSize, (req.Current-1)*req.PageSize)
	rows, err := tx.QueryContext(ctx, `SELECT `+knowledgeListColumns+` FROM v2_ai_knowledge`+where+` ORDER BY pinned DESC,updated_at DESC,id DESC LIMIT $6 OFFSET $7`, args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		k, e := scanKnowledge(rows)
		if e != nil {
			return out, e
		}
		out.Data = append(out.Data, k)
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	rows.Close()
	return out, tx.Commit()
}

func (s *Service) GetKnowledge(ctx context.Context, actor, id int64) (KnowledgeEntry, error) {
	if id <= 0 {
		return KnowledgeEntry{}, ErrValidation
	}
	tx, admin, err := s.knowledgeTx(ctx, actor, false)
	if err != nil {
		return KnowledgeEntry{}, err
	}
	defer tx.Rollback()
	k, err := scanKnowledge(tx.QueryRowContext(ctx, `SELECT `+knowledgeColumns+` FROM v2_ai_knowledge WHERE id=$1 AND ($2::BOOLEAN OR status='active' OR (status='draft' AND created_by=$3))`, id, admin, actor))
	if err != nil {
		return KnowledgeEntry{}, err
	}
	return k, tx.Commit()
}

func (s *Service) SaveKnowledge(ctx context.Context, actor int64, req KnowledgeSaveRequest) (KnowledgeEntry, error) {
	return s.saveKnowledge(ctx, actor, 0, 0, req, "")
}
func (s *Service) SaveKnowledgeFromAI(ctx context.Context, actor, session, turn int64, req KnowledgeSaveRequest, sourceNote string) (KnowledgeEntry, error) {
	if session <= 0 || turn <= 0 {
		return KnowledgeEntry{}, ErrValidation
	}
	return s.saveKnowledge(ctx, actor, session, turn, req, sourceNote)
}
func (s *Service) saveKnowledge(ctx context.Context, actor, session, turn int64, req KnowledgeSaveRequest, sourceNote string) (KnowledgeEntry, error) {
	var empty KnowledgeEntry
	req, err := normalizeKnowledgeRequest(req)
	if err != nil {
		return empty, err
	}
	sourceNote = strings.TrimSpace(sourceNote)
	if !validKnowledgeText(sourceNote, 500) {
		return empty, ErrValidation
	}
	if err = validateKnowledgeContent(sourceNote); err != nil {
		return empty, err
	}
	tx, admin, err := s.knowledgeTx(ctx, actor, true)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	if !admin && req.ID != 0 {
		return empty, ErrForbidden
	}
	k := KnowledgeEntry{SourceKind: "manual", CreatedBy: actor, CreatedAt: time.Now().Unix()}
	if session > 0 {
		if err = s.requireSessionAccessTx(ctx, tx, actor, session); err != nil {
			return empty, err
		}
		var activeTurn int64
		err = tx.QueryRowContext(ctx, `SELECT t.id FROM v2_ai_turn t JOIN v2_ai_config c ON c.id=1 WHERE t.id=$1 AND t.session_id=$2 AND t.actor_id=$3 AND t.status='running' AND NOT t.stop_requested AND t.expires_at>$4 AND c.enabled FOR SHARE OF t,c`, turn, session, actor, time.Now().Unix()).Scan(&activeTurn)
		if errors.Is(err, sql.ErrNoRows) {
			return empty, ErrForbidden
		}
		if err != nil {
			return empty, err
		}
		k.SourceKind, k.SourceNote, k.SourceSessionID, k.SourceTurnID = "ai", sourceNote, session, turn
		if admin {
			req.Status = "active"
		}
	}
	if !admin {
		req.Status, req.Pinned = "draft", false
	} else if req.Status == "" {
		req.Status = "active"
	}
	dedupe := ""
	if session > 0 && req.ID == 0 {
		// Transport defaults (nil versus zero expected_version) are not part
		// of the knowledge content and must not create duplicate AI drafts.
		canonical := req
		canonical.ExpectedVersion = nil
		raw, _ := json.Marshal(canonical)
		dedupe = hashOperation(fmt.Sprintf("%d:%d:%d:", actor, session, turn) + string(raw))
		existing, e := scanKnowledge(tx.QueryRowContext(ctx, `SELECT `+knowledgeColumns+` FROM v2_ai_knowledge WHERE dedupe_key=$1`, dedupe))
		if e == nil {
			if !admin && existing.Status != "active" && !(existing.Status == "draft" && existing.CreatedBy == actor) {
				return empty, ErrConflict
			}
			return existing, tx.Commit()
		}
		if !errors.Is(e, ErrNotFound) {
			return empty, e
		}
	}
	if req.ID > 0 {
		old, e := scanKnowledge(tx.QueryRowContext(ctx, `SELECT `+knowledgeColumns+` FROM v2_ai_knowledge WHERE id=$1 FOR UPDATE`, req.ID))
		if e != nil {
			return empty, e
		}
		if old.Version != *req.ExpectedVersion {
			return empty, ErrConflict
		}
		k.ID, k.Version, k.CreatedBy, k.CreatedAt = old.ID, old.Version, old.CreatedBy, old.CreatedAt
		if session == 0 {
			k.SourceKind, k.SourceNote, k.SourceSessionID, k.SourceTurnID = old.SourceKind, old.SourceNote, old.SourceSessionID, old.SourceTurnID
		}
	} else {
		var count int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_ai_knowledge`).Scan(&count); err != nil {
			return empty, err
		}
		if count >= maxKnowledgeEntries {
			return empty, ErrBusy
		}
	}
	k.Title, k.Summary, k.Content, k.Category, k.Status, k.Pinned = req.Title, req.Summary, req.Content, req.Category, req.Status, req.Pinned
	k.UpdatedBy, k.UpdatedAt = actor, time.Now().Unix()
	if k.ID == 0 {
		k, err = scanKnowledge(tx.QueryRowContext(ctx, `INSERT INTO v2_ai_knowledge(title,summary,content,category,status,pinned,source_kind,source_note,source_session_id,source_turn_id,created_by,updated_by,created_at,updated_at,dedupe_key) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$11,$12,$12,$13) RETURNING `+knowledgeColumns, k.Title, k.Summary, k.Content, k.Category, k.Status, k.Pinned, k.SourceKind, k.SourceNote, k.SourceSessionID, k.SourceTurnID, actor, k.CreatedAt, dedupe))
	} else {
		k, err = scanKnowledge(tx.QueryRowContext(ctx, `UPDATE v2_ai_knowledge SET title=$2,summary=$3,content=$4,category=$5,status=$6,pinned=$7,version=version+1,source_kind=$8,source_note=$9,source_session_id=$10,source_turn_id=$11,updated_by=$12,updated_at=$13 WHERE id=$1 AND version=$14 RETURNING `+knowledgeColumns, k.ID, k.Title, k.Summary, k.Content, k.Category, k.Status, k.Pinned, k.SourceKind, k.SourceNote, k.SourceSessionID, k.SourceTurnID, actor, k.UpdatedAt, k.Version))
	}
	if err != nil {
		return empty, err
	}
	return k, tx.Commit()
}

func (s *Service) DeleteKnowledge(ctx context.Context, actor, id, expectedVersion int64) error {
	if id <= 0 || expectedVersion <= 0 {
		return ErrValidation
	}
	tx, admin, err := s.knowledgeTx(ctx, actor, true)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if !admin {
		return ErrForbidden
	}
	var version int64
	err = tx.QueryRowContext(ctx, `SELECT version FROM v2_ai_knowledge WHERE id=$1 FOR UPDATE`, id).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if version != expectedVersion {
		return ErrConflict
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM v2_ai_knowledge WHERE id=$1`, id); err != nil {
		return err
	}
	return tx.Commit()
}
