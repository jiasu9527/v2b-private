package admin

import (
	"strings"
	"testing"
)

func TestBuildUserWhereSupportsCouponCodeFilter(t *testing.T) {
	service := &DBService{}

	whereClause, args, err := service.buildUserWhere(t.Context(), []UserFilter{
		{Key: "coupon_code", Condition: "模糊", Value: "SPRING"},
	})
	if err != nil {
		t.Fatalf("build user where: %v", err)
	}

	if !strings.Contains(whereClause, "WHERE EXISTS (") {
		t.Fatalf("expected coupon exists wrapper, got %s", whereClause)
	}
	if !strings.Contains(whereClause, "FROM v2_order o") {
		t.Fatalf("expected coupon order subquery, got %s", whereClause)
	}
	if !strings.Contains(whereClause, "JOIN v2_coupon c ON c.id = o.coupon_id") {
		t.Fatalf("expected coupon exists clause, got %s", whereClause)
	}
	if !strings.Contains(whereClause, "o.user_id = u.id") {
		t.Fatalf("expected coupon filter to match user orders, got %s", whereClause)
	}
	if !strings.Contains(whereClause, "o.status NOT IN (0, 2)") {
		t.Fatalf("expected coupon filter to ignore invalid orders, got %s", whereClause)
	}
	if !strings.Contains(whereClause, "c.code ILIKE $1") {
		t.Fatalf("expected coupon code ilike predicate, got %s", whereClause)
	}
	if len(args) != 1 || args[0] != "%SPRING%" {
		t.Fatalf("expected coupon filter args, got %#v", args)
	}
}
