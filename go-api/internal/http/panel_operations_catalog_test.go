package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"forest/go-api/internal/aiops"
	"forest/go-api/internal/config"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestPanelOperationCatalogContracts(t *testing.T) {
	names, methodPaths := map[string]bool{}, map[string]bool{}
	for _, op := range panelOperationCatalog() {
		if names[op.Name] {
			t.Errorf("duplicate operation %s", op.Name)
		}
		names[op.Name] = true
		key := op.Method + " " + op.Path
		if methodPaths[key] {
			t.Errorf("duplicate route %s", key)
		}
		methodPaths[key] = true
		if op.Name == "" || op.Category == "" || op.Description == "" || !strings.HasPrefix(op.Path, "/") {
			t.Errorf("incomplete descriptor %+v", op)
		}
		if strings.Contains(op.Path, "/payment/") || strings.Contains(op.Path, "/ai-ops/") {
			t.Errorf("forbidden recursive/payment route %s", op.Path)
		}
		if op.Method == "GET" && op.Mutating {
			t.Errorf("mutating GET operation %s", op.Name)
		}
		var schema struct {
			Type                 string                     `json:"type"`
			Properties           map[string]json.RawMessage `json:"properties"`
			AdditionalProperties bool                       `json:"additionalProperties"`
			Required             []string                   `json:"required"`
		}
		if json.Unmarshal(op.Parameters, &schema) != nil || schema.Type != "object" || schema.Properties == nil || schema.AdditionalProperties {
			t.Errorf("invalid operation schema %s", op.Name)
		}
		for _, required := range schema.Required {
			if _, ok := schema.Properties[required]; !ok {
				t.Errorf("undeclared required field %s.%s", op.Name, required)
			}
		}
		if strings.Contains(op.Path, "{id}") {
			if _, ok := schema.Properties["id"]; !ok {
				t.Errorf("missing path id %s", op.Name)
			}
		}
		if strings.Contains(op.Name, "clear_") && !op.AdminOnly {
			t.Errorf("log deletion exposed to staff: %s", op.Name)
		}
	}
	for _, name := range []string{"orders.cancel", "orders.refund", "plans.save", "nodes.v2node.save", "dns.record_save", "entry_monitors.save", "ip_pool.create", "apple.order_refund", "migration.apply", "subscribe_guard.search", "config.save"} {
		if !names[name] {
			t.Errorf("missing backend capability %s", name)
		}
	}
}

func TestPanelOperationDeploymentCredentialContracts(t *testing.T) {
	operations := panelOperationCatalog()
	if len(operations) != 205 {
		t.Fatalf("credential access must not duplicate backend operations: got %d", len(operations))
	}
	byName := map[string]aiops.PanelOperation{}
	for _, op := range operations {
		byName[op.Name] = op
	}
	var schema struct {
		Properties map[string]struct {
			Type    string  `json:"type"`
			Minimum float64 `json:"minimum"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(byName["config.read"].Parameters, &schema); err != nil {
		t.Fatal(err)
	}
	if schema.Properties["include_credentials"].Type != "boolean" {
		t.Fatal("config.read must declare an explicit boolean credential opt-in")
	}
	for _, key := range schema.Required {
		if key == "include_credentials" {
			t.Fatal("credential opt-in must remain optional")
		}
	}
	if err := json.Unmarshal(byName["nodes.list"].Parameters, &schema); err != nil {
		t.Fatal(err)
	}
	if schema.Properties["include_install_command"].Type != "boolean" {
		t.Fatal("nodes.list must declare an explicit boolean installation command opt-in")
	}
	if id := schema.Properties["node_id"]; id.Type != "integer" || id.Minimum != 1 {
		t.Fatal("installation command access must identify a positive node ID")
	}
	if len(schema.Required) != 0 {
		t.Fatal("ordinary nodes.list must not require credential access parameters")
	}
	for _, tc := range []struct {
		name, method, path string
		mutating           bool
	}{
		{"config.read", "GET", "/config/fetch", false},
		{"nodes.list", "GET", "/server/manage/getNodes", false},
		{"dns_failover.create_probe", "POST", "/dns-failover/probes", true},
		{"apple.order_credentials", "POST", "/apple-id/order/credentials", false},
	} {
		op := byName[tc.name]
		if op.Method != tc.method || op.Path != tc.path || op.Mutating != tc.mutating {
			t.Errorf("operation %s no longer matches its explicit credential guard", tc.name)
		}
	}
}

// A new fixed admin route fails this test until it is implemented or explicitly
// classified. This prevents silently calling a small subset "full access".
func TestPanelOperationCatalogCoversAdminRoutes(t *testing.T) {
	paths := map[string]bool{}
	for _, op := range panelOperationCatalog() {
		paths[op.Path] = true
	}
	excluded := map[string]string{
		"/payment/fetch": "payment configuration forbidden", "/payment/getPaymentMethods": "payment configuration forbidden", "/payment/getPaymentForm": "payment configuration forbidden", "/payment/save": "payment configuration forbidden", "/payment/drop": "payment configuration forbidden", "/payment/show": "payment configuration forbidden", "/payment/sort": "payment configuration forbidden",
		"/ai-ops/": "delegated to AI session/settings/SSH management; never recursively invoke", "/support/": "subroutes checked below", "/migration/xboard/": "subroutes checked below", "/apple-id/": "subroutes checked below", "/server/client-entry-user-policy/collection/": "subroutes checked below", "/dns-failover/": "dynamic REST routes checked below", "/server/": "protocol routes checked below",
	}
	raw, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`adminPrefix\+"(/[^"\n]+)"`)
	for _, m := range re.FindAllStringSubmatch(string(raw), -1) {
		if !paths[m[1]] && excluded[m[1]] == "" {
			t.Errorf("unclassified admin route %s", m[1])
		}
	}
	for _, entry := range []struct{ file, fn, base string }{
		{"router_apple_id_admin.go", "handleAdminAppleID", "/apple-id/"},
		{"router_user_migration.go", "handleUserMigration", "/migration/xboard/"},
		{"router_client_entry_collection.go", "handleAdminClientEntryCollection", "/server/client-entry-user-policy/collection/"},
	} {
		f, err := parser.ParseFile(token.NewFileSet(), entry.file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name.Name != entry.fn {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				sw, ok := n.(*ast.SwitchStmt)
				if !ok {
					return true
				}
				tag, ok := sw.Tag.(*ast.Ident)
				if !ok || tag.Name != "action" {
					return true
				}
				for _, clause := range sw.Body.List {
					for _, expr := range clause.(*ast.CaseClause).List {
						lit, ok := expr.(*ast.BasicLit)
						if !ok || lit.Kind != token.STRING {
							continue
						}
						suffix, _ := strconv.Unquote(lit.Value)
						if !paths[entry.base+suffix] {
							t.Errorf("unclassified %s route %s", entry.fn, suffix)
						}
					}
				}
				return false
			})
		}
	}
	for protocol := range managedServerRouterTypes {
		for _, action := range []string{"save", "update", "copy", "drop"} {
			if !paths["/server/"+protocol+"/"+action] {
				t.Errorf("missing protocol operation %s/%s", protocol, action)
			}
		}
	}
	for _, route := range []string{"/support/operations", "/support/operations/clear", "/dns-failover/settings", "/dns-failover/probes", "/dns-failover/probes/{id}", "/dns-failover/probes/{id}/enabled", "/dns-failover/probes/{id}/revoke", "/dns-failover/rules", "/dns-failover/rules/{id}", "/dns-failover/rules/{id}/status", "/dns-failover/rules/{id}/enabled", "/dns-failover/rules/{id}/manual-switch", "/dns-failover/events", "/dns-failover/logs", "/dns-failover/entry-monitors", "/dns-failover/entry-monitors/run", "/dns-failover/entry-monitors/runs", "/dns-failover/entry-monitors/backup-ips", "/dns-failover/entry-monitors/backup-ips/{id}", "/dns-failover/entry-monitors/backup-ips/refresh", "/dns-failover/entry-monitors/backup-ips/delete"} {
		if !paths[route] {
			t.Errorf("missing dynamic operation %s", route)
		}
	}
}

func TestPanelOperationEntryRangeAndNestedSchemas(t *testing.T) {
	byName := map[string]map[string]any{}
	for _, op := range panelOperationCatalog() {
		var s map[string]any
		if err := json.Unmarshal(op.Parameters, &s); err != nil {
			t.Fatal(err)
		}
		byName[op.Name] = s
	}
	p := byName["entry_rules.save"]["properties"].(map[string]any)
	condition := p["conditions"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
	for _, key := range []string{"field", "operator", "value", "values", "min", "max"} {
		if _, ok := condition[key]; !ok {
			t.Errorf("entry conditions omit supported field %s", key)
		}
	}
	monitor := byName["entry_monitors.save"]["properties"].(map[string]any)["items"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
	target := monitor["targets"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
	if _, ok := target["auto_split_enabled"]; !ok {
		t.Fatal("missing auto split switch")
	}
	if _, ok := byName["config.save"]["properties"].(map[string]any)["payment"]; ok {
		t.Fatal("generic config schema includes payment")
	}
}

func TestPanelOperationCatalogRealParameterRoundTrips(t *testing.T) {
	service := &fakeAdminService{}
	migration := &fakeUserMigrationService{}
	p := newPanelOperations(config.Config{AdminPath: "fixture-admin"}, &routerState{admin: service, userMigration: migration})
	actor := aiops.PanelOperationActor{ID: 42, IsAdmin: true}
	run := func(name, raw string) {
		t.Helper()
		if err := p.Validate(context.Background(), actor, name, json.RawMessage(raw)); err != nil {
			t.Fatalf("%s rejected schema: %v", name, err)
		}
		if _, err := p.Execute(context.Background(), actor, name, json.RawMessage(raw)); err != nil {
			t.Fatalf("%s handler rejected: %v", name, err)
		}
	}
	run("users.update", `{"id":9,"email":"fixture@example.test","banned":0,"is_admin":0,"is_staff":0,"expired_at":null,"discount":null,"commission_rate":null,"plan_id":null,"remarks":null}`)
	for _, field := range []string{"expired_at", "discount", "commission_rate", "plan_id", "remarks"} {
		if value, present := service.lastUserUpdate.Values[field]; !present || value != "" {
			t.Errorf("user clear %s lost: %#v", field, service.lastUserUpdate.Values)
		}
	}
	run("plans.save", `{"id":3,"name":"fixture","group_id":2,"transfer_enable":100,"month_price":null,"year_price":null,"device_limit":null,"speed_limit":null,"force_update":false}`)
	if service.lastPlanSave.ID == nil || *service.lastPlanSave.ID != 3 || service.lastPlanSave.MonthPrice != nil || service.lastPlanSave.YearPrice != nil || service.lastPlanSave.DeviceLimit != nil || service.lastPlanSave.SpeedLimit != nil {
		t.Fatal("nullable plan fields changed", service.lastPlanSave)
	}
	run("entry_rules.save", `{"id":7,"name":"fixture","action":"override","entry_host":"192.0.2.5","enabled":1,"conditions":[{"field":"user_id","operator":"between","min":1,"max":200},{"field":"ua","operator":"contains_any","values":["forest"]}],"members":[{"server_type":"vless","server_id":4,"sort":1}],"extra_nodes":["vless://fixture@192.0.2.6:54101#fixture"],"extra_nodes_position":"after"}`)
	got := service.lastClientEntryUserPolicySave
	if len(got.Conditions) != 2 || string(got.Conditions[0].Min) != "1" || string(got.Conditions[0].Max) != "200" || len(got.Conditions[1].Values) != 1 || len(got.ExtraNodes) != 1 || len(got.Members) != 1 {
		t.Fatal("entry nested fields lost", got)
	}
	run("dns.domains", `{"current":3,"page_size":25,"keyword":"example"}`)
	if service.lastDNSPodDomainList.Current != 3 || service.lastDNSPodDomainList.PageSize != 25 {
		t.Fatal("DNS domain pagination lost", service.lastDNSPodDomainList)
	}
	run("dns.records", `{"domain":"example.test","domain_id":7,"current":2,"page_size":50,"record_type":"A"}`)
	if service.lastDNSPodRecordList.Current != 2 || service.lastDNSPodRecordList.PageSize != 50 || service.lastDNSPodRecordList.DomainID != 7 {
		t.Fatal("DNS record pagination lost", service.lastDNSPodRecordList)
	}
	run("migration.preview", `{"source":{"host":"source.example.test","port":3306,"database":"fixture","username":"fixture","password":"fixture-only","tls":true},"plan_mapping":{"1":2},"locking_balance_policy":"ignore","confirm_locking_balance":true,"regenerate_tokens":true,"regenerate_uuids":true}`)
	if migration.preview.Source.Password != "fixture-only" || migration.preview.PlanMapping["1"] != 2 || !migration.preview.RegenerateTokens || !migration.preview.RegenerateUUIDs || migration.actorID != 42 {
		t.Fatal("migration typed fields lost", migration.actorID)
	}
	t.Logf("catalog operations=%d", len(panelOperationCatalog()))
}

// Dynamic dispatcher conditions are audited explicitly as well as fixed paths.
// Changing a branch forces a corresponding catalog/coverage decision.
func TestPanelOperationDynamicDispatcherCoverage(t *testing.T) {
	expected := map[string]bool{}
	for _, condition := range []string{
		`len(parts) == 1 && parts[0] == "settings"`,
		`len(parts) == 1 && parts[0] == "probes"`,
		`len(parts) == 2 && parts[0] == "probes"`,
		`len(parts) == 3 && parts[0] == "probes" && (parts[2] == "enabled" || parts[2] == "revoke")`,
		`len(parts) == 1 && parts[0] == "rules"`,
		`len(parts) == 2 && parts[0] == "rules"`,
		`len(parts) == 3 && parts[0] == "rules" && parts[2] == "status"`,
		`len(parts) == 3 && parts[0] == "rules" && (parts[2] == "enabled" || parts[2] == "manual-switch")`,
		`len(parts) == 1 && parts[0] == "events"`,
		`len(parts) == 1 && parts[0] == "logs"`,
		`len(parts) == 1 && parts[0] == "entry-monitors"`,
		`len(parts) == 2 && parts[0] == "entry-monitors" && parts[1] == "run"`,
		`len(parts) == 2 && parts[0] == "entry-monitors" && parts[1] == "runs"`,
		`len(parts) == 2 && parts[0] == "entry-monitors" && parts[1] == "backup-ips"`,
		`len(parts) == 3 && parts[0] == "entry-monitors" && parts[1] == "backup-ips" && parts[2] == "refresh"`,
		`len(parts) == 3 && parts[0] == "entry-monitors" && parts[1] == "backup-ips" && parts[2] == "delete"`,
		`len(parts) == 3 && parts[0] == "entry-monitors" && parts[1] == "backup-ips"`,
	} {
		expected[condition] = true
	}
	fs := token.NewFileSet()
	f, err := parser.ParseFile(fs, "router_dns_failover.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "handleAdminDNSFailover" {
			continue
		}
		for _, statement := range fn.Body.List {
			branch, ok := statement.(*ast.IfStmt)
			if !ok {
				continue
			}
			var b bytes.Buffer
			if err := printer.Fprint(&b, fs, branch.Cond); err != nil {
				t.Fatal(err)
			}
			condition := b.String()
			if !strings.Contains(condition, "parts") {
				continue
			}
			if !expected[condition] {
				t.Errorf("unclassified dynamic route condition: %s", condition)
			}
			seen[condition] = true
		}
	}
	for condition := range expected {
		if !seen[condition] {
			t.Errorf("stale dynamic route classification: %s", condition)
		}
	}
}

func TestPanelOperationConfigSchemaMatchesGuard(t *testing.T) {
	fields := panelConfigFields()
	for _, section := range panelConfigSections {
		for _, field := range strings.Fields(section) {
			if _, ok := fields[field]; !ok {
				t.Errorf("allowed config field missing in schema: %s", field)
			}
		}
	}
	for field := range fields {
		known := false
		for _, section := range panelConfigSections {
			for _, allowed := range strings.Fields(section) {
				known = known || allowed == field
			}
		}
		if !known {
			t.Errorf("schema advertises disallowed config field: %s", field)
		}
	}
}
