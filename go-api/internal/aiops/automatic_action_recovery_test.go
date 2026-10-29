package aiops

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestAutomaticActionOutcomeOnlyKnownSSHExitCanRecover(t *testing.T) {
	positive, zero, negative := 7, 0, -1
	for _, tt := range []struct {
		name string
		a    Action
		stop bool
	}{
		{"known ssh exit", Action{Kind: "ssh_command", Status: "failed", ExitCode: &positive}, false},
		{"legacy failure without exit", Action{Kind: "ssh_command", Status: "failed"}, true},
		{"unknown regardless of exit", Action{Kind: "ssh_command", Status: "unknown", ExitCode: &positive}, true},
		{"non ssh failure", Action{Kind: "panel_operation", Status: "failed", ExitCode: &positive}, true},
		{"invalid zero failure", Action{Kind: "ssh_command", Status: "failed", ExitCode: &zero}, true},
		{"invalid negative failure", Action{Kind: "ssh_command", Status: "failed", ExitCode: &negative}, true},
		{"rejected", Action{Kind: "ssh_command", Status: "rejected", ExitCode: &positive}, true},
		{"expired", Action{Kind: "ssh_command", Status: "expired", ExitCode: &positive}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tt.a.ID = 42
			tt.a.Error = "本地生成的动作失败原因"
			tt.a.Output = strings.Repeat("执行输出<&>", 12000)
			result, err := automaticActionOutcome(tt.a)
			if (err != nil) != tt.stop || result.ActionID != tt.a.ID || result.Pending || len(result.Content) > maxToolOutputBytes || !utf8.ValidString(result.Content) {
				t.Fatal("incorrect action outcome", result.ActionID, err)
			}
			var decoded map[string]any
			if json.Unmarshal([]byte(result.Content), &decoded) != nil || decoded["status"] != tt.a.Status || decoded["action_id"] != float64(42) {
				t.Fatal("summary lost authoritative status")
			}
			if !tt.stop {
				if result.SSHExitCode == nil || *result.SSHExitCode != positive || decoded["exit_code"] != float64(positive) || decoded["recovery_instruction"] != sshExitRecoveryInstruction {
					t.Fatal("known SSH exit recovery metadata missing")
				}
				public, _ := json.Marshal(result)
				if strings.Contains(string(public), "SSHExitCode") {
					t.Fatal("internal recovery marker serialized")
				}
			} else {
				if result.SSHExitCode != nil {
					t.Fatal("unproven exit enabled automatic recovery")
				}
				message := formatAutomaticActionError(result, err)
				if !strings.Contains(message, "#42") || !strings.Contains(message, tt.a.Status) || !strings.Contains(message, tt.a.Error) {
					t.Fatal("stopped action lost specific safe error", message)
				}
			}
		})
	}
	result, err := automaticActionOutcome(Action{ID: 43, Kind: "ssh_command", Status: "succeeded"})
	if err != nil || result.ActionID != 43 || result.SSHExitCode != nil {
		t.Fatal("successful action changed semantics", err)
	}
}

func TestFormatAutomaticActionErrorDoesNotLeakInfrastructureErrors(t *testing.T) {
	for _, err := range []error{errors.New("postgres://root:database-secret@host/private SQL"), ErrUnavailable, ErrConflict, ErrValidation, ErrForbidden, ErrBusy, ErrNotFound, context.Canceled, context.DeadlineExceeded} {
		message := formatAutomaticActionError(ToolOutcome{ActionID: 72}, err)
		if !strings.Contains(message, "#72") || strings.Contains(message, "database-secret") || strings.Contains(message, "private SQL") || strings.Contains(message, "postgres://") {
			t.Fatal("unsafe or incomplete action error", message)
		}
	}
}

func TestAutomaticActionKnownSSHFailurePreservesHeadAndTail(t *testing.T) {
	code := 23
	for _, output := range []string{"BEGIN-MARKER" + strings.Repeat("中文输出", 12000) + "FINAL-ERROR-MARKER", "BEGIN-MARKER" + strings.Repeat("<&>", 12000) + "FINAL-ERROR-MARKER"} {
		raw := automaticActionSummary(Action{ID: 91, Kind: "ssh_command", Status: "failed", ExitCode: &code, Output: output})
		var summary struct {
			Output          string `json:"output"`
			Tail            string `json:"output_tail"`
			OutputTruncated bool   `json:"output_truncated"`
		}
		if len(raw) > maxToolOutputBytes || !utf8.ValidString(raw) || json.Unmarshal([]byte(raw), &summary) != nil || !summary.OutputTruncated || !strings.HasPrefix(summary.Output, "BEGIN-MARKER") || !strings.HasSuffix(summary.Tail, "FINAL-ERROR-MARKER") {
			t.Fatal("failure summary dropped leading context or final error", len(raw), summary.OutputTruncated)
		}
	}
}
