package nodeapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// Archive blocks are immutable and created under the exclusive archive lock.
// Reports hold the corresponding shared lock while checking hot and cold IDs.
// This preserves (node,eventID) idempotency even if a retry changes its time.
// Only Bloom metadata is cached, never full payloads; positives are verified.
type observationArchiveRetryHash struct {
	a, b uint64
}

// Hash each incoming identity once, not once per archived block. At seven days
// of retained traffic, one node may be referenced by many thousands of blocks.
func observationArchiveRetryHashes(nodeID int64, events []ObservationEvent) []observationArchiveRetryHash {
	hashes := make([]observationArchiveRetryHash, len(events))
	for i, event := range events {
		hashes[i].a, hashes[i].b = observationArchiveBloomHash(nodeID, event.EventID)
	}
	return hashes
}

func observationArchiveRetryMayContain(bloom []byte, h observationArchiveRetryHash) bool {
	if len(bloom) < 64 || len(bloom) > maxObservationArchiveEvents*4 {
		return true // malformed metadata must force exact verification
	}
	bits := uint64(len(bloom)) * 8
	for i := uint64(0); i < observationArchiveBloomK; i++ {
		index := (h.a + i*h.b) % bits
		if bloom[index/8]&(1<<(index%8)) == 0 {
			return false
		}
	}
	return true
}

type observationArchiveHeader struct {
	id     int64
	lastAt int64
	bloom  []byte
}

func (s *DBService) archivedObservationRetries(ctx context.Context, tx *sql.Tx, nodeID int64, events []ObservationEvent) (map[string]bool, error) {
	s.observationArchiveMu.Lock()
	if s.observationArchiveByNode == nil {
		s.observationArchiveByNode = make(map[int64][]*observationArchiveHeader)
	}
	if s.observationArchiveNodeLast == nil {
		s.observationArchiveNodeLast = make(map[int64]int64)
	}
	if s.observationArchiveHeaders == nil {
		s.observationArchiveHeaders = make(map[int64]*observationArchiveHeader)
	}
	// Load only this node on its first report, not the entire site's seven-day
	// Bloom index. Multi-node blocks share one immutable header in memory.
	rows, err := tx.QueryContext(ctx, `SELECT id,last_at,event_bloom,to_json(node_ids) FROM v2_node_observation_archive WHERE id>$1 AND node_ids @> ARRAY[$2::bigint] ORDER BY id`, s.observationArchiveNodeLast[nodeID], nodeID)
	if err != nil {
		s.observationArchiveMu.Unlock()
		return nil, err
	}
	for rows.Next() {
		var h observationArchiveHeader
		var idsJSON []byte
		if err = rows.Scan(&h.id, &h.lastAt, &h.bloom, &idsJSON); err != nil {
			break
		}
		var nodes []int64
		if err = json.Unmarshal(idsJSON, &nodes); err != nil {
			break
		}
		header := s.observationArchiveHeaders[h.id]
		if header == nil {
			header = &h
			s.observationArchiveHeaders[h.id] = header
		}
		s.observationArchiveByNode[nodeID] = append(s.observationArchiveByNode[nodeID], header)
		s.observationArchiveNodeLast[nodeID] = h.id
		s.observationArchiveLast = max(s.observationArchiveLast, h.id)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		s.observationArchiveMu.Unlock()
		return nil, err
	}
	now := time.Now().Unix()
	cutoff := now - observationKeepDays*86400
	// Expired metadata only retains a small amount of memory until the next
	// hour. Do not walk every node's seven-day index on every 100-event report.
	if s.observationArchivePrunedAt == 0 || now-s.observationArchivePrunedAt >= 3600 {
		for id, headers := range s.observationArchiveByNode {
			keep := headers[:0]
			for _, h := range headers {
				if h.lastAt >= cutoff {
					keep = append(keep, h)
				}
			}
			// Release stale backing-array references as well as slice elements.
			clear(headers[len(keep):])
			if len(keep) == 0 {
				delete(s.observationArchiveByNode, id)
			} else {
				s.observationArchiveByNode[id] = keep
			}
		}
		for id, h := range s.observationArchiveHeaders {
			if h.lastAt < cutoff {
				delete(s.observationArchiveHeaders, id)
			}
		}
		s.observationArchivePrunedAt = now
	}
	// Header payloads are immutable. Snapshot only the current node's pointers
	// then let other nodes refresh their metadata concurrently with Bloom work.
	headers := append([]*observationArchiveHeader(nil), s.observationArchiveByNode[nodeID]...)
	s.observationArchiveMu.Unlock()
	hashes := observationArchiveRetryHashes(nodeID, events)
	var candidates []int64
	for i, h := range headers {
		if i%128 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		if h.lastAt < cutoff {
			continue
		}
		for _, hash := range hashes {
			if observationArchiveRetryMayContain(h.bloom, hash) {
				candidates = append(candidates, h.id)
				break
			}
		}
	}
	found := make(map[string]bool)
	if len(candidates) == 0 {
		return found, nil
	}
	wanted := make(map[string]bool, len(events))
	for _, event := range events {
		wanted[event.EventID] = true
	}
	idsJSON, _ := json.Marshal(candidates)
	rows, err = tx.QueryContext(ctx, `SELECT payload,payload_sha256 FROM v2_node_observation_archive WHERE id IN (SELECT value::bigint FROM jsonb_array_elements_text($1::jsonb))`, string(idsJSON))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var payload, sha []byte
		if err = rows.Scan(&payload, &sha); err != nil {
			return nil, err
		}
		decoded, decodeErr := decodeObservationArchive(payload, sha)
		if decodeErr != nil {
			return nil, fmt.Errorf("verify observation archive for retry: %w", decodeErr)
		}
		for _, event := range decoded {
			if event.NodeType == "v2node" && event.NodeID == nodeID && wanted[event.EventID] {
				found[event.EventID] = true
			}
		}
	}
	return found, rows.Err()
}
