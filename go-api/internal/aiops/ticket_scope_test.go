package aiops

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"forest/go-api/internal/config"
	"github.com/DATA-DOG/go-sqlmock"
)

func TestAITicketGlobalSchemaHasExplicitUnfilteredChoices(t *testing.T) {
	found := false
	for _, tool := range ticketToolDefinitions() {
		wire, err := json.Marshal(tool)
		if err != nil {
			t.Fatal(err)
		}
		var encoded struct {
			Function struct {
				Strict *bool `json:"strict"`
			} `json:"function"`
		}
		if json.Unmarshal(wire, &encoded) != nil || encoded.Function.Strict == nil || *encoded.Function.Strict {
			t.Fatal("ticket compatibility flag is absent on wire", tool.Function.Name)
		}
		if tool.Function.Name != "tickets_list_global" {
			continue
		}
		found = true
		var schema struct {
			Type                 string   `json:"type"`
			AdditionalProperties bool     `json:"additionalProperties"`
			Required             []string `json:"required"`
			Properties           map[string]struct {
				Type    string   `json:"type"`
				Enum    []string `json:"enum"`
				Minimum int      `json:"minimum"`
				Maximum int      `json:"maximum"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(tool.Function.Parameters, &schema); err != nil {
			t.Fatal(err)
		}
		sort.Strings(schema.Required)
		if schema.Type != "object" || schema.AdditionalProperties || !reflect.DeepEqual(schema.Required, []string{"limit", "offset", "reply_status", "status"}) || len(schema.Properties) != 4 {
			t.Fatal("global schema must be identity-free and complete under strict validation", schema)
		}
		for _, key := range schema.Required {
			if _, ok := schema.Properties[key]; !ok {
				t.Fatal("required property is missing", key)
			}
		}
		if schema.Properties["status"].Type != "string" || !reflect.DeepEqual(schema.Properties["status"].Enum, []string{"all", "open", "closed"}) || schema.Properties["reply_status"].Type != "string" || !reflect.DeepEqual(schema.Properties["reply_status"].Enum, []string{"all", "pending", "replied"}) {
			t.Fatal("global status selectors lack explicit no-filter choices", schema.Properties)
		}
		if schema.Properties["limit"].Type != "integer" || schema.Properties["limit"].Minimum != 1 || schema.Properties["limit"].Maximum != 50 || schema.Properties["offset"].Type != "integer" || schema.Properties["offset"].Minimum != 0 || schema.Properties["offset"].Maximum != 10000 {
			t.Fatal("pagination schema contract changed", schema.Properties)
		}
	}
	if !found {
		t.Fatal("missing explicit global ticket query")
	}
}

func TestAITicketGlobalFiltersMapWithoutInventingIdentity(t *testing.T) {
	for _, status := range []string{"all", "open", "closed"} {
		for _, reply := range []string{"all", "pending", "replied"} {
			t.Run(status+"/"+reply, func(t *testing.T) {
				args, err := decodeTicketArgs("tickets_list_global", fmt.Sprintf(`{"status":%q,"reply_status":%q,"limit":50,"offset":0}`, status, reply))
				if err != nil || args.UserID != 0 || args.Email != "" || args.Limit != 50 || args.Offset != 0 {
					t.Fatal("global query added identity or changed pagination", args, err)
				}
				for _, check := range []struct {
					value *int64
					name  string
					zero  string
				}{{args.Status, status, "open"}, {args.ReplyStatus, reply, "pending"}} {
					if check.name == "all" {
						if check.value != nil {
							t.Fatal("all must not apply a SQL predicate", check)
						}
					} else {
						want := int64(1)
						if check.name == check.zero {
							want = 0
						}
						if check.value == nil || *check.value != want {
							t.Fatal("incorrect SQL predicate mapping", check)
						}
					}
				}
			})
		}
	}
}

func TestAITicketListScopeSQLAndDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name, tool, args, where string
		params                  []driver.Value
		total                   int64
		limit, offset           int
		query                   aiTicketQueryDTO
		empty                   string
		items                   []aiTicketDTO
	}{
		{name: "global open includes replied customers", tool: "tickets_list_global", args: `{"status":"open","reply_status":"all","limit":50,"offset":0}`, where: "TRUE AND t.status=$1", params: []driver.Value{int64(0)}, total: 3, limit: 50, query: aiTicketQueryDTO{Scope: "all_users", Status: "open", ReplyStatus: "all"}, items: []aiTicketDTO{{ID: 3, UserID: 30, Status: 0, ReplyStatus: 1}, {ID: 2, UserID: 20, Status: 0, ReplyStatus: 0}, {ID: 1, UserID: 1, Status: 0, ReplyStatus: 1}}},
		{name: "global all has no predicates", tool: "tickets_list_global", args: `{"status":"all","reply_status":"all","limit":20,"offset":0}`, where: "TRUE", total: 0, limit: 20, query: aiTicketQueryDTO{Scope: "all_users", Status: "all", ReplyStatus: "all"}, empty: "no_matching_tickets"},
		{name: "global closed replied exact predicates", tool: "tickets_list_global", args: `{"status":"closed","reply_status":"replied","limit":2,"offset":4}`, where: "TRUE AND t.status=$1 AND t.reply_status=$2", params: []driver.Value{int64(1), int64(1)}, total: 3, limit: 2, offset: 4, query: aiTicketQueryDTO{Scope: "all_users", Status: "closed", ReplyStatus: "replied"}, empty: "offset_out_of_range"},
		{name: "global pending has only reply predicate", tool: "tickets_list_global", args: `{"status":"all","reply_status":"pending","limit":2,"offset":0}`, where: "TRUE AND t.reply_status=$1", params: []driver.Value{int64(0)}, total: 0, limit: 2, query: aiTicketQueryDTO{Scope: "all_users", Status: "all", ReplyStatus: "pending"}, empty: "no_matching_tickets"},
		{name: "observed user one placeholder remains a real exact filter", tool: "tickets_list", args: `{"user_id":1,"email":"","status":0,"reply_status":0,"limit":50,"offset":0}`, where: "TRUE AND t.user_id=$1 AND t.status=$2 AND t.reply_status=$3", params: []driver.Value{int64(1), int64(0), int64(0)}, total: 0, limit: 50, query: aiTicketQueryDTO{Scope: "filtered_users", Status: "open", ReplyStatus: "pending", UserID: 1}, empty: "no_matching_tickets"},
		{name: "user one can actually have matching tickets", tool: "tickets_list", args: `{"user_id":1}`, where: "TRUE AND t.user_id=$1", params: []driver.Value{int64(1)}, total: 1, limit: 20, query: aiTicketQueryDTO{Scope: "filtered_users", Status: "all", ReplyStatus: "all", UserID: 1}, items: []aiTicketDTO{{ID: 1, UserID: 1, Status: 1, ReplyStatus: 1}}},
		{name: "email and id remain intersected and normalized", tool: "tickets_list", args: `{"user_id":1,"email":" User@Example.Test ","limit":100}`, where: "TRUE AND t.user_id=$1 AND LOWER(u.email)=$2", params: []driver.Value{int64(1), "user@example.test"}, total: 0, limit: 50, query: aiTicketQueryDTO{Scope: "filtered_users", Status: "all", ReplyStatus: "all", UserID: 1, Email: "user@example.test"}, empty: "no_matching_tickets"},
		{name: "legacy omitted identity still global", tool: "tickets_list", args: `{"status":0}`, where: "TRUE AND t.status=$1", params: []driver.Value{int64(0)}, total: 2, limit: 20, query: aiTicketQueryDTO{Scope: "all_users", Status: "open", ReplyStatus: "all"}, empty: "empty_page"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			s := NewService(config.Config{}, db, nil)
			defer s.Close()
			where := ` FROM v2_ticket t LEFT JOIN v2_user u ON u.id=t.user_id WHERE ` + tc.where
			mock.ExpectQuery(`SELECT COUNT(*)` + where).WithArgs(tc.params...).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(tc.total))
			params := append(append([]driver.Value{}, tc.params...), int64(tc.limit), int64(tc.offset))
			rows := sqlmock.NewRows([]string{"id", "user_id", "email", "subject", "level", "status", "reply_status", "created_at", "updated_at"})
			for _, item := range tc.items {
				rows.AddRow(item.ID, item.UserID, item.Email, item.Subject, item.Level, item.Status, item.ReplyStatus, item.CreatedAt, item.UpdatedAt)
			}
			mock.ExpectQuery(`SELECT ` + aiTicketColumns + where + fmt.Sprintf(` ORDER BY t.id DESC LIMIT $%d OFFSET $%d`, len(params)-1, len(params))).WithArgs(params...).WillReturnRows(rows)
			outcome, err := s.callTicketTool(context.Background(), 99, 100, ToolCall{Function: ToolFunction{Name: tc.tool, Arguments: tc.args}})
			if err != nil {
				t.Fatal(err)
			}
			var result aiTicketListDTO
			if err := json.Unmarshal([]byte(outcome.Content), &result); err != nil {
				t.Fatal(err)
			}
			if result.Query != tc.query || result.EmptyReason != tc.empty || result.Total != tc.total || result.Limit != tc.limit || result.Offset != tc.offset || len(result.Items) != len(tc.items) || outcome.Pending || outcome.ActionID != 0 {
				t.Fatal("incorrect query scope or result", result)
			}
			if len(tc.items) > 0 && !reflect.DeepEqual(tc.items, result.Items) {
				t.Fatal("ticket identity/status altered", result.Items)
			}
			var wire map[string]json.RawMessage
			if err := json.Unmarshal([]byte(outcome.Content), &wire); err != nil {
				t.Fatal(err)
			}
			if _, ok := wire["items"]; !ok {
				t.Fatal("legacy top-level page envelope changed")
			}
			if result.Query.UserID == 0 && strings.Contains(string(wire["query"]), `"user_id"`) {
				t.Fatal("query invented an unapplied user filter")
			}
			if result.Query.Email == "" && strings.Contains(string(wire["query"]), `"email"`) {
				t.Fatal("query invented an unapplied email filter")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAITicketScopeMetadataReservesPageBudget(t *testing.T) {
	items := make([]aiTicketDTO, 50)
	for i := range items {
		items[i] = aiTicketDTO{ID: int64(50 - i), UserID: 1}
	}
	remaining := maxToolOutputBytes - 1 - jsonSize(makePage(items, 50, 50, 0))
	for i := range items {
		length := remaining / (len(items) - i)
		items[i].Subject = strings.Repeat("x", length)
		remaining -= length
	}
	if size := jsonSize(makePage(items, 50, 50, 0)); size != maxToolOutputBytes-1 {
		t.Fatal("fixture should fill the original page budget", size)
	}
	result := ticketListResult(items, 50, ticketToolArgs{Limit: 50, UserID: 1, Email: "user@example.test"})
	if size := jsonSize(result); size > maxToolOutputBytes {
		t.Fatal("diagnostics exceeded the tool output budget", size)
	}
	if !result.Truncated || !result.SizeLimited || len(result.Items) >= len(items) || result.NextOffset == nil || *result.NextOffset != len(result.Items) || result.Query.Scope != "filtered_users" || result.Query.UserID != 1 || result.Query.Email != "user@example.test" || result.Total != 50 || result.EmptyReason != "" {
		t.Fatal("page budget lost continuation or scope", result)
	}
}
