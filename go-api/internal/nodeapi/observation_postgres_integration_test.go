package nodeapi

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	platformpostgres "forest/go-api/internal/platform/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// Each run owns a separate schema, including all event partitions. The supplied
// database must be disposable, but no pre-existing tables or rows are changed.
func observationPostgresFixture(t *testing.T) (*sql.DB, *DBService, string) {
	t.Helper()
	dsn := os.Getenv("FOREST_OBSERVATION_TEST_DSN")
	if dsn == "" {
		t.Skip("set FOREST_OBSERVATION_TEST_DSN to a disposable PostgreSQL database")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	adminDB := stdlib.OpenDB(*config)
	schema := fmt.Sprintf("observation_test_%d", time.Now().UnixNano())
	if _, err := adminDB.Exec(`CREATE SCHEMA ` + schema); err != nil {
		adminDB.Close()
		t.Fatal(err)
	}
	config.RuntimeParams["search_path"] = schema
	db := stdlib.OpenDB(*config)
	db.SetMaxOpenConns(16)
	t.Cleanup(func() {
		db.Close()
		if _, err := adminDB.Exec(`DROP SCHEMA ` + schema + ` CASCADE`); err != nil {
			t.Errorf("clean up observation test schema: %v", err)
		}
		adminDB.Close()
	})
	ctx := context.Background()
	if err := platformpostgres.EnsureObservationSchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TABLE v2_user(id bigint PRIMARY KEY,email text NOT NULL)`,
		`INSERT INTO v2_user VALUES(1,'legacy@example.test'),(2,'current@example.test')`,
		`CREATE TABLE v2_server_v2node(id bigint PRIMARY KEY,client_entry_only integer NOT NULL DEFAULT 0)`,
		`INSERT INTO v2_server_v2node(id) VALUES(7),(8)`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	token := strings.Repeat("1", 64)
	hash := sha256.Sum256([]byte(token))
	if _, err := db.ExecContext(ctx, `INSERT INTO v2_node_observation_config
(node_type,node_id,enabled,mode,token_hash,allowed_user_ids,created_at,updated_at)
VALUES('v2node',7,true,'all',$1,'[]',1,1),('v2node',8,true,'all',$1,'[]',1,1)`, hash[:]); err != nil {
		t.Fatal(err)
	}
	return db, observationTestService(db), token
}

func TestObservationCompactPostgresRoundTripAndRetry(t *testing.T) {
	db, service, token := observationPostgresFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	legacyAt := now.Add(-time.Hour).Unix()
	if _, err := db.ExecContext(ctx, `INSERT INTO v2_node_observation_event
(node_type,node_id,event_id,user_id,occurred_at,client_ip,target_host,target_ip,target_port,network,result,received_at)
VALUES('v2node',7,'legacy_event_123',1,$1,'192.0.2.10','legacy.example.test','198.51.100.10',443,'tcp','route_attempt',$1)`, legacyAt); err != nil {
		t.Fatal(err)
	}
	makeEvent := func(id string, userID, occurredAt int64) ObservationEvent {
		return ObservationEvent{EventID: id, UserID: userID, OccurredAt: occurredAt,
			ClientIP: "2001:db8::1", TargetHost: "example.test", TargetIP: "2001:db8::2",
			TargetPort: 443, Network: "tcp", Result: "route_attempt"}
	}
	report := func(nodeID int64, events ...ObservationEvent) (int64, error) {
		return service.ReportObservation(ctx, ObservationReportRequest{NodeType: "v2node", NodeID: nodeID, Token: token, Events: events})
	}
	for _, occurredAt := range []int64{legacyAt, legacyAt + 86400/2} {
		// Keep the changed timestamp inside the accepted future boundary.
		if occurredAt > now.Unix() {
			occurredAt = legacyAt - 86400
		}
		if count, err := report(7, makeEvent("legacy_event_123", 1, occurredAt)); err != nil || count != 0 {
			t.Fatalf("legacy retry should not be copied into compact storage: count=%d err=%v", count, err)
		}
	}

	midnight := now.Truncate(24 * time.Hour).Unix()
	ids := []string{"0123456789abcdef0123456789abcdef", "0123456789ABCDEF0123456789ABCDEF", "event_123", strings.Repeat("a", 64)}
	events := make([]ObservationEvent, 0, len(ids))
	for i, id := range ids {
		event := makeEvent(id, 2, midnight-1+int64(i))
		if i == 2 {
			event.ClientIP, event.TargetIP, event.Network = "", "", "udp"
		}
		events = append(events, event)
	}
	duplicate := events[0]
	duplicate.OccurredAt += 2
	initialBatch := append(append([]ObservationEvent(nil), events...), duplicate)
	if count, err := report(7, initialBatch...); err != nil || count != int64(len(events)) {
		t.Fatalf("initial report count=%d err=%v", count, err)
	}
	if count, err := report(7, events...); err != nil || count != 0 {
		t.Fatalf("same-batch retry count=%d err=%v", count, err)
	}
	changedTime := events[0]
	changedTime.OccurredAt -= 86400
	if count, err := report(7, changedTime); err != nil || count != 0 {
		t.Fatalf("same event ID on another day must not duplicate: count=%d err=%v", count, err)
	}
	if count, err := report(8, events[0]); err != nil || count != 1 {
		t.Fatalf("event IDs are scoped by node: count=%d err=%v", count, err)
	}

	filter := ObservationFilter{From: midnight - 86400, To: now.Unix() + 301, PageSize: 100}
	result, err := service.ObservationEvents(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	if result["total"] != int64(6) {
		t.Fatalf("legacy/compact combined total=%v want=6", result["total"])
	}
	items := result["items"].([]map[string]any)
	seen := map[string]int{}
	for _, item := range items {
		id := item["event_id"].(string)
		seen[id]++
		if item["node_type"] != "v2node" || item["result"] != "route_attempt" || item["uplink_bytes"] != int64(0) || item["downlink_bytes"] != int64(0) {
			t.Fatalf("constant API fields changed: %#v", item)
		}
		if id == "event_123" && (item["network"] != "udp" || item["client_ip"] != "" || item["target_ip"] != "") {
			t.Fatalf("empty addresses/UDP did not round-trip: %#v", item)
		}
		if strings.Contains(id, "abcdef") && (item["client_ip"] != "2001:db8::1" || item["target_ip"] != "2001:db8::2") {
			t.Fatalf("IPv6 did not round-trip: %#v", item)
		}
	}
	for _, id := range ids {
		want := 1
		if id == ids[0] {
			want = 2
		}
		if seen[id] != want {
			t.Errorf("event ID %q returned %d times, want %d", id, seen[id], want)
		}
	}
	if seen["legacy_event_123"] != 1 {
		t.Fatal("legacy event missing or duplicated")
	}
	// Pagination must use a deterministic composite tie-breaker: the same event
	// ID and timestamp on distinct nodes is valid, and neither row may disappear.
	pages := make(map[string]bool)
	for page := 1; page <= 6; page++ {
		pageFilter := filter
		pageFilter.Page, pageFilter.PageSize = page, 1
		pageResult, err := service.ObservationEvents(ctx, pageFilter)
		if err != nil {
			t.Fatal(err)
		}
		pageItems := pageResult["items"].([]map[string]any)
		if len(pageItems) != 1 {
			t.Fatalf("page %d contains %d events", page, len(pageItems))
		}
		key := fmt.Sprintf("%v:%v", pageItems[0]["node_id"], pageItems[0]["event_id"])
		if pages[key] {
			t.Errorf("event %s returned on more than one page", key)
		}
		pages[key] = true
	}
	filter.Target = "2001:db8::2"
	targets, err := service.ObservationEvents(ctx, filter)
	if err != nil || targets["total"] != int64(4) {
		t.Fatalf("exact IPv6 target filter: %#v err=%v", targets, err)
	}
	filter.Target, filter.Account = "", "CURRENT@example.test"
	accounts, err := service.ObservationEvents(ctx, filter)
	if err != nil || accounts["total"] != int64(5) {
		t.Fatalf("email filter across compact rows: %#v err=%v", accounts, err)
	}
	filter.Account = ""
	summary, err := service.ObservationSummary(ctx, filter)
	if err != nil || summary["events"] != int64(6) || summary["users"] != int64(2) {
		t.Fatalf("summary across legacy and compact rows: %#v err=%v", summary, err)
	}
}

func TestObservationCompactPostgresConcurrentRetries(t *testing.T) {
	_, service, token := observationPostgresFixture(t)
	ctx := context.Background()
	now := time.Now().Unix()
	const reporters = 8
	var wait sync.WaitGroup
	counts := make(chan int64, reporters)
	errors := make(chan error, reporters)
	for i := 0; i < reporters; i++ {
		wait.Add(1)
		go func(i int) {
			defer wait.Done()
			count, err := service.ReportObservation(ctx, ObservationReportRequest{NodeType: "v2node", NodeID: 7, Token: token,
				Events: []ObservationEvent{{EventID: "concurrent_event_123", UserID: 2, OccurredAt: now - int64(i)*86400/2,
					TargetHost: "concurrent.example.test", TargetPort: 443, Network: "tcp", Result: "route_attempt"}}})
			counts <- count
			errors <- err
		}(i)
	}
	wait.Wait()
	close(counts)
	close(errors)
	var total int64
	for count := range counts {
		total += count
	}
	for err := range errors {
		if err != nil {
			t.Errorf("concurrent report: %v", err)
		}
	}
	if total != 1 {
		t.Fatalf("concurrent retries inserted %d events, want 1", total)
	}
}

func TestObservationCursorPostgresMixedStoresStablePagination(t *testing.T) {
	db, service, _ := observationPostgresFixture(t)
	ctx := context.Background()
	now := time.Now().Unix()
	for _, row := range []struct {
		node int
		id   string
		at   int64
	}{{7, "legacy_003", now - 2}, {7, "legacy_002", now - 2}, {8, "legacy_001", now - 2}} {
		_, err := db.Exec(`INSERT INTO v2_node_observation_event(node_type,node_id,event_id,user_id,occurred_at,target_host,target_port,network,result,received_at) VALUES('v2node',$1,$2,1,$3,'cursor.example.test',443,'tcp','route_attempt',$3)`, row.node, row.id, row.at)
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"0123456789abcdef0123456789abcdef", "AAAABBBBCCCCDDDDEEEEFFFF00001111", "compact_001"} {
		e := ObservationEvent{EventID: id, UserID: 2, OccurredAt: now - 2, TargetHost: "cursor.example.test", TargetPort: 443, Network: "tcp", Result: "route_attempt"}
		if _, err := service.insertCompactObservation(ctx, 7, []ObservationEvent{e}, now); err != nil {
			t.Fatal(err)
		}
	}
	filter := ObservationFilter{From: now - 60, To: now, PageSize: 2}
	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		page, err := service.ObservationEvents(ctx, filter)
		if err != nil {
			t.Fatal(err)
		}
		items := page["items"].([]map[string]any)
		if len(items) != 2 {
			t.Fatalf("page %d rows=%d", i, len(items))
		}
		for _, item := range items {
			key := fmt.Sprintf("%v:%v", item["node_id"], item["event_id"])
			if seen[key] {
				t.Fatalf("duplicate event %s", key)
			}
			seen[key] = true
		}
		if page["has_more"] != (i < 2) {
			t.Fatalf("wrong has_more: %#v", page)
		}
		filter.Cursor = page["next_cursor"].(string)
		if i == 0 {
			// A newly arrived later event must not shift already traversed rows.
			_, err = db.Exec(`INSERT INTO v2_node_observation_event(node_type,node_id,event_id,user_id,occurred_at,target_host,target_port,network,result,received_at) VALUES('v2node',7,'late_event_001',1,$1,'late.example.test',443,'tcp','route_attempt',$1)`, now-1)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(seen) != 6 {
		t.Fatalf("mixed-store cursor returned %d distinct events", len(seen))
	}
	filter.Cursor = ""
	filter.Target = "cursor.example.test"
	page, err := service.ObservationEvents(ctx, filter)
	if err != nil || len(page["items"].([]map[string]any)) != 2 {
		t.Fatalf("target cursor filter: %#v %v", page, err)
	}
	filter.Cursor = page["next_cursor"].(string)
	filter.Target = "different.example.test"
	if _, err := service.ObservationEvents(ctx, filter); err == nil {
		t.Fatal("accepted cursor from different target")
	}
}
