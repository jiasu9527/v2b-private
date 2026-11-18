package nodeapi

import (
	"context"
	"database/sql"
	"reflect"
	"testing"
	"time"
)

func insertHotObservationQueryEvent(t *testing.T, db *sql.DB, e archivedObservationEvent) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO v2_node_observation_event(node_type,node_id,event_id,user_id,occurred_at,client_ip,target_host,target_ip,target_port,network,result,uplink_bytes,downlink_bytes,received_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`, e.NodeType, e.NodeID, e.EventID, e.UserID, e.OccurredAt, e.ClientIP, e.TargetHost, e.TargetIP, e.TargetPort, e.Network, e.Result, e.UplinkBytes, e.DownlinkBytes, e.ReceivedAt); err != nil {
		t.Fatal(err)
	}
}

func TestObservationQueryPostgresMixedArchivePaginationAndSummary(t *testing.T) {
	db, service, _ := observationPostgresFixture(t)
	ctx := context.Background()
	at := time.Now().Add(-24*time.Hour).Truncate(time.Hour).Unix() + 100
	// Same-second mixed stores exercise bytewise ID and node tie-breakers.
	cold := []archivedObservationEvent{
		archiveQueryTestEvent(1, at, 8, "event_zz1"),
		archiveQueryTestEvent(1, at, 8, "event_ZZ1"),
		archiveQueryTestEvent(2, at-1, 7, "event_001"),
	}
	insertArchiveQueryTestBlock(t, db, cold[0], cold[1])
	insertArchiveQueryTestBlock(t, db, cold[2])
	hot := []archivedObservationEvent{
		archiveQueryTestEvent(2, at, 8, "event_az1"),
		archiveQueryTestEvent(1, at, 7, "event_zz2"),
		archiveQueryTestEvent(1, at-2, 7, "event_000"),
	}
	for _, e := range hot {
		insertHotObservationQueryEvent(t, db, e)
	}
	filter := ObservationFilter{From: at - 3, To: at + 1, PageSize: 2}
	var ids []string
	for n := 0; ; n++ {
		if n >= 4 {
			t.Fatal("cursor did not terminate")
		}
		page, err := service.ObservationEvents(ctx, filter)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range page["items"].([]map[string]any) {
			ids = append(ids, item["event_id"].(string))
			if item["email"] == "" || item["uplink_bytes"] != int64(12) || item["downlink_bytes"] != int64(34) {
				t.Fatalf("lost event fields across mixed stores: %#v", item)
			}
		}
		if !page["has_more"].(bool) {
			break
		}
		filter.Cursor = page["next_cursor"].(string)
	}
	want := []string{"event_zz1", "event_az1", "event_ZZ1", "event_zz2", "event_001", "event_000"}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("mixed cursor duplicates/omissions/order: got %v want %v", ids, want)
	}
	// OFFSET compatibility applies after merging both stores, not to each half.
	filter.Cursor, filter.Page = "", 2
	page, err := service.ObservationEvents(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	items := page["items"].([]map[string]any)
	if len(items) != 2 || items[0]["event_id"] != want[2] || items[1]["event_id"] != want[3] {
		t.Fatalf("mixed OFFSET pagination: %#v", page)
	}
	filter.Page = 1
	summary, err := service.ObservationSummary(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	if summary["events"] != int64(6) || summary["users"] != int64(2) || summary["known_domain_events"] != int64(6) {
		t.Fatalf("hot and cold users must be a union, not a sum: %#v", summary)
	}
	domains := summary["top_domains"].([]map[string]any)
	if len(domains) != 1 || domains[0]["users"] != int64(2) || domains[0]["events"] != int64(6) {
		t.Fatalf("mixed per-domain summary: %#v", domains)
	}
	filter.Account, filter.Target = "CURRENT@example.test", "2001:db8::2"
	page, err = service.ObservationEvents(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	items = page["items"].([]map[string]any)
	if len(items) != 2 || items[0]["event_id"] != "event_az1" || items[1]["event_id"] != "event_001" {
		t.Fatalf("exact filters differ across stores: %#v", page)
	}
}

func TestObservationQueryPostgresRelationLockPreventsArchiveTruncateGap(t *testing.T) {
	db, _, _ := observationPostgresFixture(t)
	ctx := context.Background()
	at := time.Now().Add(-24*time.Hour).Truncate(time.Hour).Unix() + 100
	event := archiveQueryTestEvent(1, at, 7, "snapshot_locked_event")
	insertHotObservationQueryEvent(t, db, event)
	reader, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Rollback()
	// This must be the first command, before creating a repeatable-read
	// snapshot: TRUNCATE is not MVCC-safe for a not-yet-accessed relation.
	if _, err := reader.Exec(`LOCK TABLE v2_node_observation_event,v2_node_observation_compact IN ACCESS SHARE MODE`); err != nil {
		t.Fatal(err)
	}
	var coldCount int
	if err := reader.QueryRow(`SELECT COUNT(*) FROM v2_node_observation_archive`).Scan(&coldCount); err != nil || coldCount != 0 {
		t.Fatalf("initial snapshot: cold=%d err=%v", coldCount, err)
	}
	writer, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	insertArchiveQueryTestBlock(t, writer, event)
	if _, err := writer.Exec(`DELETE FROM v2_node_observation_event`); err != nil {
		writer.Rollback()
		t.Fatal(err)
	}
	if err := writer.Commit(); err != nil {
		t.Fatal(err)
	}
	truncater, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := truncater.Exec(`SET LOCAL lock_timeout='50ms'`); err != nil {
		t.Fatal(err)
	}
	if _, err := truncater.Exec(`TRUNCATE TABLE v2_node_observation_event`); err == nil {
		truncater.Rollback()
		t.Fatal("TRUNCATE was allowed to invalidate an older hot/cold snapshot")
	}
	truncater.Rollback()
	var hotCount int
	if err := reader.QueryRow(`SELECT COUNT(*) FROM v2_node_observation_history`).Scan(&hotCount); err != nil || hotCount != 1 {
		t.Fatalf("pre-archive snapshot must retain hot event: count=%d err=%v", hotCount, err)
	}
	if err := reader.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`TRUNCATE TABLE v2_node_observation_event`); err != nil {
		t.Fatalf("empty source must become reclaimable after reader completes: %v", err)
	}
}
