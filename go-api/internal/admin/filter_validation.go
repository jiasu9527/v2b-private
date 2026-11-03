package admin

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// ErrInvalidFilter distinguishes rejected request predicates from SQL failures.
// A supplied predicate must either be applied or reject the entire operation.
var ErrInvalidFilter = errors.New("筛选参数无效")

func invalidFilter(message string) error { return fmt.Errorf("%w: %s", ErrInvalidFilter, message) }

func NormalizeOrderFilters(filters []OrderFilter) ([]OrderFilter, error) {
	if len(filters) > 100 {
		return nil, invalidFilter("筛选条件最多100项")
	}
	out := make([]OrderFilter, 0, len(filters))
	for _, f := range filters {
		f.Key = strings.TrimSpace(f.Key)
		f.Condition = strings.TrimSpace(f.Condition)
		f.Value = strings.TrimSpace(f.Value)
		text := false
		switch f.Key {
		case "email", "trade_no", "callback_no":
			text = true
		case "id", "status", "commission_status", "user_id", "invite_user_id", "commission_balance", "total_amount", "created_at", "updated_at", "paid_at":
		default:
			return nil, invalidFilter("未知订单筛选字段")
		}
		if _, ok := allowedOrderOperator(f.Condition); !ok && f.Condition != "模糊" {
			return nil, invalidFilter("未知订单比较条件")
		}
		if len(f.Value) > 4096 || strings.ContainsRune(f.Value, 0) {
			return nil, invalidFilter("筛选值过长或包含非法字符")
		}
		if !text && f.Condition != "模糊" {
			n, err := strconv.ParseInt(f.Value, 10, 64)
			if err != nil || n < math.MinInt32 || n > math.MaxInt32 {
				return nil, invalidFilter("数字或Unix秒时间必须是32位整数")
			}
			if (f.Key == "status" || f.Key == "commission_status") && (n < math.MinInt16 || n > math.MaxInt16) {
				return nil, invalidFilter("状态筛选值超出字段范围")
			}
			f.Value = strconv.FormatInt(n, 10)
		}
		out = append(out, f)
	}
	return out, nil
}

func NormalizeUserFilters(filters []UserFilter) ([]UserFilter, error) {
	if len(filters) > 100 {
		return nil, invalidFilter("筛选条件最多100项")
	}
	out := make([]UserFilter, 0, len(filters))
	for _, f := range filters {
		f.Key = strings.TrimSpace(f.Key)
		f.Condition = strings.TrimSpace(f.Condition)
		f.Value = strings.TrimSpace(f.Value)
		if f.Key == "last_login_at" {
			f.Key = "t"
		}
		if _, ok := userFilterField(f.Key); !ok && f.Key != "invite_by_email" && f.Key != "invite_code" {
			return nil, invalidFilter("未知用户筛选字段")
		}
		if _, err := normalizeUserFilterCondition(f.Condition); err != nil {
			return nil, invalidFilter("未知用户比较条件")
		}
		if len(f.Value) > 4096 || strings.ContainsRune(f.Value, 0) {
			return nil, invalidFilter("筛选值过长或包含非法字符")
		}
		if f.Key == "plan_id" && f.Value == "null" {
			if f.Condition != "=" && f.Condition != "!=" {
				return nil, invalidFilter("空套餐仅支持等于或不等于")
			}
		} else if f.Condition != "模糊" {
			switch f.Key {
			case "email", "remarks", "uuid", "token", "invite_by_email", "invite_code":
			case "d", "transfer_enable":
				n, err := strconv.ParseFloat(f.Value, 64)
				scaled := math.Round(n * float64(gibibyte))
				if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || scaled < float64(math.MinInt64) || scaled >= float64(math.MaxInt64) {
					return nil, invalidFilter("流量必须是有效GiB数值")
				}
			default:
				n, err := strconv.ParseInt(f.Value, 10, 64)
				if err != nil {
					return nil, invalidFilter("数字或Unix秒时间必须是整数")
				}
				if f.Key != "expired_at" && (n < math.MinInt32 || n > math.MaxInt32) {
					return nil, invalidFilter("筛选数值超出字段范围")
				}
				if (f.Key == "banned" || f.Key == "is_admin" || f.Key == "is_staff") && (n < math.MinInt16 || n > math.MaxInt16) {
					return nil, invalidFilter("状态筛选值超出字段范围")
				}
				f.Value = strconv.FormatInt(n, 10)
			}
		}
		out = append(out, f)
	}
	return out, nil
}
