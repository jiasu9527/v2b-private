package user

import (
	"context"
	"database/sql"
	"fmt"
	"net/netip"
	"os"
	"testing"

	"forest/go-api/internal/cliententry"
	platformpostgres "forest/go-api/internal/platform/postgres"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// Run against the same disposable database as the admin collection integration
// suite. A collection can own one leaf while siblings retain inherited settings.
func TestClientEntryCollectionLeafResolutionPostgresIntegration(t *testing.T) {
	dsn := os.Getenv("FOREST_COLLECTION_TEST_DSN")
	if dsn == "" {
		t.Skip("set FOREST_COLLECTION_TEST_DSN to a disposable PostgreSQL database")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	if err := platformpostgres.EnsureClientEntrySchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	var policyID int64
	if err := db.QueryRowContext(ctx, `INSERT INTO v2_client_entry_user_policy
(name, sort, mode, action, resolve_entry_host, created_at, updated_at)
VALUES ('collection-resolver-test', -1000, 'split', 'override', 0, 1, 1) RETURNING id`).Scan(&policyID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.ExecContext(ctx, `DELETE FROM v2_client_entry_user_policy_member WHERE policy_id=$1`, policyID)
		db.ExecContext(ctx, `DELETE FROM v2_client_entry_user_policy WHERE id=$1`, policyID)
	})
	if _, err := db.ExecContext(ctx, `INSERT INTO v2_client_entry_user_policy_member
(policy_id, server_type, server_id, sort, created_at, updated_at) VALUES ($1, 'vmess', 2000000000, 0, 1, 1)`, policyID); err != nil {
		t.Fatal(err)
	}

	zero, one := int64(0), int64(1)
	cases := []struct {
		name    string
		setting *int64
		userID  int64
	}{
		{name: "outside-collection", setting: nil, userID: 2000000001},
		{name: "collection-disabled", setting: &zero, userID: 2000000002},
		{name: "collection-enabled", setting: &one, userID: 2000000003},
	}
	for _, tc := range cases {
		var leafID int64
		if err := db.QueryRowContext(ctx, `INSERT INTO v2_client_entry_user_policy_split_group
(policy_id, name, path, entry_host, resolve_entry_host, global_sort, created_at, updated_at)
VALUES ($1, $2, $2, 'shared.example.com', $3, -1000, 1, 1) RETURNING id`, policyID, tc.name, tc.setting).Scan(&leafID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO v2_client_entry_user_policy_split_assignment
(policy_id, user_id, group_id, created_at, updated_at) VALUES ($1, $2, $3, 1, 1)`, policyID, tc.userID, leafID); err != nil {
			t.Fatal(err)
		}
	}

	service := &DBService{db: db}
	service.clientEntryEnsureOnce.Do(func() {})
	for _, parentSetting := range []int64{0, 1} {
		if _, err := db.ExecContext(ctx, `UPDATE v2_client_entry_user_policy SET resolve_entry_host=$2 WHERE id=$1`, policyID, parentSetting); err != nil {
			t.Fatal(err)
		}
		for _, tc := range cases {
			t.Run(fmt.Sprintf("parent-%d/%s", parentSetting, tc.name), func(t *testing.T) {
				policies, err := service.loadClientEntryUserPolicies(ctx, tc.userID)
				if err != nil {
					t.Fatal(err)
				}
				var loaded *clientEntryUserPolicy
				for i := range policies {
					if policies[i].ID == policyID {
						loaded = &policies[i]
						break
					}
				}
				wantResolve := parentSetting != 0
				if tc.setting != nil {
					wantResolve = *tc.setting != 0
				}
				if loaded == nil || loaded.ResolveEntryHost != wantResolve {
					t.Fatalf("loaded policy=%+v want resolve=%v", loaded, wantResolve)
				}
				lookups := 0
				resolver := newClientEntryHostResolverForTest(func(_ context.Context, _, host string) ([]netip.Addr, error) {
					lookups++
					if host != "shared.example.com" {
						t.Fatalf("lookup hostname=%q", host)
					}
					return []netip.Addr{netip.MustParseAddr("198.51.100.24")}, nil
				})
				servers := []map[string]any{{"id": int64(2000000000), "type": "vmess", "host": "original.example.com"}}
				result := applyClientEntryUserPoliciesWithResolver(ctx, servers, cliententry.Subject{UserID: tc.userID}, []clientEntryUserPolicy{*loaded}, resolver)
				wantHost, wantLookups := "shared.example.com", 0
				if wantResolve {
					wantHost, wantLookups = "198.51.100.24", 1
				}
				if len(result) != 1 || result[0]["host"] != wantHost || lookups != wantLookups {
					t.Fatalf("subscription=%#v lookups=%d want host=%q lookups=%d", result, lookups, wantHost, wantLookups)
				}
			})
		}
	}
}
