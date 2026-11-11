package payment

import (
	"context"
	"errors"
	"strings"
	"testing"

	"forest/go-api/internal/config"
	"github.com/DATA-DOG/go-sqlmock"
)

type externalManagerFixture struct {
	prefix string
	calls  []string
}

func (f *externalManagerFixture) OwnsExternalTradeNo(trade string) bool {
	return strings.HasPrefix(trade, f.prefix)
}
func (f *externalManagerFixture) LookupExternalOrder(_ context.Context, user int64, trade string) (ExternalOrder, error) {
	f.calls = append(f.calls, "lookup")
	return ExternalOrder{TradeNo: trade, TotalAmount: 100}, nil
}
func (f *externalManagerFixture) SetExternalPayment(context.Context, int64, string, int64, int64) error {
	f.calls = append(f.calls, "set")
	return nil
}
func (f *externalManagerFixture) MarkExternalPaid(context.Context, string, string, bool, *int64, *int64) error {
	f.calls = append(f.calls, "paid")
	return nil
}

type guardedExternalFixture struct {
	externalManagerFixture
	guardCalls int
}

func (f *guardedExternalFixture) CheckoutExternalOnce(context.Context, int64, CheckoutRequest, func(context.Context) (CheckoutResult, error)) (CheckoutResult, error) {
	f.guardCalls++
	return CheckoutResult{Type: 1, Data: "https://payments.example/previous-checkout"}, nil
}

func TestExternalManagersDispatchAndFailClosed(t *testing.T) {
	apple := &externalManagerFixture{prefix: "APPLE"}
	ai := &externalManagerFixture{prefix: "AIT"}
	m := NewExternalOrderManagers(apple, nil, ai)
	ctx := context.Background()
	for _, trade := range []string{"APPLE1", "AIT1"} {
		if !m.OwnsExternalTradeNo(trade) {
			t.Fatal(trade)
		}
		if _, err := m.LookupExternalOrder(ctx, 1, trade); err != nil {
			t.Fatal(err)
		}
		if err := m.SetExternalPayment(ctx, 1, trade, 2, 0); err != nil {
			t.Fatal(err)
		}
		if err := m.MarkExternalPaid(ctx, trade, "cb", false, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Join(apple.calls, ",") != "lookup,set,paid" || strings.Join(ai.calls, ",") != "lookup,set,paid" {
		t.Fatal(apple.calls, ai.calls)
	}
	if m.OwnsExternalTradeNo("ordinary-order") {
		t.Fatal("ordinary order intercepted")
	}
	if _, err := m.LookupExternalOrder(ctx, 1, "ordinary-order"); !errors.Is(err, ErrOrderPaidOrMissing) {
		t.Fatal(err)
	}
	ambiguous := NewExternalOrderManagers(apple, apple)
	if _, err := ambiguous.LookupExternalOrder(ctx, 1, "APPLE1"); !errors.Is(err, ErrInvalidParameter) {
		t.Fatal("ambiguous namespace accepted", err)
	}
}

func TestGenericCheckoutCannotBypassExternalGuard(t *testing.T) {
	for _, composite := range []bool{false, true} {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		f := &guardedExternalFixture{externalManagerFixture: externalManagerFixture{prefix: "AIT"}}
		var manager ExternalOrderManager = f
		if composite {
			manager = NewExternalOrderManagers(&externalManagerFixture{prefix: "APPLE"}, f)
		}
		s := NewDBService(config.Config{}, db, nil).WithExternalOrderManager(manager)
		for i := 0; i < 2; i++ {
			result, err := s.Checkout(context.Background(), 7, CheckoutRequest{TradeNo: "AIT1", MethodID: 3})
			if err != nil || result.Type != 1 || result.Data != "https://payments.example/previous-checkout" {
				t.Fatal(result, err)
			}
		}
		if f.guardCalls != 2 || len(f.calls) != 0 {
			t.Fatal("guard bypassed", f.guardCalls, f.calls)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
		db.Close()
	}
}

func TestLegacyExternalManagerUsesOriginalCheckout(t *testing.T) {
	f := &externalManagerFixture{prefix: "APPLE"}
	m := NewExternalOrderManagers(f).(ExternalCheckoutManager)
	calls := 0
	_, err := m.CheckoutExternalOnce(context.Background(), 1, CheckoutRequest{TradeNo: "APPLE1"}, func(context.Context) (CheckoutResult, error) {
		calls++
		return CheckoutResult{Type: -1, Data: true}, nil
	})
	if err != nil || calls != 1 {
		t.Fatal(calls, err)
	}
	_, err = m.CheckoutExternalOnce(context.Background(), 1, CheckoutRequest{TradeNo: "UNKNOWN1"}, func(context.Context) (CheckoutResult, error) { calls++; return CheckoutResult{}, nil })
	if !errors.Is(err, ErrOrderPaidOrMissing) || calls != 1 {
		t.Fatal("unknown manager invoked callback", err, calls)
	}
}
