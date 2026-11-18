package aiops

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"forest/go-api/internal/config"
	"github.com/DATA-DOG/go-sqlmock"
)

func TestAutomaticSSHSetupRecoveryRequiresTrustedMetadata(t *testing.T) {
	base := Action{ID: 1293, Kind: "ssh_command", TargetID: 10, Status: "failed", Error: "SSH handshake fixture failure", SSHSetupFailure: &SSHSetupFailure{ExecutionState: "not_executed", Stage: "handshake", Code: "timeout", Retryable: true, Attempts: 3}}
	for _, tc := range []struct {
		name    string
		mutate  func(*Action)
		recover bool
	}{
		{"exhausted connection budget", func(*Action) {}, true},
		{"authentication skip host", func(a *Action) {
			a.SSHSetupFailure.Code = "authentication"
			a.SSHSetupFailure.Retryable = false
			a.SSHSetupFailure.Attempts = 1
		}, true},
		{"legacy error text not evidence", func(a *Action) {
			a.SSHSetupFailure = nil
			a.Error = "SSH 握手阶段超时，已尝试 3 次连接；命令未发送"
		}, false},
		{"unknown cannot recover", func(a *Action) { a.Status = "unknown" }, false},
		{"canceled", func(a *Action) { a.SSHSetupFailure.Code = "canceled"; a.SSHSetupFailure.Retryable = false }, false},
		{"authorization revoked", func(a *Action) { a.SSHSetupFailure.Code = "authorization"; a.SSHSetupFailure.Retryable = false }, false},
		{"not ssh", func(a *Action) { a.Kind = "ticket_settings" }, false},
		{"no target", func(a *Action) { a.TargetID = 0 }, false},
		{"wrong execution state", func(a *Action) { a.SSHSetupFailure.ExecutionState = "unknown" }, false},
		{"invalid stage", func(a *Action) { a.SSHSetupFailure.Stage = "exec" }, false},
		{"invalid attempts", func(a *Action) { a.SSHSetupFailure.Attempts = 0 }, false},
		{"unsupported code", func(a *Action) { a.SSHSetupFailure.Code = "success" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := base
			f := *base.SSHSetupFailure
			a.SSHSetupFailure = &f
			tc.mutate(&a)
			out, err := automaticActionOutcome(a)
			if out.SSHSetupRecovery != tc.recover || (err == nil) != tc.recover {
				t.Fatalf("recovery=%v err=%v", out.SSHSetupRecovery, err)
			}
			if !tc.recover {
				return
			}
			var body map[string]any
			if json.Unmarshal([]byte(out.Content), &body) != nil || body["execution_state"] != "not_executed" || body["recovery"] != "ssh_setup" || body["server_id"] != float64(10) || body["status"] != "failed" {
				t.Fatal("lost authoritative recovery summary", body)
			}
			raw, _ := json.Marshal(out)
			if strings.Contains(string(raw), "SSHSetupRecovery") {
				t.Fatal("private control marker exposed")
			}
		})
	}
}

func TestSSHSetupRecoverySurvivesActionContextRefresh(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := NewService(config.Config{}, db, nil)
	defer s.Close()
	marker := `{"execution_state":"not_executed","stage":"handshake","code":"timeout","retryable":true,"attempts":3}`
	mock.ExpectQuery(`FROM v2_ai_action WHERE session_id`).WithArgs(int64(84)).WillReturnRows(sqlmock.NewRows([]string{"id", "kind", "status", "output", "error", "authorization_turn_id", "target_id", "ssh_setup_failure"}).AddRow(1293, "ssh_command", "failed", "", "fixture timeout", 858, 10, marker).AddRow(1292, "ssh_command", "unknown", "", "unknown result", 857, 9, marker))
	messages, err := s.refreshActionContext(context.Background(), 84, []providerMessage{{Role: "system", Content: "system"}})
	if err != nil {
		t.Fatal(err)
	}
	raw := messages[len(messages)-1].Content
	if !strings.Contains(raw, `"server_id":10`) || !strings.Contains(raw, `"recovery":"ssh_setup"`) || !strings.Contains(raw, `"attempts":3`) || strings.Contains(raw, `"server_id":9`) {
		t.Fatal("lost setup metadata or authorized unknown", raw)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSSHSetupRecoveryContextStaysBoundedAcrossManyHosts(t *testing.T) {
	messages := []providerMessage{{Role: "system", Content: "original-policy"}, {Role: "user", Content: "current task"}}
	servers := map[int64]int64{}
	for i := int64(1); i <= 200; i++ {
		servers[i] = i + 1000
		messages = sshSetupRecoveryContext(messages, servers)
	}
	if len(messages) != 3 || messages[0].Content != "original-policy" || messages[1].Content != "current task" || !strings.Contains(messages[2].Content, `"200":1200`) || len(messages[2].Content) > 10000 {
		t.Fatal("failed-host context grew without bound or lost target budget", len(messages))
	}
}
