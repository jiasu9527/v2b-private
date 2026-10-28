package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"forest/go-api/internal/config"
)

// Use only a disposable database initialized with database/install.pgsql.sql.
// No real probe/network calls are made: persisted probe observations exercise
// the same transaction and availability predicates as production conversion.
func TestClientEntrySplitPoolPostgresIntegration(t *testing.T) {
	dsn := os.Getenv("FOREST_SPLIT_UA_TEST_DSN")
	if dsn == "" {
		t.Skip("set FOREST_SPLIT_UA_TEST_DSN to a disposable database")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(8)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	s := NewDBService(config.Config{}, db)
	if err := s.ensureClientEntrySchema(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.ensureDNSFailoverSchema(ctx); err != nil {
		t.Fatal(err)
	}
	var occupied int
	if err := db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM v2_dns_probe) + (SELECT COUNT(*) FROM v2_client_entry_backup_ip)`).Scan(&occupied); err != nil {
		t.Fatal(err)
	}
	if occupied != 0 {
		t.Fatal("integration test requires an empty probe/IP pool in a disposable database")
	}

	t.Run("healthy unused hosts and UA are preserved", func(t *testing.T) {
		f := newSplitPoolFixture(t, ctx, db)
		users := f.users(3)
		policy, conditions := f.policy(users[0], users[2], "before.example.test")
		used := f.ip("192.0.2.1", 1, true)
		f.policy(users[0], users[2], "192.0.2.1")
		f.ip("192.0.2.2", 1, false)
		f.ip("192.0.2.3", 0, true)
		quarantined := f.ip("192.0.2.4", 1, true)
		f.exec(`UPDATE v2_client_entry_backup_ip SET quarantine_until=$2 WHERE id=$1`, quarantined, time.Now().Unix()+3600)
		f.ip("192.0.2.5", 1, true)
		f.ip("192.0.2.6", 1, true)
		result, err := s.ConvertClientEntryUserPolicyToSplit(ctx, ClientEntryUserPolicySplitConvertRequest{
			PolicyID: policy, UseBackupIPPool: true,
			// A preview can be stale or occupied; the server must not trust it.
			EntryHostA: "192.0.2.1", EntryHostB: "stale.example.test",
		})
		if err != nil {
			t.Fatal(err)
		}
		if result.SnapshotUserCount != 3 || len(result.SplitGroups) != 2 || result.SplitGroups[0].EntryHost != "192.0.2.5" || result.SplitGroups[1].EntryHost != "192.0.2.6" {
			t.Fatalf("unexpected conversion: %#v", result)
		}
		if result.SplitGroups[0].UserCount != 2 || result.SplitGroups[1].UserCount != 1 {
			t.Fatalf("snapshot is not balanced: %#v", result.SplitGroups)
		}
		var raw string
		if err := db.QueryRowContext(ctx, `SELECT conditions FROM v2_client_entry_user_policy WHERE id=$1`, policy).Scan(&raw); err != nil || raw != conditions {
			t.Fatalf("UA conditions changed: %q, err=%v", raw, err)
		}
		var usedIP string
		if err := db.QueryRowContext(ctx, `SELECT ip FROM v2_client_entry_backup_ip WHERE id=$1`, used).Scan(&usedIP); err != nil || usedIP != "192.0.2.1" {
			t.Fatalf("existing pool entry changed: %q, err=%v", usedIP, err)
		}
	})

	t.Run("insufficient pool leaves the original rule unchanged", func(t *testing.T) {
		f := newSplitPoolFixture(t, ctx, db)
		users := f.users(2)
		policy, conditions := f.policy(users[0], users[1], "before.example.test")
		f.ip("192.0.2.11", 1, true)
		_, err := s.ConvertClientEntryUserPolicyToSplit(ctx, ClientEntryUserPolicySplitConvertRequest{PolicyID: policy, UseBackupIPPool: true})
		if !errors.Is(err, ErrClientEntryBackupIPInsufficient) {
			t.Fatalf("expected insufficient pool, got %v", err)
		}
		f.assertUnconverted(policy, conditions)
	})

	t.Run("insufficient users rolls back IP ownership and ordering", func(t *testing.T) {
		f := newSplitPoolFixture(t, ctx, db)
		users := f.users(1)
		policy, conditions := f.policy(users[0], users[0], "before.example.test")
		laterPolicy, _ := f.policy(users[0], users[0], "later.example.test")
		f.exec(`UPDATE v2_client_entry_user_policy SET sort=20 WHERE id=$1`, laterPolicy)
		f.ip("192.0.2.21", 1, true)
		f.ip("192.0.2.22", 1, true)
		_, err := s.ConvertClientEntryUserPolicyToSplit(ctx, ClientEntryUserPolicySplitConvertRequest{PolicyID: policy, UseBackupIPPool: true})
		if err == nil || !strings.Contains(err.Error(), "不足 2 人") {
			t.Fatalf("expected insufficient users, got %v", err)
		}
		f.assertUnconverted(policy, conditions)
		var laterSort int64
		if err := db.QueryRowContext(ctx, `SELECT sort FROM v2_client_entry_user_policy WHERE id=$1`, laterPolicy).Scan(&laterSort); err != nil || laterSort != 20 {
			t.Fatalf("failed conversion reordered a later rule: sort=%d err=%v", laterSort, err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		ips, err := claimHealthyClientEntryBackupIPs(ctx, tx, 2, time.Now().Unix())
		if err != nil || len(ips) != 2 || ips[0].IP != "192.0.2.21" || ips[1].IP != "192.0.2.22" {
			t.Fatalf("failed conversion retained IP ownership: %#v, err=%v", ips, err)
		}
	})

	t.Run("concurrent conversions claim distinct IPs", func(t *testing.T) {
		f := newSplitPoolFixture(t, ctx, db)
		users := f.users(2)
		first, _ := f.policy(users[0], users[1], "before.example.test")
		second, _ := f.policy(users[0], users[1], "before.example.test")
		f.exec(`UPDATE v2_client_entry_user_policy SET sort=20 WHERE id=$1`, second)
		for i := 31; i <= 34; i++ {
			f.ip(fmt.Sprintf("192.0.2.%d", i), 1, true)
		}
		start := make(chan struct{})
		type conversion struct {
			record ClientEntryUserPolicyRecord
			err    error
		}
		results := make(chan conversion, 2)
		for _, id := range []int64{first, second} {
			go func(id int64) {
				<-start
				record, err := s.ConvertClientEntryUserPolicyToSplit(ctx, ClientEntryUserPolicySplitConvertRequest{PolicyID: id, UseBackupIPPool: true})
				results <- conversion{record, err}
			}(id)
		}
		close(start)
		hosts := make(map[string]bool)
		for i := 0; i < 2; i++ {
			result := <-results
			if result.err != nil {
				t.Errorf("concurrent conversion failed: %v", result.err)
				continue
			}
			for _, group := range result.record.SplitGroups {
				if hosts[group.EntryHost] {
					t.Errorf("IP claimed twice: %s", group.EntryHost)
				}
				hosts[group.EntryHost] = true
			}
		}
		if len(hosts) != 4 {
			t.Fatalf("want 4 distinct claimed IPs, got %#v", hosts)
		}
	})
}

type splitPoolFixture struct {
	t       *testing.T
	ctx     context.Context
	db      *sql.DB
	probeID int64
}

func newSplitPoolFixture(t *testing.T, ctx context.Context, db *sql.DB) *splitPoolFixture {
	t.Helper()
	f := &splitPoolFixture{t: t, ctx: ctx, db: db}
	f.probeID = f.insert("v2_dns_probe", `INSERT INTO v2_dns_probe(name,token_hash,last_heartbeat_at,created_at,updated_at) VALUES('split-pool-test',$1,$2,1,1) RETURNING id`, fmt.Sprintf("%064x", time.Now().UnixNano()), time.Now().Unix())
	return f
}

func (f *splitPoolFixture) exec(query string, args ...any) {
	f.t.Helper()
	if _, err := f.db.ExecContext(f.ctx, query, args...); err != nil {
		f.t.Fatal(err)
	}
}

func (f *splitPoolFixture) insert(table, query string, args ...any) int64 {
	f.t.Helper()
	var id int64
	if err := f.db.QueryRowContext(f.ctx, query, args...).Scan(&id); err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() {
		if _, err := f.db.ExecContext(context.Background(), "DELETE FROM "+table+" WHERE id=$1", id); err != nil {
			f.t.Errorf("cleanup %s %d: %v", table, id, err)
		}
	})
	return id
}

func (f *splitPoolFixture) users(count int) []int64 {
	f.t.Helper()
	ids := make([]int64, count)
	for i := range ids {
		token := fmt.Sprintf("p%016x%02d", time.Now().UnixNano(), i)
		ids[i] = f.insert("v2_user", `INSERT INTO v2_user(email,password,uuid,token,created_at,updated_at) VALUES($1,'unused','test',$2,1,1) RETURNING id`, token+"@example.test", token)
	}
	return ids
}

func (f *splitPoolFixture) policy(minimum, maximum int64, host string) (int64, string) {
	f.t.Helper()
	conditions := fmt.Sprintf(`[{"field":"user_id","operator":"between","min":%d,"max":%d},{"field":"ua","operator":"contains_any","values":["Forest"]}]`, minimum, maximum)
	id := f.insert("v2_client_entry_user_policy", `INSERT INTO v2_client_entry_user_policy(name,sort,action,conditions,entry_host,created_at,updated_at) VALUES('split-pool-test',10,'override',$1,$2,1,1) RETURNING id`, conditions, host)
	return id, conditions
}

func (f *splitPoolFixture) ip(ip string, enabled int, healthy bool) int64 {
	f.t.Helper()
	id := f.insert("v2_client_entry_backup_ip", `INSERT INTO v2_client_entry_backup_ip(ip,port,enabled,created_at,updated_at) VALUES($1,54101,$2,1,1) RETURNING id`, ip, enabled)
	if healthy {
		f.exec(`INSERT INTO v2_client_entry_backup_ip_state(backup_ip_id,probe_id,last_success,consecutive_success,last_reported_at,updated_at) VALUES($1,$2,1,2,$3,$3)`, id, f.probeID, time.Now().Unix())
	}
	return id
}

func (f *splitPoolFixture) assertUnconverted(id int64, conditions string) {
	f.t.Helper()
	var mode, host, raw string
	var sort, groups, assignments int64
	err := f.db.QueryRowContext(f.ctx, `SELECT mode,entry_host,conditions,sort,
 (SELECT COUNT(*) FROM v2_client_entry_user_policy_split_group WHERE policy_id=$1),
 (SELECT COUNT(*) FROM v2_client_entry_user_policy_split_assignment WHERE policy_id=$1)
 FROM v2_client_entry_user_policy WHERE id=$1`, id).Scan(&mode, &host, &raw, &sort, &groups, &assignments)
	if err != nil || mode != "standard" || host != "before.example.test" || raw != conditions || sort != 10 || groups != 0 || assignments != 0 {
		f.t.Fatalf("failed conversion modified original rule: mode=%s host=%s raw=%s sort=%d groups=%d assignments=%d err=%v", mode, host, raw, sort, groups, assignments, err)
	}
}
