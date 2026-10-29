package aiops

import (
	"context"
	"encoding/json"
	"forest/go-api/internal/admin"
	"forest/go-api/internal/cliententry"
	"forest/go-api/internal/config"
	"github.com/DATA-DOG/go-sqlmock"
	"strings"
	"testing"
)

type fakePanelOps struct {
	admin.Service
	called  int
	request admin.UserFetchRequest
}

func (f *fakePanelOps) ListUsers(_ context.Context, r admin.UserFetchRequest) (admin.UserListResult, error) {
	f.called++
	f.request = r
	return admin.UserListResult{Total: 1, Data: []map[string]any{{"id": int64(9), "email": "u@example.test", "password": "SENSITIVE", "uuid": "SENSITIVE", "token": "SENSITIVE", "subscribe_url": "SENSITIVE"}}}, nil
}
func TestAIToolsHaveNoApproveOrDirectExecute(t *testing.T) {
	s := NewService(config.Config{}, nil, nil)
	defer s.Close()
	defs := s.toolDefinitions()
	if len(defs) != 31 {
		t.Fatalf("tools %d", len(defs))
	}
	seen := map[string]bool{}
	for _, d := range defs {
		if seen[d.Function.Name] {
			t.Fatalf("duplicate tool %s", d.Function.Name)
		}
		seen[d.Function.Name] = true
		if strings.Contains(d.Function.Name, "approve") || strings.Contains(d.Function.Name, "execute") || d.Function.Name == "sql" {
			t.Fatal(d.Function.Name)
		}
		var schema map[string]any
		if json.Unmarshal(d.Function.Parameters, &schema) != nil || schema["additionalProperties"] != false {
			t.Fatal(string(d.Function.Parameters))
		}
	}
}
func TestAIUserLookupSanitizesAndRestricts(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	panel := &fakePanelOps{}
	s := NewService(config.Config{}, db, panel)
	defer s.Close()
	mock.ExpectQuery(`SELECT id FROM v2_user`).WithArgs(int64(1)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	v, err := s.callTool(context.Background(), 1, 2, ToolCall{ID: "call1", Function: ToolFunction{Name: "user_lookup", Arguments: `{"query":"U@example.test"}`}})
	if err != nil || strings.Contains(v.Content, "SENSITIVE") || v.Pending || len(panel.request.Filters) != 3 || panel.request.Filters[0].Condition != "=" {
		t.Fatalf("result=%+v err=%v filters=%+v", v, err, panel.request)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
func TestAISafeRuleProjection(t *testing.T) {
	r := admin.ClientEntryUserPolicyRecord{ExtraNodes: []string{"SENSITIVE"}, Conditions: []cliententry.Condition{{Field: "email", Values: []json.RawMessage{json.RawMessage(`"SENSITIVE"`)}}, {Field: "future", Values: []json.RawMessage{json.RawMessage(`"SENSITIVE"`)}}}}
	raw, _ := json.Marshal(projectRule(r))
	if strings.Contains(string(raw), "SENSITIVE") || !strings.Contains(string(raw), "email_count") {
		t.Fatal(string(raw))
	}
}

func TestAIToolArgumentsEnforcePerToolSchema(t *testing.T) {
	s := NewService(config.Config{}, nil, nil)
	defer s.Close()
	for _, test := range []struct {
		name, args string
		want       bool
	}{
		{"panel_status", `{}`, true},
		{"panel_status", `{"command":"id"}`, false},
		{"user_lookup", `{"query":"a@example.test"}`, true},
		{"user_lookup", `{}`, false},
		{"user_lookup", `{"query":null}`, false},
		{"user_lookup", `{"query":"a@example.test","user_id":1}`, false},
		{"entry_simulate", `{"email":"a@example.test","ua":""}`, true},
		{"entry_simulate", `{"email":"a@example.test"}`, false},
		{"nodes_list", `null`, false},
		{"nodes_list", `[]`, false},
		{"not_a_tool", `{}`, false},
	} {
		if got := s.validToolArgumentKeys(test.name, test.args); got != test.want {
			t.Errorf("%s %s = %v", test.name, test.args, got)
		}
	}
}
