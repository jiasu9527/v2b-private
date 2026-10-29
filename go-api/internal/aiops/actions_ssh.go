package aiops

import (
	"context"
	"encoding/json"
	"sync"
	"time"
)

const defaultSSHTimeoutSeconds = 120
const maxSSHTimeoutSeconds = 1800

func normalizeSSHTimeout(seconds int) (int, error) {
	if seconds == 0 {
		return defaultSSHTimeoutSeconds, nil
	}
	if seconds < 30 || seconds > maxSSHTimeoutSeconds {
		return 0, ErrValidation
	}
	return seconds, nil
}

func actionExecutionLifetime(a Action) time.Duration {
	if a.Kind == "ssh_command" {
		var payload struct {
			TimeoutSeconds int `json:"timeout_seconds"`
		}
		if json.Unmarshal(a.Payload, &payload) == nil {
			if seconds, err := normalizeSSHTimeout(payload.TimeoutSeconds); err == nil {
				return time.Duration(seconds) * time.Second
			}
		}
	}
	return executionLifetime
}

// Old persisted actions have no timeout. Bound both malformed and current
// payloads so an interrupted command cannot block cleanup indefinitely.
const actionStaleSecondsSQL = `CASE WHEN kind='ssh_command' AND COALESCE(payload->>'timeout_seconds','') ~ '^[0-9]{1,4}$' THEN CASE WHEN (payload->>'timeout_seconds')::INT BETWEEN 30 AND 1800 THEN (payload->>'timeout_seconds')::INT+60 ELSE 180 END ELSE 180 END`

// Output is a bounded snapshot, replacing the same action row. This must not
// insert records: deletion/reset must never be undone by a late worker.
func (s *Service) persistSSHProgress(ctx context.Context, actionID int64, output string) {
	writeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, _ = s.db.ExecContext(writeCtx, `UPDATE v2_ai_action SET output=$2,updated_at=$3 WHERE id=$1 AND status='running' AND output IS DISTINCT FROM $2`, actionID, output, time.Now().Unix())
}

// At most one write per interval, independent of how many SSH chunks arrive.
// The returned stop function joins the worker before final status is saved.
func streamSSHOutput(ctx context.Context, out *cappedSSHOutput, secrets []string, progress func(string), interval time.Duration) func() {
	if progress == nil {
		return func() {}
	}
	done, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		last := ""
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-ticker.C:
				current := out.safeString(secrets, false)
				if current != last {
					progress(current)
					last = current
				}
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done) }); <-stopped }
}
