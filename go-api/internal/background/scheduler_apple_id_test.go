package background

import (
	"context"
	"errors"
	"testing"
)

type failingOrderProcessor struct{ err error }

func (f failingOrderProcessor) HandlePendingOrders(context.Context) error { return f.err }

type captureReservations struct {
	calls int
	err   error
}

func (r *captureReservations) ReleaseExpiredReservations(context.Context) error {
	r.calls++
	return r.err
}

func TestReservationSweepRunsAfterSubscriptionFailure(t *testing.T) {
	subscriptionErr := errors.New("subscription sweep failed")
	reservationErr := errors.New("reservation sweep failed")
	reservations := &captureReservations{err: reservationErr}
	q := &captureQueue{runNow: true}
	runner := NewRunner(q, nil, failingOrderProcessor{err: subscriptionErr}, nil, nil, reservations)
	err := runner.RunOnce(context.Background())
	if reservations.calls != 1 {
		t.Fatalf("reservation cleanup was skipped after subscription failure: %d calls", reservations.calls)
	}
	if !errors.Is(err, subscriptionErr) || !errors.Is(err, reservationErr) {
		t.Fatalf("expected both sweep errors, got %v", err)
	}
}

func TestReservationSweepRunsWithoutSubscriptionProcessor(t *testing.T) {
	reservations := &captureReservations{}
	q := &captureQueue{runNow: true}
	runner := NewRunner(q, nil, nil, nil, nil, reservations)
	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if reservations.calls != 1 {
		t.Fatalf("expected independent reservation cleanup, got %d calls", reservations.calls)
	}
}
