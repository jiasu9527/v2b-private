package admin

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestGetOrderDetailClearsMissingInviteUserReference(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	svc := &DBService{db: db}

	mock.ExpectQuery(`SELECT row_to_json\(t\)`).
		WithArgs(int64(8)).
		WillReturnRows(sqlmock.NewRows([]string{"row_to_json"}).AddRow(`{"id":8,"trade_no":"T301","user_id":5,"invite_user_id":9,"status":3,"created_at":1,"updated_at":1}`))
	mock.ExpectQuery(`SELECT id, email FROM v2_user WHERE id = \$1 LIMIT 1`).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "email"}))
	mock.ExpectQuery(`SELECT COALESCE\(json_agg\(row_to_json\(t\)\), '\[\]'::json\)`).
		WithArgs("T301").
		WillReturnRows(sqlmock.NewRows([]string{"coalesce"}).AddRow([]byte(`[]`)))

	detail, err := svc.GetOrderDetail(context.Background(), 8)
	if err != nil {
		t.Fatalf("get order detail: %v", err)
	}
	if inviteUserID, ok := detail["invite_user_id"]; !ok || inviteUserID != nil {
		t.Fatalf("expected missing invite_user_id to be normalized to nil, got %#v", detail["invite_user_id"])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestBuildOrderWhereSupportsUserIDFilter(t *testing.T) {
	whereClause, args := buildOrderWhere(OrderFetchRequest{Filters: []OrderFilter{
		{Key: "user_id", Condition: "=", Value: "12"},
	}})

	if whereClause != " WHERE o.user_id = $1" {
		t.Fatalf("unexpected order where clause: %s", whereClause)
	}
	if len(args) != 1 || args[0] != "12" {
		t.Fatalf("unexpected order where args: %#v", args)
	}
}
