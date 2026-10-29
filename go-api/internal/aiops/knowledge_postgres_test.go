package aiops

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func setupKnowledgeFixture(t *testing.T) staffAIFixture {
	f := setupStaffAIFixture(t)
	t.Cleanup(func() { f.db.Exec(`DELETE FROM v2_ai_knowledge`) })
	return f
}
func knowledgeRunningTurn(t *testing.T, f staffAIFixture, actor int64) (int64, int64) {
	t.Helper()
	s, err := f.s.CreateSession(context.Background(), actor, CreateSessionRequest{Model: DefaultModels[0]})
	if err != nil {
		t.Fatal(err)
	}
	var turn int64
	now := time.Now().Unix()
	err = f.db.QueryRow(`INSERT INTO v2_ai_turn(session_id,actor_id,request_id,request_hash,model,status,created_at,expires_at)VALUES($1,$2,$3,'fixture',$4,'running',$5,$6) RETURNING id`, s.ID, actor, fmt.Sprintf("knowledge-%d", time.Now().UnixNano()), DefaultModels[0], now, now+3600).Scan(&turn)
	if err != nil {
		t.Fatal(err)
	}
	return s.ID, turn
}

func TestKnowledgePostgresCRUDVisibilityAndCAS(t *testing.T) {
	f := setupKnowledgeFixture(t)
	ctx := context.Background()
	req := knowledgeFixtureRequest()
	active, err := f.s.SaveKnowledge(ctx, f.admin, req)
	if err != nil || active.Version != 1 || active.SourceKind != "manual" || active.SourceSessionID != 0 {
		t.Fatal(active, err)
	}
	req.Status, req.Pinned = "active", true
	draft, err := f.s.SaveKnowledge(ctx, f.staff, req)
	if err != nil || draft.Status != "draft" || draft.Pinned {
		t.Fatal("staff published", draft, err)
	}
	other, err := f.s.SaveKnowledge(ctx, f.other, req)
	if err != nil {
		t.Fatal(err)
	}
	req.Status = "disabled"
	disabled, err := f.s.SaveKnowledge(ctx, f.admin, req)
	if err != nil {
		t.Fatal(err)
	}
	for _, actor := range []int64{f.staff, f.other} {
		list, e := f.s.ListKnowledge(ctx, actor, KnowledgeListRequest{})
		if e != nil || list.Total != 2 {
			t.Fatal("staff list scope", list, e)
		}
		for _, k := range list.Data {
			if k.Content != "" || k.Status == "disabled" || (k.Status == "draft" && k.CreatedBy != actor) {
				t.Fatal("list leaks", k)
			}
		}
		if _, e = f.s.GetKnowledge(ctx, actor, active.ID); e != nil {
			t.Fatal(e)
		}
		if _, e = f.s.GetKnowledge(ctx, actor, disabled.ID); !errors.Is(e, ErrNotFound) {
			t.Fatal("disabled visible", e)
		}
		list, e = f.s.ListKnowledge(ctx, actor, KnowledgeListRequest{Status: "disabled"})
		if e != nil || list.Total != 0 {
			t.Fatal("disabled count leaks", list, e)
		}
	}
	if _, err = f.s.GetKnowledge(ctx, f.staff, other.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("foreign draft visible", err)
	}
	edit := knowledgeFixtureRequest()
	edit.ID, edit.ExpectedVersion = draft.ID, &draft.Version
	if _, err = f.s.SaveKnowledge(ctx, f.staff, edit); !errors.Is(err, ErrForbidden) {
		t.Fatal("staff edited own draft", err)
	}
	if err = f.s.DeleteKnowledge(ctx, f.staff, draft.ID, draft.Version); !errors.Is(err, ErrForbidden) {
		t.Fatal("staff deleted", err)
	}
	if _, err = f.s.ListKnowledge(ctx, f.user, KnowledgeListRequest{}); !errors.Is(err, ErrForbidden) {
		t.Fatal("user read knowledge", err)
	}
	edit.Status, edit.Pinned = "active", true
	published, err := f.s.SaveKnowledge(ctx, f.admin, edit)
	if err != nil || published.Version != 2 || published.CreatedBy != f.staff || published.UpdatedBy != f.admin {
		t.Fatal("publish provenance", published, err)
	}
	if _, err = f.s.SaveKnowledge(ctx, f.admin, edit); !errors.Is(err, ErrConflict) {
		t.Fatal("stale update accepted", err)
	}
	if err = f.s.DeleteKnowledge(ctx, f.admin, published.ID, 1); !errors.Is(err, ErrConflict) {
		t.Fatal("stale delete accepted", err)
	}
	list, err := f.s.ListKnowledge(ctx, f.admin, KnowledgeListRequest{Query: "192.0.2.4", Category: "architecture", PageSize: 1})
	if err != nil || list.Total != 4 || len(list.Data) != 1 || !list.Data[0].Pinned {
		t.Fatal("filter/order/page", list, err)
	}
	if err = f.s.DeleteKnowledge(ctx, f.admin, published.ID, published.Version); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.GetKnowledge(ctx, f.admin, published.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("delete failed", err)
	}
}

func TestKnowledgePostgresAISourceDedupeAndLifecycle(t *testing.T) {
	f := setupKnowledgeFixture(t)
	ctx := context.Background()
	sid, tid := knowledgeRunningTurn(t, f, f.admin)
	req := knowledgeFixtureRequest()
	req.Status = "draft"
	entry, err := f.s.SaveKnowledgeFromAI(ctx, f.admin, sid, tid, req, "已验证的架构")
	if err != nil || entry.Status != "active" || entry.SourceKind != "ai" || entry.SourceSessionID != sid || entry.SourceTurnID != tid {
		t.Fatal(entry, err)
	}
	var wg sync.WaitGroup
	results := make(chan KnowledgeEntry, 8)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			k, e := f.s.SaveKnowledgeFromAI(ctx, f.admin, sid, tid, req, "重新归纳来源")
			results <- k
			errs <- e
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	for k := range results {
		if k.ID != entry.ID || k.Version != 1 {
			t.Fatal("AI repeated save duplicated", k)
		}
	}
	sstaff, tstaff := knowledgeRunningTurn(t, f, f.staff)
	draft, err := f.s.SaveKnowledgeFromAI(ctx, f.staff, sstaff, tstaff, req, "员工建议")
	if err != nil || draft.Status != "draft" {
		t.Fatal(draft, err)
	}
	req.ID, req.ExpectedVersion = entry.ID, &entry.Version
	if _, err = f.s.SaveKnowledgeFromAI(ctx, f.staff, sstaff, tstaff, req, ""); !errors.Is(err, ErrForbidden) {
		t.Fatal("staff AI changed active", err)
	}
	req.Content = "经核对更新入口架构"
	updated, err := f.s.SaveKnowledgeFromAI(ctx, f.admin, sid, tid, req, "更新核对")
	if err != nil || updated.Version != 2 {
		t.Fatal(updated, err)
	}
	if _, err = f.s.SaveKnowledgeFromAI(ctx, f.admin, sid, tid, req, ""); !errors.Is(err, ErrConflict) {
		t.Fatal("AI stale write", err)
	}
	req = knowledgeFixtureRequest()
	if _, err = f.s.SaveKnowledgeFromAI(ctx, f.admin, sid, tstaff, req, ""); !errors.Is(err, ErrForbidden) {
		t.Fatal("foreign turn accepted", err)
	}
	if _, err = f.db.Exec(`UPDATE v2_ai_turn SET stop_requested=TRUE WHERE id=$1`, tid); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.SaveKnowledgeFromAI(ctx, f.admin, sid, tid, req, ""); !errors.Is(err, ErrForbidden) {
		t.Fatal("stopped turn wrote", err)
	}
	if _, err = f.db.Exec(`UPDATE v2_ai_turn SET status='completed' WHERE id IN($1,$2)`, tid, tstaff); err != nil {
		t.Fatal(err)
	}
	if err = f.s.DeleteSession(ctx, f.admin, sid); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.GetKnowledge(ctx, f.admin, entry.ID); err != nil {
		t.Fatal("session deletion lost knowledge", err)
	}
	if err = f.s.ClearSessions(ctx, f.admin); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.GetKnowledge(ctx, f.staff, entry.ID); err != nil {
		t.Fatal("session clear lost active knowledge", err)
	}
	if err = f.s.ResetData(ctx, f.admin); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = f.db.QueryRow(`SELECT COUNT(*) FROM v2_ai_knowledge`).Scan(&count); err != nil || count != 0 {
		t.Fatal("reset left knowledge", count, err)
	}
}

func TestKnowledgePostgresConcurrentCapAndVersions(t *testing.T) {
	f := setupKnowledgeFixture(t)
	ctx := context.Background()
	_, err := f.db.Exec(`INSERT INTO v2_ai_knowledge(title,content,category,status,source_kind,created_by,updated_by,created_at,updated_at) SELECT 'fixture-'||i,'fixture','other','active','manual',$1,$1,1,1 FROM generate_series(1,499)i`, f.admin)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	entries := make(chan KnowledgeEntry, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			k, e := f.s.SaveKnowledge(ctx, f.admin, knowledgeFixtureRequest())
			entries <- k
			errs <- e
		}()
	}
	wg.Wait()
	close(errs)
	close(entries)
	var success, busy int
	var entry KnowledgeEntry
	for e := range errs {
		if e == nil {
			success++
		} else if errors.Is(e, ErrBusy) {
			busy++
		} else {
			t.Fatal(e)
		}
	}
	for k := range entries {
		if k.ID > 0 {
			entry = k
		}
	}
	if success != 1 || busy != 7 {
		t.Fatal("concurrent cap exceeded", success, busy)
	}
	req := knowledgeFixtureRequest()
	req.ID, req.ExpectedVersion = entry.ID, &entry.Version
	errs = make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, e := f.s.SaveKnowledge(ctx, f.admin, req); errs <- e }()
	}
	wg.Wait()
	close(errs)
	success = 0
	var conflicts int
	for e := range errs {
		if e == nil {
			success++
		} else if errors.Is(e, ErrConflict) {
			conflicts++
		} else {
			t.Fatal(e)
		}
	}
	if success != 1 || conflicts != 7 {
		t.Fatal("CAS accepted duplicate updates", success, conflicts)
	}
}

func TestKnowledgePostgresRechecksRoleInsideWrite(t *testing.T) {
	f := setupKnowledgeFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	entry, err := f.s.SaveKnowledge(ctx, f.admin, knowledgeFixtureRequest())
	if err != nil {
		t.Fatal(err)
	}
	blocker, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback()
	if _, err = blocker.ExecContext(ctx, `SELECT pg_advisory_xact_lock(2026102902)`); err != nil {
		t.Fatal(err)
	}
	req := knowledgeFixtureRequest()
	req.ID, req.ExpectedVersion = entry.ID, &entry.Version
	result := make(chan error, 1)
	go func() { _, e := f.s.SaveKnowledge(ctx, f.admin, req); result <- e }()
	for {
		var blocked bool
		if err = f.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query='SELECT pg_advisory_xact_lock_shared(2026102902)')`).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case e := <-result:
			t.Fatal("write never blocked", e)
		case <-time.After(10 * time.Millisecond):
		}
	}
	if _, err = f.db.ExecContext(ctx, `UPDATE v2_user SET is_admin=0,is_staff=1 WHERE id=$1`, f.admin); err != nil {
		t.Fatal(err)
	}
	defer f.db.Exec(`UPDATE v2_user SET is_admin=1 WHERE id=$1`, f.admin)
	if err = blocker.Rollback(); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-result:
		if !errors.Is(err, ErrForbidden) {
			t.Fatal("revoked admin updated", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
