package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"time"

	"forest/go-api/internal/entryevidence"
)

// Acquired is evidence of server-side writes only, never proof of a client
// connection or a person's responsibility for a network incident.
type EntryEvidenceBranchObservation struct {
	Branch             string `json:"branch"`
	PolicyID           int64  `json:"policy_id"`
	GroupID            int64  `json:"group_id"`
	EntryHost          string `json:"entry_host"`
	FixedUsers         int64  `json:"fixed_users"`
	AcquiredUsers      int64  `json:"acquired_users"`
	NoRecordedDelivery int64  `json:"no_recorded_delivery"`
	FirstDeliveryAt    int64  `json:"first_delivery_at"`
	LastDeliveryAt     int64  `json:"last_delivery_at"`
	EnabledAt          int64  `json:"enabled_at"`
	ObservedSeconds    int64  `json:"observed_seconds"`
	CoverageGap        bool   `json:"coverage_gap"`
	RefreshedAt        int64  `json:"refreshed_at"`
}

func (s *DBService) refreshEntryEvidenceCaseObservations(ctx context.Context, c *EntryEvidenceCase) error {
	c.Observations = nil
	c.ObservationWarning = ""
	if c.Execution == nil {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Serialize refreshes for this case, but never block the topology editors.
	var status string
	if err = tx.QueryRowContext(ctx, `SELECT status FROM v2_entry_evidence_case WHERE id=$1 FOR UPDATE`, c.ID).Scan(&status); err != nil {
		return err
	}
	if status != "executed" {
		return ErrEntryEvidenceConflict
	}
	now := time.Now().Unix()
	var keepDays int64
	if err = tx.QueryRowContext(ctx, `SELECT keep_days FROM v2_entry_delivery_settings WHERE id=1`).Scan(&keepDays); err != nil {
		return err
	}
	var last int64
	var priorGap bool
	err = tx.QueryRowContext(ctx, `SELECT last_refreshed_at,coverage_gap FROM v2_entry_evidence_observation_state WHERE case_id=$1`, c.ID).Scan(&last, &priorGap)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if last == 0 {
		last = c.Execution.ExecutedAt
	}
	gap := priorGap || last < now-keepDays*86400
	// Always read the retained interval. A response may finish before its
	// evidence transaction commits, so a timestamp cursor could miss it.
	for _, branch := range []struct {
		name  string
		group int64
		host  string
		count int64
	}{
		{"A", c.Execution.GroupAID, c.Execution.EntryHostA, (c.SelectedCount + 1) / 2},
		{"B", c.Execution.GroupBID, c.Execution.EntryHostB, c.SelectedCount / 2},
	} {
		_, err = tx.ExecContext(ctx, `INSERT INTO v2_entry_evidence_exposure(case_id,branch,user_id,first_at,last_at,first_request_id,last_request_id)
SELECT $1,$2,r.user_id,MIN(r.completed_at),MAX(r.completed_at),
 (array_agg(r.id ORDER BY r.completed_at,r.id))[1],(array_agg(r.id ORDER BY r.completed_at DESC,r.id DESC))[1]
FROM v2_entry_delivery_request r
JOIN v2_entry_evidence_case_candidate candidate ON candidate.case_id=$1 AND candidate.user_id=r.user_id AND candidate.selected=TRUE AND candidate.branch=$2
WHERE r.response_complete=TRUE AND r.coverage='complete' AND r.status BETWEEN 200 AND 299 AND r.completed_at>=$3
AND EXISTS(SELECT 1 FROM v2_entry_delivery_node n WHERE n.request_id=r.id AND n.policy_id=$4 AND n.group_id=$5
 AND (LOWER(n.delivered_host)=LOWER($6) OR (n.resolved=TRUE AND LOWER(n.source_host)=LOWER($6) AND n.delivered_host<>'')) AND n.port=$7)
GROUP BY r.user_id
ON CONFLICT(case_id,branch,user_id) DO UPDATE SET
 first_request_id=CASE WHEN EXCLUDED.first_at<v2_entry_evidence_exposure.first_at THEN EXCLUDED.first_request_id ELSE v2_entry_evidence_exposure.first_request_id END,
 last_request_id=CASE WHEN EXCLUDED.last_at>=v2_entry_evidence_exposure.last_at THEN EXCLUDED.last_request_id ELSE v2_entry_evidence_exposure.last_request_id END,
 first_at=LEAST(v2_entry_evidence_exposure.first_at,EXCLUDED.first_at),last_at=GREATEST(v2_entry_evidence_exposure.last_at,EXCLUDED.last_at)`, c.ID, branch.name, c.Execution.ExecutedAt, c.Execution.PolicyID, branch.group, branch.host, fmt.Sprint(c.Query.Port))
		if err != nil {
			return err
		}
		v := EntryEvidenceBranchObservation{Branch: branch.name, PolicyID: c.Execution.PolicyID, GroupID: branch.group, EntryHost: branch.host, FixedUsers: branch.count, EnabledAt: c.Execution.ExecutedAt, ObservedSeconds: max(0, now-c.Execution.ExecutedAt), CoverageGap: gap, RefreshedAt: now}
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(MIN(first_at),0),COALESCE(MAX(last_at),0) FROM v2_entry_evidence_exposure WHERE case_id=$1 AND branch=$2`, c.ID, branch.name).Scan(&v.AcquiredUsers, &v.FirstDeliveryAt, &v.LastDeliveryAt); err != nil {
			return err
		}
		v.NoRecordedDelivery = max(0, v.FixedUsers-v.AcquiredUsers)
		c.Observations = append(c.Observations, v)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO v2_entry_evidence_observation_state(case_id,last_refreshed_at,coverage_gap) VALUES($1,$2,$3) ON CONFLICT(case_id) DO UPDATE SET last_refreshed_at=EXCLUDED.last_refreshed_at,coverage_gap=EXCLUDED.coverage_gap`, c.ID, now, gap)
	if err != nil {
		return err
	}
	if gap {
		c.ObservationWarning = "有观察间隔超过普通记录保留期，累计人数是已保存证据的下限；未记录下发不等于未获得入口"
	}
	if err = refreshEntryEvidenceProbeEvents(ctx, tx, c); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *DBService) RefreshEntryEvidenceObservations(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM v2_entry_evidence_case WHERE status='executed' ORDER BY id`)
	if err != nil {
		return err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var refreshErr error
	for _, id := range ids {
		c, err := readEvidenceCase(ctx, s.db, id, false)
		if err != nil {
			if !errors.Is(err, ErrEntryEvidenceNotFound) {
				refreshErr = err
			}
			continue
		}
		if err = s.refreshEntryEvidenceCaseObservations(ctx, &c); err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				refreshErr = err
			}
		}
	}
	return refreshErr
}

// Refresh frozen exposure before retention, including the first run after a
// restart. Never delete the raw source if this refresh has not completed.
func (s *DBService) RunEntryEvidenceMaintenance(ctx context.Context, store *entryevidence.Store) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	var lastCleanup time.Time
	for {
		run, cancel := context.WithTimeout(ctx, time.Minute)
		err := s.RefreshEntryEvidenceObservations(run)
		cancel()
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			log.Print("entry evidence: observation refresh incomplete; retention cleanup deferred")
		} else if store != nil && time.Since(lastCleanup) >= time.Hour {
			cleanup, cancel := context.WithTimeout(ctx, 30*time.Second)
			err := store.Cleanup(cleanup)
			cancel()
			if err == nil {
				lastCleanup = time.Now()
			} else if ctx.Err() == nil {
				log.Print("entry evidence: retention cleanup incomplete; will retry")
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
