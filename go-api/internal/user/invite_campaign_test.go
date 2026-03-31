package user

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"forest/go-api/internal/config"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestInviteCampaignFetchUsesLatestConfigFile(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sqlmock: %v", err)
	}
	defer db.Close()

	root := t.TempDir()
	goDir := filepath.Join(root, "go-api")
	configDir := filepath.Join(root, "config")
	if err := os.MkdirAll(goDir, 0o755); err != nil {
		t.Fatalf("mkdir go dir: %v", err)
	}
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}

	writeConfig := func(values map[string]any) {
		raw, err := json.Marshal(values)
		if err != nil {
			t.Fatalf("marshal config: %v", err)
		}
		if err := os.WriteFile(filepath.Join(configDir, "admin.json"), raw, 0o644); err != nil {
			t.Fatalf("write admin.json: %v", err)
		}
	}

	writeConfig(map[string]any{
		"invite_campaign_enable":              0,
		"invite_campaign_reward_amount":       1000,
		"invite_campaign_expire_hours":        48,
		"invite_campaign_try_out_plan_id":     0,
		"invite_campaign_try_out_transfer_gb": 0,
		"invite_campaign_try_out_hours":       0,
	})

	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	defer func() {
		_ = os.Chdir(prevWD)
	}()
	if err := os.Chdir(goDir); err != nil {
		t.Fatalf("chdir go dir: %v", err)
	}

	cfg := config.Load()
	service := NewDBService(cfg, db)

	writeConfig(map[string]any{
		"invite_campaign_enable":              1,
		"invite_campaign_reward_amount":       2300,
		"invite_campaign_expire_hours":        72,
		"invite_campaign_try_out_plan_id":     9,
		"invite_campaign_try_out_transfer_gb": 12.5,
		"invite_campaign_try_out_hours":       36,
	})

	mock.ExpectQuery(`SELECT id, user_id, plan_id, period, invite_code_id, invite_code, reward_amount, target_amount, current_amount, invite_count, status, started_at, expired_at, completed_at, abandoned_at, used_at, created_at, updated_at`).
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "user_id", "plan_id", "period", "invite_code_id", "invite_code",
			"reward_amount", "target_amount", "current_amount", "invite_count", "status",
			"started_at", "expired_at", "completed_at", "abandoned_at", "used_at", "created_at", "updated_at",
		}))

	payload, err := service.InviteCampaign(context.Background(), 7)
	if err != nil {
		t.Fatalf("invite campaign: %v", err)
	}

	if enabled, _ := payload["enabled"].(bool); !enabled {
		t.Fatalf("expected latest invite campaign config to be enabled, got %#v", payload["enabled"])
	}

	settings, ok := payload["settings"].(map[string]any)
	if !ok {
		t.Fatalf("expected settings map, got %#v", payload["settings"])
	}
	if settings["reward_amount"] != int64(2300) {
		t.Fatalf("expected reward amount 2300, got %#v", settings["reward_amount"])
	}
	if settings["expire_hours"] != int64(72) {
		t.Fatalf("expected expire hours 72, got %#v", settings["expire_hours"])
	}
	if settings["invitee_try_out_plan_id"] != int64(9) {
		t.Fatalf("expected try out plan 9, got %#v", settings["invitee_try_out_plan_id"])
	}
	if settings["invitee_try_out_transfer_gb"] != 12.5 {
		t.Fatalf("expected try out transfer 12.5, got %#v", settings["invitee_try_out_transfer_gb"])
	}
	if settings["invitee_try_out_hours"] != 36.0 {
		t.Fatalf("expected try out hours 36, got %#v", settings["invitee_try_out_hours"])
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestInviteCampaignTrimsPaddedInviteCode(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sqlmock: %v", err)
	}
	defer db.Close()

	root := t.TempDir()
	goDir := filepath.Join(root, "go-api")
	configDir := filepath.Join(root, "config")
	if err := os.MkdirAll(goDir, 0o755); err != nil {
		t.Fatalf("mkdir go dir: %v", err)
	}
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "admin.json"), []byte(`{"invite_campaign_enable":1}`), 0o644); err != nil {
		t.Fatalf("write admin.json: %v", err)
	}

	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	defer func() {
		_ = os.Chdir(prevWD)
	}()
	if err := os.Chdir(goDir); err != nil {
		t.Fatalf("chdir go dir: %v", err)
	}

	mock.ExpectQuery(`SELECT id, user_id, plan_id, period, invite_code_id, invite_code, reward_amount, target_amount, current_amount, invite_count, status, started_at, expired_at, completed_at, abandoned_at, used_at, created_at, updated_at`).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "user_id", "plan_id", "period", "invite_code_id", "invite_code",
			"reward_amount", "target_amount", "current_amount", "invite_count", "status",
			"started_at", "expired_at", "completed_at", "abandoned_at", "used_at", "created_at", "updated_at",
		}).AddRow(
			int64(18),
			int64(9),
			int64(0),
			"month_price",
			nil,
			"ABCD1234                        ",
			int64(1000),
			int64(2000),
			int64(500),
			int64(1),
			int64(0),
			int64(100),
			int64(4102444800),
			nil,
			nil,
			nil,
			int64(100),
			int64(100),
		))
	mock.ExpectQuery(`SELECT \* FROM v2_plan WHERE id = \$1 LIMIT 1`).
		WithArgs(int64(0)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	service := NewDBService(config.Load(), db)
	payload, err := service.InviteCampaign(context.Background(), 9)
	if err != nil {
		t.Fatalf("invite campaign: %v", err)
	}

	data, ok := payload["data"].(map[string]any)
	if !ok {
		t.Fatalf("expected data map, got %#v", payload["data"])
	}
	if data["invite_code"] != "ABCD1234" {
		t.Fatalf("expected trimmed invite_code, got %#v", data["invite_code"])
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}
