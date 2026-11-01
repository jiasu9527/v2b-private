package aiops

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func retryFixtureUserAction(t *testing.T, f staffAIFixture, session int64, key string) int64 {
	t.Helper()
	out, err := f.s.callTool(context.Background(), f.staff, session, ToolCall{ID: key, Function: ToolFunction{Name: "propose_user_update", Arguments: fmt.Sprintf(`{"user_id":%d,"changes":{"balance":654},"reason":"fixture retry"}`, f.user)}})
	if err != nil || !out.Pending || out.ActionID == 0 {
		t.Fatal(out, err)
	}
	return out.ActionID
}

func TestOperationRetryPostgresTransactionRollback(t *testing.T) {
	for _, code := range []string{"40001", "40P01"} {
		t.Run(code, func(t *testing.T) {
			f := setupStaffAIFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			session, err := f.s.CreateSession(ctx, f.staff, CreateSessionRequest{Model: DefaultModels[0]})
			if err != nil {
				t.Fatal(err)
			}
			action := retryFixtureUserAction(t, f, session.ID, "retry-transaction")
			name := fmt.Sprintf("ai_retry_%d", f.user)
			// Sequence increments survive rollback, while the ordinary audit row
			// written before the injected failure must be rolled back.
			for _, q := range []string{
				`CREATE SEQUENCE ` + name + `_seq`,
				`CREATE TABLE ` + name + `_audit (id BIGINT)`,
				fmt.Sprintf(`CREATE FUNCTION %s_fn() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.id=%d THEN INSERT INTO %s_audit VALUES(NEW.id); IF nextval('%s_seq')=1 THEN RAISE EXCEPTION 'fixture rollback' USING ERRCODE='%s'; END IF; END IF; RETURN NEW; END $$`, name, f.user, name, name, code),
				fmt.Sprintf(`CREATE TRIGGER %s_trigger AFTER UPDATE ON v2_user FOR EACH ROW EXECUTE FUNCTION %s_fn()`, name, name),
			} {
				if _, err = f.db.ExecContext(ctx, q); err != nil {
					t.Fatal(err)
				}
			}
			t.Cleanup(func() {
				f.db.Exec(`DROP TRIGGER IF EXISTS ` + name + `_trigger ON v2_user`)
				f.db.Exec(`DROP FUNCTION IF EXISTS ` + name + `_fn()`)
				f.db.Exec(`DROP TABLE IF EXISTS ` + name + `_audit`)
				f.db.Exec(`DROP SEQUENCE IF EXISTS ` + name + `_seq`)
			})
			done, err := f.s.ApproveAction(ctx, f.staff, session.ID, action)
			if err != nil || done.ID != action || done.Status != "succeeded" {
				t.Fatal(done, err)
			}
			var balance, attempts, committed, actions int
			for q, dest := range map[string]*int{
				fmt.Sprintf(`SELECT balance FROM v2_user WHERE id=%d`, f.user):                   &balance,
				`SELECT last_value FROM ` + name + `_seq`:                                        &attempts,
				`SELECT COUNT(*) FROM ` + name + `_audit`:                                        &committed,
				fmt.Sprintf(`SELECT COUNT(*) FROM v2_ai_action WHERE session_id=%d`, session.ID): &actions,
			} {
				if err = f.db.QueryRowContext(ctx, q).Scan(dest); err != nil {
					t.Fatal(err)
				}
			}
			if balance != 654 || attempts != 2 || committed != 1 || actions != 1 {
				t.Fatal(balance, attempts, committed, actions)
			}
			if _, err = f.s.ApproveAction(ctx, f.staff, session.ID, action); !errors.Is(err, ErrConflict) {
				t.Fatal("successful action replayed", err)
			}
		})
	}
}

func TestOperationRetryPostgresLockThenRevalidate(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(fmt.Sprintf("stale_%v", stale), func(t *testing.T) {
			f := setupStaffAIFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			session, err := f.s.CreateSession(ctx, f.staff, CreateSessionRequest{Model: DefaultModels[0]})
			if err != nil {
				t.Fatal(err)
			}
			action := retryFixtureUserAction(t, f, session.ID, "retry-session-lock")
			tx, err := f.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err = tx.ExecContext(ctx, `SELECT id FROM v2_ai_session WHERE id=$1 FOR UPDATE`, session.ID); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { _, e := f.s.ApproveAction(ctx, f.staff, session.ID, action); done <- e }()
			time.Sleep(250 * time.Millisecond)
			if stale {
				if _, err = tx.ExecContext(ctx, `UPDATE v2_user SET balance=123 WHERE id=$1`, f.user); err != nil {
					t.Fatal(err)
				}
			}
			if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}
			err = <-done
			if stale && !errors.Is(err, ErrConflict) || !stale && err != nil {
				t.Fatal(stale, err)
			}
			var balance int
			if err = f.db.QueryRowContext(ctx, `SELECT balance FROM v2_user WHERE id=$1`, f.user).Scan(&balance); err != nil {
				t.Fatal(err)
			}
			want := 654
			if stale {
				want = 123
			}
			if balance != want {
				t.Fatal("retry bypassed snapshot", balance, want)
			}
		})
	}
}
