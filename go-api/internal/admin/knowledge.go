package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type KnowledgeRecord struct {
	ID        int64  `json:"id"`
	Language  string `json:"language"`
	Category  string `json:"category"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	Sort      *int64 `json:"sort,omitempty"`
	Show      int64  `json:"show"`
	CreatedAt int64  `json:"created_at,omitempty"`
	UpdatedAt int64  `json:"updated_at"`
}

type KnowledgeSaveRequest struct {
	ID       *int64
	Language string
	Category string
	Title    string
	Body     string
}

type knowledgeRow struct {
	ID        int64
	Language  string
	Category  string
	Title     string
	Body      string
	Sort      sql.NullInt64
	Show      int64
	CreatedAt int64
	UpdatedAt int64
}

func (s *DBService) ListKnowledges(ctx context.Context) ([]KnowledgeRecord, error) {
	if s.db == nil {
		return nil, ErrUnavailable
	}

	rows, err := s.db.QueryContext(ctx, `SELECT id, language, category, title, '' AS body, sort, "show", 0 AS created_at, updated_at
FROM v2_knowledge
ORDER BY sort ASC NULLS LAST, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("query knowledges: %w", err)
	}
	defer rows.Close()

	result := make([]KnowledgeRecord, 0)
	for rows.Next() {
		row, err := scanKnowledgeRow(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, knowledgeRecord(row))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate knowledges: %w", err)
	}
	return result, nil
}

func (s *DBService) GetKnowledge(ctx context.Context, id int64) (KnowledgeRecord, error) {
	if s.db == nil {
		return KnowledgeRecord{}, ErrUnavailable
	}

	row, err := scanKnowledgeRow(s.db.QueryRowContext(ctx, `SELECT id, language, category, title, body, sort, "show", created_at, updated_at
FROM v2_knowledge
WHERE id = $1
LIMIT 1`, id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return KnowledgeRecord{}, errors.New("知识不存在")
		}
		return KnowledgeRecord{}, fmt.Errorf("query knowledge: %w", err)
	}
	return knowledgeRecord(row), nil
}

func (s *DBService) ListKnowledgeCategories(ctx context.Context) ([]string, error) {
	if s.db == nil {
		return nil, ErrUnavailable
	}

	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT category FROM v2_knowledge ORDER BY category ASC`)
	if err != nil {
		return nil, fmt.Errorf("query knowledge categories: %w", err)
	}
	defer rows.Close()

	result := make([]string, 0)
	for rows.Next() {
		var category string
		if err := rows.Scan(&category); err != nil {
			return nil, fmt.Errorf("scan knowledge category: %w", err)
		}
		category = strings.TrimSpace(category)
		if category != "" {
			result = append(result, category)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate knowledge categories: %w", err)
	}
	return result, nil
}

func (s *DBService) SaveKnowledge(ctx context.Context, req KnowledgeSaveRequest) (bool, error) {
	if s.db == nil {
		return false, ErrUnavailable
	}

	req.Title = strings.TrimSpace(req.Title)
	req.Category = strings.TrimSpace(req.Category)
	req.Language = strings.TrimSpace(req.Language)
	req.Body = strings.TrimSpace(req.Body)
	if req.Title == "" {
		return false, errors.New("标题不能为空")
	}
	if req.Category == "" {
		return false, errors.New("分类不能为空")
	}
	if req.Language == "" {
		return false, errors.New("语言不能为空")
	}
	if req.Body == "" {
		return false, errors.New("内容不能为空")
	}

	now := time.Now().Unix()
	if req.ID == nil {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO v2_knowledge (language, category, title, body, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6)`,
			req.Language, req.Category, req.Title, req.Body, now, now,
		); err != nil {
			return false, errors.New("创建失败")
		}
		return true, nil
	}

	result, err := s.db.ExecContext(ctx, `UPDATE v2_knowledge
SET language = $2, category = $3, title = $4, body = $5, updated_at = $6
WHERE id = $1`,
		*req.ID, req.Language, req.Category, req.Title, req.Body, now,
	)
	if err != nil {
		return false, errors.New("保存失败")
	}
	affected, err := result.RowsAffected()
	if err != nil || affected == 0 {
		return false, errors.New("保存失败")
	}
	return true, nil
}

func (s *DBService) ToggleKnowledge(ctx context.Context, id int64) (bool, error) {
	if s.db == nil {
		return false, ErrUnavailable
	}
	if id <= 0 {
		return false, errors.New("参数有误")
	}
	var show int64
	err := s.db.QueryRowContext(ctx, `SELECT "show" FROM v2_knowledge WHERE id = $1 LIMIT 1`, id).Scan(&show)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, errors.New("知识不存在")
		}
		return false, fmt.Errorf("query knowledge show: %w", err)
	}
	nextShow := int64(1)
	if show != 0 {
		nextShow = 0
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE v2_knowledge SET "show" = $2, updated_at = $3 WHERE id = $1`, id, nextShow, time.Now().Unix()); err != nil {
		return false, errors.New("保存失败")
	}
	return true, nil
}

func (s *DBService) SortKnowledges(ctx context.Context, ids []int64) (bool, error) {
	if s.db == nil {
		return false, ErrUnavailable
	}
	if len(ids) == 0 {
		return false, errors.New("知识ID不能为空")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, errors.New("保存失败")
	}
	defer tx.Rollback()
	for idx, id := range ids {
		result, err := tx.ExecContext(ctx, `UPDATE v2_knowledge SET sort = $2 WHERE id = $1`, id, idx+1)
		if err != nil {
			return false, errors.New("保存失败")
		}
		affected, err := result.RowsAffected()
		if err != nil || affected == 0 {
			return false, errors.New("保存失败")
		}
	}
	if err := tx.Commit(); err != nil {
		return false, errors.New("保存失败")
	}
	return true, nil
}

func (s *DBService) DeleteKnowledge(ctx context.Context, id int64) (bool, error) {
	if s.db == nil {
		return false, ErrUnavailable
	}
	if id <= 0 {
		return false, errors.New("参数有误")
	}
	if ok, err := s.knowledgeExists(ctx, id); err != nil {
		return false, err
	} else if !ok {
		return false, errors.New("知识不存在")
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM v2_knowledge WHERE id = $1`, id)
	if err != nil {
		return false, errors.New("删除失败")
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, errors.New("删除失败")
	}
	return affected > 0, nil
}

func (s *DBService) knowledgeExists(ctx context.Context, id int64) (bool, error) {
	var exists int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM v2_knowledge WHERE id = $1 LIMIT 1`, id).Scan(&exists)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return false, fmt.Errorf("query knowledge exists: %w", err)
}

func scanKnowledgeRow(scanner interface{ Scan(...any) error }) (knowledgeRow, error) {
	var row knowledgeRow
	if err := scanner.Scan(
		&row.ID,
		&row.Language,
		&row.Category,
		&row.Title,
		&row.Body,
		&row.Sort,
		&row.Show,
		&row.CreatedAt,
		&row.UpdatedAt,
	); err != nil {
		return knowledgeRow{}, err
	}
	return row, nil
}

func knowledgeRecord(row knowledgeRow) KnowledgeRecord {
	return KnowledgeRecord{
		ID:        row.ID,
		Language:  row.Language,
		Category:  row.Category,
		Title:     row.Title,
		Body:      row.Body,
		Sort:      nullInt64Ptr(row.Sort),
		Show:      row.Show,
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
	}
}
