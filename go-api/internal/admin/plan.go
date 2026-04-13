package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type PlanRecord struct {
	ID                 int64   `json:"id"`
	GroupID            int64   `json:"group_id"`
	TransferEnable     int64   `json:"transfer_enable"`
	DeviceLimit        *int64  `json:"device_limit"`
	Name               string  `json:"name"`
	SpeedLimit         *int64  `json:"speed_limit"`
	Show               int64   `json:"show"`
	Sort               *int64  `json:"sort"`
	Renew              int64   `json:"renew"`
	Content            *string `json:"content"`
	MonthPrice         *int64  `json:"month_price"`
	QuarterPrice       *int64  `json:"quarter_price"`
	HalfYearPrice      *int64  `json:"half_year_price"`
	YearPrice          *int64  `json:"year_price"`
	TwoYearPrice       *int64  `json:"two_year_price"`
	ThreeYearPrice     *int64  `json:"three_year_price"`
	OnetimePrice       *int64  `json:"onetime_price"`
	ResetPrice         *int64  `json:"reset_price"`
	ResetTrafficMethod *int64  `json:"reset_traffic_method"`
	CapacityLimit      *int64  `json:"capacity_limit"`
	CreatedAt          int64   `json:"created_at"`
	UpdatedAt          int64   `json:"updated_at"`
	Count              int64   `json:"count"`
}

type PlanSaveRequest struct {
	ID                 *int64
	Name               string
	Content            *string
	GroupID            int64
	TransferEnable     int64
	DeviceLimit        *int64
	MonthPrice         *int64
	QuarterPrice       *int64
	HalfYearPrice      *int64
	YearPrice          *int64
	TwoYearPrice       *int64
	ThreeYearPrice     *int64
	OnetimePrice       *int64
	ResetPrice         *int64
	ResetTrafficMethod *int64
	CapacityLimit      *int64
	SpeedLimit         *int64
	ForceUpdate        bool
}

type PlanToggleRequest struct {
	ID    int64
	Show  *int64
	Renew *int64
}

type planRow struct {
	ID                 int64
	GroupID            int64
	TransferEnable     int64
	DeviceLimit        sql.NullInt64
	Name               string
	SpeedLimit         sql.NullInt64
	Show               int64
	Sort               sql.NullInt64
	Renew              int64
	Content            sql.NullString
	MonthPrice         sql.NullInt64
	QuarterPrice       sql.NullInt64
	HalfYearPrice      sql.NullInt64
	YearPrice          sql.NullInt64
	TwoYearPrice       sql.NullInt64
	ThreeYearPrice     sql.NullInt64
	OnetimePrice       sql.NullInt64
	ResetPrice         sql.NullInt64
	ResetTrafficMethod sql.NullInt64
	CapacityLimit      sql.NullInt64
	CreatedAt          int64
	UpdatedAt          int64
}

func (s *DBService) ListPlans(ctx context.Context) ([]PlanRecord, error) {
	if s.db == nil {
		return nil, ErrUnavailable
	}

	counts, err := s.activePlanCounts(ctx)
	if err != nil {
		return nil, err
	}

	rows, err := s.db.QueryContext(ctx, `SELECT id, group_id, transfer_enable, device_limit, name, speed_limit, "show", sort, renew, content,
month_price, quarter_price, half_year_price, year_price, two_year_price, three_year_price, onetime_price, reset_price,
reset_traffic_method, capacity_limit, created_at, updated_at
FROM v2_plan
ORDER BY sort ASC NULLS LAST, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("query plans: %w", err)
	}
	defer rows.Close()

	result := make([]PlanRecord, 0)
	for rows.Next() {
		row, err := scanPlanRow(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, planRecord(row, counts[row.ID]))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate plans: %w", err)
	}
	return result, nil
}

func (s *DBService) SavePlan(ctx context.Context, req PlanSaveRequest) (bool, error) {
	if s.db == nil {
		return false, ErrUnavailable
	}

	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		return false, errors.New("套餐名称不能为空")
	}

	now := time.Now().Unix()
	if req.ID == nil {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO v2_plan (
group_id, transfer_enable, device_limit, name, speed_limit, content,
month_price, quarter_price, half_year_price, year_price, two_year_price, three_year_price,
onetime_price, reset_price, reset_traffic_method, capacity_limit, created_at, updated_at
) VALUES (
$1, $2, $3, $4, $5, $6,
$7, $8, $9, $10, $11, $12,
$13, $14, $15, $16, $17, $18
)`,
			req.GroupID,
			req.TransferEnable,
			nullableInt64(req.DeviceLimit),
			req.Name,
			nullableInt64(req.SpeedLimit),
			nullableString(req.Content),
			nullableInt64(req.MonthPrice),
			nullableInt64(req.QuarterPrice),
			nullableInt64(req.HalfYearPrice),
			nullableInt64(req.YearPrice),
			nullableInt64(req.TwoYearPrice),
			nullableInt64(req.ThreeYearPrice),
			nullableInt64(req.OnetimePrice),
			nullableInt64(req.ResetPrice),
			nullableInt64(req.ResetTrafficMethod),
			nullableInt64(req.CapacityLimit),
			now,
			now,
		); err != nil {
			return false, errors.New("创建失败")
		}
		return true, nil
	}

	if ok, err := s.planExists(ctx, *req.ID); err != nil {
		return false, err
	} else if !ok {
		return false, errors.New("该订阅不存在")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, errors.New("保存失败")
	}
	defer tx.Rollback()

	if req.ForceUpdate {
		if _, err := tx.ExecContext(ctx, `UPDATE v2_user
SET group_id = $2, transfer_enable = $3, device_limit = $4, speed_limit = $5, updated_at = $6
WHERE plan_id = $1`,
			*req.ID,
			req.GroupID,
			req.TransferEnable*1073741824,
			nullableInt64(req.DeviceLimit),
			nullableInt64(req.SpeedLimit),
			now,
		); err != nil {
			return false, errors.New("保存失败")
		}
	}

	if _, err := tx.ExecContext(ctx, `UPDATE v2_plan
SET group_id = $2,
	transfer_enable = $3,
	device_limit = $4,
	name = $5,
	speed_limit = $6,
	content = $7,
	month_price = $8,
	quarter_price = $9,
	half_year_price = $10,
	year_price = $11,
	two_year_price = $12,
	three_year_price = $13,
	onetime_price = $14,
	reset_price = $15,
	reset_traffic_method = $16,
	capacity_limit = $17,
	updated_at = $18
WHERE id = $1`,
		*req.ID,
		req.GroupID,
		req.TransferEnable,
		nullableInt64(req.DeviceLimit),
		req.Name,
		nullableInt64(req.SpeedLimit),
		nullableString(req.Content),
		nullableInt64(req.MonthPrice),
		nullableInt64(req.QuarterPrice),
		nullableInt64(req.HalfYearPrice),
		nullableInt64(req.YearPrice),
		nullableInt64(req.TwoYearPrice),
		nullableInt64(req.ThreeYearPrice),
		nullableInt64(req.OnetimePrice),
		nullableInt64(req.ResetPrice),
		nullableInt64(req.ResetTrafficMethod),
		nullableInt64(req.CapacityLimit),
		now,
	); err != nil {
		return false, errors.New("保存失败")
	}

	if err := tx.Commit(); err != nil {
		return false, errors.New("保存失败")
	}
	return true, nil
}

func (s *DBService) DeletePlan(ctx context.Context, id int64) (bool, error) {
	if s.db == nil {
		return false, ErrUnavailable
	}

	hasOrders, err := s.planHasOrders(ctx, id)
	if err != nil {
		return false, err
	}
	if hasOrders {
		return false, errors.New("该订阅下存在订单无法删除")
	}

	hasUsers, err := s.planHasUsers(ctx, id)
	if err != nil {
		return false, err
	}
	if hasUsers {
		return false, errors.New("该订阅下存在用户无法删除")
	}

	if ok, err := s.planExists(ctx, id); err != nil {
		return false, err
	} else if !ok {
		return false, errors.New("该订阅ID不存在")
	}

	result, err := s.db.ExecContext(ctx, `DELETE FROM v2_plan WHERE id = $1`, id)
	if err != nil {
		return false, errors.New("删除失败")
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, errors.New("删除失败")
	}
	return affected > 0, nil
}

func (s *DBService) TogglePlan(ctx context.Context, req PlanToggleRequest) (bool, error) {
	if s.db == nil {
		return false, ErrUnavailable
	}
	if req.ID <= 0 {
		return false, errors.New("该订阅不存在")
	}
	if req.Show == nil && req.Renew == nil {
		return false, errors.New("保存失败")
	}

	sets := make([]string, 0, 3)
	args := make([]any, 0, 4)
	if req.Show != nil {
		args = append(args, *req.Show)
		sets = append(sets, fmt.Sprintf(`"show" = $%d`, len(args)))
	}
	if req.Renew != nil {
		args = append(args, *req.Renew)
		sets = append(sets, fmt.Sprintf(`renew = $%d`, len(args)))
	}
	args = append(args, time.Now().Unix())
	sets = append(sets, fmt.Sprintf(`updated_at = $%d`, len(args)))
	args = append(args, req.ID)

	query := fmt.Sprintf(`UPDATE v2_plan SET %s WHERE id = $%d`, strings.Join(sets, ", "), len(args))
	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return false, errors.New("保存失败")
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, errors.New("保存失败")
	}
	if affected == 0 {
		return false, errors.New("该订阅不存在")
	}
	return true, nil
}

func (s *DBService) SortPlans(ctx context.Context, ids []int64) (bool, error) {
	if s.db == nil {
		return false, ErrUnavailable
	}
	if len(ids) == 0 {
		return false, errors.New("订阅计划ID不能为空")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, errors.New("保存失败")
	}
	defer tx.Rollback()

	for index, id := range ids {
		result, err := tx.ExecContext(ctx, `UPDATE v2_plan SET sort = $2, updated_at = $3 WHERE id = $1`, id, index+1, time.Now().Unix())
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

func (s *DBService) activePlanCounts(ctx context.Context) (map[int64]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT plan_id, COUNT(*) AS count
FROM v2_user
WHERE plan_id IS NOT NULL AND (expired_at >= $1 OR expired_at IS NULL OR expired_at <= 0)
GROUP BY plan_id`, time.Now().Unix())
	if err != nil {
		return nil, fmt.Errorf("query active plan counts: %w", err)
	}
	defer rows.Close()

	counts := make(map[int64]int64)
	for rows.Next() {
		var (
			planID int64
			count  int64
		)
		if err := rows.Scan(&planID, &count); err != nil {
			return nil, fmt.Errorf("scan active plan counts: %w", err)
		}
		counts[planID] = count
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate active plan counts: %w", err)
	}
	return counts, nil
}

func (s *DBService) planExists(ctx context.Context, id int64) (bool, error) {
	var exists int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM v2_plan WHERE id = $1 LIMIT 1`, id).Scan(&exists)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return false, fmt.Errorf("query plan exists: %w", err)
}

func (s *DBService) planHasOrders(ctx context.Context, id int64) (bool, error) {
	var exists int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM v2_order WHERE plan_id = $1 LIMIT 1`, id).Scan(&exists)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return false, fmt.Errorf("query plan orders: %w", err)
}

func (s *DBService) planHasUsers(ctx context.Context, id int64) (bool, error) {
	var exists int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM v2_user WHERE plan_id = $1 LIMIT 1`, id).Scan(&exists)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return false, fmt.Errorf("query plan users: %w", err)
}

func scanPlanRow(scanner interface{ Scan(...any) error }) (planRow, error) {
	var row planRow
	if err := scanner.Scan(
		&row.ID,
		&row.GroupID,
		&row.TransferEnable,
		&row.DeviceLimit,
		&row.Name,
		&row.SpeedLimit,
		&row.Show,
		&row.Sort,
		&row.Renew,
		&row.Content,
		&row.MonthPrice,
		&row.QuarterPrice,
		&row.HalfYearPrice,
		&row.YearPrice,
		&row.TwoYearPrice,
		&row.ThreeYearPrice,
		&row.OnetimePrice,
		&row.ResetPrice,
		&row.ResetTrafficMethod,
		&row.CapacityLimit,
		&row.CreatedAt,
		&row.UpdatedAt,
	); err != nil {
		return planRow{}, err
	}
	return row, nil
}

func planRecord(row planRow, count int64) PlanRecord {
	return PlanRecord{
		ID:                 row.ID,
		GroupID:            row.GroupID,
		TransferEnable:     row.TransferEnable,
		DeviceLimit:        nullInt64Ptr(row.DeviceLimit),
		Name:               row.Name,
		SpeedLimit:         nullInt64Ptr(row.SpeedLimit),
		Show:               row.Show,
		Sort:               nullInt64Ptr(row.Sort),
		Renew:              row.Renew,
		Content:            nullStringPtr(row.Content),
		MonthPrice:         nullInt64Ptr(row.MonthPrice),
		QuarterPrice:       nullInt64Ptr(row.QuarterPrice),
		HalfYearPrice:      nullInt64Ptr(row.HalfYearPrice),
		YearPrice:          nullInt64Ptr(row.YearPrice),
		TwoYearPrice:       nullInt64Ptr(row.TwoYearPrice),
		ThreeYearPrice:     nullInt64Ptr(row.ThreeYearPrice),
		OnetimePrice:       nullInt64Ptr(row.OnetimePrice),
		ResetPrice:         nullInt64Ptr(row.ResetPrice),
		ResetTrafficMethod: nullInt64Ptr(row.ResetTrafficMethod),
		CapacityLimit:      nullInt64Ptr(row.CapacityLimit),
		CreatedAt:          row.CreatedAt,
		UpdatedAt:          row.UpdatedAt,
		Count:              count,
	}
}
