package admin

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"forest/go-api/internal/config"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func TestClientEntryRelativeOrderPostgres(t *testing.T) {
	dsn := os.Getenv("FOREST_ENTRY_EVIDENCE_TEST_DSN")
	if dsn == "" {
		dsn = os.Getenv("FOREST_AIOPS_TEST_DSN")
	}
	if dsn == "" {
		t.Skip("requires disposable PostgreSQL test DSN")
	}
	conf, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	db := stdlib.OpenDB(*conf)
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	schema := fmt.Sprintf("entry_order_%d", time.Now().UnixNano())
	if _, err = db.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	defer db.ExecContext(ctx, `DROP SCHEMA `+schema+` CASCADE`)
	conf.RuntimeParams["search_path"] = schema
	scoped := stdlib.OpenDB(*conf)
	defer scoped.Close()
	for _, protocol := range []string{"shadowsocks", "vmess", "vless", "trojan", "tuic", "hysteria", "anytls", "v2node"} {
		if _, err := scoped.ExecContext(ctx, `CREATE TABLE v2_server_`+protocol+`(id INTEGER PRIMARY KEY)`); err != nil {
			t.Fatal(err)
		}
	}
	s := NewDBService(config.Config{}, scoped)
	if err = s.ensureClientEntrySchema(ctx); err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := scoped.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO v2_client_entry_user_policy(id,name,mode,sort,enabled,conditions,created_at,updated_at) VALUES(1,'protect','standard',10,1,'[]',1,1),(2,'内鬼假地址','standard',20,1,'[]',1,1),(3,'other','standard',30,1,'[]',1,1),(4,'AB parent','split',50,1,'[]',1,1),(5,'disabled','standard',40,0,'[]',1,1)`)
	exec(`INSERT INTO v2_client_entry_user_policy_split_group(id,policy_id,parent_id,name,path,entry_host,sort,global_sort,created_at,updated_at) VALUES(99,4,NULL,'parent','R','192.0.2.1',10,45,1,1),(2,4,99,'A','R.A','192.0.2.2',10,50,1,1),(6,4,99,'B','R.B','192.0.2.3',20,60,1,1)`)
	exec(`INSERT INTO v2_client_entry_user_policy_split_assignment(policy_id,user_id,group_id,created_at,updated_at)VALUES(4,100,2,1,1),(4,101,6,1,1)`)
	exec(`INSERT INTO v2_client_entry_user_policy_member(policy_id,server_type,server_id,sort,created_at,updated_at)VALUES(4,'vmess',14,10,1,1)`)
	exec(`INSERT INTO v2_client_entry_collection(id,name,entry_host,resolve_entry_host,version,created_at,updated_at)VALUES(1,'shared','192.0.2.7',0,1,1,1)`)
	exec(`INSERT INTO v2_client_entry_collection_member(collection_id,policy_id,split_group_id)VALUES(1,NULL,2)`)
	digest := func() string {
		t.Helper()
		var v string
		err := scoped.QueryRowContext(ctx, `SELECT md5((SELECT string_agg(to_jsonb(a)::text,'' ORDER BY user_id) FROM v2_client_entry_user_policy_split_assignment a)||(SELECT string_agg(to_jsonb(m)::text,'' ORDER BY id) FROM v2_client_entry_user_policy_member m)||(SELECT string_agg(to_jsonb(c)::text,'' ORDER BY id) FROM v2_client_entry_collection c)||(SELECT string_agg(to_jsonb(c)::text,'' ORDER BY collection_id) FROM v2_client_entry_collection_member c))`).Scan(&v)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	before := digest()
	page, err := s.ListClientEntryVisibleOrder(ctx, 1, 100)
	if err != nil || page.Total != 6 {
		t.Fatal(page, err)
	}
	page2, err := s.ListClientEntryVisibleOrder(ctx, 2, 2)
	if err != nil || page2.Revision != page.Revision || len(page2.Items) != 2 {
		t.Fatal(page2, err)
	}
	req := ClientEntryOrderMoveRequest{Items: []ClientEntryUserPolicySortItem{{Kind: "split_group", ID: 2}, {Kind: "split_group", ID: 6}}, After: ClientEntryUserPolicySortItem{Kind: "policy", ID: 2}, ExpectedRevision: page.Revision}
	moved, err := s.MoveClientEntryVisibleRows(ctx, req)
	if err != nil || !moved.Changed || moved.Total != 6 {
		t.Fatal(moved, err)
	}
	current, err := s.ListClientEntryVisibleOrder(ctx, 1, 100)
	want := []ClientEntryUserPolicySortItem{{Kind: "policy", ID: 1}, {Kind: "policy", ID: 2}, {Kind: "split_group", ID: 2}, {Kind: "split_group", ID: 6}, {Kind: "policy", ID: 3}, {Kind: "policy", ID: 5}}
	if err != nil || !reflect.DeepEqual(orderKeys(current.Items), want) || current.Revision != moved.Revision {
		t.Fatal(current, err)
	}
	if digest() != before {
		t.Fatal("move changed assignments, members or collections")
	}
	if _, err = s.MoveClientEntryVisibleRows(ctx, req); !errors.Is(err, ErrClientEntryOrderConflict) {
		t.Fatal("stale revision accepted", err)
	}
	req.ExpectedRevision = current.Revision
	noOp, err := s.MoveClientEntryVisibleRows(ctx, req)
	if err != nil || noOp.Changed || noOp.Revision != current.Revision {
		t.Fatal(noOp, err)
	}
	for _, item := range []ClientEntryUserPolicySortItem{{Kind: "policy", ID: 4}, {Kind: "split_group", ID: 99}} {
		req.Items = []ClientEntryUserPolicySortItem{item}
		if _, err = s.MoveClientEntryVisibleRows(ctx, req); !errors.Is(err, ErrClientEntryOrderConflict) {
			t.Fatal("hidden row accepted", err)
		}
	}
	unchanged, err := s.ListClientEntryVisibleOrder(ctx, 1, 100)
	if err != nil || unchanged.Revision != current.Revision {
		t.Fatal("rejected operations changed order", unchanged, err)
	}
	// Competing plans based on the same order cannot both commit.
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, id := range []int64{2, 6} {
		go func(id int64) {
			<-start
			_, err := s.MoveClientEntryVisibleRows(ctx, ClientEntryOrderMoveRequest{
				Items: []ClientEntryUserPolicySortItem{{Kind: "split_group", ID: id}},
				After: ClientEntryUserPolicySortItem{Kind: "policy", ID: 1}, ExpectedRevision: current.Revision,
			})
			results <- err
		}(id)
	}
	close(start)
	succeeded, conflicted := 0, 0
	for range 2 {
		err := <-results
		if err == nil {
			succeeded++
		} else if errors.Is(err, ErrClientEntryOrderConflict) {
			conflicted++
		} else {
			t.Fatal(err)
		}
	}
	if succeeded != 1 || conflicted != 1 || digest() != before {
		t.Fatal("concurrent stale plan committed or changed unrelated data", succeeded, conflicted)
	}
}
