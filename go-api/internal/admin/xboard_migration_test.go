package admin

import (
	"context"
	"regexp"
	"testing"

	"forest/go-api/internal/config"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestScanXBoardMigrationClassifiesUsersBeforeWrite(t *testing.T) {
	sourceDB, sourceMock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer sourceDB.Close()
	targetDB, targetMock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer targetDB.Close()

	columns := []string{"id", "email", "password", "password_algo", "password_salt", "t", "u", "d", "transfer_enable", "device_limit", "banned", "plan_id", "speed_limit", "remind_expire", "remind_traffic", "expired_at", "remarks", "created_at", "updated_at", "is_admin", "is_staff"}
	rows := sqlmock.NewRows(columns).
		AddRow(1, "admin@example.com", "hash", nil, nil, 0, 0, 0, 100, nil, 0, 1, nil, 1, 1, nil, nil, 1, 1, 1, 0).
		AddRow(2, "no-plan@example.com", "hash", nil, nil, 0, 0, 0, 100, nil, 0, nil, nil, 1, 1, nil, nil, 1, 1, 0, 0).
		AddRow(3, "exists@example.com", "hash", nil, nil, 0, 0, 0, 100, nil, 0, 2, nil, 1, 1, nil, nil, 1, 1, 0, 0).
		AddRow(4, "ready@example.com", "hash", nil, nil, 0, 0, 0, 100, nil, 0, 3, nil, 1, 1, nil, nil, 1, 1, 0, 0).
		AddRow(5, "unknown@example.com", "hash", nil, nil, 0, 0, 0, 100, nil, 0, 10, nil, 1, 1, nil, nil, 1, 1, 0, 0)
	sourceMock.ExpectQuery(regexp.QuoteMeta("SELECT id,email,password,password_algo,password_salt,t,u,d,transfer_enable,device_limit,banned,plan_id,speed_limit,remind_expire,remind_traffic,expired_at,remarks,created_at,updated_at,is_admin,is_staff FROM v2_user ORDER BY id")).WillReturnRows(rows)

	targetMock.ExpectQuery(regexp.QuoteMeta("SELECT id,name,group_id,device_limit FROM v2_plan WHERE id IN ($1,$2,$3,$4)")).WithArgs(int64(1), int64(2), int64(3), int64(5)).WillReturnRows(
		sqlmock.NewRows([]string{"id", "name", "group_id", "device_limit"}).
			AddRow(1, "轻量", 1, nil).AddRow(2, "高级", 2, nil).AddRow(3, "旗舰", 3, nil).AddRow(5, "不限时200G", 5, nil),
	)
	targetMock.ExpectQuery(regexp.QuoteMeta("SELECT lower(trim(email)) FROM v2_user")).WillReturnRows(sqlmock.NewRows([]string{"email"}).AddRow("exists@example.com"))

	service := NewDBService(configForXBoardTest(), targetDB)
	scan, err := service.scanXBoardMigration(context.Background(), sourceDB)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if scan.Preview.SourceUsers != 5 || scan.Preview.Ready != 1 || scan.Preview.SkipAdmin != 1 || scan.Preview.SkipNoPlan != 1 || scan.Preview.SkipConflict != 1 || scan.Preview.SkipUnmapped != 1 {
		t.Fatalf("unexpected preview: %#v", scan.Preview)
	}
	if len(scan.Preview.PlanBreakdown) != 1 || scan.Preview.PlanBreakdown[0].SourcePlanID != 3 || scan.Preview.PlanBreakdown[0].TargetPlanID != 2 {
		t.Fatalf("unexpected mapping: %#v", scan.Preview.PlanBreakdown)
	}
	if scan.Fingerprint == "" {
		t.Fatal("expected fingerprint")
	}
	if err := sourceMock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	if err := targetMock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// Keep the test independent from production runtime settings.
func configForXBoardTest() config.Config { return config.Config{} }
