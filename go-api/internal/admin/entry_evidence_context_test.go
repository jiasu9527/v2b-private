package admin

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"forest/go-api/internal/config"
	"forest/go-api/internal/entryevidence"
)

func TestEntryEvidenceContextPostgres(t *testing.T) {
	dsn := os.Getenv("FOREST_ENTRY_EVIDENCE_CONTEXT_DSN")
	if dsn == "" {
		t.Skip("requires disposable full-schema context DB")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	s := NewDBService(config.Config{}, db)
	marker := fmt.Sprint(time.Now().UnixNano())
	now := time.Now().Unix()
	ids := []int64{}
	for i := 0; i < 8; i++ {
		var id int64
		if err = db.QueryRow(`INSERT INTO v2_user(email,password,uuid,token,is_admin,is_staff,banned,created_at,updated_at) VALUES($1,'x','x',$2,1,0,0,1,1) RETURNING id`, fmt.Sprintf("context-%s-%d@example.test", marker, i), marker+fmt.Sprint(i)).Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	t.Cleanup(func() {
		db.Exec(`DELETE FROM v2_entry_delivery_request WHERE id LIKE $1`, "context-"+marker+"-%")
		for _, id := range ids {
			db.Exec(`DELETE FROM v2_user WHERE id=$1`, id)
		}
	})
	if err = s.prepareEntryEvidence(ctx, ids[0], false); err != nil {
		t.Fatal(err)
	}
	store := entryevidence.NewStore(db)
	target := entryevidence.Node{ServerType: "vmess", ServerID: 1, PolicyID: 818181, GroupID: 818182, DeliveredHost: "192.0.2.45", Port: "54101", Source: "split"}
	add := func(label string, user, at int64, complete bool, nodes ...entryevidence.Node) {
		t.Helper()
		r := entryevidence.Request{ID: "context-" + marker + "-" + label, UserID: user, EmailAtEvent: fmt.Sprintf("%d@example.test", user), UA: "Forest/" + marker, RequestedAt: at, CompletedAt: at, Transport: "clash", Status: 200, ResponseComplete: complete, Coverage: "complete"}
		if err := store.Record(ctx, r, nodes); err != nil {
			t.Fatal(err)
		}
	}
	add("window", ids[1], now-10, true, target)
	add("window-history", ids[1], now-100, true, target)
	add("history", ids[2], now-100, true, target)
	add("partial", ids[3], now-5, false, target)
	unrelated := target
	unrelated.DeliveredHost = "192.0.2.99"
	add("unrelated-failure", ids[4], now-5, false, unrelated)
	add("no-snapshot", ids[5], now-5, false)
	otherPolicy := target
	otherPolicy.PolicyID = 99
	otherPolicy.GroupID = 99
	add("cross-node", ids[6], now-5, true, unrelated, otherPolicy)
	add("exclusive-end", ids[7], now, true, target)
	add("orphan", 2000000000, now-5, true, target)
	q := EntryEvidenceQueryRequest{Kind: "split_group", TargetID: target.GroupID, Host: target.DeliveredHost, Port: 54101, From: now - 60, To: now, UA: marker, PageSize: 100}
	page, err := s.EntryEvidenceContext(ctx, ids[0], q)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 4 || page.Counts["window_delivered"] != 2 || page.Counts["historical_exposure"] != 1 || page.Counts["request_only"] != 1 || page.Counts["orphaned"] != 1 {
		t.Fatalf("incorrect classification: %+v", page)
	}
	if !strings.Contains(strings.Join(page.Warnings, " "), "1 条没有节点快照") {
		t.Fatal("missing unlinked request warning", page.Warnings)
	}
	q.Current = 2
	q.PageSize = 2
	paged, err := s.EntryEvidenceContext(ctx, ids[0], q)
	if err != nil || paged.Total != 4 || len(paged.Items) != 2 {
		t.Fatal(paged, err)
	}
	q.Account = fmt.Sprint(ids[4])
	q.Current = 1
	empty, err := s.EntryEvidenceContext(ctx, ids[0], q)
	if err != nil || empty.Total != 0 {
		t.Fatal("unrelated failed request linked to target", empty, err)
	}
}
