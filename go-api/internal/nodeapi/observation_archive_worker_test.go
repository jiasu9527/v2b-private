package nodeapi

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func archiveWorkerTestEvents(t *testing.T, service *DBService, archived bool) []archivedObservationEvent {
	t.Helper()
	rows, err := service.db.Query(`SELECT node_type,node_id,event_id,user_id,occurred_at,received_at,client_ip,target_host,target_ip,target_port,network,result,uplink_bytes,downlink_bytes FROM v2_node_observation_history`)
	if err != nil {
		t.Fatal(err)
	}
	var events []archivedObservationEvent
	for rows.Next() {
		var e archivedObservationEvent
		if err = rows.Scan(&e.NodeType, &e.NodeID, &e.EventID, &e.UserID, &e.OccurredAt, &e.ReceivedAt, &e.ClientIP, &e.TargetHost, &e.TargetIP, &e.TargetPort, &e.Network, &e.Result, &e.UplinkBytes, &e.DownlinkBytes); err != nil {
			t.Fatal(err)
		}
		events = append(events, e)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if archived {
		rows, err = service.db.Query(`SELECT payload,payload_sha256 FROM v2_node_observation_archive`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var payload, checksum []byte
			if err = rows.Scan(&payload, &checksum); err != nil {
				t.Fatal(err)
			}
			block, err := decodeObservationArchive(payload, checksum)
			if err != nil {
				t.Fatal(err)
			}
			events = append(events, block...)
		}
		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
	}
	sort.Slice(events, func(i, j int) bool {
		return fmt.Sprintf("%s:%020d:%s", events[i].NodeType, events[i].NodeID, events[i].EventID) < fmt.Sprintf("%s:%020d:%s", events[j].NodeType, events[j].NodeID, events[j].EventID)
	})
	return events
}

func TestObservationArchivePostgresRoundTripAndRetry(t *testing.T) {
	db, service, _ := observationPostgresFixture(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Hour)
	coldAt := now.Add(-24*time.Hour).Unix() + 100
	hotAt := now.Add(-time.Hour).Unix() + 100
	_, err := db.Exec(`INSERT INTO v2_node_observation_event(node_type,node_id,event_id,user_id,occurred_at,received_at,client_ip,target_host,target_ip,target_port,network,result,uplink_bytes,downlink_bytes)
VALUES('v2node',7,'legacy_event_123',1,$1::bigint,$1::bigint+3,'192.0.2.1','legacy.example.test','198.51.100.1',443,'tcp','route_attempt',12,34),
('trojan',7,'same_event_123',2,$1::bigint+1,$1::bigint+4,'2001:db8::1','other.example.test','2001:db8::2',53,'udp','route_attempt',56,78),
('v2node',7,'expired_event_123',1,$2::bigint,$2::bigint+1,'','','192.0.2.2',443,'tcp','route_attempt',0,0)`, coldAt, now.Add(-8*24*time.Hour).Unix())
	if err != nil {
		t.Fatal(err)
	}
	cold := ObservationEvent{EventID: strings.Repeat("ab", 16), UserID: 1, OccurredAt: coldAt + 2, ClientIP: "2001:db8::3", TargetHost: "compact.example.test", TargetIP: "2001:db8::4", TargetPort: 443, Network: "tcp", Result: "route_attempt"}
	second := cold
	second.EventID, second.UserID, second.TargetIP, second.ClientIP, second.Network = "Mixed_Archive123", 2, "", "", "udp"
	hot := cold
	hot.EventID, hot.OccurredAt = "hot_event_123", hotAt
	if n, err := service.insertCompactObservation(ctx, 7, []ObservationEvent{cold, second, hot}, now.Unix()); err != nil || n != 3 {
		t.Fatalf("initial compact insert: %d %v", n, err)
	}
	before := archiveWorkerTestEvents(t, service, false)
	stats, err := service.ArchiveObservation(ctx, now)
	if err != nil || stats["archived_events"] != 4 || stats["blocks"] != 4 || stats["compressed_bytes"] <= 0 {
		t.Fatalf("archive stats=%v err=%v", stats, err)
	}
	after := archiveWorkerTestEvents(t, service, true)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("archive changed original event fields:\nbefore=%+v\nafter=%+v", before, after)
	}
	var remaining int
	if err := db.QueryRow(`SELECT COUNT(*) FROM v2_node_observation_history`).Scan(&remaining); err != nil || remaining != 2 {
		t.Fatalf("hot and out-of-retention rows must remain untouched: %d %v", remaining, err)
	}
	// A retry may change its timestamp, user or target. Event identity must not.
	for _, retried := range []ObservationEvent{cold, second, {EventID: "legacy_event_123", UserID: 1}} {
		retried.OccurredAt, retried.TargetHost, retried.TargetPort, retried.Network = hotAt, "changed.example.test", 443, "tcp"
		fresh := observationTestService(db)
		if n, err := fresh.insertCompactObservation(ctx, 7, []ObservationEvent{retried}, now.Unix()); err != nil || n != 0 {
			t.Fatalf("archived changed-time retry inserted %d events: %v", n, err)
		}
	}
	// Legacy archives contain other node types; equal node/event IDs are not
	// duplicates across those namespaces, or across different v2node nodes.
	newType := cold
	newType.EventID, newType.OccurredAt = "same_event_123", hotAt
	if n, err := service.insertCompactObservation(ctx, 7, []ObservationEvent{newType}, now.Unix()); err != nil || n != 1 {
		t.Fatalf("other node type incorrectly suppressed: %d %v", n, err)
	}
	cold.OccurredAt = hotAt
	if n, err := service.insertCompactObservation(ctx, 8, []ObservationEvent{cold}, now.Unix()); err != nil || n != 1 {
		t.Fatalf("other node incorrectly suppressed: %d %v", n, err)
	}
	stats, err = service.ArchiveObservation(ctx, now)
	if err != nil || stats["archived_events"] != 0 || stats["blocks"] != 0 {
		t.Fatalf("second archive must be a no-op: %v %v", stats, err)
	}
}

func TestObservationArchivePostgresConcurrentSourceUpdateRollsBack(t *testing.T) {
	for _, source := range []string{"legacy", "compact"} {
		t.Run(source, func(t *testing.T) {
			db, service, _ := observationPostgresFixture(t)
			ctx := context.Background()
			now := time.Now().UTC().Truncate(time.Hour)
			at := now.Add(-24*time.Hour).Unix() + 100
			table := "v2_node_observation_event"
			if source == "compact" {
				table = "v2_node_observation_compact"
				if _, err := service.insertCompactObservation(ctx, 7, []ObservationEvent{{EventID: "changed_event_123", UserID: 1, OccurredAt: at, TargetHost: "before.test", TargetPort: 443, Network: "tcp"}}, at); err != nil {
					t.Fatal(err)
				}
			} else if _, err := db.Exec(`INSERT INTO v2_node_observation_event(node_type,node_id,event_id,user_id,occurred_at,received_at,target_host,target_port,network,result) VALUES('v2node',7,'changed_event_123',1,$1,$1,'before.test',443,'tcp','route_attempt')`, at); err != nil {
				t.Fatal(err)
			}
			candidate, found, err := service.nextObservationArchiveCandidate(ctx, now.Add(-7*24*time.Hour).Unix(), now.Add(-12*time.Hour).Unix())
			if err != nil || !found {
				t.Fatalf("candidate: %+v %t %v", candidate, found, err)
			}
			batch, err := service.readObservationArchiveBatch(ctx, candidate, now.Add(-7*24*time.Hour).Unix(), now.Add(-12*time.Hour).Unix())
			if err != nil || len(batch.events) != 1 {
				t.Fatalf("batch: %+v %v", batch, err)
			}
			block, err := encodeObservationArchive(batch.events)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE ` + table + ` SET target_host='after.test'`); err != nil {
				t.Fatal(err)
			}
			if err := service.commitObservationArchiveBatch(ctx, batch, block); !errors.Is(err, errObservationArchiveSourceChanged) {
				t.Fatalf("updated source must abort archive: %v", err)
			}
			var archives, originals int
			if err := db.QueryRow(`SELECT COUNT(*) FROM v2_node_observation_archive`).Scan(&archives); err != nil || archives != 0 {
				t.Fatalf("failed archive must not be published: %d %v", archives, err)
			}
			if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table + ` WHERE target_host='after.test'`).Scan(&originals); err != nil || originals != 1 {
				t.Fatalf("updated original lost: %d %v", originals, err)
			}
		})
	}
}

func TestObservationArchivePostgresPersistedChecksumFailureRetainsOriginals(t *testing.T) {
	db, service, _ := observationPostgresFixture(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Hour)
	at := now.Add(-24*time.Hour).Unix() + 100
	if _, err := service.insertCompactObservation(ctx, 7, []ObservationEvent{{EventID: "corrupt_event_123", UserID: 1, OccurredAt: at, TargetHost: "original.test", TargetPort: 443, Network: "tcp"}}, at); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE FUNCTION corrupt_test_archive() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN NEW.payload=substring(NEW.payload FROM 1 FOR octet_length(NEW.payload)-1); RETURN NEW; END $$`,
		`CREATE TRIGGER corrupt_archive BEFORE INSERT ON v2_node_observation_archive FOR EACH ROW EXECUTE FUNCTION corrupt_test_archive()`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	stats, err := service.ArchiveObservation(ctx, now)
	if err == nil || !strings.Contains(err.Error(), "verify persisted observation archive") || stats["archived_events"] != 0 {
		t.Fatalf("corrupt persisted bytes were accepted: %v %v", stats, err)
	}
	var archives, originals int
	if err := db.QueryRow(`SELECT COUNT(*) FROM v2_node_observation_archive`).Scan(&archives); err != nil || archives != 0 {
		t.Fatalf("corrupt archive remained: %d %v", archives, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM v2_node_observation_compact`).Scan(&originals); err != nil || originals != 1 {
		t.Fatalf("original deleted before persisted verification: %d %v", originals, err)
	}
}

func TestObservationArchivePostgresLargeHourAndConcurrentArchivers(t *testing.T) {
	db, service, _ := observationPostgresFixture(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Hour)
	at := now.Add(-24*time.Hour).Unix() + 100
	const count = maxObservationArchiveEvents + 5
	_, err := db.Exec(`INSERT INTO v2_node_observation_event(node_type,node_id,event_id,user_id,occurred_at,received_at,target_host,target_port,network,result)
SELECT 'v2node',7,'bulk_'||lpad(i::text,8,'0'),1,$1,$1,'same.example.test',443,'tcp','route_attempt' FROM generate_series(1,$2::integer) i`, at, count)
	if err != nil {
		t.Fatal(err)
	}
	before := archiveWorkerTestEvents(t, service, false)
	var wait sync.WaitGroup
	for i := 0; i < 2; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := service.ArchiveObservation(ctx, now)
			if err != nil && !errors.Is(err, errObservationArchiveSourceChanged) {
				t.Errorf("concurrent archive: %v", err)
			}
		}()
	}
	wait.Wait()
	if _, err := service.ArchiveObservation(ctx, now); err != nil {
		t.Fatal(err)
	}
	if after := archiveWorkerTestEvents(t, service, true); !reflect.DeepEqual(before, after) {
		t.Fatalf("concurrent batch archives lost or duplicated events: before=%d after=%d", len(before), len(after))
	}
	var originals, blocks, events int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM v2_node_observation_event`).Scan(&originals); err != nil || originals != 0 {
		t.Fatalf("source rows remain: %d %v", originals, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*),SUM(event_count) FROM v2_node_observation_archive`).Scan(&blocks, &events); err != nil || blocks != 2 || events != count {
		t.Fatalf("large user/hour must split into bounded blocks: blocks=%d events=%d err=%v", blocks, events, err)
	}
}
