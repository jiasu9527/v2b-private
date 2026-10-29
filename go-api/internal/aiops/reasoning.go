package aiops

import (
	"context"
	"database/sql"
	"errors"
)

func (s *Service) requireReasoningAccess(ctx context.Context, actorID int64, selection string) error {
	if selection != "" && selection != "default" {
		return s.requireAdmin(ctx, actorID)
	}
	return s.requireOperator(ctx, actorID)
}

// Lock the live role until the turn or session is committed. A cached browser
// role or a former administrator's saved selection cannot grant this setting.
func (s *Service) requireReasoningAccessTx(ctx context.Context, tx *sql.Tx, actorID int64, selection string) (bool, error) {
	if actorID <= 0 {
		return false, ErrForbidden
	}
	var isAdmin bool
	err := tx.QueryRowContext(ctx, `SELECT is_admin=1 FROM v2_user WHERE id=$1 AND (is_admin=1 OR is_staff=1) AND banned=0 FOR SHARE`, actorID).Scan(&isAdmin)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !isAdmin && selection != "" && selection != "default") {
		return false, ErrForbidden
	}
	return isAdmin, err
}

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
