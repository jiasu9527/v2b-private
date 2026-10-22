package usermigration

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestSourceReadOnlySnapshotOptionalFieldsAndDeletedPlan(t *testing.T) {
	s, _ := mockService(t)
	db, m, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	if err != nil {
		t.Fatal(err)
	}
	s.openSource = func(c SourceConfig) (*sql.DB, error) {
		if c.Host != "127.0.0.1" || c.Port != 3306 || c.Password != "fake-only-test" {
			t.Error("connection config lost")
		}
		return db, nil
	}
	m.ExpectPing()
	m.ExpectBegin()
	m.ExpectQuery(`SELECT TABLE_NAME,ENGINE FROM INFORMATION_SCHEMA.TABLES`).WillReturnRows(sqlmock.NewRows([]string{"TABLE_NAME", "ENGINE"}).AddRow("v2_user", "InnoDB").AddRow("v2_plan", "InnoDB"))
	required := []string{"id", "email", "password", "balance", "commission_balance", "u", "d", "transfer_enable", "is_admin", "is_staff", "banned", "plan_id", "expired_at", "token", "uuid"}
	columns := sqlmock.NewRows([]string{"COLUMN_NAME"})
	for _, name := range required {
		columns.AddRow(name)
	}
	m.ExpectQuery(`SELECT COLUMN_NAME FROM INFORMATION_SCHEMA.COLUMNS`).WillReturnRows(columns)
	src := sampleUser(1)
	m.ExpectQuery("SELECT .* FROM `v2_user` ORDER BY `id` LIMIT \\?").WithArgs(MaxSourceUsers + 1).WillReturnRows(sqlmock.NewRows([]string{"id", "email", "password", "password_algo", "token", "uuid", "balance", "commission_balance", "locking_balance", "u", "d", "t", "transfer_enable", "plan_id", "expired_at", "banned", "is_admin", "is_staff", "created_at", "updated_at"}).AddRow(src.ID, src.Email, src.Password, "", src.Token, src.UUID, src.Balance, src.CommissionBalance, 0, src.U, src.D, 0, src.TransferEnable, 4, nil, 0, 0, 0, 0, 0))
	m.ExpectQuery("SELECT `id`, LEFT\\(`name`,255\\), COALESCE\\(`transfer_enable`,0\\) FROM `v2_plan`").WillReturnRows(sqlmock.NewRows([]string{"id", "name", "transfer_enable"}).AddRow(9, "Unused", 100))
	m.ExpectCommit()
	m.ExpectClose()
	snap, err := s.sourceSnapshot(context.Background(), SourceConfig{Host: "127.0.0.1", Database: "xboard", Username: "reader", Password: "fake-only-test"})
	if err != nil || len(snap.users) != 1 || snap.users[0].LockingBalance != 0 || snap.users[0].ExpiredAt != nil {
		t.Fatalf("snapshot %+v err=%v", snap, err)
	}
	if len(snap.plans) != 2 || snap.plans[0].ID != 4 || snap.plans[0].Users != 1 || !strings.Contains(snap.plans[0].Name, "已删除") {
		t.Fatalf("deleted plan invisible: %+v", snap.plans)
	}
	if err = m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
func TestSourceRejectsNonTransactionalEngine(t *testing.T) {
	s, _ := mockService(t)
	db, m, _ := sqlmock.New(sqlmock.MonitorPingsOption(true))
	s.openSource = func(SourceConfig) (*sql.DB, error) { return db, nil }
	m.ExpectPing()
	m.ExpectBegin()
	m.ExpectQuery(`SELECT TABLE_NAME,ENGINE FROM INFORMATION_SCHEMA.TABLES`).WillReturnRows(sqlmock.NewRows([]string{"TABLE_NAME", "ENGINE"}).AddRow("v2_user", "MyISAM"))
	m.ExpectRollback()
	m.ExpectClose()
	_, err := s.sourceSnapshot(context.Background(), SourceConfig{Host: "localhost", Database: "xboard", Username: "reader"})
	if !errors.Is(err, ErrSourceSchema) {
		t.Fatal(err)
	}
	if err = m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
func TestSourceConnectionErrorsNeverExposeCredentials(t *testing.T) {
	s, _ := mockService(t)
	s.openSource = func(SourceConfig) (*sql.DB, error) { return nil, errors.New("secret login details") }
	_, err := s.sourceSnapshot(context.Background(), SourceConfig{Host: "localhost", Database: "xboard", Username: "reader"})
	if err != ErrSourceConnection || strings.Contains(err.Error(), "secret") {
		t.Fatal(err)
	}
}
func TestDestructiveLockingPoliciesRequireExplicitConfirmation(t *testing.T) {
	s, m := mockService(t)
	for _, policy := range []string{"discard", "to_balance"} {
		if _, err := s.Preview(context.Background(), 7, PreviewRequest{LockingBalancePolicy: policy}); err != ErrInvalidParameter {
			t.Fatal(err)
		}
	}
	if err := m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestNewPreviewInvalidatesOldDraftAndUnknownBlocksNewImport(t *testing.T) {
	s, m := mockService(t)
	id := preparedPreview(t, s)
	old := s.previews[id]
	s.openSource = func(SourceConfig) (*sql.DB, error) { return nil, errors.New("connection failed") }
	req := PreviewRequest{Source: SourceConfig{Host: "localhost", Database: "xboard", Username: "reader"}}
	if _, err := s.Preview(context.Background(), 7, req); err != ErrSourceConnection {
		t.Fatal(err)
	}
	if s.previews[id] != nil || old.users != nil {
		t.Fatal("old draft retained after replacement")
	}
	id = preparedPreview(t, s)
	s.previews[id].state = "unknown"
	if _, err := s.Preview(context.Background(), 7, req); err != ErrCommitUnknown {
		t.Fatal(err)
	}
	if err := m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
