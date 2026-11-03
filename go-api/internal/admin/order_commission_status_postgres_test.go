package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"forest/go-api/internal/config"
	platformpostgres "forest/go-api/internal/platform/postgres"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// Opt-in only: this database must be disposable and initialized from install SQL.
func TestOrderCommissionStatusPostgresSettlementRaceAndHiddenHistory(t *testing.T) {
	dsn := os.Getenv("FOREST_COMMISSION_TEST_DSN")
	if dsn == "" {
		t.Skip("requires disposable FOREST_COMMISSION_TEST_DSN")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err = platformpostgres.EnsureCommissionLogSchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	trade := fmt.Sprintf("commission-race-%d", time.Now().UnixNano())
	if _, err = db.ExecContext(ctx, `INSERT INTO v2_order(user_id,plan_id,type,period,trade_no,total_amount,status,commission_status,commission_balance,created_at,updated_at) VALUES(1,1,1,'month_price',$1,1000,3,0,100,1,1)`, trade); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM v2_order WHERE trade_no=$1`, trade)
	defer db.Exec(`DELETE FROM v2_commission_log WHERE trade_no=$1`, trade)
	s := NewDBService(config.Config{}, db)
	zero := int64(0)
	if ok, err := s.UpdateOrder(ctx, OrderUpdateRequest{TradeNo: trade, CommissionStatus: &zero}); err != nil || !ok {
		t.Fatal(ok, err)
	}
	var updated int64
	if err = db.QueryRowContext(ctx, `SELECT updated_at FROM v2_order WHERE trade_no=$1`, trade).Scan(&updated); err != nil || updated != 1 {
		t.Fatal("no-op changed timestamp", updated, err)
	}
	writer, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback()
	if _, err = writer.ExecContext(ctx, `SELECT id FROM v2_order WHERE trade_no=$1 FOR UPDATE`, trade); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		target := int64(1)
		_, e := s.UpdateOrder(ctx, OrderUpdateRequest{TradeNo: trade, CommissionStatus: &target})
		result <- e
	}()
	// The writer owns the same row lock as the actual settlement worker.
	select {
	case err := <-result:
		t.Fatalf("status editor escaped settlement lock: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	if _, err = writer.ExecContext(ctx, `INSERT INTO v2_commission_log(invite_user_id,user_id,trade_no,order_amount,get_amount,created_at,updated_at) VALUES(2,1,$1,1000,100,1,1)`, trade); err != nil {
		t.Fatal(err)
	}
	if _, err = writer.ExecContext(ctx, `UPDATE v2_order SET commission_status=2,actual_commission_balance=100 WHERE trade_no=$1`, trade); err != nil {
		t.Fatal(err)
	}
	if err = writer.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = <-result; !errors.Is(err, ErrOrderCommissionSettled) {
		t.Fatalf("settlement fence failed after lock wait: %v", err)
	}
	// Simulate an old bad state edit, then hide the ledger. A hidden historical
	// payment remains evidence, even when status/amount flags were corrupted.
	if _, err = db.ExecContext(ctx, `UPDATE v2_order SET commission_status=0,actual_commission_balance=NULL WHERE trade_no=$1`, trade); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `UPDATE v2_commission_log SET deleted_at=1,deleted_by=9,delete_reason='fixture',visibility_version=1 WHERE trade_no=$1`, trade); err != nil {
		t.Fatal(err)
	}
	target := int64(1)
	if _, err = s.UpdateOrder(ctx, OrderUpdateRequest{TradeNo: trade, CommissionStatus: &target}); !errors.Is(err, ErrOrderCommissionSettled) {
		t.Fatalf("hidden ledger permitted reissue: %v", err)
	}
	var status, count int64
	if err = db.QueryRowContext(ctx, `SELECT commission_status FROM v2_order WHERE trade_no=$1`, trade).Scan(&status); err != nil || status != 0 {
		t.Fatal(status, err)
	}
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_commission_log WHERE trade_no=$1`, trade).Scan(&count); err != nil || count != 1 {
		t.Fatal("ledger mutated", count, err)
	}
}
