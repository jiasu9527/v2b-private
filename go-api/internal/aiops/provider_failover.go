package aiops

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const (
	relayCooldown           = 60 * time.Second
	completionTimeout       = 5 * time.Minute
	providerRetryBackoff    = 300 * time.Millisecond
	initialCompletionTokens = 4096
	maxCompletionTokens     = 16384
)

var errProviderCanceled = errors.New("AI 请求已取消，未执行工具")
var errProviderOutputLimit = errors.New("AI 回复达到输出长度上限，未执行工具；请拆分单次操作")

// Only messages generated in this package may be wrapped here. Raw transport
// errors and relay bodies can contain credentials and must never be returned.
type providerFailure struct {
	message        string
	retrySameRelay bool
}

func (e *providerFailure) Error() string { return e.message }

func retryableProviderError(message string) error { return &providerFailure{message: message} }
func transientProviderError(message string) error {
	return &providerFailure{message: message, retrySameRelay: true}
}

func canSwitchRelay(err error) bool {
	var failure *providerFailure
	return errors.As(err, &failure)
}

func canRetryRelay(err error) bool {
	var failure *providerFailure
	return errors.As(err, &failure) && failure.retrySameRelay
}

// A completion is the retry boundary. The caller receives either one whole
// valid response or no tools at all; previously executed chat turns and tools
// are never replayed. Messages/model/tools stay fixed. Only an explicit length
// completion may increase max_tokens; incomplete tools never reach the caller.
func (s *Service) completeWithRelays(ctx context.Context, cfg Config, endpoints []RelayEndpoint, model string, payload []byte, onContent func(string) error) (providerMessage, error) {
	var empty providerMessage
	if len(endpoints) == 0 {
		return empty, errors.New("AI 中转配置不可用")
	}
	if !cfg.FailoverEnabled {
		endpoints = endpoints[:1]
	}
	completionCtx, cancelCompletion := context.WithTimeout(ctx, completionTimeout)
	defer cancelCompletion()
	contextError := func() error {
		if ctx.Err() != nil {
			return errProviderCanceled
		}
		return errors.New("AI 单次模型请求超过 5 分钟，未执行工具；已保留此前执行结果")
	}
	timeout := time.Duration(cfg.FailoverTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	attempted := 0
	draftVisible := false
	var lastErr error
	// Do not immediately fail the next turn just because all relays cooled
	// down during the preceding turn. Wait once until one can be probed; this
	// wait is cancelable and consumes the same bounded completion deadline.
	candidates := endpoints
	if cfg.FailoverEnabled {
		for {
			candidates = nil
			var wait time.Duration
			for _, endpoint := range endpoints {
				remaining := s.relayCooldownRemaining(relayFingerprint(endpoint, model))
				if remaining <= 0 {
					candidates = append(candidates, endpoint)
				} else if wait == 0 || remaining < wait {
					wait = remaining
				}
			}
			if len(candidates) > 0 {
				break
			}
			if cfg.skipCooldownWait {
				return empty, retryableProviderError("当前模型中转处于故障冷却，尝试备用模型")
			}
			if !waitProviderRetry(completionCtx, wait) {
				return empty, contextError()
			}
		}
	}
	// Prefer a different healthy relay before retrying a transiently failed
	// one. Each endpoint has at most one transient retry; auth/config/parser
	// errors can switch relay, but are never blindly retried at the same site.
	retries := make([]RelayEndpoint, 0, len(candidates))
	for index := 0; index < len(candidates)+len(retries); index++ {
		if completionCtx.Err() != nil {
			return empty, contextError()
		}
		retrying := index >= len(candidates)
		var endpoint RelayEndpoint
		if retrying {
			endpoint = retries[index-len(candidates)]
			if !waitProviderRetry(completionCtx, providerRetryBackoff) {
				return empty, contextError()
			}
		} else {
			endpoint = candidates[index]
		}
		healthKey := relayFingerprint(endpoint, model)
		for {
			// A failed streaming attempt may already have displayed some text.
			// Clear it before contacting the next relay so their drafts never mix.
			if draftVisible && onContent != nil {
				if err := onContent(""); err != nil {
					return empty, errProviderContentUpdate
				}
				draftVisible = false
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
			out, err := s.completeAtRelay(completionCtx, endpoint, payload, timeout, receive)
			if completionCtx.Err() != nil {
				return empty, contextError()
			}
			if err == nil {
				s.clearRelayCooldown(healthKey)
				return out, nil
			}
			if errors.Is(err, errProviderOutputLimit) {
				larger, ok := expandCompletionOutputBudget(payload)
				if !ok {
					return empty, err
				}
				payload = larger
				continue
			}
			if !canSwitchRelay(err) {
				return empty, err
			}
			if cfg.FailoverEnabled {
				s.markRelayCoolingDown(healthKey)
			}
			if !retrying && canRetryRelay(err) {
				retries = append(retries, endpoint)
			}
			lastErr = err
			break
		}
	}
	if lastErr != nil {
		if attempted > 1 {
			return empty, fmt.Errorf("AI 中转在切换及有限重试后仍未完成请求（共 %d 次）；%w", attempted, lastErr)
		}
		return empty, lastErr
	}
	return empty, errors.New("可用 AI 中转均处于故障冷却中，请在 60 秒后重试或检查中转配置")
}

func waitProviderRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return ctx.Err() == nil
	}
}

func expandCompletionOutputBudget(payload []byte) ([]byte, bool) {
	var request map[string]json.RawMessage
	if json.Unmarshal(payload, &request) != nil || request == nil {
		return nil, false
	}
	budget := initialCompletionTokens
	if raw, ok := request["max_tokens"]; ok && (json.Unmarshal(raw, &budget) != nil || budget <= 0) {
		return nil, false
	}
	if budget >= maxCompletionTokens {
		return nil, false
	}
	request["max_tokens"], _ = json.Marshal(min(budget*2, maxCompletionTokens))
	encoded, err := json.Marshal(request)
	return encoded, err == nil && len(encoded) <= maxProviderRequestBytes
}

// Credential/config changes receive a new health identity. A temporary model
// failure must not suppress unrelated models on the same relay. The digest is
// process-local and is never logged or persisted.
func relayFingerprint(endpoint RelayEndpoint, model string) [32]byte {
	return sha256.Sum256([]byte(endpoint.ID + "\x00" + endpoint.BaseURL + "\x00" + endpoint.APIKeyCipher + "\x00" + model))
}

func (s *Service) relayCoolingDown(key [32]byte) bool {
	return s.relayCooldownRemaining(key) > 0
}

func (s *Service) relayCooldownRemaining(key [32]byte) time.Duration {
	s.relayHealthMu.Lock()
	defer s.relayHealthMu.Unlock()
	now := time.Now()
	for candidate, until := range s.relayCooldowns {
		if !now.Before(until) {
			delete(s.relayCooldowns, candidate)
		}
	}
	if until, ok := s.relayCooldowns[key]; ok && now.Before(until) {
		return until.Sub(now)
	}
	return 0
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
