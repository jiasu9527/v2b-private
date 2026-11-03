package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCommissionLogPostgresVisibilityPreviewIdempotencyAndBalance(t *testing.T) {
	dsn := os.Getenv("FOREST_COMMISSION_TEST_DSN")
	if dsn == "" {
		t.Skip("requires disposable FOREST_COMMISSION_TEST_DSN initialized with install.pgsql.sql")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s := &DBService{db: db}
	if err = s.EnsureCommissionLogSchema(ctx); err != nil {
		t.Fatal(err)
	}
	marker := fmt.Sprint(time.Now().UnixNano())
	var actor, other, inviter, buyer int64
	for i, target := range []*int64{&actor, &other, &inviter, &buyer} {
		role := 0
		if i < 2 {
			role = 1
		}
		if err = db.QueryRowContext(ctx, `INSERT INTO v2_user(email,password,uuid,token,is_staff,commission_balance,balance,created_at,updated_at) VALUES($1,'fixture',$2::TEXT,$2::TEXT,$3,43415,12345,1,1) RETURNING id`, fmt.Sprintf("ledger-%d-%s@example.test", i, marker), fmt.Sprintf("ledger-%d-%s", i, marker), role).Scan(target); err != nil {
			t.Fatal(err)
		}
	}

	// Cleanup must run before the parent function closes the database.
	defer func() {
		db.Exec(`DELETE FROM v2_commission_log_operation WHERE actor_id IN ($1,$2)`, actor, other)
		db.Exec(`DELETE FROM v2_commission_log_preview WHERE actor_id IN ($1,$2)`, actor, other)
		db.Exec(`DELETE FROM v2_commission_log WHERE invite_user_id=$1`, inviter)
		db.Exec(`DELETE FROM v2_order WHERE invite_user_id=$1`, inviter)
		db.Exec(`DELETE FROM v2_user WHERE id IN ($1,$2,$3,$4)`, actor, other, inviter, buyer)
	}()
	insert := func(index int64) int64 {
		t.Helper()
		trade := fmt.Sprintf("ledger-%s-%d", marker, index)
		if _, err = db.ExecContext(ctx, `INSERT INTO v2_order(invite_user_id,user_id,plan_id,type,period,trade_no,total_amount,status,commission_status,commission_balance,actual_commission_balance,created_at,updated_at) VALUES($1,$2,1,1,'month_price',$3,1000,3,2,100,100,$4,$4)`, inviter, buyer, trade, index); err != nil {
			t.Fatal(err)
		}
		var id int64
		if err = db.QueryRowContext(ctx, `INSERT INTO v2_commission_log(invite_user_id,user_id,trade_no,order_amount,get_amount,created_at,updated_at) VALUES($1,$2,$3,1000,100,$4,$4) RETURNING id`, inviter, buyer, trade, index).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	a, b := insert(10), insert(20)
	filter := CommissionLogFilter{InviteUserID: &inviter, CreatedFrom: commissionInt(10), CreatedTo: commissionInt(21), CommissionStatus: commissionInt(2)}
	list, err := s.ListCommissionLogs(ctx, CommissionLogListRequest{Filter: filter, PageSize: 1})
	if err != nil || list.Total != 2 || list.TotalAmount != 200 || len(list.Data) != 1 || list.Data[0].ID != a {
		t.Fatal(list, err)
	}
	preview, err := s.PreviewCommissionLogs(ctx, actor, CommissionLogPreviewRequest{Filter: filter, Action: "delete", Reason: "duplicate display cleanup"})
	if err != nil || preview.Count != 2 || preview.TotalAmount != 200 {
		t.Fatal(preview, err)
	}
	if _, err = s.ApplyCommissionLogs(ctx, other, CommissionLogApplyRequest{PreviewHash: preview.PreviewHash, IdempotencyKey: "foreign", Confirm: true}); !errors.Is(err, ErrCommissionLogNotFound) {
		t.Fatal("preview not actor bound", err)
	}
	if _, err = s.ApplyCommissionLogs(ctx, actor, CommissionLogApplyRequest{PreviewHash: preview.PreviewHash, IdempotencyKey: "wrong-action", Confirm: true, ExpectedAction: "restore"}); !errors.Is(err, ErrCommissionLogConflict) {
		t.Fatal("wrong route allowed", err)
	}
	if _, err = s.ApplyCommissionLogs(ctx, actor, CommissionLogApplyRequest{PreviewHash: preview.PreviewHash, IdempotencyKey: "wrong-single", Confirm: true, TargetID: &a}); !errors.Is(err, ErrCommissionLogConflict) {
		t.Fatal("bulk preview used as single", err)
	}
	// New matches after preview are not silently added to the frozen batch.
	c := insert(15)
	request := CommissionLogApplyRequest{PreviewHash: preview.PreviewHash, IdempotencyKey: "same-key", Confirm: true, ExpectedAction: "delete"}
	const concurrency = 8
	results := make(chan CommissionLogApplyResult, concurrency)
	errs := make(chan error, concurrency)
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); r, e := s.ApplyCommissionLogs(ctx, actor, request); results <- r; errs <- e }()
	}
	wg.Wait()
	close(results)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal("concurrent replay failed", e)
		}
	}
	var operationID int64
	first := 0
	for r := range results {
		if !r.Committed || r.Count != 2 || r.BalanceChanged || r.TotalAmount != 200 {
			t.Fatal(r)
		}
		if !r.Replayed {
			first++
		}
		if operationID == 0 {
			operationID = r.OperationID
		}
		if operationID != r.OperationID {
			t.Fatal("multiple operations", r)
		}
	}
	if first != 1 {
		t.Fatal("write executed more than once", first)
	}
	if _, err = s.ApplyCommissionLogs(ctx, actor, CommissionLogApplyRequest{PreviewHash: preview.PreviewHash, IdempotencyKey: "another-key", Confirm: true}); !errors.Is(err, ErrCommissionLogConflict) {
		t.Fatal("consumed preview repeated", err)
	}
	request.ExpectedAction = "restore"
	if _, err = s.ApplyCommissionLogs(ctx, actor, request); !errors.Is(err, ErrCommissionLogConflict) {
		t.Fatal("key reused for another action", err)
	}
	request.ExpectedAction = "delete"
	db.ExecContext(ctx, `UPDATE v2_commission_log_preview SET expires_at=1 WHERE actor_id=$1`, actor)
	replay, err := s.ApplyCommissionLogs(ctx, actor, request)
	if err != nil || !replay.Replayed || replay.OperationID != operationID {
		t.Fatal("expired receipt retry failed", replay, err)
	}
	assertMoney := func() {
		t.Helper()
		var balance, commission int64
		if err = db.QueryRowContext(ctx, `SELECT balance,commission_balance FROM v2_user WHERE id=$1`, inviter).Scan(&balance, &commission); err != nil || balance != 12345 || commission != 43415 {
			t.Fatal("visibility operation changed balance", balance, commission, err)
		}
		var invalid int
		if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_order WHERE invite_user_id=$1 AND (commission_status<>2 OR commission_balance<>100 OR actual_commission_balance<>100)`, inviter).Scan(&invalid); err != nil || invalid != 0 {
			t.Fatal("settlement state changed", invalid, err)
		}
	}
	assertMoney()
	list, err = s.ListCommissionLogs(ctx, CommissionLogListRequest{Filter: CommissionLogFilter{InviteUserID: &inviter}})
	if err != nil || list.Total != 1 || list.Data[0].ID != c {
		t.Fatal("new row included or deleted rows visible", list, err)
	}
	deletedFilter := CommissionLogFilter{InviteUserID: &inviter, Visibility: "deleted"}
	hidden, err := s.ListCommissionLogs(ctx, CommissionLogListRequest{Filter: deletedFilter})
	if err != nil || hidden.Total != 2 || hidden.Data[0].DeletedBy == nil || *hidden.Data[0].DeletedBy != actor || hidden.Data[1].ID != b {
		t.Fatal(hidden, err)
	}
	restore, err := s.PreviewCommissionLogs(ctx, actor, CommissionLogPreviewRequest{Filter: deletedFilter, Action: "restore", Reason: "undo cleanup"})
	if err != nil {
		t.Fatal(err)
	}
	restored, err := s.ApplyCommissionLogs(ctx, actor, CommissionLogApplyRequest{PreviewHash: restore.PreviewHash, IdempotencyKey: "restore-key", Confirm: true, ExpectedAction: "restore"})
	if err != nil || restored.Action != "restore" || restored.Count != 2 {
		t.Fatal(restored, err)
	}
	assertMoney()
	list, err = s.ListCommissionLogs(ctx, CommissionLogListRequest{Filter: CommissionLogFilter{InviteUserID: &inviter}})
	if err != nil || list.Total != 3 || list.Data[0].VisibilityVersion != 2 {
		t.Fatal(list, err)
	}
	// A source row changing, including a refunded/deleted source, invalidates the
	// entire preview; no surviving subset is modified.
	stale, err := s.PreviewCommissionLogs(ctx, actor, CommissionLogPreviewRequest{Filter: CommissionLogFilter{IDs: []int64{a, b}}, Action: "delete", Reason: "stale preview"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `UPDATE v2_commission_log SET get_amount=101 WHERE id=$1`, b); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ApplyCommissionLogs(ctx, actor, CommissionLogApplyRequest{PreviewHash: stale.PreviewHash, IdempotencyKey: "stale-key", Confirm: true}); !errors.Is(err, ErrCommissionLogConflict) {
		t.Fatal("stale snapshot accepted", err)
	}
	var stillActive bool
	db.QueryRowContext(ctx, `SELECT deleted_at IS NULL FROM v2_commission_log WHERE id=$1`, a).Scan(&stillActive)
	if !stillActive {
		t.Fatal("partial stale batch committed")
	}
	statusPreview, err := s.PreviewCommissionLogs(ctx, actor, CommissionLogPreviewRequest{Filter: CommissionLogFilter{ID: &c}, Action: "delete", Reason: "order state changed"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `UPDATE v2_order SET commission_status=3 WHERE trade_no=(SELECT trade_no FROM v2_commission_log WHERE id=$1)`, c); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ApplyCommissionLogs(ctx, actor, CommissionLogApplyRequest{PreviewHash: statusPreview.PreviewHash, IdempotencyKey: "changed-order", Confirm: true, TargetID: &c}); !errors.Is(err, ErrCommissionLogConflict) {
		t.Fatal("changed settlement state not detected", err)
	}
	expiredPreview, err := s.PreviewCommissionLogs(ctx, actor, CommissionLogPreviewRequest{Filter: CommissionLogFilter{ID: &c}, Action: "delete", Reason: "expired preview"})
	if err != nil {
		t.Fatal(err)
	}
	db.ExecContext(ctx, `UPDATE v2_commission_log_preview SET expires_at=1 WHERE token_hash=$1`, commissionLogTokenHash(expiredPreview.PreviewHash))
	if _, err = s.ApplyCommissionLogs(ctx, actor, CommissionLogApplyRequest{PreviewHash: expiredPreview.PreviewHash, IdempotencyKey: "expired-key", Confirm: true}); !errors.Is(err, ErrCommissionLogConflict) {
		t.Fatal("expired unapplied preview accepted", err)
	}
	// Empty target filters require confirmation twice, even with visibility.
	if _, err = s.PreviewCommissionLogs(ctx, actor, CommissionLogPreviewRequest{Action: "delete", Reason: "all"}); !errors.Is(err, ErrCommissionLogValidation) {
		t.Fatal(err)
	}
	all, err := s.PreviewCommissionLogs(ctx, actor, CommissionLogPreviewRequest{Action: "delete", Reason: "all explicit", ConfirmAll: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ApplyCommissionLogs(ctx, actor, CommissionLogApplyRequest{PreviewHash: all.PreviewHash, IdempotencyKey: "all-no-confirm", Confirm: true}); !errors.Is(err, ErrCommissionLogValidation) {
		t.Fatal("missing second all confirmation accepted", err)
	}
	var auditCount int
	var snapshot string
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_commission_log_operation WHERE actor_id=$1`, actor).Scan(&auditCount); err != nil || auditCount != 2 {
		t.Fatal("audit is not exactly once", auditCount, err)
	}
	if err = db.QueryRowContext(ctx, `SELECT snapshot::text FROM v2_commission_log_operation WHERE id=$1`, operationID).Scan(&snapshot); err != nil || !strings.Contains(snapshot, `"get_amount": 100`) || !strings.Contains(snapshot, fmt.Sprintf(`"invite_user_id": %d`, inviter)) {
		t.Fatal("original audit evidence lost", snapshot, err)
	}
	var previewSnapshot string
	if err = db.QueryRowContext(ctx, `SELECT snapshot::text FROM v2_commission_log_preview WHERE applied_operation_id=$1`, operationID).Scan(&previewSnapshot); err != nil || previewSnapshot != "[]" {
		t.Fatal("applied preview keeps unnecessary duplicate audit body", previewSnapshot, err)
	}
}
