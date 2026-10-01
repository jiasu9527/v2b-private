package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"forest/go-api/internal/appleid"
	"forest/go-api/internal/session"
)

type appleIDAdminService interface {
	AdminListProducts(context.Context) ([]appleid.AdminProduct, error)
	AdminSaveProduct(context.Context, appleid.AdminSaveProductRequest) (appleid.AdminProduct, error)
	AdminSetProductEnabled(context.Context, appleid.AdminSetProductEnabledRequest) error
	AdminDeleteProduct(context.Context, int64) error
	AdminAddInventoryBatch(context.Context, appleid.AdminAddInventoryRequest) (appleid.AdminAddInventoryResult, error)
	AdminListInventory(context.Context, appleid.AdminInventoryListRequest) (appleid.AdminInventoryListResult, error)
	AdminDisableInventory(context.Context, appleid.AdminDisableInventoryRequest) error
	AdminListOrders(context.Context, appleid.AdminOrderListRequest) (appleid.AdminOrderListResult, error)
	AdminGetOrderDetail(context.Context, int64) (appleid.AdminOrderDetail, error)
	AdminGetOrderCredentials(context.Context, int64, int64) (appleid.AdminCredentialDetail, error)
	AdminReplaceInventory(context.Context, appleid.AdminReplaceInventoryRequest) (appleid.AdminReplaceInventoryResult, error)
	AdminCancelOrder(context.Context, appleid.AdminCancelOrderRequest) error
	AdminMarkOrderRefunded(context.Context, appleid.AdminMarkRefundedRequest) error
	AdminListAudits(context.Context, appleid.AdminAuditListRequest) (appleid.AdminAuditListResult, error)
	AdminFinanceStats(context.Context, appleid.AdminFinanceRequest) (appleid.AdminFinanceStatsResult, error)
	AdminFinanceTransactions(context.Context, appleid.AdminFinanceRequest) (appleid.AdminFinanceTransactionsResult, error)
}

func handleAdminAppleID(w http.ResponseWriter, r *http.Request, sessions session.Service, service appleIDAdminService, action string) bool {
	disableResponseCache(w)
	identity, ok := authenticateRequest(w, r, sessions, true)
	if !ok {
		return true
	}
	if service == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "apple id service unavailable"})
		return true
	}

	switch action {
	case "finance/stats", "finance/transactions":
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "Method not allowed"})
			return true
		}
		inputs, err := readInputs(r)
		if err != nil {
			return writeAdminAppleIDBadRequest(w, err)
		}
		query, err := parseAdminAppleFinance(inputs)
		if err != nil {
			return writeAdminAppleIDBadRequest(w, err)
		}
		if action == "finance/stats" {
			result, err := service.AdminFinanceStats(r.Context(), query)
			if err != nil {
				return handleAdminAppleIDError(w, err)
			}
			writeJSON(w, http.StatusOK, map[string]any{"data": result})
		} else {
			result, err := service.AdminFinanceTransactions(r.Context(), query)
			if err != nil {
				return handleAdminAppleIDError(w, err)
			}
			writeJSON(w, http.StatusOK, map[string]any{"data": result.Data, "total": result.Total})
		}
		return true
	case "product/fetch":
		if r.Method != http.MethodGet {
			return false
		}
		items, err := service.AdminListProducts(r.Context())
		if err != nil {
			return handleAdminAppleIDError(w, err)
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": items})
		return true
	case "product/save":
		if r.Method != http.MethodPost {
			return false
		}
		inputs, err := readInputs(r)
		if err != nil {
			return writeAdminAppleIDBadRequest(w, err)
		}
		id, err := optionalAppleIDInt64(inputs, "id")
		if err != nil {
			return writeAdminAppleIDBadRequest(w, err)
		}
		owned, err := parseAppleIDBool(inputs["owned_shadowrocket"], false)
		if err != nil {
			return writeAdminAppleIDBadRequest(w, err)
		}
		price, err := requiredAppleIDInt64(inputs, "price")
		if err != nil {
			return writeAdminAppleIDBadRequest(w, err)
		}
		var enabled *bool
		if raw, exists := inputs["enabled"]; exists && strings.TrimSpace(raw) != "" {
			value, parseErr := parseAppleIDBool(raw, true)
			if parseErr != nil {
				return writeAdminAppleIDBadRequest(w, parseErr)
			}
			enabled = &value
		}
		item, err := service.AdminSaveProduct(r.Context(), appleid.AdminSaveProductRequest{
			ID:                id,
			Name:              inputs["name"],
			Region:            inputs["region"],
			OwnedShadowrocket: owned,
			Price:             price,
			AfterSales:        inputs["after_sales"],
			Enabled:           enabled,
		})
		if err != nil {
			return handleAdminAppleIDError(w, err)
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": item})
		return true
	case "product/show":
		if r.Method != http.MethodPost {
			return false
		}
		inputs, err := readInputs(r)
		if err != nil {
			return writeAdminAppleIDBadRequest(w, err)
		}
		id, err := requiredAppleIDInt64(inputs, "id")
		if err != nil {
			return writeAdminAppleIDBadRequest(w, err)
		}
		enabled, err := parseAppleIDBool(firstAppleIDValue(inputs, "enabled", "show"), false)
		if err != nil {
			return writeAdminAppleIDBadRequest(w, err)
		}
		if err := service.AdminSetProductEnabled(r.Context(), appleid.AdminSetProductEnabledRequest{ProductID: id, Enabled: enabled}); err != nil {
			return handleAdminAppleIDError(w, err)
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": true})
		return true
	case "product/drop":
		if r.Method != http.MethodPost {
			return false
		}
		inputs, err := readInputs(r)
		if err != nil {
			return writeAdminAppleIDBadRequest(w, err)
		}
		id, err := requiredAppleIDInt64(inputs, "id")
		if err != nil {
			return writeAdminAppleIDBadRequest(w, err)
		}
		if err := service.AdminDeleteProduct(r.Context(), id); err != nil {
			return handleAdminAppleIDError(w, err)
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": true})
		return true
	case "inventory/import":
		if r.Method != http.MethodPost {
			return false
		}
		var payload struct {
			ProductID int64 `json:"product_id"`
			Items     []struct {
				Credential string `json:"credential"`
				Account    string `json:"account"`
				Password   string `json:"password"`
			} `json:"items"`
		}
		if err := readJSONBody(r, &payload); err != nil {
			return writeAdminAppleIDBadRequest(w, err)
		}
		items := make([]appleid.AdminInventoryCredential, 0, len(payload.Items))
		for _, item := range payload.Items {
			items = append(items, appleid.AdminInventoryCredential{Credential: item.Credential, Account: item.Account, Password: item.Password})
		}
		result, err := service.AdminAddInventoryBatch(r.Context(), appleid.AdminAddInventoryRequest{ProductID: payload.ProductID, Items: items})
		if err != nil {
			return handleAdminAppleIDError(w, err)
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": result})
		return true
	case "inventory/fetch":
		if r.Method != http.MethodGet {
			return false
		}
		inputs, err := readInputs(r)
		if err != nil {
			return writeAdminAppleIDBadRequest(w, err)
		}
		request, err := parseAdminAppleInventoryList(inputs)
		if err != nil {
			return writeAdminAppleIDBadRequest(w, err)
		}
		result, err := service.AdminListInventory(r.Context(), request)
		if err != nil {
			return handleAdminAppleIDError(w, err)
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": result.Data, "total": result.Total})
		return true
	case "inventory/disable":
		if r.Method != http.MethodPost {
			return false
		}
		inputs, err := readInputs(r)
		if err != nil {
			return writeAdminAppleIDBadRequest(w, err)
		}
		id, err := requiredAppleIDInt64(inputs, "id")
		if err != nil {
			return writeAdminAppleIDBadRequest(w, err)
		}
		if err := service.AdminDisableInventory(r.Context(), appleid.AdminDisableInventoryRequest{InventoryID: id, AdminID: identity.ID}); err != nil {
			return handleAdminAppleIDError(w, err)
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": true})
		return true
	case "order/fetch":
		if r.Method != http.MethodGet {
			return false
		}
		inputs, err := readInputs(r)
		if err != nil {
			return writeAdminAppleIDBadRequest(w, err)
		}
		request, err := parseAdminAppleOrderList(inputs)
		if err != nil {
			return writeAdminAppleIDBadRequest(w, err)
		}
		result, err := service.AdminListOrders(r.Context(), request)
		if err != nil {
			return handleAdminAppleIDError(w, err)
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": result.Data, "total": result.Total, "business_type": appleid.BusinessType})
		return true
	case "order/detail":
		if r.Method != http.MethodGet {
			return false
		}
		inputs, err := readInputs(r)
		if err != nil {
			return writeAdminAppleIDBadRequest(w, err)
		}
		id, err := requiredAppleIDInt64(inputs, "id")
		if err != nil {
			return writeAdminAppleIDBadRequest(w, err)
		}
		result, err := service.AdminGetOrderDetail(r.Context(), id)
		if err != nil {
			return handleAdminAppleIDError(w, err)
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": result})
		return true
	case "order/credentials":
		if r.Method != http.MethodPost {
			return false
		}
		inputs, err := readInputs(r)
		if err != nil {
			return writeAdminAppleIDBadRequest(w, err)
		}
		id, err := requiredAppleIDInt64(inputs, "id")
		if err != nil {
			return writeAdminAppleIDBadRequest(w, err)
		}
		result, err := service.AdminGetOrderCredentials(r.Context(), id, identity.ID)
		if err != nil {
			return handleAdminAppleIDError(w, err)
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": result})
		return true
	case "order/replace":
		if r.Method != http.MethodPost {
			return false
		}
		inputs, err := readInputs(r)
		if err != nil {
			return writeAdminAppleIDBadRequest(w, err)
		}
		orderID, err := requiredAppleIDInt64(inputs, "id")
		if err != nil {
			return writeAdminAppleIDBadRequest(w, err)
		}
		inventoryID, err := optionalAppleIDInt64(inputs, "inventory_id")
		if err != nil {
			return writeAdminAppleIDBadRequest(w, err)
		}
		result, err := service.AdminReplaceInventory(r.Context(), appleid.AdminReplaceInventoryRequest{OrderID: orderID, NewInventoryID: inventoryID, AdminID: identity.ID, Reason: inputs["reason"]})
		if err != nil {
			return handleAdminAppleIDError(w, err)
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": result})
		return true
	case "order/refund":
		if r.Method != http.MethodPost {
			return false
		}
		inputs, err := readInputs(r)
		if err != nil {
			return writeAdminAppleIDBadRequest(w, err)
		}
		orderID, err := requiredAppleIDInt64(inputs, "id")
		if err != nil {
			return writeAdminAppleIDBadRequest(w, err)
		}
		if err := service.AdminMarkOrderRefunded(r.Context(), appleid.AdminMarkRefundedRequest{OrderID: orderID, AdminID: identity.ID, Reason: inputs["reason"]}); err != nil {
			return handleAdminAppleIDError(w, err)
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": true})
		return true
	case "order/cancel":
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "Method not allowed"})
			return true
		}
		inputs, err := readInputs(r)
		if err != nil {
			return writeAdminAppleIDBadRequest(w, err)
		}
		orderID, err := requiredAppleIDInt64(inputs, "id")
		if err != nil {
			return writeAdminAppleIDBadRequest(w, err)
		}
		if err := service.AdminCancelOrder(r.Context(), appleid.AdminCancelOrderRequest{OrderID: orderID, AdminID: identity.ID, Reason: inputs["reason"]}); err != nil {
			return handleAdminAppleIDError(w, err)
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": true})
		return true
	case "order/audits":
		if r.Method != http.MethodGet {
			return false
		}
		inputs, err := readInputs(r)
		if err != nil {
			return writeAdminAppleIDBadRequest(w, err)
		}
		orderID, err := requiredAppleIDInt64(inputs, "id")
		if err != nil {
			return writeAdminAppleIDBadRequest(w, err)
		}
		current, err := optionalAppleIDInt64Value(inputs, "current")
		if err != nil {
			return writeAdminAppleIDBadRequest(w, err)
		}
		pageSize, err := optionalAppleIDInt64Value(inputs, "page_size")
		if err != nil {
			return writeAdminAppleIDBadRequest(w, err)
		}
		result, err := service.AdminListAudits(r.Context(), appleid.AdminAuditListRequest{OrderID: orderID, Current: current, PageSize: pageSize})
		if err != nil {
			return handleAdminAppleIDError(w, err)
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": result.Data, "total": result.Total})
		return true
	default:
		return false
	}
}

func parseAdminAppleInventoryList(inputs map[string]string) (appleid.AdminInventoryListRequest, error) {
	current, err := optionalAppleIDInt64Value(inputs, "current")
	if err != nil {
		return appleid.AdminInventoryListRequest{}, err
	}
	pageSize, err := optionalAppleIDInt64Value(inputs, "page_size")
	if err != nil {
		return appleid.AdminInventoryListRequest{}, err
	}
	productID, err := optionalAppleIDInt64(inputs, "product_id")
	if err != nil {
		return appleid.AdminInventoryListRequest{}, err
	}
	status, err := optionalAppleIDInt64(inputs, "status")
	if err != nil {
		return appleid.AdminInventoryListRequest{}, err
	}
	return appleid.AdminInventoryListRequest{Current: current, PageSize: pageSize, ProductID: productID, Status: status}, nil
}

func parseAdminAppleOrderList(inputs map[string]string) (appleid.AdminOrderListRequest, error) {
	current, err := optionalAppleIDInt64Value(inputs, "current")
	if err != nil {
		return appleid.AdminOrderListRequest{}, err
	}
	pageSize, err := optionalAppleIDInt64Value(inputs, "page_size")
	if err != nil {
		return appleid.AdminOrderListRequest{}, err
	}
	productID, err := optionalAppleIDInt64(inputs, "product_id")
	if err != nil {
		return appleid.AdminOrderListRequest{}, err
	}
	status, err := optionalAppleIDInt64(inputs, "status")
	if err != nil {
		return appleid.AdminOrderListRequest{}, err
	}
	return appleid.AdminOrderListRequest{Current: current, PageSize: pageSize, ProductID: productID, Status: status, UserEmail: inputs["email"], TradeNo: inputs["trade_no"]}, nil
}

func parseAdminAppleFinance(inputs map[string]string) (appleid.AdminFinanceRequest, error) {
	current, err := optionalAppleIDInt64Value(inputs, "current")
	if err != nil {
		return appleid.AdminFinanceRequest{}, err
	}
	pageSize, err := optionalAppleIDInt64Value(inputs, "page_size")
	if err != nil {
		return appleid.AdminFinanceRequest{}, err
	}
	productID, err := optionalAppleIDInt64(inputs, "product_id")
	if err != nil {
		return appleid.AdminFinanceRequest{}, err
	}
	if current < 0 || pageSize < 0 || (productID != nil && *productID <= 0) {
		return appleid.AdminFinanceRequest{}, appleid.ErrInvalidParameter
	}
	return appleid.AdminFinanceRequest{StartDate: inputs["start_date"], EndDate: inputs["end_date"], ProductID: productID, Current: current, PageSize: pageSize}, nil
}

func requiredAppleIDInt64(inputs map[string]string, key string) (int64, error) {
	value, err := strconv.ParseInt(strings.TrimSpace(inputs[key]), 10, 64)
	if err != nil || value <= 0 {
		return 0, appleid.ErrInvalidParameter
	}
	return value, nil
}

func optionalAppleIDInt64(inputs map[string]string, key string) (*int64, error) {
	raw := strings.TrimSpace(inputs[key])
	if raw == "" {
		return nil, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return nil, appleid.ErrInvalidParameter
	}
	return &value, nil
}

func optionalAppleIDInt64Value(inputs map[string]string, key string) (int64, error) {
	value, err := optionalAppleIDInt64(inputs, key)
	if err != nil || value == nil {
		return 0, err
	}
	return *value, nil
}

func parseAppleIDBool(raw string, fallback bool) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	case "":
		return fallback, nil
	default:
		return false, appleid.ErrInvalidParameter
	}
}

func firstAppleIDValue(inputs map[string]string, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(inputs[key]); value != "" {
			return value
		}
	}
	return ""
}

func writeAdminAppleIDBadRequest(w http.ResponseWriter, err error) bool {
	writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
	return true
}

func handleAdminAppleIDError(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, appleid.ErrInvalidParameter), errors.Is(err, appleid.ErrAdminAuditRequired):
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
	case errors.Is(err, appleid.ErrProductNotFound), errors.Is(err, appleid.ErrOrderNotFound), errors.Is(err, appleid.ErrInventoryNotFound):
		writeJSON(w, http.StatusNotFound, map[string]any{"message": err.Error()})
	case errors.Is(err, appleid.ErrProductDisabled), errors.Is(err, appleid.ErrOutOfStock), errors.Is(err, appleid.ErrDuplicateInventory), errors.Is(err, appleid.ErrProductInUse), errors.Is(err, appleid.ErrInventorySold), errors.Is(err, appleid.ErrInventoryReserved), errors.Is(err, appleid.ErrOrderStatus), errors.Is(err, appleid.ErrDeliveryNotReady):
		writeJSON(w, http.StatusConflict, map[string]any{"message": err.Error()})
	case errors.Is(err, appleid.ErrUnavailable):
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "apple id service unavailable"})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "Apple ID operation failed"})
	}
	return true
}
