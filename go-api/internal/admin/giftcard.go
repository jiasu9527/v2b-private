package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type GiftcardListRequest struct {
	Current  int64
	PageSize int64
	Sort     string
	SortType string
}

type GiftcardListResult struct {
	Data  []GiftcardRecord `json:"data"`
	Total int64            `json:"total"`
}

type GiftcardRecord struct {
	ID          int64   `json:"id"`
	Code        string  `json:"code"`
	Name        string  `json:"name"`
	Type        int64   `json:"type"`
	Value       *int64  `json:"value"`
	PlanID      *int64  `json:"plan_id"`
	LimitUse    *int64  `json:"limit_use"`
	UsedUserIDs []int64 `json:"used_user_ids"`
	StartedAt   int64   `json:"started_at"`
	EndedAt     int64   `json:"ended_at"`
	CreatedAt   int64   `json:"created_at"`
	UpdatedAt   int64   `json:"updated_at"`
}

type GiftcardGenerateRequest struct {
	ID            *int64
	GenerateCount *int64
	Name          string
	Type          int64
	Value         *int64
	PlanID        *int64
	StartedAt     int64
	EndedAt       int64
	LimitUse      *int64
	Code          *string
}

type giftcardRow struct {
	ID          int64
	Code        string
	Name        string
	Type        int64
	Value       sql.NullInt64
	PlanID      sql.NullInt64
	LimitUse    sql.NullInt64
	UsedUserIDs sql.NullString
	StartedAt   int64
	EndedAt     int64
	CreatedAt   int64
	UpdatedAt   int64
}

func (s *DBService) ListGiftcards(ctx context.Context, req GiftcardListRequest) (GiftcardListResult, error) {
	if s.db == nil {
		return GiftcardListResult{}, ErrUnavailable
	}

	current := req.Current
	if current <= 0 {
		current = 1
	}
	pageSize := req.PageSize
	if pageSize < 10 {
		pageSize = 10
	}
	sort := sanitizeAdminSort(req.Sort, []string{"id", "name", "type", "value", "plan_id", "started_at", "ended_at", "created_at", "updated_at"}, "id")
	sortType := sanitizeAdminSortType(req.SortType)

	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_giftcard`).Scan(&total); err != nil {
		return GiftcardListResult{}, fmt.Errorf("count giftcards: %w", err)
	}

	offset := (current - 1) * pageSize
	query := fmt.Sprintf(`SELECT id, code, name, type, value, plan_id, limit_use, used_user_ids, started_at, ended_at, created_at, updated_at
FROM v2_giftcard
ORDER BY %s %s
LIMIT $1 OFFSET $2`, sort, sortType)
	rows, err := s.db.QueryContext(ctx, query, pageSize, offset)
	if err != nil {
		return GiftcardListResult{}, fmt.Errorf("query giftcards: %w", err)
	}
	defer rows.Close()

	result := make([]GiftcardRecord, 0)
	for rows.Next() {
		row, err := scanGiftcardRow(rows)
		if err != nil {
			return GiftcardListResult{}, err
		}
		result = append(result, giftcardRecord(row))
	}
	if err := rows.Err(); err != nil {
		return GiftcardListResult{}, fmt.Errorf("iterate giftcards: %w", err)
	}

	return GiftcardListResult{Data: result, Total: total}, nil
}

func (s *DBService) GenerateGiftcard(ctx context.Context, req GiftcardGenerateRequest) (string, bool, error) {
	if s.db == nil {
		return "", false, ErrUnavailable
	}

	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		return "", false, errors.New("名称不能为空")
	}
	if req.Type < 1 || req.Type > 5 {
		return "", false, errors.New("类型格式有误")
	}
	if req.Type != 4 && req.Value == nil {
		return "", false, errors.New("数值不能为空")
	}
	if req.Type == 5 && req.PlanID == nil {
		return "", false, errors.New("订阅不能为空")
	}

	now := time.Now().Unix()
	if req.GenerateCount != nil && *req.GenerateCount > 0 {
		count := *req.GenerateCount
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return "", false, errors.New("礼品卡批量生成失败")
		}
		defer tx.Rollback()

		rows := make([]GiftcardRecord, 0, count)
		seen := make(map[string]struct{})
		for i := int64(0); i < count; i++ {
			code, err := s.uniqueGiftcardCode(ctx, tx, seen)
			if err != nil {
				return "", false, errors.New("礼品卡批量生成失败")
			}
			record := GiftcardRecord{
				Code:      code,
				Name:      req.Name,
				Type:      req.Type,
				Value:     req.Value,
				PlanID:    req.PlanID,
				LimitUse:  req.LimitUse,
				StartedAt: req.StartedAt,
				EndedAt:   req.EndedAt,
				CreatedAt: now,
				UpdatedAt: now,
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO v2_giftcard (
code, name, type, value, plan_id, limit_use, started_at, ended_at, created_at, updated_at
) VALUES (
$1, $2, $3, $4, $5, $6, $7, $8, $9, $10
)`,
				record.Code,
				record.Name,
				record.Type,
				nullableInt64(record.Value),
				nullableInt64(record.PlanID),
				nullableInt64(record.LimitUse),
				record.StartedAt,
				record.EndedAt,
				record.CreatedAt,
				record.UpdatedAt,
			); err != nil {
				return "", false, errors.New("礼品卡批量生成失败")
			}
			rows = append(rows, record)
		}
		if err := tx.Commit(); err != nil {
			return "", false, errors.New("礼品卡批量生成失败")
		}
		return giftcardCSV(rows), true, nil
	}

	code := trimmedStringPtr(req.Code)
	if req.ID == nil && code == nil {
		generated, err := randomAlphaNumeric(16)
		if err != nil {
			return "", false, errors.New("礼品卡创建失败")
		}
		code = &generated
	}

	if req.ID == nil {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO v2_giftcard (
code, name, type, value, plan_id, limit_use, started_at, ended_at, created_at, updated_at
) VALUES (
$1, $2, $3, $4, $5, $6, $7, $8, $9, $10
)`,
			nullableString(code),
			req.Name,
			req.Type,
			nullableInt64(req.Value),
			nullableInt64(req.PlanID),
			nullableInt64(req.LimitUse),
			req.StartedAt,
			req.EndedAt,
			now,
			now,
		); err != nil {
			return "", false, errors.New("礼品卡创建失败")
		}
		return "", false, nil
	}

	if ok, err := s.giftcardExists(ctx, *req.ID); err != nil {
		return "", false, err
	} else if !ok {
		return "", false, errors.New("礼品卡不存在")
	}

	if _, err := s.db.ExecContext(ctx, `UPDATE v2_giftcard
SET code = COALESCE($2, code),
	name = $3,
	type = $4,
	value = $5,
	plan_id = $6,
	limit_use = $7,
	started_at = $8,
	ended_at = $9,
	updated_at = $10
WHERE id = $1`,
		*req.ID,
		nullableString(code),
		req.Name,
		req.Type,
		nullableInt64(req.Value),
		nullableInt64(req.PlanID),
		nullableInt64(req.LimitUse),
		req.StartedAt,
		req.EndedAt,
		now,
	); err != nil {
		return "", false, errors.New("礼品卡保存失败")
	}
	return "", false, nil
}

func (s *DBService) DeleteGiftcard(ctx context.Context, id int64) (bool, error) {
	if s.db == nil {
		return false, ErrUnavailable
	}
	if id <= 0 {
		return false, errors.New("未找到礼品卡")
	}
	if ok, err := s.giftcardExists(ctx, id); err != nil {
		return false, err
	} else if !ok {
		return false, errors.New("礼品卡不存在")
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM v2_giftcard WHERE id = $1`, id)
	if err != nil {
		return false, errors.New("删除失败")
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, errors.New("删除失败")
	}
	return affected > 0, nil
}

func (s *DBService) uniqueGiftcardCode(ctx context.Context, tx *sql.Tx, seen map[string]struct{}) (string, error) {
	for attempt := 0; attempt < 10; attempt++ {
		code, err := randomAlphaNumeric(16)
		if err != nil {
			return "", err
		}
		if _, ok := seen[code]; ok {
			continue
		}
		var exists int
		err = tx.QueryRowContext(ctx, `SELECT 1 FROM v2_giftcard WHERE code = $1 LIMIT 1`, code).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			seen[code] = struct{}{}
			return code, nil
		}
		if err != nil {
			return "", err
		}
	}
	return "", errors.New("unable to allocate giftcard code")
}

func (s *DBService) giftcardExists(ctx context.Context, id int64) (bool, error) {
	var exists int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM v2_giftcard WHERE id = $1 LIMIT 1`, id).Scan(&exists)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return false, fmt.Errorf("query giftcard exists: %w", err)
}

func scanGiftcardRow(scanner interface{ Scan(...any) error }) (giftcardRow, error) {
	var row giftcardRow
	if err := scanner.Scan(
		&row.ID,
		&row.Code,
		&row.Name,
		&row.Type,
		&row.Value,
		&row.PlanID,
		&row.LimitUse,
		&row.UsedUserIDs,
		&row.StartedAt,
		&row.EndedAt,
		&row.CreatedAt,
		&row.UpdatedAt,
	); err != nil {
		return giftcardRow{}, err
	}
	return row, nil
}

func giftcardRecord(row giftcardRow) GiftcardRecord {
	return GiftcardRecord{
		ID:          row.ID,
		Code:        row.Code,
		Name:        row.Name,
		Type:        row.Type,
		Value:       nullInt64Ptr(row.Value),
		PlanID:      nullInt64Ptr(row.PlanID),
		LimitUse:    nullInt64Ptr(row.LimitUse),
		UsedUserIDs: decodeInt64Slice(row.UsedUserIDs),
		StartedAt:   row.StartedAt,
		EndedAt:     row.EndedAt,
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
	}
}

func giftcardCSV(rows []GiftcardRecord) string {
	var builder strings.Builder
	builder.WriteString("名称,类型,数值,开始时间,结束时间,可用次数,礼品卡卡密,生成时间\r\n")
	for _, giftcard := range rows {
		typeText := map[int64]string{1: "金额", 2: "时长", 3: "流量", 4: "重置", 5: "套餐"}[giftcard.Type]
		valueText := "-"
		switch giftcard.Type {
		case 1:
			if giftcard.Value != nil {
				valueText = strconv.FormatFloat(float64(*giftcard.Value)/100, 'f', 2, 64)
			}
		case 2, 5:
			if giftcard.Value != nil {
				valueText = strconv.FormatInt(*giftcard.Value, 10) + "天"
			}
		case 3:
			if giftcard.Value != nil {
				valueText = strconv.FormatInt(*giftcard.Value, 10) + "GB"
			}
		}
		limitUse := "不限制"
		if giftcard.LimitUse != nil {
			limitUse = strconv.FormatInt(*giftcard.LimitUse, 10)
		}
		builder.WriteString(fmt.Sprintf("%s,%s,%s,%s,%s,%s,%s,%s\r\n",
			giftcard.Name,
			typeText,
			valueText,
			time.Unix(giftcard.StartedAt, 0).Format("2006-01-02 15:04:05"),
			time.Unix(giftcard.EndedAt, 0).Format("2006-01-02 15:04:05"),
			limitUse,
			giftcard.Code,
			time.Unix(giftcard.CreatedAt, 0).Format("2006-01-02 15:04:05"),
		))
	}
	return builder.String()
}
