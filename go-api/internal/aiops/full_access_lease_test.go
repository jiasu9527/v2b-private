package aiops

import (
	"context"
	"errors"
	"testing"
	"time"

	"forest/go-api/internal/config"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestFullAccessLeaseRenewalFailsClosed(t *testing.T) {
	for _, name := range []string{"no row", "db error", "query timeout", "rowcount error", "lock busy", "success"} {
		t.Run(name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			s := NewService(config.Config{}, db, nil)
			defer s.Close()
			expect := mock.ExpectExec(`WITH locked_turn AS MATERIALIZED`).WithArgs(int64(3), int64(2), int64(1), int64(120))
			switch name {
			case "no row":
				expect.WillReturnResult(sqlmock.NewResult(0, 0))
			case "db error":
				expect.WillReturnError(errors.New("fixture database down"))
			case "query timeout":
				expect.WillReturnError(&pgconn.PgError{Code: "57014", Message: "query canceled"})
			case "lock busy":
				expect.WillReturnError(&pgconn.PgError{Code: "55P03", Message: "row locked"})
			case "rowcount error":
				expect.WillReturnResult(sqlmock.NewErrorResult(errors.New("fixture rows unavailable")))
			case "success":
				expect.WillReturnResult(sqlmock.NewResult(0, 1))
			}
			err = s.renewFullAccessTurn(context.Background(), 1, 2, 3)
			if (err == nil) != (name == "success") {
				t.Fatal("wrong lease decision", err)
			}
			if errors.Is(err, ErrBusy) != (name == "lock busy") {
				t.Fatal("infrastructure error mistaken for harmless lock competition", err)
			}
			if err = mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFullAccessLeaseWatchCancelsOnDatabaseFailure(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := NewService(config.Config{}, db, nil)
	defer s.Close()
	mock.ExpectExec(`WITH locked_turn AS MATERIALIZED`).WillReturnError(errors.New("fixture database down"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan struct{})
	go func() { s.watchTurn(ctx, cancel, 1, 2, 3, true); close(finished) }()
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("watch did not stop on failed renewal")
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("lease failure did not cancel worker")
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
