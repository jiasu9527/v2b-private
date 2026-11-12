package aiops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

func normalizeFallbackModels(values, models []string) ([]string, error) {
	allowed := map[string]bool{}
	for _, model := range models {
		allowed[model] = true
	}
	seen := map[string]bool{}
	out := []string{}
	if len(values) > 20 {
		return nil, ErrValidation
	}
	for _, model := range values {
		if !allowed[model] || seen[model] {
			return nil, ErrValidation
		}
		seen[model] = true
		out = append(out, model)
	}
	return out, nil
}

// All model/provider attempts use one configuration snapshot and one deadline.
// Only completed responses leave this boundary, so tools are never replayed.
func (s *Service) completeWithModelFailover(ctx context.Context, cfg Config, cipher string, relays []RelayEndpoint, selected string, request completionRequest, onContent func(string) error) (providerMessage, error) {
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, completionTimeout)
	defer cancel()
	ctx = withProviderArgumentCorrection(ctx)
	models := []string{selected}
	if cfg.FailoverEnabled {
		for _, m := range cfg.FallbackModels {
			if m != selected && cfg.modelReady(m) {
				models = append(models, m)
			}
		}
	}
	cfg.skipCooldownWait = len(models) > 1
	var last error
	for index, model := range models {
		if ctx.Err() != nil {
			if parent.Err() != nil {
				return providerMessage{}, errProviderCanceled
			}
			return providerMessage{}, fmt.Errorf("AI 单次模型请求超过 5 分钟，未执行工具；已保留此前执行结果")
		}
		if index > 0 {
			if onContent != nil {
				if err := onContent(""); err != nil {
					return providerMessage{}, errProviderContentUpdate
				}
			}
			request.ReasoningEffort = cfg.ReasoningEfforts[model]
			if request.ReasoningEffort == "auto" {
				request.ReasoningEffort = ""
			}
		}
		request.Model = model
		payload, err := json.Marshal(request)
		if err != nil || len(payload) > maxProviderRequestBytes {
			return providerMessage{}, errContextTooLarge
		}
		candidates := providerCandidates(cfg, cipher, relays, model)
		out, err := s.completeWithRelays(ctx, cfg, candidates, model, payload, onContent)
		if err == nil {
			if model != selected {
				out.actualModel = model
			}
			out.actualReasoning = request.ReasoningEffort
			if out.actualReasoning == "" {
				out.actualReasoning = "auto"
			}
			if model != selected {
				out.Content = "[灾备已从 " + selected + " 切换至 " + model + "]\n\n" + out.Content
			}
			return out, nil
		}
		last = err
		if !canSwitchRelay(err) || errors.Is(err, errProviderCanceled) || ctx.Err() != nil {
			return providerMessage{}, err
		}
	}
	return providerMessage{}, last
}
