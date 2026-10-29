package aiops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/config"
	"github.com/DATA-DOG/go-sqlmock"
)

type paginationToolPanel struct {
	admin.Service
	rules  []admin.ClientEntryUserPolicyRecord
	health admin.SupportEntryOverview
	calls  int
}

func (p *paginationToolPanel) ListClientEntryUserPolicies(context.Context) ([]admin.ClientEntryUserPolicyRecord, error) {
	p.calls++
	return p.rules, nil
}
func (p *paginationToolPanel) SupportEntryOverview(context.Context) (admin.SupportEntryOverview, error) {
	p.calls++
	return p.health, nil
}

func newPaginationToolFixture(t *testing.T) (*Service, sqlmock.Sqlmock, *paginationToolPanel) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	panel := &paginationToolPanel{}
	for id := 1; id <= 60; id++ {
		panel.rules = append(panel.rules, admin.ClientEntryUserPolicyRecord{ID: int64(id), Name: fmt.Sprint(id), Mode: "standard", Enabled: 1})
		panel.health.Items = append(panel.health.Items, admin.SupportEntryItem{Key: fmt.Sprint(id), Host: fmt.Sprintf("192.0.2.%d", id), Port: 54101, Status: "healthy"})
	}
	panel.health.Summary.Total = 60
	panel.health.Summary.Healthy = 60
	s := NewService(config.Config{}, db, panel)
	t.Cleanup(func() {
		s.Close()
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
		db.Close()
	})
	return s, mock, panel
}

func paginationToolCall(t *testing.T, s *Service, mock sqlmock.Sqlmock, name, arguments string) (ToolOutcome, error) {
	t.Helper()
	mock.ExpectQuery(`SELECT id FROM v2_user`).WithArgs(int64(1)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	mock.ExpectQuery(`SELECT s.id FROM v2_ai_session s JOIN v2_user`).WithArgs(int64(2), int64(1)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(2))
	return s.callTool(context.Background(), 1, 2, ToolCall{ID: "pagination-fixture", Function: ToolFunction{Name: name, Arguments: arguments}})
}

func paginationToolPage(t *testing.T, name, content string) pageDTO[json.RawMessage] {
	t.Helper()
	var page pageDTO[json.RawMessage]
	if name == "entry_health" {
		var health struct {
			Entries pageDTO[json.RawMessage] `json:"entries"`
		}
		if err := json.Unmarshal([]byte(content), &health); err != nil {
			t.Fatal(err)
		}
		page = health.Entries
	} else if err := json.Unmarshal([]byte(content), &page); err != nil {
		t.Fatal(err)
	}
	return page
}

func TestAIToolOversizedReadLimitContinuesWithoutSkipping(t *testing.T) {
	for _, name := range []string{"entry_rules_list", "entry_health"} {
		t.Run(name, func(t *testing.T) {
			s, mock, panel := newPaginationToolFixture(t)
			offset, seen := 0, 0
			for pageNumber := 0; pageNumber < 2; pageNumber++ {
				result, err := paginationToolCall(t, s, mock, name, fmt.Sprintf(`{"limit":100,"offset":%d}`, offset))
				if err != nil || result.Pending {
					t.Fatalf("read query failed or created pending operation: %v", err)
				}
				page := paginationToolPage(t, name, result.Content)
				wantCount := 50
				if pageNumber == 1 {
					wantCount = 10
				}
				if page.Limit != 50 || page.Total != 60 || page.Offset != offset || len(page.Items) != wantCount {
					t.Fatalf("wrong effective page: limit=%d total=%d offset=%d items=%d", page.Limit, page.Total, page.Offset, len(page.Items))
				}
				for _, raw := range page.Items {
					var item struct {
						ID  int64  `json:"id"`
						Key string `json:"key"`
					}
					if err := json.Unmarshal(raw, &item); err != nil {
						t.Fatal(err)
					}
					seen++
					if (name == "entry_rules_list" && item.ID != int64(seen)) || (name == "entry_health" && item.Key != fmt.Sprint(seen)) {
						t.Fatalf("skipped or duplicated record at position %d", seen)
					}
				}
				if pageNumber == 0 {
					if !page.Truncated || page.NextOffset == nil || *page.NextOffset != 50 {
						t.Fatal("missing actual continuation offset")
					}
					offset = *page.NextOffset
				} else if page.Truncated || page.NextOffset != nil {
					t.Fatal("final page advertises nonexistent continuation")
				}
			}
			if panel.calls != 2 || seen != 60 {
				t.Fatalf("read count=%d seen=%d", panel.calls, seen)
			}
		})
	}
}

func TestAIToolDefaultReadLimitPreserved(t *testing.T) {
	for _, name := range []string{"entry_rules_list", "entry_health"} {
		for _, arguments := range []string{`{}`, `{"limit":0}`} {
			t.Run(name+arguments, func(t *testing.T) {
				s, mock, panel := newPaginationToolFixture(t)
				result, err := paginationToolCall(t, s, mock, name, arguments)
				if err != nil || result.Pending {
					t.Fatalf("default read failed: %v", err)
				}
				page := paginationToolPage(t, name, result.Content)
				if page.Limit != 20 || len(page.Items) != 20 || page.NextOffset == nil || *page.NextOffset != 20 || panel.calls != 1 {
					t.Fatal("default page behavior changed")
				}
			})
		}
	}
}

func TestAIToolLimitCompatibilityDoesNotWeakenValidation(t *testing.T) {
	for _, name := range []string{"entry_rules_list", "entry_health"} {
		for _, arguments := range []string{
			`{"limit":-1}`, `{"limit":"100"}`, `{"limit":null}`, `{"limit":100.5}`,
			`{"limit":100,"offset":-1}`, `{"limit":100,"offset":10001}`, `{"limit":100,"unknown":1}`,
		} {
			t.Run(name+arguments, func(t *testing.T) {
				s, mock, panel := newPaginationToolFixture(t)
				result, err := paginationToolCall(t, s, mock, name, arguments)
				if !errors.Is(err, ErrValidation) || result.Pending || panel.calls != 0 {
					t.Fatalf("invalid query reached panel/action: err=%v pending=%v calls=%d", err, result.Pending, panel.calls)
				}
			})
		}
	}
	for _, test := range []struct{ name, arguments string }{
		{"monitor_run", `{"policy_ids":[1],"reason":"fixture","limit":100}`},
		{"propose_ssh_command", `{"server_id":1,"command":"uname -a","reason":"fixture","limit":100}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, mock, panel := newPaginationToolFixture(t)
			result, err := paginationToolCall(t, s, mock, test.name, test.arguments)
			// Only identity and session ownership lookups are expected in sqlmock. No proposal/schema
			// write may run, even though a read-only page would accept limit=100.
			if !errors.Is(err, ErrValidation) || result.Pending || panel.calls != 0 {
				t.Fatalf("undeclared mutation argument accepted: err=%v pending=%v calls=%d", err, result.Pending, panel.calls)
			}
		})
	}
}
