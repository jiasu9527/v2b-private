package nodeapi

import (
	"context"
	"encoding/hex"
	"strconv"
	"strings"
)

// Never truncate an identifier. Standard node IDs are 32 lowercase hex digits:
// store all 128 bits plus a format byte, and preserve legacy IDs verbatim.
func compactObservationID(id string) []byte {
	if len(id) == 32 && strings.ToLower(id) == id {
		if raw, err := hex.DecodeString(id); err == nil {
			return append([]byte{0}, raw...)
		}
	}
	return append([]byte{1}, []byte(id)...)
}

func (s *DBService) insertCompactObservation(ctx context.Context, nodeID int64, events []ObservationEvent, receivedAt int64) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SET LOCAL lock_timeout='3s'`); err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock_shared(582791,1)`); err != nil {
		return 0, err
	}
	// PostgreSQL partition uniqueness includes the day/time. Serialize reports
	// from the same node and probe all live partitions to preserve the original
	// (node,eventID) idempotency even if a retry changes its timestamp.
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('observation-report:'||$1::bigint::text,0))`, nodeID); err != nil {
		return 0, err
	}
	archived, err := s.archivedObservationRetries(ctx, tx, nodeID, events)
	if err != nil {
		return 0, err
	}
	remaining := make([]ObservationEvent, 0, len(events))
	for _, event := range events {
		if !archived[event.EventID] {
			remaining = append(remaining, event)
		}
	}
	events = remaining
	if len(events) == 0 {
		return 0, tx.Commit()
	}
	var q strings.Builder
	q.WriteString(`WITH incoming(node_id,user_id,event_key,occurred_at,client_ip,target_host,target_ip,target_port,network,received_at,event_id) AS (VALUES `)
	args := make([]any, 0, len(events)*11)
	casts := []string{"::bigint", "::bigint", "::bytea", "::bigint", "::text", "::text", "::text", "::integer", "::smallint", "::bigint", "::text"}
	for i, event := range events {
		if i > 0 {
			q.WriteByte(',')
		}
		q.WriteByte('(')
		for field, cast := range casts {
			if field > 0 {
				q.WriteByte(',')
			}
			q.WriteString("$" + strconv.Itoa(len(args)+field+1) + cast)
		}
		q.WriteByte(')')
		network := 0
		if event.Network == "udp" {
			network = 1
		}
		args = append(args, nodeID, event.UserID, compactObservationID(event.EventID), event.OccurredAt, event.ClientIP, event.TargetHost, event.TargetIP, event.TargetPort, network, receivedAt, event.EventID)
	}
	q.WriteString(`), unique_events AS (
 SELECT DISTINCT ON (node_id,event_key) * FROM incoming ORDER BY node_id,event_key,occurred_at
)
INSERT INTO v2_node_observation_compact(node_id,user_id,event_key,occurred_at,client_ip,target_host,target_ip,target_port,network,received_at)
SELECT i.node_id,i.user_id,i.event_key,i.occurred_at,NULLIF(i.client_ip,'')::inet,i.target_host,NULLIF(i.target_ip,'')::inet,i.target_port,i.network,i.received_at
FROM unique_events i
WHERE NOT EXISTS (SELECT 1 FROM v2_node_observation_compact c WHERE c.node_id=i.node_id AND c.event_key=i.event_key)
 AND NOT EXISTS (SELECT 1 FROM v2_node_observation_event old WHERE old.node_type='v2node' AND old.node_id=i.node_id AND old.event_id=i.event_id)
ON CONFLICT DO NOTHING`)
	result, err := tx.ExecContext(ctx, q.String(), args...)
	if err != nil {
		return 0, err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return inserted, nil
}
