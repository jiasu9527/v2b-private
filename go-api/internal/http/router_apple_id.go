package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"forest/go-api/internal/appleid"
	"forest/go-api/internal/payment"
	"forest/go-api/internal/session"
)

type appleIDUserService interface {
	ListProducts(context.Context) ([]appleid.Product, error)
	CreateOrder(context.Context, int64, appleid.CreateOrderRequest) (appleid.Order, error)
	ListOrders(context.Context, int64) ([]appleid.Order, error)
	OrderDetail(context.Context, int64, string) (appleid.Order, error)
	Delivery(context.Context, int64, string) (appleid.Delivery, error)
	CancelOrder(context.Context, int64, string) error
}

func handleAppleProducts(w http.ResponseWriter, r *http.Request, sessions session.Service, service appleIDUserService) bool {
	disableResponseCache(w)
	if _, ok := authenticateRequest(w, r, sessions, false); !ok {
		return true
	}
	if service == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "apple id service unavailable"})
		return true
	}
	products, err := service.ListProducts(r.Context())
	if err != nil {
		return handleAppleError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": products})
	return true
}

func handleAppleCreateOrder(w http.ResponseWriter, r *http.Request, sessions session.Service, service appleIDUserService) bool {
	disableResponseCache(w)
	identity, ok := authenticateRequest(w, r, sessions, false)
	if !ok {
		return true
	}
	if service == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "apple id service unavailable"})
		return true
	}
	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	productID, _ := strconv.ParseInt(strings.TrimSpace(inputs["product_id"]), 10, 64)
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		key = strings.TrimSpace(inputs["idempotency_key"])
	}
	order, err := service.CreateOrder(r.Context(), identity.ID, appleid.CreateOrderRequest{ProductID: productID, IdempotencyKey: key})
	if err != nil {
		return handleAppleError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": order})
	return true
}

func handleApplePayOrder(w http.ResponseWriter, r *http.Request, sessions session.Service, payments payment.Service, tradeNo string) bool {
	disableResponseCache(w)
	identity, ok := authenticateRequest(w, r, sessions, false)
	if !ok {
		return true
	}
	tradeNo = strings.TrimSpace(tradeNo)
	if !strings.HasPrefix(tradeNo, "apple-") {
		return handleAppleError(w, appleid.ErrInvalidParameter)
	}
	if payments == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "payment service unavailable"})
		return true
	}
	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	methodID, _ := strconv.ParseInt(strings.TrimSpace(inputs["method"]), 10, 64)
	result, err := payments.Checkout(r.Context(), identity.ID, payment.CheckoutRequest{TradeNo: tradeNo, MethodID: methodID, Token: strings.TrimSpace(inputs["token"]), RequestBaseURL: detectCheckoutRequestBaseURL(r)})
	if err != nil {
		if isAppleIDError(err) {
			return handleAppleError(w, err)
		}
		switch {
		case errors.Is(err, payment.ErrInvalidParameter), errors.Is(err, payment.ErrOrderPaidOrMissing),
			errors.Is(err, payment.ErrPaymentMethodUnavailable), errors.Is(err, payment.ErrUnsupportedGateway),
			errors.Is(err, payment.ErrRequestFailed), errors.Is(err, payment.ErrUnavailable):
			return handlePaymentError(w, err)
		default:
			return handleAppleError(w, err)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"type": result.Type, "data": result.Data})
	return true
}

func handleAppleListOrders(w http.ResponseWriter, r *http.Request, sessions session.Service, service appleIDUserService) bool {
	disableResponseCache(w)
	identity, ok := authenticateRequest(w, r, sessions, false)
	if !ok {
		return true
	}
	if service == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "apple id service unavailable"})
		return true
	}
	orders, err := service.ListOrders(r.Context(), identity.ID)
	if err != nil {
		return handleAppleError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": orders})
	return true
}

func handleAppleOrderDetail(w http.ResponseWriter, r *http.Request, sessions session.Service, service appleIDUserService, tradeNo string) bool {
	disableResponseCache(w)
	identity, ok := authenticateRequest(w, r, sessions, false)
	if !ok {
		return true
	}
	if service == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "apple id service unavailable"})
		return true
	}
	order, err := service.OrderDetail(r.Context(), identity.ID, tradeNo)
	if err != nil {
		return handleAppleError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": order})
	return true
}

func handleAppleDelivery(w http.ResponseWriter, r *http.Request, sessions session.Service, service appleIDUserService, tradeNo string) bool {
	disableResponseCache(w)
	identity, ok := authenticateRequest(w, r, sessions, false)
	if !ok {
		return true
	}
	if service == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "apple id service unavailable"})
		return true
	}
	delivery, err := service.Delivery(r.Context(), identity.ID, tradeNo)
	if err != nil {
		return handleAppleError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": delivery})
	return true
}

func handleAppleCancel(w http.ResponseWriter, r *http.Request, sessions session.Service, service appleIDUserService, tradeNo string) bool {
	disableResponseCache(w)
	identity, ok := authenticateRequest(w, r, sessions, false)
	if !ok {
		return true
	}
	if service == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "apple id service unavailable"})
		return true
	}
	if err := service.CancelOrder(r.Context(), identity.ID, tradeNo); err != nil {
		return handleAppleError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": true})
	return true
}

func handleAppleError(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, appleid.ErrInvalidParameter):
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "Invalid parameter"})
	case errors.Is(err, appleid.ErrProductNotFound):
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "Apple ID product not found"})
	case errors.Is(err, appleid.ErrProductDisabled), errors.Is(err, appleid.ErrOutOfStock):
		writeJSON(w, http.StatusConflict, map[string]any{"message": err.Error()})
	case errors.Is(err, appleid.ErrOrderNotFound):
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "Apple ID order not found"})
	case errors.Is(err, appleid.ErrDeliveryNotReady):
		writeJSON(w, http.StatusConflict, map[string]any{"message": "Apple ID delivery is not ready"})
	case errors.Is(err, appleid.ErrOrderPaid):
		writeJSON(w, http.StatusConflict, map[string]any{"message": "Apple ID order can no longer be changed"})
	case errors.Is(err, appleid.ErrUnavailable):
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "apple id service unavailable"})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "Apple ID operation failed"})
	}
	return true
}

func isAppleIDError(err error) bool {
	return errors.Is(err, appleid.ErrInvalidParameter) ||
		errors.Is(err, appleid.ErrProductNotFound) ||
		errors.Is(err, appleid.ErrProductDisabled) ||
		errors.Is(err, appleid.ErrOutOfStock) ||
		errors.Is(err, appleid.ErrOrderNotFound) ||
		errors.Is(err, appleid.ErrDeliveryNotReady) ||
		errors.Is(err, appleid.ErrOrderPaid) ||
		errors.Is(err, appleid.ErrUnavailable)
}

func handleAppleOrderRoute(w http.ResponseWriter, r *http.Request, sessions session.Service, payments payment.Service, service appleIDUserService) bool {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/apple-id/orders/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 1 || parts[0] == "" {
		return false
	}
	tradeNo := parts[0]
	if len(parts) == 2 && parts[1] == "payment" && r.Method == http.MethodPost {
		return handleApplePayOrder(w, r, sessions, payments, tradeNo)
	}
	if len(parts) == 2 && parts[1] == "delivery" && r.Method == http.MethodGet {
		return handleAppleDelivery(w, r, sessions, service, tradeNo)
	}
	if len(parts) == 2 && parts[1] == "cancel" && r.Method == http.MethodPost {
		return handleAppleCancel(w, r, sessions, service, tradeNo)
	}
	if len(parts) == 1 && r.Method == http.MethodGet {
		return handleAppleOrderDetail(w, r, sessions, service, tradeNo)
	}
	return false
}
