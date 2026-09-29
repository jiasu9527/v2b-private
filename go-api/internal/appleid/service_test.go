package appleid

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"forest/go-api/internal/config"
	"github.com/DATA-DOG/go-sqlmock"
)

func expectReservationSweep(mock sqlmock.Sqlmock) {
	mock.ExpectExec(`WITH expired AS`).WithArgs(OrderCanceled, sqlmock.AnyArg(), OrderPending).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE v2_apple_inventory`).WithArgs(InventoryAvailable, sqlmock.AnyArg(), InventoryReserved).WillReturnResult(sqlmock.NewResult(0, 1))
}

func orderRows(productID, status int64) *sqlmock.Rows {
	return sqlmock.NewRows(strings.Split(orderSelectColumns, ",")).AddRow(
		int64(41), "apple-test", productID, "US account", "US", int64(1), "7 days", int64(1000), int64(0), status, nil, int64(1000), int64(100), nil, int64(100),
	)
}

func expectCreateProduct(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(`SELECT id,name,region,owned_shadowrocket,price,after_sales,enabled FROM v2_apple_product`).WithArgs(int64(3)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "region", "owned_shadowrocket", "price", "after_sales", "enabled"}).AddRow(int64(3), "US account", "US", int64(1), int64(1000), "7 days", int64(1)))
}

func expectCreateInsert(mock sqlmock.Sqlmock, key string) *sqlmock.ExpectedQuery {
	return mock.ExpectQuery(`INSERT INTO v2_apple_order\(`).WithArgs(
		int64(7), int64(3), "US account", "US", int64(1), "7 days", int64(1000), sqlmock.AnyArg(), key, OrderPending, sqlmock.AnyArg(), sqlmock.AnyArg(),
	)
}

func TestCreateOrderReservesOneInventoryAndCommits(t *testing.T) {
	s, mock := newAdminTestService(t)
	mock.ExpectBegin()
	expectReservationSweep(mock)
	expectCreateProduct(mock)
	expectCreateInsert(mock, "").WillReturnRows(orderRows(3, OrderPending))
	mock.ExpectQuery(`UPDATE v2_apple_inventory .*FOR UPDATE SKIP LOCKED LIMIT 1\) RETURNING id`).
		WithArgs(InventoryReserved, int64(41), sqlmock.AnyArg(), sqlmock.AnyArg(), int64(3), InventoryAvailable).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(12)))
	mock.ExpectExec(`UPDATE v2_apple_order SET inventory_id=\$2 WHERE id=\$1`).WithArgs(int64(41), int64(12)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO v2_apple_order_audit`).WithArgs(int64(41), int64(7), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	order, err := s.CreateOrder(context.Background(), 7, CreateOrderRequest{ProductID: 3})
	if err != nil {
		t.Fatal(err)
	}
	if order.inventoryID != 12 || order.Price != 1000 || order.BusinessType != BusinessType {
		t.Fatalf("unexpected order: %+v", order)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCreateOrderOutOfStockRollsBackOrder(t *testing.T) {
	s, mock := newAdminTestService(t)
	mock.ExpectBegin()
	expectReservationSweep(mock)
	expectCreateProduct(mock)
	expectCreateInsert(mock, "").WillReturnRows(orderRows(3, OrderPending))
	mock.ExpectQuery(`UPDATE v2_apple_inventory .*FOR UPDATE SKIP LOCKED LIMIT 1\) RETURNING id`).WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()
	_, err := s.CreateOrder(context.Background(), 7, CreateOrderRequest{ProductID: 3})
	if !errors.Is(err, ErrOutOfStock) {
		t.Fatalf("expected out of stock, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCreateOrderIdempotentRetryCommitsExpiration(t *testing.T) {
	s, mock := newAdminTestService(t)
	mock.ExpectBegin()
	expectReservationSweep(mock)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT `+orderSelectColumns+` FROM v2_apple_order WHERE user_id=$1 AND idempotency_key=$2`)).
		WithArgs(int64(7), "retry-key").WillReturnRows(orderRows(3, OrderCanceled))
	mock.ExpectCommit()
	order, err := s.CreateOrder(context.Background(), 7, CreateOrderRequest{ProductID: 3, IdempotencyKey: "retry-key"})
	if err != nil {
		t.Fatal(err)
	}
	if order.Status != OrderCanceled || order.ID != 41 {
		t.Fatalf("unexpected existing order: %+v", order)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCreateOrderIdempotencyRejectsDifferentProduct(t *testing.T) {
	s, mock := newAdminTestService(t)
	mock.ExpectBegin()
	expectReservationSweep(mock)
	mock.ExpectQuery(`SELECT .* FROM v2_apple_order WHERE user_id=\$1 AND idempotency_key=\$2`).WithArgs(int64(7), "retry-key").WillReturnRows(orderRows(8, OrderPending))
	mock.ExpectRollback()
	_, err := s.CreateOrder(context.Background(), 7, CreateOrderRequest{ProductID: 3, IdempotencyKey: "retry-key"})
	if !errors.Is(err, ErrInvalidParameter) {
		t.Fatalf("expected conflict, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCreateOrderConcurrentIdempotencyConflictReturnsOriginal(t *testing.T) {
	s, mock := newAdminTestService(t)
	mock.ExpectBegin()
	expectReservationSweep(mock)
	mock.ExpectQuery(`SELECT .* FROM v2_apple_order WHERE user_id=\$1 AND idempotency_key=\$2`).WithArgs(int64(7), "retry-key").WillReturnError(sql.ErrNoRows)
	expectCreateProduct(mock)
	expectCreateInsert(mock, "retry-key").WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`SELECT .* FROM v2_apple_order WHERE user_id=\$1 AND idempotency_key=\$2`).WithArgs(int64(7), "retry-key").WillReturnRows(orderRows(3, OrderPending))
	mock.ExpectCommit()
	order, err := s.CreateOrder(context.Background(), 7, CreateOrderRequest{ProductID: 3, IdempotencyKey: "retry-key"})
	if err != nil {
		t.Fatal(err)
	}
	if order.ID != 41 {
		t.Fatalf("unexpected existing order: %+v", order)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func expectPaidOrderLock(mock sqlmock.Sqlmock, status, until int64) {
	mock.ExpectQuery(`SELECT id,product_id,inventory_id,status,reserved_until,payment_id,price,handling_amount FROM v2_apple_order WHERE trade_no=\$1 FOR UPDATE`).WithArgs("apple-test").
		WillReturnRows(sqlmock.NewRows([]string{"id", "product_id", "inventory_id", "status", "reserved_until", "payment_id", "price", "handling_amount"}).AddRow(int64(41), int64(3), int64(12), status, until, int64(9), int64(1000), int64(50)))
}

func TestMarkExternalPaidConsumesReservationOnce(t *testing.T) {
	s, mock := newAdminTestService(t)
	until := time.Now().Add(time.Hour).Unix()
	mock.ExpectBegin()
	expectPaidOrderLock(mock, OrderPending, until)
	mock.ExpectQuery(`SELECT status,reserved_order_id,reserved_until FROM v2_apple_inventory WHERE id=\$1 FOR UPDATE`).WithArgs(int64(12)).
		WillReturnRows(sqlmock.NewRows([]string{"status", "reserved_order_id", "reserved_until"}).AddRow(InventoryReserved, int64(41), until))
	mock.ExpectExec(`UPDATE v2_apple_inventory SET status=\$1,sold_order_id=\$2`).WithArgs(InventorySold, int64(41), sqlmock.AnyArg(), int64(12)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE v2_apple_order SET status=\$1,inventory_id=\$2`).WithArgs(OrderPaid, int64(12), "gw-1", sqlmock.AnyArg(), int64(41)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO v2_apple_order_audit`).WithArgs(int64(41), "paid", "gw-1", sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := s.MarkExternalPaid(context.Background(), "apple-test", "gw-1", false, nil, nil); err != nil {
		t.Fatal(err)
	}
	mock.ExpectBegin()
	expectPaidOrderLock(mock, OrderPaid, until)
	mock.ExpectRollback()
	if err := s.MarkExternalPaid(context.Background(), "apple-test", "gw-1", false, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMarkExternalPaidLateOutOfStockNeedsManualHandling(t *testing.T) {
	s, mock := newAdminTestService(t)
	mock.ExpectBegin()
	expectPaidOrderLock(mock, OrderCanceled, time.Now().Add(-time.Hour).Unix())
	mock.ExpectQuery(`UPDATE v2_apple_inventory .*FOR UPDATE SKIP LOCKED LIMIT 1\) RETURNING id`).
		WithArgs(InventorySold, int64(41), sqlmock.AnyArg(), int64(3), InventoryAvailable).WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(`UPDATE v2_apple_order SET status=\$1,inventory_id=NULL,callback_no=\$2`).WithArgs(OrderManual, "gw-1", sqlmock.AnyArg(), int64(41)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO v2_apple_order_audit`).WithArgs(int64(41), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := s.MarkExternalPaid(context.Background(), "apple-test", "gw-1", false, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMarkExternalPaidReleasesExpiredReservationBeforeAllocation(t *testing.T) {
	s, mock := newAdminTestService(t)
	until := time.Now().Add(-time.Hour).Unix()
	mock.ExpectBegin()
	expectPaidOrderLock(mock, OrderPending, until)
	mock.ExpectQuery(`SELECT status,reserved_order_id,reserved_until FROM v2_apple_inventory WHERE id=\$1 FOR UPDATE`).WithArgs(int64(12)).
		WillReturnRows(sqlmock.NewRows([]string{"status", "reserved_order_id", "reserved_until"}).AddRow(InventoryReserved, int64(41), until))
	mock.ExpectExec(`UPDATE v2_apple_inventory SET status=\$1,reserved_order_id=NULL`).WithArgs(InventoryAvailable, sqlmock.AnyArg(), int64(12), InventoryReserved, int64(41)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`UPDATE v2_apple_inventory .*FOR UPDATE SKIP LOCKED LIMIT 1\) RETURNING id`).
		WithArgs(InventorySold, int64(41), sqlmock.AnyArg(), int64(3), InventoryAvailable).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(15)))
	mock.ExpectExec(`UPDATE v2_apple_order SET status=\$1,inventory_id=\$2`).WithArgs(OrderPaid, int64(15), "gw-1", sqlmock.AnyArg(), int64(41)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO v2_apple_order_audit`).WithArgs(int64(41), "paid", "gw-1", sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := s.MarkExternalPaid(context.Background(), "apple-test", "gw-1", false, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMarkExternalPaidDoesNotTouchReassignedInventory(t *testing.T) {
	s, mock := newAdminTestService(t)
	until := time.Now().Add(time.Hour).Unix()
	mock.ExpectBegin()
	expectPaidOrderLock(mock, OrderPending, until)
	mock.ExpectQuery(`SELECT status,reserved_order_id,reserved_until FROM v2_apple_inventory WHERE id=\$1 FOR UPDATE`).WithArgs(int64(12)).
		WillReturnRows(sqlmock.NewRows([]string{"status", "reserved_order_id", "reserved_until"}).AddRow(InventoryReserved, int64(99), until))
	mock.ExpectQuery(`UPDATE v2_apple_inventory .*FOR UPDATE SKIP LOCKED LIMIT 1\) RETURNING id`).
		WithArgs(InventorySold, int64(41), sqlmock.AnyArg(), int64(3), InventoryAvailable).WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(`UPDATE v2_apple_order SET status=\$1,inventory_id=NULL,callback_no=\$2`).WithArgs(OrderManual, "gw-1", sqlmock.AnyArg(), int64(41)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO v2_apple_order_audit`).WithArgs(int64(41), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := s.MarkExternalPaid(context.Background(), "apple-test", "gw-1", false, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDeliveryEnforcesOwnershipAndPaidInventory(t *testing.T) {
	tests := []struct {
		name      string
		missing   bool
		inventory any
		status    int64
		want      error
	}{
		{name: "other owner", missing: true, want: ErrOrderNotFound},
		{name: "pending", inventory: int64(12), status: OrderPending, want: ErrDeliveryNotReady},
		{name: "manual without inventory", status: OrderManual, want: ErrDeliveryNotReady},
		{name: "refunded", inventory: int64(12), status: OrderRefunded, want: ErrDeliveryNotReady},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, mock := newAdminTestService(t)
			mock.ExpectBegin()
			query := mock.ExpectQuery(`SELECT id,trade_no,product_id,inventory_id,status FROM v2_apple_order WHERE user_id=\$1 AND trade_no=\$2 FOR SHARE`).WithArgs(int64(7), "apple-test")
			if tt.missing {
				query.WillReturnError(sql.ErrNoRows)
			} else {
				query.WillReturnRows(sqlmock.NewRows([]string{"id", "trade_no", "product_id", "inventory_id", "status"}).AddRow(int64(41), "apple-test", int64(3), tt.inventory, tt.status))
			}
			mock.ExpectRollback()
			_, err := s.Delivery(context.Background(), 7, "apple-test")
			if !errors.Is(err, tt.want) {
				t.Fatalf("want %v got %v", tt.want, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDeliveryDecryptsOnlyAfterAuditCommits(t *testing.T) {
	s, mock := newAdminTestService(t)
	account, _ := s.encrypt("buyer@example.com")
	password, _ := s.encrypt("private-password")
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id,trade_no,product_id,inventory_id,status FROM v2_apple_order WHERE user_id=\$1 AND trade_no=\$2 FOR SHARE`).WithArgs(int64(7), "apple-test").
		WillReturnRows(sqlmock.NewRows([]string{"id", "trade_no", "product_id", "inventory_id", "status"}).AddRow(int64(41), "apple-test", int64(3), int64(12), OrderPaid))
	mock.ExpectQuery(`SELECT account_ciphertext,password_ciphertext FROM v2_apple_inventory WHERE id=\$1 AND status=2 AND sold_order_id=\$2`).WithArgs(int64(12), int64(41)).
		WillReturnRows(sqlmock.NewRows([]string{"account_ciphertext", "password_ciphertext"}).AddRow(account, password))
	mock.ExpectExec(`INSERT INTO v2_apple_order_audit`).WithArgs(int64(41), int64(7), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	delivery, err := s.Delivery(context.Background(), 7, "apple-test")
	if err != nil {
		t.Fatal(err)
	}
	if delivery.Account != "buyer@example.com" || delivery.Password != "private-password" {
		t.Fatal("credential roundtrip failed")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestReleaseExpiredReservationsCommitsTogether(t *testing.T) {
	s, mock := newAdminTestService(t)
	mock.ExpectBegin()
	expectReservationSweep(mock)
	mock.ExpectCommit()
	if err := s.ReleaseExpiredReservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCredentialEncryptionAndFingerprint(t *testing.T) {
	s := NewDBService(config.Config{AppKey: "test-key"}, nil)
	first, err := s.encrypt("sensitive-password")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.encrypt("sensitive-password")
	if err != nil {
		t.Fatal(err)
	}
	if first == second || strings.Contains(first, "sensitive-password") {
		t.Fatal("ciphertext must use unique nonces and conceal plaintext")
	}
	plain, err := s.decrypt(first)
	if err != nil || plain != "sensitive-password" {
		t.Fatalf("roundtrip failed: %v", err)
	}
	other := NewDBService(config.Config{AppKey: "different-key"}, nil)
	if _, err := other.decrypt(first); err == nil {
		t.Fatal("wrong key was accepted")
	}
	if _, err := s.decrypt("invalid"); err == nil {
		t.Fatal("malformed ciphertext was accepted")
	}
	f1, _ := s.accountFingerprint(" Buyer@Example.com ")
	f2, _ := s.accountFingerprint("buyer@example.com")
	if f1 != f2 {
		t.Fatal("fingerprints must normalize case and surrounding whitespace")
	}
	if _, err := NewDBService(config.Config{}, nil).encrypt("secret"); err == nil {
		t.Fatal("empty application key was accepted")
	}
}
