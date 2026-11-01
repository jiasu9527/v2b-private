package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"forest/go-api/internal/aiops"
	"forest/go-api/internal/config"
	"forest/go-api/internal/entryevidence"
	"forest/go-api/internal/session"
)

// Runs the real subscription renderer -> request collector -> PostgreSQL store
// -> admin HTTP / AI private adapter, without current routing lookups at query
// time. The user and policy IDs intentionally have no current database rows.
func TestEntryDeliveryStatisticsHTTPPostgresEndToEnd(t *testing.T) {
	dsn := os.Getenv("FOREST_DELIVERY_HTTP_TEST_DSN")
	if dsn == "" {
		t.Skip("requires disposable FOREST_DELIVERY_HTTP_TEST_DSN")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := entryevidence.NewStore(db)
	ctx := context.Background()
	if err = store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`TRUNCATE v2_entry_delivery_request CASCADE`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec(`TRUNCATE v2_entry_delivery_request CASCADE`) })
	var oldRevision = entryevidence.Revision("old-rule-snapshot")
	deliver := func(id int64, ua, revision, host string, failed bool) {
		t.Helper()
		r := httptest.NewRequest("GET", "http://fixture/subscribe?token=PRIVATE_SUB_TOKEN", nil).WithContext(entryevidence.WithStore(ctx, store))
		r.Header.Set("User-Agent", ua)
		var base http.ResponseWriter = httptest.NewRecorder()
		if failed {
			base = failingEntryWriter{httptest.NewRecorder(), true}
		}
		w, r, finish := beginEntryDelivery(base, r, "subscription")
		c := entryevidence.FromContext(r.Context())
		c.SetUser(id, "historical@example.test")
		servers := []map[string]any{}
		for i := int64(1); i <= 2; i++ {
			c.Observe(entryevidence.Node{ServerType: "vmess", ServerID: i, PolicyID: 7, GroupID: 9, CollectionID: 3, RuleRevision: revision, Source: "split", SourceHost: host, DeliveredHost: host, Port: "54101"})
			servers = append(servers, map[string]any{"type": "vmess", "id": i, "host": host, "port": 54101, "name": "fixture", "network": "tcp"})
		}
		body := buildGeneralSubscribePayload("PRIVATE_NODE_UUID", servers, c)
		_, _ = w.Write([]byte(body))
		finish()
	}
	for i := 0; i < 3; i++ {
		deliver(71, "Forest/1", oldRevision, "192.0.2.7", false)
	}
	deliver(72, "Clash/1", oldRevision, "192.0.2.7", false)
	deliver(73, "Forest/1", oldRevision, "192.0.2.7", true)
	deliver(71, "Forest/1", entryevidence.Revision("new-rule-snapshot"), "192.0.2.8", false)
	q := url.Values{"host": {"192.0.2.7"}, "port": {"54101"}, "collection_id": {"3"}, "from": {"1"}}
	read := func(action string, query url.Values) map[string]json.RawMessage {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/?auth_data=fixture&"+query.Encode(), nil)
		handleEntryEvidence(w, r, strictAppleIDSession{identity: &session.Identity{ID: 1, IsAdmin: 1}}, nil, store, action)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "PRIVATE_") {
			t.Fatal("subscription credential exposed")
		}
		var out struct {
			Data map[string]json.RawMessage `json:"data"`
		}
		if err = json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out.Data
	}
	stats := read("delivery-stats", q)
	if string(stats["users"]) != "2" || string(stats["requests"]) != "4" {
		t.Fatal(stats)
	}
	users := read("delivery-users", q)
	if string(users["total"]) != "2" {
		t.Fatal(users)
	}
	versions := read("delivery-versions", q)
	var rows []struct {
		Version  string `json:"version"`
		Revision string `json:"rule_revision"`
	}
	if json.Unmarshal(versions["items"], &rows) != nil || len(rows) != 1 || rows[0].Revision != oldRevision || len(rows[0].Version) != 32 {
		t.Fatal(versions)
	}
	q.Set("version", rows[0].Version)
	q.Set("ua", "forest")
	stats = read("delivery-stats", q)
	if string(stats["users"]) != "1" || string(stats["requests"]) != "3" {
		t.Fatal(stats)
	}
	panel := newPanelOperations(config.Config{}, &routerState{entryEvidence: store})
	for _, name := range []string{"delivery_stats", "delivery_users", "delivery_versions"} {
		out, e := panel.Execute(ctx, aiops.PanelOperationActor{ID: 1}, "entry_evidence."+name, json.RawMessage(`{"host":"192.0.2.7","collection_id":3,"ua":"forest"}`))
		if e != nil || !json.Valid(out) || strings.Contains(string(out), "PRIVATE_") {
			t.Fatal(name, string(out), e)
		}
	}
	// A 304 must not add new delivery even though a caller prepared nodes.
	r := httptest.NewRequest("GET", "/subscription", nil).WithContext(entryevidence.WithStore(ctx, store))
	r.Header.Set("User-Agent", "Forest/1")
	w, r, finish := beginEntryDelivery(httptest.NewRecorder(), r, "subscription")
	c := entryevidence.FromContext(r.Context())
	c.SetUser(74, "cache@example.test")
	c.Observe(entryevidence.Node{ServerType: "vmess", ServerID: 1, PolicyID: 7, GroupID: 9, CollectionID: 3, RuleRevision: oldRevision, Source: "split", SourceHost: "192.0.2.7", DeliveredHost: "192.0.2.7", Port: "54101"})
	buildGeneralSubscribePayload("PRIVATE_UUID", []map[string]any{{"type": "vmess", "id": int64(1), "host": "192.0.2.7", "port": 54101, "name": "fixture", "network": "tcp"}}, c)
	w.WriteHeader(http.StatusNotModified)
	finish()
	stats = read("delivery-stats", q)
	if string(stats["requests"]) != "3" {
		t.Fatal("304 counted as new delivery", stats)
	}
}
