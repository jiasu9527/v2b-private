package admin

import (
	"context"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestBuildOrderWhereUserIDComparisonsRemainNumericAndParameterized(t *testing.T) {
	for _, operator := range []string{"=", ">", "<"} {
		for _, value := range []string{"0", "1", "2147483647"} {
			t.Run(operator+"_"+value, func(t *testing.T) {
				where, args := buildOrderWhere(OrderFetchRequest{Filters: []OrderFilter{
					{Key: " user_id ", Condition: " " + operator + " ", Value: " " + value + " "},
				}})
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
	where, args := buildOrderWhere(OrderFetchRequest{Filters: []OrderFilter{
		{Key: "user_id", Condition: ">", Value: "100"},
		{Key: "user_id", Condition: "<", Value: "200"},
		{Key: "status", Condition: "=", Value: "3"},
	}})
	if want := " WHERE o.user_id > $1 AND o.user_id < $2 AND o.status = $3"; where != want {
		t.Fatalf("where=%q, want %q", where, want)
	}
	if !reflect.DeepEqual(args, []any{"100", "200", "3"}) {
		t.Fatalf("range arguments out of order: %#v", args)
	}
}

func TestBuildOrderWhereUserIDRejectsInjectedOperatorAndBindsValue(t *testing.T) {
	for _, operator := range []string{"> 0 OR TRUE --", "= $1; DROP TABLE v2_order; --", "LIKE", ""} {
		where, args := buildOrderWhere(OrderFetchRequest{Filters: []OrderFilter{
			{Key: "user_id", Condition: operator, Value: "100"},
			{Key: "status", Condition: "=", Value: "3"},
		}})
		if where != " WHERE o.status = $1" || !reflect.DeepEqual(args, []any{"3"}) {
			t.Fatalf("invalid operator %q affected query: %q %#v", operator, where, args)
		}
	}

	value := "100 OR 1=1 --"
	where, args := buildOrderWhere(OrderFetchRequest{Filters: []OrderFilter{{Key: "user_id", Condition: ">", Value: value}}})
	if where != " WHERE o.user_id > $1" || strings.Contains(where, value) || !reflect.DeepEqual(args, []any{value}) {
		t.Fatalf("filter value must stay outside SQL syntax: %q %#v", where, args)
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

			where := " WHERE o.user_id " + operator + " $1"
			mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM v2_order o" + where)).
				WithArgs("100").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(25))
			query := fmt.Sprintf(`SELECT COALESCE(json_agg(row_to_json(t)), '[]'::json)
FROM (
	SELECT o.*, p.name AS plan_name
	FROM v2_order o
	LEFT JOIN v2_plan p ON p.id = o.plan_id
	%s
	ORDER BY o.created_at DESC
	LIMIT $2 OFFSET $3
) AS t`, where)
			mock.ExpectQuery(regexp.QuoteMeta(query)).WithArgs("100", int64(10), int64(20)).
				WillReturnRows(sqlmock.NewRows([]string{"coalesce"}).AddRow([]byte(`[]`)))

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
