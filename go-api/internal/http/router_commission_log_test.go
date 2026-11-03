package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/aiops"
	"forest/go-api/internal/config"
	"forest/go-api/internal/session"
)

type commissionHTTPFixture struct {
	admin.Service
	calls   int
	actor   int64
	list    admin.CommissionLogListRequest
	preview admin.CommissionLogPreviewRequest
	apply   admin.CommissionLogApplyRequest
	err     error
}

func (f *commissionHTTPFixture) ListCommissionLogs(_ context.Context, req admin.CommissionLogListRequest) (admin.CommissionLogListResult, error) {
	f.calls++
	f.list = req
	return admin.CommissionLogListResult{Total: 1, TotalAmount: 123, AppliedFilters: req.Filter}, f.err
}

func (f *commissionHTTPFixture) PreviewCommissionLogs(_ context.Context, actor int64, req admin.CommissionLogPreviewRequest) (admin.CommissionLogPreviewResult, error) {
	f.calls++
	f.actor, f.preview = actor, req
	return admin.CommissionLogPreviewResult{PreviewHash: strings.Repeat("a", 64), Count: 1, TotalAmount: 123, Action: req.Action}, f.err
}

func (f *commissionHTTPFixture) ApplyCommissionLogs(_ context.Context, actor int64, req admin.CommissionLogApplyRequest) (admin.CommissionLogApplyResult, error) {
	f.calls++
	f.actor, f.apply = actor, req
	return admin.CommissionLogApplyResult{OperationID: 7, Count: 1, TotalAmount: 123, Action: req.ExpectedAction, Committed: true}, f.err
}

func commissionHTTPCall(service admin.Service, identity *session.Identity, action, raw, key string) *httptest.ResponseRecorder {
	router := NewRouter(config.Config{AdminPath: "control"}, func(state *routerState) {
		state.admin = service
		state.session = strictAppleIDSession{identity: identity}
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/control/commission-log/"+action, strings.NewReader(raw))
	req.Header.Set("Authorization", "fixture")
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestAdminCommissionLogHTTPDispatchAndActor(t *testing.T) {
	identity := &session.Identity{ID: 17655, IsAdmin: 1}
	f := &commissionHTTPFixture{}
	rec := commissionHTTPCall(f, identity, "list", `{"filter":{"invite_user_id":61624,"created_to":1790812800},"current":2,"page_size":10}`, "")
	if rec.Code != 200 || f.calls != 1 || f.list.Filter.InviteUserID == nil || *f.list.Filter.InviteUserID != 61624 || f.list.Filter.CreatedTo == nil || *f.list.Filter.CreatedTo != 1790812800 || f.list.Current != 2 || f.list.PageSize != 10 || !strings.Contains(rec.Body.String(), `"applied_filters"`) || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(rec.Code, rec.Body.String(), f.list)
	}
	rec = commissionHTTPCall(f, identity, "preview", `{"filter":{"id":19},"action":"delete","reason":"requested cleanup"}`, "")
	if rec.Code != 200 || f.actor != identity.ID || f.preview.Action != "delete" || f.preview.Filter.ID == nil || *f.preview.Filter.ID != 19 || !strings.Contains(rec.Body.String(), strings.Repeat("a", 64)) {
		t.Fatal(rec.Code, rec.Body.String(), f.preview, f.actor)
	}
	for _, action := range []string{"delete", "delete-many", "restore"} {
		rec = commissionHTTPCall(f, identity, action, `{"id":19,"preview_hash":"`+strings.Repeat("a", 64)+`","confirm":true}`, "stable-key")
		wantAction := "delete"
		if action == "restore" {
			wantAction = "restore"
		}
		if rec.Code != 200 || f.actor != identity.ID || f.apply.ExpectedAction != wantAction || f.apply.IdempotencyKey != "stable-key" || f.apply.TargetID == nil || *f.apply.TargetID != 19 || !f.apply.Confirm {
			t.Fatal(action, rec.Code, rec.Body.String(), f.apply)
		}
	}
}

func TestAdminCommissionLogHTTPMalformedSelectorsFailBeforeDispatch(t *testing.T) {
	filters := []string{`null`, `[]`, `{"unknown":1}`, `{"invite_user_id":null}`, `{"invite_user_id":0}`, `{"invite_user_id":"61624"}`, `{"ids":[]}`, `{"ids":[1,null]}`, `{"ids":[1],"id":1}`, `{"trade_no":" "}`, `{"commission_status":4}`, `{"created_from":10,"created_to":9}`, `{"id":1,"id":2}`}
	for _, action := range []string{"list", "preview"} {
		for _, filter := range filters {
			t.Run(action+filter, func(t *testing.T) {
				f := &commissionHTTPFixture{}
				extra := ""
				if action == "preview" {
					extra = `,"action":"delete","reason":"fixture","confirm_all":true`
				}
				rec := commissionHTTPCall(f, &session.Identity{ID: 1, IsAdmin: 1}, action, `{"filter":`+filter+extra+`}`, "")
				if rec.Code != 400 || f.calls != 0 {
					t.Fatal("malformed selector reached service", rec.Code, rec.Body.String(), f.calls)
				}
			})
		}
	}
	for _, body := range []string{`{"id":1,"expected_action":"restore"}`, `{"id":1,"filter":{"id":2}}`, `{"id":1} {"id":2}`, `{"id":`} {
		f := &commissionHTTPFixture{}
		rec := commissionHTTPCall(f, &session.Identity{ID: 1, IsAdmin: 1}, "delete", body, "")
		if rec.Code != 400 || f.calls != 0 {
			t.Fatal(rec.Code, rec.Body.String(), f.calls)
		}
	}
}

func TestAdminCommissionLogHTTPIdempotencyAndSingleID(t *testing.T) {
	for _, tc := range []struct {
		body, header string
		status       int
	}{
		{`{"id":19,"idempotency_key":"same"}`, "same", 200},
		{`{"id":19,"idempotency_key":"body"}`, "header", 400},
		{`{"idempotency_key":"same"}`, "same", 400},
		{`{"id":0,"idempotency_key":"same"}`, "same", 400},
		{`{"id":null,"idempotency_key":"same"}`, "same", 400},
	} {
		f := &commissionHTTPFixture{}
		rec := commissionHTTPCall(f, &session.Identity{ID: 1, IsAdmin: 1}, "delete", tc.body, tc.header)
		if rec.Code != tc.status || (f.calls > 0) != (tc.status == 200) {
			t.Fatal(tc, rec.Code, rec.Body.String(), f.calls)
		}
	}
	// The real service rejects missing confirmation/hash/key before opening a DB.
	service := admin.NewDBService(config.Config{}, nil)
	for _, raw := range []string{`{}`, `{"preview_hash":"x","confirm":true,"idempotency_key":"key"}`, `{"preview_hash":"` + strings.Repeat("a", 64) + `","confirm":false,"idempotency_key":"key"}`, `{"preview_hash":"` + strings.Repeat("a", 64) + `","confirm":true}`} {
		rec := commissionHTTPCall(service, &session.Identity{ID: 1, IsAdmin: 1}, "delete-many", raw, "")
		if rec.Code != 400 {
			t.Fatal(rec.Code, rec.Body.String())
		}
	}
	// Explicit all-records selectors still need confirmation in business logic.
	rec := commissionHTTPCall(service, &session.Identity{ID: 1, IsAdmin: 1}, "preview", `{"filter":{},"action":"delete","reason":"fixture"}`, "")
	if rec.Code != 400 {
		t.Fatal(rec.Code, rec.Body.String())
	}
}

func TestAdminCommissionLogHTTPErrorMappings(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code int
	}{
		{admin.ErrCommissionLogValidation, 400}, {admin.ErrCommissionLogConflict, 409}, {admin.ErrCommissionLogForbidden, 403}, {admin.ErrCommissionLogNotFound, 404}, {admin.ErrUnavailable, 503}, {errors.New("private sql secret"), 500},
	} {
		f := &commissionHTTPFixture{err: fmt.Errorf("wrapped: %w", tc.err)}
		rec := commissionHTTPCall(f, &session.Identity{ID: 1, IsAdmin: 1}, "list", `{}`, "")
		if rec.Code != tc.code || strings.Contains(rec.Body.String(), "private sql secret") {
			t.Fatal(tc, rec.Code, rec.Body.String())
		}
	}
}

func TestAdminCommissionLogHTTPStaffBoundaryAndAIAdapter(t *testing.T) {
	f := &commissionHTTPFixture{}
	staff := &session.Identity{ID: 42, IsStaff: 1}
	for _, action := range []string{"list", "preview", "delete", "delete-many", "restore"} {
		rec := commissionHTTPCall(f, staff, action, `{}`, "")
		if rec.Code != 403 || f.calls != 0 {
			t.Fatal("staff obtained direct administrator route", action, rec.Code, f.calls)
		}
	}
	p := newPanelOperations(config.Config{AdminPath: "control"}, &routerState{admin: f})
	actor := aiops.PanelOperationActor{ID: 42}
	for _, tc := range []struct{ op, raw string }{
		{"commission_log.list", `{"filter":{"invite_user_id":61624,"commission_status":2},"page_size":20}`},
		{"commission_log.preview", `{"filter":{"id":19},"action":"delete","reason":"fixture"}`},
		{"commission_log.delete", `{"id":19,"preview_hash":"` + strings.Repeat("a", 64) + `","idempotency_key":"stable-key","confirm":true}`},
		{"commission_log.delete_many", `{"preview_hash":"` + strings.Repeat("a", 64) + `","idempotency_key":"stable-key","confirm":true}`},
		{"commission_log.restore", `{"preview_hash":"` + strings.Repeat("a", 64) + `","idempotency_key":"stable-key","confirm":true}`},
	} {
		raw, err := p.Execute(context.Background(), actor, tc.op, json.RawMessage(tc.raw))
		if err != nil || !json.Valid(raw) || tc.op != "commission_log.list" && f.actor != actor.ID {
			t.Fatal(tc.op, string(raw), err, f.actor)
		}
		if tc.op == "commission_log.preview" && !strings.Contains(string(raw), strings.Repeat("a", 64)) {
			t.Fatal("preview hash was scrubbed before next operation", string(raw))
		}
	}
	for _, raw := range []string{`{"filter":{"unknown":1}}`, `{"filter":{"invite_user_id":null}}`, `{"filter":{"ids":[]}}`} {
		before := f.calls
		if _, err := p.Execute(context.Background(), actor, "commission_log.list", json.RawMessage(raw)); err == nil || f.calls != before {
			t.Fatal("unsafe AI selector reached service", raw, err)
		}
	}
	for _, status := range []int{-1, 0, 1, 2, 3, 4} {
		err := p.Validate(context.Background(), actor, "orders.commission", json.RawMessage(fmt.Sprintf(`{"trade_no":"fixture","commission_status":%d}`, status)))
		wantAllowed := status == 0 || status == 1 || status == 3
		if (err == nil) != wantAllowed {
			t.Fatal("commission review state schema", status, err)
		}
	}
}

func TestAdminCommissionLogPostgresHTTPWorkflow(t *testing.T) {
	dsn := os.Getenv("FOREST_COMMISSION_TEST_DSN")
	if dsn == "" {
		t.Skip("requires disposable FOREST_COMMISSION_TEST_DSN")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	service := admin.NewDBService(config.Config{}, db)
	if err = service.EnsureCommissionLogSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	marker := fmt.Sprint(time.Now().UnixNano())
	var administrator, staff, logID int64
	for i, dst := range []*int64{&administrator, &staff} {
		if err = db.QueryRow(`INSERT INTO v2_user(email,password,uuid,token,is_admin,is_staff,commission_balance,created_at,updated_at) VALUES($1,'fixture',$2::TEXT,$2::TEXT,$3,$4,43415,1,1) RETURNING id`, fmt.Sprintf("commission-http-%d-%s@example.test", i, marker), fmt.Sprintf("c%d%s", i, marker), 1-i, i).Scan(dst); err != nil {
			t.Fatal(err)
		}
	}
	trade := "http-" + marker
	t.Cleanup(func() {
		db.Exec(`DELETE FROM v2_commission_log_operation WHERE actor_id IN ($1,$2)`, administrator, staff)
		db.Exec(`DELETE FROM v2_commission_log_preview WHERE actor_id IN ($1,$2)`, administrator, staff)
		db.Exec(`DELETE FROM v2_commission_log WHERE id=$1`, logID)
		db.Exec(`DELETE FROM v2_order WHERE trade_no=$1`, trade)
		db.Exec(`DELETE FROM v2_user WHERE id IN ($1,$2)`, administrator, staff)
	})
	if _, err = db.Exec(`INSERT INTO v2_order(user_id,invite_user_id,plan_id,type,period,trade_no,total_amount,status,commission_status,created_at,updated_at) VALUES($1,$2,1,1,'month_price',$3,1000,3,2,1,1)`, staff, administrator, trade); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`INSERT INTO v2_commission_log(invite_user_id,user_id,trade_no,order_amount,get_amount,created_at,updated_at) VALUES($1,$2,$3,1000,123,1,1) RETURNING id`, administrator, staff, trade).Scan(&logID); err != nil {
		t.Fatal(err)
	}
	identity := &session.Identity{ID: administrator, IsAdmin: 1}
	rec := commissionHTTPCall(service, identity, "preview", fmt.Sprintf(`{"filter":{"id":%d},"action":"delete","reason":"http fixture"}`, logID), "")
	var preview struct {
		Data admin.CommissionLogPreviewResult `json:"data"`
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &preview) != nil || preview.Data.Count != 1 || preview.Data.TotalAmount != 123 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	body := fmt.Sprintf(`{"id":%d,"preview_hash":%q,"confirm":true}`, logID, preview.Data.PreviewHash)
	for replay := 0; replay < 2; replay++ {
		rec = commissionHTTPCall(service, identity, "delete", body, "http-key-"+marker)
		var result struct {
			Data admin.CommissionLogApplyResult `json:"data"`
		}
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &result) != nil || !result.Data.Committed || result.Data.BalanceChanged || result.Data.Replayed != (replay == 1) {
			t.Fatal(rec.Code, rec.Body.String())
		}
	}
	var deletedBy, balance, status int64
	if err = db.QueryRow(`SELECT deleted_by FROM v2_commission_log WHERE id=$1 AND deleted_at IS NOT NULL`, logID).Scan(&deletedBy); err != nil || deletedBy != administrator {
		t.Fatal(deletedBy, err)
	}
	for _, visibility := range []string{"active", "deleted", "all"} {
		rec = commissionHTTPCall(service, identity, "list", fmt.Sprintf(`{"filter":{"id":%d,"visibility":%q}}`, logID, visibility), "")
		var result admin.CommissionLogListResult
		wantTotal := int64(1)
		if visibility == "active" {
			wantTotal = 0
		}
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &result) != nil || result.Total != wantTotal || result.TotalAmount != wantTotal*123 || result.AppliedFilters.Visibility != visibility {
			t.Fatal("hidden row visibility mismatch", visibility, rec.Code, rec.Body.String())
		}
	}
	// Staff can restore only through the already-authorized AI adapter; the
	// database actor check sees the real staff ID, not the temporary admin flag.
	p := newPanelOperations(config.Config{AdminPath: "control"}, &routerState{admin: service})
	actor := aiops.PanelOperationActor{ID: staff}
	raw, err := p.Execute(context.Background(), actor, "commission_log.preview", json.RawMessage(fmt.Sprintf(`{"filter":{"id":%d},"action":"restore","reason":"restore fixture"}`, logID)))
	if err != nil || json.Unmarshal(raw, &preview) != nil || preview.Data.Count != 1 {
		t.Fatal(string(raw), err)
	}
	raw, err = p.Execute(context.Background(), actor, "commission_log.restore", json.RawMessage(fmt.Sprintf(`{"preview_hash":%q,"confirm":true,"idempotency_key":%q}`, preview.Data.PreviewHash, "restore-"+marker)))
	if err != nil || !strings.Contains(string(raw), `"committed":true`) {
		t.Fatal(string(raw), err)
	}
	if err = db.QueryRow(`SELECT commission_balance FROM v2_user WHERE id=$1`, administrator).Scan(&balance); err != nil || balance != 43415 {
		t.Fatal("balance changed", balance, err)
	}
	if err = db.QueryRow(`SELECT commission_status FROM v2_order WHERE trade_no=$1`, trade).Scan(&status); err != nil || status != 2 {
		t.Fatal("settlement status changed", status, err)
	}
	var active bool
	if err = db.QueryRow(`SELECT deleted_at IS NULL FROM v2_commission_log WHERE id=$1`, logID).Scan(&active); err != nil || !active {
		t.Fatal(active, err)
	}
	var actions int
	if err = db.QueryRow(`SELECT COUNT(*) FROM v2_commission_log_operation WHERE actor_id IN ($1,$2)`, administrator, staff).Scan(&actions); err != nil || actions != 2 {
		t.Fatal("replay created a second audit operation", actions, err)
	}
}
