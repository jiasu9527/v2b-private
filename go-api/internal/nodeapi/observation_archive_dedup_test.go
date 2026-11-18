package nodeapi

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestObservationArchiveRetryHashesMatchCodecBloom(t *testing.T) {
	for _, size := range []int{1, 16, 100, 10000} {
		bloom := make([]byte, max(64, size*4))
		for i := 0; i < size; i++ {
			observationArchiveBloomAdd(bloom, 7, fmt.Sprintf("retry_event_%08d", i))
		}
		for _, node := range []int64{7, 8} {
			for i := 0; i < size+100; i++ {
				id := fmt.Sprintf("retry_event_%08d", i)
				events := []ObservationEvent{{EventID: id}}
				hash := observationArchiveRetryHashes(node, events)[0]
				if got, want := observationArchiveRetryMayContain(bloom, hash), observationArchiveMayContain(bloom, node, id); got != want {
					t.Fatalf("optimized bloom differs for node=%d id=%s: got=%v want=%v", node, id, got, want)
				}
				if node == 7 && i < size && !observationArchiveRetryMayContain(bloom, hash) {
					t.Fatal("false negative")
				}
			}
		}
	}
	hash := observationArchiveRetryHashes(7, []ObservationEvent{{EventID: "malformed_event"}})[0]
	if !observationArchiveRetryMayContain(nil, hash) {
		t.Fatal("malformed metadata must force exact lookup")
	}
}

func TestObservationArchiveRetryPostgresChangedFieldsAndIncrementalCache(t *testing.T) {
	db, service, _ := observationPostgresFixture(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Hour)
	coldAt := now.Add(-24*time.Hour).Unix() + 100
	event := ObservationEvent{EventID: "dedup_event_original", UserID: 1, OccurredAt: coldAt, ClientIP: "192.0.2.1", TargetHost: "original.test", TargetIP: "198.51.100.1", TargetPort: 443, Network: "tcp", Result: "route_attempt"}
	if n, err := service.insertCompactObservation(ctx, 7, []ObservationEvent{event}, now.Unix()); err != nil || n != 1 {
		t.Fatalf("initial n=%d err=%v", n, err)
	}
	if stats, err := service.ArchiveObservation(ctx, now); err != nil || stats["archived_events"] != 1 {
		t.Fatalf("archive stats=%v err=%v", stats, err)
	}
	retry := event
	retry.UserID, retry.OccurredAt, retry.TargetHost, retry.TargetPort, retry.Network = 2, now.Unix(), "different.test", 8443, "udp"
	for _, current := range []*DBService{service, observationTestService(db)} {
		if n, err := current.insertCompactObservation(ctx, 7, []ObservationEvent{retry}, now.Unix()); err != nil || n != 0 {
			t.Fatalf("changed-fields retry n=%d err=%v", n, err)
		}
	}
	previousID := service.observationArchiveLast
	second := event
	second.EventID = "dedup_event_incremental"
	if n, err := service.insertCompactObservation(ctx, 7, []ObservationEvent{second}, now.Unix()); err != nil || n != 1 {
		t.Fatalf("new cold n=%d err=%v", n, err)
	}
	if stats, err := service.ArchiveObservation(ctx, now); err != nil || stats["archived_events"] != 1 {
		t.Fatalf("second archive stats=%v err=%v", stats, err)
	}
	second.UserID, second.OccurredAt = 2, now.Unix()
	if n, err := service.insertCompactObservation(ctx, 7, []ObservationEvent{second}, now.Unix()); err != nil || n != 0 {
		t.Fatalf("newly archived retry n=%d err=%v", n, err)
	}
	if service.observationArchiveLast <= previousID {
		t.Fatal("incremental cache did not advance")
	}
	if n, err := observationTestService(db).insertCompactObservation(ctx, 7, []ObservationEvent{second}, now.Unix()); err != nil || n != 0 {
		t.Fatalf("rebuilt cache retry n=%d err=%v", n, err)
	}
	if n, err := service.insertCompactObservation(ctx, 8, []ObservationEvent{retry}, now.Unix()); err != nil || n != 1 {
		t.Fatalf("node scope n=%d err=%v", n, err)
	}
	var hot int
	if err := db.QueryRow(`SELECT COUNT(*) FROM v2_node_observation_history WHERE node_id=7`).Scan(&hot); err != nil || hot != 0 {
		t.Fatalf("duplicate escaped into hot storage: %d %v", hot, err)
	}
}

func TestObservationArchiveRetryPostgresCorruptionFailsClosed(t *testing.T) {
	db, service, _ := observationPostgresFixture(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Hour)
	event := ObservationEvent{EventID: "dedup_corrupt_event", UserID: 1, OccurredAt: now.Add(-24*time.Hour).Unix() + 100, TargetHost: "original.test", TargetPort: 443, Network: "tcp", Result: "route_attempt"}
	if _, err := service.insertCompactObservation(ctx, 7, []ObservationEvent{event}, now.Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ArchiveObservation(ctx, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE v2_node_observation_archive SET payload='broken'::bytea`); err != nil {
		t.Fatal(err)
	}
	event.UserID, event.OccurredAt = 2, now.Unix()
	if n, err := service.insertCompactObservation(ctx, 7, []ObservationEvent{event}, now.Unix()); err == nil || n != 0 {
		t.Fatalf("corrupt archive must reject unverified retry: n=%d err=%v", n, err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM v2_node_observation_history`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("corrupt verification inserted duplicate: %d %v", count, err)
	}
}

func BenchmarkObservationArchiveRetryBloomScan(b *testing.B) {
	// 10,000 cold blocks x 100 report identities. This excludes PostgreSQL
	// metadata loading and demonstrates the CPU bound of retained-block scans.
	const blocks = 10000
	blooms := make([][]byte, blocks)
	for i := range blooms {
		bloom := make([]byte, 4000)
		for j := 0; j < 1000; j++ {
			observationArchiveBloomAdd(bloom, 7, fmt.Sprintf("archived_%08d_%08d", i, j))
		}
		blooms[i] = bloom
	}
	events := make([]ObservationEvent, 100)
	for i := range events {
		events[i].EventID = fmt.Sprintf("incoming_%08d", i)
	}
	hashes := observationArchiveRetryHashes(7, events)
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		for _, bloom := range blooms {
			for _, hash := range hashes {
				if observationArchiveRetryMayContain(bloom, hash) {
					break
				}
			}
		}
	}
}

func TestObservationArchiveRetryPostgresBloomPositiveRequiresExactIdentity(t *testing.T) {
	db, service, _ := observationPostgresFixture(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Hour)
	event := ObservationEvent{EventID: "dedup_bloom_original", UserID: 1, OccurredAt: now.Add(-24*time.Hour).Unix() + 100, TargetHost: "original.test", TargetPort: 443, Network: "tcp", Result: "route_attempt"}
	if _, err := service.insertCompactObservation(ctx, 7, []ObservationEvent{event}, now.Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ArchiveObservation(ctx, now); err != nil {
		t.Fatal(err)
	}
	// All bits set simulates a false positive; it can only trigger an exact
	// lookup, never incorrectly acknowledge an event that has not been stored.
	if _, err := db.Exec(`UPDATE v2_node_observation_archive SET event_bloom=decode(repeat('ff',64),'hex')`); err != nil {
		t.Fatal(err)
	}
	event.EventID, event.UserID, event.OccurredAt = "dedup_bloom_new", 2, now.Unix()
	if n, err := service.insertCompactObservation(ctx, 7, []ObservationEvent{event}, now.Unix()); err != nil || n != 1 {
		t.Fatalf("Bloom false positive incorrectly suppressed new event: %d %v", n, err)
	}
	if n, err := service.insertCompactObservation(ctx, 7, []ObservationEvent{event}, now.Unix()); err != nil || n != 0 {
		t.Fatalf("hot retry with positive Bloom was not idempotent: %d %v", n, err)
	}
}
