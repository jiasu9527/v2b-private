package nodeapi

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"testing"

	"forest/go-api/internal/config"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestDBServiceRoutesPreservesRequestedOrder(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	service := &DBService{db: db}
	rows := sqlmock.NewRows([]string{"id", "match", "action", "action_value"}).
		AddRow(int64(1), `["a.example"]`, "route", nil).
		AddRow(int64(2), `["b.example"]`, "route", nil).
		AddRow(int64(3), `["c.example"]`, "route", nil)
	mock.ExpectQuery(`SELECT id, match, action, action_value\s+FROM v2_server_route\s+WHERE id IN \(\$1, \$2, \$3\)\s+ORDER BY id ASC`).
		WithArgs(int64(3), int64(1), int64(2)).
		WillReturnRows(rows)

	result, err := service.Routes(context.Background(), []int64{3, 1, 2})
	if err != nil {
		t.Fatalf("Routes() error = %v", err)
	}
	if len(result) != 3 {
		t.Fatalf("expected 3 routes, got %d", len(result))
	}

	want := []int64{3, 1, 2}
	for i, id := range want {
		if got := result[i]["id"]; got != id {
			t.Fatalf("route index %d id = %#v, want %d", i, got, id)
		}
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

type fakeRuntimeConfig struct {
	cfg config.Config
}

func (f fakeRuntimeConfig) CurrentConfig() config.Config {
	return f.cfg
}

type aliveStateMatcher struct {
	wantAliveIP int64
}

func (m aliveStateMatcher) Match(value driver.Value) bool {
	raw, ok := value.(string)
	if !ok {
		return false
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return false
	}
	return mapAnyInt64(payload["alive_ip"]) == m.wantAliveIP
}

func TestReportAliveUsesRuntimeDeviceLimitMode(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	service := NewDBService(config.Config{DeviceLimitMode: 0}, db, nil).
		WithRuntimeConfig(fakeRuntimeConfig{cfg: config.Config{DeviceLimitMode: 1}})

	mock.ExpectQuery(`SELECT v, expire_at FROM v2_runtime_kv WHERE k = \$1 LIMIT 1`).
		WithArgs("ALIVE_IP_USER_9").
		WillReturnRows(sqlmock.NewRows([]string{"v", "expire_at"}).AddRow(`{
			"v2node1": {"aliveips": ["1.1.1.1_ios"], "lastupdateAt": 4102444800},
			"alive_ip": 1
		}`, int64(0)))
	mock.ExpectExec(`INSERT INTO v2_runtime_kv \(k, v, expire_at, created_at, updated_at\)`).
		WithArgs("ALIVE_IP_USER_9", aliveStateMatcher{wantAliveIP: 1}, sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))

	err = service.ReportAlive(context.Background(), AliveReportRequest{
		NodeID:   2,
		NodeType: "v2node",
		Users: map[int64][]string{
			9: []string{"1.1.1.1_android"},
		},
	})
	if err != nil {
		t.Fatalf("ReportAlive() error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}
