package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ReclaimEmptyObservationStorage only truncates physically allocated relations
// with no live rows, after an exclusive table lock and a second emptiness check.
// Queries must lock both hot relations before taking their repeatable-read
// snapshot: PostgreSQL TRUNCATE itself is not MVCC-safe for older snapshots.
// Keep empty daily partitions attached so accepted late arrivals remain valid.
func ReclaimEmptyObservationStorage(ctx context.Context, db *sql.DB, coldBefore int64) (int64, error) {
	if db == nil {
		return 0, fmt.Errorf("db is nil")
	}
	rows, err := db.QueryContext(ctx, `SELECT namespace.nspname,c.relname,COALESCE(pg_get_expr(c.relpartbound,c.oid),'')
 FROM pg_class c JOIN pg_namespace namespace ON namespace.oid=c.relnamespace
 WHERE (c.oid='v2_node_observation_event'::regclass OR c.oid IN
 (SELECT inhrelid FROM pg_inherits WHERE inhparent='v2_node_observation_compact'::regclass))
 AND pg_relation_size(c.oid)>0`)
	if err != nil {
		return 0, err
	}
	var tables []string
	for rows.Next() {
		var schema, name, bound string
		if err = rows.Scan(&schema, &name, &bound); err != nil {
			rows.Close()
			return 0, err
		}
		if name == "v2_node_observation_event" || expiredObservationPartition(name, bound, coldBefore) {
			tables = append(tables, pgx.Identifier{schema, name}.Sanitize())
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	var reclaimed int64
	for _, table := range tables {
		// Avoid taking an exclusive lock on a large, still-live hot table on
		// every maintenance tick. MAX can use the existing time index.
		var newest sql.NullInt64
		if err := db.QueryRowContext(ctx, `SELECT MAX(occurred_at) FROM `+table).Scan(&newest); err != nil {
			return reclaimed, err
		}
		if newest.Valid {
			continue
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return reclaimed, err
		}
		if _, err = tx.ExecContext(ctx, `SET LOCAL lock_timeout='2s'`); err == nil {
			_, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(582791,0)`)
		}
		if err == nil {
			_, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(582791,1)`)
		}
		if err == nil {
			_, err = tx.ExecContext(ctx, `LOCK TABLE `+table+` IN ACCESS EXCLUSIVE MODE`)
		}
		var exists bool
		if err == nil {
			err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM `+table+` LIMIT 1)`).Scan(&exists)
		}
		var size int64
		if err == nil && !exists {
			err = tx.QueryRowContext(ctx, `SELECT pg_total_relation_size($1::regclass)`, table).Scan(&size)
			if err == nil {
				_, err = tx.ExecContext(ctx, `TRUNCATE TABLE `+table)
			}
		}
		if err != nil {
			tx.Rollback()
			return reclaimed, fmt.Errorf("reclaim empty observation storage: %w", err)
		}
		if err = tx.Commit(); err != nil {
			return reclaimed, err
		}
		reclaimed += size
	}
	return reclaimed, nil
}

// Archive blocks cover at most one UTC hour. Delete only fully expired blocks;
// queries clamp the partial boundary hour to the exact seven-day retention.
// A bounded block deletion is far smaller than deleting millions of events.
func DeleteExpiredObservationArchives(ctx context.Context, db *sql.DB, cutoff int64) error {
	if db == nil {
		return fmt.Errorf("db is nil")
	}
	_, err := db.ExecContext(ctx, `DELETE FROM v2_node_observation_archive WHERE id IN
 (SELECT id FROM v2_node_observation_archive WHERE last_at<$1 ORDER BY last_at LIMIT 2000)`, cutoff)
	return err
}

// Used by maintenance and tests so the hot/cold boundary is explicit.
func ObservationColdBefore(now time.Time) int64 {
	return now.UTC().Add(-12 * time.Hour).Truncate(time.Hour).Unix()
}
