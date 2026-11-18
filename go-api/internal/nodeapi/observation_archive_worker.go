package nodeapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

const observationHotHours = 12

var errObservationArchiveSourceChanged = errors.New("observation archive source changed; original rows retained")

// Physical tuple identities include the creating transaction. A concurrent
// UPDATE, DELETE, table rewrite or tuple reuse makes the batch fail closed,
// instead of allowing an old snapshot to delete a different/newer row.
type observationArchiveTuple struct {
	TableOID int64  `json:"table_oid"`
	CTID     string `json:"ctid"`
	Xmin     string `json:"xmin"`
}

type observationArchiveBatch struct {
	source string
	events []archivedObservationEvent
	tuples []observationArchiveTuple
}

type observationArchiveCandidate struct {
	source string
	userID int64
	hourAt int64
	at     int64
}

// ArchiveObservation moves cold events, not aggregates, into verified gzip
// blocks. The atomic archive INSERT and exact source DELETE are in the same
// PostgreSQL transaction, so repeatable-read queries see either representation
// once. Compression and its initial round-trip verification happen outside the
// short final archive lock; reports take the shared version of that lock.
//
// The caller supplies a bounded context and can resume after cancellation.
// Committed blocks remain valid; the uncommitted current batch is rolled back.
func (s *DBService) ArchiveObservation(ctx context.Context, now time.Time) (map[string]int64, error) {
	stats := map[string]int64{"archived_events": 0, "blocks": 0, "compressed_bytes": 0}
	if s == nil || s.db == nil {
		return stats, errors.New("node service unavailable")
	}
	if err := s.ensureObservationSchema(ctx); err != nil {
		return stats, err
	}
	coldBefore := now.UTC().Add(-observationHotHours * time.Hour).Truncate(time.Hour).Unix()
	keepFrom := now.Unix() - observationKeepDays*86400
	for {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		candidate, ok, err := s.nextObservationArchiveCandidate(ctx, keepFrom, coldBefore)
		if err != nil || !ok {
			return stats, err
		}
		batch, err := s.readObservationArchiveBatch(ctx, candidate, keepFrom, coldBefore)
		if err != nil {
			return stats, err
		}
		if len(batch.events) == 0 {
			// Another archiver may have finished this group after selection.
			continue
		}
		block, err := encodeObservationArchive(batch.events)
		if err != nil {
			return stats, fmt.Errorf("encode observation archive: %w", err)
		}
		decoded, err := decodeObservationArchive(block.Payload, block.PayloadSHA256)
		if err != nil || !reflect.DeepEqual(decoded, batch.events) {
			return stats, fmt.Errorf("verify observation archive before write: %v", err)
		}
		if err := s.commitObservationArchiveBatch(ctx, batch, block); err != nil {
			return stats, err
		}
		stats["archived_events"] += int64(len(batch.events))
		stats["blocks"]++
		stats["compressed_bytes"] += int64(len(block.Payload))
	}
}

func (s *DBService) nextObservationArchiveCandidate(ctx context.Context, keepFrom, coldBefore int64) (observationArchiveCandidate, bool, error) {
	var best observationArchiveCandidate
	found := false
	for _, source := range []string{"legacy", "compact"} {
		table := "v2_node_observation_event"
		if source == "compact" {
			table = "v2_node_observation_compact"
		}
		var candidate observationArchiveCandidate
		candidate.source = source
		err := s.db.QueryRowContext(ctx, `SELECT user_id,occurred_at FROM `+table+` WHERE occurred_at >= $1 AND occurred_at < $2 ORDER BY occurred_at ASC LIMIT 1`, keepFrom, coldBefore).Scan(&candidate.userID, &candidate.at)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return best, false, fmt.Errorf("select observation archive candidate: %w", err)
		}
		candidate.hourAt = candidate.at / 3600 * 3600
		if !found || candidate.at < best.at {
			best, found = candidate, true
		}
	}
	return best, found, nil
}

func (s *DBService) readObservationArchiveBatch(ctx context.Context, candidate observationArchiveCandidate, keepFrom, coldBefore int64) (observationArchiveBatch, error) {
	batch := observationArchiveBatch{source: candidate.source}
	var query string
	switch candidate.source {
	case "legacy":
		query = `SELECT tableoid::bigint,ctid::text,xmin::text,node_type,node_id,event_id,user_id,occurred_at,received_at,client_ip,target_host,target_ip,target_port,network,result,uplink_bytes,downlink_bytes
FROM v2_node_observation_event`
	case "compact":
		query = `SELECT tableoid::bigint,ctid::text,xmin::text,'v2node',node_id,
CASE WHEN get_byte(event_key,0)=0 THEN encode(substring(event_key FROM 2),'hex') ELSE convert_from(substring(event_key FROM 2),'UTF8') END,
user_id,occurred_at,received_at,COALESCE(host(client_ip),''),target_host,COALESCE(host(target_ip),''),target_port,
CASE network WHEN 0 THEN 'tcp' ELSE 'udp' END,'route_attempt',0::bigint,0::bigint
FROM v2_node_observation_compact`
	default:
		return batch, errors.New("unknown observation archive source")
	}
	query += ` WHERE user_id=$1 AND occurred_at >= $2 AND occurred_at < $3 ORDER BY occurred_at DESC,node_id DESC,event_id DESC LIMIT $4`
	// Compact's generated event ID is not a physical column. It is selected as
	// column 6, so use ordinal ordering there without changing the tie-breaker.
	if candidate.source == "compact" {
		query = query[:len(query)-len(`ORDER BY occurred_at DESC,node_id DESC,event_id DESC LIMIT $4`)] + `ORDER BY occurred_at DESC,node_id DESC,6 DESC LIMIT $4`
	}
	rows, err := s.db.QueryContext(ctx, query, candidate.userID, max(candidate.hourAt, keepFrom), min(candidate.hourAt+3600, coldBefore), maxObservationArchiveEvents)
	if err != nil {
		return batch, fmt.Errorf("read observation archive batch: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var identity observationArchiveTuple
		var event archivedObservationEvent
		if err := rows.Scan(&identity.TableOID, &identity.CTID, &identity.Xmin, &event.NodeType, &event.NodeID, &event.EventID, &event.UserID, &event.OccurredAt, &event.ReceivedAt, &event.ClientIP, &event.TargetHost, &event.TargetIP, &event.TargetPort, &event.Network, &event.Result, &event.UplinkBytes, &event.DownlinkBytes); err != nil {
			return batch, err
		}
		batch.events = append(batch.events, event)
		batch.tuples = append(batch.tuples, identity)
	}
	return batch, rows.Err()
}

func (s *DBService) commitObservationArchiveBatch(ctx context.Context, batch observationArchiveBatch, block observationArchiveBlock) error {
	if len(batch.events) == 0 || len(batch.events) != len(batch.tuples) || block.EventCount != int64(len(batch.events)) {
		return errors.New("invalid observation archive batch")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SET LOCAL lock_timeout='2s'`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(582791,1)`); err != nil {
		return err
	}
	nodes, _ := json.Marshal(block.NodeIDs)
	hosts, _ := json.Marshal(block.TargetHosts)
	ips, _ := json.Marshal(block.TargetIPs)
	var persistedPayload, persistedSHA, persistedBloom, persistedNodes, persistedHosts, persistedIPs []byte
	var archiveID int64
	err = tx.QueryRowContext(ctx, `INSERT INTO v2_node_observation_archive
(hour_at,user_id,first_at,last_at,event_count,known_events,payload,payload_sha256,event_bloom,node_ids,target_hosts,target_ips)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,
ARRAY(SELECT value::bigint FROM jsonb_array_elements_text($10::jsonb)),
ARRAY(SELECT value FROM jsonb_array_elements_text($11::jsonb)),
ARRAY(SELECT value FROM jsonb_array_elements_text($12::jsonb)))
RETURNING id,payload,payload_sha256,event_bloom,to_json(node_ids),to_json(target_hosts),to_json(target_ips)`, block.HourAt, block.UserID, block.FirstAt, block.LastAt, block.EventCount, block.KnownEvents, block.Payload, block.PayloadSHA256, block.EventBloom, string(nodes), string(hosts), string(ips)).Scan(&archiveID, &persistedPayload, &persistedSHA, &persistedBloom, &persistedNodes, &persistedHosts, &persistedIPs)
	if err != nil {
		return fmt.Errorf("save observation archive: %w", err)
	}
	// Check the bytes read back through PostgreSQL, not only the encoder's
	// memory buffer, before deleting any source representation.
	persistedEvents, err := decodeObservationArchive(persistedPayload, persistedSHA)
	if err != nil || !bytes.Equal(persistedPayload, block.Payload) || !bytes.Equal(persistedBloom, block.EventBloom) || !bytes.Equal(persistedNodes, nodes) || !bytes.Equal(persistedHosts, hosts) || !bytes.Equal(persistedIPs, ips) || !reflect.DeepEqual(persistedEvents, batch.events) {
		return fmt.Errorf("verify persisted observation archive: %v", err)
	}
	grouped := map[int64][]observationArchiveTuple{}
	for _, identity := range batch.tuples {
		grouped[identity.TableOID] = append(grouped[identity.TableOID], identity)
	}
	// A user/hour belongs to one compact partition, but explicitly support
	// multiple physical tables and lock/delete in deterministic OID order.
	oids := make([]int64, 0, len(grouped))
	for oid := range grouped {
		oids = append(oids, oid)
	}
	sort.Slice(oids, func(i, j int) bool { return oids[i] < oids[j] })
	for _, oid := range oids {
		var schema, name string
		var allowed bool
		err := tx.QueryRowContext(ctx, `SELECT namespace.nspname,c.relname,
(c.oid='v2_node_observation_event'::regclass OR EXISTS(SELECT 1 FROM pg_inherits i WHERE i.inhrelid=c.oid AND i.inhparent='v2_node_observation_compact'::regclass))
FROM pg_class c JOIN pg_namespace namespace ON namespace.oid=c.relnamespace WHERE c.oid=$1::oid`, oid).Scan(&schema, &name, &allowed)
		if err != nil || !allowed {
			return fmt.Errorf("validate observation archive source relation: %w", errObservationArchiveSourceChanged)
		}
		if batch.source == "legacy" && name != "v2_node_observation_event" || batch.source == "compact" && name == "v2_node_observation_event" {
			return errObservationArchiveSourceChanged
		}
		payload, err := json.Marshal(grouped[oid])
		if err != nil {
			return err
		}
		// The materialized identities make a bounded TID scan possible instead
		// of a full source-table scan for every compressed block. xmin also
		// prevents deleting a tuple changed or reused after the source read.
		query := `WITH expected AS MATERIALIZED (
SELECT (value->>'ctid')::tid AS tuple_id,(value->>'xmin')::xid AS version FROM jsonb_array_elements($1::jsonb) value
)
DELETE FROM ` + pgx.Identifier{schema, name}.Sanitize() + ` source USING expected e
WHERE source.ctid=ANY(ARRAY(SELECT tuple_id FROM expected)) AND source.ctid=e.tuple_id AND source.xmin=e.version`
		result, err := tx.ExecContext(ctx, query, string(payload))
		if err != nil {
			return fmt.Errorf("remove verified archived observation tuples: %w", err)
		}
		deleted, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if deleted != int64(len(grouped[oid])) {
			return errObservationArchiveSourceChanged
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}
