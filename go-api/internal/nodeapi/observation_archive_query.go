package nodeapi

import (
	"container/heap"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Archive reads use the caller's repeatable-read transaction, so moving an
// event from its hot table to a compressed block cannot hide or duplicate it
// between the two halves of one request.
const observationArchiveQueryBatch = 16

type observationArchiveQueryBlock struct {
	id         int64
	userID     int64
	firstAt    int64
	lastAt     int64
	eventCount int64
	payload    []byte
	checksum   []byte
}

func observationArchiveWhere(filter ObservationFilter, cursor observationCursor) (string, []any) {
	from := max(filter.From, time.Now().Unix()-observationKeepDays*86400)
	clauses := []string{"a.last_at >= $1", "a.first_at < $2"}
	args := []any{from, filter.To}
	add := func(expression string, value any) {
		args = append(args, value)
		clauses = append(clauses, fmt.Sprintf(expression, len(args)))
	}
	if filter.UserID > 0 {
		add("a.user_id = $%d", filter.UserID)
	}
	if filter.Account != "" {
		add("a.user_id IN (SELECT id FROM v2_user WHERE lower(email)=lower($%d))", filter.Account)
	}
	if filter.NodeID > 0 {
		add("a.node_ids @> ARRAY[$%d::bigint]", filter.NodeID)
	}
	if filter.Target != "" {
		if net.ParseIP(filter.Target) != nil {
			add("a.target_ips @> ARRAY[$%d::text]", filter.Target)
		} else {
			add("a.target_hosts @> ARRAY[$%d::text]", filter.Target)
		}
	}
	if cursor.At > 0 {
		// A block may straddle the cursor and must still be decoded. The full
		// tuple comparison is applied to each event, not just the block time.
		add("a.first_at <= $%d", cursor.At)
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

func scanObservationArchiveBlocks(ctx context.Context, tx *sql.Tx, filter ObservationFilter, cursor observationCursor, visit func(observationArchiveQueryBlock) (bool, error)) error {
	if tx == nil {
		return fmt.Errorf("observation archive read requires a transaction")
	}
	where, args := observationArchiveWhere(filter, cursor)
	var previousAt, previousID int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		queryWhere := where
		queryArgs := append([]any(nil), args...)
		if previousID > 0 {
			base := len(queryArgs)
			queryWhere += fmt.Sprintf(" AND (a.last_at,a.id) < ($%d,$%d)", base+1, base+2)
			queryArgs = append(queryArgs, previousAt, previousID)
		}
		queryArgs = append(queryArgs, observationArchiveQueryBatch)
		rows, err := tx.QueryContext(ctx, `SELECT a.id,a.user_id,a.first_at,a.last_at,a.event_count,a.payload,a.payload_sha256
FROM v2_node_observation_archive a`+queryWhere+` ORDER BY a.last_at DESC,a.id DESC LIMIT $`+strconv.Itoa(len(queryArgs)), queryArgs...)
		if err != nil {
			return fmt.Errorf("read observation archive blocks: %w", err)
		}
		// Close the result before invoking callbacks. This keeps callback work
		// off a live query and bounds each network/result buffer to 16 blocks.
		blocks := make([]observationArchiveQueryBlock, 0, observationArchiveQueryBatch)
		for rows.Next() {
			var block observationArchiveQueryBlock
			if err = rows.Scan(&block.id, &block.userID, &block.firstAt, &block.lastAt, &block.eventCount, &block.payload, &block.checksum); err != nil {
				rows.Close()
				return fmt.Errorf("scan observation archive block: %w", err)
			}
			blocks = append(blocks, block)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return fmt.Errorf("read observation archive blocks: %w", err)
		}
		for _, block := range blocks {
			if err = ctx.Err(); err != nil {
				return err
			}
			stop, err := visit(block)
			if err != nil {
				return err
			}
			if stop {
				return nil
			}
		}
		if len(blocks) < observationArchiveQueryBatch {
			return nil
		}
		last := blocks[len(blocks)-1]
		previousAt, previousID = last.lastAt, last.id
	}
}

func decodeObservationQueryBlock(block observationArchiveQueryBlock) ([]archivedObservationEvent, error) {
	events, err := decodeObservationArchive(block.payload, block.checksum)
	if err != nil {
		return nil, fmt.Errorf("observation archive block %d failed integrity check: %w", block.id, err)
	}
	if int64(len(events)) != block.eventCount || block.eventCount <= 0 || block.firstAt > block.lastAt {
		return nil, fmt.Errorf("observation archive block %d has inconsistent metadata", block.id)
	}
	firstAt, lastAt := events[0].OccurredAt, events[0].OccurredAt
	for _, event := range events {
		if event.UserID != block.userID {
			return nil, fmt.Errorf("observation archive block %d has inconsistent user", block.id)
		}
		firstAt = min(firstAt, event.OccurredAt)
		lastAt = max(lastAt, event.OccurredAt)
	}
	if firstAt != block.firstAt || lastAt != block.lastAt {
		return nil, fmt.Errorf("observation archive block %d has inconsistent time bounds", block.id)
	}
	return events, nil
}

func archivedObservationMatches(event archivedObservationEvent, filter ObservationFilter, cursor observationCursor, from int64) bool {
	if event.OccurredAt < from || event.OccurredAt >= filter.To ||
		filter.NodeType != "" && event.NodeType != NormalizeNodeType(filter.NodeType) ||
		filter.NodeID > 0 && event.NodeID != filter.NodeID ||
		filter.UserID > 0 && event.UserID != filter.UserID {
		return false
	}
	if filter.Target != "" {
		if net.ParseIP(filter.Target) != nil {
			if event.TargetIP != filter.Target {
				return false
			}
		} else if event.TargetHost != filter.Target {
			return false
		}
	}
	if cursor.At > 0 && (event.OccurredAt > cursor.At ||
		event.OccurredAt == cursor.At && (event.NodeID > cursor.NodeID || event.NodeID == cursor.NodeID && event.EventID >= cursor.EventID)) {
		return false
	}
	return true
}

// The heap root is the oldest selected event, i.e. the first to discard when a
// newer event arrives. The event-ID ordering is bytewise, matching COLLATE "C"
// in the hot SQL query and its keyset cursor predicate.
type observationArchiveEventHeap []archivedObservationEvent

func observationArchiveEventOlder(a, b archivedObservationEvent) bool {
	if a.OccurredAt != b.OccurredAt {
		return a.OccurredAt < b.OccurredAt
	}
	if a.NodeID != b.NodeID {
		return a.NodeID < b.NodeID
	}
	return a.EventID < b.EventID
}
func (h observationArchiveEventHeap) Len() int { return len(h) }
func (h observationArchiveEventHeap) Less(i, j int) bool {
	return observationArchiveEventOlder(h[i], h[j])
}
func (h observationArchiveEventHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *observationArchiveEventHeap) Push(v any)   { *h = append(*h, v.(archivedObservationEvent)) }
func (h *observationArchiveEventHeap) Pop() any {
	old := *h
	last := old[len(old)-1]
	*h = old[:len(old)-1]
	return last
}

func (s *DBService) archivedObservationEvents(ctx context.Context, tx *sql.Tx, filter ObservationFilter, cursor observationCursor, limit int) ([]map[string]any, error) {
	if limit < 1 || limit > 10101 {
		return nil, ErrObservationInvalid
	}
	selected := make(observationArchiveEventHeap, 0, limit)
	from := max(filter.From, time.Now().Unix()-observationKeepDays*86400)
	err := scanObservationArchiveBlocks(ctx, tx, filter, cursor, func(block observationArchiveQueryBlock) (bool, error) {
		// LastAt is an upper bound for every event in this and all later
		// blocks. Strict inequality is necessary: equal-second IDs/nodes may
		// still replace the current oldest candidate.
		if len(selected) == limit && block.lastAt < selected[0].OccurredAt {
			return true, nil
		}
		events, err := decodeObservationQueryBlock(block)
		if err != nil {
			return false, err
		}
		for i, event := range events {
			if i%256 == 0 {
				if err := ctx.Err(); err != nil {
					return false, err
				}
			}
			if !archivedObservationMatches(event, filter, cursor, from) {
				continue
			}
			if len(selected) < limit {
				heap.Push(&selected, event)
			} else if observationArchiveEventOlder(selected[0], event) {
				selected[0] = event
				heap.Fix(&selected, 0)
			}
		}
		return false, nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(selected, func(i, j int) bool { return observationArchiveEventOlder(selected[j], selected[i]) })
	users := make([]int64, 0, len(selected))
	seen := make(map[int64]bool, len(selected))
	for _, event := range selected {
		if !seen[event.UserID] {
			seen[event.UserID] = true
			users = append(users, event.UserID)
		}
	}
	emails := make(map[int64]string, len(users))
	if len(users) > 0 {
		encoded, _ := json.Marshal(users)
		rows, err := tx.QueryContext(ctx, `SELECT id,email FROM v2_user WHERE id IN (SELECT value::bigint FROM jsonb_array_elements_text($1::jsonb))`, string(encoded))
		if err != nil {
			return nil, fmt.Errorf("read archived observation account labels: %w", err)
		}
		for rows.Next() {
			var id int64
			var email string
			if err = rows.Scan(&id, &email); err != nil {
				rows.Close()
				return nil, err
			}
			emails[id] = email
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	items := make([]map[string]any, 0, len(selected))
	for _, event := range selected {
		items = append(items, archivedObservationMap(event, emails[event.UserID]))
	}
	return items, nil
}

func archivedObservationMap(event archivedObservationEvent, email string) map[string]any {
	return map[string]any{
		"node_type": event.NodeType, "node_id": event.NodeID, "event_id": event.EventID,
		"user_id": event.UserID, "email": email, "occurred_at": event.OccurredAt,
		"client_ip": event.ClientIP, "target_host": event.TargetHost, "target_ip": event.TargetIP,
		"target_port": event.TargetPort, "network": event.Network, "result": event.Result,
		"uplink_bytes": event.UplinkBytes, "downlink_bytes": event.DownlinkBytes,
	}
}

// scanArchivedObservations visits one decompressed block at a time. No event
// list grows with the requested time span. The caller owns aggregate memory,
// cancellation and the transaction shared with the hot-table query.
func scanArchivedObservations(ctx context.Context, tx *sql.Tx, filter ObservationFilter, visit func(archivedObservationEvent) error) error {
	if visit == nil {
		return fmt.Errorf("observation archive scan requires a visitor")
	}
	from := max(filter.From, time.Now().Unix()-observationKeepDays*86400)
	return scanObservationArchiveBlocks(ctx, tx, filter, observationCursor{}, func(block observationArchiveQueryBlock) (bool, error) {
		events, err := decodeObservationQueryBlock(block)
		if err != nil {
			return false, err
		}
		for i, event := range events {
			if i%256 == 0 {
				if err := ctx.Err(); err != nil {
					return false, err
				}
			}
			if archivedObservationMatches(event, filter, observationCursor{}, from) {
				if err := visit(event); err != nil {
					return false, err
				}
			}
		}
		return false, nil
	})
}
