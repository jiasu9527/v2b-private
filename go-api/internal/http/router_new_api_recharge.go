package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"

	"forest/go-api/internal/newapibridge"
	"forest/go-api/internal/payment"
)

// Kept separate from the account interface so deployments/fakes without cash
// recharge explicitly return unavailable rather than changing wallet behavior.
type newAPIWorkspaceRechargeService interface {
	WorkspaceRechargeOptions(context.Context, int64, string) (any, error)
	CreateWorkspaceRecharge(context.Context, int64, string, newapibridge.WorkspaceRechargeRequest) (newapibridge.RechargeOrder, error)
	CheckoutWorkspaceRecharge(context.Context, int64, newapibridge.WorkspaceRechargeCheckoutRequest, string) (newapibridge.RechargeCheckout, error)
	WorkspaceRechargeDetail(context.Context, int64, string) (newapibridge.RechargeOrder, error)
	WorkspaceRecharges(context.Context, int64, int, int) (newapibridge.RechargePage, error)
}

func handleWorkspaceRecharge(w http.ResponseWriter, r *http.Request, bridge newAPIBridgeService, user int64, auth, action string) {
	s, ok := bridge.(newAPIWorkspaceRechargeService)
	if !ok {
		workspaceError(w, newapibridge.ErrUnavailable)
		return
	}
	if r.Method == http.MethodGet && r.Body != nil {
		b, err := io.ReadAll(io.LimitReader(r.Body, 1))
		if err != nil || len(b) != 0 {
			workspaceError(w, newapibridge.ErrInvalid)
			return
		}
	}
	var result any
	var err error
	switch action {
	case "recharge/options":
		if _, err = workspaceQuery(r); err == nil {
			result, err = s.WorkspaceRechargeOptions(r.Context(), user, auth)
		}
	case "recharge/create":
		var req newapibridge.WorkspaceRechargeRequest
		if !decodeWorkspaceJSON(w, r, 8<<10, &req) {
			return
		}
		if req.AmountFen <= 0 || len(req.RequestID) < 8 || len(req.RequestID) > 128 {
			err = newapibridge.ErrInvalid
		} else {
			result, err = s.CreateWorkspaceRecharge(r.Context(), user, auth, req)
		}
	case "recharge/checkout":
		var req newapibridge.WorkspaceRechargeCheckoutRequest
		if !decodeWorkspaceJSON(w, r, 8<<10, &req) {
			return
		}
		if !validRechargeTradeNo(req.TradeNo) || req.PaymentID < 1 {
			err = newapibridge.ErrInvalid
		} else {
			result, err = s.CheckoutWorkspaceRecharge(r.Context(), user, req, detectCheckoutRequestBaseURL(r))
		}
	case "recharge/detail":
		q, e := workspaceQuery(r, "trade_no")
		if e != nil || !validRechargeTradeNo(q.Get("trade_no")) {
			err = newapibridge.ErrInvalid
		} else {
			result, err = s.WorkspaceRechargeDetail(r.Context(), user, q.Get("trade_no"))
		}
	case "recharge/orders":
		q, e := workspaceQuery(r, "current", "page_size")
		if e != nil {
			err = e
			break
		}
		page, size, e := workspacePagination(q)
		if e != nil {
			err = e
			break
		}
		result, err = s.WorkspaceRecharges(r.Context(), user, page, size)
	default:
		err = newapibridge.ErrInvalid
	}
	if err != nil {
		rechargeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": result})
}

func validRechargeTradeNo(value string) bool {
	if !strings.HasPrefix(value, "AIT") || len(value) < 4 || len(value) > 100 {
		return false
	}
	for _, c := range value {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')) {
			return false
		}
	}
	return true
}

func rechargeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, payment.ErrInvalidParameter):
		workspaceError(w, newapibridge.ErrInvalid)
	case errors.Is(err, payment.ErrOrderPaidOrMissing):
		writeJSON(w, 409, map[string]any{"code": "recharge_order_changed", "message": "订单已支付或不存在，请刷新充值记录核对"})
	case errors.Is(err, payment.ErrPaymentMethodUnavailable), errors.Is(err, payment.ErrUnsupportedGateway):
		writeJSON(w, 409, map[string]any{"code": "payment_unavailable", "message": "该支付方式暂不可用，请刷新核对"})
	case errors.Is(err, payment.ErrCheckoutInProgress):
		writeJSON(w, 409, map[string]any{"code": "checkout_in_progress", "message": "正在创建付款信息，请稍后查询本订单，不要重复充值"})
	case errors.Is(err, payment.ErrCheckoutConfigChanged):
		writeJSON(w, 409, map[string]any{"code": "payment_config_changed", "message": "订单支付设置已变化，请核对原充值记录"})
	default:
		workspaceError(w, err)
	}
}
