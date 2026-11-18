package nodeapi

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"forest/go-api/internal/platform/postgres"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// The supplied DSN must target a disposable local test database, never production.
func TestCompactObservationPostgresIntegration(t *testing.T) {
	dsn := os.Getenv("OBSERVATION_TEST_DSN")
	if dsn == "" {
		t.Skip("OBSERVATION_TEST_DSN not set")
	}
	ctx := context.Background()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	schema := fmt.Sprintf("observation_test_%d", time.Now().UnixNano())
	if _, err = db.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	defer db.ExecContext(ctx, `DROP SCHEMA `+schema+` CASCADE`)
	if _, err = db.ExecContext(ctx, `SET search_path TO `+schema); err != nil {
		t.Fatal(err)
	}
	if err = postgres.EnsureObservationSchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err = postgres.EnsureObservationSchema(ctx, db); err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	params := parsed.Query()
	params.Set("search_path", schema)
	parsed.RawQuery = params.Encode()
	pool, err := sql.Open("pgx", parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	pool.SetMaxOpenConns(4)
	svc := &DBService{db: pool}
	now := time.Now().Unix()
	events := []ObservationEvent{
		{EventID: strings.Repeat("ab", 16), UserID: 1, OccurredAt: now, ClientIP: "192.0.2.1", TargetHost: "example.test", TargetIP: "2001:db8::1", TargetPort: 443, Network: "tcp", Result: "route_attempt"},
		{EventID: "Legacy_Mixed-Case123", UserID: 1, OccurredAt: now - 86400, TargetHost: "legacy.test", TargetPort: 53, Network: "udp", Result: "route_attempt"},
	}
	n, err := svc.insertCompactObservation(ctx, 7, events, now)
	if err != nil || n != 2 {
		t.Fatalf("insert: %d %v", n, err)
	}
	retry := append([]ObservationEvent(nil), events...)
	retry[0].OccurredAt -= 86400
	n, err = svc.insertCompactObservation(ctx, 7, retry, now)
	if err != nil || n != 0 {
		t.Fatalf("retry changed partition: %d %v", n, err)
	}
	// Same ID within one batch, and a simultaneous retry, must not duplicate it.
	next := events[0]
	next.EventID = strings.Repeat("cd", 16)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, e := svc.insertCompactObservation(ctx, 7, []ObservationEvent{next, next}, now); e != nil {
				t.Errorf("concurrent duplicate: %v", e)
			}
		}()
	}
	wg.Wait()
	var count int
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_node_observation_history`).Scan(&count); err != nil || count != 3 {
		t.Fatalf("count: %d %v", count, err)
	}
	var id, clientIP, targetIP, network, result string
	var up, down int64
	err = db.QueryRowContext(ctx, `SELECT event_id,client_ip,target_ip,network,result,uplink_bytes,downlink_bytes FROM v2_node_observation_history WHERE target_host='example.test' ORDER BY event_id LIMIT 1`).Scan(&id, &clientIP, &targetIP, &network, &result, &up, &down)
	if err != nil || id != events[0].EventID || clientIP != "192.0.2.1" || targetIP != "2001:db8::1" || network != "tcp" || result != "route_attempt" || up != 0 || down != 0 {
		t.Fatalf("round-trip: %q %q %q %q %q %d %d %v", id, clientIP, targetIP, network, result, up, down, err)
	}
	var legacyID string
	if err = db.QueryRowContext(ctx, `SELECT event_id FROM v2_node_observation_history WHERE target_host='legacy.test'`).Scan(&legacyID); err != nil || legacyID != events[1].EventID {
		t.Fatalf("legacy ID: %q %v", legacyID, err)
	}
	// Legacy table remains visible and its IDs participate in retry deduplication.
	_, err = db.ExecContext(ctx, `INSERT INTO v2_node_observation_event(node_type,node_id,event_id,user_id,occurred_at,target_host,target_port,network,result,received_at) VALUES('v2node',7,'old_event_123',1,$1,'old.test',443,'tcp','route_attempt',$1)`, now)
	if err != nil {
		t.Fatal(err)
	}
	old := events[0]
	old.EventID = "old_event_123"
	n, err = svc.insertCompactObservation(ctx, 7, []ObservationEvent{old}, now)
	if err != nil || n != 0 {
		t.Fatalf("old retry: %d %v", n, err)
	}
	// Exact target filtering and PostgreSQL partition pruning still work through the view.
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_node_observation_history WHERE occurred_at>=$1 AND target_ip=$2`, now-60, "2001:db8::1").Scan(&count); err != nil || count != 2 {
		t.Fatalf("target filter: %d %v", count, err)
	}
	cutoff := time.Date(time.Now().UTC().Year(), time.Now().UTC().Month(), time.Now().UTC().Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -9)
	name := "v2_node_observation_day_" + cutoff.Format("20060102")
	_, err = db.ExecContext(ctx, fmt.Sprintf(`CREATE TABLE %s PARTITION OF v2_node_observation_compact FOR VALUES FROM (%d) TO (%d)`, name, cutoff.Unix(), cutoff.AddDate(0, 0, 1).Unix()))
	if err != nil {
		t.Fatal(err)
	}
	if err = postgres.DeleteExpiredObservationPartitions(ctx, db, now-7*86400); err != nil {
		t.Fatalf("maintenance: %v", err)
	}
	var relation sql.NullString
	if err = db.QueryRowContext(ctx, `SELECT to_regclass($1)::text`, name).Scan(&relation); err != nil || relation.Valid {
		t.Fatalf("expired partition retained: %v %v", relation, err)
	}
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_node_observation_event`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("recent old row deleted: %d %v", count, err)
	}
	// An entirely expired legacy heap is reclaimed, not copied and deleted row by row.
	_, err = db.ExecContext(ctx, `UPDATE v2_node_observation_event SET occurred_at=$1`, now-8*86400)
	if err != nil {
		t.Fatal(err)
	}
	if err = postgres.DeleteExpiredObservationPartitions(ctx, db, now-7*86400); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_node_observation_event`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("old heap not emptied: %d %v", count, err)
	}
}
