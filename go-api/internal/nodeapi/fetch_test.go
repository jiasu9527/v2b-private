package nodeapi

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestDBServiceRoutesPreservesRequestedOrder(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	service := &DBService{db: db}
	rows := sqlmock.NewRows([]string{"id", "match", "action", "action_value"}).
		AddRow(int64(1), `["a.example"]`, "route", nil).
		AddRow(int64(2), `["b.example"]`, "route", nil).
		AddRow(int64(3), `["c.example"]`, "route", nil)
	mock.ExpectQuery(`SELECT id, match, action, action_value\s+FROM v2_server_route\s+WHERE id IN \(\$1, \$2, \$3\)\s+ORDER BY id ASC`).
		WithArgs(int64(3), int64(1), int64(2)).
		WillReturnRows(rows)

	result, err := service.Routes(context.Background(), []int64{3, 1, 2})
	if err != nil {
		t.Fatalf("Routes() error = %v", err)
	}
	if len(result) != 3 {
		t.Fatalf("expected 3 routes, got %d", len(result))
	}

	want := []int64{3, 1, 2}
	for i, id := range want {
		if got := result[i]["id"]; got != id {
			t.Fatalf("route index %d id = %#v, want %d", i, got, id)
		}
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}
