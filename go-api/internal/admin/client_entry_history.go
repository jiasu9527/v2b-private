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
	host, err := normalizeHistoricalEntryWindow(req.HistoryHost, from, to)
	if err != nil {
		return out, err
	}
	ids, err := entryevidence.HistoricalUserIDs(ctx, s.db, host, from, to)
	if err != nil {
		return out, err
	}
	return ClientEntryUserPolicySplitPreviewResult{HistoryHost: host, From: from, To: to, UserCount: int64(len(ids)), SnapshotHash: historicalEntryHash(host, from, to, ids)}, nil
}
