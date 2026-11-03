package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/config"
	"forest/go-api/internal/session"
)

func TestAdminCommissionFiltersPostgresActualHTTP(t *testing.T) {
	dsn := os.Getenv("FOREST_COMMISSION_TEST_DSN")
	if dsn == "" {
		t.Skip("requires disposable FOREST_COMMISSION_TEST_DSN")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	marker := fmt.Sprint(time.Now().UnixNano())
	ids := []int64{}
	for i, created := range []int64{1700000000, 1793630000} {
		var id int64
		err = db.QueryRowContext(ctx, `INSERT INTO v2_user(email,password,uuid,token,created_at,updated_at) VALUES($1,'fixture',$2::TEXT,$2::TEXT,$3,$3) RETURNING id`, fmt.Sprintf("filter-%d-%s@example.test", i, marker), fmt.Sprintf("f%d%s", i, marker), created).Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		defer db.Exec(`DELETE FROM v2_user WHERE id=$1`, id)
	}
	inviter := ids[0]
	for i, created := range []int64{1700000000, 1793630000} {
		trade := fmt.Sprintf("f-%d-%s", i, marker)
		if _, err = db.ExecContext(ctx, `INSERT INTO v2_order(user_id,invite_user_id,plan_id,type,period,trade_no,total_amount,status,commission_status,created_at,updated_at) VALUES($1,$2,1,1,'month_price',$3,1000,3,2,$4,$4)`, ids[i], inviter, trade, created); err != nil {
			t.Fatal(err)
		}
		defer db.Exec(`DELETE FROM v2_order WHERE trade_no=$1`, trade)
	}
	service := admin.NewDBService(config.Config{}, db)
	ss := strictAppleIDSession{identity: &session.Identity{ID: 1, IsAdmin: 1}}
	orderFilters := fmt.Sprintf(`[{"key":"invite_user_id","condition":"=","value":"%d"},{"key":"created_at","condition":"<","value":"1790812800"}]`, inviter)
	userFilters := fmt.Sprintf(`[{"key":"id","condition":">=","value":"%d"},{"key":"id","condition":"<=","value":"%d"},{"key":"created_at","condition":"<","value":"1790812800"}]`, ids[0], ids[1])
	for _, tc := range []struct{ name, filters string }{{"orders", orderFilters}, {"users", userFilters}} {
		req := httptest.NewRequest(http.MethodGet, "/?filter="+url.QueryEscape(tc.filters), nil).WithContext(ctx)
		req.Header.Set("Authorization", "fixture")
		rec := httptest.NewRecorder()
		if tc.name == "orders" {
			handleAdminOrderFetch(rec, req, ss, service)
		} else {
			handleAdminUserFetch(rec, req, ss, service)
		}
		if rec.Code != 200 {
			t.Fatal(tc.name, rec.Code, rec.Body.String())
		}
		var result struct {
			Total   int64            `json:"total"`
			Data    []map[string]any `json:"data"`
			Applied []any            `json:"applied_filters"`
		}
		if err = json.Unmarshal(rec.Body.Bytes(), &result); err != nil || result.Total != 1 || len(result.Data) != 1 || len(result.Applied) < 2 {
			t.Fatal(tc.name, result, err)
		}
		if result.Data[0]["created_at"] != float64(1700000000) {
			t.Fatal("time comparison not applied", tc.name, result)
		}
	}
	// Malformed user deletion cannot broaden to all users.
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"filter":[{"key":"created_at_typo","condition":"<","value":1790812800}]}`)).WithContext(ctx)
	req.Header.Set("Authorization", "fixture")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handleAdminUserAllDel(rec, req, ss, service)
	if rec.Code != 400 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	var count int
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM v2_user WHERE id IN($1,$2)`, ids[0], ids[1]).Scan(&count); err != nil || count != 2 {
		t.Fatal("invalid filter mutated users", count, err)
	}
}
