package aiops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"reflect"
	"strings"
	"testing"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/config"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestReadToolRetrySuccessPreservesOutcome(t *testing.T) {
	var calls, guards int
	var waits []int
	want := ToolOutcome{Content: `{"items":[{"id":3}]}`}
	got, err := retryReadTool(context.Background(), func() bool { return true }, func(context.Context) error { guards++; return nil }, func() (ToolOutcome, error) {
		calls++
		if calls < 3 {
			return ToolOutcome{}, &PanelOperationError{Status: 503}
		}
		return want, nil
	}, func(_ context.Context, attempt int) error { waits = append(waits, attempt); return nil })
	if err != nil || !reflect.DeepEqual(got, want) || calls != 3 || guards != 3 || !reflect.DeepEqual(waits, []int{1, 2}) {
		t.Fatalf("outcome=%+v err=%v calls=%d guards=%d waits=%v", got, err, calls, guards, waits)
	}
}

func TestReadToolRetryExhaustionSafeAndBounded(t *testing.T) {
	calls := 0
	_, err := retryReadTool(context.Background(), func() bool { return true }, nil, func() (ToolOutcome, error) {
		calls++
		return ToolOutcome{}, &PanelOperationError{Status: 500, Message: "PRIVATE_SENTINEL raw SQL"}
	}, func(context.Context, int) error { return nil })
	var exhausted *readToolRetryExhausted
	if !errors.As(err, &exhausted) || calls != 3 || exhausted.attempts != 3 {
		t.Fatal(calls, err)
	}
	content := toolErrorContent(safeAIError(err))
	if !strings.Contains(content, `"code":"read_retry_exhausted"`) || !strings.Contains(content, "3 次") || strings.Contains(content, "PRIVATE_SENTINEL") {
		t.Fatal(content)
	}
}

func TestReadToolRetryNeverReplaysActionsOrUnreviewedCalls(t *testing.T) {
	for _, tc := range []struct {
		name string
		read bool
		out  ToolOutcome
	}{
		{"unreviewed or writing tool", false, ToolOutcome{}},
		{"pending action despite read descriptor", true, ToolOutcome{ActionID: 7, Pending: true}},
		{"action result despite read descriptor", true, ToolOutcome{ActionID: 7}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			out, err := retryReadTool(context.Background(), func() bool { return tc.read }, nil, func() (ToolOutcome, error) { calls++; return tc.out, ErrUnavailable }, func(context.Context, int) error {
				t.Fatal("unsafe operation entered retry backoff")
				return nil
			})
			if !errors.Is(err, ErrUnavailable) || calls != 1 || !reflect.DeepEqual(out, tc.out) {
				t.Fatal(out, calls, err)
			}
		})
	}
}

func TestReadToolRetryHonorsCancellationGuardAndDirectoryDrift(t *testing.T) {
	for _, mode := range []string{"cancel", "guard", "directory"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls, guards, wait := 0, 0, false
			_, err := retryReadTool(ctx, func() bool { return mode != "directory" || !wait }, func(context.Context) error {
				guards++
				if mode == "guard" && wait {
					return ErrForbidden
				}
				return nil
			}, func() (ToolOutcome, error) { calls++; return ToolOutcome{}, ErrUnavailable }, func(context.Context, int) error {
				wait = true
				if mode == "cancel" {
					cancel()
				}
				return nil
			})
			want := map[string]error{"cancel": context.Canceled, "guard": ErrForbidden, "directory": ErrConflict}[mode]
			if !errors.Is(err, want) || calls != 1 || guards > 2 {
				t.Fatal(mode, calls, guards, err)
			}
		})
	}
}

func TestReadToolRetryClassifiesOnlyTemporaryReads(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 409, 422, 408, 429, 500, 502, 503, 504} {
		want := status == 408 || status == 429 || status == 500 || status == 502 || status == 503 || status == 504
		if got := retryableReadToolError(context.Background(), &PanelOperationError{Status: status}); got != want {
			t.Fatal(status, got, want)
		}
	}
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{ErrForbidden, false}, {ErrValidation, false}, {ErrNotFound, false}, {ErrConflict, false}, {context.Canceled, false},
		{errors.New("PRIVATE_SENTINEL please retry"), false}, {nil, false},
		{ErrUnavailable, true}, {ErrBusy, true}, {context.DeadlineExceeded, true}, {io.EOF, true},
		{&net.DNSError{IsTimeout: true}, true}, {&pgconn.PgError{Code: "40001", Message: "PRIVATE_SENTINEL"}, true}, {&pgconn.PgError{Code: "23505"}, false},
		{&PanelOperationError{Status: 503, NotExecuted: true, Retryable: false}, false},
		{&PanelOperationError{Status: 503, NotExecuted: true, Retryable: true}, false},
		{&PanelOperationError{Status: 422, NotExecuted: true, Retryable: true}, false},
		{&PanelOperationError{Status: 422, Code: "dns_domain_metadata", NotExecuted: true, Retryable: true}, true},
		{&PanelOperationError{Status: 503, Code: "dns_record_line_unavailable", NotExecuted: true, Retryable: true}, true},
		{&PanelOperationError{Status: 503, Code: "dns_invalid_arguments", NotExecuted: true, Retryable: true}, false},
	} {
		if got := retryableReadToolError(context.Background(), tc.err); got != tc.want {
			t.Fatal(fmt.Sprintf("%T", tc.err), got, tc.want)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if retryableReadToolError(ctx, ErrUnavailable) {
		t.Fatal("cancelled parent retried")
	}
	if err := readToolRetryDelay(ctx, 1); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestReadToolRetryAllowlistRejectsSideEffects(t *testing.T) {
	s := &Service{}
	s.SetPanelOperations(&fixturePanelOperations{ops: []PanelOperation{
		fixtureOperation("dns.records", "/dns/record/list", false, false),
		fixtureOperation("dns.record_save", "/dns/record/save", true, false),
		fixtureOperation("future.read", "/future/read", false, false),
		fixtureOperation("client_entries.list", "/server/client-entry/fetch", false, false),
	}})
	for _, name := range []string{"knowledge_save", "knowledge_create", "entry_probe_run", "entry_probe_targets", "monitor_run", "ssh_status", "ssh_server_register", "propose_panel_operation", "propose_user_update", "future_tool"} {
		if s.panelRecoveryReadOnlyCall(ToolCall{Function: ToolFunction{Name: name, Arguments: `{}`}}) {
			t.Fatal("unsafe retry candidate", name)
		}
	}
	for operation, want := range map[string]bool{"dns.records": true, "dns.record_save": false, "future.read": false, "client_entries.list": false} {
		raw, _ := json.Marshal(map[string]any{"operation": operation, "parameters": map[string]any{}})
		if s.panelRecoveryReadOnlyCall(ToolCall{Function: ToolFunction{Name: "panel_query", Arguments: string(raw)}}) != want {
			t.Fatal(operation)
		}
	}
}

func TestReadToolRetrySafeAIErrorKeepsOnlyTrustedTemporaryFlag(t *testing.T) {
	for _, tc := range []struct {
		code              string
		notExecuted, want bool
	}{
		{"dns_domain_metadata", true, true}, {"dns_record_line_unavailable", true, true}, {"dns_preflight_failed", true, true},
		{"dns_invalid_arguments", true, false}, {"dns_domain_metadata", false, false}, {"PRIVATE_SENTINEL", true, false},
	} {
		var got *PanelOperationError
		err := safeAIError(&PanelOperationError{Status: 503, Code: tc.code, Message: "PRIVATE_SENTINEL", NotExecuted: tc.notExecuted, Retryable: true})
		if !errors.As(err, &got) || got.Retryable != tc.want || strings.Contains(err.Error(), "PRIVATE_SENTINEL") {
			t.Fatal(tc.code, got, err)
		}
	}
}

func TestReadToolRetrySafeAIErrorPreservesTransientClassification(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"temporary database", &pgconn.PgError{Code: "40001", Message: "PRIVATE_SENTINEL"}, true},
		{"permanent database", &pgconn.PgError{Code: "23505", Message: "PRIVATE_SENTINEL"}, false},
		{"broken socket", fmt.Errorf("PRIVATE_SENTINEL: %w", io.ErrUnexpectedEOF), true},
		{"unknown backend error", errors.New("PRIVATE_SENTINEL"), false},
		{"nil preserves unavailable category", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Repeated sanitization must not flatten permanent errors into the
			// generic retryable availability sentinel again.
			err := safeAIError(safeAIError(tc.err))
			if !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), "PRIVATE_SENTINEL") || strings.Contains(toolErrorContent(err), "PRIVATE_SENTINEL") {
				t.Fatal(err, toolErrorContent(err))
			}
			if got := retryableReadToolError(context.Background(), err); got != tc.want {
				t.Fatalf("retry=%v want=%v", got, tc.want)
			}
		})
	}
}

type retryReadTestPanel struct {
	admin.Service
	calls int
	err   error
}

func (p *retryReadTestPanel) ListUsers(context.Context, admin.UserFetchRequest) (admin.UserListResult, error) {
	p.calls++
	if p.calls == 1 {
		return admin.UserListResult{}, p.err
	}
	return admin.UserListResult{Total: 1, Data: []map[string]any{{"id": int64(9), "email": "u@example.test", "password": "PRIVATE_SENTINEL"}}}, nil
}

func TestReadToolRetryRealDispatchRestoresLookupAndChecksAccess(t *testing.T) {
	for _, tc := range []struct {
		name  string
		err   error
		calls int
	}{
		{"temporary read recovered", &pgconn.PgError{Code: "08006", Message: "PRIVATE_SENTINEL"}, 2},
		{"permanent read not retried", &pgconn.PgError{Code: "42703", Message: "PRIVATE_SENTINEL"}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			panel := &retryReadTestPanel{err: tc.err}
			s := NewService(config.Config{}, db, panel)
			defer s.Close()
			for attempt := 0; attempt < tc.calls; attempt++ {
				mock.ExpectQuery(`SELECT id FROM v2_user`).WithArgs(int64(1)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
				mock.ExpectQuery(`SELECT s.id FROM v2_ai_session s JOIN v2_user`).WithArgs(int64(2), int64(1)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(2))
			}
			guards := 0
			out, err := s.callToolWithRetry(context.Background(), 1, 2, ToolCall{ID: "fixture", Function: ToolFunction{Name: "user_lookup", Arguments: `{"query":"u@example.test"}`}}, func(context.Context) error { guards++; return nil })
			if panel.calls != tc.calls || guards != tc.calls || strings.Contains(out.Content, "PRIVATE_SENTINEL") {
				t.Fatal(panel.calls, guards, out, err)
			}
			if tc.calls > 1 && (err != nil || !strings.Contains(out.Content, `"email":"u@example.test"`)) {
				t.Fatal(out, err)
			}
			if tc.calls == 1 && !errors.Is(err, ErrUnavailable) {
				t.Fatal(err)
			}
			if err = mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
