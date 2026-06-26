package user

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"forest/go-api/internal/config"
)

func TestPlansExposeTransferEnableAsGBForFrontend(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(`SELECT \* FROM v2_plan WHERE "show" = 1 ORDER BY sort ASC NULLS LAST, id ASC`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "transfer_enable", "show"}).
			AddRow(int64(1), "轻量", int64(214748364800), int64(1)).
			AddRow(int64(2), "Legacy", int64(50), int64(1)))
	mock.ExpectQuery(`SELECT plan_id, COUNT\(\*\) AS count`).
		WillReturnRows(sqlmock.NewRows([]string{"plan_id", "count"}))

	service := NewDBService(config.Config{}, db)
	payload, err := service.Plans(context.Background(), 9, nil)
	if err != nil {
		t.Fatalf("plans: %v", err)
	}
	plans, ok := payload.([]map[string]any)
	if !ok {
		t.Fatalf("expected plan slice, got %#v", payload)
	}
	if plans[0]["transfer_enable"] != int64(200) {
		t.Fatalf("expected byte value converted to 200GB, got %#v", plans[0]["transfer_enable"])
	}
	if plans[1]["transfer_enable"] != int64(50) {
		t.Fatalf("expected legacy GB value preserved, got %#v", plans[1]["transfer_enable"])
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestPlanDetailExposesTransferEnableAsGBForFrontend(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(`SELECT \* FROM v2_plan WHERE id = \$1 LIMIT 1`).
		WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "transfer_enable", "show", "renew"}).
			AddRow(int64(1), "轻量", int64(214748364800), int64(1), int64(1)))
	mock.ExpectQuery(`SELECT plan_id FROM v2_user WHERE id = \$1 LIMIT 1`).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"plan_id"}).AddRow(nil))

	service := NewDBService(config.Config{}, db)
	planID := int64(1)
	payload, err := service.Plans(context.Background(), 9, &planID)
	if err != nil {
		t.Fatalf("plan detail: %v", err)
	}
	plan, ok := payload.(map[string]any)
	if !ok {
		t.Fatalf("expected plan map, got %#v", payload)
	}
	if plan["transfer_enable"] != int64(200) {
		t.Fatalf("expected byte value converted to 200GB, got %#v", plan["transfer_enable"])
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}
func TestSubscribeUsesRuntimeAllowNewPeriod(t *testing.T) {
	root, restoreWD := prepareRuntimeConfigFixture(t, map[string]any{
		"allow_new_period": 0,
	})
	defer restoreWD()

	cfg := config.Load()
	runtimeState := config.NewRuntimeState(cfg)

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	service := NewDBService(cfg, db).WithRuntimeConfig(runtimeState)
	userRows := sqlmock.NewRows([]string{
		"plan_id", "token", "expired_at", "u", "d", "transfer_enable", "device_limit", "email", "uuid",
	}).AddRow(nil, "token-1", nil, int64(0), int64(0), int64(1024), nil, "user@example.com", "uuid-1")

	mock.ExpectQuery(`SELECT plan_id, token, expired_at, u, d, transfer_enable, device_limit, email, uuid`).
		WithArgs(int64(1)).
		WillReturnRows(userRows)
	mock.ExpectQuery(`SELECT v, expire_at FROM v2_runtime_kv WHERE k = \$1 LIMIT 1`).
		WithArgs("ALIVE_IP_USER_1").
		WillReturnError(sql.ErrNoRows)

	subscribe, err := service.Subscribe(context.Background(), 1)
	if err != nil {
		t.Fatalf("subscribe before reload: %v", err)
	}
	if subscribe.AllowNewPeriod != "0" {
		t.Fatalf("expected allow_new_period=\"0\" before reload, got %q", subscribe.AllowNewPeriod)
	}

	writeRuntimeAdminJSON(t, root, map[string]any{
		"allow_new_period": 1,
	})
	runtimeState.Reload()

	userRows = sqlmock.NewRows([]string{
		"plan_id", "token", "expired_at", "u", "d", "transfer_enable", "device_limit", "email", "uuid",
	}).AddRow(nil, "token-1", nil, int64(0), int64(0), int64(1024), nil, "user@example.com", "uuid-1")

	mock.ExpectQuery(`SELECT plan_id, token, expired_at, u, d, transfer_enable, device_limit, email, uuid`).
		WithArgs(int64(1)).
		WillReturnRows(userRows)
	mock.ExpectQuery(`SELECT v, expire_at FROM v2_runtime_kv WHERE k = \$1 LIMIT 1`).
		WithArgs("ALIVE_IP_USER_1").
		WillReturnError(sql.ErrNoRows)

	subscribe, err = service.Subscribe(context.Background(), 1)
	if err != nil {
		t.Fatalf("subscribe after reload: %v", err)
	}
	if subscribe.AllowNewPeriod != "1" {
		t.Fatalf("expected allow_new_period=\"1\" after reload, got %q", subscribe.AllowNewPeriod)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestSubscribeTreatsZeroExpiredAtAsUnlimited(t *testing.T) {
	root, restoreWD := prepareRuntimeConfigFixture(t, map[string]any{
		"allow_new_period": 1,
	})
	defer restoreWD()

	cfg := config.Load()
	runtimeState := config.NewRuntimeState(cfg)

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	service := NewDBService(cfg, db).WithRuntimeConfig(runtimeState)
	userRows := sqlmock.NewRows([]string{
		"plan_id", "token", "expired_at", "u", "d", "transfer_enable", "device_limit", "email", "uuid",
	}).AddRow(nil, "token-1", int64(0), int64(0), int64(0), int64(1024), nil, "user@example.com", "uuid-1")

	mock.ExpectQuery(`SELECT plan_id, token, expired_at, u, d, transfer_enable, device_limit, email, uuid`).
		WithArgs(int64(2)).
		WillReturnRows(userRows)
	mock.ExpectQuery(`SELECT v, expire_at FROM v2_runtime_kv WHERE k = \$1 LIMIT 1`).
		WithArgs("ALIVE_IP_USER_2").
		WillReturnError(sql.ErrNoRows)

	subscribe, err := service.Subscribe(context.Background(), 2)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if subscribe.ExpiredAt != nil {
		t.Fatalf("expected zero expired_at to become nil, got %#v", subscribe.ExpiredAt)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}

	_ = root
}

func TestCalculateResetPeriodForPlanUsesRuntimeResetTrafficMethod(t *testing.T) {
	root, restoreWD := prepareRuntimeConfigFixture(t, map[string]any{
		"reset_traffic_method": 0,
	})
	defer restoreWD()

	cfg := config.Load()
	runtimeState := config.NewRuntimeState(cfg)
	service := NewDBService(cfg, nil).WithRuntimeConfig(runtimeState)
	expiredAt := sql.NullInt64{Int64: time.Now().Add(30 * 24 * time.Hour).Unix(), Valid: true}

	period := service.calculateResetPeriodForPlan(planRecord{}, expiredAt)
	if period == nil || *period != 1 {
		t.Fatalf("expected reset period 1 before reload, got %#v", period)
	}

	writeRuntimeAdminJSON(t, root, map[string]any{
		"reset_traffic_method": 4,
	})
	runtimeState.Reload()

	period = service.calculateResetPeriodForPlan(planRecord{}, expiredAt)
	if period == nil || *period != 365 {
		t.Fatalf("expected reset period 365 after reload, got %#v", period)
	}
}

func prepareRuntimeConfigFixture(t *testing.T, values map[string]any) (string, func()) {
	t.Helper()

	root := t.TempDir()
	configRoot := filepath.Join(root, "config")
	workDir := filepath.Join(root, "go-api")
	if err := os.MkdirAll(configRoot, 0o755); err != nil {
		t.Fatalf("mkdir config root: %v", err)
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("mkdir work dir: %v", err)
	}
	writeRuntimeAdminJSON(t, root, values)

	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(workDir); err != nil {
		t.Fatalf("chdir work dir: %v", err)
	}

	restore := func() {
		_ = os.Chdir(prevWD)
	}
	return root, restore
}

func writeRuntimeAdminJSON(t *testing.T, root string, values map[string]any) {
	t.Helper()
	raw, err := json.MarshalIndent(values, "", "  ")
	if err != nil {
		t.Fatalf("marshal admin json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "config", "admin.json"), raw, 0o644); err != nil {
		t.Fatalf("write admin json: %v", err)
	}
}
