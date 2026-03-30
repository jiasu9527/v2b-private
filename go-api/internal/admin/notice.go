package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type NoticeRecord struct {
	ID        int64    `json:"id"`
	Title     string   `json:"title"`
	Content   string   `json:"content"`
	Show      int64    `json:"show"`
	ImgURL    *string  `json:"img_url"`
	Tags      []string `json:"tags"`
	CreatedAt int64    `json:"created_at"`
	UpdatedAt int64    `json:"updated_at"`
}

type NoticeSaveRequest struct {
	ID      *int64
	Title   string
	Content string
	ImgURL  *string
	Tags    []string
}

type noticeRow struct {
	ID        int64
	Title     string
	Content   string
	Show      int64
	ImgURL    sql.NullString
	Tags      sql.NullString
	CreatedAt int64
	UpdatedAt int64
}

func (s *DBService) ListNotices(ctx context.Context) ([]NoticeRecord, error) {
	if s.db == nil {
		return nil, ErrUnavailable
	}

	rows, err := s.db.QueryContext(ctx, `SELECT id, title, content, "show", img_url, tags, created_at, updated_at
FROM v2_notice
ORDER BY id DESC`)
	if err != nil {
		return nil, fmt.Errorf("query notices: %w", err)
	}
	defer rows.Close()

	result := make([]NoticeRecord, 0)
	for rows.Next() {
		row, err := scanNoticeRow(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, noticeRecord(row))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate notices: %w", err)
	}
	return result, nil
}

func (s *DBService) SaveNotice(ctx context.Context, req NoticeSaveRequest) (bool, error) {
	if s.db == nil {
		return false, ErrUnavailable
	}

	req.Title = strings.TrimSpace(req.Title)
	req.Content = strings.TrimSpace(req.Content)
	if req.Title == "" {
		return false, errors.New("标题不能为空")
	}
	if req.Content == "" {
		return false, errors.New("内容不能为空")
	}

	now := time.Now().Unix()
	tagsJSON, err := encodeNoticeTags(req.Tags)
	if err != nil {
		return false, errors.New("标签格式不正确")
	}

	if req.ID == nil {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO v2_notice (title, content, img_url, tags, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6)`,
			req.Title,
			req.Content,
			nullableString(req.ImgURL),
			tagsJSON,
			now,
			now,
		); err != nil {
			return false, errors.New("保存失败")
		}
		return true, nil
	}

	result, err := s.db.ExecContext(ctx, `UPDATE v2_notice
SET title = $2, content = $3, img_url = $4, tags = $5, updated_at = $6
WHERE id = $1`,
		*req.ID,
		req.Title,
		req.Content,
		nullableString(req.ImgURL),
		tagsJSON,
		now,
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

func (s *DBService) ToggleNotice(ctx context.Context, id int64) (bool, error) {
	if s.db == nil {
		return false, ErrUnavailable
	}
	if id <= 0 {
		return false, errors.New("参数有误")
	}

	var show int64
	err := s.db.QueryRowContext(ctx, `SELECT "show" FROM v2_notice WHERE id = $1 LIMIT 1`, id).Scan(&show)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, errors.New("公告不存在")
		}
		return false, fmt.Errorf("query notice: %w", err)
	}

	nextShow := int64(1)
	if show != 0 {
		nextShow = 0
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE v2_notice SET "show" = $2, updated_at = $3 WHERE id = $1`, id, nextShow, time.Now().Unix()); err != nil {
		return false, errors.New("保存失败")
	}
	return true, nil
}

func (s *DBService) DeleteNotice(ctx context.Context, id int64) (bool, error) {
	if s.db == nil {
		return false, ErrUnavailable
	}
	if id <= 0 {
		return false, errors.New("参数错误")
	}

	if ok, err := s.noticeExists(ctx, id); err != nil {
		return false, err
	} else if !ok {
		return false, errors.New("公告不存在")
	}

	result, err := s.db.ExecContext(ctx, `DELETE FROM v2_notice WHERE id = $1`, id)
	if err != nil {
		return false, errors.New("删除失败")
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, errors.New("删除失败")
	}
	return affected > 0, nil
}

func (s *DBService) noticeExists(ctx context.Context, id int64) (bool, error) {
	var exists int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM v2_notice WHERE id = $1 LIMIT 1`, id).Scan(&exists)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return false, fmt.Errorf("query notice exists: %w", err)
}

func scanNoticeRow(scanner interface{ Scan(...any) error }) (noticeRow, error) {
	var row noticeRow
	if err := scanner.Scan(
		&row.ID,
		&row.Title,
		&row.Content,
		&row.Show,
		&row.ImgURL,
		&row.Tags,
		&row.CreatedAt,
		&row.UpdatedAt,
	); err != nil {
		return noticeRow{}, err
	}
	return row, nil
}

func noticeRecord(row noticeRow) NoticeRecord {
	return NoticeRecord{
		ID:        row.ID,
		Title:     row.Title,
		Content:   row.Content,
		Show:      row.Show,
		ImgURL:    nullStringPtr(row.ImgURL),
		Tags:      decodeNoticeTags(row.Tags),
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
	}
}

func encodeNoticeTags(tags []string) (any, error) {
	if len(tags) == 0 {
		return nil, nil
	}
	filtered := make([]string, 0, len(tags))
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		filtered = append(filtered, tag)
	}
	if len(filtered) == 0 {
		return nil, nil
	}
	raw, err := json.Marshal(filtered)
	if err != nil {
		return nil, err
	}
	return string(raw), nil
}

func decodeNoticeTags(raw sql.NullString) []string {
	if !raw.Valid || strings.TrimSpace(raw.String) == "" {
		return nil
	}
	var tags []string
	if err := json.Unmarshal([]byte(raw.String), &tags); err == nil {
		return tags
	}
	parts := strings.Split(raw.String, ",")
	tags = make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			tags = append(tags, part)
		}
	}
	return tags
}
