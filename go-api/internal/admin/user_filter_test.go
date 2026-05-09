package admin

import (
	"strings"
	"testing"
)

func TestBuildUserWhereSupportsInviteCodeFilter(t *testing.T) {
	service := &DBService{}

	whereClause, args, err := service.buildUserWhere(t.Context(), []UserFilter{
		{Key: "invite_code", Condition: "模糊", Value: "INV123"},
	})
	if err != nil {
		t.Fatalf("build user where: %v", err)
	}

	if !strings.Contains(whereClause, "WHERE EXISTS (") {
		t.Fatalf("expected invite exists wrapper, got %s", whereClause)
	}
	if !strings.Contains(whereClause, "FROM v2_invite_code ic") {
		t.Fatalf("expected invite code subquery, got %s", whereClause)
	}
	if !strings.Contains(whereClause, "ic.user_id = u.id") {
		t.Fatalf("expected invite code user match, got %s", whereClause)
	}
	if !strings.Contains(whereClause, "ic.code ILIKE $1") {
		t.Fatalf("expected invite code ilike predicate, got %s", whereClause)
	}
	if len(args) != 1 || args[0] != "%INV123%" {
		t.Fatalf("expected invite code filter args, got %#v", args)
	}
}

func TestBuildUserWhereSupportsLastOnlineFilter(t *testing.T) {
	service := &DBService{}

	whereClause, args, err := service.buildUserWhere(t.Context(), []UserFilter{
		{Key: "t", Condition: ">=", Value: "1710000000"},
	})
	if err != nil {
		t.Fatalf("build user where: %v", err)
	}

	if !strings.Contains(whereClause, "u.t >= $1") {
		t.Fatalf("expected last online predicate, got %s", whereClause)
	}
	if len(args) != 1 || args[0] != int64(1710000000) {
		t.Fatalf("expected last online args, got %#v", args)
	}
}

func TestBuildUserWhereAcceptsLastLoginAtAsLastOnlineAlias(t *testing.T) {
	service := &DBService{}

	whereClause, args, err := service.buildUserWhere(t.Context(), []UserFilter{
		{Key: "last_login_at", Condition: ">=", Value: "1710000000"},
	})
	if err != nil {
		t.Fatalf("build user where: %v", err)
	}

	if !strings.Contains(whereClause, "u.t >= $1") {
		t.Fatalf("expected last online alias predicate, got %s", whereClause)
	}
	if len(args) != 1 || args[0] != int64(1710000000) {
		t.Fatalf("expected last online alias args, got %#v", args)
	}
}

func TestBuildUserWhereSupportsIsStaffFilter(t *testing.T) {
	service := &DBService{}

	whereClause, args, err := service.buildUserWhere(t.Context(), []UserFilter{
		{Key: "is_staff", Condition: "=", Value: "0"},
	})
	if err != nil {
		t.Fatalf("build user where: %v", err)
	}

	if !strings.Contains(whereClause, "u.is_staff = $1") {
		t.Fatalf("expected staff predicate, got %s", whereClause)
	}
	if len(args) != 1 || args[0] != int64(0) {
		t.Fatalf("expected staff args, got %#v", args)
	}
}
