package nodeapi

import (
	"context"
	"forest/go-api/internal/platform/postgres"
	"testing"
	"time"
)

func TestObservationArchiveReclaimsOnlyEmptyStorage(t *testing.T) {
	db, svc, _ := observationPostgresFixture(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Hour)
	at := now.Add(-48*time.Hour).Unix() + 123
	event := ObservationEvent{EventID: "reclaim_event_123", UserID: 1, OccurredAt: at, TargetHost: "example.test", TargetPort: 443, Network: "tcp"}
	if _, err := svc.insertCompactObservation(ctx, 7, []ObservationEvent{event}, now.Unix()); err != nil {
		t.Fatal(err)
	}
	if bytes, err := postgres.ReclaimEmptyObservationStorage(ctx, db, postgres.ObservationColdBefore(now)); err != nil || bytes != 0 {
		t.Fatalf("nonempty storage reclaimed: %d %v", bytes, err)
	}
	if stats, err := svc.ArchiveObservation(ctx, now); err != nil || stats["archived_events"] != 1 {
		t.Fatalf("archive: %v %v", stats, err)
	}
	if bytes, err := postgres.ReclaimEmptyObservationStorage(ctx, db, postgres.ObservationColdBefore(now)); err != nil || bytes <= 0 {
		t.Fatalf("empty storage not reclaimed: %d %v", bytes, err)
	}
	// A late report on that day remains accepted, so the day was not dropped.
	event.EventID = "late_reclaim_event_123"
	if n, err := svc.insertCompactObservation(ctx, 7, []ObservationEvent{event}, now.Unix()); err != nil || n != 1 {
		t.Fatalf("late arrival rejected: %d %v", n, err)
	}
	if bytes, err := postgres.ReclaimEmptyObservationStorage(ctx, db, postgres.ObservationColdBefore(now)); err != nil || bytes != 0 {
		t.Fatalf("late event removed: %d %v", bytes, err)
	}
	var hot, cold int
	if err := db.QueryRow(`SELECT COUNT(*) FROM v2_node_observation_history`).Scan(&hot); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT SUM(event_count) FROM v2_node_observation_archive`).Scan(&cold); err != nil {
		t.Fatal(err)
	}
	if hot != 1 || cold != 1 {
		t.Fatalf("events lost: hot=%d cold=%d", hot, cold)
	}
}

func TestObservationArchiveExpiresOnlyFullyExpiredBlocks(t *testing.T) {
	db, svc, _ := observationPostgresFixture(t)
	now := time.Now().UTC().Truncate(time.Hour)
	at := now.Add(-24*time.Hour).Unix() + 123
	event := ObservationEvent{EventID: "expiry_event_123", UserID: 1, OccurredAt: at, TargetHost: "example.test", TargetPort: 443, Network: "tcp"}
	if _, err := svc.insertCompactObservation(context.Background(), 7, []ObservationEvent{event}, now.Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ArchiveObservation(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		cutoff int64
		want   int
	}{{at, 1}, {at + 1, 0}} {
		if err := postgres.DeleteExpiredObservationArchives(context.Background(), db, tc.cutoff); err != nil {
			t.Fatal(err)
		}
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM v2_node_observation_archive`).Scan(&count); err != nil || count != tc.want {
			t.Fatalf("expiry count=%d want=%d err=%v", count, tc.want, err)
		}
	}
}
