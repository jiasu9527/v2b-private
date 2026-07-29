package admin

import (
	"context"
	"fmt"
	"strings"
)

type clientEntryMonitorRunResultStats struct {
	Successful    int64
	Failed        int64
	Targets       int64
	FailedTargets int64
	Probes        int64
}

// populateClientEntryMonitorRunResultStats keeps report totals exact while the
// result details remain bounded. Most runs use the already loaded rows; only a
// truncated run needs the aggregate query.
func (s *DBService) populateClientEntryMonitorRunResultStats(ctx context.Context, runs []ClientEntryMonitorRun) error {
	truncatedIDs := make([]int64, 0)
	for index := range runs {
		if !runs[index].ResultsTruncated {
			applyClientEntryMonitorRunResultStats(&runs[index], summarizeClientEntryMonitorVisibleResults(runs[index].Results))
			continue
		}
		truncatedIDs = append(truncatedIDs, runs[index].ID)
	}
	if len(truncatedIDs) == 0 {
		return nil
	}
	if s == nil || s.db == nil {
		return ErrUnavailable
	}
	placeholders, args := clientEntryMonitorIDPlaceholders(truncatedIDs, 1)
	rows, err := s.db.QueryContext(ctx, `SELECT run_id,
	COUNT(*) FILTER (WHERE success = 1),
	COUNT(*) FILTER (WHERE success = 0),
	COUNT(DISTINCT target_id),
	COUNT(DISTINCT target_id) FILTER (WHERE success = 0),
	COUNT(DISTINCT probe_id)
FROM v2_client_entry_monitor_run_result
WHERE run_id IN (`+strings.Join(placeholders, ",")+`)
GROUP BY run_id`, args...)
	if err != nil {
		return fmt.Errorf("query client entry monitor run result stats: %w", err)
	}
	defer rows.Close()
	statsByRunID := make(map[int64]clientEntryMonitorRunResultStats, len(truncatedIDs))
	for rows.Next() {
		var runID int64
		var stats clientEntryMonitorRunResultStats
		if err := rows.Scan(&runID, &stats.Successful, &stats.Failed, &stats.Targets, &stats.FailedTargets, &stats.Probes); err != nil {
			return fmt.Errorf("scan client entry monitor run result stats: %w", err)
		}
		statsByRunID[runID] = stats
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate client entry monitor run result stats: %w", err)
	}
	for index := range runs {
		if !runs[index].ResultsTruncated {
			continue
		}
		stats, ok := statsByRunID[runs[index].ID]
		if !ok {
			// Preserve report availability for a legacy or externally repaired run
			// whose persisted counter no longer has matching detail rows.
			continue
		}
		applyClientEntryMonitorRunResultStats(&runs[index], stats)
	}
	return nil
}

func summarizeClientEntryMonitorVisibleResults(results []ClientEntryMonitorRunResult) clientEntryMonitorRunResultStats {
	stats := clientEntryMonitorRunResultStats{}
	targets := make(map[int64]struct{})
	failedTargets := make(map[int64]struct{})
	probes := make(map[int64]struct{})
	for _, result := range results {
		if result.Success {
			stats.Successful++
		} else {
			stats.Failed++
			failedTargets[result.TargetID] = struct{}{}
		}
		targets[result.TargetID] = struct{}{}
		probes[result.ProbeID] = struct{}{}
	}
	stats.Targets = int64(len(targets))
	stats.FailedTargets = int64(len(failedTargets))
	stats.Probes = int64(len(probes))
	return stats
}

func applyClientEntryMonitorRunResultStats(run *ClientEntryMonitorRun, stats clientEntryMonitorRunResultStats) {
	if run == nil {
		return
	}
	run.ResultStatsLoaded = true
	run.SuccessfulResults = stats.Successful
	run.FailedResults = stats.Failed
	run.ResultTargetCount = stats.Targets
	run.FailedTargetCount = stats.FailedTargets
	run.ResultProbeCount = stats.Probes
	total := stats.Successful + stats.Failed
	if total > run.ReceivedResults {
		run.ReceivedResults = total
	}
	if total > run.TotalResults {
		run.TotalResults = total
	}
	run.ResultsTruncated = run.TotalResults > int64(len(run.Results))
}
