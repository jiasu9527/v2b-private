package usermigration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"forest/go-api/internal/queue"
	"github.com/DATA-DOG/go-sqlmock"
)

type migrationMailQueue struct {
	mu      sync.Mutex
	jobs    []queue.JobFunc
	names   []string
	fail    bool
	stopped bool
}

func (q *migrationMailQueue) Enqueue(name, jobName string, job queue.JobFunc) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.fail {
		return errors.New("test queue stopped")
	}
	q.names = append(q.names, name)
	q.jobs = append(q.jobs, job)
	return nil
}
func (q *migrationMailQueue) Snapshot() queue.Snapshot { return queue.Snapshot{Running: !q.stopped} }
func (q *migrationMailQueue) count() int               { q.mu.Lock(); defer q.mu.Unlock(); return len(q.jobs) }
func (q *migrationMailQueue) run(ctx context.Context, index int) error {
	q.mu.Lock()
	run := q.jobs[index]
	q.mu.Unlock()
	return run(ctx)
}

type migrationMailDispatcher struct {
	emails           [][]string
	subject, content string
	unconfigured     bool
	failureAt        int // 1-based; zero means no fake SMTP failure.
	after            func(int)
}

func (d *migrationMailDispatcher) MigrationMailSettings() (int64, bool, error) {
	return 5, !d.unconfigured, nil
}
func (d *migrationMailDispatcher) PrepareMigrationMail(ctx context.Context, emails []string, subject, content string, cb func(context.Context, bool) error) (int64, queue.JobFunc, error) {
	if d.unconfigured {
		return 0, nil, errors.New("test smtp unavailable")
	}
	d.emails = append(d.emails, append([]string(nil), emails...))
	d.subject, d.content = subject, content
	return 5, func(ctx context.Context) error {
		failed := false
		for i := range emails {
			if err := ctx.Err(); err != nil {
				return err
			}
			success := i+1 != d.failureAt
			if !success {
				failed = true
			}
			if d.after != nil {
				d.after(i)
			}
			if err := cb(ctx, success); err != nil {
				return err
			}
		}
		if failed {
			return errors.New("fake SMTP rejection")
		}
		return nil
	}, nil
}

func mailTestRequest() MailRequest {
	return MailRequest{PreviewID: strings.Repeat("a", 64), RequestID: strings.Repeat("b", 64), Subject: "Migration notice", Content: "Welcome to the target site.", Confirm: true}
}
func expectMailReceipt(m sqlmock.Sqlmock, id string, tracking bool) {
	raw, _ := json.Marshal(Receipt{PreviewID: id, Status: "completed", Imported: 2, MailTrackingAvailable: tracking})
	m.ExpectQuery(`SELECT receipt_json FROM v2_user_migration_receipt`).WithArgs(id, int64(7)).WillReturnRows(sqlmock.NewRows([]string{"receipt_json"}).AddRow(string(raw)))
}
func expectNewMail(m sqlmock.Sqlmock, req MailRequest) {
	m.ExpectBegin()
	expectMailReceipt(m, req.PreviewID, true)
	m.ExpectQuery(`SELECT request_id,preview_id,status,recipients,sent,failed,interval_seconds,created_at,updated_at,content_hash FROM v2_user_migration_mail`).WithArgs(req.RequestID, req.PreviewID, int64(7)).WillReturnError(sql.ErrNoRows)
	m.ExpectQuery(`SELECT EXISTS`).WithArgs(req.PreviewID).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
}
func expectMailInsert(m sqlmock.Sqlmock, req MailRequest) {
	m.ExpectQuery(`SELECT DISTINCT lower\(btrim\(u.email\)\).*m.preview_id=\$1`).WithArgs(req.PreviewID).WillReturnRows(sqlmock.NewRows([]string{"email"}).AddRow("new1@example.test").AddRow("new2@example.test"))
	_, hash, _ := normalizeMailRequest(7, req)
	m.ExpectExec(`INSERT INTO v2_user_migration_mail`).WithArgs(req.RequestID, req.PreviewID, int64(7), hash, 2, int64(5), fixedNow.Unix()).WillReturnResult(sqlmock.NewResult(0, 1))
	m.ExpectCommit()
}
func expectSavedMail(m sqlmock.Sqlmock, req MailRequest, hash, status string) {
	m.ExpectBegin()
	expectMailReceipt(m, req.PreviewID, true)
	m.ExpectQuery(`SELECT request_id,preview_id,status,recipients,sent,failed,interval_seconds,created_at,updated_at,content_hash FROM v2_user_migration_mail`).WithArgs(req.RequestID, req.PreviewID, int64(7)).WillReturnRows(sqlmock.NewRows([]string{"request_id", "preview_id", "status", "recipients", "sent", "failed", "interval_seconds", "created_at", "updated_at", "content_hash"}).AddRow(req.RequestID, req.PreviewID, status, 2, 0, 0, 5, fixedNow.Unix(), fixedNow.Unix(), hash))
	m.ExpectRollback()
}
func assertMailExpectations(t *testing.T, m sqlmock.Sqlmock) {
	t.Helper()
	if err := m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationMailValidationAndHash(t *testing.T) {
	req := mailTestRequest()
	cases := []MailRequest{req, req, req, req, req, req, req, req, req, req, req}
	cases[0].Subject = "\r\nBcc: target@example.test"
	cases[1].Subject = strings.Repeat("界", 201)
	cases[2].Content = strings.Repeat("a", 100001)
	cases[3].Confirm = false
	cases[4].RequestID = "not-a-request-id"
	cases[5].PreviewID = "bad"
	cases[6].Subject = " "
	cases[7].Content = " "
	cases[8].Subject = "bad\x00subject"
	cases[9].Content = "\xff"
	cases[10].Subject = "\xff"
	for i, bad := range cases {
		if _, _, err := normalizeMailRequest(7, bad); !errors.Is(err, ErrInvalidParameter) {
			t.Fatalf("case %d: %v", i, err)
		}
	}
	normalized, hash, err := normalizeMailRequest(7, req)
	if err != nil || len(hash) != 64 || normalized != req {
		t.Fatal("valid request rejected")
	}
	req.Subject = " " + req.Subject + " "
	req.Content = "\n" + req.Content + "\n"
	_, again, _ := normalizeMailRequest(7, req)
	if hash != again {
		t.Fatal("normalized identical bodies use different hash")
	}
}

func TestMigrationMailRejectsOtherActorAndLegacyBatch(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "other actor", true: "legacy"}[legacy], func(t *testing.T) {
			s, m := mockService(t)
			req := mailTestRequest()
			m.ExpectBegin()
			if legacy {
				expectMailReceipt(m, req.PreviewID, false)
			} else {
				expectMissingReceipt(m, req.PreviewID, 7)
			}
			m.ExpectRollback()
			_, err := s.SendMail(context.Background(), 7, req)
			expected := ErrReceiptNotFound
			if legacy {
				expected = ErrMailTrackingUnavailable
			}
			if !errors.Is(err, expected) {
				t.Fatal(err)
			}
			assertMailExpectations(t, m)
		})
	}
}

func TestMigrationMailNoSynchronousFallbackWithoutQueue(t *testing.T) {
	s, m := mockService(t)
	req := mailTestRequest()
	d := &migrationMailDispatcher{}
	s.WithMailDispatcher(d, nil)
	expectNewMail(m, req)
	m.ExpectRollback()
	_, err := s.SendMail(context.Background(), 7, req)
	if !errors.Is(err, ErrMailQueueUnavailable) || len(d.emails) != 0 {
		t.Fatalf("unexpected send without queue: %v", err)
	}
	assertMailExpectations(t, m)
}

func TestMigrationMailExactRecipientsAndQueue(t *testing.T) {
	s, m := mockService(t)
	req := mailTestRequest()
	q, d := &migrationMailQueue{}, &migrationMailDispatcher{}
	s.WithMailDispatcher(d, q)
	expectNewMail(m, req)
	expectMailInsert(m, req)
	task, err := s.SendMail(context.Background(), 7, req)
	if err != nil || task.Status != "queued" || task.Recipients != 2 || task.IntervalSeconds != 5 || q.count() != 1 || q.names[0] != "send_email_mass" || !reflect.DeepEqual(d.emails, [][]string{{"new1@example.test", "new2@example.test"}}) {
		t.Fatalf("task=%+v err=%v emails=%v", task, err, d.emails)
	}
	assertMailExpectations(t, m)
}

func TestMigrationMailDurableRequestIdempotencyAndContentConflict(t *testing.T) {
	for _, status := range []string{"queued", "running", "completed", "partial_failed", "interrupted", "queue_failed"} {
		t.Run(status, func(t *testing.T) {
			s, m := mockService(t)
			req := mailTestRequest()
			_, hash, _ := normalizeMailRequest(7, req)
			// Replay remains available after SMTP/queue are removed. It never enqueues.
			expectSavedMail(m, req, hash, status)
			task, err := s.SendMail(context.Background(), 7, req)
			if err != nil || task.Status != status {
				t.Fatalf("replay %+v %v", task, err)
			}
			changed := req
			changed.Content = "different message"
			expectSavedMail(m, changed, hash, status)
			if _, err = s.SendMail(context.Background(), 7, changed); !errors.Is(err, ErrMailConflict) {
				t.Fatal(err)
			}
			assertMailExpectations(t, m)
		})
	}
}

func TestMigrationMailRejectsSecondActiveRequest(t *testing.T) {
	s, m := mockService(t)
	req := mailTestRequest()
	m.ExpectBegin()
	expectMailReceipt(m, req.PreviewID, true)
	m.ExpectQuery(`SELECT request_id.*content_hash FROM v2_user_migration_mail`).WillReturnError(sql.ErrNoRows)
	m.ExpectQuery(`SELECT EXISTS`).WithArgs(req.PreviewID).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	m.ExpectRollback()
	if _, err := s.SendMail(context.Background(), 7, req); !errors.Is(err, ErrMailBusy) {
		t.Fatal(err)
	}
	assertMailExpectations(t, m)
}

func TestMigrationMailQueueFailureIsPersistedAndNotSent(t *testing.T) {
	s, m := mockService(t)
	req := mailTestRequest()
	q := &migrationMailQueue{fail: true}
	s.WithMailDispatcher(&migrationMailDispatcher{}, q)
	expectNewMail(m, req)
	expectMailInsert(m, req)
	m.ExpectExec(`UPDATE v2_user_migration_mail SET status='queue_failed'`).WithArgs(req.RequestID, fixedNow.Unix()).WillReturnResult(sqlmock.NewResult(0, 1))
	task, err := s.SendMail(context.Background(), 7, req)
	if err != nil || task.Status != "queue_failed" || q.count() != 0 {
		t.Fatalf("%+v %v", task, err)
	}
	assertMailExpectations(t, m)
}

func TestMigrationMailCountsAttemptAfterCancellationAndStops(t *testing.T) {
	s, m := mockService(t)
	req := mailTestRequest()
	q, d := &migrationMailQueue{}, &migrationMailDispatcher{}
	s.WithMailDispatcher(d, q)
	expectNewMail(m, req)
	expectMailInsert(m, req)
	if _, err := s.SendMail(context.Background(), 7, req); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.after = func(int) { cancel() }
	m.ExpectExec(`UPDATE v2_user_migration_mail SET status='running'`).WithArgs(req.RequestID, fixedNow.Unix()).WillReturnResult(sqlmock.NewResult(0, 1))
	m.ExpectExec(`UPDATE v2_user_migration_mail SET sent=sent\+\$2,failed=failed\+\$3`).WithArgs(req.RequestID, 1, 0, fixedNow.Unix()).WillReturnResult(sqlmock.NewResult(0, 1))
	m.ExpectExec(`UPDATE v2_user_migration_mail SET status=CASE`).WithArgs(req.RequestID, fixedNow.Unix()).WillReturnResult(sqlmock.NewResult(0, 1))
	if err := q.run(ctx, 0); err == nil {
		t.Fatal("cancelled job reported complete")
	}
	assertMailExpectations(t, m)
}

func TestMigrationMailDuplicateQueueExecutionAndAccountingFailure(t *testing.T) {
	s, m := mockService(t)
	req := mailTestRequest()
	m.ExpectExec(`UPDATE v2_user_migration_mail SET status='running'`).WithArgs(req.RequestID, fixedNow.Unix()).WillReturnResult(sqlmock.NewResult(0, 0))
	called := false
	if err := s.runMailTask(context.Background(), req.RequestID, func(context.Context) error { called = true; return nil }); err != nil || called {
		t.Fatal("duplicate queued job sent again")
	}
	m.ExpectExec(`UPDATE v2_user_migration_mail SET sent=sent\+\$2,failed=failed\+\$3`).WithArgs(req.RequestID, 0, 1, fixedNow.Unix()).WillReturnError(errors.New("private DB error"))
	if err := s.recordMailResult(req.RequestID, false); !errors.Is(err, ErrTargetWrite) || strings.Contains(err.Error(), "private") {
		t.Fatal(err)
	}
	assertMailExpectations(t, m)
}

func TestMigrationMailInfoLegacyDoesNotGuessRecipients(t *testing.T) {
	s, m := mockService(t)
	req := mailTestRequest()
	s.WithMailDispatcher(&migrationMailDispatcher{}, &migrationMailQueue{})
	expectMailReceipt(m, req.PreviewID, false)
	m.ExpectQuery(`SELECT request_id.*FROM v2_user_migration_mail WHERE preview_id=\$1 AND actor_id=\$2`).WithArgs(req.PreviewID, int64(7)).WillReturnError(sql.ErrNoRows)
	info, err := s.MailInfo(context.Background(), 7, req.PreviewID)
	if err != nil || info.TrackingAvailable || info.Recipients != 0 || info.Task != nil || info.IntervalSeconds != 5 {
		t.Fatalf("%+v %v", info, err)
	}
	assertMailExpectations(t, m)
}

type migrationTryMailQueue struct {
	migrationMailQueue
	tried bool
}

func (q *migrationTryMailQueue) TryEnqueue(name, jobName string, fn queue.JobFunc) error {
	q.tried = true
	return errors.New("test nonblocking queue full")
}
func TestMigrationMailUsesNonblockingQueueAndNeverFallsBack(t *testing.T) {
	s, m := mockService(t)
	req := mailTestRequest()
	q := &migrationTryMailQueue{}
	s.WithMailDispatcher(&migrationMailDispatcher{}, q)
	expectNewMail(m, req)
	expectMailInsert(m, req)
	m.ExpectExec(`UPDATE v2_user_migration_mail SET status='queue_failed'`).WithArgs(req.RequestID, fixedNow.Unix()).WillReturnResult(sqlmock.NewResult(0, 1))
	task, err := s.SendMail(context.Background(), 7, req)
	if err != nil || task.Status != "queue_failed" || !q.tried || q.count() != 0 {
		t.Fatalf("nonblocking rejection incorrectly fell back: %+v %v", task, err)
	}
	assertMailExpectations(t, m)
}

func TestMigrationMailRejectsNoRecipientsAndUnconfiguredSender(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(map[bool]string{false: "smtp", true: "empty"}[empty], func(t *testing.T) {
			s, m := mockService(t)
			req := mailTestRequest()
			q, d := &migrationMailQueue{}, &migrationMailDispatcher{unconfigured: true}
			s.WithMailDispatcher(d, q)
			expectNewMail(m, req)
			rows := sqlmock.NewRows([]string{"email"})
			if !empty {
				rows.AddRow("new@example.test")
			}
			m.ExpectQuery(`SELECT DISTINCT lower`).WithArgs(req.PreviewID).WillReturnRows(rows)
			m.ExpectRollback()
			_, err := s.SendMail(context.Background(), 7, req)
			want := ErrMailUnavailable
			if empty {
				want = ErrMailNoRecipients
			}
			if !errors.Is(err, want) || q.count() != 0 {
				t.Fatalf("%v", err)
			}
			assertMailExpectations(t, m)
		})
	}
}

func TestMigrationMailAccountingFailureStopsRemainingRecipients(t *testing.T) {
	s, m := mockService(t)
	req := mailTestRequest()
	q, d := &migrationMailQueue{}, &migrationMailDispatcher{}
	s.WithMailDispatcher(d, q)
	expectNewMail(m, req)
	expectMailInsert(m, req)
	if _, err := s.SendMail(context.Background(), 7, req); err != nil {
		t.Fatal(err)
	}
	m.ExpectExec(`UPDATE v2_user_migration_mail SET status='running'`).WithArgs(req.RequestID, fixedNow.Unix()).WillReturnResult(sqlmock.NewResult(0, 1))
	m.ExpectExec(`UPDATE v2_user_migration_mail SET sent=sent\+\$2,failed=failed\+\$3`).WithArgs(req.RequestID, 1, 0, fixedNow.Unix()).WillReturnError(errors.New("DB disconnected"))
	m.ExpectExec(`UPDATE v2_user_migration_mail SET status=CASE`).WithArgs(req.RequestID, fixedNow.Unix()).WillReturnResult(sqlmock.NewResult(0, 1))
	attempts := 0
	d.after = func(int) { attempts++ }
	if err := q.run(context.Background(), 0); err == nil || attempts != 1 {
		t.Fatalf("accounting failure did not stop: %v attempts=%d", err, attempts)
	}
	assertMailExpectations(t, m)
}

func TestMigrationMailTaskInsertCommitUncertaintyDoesNotQueue(t *testing.T) {
	s, m := mockService(t)
	req := mailTestRequest()
	q := &migrationMailQueue{}
	s.WithMailDispatcher(&migrationMailDispatcher{}, q)
	expectNewMail(m, req)
	m.ExpectQuery(`SELECT DISTINCT lower`).WithArgs(req.PreviewID).WillReturnRows(sqlmock.NewRows([]string{"email"}).AddRow("new@example.test"))
	m.ExpectExec(`INSERT INTO v2_user_migration_mail`).WillReturnResult(sqlmock.NewResult(0, 1))
	m.ExpectCommit().WillReturnError(errors.New("commit transport uncertainty"))
	if _, err := s.SendMail(context.Background(), 7, req); !errors.Is(err, ErrCommitUnknown) || q.count() != 0 {
		t.Fatalf("unknown commit queued mail: %v", err)
	}
	assertMailExpectations(t, m)
}
