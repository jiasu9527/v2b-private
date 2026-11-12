package admin

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
)

type clientEntryMonitorRef struct{ id, policyID int64 }

// Automatic splitting holds monitor/target locks before changing topology.
// Policy deletion does the reverse through ON DELETE CASCADE. Never wait for
// topology while holding monitors: a failed try-lock releases these locks via
// rollback, allowing either writer to finish without a deadlock.
func lockClientEntryMonitorTopology(ctx context.Context, tx *sql.Tx) ([]clientEntryMonitorRef, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, policy_id FROM v2_client_entry_monitor ORDER BY id FOR UPDATE`)
	if err != nil {
		return nil, fmt.Errorf("lock client entry monitors: %w", err)
	}
	monitors := make([]clientEntryMonitorRef, 0)
	for rows.Next() {
		var item clientEntryMonitorRef
		if err := rows.Scan(&item.id, &item.policyID); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan locked client entry monitor: %w", err)
		}
		monitors = append(monitors, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate locked client entry monitors: %w", err)
	}
	_ = rows.Close()
	var locked bool
	if err := tx.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock($1)`, clientEntryVisibleOrderLockKey).Scan(&locked); err != nil {
		return nil, fmt.Errorf("lock client entry monitor topology: %w", err)
	}
	if !locked {
		return nil, ErrClientEntryMonitorRevisionConflict
	}
	return monitors, nil
}

func readClientEntryMonitorPolicies(ctx context.Context, q clientEntryPolicyQueryer) ([]ClientEntryMonitorPolicy, error) {
	policies, err := readClientEntryUserPolicies(ctx, q)
	if err != nil {
		return nil, err
	}
	nodes, err := readClientEntryMonitorNodes(ctx, q, policies)
	if err != nil {
		return nil, err
	}
	return buildClientEntryMonitorPolicies(policies, nodes), nil
}

// Validate only the policies being saved: unrelated rule changes should not
// prevent incremental configuration. This check must precede every write.
func revalidateClientEntryMonitorPolicies(ctx context.Context, tx *sql.Tx, before map[int64]ClientEntryMonitorPolicy, items []ClientEntryMonitorSaveItem) error {
	current, err := readClientEntryMonitorPolicies(ctx, tx)
	if err != nil {
		return err
	}
	byID := make(map[int64]ClientEntryMonitorPolicy, len(current))
	for _, policy := range current {
		byID[policy.ID] = policy
	}
	for _, item := range items {
		if !reflect.DeepEqual(before[item.PolicyID], byID[item.PolicyID]) {
			return ErrClientEntryMonitorRevisionConflict
		}
	}
	return nil
}
