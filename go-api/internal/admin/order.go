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

	usersvc "forest/go-api/internal/user"
)

type OrderFilter struct {
	Key       string
	Condition string
	Value     string
}

type OrderFetchRequest struct {
	Current      int64
	PageSize     int64
	IsCommission bool
	Filters      []OrderFilter
}

type OrderListResult struct {
	Data  []map[string]any `json:"data"`
	Total int64            `json:"total"`
}

type OrderUpdateRequest struct {
	TradeNo          string
	CommissionStatus *int64
}

type OrderAssignRequest struct {
	PlanID      int64
	Email       string
	TotalAmount int64
	Period      string
}

type orderRuntime interface {
	MarkOrderPaid(ctx context.Context, tradeNo string, confirmation usersvc.OrderPaymentConfirmation) error
	CancelOrder(ctx context.Context, userID int64, tradeNo string) (bool, error)
	AssignAdminOrder(ctx context.Context, req usersvc.AdminAssignOrderRequest) (string, error)
	RefundManagedOrder(ctx context.Context, tradeNo string) error
}

func (s *DBService) FetchOrders(ctx context.Context, req OrderFetchRequest) (OrderListResult, error) {
	if s.db == nil {
		return OrderListResult{}, ErrUnavailable
	}

	current := req.Current
	if current <= 0 {
		current = 1
	}
	pageSize := req.PageSize
	if pageSize < 10 {
		pageSize = 10
	}

	whereClause, args := buildOrderWhere(req)
	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_order o`+whereClause, args...).Scan(&total); err != nil {
		return OrderListResult{}, fmt.Errorf("count admin orders: %w", err)
	}

	offset := (current - 1) * pageSize
	dataArgs := append(append([]any{}, args...), pageSize, offset)
	query := fmt.Sprintf(`SELECT COALESCE(json_agg(row_to_json(t)), '[]'::json)
FROM (
	SELECT o.*, p.name AS plan_name
	FROM v2_order o
	LEFT JOIN v2_plan p ON p.id = o.plan_id
	%s
	ORDER BY o.created_at DESC
	LIMIT $%d OFFSET $%d
) AS t`, whereClause, len(dataArgs)-1, len(dataArgs))

	var raw []byte
	if err := s.db.QueryRowContext(ctx, query, dataArgs...).Scan(&raw); err != nil {
		return OrderListResult{}, fmt.Errorf("query admin orders: %w", err)
	}

	var data []map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return OrderListResult{}, fmt.Errorf("decode admin orders: %w", err)
	}

	return OrderListResult{Data: data, Total: total}, nil
}

func (s *DBService) GetOrderDetail(ctx context.Context, id int64) (map[string]any, error) {
	if s.db == nil {
		return nil, ErrUnavailable
	}

	var raw []byte
	err := s.db.QueryRowContext(ctx, `SELECT row_to_json(t)
FROM (
	SELECT * FROM v2_order WHERE id = $1 LIMIT 1
) AS t`, id).Scan(&raw)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("订单不存在")
		}
		return nil, fmt.Errorf("query order detail: %w", err)
	}

	var detail map[string]any
	if err := json.Unmarshal(raw, &detail); err != nil {
		return nil, fmt.Errorf("decode order detail: %w", err)
	}

	if inviteID := mapNullableAnyInt64(detail["invite_user_id"]); inviteID != nil && *inviteID > 0 {
		inviteUser, err := s.fetchUserSummary(ctx, *inviteID)
		if err != nil {
			return nil, err
		}
		if inviteUser == nil {
			detail["invite_user_id"] = nil
		} else {
			detail["invite_user"] = inviteUser
		}
	}

	logs, err := s.queryJSONList(ctx, `SELECT COALESCE(json_agg(row_to_json(t)), '[]'::json)
FROM (
	SELECT * FROM v2_commission_log WHERE trade_no = $1 ORDER BY id ASC
) AS t`, detail["trade_no"])
	if err != nil {
		return nil, err
	}
	detail["commission_log"] = logs

	ids := parseIDList(detail["surplus_order_ids"])
	if len(ids) > 0 {
		query, args := buildInt64InQuery(`SELECT COALESCE(json_agg(row_to_json(t)), '[]'::json)
FROM (
	SELECT * FROM v2_order WHERE id IN (%s) ORDER BY created_at DESC
) AS t`, ids)
		surplusOrders, err := s.queryJSONList(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		detail["surplus_orders"] = surplusOrders
	}

	return detail, nil
}

func (s *DBService) UpdateOrder(ctx context.Context, req OrderUpdateRequest) (bool, error) {
	if s.db == nil {
		return false, ErrUnavailable
	}
	req.TradeNo = strings.TrimSpace(req.TradeNo)
	if req.TradeNo == "" {
		return false, errors.New("订单不存在")
	}
	if req.CommissionStatus == nil {
		return false, errors.New("佣金状态格式不正确")
	}

	result, err := s.db.ExecContext(ctx, `UPDATE v2_order
SET commission_status = $2, updated_at = $3
WHERE trade_no = $1`, req.TradeNo, *req.CommissionStatus, time.Now().Unix())
	if err != nil {
		return false, errors.New("更新失败")
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, errors.New("更新失败")
	}
	if affected == 0 {
		return false, errors.New("订单不存在")
	}
	return true, nil
}

func (s *DBService) MarkOrderPaid(ctx context.Context, tradeNo string) (bool, error) {
	if s.db == nil || s.orders == nil {
		return false, ErrUnavailable
	}
	tradeNo = strings.TrimSpace(tradeNo)
	if tradeNo == "" {
		return false, errors.New("订单不存在")
	}

	var status int64
	err := s.db.QueryRowContext(ctx, `SELECT status FROM v2_order WHERE trade_no = $1 LIMIT 1`, tradeNo).Scan(&status)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, errors.New("订单不存在")
		}
		return false, fmt.Errorf("query order pay target: %w", err)
	}
	if status != 0 && status != 2 {
		return false, errors.New("当前订单不支持补单")
	}

	if err := s.orders.MarkOrderPaid(ctx, tradeNo, usersvc.OrderPaymentConfirmation{
		CallbackNo:     "manual_operation:" + tradeNo,
		AllowCancelled: true,
		Manual:         true,
	}); err != nil {
		switch {
		case errors.Is(err, usersvc.ErrOrderNotFound):
			return false, errors.New("订单不存在")
		case errors.Is(err, usersvc.ErrOrderPaidOrMissing):
			return false, errors.New("当前订单不支持补单")
		default:
			return false, err
		}
	}
	return true, nil
}

func (s *DBService) CancelManagedOrder(ctx context.Context, tradeNo string) (bool, error) {
	if s.db == nil || s.orders == nil {
		return false, ErrUnavailable
	}
	tradeNo = strings.TrimSpace(tradeNo)
	if tradeNo == "" {
		return false, errors.New("订单不存在")
	}

	var (
		userID int64
		status int64
	)
	err := s.db.QueryRowContext(ctx, `SELECT user_id, status FROM v2_order WHERE trade_no = $1 LIMIT 1`, tradeNo).Scan(&userID, &status)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, errors.New("订单不存在")
		}
		return false, fmt.Errorf("query order cancel target: %w", err)
	}
	if status != 0 {
		return false, errors.New("只能对待支付的订单进行操作")
	}

	ok, err := s.orders.CancelOrder(ctx, userID, tradeNo)
	if err != nil {
		switch {
		case errors.Is(err, usersvc.ErrOrderNotFound):
			return false, errors.New("订单不存在")
		case errors.Is(err, usersvc.ErrCancelPendingOnly):
			return false, errors.New("只能对待支付的订单进行操作")
		default:
			return false, err
		}
	}
	return ok, nil
}

func (s *DBService) RefundManagedOrder(ctx context.Context, tradeNo string) (bool, error) {
	if s.db == nil || s.orders == nil {
		return false, ErrUnavailable
	}
	tradeNo = strings.TrimSpace(tradeNo)
	if tradeNo == "" {
		return false, errors.New("订单不存在")
	}

	if err := s.orders.RefundManagedOrder(ctx, tradeNo); err != nil {
		switch {
		case errors.Is(err, usersvc.ErrOrderNotFound):
			return false, errors.New("订单不存在")
		case errors.Is(err, usersvc.ErrRefundCompletedOnly):
			return false, errors.New("只能对已完成的订单进行退款")
		case errors.Is(err, usersvc.ErrRefundLatestOnly):
			return false, errors.New("仅支持退款用户最近一笔已完成订单")
		case errors.Is(err, usersvc.ErrRefundTargetNotSupported):
			return false, errors.New("当前订单不支持退款")
		case errors.Is(err, usersvc.ErrCommissionRollbackInsufficient):
			return false, errors.New("邀请佣金余额不足，无法完成退款")
		default:
			return false, err
		}
	}
	return true, nil
}

func (s *DBService) AssignOrder(ctx context.Context, req OrderAssignRequest) (string, error) {
	if s.db == nil || s.orders == nil {
		return "", ErrUnavailable
	}

	tradeNo, err := s.orders.AssignAdminOrder(ctx, usersvc.AdminAssignOrderRequest{
		Email:       strings.TrimSpace(req.Email),
		PlanID:      req.PlanID,
		Period:      strings.TrimSpace(req.Period),
		TotalAmount: req.TotalAmount,
	})
	if err != nil {
		switch {
		case errors.Is(err, usersvc.ErrNotFound):
			return "", errors.New("该用户不存在")
		case errors.Is(err, usersvc.ErrPlanNotFound):
			return "", errors.New("该订阅不存在")
		case errors.Is(err, usersvc.ErrPendingOrderExists):
			return "", errors.New("该用户还有待支付的订单，无法分配")
		case errors.Is(err, usersvc.ErrInvalidParameter):
			return "", errors.New("订阅周期格式有误")
		default:
			return "", err
		}
	}
	return tradeNo, nil
}

func buildOrderWhere(req OrderFetchRequest) (string, []any) {
	clauses := make([]string, 0)
	args := make([]any, 0)

	if req.IsCommission {
		clauses = append(clauses, `o.invite_user_id IS NOT NULL`)
		clauses = append(clauses, `o.status NOT IN (0, 2)`)
		clauses = append(clauses, `o.commission_balance > 0`)
	}

	for _, filter := range req.Filters {
		key := strings.TrimSpace(filter.Key)
		condition := strings.TrimSpace(filter.Condition)
		value := strings.TrimSpace(filter.Value)
		if key == "" || condition == "" {
			continue
		}

		switch key {
		case "email":
			if condition == "模糊" {
				args = append(args, "%"+value+"%")
				clauses = append(clauses, fmt.Sprintf(`EXISTS (SELECT 1 FROM v2_user u WHERE u.id = o.user_id AND u.email ILIKE $%d)`, len(args)))
				continue
			}
			if operator, ok := allowedOrderOperator(condition); ok {
				args = append(args, value)
				clauses = append(clauses, fmt.Sprintf(`EXISTS (SELECT 1 FROM v2_user u WHERE u.id = o.user_id AND u.email %s $%d)`, operator, len(args)))
			}
		case "trade_no", "status", "commission_status", "user_id", "invite_user_id", "callback_no", "commission_balance":
			column := "o." + key
			if condition == "模糊" {
				args = append(args, "%"+value+"%")
				clauses = append(clauses, fmt.Sprintf(`CAST(%s AS TEXT) ILIKE $%d`, column, len(args)))
				continue
			}
			if operator, ok := allowedOrderOperator(condition); ok {
				args = append(args, value)
				clauses = append(clauses, fmt.Sprintf(`%s %s $%d`, column, operator, len(args)))
			}
		}
	}

	if len(clauses) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

func allowedOrderOperator(condition string) (string, bool) {
	switch condition {
	case ">", "<", "=", ">=", "<=", "!=":
		return condition, true
	default:
		return "", false
	}
}

func (s *DBService) queryJSONList(ctx context.Context, query string, args ...any) ([]map[string]any, error) {
	var raw []byte
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&raw); err != nil {
		return nil, fmt.Errorf("query json list: %w", err)
	}
	var result []map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("decode json list: %w", err)
	}
	return result, nil
}

func parseIDList(value any) []int64 {
	switch typed := value.(type) {
	case nil:
		return nil
	case string:
		return parseIDString(typed)
	default:
		return parseIDString(strings.TrimSpace(fmt.Sprint(typed)))
	}
}

func parseIDString(raw string) []int64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}

	var ids []int64
	if strings.HasPrefix(raw, "[") {
		_ = json.Unmarshal([]byte(raw), &ids)
		return ids
	}
	for _, part := range strings.Split(strings.Trim(raw, "{}"), ",") {
		part = strings.TrimSpace(strings.Trim(part, `"'`))
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

func buildInt64InQuery(format string, values []int64) (string, []any) {
	parts := make([]string, 0, len(values))
	args := make([]any, 0, len(values))
	for _, value := range values {
		args = append(args, value)
		parts = append(parts, fmt.Sprintf("$%d", len(args)))
	}
	return fmt.Sprintf(format, strings.Join(parts, ",")), args
}
