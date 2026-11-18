package aiops

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestSSHSetupFailureMetadataStrictlyValidated(t *testing.T) {
	valid := &SSHSetupFailure{ExecutionState: "not_executed", Stage: "handshake", Code: "timeout", Retryable: true, Attempts: 3}
	raw, _ := json.Marshal(valid)
	if got := decodeSSHSetupFailure(raw); !reflect.DeepEqual(got, valid) {
		t.Fatalf("valid local metadata lost: %+v", got)
	}
	for _, invalid := range []string{
		`null`, `{}`, `{"execution_state":"not_executed"}`,
		`{"execution_state":"unknown","stage":"handshake","code":"timeout","retryable":true,"attempts":3}`,
		`{"execution_state":"not_executed","stage":"exec","code":"timeout","retryable":true,"attempts":3}`,
		`{"execution_state":"not_executed","stage":"handshake","code":"timeout","retryable":true,"attempts":4}`,
		`{"execution_state":"not_executed","stage":"handshake","code":"authentication","retryable":true,"attempts":1}`,
		`{"execution_state":"not_executed","stage":"handshake","code":"authorization","retryable":true,"attempts":1}`,
		`{"execution_state":"not_executed","stage":"handshake","code":"canceled","retryable":true,"attempts":1}`,
		string(raw) + `{}`, strings.Replace(string(raw), `"attempts":3`, `"attempts":3,"command":"remote-controlled"`, 1),
	} {
		if got := decodeSSHSetupFailure([]byte(invalid)); got != nil {
			t.Fatalf("invalid metadata enabled recovery: %s -> %+v", invalid, got)
		}
	}
}

func TestSSHSetupFailureOnlyBeforeExec(t *testing.T) {
	for _, mode := range []string{"eof", "exec_disconnect", "exec_nonzero", "success"} {
		t.Run(mode, func(t *testing.T) {
			f := newSSHReconnectFixture(t, func(int64) string { return mode })
			s, server := sshReconnectService(t, f, "retry-secret-fixture")
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			result := s.runSSH(ctx, server, "fixture-pre-exec-metadata")
			if mode == "eof" {
				want := &SSHSetupFailure{ExecutionState: "not_executed", Stage: "handshake", Code: "network", Retryable: true, Attempts: 3}
				if !reflect.DeepEqual(result.SetupFailure, want) || len(f.sentCommands()) != 0 || result.Uncertain {
					t.Fatalf("pre-exec metadata incorrect: %+v commands=%v", result, f.sentCommands())
				}
			} else if result.SetupFailure != nil || len(f.sentCommands()) != 1 {
				t.Fatalf("post-exec failure received pre-exec proof: %+v commands=%v", result, f.sentCommands())
			}
		})
	}
}

func TestSSHSetupFailurePersistenceDoesNotTrustWrongActionState(t *testing.T) {
	valid := &SSHSetupFailure{ExecutionState: "not_executed", Stage: "handshake", Code: "network", Retryable: true, Attempts: 3}
	encoded, _ := json.Marshal(valid)
	code := 7
	for _, tc := range []struct {
		name, kind, status string
		exit               *int
		want               any
	}{
		{"pre-exec failure", "ssh_command", "failed", nil, string(encoded)},
		{"unknown", "ssh_command", "unknown", nil, nil},
		{"success", "ssh_command", "succeeded", nil, nil},
		{"known exit", "ssh_command", "failed", &code, nil},
		{"non ssh", "panel_operation", "failed", nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			mock.ExpectExec(`UPDATE v2_ai_action SET status=.*ssh_setup_failure=.*WHERE id=\$1 AND status='running'`).
				WithArgs(int64(19), tc.status, "", "fixture", sqlmock.AnyArg(), tc.exit, tc.want).
				WillReturnResult(sqlmock.NewResult(0, 1))
			if err := (&Service{db: db}).persistActionCompletion(Action{ID: 19, Kind: tc.kind}, tc.status, "", "fixture", tc.exit, valid); err != nil {
				t.Fatal(err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
