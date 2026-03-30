package user

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

func (s *DBService) Knowledges(ctx context.Context, language, keyword string) (map[string][]map[string]any, error) {
	if s.db == nil {
		return nil, ErrUnavailable
	}

	language = strings.TrimSpace(language)
	keyword = strings.TrimSpace(keyword)

	query := `SELECT id, category, title, updated_at
FROM v2_knowledge
WHERE language = $1 AND "show" = 1`
	args := []any{language}
	if keyword != "" {
		query += fmt.Sprintf(" AND (title ILIKE $%d OR body ILIKE $%d)", len(args)+1, len(args)+2)
		like := "%" + keyword + "%"
		args = append(args, like, like)
	}
	query += ` ORDER BY sort ASC NULLS LAST, id ASC`

	rows, err := s.queryRowsAsMaps(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query knowledges: %w", err)
	}

	grouped := make(map[string][]map[string]any)
	for _, row := range rows {
		category := strings.TrimSpace(fmt.Sprint(row["category"]))
		grouped[category] = append(grouped[category], row)
	}
	return grouped, nil
}

func (s *DBService) KnowledgeDetail(ctx context.Context, userID, id int64) (map[string]any, error) {
	if s.db == nil {
		return nil, ErrUnavailable
	}
	if id <= 0 {
		return nil, errors.New("Article does not exist")
	}

	knowledge, err := s.querySingleMap(ctx, `SELECT id, category, title, body, language, sort, "show", created_at, updated_at
FROM v2_knowledge
WHERE id = $1 AND "show" = 1
LIMIT 1`, id)
	if err != nil {
		return nil, fmt.Errorf("query knowledge detail: %w", err)
	}
	if knowledge == nil {
		return nil, errors.New("Article does not exist")
	}

	var (
		token          string
		banned         int64
		transferEnable int64
		expiredAt      sql.NullInt64
	)
	err = s.db.QueryRowContext(ctx, `SELECT token, banned, transfer_enable, expired_at
FROM v2_user
WHERE id = $1
LIMIT 1`, userID).Scan(&token, &banned, &transferEnable, &expiredAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("query knowledge user: %w", err)
	}

	body := fmt.Sprint(knowledge["body"])
	if !knowledgeUserAvailable(banned, transferEnable, expiredAt, time.Now().Unix()) {
		body = replaceKnowledgeAccessSections(body)
	}

	subscribeURL, err := s.buildSubscribeURL(ctx, userID, token)
	if err != nil {
		return nil, err
	}
	body = applyKnowledgeTemplate(body, fallbackString(s.cfg.AppName, "V2Board"), subscribeURL, token)
	knowledge["body"] = body
	return knowledge, nil
}

func (s *DBService) KnowledgeCategories(ctx context.Context, language string) ([]string, error) {
	if s.db == nil {
		return nil, ErrUnavailable
	}

	language = strings.TrimSpace(language)
	var (
		rows *sql.Rows
		err  error
	)
	if language == "" {
		rows, err = s.db.QueryContext(ctx, `SELECT DISTINCT category
FROM v2_knowledge
WHERE "show" = 1
ORDER BY category ASC`)
	} else {
		rows, err = s.db.QueryContext(ctx, `SELECT DISTINCT category
FROM v2_knowledge
WHERE "show" = 1 AND language = $1
ORDER BY category ASC`, language)
	}
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
		if category == "" {
			continue
		}
		result = append(result, category)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate knowledge categories: %w", err)
	}
	return result, nil
}

func knowledgeUserAvailable(banned, transferEnable int64, expiredAt sql.NullInt64, now int64) bool {
	return banned == 0 && transferEnable > 0 && (!expiredAt.Valid || expiredAt.Int64 > now)
}

func replaceKnowledgeAccessSections(body string) string {
	const (
		startMarker = "<!--access start-->"
		endMarker   = "<!--access end-->"
		replacement = `<div class="v2board-no-access">You must have a valid subscription to view content in this area</div>`
	)

	for {
		start := strings.Index(body, startMarker)
		if start < 0 {
			return body
		}
		end := strings.Index(body[start+len(startMarker):], endMarker)
		if end < 0 {
			return body
		}
		end += start + len(startMarker)
		body = body[:start] + replacement + body[end+len(endMarker):]
	}
}

func applyKnowledgeTemplate(body, siteName, subscribeURL, token string) string {
	body = strings.ReplaceAll(body, "{{siteName}}", siteName)
	body = strings.ReplaceAll(body, "{{subscribeUrl}}", subscribeURL)
	body = strings.ReplaceAll(body, "{{urlEncodeSubscribeUrl}}", url.QueryEscape(subscribeURL))
	body = strings.ReplaceAll(body, "{{safeBase64SubscribeUrl}}", safeBase64Encode(subscribeURL))
	body = strings.ReplaceAll(body, "{{subscribeToken}}", token)
	return body
}

func safeBase64Encode(value string) string {
	encoded := base64.StdEncoding.EncodeToString([]byte(value))
	encoded = strings.ReplaceAll(encoded, "+", "-")
	encoded = strings.ReplaceAll(encoded, "/", "_")
	encoded = strings.ReplaceAll(encoded, "=", "")
	return encoded
}
