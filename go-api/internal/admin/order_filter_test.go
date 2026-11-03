package admin

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestBuildOrderWhereUserIDComparisonsRemainNumericAndParameterized(t *testing.T) {
	for _, operator := range []string{"=", ">", "<"} {
		for _, value := range []string{"0", "1", "2147483647"} {
			t.Run(operator+"_"+value, func(t *testing.T) {
				where, args, err := buildOrderWhere(OrderFetchRequest{Filters: []OrderFilter{
					{Key: " user_id ", Condition: " " + operator + " ", Value: " " + value + " "},
				}})
				if err != nil {
					t.Fatal(err)
				}
				// Compare the integer column directly, not a textual/LIKE cast.
				if want := " WHERE o.user_id " + operator + " $1"; where != want {
					t.Fatalf("where=%q, want %q", where, want)
				}
				if !reflect.DeepEqual(args, []any{value}) {
					t.Fatalf("args=%#v, want bound value %q", args, value)
				}
			})
		}
	}
}

func TestBuildOrderWhereUserIDRangeCombinesWithStatus(t *testing.T) {
	where, args, err := buildOrderWhere(OrderFetchRequest{Filters: []OrderFilter{
		{Key: "user_id", Condition: ">", Value: "100"},
		{Key: "user_id", Condition: "<", Value: "200"},
		{Key: "status", Condition: "=", Value: "3"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if want := " WHERE o.user_id > $1 AND o.user_id < $2 AND o.status = $3"; where != want {
		t.Fatalf("where=%q, want %q", where, want)
	}
	if !reflect.DeepEqual(args, []any{"100", "200", "3"}) {
		t.Fatalf("range arguments out of order: %#v", args)
	}
}

func TestBuildOrderWhereUserIDRejectsInjectedOperatorAndValue(t *testing.T) {
	for _, f := range []OrderFilter{{Key: "user_id", Condition: "> 0 OR TRUE --", Value: "100"}, {Key: "user_id", Condition: "", Value: "100"}, {Key: "user_id", Condition: ">", Value: "100 OR 1=1 --"}, {Key: "unknown", Condition: "=", Value: "1"}} {
		where, args, err := buildOrderWhere(OrderFetchRequest{Filters: []OrderFilter{f, {Key: "status", Condition: "=", Value: "3"}}})
		if !errors.Is(err, ErrInvalidFilter) || where != "" || args != nil {
			t.Fatalf("invalid filter silently weakened query: %q %#v %v", where, args, err)
		}
	}
}

func TestFetchOrdersUserIDComparisonAppliesToCountAndPage(t *testing.T) {
	for _, operator := range []string{"=", ">", "<"} {
		t.Run(operator, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()

			mock.ExpectBegin()
			where := " WHERE o.user_id " + operator + " $1"
			mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM v2_order o" + where)).
				WithArgs("100").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(25))
			query := fmt.Sprintf(`SELECT COALESCE(json_agg(row_to_json(t)), '[]'::json)
FROM (
	SELECT o.*, p.name AS plan_name
	FROM v2_order o
	LEFT JOIN v2_plan p ON p.id = o.plan_id
	%s
	ORDER BY o.created_at DESC, o.id DESC
	LIMIT $2 OFFSET $3
) AS t`, where)
			mock.ExpectQuery(regexp.QuoteMeta(query)).WithArgs("100", int64(10), int64(20)).
				WillReturnRows(sqlmock.NewRows([]string{"coalesce"}).AddRow([]byte(`[]`)))

			mock.ExpectCommit()
			result, err := (&DBService{db: db}).FetchOrders(context.Background(), OrderFetchRequest{
				Current: 3, PageSize: 10,
				Filters: []OrderFilter{{Key: "user_id", Condition: operator, Value: "100"}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.Total != 25 || result.Data == nil || len(result.Data) != 0 {
				t.Fatalf("unexpected filtered page: %+v", result)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
