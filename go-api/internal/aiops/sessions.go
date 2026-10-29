package aiops

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

const sessionColumns = `id,title,model,created_by,status,created_at,updated_at,access_mode,full_access_by,access_version`
const turnColumns = `id,session_id,request_id,status,error,created_at,completed_at,draft_content,draft_round,draft_version,access_mode,stop_requested`

func scanSession(row interface{ Scan(...any) error }) (Session, error) {
	var v Session
	err := row.Scan(&v.ID, &v.Title, &v.Model, &v.CreatedBy, &v.Status, &v.CreatedAt, &v.UpdatedAt, &v.AccessMode, &v.FullAccessBy, &v.AccessVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrNotFound
	}
	return v, err
}
func scanTurn(row interface{ Scan(...any) error }) (Turn, error) {
	var v Turn
	err := row.Scan(&v.ID, &v.SessionID, &v.RequestID, &v.Status, &v.Error, &v.CreatedAt, &v.CompletedAt, &v.DraftContent, &v.DraftRound, &v.DraftVersion, &v.AccessMode, &v.StopRequested)
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrNotFound
	}
	return v, err
}
func (s *Service) expireTurns(ctx context.Context, tx *sql.Tx) error {
	now := time.Now().Unix()
	_, err := tx.ExecContext(ctx, `WITH locked AS (SELECT id FROM v2_ai_session WHERE id IN(SELECT session_id FROM v2_ai_turn WHERE status='running' AND expires_at<=$1) FOR UPDATE SKIP LOCKED), expired AS (UPDATE v2_ai_turn SET status='interrupted',error='处理已中断，请检查结果后使用新请求继续',completed_at=$1,draft_content='' WHERE status='running' AND expires_at<=$1 AND session_id IN(SELECT id FROM locked) RETURNING session_id) UPDATE v2_ai_session SET status='idle',updated_at=$1 WHERE id IN(SELECT session_id FROM expired)`, now)
	return err
}
func (s *Service) ListSessions(ctx context.Context, actorID int64) ([]Session, error) {
	if err := s.requireOperator(ctx, actorID); err != nil {
		return nil, err
	}
	if err := s.ensureSchema(ctx); err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err = s.expireTurns(ctx, tx); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+sessionColumns+` FROM v2_ai_session WHERE EXISTS(SELECT 1 FROM v2_user u WHERE u.id=$1 AND u.banned=0 AND (u.is_admin=1 OR (u.is_staff=1 AND created_by=u.id))) ORDER BY updated_at DESC,id DESC LIMIT 200`, actorID)
	if err != nil {
		return nil, err
	}
	out := []Session{}
	for rows.Next() {
		v, err := scanSession(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	return out, tx.Commit()
}
func (s *Service) GetSession(ctx context.Context, actorID, id int64) (SessionDetail, error) {
	var out SessionDetail
	if id <= 0 {
		return out, ErrValidation
	}
	if err := s.requireOperator(ctx, actorID); err != nil {
		return out, err
	}
	if err := s.ensureSchema(ctx); err != nil {
		return out, err
	}
	// Final messages replace the live draft in one write transaction. Read all
	// three parts from one snapshot so that transition cannot hide both at once.
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if err = s.requireSessionAccessTx(ctx, tx, actorID, id); err != nil {
		return out, err
	}
	if err = s.expireTurns(ctx, tx); err != nil {
		return out, err
	}
	out.Session, err = scanSession(tx.QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM v2_ai_session WHERE id=$1`, id))
	if err != nil {
		return out, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,session_id,turn_id,role,content,tool_calls::TEXT,tool_call_id,created_at FROM (SELECT * FROM v2_ai_message WHERE session_id=$1 ORDER BY id DESC LIMIT 400) recent ORDER BY id`, id)
	if err != nil {
		return out, err
	}
	out.Messages = []Message{}
	for rows.Next() {
		var m Message
		var raw string
		if err = rows.Scan(&m.ID, &m.SessionID, &m.TurnID, &m.Role, &m.Content, &raw, &m.ToolCallID, &m.CreatedAt); err != nil {
			rows.Close()
			return out, err
		}
		if json.Unmarshal([]byte(raw), &m.ToolCalls) != nil {
			rows.Close()
			return out, ErrUnavailable
		}
		out.Messages = append(out.Messages, m)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if len(out.Messages) == 400 {
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM v2_ai_message WHERE session_id=$1 AND id<$2)`, id, out.Messages[0].ID).Scan(&out.MessagesTruncated); err != nil {
			return out, err
		}
	}
	rows, err = tx.QueryContext(ctx, `SELECT `+turnColumns+` FROM v2_ai_turn WHERE session_id=$1 ORDER BY id DESC LIMIT 200`, id)
	if err != nil {
		return out, err
	}
	out.Turns = []Turn{}
	for rows.Next() {
		v, err := scanTurn(rows)
		if err != nil {
			rows.Close()
			return out, err
		}
		out.Turns = append(out.Turns, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	return out, tx.Commit()
}
func (s *Service) validateModel(ctx context.Context, model string) error {
	cfg, _, err := s.loadConfig(ctx)
	if err != nil {
		return err
	}
	for _, m := range cfg.Models {
		if m == model {
			return nil
		}
	}
	return ErrValidation
}
func (s *Service) CreateSession(ctx context.Context, actorID int64, req CreateSessionRequest) (Session, error) {
	var empty Session
	if err := s.requireOperator(ctx, actorID); err != nil {
		return empty, err
	}
	if err := s.ensureSchema(ctx); err != nil {
		return empty, err
	}
	if err := s.validateModel(ctx, req.Model); err != nil {
		return empty, err
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = "新运维会话"
	}
	if !utf8.ValidString(title) || utf8.RuneCountInString(title) > 200 {
		return empty, ErrValidation
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	if err = lockAIState(ctx, tx); err != nil {
		return empty, err
	}
	if err = s.requireOperatorTx(ctx, tx, actorID); err != nil {
		return empty, err
	}
	v, err := insertSession(ctx, tx, actorID, title, req.Model)
	if err != nil {
		return empty, err
	}
	return v, tx.Commit()
}
func insertSession(ctx context.Context, tx *sql.Tx, actorID int64, title, model string) (Session, error) {
	var empty Session
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(2026102904)`); err != nil {
		return empty, err
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_ai_session`).Scan(&count); err != nil {
		return empty, err
	}
	if count >= maxSessions {
		return empty, ErrBusy
	}
	now := time.Now().Unix()
	return scanSession(tx.QueryRowContext(ctx, `INSERT INTO v2_ai_session(title,model,created_by,status,created_at,updated_at,access_mode,full_access_by) VALUES($1,$2,$3,'idle',$4,$4,'full',$3) RETURNING `+sessionColumns, title, model, actorID, now))
}
func (s *Service) DeleteSession(ctx context.Context, actorID, id int64) error {
	if id <= 0 {
		return ErrValidation
	}
	return s.deleteData(ctx, actorID, id, false)
}
func (s *Service) ClearSessions(ctx context.Context, actorID int64) error {
	return s.deleteData(ctx, actorID, 0, false)
}
func (s *Service) ResetData(ctx context.Context, actorID int64) error {
	return s.deleteData(ctx, actorID, 0, true)
}
func (s *Service) deleteData(ctx context.Context, actorID, id int64, reset bool) error {
	if err := s.requireAdmin(ctx, actorID); err != nil {
		return err
	}
	if err := s.ensureSchema(ctx); err != nil {
		return err
	}
	if id > 0 {
		if err := s.requireSessionAccess(ctx, actorID, id); err != nil {
			return err
		}
	}
	// A prior process may have died after claiming an external operation. Such
	// operations remain unknown, never retried, but must not block cleanup forever.
	if err := s.reconcileExpiredActions(ctx); err != nil {
		return err
	}
	var inFlight bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM v2_ai_action WHERE status='running' AND ($1::BIGINT=0 OR session_id=$1))`, id).Scan(&inFlight); err != nil {
		return err
	}
	if inFlight {
		return ErrBusy
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockAIStateExclusive(ctx, tx); err != nil {
		return err
	}
	if err = s.requireAdminTx(ctx, tx, actorID); err != nil {
		return err
	}
	if id > 0 {
		if err = s.requireSessionAccessTx(ctx, tx, actorID, id); err != nil {
			return err
		}
		if err = s.requireSessionTx(ctx, tx, id); err != nil {
			return err
		}
	}
	var running bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM v2_ai_action WHERE status='running' AND ($1::BIGINT=0 OR session_id=$1))`, id).Scan(&running); err != nil {
		return err
	}
	if running {
		return ErrBusy
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM v2_ai_session WHERE $1::BIGINT=0 OR id=$1`, id); err != nil {
		return err
	}
	if reset {
		if _, err = tx.ExecContext(ctx, `DELETE FROM v2_ai_server`); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM v2_ai_config`); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err = tx.Commit(); err != nil {
		return err
	}
	if id == 0 {
		for key, cancel := range s.workers {
			if key > 0 {
				cancel()
			}
		}
	} else if cancel := s.workers[id]; cancel != nil {
		cancel()
	}
	return nil
}
