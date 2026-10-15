package user

import (
	"context"
	"encoding/json"
	"fmt"
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
