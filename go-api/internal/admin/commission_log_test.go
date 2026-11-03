package admin

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func commissionInt(v int64) *int64 { return &v }
func TestCommissionLogStrictFilters(t *testing.T) {
	for _, f := range []CommissionLogFilter{
		{ID: commissionInt(0)}, {ID: commissionInt(2147483648)}, {CreatedTo: commissionInt(2147483648)}, {ID: commissionInt(1), IDs: []int64{2}}, {IDs: []int64{-1}},
		{Visibility: "unknown"}, {CreatedFrom: commissionInt(20), CreatedTo: commissionInt(10)},
		{CreatedFrom: commissionInt(-1)}, {CommissionStatus: commissionInt(4)},
		{TradeNo: strings.Repeat("x", 37)}, {TradeNo: "x\ny"},
	} {
		if _, err := normalizeCommissionLogFilter(f); !errors.Is(err, ErrCommissionLogValidation) {
			t.Fatalf("accepted bad filter %#v %v", f, err)
		}
	}
	f, err := normalizeCommissionLogFilter(CommissionLogFilter{IDs: []int64{3, 1, 3, 2}, InviteUserID: commissionInt(61624), UserID: commissionInt(81), TradeNo: " target ", CreatedFrom: commissionInt(10), CreatedTo: commissionInt(20), CommissionStatus: commissionInt(2)})
	if err != nil || !reflect.DeepEqual(f.IDs, []int64{1, 2, 3}) || f.Visibility != "active" || f.TradeNo != "target" {
		t.Fatal(f, err)
	}
	where, args := commissionLogWhere(f)
	for _, clause := range []string{"l.id IN ($1,$2,$3)", "l.invite_user_id = $4", "l.user_id = $5", "l.trade_no = $6", "l.created_at >= $7", "l.created_at < $8", "o.commission_status = $9", "l.deleted_at IS NULL"} {
		if !strings.Contains(where, clause) {
			t.Fatal(where)
		}
	}
	if len(args) != 9 || strings.Contains(where, "61624") || strings.Count(where, " AND ") != 7 {
		t.Fatal(where, args)
	}
	if commissionLogFilterAll(f) || !commissionLogFilterAll(CommissionLogFilter{Visibility: "deleted"}) {
		t.Fatal("all-records boundary")
	}
}
func TestCommissionLogRejectsUnconfirmedOrAmbiguousBeforeDatabase(t *testing.T) {
	s := &DBService{}
	for _, req := range []CommissionLogPreviewRequest{
		{Action: "delete", Reason: "cleanup"},
		{Action: "delete", Reason: "cleanup", Filter: CommissionLogFilter{Visibility: "all"}, ConfirmAll: true},
		{Action: "restore", Reason: "cleanup", Filter: CommissionLogFilter{Visibility: "active"}, ConfirmAll: true},
		{Action: "delete", ConfirmAll: true},
		{Action: "recredit", Reason: "no", ConfirmAll: true},
	} {
		if _, err := s.PreviewCommissionLogs(context.Background(), 1, req); !errors.Is(err, ErrCommissionLogValidation) {
			t.Fatal(req, err)
		}
	}
	for _, req := range []CommissionLogApplyRequest{
		{PreviewHash: strings.Repeat("a", 64), IdempotencyKey: "key"},
		{PreviewHash: "invalid", IdempotencyKey: "key", Confirm: true},
		{PreviewHash: strings.Repeat("a", 64), IdempotencyKey: " key", Confirm: true},
		{PreviewHash: strings.Repeat("a", 64), IdempotencyKey: "key", Confirm: true, TargetID: commissionInt(0)},
		{PreviewHash: strings.Repeat("a", 64), IdempotencyKey: "key", Confirm: true, ExpectedAction: "other"},
	} {
		if _, err := s.ApplyCommissionLogs(context.Background(), 1, req); !errors.Is(err, ErrCommissionLogValidation) {
			t.Fatal(req, err)
		}
	}
}
func TestCommissionLogListCountSumAndDataUseSameSnapshotFilters(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := &DBService{db: db, commissionLogSchemaOK: true}
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT COUNT\(\*\),COALESCE\(SUM\(l.get_amount\),0\).*WHERE l.invite_user_id = \$1 AND l.created_at < \$2 AND l.deleted_at IS NULL`).WithArgs(int64(61624), int64(1790812800)).WillReturnRows(sqlmock.NewRows([]string{"count", "sum"}).AddRow(1, 500))
	mock.ExpectQuery(`SELECT l.id,.*WHERE l.invite_user_id = \$1 AND l.created_at < \$2 AND l.deleted_at IS NULL ORDER BY l.id ASC LIMIT \$3 OFFSET \$4`).WithArgs(int64(61624), int64(1790812800), int64(20), int64(0)).WillReturnRows(sqlmock.NewRows([]string{"id", "invite_user_id", "user_id", "trade_no", "order_amount", "get_amount", "created_at", "updated_at", "commission_status", "deleted_at", "deleted_by", "delete_reason", "visibility_version"}).AddRow(1, 61624, 81, "fixture", 1000, 500, 100, 100, 2, nil, nil, "", 0))
	mock.ExpectCommit()
	got, err := s.ListCommissionLogs(context.Background(), CommissionLogListRequest{Filter: CommissionLogFilter{InviteUserID: commissionInt(61624), CreatedTo: commissionInt(1790812800)}})
	if err != nil || got.Total != 1 || got.TotalAmount != 500 || len(got.Data) != 1 || *got.AppliedFilters.InviteUserID != 61624 || got.AppliedFilters.Visibility != "active" {
		t.Fatal(got, err)
	}
	raw, _ := json.Marshal(got)
	for _, secret := range []string{"password", "token", "checkout", "uuid"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("unexpected field", string(raw))
		}
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
