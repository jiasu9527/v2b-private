package aiops

func validReasoningEffort(value string) bool {
	switch value {
	case "auto", "none", "minimal", "low", "medium", "high", "xhigh":
		return true
	default:
		return false
	}
}

func validReasoningSelection(value string) bool {
	return value == "default" || validReasoningEffort(value)
}

func normalizeReasoningEfforts(models []string, values map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(models))
	for _, model := range models {
		out[model] = "auto"
	}
	for model, effort := range values {
		if _, exists := out[model]; !exists || !validReasoningEffort(effort) {
			return nil, ErrValidation
		}
		out[model] = effort
	}
	return out, nil
}

func resolveReasoningEffort(selection, model string, cfg Config) (string, error) {
	if selection == "" || selection == "default" {
		value := cfg.ReasoningEfforts[model]
		if value == "" {
			value = "auto"
		}
		if !validReasoningEffort(value) {
			return "", ErrValidation
		}
		return value, nil
	}
	if !validReasoningEffort(selection) {
		return "", ErrValidation
	}
	return selection, nil
}
