package aiops

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"forest/go-api/internal/admin"
)

type entryQueryHintPanel struct {
	admin.Service
	reads int
}

func TestEntryQueryToolsKeepOptionalFiltersOptional(t *testing.T) {
	for _, tool := range append(probeToolDefinitions(), entryInspectionToolDefinitions()...) {
		if tool.Function.Name != "entry_probe_targets" && tool.Function.Name != "host_references" {
			continue
		}
		if tool.Function.Strict == nil || *tool.Function.Strict {
			t.Fatal("relay must not force optional filters", tool.Function.Name)
		}
		var schema struct {
			Required []string `json:"required"`
		}
		if json.Unmarshal(tool.Function.Parameters, &schema) != nil {
			t.Fatal("invalid schema")
		}
		for _, key := range schema.Required {
			if key == "target_id" || key == "policy_id" || key == "revision" || key == "port" {
				t.Fatal("optional filter required", key)
			}
		}
	}
}

func (p *entryQueryHintPanel) ReadClientEntryMonitors(context.Context) (admin.ClientEntryMonitorOverview, error) {
	p.reads++
	return admin.ClientEntryMonitorOverview{
		Revision: 71,
		Policies: []admin.ClientEntryMonitorPolicy{{ID: 511, Enabled: true}},
		Items: []admin.ClientEntryMonitorRecord{{PolicyID: 511, Enabled: true, Targets: []admin.ClientEntryMonitorTarget{
			{ID: 13517644, Host: "192.0.2.10", Port: 443},
		}}},
	}, nil
}

func TestEntryProbeEmptyTargetExplainsDiscoveryWithoutWidening(t *testing.T) {
	p := &entryQueryHintPanel{}
	s := &Service{panel: p}
	for _, tc := range []struct {
		args     string
		total    int64
		wantHint string
	}{
		{`{"policy_id":511,"target_id":1}`, 0, "省略target_id"},
		{`{"policy_id":511,"target_id":511}`, 0, "省略target_id"},
		{`{"policy_id":999}`, 0, "entry_coverage"},
		{`{"policy_id":511}`, 1, ""},
		{`{"policy_id":511,"target_id":13517644}`, 1, ""},
		{`{"policy_id":511,"offset":20}`, 1, ""},
	} {
		out, err := s.callProbeTool(context.Background(), 1, 1, ToolCall{Function: ToolFunction{Name: "entry_probe_targets", Arguments: tc.args}})
		if err != nil {
			t.Fatal(err)
		}
		var v struct {
			Targets pageDTO[probeTargetDTO] `json:"targets"`
			Hint    string                  `json:"query_hint"`
		}
		if json.Unmarshal([]byte(out.Content), &v) != nil || v.Targets.Total != tc.total || !strings.Contains(v.Hint, tc.wantHint) {
			t.Fatalf("args %s: unexpected result %s", tc.args, out.Content)
		}
		if tc.wantHint == "" && v.Hint != "" {
			t.Fatal("successful/out-of-range page must not claim no configured targets")
		}
		if tc.total == 0 && len(v.Targets.Items) != 0 {
			t.Fatal("empty filter silently widened")
		}
	}
}

func TestEntryInspectionRevisionErrorGivesSafeCorrection(t *testing.T) {
	s := &Service{}
	for _, args := range []string{
		`{"host":"192.0.2.10","revision":"71"}`,
		`{"host":"192.0.2.10","revision":"SECRET_INVALID_REVISION"}`,
		`{"host":"192.0.2.10","offset":50}`,
	} {
		_, err := s.callEntryInspectionTool(context.Background(), ToolCall{Function: ToolFunction{Name: "host_references", Arguments: args}})
		if !errors.Is(err, ErrValidation) || !errors.Is(err, errEntryInspectionRevision) {
			t.Fatalf("expected typed validation before any backend access: %v", err)
		}
		message := toolErrorContent(safeAIError(err))
		if !strings.Contains(message, "首次查询请省略revision") || !strings.Contains(message, "invalid_arguments") || strings.Contains(message, "SECRET_INVALID_REVISION") {
			t.Fatalf("incorrect correction: %s", message)
		}
		if retryableReadToolError(context.Background(), err) {
			t.Fatal("validation must not retry unchanged")
		}
	}
	_, err := s.callEntryInspectionTool(context.Background(), ToolCall{Function: ToolFunction{Name: "host_references", Arguments: `{"host":"192.0.2.10"}`}})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("valid first page should reach backend lookup: %v", err)
	}
}
