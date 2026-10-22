package usermigration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"forest/go-api/internal/config"
	"github.com/DATA-DOG/go-sqlmock"
)

func mockService(t *testing.T) (*DBService, sqlmock.Sqlmock) {
	t.Helper()
	db, m, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s := NewDBService(config.Config{}, db)
	s.ensured = true
	s.now = func() time.Time { return fixedNow }
	return s, m
}
func preparedPreview(t *testing.T, s *DBService) string {
	t.Helper()
	result, users, plans, err := buildPreview(snapshot{users: []sourceUser{sampleUser(1)}}, sampleTarget(), map[int64]int64{4: 9}, "discard", fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("a", 64)
	result.PreviewID = id
	result.ExpiresAt = fixedNow.Add(PreviewTTL).Unix()
	result.SourceDigest = "test-digest"
	result.LockingBalancePolicy = "discard"
	s.previews[id] = &storedPreview{actorID: 7, result: result, users: users, plans: plans, state: "ready"}
	return id
}
func expectMissingReceipt(m sqlmock.Sqlmock, id string, actor int64) {
	m.ExpectQuery(`SELECT receipt_json FROM v2_user_migration_receipt`).WithArgs(id, actor).WillReturnError(sql.ErrNoRows)
}
func expectTarget(m sqlmock.Sqlmock, collision bool) {
	m.ExpectQuery(`SELECT id, name, group_id, transfer_enable, device_limit, speed_limit, reset_traffic_method FROM v2_plan`).WillReturnRows(sqlmock.NewRows([]string{"id", "name", "group_id", "transfer_enable", "device_limit", "speed_limit", "reset_traffic_method"}).AddRow(9, "Target", 3, 500, 4, 100, 1))
	rows := sqlmock.NewRows([]string{"email", "token", "uuid"})
	if collision {
		rows.AddRow(" PERSON1@EXAMPLE.TEST ", "unrelated-token", "unrelated-uuid")
	}
	m.ExpectQuery(`SELECT email, token, uuid FROM v2_user`).WithArgs(maxTargetUsers + 1).WillReturnRows(rows)
}
func expectStartApply(m sqlmock.Sqlmock, id string, collision bool) {
	expectMissingReceipt(m, id, 7)
	m.ExpectBegin()
	for _, q := range []string{"SET LOCAL lock_timeout = '5s'", "SET LOCAL statement_timeout = '100s'", "LOCK TABLE v2_user IN SHARE ROW EXCLUSIVE MODE", "LOCK TABLE v2_plan IN SHARE MODE"} {
		m.ExpectExec(regexp.QuoteMeta(q)).WillReturnResult(sqlmock.NewResult(0, 0))
	}
	expectMissingReceipt(m, id, 7)
	expectTarget(m, collision)
}
func expectInsert(m sqlmock.Sqlmock) {
	src := sampleUser(1)
	m.ExpectQuery(`INSERT INTO v2_user \(email,password,`).WithArgs(src.Email, src.Password, nil, nil, int64(1234), int64(567), src.T, src.U, src.D, src.TransferEnable, int64(0), 0, 0, src.UUID, int64(3), int64(9), int64(4), int64(100), src.Token, *src.ExpiredAt, src.CreatedAt, fixedNow.Unix(), 0).WillReturnRows(sqlmock.NewRows([]string{"id", "email"}).AddRow(101, src.Email))
	m.ExpectExec(`INSERT INTO v2_runtime_kv`).WithArgs("TRAFFIC_RESET_CYCLE_USER_101", sqlmock.AnyArg(), fixedNow.Add(400*24*time.Hour).Unix(), fixedNow.Unix(), fixedNow.Unix()).WillReturnResult(sqlmock.NewResult(0, 1))
	m.ExpectExec(`INSERT INTO v2_user_migration_receipt`).WithArgs(strings.Repeat("a", 64), int64(7), sqlmock.AnyArg(), fixedNow.Unix()).WillReturnResult(sqlmock.NewResult(0, 1))
}
func TestApplyAtomicSuccessAndDurableIdempotency(t *testing.T) {
	s, m := mockService(t)
	id := preparedPreview(t, s)
	expectStartApply(m, id, false)
	expectInsert(m)
	m.ExpectCommit()
	r, err := s.Apply(context.Background(), 7, ApplyRequest{PreviewID: id, Confirm: true})
	if err != nil || r.Status != "completed" || r.Imported != 1 {
		t.Fatalf("receipt %+v err=%v", r, err)
	}
	if _, ok := s.previews[id]; ok {
		t.Fatal("committed credentials retained")
	}
	raw, _ := json.Marshal(r)
	m.ExpectQuery(`SELECT receipt_json FROM v2_user_migration_receipt`).WithArgs(id, int64(7)).WillReturnRows(sqlmock.NewRows([]string{"receipt_json"}).AddRow(string(raw)))
	again, err := s.Apply(context.Background(), 7, ApplyRequest{PreviewID: id, Confirm: true})
	if err != nil || again != r {
		t.Fatalf("idempotent receipt = %+v %v", again, err)
	}
	if err := m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
func TestApplyTargetConflictRollsBackWithoutAnyUserWrite(t *testing.T) {
	s, m := mockService(t)
	id := preparedPreview(t, s)
	expectStartApply(m, id, true)
	m.ExpectRollback()
	_, err := s.Apply(context.Background(), 7, ApplyRequest{PreviewID: id, Confirm: true})
	if !errors.Is(err, ErrPreviewStale) {
		t.Fatal(err)
	}
	if err := m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
func TestApplyWriteFailureIsRedactedAndRolledBack(t *testing.T) {
	s, m := mockService(t)
	id := preparedPreview(t, s)
	expectStartApply(m, id, false)
	m.ExpectQuery(`INSERT INTO v2_user`).WillReturnError(errors.New("connection secret-password and private-user@example.test"))
	m.ExpectRollback()
	_, err := s.Apply(context.Background(), 7, ApplyRequest{PreviewID: id, Confirm: true})
	if !errors.Is(err, ErrTargetWrite) || strings.Contains(err.Error(), "secret-password") {
		t.Fatalf("unredacted error %v", err)
	}
	if s.previews[id].state != "ready" {
		t.Fatal("definite rollback cannot be retried")
	}
	if err := m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
func TestUnknownCommitNeverRetriesAndDropsSensitiveSnapshot(t *testing.T) {
	s, m := mockService(t)
	id := preparedPreview(t, s)
	expectStartApply(m, id, false)
	expectInsert(m)
	m.ExpectCommit().WillReturnError(errors.New("transport lost during commit"))
	expectMissingReceipt(m, id, 7)
	_, err := s.Apply(context.Background(), 7, ApplyRequest{PreviewID: id, Confirm: true})
	if !errors.Is(err, ErrCommitUnknown) {
		t.Fatal(err)
	}
	if s.previews[id].state != "unknown" || s.previews[id].users != nil {
		t.Fatal("unknown outcome remains replayable or sensitive snapshot retained")
	}
	expectMissingReceipt(m, id, 7)
	_, err = s.Apply(context.Background(), 7, ApplyRequest{PreviewID: id, Confirm: true})
	if !errors.Is(err, ErrCommitUnknown) {
		t.Fatal(err)
	}
	if err := m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
func TestPreviewOwnershipExpiryAndConfirmation(t *testing.T) {
	s, m := mockService(t)
	id := preparedPreview(t, s)
	if _, err := s.Apply(context.Background(), 7, ApplyRequest{PreviewID: id}); !errors.Is(err, ErrInvalidParameter) {
		t.Fatal(err)
	}
	expectMissingReceipt(m, id, 8)
	if _, err := s.Apply(context.Background(), 8, ApplyRequest{PreviewID: id, Confirm: true}); !errors.Is(err, ErrPreviewExpired) {
		t.Fatal(err)
	}
	s.previews[id].result.ExpiresAt = fixedNow.Add(-time.Second).Unix()
	expectMissingReceipt(m, id, 7)
	if _, err := s.Apply(context.Background(), 7, ApplyRequest{PreviewID: id, Confirm: true}); !errors.Is(err, ErrPreviewExpired) {
		t.Fatal(err)
	}
	if _, ok := s.previews[id]; ok {
		t.Fatal("expired snapshot not removed")
	}
	if err := m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
func TestActiveExpiryClearsCredentialsAndProtectsInFlight(t *testing.T) {
	s, _ := mockService(t)
	id := preparedPreview(t, s)
	p := s.previews[id]
	s.expirePreview(id)
	if p.users != nil || s.previews[id] != nil {
		t.Fatal("expired snapshot retained")
	}
	id = preparedPreview(t, s)
	p = s.previews[id]
	p.state = "applying"
	s.expirePreview(id)
	if !p.expired || len(p.users) != 1 || s.previews[id] == nil {
		t.Fatal("expiry corrupted in-flight import")
	}
	p.state = "unknown"
	s.expirePreview(id)
	if p.users != nil || s.previews[id] == nil {
		t.Fatal("uncertain outcome lost or credentials retained")
	}
}
func TestReceiptDoesNotDiscloseAnotherActorsImport(t *testing.T) {
	s, m := mockService(t)
	id := preparedPreview(t, s)
	s.previews[id].state = "unknown"
	expectMissingReceipt(m, id, 8)
	if _, err := s.Receipt(context.Background(), 8, id); !errors.Is(err, ErrReceiptNotFound) {
		t.Fatal(err)
	}
	if err := m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
