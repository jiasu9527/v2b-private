package aiops

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"forest/go-api/internal/admin"
)

type entryRuleUpdatePayload struct {
	Kind             string                     `json:"kind"`
	TargetID         int64                      `json:"target_id"`
	Revision         string                     `json:"revision"`
	Changes          map[string]json.RawMessage `json:"changes"`
	PolicyID         int64                      `json:"policy_id"`
	Before           map[string]json.RawMessage `json:"before"`
	EffectiveChanges map[string]json.RawMessage `json:"effective_changes"`
	SharedFields     []string                   `json:"shared_fields"`
	CollectionID     *int64                     `json:"collection_id"`
}

func entryRuleError(err error) error {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return ErrNotFound
	case errors.Is(err, admin.ErrAIEntryRuleInvalid):
		return ErrValidation
	case errors.Is(err, admin.ErrAIEntryRuleConflict):
		return ErrConflict
	default:
		return err
	}
}

func (s *Service) captureEntryRuleAction(ctx context.Context, tx *sql.Tx, spec actionSpec) (json.RawMessage, string, string, error) {
	var p entryRuleUpdatePayload
	if spec.Kind != "entry_rule_update" || json.Unmarshal(spec.Payload, &p) != nil || p.TargetID <= 0 || p.TargetID != spec.TargetID || p.Kind != spec.TargetType || len(p.Revision) != 64 || len(p.Changes) == 0 {
		return nil, "", "", ErrValidation
	}
	before, err := admin.LoadAIEntryRule(ctx, tx, p.Kind, p.TargetID)
	if err != nil {
		return nil, "", "", entryRuleError(err)
	}
	if before.Revision != p.Revision {
		return nil, "", "", ErrConflict
	}
	effective, err := admin.PrepareAIEntryRuleChanges(ctx, tx, before, p.Changes)
	if err != nil {
		return nil, "", "", entryRuleError(err)
	}
	encoded, _ := json.Marshal(before)
	var allBefore map[string]json.RawMessage
	if json.Unmarshal(encoded, &allBefore) != nil {
		return nil, "", "", ErrUnavailable
	}
	p.Before = map[string]json.RawMessage{}
	for key := range effective {
		p.Before[key] = allBefore[key]
	}
	p.EffectiveChanges = effective
	p.PolicyID = before.PolicyID
	p.CollectionID = before.CollectionID
	p.SharedFields = []string{}
	for _, key := range before.SharedFields {
		if _, ok := effective[key]; ok {
			p.SharedFields = append(p.SharedFields, key)
		}
	}
	raw, err := json.Marshal(p)
	// The complete changed lists must fit the review card; no silent truncation.
	if err != nil || len(raw) > 256<<10 {
		return nil, "", "", ErrValidation
	}
	return raw, before.Revision, strings.TrimSpace(before.Name), nil
}

func applyEntryRuleAction(ctx context.Context, tx *sql.Tx, a Action) error {
	var p entryRuleUpdatePayload
	if json.Unmarshal(a.Payload, &p) != nil || p.TargetID != a.TargetID || p.Kind != a.TargetType {
		return ErrValidation
	}
	before, err := admin.LoadAIEntryRule(ctx, tx, p.Kind, p.TargetID)
	if err != nil {
		return entryRuleError(err)
	}
	if before.Revision != p.Revision {
		return ErrConflict
	}
	effective, err := admin.PrepareAIEntryRuleChanges(ctx, tx, before, p.Changes)
	if err != nil {
		return entryRuleError(err)
	}
	return entryRuleError(admin.ApplyAIEntryRuleChanges(ctx, tx, before, effective))
}
