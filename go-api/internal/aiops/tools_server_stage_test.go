package aiops

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"forest/go-api/internal/config"
	"github.com/DATA-DOG/go-sqlmock"
)

type encryptedStageArgument struct{}

func (encryptedStageArgument) Match(value driver.Value) bool {
	cipher, ok := value.(string)
	return ok && strings.HasPrefix(cipher, "v1:") && !strings.Contains(cipher, "fixture")
}

func TestSSHServerStageAcceptsStructuredModelOutput(t *testing.T) {
	input, err := validateSSHStageItem(sshAIExtracted{Host: "192.0.2.10:2222", Username: "root", Password: "fixture-secret", Name: "  香港落地  ", Group: "亚洲"})
	if err != nil || input.Host != "192.0.2.10" || input.Port != 2222 || input.Username != "root" || input.Password != "fixture-secret" || input.Name != "香港落地" || input.Group != "亚洲" {
		t.Fatalf("valid model output rejected or changed: %+v err=%v", input, err)
	}
	var decoded sshAIExtracted
	if err := json.Unmarshal([]byte(`{"host":"192.0.2.11","port":"22","username":"debian","password":"second-fixture"}`), &decoded); err != nil {
		t.Fatal(err)
	}
	input, err = validateSSHStageItem(decoded)
	if err != nil || input.Port != 22 || input.Password != "second-fixture" {
		t.Fatal("string port from relay should be normalized by model-output decoder", err)
	}
}

func TestSSHServerStageRejectsIncompleteOrConflictingCredentials(t *testing.T) {
	for name, item := range map[string]sshAIExtracted{
		"missing host":           {Username: "root", Password: "fixture"},
		"missing username":       {Host: "192.0.2.10", Password: "fixture"},
		"missing credential":     {Host: "192.0.2.10", Username: "root"},
		"both credential types":  {Host: "192.0.2.10", Username: "root", Password: "fixture", PrivateKey: "fake-key"},
		"port conflict":          {Host: "192.0.2.10:2222", Port: 22, Username: "root", Password: "fixture"},
		"private key incomplete": {Host: "192.0.2.10", Username: "root", PrivateKey: "-----BEGIN PRIVATE KEY-----"},
		"passphrase without key": {Host: "192.0.2.10", Username: "root", Password: "fixture", Passphrase: "phrase"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := validateSSHStageItem(item); err == nil || strings.Contains(err.Error(), "fixture") {
				t.Fatalf("invalid item accepted or credential leaked in error: %v", err)
			}
		})
	}
}

func TestSSHServerStageToolSchemaIsAvailable(t *testing.T) {
	s := NewService(config.Config{}, nil, nil)
	defer s.Close()
	if !toolCapabilityAllowed("ssh_server_stage") || !s.validToolArgumentKeys("ssh_server_stage", `{"items":[{"host":"192.0.2.10","username":"root","password":"fixture"}]}`) {
		t.Fatal("model cannot access SSH staging tool")
	}
	if s.validToolArgumentKeys("ssh_server_stage", `{"items":[],"actor_id":1}`) {
		t.Fatal("unexpected top-level staging field accepted")
	}
}

func TestSSHServerStageStoresCiphertextAndReplaysSameCall(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := NewService(config.Config{AppKey: "stage-test-key"}, db, nil)
	defer s.Close()
	s.schemaOK = true
	const arguments = `{"items":[{"host":"192.0.2.10","username":"root","password":"fixture-secret"}]}`
	call := ToolCall{ID: "turn:11:round:1:stage", Function: ToolFunction{Name: "ssh_server_stage", Arguments: arguments}}
	ctx := context.WithValue(context.Background(), knowledgeTurnContextKey{}, int64(11))
	readyUntil := time.Now().Add(time.Hour).Unix()
	itemRows := sqlmock.NewRows([]string{"id", "item_index", "host", "port", "username", "tool_request_hash"})
	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock_shared`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT id FROM v2_user WHERE id=\$1`).WithArgs(int64(1)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	mock.ExpectQuery(`SELECT s.id FROM v2_ai_session s JOIN v2_user`).WithArgs(int64(2), int64(1)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(2))
	mock.ExpectQuery(`SELECT status,stop_requested,expires_at FROM v2_ai_turn`).WithArgs(int64(11), int64(2), int64(1)).WillReturnRows(sqlmock.NewRows([]string{"status", "stop_requested", "expires_at"}).AddRow("running", false, readyUntil))
	mock.ExpectQuery(`SELECT id,item_index,host,port,username,tool_request_hash FROM v2_ai_server_intake`).WithArgs(int64(11), int64(2), int64(1), call.ID).WillReturnRows(itemRows)
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM v2_ai_server_intake`).WithArgs(int64(11), int64(2), int64(1)).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(`INSERT INTO v2_ai_server_intake`).WithArgs(int64(2), int64(11), int64(1), 1, "192.0.2.10", "192.0.2.10", 22, "root", "", encryptedStageArgument{}, "", "", "", call.ID, hashOperation(arguments), sqlmock.AnyArg(), sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(50))
	mock.ExpectCommit()
	first, err := s.callServerStageTool(ctx, 1, 2, call)
	if err != nil || strings.Contains(first.Content, "fixture-secret") || !strings.Contains(first.Content, `"intake_id":50`) || !strings.Contains(first.Content, `"already_staged":false`) {
		t.Fatalf("stage did not persist encrypted intake: content=%s err=%v", first.Content, err)
	}

	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock_shared`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT id FROM v2_user WHERE id=\$1`).WithArgs(int64(1)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	mock.ExpectQuery(`SELECT s.id FROM v2_ai_session s JOIN v2_user`).WithArgs(int64(2), int64(1)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(2))
	mock.ExpectQuery(`SELECT status,stop_requested,expires_at FROM v2_ai_turn`).WithArgs(int64(11), int64(2), int64(1)).WillReturnRows(sqlmock.NewRows([]string{"status", "stop_requested", "expires_at"}).AddRow("running", false, readyUntil))
	mock.ExpectQuery(`SELECT id,item_index,host,port,username,tool_request_hash FROM v2_ai_server_intake`).WithArgs(int64(11), int64(2), int64(1), call.ID).WillReturnRows(sqlmock.NewRows([]string{"id", "item_index", "host", "port", "username", "tool_request_hash"}).AddRow(50, 1, "192.0.2.10", 22, "root", hashOperation(arguments)))
	mock.ExpectRollback()
	second, err := s.callServerStageTool(ctx, 1, 2, call)
	if err != nil || !strings.Contains(second.Content, `"already_staged":true`) {
		t.Fatalf("same call should reuse intake: content=%s err=%v", second.Content, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
