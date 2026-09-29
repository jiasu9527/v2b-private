package appleid

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	ErrProductInUse       = errors.New("apple id product is in use")
	ErrInventorySold      = errors.New("sold apple id inventory cannot be changed")
	ErrInventoryReserved  = errors.New("reserved apple id inventory cannot be changed")
	ErrOrderStatus        = errors.New("apple id order status does not allow this operation")
	ErrAdminAuditRequired = errors.New("admin audit information is required")
)

const (
	adminDefaultPageSize = int64(20)
	adminMaxPageSize     = int64(200)
	adminMaxInventoryAdd = 500
)

// AdminProduct is the full product representation used by the management API.
// Unlike Product, it also includes disabled products and stock totals by state.
type AdminProduct struct {
	ID                int64  `json:"id"`
	Name              string `json:"name"`
	Region            string `json:"region"`
	OwnedShadowrocket bool   `json:"owned_shadowrocket"`
	Price             int64  `json:"price"`
	AfterSales        string `json:"after_sales"`
	Enabled           bool   `json:"enabled"`
	AvailableStock    int64  `json:"available_stock"`
	ReservedStock     int64  `json:"reserved_stock"`
	SoldStock         int64  `json:"sold_stock"`
	DisabledStock     int64  `json:"disabled_stock"`
	CreatedAt         int64  `json:"created_at"`
	UpdatedAt         int64  `json:"updated_at"`
}

type AdminSaveProductRequest struct {
	ID                *int64 `json:"id,omitempty"`
	Name              string `json:"name"`
	Region            string `json:"region"`
	OwnedShadowrocket bool   `json:"owned_shadowrocket"`
	Price             int64  `json:"price"`
	AfterSales        string `json:"after_sales"`
	Enabled           *bool  `json:"enabled,omitempty"`
}

type AdminSetProductEnabledRequest struct {
	ProductID int64 `json:"product_id"`
	Enabled   bool  `json:"enabled"`
}

type AdminInventoryCredential struct {
	// Credential is a complete one-line delivery record. Account and Password
	// remain accepted for compatibility with older API clients.
	Credential string `json:"credential,omitempty"`
	Account    string `json:"account"`
	Password   string `json:"password"`
}

type AdminAddInventoryRequest struct {
	ProductID int64                      `json:"product_id"`
	Items     []AdminInventoryCredential `json:"items"`
}

type AdminAddInventoryResult struct {
	IDs   []int64 `json:"ids"`
	Count int     `json:"count"`
}

// AdminInventory returns the original account value to administrators. For
// one-line imports, Account contains the complete delivery record.
type AdminInventory struct {
	ID              int64  `json:"id"`
	ProductID       int64  `json:"product_id"`
	ProductName     string `json:"product_name"`
	Account         string `json:"account"`
	Status          int64  `json:"status"`
	ReservedOrderID *int64 `json:"reserved_order_id,omitempty"`
	ReservedUntil   *int64 `json:"reserved_until,omitempty"`
	SoldOrderID     *int64 `json:"sold_order_id,omitempty"`
	CreatedAt       int64  `json:"created_at"`
	UpdatedAt       int64  `json:"updated_at"`
}

type AdminInventoryListRequest struct {
	Current   int64  `json:"current"`
	PageSize  int64  `json:"page_size"`
	ProductID *int64 `json:"product_id,omitempty"`
	Status    *int64 `json:"status,omitempty"`
}

type AdminInventoryListResult struct {
	Data  []AdminInventory `json:"data"`
	Total int64            `json:"total"`
}

type AdminDisableInventoryRequest struct {
	InventoryID int64 `json:"inventory_id"`
	AdminID     int64 `json:"-"`
}

// AdminOrder includes the original account value for administrator list/detail
// responses. A separate Password field is deliberately absent from the type.
type AdminOrder struct {
	ID                int64   `json:"id"`
	BusinessType      string  `json:"business_type"`
	UserID            int64   `json:"user_id"`
	UserEmail         string  `json:"user_email"`
	TradeNo           string  `json:"trade_no"`
	ProductID         int64   `json:"product_id"`
	ProductName       string  `json:"product_name"`
	Region            string  `json:"region"`
	Price             int64   `json:"price"`
	HandlingAmount    int64   `json:"handling_amount"`
	TotalAmount       int64   `json:"total_amount"`
	Status            int64   `json:"status"`
	PaymentID         *int64  `json:"payment_id,omitempty"`
	CallbackNo        *string `json:"callback_no,omitempty"`
	InventoryID       *int64  `json:"inventory_id,omitempty"`
	Account           string  `json:"account,omitempty"`
	ReservationExpiry *int64  `json:"reserved_until,omitempty"`
	PaidAt            *int64  `json:"paid_at,omitempty"`
	CreatedAt         int64   `json:"created_at"`
	UpdatedAt         int64   `json:"updated_at"`
}

type AdminOrderListRequest struct {
	Current   int64  `json:"current"`
	PageSize  int64  `json:"page_size"`
	Status    *int64 `json:"status,omitempty"`
	ProductID *int64 `json:"product_id,omitempty"`
	UserEmail string `json:"user_email,omitempty"`
	TradeNo   string `json:"trade_no,omitempty"`
}

type AdminOrderListResult struct {
	Data  []AdminOrder `json:"data"`
	Total int64        `json:"total"`
}

type AdminOrderDetail struct {
	Order  AdminOrder        `json:"order"`
	Audits []AdminAuditEntry `json:"audits"`
}

// AdminCredentialDetail is returned only by AdminGetOrderCredentials. Reading
// credentials requires an admin actor and does not write an audit row.
type AdminCredentialDetail struct {
	OrderID     int64  `json:"order_id"`
	TradeNo     string `json:"trade_no"`
	InventoryID int64  `json:"inventory_id"`
	ProductID   int64  `json:"product_id"`
	Account     string `json:"account"`
	Password    string `json:"password"`
	Credential  string `json:"credential,omitempty"`
}

type AdminReplaceInventoryRequest struct {
	OrderID        int64  `json:"order_id"`
	NewInventoryID *int64 `json:"new_inventory_id,omitempty"`
	AdminID        int64  `json:"-"`
	Reason         string `json:"reason,omitempty"`
}

type AdminReplaceInventoryResult struct {
	OrderID             int64  `json:"order_id"`
	PreviousInventoryID int64  `json:"previous_inventory_id"`
	NewInventoryID      int64  `json:"new_inventory_id"`
	PreviousAccount     string `json:"previous_account"`
	NewAccount          string `json:"new_account"`
}

type AdminMarkRefundedRequest struct {
	OrderID int64  `json:"order_id"`
	AdminID int64  `json:"-"`
	Reason  string `json:"reason,omitempty"`
}

// AdminCancelOrderRequest cancels an unpaid order and releases the inventory
// reservation held by that order. Cancellation is deliberately restricted to
// pending orders; paid/manual orders must go through the refund workflow.
type AdminCancelOrderRequest struct {
	OrderID int64  `json:"order_id"`
	AdminID int64  `json:"-"`
	Reason  string `json:"reason,omitempty"`
}

// AdminAuditDetail is intentionally structured: there is no credential or
// password field, preventing callers from accidentally persisting secrets.
type AdminAuditDetail struct {
	Reason              string `json:"reason,omitempty"`
	InventoryID         *int64 `json:"inventory_id,omitempty"`
	PreviousInventoryID *int64 `json:"previous_inventory_id,omitempty"`
	NewInventoryID      *int64 `json:"new_inventory_id,omitempty"`
	Status              *int64 `json:"status,omitempty"`
	Account             string `json:"account,omitempty"`
}

type AdminAuditWriteRequest struct {
	OrderID int64            `json:"order_id"`
	AdminID int64            `json:"-"`
	Action  string           `json:"action"`
	Detail  AdminAuditDetail `json:"detail"`
}

type AdminAuditEntry struct {
	ID        int64            `json:"id"`
	OrderID   int64            `json:"order_id"`
	AdminID   *int64           `json:"actor_admin_id,omitempty"`
	Action    string           `json:"action"`
	Detail    AdminAuditDetail `json:"detail"`
	CreatedAt int64            `json:"created_at"`
}

type AdminAuditListRequest struct {
	OrderID  int64 `json:"order_id"`
	Current  int64 `json:"current"`
	PageSize int64 `json:"page_size"`
}

type AdminAuditListResult struct {
	Data  []AdminAuditEntry `json:"data"`
	Total int64             `json:"total"`
}

func (s *DBService) AdminListProducts(ctx context.Context) ([]AdminProduct, error) {
	if err := s.ensureSchema(ctx); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT p.id,p.name,p.region,p.owned_shadowrocket,p.price,p.after_sales,p.enabled,
	COUNT(i.id) FILTER (WHERE i.status=0),
	COUNT(i.id) FILTER (WHERE i.status=1),
	COUNT(i.id) FILTER (WHERE i.status=2),
	COUNT(i.id) FILTER (WHERE i.status=3),
	p.created_at,p.updated_at
FROM v2_apple_product p
LEFT JOIN v2_apple_inventory i ON i.product_id=p.id
GROUP BY p.id
ORDER BY p.id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list admin apple products: %w", err)
	}
	defer rows.Close()
	result := make([]AdminProduct, 0)
	for rows.Next() {
		var item AdminProduct
		var owned, enabled int64
		if err := rows.Scan(&item.ID, &item.Name, &item.Region, &owned, &item.Price, &item.AfterSales, &enabled,
			&item.AvailableStock, &item.ReservedStock, &item.SoldStock, &item.DisabledStock, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan admin apple product: %w", err)
		}
		item.OwnedShadowrocket = owned != 0
		item.Enabled = enabled != 0
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate admin apple products: %w", err)
	}
	return result, nil
}

func (s *DBService) AdminSaveProduct(ctx context.Context, req AdminSaveProductRequest) (AdminProduct, error) {
	if err := s.ensureSchema(ctx); err != nil {
		return AdminProduct{}, err
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Region = strings.TrimSpace(req.Region)
	req.AfterSales = strings.TrimSpace(req.AfterSales)
	if req.Name == "" || req.Region == "" || req.Price <= 0 || utf8.RuneCountInString(req.Name) > 255 || utf8.RuneCountInString(req.Region) > 64 {
		return AdminProduct{}, ErrInvalidParameter
	}
	now := time.Now().Unix()
	if req.ID == nil {
		enabled := true
		if req.Enabled != nil {
			enabled = *req.Enabled
		}
		var item AdminProduct
		var owned, enabledValue int64
		err := s.db.QueryRowContext(ctx, `INSERT INTO v2_apple_product(name,region,owned_shadowrocket,price,after_sales,enabled,created_at,updated_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$7)
RETURNING id,name,region,owned_shadowrocket,price,after_sales,enabled,created_at,updated_at`,
			req.Name, req.Region, boolInt(req.OwnedShadowrocket), req.Price, req.AfterSales, boolInt(enabled), now,
		).Scan(&item.ID, &item.Name, &item.Region, &owned, &item.Price, &item.AfterSales, &enabledValue, &item.CreatedAt, &item.UpdatedAt)
		if err != nil {
			return AdminProduct{}, fmt.Errorf("create apple product: %w", err)
		}
		item.OwnedShadowrocket = owned != 0
		item.Enabled = enabledValue != 0
		return item, nil
	}
	if *req.ID <= 0 {
		return AdminProduct{}, ErrInvalidParameter
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AdminProduct{}, err
	}
	defer tx.Rollback()
	var currentEnabled int64
	if err := tx.QueryRowContext(ctx, `SELECT enabled FROM v2_apple_product WHERE id=$1 FOR UPDATE`, *req.ID).Scan(&currentEnabled); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return AdminProduct{}, ErrProductNotFound
		}
		return AdminProduct{}, err
	}
	if req.Enabled != nil {
		currentEnabled = boolInt(*req.Enabled)
	}
	var item AdminProduct
	var owned, enabled int64
	if err := tx.QueryRowContext(ctx, `UPDATE v2_apple_product
SET name=$2,region=$3,owned_shadowrocket=$4,price=$5,after_sales=$6,enabled=$7,updated_at=$8
WHERE id=$1
RETURNING id,name,region,owned_shadowrocket,price,after_sales,enabled,created_at,updated_at`,
		*req.ID, req.Name, req.Region, boolInt(req.OwnedShadowrocket), req.Price, req.AfterSales, currentEnabled, now,
	).Scan(&item.ID, &item.Name, &item.Region, &owned, &item.Price, &item.AfterSales, &enabled, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return AdminProduct{}, fmt.Errorf("update apple product: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return AdminProduct{}, err
	}
	item.OwnedShadowrocket = owned != 0
	item.Enabled = enabled != 0
	return item, nil
}

func (s *DBService) AdminSetProductEnabled(ctx context.Context, req AdminSetProductEnabledRequest) error {
	if err := s.ensureSchema(ctx); err != nil {
		return err
	}
	if req.ProductID <= 0 {
		return ErrInvalidParameter
	}
	result, err := s.db.ExecContext(ctx, `UPDATE v2_apple_product SET enabled=$2,updated_at=$3 WHERE id=$1`, req.ProductID, boolInt(req.Enabled), time.Now().Unix())
	if err != nil {
		return fmt.Errorf("set apple product enabled: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrProductNotFound
	}
	return nil
}

func (s *DBService) AdminDeleteProduct(ctx context.Context, productID int64) error {
	if err := s.ensureSchema(ctx); err != nil {
		return err
	}
	if productID <= 0 {
		return ErrInvalidParameter
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM v2_apple_product WHERE id=$1 FOR UPDATE`, productID).Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrProductNotFound
		}
		return err
	}
	var references int64
	if err := tx.QueryRowContext(ctx, `SELECT
	(SELECT COUNT(*) FROM v2_apple_inventory WHERE product_id=$1) +
	(SELECT COUNT(*) FROM v2_apple_order WHERE product_id=$1)`, productID).Scan(&references); err != nil {
		return err
	}
	if references != 0 {
		return ErrProductInUse
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM v2_apple_product WHERE id=$1`, productID); err != nil {
		return fmt.Errorf("delete apple product: %w", err)
	}
	return tx.Commit()
}

func (s *DBService) AdminAddInventoryBatch(ctx context.Context, req AdminAddInventoryRequest) (AdminAddInventoryResult, error) {
	if err := s.ensureSchema(ctx); err != nil {
		return AdminAddInventoryResult{}, err
	}
	if req.ProductID <= 0 || len(req.Items) == 0 || len(req.Items) > adminMaxInventoryAdd {
		return AdminAddInventoryResult{}, ErrInvalidParameter
	}
	type encryptedItem struct {
		account     string
		password    string
		fingerprint string
	}
	items := make([]encryptedItem, 0, len(req.Items))
	seen := make(map[string]struct{}, len(req.Items))
	for _, raw := range req.Items {
		account := raw.Account
		password := raw.Password
		if raw.Credential != "" {
			account = raw.Credential
			password = ""
		}
		if strings.TrimSpace(account) == "" || (raw.Credential == "" && password == "") {
			return AdminAddInventoryResult{}, ErrInvalidParameter
		}
		key := strings.ToLower(strings.TrimSpace(account))
		if _, exists := seen[key]; exists {
			return AdminAddInventoryResult{}, ErrDuplicateInventory
		}
		seen[key] = struct{}{}
		a, err := s.encrypt(account)
		if err != nil {
			return AdminAddInventoryResult{}, err
		}
		p, err := s.encrypt(password)
		if err != nil {
			return AdminAddInventoryResult{}, err
		}
		fingerprint, err := s.accountFingerprint(account)
		if err != nil {
			return AdminAddInventoryResult{}, err
		}
		items = append(items, encryptedItem{account: a, password: p, fingerprint: fingerprint})
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AdminAddInventoryResult{}, err
	}
	defer tx.Rollback()
	var product int64
	// A product row lock serializes imports for that product, allowing duplicate
	// detection despite randomized ciphertext and no plaintext/account hash.
	if err := tx.QueryRowContext(ctx, `SELECT id FROM v2_apple_product WHERE id=$1 FOR UPDATE`, req.ProductID).Scan(&product); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return AdminAddInventoryResult{}, ErrProductNotFound
		}
		return AdminAddInventoryResult{}, err
	}
	now := time.Now().Unix()
	result := AdminAddInventoryResult{IDs: make([]int64, 0, len(items))}
	for _, item := range items {
		var id int64
		if err := tx.QueryRowContext(ctx, `INSERT INTO v2_apple_inventory(product_id,account_ciphertext,password_ciphertext,account_fingerprint,status,created_at,updated_at)
VALUES($1,$2,$3,$4,0,$5,$5)
ON CONFLICT (account_fingerprint) WHERE account_fingerprint <> '' DO NOTHING
RETURNING id`, req.ProductID, item.account, item.password, item.fingerprint, now).Scan(&id); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return AdminAddInventoryResult{}, ErrDuplicateInventory
			}
			return AdminAddInventoryResult{}, fmt.Errorf("insert apple inventory: %w", err)
		}
		result.IDs = append(result.IDs, id)
	}
	if err := tx.Commit(); err != nil {
		return AdminAddInventoryResult{}, err
	}
	result.Count = len(result.IDs)
	return result, nil
}

func (s *DBService) AdminListInventory(ctx context.Context, req AdminInventoryListRequest) (AdminInventoryListResult, error) {
	if err := s.ensureSchema(ctx); err != nil {
		return AdminInventoryListResult{}, err
	}
	if req.ProductID != nil && *req.ProductID <= 0 || req.Status != nil && (*req.Status < InventoryAvailable || *req.Status > InventoryDisabled) {
		return AdminInventoryListResult{}, ErrInvalidParameter
	}
	current, pageSize := adminPage(req.Current, req.PageSize)
	where, args := adminInventoryWhere(req)
	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_apple_inventory i`+where, args...).Scan(&total); err != nil {
		return AdminInventoryListResult{}, fmt.Errorf("count apple inventory: %w", err)
	}
	queryArgs := append(append([]any{}, args...), pageSize, (current-1)*pageSize)
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`SELECT i.id,i.product_id,p.name,i.account_ciphertext,i.status,i.reserved_order_id,i.reserved_until,i.sold_order_id,i.created_at,i.updated_at
FROM v2_apple_inventory i
JOIN v2_apple_product p ON p.id=i.product_id%s
ORDER BY i.id DESC LIMIT $%d OFFSET $%d`, where, len(queryArgs)-1, len(queryArgs)), queryArgs...)
	if err != nil {
		return AdminInventoryListResult{}, fmt.Errorf("list apple inventory: %w", err)
	}
	defer rows.Close()
	data := make([]AdminInventory, 0)
	for rows.Next() {
		item, err := s.scanAdminInventory(rows)
		if err != nil {
			return AdminInventoryListResult{}, err
		}
		data = append(data, item)
	}
	if err := rows.Err(); err != nil {
		return AdminInventoryListResult{}, err
	}
	return AdminInventoryListResult{Data: data, Total: total}, nil
}

func (s *DBService) AdminDisableInventory(ctx context.Context, req AdminDisableInventoryRequest) error {
	if err := s.ensureSchema(ctx); err != nil {
		return err
	}
	if req.InventoryID <= 0 || req.AdminID <= 0 {
		return ErrInvalidParameter
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status int64
	if err := tx.QueryRowContext(ctx, `SELECT status FROM v2_apple_inventory WHERE id=$1 FOR UPDATE`, req.InventoryID).Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrInventoryNotFound
		}
		return err
	}
	switch status {
	case InventorySold:
		return ErrInventorySold
	case InventoryReserved:
		return ErrInventoryReserved
	case InventoryDisabled:
		return tx.Commit()
	case InventoryAvailable:
		if _, err := tx.ExecContext(ctx, `UPDATE v2_apple_inventory SET status=3,updated_at=$2 WHERE id=$1 AND status=0`, req.InventoryID, time.Now().Unix()); err != nil {
			return err
		}
	default:
		return ErrInvalidParameter
	}
	return tx.Commit()
}

func (s *DBService) AdminListOrders(ctx context.Context, req AdminOrderListRequest) (AdminOrderListResult, error) {
	if err := s.ensureSchema(ctx); err != nil {
		return AdminOrderListResult{}, err
	}
	if req.Status != nil && (*req.Status < OrderPending || *req.Status > OrderManual) || req.ProductID != nil && *req.ProductID <= 0 {
		return AdminOrderListResult{}, ErrInvalidParameter
	}
	current, pageSize := adminPage(req.Current, req.PageSize)
	where, args := adminOrderWhere(req)
	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_apple_order o LEFT JOIN v2_user u ON u.id=o.user_id`+where, args...).Scan(&total); err != nil {
		return AdminOrderListResult{}, fmt.Errorf("count admin apple orders: %w", err)
	}
	queryArgs := append(append([]any{}, args...), pageSize, (current-1)*pageSize)
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`SELECT o.id,o.user_id,COALESCE(u.email,''),o.trade_no,o.product_id,o.product_name,o.region,o.price,o.handling_amount,o.status,o.payment_id,o.callback_no,o.inventory_id,i.account_ciphertext,o.reserved_until,o.paid_at,o.created_at,o.updated_at
FROM v2_apple_order o
LEFT JOIN v2_user u ON u.id=o.user_id
LEFT JOIN v2_apple_inventory i ON i.id=o.inventory_id%s
ORDER BY o.created_at DESC,o.id DESC LIMIT $%d OFFSET $%d`, where, len(queryArgs)-1, len(queryArgs)), queryArgs...)
	if err != nil {
		return AdminOrderListResult{}, fmt.Errorf("list admin apple orders: %w", err)
	}
	defer rows.Close()
	data := make([]AdminOrder, 0)
	for rows.Next() {
		item, err := s.scanAdminOrder(rows)
		if err != nil {
			return AdminOrderListResult{}, err
		}
		data = append(data, item)
	}
	if err := rows.Err(); err != nil {
		return AdminOrderListResult{}, err
	}
	return AdminOrderListResult{Data: data, Total: total}, nil
}

func (s *DBService) AdminGetOrderDetail(ctx context.Context, orderID int64) (AdminOrderDetail, error) {
	if err := s.ensureSchema(ctx); err != nil {
		return AdminOrderDetail{}, err
	}
	if orderID <= 0 {
		return AdminOrderDetail{}, ErrInvalidParameter
	}
	row := s.db.QueryRowContext(ctx, `SELECT o.id,o.user_id,COALESCE(u.email,''),o.trade_no,o.product_id,o.product_name,o.region,o.price,o.handling_amount,o.status,o.payment_id,o.callback_no,o.inventory_id,i.account_ciphertext,o.reserved_until,o.paid_at,o.created_at,o.updated_at
FROM v2_apple_order o
LEFT JOIN v2_user u ON u.id=o.user_id
LEFT JOIN v2_apple_inventory i ON i.id=o.inventory_id
WHERE o.id=$1`, orderID)
	order, err := s.scanAdminOrder(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return AdminOrderDetail{}, ErrOrderNotFound
		}
		return AdminOrderDetail{}, err
	}
	audits, err := s.AdminListAudits(ctx, AdminAuditListRequest{OrderID: orderID, Current: 1, PageSize: adminMaxPageSize})
	if err != nil {
		return AdminOrderDetail{}, err
	}
	return AdminOrderDetail{Order: order, Audits: audits.Data}, nil
}

func (s *DBService) AdminGetOrderCredentials(ctx context.Context, orderID, adminID int64) (AdminCredentialDetail, error) {
	if err := s.ensureSchema(ctx); err != nil {
		return AdminCredentialDetail{}, err
	}
	if orderID <= 0 || adminID <= 0 {
		return AdminCredentialDetail{}, ErrAdminAuditRequired
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AdminCredentialDetail{}, err
	}
	defer tx.Rollback()
	var result AdminCredentialDetail
	var accountCiphertext, passwordCiphertext string
	var status, inventoryStatus int64
	err = tx.QueryRowContext(ctx, `SELECT o.id,o.trade_no,o.product_id,o.inventory_id,o.status,i.status,i.account_ciphertext,i.password_ciphertext
FROM v2_apple_order o
JOIN v2_apple_inventory i ON i.id=o.inventory_id
WHERE o.id=$1
FOR SHARE OF o,i`, orderID).Scan(&result.OrderID, &result.TradeNo, &result.ProductID, &result.InventoryID, &status, &inventoryStatus, &accountCiphertext, &passwordCiphertext)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return AdminCredentialDetail{}, ErrOrderNotFound
		}
		return AdminCredentialDetail{}, err
	}
	if status != OrderPaid && status != OrderRefunded || inventoryStatus != InventorySold {
		return AdminCredentialDetail{}, ErrDeliveryNotReady
	}
	result.Account, err = s.decrypt(accountCiphertext)
	if err != nil {
		return AdminCredentialDetail{}, err
	}
	result.Password, err = s.decrypt(passwordCiphertext)
	if err != nil {
		return AdminCredentialDetail{}, err
	}
	if result.Password == "" {
		result.Credential = result.Account
	}
	if err := tx.Commit(); err != nil {
		return AdminCredentialDetail{}, err
	}
	return result, nil
}

func (s *DBService) AdminReplaceInventory(ctx context.Context, req AdminReplaceInventoryRequest) (AdminReplaceInventoryResult, error) {
	if err := s.ensureSchema(ctx); err != nil {
		return AdminReplaceInventoryResult{}, err
	}
	if req.OrderID <= 0 || req.AdminID <= 0 || req.NewInventoryID != nil && *req.NewInventoryID <= 0 {
		return AdminReplaceInventoryResult{}, ErrInvalidParameter
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AdminReplaceInventoryResult{}, err
	}
	defer tx.Rollback()
	var productID, oldInventoryID, orderStatus int64
	if err := tx.QueryRowContext(ctx, `SELECT product_id,inventory_id,status FROM v2_apple_order WHERE id=$1 FOR UPDATE`, req.OrderID).Scan(&productID, &oldInventoryID, &orderStatus); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return AdminReplaceInventoryResult{}, ErrOrderNotFound
		}
		return AdminReplaceInventoryResult{}, err
	}
	if orderStatus != OrderPaid {
		return AdminReplaceInventoryResult{}, ErrOrderStatus
	}
	var oldStatus int64
	var oldSoldOrder sql.NullInt64
	var oldAccountCiphertext string
	if err := tx.QueryRowContext(ctx, `SELECT status,sold_order_id,account_ciphertext FROM v2_apple_inventory WHERE id=$1 FOR UPDATE`, oldInventoryID).Scan(&oldStatus, &oldSoldOrder, &oldAccountCiphertext); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return AdminReplaceInventoryResult{}, ErrInventoryNotFound
		}
		return AdminReplaceInventoryResult{}, err
	}
	if oldStatus != InventorySold || !oldSoldOrder.Valid || oldSoldOrder.Int64 != req.OrderID {
		return AdminReplaceInventoryResult{}, ErrOrderStatus
	}
	var newInventoryID, newStatus int64
	var newAccountCiphertext string
	if req.NewInventoryID != nil {
		if *req.NewInventoryID == oldInventoryID {
			return AdminReplaceInventoryResult{}, ErrInvalidParameter
		}
		err = tx.QueryRowContext(ctx, `SELECT id,status,account_ciphertext FROM v2_apple_inventory WHERE id=$1 AND product_id=$2 AND status=0 FOR UPDATE SKIP LOCKED`, *req.NewInventoryID, productID).Scan(&newInventoryID, &newStatus, &newAccountCiphertext)
	} else {
		err = tx.QueryRowContext(ctx, `SELECT id,status,account_ciphertext FROM v2_apple_inventory WHERE product_id=$1 AND status=0 ORDER BY id FOR UPDATE SKIP LOCKED LIMIT 1`, productID).Scan(&newInventoryID, &newStatus, &newAccountCiphertext)
	}
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return AdminReplaceInventoryResult{}, ErrOutOfStock
		}
		return AdminReplaceInventoryResult{}, err
	}
	if newStatus != InventoryAvailable {
		return AdminReplaceInventoryResult{}, ErrInventoryReserved
	}
	oldAccount, err := s.decrypt(oldAccountCiphertext)
	if err != nil {
		return AdminReplaceInventoryResult{}, err
	}
	newAccount, err := s.decrypt(newAccountCiphertext)
	if err != nil {
		return AdminReplaceInventoryResult{}, err
	}
	now := time.Now().Unix()
	// Replacement is the sole operation allowed to retire a sold row. The old
	// sold_order_id remains intact as provenance; it is never made sellable.
	if _, err := tx.ExecContext(ctx, `UPDATE v2_apple_inventory SET status=3,updated_at=$2 WHERE id=$1 AND status=2`, oldInventoryID, now); err != nil {
		return AdminReplaceInventoryResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE v2_apple_inventory SET status=2,sold_order_id=$2,reserved_order_id=NULL,reserved_until=NULL,updated_at=$3 WHERE id=$1 AND status=0`, newInventoryID, req.OrderID, now); err != nil {
		return AdminReplaceInventoryResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE v2_apple_order SET inventory_id=$2,updated_at=$3 WHERE id=$1`, req.OrderID, newInventoryID, now); err != nil {
		return AdminReplaceInventoryResult{}, err
	}
	detail := AdminAuditDetail{
		Reason:              cleanAuditReason(req.Reason),
		PreviousInventoryID: &oldInventoryID,
		NewInventoryID:      &newInventoryID,
		Account:             maskAppleAccount(newAccount),
	}
	if err := insertAdminAudit(ctx, tx, AdminAuditWriteRequest{OrderID: req.OrderID, AdminID: req.AdminID, Action: "inventory_replace", Detail: detail}); err != nil {
		return AdminReplaceInventoryResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return AdminReplaceInventoryResult{}, err
	}
	return AdminReplaceInventoryResult{
		OrderID: req.OrderID, PreviousInventoryID: oldInventoryID, NewInventoryID: newInventoryID,
		PreviousAccount: oldAccount, NewAccount: newAccount,
	}, nil
}

// AdminMarkOrderRefunded records the result of a refund. The actual payment
// gateway refund must finish before this method is called.
func (s *DBService) AdminMarkOrderRefunded(ctx context.Context, req AdminMarkRefundedRequest) error {
	if err := s.ensureSchema(ctx); err != nil {
		return err
	}
	if req.OrderID <= 0 || req.AdminID <= 0 {
		return ErrInvalidParameter
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status int64
	if err := tx.QueryRowContext(ctx, `SELECT status FROM v2_apple_order WHERE id=$1 FOR UPDATE`, req.OrderID).Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrOrderNotFound
		}
		return err
	}
	if status == OrderRefunded {
		return tx.Commit()
	}
	if status != OrderPaid && status != OrderManual {
		return ErrOrderStatus
	}
	now := time.Now().Unix()
	if _, err := tx.ExecContext(ctx, `UPDATE v2_apple_order SET status=3,updated_at=$2 WHERE id=$1`, req.OrderID, now); err != nil {
		return err
	}
	newStatus := OrderRefunded
	if err := insertAdminAudit(ctx, tx, AdminAuditWriteRequest{
		OrderID: req.OrderID,
		AdminID: req.AdminID,
		Action:  "refund_confirmed",
		Detail:  AdminAuditDetail{Reason: cleanAuditReason(req.Reason), Status: &newStatus},
	}); err != nil {
		return err
	}
	return tx.Commit()
}

// AdminCancelOrder cancels an unpaid order, returns its reserved inventory to
// the available pool, and records the administrator action. The order and
// inventory rows are locked in one transaction so a concurrent payment
// callback either wins before cancellation or observes the canceled order
// afterward; the reservation can never be double released.
func (s *DBService) AdminCancelOrder(ctx context.Context, req AdminCancelOrderRequest) error {
	if err := s.ensureSchema(ctx); err != nil {
		return err
	}
	if req.OrderID <= 0 || req.AdminID <= 0 {
		return ErrInvalidParameter
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var status int64
	var inventoryID sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT status,inventory_id FROM v2_apple_order WHERE id=$1 FOR UPDATE`, req.OrderID).Scan(&status, &inventoryID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrOrderNotFound
		}
		return err
	}
	if status == OrderCanceled {
		// Repeated cancellation is idempotent. The first request already
		// released the reservation and wrote its audit record.
		return tx.Commit()
	}
	if status != OrderPending {
		return ErrOrderStatus
	}

	now := time.Now().Unix()
	if inventoryID.Valid {
		if _, err := tx.ExecContext(ctx, `UPDATE v2_apple_inventory
SET status=$1,reserved_order_id=NULL,reserved_until=NULL,updated_at=$2
WHERE id=$3 AND status=$4 AND reserved_order_id=$5`, InventoryAvailable, now, inventoryID.Int64, InventoryReserved, req.OrderID); err != nil {
			return fmt.Errorf("release apple id inventory on admin cancellation: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE v2_apple_order SET status=$1,inventory_id=NULL,reserved_until=NULL,updated_at=$2 WHERE id=$3`, OrderCanceled, now, req.OrderID); err != nil {
		return fmt.Errorf("cancel apple id order: %w", err)
	}
	newStatus := OrderCanceled
	if err := insertAdminAudit(ctx, tx, AdminAuditWriteRequest{
		OrderID: req.OrderID,
		AdminID: req.AdminID,
		Action:  "admin_cancel",
		Detail: AdminAuditDetail{
			Reason: cleanAuditReason(req.Reason),
			Status: &newStatus,
		},
	}); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *DBService) AdminWriteAudit(ctx context.Context, req AdminAuditWriteRequest) error {
	if err := s.ensureSchema(ctx); err != nil {
		return err
	}
	if err := validateAdminAudit(req); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var orderID int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM v2_apple_order WHERE id=$1 FOR SHARE`, req.OrderID).Scan(&orderID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrOrderNotFound
		}
		return err
	}
	req.Detail.Reason = cleanAuditReason(req.Detail.Reason)
	if err := insertAdminAudit(ctx, tx, req); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *DBService) AdminListAudits(ctx context.Context, req AdminAuditListRequest) (AdminAuditListResult, error) {
	if err := s.ensureSchema(ctx); err != nil {
		return AdminAuditListResult{}, err
	}
	if req.OrderID <= 0 {
		return AdminAuditListResult{}, ErrInvalidParameter
	}
	current, pageSize := adminPage(req.Current, req.PageSize)
	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_apple_order_audit WHERE order_id=$1`, req.OrderID).Scan(&total); err != nil {
		return AdminAuditListResult{}, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,order_id,actor_user_id,action,detail,created_at
FROM v2_apple_order_audit WHERE order_id=$1 ORDER BY id DESC LIMIT $2 OFFSET $3`, req.OrderID, pageSize, (current-1)*pageSize)
	if err != nil {
		return AdminAuditListResult{}, err
	}
	defer rows.Close()
	data := make([]AdminAuditEntry, 0)
	for rows.Next() {
		var entry AdminAuditEntry
		var actor sql.NullInt64
		var raw string
		if err := rows.Scan(&entry.ID, &entry.OrderID, &actor, &entry.Action, &raw, &entry.CreatedAt); err != nil {
			return AdminAuditListResult{}, err
		}
		if actor.Valid {
			entry.AdminID = int64Pointer(actor.Int64)
		}
		if strings.TrimSpace(raw) != "" {
			if err := json.Unmarshal([]byte(raw), &entry.Detail); err != nil {
				// Legacy/system audit entries use plain text. Preserve the text as a
				// reason without trying to interpret it as credential data.
				entry.Detail.Reason = cleanAuditReason(raw)
			}
		}
		data = append(data, entry)
	}
	if err := rows.Err(); err != nil {
		return AdminAuditListResult{}, err
	}
	return AdminAuditListResult{Data: data, Total: total}, nil
}

type adminScanner interface {
	Scan(dest ...any) error
}

func (s *DBService) scanAdminInventory(scanner adminScanner) (AdminInventory, error) {
	var item AdminInventory
	var accountCiphertext string
	var reservedOrder, reservedUntil, soldOrder sql.NullInt64
	if err := scanner.Scan(&item.ID, &item.ProductID, &item.ProductName, &accountCiphertext, &item.Status,
		&reservedOrder, &reservedUntil, &soldOrder, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return AdminInventory{}, err
	}
	account, err := s.decrypt(accountCiphertext)
	if err != nil {
		return AdminInventory{}, err
	}
	item.Account = account
	item.ReservedOrderID = nullInt64Pointer(reservedOrder)
	item.ReservedUntil = nullInt64Pointer(reservedUntil)
	item.SoldOrderID = nullInt64Pointer(soldOrder)
	return item, nil
}

func (s *DBService) scanAdminOrder(scanner adminScanner) (AdminOrder, error) {
	var item AdminOrder
	var paymentID, inventoryID, reservedUntil, paidAt sql.NullInt64
	var callbackNo, accountCiphertext sql.NullString
	if err := scanner.Scan(&item.ID, &item.UserID, &item.UserEmail, &item.TradeNo, &item.ProductID, &item.ProductName,
		&item.Region, &item.Price, &item.HandlingAmount, &item.Status, &paymentID, &callbackNo, &inventoryID,
		&accountCiphertext, &reservedUntil, &paidAt, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return AdminOrder{}, err
	}
	item.PaymentID = nullInt64Pointer(paymentID)
	item.BusinessType = BusinessType
	item.TotalAmount = item.Price + item.HandlingAmount
	item.InventoryID = nullInt64Pointer(inventoryID)
	item.ReservationExpiry = nullInt64Pointer(reservedUntil)
	item.PaidAt = nullInt64Pointer(paidAt)
	if callbackNo.Valid {
		item.CallbackNo = stringPointer(callbackNo.String)
	}
	if accountCiphertext.Valid && accountCiphertext.String != "" {
		account, err := s.decrypt(accountCiphertext.String)
		if err != nil {
			return AdminOrder{}, err
		}
		item.Account = account
	}
	return item, nil
}

func insertAdminAudit(ctx context.Context, tx *sql.Tx, req AdminAuditWriteRequest) error {
	if err := validateAdminAudit(req); err != nil {
		return err
	}
	detail := req.Detail
	detail.Reason = cleanAuditReason(detail.Reason)
	if detail.Account != "" {
		detail.Account = maskAppleAccount(detail.Account)
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO v2_apple_order_audit(order_id,actor_user_id,action,detail,created_at)
VALUES($1,$2,$3,$4,$5)`, req.OrderID, req.AdminID, strings.TrimSpace(req.Action), string(raw), time.Now().Unix())
	if err != nil {
		return fmt.Errorf("write apple admin audit: %w", err)
	}
	return nil
}

func validateAdminAudit(req AdminAuditWriteRequest) error {
	action := strings.TrimSpace(req.Action)
	if req.OrderID <= 0 || req.AdminID <= 0 || action == "" || len(action) > 64 {
		return ErrAdminAuditRequired
	}
	for _, r := range action {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_') {
			return ErrAdminAuditRequired
		}
	}
	return nil
}

func adminInventoryWhere(req AdminInventoryListRequest) (string, []any) {
	clauses := make([]string, 0, 2)
	args := make([]any, 0, 2)
	if req.ProductID != nil {
		args = append(args, *req.ProductID)
		clauses = append(clauses, fmt.Sprintf("i.product_id=$%d", len(args)))
	}
	if req.Status != nil {
		args = append(args, *req.Status)
		clauses = append(clauses, fmt.Sprintf("i.status=$%d", len(args)))
	}
	if len(clauses) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

func adminOrderWhere(req AdminOrderListRequest) (string, []any) {
	clauses := make([]string, 0, 4)
	args := make([]any, 0, 4)
	if req.Status != nil {
		args = append(args, *req.Status)
		clauses = append(clauses, fmt.Sprintf("o.status=$%d", len(args)))
	}
	if req.ProductID != nil {
		args = append(args, *req.ProductID)
		clauses = append(clauses, fmt.Sprintf("o.product_id=$%d", len(args)))
	}
	if email := strings.TrimSpace(req.UserEmail); email != "" {
		args = append(args, "%"+strings.ToLower(email)+"%")
		clauses = append(clauses, fmt.Sprintf("LOWER(u.email) LIKE $%d", len(args)))
	}
	if tradeNo := strings.TrimSpace(req.TradeNo); tradeNo != "" {
		args = append(args, "%"+tradeNo+"%")
		clauses = append(clauses, fmt.Sprintf("o.trade_no LIKE $%d", len(args)))
	}
	if len(clauses) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

func adminPage(current, pageSize int64) (int64, int64) {
	if current <= 0 {
		current = 1
	}
	if pageSize <= 0 {
		pageSize = adminDefaultPageSize
	}
	if pageSize > adminMaxPageSize {
		pageSize = adminMaxPageSize
	}
	return current, pageSize
}

func maskAppleAccount(account string) string {
	account = strings.TrimSpace(account)
	if account == "" {
		return ""
	}
	if strings.Contains(account, "*") {
		return account
	}
	// New inventory records are complete one-line credential records and may
	// contain passwords or security answers. Only expose the familiar masked
	// email form for a plain account-looking value; never leak the tail of an
	// opaque record as a domain.
	if strings.Contains(account, "----") {
		return maskOpaqueAppleCredential(account)
	}
	for _, r := range account {
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("._%+-@", r)) {
			return maskOpaqueAppleCredential(account)
		}
	}
	if at := strings.LastIndex(account, "@"); at > 0 && at < len(account)-1 {
		local := []rune(account[:at])
		domain := account[at+1:]
		switch len(local) {
		case 1:
			return string(local[0]) + "***@" + domain
		case 2:
			return string(local[0]) + "***" + string(local[1]) + "@" + domain
		default:
			return string(local[0]) + "***" + string(local[len(local)-1]) + "@" + domain
		}
	}
	runes := []rune(account)
	if len(runes) <= 2 {
		return strings.Repeat("*", len(runes))
	}
	if len(runes) <= 4 {
		return string(runes[0]) + "***" + string(runes[len(runes)-1])
	}
	return string(runes[:2]) + "***" + string(runes[len(runes)-2:])
}

func maskOpaqueAppleCredential(value string) string {
	runes := []rune(value)
	if len(runes) <= 2 {
		return strings.Repeat("*", len(runes))
	}
	return string(runes[:2]) + "***"
}

func cleanAuditReason(reason string) string {
	reason = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(reason, "\x00", ""), "\r", " "))
	reason = strings.ReplaceAll(reason, "\n", " ")
	runes := []rune(reason)
	if len(runes) > 500 {
		reason = string(runes[:500])
	}
	return reason
}

func boolInt(value bool) int64 {
	if value {
		return 1
	}
	return 0
}

func nullInt64Pointer(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	return int64Pointer(value.Int64)
}

func int64Pointer(value int64) *int64    { return &value }
func stringPointer(value string) *string { return &value }
