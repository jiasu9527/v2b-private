package aiops

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"forest/go-api/internal/config"
	"github.com/DATA-DOG/go-sqlmock"
)

func TestAIHistoryContinueRetainsFailedTaskWithoutPartialTools(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := NewService(config.Config{}, db, nil)
	defer s.Close()
	rows := sqlmock.NewRows([]string{"turn_id", "status", "role", "content", "tool_calls", "tool_call_id"}).
		AddRow(1, "completed", "user", "先前完整请求", `[]`, "").
		AddRow(1, "completed", "assistant", "", `[{"id":"complete-call","type":"function","function":{"name":"panel_status","arguments":"{}"}}]`, "").
		AddRow(1, "completed", "tool", `{"healthy":true}`, `[]`, "complete-call").
		AddRow(1, "completed", "assistant", "先前已完成", `[]`, "").
		AddRow(2, "failed", "user", "查明 54101 端口对应的落地服务器", `[]`, "").
		// SQL excludes these rows. The projection also guards against a legacy
		// or mocked source returning an incomplete assistant/result sequence.
		AddRow(2, "failed", "assistant", "PARTIAL-ASSISTANT", `[{"id":"unfinished","type":"function","function":{"name":"ssh_status","arguments":"{}"}}]`, "").
		AddRow(2, "failed", "tool", "PARTIAL-ORPHAN-RESULT", `[]`, "other-id").
		AddRow(3, "interrupted", "user", "继续，保留现有中转配置", `[]`, "").
		AddRow(3, "interrupted", "assistant", "INTERRUPTED-ASSISTANT", `[`, "").
		AddRow(4, "failed", "user", "继续", `[]`, "").
		AddRow(5, "running", "user", "继续", `[]`, "")
	mock.ExpectQuery(`SELECT turn.id,turn.status,.*message.role='user'.*status IN\('failed','interrupted'\).*ORDER BY message.id`).WithArgs(int64(9), int64(5)).WillReturnRows(rows)
	mock.ExpectQuery(`SELECT id,kind,status,output,error FROM v2_ai_action`).WithArgs(int64(9)).WillReturnRows(sqlmock.NewRows([]string{"id", "kind", "status", "output", "error"}).
		AddRow(88, "ssh_command", "unknown", "端口读取到一部分", "断线，结果未知").
		AddRow(87, "node_update", "succeeded", "连接端口已更新为 54101", ""))
	messages, err := s.history(context.Background(), 9, 5)
	if err != nil {
		t.Fatal(err)
	}
	assertContextToolPairs(t, messages)
	encoded, _ := json.Marshal(messages)
	for _, want := range []string{"54101 端口对应的落地服务器", "保留现有中转配置", "先前已完成", "该轮未完成", "部分操作可能已经执行", "实际状态以最新操作摘要为准", "不得自动重试", "unknown", "succeeded"} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("missing task or execution truth %q", want)
		}
	}
	for _, forbidden := range []string{"PARTIAL-ASSISTANT", "PARTIAL-ORPHAN-RESULT", "INTERRUPTED-ASSISTANT", "unfinished", "other-id"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("failed tool fragment reached model: %q", forbidden)
		}
	}
	requests := []string{}
	for _, message := range messages {
		if message.Role == "user" && message.contextKind != "actions" {
			requests = append(requests, message.Content)
		}
	}
	if len(requests) != 5 || requests[len(requests)-1] != "继续" {
		t.Fatalf("current continuation lost: %v", requests)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
