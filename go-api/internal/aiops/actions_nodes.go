package aiops

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"forest/go-api/internal/admin"
)

type nodeUpdatePayload struct {
	NodeType string         `json:"node_type"`
	NodeID   int64          `json:"node_id"`
	Changes  map[string]any `json:"changes"`
	Before   map[string]any `json:"before,omitempty"`
	After    map[string]any `json:"after,omitempty"`
}

func loadNodeUpdateRow(ctx context.Context, tx *sql.Tx, kind string, id int64, lock bool) (map[string]any, string, error) {
	table := nodeTables[kind]
	if table == "" || id <= 0 {
		return nil, "", ErrValidation
	}
	query := `SELECT to_jsonb(n)::TEXT FROM ` + table + ` n WHERE id=$1`
	if lock {
		query += ` FOR UPDATE`
	}
	var raw string
	if err := tx.QueryRowContext(ctx, query, id).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, "", ErrNotFound
		}
		return nil, "", err
	}
	var row map[string]any
	if json.Unmarshal([]byte(raw), &row) != nil {
		return nil, "", ErrUnavailable
	}
	return row, hashOperation(raw), nil
}
func checkNodeUpdateGroups(ctx context.Context, tx *sql.Tx, changes map[string]any) error {
	groups, ok := changes["group_id"].([]any)
	if !ok {
		return nil
	}
	for _, group := range groups {
		var id int64
		if err := tx.QueryRowContext(ctx, `SELECT id FROM v2_server_group WHERE id=$1 FOR SHARE`, int64(group.(float64))).Scan(&id); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
	}
	return nil
}
func (s *Service) captureNodeUpdateAction(ctx context.Context, tx *sql.Tx, spec actionSpec) (json.RawMessage, string, string, error) {
	var p nodeUpdatePayload
	if spec.Kind != "node_update" || json.Unmarshal(spec.Payload, &p) != nil || p.NodeID != spec.TargetID || p.NodeType != spec.TargetType {
		return nil, "", "", ErrValidation
	}
	if admin.ValidateAIManagedNodeChanges(p.NodeType, p.Changes) != nil {
		return nil, "", "", ErrValidation
	}
	row, snapshot, err := loadNodeUpdateRow(ctx, tx, p.NodeType, p.NodeID, true)
	if err != nil {
		return nil, "", "", err
	}
	if err = checkNodeUpdateGroups(ctx, tx, p.Changes); err != nil {
		return nil, "", "", err
	}
	_, before, after, err := admin.PrepareAIManagedNodeUpdate(p.NodeType, row, p.Changes)
	if err != nil {
		return nil, "", "", ErrValidation
	}
	p.Before, p.After = before, after
	raw, _ := json.Marshal(p)
	if len(raw) > 32768 {
		return nil, "", "", ErrValidation
	}
	return raw, snapshot, fmt.Sprint(row["name"]), nil
}
func applyNodeUpdateAction(ctx context.Context, tx *sql.Tx, a Action) error {
	var p nodeUpdatePayload
	if a.Kind != "node_update" || json.Unmarshal(a.Payload, &p) != nil || p.NodeID != a.TargetID || p.NodeType != a.TargetType {
		return ErrValidation
	}
	row, _, err := loadNodeUpdateRow(ctx, tx, p.NodeType, p.NodeID, true)
	if err != nil {
		return err
	}
	values, _, _, err := admin.PrepareAIManagedNodeUpdate(p.NodeType, row, p.Changes)
	if err != nil {
		return ErrValidation
	}
	if err = checkNodeUpdateGroups(ctx, tx, p.Changes); err != nil {
		return err
	}
	keys := []string{}
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	args := []any{p.NodeID}
	sets := []string{}
	for _, key := range keys {
		args = append(args, values[key])
		sets = append(sets, `"`+strings.ReplaceAll(key, `"`, `""`)+`"=$`+fmt.Sprint(len(args)))
	}
	args = append(args, time.Now().Unix())
	sets = append(sets, `updated_at=$`+fmt.Sprint(len(args)))
	result, err := tx.ExecContext(ctx, `UPDATE `+nodeTables[p.NodeType]+` SET `+strings.Join(sets, ",")+` WHERE id=$1`, args...)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	return nil
}
