package appleid

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"

	"forest/go-api/internal/config"

	"github.com/DATA-DOG/go-sqlmock"
)

func newAdminTestService(t *testing.T) (*DBService, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	service := NewDBService(config.Config{AppKey: "admin-test-app-key"}, db)
	service.ensured = true
	return service, mock
}

func TestMaskAppleAccount(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"alice@example.com": "a***e@example.com",
		"ab@example.com":    "a***b@example.com",
		"a@example.com":     "a***@example.com",
		"abcdef":            "ab***ef",
		"ab":                "**",
		"":                  "",
	}
	for input, expected := range tests {
		if actual := maskAppleAccount(input); actual != expected {
			t.Errorf("maskAppleAccount(%q) = %q, want %q", input, actual, expected)
		}
	}
}

func TestAdminListInventoryReturnsOriginalAccounts(t *testing.T) {
	service, mock := newAdminTestService(t)
	accounts := []string{"alice@example.com", "  alice@example.com----Password123!----密保问题：城市？答案：上海  "}
	rows := sqlmock.NewRows([]string{
		"id", "product_id", "name", "account_ciphertext", "status", "reserved_order_id", "reserved_until", "sold_order_id", "created_at", "updated_at",
	})
	for i, account := range accounts {
		ciphertext, err := service.encrypt(account)
		if err != nil {
			t.Fatal(err)
		}
		rows.AddRow(int64(i+7), int64(3), "US Shadowrocket", ciphertext, InventoryAvailable, nil, nil, nil, int64(100), int64(101))
	}

	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM v2_apple_inventory i`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(len(accounts))))
	mock.ExpectQuery(`SELECT i.id,i.product_id,p.name,i.account_ciphertext`).
		WithArgs(int64(20), int64(0)).
		WillReturnRows(rows)

	result, err := service.AdminListInventory(context.Background(), AdminInventoryListRequest{})
	if err != nil {
		t.Fatalf("AdminListInventory: %v", err)
	}
	if result.Total != int64(len(accounts)) || len(result.Data) != len(accounts) {
		t.Fatalf("unexpected result: %+v", result)
	}
	for i, account := range accounts {
		if result.Data[i].Account != account {
			t.Errorf("account = %q, want original %q", result.Data[i].Account, account)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAdminDisableInventoryRejectsSoldRow(t *testing.T) {
	service, mock := newAdminTestService(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status FROM v2_apple_inventory WHERE id=\$1 FOR UPDATE`).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow(InventorySold))
	mock.ExpectRollback()

	err := service.AdminDisableInventory(context.Background(), AdminDisableInventoryRequest{InventoryID: 9, AdminID: 77})
	if !errors.Is(err, ErrInventorySold) {
		t.Fatalf("error = %v, want ErrInventorySold", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAdminGetOrderCredentialsDoesNotWriteAudit(t *testing.T) {
	for _, tt := range []struct {
		name       string
		account    string
		password   string
		credential string
	}{
		{name: "separate fields", account: "alice@example.com", password: "TopSecret-password-123"},
		{name: "one-line record", account: " alice@example.com 密码：Secret 密保：上海 ", credential: " alice@example.com 密码：Secret 密保：上海 "},
	} {
		t.Run(tt.name, func(t *testing.T) {
			service, mock := newAdminTestService(t)
			accountCiphertext, err := service.encrypt(tt.account)
			if err != nil {
				t.Fatal(err)
			}
			passwordCiphertext, err := service.encrypt(tt.password)
			if err != nil {
				t.Fatal(err)
			}

			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT o.id,o.trade_no,o.product_id,o.inventory_id,o.status,i.status,i.account_ciphertext,i.password_ciphertext[\s\S]*FOR SHARE OF o,i`).
				WithArgs(int64(11)).
				WillReturnRows(sqlmock.NewRows([]string{
					"id", "trade_no", "product_id", "inventory_id", "order_status", "inventory_status", "account_ciphertext", "password_ciphertext",
				}).AddRow(int64(11), "apple-trade", int64(3), int64(8), OrderPaid, InventorySold, accountCiphertext, passwordCiphertext))
			// Any audit INSERT makes this strict transaction expectation fail.
			mock.ExpectCommit()

			detail, err := service.AdminGetOrderCredentials(context.Background(), 11, 77)
			if err != nil {
				t.Fatalf("AdminGetOrderCredentials: %v", err)
			}
			if detail.Account != tt.account || detail.Password != tt.password || detail.Credential != tt.credential {
				t.Fatalf("unexpected credential detail: %+v", detail)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAdminGetOrderCredentialsStillRequiresAdminActor(t *testing.T) {
	service, mock := newAdminTestService(t)
	if _, err := service.AdminGetOrderCredentials(context.Background(), 11, 0); !errors.Is(err, ErrAdminAuditRequired) {
		t.Fatalf("error = %v, want ErrAdminAuditRequired", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAdminReplaceInventoryLocksAndAudits(t *testing.T) {
	service, mock := newAdminTestService(t)
	oldCiphertext, err := service.encrypt("old-account@example.com")
	if err != nil {
		t.Fatal(err)
	}
	newCiphertext, err := service.encrypt("new-account@example.com")
	if err != nil {
		t.Fatal(err)
	}

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT product_id,inventory_id,status FROM v2_apple_order WHERE id=\$1 FOR UPDATE`).
		WithArgs(int64(22)).
		WillReturnRows(sqlmock.NewRows([]string{"product_id", "inventory_id", "status"}).AddRow(int64(3), int64(8), OrderPaid))
	mock.ExpectQuery(`SELECT status,sold_order_id,account_ciphertext FROM v2_apple_inventory WHERE id=\$1 FOR UPDATE`).
		WithArgs(int64(8)).
		WillReturnRows(sqlmock.NewRows([]string{"status", "sold_order_id", "account_ciphertext"}).AddRow(InventorySold, int64(22), oldCiphertext))
	mock.ExpectQuery(`SELECT id,status,account_ciphertext FROM v2_apple_inventory WHERE product_id=\$1 AND status=0`).
		WithArgs(int64(3)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "status", "account_ciphertext"}).AddRow(int64(9), InventoryAvailable, newCiphertext))
	mock.ExpectExec(`UPDATE v2_apple_inventory SET status=3`).
		WithArgs(int64(8), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE v2_apple_inventory SET status=2`).
		WithArgs(int64(9), int64(22), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE v2_apple_order SET inventory_id=\$2`).
		WithArgs(int64(22), int64(9), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO v2_apple_order_audit`).
		WithArgs(int64(22), int64(77), "inventory_replace", safeAuditJSONMatcher{
			forbidden: []string{"old-account@example.com", "new-account@example.com"},
			required:  []string{`"previous_inventory_id":8`, `"new_inventory_id":9`, `"account":"n***t@example.com"`},
		}, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	result, err := service.AdminReplaceInventory(context.Background(), AdminReplaceInventoryRequest{OrderID: 22, AdminID: 77, Reason: "customer reported login failure"})
	if err != nil {
		t.Fatalf("AdminReplaceInventory: %v", err)
	}
	if result.PreviousInventoryID != 8 || result.NewInventoryID != 9 || result.PreviousAccount != "old-account@example.com" || result.NewAccount != "new-account@example.com" {
		t.Fatalf("unexpected replacement: %+v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAdminMarkOrderRefundedIsTransactionalAndAudited(t *testing.T) {
	service, mock := newAdminTestService(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status FROM v2_apple_order WHERE id=\$1 FOR UPDATE`).
		WithArgs(int64(22)).
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow(OrderPaid))
	mock.ExpectExec(`UPDATE v2_apple_order SET status=3`).
		WithArgs(int64(22), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO v2_apple_order_audit`).
		WithArgs(int64(22), int64(77), "refund_confirmed", safeAuditJSONMatcher{required: []string{`"reason":"gateway refund rf-1"`, `"status":3`}}, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	err := service.AdminMarkOrderRefunded(context.Background(), AdminMarkRefundedRequest{OrderID: 22, AdminID: 77, Reason: "gateway refund rf-1"})
	if err != nil {
		t.Fatalf("AdminMarkOrderRefunded: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAdminCancelOrderReleasesReservationAndAudits(t *testing.T) {
	service, mock := newAdminTestService(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status,inventory_id FROM v2_apple_order WHERE id=\$1 FOR UPDATE`).
		WithArgs(int64(22)).
		WillReturnRows(sqlmock.NewRows([]string{"status", "inventory_id"}).AddRow(OrderPending, int64(8)))
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE v2_apple_inventory SET status=$1,reserved_order_id=NULL,reserved_until=NULL,updated_at=$2 WHERE id=$3 AND status=$4 AND reserved_order_id=$5`)).
		WithArgs(InventoryAvailable, sqlmock.AnyArg(), int64(8), InventoryReserved, int64(22)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE v2_apple_order SET status=\$1,inventory_id=NULL,reserved_until=NULL,updated_at=\$2 WHERE id=\$3`).
		WithArgs(OrderCanceled, sqlmock.AnyArg(), int64(22)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO v2_apple_order_audit`).
		WithArgs(int64(22), int64(77), "admin_cancel", safeAuditJSONMatcher{required: []string{`"reason":"customer request"`, `"status":2`}}, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	err := service.AdminCancelOrder(context.Background(), AdminCancelOrderRequest{OrderID: 22, AdminID: 77, Reason: "customer request"})
	if err != nil {
		t.Fatalf("AdminCancelOrder: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAdminCancelOrderRejectsSettledOrders(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status int64
	}{
		{name: "paid", status: OrderPaid},
		{name: "paid awaiting manual handling", status: OrderManual},
		{name: "refunded", status: OrderRefunded},
	} {
		t.Run(tt.name, func(t *testing.T) {
			service, mock := newAdminTestService(t)
			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT status,inventory_id FROM v2_apple_order WHERE id=\$1 FOR UPDATE`).
				WithArgs(int64(22)).
				WillReturnRows(sqlmock.NewRows([]string{"status", "inventory_id"}).AddRow(tt.status, nil))
			mock.ExpectRollback()

			err := service.AdminCancelOrder(context.Background(), AdminCancelOrderRequest{OrderID: 22, AdminID: 77})
			if !errors.Is(err, ErrOrderStatus) {
				t.Fatalf("error = %v, want ErrOrderStatus", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAdminCancelOrderCanceledIsIdempotentWithoutInventory(t *testing.T) {
	service, mock := newAdminTestService(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status,inventory_id FROM v2_apple_order WHERE id=\$1 FOR UPDATE`).
		WithArgs(int64(22)).
		WillReturnRows(sqlmock.NewRows([]string{"status", "inventory_id"}).AddRow(OrderCanceled, nil))
	mock.ExpectCommit()
	if err := service.AdminCancelOrder(context.Background(), AdminCancelOrderRequest{OrderID: 22, AdminID: 77}); err != nil {
		t.Fatalf("repeat cancellation: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAdminCancelOrderHandlesMissingAndReassignedInventory(t *testing.T) {
	for _, tt := range []struct {
		name        string
		inventoryID any
	}{
		{name: "missing inventory", inventoryID: nil},
		{name: "inventory already reassigned", inventoryID: int64(8)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			service, mock := newAdminTestService(t)
			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT status,inventory_id FROM v2_apple_order WHERE id=\$1 FOR UPDATE`).
				WithArgs(int64(22)).
				WillReturnRows(sqlmock.NewRows([]string{"status", "inventory_id"}).AddRow(OrderPending, tt.inventoryID))
			if tt.inventoryID != nil {
				mock.ExpectExec(regexp.QuoteMeta(`UPDATE v2_apple_inventory SET status=$1,reserved_order_id=NULL,reserved_until=NULL,updated_at=$2 WHERE id=$3 AND status=$4 AND reserved_order_id=$5`)).
					WithArgs(InventoryAvailable, sqlmock.AnyArg(), int64(8), InventoryReserved, int64(22)).
					WillReturnResult(sqlmock.NewResult(0, 0))
			}
			mock.ExpectExec(regexp.QuoteMeta(`UPDATE v2_apple_order SET status=$1,inventory_id=NULL,reserved_until=NULL,updated_at=$2 WHERE id=$3`)).
				WithArgs(OrderCanceled, sqlmock.AnyArg(), int64(22)).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec(`INSERT INTO v2_apple_order_audit`).
				WithArgs(int64(22), int64(77), "admin_cancel", safeAuditJSONMatcher{required: []string{`"status":2`}}, sqlmock.AnyArg()).
				WillReturnResult(sqlmock.NewResult(1, 1))
			mock.ExpectCommit()
			if err := service.AdminCancelOrder(context.Background(), AdminCancelOrderRequest{OrderID: 22, AdminID: 77}); err != nil {
				t.Fatalf("AdminCancelOrder: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAdminCancelOrderAuditFailureRollsBackReservationRelease(t *testing.T) {
	service, mock := newAdminTestService(t)
	auditErr := errors.New("audit write failed")
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status,inventory_id FROM v2_apple_order WHERE id=\$1 FOR UPDATE`).
		WithArgs(int64(22)).
		WillReturnRows(sqlmock.NewRows([]string{"status", "inventory_id"}).AddRow(OrderPending, int64(8)))
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE v2_apple_inventory SET status=$1,reserved_order_id=NULL,reserved_until=NULL,updated_at=$2 WHERE id=$3 AND status=$4 AND reserved_order_id=$5`)).
		WithArgs(InventoryAvailable, sqlmock.AnyArg(), int64(8), InventoryReserved, int64(22)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE v2_apple_order SET status=$1,inventory_id=NULL,reserved_until=NULL,updated_at=$2 WHERE id=$3`)).
		WithArgs(OrderCanceled, sqlmock.AnyArg(), int64(22)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO v2_apple_order_audit`).
		WithArgs(int64(22), int64(77), "admin_cancel", safeAuditJSONMatcher{required: []string{`"status":2`}}, sqlmock.AnyArg()).WillReturnError(auditErr)
	mock.ExpectRollback()
	if err := service.AdminCancelOrder(context.Background(), AdminCancelOrderRequest{OrderID: 22, AdminID: 77}); !errors.Is(err, auditErr) {
		t.Fatalf("error = %v, want audit failure", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAdminAddInventoryBatchRejectsDuplicateInputBeforeTransaction(t *testing.T) {
	service, mock := newAdminTestService(t)
	_, err := service.AdminAddInventoryBatch(context.Background(), AdminAddInventoryRequest{
		ProductID: 3,
		Items: []AdminInventoryCredential{
			{Account: "Alice@example.com", Password: "one"},
			{Account: " alice@example.com ", Password: "two"},
		},
	})
	if !errors.Is(err, ErrDuplicateInventory) {
		t.Fatalf("error = %v, want ErrDuplicateInventory", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAdminListOrdersRetainsOrdersForDeletedUsers(t *testing.T) {
	service, mock := newAdminTestService(t)
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM v2_apple_order o LEFT JOIN v2_user u ON u.id=o.user_id`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(1)))
	mock.ExpectQuery(`SELECT o.id,o.user_id,COALESCE\(u.email,''\),o.trade_no,o.product_id`).
		WithArgs(int64(20), int64(0)).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "user_id", "email", "trade_no", "product_id", "product_name", "region", "price", "handling_amount", "status", "payment_id", "callback_no", "inventory_id", "account_ciphertext", "reserved_until", "paid_at", "created_at", "updated_at",
		}).AddRow(int64(31), int64(404), "", "apple-deleted-user", int64(3), "US ID", "US", int64(1200), int64(0), OrderRefunded, nil, nil, nil, nil, nil, int64(200), int64(100), int64(201)))

	result, err := service.AdminListOrders(context.Background(), AdminOrderListRequest{})
	if err != nil {
		t.Fatalf("AdminListOrders: %v", err)
	}
	if result.Total != 1 || len(result.Data) != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result.Data[0].UserID != 404 || result.Data[0].UserEmail != "" || result.Data[0].BusinessType != BusinessType {
		t.Fatalf("deleted-user order was not retained correctly: %+v", result.Data[0])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAdminOrderReadsReturnOriginalAccounts(t *testing.T) {
	for _, account := range []string{"alice@example.com", " alice@example.com----Secret----密保：上海 "} {
		for _, endpoint := range []string{"list", "detail"} {
			t.Run(endpoint+"/"+account, func(t *testing.T) {
				service, mock := newAdminTestService(t)
				ciphertext, err := service.encrypt(account)
				if err != nil {
					t.Fatal(err)
				}
				rows := sqlmock.NewRows([]string{
					"id", "user_id", "email", "trade_no", "product_id", "product_name", "region", "price", "handling_amount", "status", "payment_id", "callback_no", "inventory_id", "account_ciphertext", "reserved_until", "paid_at", "created_at", "updated_at",
				}).AddRow(int64(31), int64(5), "buyer@example.com", "apple-trade", int64(3), "US ID", "US", int64(1200), int64(0), OrderPaid, nil, nil, int64(8), ciphertext, nil, int64(200), int64(100), int64(201))
				var result AdminOrder
				if endpoint == "list" {
					mock.ExpectQuery(`SELECT COUNT\(\*\) FROM v2_apple_order o`).
						WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(1)))
					mock.ExpectQuery(`SELECT o.id,o.user_id,COALESCE\(u.email,''\),o.trade_no,o.product_id`).
						WithArgs(int64(20), int64(0)).WillReturnRows(rows)
					list, err := service.AdminListOrders(context.Background(), AdminOrderListRequest{})
					if err != nil {
						t.Fatal(err)
					}
					if len(list.Data) != 1 {
						t.Fatalf("unexpected order list: %+v", list)
					}
					result = list.Data[0]
				} else {
					mock.ExpectQuery(`SELECT o.id,o.user_id,COALESCE\(u.email,''\),o.trade_no,o.product_id`).
						WithArgs(int64(31)).WillReturnRows(rows)
					mock.ExpectQuery(`SELECT COUNT\(\*\) FROM v2_apple_order_audit`).
						WithArgs(int64(31)).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(0)))
					mock.ExpectQuery(`SELECT id,order_id,actor_user_id,action,detail,created_at`).
						WithArgs(int64(31), adminMaxPageSize, int64(0)).
						WillReturnRows(sqlmock.NewRows([]string{"id", "order_id", "actor_user_id", "action", "detail", "created_at"}))
					detail, err := service.AdminGetOrderDetail(context.Background(), 31)
					if err != nil {
						t.Fatal(err)
					}
					result = detail.Order
				}
				if result.Account != account {
					t.Errorf("account = %q, want original %q", result.Account, account)
				}
				if err := mock.ExpectationsWereMet(); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

type safeAuditJSONMatcher struct {
	forbidden []string
	required  []string
}

func (m safeAuditJSONMatcher) Match(value driver.Value) bool {
	text, ok := value.(string)
	if !ok || !json.Valid([]byte(text)) {
		return false
	}
	for _, forbidden := range m.forbidden {
		if forbidden != "" && strings.Contains(text, forbidden) {
			return false
		}
	}
	for _, required := range m.required {
		if !strings.Contains(text, required) {
			return false
		}
	}
	return true
}
