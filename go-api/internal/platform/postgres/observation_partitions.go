package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const observationPartitionPrefix = "v2_node_observation_day_"

func observationUTCDay(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// Precreate all accepted late-arrival days plus tomorrow. No default partition
// is used: it could silently become an unbounded, never-expiring event heap.
func EnsureObservationPartitions(ctx context.Context, db *sql.DB, now time.Time) error {
	if db == nil {
		return fmt.Errorf("db is nil")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SET LOCAL lock_timeout='2s'`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(582791,0)`); err != nil {
		return err
	}
	today := observationUTCDay(now)
	for offset := -7; offset <= 1; offset++ {
		day := today.AddDate(0, 0, offset)
		name := observationPartitionPrefix + day.Format("20060102")
		_, err = tx.ExecContext(ctx, fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s PARTITION OF v2_node_observation_compact FOR VALUES FROM (%d) TO (%d)`, name, day.Unix(), day.AddDate(0, 0, 1).Unix()))
		if err != nil {
			return fmt.Errorf("create observation partition: %w", err)
		}
	}
	return tx.Commit()
}

var observationPartitionBounds = regexp.MustCompile(`^FOR VALUES FROM \('?([0-9]+)'?\) TO \('?([0-9]+)'?\)$`)

func expiredObservationPartition(name, bound string, cutoff int64) bool {
	if len(name) != len(observationPartitionPrefix)+8 || name[:len(observationPartitionPrefix)] != observationPartitionPrefix {
		return false
	}
	day, err := time.Parse("20060102", name[len(observationPartitionPrefix):])
	if err != nil {
		return false
	}
	parts := observationPartitionBounds.FindStringSubmatch(bound)
	if len(parts) != 3 {
		return false
	}
	from, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return false
	}
	to, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return false
	}
	return from == day.Unix() && to == day.AddDate(0, 0, 1).Unix() && to <= cutoff
}

// Whole-day reclamation retains at least seven days and physically less than
// eight days. The public query still exposes only its seven-day window.
func DeleteExpiredObservationPartitions(ctx context.Context, db *sql.DB, cutoff int64) error {
	if db == nil {
		return fmt.Errorf("db is nil")
	}
	rows, err := db.QueryContext(ctx, `SELECT namespace.nspname,child.relname,pg_get_expr(child.relpartbound,child.oid)
FROM pg_inherits i JOIN pg_class child ON child.oid=i.inhrelid
JOIN pg_namespace namespace ON namespace.oid=child.relnamespace
WHERE i.inhparent='v2_node_observation_compact'::regclass`)
	if err != nil {
		return err
	}
	var expired []string
	for rows.Next() {
		var schema, name, bound string
		if err = rows.Scan(&schema, &name, &bound); err != nil {
			rows.Close()
			return err
		}
		if expiredObservationPartition(name, bound, cutoff) {
			// A partition can live outside its parent's schema. Never resolve a
			// bare child name through search_path when dropping retained data.
			qualified := `"` + strings.ReplaceAll(schema, `"`, `""`) + `"."` + strings.ReplaceAll(name, `"`, `""`) + `"`
			expired = append(expired, qualified)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, name := range expired {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `SET LOCAL lock_timeout='2s'`); err == nil {
			_, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(582791,0)`)
		}
		if err == nil {
			_, err = tx.ExecContext(ctx, `DROP TABLE IF EXISTS `+name)
		}
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("drop expired observation partition: %w", err)
		}
		if err = tx.Commit(); err != nil {
			return err
		}
	}
	// Keep old, unpartitioned records readable without copying a large table.
	// Only when every old event is expired can its entire heap/indexes be freed.
	var latest sql.NullInt64
	if err = db.QueryRowContext(ctx, `SELECT MAX(occurred_at) FROM v2_node_observation_event`).Scan(&latest); err != nil {
		return err
	}
	if !latest.Valid || latest.Int64 >= cutoff {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SET LOCAL lock_timeout='2s'`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `LOCK TABLE v2_node_observation_event IN ACCESS EXCLUSIVE MODE`); err != nil {
		return err
	}
	if err = tx.QueryRowContext(ctx, `SELECT MAX(occurred_at) FROM v2_node_observation_event`).Scan(&latest); err != nil {
		return err
	}
	if latest.Valid && latest.Int64 < cutoff {
		if _, err = tx.ExecContext(ctx, `TRUNCATE TABLE v2_node_observation_event`); err != nil {
			return err
		}
	}
	return tx.Commit()
}
