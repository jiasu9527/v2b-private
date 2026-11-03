package admin

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestStrictOrderTimeFiltersAreANDAndEchoable(t *testing.T) {
	filters := []OrderFilter{{Key: " invite_user_id ", Condition: "=", Value: "061624"}, {Key: "created_at", Condition: "<", Value: "1790812800"}, {Key: "paid_at", Condition: ">=", Value: "1700000000"}}
	normalized, err := NormalizeOrderFilters(filters)
	if err != nil {
		t.Fatal(err)
	}
	if normalized[0].Value != "61624" {
		t.Fatal(normalized)
	}
	clause, args, err := buildOrderWhere(OrderFetchRequest{Filters: filters})
	if err != nil || clause != " WHERE o.invite_user_id = $1 AND o.created_at < $2 AND o.paid_at >= $3" || !reflect.DeepEqual(args, []any{"61624", "1790812800", "1700000000"}) {
		t.Fatal(clause, args, err)
	}
}

func TestStrictUserTimeFiltersAndInvalidInputs(t *testing.T) {
	s := &DBService{}
	clause, args, err := s.buildUserWhere(context.Background(), []UserFilter{{Key: "created_at", Condition: "<", Value: "1790812800"}, {Key: "updated_at", Condition: ">=", Value: "1700000000"}})
	if err != nil || clause != " WHERE u.created_at < $1 AND u.updated_at >= $2" || !reflect.DeepEqual(args, []any{int64(1790812800), int64(1700000000)}) {
		t.Fatal(clause, args, err)
	}
	for _, f := range []UserFilter{{Key: "", Condition: "=", Value: "1"}, {Key: "not_real", Condition: "=", Value: "1"}, {Key: "created_at", Condition: "<", Value: "abc"}, {Key: "created_at", Condition: "<", Value: "1790812800000"}, {Key: "created_at", Condition: "LIKE", Value: "1"}, {Key: "transfer_enable", Condition: ">", Value: "NaN"}, {Key: "transfer_enable", Condition: ">", Value: "oops"}, {Key: "plan_id", Condition: "<", Value: "null"}} {
		clause, args, err := s.buildUserWhere(context.Background(), []UserFilter{f})
		if !errors.Is(err, ErrInvalidFilter) || clause != "" || args != nil {
			t.Fatalf("invalid filter weakened to all users: %+v %q %#v %v", f, clause, args, err)
		}
	}
	for _, f := range []OrderFilter{{Key: "created_at", Condition: "<", Value: "1790812800000"}, {Key: "created_at", Condition: "<", Value: "NaN"}, {Key: "", Condition: "=", Value: "1"}} {
		if _, err := NormalizeOrderFilters([]OrderFilter{f}); !errors.Is(err, ErrInvalidFilter) {
			t.Fatal(f, err)
		}
	}
}

func TestInvalidFiltersCannotReachUserOrOrderDatabase(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := &DBService{db: db}
	if _, err := s.FetchOrders(context.Background(), OrderFetchRequest{Filters: []OrderFilter{{Key: "unknown", Condition: "=", Value: "1"}}}); !errors.Is(err, ErrInvalidFilter) {
		t.Fatal(err)
	}
	bad := []UserFilter{{Key: "unknown", Condition: "=", Value: "1"}}
	if _, err := s.ListUsers(context.Background(), UserFetchRequest{Filters: bad}); !errors.Is(err, ErrInvalidFilter) {
		t.Fatal(err)
	}
	if _, err := s.DeleteUsers(context.Background(), bad); !errors.Is(err, ErrInvalidFilter) {
		t.Fatal(err)
	}
	if _, err := s.BanUsers(context.Background(), bad); !errors.Is(err, ErrInvalidFilter) {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
