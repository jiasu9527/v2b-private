package aiops

import (
	"encoding/json"
	"strings"
	"testing"

	"forest/go-api/internal/config"
)

func TestServerRegistrationArgumentsNeverAcceptCredentials(t *testing.T) {
	s := NewService(config.Config{}, nil, nil)
	defer s.Close()
	for _, args := range []string{`{"intake_id":1,"reason":"register"}`, `{"intake_id":1,"name":"landing","reason":"register"}`} {
		if !s.validToolArgumentKeys("ssh_server_register", args) {
			t.Fatalf("valid registration rejected: %s", args)
		}
	}
	for _, args := range []string{`{"intake_id":1}`, `{"reason":"register"}`, `{"intake_id":1,"reason":"register","password":"secret"}`, `{"intake_id":1,"reason":"register","private_key":"secret"}`, `{"intake_id":1,"reason":"register","host":"other.example"}`, `{"intake_id":1,"reason":"register","actor_id":2}`} {
		if s.validToolArgumentKeys("ssh_server_register", args) {
			t.Fatalf("unexpected registration fields accepted: %s", args)
		}
	}
}

func TestServerRegistrationPendingCannotAdoptEarlierTurn(t *testing.T) {
	a := Action{ID: 3, ToolCallID: "turn:12:round:1:register", Status: "pending"}
	for _, tt := range []struct {
		call    string
		pending bool
	}{{a.ToolCallID, true}, {"turn:12:round:2:register", true}, {"turn:13:round:1:register", false}, {"register", false}} {
		got := serverRegistrationProposalOutcome(a, tt.call)
		if got.Pending != tt.pending || got.ActionID != a.ID {
			t.Fatalf("%s: %+v", tt.call, got)
		}
	}
}

func TestRegistrationInternalScopeDoesNotChangeActionRequestHash(t *testing.T) {
	before := actionSpec{Kind: "entry_host", TargetID: 1, Payload: json.RawMessage(`{"host":"fixture.example"}`), Reason: "fixture"}
	old, _ := json.Marshal(before)
	before.ActorID, before.SessionID = 10, 20
	current, _ := json.Marshal(before)
	if string(old) != string(current) {
		t.Fatal("internal action scope changed persisted request hash")
	}
	result := serverRegistrationOutput(7, false)
	if !strings.Contains(result, `"server_id":7`) || !strings.Contains(result, `"connection_verified":false`) {
		t.Fatal(result)
	}
}
