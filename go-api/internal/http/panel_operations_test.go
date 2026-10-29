package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/aiops"
	"forest/go-api/internal/config"
)

func testPanelOperations(service *fakeAdminService, operations ...aiops.PanelOperation) *panelOperations {
	p := &panelOperations{cfg: config.Config{AdminPath: "real-admin-path"}, state: &routerState{admin: service}, operations: map[string]aiops.PanelOperation{}}
	for _, op := range operations {
		if len(op.Parameters) == 0 {
			op.Parameters = json.RawMessage(`{"type":"object"}`)
		}
		p.operations[op.Name] = op
	}
	return p
}

func TestPanelOperationReadsAndMutationsUseExistingHandlers(t *testing.T) {
	service := &fakeAdminService{userList: admin.UserListResult{Data: []map[string]any{{"id": 9, "email": "fixture@example.test", "token": "must-not-return", "uuid": "must-not-return", "nested": map[string]any{"password": "must-not-return"}}}, Total: 1}}
	p := testPanelOperations(service,
		aiops.PanelOperation{Name: "users", Path: "/user/fetch", Method: "GET"},
		aiops.PanelOperation{Name: "cancel", Path: "/order/cancel", Method: "POST", Mutating: true},
		aiops.PanelOperation{Name: "sort", Path: "/plan/sort", Method: "POST", Mutating: true},
		aiops.PanelOperation{Name: "probe.delete", Path: "/dns-failover/probes/{id}", Method: "DELETE", Mutating: true},
	)
	actor := aiops.PanelOperationActor{ID: 42}
	result, err := p.Execute(context.Background(), actor, "users", json.RawMessage(`{"current":1,"pageSize":20,"filter":[{"key":"email","condition":"=","value":"fixture@example.test"}]}`))
	if err != nil || !json.Valid(result) || strings.Contains(string(result), "must-not-return") || !strings.Contains(string(result), "fixture@example.test") {
		t.Fatal("read/scrub failed", string(result), err)
	}
	if len(service.lastUserFetch.Filters) != 1 || service.lastUserFetch.Filters[0].Value != "fixture@example.test" {
		t.Fatal("nested GET filter lost", service.lastUserFetch)
	}
	if _, err = p.Execute(context.Background(), actor, "cancel", json.RawMessage(`{"trade_no":"fixture-order"}`)); err != nil || service.lastCancel != "fixture-order" {
		t.Fatal("mutation not dispatched", err, service.lastCancel)
	}
	if _, err = p.Execute(context.Background(), actor, "sort", json.RawMessage(`{"plan_ids":[9,7,8]}`)); err != nil || len(service.lastPlanSortIDs) != 3 || service.lastPlanSortIDs[1] != 7 {
		t.Fatal("JSON array lost", err, service.lastPlanSortIDs)
	}
	if _, err = p.Execute(context.Background(), actor, "probe.delete", json.RawMessage(`{"id":12}`)); err != nil || service.lastDNSProbeDeleteID != 12 {
		t.Fatal("numeric path not dispatched", err, service.lastDNSProbeDeleteID)
	}
}

func TestPanelOperationRejectsUnregisteredRoutesAuthAndStaffPrivilegeBypass(t *testing.T) {
	service := &fakeAdminService{userInfoDetails: map[int64]map[string]any{9: {"id": 9, "is_admin": 0, "is_staff": 0}, 10: {"id": 10, "is_admin": 1, "is_staff": 0}, 11: {"id": 11, "is_admin": 0, "is_staff": 1}}}
	p := testPanelOperations(service,
		aiops.PanelOperation{Name: "update", Path: "/user/update", Method: "POST", Mutating: true},
		aiops.PanelOperation{Name: "delete", Path: "/user/delUser", Method: "POST", Mutating: true},
		aiops.PanelOperation{Name: "admin", Path: "/system/getSystemLog", Method: "GET", AdminOnly: true},
	)
	actor := aiops.PanelOperationActor{ID: 42}
	for _, tc := range []struct{ name, params string }{
		{"unknown", `{}`}, {"admin", `{}`}, {"update", `{"id":9,"is_admin":1}`}, {"update", `{"id":9,"is_staff":1}`}, {"update", `{"id":10,"password":"new-password"}`}, {"delete", `{"id":11}`}, {"update", `{"id":9,"auth_data":"forged"}`}, {"update", `{"id":9,"filter[0][key]":"id"}`},
	} {
		if _, err := p.Execute(context.Background(), actor, tc.name, json.RawMessage(tc.params)); err == nil {
			t.Fatal("unexpected authorization", tc)
		}
	}
	for _, path := range []string{"/payment/fetch", "/payment/save", "/ai-ops/config/save", "/session/delete", "/../payment/save", "//payment/fetch", "/payment%2Fsave", "https://evil.test"} {
		p.operations["forbidden"] = aiops.PanelOperation{Name: "forbidden", Path: path, Method: "POST"}
		if _, err := p.Execute(context.Background(), aiops.PanelOperationActor{ID: 1, IsAdmin: true}, "forbidden", json.RawMessage(`{}`)); !errors.Is(err, aiops.ErrForbidden) {
			t.Fatal("forbidden path accepted", path, err)
		}
	}
	if _, err := p.Execute(context.Background(), actor, "update", json.RawMessage(`{"id":9,"email":"changed@example.test","banned":0,"password":"new-password"}`)); err != nil || service.lastUserUpdate.Values["is_admin"] != "0" || service.lastUserUpdate.Values["is_staff"] != "0" {
		t.Fatal("ordinary user update failed", err, service.lastUserUpdate)
	}
}

func TestPanelOperationStaffBulkUserScopeAndConfigBoundary(t *testing.T) {
	service := &fakeAdminService{configData: map[string]any{"email": map[string]any{"email_host": "smtp.example.test", "email_password": "hidden-fixture", "unknown_new_field": "hidden-fixture"}, "payment": map[string]any{"key": "hidden-fixture"}}}
	p := testPanelOperations(service,
		aiops.PanelOperation{Name: "ban", Path: "/user/ban", Method: "POST", Mutating: true},
		aiops.PanelOperation{Name: "config.read", Path: "/config/fetch", Method: "GET"},
		aiops.PanelOperation{Name: "config.write", Path: "/config/save", Method: "POST", Mutating: true},
	)
	actor := aiops.PanelOperationActor{ID: 42}
	if _, err := p.Execute(context.Background(), actor, "ban", json.RawMessage(`{"filter":[{"key":"plan_id","condition":"=","value":"4"}]}`)); err != nil {
		t.Fatal(err)
	}
	if len(service.lastUserBan) != 3 || service.lastUserBan[1].Key != "is_admin" || service.lastUserBan[2].Key != "is_staff" {
		t.Fatal("staff bulk scope missing", service.lastUserBan)
	}
	for _, tc := range []struct{ name, params string }{
		{"config.read", `{}`}, {"config.read", `{"key":"payment"}`}, {"config.read", `{"key":"does-not-exist"}`},
		{"config.write", `{"payment_key":"secret"}`}, {"config.write", `{"ai_ops_enabled":true}`}, {"config.write", `{"log_keep_days":1}`},
	} {
		if _, err := p.Execute(context.Background(), actor, tc.name, json.RawMessage(tc.params)); err == nil {
			t.Fatal("unsafe config accepted", tc)
		}
	}
	result, err := p.Execute(context.Background(), actor, "config.read", json.RawMessage(`{"key":"email"}`))
	if err != nil || strings.Contains(string(result), "hidden-fixture") || strings.Contains(string(result), "payment") || strings.Contains(string(result), "unknown_new_field") || !strings.Contains(string(result), "smtp.example.test") {
		t.Fatal("config projection failed", string(result), err)
	}
	if _, err = p.Execute(context.Background(), actor, "config.write", json.RawMessage(`{"email_bulk_interval":5,"commission_withdraw_method":["USDT"]}`)); err != nil || fmtPanelNumber(service.lastConfigSave["email_bulk_interval"]) != "5" {
		t.Fatal("permitted config failed", err, service.lastConfigSave)
	}
	if _, err = p.Execute(context.Background(), aiops.PanelOperationActor{ID: 1, IsAdmin: true}, "config.write", json.RawMessage(`{"log_keep_days":7}`)); err != nil {
		t.Fatal("admin retention refused", err)
	}
}

func fmtPanelNumber(value any) string {
	if number, ok := value.(json.Number); ok {
		return string(number)
	}
	return ""
}

func TestPanelOperationSchemaLargeResponsesAndCSV(t *testing.T) {
	p := testPanelOperations(&fakeAdminService{}, aiops.PanelOperation{Name: "probe", Path: "/dns-failover/probes/{id}", Method: "DELETE", Parameters: json.RawMessage(`{"type":"object","required":["id"],"properties":{"id":{"type":"integer","minimum":1}},"additionalProperties":false}`)})
	for _, raw := range []string{`{}`, `{"id":0}`, `{"id":"../payment"}`, `{"id":1,"extra":true}`, `null`, `{} {}`} {
		if p.Validate(context.Background(), aiops.PanelOperationActor{ID: 42}, "probe", json.RawMessage(raw)) == nil {
			t.Fatal("invalid schema accepted", raw)
		}
	}
	w := &panelOperationResponse{header: http.Header{}, status: 200}
	w.Write([]byte(strings.Repeat("x", panelOperationResponseLimit+1)))
	if !w.overflow || w.body.Len() != 0 {
		t.Fatal("oversize response silently captured")
	}
	result, err := sanitizePanelResponse([]byte("邮箱,密码,UUID,订阅地址\nfixture@example.test,secret-pass,secret-id,https://example.test/secret-token\n"), "text/csv")
	if err != nil || strings.Contains(string(result), "secret-") || !strings.Contains(string(result), "fixture@example.test") {
		t.Fatal("csv secret exposed", string(result), err)
	}
	if _, err = sanitizePanelResponse([]byte("private backend error containing secrets"), "text/plain"); err == nil {
		t.Fatal("opaque text should not be returned")
	}
}

func TestPanelOperationPlainTextCSVExportsAndOperationalFlags(t *testing.T) {
	service := &fakeAdminService{userDumpCSV: "邮箱,订阅地址\nfixture@example.test,https://example.test/private-token\n", userGenerateBatch: true, userGenerateCSV: "账号,密码,UUID,订阅地址\nfixture@example.test,private-password,private-uuid,https://example.test/private-token\n"}
	p := testPanelOperations(service,
		aiops.PanelOperation{Name: "export", Path: "/user/dumpCSV", Method: "GET"},
		aiops.PanelOperation{Name: "generate", Path: "/user/generate", Method: "POST", Mutating: true},
	)
	for _, name := range []string{"export", "generate"} {
		result, err := p.Execute(context.Background(), aiops.PanelOperationActor{ID: 1, IsAdmin: true}, name, json.RawMessage(`{}`))
		if err != nil || strings.Contains(string(result), "private-") || !strings.Contains(string(result), "fixture@example.test") {
			t.Fatal("plain text CSV failed", name, string(result), err)
		}
	}
	raw := []byte(`{"data":{"password_limit_enable":1,"password_limit_count":5,"password_limit_expire":60,"subscribe_token_in_path":1,"token_configured":true,"has_api_key":true,"site":{"subscribe_url":"https://sub.example.test"},"user":{"email":"fixture@example.test","subscribe_url":"https://sub.example.test/private-token"},"token":"private-token","payment_config":{"key":"private-payment"}}}`)
	result, err := sanitizePanelResponse(raw, "application/json")
	if err != nil || strings.Contains(string(result), "private-") {
		t.Fatal("secret leaked", string(result), err)
	}
	for _, value := range []string{`"password_limit_count":5`, `"token_configured":true`, `"has_api_key":true`, `"subscribe_token_in_path":1`, `"subscribe_url":"https://sub.example.test"`} {
		if !strings.Contains(string(result), value) {
			t.Fatal("operational flag hidden", value, string(result))
		}
	}
}

func TestPanelOperationUsesActualActorEmailAndClassifiesHTTPFailure(t *testing.T) {
	service := &fakeAdminService{userInfoDetails: map[int64]map[string]any{42: {"id": 42, "email": "operator@example.test"}}}
	p := testPanelOperations(service, aiops.PanelOperation{Name: "mail.test", Path: "/config/testSendMail", Method: "POST", Mutating: true}, aiops.PanelOperation{Name: "probe.delete", Path: "/dns-failover/probes/{id}", Method: "DELETE", Mutating: true})
	if _, err := p.Execute(context.Background(), aiops.PanelOperationActor{ID: 42}, "mail.test", json.RawMessage(`{}`)); err != nil || service.lastMailTestEmail != "operator@example.test" {
		t.Fatal("actor email not carried", err, service.lastMailTestEmail)
	}
	service.err = errors.New("SQL backend secret must not leak")
	_, err := p.Execute(context.Background(), aiops.PanelOperationActor{ID: 42}, "probe.delete", json.RawMessage(`{"id":5}`))
	var backend *aiops.PanelOperationError
	if !errors.As(err, &backend) || backend.Status < 400 || strings.Contains(err.Error(), "SQL backend secret") {
		t.Fatal("unsafe HTTP error", err)
	}
}

func TestPanelOperationExplicitCredentialPathsAndLogBoundary(t *testing.T) {
	service := &fakeAdminService{configData: map[string]any{"server": map[string]any{"server_token": "deployment-fixture", "server_cf_api_token": "still-hidden"}}}
	p := testPanelOperations(service, aiops.PanelOperation{Name: "config.read", Path: "/config/fetch", Method: "GET"})
	actor := aiops.PanelOperationActor{ID: 42}
	for _, include := range []bool{false, true} {
		raw, _ := json.Marshal(map[string]any{"key": "server", "include_credentials": include})
		result, err := p.Execute(context.Background(), actor, "config.read", raw)
		if err != nil || strings.Contains(string(result), "still-hidden") || strings.Contains(string(result), "deployment-fixture") != include {
			t.Fatal("scoped server token boundary", string(result), err)
		}
	}
	if p.Validate(context.Background(), actor, "config.read", json.RawMessage(`{"key":"email","include_credentials":true}`)) == nil {
		t.Fatal("credential exception broadened beyond server")
	}
	for _, tc := range []struct{ path, method, original string }{
		{"/dns-failover/probes", "POST", `{"data":{"secret":"needed-probe","install_command":"install-needed-probe","probe":{"secret":"nested-hidden"}}}`},
		{"/apple-id/order/credentials", "POST", `{"data":{"order_id":1,"password":"needed-password","credential":"needed-credential","token":"still-hidden"}}`},
	} {
		sanitized, err := sanitizePanelResponse([]byte(tc.original), "application/json")
		if err != nil {
			t.Fatal(err)
		}
		result, err := restorePanelExplicitCredentials(tc.path, tc.method, false, []byte(tc.original), sanitized)
		if err != nil || !strings.Contains(string(result), "needed-") || strings.Contains(string(result), "nested-hidden") || strings.Contains(string(result), "still-hidden") {
			t.Fatal("explicit read boundary", string(result), err)
		}
	}
	log, _ := json.Marshal(map[string]any{"data": []any{
		map[string]any{"id": 1, "uri": "/api/v1/admin/payment/save?key=private-query", "title": "private-title", "data": `{"config":"private-payment"}`, "context": "private-opaque"},
		map[string]any{"id": 2, "uri": "/api/v1/admin/user/update?auth_data=private-query", "title": "update user", "data": `{"user_id":9,"privateKey":"private-key","password":"private-password","count":5}`, "context": "private-opaque"},
	}})
	filtered, err := filterPanelSystemLogs(log)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(filtered), "private-") || !strings.Contains(string(filtered), `"count":5`) {
		t.Fatal("log payload boundary", string(filtered))
	}
}

func TestPanelOperationV2NodeInstallationIsExplicitAndExactlyScoped(t *testing.T) {
	service := &fakeAdminService{managedServers: []map[string]any{
		{"id": 7, "type": "v2node", "name": "selected", "install_command": "custom-script --token selected-secret", "password": "never-reveal"},
		{"id": 7, "type": "vless", "name": "other-protocol", "install_command": "other-protocol-secret"},
		{"id": 8, "type": "v2node", "name": "other-node", "install_command": "other-node-secret"},
	}}
	p := testPanelOperations(service, aiops.PanelOperation{Name: "nodes.list", Path: "/server/manage/getNodes", Method: "GET"})
	actor := aiops.PanelOperationActor{ID: 42}
	for _, raw := range []string{`{"include_install_command":true}`, `{"include_install_command":true,"node_id":0}`, `{"include_install_command":"true","node_id":7}`} {
		if p.Validate(context.Background(), actor, "nodes.list", json.RawMessage(raw)) == nil {
			t.Fatal("invalid credential opt-in accepted", raw)
		}
	}
	result, err := p.Execute(context.Background(), actor, "nodes.list", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"selected-secret", "never-reveal", "other-protocol-secret", "other-node-secret"} {
		if strings.Contains(string(result), secret) {
			t.Fatal("ordinary node listing exposed credentials", string(result))
		}
	}
	result, err = p.Execute(context.Background(), actor, "nodes.list", json.RawMessage(`{"include_install_command":true,"node_id":7}`))
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Data []map[string]any `json:"data"`
	}
	if json.Unmarshal(result, &parsed) != nil || len(parsed.Data) != 1 || parsed.Data[0]["type"] != "v2node" || parsed.Data[0]["install_command"] != "custom-script --token selected-secret" {
		t.Fatal("installation target wrong", string(result))
	}
	for _, secret := range []string{"never-reveal", "other-protocol-secret", "other-node-secret"} {
		if strings.Contains(string(result), secret) {
			t.Fatal("unselected secret exposed", secret)
		}
	}
	_, err = p.Execute(context.Background(), actor, "nodes.list", json.RawMessage(`{"include_install_command":true,"node_id":99}`))
	var missing *aiops.PanelOperationError
	if !errors.As(err, &missing) || missing.Status != 404 {
		t.Fatal("missing target not reported", err)
	}
	service.managedServers = []map[string]any{{"id": 7, "type": "vless", "install_command": "other-protocol-secret"}}
	_, err = p.Execute(context.Background(), actor, "nodes.list", json.RawMessage(`{"include_install_command":true,"node_id":7}`))
	if !errors.As(err, &missing) || missing.Status != 404 {
		t.Fatal("non-v2node credentials exposed", err)
	}
}
