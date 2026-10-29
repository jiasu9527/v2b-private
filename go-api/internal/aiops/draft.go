package aiops

import (
	"context"
	"database/sql"
	"errors"
	"time"
	"unicode/utf8"
)

type Draft struct {
	SessionID int64  `json:"session_id"`
	TurnID    int64  `json:"turn_id"`
	Round     int    `json:"round"`
	Content   string `json:"content"`
	Status    string `json:"status"`
	Version   int64  `json:"version"`
}

// Overwrite one bounded draft, never append a row per token. UPDATE-only plus
// the existing FK ensures reset/deletion cannot be undone by late stream data.
func (s *Service) updateDraft(ctx context.Context, actorID, sessionID, turnID int64, round int, content string) error {
	if len(content) > 32000 || !utf8.ValidString(content) || round < 1 || round > 6 {
		return ErrValidation
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	result, err := s.db.ExecContext(ctx, `UPDATE v2_ai_turn SET draft_content=$4,draft_round=$5,draft_version=draft_version+1 WHERE id=$1 AND session_id=$2 AND status='running' AND expires_at>$6 AND EXISTS(SELECT 1 FROM v2_user WHERE id=$3 AND is_admin=1 AND banned=0)`, turnID, sessionID, actorID, content, round, time.Now().Unix())
	if err != nil {
		return ErrUnavailable
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return ErrConflict
	}
	return nil
}

// Used by the short-lived SSE connection; a small DB snapshot also works when
// the worker and browser are routed to different backend instances.
func (s *Service) GetDraft(ctx context.Context, actorID, sessionID int64) (Draft, error) {
	var out Draft
	if sessionID <= 0 {
		return out, ErrValidation
	}
	if err := s.requireAdmin(ctx, actorID); err != nil {
		return out, err
	}
	if err := s.ensureSchema(ctx); err != nil {
		return out, err
	}
	err := s.db.QueryRowContext(ctx, `SELECT s.id,COALESCE(t.id,0),COALESCE(t.draft_round,0),CASE WHEN t.status='running' AND t.expires_at>$2 THEN t.draft_content ELSE '' END,COALESCE(t.status,'idle'),COALESCE(t.draft_version,0) FROM v2_ai_session s LEFT JOIN LATERAL (SELECT id,draft_round,draft_content,status,expires_at,draft_version FROM v2_ai_turn WHERE session_id=s.id ORDER BY id DESC LIMIT 1) t ON TRUE WHERE s.id=$1`, sessionID, time.Now().Unix()).Scan(&out.SessionID, &out.TurnID, &out.Round, &out.Content, &out.Status, &out.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return out, ErrNotFound
	}
	return out, err
}
