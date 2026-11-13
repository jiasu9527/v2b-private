package aiops

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// This review-card payload contains display metadata only. SSH credentials,
// encrypted or plain, are never included in an action or provider message.
type serverGroupPayload struct {
	ServerID    int64  `json:"server_id"`
	Version     int64  `json:"version"`
	BeforeGroup string `json:"before_group"`
	Group       string `json:"group"`
}

func captureServerGroupAction(ctx context.Context, tx *sql.Tx, spec actionSpec) (json.RawMessage, string, string, error) {
	var p serverGroupPayload
	if spec.Kind != "server_group_set" || spec.TargetType != "server" || json.Unmarshal(spec.Payload, &p) != nil || p.ServerID <= 0 || p.ServerID != spec.TargetID || p.Version <= 0 {
		return nil, "", "", ErrValidation
	}
	group, err := normalizeServerGroup(p.Group)
	if err != nil {
		return nil, "", "", err
	}
	p.Group = group
	var name, before string
	var version int64
	err = tx.QueryRowContext(ctx, `SELECT name,group_name,version FROM v2_ai_server WHERE id=$1 FOR UPDATE`, p.ServerID).Scan(&name, &before, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", "", ErrNotFound
	}
	if err != nil {
		return nil, "", "", err
	}
	if version != p.Version || (p.BeforeGroup != "" && p.BeforeGroup != before) {
		return nil, "", "", ErrConflict
	}
	if before == p.Group {
		return nil, "", "", ErrValidation
	}
	p.BeforeGroup = before
	raw, _ := json.Marshal(p)
	// Any concurrent edit of this server increments version, invalidating the
	// pending proposal even if its display group did not change.
	snapshot, _ := json.Marshal([]any{p.ServerID, version, before})
	return raw, hashOperation(string(snapshot)), name, nil
}

func applyServerGroupAction(ctx context.Context, tx *sql.Tx, a Action) (string, error) {
	var p serverGroupPayload
	if a.Kind != "server_group_set" || a.TargetType != "server" || json.Unmarshal(a.Payload, &p) != nil || p.ServerID <= 0 || p.ServerID != a.TargetID || p.Version <= 0 {
		return "", ErrValidation
	}
	group, err := normalizeServerGroup(p.Group)
	if err != nil || group != p.Group {
		return "", ErrValidation
	}
	var newVersion int64
	err = tx.QueryRowContext(ctx, `UPDATE v2_ai_server SET group_name=$3,version=version+1,updated_at=$4 WHERE id=$1 AND version=$2 AND group_name=$5 RETURNING version`, p.ServerID, p.Version, p.Group, time.Now().Unix(), p.BeforeGroup).Scan(&newVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrConflict
	}
	if err != nil {
		return "", err
	}
	output, _ := json.Marshal(map[string]any{"status": "succeeded", "server_id": p.ServerID, "group": p.Group, "version": newVersion})
	return string(output), nil
}
