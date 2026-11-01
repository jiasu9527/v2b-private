package entryevidence

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestDeliveryStatisticsFilterValidation(t *testing.T) {
	store := NewStore(nil)
	for _, f := range []Filter{
		{Limit: -1}, {Limit: 101}, {Offset: -1}, {Offset: 10001}, {From: 12, To: 12},
		{ProviderID: -1}, {CollectionID: -1}, {ServerType: "raw-secret"}, {Transport: "unknown"},
		{RuleRevision: "invalid"}, {Version: strings.Repeat("A", 32)}, {Version: "invalid"},
		{Host: "vless://secret@example.test"}, {Port: "password=secret"},
	} {
		if _, err := store.Statistics(context.Background(), f); !errors.Is(err, ErrValidation) {
			t.Fatal(f, err)
		}
		if _, err := store.Users(context.Background(), f); !errors.Is(err, ErrValidation) {
			t.Fatal(f, err)
		}
		if _, err := store.Versions(context.Background(), f); !errors.Is(err, ErrValidation) {
			t.Fatal(f, err)
		}
		if _, err := store.List(context.Background(), f); !errors.Is(err, ErrValidation) {
			t.Fatal(f, err)
		}
	}
	for _, f := range []Filter{{ServerID: -7001}, {Limit: 100, Offset: 10000}, {RuleRevision: Revision("valid")}, {Version: strings.Repeat("a", 32)}} {
		if _, err := buildDeliveryFilter(f, true); err != nil {
			t.Fatal("valid filter rejected", f, err)
		}
	}
}

func TestDeliveryStatisticsRequiresCompletedRequestAndSameNode(t *testing.T) {
	f, err := buildDeliveryFilter(Filter{PolicyID: 1, GroupID: 2, CollectionID: 3, ProviderID: 4, ServerID: -5, ServerType: "vless", Host: "example.test", Port: "54101", UA: "Forest", Transport: "subscription", From: 10, To: 20, Version: strings.Repeat("a", 32)}, true)
	if err != nil {
		t.Fatal(err)
	}
	clause := f.requestClause()
	if strings.Count(clause, "EXISTS(") != 1 || strings.Contains(clause, " JOIN ") {
		t.Fatal(clause)
	}
	for _, part := range []string{"r.response_complete=TRUE", "r.coverage='complete'", "r.status BETWEEN 200 AND 299", "n.collection_id=", "n.provider_id=", "n.server_id=", "n.server_type=", "n.group_id=", "n.policy_id=", "n.source IN", "r.transport=", "r.completed_at <", "md5(jsonb_build_array("} {
		if !strings.Contains(clause, part) {
			t.Fatal("missing exact condition", part, clause)
		}
	}
}

func TestDeliveryStatisticsPagesRollbackOnReadFailure(t *testing.T) {
	for _, versions := range []bool{false, true} {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		store := NewStore(db)
		mock.ExpectBegin()
		mock.ExpectQuery("SELECT COUNT").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
		mock.ExpectQuery("WITH matched|SELECT md5").WillReturnError(errors.New("fixture read failure"))
		mock.ExpectRollback()
		if versions {
			_, err = store.Versions(context.Background(), Filter{})
		} else {
			_, err = store.Users(context.Background(), Filter{})
		}
		if err == nil {
			t.Fatal("page failure hidden")
		}
		if err = mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
		db.Close()
	}
}
