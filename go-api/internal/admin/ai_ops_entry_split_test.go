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

func TestAIEntrySplitPostgres(t *testing.T) {
	dsn := os.Getenv("FOREST_AIOPS_TEST_DSN")
	if dsn == "" {
		t.Skip("dedicated disposable FOREST_AIOPS_TEST_DSN required")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	s := NewDBService(config.Config{}, db)
	if err := s.ensureClientEntrySchema(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.ensureDNSFailoverSchema(ctx); err != nil {
		t.Fatal(err)
	}
	begin := func() *sql.Tx {
		t.Helper()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		return tx
	}
	t.Run("direct conversion preserves UA and pool shortage rolls back", func(t *testing.T) {
		f := newSplitPoolFixture(t, ctx, db)
		users := f.users(4)
		policy, conditions := f.policy(users[0], users[3], "before.example.test")
		tx := begin()
		defer tx.Rollback()
		rule, err := LoadAIEntryRule(ctx, tx, "policy", policy)
		if err != nil {
			t.Fatal(err)
		}
		selection := AIEntryDirectSplit{Kind: "policy", TargetID: policy, Revision: rule.Revision, UseBackupIPPool: true}
		snapshot, err := CaptureAIEntryDirectSplit(ctx, tx, selection)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = ApplyAIEntryDirectSplitTx(ctx, tx, snapshot); !errors.Is(err, ErrAIEntrySplitInvalid) || !strings.Contains(err.Error(), "不足 2 个") {
			t.Fatal("missing pool shortage", err)
		}
		tx.Rollback()
		f.assertUnconverted(policy, conditions)
		f.ip("192.0.2.171", 1, true)
		f.ip("192.0.2.172", 1, true)
		tx = begin()
		defer tx.Rollback()
		snapshot, err = CaptureAIEntryDirectSplit(ctx, tx, selection)
		if err != nil {
			t.Fatal(err)
		}
		result, err := ApplyAIEntryDirectSplitTx(ctx, tx, snapshot)
		if err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if result.SnapshotUserCount != 4 || len(result.SplitGroups) != 2 {
			t.Fatal("conversion did not split", result)
		}
		var actual string
		if err = db.QueryRow(`SELECT conditions FROM v2_client_entry_user_policy WHERE id=$1`, policy).Scan(&actual); err != nil || actual != conditions {
			t.Fatal("UA changed", actual, err)
		}
	})
	t.Run("manual disabled flag durable worker and switch are independent", func(t *testing.T) {
		f := newSplitPoolFixture(t, ctx, db)
		users := f.users(4)
		policy, _ := f.policy(users[0], users[3], "before.example.test")
		converted, err := s.ConvertClientEntryUserPolicyToSplit(ctx, ClientEntryUserPolicySplitConvertRequest{PolicyID: policy, EntryHostA: "192.0.2.181", EntryHostB: "192.0.2.182"})
		if err != nil {
			t.Fatal(err)
		}
		leaf := converted.SplitGroups[0].ID
		now := time.Now().Unix()
		monitor := f.insert("v2_client_entry_monitor", `INSERT INTO v2_client_entry_monitor(policy_id,enabled,check_interval_sec,tcp_timeout_ms,failure_threshold,success_threshold,created_at,updated_at)VALUES($1,1,30,3000,3,2,$2,$2)RETURNING id`, policy, now)
		target := f.insert("v2_client_entry_monitor_target", `INSERT INTO v2_client_entry_monitor_target(monitor_id,source_key,name,host,port,sort,auto_split_enabled,created_at,updated_at)VALUES($1,$2,'fixture','192.0.2.181',54101,10,0,$3,$3)RETURNING id`, monitor, fmt.Sprintf("policy:%d:split-group:%d", policy, leaf), now)
		f.exec(`INSERT INTO v2_client_entry_monitor_state(target_id,probe_id,last_success,consecutive_failure,last_reported_at,updated_at)VALUES($1,$2,0,3,$3,$3)`, target, f.probeID, now)
		tx := begin()
		snapshot, err := CaptureAIEntrySplitTarget(ctx, tx, target, false)
		if err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		operation, err := RequestAIEntryManualSplitTx(ctx, tx, snapshot)
		if err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Exec(`DELETE FROM v2_client_entry_auto_split_operation WHERE policy_id=$1`, policy) })
		var enabled int
		if err = db.QueryRow(`SELECT auto_split_enabled FROM v2_client_entry_monitor_target WHERE id=$1`, target).Scan(&enabled); err != nil || enabled != 0 {
			t.Fatal("manual request enabled automation", enabled, err)
		}
		tx = begin()
		snapshot, err = CaptureAIEntrySplitTarget(ctx, tx, target, false)
		if err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		replayed, err := RequestAIEntryManualSplitTx(ctx, tx, snapshot)
		tx.Rollback()
		if err != nil || replayed != operation {
			t.Fatal("duplicate durable operation", replayed, err)
		}
		if _, _, err = s.processNextClientEntryAutoSplit(ctx); err != nil {
			t.Fatal(err)
		}
		status, err := s.GetAIEntrySplitOperation(ctx, operation)
		if err != nil || status.Status != "pending" || !strings.Contains(status.LastError, "不足 2 个") {
			t.Fatal("missing durable pool wait reason", status, err)
		}
		f.ip("192.0.2.183", 1, true)
		f.ip("192.0.2.184", 1, true)
		f.exec(`UPDATE v2_client_entry_auto_split_operation SET next_attempt_at=0 WHERE id=$1`, operation)
		processed, changed, err := s.processNextClientEntryAutoSplit(ctx)
		if err != nil || !processed || !changed {
			t.Fatal("manual disabled split not applied", processed, changed, err)
		}
		status, err = s.GetAIEntrySplitOperation(ctx, operation)
		if err != nil || status.Status != "succeeded" || status.ChildGroupAID == 0 || status.ChildGroupBID == 0 || !status.Manual {
			t.Fatal("durable success missing", status, err)
		}
		var childrenEnabled int
		if err = db.QueryRow(`SELECT COALESCE(SUM(auto_split_enabled),0) FROM v2_client_entry_monitor_target WHERE monitor_id=$1`, monitor).Scan(&childrenEnabled); err != nil || childrenEnabled != 0 {
			t.Fatal("manual split enabled children", childrenEnabled, err)
		}
	})
	t.Run("automatic flag resets only chosen target and rejects stale revision", func(t *testing.T) {
		f := newSplitPoolFixture(t, ctx, db)
		users := f.users(6)
		policy, _ := f.policy(users[0], users[5], "before.example.test")
		converted, err := s.ConvertClientEntryUserPolicyToSplit(ctx, ClientEntryUserPolicySplitConvertRequest{PolicyID: policy, EntryHostA: "192.0.2.191", EntryHostB: "192.0.2.192"})
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now().Unix()
		monitor := f.insert("v2_client_entry_monitor", `INSERT INTO v2_client_entry_monitor(policy_id,enabled,created_at,updated_at)VALUES($1,1,$2,$2)RETURNING id`, policy, now)
		targets := []int64{}
		for _, leaf := range converted.SplitGroups {
			target := f.insert("v2_client_entry_monitor_target", `INSERT INTO v2_client_entry_monitor_target(monitor_id,source_key,name,host,port,sort,auto_split_enabled,created_at,updated_at)VALUES($1,$2,'fixture',$3,54101,10,0,$4,$4)RETURNING id`, monitor, fmt.Sprintf("policy:%d:split-group:%d", policy, leaf.ID), leaf.EntryHost, now)
			targets = append(targets, target)
			f.exec(`INSERT INTO v2_client_entry_monitor_state(target_id,probe_id,last_success,consecutive_failure,last_reported_at,updated_at)VALUES($1,$2,0,3,$3,$3)`, target, f.probeID, now)
		}
		tx := begin()
		snapshot, err := CaptureAIEntrySplitTarget(ctx, tx, targets[0], true)
		if err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		oldRevision := snapshot.Revision
		if err = ApplyAIEntryAutoSplitTx(ctx, tx, snapshot, true); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
		var ownStates, otherStates int
		if err = db.QueryRow(`SELECT (SELECT COUNT(*) FROM v2_client_entry_monitor_state WHERE target_id=$1),(SELECT COUNT(*) FROM v2_client_entry_monitor_state WHERE target_id=$2)`, targets[0], targets[1]).Scan(&ownStates, &otherStates); err != nil || ownStates != 0 || otherStates != 1 {
			t.Fatal("toggle touched other target", ownStates, otherStates, err)
		}
		newSnapshot, err := s.ReadAIEntrySplitTarget(ctx, targets[0])
		if err != nil || newSnapshot.Revision == oldRevision || !newSnapshot.AutoSplitEnabled {
			t.Fatal("revision did not change", newSnapshot, err)
		}
	})
	t.Run("direct split rejects too few users and changed assigned set", func(t *testing.T) {
		f := newSplitPoolFixture(t, ctx, db)
		users := f.users(6)
		policy, _ := f.policy(users[0], users[5], "before.example.test")
		converted, err := s.ConvertClientEntryUserPolicyToSplit(ctx, ClientEntryUserPolicySplitConvertRequest{PolicyID: policy, EntryHostA: "192.0.2.201", EntryHostB: "192.0.2.202"})
		if err != nil {
			t.Fatal(err)
		}
		leaf := converted.SplitGroups[0].ID
		tx := begin()
		defer tx.Rollback()
		rule, err := LoadAIEntryRule(ctx, tx, "split_group", leaf)
		if err != nil {
			t.Fatal(err)
		}
		selection := AIEntryDirectSplit{Kind: "split_group", TargetID: leaf, Revision: rule.Revision, EntryHostA: "192.0.2.203", EntryHostB: "192.0.2.204"}
		snapshot, err := CaptureAIEntryDirectSplit(ctx, tx, selection)
		if err != nil {
			t.Fatal(err)
		}
		// Simulates a user deletion between CAS capture and assignment locking.
		if _, err = tx.Exec(`DELETE FROM v2_client_entry_user_policy_split_assignment WHERE policy_id=$1 AND group_id=$2 AND user_id=(SELECT MIN(user_id) FROM v2_client_entry_user_policy_split_assignment WHERE policy_id=$1 AND group_id=$2)`, policy, leaf); err != nil {
			t.Fatal(err)
		}
		if _, err = ApplyAIEntryDirectSplitTx(ctx, tx, snapshot); !errors.Is(err, ErrAIEntrySplitConflict) {
			t.Fatal("changed assigned set applied", err)
		}
		tx.Rollback()
		onePolicy, _ := f.policy(users[0], users[0], "one.example.test")
		tx = begin()
		defer tx.Rollback()
		rule, err = LoadAIEntryRule(ctx, tx, "policy", onePolicy)
		if err != nil {
			t.Fatal(err)
		}
		_, err = CaptureAIEntryDirectSplit(ctx, tx, AIEntryDirectSplit{Kind: "policy", TargetID: onePolicy, Revision: rule.Revision, EntryHostA: "a.example.test", EntryHostB: "b.example.test"})
		if !errors.Is(err, ErrAIEntrySplitInvalid) || !strings.Contains(err.Error(), "不足 2 名用户") {
			t.Fatal("missing insufficient-user reason", err)
		}
	})
}
