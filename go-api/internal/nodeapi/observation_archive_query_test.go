package nodeapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	platformpostgres "forest/go-api/internal/platform/postgres"
	"github.com/DATA-DOG/go-sqlmock"
)

func archiveQueryTestEvent(userID, at, nodeID int64, id string) archivedObservationEvent {
	return archivedObservationEvent{NodeType: "v2node", NodeID: nodeID, EventID: id, UserID: userID,
		OccurredAt: at, ReceivedAt: at + 2, ClientIP: "2001:db8::1", TargetHost: "example.test",
		TargetIP: "2001:db8::2", TargetPort: 443, Network: "tcp", Result: "route_attempt", UplinkBytes: 12, DownlinkBytes: 34}
}

func archiveQueryTestBlock(t *testing.T, id int64, events ...archivedObservationEvent) observationArchiveQueryBlock {
	t.Helper()
	b, err := encodeObservationArchive(events)
	if err != nil {
		t.Fatal(err)
	}
	return observationArchiveQueryBlock{id: id, userID: b.UserID, firstAt: b.FirstAt, lastAt: b.LastAt,
		eventCount: b.EventCount, payload: b.Payload, checksum: b.PayloadSHA256}
}

func archiveQueryTestRows(blocks ...observationArchiveQueryBlock) *sqlmock.Rows {
	rows := sqlmock.NewRows([]string{"id", "user_id", "first_at", "last_at", "event_count", "payload", "payload_sha256"})
	for _, b := range blocks {
		rows.AddRow(b.id, b.userID, b.firstAt, b.lastAt, b.eventCount, b.payload, b.checksum)
	}
	return rows
}

func TestArchivedObservationMatchesExactFiltersAndCursor(t *testing.T) {
	at := time.Now().Add(-24*time.Hour).Truncate(time.Hour).Unix() + 100
	e := archiveQueryTestEvent(2, at, 8, "event_002")
	f := ObservationFilter{NodeType: "V2Node", NodeID: 8, UserID: 2, From: at, To: at + 1, Target: "2001:db8::2"}
	if !archivedObservationMatches(e, f, observationCursor{}, at) {
		t.Fatal("matching exact IPv6/node/user/half-open interval rejected")
	}
	for _, mutate := range []func(*ObservationFilter){
		func(f *ObservationFilter) { f.To = at },
		func(f *ObservationFilter) { f.NodeID = 7 },
		func(f *ObservationFilter) { f.UserID = 1 },
		func(f *ObservationFilter) { f.Target = "2001:db8::3" },
		func(f *ObservationFilter) { f.Target = "other.example.test" },
	} {
		bad := f
		mutate(&bad)
		if archivedObservationMatches(e, bad, observationCursor{}, at) {
			t.Fatalf("nonmatching event accepted: %+v", bad)
		}
	}
	f.Target = "example.test"
	if !archivedObservationMatches(e, f, observationCursor{}, at) || archivedObservationMatches(e, f, observationCursor{}, at+1) {
		t.Fatal("domain or inclusive lower bound is incorrect")
	}
	for _, cursor := range []observationCursor{{At: at - 1, NodeID: 8, EventID: "event_999"}, {At: at, NodeID: 7, EventID: "event_999"}, {At: at, NodeID: 8, EventID: e.EventID}} {
		if archivedObservationMatches(e, f, cursor, at) {
			t.Fatalf("event on/after cursor accepted: %+v", cursor)
		}
	}
	for _, cursor := range []observationCursor{{At: at + 1, NodeID: 1, EventID: "a"}, {At: at, NodeID: 9, EventID: "a"}, {At: at, NodeID: 8, EventID: "event_003"}} {
		if !archivedObservationMatches(e, f, cursor, at) {
			t.Fatalf("event strictly before cursor rejected: %+v", cursor)
		}
	}
}

func TestObservationArchiveWhereUsesMetadataIndexesAndParameters(t *testing.T) {
	at := time.Now().Add(-time.Hour).Unix()
	f := ObservationFilter{From: at, To: at + 60, UserID: 2, Account: "X@example.test", NodeID: 7, Target: "example.test"}
	where, args := observationArchiveWhere(f, observationCursor{At: at + 30})
	for _, part := range []string{"a.last_at >= $1", "a.first_at < $2", "a.user_id = $3", "lower(email)=lower($4)", "a.node_ids @> ARRAY[$5::bigint]", "a.target_hosts @> ARRAY[$6::text]", "a.first_at <= $7"} {
		if !strings.Contains(where, part) {
			t.Fatalf("missing metadata prefilter %q in %s", part, where)
		}
	}
	if !reflect.DeepEqual(args, []any{at, at + 60, int64(2), "X@example.test", int64(7), "example.test", at + 30}) {
		t.Fatalf("incorrect query args: %#v", args)
	}
	f.Target = "2001:db8::2"
	where, _ = observationArchiveWhere(f, observationCursor{})
	if !strings.Contains(where, "a.target_ips @>") || strings.Contains(where, "a.target_hosts @>") {
		t.Fatalf("IPv6 must use its exact index: %s", where)
	}
}

func TestArchivedObservationEventsTopNIncludesSameSecondBlocks(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	at := time.Now().Add(-24*time.Hour).Truncate(time.Hour).Unix() + 100
	first := archiveQueryTestBlock(t, 3, archiveQueryTestEvent(1, at, 8, "A"), archiveQueryTestEvent(1, at, 7, "z"))
	second := archiveQueryTestBlock(t, 2, archiveQueryTestEvent(2, at, 8, "z"), archiveQueryTestEvent(2, at, 8, "Z"))
	// This older corrupt block must never be decoded once the top-N boundary
	// is established, while equal-second blocks above must not be skipped.
	older := observationArchiveQueryBlock{id: 1, lastAt: at - 1, payload: []byte("corrupt")}
	mock.ExpectBegin()
	mock.ExpectQuery(`FROM v2_node_observation_archive`).WithArgs(at-1, at+1, observationArchiveQueryBatch).
		WillReturnRows(archiveQueryTestRows(first, second, older))
	mock.ExpectQuery(`SELECT id,email FROM v2_user`).WithArgs("[2]").WillReturnRows(sqlmock.NewRows([]string{"id", "email"}).AddRow(2, "two@example.test"))
	mock.ExpectRollback()
	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	items, err := (&DBService{}).archivedObservationEvents(context.Background(), tx, ObservationFilter{From: at - 1, To: at + 1}, observationCursor{}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0]["event_id"] != "z" || items[1]["event_id"] != "Z" || items[0]["email"] != "two@example.test" || items[0]["result"] != "route_attempt" || items[0]["uplink_bytes"] != int64(12) {
		t.Fatalf("incorrect descending top-N or event fields: %#v", items)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestArchivedObservationEventsBatchKeysetAndDeletedAccount(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	at := time.Now().Add(-24*time.Hour).Truncate(time.Hour).Unix() + 100
	blocks := make([]observationArchiveQueryBlock, 17)
	for i := range blocks {
		blocks[i] = archiveQueryTestBlock(t, int64(100-i), archiveQueryTestEvent(3, at-int64(i), 7, fmt.Sprintf("event_%03d", i)))
	}
	mock.ExpectBegin()
	mock.ExpectQuery(`FROM v2_node_observation_archive`).WithArgs(at-100, at+1, observationArchiveQueryBatch).WillReturnRows(archiveQueryTestRows(blocks[:16]...))
	mock.ExpectQuery(`AND \(a.last_at,a.id\) < \(\$3,\$4\)`).WithArgs(at-100, at+1, blocks[15].lastAt, blocks[15].id, observationArchiveQueryBatch).WillReturnRows(archiveQueryTestRows(blocks[16]))
	mock.ExpectQuery(`SELECT id,email FROM v2_user`).WithArgs("[3]").WillReturnRows(sqlmock.NewRows([]string{"id", "email"}))
	mock.ExpectRollback()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	items, err := (&DBService{}).archivedObservationEvents(context.Background(), tx, ObservationFilter{From: at - 100, To: at + 1}, observationCursor{}, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 17 || items[0]["event_id"] != "event_000" || items[16]["event_id"] != "event_016" || items[0]["email"] != "" {
		t.Fatalf("incorrect paged candidates/deleted account: %#v", items)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeObservationQueryBlockRejectsMetadataMismatch(t *testing.T) {
	at := time.Now().Add(-24*time.Hour).Truncate(time.Hour).Unix() + 100
	valid := archiveQueryTestBlock(t, 1, archiveQueryTestEvent(2, at, 7, "event_001"))
	if _, err := decodeObservationQueryBlock(valid); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*observationArchiveQueryBlock){
		"checksum": func(b *observationArchiveQueryBlock) { b.checksum = []byte("bad") },
		"count":    func(b *observationArchiveQueryBlock) { b.eventCount++ },
		"zero":     func(b *observationArchiveQueryBlock) { b.eventCount = 0 },
		"user":     func(b *observationArchiveQueryBlock) { b.userID++ },
		"first":    func(b *observationArchiveQueryBlock) { b.firstAt-- },
		"last":     func(b *observationArchiveQueryBlock) { b.lastAt++ },
	} {
		t.Run(name, func(t *testing.T) {
			bad := valid
			mutate(&bad)
			if _, err := decodeObservationQueryBlock(bad); err == nil {
				t.Fatal("corrupt archive metadata accepted")
			}
		})
	}
}

func TestScanArchivedObservationsPropagatesVisitorError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	at := time.Now().Add(-24*time.Hour).Truncate(time.Hour).Unix() + 100
	block := archiveQueryTestBlock(t, 1, archiveQueryTestEvent(1, at, 7, "event_001"))
	mock.ExpectBegin()
	mock.ExpectQuery(`FROM v2_node_observation_archive`).WillReturnRows(archiveQueryTestRows(block))
	mock.ExpectRollback()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	want := errors.New("stop visitor")
	calls := 0
	err = scanArchivedObservations(context.Background(), tx, ObservationFilter{From: at, To: at + 1}, func(e archivedObservationEvent) error { calls++; return want })
	if !errors.Is(err, want) || calls != 1 {
		t.Fatalf("visitor error lost: calls=%d err=%v", calls, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := scanArchivedObservations(ctx, tx, ObservationFilter{}, func(archivedObservationEvent) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	if err := scanArchivedObservations(context.Background(), nil, ObservationFilter{}, nil); err == nil {
		t.Fatal("nil visitor accepted")
	}
}

func insertArchiveQueryTestBlock(t *testing.T, db interface {
	Exec(string, ...any) (sql.Result, error)
}, events ...archivedObservationEvent) observationArchiveBlock {
	t.Helper()
	b, err := encodeObservationArchive(events)
	if err != nil {
		t.Fatal(err)
	}
	nodes, _ := json.Marshal(b.NodeIDs)
	hosts, _ := json.Marshal(b.TargetHosts)
	ips, _ := json.Marshal(b.TargetIPs)
	if _, err := db.Exec(`INSERT INTO v2_node_observation_archive(hour_at,user_id,first_at,last_at,event_count,known_events,payload,payload_sha256,event_bloom,node_ids,target_hosts,target_ips)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,
ARRAY(SELECT value::bigint FROM jsonb_array_elements_text($10::jsonb)),
ARRAY(SELECT value FROM jsonb_array_elements_text($11::jsonb)),
ARRAY(SELECT value FROM jsonb_array_elements_text($12::jsonb)))`, b.HourAt, b.UserID, b.FirstAt, b.LastAt, b.EventCount, b.KnownEvents, b.Payload, b.PayloadSHA256, b.EventBloom, string(nodes), string(hosts), string(ips)); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestArchivedObservationQueryPostgresFiltersAndStreaming(t *testing.T) {
	db, service, _ := observationPostgresFixture(t)
	ctx := context.Background()
	if err := platformpostgres.EnsureObservationArchiveSchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-24*time.Hour).Truncate(time.Hour).Unix() + 100
	one := archiveQueryTestEvent(1, at, 7, "event_001")
	two := archiveQueryTestEvent(2, at+1, 8, "event_002")
	other := archiveQueryTestEvent(2, at+2, 7, "event_003")
	other.TargetHost, other.TargetIP = "other.example.test", "198.51.100.1"
	insertArchiveQueryTestBlock(t, db, one)
	insertArchiveQueryTestBlock(t, db, two, other)
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, test := range []struct {
		filter ObservationFilter
		ids    []string
	}{
		{ObservationFilter{From: at, To: at + 3}, []string{"event_003", "event_002", "event_001"}},
		{ObservationFilter{From: at, To: at + 3, Account: "CURRENT@example.test"}, []string{"event_003", "event_002"}},
		{ObservationFilter{From: at, To: at + 3, Target: "2001:db8::2"}, []string{"event_002", "event_001"}},
		{ObservationFilter{From: at, To: at + 3, Target: "example.test", NodeID: 8}, []string{"event_002"}},
		{ObservationFilter{From: at, To: at + 1}, []string{"event_001"}},
		{ObservationFilter{From: at, To: at + 3, UserID: 99}, []string{}},
	} {
		items, err := service.archivedObservationEvents(ctx, tx, test.filter, observationCursor{}, 100)
		if err != nil {
			t.Fatal(err)
		}
		ids := make([]string, 0, len(items))
		for _, item := range items {
			ids = append(ids, item["event_id"].(string))
		}
		if !reflect.DeepEqual(ids, test.ids) {
			t.Fatalf("filter %+v: got %v want %v", test.filter, ids, test.ids)
		}
	}
	count := 0
	if err := scanArchivedObservations(ctx, tx, ObservationFilter{From: at, To: at + 3, Account: "current@example.test"}, func(e archivedObservationEvent) error {
		count++
		if e.UserID != 2 {
			return fmt.Errorf("account filter leaked user %d", e.UserID)
		}
		return nil
	}); err != nil || count != 2 {
		t.Fatalf("stream count=%d err=%v", count, err)
	}
}

func TestArchivedObservationQueryPostgresSnapshotDuringMove(t *testing.T) {
	db, service, _ := observationPostgresFixture(t)
	ctx := context.Background()
	if err := platformpostgres.EnsureObservationArchiveSchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-24*time.Hour).Truncate(time.Hour).Unix() + 100
	event := archiveQueryTestEvent(1, at, 7, "snapshot_event_001")
	if _, err := db.Exec(`INSERT INTO v2_node_observation_event(node_type,node_id,event_id,user_id,occurred_at,client_ip,target_host,target_ip,target_port,network,result,uplink_bytes,downlink_bytes,received_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`, event.NodeType, event.NodeID, event.EventID, event.UserID, event.OccurredAt, event.ClientIP, event.TargetHost, event.TargetIP, event.TargetPort, event.Network, event.Result, event.UplinkBytes, event.DownlinkBytes, event.ReceivedAt); err != nil {
		t.Fatal(err)
	}
	reader, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Rollback()
	var before int
	if err := reader.QueryRow(`SELECT COUNT(*) FROM v2_node_observation_history`).Scan(&before); err != nil || before != 1 {
		t.Fatalf("initial hot read count=%d err=%v", before, err)
	}
	writer, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback()
	insertArchiveQueryTestBlock(t, writer, event)
	if _, err := writer.Exec(`DELETE FROM v2_node_observation_event WHERE node_id=7 AND event_id=$1`, event.EventID); err != nil {
		t.Fatal(err)
	}
	if err := writer.Commit(); err != nil {
		t.Fatal(err)
	}
	filter := ObservationFilter{From: at, To: at + 1}
	cold, err := service.archivedObservationEvents(ctx, reader, filter, observationCursor{}, 10)
	if err != nil || len(cold) != 0 {
		t.Fatalf("old snapshot exposed newly archived event: cold=%d err=%v", len(cold), err)
	}
	var oldHot int
	if err := reader.QueryRow(`SELECT COUNT(*) FROM v2_node_observation_history`).Scan(&oldHot); err != nil || oldHot != 1 {
		t.Fatalf("old snapshot lost hot event: hot=%d err=%v", oldHot, err)
	}
	fresh, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Rollback()
	var newHot int
	if err := fresh.QueryRow(`SELECT COUNT(*) FROM v2_node_observation_history`).Scan(&newHot); err != nil || newHot != 0 {
		t.Fatalf("new snapshot retained deleted hot event: hot=%d err=%v", newHot, err)
	}
	cold, err = service.archivedObservationEvents(ctx, fresh, filter, observationCursor{}, 10)
	if err != nil || len(cold) != 1 || cold[0]["event_id"] != event.EventID {
		t.Fatalf("new snapshot must expose one cold event: cold=%#v err=%v", cold, err)
	}
}
