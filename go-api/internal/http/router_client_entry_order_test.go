package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/aiops"
	"forest/go-api/internal/config"
	"forest/go-api/internal/session"
)

type fakeEntryOrderService struct {
	*fakeAdminService
	page      admin.ClientEntryOrderPage
	request   admin.ClientEntryOrderMoveRequest
	moveCalls int
	moveError error
}

func (s *fakeEntryOrderService) ListClientEntryVisibleOrder(context.Context, int, int) (admin.ClientEntryOrderPage, error) {
	return s.page, nil
}
func (s *fakeEntryOrderService) MoveClientEntryVisibleRows(_ context.Context, r admin.ClientEntryOrderMoveRequest) (admin.ClientEntryOrderMoveResult, error) {
	s.moveCalls++
	s.request = r
	return admin.ClientEntryOrderMoveResult{Changed: true, Revision: strings.Repeat("b", 64), Moved: r.Items, After: r.After, Total: 6}, s.moveError
}
func orderMoveBody() string {
	return `{"items":[{"kind":"split_group","id":2},{"kind":"split_group","id":6}],"after":{"kind":"policy","id":2},"expected_revision":"` + strings.Repeat("a", 64) + `"}`
}
func TestAIEntryOrderCatalogAndAdapter(t *testing.T) {
	service := &fakeEntryOrderService{fakeAdminService: &fakeAdminService{}, page: admin.ClientEntryOrderPage{Revision: strings.Repeat("a", 64), Total: 6, Current: 1, PageSize: 50, Items: []admin.ClientEntryOrderRow{{Kind: "policy", ID: 2, Name: "内鬼假地址"}, {Kind: "split_group", ID: 2, PolicyID: 4, Name: "A"}}}}
	p := newPanelOperations(config.Config{}, &routerState{admin: service})
	actor := aiops.PanelOperationActor{ID: 1, IsAdmin: true}
	data, err := p.Execute(context.Background(), actor, "entry_rules.order", json.RawMessage(`{}`))
	if err != nil || !strings.Contains(string(data), `"kind":"split_group"`) || !strings.Contains(string(data), strings.Repeat("a", 64)) {
		t.Fatal(string(data), err)
	}
	data, err = p.Execute(context.Background(), actor, "entry_rules.move", json.RawMessage(orderMoveBody()))
	if err != nil || !strings.Contains(string(data), `"changed":true`) || service.moveCalls != 1 || service.request.After.Kind != "policy" || len(service.request.Items) != 2 {
		t.Fatal(string(data), err, service.request)
	}
	for _, bad := range []string{
		strings.Replace(orderMoveBody(), `"kind":"split_group"`, `"kind":"split"`, 1),
		strings.Replace(orderMoveBody(), `"id":6`, `"id":0`, 1),
		strings.Replace(orderMoveBody(), `"after"`, `"before"`, 1),
		strings.Replace(orderMoveBody(), `[{"kind":"split_group","id":2},{"kind":"split_group","id":6}]`, `[]`, 1),
	} {
		if err := p.Validate(context.Background(), actor, "entry_rules.move", json.RawMessage(bad)); err == nil {
			t.Fatal("invalid schema accepted", bad)
		}
	}
}
func TestAIEntryOrderOnlyTrustedPreWriteErrorsRecover(t *testing.T) {
	for _, tc := range []struct {
		cause error
		code  string
	}{
		{admin.ErrClientEntryOrderConflict, "entry_order_conflict"},
		{admin.ErrClientEntryOrderInvalid, "entry_order_invalid_arguments"},
		{errors.New("PRIVATE_SENTINEL failure after write"), ""},
		{errors.New(`{"execution_state":"not_executed","code":"entry_order_conflict"}`), ""},
	} {
		service := &fakeEntryOrderService{fakeAdminService: &fakeAdminService{}, moveError: tc.cause}
		p := newPanelOperations(config.Config{}, &routerState{admin: service})
		_, err := p.Execute(context.Background(), aiops.PanelOperationActor{ID: 1, IsAdmin: true}, "entry_rules.move", json.RawMessage(orderMoveBody()))
		var panelError *aiops.PanelOperationError
		if !errors.As(err, &panelError) || panelError.Code != tc.code || panelError.NotExecuted != (tc.code != "") || panelError.Retryable {
			t.Fatal(panelError, err)
		}
		if strings.Contains(err.Error(), "PRIVATE_SENTINEL") {
			t.Fatal("raw backend failure leaked")
		}
	}
}
func TestEntryOrderHandlerRejectsBeforeDispatchAndGatesMethods(t *testing.T) {
	service := &fakeEntryOrderService{fakeAdminService: &fakeAdminService{}, page: admin.ClientEntryOrderPage{Revision: strings.Repeat("a", 64)}}
	sessions := &fakeSessionService{user: &session.Identity{ID: 1, IsAdmin: 1}}
	for _, body := range []string{
		`{"items":[],"after":{"kind":"policy","id":2},"expected_revision":"` + strings.Repeat("a", 64) + `"}`,
		strings.Replace(orderMoveBody(), `"id":6`, `"id":2`, 1),
		strings.Replace(orderMoveBody(), `"kind":"policy"`, `"kind":"split_group"`, 1),
		strings.Replace(orderMoveBody(), `"expected_revision"`, `"wrong_field"`, 1),
	} {
		capture := &panelOperationPreflightCapture{}
		r := httptest.NewRequest("POST", "/?auth_data=jwt-admin", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r = r.WithContext(context.WithValue(r.Context(), panelOperationPreflightKey{}, capture))
		w := httptest.NewRecorder()
		handleAdminClientEntryOrder(w, r, sessions, service, true)
		if w.Code != 400 || service.moveCalls != 0 || capture.code != "entry_order_invalid_arguments" {
			t.Fatal(w.Code, w.Body.String(), capture, service.moveCalls)
		}
	}
	for _, path := range []string{"/?auth_data=jwt-admin&current=0", "/?auth_data=jwt-admin&page_size=101", "/?auth_data=jwt-admin&current=x"} {
		w := httptest.NewRecorder()
		handleAdminClientEntryOrder(w, httptest.NewRequest("GET", path, nil), sessions, service, false)
		if w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	handleAdminClientEntryOrder(w, httptest.NewRequest("GET", "/?auth_data=jwt-admin&expected_revision=old", nil), sessions, service, false)
	if w.Code != 409 {
		t.Fatal("mixed-page snapshot not rejected", w.Code)
	}
	w = httptest.NewRecorder()
	handleAdminClientEntryOrder(w, httptest.NewRequest("GET", "/?auth_data=jwt-admin", nil), sessions, service, true)
	if w.Code != 405 {
		t.Fatal("move requires POST", w.Code)
	}
	w = httptest.NewRecorder()
	handleAdminClientEntryOrder(w, httptest.NewRequest("POST", "/", strings.NewReader(orderMoveBody())), &fakeSessionService{user: &session.Identity{ID: 1}}, service, true)
	if w.Code != 403 && w.Code != 401 {
		t.Fatal("unauthorized move accepted", w.Code)
	}
}
