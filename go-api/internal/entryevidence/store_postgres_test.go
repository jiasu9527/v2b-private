package entryevidence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestEntryEvidenceStorePostgres(t *testing.T) {
	dsn := os.Getenv("FOREST_ENTRY_EVIDENCE_TEST_DSN")
	if dsn == "" {
		t.Skip("requires disposable FOREST_ENTRY_EVIDENCE_TEST_DSN")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	store := NewStore(db)
	if err = store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`TRUNCATE v2_entry_delivery_request CASCADE`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec(`TRUNCATE v2_entry_delivery_request CASCADE`); store.SaveSettings(ctx, 30) })
	now := time.Now().Unix()
	request := Request{ID: "fixture-1", UserID: 71, EmailAtEvent: "user@example.test", UA: "Forest/1", RequestedAt: now - 11, CompletedAt: now - 10, Transport: "subscription", Status: 200, ResponseComplete: true, Coverage: "complete", BytesWritten: 100}
	nodes := []Node{{ServerType: "vless", ServerID: 1, PolicyID: 7, GroupID: 11, SourceHost: "entry.example.test", DeliveredHost: "192.0.2.7", Port: "54101", Resolved: true, Source: "split", RuleRevision: Revision("fixture")}, {ServerType: "vless", ServerID: 2, PolicyID: 8, GroupID: 12, DeliveredHost: "192.0.2.8", Port: "54102", Source: "split"}}
	if err = store.Record(ctx, request, nodes); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		filter Filter
		want   int64
	}{
		{"identity and actual node", Filter{UserID: 71, Host: "192.0.2.7", PolicyID: 7, GroupID: 11, Port: "54101", UA: "forest", From: now - 10, To: now}, 1},
		{"AND must apply to same emitted node", Filter{PolicyID: 7, Host: "192.0.2.8"}, 0},
		{"half-open end excludes boundary", Filter{From: now - 20, To: now - 10}, 0},
		{"identity stays precise", Filter{UserID: 72}, 0},
		{"email normalization", Filter{Email: " USER@EXAMPLE.TEST "}, 1},
		{"literal UA no SQL wildcard", Filter{UA: "%"}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page, err := store.List(ctx, tc.filter)
			if err != nil || page.Total != tc.want {
				t.Fatal(page, err)
			}
			if tc.want > 0 && (len(page.Items) != 1 || len(page.Items[0].Nodes) != 2) {
				t.Fatal("actual node metadata missing", page)
			}
		})
	}
	copy := request
	copy.ID = "fixture-304"
	copy.Status = 304
	copy.ResponseComplete = false
	copy.Coverage = "not_emitted"
	if err = store.Record(ctx, copy, nil); err != nil {
		t.Fatal(err)
	}
	copy.ID = "fixture-failed"
	copy.Status = 200
	copy.Coverage = "complete"
	copy.WriteError = "short_write"
	if err = store.Record(ctx, copy, nodes); err != nil {
		t.Fatal(err)
	}
	page, err := store.List(ctx, Filter{CompleteOnly: true})
	if err != nil || page.Total != 1 {
		t.Fatal("failed/304 falsely reported delivered", page, err)
	}
	if err = store.Record(ctx, request, nodes); err == nil {
		t.Fatal("duplicate request must not duplicate evidence")
	}
	status, err := store.Status(ctx)
	if err != nil || status.Requests != 3 || status.Completed != 1 || status.WriteFailures != 1 {
		t.Fatal(status, err)
	}
	// A failure after the request insert must roll back that request too.
	if _, err = db.Exec(`ALTER TABLE v2_entry_delivery_node ADD CONSTRAINT fixture_reject_node CHECK(server_id<>99999)`); err != nil {
		t.Fatal(err)
	}
	copy = request
	copy.ID = "fixture-rollback"
	bad := nodes[0]
	bad.ServerID = 99999
	if err = store.Record(ctx, copy, []Node{bad}); err == nil {
		t.Fatal("fixture rejection did not fail")
	}
	if _, err = db.Exec(`ALTER TABLE v2_entry_delivery_node DROP CONSTRAINT fixture_reject_node`); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Get(ctx, copy.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("partial evidence transaction persisted", err)
	}
	for _, days := range []int{0, -1, 366} {
		if _, err = store.SaveSettings(ctx, days); !errors.Is(err, ErrValidation) {
			t.Fatal("invalid retention accepted", days, err)
		}
	}
	if _, err = store.SaveSettings(ctx, 1); err != nil {
		t.Fatal(err)
	}
	copy = request
	copy.ID = "fixture-old"
	copy.RequestedAt = now - 3*86400
	copy.CompletedAt = now - 2*86400
	if err = store.Record(ctx, copy, nodes); err != nil {
		t.Fatal(err)
	}
	if err = store.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Get(ctx, copy.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("retention did not remove old request", err)
	}
	var n int
	if err = db.QueryRow(`SELECT COUNT(*) FROM v2_entry_delivery_node WHERE request_id=$1`, copy.ID).Scan(&n); err != nil || n != 0 {
		t.Fatal("orphan node metadata retained", n, err)
	}
	// No raw URI/credential fields are accepted by the stored node projection.
	copy = request
	copy.ID = "fixture-sanitized"
	bad = nodes[0]
	bad.SourceHost = "vless://secret-token@example.test"
	bad.DeliveredHost = "https://example.test/?token=secret-token"
	bad.Port = "password=secret-token"
	bad.RuleRevision = "secret-token"
	bad.Source = "secret-token"
	if err = store.Record(ctx, copy, []Node{bad}); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(ctx, copy.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), "secret-token") {
		t.Fatal("credential-bearing metadata stored", string(raw))
	}
}
