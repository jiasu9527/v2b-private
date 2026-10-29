package aiops

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"forest/go-api/internal/config"
	"github.com/DATA-DOG/go-sqlmock"
)

func TestAITicketToolArgumentValidation(t *testing.T) {
	for _, tc := range []struct {
		name, args string
		valid      bool
	}{
		{"tickets_list", `{"email":"missing@example.test","limit":100}`, true},
		{"tickets_list", `{"status":2}`, false},
		{"tickets_list", `{"reply_status":null}`, false},
		{"ticket_get", `{"ticket_id":4,"offset":0}`, true},
		{"ticket_get", `{"ticket_id":4,"offset":-1}`, false},
		{"ticket_get", `{"ticket_id":"4"}`, false},
		{"ticket_settings", `{"api_key":"secret"}`, false},
		{"propose_ticket_update", `{"ticket_id":4,"operation":"reply","reason":"reply","message":"exact reply"}`, true},
		{"propose_ticket_update", `{"ticket_id":4,"operation":"close","reason":"close","limit":100}`, false},
		{"propose_ticket_settings", `{"mode":"disabled","reason":"maintenance"}`, true},
	} {
		t.Run(tc.name+tc.args, func(t *testing.T) {
			_, err := decodeTicketArgs(tc.name, tc.args)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
	for _, tc := range []struct {
		mode   string
		status int64
	}{{"all", 0}, {"paid_only", 1}, {"disabled", 2}} {
		status, ok := ticketSettingValue(tc.mode)
		if !ok || status != tc.status || ticketSettingMode(status) != tc.mode {
			t.Fatal("ticket setting semantics changed")
		}
	}
	if validateTicketUpdate(ticketUpdatePayload{TicketID: 1, Operation: "close", Message: "discarded reply"}) == nil {
		t.Fatal("hidden message on close accepted")
	}
}

func TestAITicketMissingEmailKeepsExactQueryPredicate(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := NewService(config.Config{}, db, nil)
	defer s.Close()
	mock.ExpectQuery(`SELECT COUNT\(\*\).*LOWER\(u.email\)=\$1`).WithArgs("missing@example.test").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(`SELECT t.id.*LOWER\(u.email\)=\$1.*LIMIT \$2 OFFSET \$3`).WithArgs("missing@example.test", 20, 0).WillReturnRows(sqlmock.NewRows([]string{"id", "user_id", "email", "subject", "level", "status", "reply_status", "created_at", "updated_at"}))
	v, err := s.callTicketTool(context.Background(), 1, 1, ToolCall{Function: ToolFunction{Name: "tickets_list", Arguments: `{"email":"missing@example.test"}`}})
	if err != nil {
		t.Fatal(err)
	}
	var page pageDTO[aiTicketDTO]
	if json.Unmarshal([]byte(v.Content), &page) != nil || page.Total != 0 || len(page.Items) != 0 {
		t.Fatal("missing email expanded search")
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAITicketInvalidWriteDoesNotTouchDatabase(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := NewService(config.Config{}, db, nil)
	defer s.Close()
	for _, args := range []string{`{"ticket_id":1,"operation":"reply","message":"","reason":"fixture"}`, `{"ticket_id":1,"operation":"delete","reason":"fixture"}`, `{"ticket_id":1,"operation":"close","message":"hidden","reason":"fixture"}`} {
		_, err = s.callTicketTool(context.Background(), 1, 1, ToolCall{Function: ToolFunction{Name: "propose_ticket_update", Arguments: args}})
		if !errors.Is(err, ErrValidation) {
			t.Fatal("invalid write accepted", err)
		}
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
