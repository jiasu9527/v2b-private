package admin

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestOrderCommissionStatusRejectsInvalidTargetsBeforeDatabase(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := &DBService{db: db}
	for _, target := range []int64{-1, 2, 4, 99} {
		ok, err := s.UpdateOrder(context.Background(), OrderUpdateRequest{TradeNo: "fixture", CommissionStatus: &target})
		if ok || !errors.Is(err, ErrOrderCommissionInvalid) {
			t.Fatalf("target %d: %v %v", target, ok, err)
		}
	}
	for _, req := range []OrderUpdateRequest{{TradeNo: "fixture"}, {}} {
		if _, err := s.UpdateOrder(context.Background(), req); !errors.Is(err, ErrOrderCommissionInvalid) {
			t.Fatal(err)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestOrderCommissionStatusSettlementFence(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		current, credited, target int64
		hasLedger, blocked        bool
	}{
		{"pending_to_queued", 0, 0, 1, false, false},
		{"queued_to_rejected", 1, 0, 3, false, false},
		{"rejected_to_pending", 3, 0, 0, false, false},
		{"same_state_no_write", 1, 0, 1, false, false},
		{"settled_cannot_queue", 2, 100, 1, false, true},
		{"settled_zero_cannot_reset", 2, 0, 0, false, true},
		{"credited_cannot_reject", 1, 100, 3, false, true},
		{"history_even_if_hidden_blocks_queue", 0, 0, 1, true, true},
		{"history_even_same_state_is_unsafe", 1, 0, 1, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			mock.ExpectBegin()
			mock.ExpectQuery(regexp.QuoteMeta(`SELECT commission_status, COALESCE(actual_commission_balance, 0)
FROM v2_order WHERE trade_no = $1 FOR UPDATE`)).WithArgs("fixture").WillReturnRows(sqlmock.NewRows([]string{"commission_status", "actual"}).AddRow(tc.current, tc.credited))
			if tc.current != 2 && tc.credited <= 0 {
				mock.ExpectQuery(regexp.QuoteMeta(`SELECT EXISTS (SELECT 1 FROM v2_commission_log WHERE trade_no = $1)`)).WithArgs("fixture").WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(tc.hasLedger))
			}
			if tc.blocked {
				mock.ExpectRollback()
			} else {
				if tc.current != tc.target {
					mock.ExpectExec(regexp.QuoteMeta(`UPDATE v2_order
SET commission_status = $2, updated_at = $3
WHERE trade_no = $1`)).WithArgs("fixture", tc.target, sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
				}
				mock.ExpectCommit()
			}
			ok, err := (&DBService{db: db}).UpdateOrder(context.Background(), OrderUpdateRequest{TradeNo: " fixture ", CommissionStatus: &tc.target})
			if tc.blocked {
				if ok || !errors.Is(err, ErrOrderCommissionSettled) {
					t.Fatalf("settlement not fenced: %v %v", ok, err)
				}
			} else if !ok || err != nil {
				t.Fatal(ok, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestOrderCommissionStatusDoesNotClaimSuccessOnCommitFailure(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT commission_status, COALESCE`).WithArgs("fixture").WillReturnRows(sqlmock.NewRows([]string{"status", "amount"}).AddRow(0, 0))
	mock.ExpectQuery(`SELECT EXISTS`).WithArgs("fixture").WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectExec(`UPDATE v2_order`).WithArgs("fixture", int64(3), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit().WillReturnError(errors.New("connection lost"))
	target := int64(3)
	if ok, err := (&DBService{db: db}).UpdateOrder(context.Background(), OrderUpdateRequest{TradeNo: "fixture", CommissionStatus: &target}); ok || err == nil {
		t.Fatal("uncertain commit reported success", ok, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
