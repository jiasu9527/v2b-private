package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"forest/go-api/internal/config"
	"forest/go-api/internal/entryevidence"
	"forest/go-api/internal/platform/postgres"
)

func TestEntryEvidenceObservationPostgres(t *testing.T) {
	dsn := os.Getenv("FOREST_ENTRY_EVIDENCE_OBSERVATION_DSN")
	if dsn == "" {
		t.Skip("requires disposable observation DB")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	// This exposure-only fixture has no business tables. Empty session-local
	// monitor tables exercise the shared refresh without touching other tests.
	for _, stmt := range []string{
		`CREATE TEMP TABLE v2_client_entry_monitor(id BIGINT PRIMARY KEY,policy_id BIGINT)`,
		`CREATE TEMP TABLE v2_client_entry_monitor_target(id BIGINT PRIMARY KEY,monitor_id BIGINT,source_key TEXT)`,
		`CREATE TEMP TABLE v2_client_entry_monitor_event(id BIGINT PRIMARY KEY,monitor_id BIGINT,target_id BIGINT,probe_id BIGINT,event_type TEXT,created_at BIGINT,details TEXT)`,
	} {
		if _, err = db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	if err = entryevidence.EnsureSchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err = postgres.EnsureEntryEvidenceCasesSchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	s := NewDBService(config.Config{}, db)
	now := time.Now().Unix()
	fixtureID := time.Now().UnixNano()
	c := EntryEvidenceCase{SelectedCount: 3, Query: EntryEvidenceQueryRequest{Port: 54101}, Execution: &EntryEvidenceExecution{PolicyID: fixtureID, GroupAID: fixtureID + 1, GroupBID: fixtureID + 2, EntryHostA: "192.0.2.1", EntryHostB: "192.0.2.2", ExecutedAt: now - 100}}
	raw, _ := json.Marshal(c)
	execution, _ := json.Marshal(c.Execution)
	if err = db.QueryRow(`INSERT INTO v2_entry_evidence_case(created_by,created_at,updated_at,status,snapshot,topology_revision,execution) VALUES(1,$1,$1,'executed',$2,'fixture',$3) RETURNING id`, now, string(raw), string(execution)).Scan(&c.ID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Exec(`DELETE FROM v2_entry_evidence_case WHERE id=$1`, c.ID)
		db.Exec(`DELETE FROM v2_entry_delivery_request WHERE id LIKE $1`, fmt.Sprintf("obs-%d-%%", c.ID))
	})
	for id, branch := range map[int]string{1: "A", 2: "A", 3: "B"} {
		if _, err = db.Exec(`INSERT INTO v2_entry_evidence_case_candidate(case_id,user_id,selected,branch,evidence) VALUES($1,$2,true,$3,'{}')`, c.ID, id, branch); err != nil {
			t.Fatal(err)
		}
	}
	store := entryevidence.NewStore(db)
	for _, event := range []struct {
		id, user, group int
		host            string
		at              int64
		complete        bool
	}{{1, 1, 91, "192.0.2.1", now - 10, true}, {2, 1, 91, "192.0.2.1", now - 5, true}, {3, 2, 91, "192.0.2.1", now - 5, false}, {4, 3, 92, "192.0.2.2", now - 5, true}, {5, 99, 91, "192.0.2.1", now - 5, true}, {6, 2, 91, "192.0.2.1", now - 200, true}, {7, 2, 91, "192.0.2.9", now - 5, true}} {
		r := entryevidence.Request{ID: fmt.Sprintf("obs-%d-%d", c.ID, event.id), UserID: int64(event.user), RequestedAt: event.at, CompletedAt: event.at, Transport: "server_json", Status: 200, ResponseComplete: event.complete, Coverage: "complete"}
		if err = store.Record(ctx, r, []entryevidence.Node{{ServerType: "vless", ServerID: 5, PolicyID: c.Execution.PolicyID, GroupID: c.Execution.GroupAID + int64(event.group-91), DeliveredHost: event.host, Port: "54101", Source: "split"}}); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.refreshEntryEvidenceCaseObservations(ctx, &c); err != nil {
		t.Fatal(err)
	}
	if len(c.Observations) != 2 || c.Observations[0].FixedUsers != 2 || c.Observations[0].AcquiredUsers != 1 || c.Observations[0].NoRecordedDelivery != 1 || c.Observations[1].AcquiredUsers != 1 {
		t.Fatal("wrong user dedup/window/host/failure filtering", c.Observations)
	}
	if _, err = db.Exec(`DELETE FROM v2_entry_delivery_request WHERE id LIKE $1`, fmt.Sprintf("obs-%d-%%", c.ID)); err != nil {
		t.Fatal(err)
	}
	if err = s.refreshEntryEvidenceCaseObservations(ctx, &c); err != nil {
		t.Fatal(err)
	}
	if len(c.Observations) != 2 || c.Observations[0].AcquiredUsers != 1 || c.Observations[1].AcquiredUsers != 1 {
		t.Fatal("retention erased frozen exposure", c.Observations)
	}
	if _, err = db.Exec(`UPDATE v2_entry_evidence_observation_state SET last_refreshed_at=$2 WHERE case_id=$1`, c.ID, now-400*86400); err != nil {
		t.Fatal(err)
	}
	c.Observations = nil
	if err = s.refreshEntryEvidenceCaseObservations(ctx, &c); err != nil {
		t.Fatal(err)
	}
	if !c.Observations[0].CoverageGap || c.ObservationWarning == "" {
		t.Fatal("retention gap hidden", c)
	}
	// A resolved domain is attributed via the captured source, never fresh DNS.
	c.Execution.EntryHostA = "round.example.test"
	resolvedRequest := entryevidence.Request{ID: fmt.Sprintf("obs-%d-domain", c.ID), UserID: 2, RequestedAt: now - 1, CompletedAt: now - 1, Transport: "server_json", Status: 200, ResponseComplete: true, Coverage: "complete"}
	node := entryevidence.Node{ServerType: "vless", ServerID: 5, PolicyID: c.Execution.PolicyID, GroupID: c.Execution.GroupAID, SourceHost: "round.example.test", DeliveredHost: "192.0.2.7", Port: "54101", Source: "split"}
	if err = store.Record(ctx, resolvedRequest, []entryevidence.Node{node}); err != nil {
		t.Fatal(err)
	}
	if err = s.refreshEntryEvidenceCaseObservations(ctx, &c); err != nil {
		t.Fatal(err)
	}
	if c.Observations[0].AcquiredUsers != 1 {
		t.Fatal("unresolved source falsely matched", c.Observations)
	}
	resolvedRequest.ID += "-resolved"
	node.Resolved = true
	if err = store.Record(ctx, resolvedRequest, []entryevidence.Node{node}); err != nil {
		t.Fatal(err)
	}
	if err = s.refreshEntryEvidenceCaseObservations(ctx, &c); err != nil {
		t.Fatal(err)
	}
	if c.Observations[0].AcquiredUsers != 2 {
		t.Fatal("resolved-domain exposure missing", c.Observations)
	}
}
