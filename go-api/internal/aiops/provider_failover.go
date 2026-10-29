package aiops

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"
)

const relayCooldown = 60 * time.Second

var errProviderCanceled = errors.New("AI 请求已取消，未执行工具")

// Only messages generated in this package may be wrapped here. Raw transport
// errors and relay bodies can contain credentials and must never be returned.
type providerFailure struct{ message string }

func (e *providerFailure) Error() string { return e.message }

func retryableProviderError(message string) error { return &providerFailure{message: message} }

func canSwitchRelay(err error) bool {
	var failure *providerFailure
	return errors.As(err, &failure)
}

// A completion is the retry boundary. The caller receives either one whole
// valid response or no tools at all; previously executed chat turns and tools
// are never replayed. Candidates and request bytes stay fixed for this call.
func (s *Service) completeWithRelays(ctx context.Context, cfg Config, endpoints []RelayEndpoint, model string, payload []byte, onContent func(string) error) (providerMessage, error) {
	var empty providerMessage
	if len(endpoints) == 0 {
		return empty, errors.New("AI 中转配置不可用")
	}
	timeout := time.Duration(cfg.FailoverTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	attempted := 0
	draftVisible := false
	var lastErr error
	for index, endpoint := range endpoints {
		if ctx.Err() != nil {
			return empty, errProviderCanceled
		}
		if index > 0 && !cfg.FailoverEnabled {
			break
		}
		healthKey := relayFingerprint(endpoint, model)
		if cfg.FailoverEnabled && s.relayCoolingDown(healthKey) {
			continue
		}
		// A failed streaming attempt may already have displayed some text.
		// Clear it before contacting the next relay so their drafts never mix.
		if draftVisible && onContent != nil {
			if err := onContent(""); err != nil {
				return empty, errProviderContentUpdate
			}
			draftVisible = false
		}
		attemptCtx := ctx
		cancel := func() {}
		if cfg.FailoverEnabled {
			attemptCtx, cancel = context.WithTimeout(ctx, timeout)
		}
		var receive func(string) error
		if onContent != nil {
			receive = func(content string) error {
				if err := onContent(content); err != nil {
					return err
				}
				draftVisible = content != ""
				return nil
			}
		}
		attempted++
		out, err := s.completeAtRelay(attemptCtx, endpoint, payload, receive)
		cancel()
		if ctx.Err() != nil {
			return empty, errProviderCanceled
		}
		if err == nil {
			s.clearRelayCooldown(healthKey)
			return out, nil
		}
		if !cfg.FailoverEnabled || !canSwitchRelay(err) {
			return empty, err
		}
		s.markRelayCoolingDown(healthKey)
		lastErr = err
	}
	if lastErr != nil {
		if attempted > 1 {
			return empty, fmt.Errorf("AI 中转主备均未完成请求（已尝试 %d 个）；%s", attempted, lastErr.Error())
		}
		return empty, lastErr
	}
	return empty, errors.New("可用 AI 中转均处于故障冷却中，请在 60 秒后重试或检查中转配置")
}

// Credential/config changes receive a new health identity. A temporary model
// failure must not suppress unrelated models on the same relay. The digest is
// process-local and is never logged or persisted.
func relayFingerprint(endpoint RelayEndpoint, model string) [32]byte {
	return sha256.Sum256([]byte(endpoint.ID + "\x00" + endpoint.BaseURL + "\x00" + endpoint.APIKeyCipher + "\x00" + model))
}

func (s *Service) relayCoolingDown(key [32]byte) bool {
	s.relayHealthMu.Lock()
	defer s.relayHealthMu.Unlock()
	now := time.Now()
	for candidate, until := range s.relayCooldowns {
		if !now.Before(until) {
			delete(s.relayCooldowns, candidate)
		}
	}
	return now.Before(s.relayCooldowns[key])
}

func (s *Service) markRelayCoolingDown(key [32]byte) {
	s.relayHealthMu.Lock()
	defer s.relayHealthMu.Unlock()
	if s.relayCooldowns == nil {
		s.relayCooldowns = make(map[[32]byte]time.Time)
	}
	s.relayCooldowns[key] = time.Now().Add(relayCooldown)
}

func (s *Service) clearRelayCooldown(key [32]byte) {
	s.relayHealthMu.Lock()
	defer s.relayHealthMu.Unlock()
	delete(s.relayCooldowns, key)
}
