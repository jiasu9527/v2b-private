package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type CouponListRequest struct {
	Current  int64
	PageSize int64
	Sort     string
	SortType string
}

type CouponListResult struct {
	Data  []CouponRecord `json:"data"`
	Total int64          `json:"total"`
}

type CouponRecord struct {
	ID               int64    `json:"id"`
	Code             string   `json:"code"`
	Name             string   `json:"name"`
	Type             int64    `json:"type"`
	Value            int64    `json:"value"`
	Show             int64    `json:"show"`
	LimitUse         *int64   `json:"limit_use"`
	LimitUseWithUser *int64   `json:"limit_use_with_user"`
	LimitPlanIDs     []int64  `json:"limit_plan_ids"`
	LimitPeriod      []string `json:"limit_period"`
	StartedAt        int64    `json:"started_at"`
	EndedAt          int64    `json:"ended_at"`
	CreatedAt        int64    `json:"created_at"`
	UpdatedAt        int64    `json:"updated_at"`
}

type CouponGenerateRequest struct {
	ID               *int64
	GenerateCount    *int64
	Name             string
	Type             int64
	Value            int64
	StartedAt        int64
	EndedAt          int64
	LimitUse         *int64
	LimitUseWithUser *int64
	LimitPlanIDs     []int64
	LimitPeriod      []string
	Code             *string
}

type couponRow struct {
	ID               int64
	Code             string
	Name             string
	Type             int64
	Value            int64
	Show             int64
	LimitUse         sql.NullInt64
	LimitUseWithUser sql.NullInt64
	LimitPlanIDs     sql.NullString
	LimitPeriod      sql.NullString
	StartedAt        int64
	EndedAt          int64
	CreatedAt        int64
	UpdatedAt        int64
}

func (s *DBService) ListCoupons(ctx context.Context, req CouponListRequest) (CouponListResult, error) {
	if s.db == nil {
		return CouponListResult{}, ErrUnavailable
	}

	current := req.Current
	if current <= 0 {
		current = 1
	}
	pageSize := req.PageSize
	if pageSize < 10 {
		pageSize = 10
	}
	sort := sanitizeAdminSort(req.Sort, []string{"id", "name", "type", "value", "show", "started_at", "ended_at", "created_at", "updated_at"}, "id")
	sortType := sanitizeAdminSortType(req.SortType)

	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_coupon`).Scan(&total); err != nil {
		return CouponListResult{}, fmt.Errorf("count coupons: %w", err)
	}

	offset := (current - 1) * pageSize
	query := fmt.Sprintf(`SELECT id, code, name, type, value, "show", limit_use, limit_use_with_user, limit_plan_ids, limit_period, started_at, ended_at, created_at, updated_at
FROM v2_coupon
ORDER BY %s %s
LIMIT $1 OFFSET $2`, sort, sortType)
	rows, err := s.db.QueryContext(ctx, query, pageSize, offset)
	if err != nil {
		return CouponListResult{}, fmt.Errorf("query coupons: %w", err)
	}
	defer rows.Close()

	result := make([]CouponRecord, 0)
	for rows.Next() {
		row, err := scanCouponRow(rows)
		if err != nil {
			return CouponListResult{}, err
		}
		result = append(result, couponRecord(row))
	}
	if err := rows.Err(); err != nil {
		return CouponListResult{}, fmt.Errorf("iterate coupons: %w", err)
	}

	return CouponListResult{Data: result, Total: total}, nil
}

func (s *DBService) GenerateCoupon(ctx context.Context, req CouponGenerateRequest) (string, bool, error) {
	if s.db == nil {
		return "", false, ErrUnavailable
	}

	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		return "", false, errors.New("名称不能为空")
	}
	if req.Type != 1 && req.Type != 2 {
		return "", false, errors.New("类型格式有误")
	}
	now := time.Now().Unix()
	planIDsJSON, err := encodeInt64SliceJSON(req.LimitPlanIDs)
	if err != nil {
		return "", false, errors.New("指定订阅格式有误")
	}
	periodJSON, err := encodeStringSliceJSON(req.LimitPeriod)
	if err != nil {
		return "", false, errors.New("指定周期格式有误")
	}

	if req.GenerateCount != nil && *req.GenerateCount > 0 {
		count := *req.GenerateCount
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return "", false, errors.New("生成失败")
		}
		defer tx.Rollback()

		rows := make([]CouponRecord, 0, count)
		for i := int64(0); i < count; i++ {
			code, err := randomAlphaNumeric(8)
			if err != nil {
				return "", false, errors.New("生成失败")
			}
			record := CouponRecord{
				Code:             code,
				Name:             req.Name,
				Type:             req.Type,
				Value:            req.Value,
				Show:             1,
				LimitUse:         req.LimitUse,
				LimitUseWithUser: req.LimitUseWithUser,
				LimitPlanIDs:     append([]int64(nil), req.LimitPlanIDs...),
				LimitPeriod:      append([]string(nil), req.LimitPeriod...),
				StartedAt:        req.StartedAt,
				EndedAt:          req.EndedAt,
				CreatedAt:        now,
				UpdatedAt:        now,
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO v2_coupon (
code, name, type, value, "show", limit_use, limit_use_with_user, limit_plan_ids, limit_period, started_at, ended_at, created_at, updated_at
) VALUES (
$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13
)`,
				record.Code,
				record.Name,
				record.Type,
				record.Value,
				record.Show,
				nullableInt64(record.LimitUse),
				nullableInt64(record.LimitUseWithUser),
				planIDsJSON,
				periodJSON,
				record.StartedAt,
				record.EndedAt,
				record.CreatedAt,
				record.UpdatedAt,
			); err != nil {
				return "", false, errors.New("生成失败")
			}
			rows = append(rows, record)
		}
		if err := tx.Commit(); err != nil {
			return "", false, errors.New("生成失败")
		}
		return couponCSV(rows), true, nil
	}

	code := trimmedStringPtr(req.Code)
	if req.ID == nil && code == nil {
		generated, err := randomAlphaNumeric(8)
		if err != nil {
			return "", false, errors.New("创建失败")
		}
		code = &generated
	}

	if req.ID == nil {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO v2_coupon (
code, name, type, value, limit_use, limit_use_with_user, limit_plan_ids, limit_period, started_at, ended_at, created_at, updated_at
) VALUES (
$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12
)`,
			nullableString(code),
			req.Name,
			req.Type,
			req.Value,
			nullableInt64(req.LimitUse),
			nullableInt64(req.LimitUseWithUser),
			planIDsJSON,
			periodJSON,
			req.StartedAt,
			req.EndedAt,
			now,
			now,
		); err != nil {
			return "", false, errors.New("创建失败")
		}
		return "", false, nil
	}

	result, err := s.db.ExecContext(ctx, `UPDATE v2_coupon
SET code = COALESCE($2, code),
	name = $3,
	type = $4,
	value = $5,
	limit_use = $6,
	limit_use_with_user = $7,
	limit_plan_ids = $8,
	limit_period = $9,
	started_at = $10,
	ended_at = $11,
	updated_at = $12
WHERE id = $1`,
		*req.ID,
		nullableString(code),
		req.Name,
		req.Type,
		req.Value,
		nullableInt64(req.LimitUse),
		nullableInt64(req.LimitUseWithUser),
		planIDsJSON,
		periodJSON,
		req.StartedAt,
		req.EndedAt,
		now,
	)
	if err != nil {
		return "", false, errors.New("保存失败")
	}
	affected, err := result.RowsAffected()
	if err != nil || affected == 0 {
		return "", false, errors.New("保存失败")
	}
	return "", false, nil
}

func (s *DBService) ToggleCoupon(ctx context.Context, id int64) (bool, error) {
	if s.db == nil {
		return false, ErrUnavailable
	}
	if id <= 0 {
		return false, errors.New("参数有误")
	}

	var show int64
	err := s.db.QueryRowContext(ctx, `SELECT "show" FROM v2_coupon WHERE id = $1 LIMIT 1`, id).Scan(&show)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, errors.New("优惠券不存在")
		}
		return false, fmt.Errorf("query coupon: %w", err)
	}
	nextShow := int64(1)
	if show != 0 {
		nextShow = 0
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE v2_coupon SET "show" = $2, updated_at = $3 WHERE id = $1`, id, nextShow, time.Now().Unix()); err != nil {
		return false, errors.New("保存失败")
	}
	return true, nil
}

func (s *DBService) DeleteCoupon(ctx context.Context, id int64) (bool, error) {
	if s.db == nil {
		return false, ErrUnavailable
	}
	if id <= 0 {
		return false, errors.New("参数有误")
	}
	if ok, err := s.couponExists(ctx, id); err != nil {
		return false, err
	} else if !ok {
		return false, errors.New("优惠券不存在")
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM v2_coupon WHERE id = $1`, id)
	if err != nil {
		return false, errors.New("删除失败")
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, errors.New("删除失败")
	}
	return affected > 0, nil
}

func (s *DBService) couponExists(ctx context.Context, id int64) (bool, error) {
	var exists int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM v2_coupon WHERE id = $1 LIMIT 1`, id).Scan(&exists)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return false, fmt.Errorf("query coupon exists: %w", err)
}

func scanCouponRow(scanner interface{ Scan(...any) error }) (couponRow, error) {
	var row couponRow
	if err := scanner.Scan(
		&row.ID,
		&row.Code,
		&row.Name,
		&row.Type,
		&row.Value,
		&row.Show,
		&row.LimitUse,
		&row.LimitUseWithUser,
		&row.LimitPlanIDs,
		&row.LimitPeriod,
		&row.StartedAt,
		&row.EndedAt,
		&row.CreatedAt,
		&row.UpdatedAt,
	); err != nil {
		return couponRow{}, err
	}
	return row, nil
}

func couponRecord(row couponRow) CouponRecord {
	return CouponRecord{
		ID:               row.ID,
		Code:             row.Code,
		Name:             row.Name,
		Type:             row.Type,
		Value:            row.Value,
		Show:             row.Show,
		LimitUse:         nullInt64Ptr(row.LimitUse),
		LimitUseWithUser: nullInt64Ptr(row.LimitUseWithUser),
		LimitPlanIDs:     decodeInt64Slice(row.LimitPlanIDs),
		LimitPeriod:      decodeStringSlice(row.LimitPeriod),
		StartedAt:        row.StartedAt,
		EndedAt:          row.EndedAt,
		CreatedAt:        row.CreatedAt,
		UpdatedAt:        row.UpdatedAt,
	}
}

func couponCSV(rows []CouponRecord) string {
	var builder strings.Builder
	builder.WriteString("名称,类型,金额或比例,开始时间,结束时间,可用次数,可用于订阅,券码,生成时间\r\n")
	for _, coupon := range rows {
		typeText := map[int64]string{1: "金额", 2: "比例"}[coupon.Type]
		value := strconv.FormatInt(coupon.Value, 10)
		if coupon.Type == 1 {
			value = strconv.FormatFloat(float64(coupon.Value)/100, 'f', -1, 64)
		}
		limitUse := "不限制"
		if coupon.LimitUse != nil {
			limitUse = strconv.FormatInt(*coupon.LimitUse, 10)
		}
		limitPlans := "不限制"
		if len(coupon.LimitPlanIDs) > 0 {
			parts := make([]string, 0, len(coupon.LimitPlanIDs))
			for _, id := range coupon.LimitPlanIDs {
				parts = append(parts, strconv.FormatInt(id, 10))
			}
			limitPlans = strings.Join(parts, "/")
		}
		builder.WriteString(fmt.Sprintf("%s,%s,%s,%s,%s,%s,%s,%s,%s\r\n",
			coupon.Name,
			typeText,
			value,
			time.Unix(coupon.StartedAt, 0).Format("2006-01-02 15:04:05"),
			time.Unix(coupon.EndedAt, 0).Format("2006-01-02 15:04:05"),
			limitUse,
			limitPlans,
			coupon.Code,
			time.Unix(coupon.CreatedAt, 0).Format("2006-01-02 15:04:05"),
		))
	}
	return builder.String()
}

func sanitizeAdminSort(raw string, allowed []string, fallback string) string {
	raw = strings.TrimSpace(raw)
	for _, item := range allowed {
		if raw == item {
			if raw == "show" {
				return `"show"`
			}
			return raw
		}
	}
	return fallback
}

func sanitizeAdminSortType(raw string) string {
	if strings.EqualFold(strings.TrimSpace(raw), "ASC") {
		return "ASC"
	}
	return "DESC"
}

func encodeInt64SliceJSON(values []int64) (any, error) {
	if len(values) == 0 {
		return nil, nil
	}
	raw, err := json.Marshal(values)
	if err != nil {
		return nil, err
	}
	return string(raw), nil
}

func encodeStringSliceJSON(values []string) (any, error) {
	if len(values) == 0 {
		return nil, nil
	}
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			normalized = append(normalized, value)
		}
	}
	if len(normalized) == 0 {
		return nil, nil
	}
	raw, err := json.Marshal(normalized)
	if err != nil {
		return nil, err
	}
	return string(raw), nil
}

func decodeInt64Slice(raw sql.NullString) []int64 {
	if !raw.Valid {
		return nil
	}
	return parseIDString(raw.String)
}

func decodeStringSlice(raw sql.NullString) []string {
	if !raw.Valid || strings.TrimSpace(raw.String) == "" {
		return nil
	}
	var values []string
	if err := json.Unmarshal([]byte(raw.String), &values); err == nil {
		return values
	}
	parts := strings.Split(raw.String, ",")
	values = make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(strings.Trim(part, `"'`))
		if part != "" {
			values = append(values, part)
		}
	}
	return values
}
