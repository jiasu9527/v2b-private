package user

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"forest/go-api/internal/cliententry"
	"github.com/DATA-DOG/go-sqlmock"
)

func TestSupportEntryCompatibilityRequiresIdenticalNonemptyMemberSets(t *testing.T) {
	a := []ClientEntryGroupMember{{ServerType: " VMESS ", ServerID: 1}, {ServerType: "vmess", ServerID: 1}}
	if !SupportEntryCompatible(a, []ClientEntryGroupMember{{ServerType: "vmess", ServerID: 1}}) {
		t.Fatal("normalized sets should match")
	}
	for _, b := range [][]ClientEntryGroupMember{nil, {{ServerType: "vless", ServerID: 1}}, {{ServerType: "vmess", ServerID: 2}}, {{ServerType: "vmess", ServerID: 1}, {ServerType: "vmess", ServerID: 2}}} {
		if SupportEntryCompatible(a, b) {
			t.Fatalf("unsafe compatibility: %#v", b)
		}
	}
	if SupportEntryCompatible(nil, nil) {
		t.Fatal("empty scope must not grant access")
	}
}
func TestSupportEntryAppliesOnlyToNormalVisibleSourceNodes(t *testing.T) {
	member := []ClientEntryGroupMember{{ServerType: "vmess", ServerID: 1}, {ServerType: "vmess", ServerID: 2}, {ServerType: "vmess", ServerID: 3}}
	conditions, err := cliententry.DecodeConditions(`[{"field":"user_id","operator":"eq","value":7}]`)
	if err != nil {
		t.Fatal(err)
	}
	policies := []clientEntryUserPolicy{
		{ID: 9, Action: cliententry.ActionHide, Conditions: conditions, Members: []ClientEntryGroupMember{{ServerType: "vmess", ServerID: 2}}},
		{ID: 10, Action: cliententry.ActionOverride, EntryHost: "automatic.example", Members: member, ExtraNodes: []string{"ss://YWVzLTEyOC1nY206cGFzcw@extras.example:443#extra"}},
	}
	servers := []map[string]any{
		{"id": int64(1), "type": "vmess", "host": "original.example"},
		{"id": int64(2), "type": "vmess", "host": "hidden.example"},
		{"id": int64(3), "type": "vmess", "host": "entry-only.example", "client_entry_only": int64(1)},
		{"id": int64(4), "type": "vmess", "host": "outside.example"},
		{"id": int64(5), "type": "vmess", "host": "ungranted.example", "client_entry_only": int64(1)},
	}
	servers = applyClientEntryUserPolicies(servers, cliententry.Subject{UserID: 7}, policies)
	target := SupportEntryTarget{Key: "policy:20", PolicyID: 20, EntryHost: "manual.example", Members: member}
	overrides := []SupportEntryOverride{{SourcePolicyID: 10, TargetPolicyID: 20, TargetKey: target.Key, Active: true}}
	result := applySupportEntryOverrides(context.Background(), servers, policies, overrides, []SupportEntryTarget{target}, 7, 100, nil)
	seen := map[int64]string{}
	for _, row := range result {
		seen[mapInt64(row["id"])] = row["host"].(string)
	}
	if seen[1] != "manual.example" || seen[3] != "manual.example" || seen[4] != "outside.example" {
		t.Fatalf("unexpected overrides: %#v", seen)
	}
	if _, ok := seen[2]; ok {
		t.Fatal("manual override bypassed hide")
	}
	if _, ok := seen[5]; ok {
		t.Fatal("manual override granted unrelated entry-only node")
	}
	for id, host := range seen {
		if id < 0 && host != "extras.example" {
			t.Fatalf("source appended link was mutated: %s", host)
		}
	}
}
func TestSupportEntryExpiredRemovedIncompatibleRestoredFallback(t *testing.T) {
	members := []ClientEntryGroupMember{{ServerType: "vmess", ServerID: 1}}
	policy := clientEntryUserPolicy{ID: 10, Action: cliententry.ActionOverride, Members: members}
	target := SupportEntryTarget{Key: "policy:20", PolicyID: 20, EntryHost: "new-admin-address.example", Members: members}
	base := SupportEntryOverride{SourcePolicyID: 10, TargetPolicyID: 20, TargetKey: target.Key, Active: true}
	cases := []struct {
		name    string
		record  SupportEntryOverride
		targets []SupportEntryTarget
		want    string
	}{
		{"active follows referenced address", base, []SupportEntryTarget{target}, target.EntryHost},
		{"removed target", base, nil, "auto.example"},
		{"incompatible target", base, []SupportEntryTarget{{Key: target.Key, PolicyID: 20, EntryHost: target.EntryHost, Members: []ClientEntryGroupMember{{ServerType: "vmess", ServerID: 2}}}}, "auto.example"},
	}
	expired := base
	expired.ExpiresAt = 100
	cases = append(cases, struct {
		name    string
		record  SupportEntryOverride
		targets []SupportEntryTarget
		want    string
	}{"expired", expired, []SupportEntryTarget{target}, "auto.example"})
	restored := base
	restored.Active = false
	cases = append(cases, struct {
		name    string
		record  SupportEntryOverride
		targets []SupportEntryTarget
		want    string
	}{"restored", restored, []SupportEntryTarget{target}, "auto.example"})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			servers := []map[string]any{{"id": int64(1), "type": "vmess", "host": "auto.example", "client_entry_user_policy_id": int64(10)}}
			got := applySupportEntryOverrides(context.Background(), servers, []clientEntryUserPolicy{policy}, []SupportEntryOverride{tc.record}, tc.targets, 1, 100, nil)
			if got[0]["host"] != tc.want {
				t.Fatalf("got %#v", got)
			}
		})
	}
}
func TestSupportEntryCannotChangeDifferentFirstMatchOrUnmatchedNode(t *testing.T) {
	members := []ClientEntryGroupMember{{ServerType: "vmess", ServerID: 1}}
	policy := clientEntryUserPolicy{ID: 10, Action: cliententry.ActionOriginal, Members: members}
	target := SupportEntryTarget{Key: "policy:20", PolicyID: 20, EntryHost: "manual.example", Members: members}
	servers := []map[string]any{{"id": int64(1), "type": "vmess", "host": "other-rule.example", "client_entry_user_policy_id": int64(11)}, {"id": int64(2), "type": "vmess", "host": "unmatched.example"}}
	got := applySupportEntryOverrides(context.Background(), servers, []clientEntryUserPolicy{policy}, []SupportEntryOverride{{SourcePolicyID: 10, TargetPolicyID: 20, TargetKey: target.Key, Active: true}}, []SupportEntryTarget{target}, 1, 100, nil)
	if got[0]["host"] != "other-rule.example" || got[1]["host"] != "unmatched.example" {
		t.Fatal("override crossed source policy boundary")
	}
}
func TestSupportEntryDefaultDoesNotChangeLegacyQueryContract(t *testing.T) {
	service := &DBService{}
	got, err := service.applySupportEntries(context.Background(), []map[string]any{{"host": "auto.example"}}, nil, 1, 100)
	if err != nil || got[0]["host"] != "auto.example" {
		t.Fatalf("unexpected legacy behavior: %v, %#v", err, got)
	}
}
func TestSupportEntryTargetLoaderGroupsLeafReferencesWithoutConditionsOrExtras(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery(`(?s)SELECT p.id,p.name.*NOT EXISTS .*child.parent_id = g.id.*p.enabled = 1 AND p.action = 'override'`).WillReturnRows(sqlmock.NewRows([]string{"id", "name", "group_id", "group_name", "entry_host", "resolve", "type", "server_id"}).AddRow(10, "Rule", 0, "", "old.example", 0, "vmess", 1).AddRow(10, "Rule", 0, "", "old.example", 0, "vless", 2).AddRow(20, "Split", 30, "Leaf", "leaf.example", 1, "vmess", 1))
	targets, err := LoadSupportEntryTargets(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 || targets[0].Key != "policy:10" || len(targets[0].Members) != 2 || targets[1].Key != "policy:20:split-group:30" || !targets[1].ResolveEntryHost {
		t.Fatalf("bad targets: %#v", targets)
	}
	raw, _ := json.Marshal(targets)
	var decoded []map[string]any
	json.Unmarshal(raw, &decoded)
	if _, ok := decoded[0]["members"]; ok {
		t.Fatal("internal members leaked")
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestServersSupportOverrideRunsAfterPermissionFiltering(t *testing.T) {
	for _, authorized := range []bool{true, false} {
		t.Run(fmt.Sprintf("permission_%v", authorized), func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			service := (&DBService{db: db}).WithSupportEntries()
			service.clientEntryEnsureOnce.Do(func() {})
			expectServerFetchUserForVisibilityTest(mock, 100, 1)
			expectMatchingOverridePolicyForVisibilityTest(mock, "vmess", 11, 100)
			for _, table := range serverFetchTables {
				rows := sqlmock.NewRows([]string{"id", "name", "group_id", "show", "client_entry_only", "host", "port", "updated_at"})
				if table.serverType == "vmess" {
					groups := `[1]`
					if !authorized {
						groups = `[2]`
					}
					rows.AddRow(11, "Node", groups, 1, 1, "original.example", 443, 100)
				}
				mock.ExpectQuery(`SELECT \*\s+FROM ` + table.table + `\s+WHERE "show" = 1`).WillReturnRows(rows)
				if authorized && table.serverType == "vmess" {
					mock.ExpectQuery(`SELECT v, expire_at FROM v2_runtime_kv`).WithArgs("SERVER_VMESS_LAST_CHECK_AT_11").WillReturnRows(sqlmock.NewRows([]string{"v", "expiry"}))
				}
			}
			mock.ExpectQuery(`SELECT source_policy_id,COALESCE\(target_policy_id,0\)`).WithArgs(int64(100)).WillReturnRows(sqlmock.NewRows([]string{"source", "target", "group", "key", "active", "expiry", "version"}).AddRow(7, 20, 0, "policy:20", 1, 0, 1))
			mock.ExpectQuery(`SELECT p.id,p.name,COALESCE\(g.id,0\)`).WillReturnRows(sqlmock.NewRows([]string{"id", "name", "group", "group_name", "host", "resolve", "type", "server_id"}).AddRow(20, "B", 0, "", "manual.example", 0, "vmess", 11))
			expectSubscribeActivityForVisibilityTest(mock, 100)
			result, err := service.Servers(context.Background(), 100, "ClashMeta")
			if err != nil {
				t.Fatal(err)
			}
			if authorized {
				if len(result) != 1 || result[0]["host"] != "manual.example" || result[0]["port"] != int64(443) {
					t.Fatalf("support override not applied: %#v", result)
				}
			} else if len(result) != 0 {
				t.Fatalf("support override bypassed account node group: %#v", result)
			}
			if err = mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func supportMembershipTestRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id", "name", "enabled", "conditions", "host", "resolve", "has_members", "sort", "members"})
}
func TestSupportMembershipOnlySafeEmailGroupsAreOfferedAndDisabledMembershipCanExit(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows := supportMembershipTestRows().
		AddRow(1, "Automatic", 1, `[]`, "default.example", 0, true, 10, "vmess:1").
		AddRow(2, "VIP", 1, `[{"field":"email","operator":"in","values":[]}]`, "vip.example", 0, true, 20, "vmess:9,vless:100").
		AddRow(3, "Disabled current", 0, `[{"field":"email","operator":"in","values":["u@example.com"]}]`, "paused.example", 0, true, 30, "vmess:1").
		AddRow(4, "Different plan", 1, `[{"field":"email","operator":"in","values":[]},{"field":"plan_id","operator":"eq","value":100}]`, "plan.example", 0, true, 40, "vmess:1").
		AddRow(5, "Multiple email AND", 1, `[{"field":"email","operator":"in","values":["u@example.com"]},{"field":"email","operator":"in","values":["u@example.com"]}]`, "ambiguous.example", 0, true, 50, "vmess:1").
		AddRow(6, "No members", 1, `[{"field":"email","operator":"in","values":[]}]`, "empty.example", 0, false, 60, "").
		AddRow(7, "Invalid host", 1, `[{"field":"email","operator":"in","values":[]}]`, "https://invalid.example", 0, true, 70, "vmess:1").
		AddRow(8, "Current but shadowed", 1, `[{"field":"email","operator":"in","values":["u@example.com"]}]`, "shadowed.example", 1, true, 80, "vmess:1")
	mock.ExpectQuery(`(?s)SELECT p.id,p.name,p.enabled.*WHERE p.mode='standard' AND p.action='override' ORDER BY p.id`).WillReturnRows(rows)
	membership, err := LoadSupportEntryMembership(context.Background(), db, cliententry.Subject{UserID: 7, Email: "u@example.com", PlanID: 2}, []SupportEntryScope{{SourcePolicyID: 1, Applicable: true}}, nil, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(membership.Targets) != 2 || membership.Targets[0].PolicyID != 2 || membership.Targets[1].PolicyID != 8 {
		t.Fatalf("unsafe targets: %#v", membership.Targets)
	}
	if len(membership.CurrentGroups) != 2 || membership.CurrentGroups[0].Enabled || membership.CurrentGroups[1].Effective {
		t.Fatalf("current disabled/shadowed groups lost: %#v", membership.CurrentGroups)
	}
	if membership.Version < 0 || membership.Version >= 1<<53 {
		t.Fatalf("unsafe JS version %d", membership.Version)
	}
	raw, _ := json.Marshal(membership)
	if strings.Contains(string(raw), "u@example.com") || strings.Contains(string(raw), "MemberFingerprint") || strings.Contains(string(raw), "conditions") {
		t.Fatalf("raw email lists/config leaked: %s", raw)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
func TestSupportMembershipEmailEditPreservesAllOtherUsersConditionsAndInput(t *testing.T) {
	original, err := cliententry.DecodeConditions(`[{"field":"plan_id","operator":"eq","value":2},{"field":"email","operator":"in","values":["other@example.com"]},{"field":"ua","operator":"contains_any","values":["Clash"]}]`)
	if err != nil {
		t.Fatal(err)
	}
	policy := SupportEntryMembershipPolicy{PolicyID: 20, Conditions: original, EmailIndex: 1}
	raw, changed, err := SupportEntryPolicyEmailUpdate(policy, " User@Example.com ", true)
	if err != nil || !changed {
		t.Fatalf("add: %s,%v,%v", raw, changed, err)
	}
	after, err := cliententry.DecodeConditions(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 3 || len(after[1].Values) != 2 || len(original[1].Values) != 1 || after[0].Field != "plan_id" || after[2].Field != "ua" {
		t.Fatalf("unrelated fields/membership overwritten: %#v", after)
	}
	policy.Conditions = after
	raw, changed, err = SupportEntryPolicyEmailUpdate(policy, "user@example.com", false)
	if err != nil || !changed {
		t.Fatal(err)
	}
	after, err = cliententry.DecodeConditions(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(after[1].Values) != 1 || string(after[1].Values[0]) != `"other@example.com"` {
		t.Fatalf("another user's membership lost: %s", raw)
	}
}
func TestSupportMembershipLastExitRetainsDenyAllEmailConditionAndRealFirstMatchFallback(t *testing.T) {
	email, err := cliententry.DecodeConditions(`[{"field":"email","operator":"in","values":["u@example.com"]}]`)
	if err != nil {
		t.Fatal(err)
	}
	policy := SupportEntryMembershipPolicy{PolicyID: 20, Conditions: email, EmailIndex: 0}
	raw, changed, err := SupportEntryPolicyEmailUpdate(policy, "u@example.com", false)
	if err != nil || !changed {
		t.Fatal(err)
	}
	empty, err := cliententry.DecodeConditions(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty) != 1 || len(empty[0].Values) != 0 || !strings.Contains(raw, `"values":[]`) {
		t.Fatalf("last exit broadened match: %s", raw)
	}
	run := func(conditions []cliententry.Condition) []map[string]any {
		// The VIP and default have different full node member sets. No compatibility
		// override is involved: regular subscription first-match performs the switch.
		return applyClientEntryUserPolicies([]map[string]any{{"type": "vmess", "id": int64(1), "host": "original.example"}, {"type": "vmess", "id": int64(2), "host": "private.example", "client_entry_only": int64(1)}}, cliententry.Subject{UserID: 7, Email: "u@example.com"}, []clientEntryUserPolicy{{ID: 20, Mode: "standard", Action: "override", EntryHost: "vip.example", Conditions: conditions, Members: []ClientEntryGroupMember{{ServerType: "vmess", ServerID: 1}, {ServerType: "vmess", ServerID: 2}}}, {ID: 10, Mode: "standard", Action: "override", EntryHost: "default.example", Members: []ClientEntryGroupMember{{ServerType: "vmess", ServerID: 1}}}})
	}
	joined := run(email)
	if len(joined) != 2 || joined[0]["host"] != "vip.example" || joined[1]["host"] != "vip.example" {
		t.Fatalf("membership not applied by actual first-match: %#v", joined)
	}
	restored := run(empty)
	if len(restored) != 1 || restored[0]["host"] != "default.example" {
		t.Fatalf("last exit did not restore default/entry-only visibility: %#v", restored)
	}
}
func TestSupportMembershipVersionIsUserSpecificButTracksPolicyConfiguration(t *testing.T) {
	version := func(values string, sort int64, members string) int64 {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		mock.ExpectQuery(`(?s)SELECT p.id,p.name,p.enabled`).WillReturnRows(supportMembershipTestRows().AddRow(20, "VIP", 1, `[{"field":"email","operator":"in","values":`+values+`}]`, "vip.example", 0, true, sort, members))
		result, err := LoadSupportEntryMembership(context.Background(), db, cliententry.Subject{UserID: 7, Email: "u@example.com"}, nil, nil, 100)
		if err != nil {
			t.Fatal(err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
		return result.Version
	}
	base := version(`["other@example.com"]`, 10, "vmess:1")
	if version(`["other@example.com","another@example.com"]`, 10, "vmess:1") != base {
		t.Fatal("unrelated user's edit invalidates this user's version")
	}
	for _, next := range []int64{version(`["u@example.com","other@example.com"]`, 10, "vmess:1"), version(`["other@example.com"]`, 20, "vmess:1"), version(`["other@example.com"]`, 10, "vmess:1,vless:2")} {
		if next == base {
			t.Fatal("membership/sort/full member change did not update version")
		}
	}
}
