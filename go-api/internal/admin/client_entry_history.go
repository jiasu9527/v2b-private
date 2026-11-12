package admin

import (
	"context"
	"forest/go-api/internal/entryevidence"
	"time"
)

func normalizeHistoricalEntryWindow(host string, from, to int64) (string, error) {
	host, err := entryevidence.HistoryHost(host)
	if err != nil || from <= 0 || to <= from || to > time.Now().Unix()+1 || to-from > 365*86400 {
		return "", ErrEntryEvidenceInvalid
	}
	return host, nil
}
func historicalEntryHash(host string, from, to int64, ids []int64) string {
	return aiEntrySplitHash([]any{"historical-delivery-v1", host, from, to, ids})
}

func normalizeHistoricalEntrySelection(host string, groups []int64, excludes []string, from, to int64) (entryevidence.HistorySelection, error) {
	selection, err := entryevidence.NormalizeHistorySelection(entryevidence.HistorySelection{Host: host, GroupIDs: groups, ExcludeHosts: excludes})
	if err != nil || from <= 0 || to <= from || to > time.Now().Unix()+1 || to-from > 365*86400 {
		return entryevidence.HistorySelection{}, ErrEntryEvidenceInvalid
	}
	return selection, nil
}

func historicalEntrySelectionHash(selection entryevidence.HistorySelection, from, to int64, ids []int64) string {
	if len(selection.GroupIDs) == 0 && len(selection.ExcludeHosts) == 0 {
		return historicalEntryHash(selection.Host, from, to, ids)
	}
	return aiEntrySplitHash([]any{"historical-delivery-selection-v2", selection, from, to, ids})
}
func (s *DBService) previewHistoricalEntrySplit(ctx context.Context, req ClientEntryUserPolicySplitPreviewRequest) (ClientEntryUserPolicySplitPreviewResult, error) {
	var out ClientEntryUserPolicySplitPreviewResult
	to := req.HistoryTo
	if to == 0 {
		to = time.Now().Unix()
	}
	from := req.HistoryFrom
	if from == 0 {
		minutes, err := normalizeClientEntryUserPolicySplitMinutes(req.Minutes)
		if err != nil {
			return out, err
		}
		from = to - minutes*60
	}
	selection, err := normalizeHistoricalEntrySelection(req.HistoryHost, req.HistoryGroupIDs, req.HistoryExcludeHosts, from, to)
	if err != nil {
		return out, err
	}
	ids, err := entryevidence.HistoricalSelectionUserIDs(ctx, s.db, selection, from, to)
	if err != nil {
		return out, err
	}
	return ClientEntryUserPolicySplitPreviewResult{HistoryHost: selection.Host, HistoryGroupIDs: selection.GroupIDs, HistoryExcludeHosts: selection.ExcludeHosts, From: from, To: to, UserCount: int64(len(ids)), SnapshotHash: historicalEntrySelectionHash(selection, from, to, ids)}, nil
}
