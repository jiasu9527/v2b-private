package aiops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/cliententry"
	"forest/go-api/internal/config"
	"github.com/DATA-DOG/go-sqlmock"
)

type largeToolPanel struct {
	admin.Service
	rules  []admin.ClientEntryUserPolicyRecord
	health admin.SupportEntryOverview
}

func (p *largeToolPanel) ListClientEntryUserPolicies(context.Context) ([]admin.ClientEntryUserPolicyRecord, error) {
	return p.rules, nil
}
func (p *largeToolPanel) SupportEntryOverview(context.Context) (admin.SupportEntryOverview, error) {
	return p.health, nil
}

func largeRuleFixture(id int64) admin.ClientEntryUserPolicyRecord {
	long := strings.Repeat("入口🚀<&", 100)
	r := admin.ClientEntryUserPolicyRecord{ID: id, Name: long, Mode: "split", Action: "override", EntryHost: "192.0.2.10", Enabled: 1, SnapshotUserCount: 1000}
	value, _ := json.Marshal(long)
	for i := 0; i < 25; i++ {
		c := cliententry.Condition{Field: "ua", Operator: "contains_any", Min: value, Max: value, Value: value}
		for j := 0; j < 30; j++ {
			c.Values = append(c.Values, value)
		}
		r.Conditions = append(r.Conditions, c)
	}
	for i := 0; i < 80; i++ {
		r.SplitGroups = append(r.SplitGroups, admin.ClientEntryUserPolicySplitGroupRecord{ID: int64(i + 1), Name: long, Path: long, EntryHost: long, UserCount: 100, IsLeaf: true})
		r.Members = append(r.Members, admin.ClientEntryGroupMemberRecord{ServerID: int64(i + 1), ServerType: "vmess"})
	}
	return r
}

func requireBoundedJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > maxToolOutputBytes || !utf8.Valid(raw) || !json.Valid(raw) {
		t.Fatalf("invalid tool result err=%v bytes=%d", err, len(raw))
	}
	return raw
}

func TestAIToolRulesAdaptivePaginationDoesNotSkipOrLoseWholeRule(t *testing.T) {
	panel := &largeToolPanel{}
	for i := 0; i < 23; i++ {
		panel.rules = append(panel.rules, largeRuleFixture(int64(i+1)))
	}
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := NewService(config.Config{}, db, panel)
	defer s.Close()
	offset, seen := 0, []int64{}
	for attempts := 0; attempts < 30; attempts++ {
		mock.ExpectQuery(`SELECT id FROM v2_user`).WithArgs(int64(1)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
		result, err := s.callTool(context.Background(), 1, 2, ToolCall{ID: "rules", Function: ToolFunction{Name: "entry_rules_list", Arguments: fmt.Sprintf(`{"limit":50,"offset":%d}`, offset)}})
		if err != nil || result.Pending || len(result.Content) > maxToolOutputBytes {
			t.Fatalf("tool failure err=%v bytes=%d", err, len(result.Content))
		}
		var page pageDTO[ruleDTO]
		if json.Unmarshal([]byte(result.Content), &page) != nil || len(page.Items) == 0 || page.Total != 23 || page.Offset != offset {
			t.Fatalf("missing page offset=%d bytes=%d", offset, len(result.Content))
		}
		for _, item := range page.Items {
			if item.ID != int64(len(seen)+1) {
				t.Fatalf("skipped/duplicated ID=%d seen=%d", item.ID, len(seen))
			}
			if item.GroupsTotal != 80 || !item.GroupsTruncated || item.ConditionsTotal != 25 || !item.ConditionsTruncated || item.MembersTotal != 80 || !item.MembersTruncated {
				t.Fatal("nested summary count/truncation lost")
			}
			if jsonSize(item) > maxToolItemBytes || !utf8.ValidString(item.Name) {
				t.Fatal("invalid oversized summary")
			}
			seen = append(seen, item.ID)
		}
		requireBoundedJSON(t, page)
		if !page.Truncated {
			if page.NextOffset != nil {
				t.Fatal("final page has next offset")
			}
			break
		}
		if !page.SizeLimited || page.NextOffset == nil || *page.NextOffset != offset+len(page.Items) {
			t.Fatal("adaptive page did not advertise actual continuation")
		}
		offset = *page.NextOffset
	}
	if len(seen) != 23 {
		t.Fatalf("lost records: %d", len(seen))
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAIToolHealthAdaptivePaginationPreservesSummaryAndUnicode(t *testing.T) {
	long := strings.Repeat("检测🚀<&", 150)
	v := admin.SupportEntryOverview{Coverage: "entry_assignment", GeneratedAt: 100}
	for i := 0; i < 30; i++ {
		item := admin.SupportEntryItem{Key: fmt.Sprintf("tcp:192.0.2.%d:54101", i), Host: fmt.Sprintf("192.0.2.%d", i), Port: 54101, Status: "healthy", StatusReason: long}
		for j := 0; j < 100; j++ {
			item.Sources = append(item.Sources, admin.SupportEntrySource{PolicyID: int64(j + 1), PolicyName: long, SourceKey: long, Status: "healthy", PolicyEnabled: true})
			item.Probes = append(item.Probes, admin.SupportEntryProbe{ProbeID: int64(j + 1), ProbeName: long, Online: true, Status: "healthy"})
		}
		v.Items = append(v.Items, item)
	}
	v.Summary.Total = 30
	v.Summary.Healthy = 30
	offset, seen := 0, 0
	for attempts := 0; attempts < 31; attempts++ {
		out := projectHealth(v, "", 50, offset)
		requireBoundedJSON(t, out)
		if out.Summary.Total != 30 || out.Summary.Healthy != 30 || out.Entries.Total != 30 || len(out.Entries.Items) == 0 {
			t.Fatal("health summary lost")
		}
		for _, item := range out.Entries.Items {
			if item.Host != fmt.Sprintf("192.0.2.%d", seen) || item.SourceCount != 100 || item.ProbeCount != 100 || !item.SourcesTruncated || !item.ProbesTruncated {
				t.Fatal("health pagination/summary mismatch")
			}
			seen++
		}
		if !out.Entries.Truncated {
			break
		}
		if out.Entries.NextOffset == nil || *out.Entries.NextOffset != seen {
			t.Fatal("health offset skips records")
		}
		offset = *out.Entries.NextOffset
	}
	if seen != 30 {
		t.Fatalf("missing health entries: %d", seen)
	}
}

func TestAIToolBudgetEmptyAndBoundaryPages(t *testing.T) {
	for _, offset := range []int{0, 5, 10000} {
		page := boundedPage([]ruleDTO{}, 0, 50, offset, maxToolOutputBytes)
		raw := requireBoundedJSON(t, page)
		if page.Truncated || page.NextOffset != nil || page.SizeLimited || !strings.Contains(string(raw), `"items":[]`) {
			t.Fatal("empty page is misleading")
		}
	}
	rule := projectRule(largeRuleFixture(1))
	sim := simulationDTO{Found: true, User: &simulationUserDTO{ID: 1, Email: "test@example.test"}, Matched: &rule}
	requireBoundedJSON(t, sim)
	page := boundedPage([]ruleDTO{rule}, 1, 1, 0, maxToolOutputBytes)
	if len(page.Items) != 1 || page.Truncated || page.NextOffset != nil {
		t.Fatal("single large rule disappeared")
	}
	requireBoundedJSON(t, page)
	values := []string{}
	for i := 0; i < 100; i++ {
		values = append(values, strings.Repeat("<&🚀", 128))
	}
	node := projectNode(map[string]any{"id": int64(1), "group_id": values})
	if !node.GroupIDsTruncated {
		t.Fatal("node group trimming not marked")
	}
	requireBoundedJSON(t, node)
}

func TestAIToolErrorsContainOnlySafeCategories(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code string
	}{
		{fmt.Errorf("secret %w", ErrValidation), "invalid_arguments"},
		{fmt.Errorf("secret %w", ErrConflict), "conflict"},
		{ErrForbidden, "forbidden"}, {ErrNotFound, "not_found"}, {ErrBusy, "busy"},
		{safeAIError(fmt.Errorf("secret %w", context.DeadlineExceeded)), "timeout"},
		{errors.New("secret sql password"), "unavailable"},
	} {
		raw := toolErrorContent(tc.err)
		if !json.Valid([]byte(raw)) || strings.Contains(raw, "secret") || !strings.Contains(raw, `"code":"`+tc.code+`"`) {
			t.Fatal("unsafe/missing classification", raw)
		}
	}
}

func TestAIActionSummaryDoesNotDuplicateOversizedApprovalPayload(t *testing.T) {
	payload, _ := json.Marshal(map[string]string{"command": strings.Repeat("<&", 8000)})
	result := actionToolSummary(Action{ID: 42, SessionID: 5, Kind: "ssh_command", Status: "pending", TargetID: 8, ExpiresAt: 123, Payload: payload})
	raw := requireBoundedJSON(t, result)
	if len(raw) > 1000 || result["action_id"] != int64(42) || result["status"] != "pending" || strings.Contains(string(raw), "command") && strings.Contains(string(raw), "&") {
		t.Fatal("pending summary lost or copied payload")
	}
	if _, exists := result["payload"]; exists {
		t.Fatal("full payload must stay in human review card")
	}
}
