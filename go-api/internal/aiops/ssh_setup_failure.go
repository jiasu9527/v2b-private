package aiops

import (
	"bytes"
	"encoding/json"
	"io"
)

// SSHSetupFailure is generated only by the local connection/session opener,
// before an SSH exec request is sent. It is never inferred from remote output
// or an error's display text. Missing/invalid metadata must fail closed.
type SSHSetupFailure struct {
	ExecutionState string `json:"execution_state"`
	Stage          string `json:"stage"`
	Code           string `json:"code"`
	Retryable      bool   `json:"retryable"`
	Attempts       int    `json:"attempts"`
}

func validSSHSetupFailure(v *SSHSetupFailure) bool {
	if v == nil || v.ExecutionState != "not_executed" || v.Attempts < 0 || v.Attempts > sshConnectionAttempts {
		return false
	}
	if v.Stage != "dial" && v.Stage != "handshake" && v.Stage != "session" {
		// Explicit cancellation/authorization checks can fail before the first
		// connection attempt, or after creating a session but still before exec.
		if v.Stage != "" || v.Code != "canceled" && v.Code != "authorization" {
			return false
		}
	}
	switch v.Code {
	case "timeout", "network":
		return v.Attempts > 0 && v.Stage != ""
	case "setup", "host_key", "authentication", "protocol", "canceled", "authorization":
		return !v.Retryable
	default:
		return false
	}
}

func (e *sshConnectionFailure) setupMetadata() *SSHSetupFailure {
	if e == nil {
		return nil
	}
	v := &SSHSetupFailure{ExecutionState: "not_executed", Stage: e.stage, Code: e.code, Retryable: e.retryable, Attempts: e.attempts}
	if !validSSHSetupFailure(v) {
		return nil
	}
	return v
}

func decodeSSHSetupFailure(raw []byte) *SSHSetupFailure {
	if len(raw) == 0 || len(raw) > 2048 {
		return nil
	}
	var v SSHSetupFailure
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&v) != nil || decoder.Decode(new(any)) != io.EOF || !validSSHSetupFailure(&v) {
		return nil
	}
	return &v
}
