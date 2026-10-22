package usermigration

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
)

func TestMigrationIntegrationMailTracksExactImportedMembersAndSeparateBatches(t *testing.T) {
	f := newMigrationIntegrationFixture(t)
	ctx := context.Background()
	ordinary := f.users(t, 4)
	ordinary[1].PlanID = nil // Planless imported users must also be tracked.
	existing, admin := sampleUser(10), sampleUser(11)
	admin.IsAdmin = 1
	f.seedSource(t, append(ordinary, existing, admin))
	if _, err := f.pg.ExecContext(ctx, `INSERT INTO v2_user (email,password,uuid,token,created_at,updated_at) VALUES ($1,'target-password','target-uuid','target-token',1,1)`, existing.Email); err != nil {
		t.Fatal(err)
	}
	p := f.preview(t)
	receipt, err := f.service.Apply(ctx, 7, ApplyRequest{PreviewID: p.PreviewID, Confirm: true})
	if err != nil || receipt.Imported != 4 || !receipt.MailTrackingAvailable {
		t.Fatalf("receipt=%+v err=%v", receipt, err)
	}
	if n := integrationCount(t, f.pg, `SELECT count(*) FROM v2_user_migration_member WHERE preview_id=$1`, p.PreviewID); n != 4 {
		t.Fatalf("member count %d", n)
	}
	if n := integrationCount(t, f.pg, `SELECT count(*) FROM v2_user_migration_member m JOIN v2_user u ON u.id=m.user_id WHERE m.preview_id=$1 AND u.email=$2`, p.PreviewID, existing.Email); n != 0 {
		t.Fatal("pre-existing user tracked as imported")
	}
	q, d := &migrationMailQueue{}, &migrationMailDispatcher{}
	f.service.WithMailDispatcher(d, q)
	// Respect current target state without looking back into the source DB.
	if _, err = f.pg.ExecContext(ctx, `UPDATE v2_user SET is_staff=1 WHERE email=$1`, ordinary[2].Email); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.ExecContext(ctx, `DELETE FROM v2_user WHERE email=$1`, ordinary[3].Email); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.ExecContext(ctx, `UPDATE v2_user SET email='current-email@example.test' WHERE email=$1`, ordinary[0].Email); err != nil {
		t.Fatal(err)
	}
	info, err := f.service.MailInfo(ctx, 7, p.PreviewID)
	if err != nil || info.Recipients != 2 || !info.Configured || !info.QueueAvailable || info.IntervalSeconds != 5 {
		t.Fatalf("mail info=%+v %v", info, err)
	}
	if _, err = f.service.MailInfo(ctx, 8, p.PreviewID); !errors.Is(err, ErrReceiptNotFound) {
		t.Fatalf("other actor info: %v", err)
	}
	req := mailTestRequest()
	req.PreviewID = p.PreviewID
	task, err := f.service.SendMail(ctx, 7, req)
	wantEmails := []string{"current-email@example.test", ordinary[1].Email}
	sort.Strings(wantEmails)
	if err != nil || task.Status != "queued" || q.count() != 1 || len(d.emails) != 1 || !reflect.DeepEqual(d.emails[0], wantEmails) {
		t.Fatalf("queued task=%+v err=%v recipients=%v", task, err, d.emails)
	}
	// Persistent idempotency also holds through a fresh service instance.
	restarted := NewDBService(f.service.cfg, f.pg).WithMailDispatcher(d, q)
	again, err := restarted.SendMail(ctx, 7, req)
	if err != nil || again != task || q.count() != 1 {
		t.Fatalf("restart replay=%+v %v jobs=%d", again, err, q.count())
	}
	changed := req
	changed.Content = "Changed body"
	if _, err = f.service.SendMail(ctx, 7, changed); !errors.Is(err, ErrMailConflict) {
		t.Fatalf("content conflict: %v", err)
	}
	active := req
	active.RequestID = strings.Repeat("a", 64)
	if _, err = f.service.SendMail(ctx, 7, active); !errors.Is(err, ErrMailBusy) {
		t.Fatalf("active batch conflict: %v", err)
	}
	if err = q.run(ctx, 0); err != nil {
		t.Fatal(err)
	}
	info, err = f.service.MailInfo(ctx, 7, p.PreviewID)
	if err != nil || info.Task == nil || info.Task.Status != "completed" || info.Task.Sent != 2 || info.Task.Failed != 0 {
		t.Fatalf("completed info=%+v %v", info, err)
	}
	// Invoking a queue job a second time must not invoke SMTP/callback again.
	if err = q.run(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if n := integrationCount(t, f.pg, `SELECT sent FROM v2_user_migration_mail WHERE request_id=$1`, req.RequestID); n != 2 {
		t.Fatalf("duplicate queue run counted %d", n)
	}
	// A new explicit request after completion is permitted, not auto-resumed.
	if _, err = f.service.SendMail(ctx, 7, active); err != nil {
		t.Fatalf("explicit second batch notice: %v", err)
	}
	if err = q.run(ctx, 1); err != nil {
		t.Fatal(err)
	}
	info, err = f.service.MailInfo(ctx, 7, p.PreviewID)
	if err != nil || info.Task == nil || info.Task.RequestID != active.RequestID {
		t.Fatalf("same-second submissions did not return latest task: %+v %v", info, err)
	}
	// Import a second snapshot into a different receipt and prove isolation.
	// Restore deleted/changed emails to ensure this second import is one new user.
	if _, err = f.source.ExecContext(ctx, `DELETE FROM v2_user`); err != nil {
		t.Fatal(err)
	}
	second := sampleUser(20)
	f.seedSource(t, []sourceUser{second})
	p2 := f.preview(t)
	if _, err = f.service.Apply(ctx, 7, ApplyRequest{PreviewID: p2.PreviewID, Confirm: true}); err != nil {
		t.Fatal(err)
	}
	req2 := req
	req2.PreviewID = p2.PreviewID
	req2.RequestID = strings.Repeat("d", 64)
	task2, err := f.service.SendMail(ctx, 7, req2)
	if err != nil || task2.Recipients != 1 || !reflect.DeepEqual(d.emails[2], []string{second.Email}) {
		t.Fatalf("cross-batch leak %+v %v recipients=%v", task2, err, d.emails)
	}
	if err = q.run(ctx, 2); err != nil {
		t.Fatal(err)
	}
	// Reusing a request ID from another batch cannot create a second dispatch.
	req2.RequestID = req.RequestID
	if _, err = f.service.SendMail(ctx, 7, req2); !errors.Is(err, ErrMailConflict) || q.count() != 3 {
		t.Fatalf("cross-batch duplicate id: %v", err)
	}
}

func TestMigrationIntegrationMailConcurrentIdempotencyAndFailureStates(t *testing.T) {
	f := newMigrationIntegrationFixture(t)
	ctx := context.Background()
	f.seedSource(t, f.users(t, 3))
	p := f.preview(t)
	if _, err := f.service.Apply(ctx, 7, ApplyRequest{PreviewID: p.PreviewID, Confirm: true}); err != nil {
		t.Fatal(err)
	}
	q, d := &migrationMailQueue{}, &migrationMailDispatcher{failureAt: 2}
	f.service.WithMailDispatcher(d, q)
	req := mailTestRequest()
	req.PreviewID = p.PreviewID
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := f.service.SendMail(ctx, 7, req); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if q.count() != 1 || len(d.emails) != 1 {
		t.Fatalf("concurrent duplicate queued=%d prepared=%d", q.count(), len(d.emails))
	}
	if err := q.run(ctx, 0); err == nil {
		t.Fatal("fake SMTP failure was not propagated")
	}
	info, err := f.service.MailInfo(ctx, 7, p.PreviewID)
	if err != nil || info.Task.Status != "partial_failed" || info.Task.Sent != 2 || info.Task.Failed != 1 {
		t.Fatalf("partial state %+v %v", info, err)
	}
	// Cancellation after the first SMTP attempt still records that attempt.
	req.RequestID = strings.Repeat("c", 64)
	d.failureAt = 0
	task, err := f.service.SendMail(ctx, 7, req)
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	d.after = func(int) { cancel() }
	if err = q.run(runCtx, 1); err == nil {
		t.Fatal("cancelled task did not fail")
	}
	var status string
	var sent, failed int
	if err = f.pg.QueryRowContext(ctx, `SELECT status,sent,failed FROM v2_user_migration_mail WHERE request_id=$1`, task.RequestID).Scan(&status, &sent, &failed); err != nil || status != "interrupted" || sent != 1 || failed != 0 {
		t.Fatalf("interruption=%s sent=%d failed=%d err=%v", status, sent, failed, err)
	}
	// An interrupted request cannot be silently resent by repeating the POST.
	if _, err = f.service.SendMail(ctx, 7, req); err != nil || q.count() != 2 {
		t.Fatalf("interrupted replay created a job: %v", err)
	}
	q.fail = true
	req.RequestID = strings.Repeat("d", 64)
	task, err = f.service.SendMail(ctx, 7, req)
	if err != nil || task.Status != "queue_failed" {
		t.Fatalf("queue failure=%+v err=%v", task, err)
	}
	q.fail = false
	if _, err = f.service.SendMail(ctx, 7, req); err != nil || q.count() != 2 {
		t.Fatalf("queue_failed replay created a job: %v", err)
	}
}

func TestMigrationIntegrationMailLegacyReceiptDoesNotGuess(t *testing.T) {
	f := newMigrationIntegrationFixture(t)
	ctx := context.Background()
	if err := f.service.ensureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	req := mailTestRequest()
	receipt := Receipt{PreviewID: req.PreviewID, Status: "completed", Imported: 42}
	raw, _ := json.Marshal(receipt)
	if _, err := f.pg.ExecContext(ctx, `INSERT INTO v2_user_migration_receipt (preview_id,actor_id,receipt_json,created_at) VALUES ($1,7,$2,1)`, req.PreviewID, string(raw)); err != nil {
		t.Fatal(err)
	}
	f.service.WithMailDispatcher(&migrationMailDispatcher{}, &migrationMailQueue{})
	info, err := f.service.MailInfo(ctx, 7, req.PreviewID)
	if err != nil || info.TrackingAvailable || info.Recipients != 0 {
		t.Fatalf("legacy info=%+v %v", info, err)
	}
	if _, err = f.service.SendMail(ctx, 7, req); !errors.Is(err, ErrMailTrackingUnavailable) {
		t.Fatal(err)
	}
}
