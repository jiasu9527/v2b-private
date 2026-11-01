package aiops

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestOperationRetryTransactionBoundaries(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{&pgconn.PgError{Code: "40001"}, true}, {&pgconn.PgError{Code: "40P01"}, true},
		{&actionApprovalLockBusy{}, true}, {ErrBusy, false},
		{&pgconn.PgError{Code: "55P03"}, false}, {&pgconn.PgError{Code: "23505"}, false},
		{io.EOF, false}, {context.DeadlineExceeded, false}, {ErrForbidden, false},
		{ErrConflict, false}, {ErrValidation, false}, {ErrUnavailable, false},
	} {
		if got := retryableApprovalTransaction(context.Background(), tc.err); got != tc.want {
			t.Fatalf("%T: retry=%v want=%v", tc.err, got, tc.want)
		}
	}
	for _, err := range []error{ErrBusy, ErrUnavailable, ErrConflict, ErrForbidden} {
		if transientOperationIO(context.Background(), err) {
			t.Fatal("business denial treated as transient I/O", err)
		}
	}
}

func TestOperationRetryBoundedAndCancelable(t *testing.T) {
	for _, failUntil := range []int{0, 1, 2, 10} {
		calls := 0
		err := retryOperation(context.Background(), transientOperationIO, 0, func(context.Context) error {
			calls++
			if calls <= failUntil {
				return io.EOF
			}
			return nil
		})
		want := min(failUntil+1, 3)
		if calls != want || (err == nil) != (failUntil < 3) {
			t.Fatal(calls, err, failUntil)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	err := retryOperation(ctx, transientOperationIO, time.Hour, func(context.Context) error {
		calls++
		cancel()
		return io.EOF
	})
	if calls != 1 || err == nil {
		t.Fatal(calls, err)
	}
}

func TestOperationRetryPermissionRead(t *testing.T) {
	for _, allowed := range []bool{true, false} {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		s := &Service{db: db}
		mock.ExpectQuery("SELECT EXISTS").WillReturnError(io.ErrUnexpectedEOF)
		mock.ExpectQuery("SELECT EXISTS").WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(allowed))
		err = s.requireFullAccess(context.Background(), 1, 2, 3)
		if allowed && err != nil || !allowed && !errors.Is(err, ErrForbidden) {
			t.Fatal(allowed, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
		db.Close()
	}
	// Explicit revocation never sleeps or makes a second permission query.
	db, mock, _ := sqlmock.New()
	defer db.Close()
	mock.ExpectQuery("SELECT EXISTS").WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	if err := (&Service{db: db}).requireFullAccess(context.Background(), 1, 2, 3); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestOperationRetryCompletionOnly(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := &Service{db: db}
	query := "UPDATE v2_ai_action SET status=.*WHERE id=.*AND status='running'"
	mock.ExpectExec(query).WithArgs(int64(19), "succeeded", "done", "", sqlmock.AnyArg(), nil).WillReturnError(io.EOF)
	// The first UPDATE may already have committed. Zero rows is a successful
	// reconciliation, not a reason to create or execute a replacement action.
	mock.ExpectExec(query).WithArgs(int64(19), "succeeded", "done", "", sqlmock.AnyArg(), nil).WillReturnResult(sqlmock.NewResult(0, 0))
	if err = s.persistActionCompletion(Action{ID: 19}, "succeeded", "done", "", nil); err != nil {
		t.Fatal(err)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
