package user

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"forest/go-api/internal/cliententry"
	"forest/go-api/internal/config"
	"forest/go-api/internal/entryevidence"
	platformpostgres "forest/go-api/internal/platform/postgres"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestEntryDeliveryCollectionReadPostgres(t *testing.T) {
	dsn := os.Getenv("FOREST_COLLECTION_TEST_DSN")
	if dsn == "" {
		t.Skip("requires disposable FOREST_COLLECTION_TEST_DSN")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	if err = platformpostgres.EnsureClientEntrySchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	s := NewDBService(config.Config{}, db)
	var user, collection, standard, split, leaf, otherLeaf int64
	marker := fmt.Sprint(time.Now().UnixNano())
	if err = db.QueryRow(`INSERT INTO v2_user(email,password,uuid,token,created_at,updated_at) VALUES($1,'unused','unused',$2,1,1) RETURNING id`, "delivery-"+marker+"@example.test", marker).Scan(&user); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM v2_user WHERE id=$1`, user) })
	for i, id := range []*int64{&standard, &split} {
		mode := "standard"
		if i == 1 {
			mode = "split"
		}
		if err = db.QueryRow(`INSERT INTO v2_client_entry_user_policy(name,mode,action,entry_host,created_at,updated_at) VALUES($1,$2,'override','same.example.test',1,1) RETURNING id`, "delivery-"+marker, mode).Scan(id); err != nil {
			t.Fatal(err)
		}
		policy := *id
		t.Cleanup(func() {
			db.Exec(`DELETE FROM v2_client_entry_user_policy_member WHERE policy_id=$1`, policy)
			db.Exec(`DELETE FROM v2_client_entry_user_policy WHERE id=$1`, policy)
		})
		for server := 1; server <= 2; server++ {
			if _, err = db.Exec(`INSERT INTO v2_client_entry_user_policy_member(policy_id,server_type,server_id,created_at,updated_at) VALUES($1,'vless',$2,1,1)`, policy, server); err != nil {
				t.Fatal(err)
			}
		}
	}
	for i, id := range []*int64{&leaf, &otherLeaf} {
		if err = db.QueryRow(`INSERT INTO v2_client_entry_user_policy_split_group(policy_id,name,path,entry_host,created_at,updated_at) VALUES($1,'leaf',$2,'same.example.test',1,1) RETURNING id`, split, fmt.Sprint(i)).Scan(id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec(`INSERT INTO v2_client_entry_user_policy_split_assignment(policy_id,user_id,group_id,created_at,updated_at) VALUES($1,$2,$3,1,1)`, split, user, leaf); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`INSERT INTO v2_client_entry_collection(name,entry_host,version,created_at,updated_at) VALUES($1,'same.example.test',7,1,1) RETURNING id`, "delivery-"+marker).Scan(&collection); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM v2_client_entry_collection WHERE id=$1`, collection) })
	if _, err = db.Exec(`INSERT INTO v2_client_entry_collection_member(collection_id,policy_id) VALUES($1,$2)`, collection, standard); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO v2_client_entry_collection_member(collection_id,split_group_id) VALUES($1,$2)`, collection, leaf); err != nil {
		t.Fatal(err)
	}
	policies, err := s.loadClientEntryUserPolicies(ctx, user)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, p := range policies {
		if p.ID != standard && p.ID != split {
			continue
		}
		seen++
		if p.CollectionID != collection || p.CollectionVersion != 7 || len(p.Members) != 2 || p.ID == split && p.AssignedGroupID != leaf {
			t.Fatalf("wrong collection or duplicated members: %+v", p)
		}
	}
	if seen != 2 {
		t.Fatal("missing fixture policies", seen)
	}
	targets, err := LoadSupportEntryTargets(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	seen = 0
	for _, target := range targets {
		if target.PolicyID != standard && target.PolicyID != split {
			continue
		}
		seen++
		want := collection
		if target.GroupID == otherLeaf {
			want = 0
		}
		if target.CollectionID != want || len(target.Members) != 2 {
			t.Fatalf("target collection matched by host rather than exact source: %+v", target)
		}
	}
	if seen != 3 {
		t.Fatal("wrong target count", seen)
	}
	// Changing today's collection does not rewrite already captured metadata.
	c := entryevidence.NewCollector("subscription", "Forest")
	applyClientEntryUserPoliciesWithResolver(entryevidence.WithCollector(ctx, c), []map[string]any{{"id": int64(1), "type": "vless", "host": "origin.example", "port": 54101}}, cliententry.Subject{UserID: user}, policies, nil)
	if _, err = db.Exec(`DELETE FROM v2_client_entry_collection_member WHERE collection_id=$1`, collection); err != nil {
		t.Fatal(err)
	}
	n, ok := c.Observed("vless", 1)
	if !ok || n.CollectionID != collection {
		t.Fatal("historical metadata lost", n)
	}
	after, err := s.loadClientEntryUserPolicies(ctx, user)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range after {
		if (p.ID == standard || p.ID == split) && p.CollectionID != 0 {
			t.Fatal("new request inherited stale membership", p)
		}
	}
}
