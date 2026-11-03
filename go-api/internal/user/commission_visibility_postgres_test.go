package user_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/config"
	platformpostgres "forest/go-api/internal/platform/postgres"
	usersvc "forest/go-api/internal/user"
)

func TestCommissionVisibilityPostgresAdminCleanupUserDetailsAndHistory(t *testing.T) {
	dsn := os.Getenv("FOREST_COMMISSION_TEST_DSN")
	if dsn == "" {
		t.Skip("requires disposable FOREST_COMMISSION_TEST_DSN initialized with install.pgsql.sql")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err = platformpostgres.EnsureCommissionLogSchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	marker := fmt.Sprint(time.Now().UnixNano())
	var actor, inviter, buyer, logID int64
	ids := []int64{}
	defer func() {
		if actor > 0 {
			db.Exec(`DELETE FROM v2_commission_log_operation WHERE actor_id=$1`, actor)
			db.Exec(`DELETE FROM v2_commission_log_preview WHERE actor_id=$1`, actor)
		}
		if inviter > 0 {
			db.Exec(`DELETE FROM v2_commission_log WHERE invite_user_id=$1`, inviter)
			db.Exec(`DELETE FROM v2_order WHERE invite_user_id=$1`, inviter)
		}
		for i := len(ids) - 1; i >= 0; i-- {
			db.Exec(`DELETE FROM v2_user WHERE id=$1`, ids[i])
		}
	}()
	for i, target := range []*int64{&actor, &inviter, &buyer} {
		isAdmin := 0
		if i == 0 {
			isAdmin = 1
		}
		if err = db.QueryRowContext(ctx, `INSERT INTO v2_user(email,password,uuid,token,is_admin,commission_balance,balance,created_at,updated_at) VALUES($1,'fixture',$2::TEXT,$2::TEXT,$3,43415,1250,1,1) RETURNING id`, fmt.Sprintf("visibility-%d-%s@example.test", i, marker), fmt.Sprintf("visibility-%d-%s", i, marker), isAdmin).Scan(target); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, *target)
	}
	if _, err = db.ExecContext(ctx, `UPDATE v2_user SET invite_user_id=$2 WHERE id=$1`, buyer, inviter); err != nil {
		t.Fatal(err)
	}
	trade := "visibility-" + marker
	if _, err = db.ExecContext(ctx, `INSERT INTO v2_order(invite_user_id,user_id,plan_id,type,period,trade_no,total_amount,status,commission_status,commission_balance,actual_commission_balance,created_at,updated_at) VALUES($1,$2,1,1,'month_price',$3,1000,3,2,300,300,1,1)`, inviter, buyer, trade); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRowContext(ctx, `INSERT INTO v2_commission_log(invite_user_id,user_id,trade_no,order_amount,get_amount,created_at,updated_at) VALUES($1,$2,$3,1000,300,1,1) RETURNING id`, inviter, buyer, trade).Scan(&logID); err != nil {
		t.Fatal(err)
	}
	panel := admin.NewDBService(config.Config{}, db)
	user := usersvc.NewDBService(config.Config{InviteCommission: 30}, db)
	check := func(visible bool) {
		t.Helper()
		rows, total, e := user.InviteDetails(ctx, inviter, 1, 20)
		if e != nil {
			t.Fatal(e)
		}
		want := int64(0)
		if visible {
			want = 1
		}
		if total != want || int64(len(rows)) != want {
			t.Fatalf("details visible=%t total=%d rows=%#v", visible, total, rows)
		}
		if visible && strings.TrimSpace(fmt.Sprint(rows[0]["trade_no"])) != trade {
			t.Fatal("wrong detail row", rows)
		}
		overview, e := user.InviteOverview(ctx, inviter)
		if e != nil {
			t.Fatal(e)
		}
		stat, ok := overview["stat"].([]int64)
		if !ok || len(stat) != 5 || stat[0] != 1 || stat[1] != 300 || stat[4] != 43415 {
			t.Fatalf("history or balance changed after visibility edit: %#v", overview)
		}
		var balance int64
		var status int64
		if e = db.QueryRowContext(ctx, `SELECT balance FROM v2_user WHERE id=$1`, inviter).Scan(&balance); e != nil || balance != 1250 {
			t.Fatal(balance, e)
		}
		if e = db.QueryRowContext(ctx, `SELECT commission_status FROM v2_order WHERE trade_no=$1`, trade).Scan(&status); e != nil || status != 2 {
			t.Fatal("settlement flag changed", status, e)
		}
	}
	check(true)
	preview, e := panel.PreviewCommissionLogs(ctx, actor, admin.CommissionLogPreviewRequest{Filter: admin.CommissionLogFilter{ID: &logID}, Action: "delete", Reason: "user requested display cleanup"})
	if e != nil {
		t.Fatal(e)
	}
	deleted, e := panel.ApplyCommissionLogs(ctx, actor, admin.CommissionLogApplyRequest{PreviewHash: preview.PreviewHash, IdempotencyKey: "visibility-delete", Confirm: true, TargetID: &logID, ExpectedAction: "delete"})
	if e != nil || !deleted.Committed || deleted.BalanceChanged {
		t.Fatal(deleted, e)
	}
	check(false)
	restore, e := panel.PreviewCommissionLogs(ctx, actor, admin.CommissionLogPreviewRequest{Filter: admin.CommissionLogFilter{ID: &logID}, Action: "restore", Reason: "undo display cleanup"})
	if e != nil {
		t.Fatal(e)
	}
	restored, e := panel.ApplyCommissionLogs(ctx, actor, admin.CommissionLogApplyRequest{PreviewHash: restore.PreviewHash, IdempotencyKey: "visibility-restore", Confirm: true, TargetID: &logID, ExpectedAction: "restore"})
	if e != nil || !restored.Committed || restored.BalanceChanged {
		t.Fatal(restored, e)
	}
	check(true)
}
