package aiops

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func fullLeaseFixtureTurn(t *testing.T, f staffAIFixture, actor int64) (int64, int64) {
	t.Helper()
	ctx := context.Background()
	session, err := f.s.CreateSession(ctx, actor, CreateSessionRequest{Model: DefaultModels[0]})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(`UPDATE v2_ai_session SET access_mode='full',full_access_by=$2,status='running' WHERE id=$1`, session.ID, actor); err != nil {
		t.Fatal(err)
	}
	var turn int64
	now := time.Now().Unix()
	err = f.db.QueryRow(`INSERT INTO v2_ai_turn(session_id,actor_id,request_id,request_hash,model,status,access_mode,created_at,expires_at)VALUES($1,$2,$3,'fixture',$4,'running','full',$5,$6)RETURNING id`, session.ID, actor, fmt.Sprintf("lease-%d", time.Now().UnixNano()), DefaultModels[0], now-7200, now+30).Scan(&turn)
	if err != nil {
		t.Fatal(err)
	}
	return session.ID, turn
}

func TestFullAccessLeasePostgresActiveWorkerAndInvalidation(t *testing.T) {
	f := setupStaffAIFixture(t)
	ctx := context.Background()
	for _, actor := range []int64{f.admin, f.staff} {
		session, turn := fullLeaseFixtureTurn(t, f, actor)
		if err := f.s.renewFullAccessTurn(ctx, actor, session, turn); err != nil {
			t.Fatal("long-running valid worker was not renewed", err)
		}
		var expiry int64
		if err := f.db.QueryRow(`SELECT expires_at FROM v2_ai_turn WHERE id=$1`, turn).Scan(&expiry); err != nil || expiry < time.Now().Unix()+115 || expiry > time.Now().Unix()+120 {
			t.Fatal("incorrect renewable deadline", expiry, err)
		}
		for _, tt := range []struct {
			name, query string
			args        []any
		}{
			{"stopped", `UPDATE v2_ai_turn SET stop_requested=TRUE WHERE id=$1`, []any{turn}},
			{"expired", `UPDATE v2_ai_turn SET expires_at=$2 WHERE id=$1`, []any{turn, time.Now().Unix() - 1}},
			{"completed", `UPDATE v2_ai_turn SET status='completed' WHERE id=$1`, []any{turn}},
			{"standard turn", `UPDATE v2_ai_turn SET access_mode='standard' WHERE id=$1`, []any{turn}},
			{"revoked session", `UPDATE v2_ai_session SET access_mode='standard' WHERE id=$1`, []any{session}},
			{"different grant", `UPDATE v2_ai_session SET full_access_by=$2 WHERE id=$1`, []any{session, f.other}},
			{"banned", `UPDATE v2_user SET banned=1 WHERE id=$1`, []any{actor}},
			{"no role", `UPDATE v2_user SET is_admin=0,is_staff=0 WHERE id=$1`, []any{actor}},
			{"disabled AI", `UPDATE v2_ai_config SET enabled=FALSE WHERE id=1`, nil},
		} {
			t.Run(fmt.Sprintf("%d/%s", actor, tt.name), func(t *testing.T) {
				_, err := f.db.Exec(`UPDATE v2_ai_turn SET access_mode='full',status='running',stop_requested=FALSE,expires_at=$2 WHERE id=$1`, turn, time.Now().Unix()+30)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = f.db.Exec(`UPDATE v2_ai_session SET access_mode='full',full_access_by=$2 WHERE id=$1`, session, actor); err != nil {
					t.Fatal(err)
				}
				if _, err = f.db.Exec(`UPDATE v2_user SET banned=0,is_admin=CASE WHEN id=$2 THEN 1 ELSE 0 END,is_staff=CASE WHEN id=$2 THEN 0 ELSE 1 END WHERE id=$1`, actor, f.admin); err != nil {
					t.Fatal(err)
				}
				if _, err = f.db.Exec(`UPDATE v2_ai_config SET enabled=TRUE WHERE id=1`); err != nil {
					t.Fatal(err)
				}
				if _, err = f.db.Exec(tt.query, tt.args...); err != nil {
					t.Fatal(err)
				}
				var before int64
				if err = f.db.QueryRow(`SELECT expires_at FROM v2_ai_turn WHERE id=$1`, turn).Scan(&before); err != nil {
					t.Fatal(err)
				}
				if err = f.s.renewFullAccessTurn(ctx, actor, session, turn); !errors.Is(err, ErrForbidden) {
					t.Fatal("invalid worker lease extended", err)
				}
				var after int64
				if err = f.db.QueryRow(`SELECT expires_at FROM v2_ai_turn WHERE id=$1`, turn).Scan(&after); err != nil || before != after {
					t.Fatal("refused renewal changed deadline", before, after, err)
				}
			})
		}
		if _, err := f.db.Exec(`UPDATE v2_ai_config SET enabled=TRUE WHERE id=1`); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFullAccessLeasePostgresWatchKeepsIdleWorkerAndStops(t *testing.T) {
	f := setupStaffAIFixture(t)
	session, turn := fullLeaseFixtureTurn(t, f, f.admin)
	initial := time.Now().Unix() + 3
	if _, err := f.db.Exec(`UPDATE v2_ai_turn SET expires_at=$2 WHERE id=$1`, turn, initial); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan struct{})
	go func() { f.s.watchTurn(ctx, cancel, f.admin, session, turn, true); close(finished) }()
	defer func() { cancel(); <-finished }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var expires int64
		if err := f.db.QueryRow(`SELECT expires_at FROM v2_ai_turn WHERE id=$1`, turn).Scan(&expires); err != nil {
			t.Fatal(err)
		}
		if expires > initial+60 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("background watcher did not renew stalled model/SSH turn")
		}
		select {
		case <-ctx.Done():
			t.Fatal("watcher cancelled live worker")
		case <-time.After(20 * time.Millisecond):
		}
	}
	// Advance well beyond the original lease while the execution is idle. The
	// model/SSH path does not need to call back into the scheduler to stay alive.
	for time.Now().Unix() <= initial {
		select {
		case <-ctx.Done():
			t.Fatal("original expiry stopped renewed worker")
		case <-time.After(20 * time.Millisecond):
		}
	}
	if err := f.s.requireFullAccess(context.Background(), f.admin, session, turn); err != nil {
		t.Fatal("idle execution lost renewed authorization", err)
	}
	if _, err := f.db.Exec(`UPDATE v2_ai_turn SET stop_requested=TRUE WHERE id=$1`, turn); err != nil {
		t.Fatal(err)
	}
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("stop ignored by watcher")
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("stop did not cancel parent execution")
	}
}

func TestFullAccessLeasePostgresStandardWatcherDoesNotRenew(t *testing.T) {
	f := setupStaffAIFixture(t)
	session, turn := fullLeaseFixtureTurn(t, f, f.admin)
	expires := time.Now().Unix() + 2
	if _, err := f.db.Exec(`UPDATE v2_ai_turn SET access_mode='standard',expires_at=$2 WHERE id=$1`, turn, expires); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan struct{})
	go func() { f.s.watchTurn(ctx, cancel, f.admin, session, turn, false); close(finished) }()
	select {
	case <-finished:
	case <-time.After(4 * time.Second):
		t.Fatal("standard mode lost existing expiry")
	}
	var after int64
	if err := f.db.QueryRow(`SELECT expires_at FROM v2_ai_turn WHERE id=$1`, turn).Scan(&after); err != nil || after != expires {
		t.Fatal("standard mode lease renewed", after, err)
	}
}

func TestFullAccessLeasePostgresDoesNotRenewAfterBusyLeaseExpires(t *testing.T) {
	f := setupStaffAIFixture(t)
	session, turn := fullLeaseFixtureTurn(t, f, f.admin)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	expires := time.Now().Unix() + 2
	if _, err := f.db.ExecContext(ctx, `UPDATE v2_ai_turn SET expires_at=$2 WHERE id=$1`, turn, expires); err != nil {
		t.Fatal(err)
	}
	blocker, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback()
	var id int64
	if err = blocker.QueryRowContext(ctx, `SELECT id FROM v2_ai_turn WHERE id=$1 FOR SHARE`, turn).Scan(&id); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if err = f.s.renewFullAccessTurn(ctx, f.admin, session, turn); !errors.Is(err, ErrBusy) || time.Since(started) > time.Second {
		t.Fatal("renewal did not return explicit lock competition promptly", err)
	}
	workerCtx, stopWorker := context.WithCancel(ctx)
	defer stopWorker()
	finished := make(chan struct{})
	go func() { f.s.watchTurn(workerCtx, stopWorker, f.admin, session, turn, true); close(finished) }()
	select {
	case <-finished:
		if time.Now().Unix() < expires {
			t.Fatal("live busy lease stopped before expiry")
		}
	case <-ctx.Done():
		t.Fatal("lock contention prevented expiry check", ctx.Err())
	}
	if err = blocker.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err = f.s.renewFullAccessTurn(ctx, f.admin, session, turn); !errors.Is(err, ErrForbidden) {
		t.Fatal("expired turn revived after lock released", err)
	}
	var after int64
	if err = f.db.QueryRowContext(ctx, `SELECT expires_at FROM v2_ai_turn WHERE id=$1`, turn).Scan(&after); err != nil || after != expires {
		t.Fatal("expired deadline changed", after, err)
	}
}

func TestFullAccessLeasePostgresLongBusinessShareLockDoesNotStopWorker(t *testing.T) {
	f := setupStaffAIFixture(t)
	session, turn := fullLeaseFixtureTurn(t, f, f.admin)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	blocker, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback()
	var id int64
	if err = blocker.QueryRowContext(ctx, `SELECT id FROM v2_ai_turn WHERE id=$1 FOR SHARE`, turn).Scan(&id); err != nil {
		t.Fatal(err)
	}
	workerCtx, stopWorker := context.WithCancel(ctx)
	defer stopWorker()
	finished := make(chan struct{})
	go func() { f.s.watchTurn(workerCtx, stopWorker, f.admin, session, turn, true); close(finished) }()
	defer func() { stopWorker(); <-finished }()
	select {
	case <-finished:
		t.Fatal("business SHARE lock was mistaken for a lost worker")
	case <-time.After(4500 * time.Millisecond):
	}
	if err = blocker.Rollback(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var expires int64
		if err = f.db.QueryRowContext(ctx, `SELECT expires_at FROM v2_ai_turn WHERE id=$1`, turn).Scan(&expires); err != nil {
			t.Fatal(err)
		}
		if expires > time.Now().Unix()+100 {
			return
		}
		select {
		case <-finished:
			t.Fatal("worker stopped after business lock released")
		case <-time.After(20 * time.Millisecond):
		}
	}
	t.Fatal("worker did not retry renewal after SHARE lock released")
}

func TestFullAccessLeasePostgresWatcherRenewsRepeatedly(t *testing.T) {
	f := setupStaffAIFixture(t)
	session, turn := fullLeaseFixtureTurn(t, f, f.admin)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan struct{})
	go func() { f.s.watchTurn(ctx, cancel, f.admin, session, turn, true); close(finished) }()
	defer func() { cancel(); <-finished }()
	var first int64
	deadline := time.Now().Add(fullAccessRenewInterval + 5*time.Second)
	for time.Now().Before(deadline) {
		var expires int64
		if err := f.db.QueryRow(`SELECT expires_at FROM v2_ai_turn WHERE id=$1`, turn).Scan(&expires); err != nil {
			t.Fatal(err)
		}
		if first == 0 && expires > time.Now().Unix()+100 {
			first = expires
		}
		if first > 0 && expires >= first+int64(fullAccessRenewInterval/time.Second) {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("idle worker cancelled between renewals")
		case <-time.After(100 * time.Millisecond):
		}
	}
	t.Fatal("worker only renewed once and would expire during a long model/SSH request")
}
