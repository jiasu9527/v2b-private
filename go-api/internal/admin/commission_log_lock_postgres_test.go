package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"
)

func TestCommissionLogApplyPostgresFinancialLockOrderAndRevocation(t *testing.T) {
	dsn := os.Getenv("FOREST_COMMISSION_TEST_DSN")
	if dsn == "" {
		t.Skip("requires disposable FOREST_COMMISSION_TEST_DSN")
	}
	for _, revoke := range []bool{false, true} {
		name := "order_then_actor_no_deadlock"
		if revoke {
			name = "revoke_actor_while_waiting"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			db, err := sql.Open("pgx", dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			s := &DBService{db: db}
			if err = s.EnsureCommissionLogSchema(ctx); err != nil {
				t.Fatal(err)
			}
			marker := fmt.Sprint(time.Now().UnixNano())
			trade := "lock-" + marker
			var actor, logID int64
			if err = db.QueryRowContext(ctx, `INSERT INTO v2_user(email,password,uuid,token,is_staff,commission_balance,created_at,updated_at) VALUES($1,'fixture',$2::TEXT,$2::TEXT,1,100,1,1) RETURNING id`, "ledger-lock-"+marker+"@example.test", marker).Scan(&actor); err != nil {
				t.Fatal(err)
			}
			defer func() {
				db.Exec(`DELETE FROM v2_commission_log_operation WHERE actor_id=$1`, actor)
				db.Exec(`DELETE FROM v2_commission_log_preview WHERE actor_id=$1`, actor)
				db.Exec(`DELETE FROM v2_commission_log WHERE invite_user_id=$1`, actor)
				db.Exec(`DELETE FROM v2_order WHERE trade_no=$1`, trade)
				db.Exec(`DELETE FROM v2_user WHERE id=$1`, actor)
			}()
			if _, err = db.ExecContext(ctx, `INSERT INTO v2_order(invite_user_id,user_id,plan_id,type,period,trade_no,total_amount,status,commission_status,commission_balance,actual_commission_balance,created_at,updated_at) VALUES($1,$1,1,1,'month_price',$2,1000,3,2,100,100,1,1)`, actor, trade); err != nil {
				t.Fatal(err)
			}
			if err = db.QueryRowContext(ctx, `INSERT INTO v2_commission_log(invite_user_id,user_id,trade_no,order_amount,get_amount,created_at,updated_at) VALUES($1,$1,$2,1000,100,1,1) RETURNING id`, actor, trade).Scan(&logID); err != nil {
				t.Fatal(err)
			}
			preview, err := s.PreviewCommissionLogs(ctx, actor, CommissionLogPreviewRequest{Filter: CommissionLogFilter{ID: &logID}, Action: "delete", Reason: "lock ordering fixture"})
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := url.Parse(dsn)
			if err != nil {
				t.Fatal(err)
			}
			q := parsed.Query()
			application := "ledger-lock-" + marker
			q.Set("application_name", application)
			parsed.RawQuery = q.Encode()
			applyDB, err := sql.Open("pgx", parsed.String())
			if err != nil {
				t.Fatal(err)
			}
			defer applyDB.Close()
			applyDB.SetMaxOpenConns(1)
			applyService := &DBService{db: applyDB, commissionLogSchemaOK: true}
			financial, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer financial.Rollback()
			var orderID int64
			if err = financial.QueryRowContext(ctx, `SELECT id FROM v2_order WHERE trade_no=$1 FOR UPDATE`, trade).Scan(&orderID); err != nil {
				t.Fatal(err)
			}
			type result struct {
				receipt CommissionLogApplyResult
				err     error
			}
			done := make(chan result, 1)
			request := CommissionLogApplyRequest{PreviewHash: preview.PreviewHash, IdempotencyKey: "lock-key", Confirm: true, TargetID: &logID, ExpectedAction: "delete"}
			go func() { r, e := applyService.ApplyCommissionLogs(ctx, actor, request); done <- result{r, e} }()
			// Wait until apply actually reaches the order lock. The old inversion held
			// actor FOR SHARE at this point, making the following financial update wait.
			waiting := false
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				if err = db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE application_name=$1 AND wait_event_type='Lock' AND query LIKE 'SELECT trade_no FROM v2_order%')`, application).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				select {
				case unexpected := <-done:
					t.Fatalf("apply did not wait for financial order lock: %#v", unexpected)
				default:
				}
				time.Sleep(10 * time.Millisecond)
			}
			if !waiting {
				t.Fatal("apply never reached order lock")
			}
			updateCtx, updateCancel := context.WithTimeout(ctx, 750*time.Millisecond)
			if revoke {
				_, err = financial.ExecContext(updateCtx, `UPDATE v2_user SET is_staff=0 WHERE id=$1`, actor)
			} else {
				_, err = financial.ExecContext(updateCtx, `UPDATE v2_user SET commission_balance=commission_balance+1 WHERE id=$1`, actor)
			}
			updateCancel()
			if err != nil {
				t.Fatal("financial order->actor lock was blocked by visibility apply", err)
			}
			if err = financial.Commit(); err != nil {
				t.Fatal(err)
			}
			var out result
			select {
			case out = <-done:
			case <-ctx.Done():
				t.Fatal("apply never completed", ctx.Err())
			}
			var auditCount int
			var hidden bool
			var balance int64
			db.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_commission_log_operation WHERE actor_id=$1`, actor).Scan(&auditCount)
			db.QueryRowContext(ctx, `SELECT deleted_at IS NOT NULL FROM v2_commission_log WHERE id=$1`, logID).Scan(&hidden)
			db.QueryRowContext(ctx, `SELECT commission_balance FROM v2_user WHERE id=$1`, actor).Scan(&balance)
			if revoke {
				if !errors.Is(out.err, ErrCommissionLogForbidden) || auditCount != 0 || hidden || balance != 100 {
					t.Fatal("revocation after wait bypassed or partially committed", out.err, auditCount, hidden, balance)
				}
			} else {
				if out.err != nil || !out.receipt.Committed || auditCount != 1 || !hidden || balance != 101 {
					t.Fatal("financial lock regression", out.err, auditCount, hidden, balance)
				}
				replay, e := applyService.ApplyCommissionLogs(ctx, actor, request)
				if e != nil || !replay.Replayed || replay.OperationID != out.receipt.OperationID {
					t.Fatal("retry duplicated receipt", replay, e)
				}
			}
		})
	}
}
