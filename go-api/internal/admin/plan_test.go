package admin

import (
	"context"
	"testing"

	cfgpkg "forest/go-api/internal/config"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestSavePlanInsertStoresTransferEnableAsBytes(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	show := int64(1)
	renew := int64(0)
	mock.ExpectExec(`INSERT INTO v2_plan`).
		WithArgs(
			int64(2), int64(100)*planTrafficGiB, nil, "Pro", nil, show, renew, nil,
			nil, nil, nil, nil, nil, nil,
			nil, nil, nil, nil, sqlmock.AnyArg(), sqlmock.AnyArg(),
		).
		WillReturnResult(sqlmock.NewResult(1, 1))

	service := NewDBService(cfgpkg.Config{}, db)
	ok, err := service.SavePlan(context.Background(), PlanSaveRequest{
		Name:           "Pro",
		GroupID:        2,
		TransferEnable: 100,
		Show:           &show,
		Renew:          &renew,
	})
	if err != nil {
		t.Fatalf("save plan: %v", err)
	}
	if !ok {
		t.Fatalf("expected ok")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestSavePlanUpdateStoresTransferEnableAsBytes(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	id := int64(7)
	show := int64(1)
	renew := int64(0)
	mock.ExpectQuery(`SELECT 1 FROM v2_plan WHERE id = \$1 LIMIT 1`).
		WithArgs(id).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE v2_plan\s+SET group_id = \$2,`).
		WithArgs(
			id, int64(3), int64(200)*planTrafficGiB, nil, "Premium", nil, nil,
			nil, nil, nil, nil, nil, nil,
			nil, nil, nil, nil, show, renew, sqlmock.AnyArg(),
		).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	service := NewDBService(cfgpkg.Config{}, db)
	ok, err := service.SavePlan(context.Background(), PlanSaveRequest{
		ID:             &id,
		Name:           "Premium",
		GroupID:        3,
		TransferEnable: 200,
		Show:           &show,
		Renew:          &renew,
	})
	if err != nil {
		t.Fatalf("save plan: %v", err)
	}
	if !ok {
		t.Fatalf("expected ok")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}
