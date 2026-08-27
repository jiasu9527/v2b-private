package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestApplyUpdateCompatFixesIgnoresOwnerOnlyIndexErrors(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_v2_runtime_kv_expire_at ON v2_runtime_kv\(expire_at\)`).
		WillReturnError(errors.New("ERROR: must be owner of table v2_runtime_kv (SQLSTATE 42501)"))
	mock.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_v2_auth_session_user_id ON v2_auth_session\(user_id\)`).
		WillReturnError(errors.New("ERROR: must be owner of table v2_auth_session (SQLSTATE 42501)"))
	mock.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_v2_order_payment_callback ON v2_order\(payment_id, callback_no\) WHERE callback_no IS NOT NULL`).
		WillReturnError(errors.New("ERROR: must be owner of table v2_order (SQLSTATE 42501)"))
	mock.ExpectQuery(`SELECT EXISTS \(\s*SELECT 1 FROM information_schema.columns`).
		WithArgs("v2_server_v2node", "send_through").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectExec(`ALTER TABLE v2_server_v2node ADD COLUMN send_through varchar\(255\) DEFAULT NULL`).
		WillReturnError(errors.New("ERROR: must be owner of table v2_server_v2node (SQLSTATE 42501)"))
	mock.ExpectQuery(`SELECT EXISTS \(\s*SELECT 1 FROM information_schema.columns`).
		WithArgs("v2_server_v2node", "ddns_settings").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectExec(`ALTER TABLE v2_server_v2node ADD COLUMN ddns_settings text DEFAULT NULL`).
		WillReturnError(errors.New("ERROR: must be owner of table v2_server_v2node (SQLSTATE 42501)"))
	expectColumnDataType(mock, "v2_plan", "transfer_enable", "integer")
	mock.ExpectExec(`ALTER TABLE v2_plan ALTER COLUMN transfer_enable TYPE BIGINT USING transfer_enable::BIGINT`).
		WillReturnError(errors.New("ERROR: must be owner of table v2_plan (SQLSTATE 42501)"))

	for _, item := range []struct {
		table  string
		column string
	}{
		{table: "v2_user", column: "expired_at"},
		{table: "v2_invite_code", column: "code"},
		{table: "v2_invite_campaign", column: "invite_code"},
		{table: "v2_invite_campaign_record", column: "invite_code"},
	} {
		mock.ExpectQuery(`SELECT EXISTS \(\s*SELECT 1 FROM information_schema.columns`).
			WithArgs(item.table, item.column).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	}

	if err := applyUpdateCompatFixes(context.Background(), db); err != nil {
		t.Fatalf("applyUpdateCompatFixes: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestApplyUpdateCompatFixesTrimsExistingInviteColumns(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_v2_runtime_kv_expire_at ON v2_runtime_kv\(expire_at\)`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_v2_auth_session_user_id ON v2_auth_session\(user_id\)`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_v2_order_payment_callback ON v2_order\(payment_id, callback_no\) WHERE callback_no IS NOT NULL`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT EXISTS \(\s*SELECT 1 FROM information_schema.columns`).
		WithArgs("v2_server_v2node", "send_through").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(`SELECT EXISTS \(\s*SELECT 1 FROM information_schema.columns`).
		WithArgs("v2_server_v2node", "ddns_settings").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	expectColumnDataType(mock, "v2_plan", "transfer_enable", "bigint")

	mock.ExpectQuery(`SELECT EXISTS \(\s*SELECT 1 FROM information_schema.columns`).
		WithArgs("v2_user", "expired_at").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectExec(`UPDATE v2_user SET expired_at = NULL WHERE expired_at IS NOT NULL AND expired_at <= 0`).
		WillReturnResult(sqlmock.NewResult(0, 4))

	mock.ExpectQuery(`SELECT EXISTS \(\s*SELECT 1 FROM information_schema.columns`).
		WithArgs("v2_invite_code", "code").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectExec(`UPDATE v2_invite_code SET code = BTRIM\(code\)`).
		WillReturnResult(sqlmock.NewResult(0, 2))

	mock.ExpectQuery(`SELECT EXISTS \(\s*SELECT 1 FROM information_schema.columns`).
		WithArgs("v2_invite_campaign", "invite_code").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectExec(`UPDATE v2_invite_campaign SET invite_code = CASE WHEN invite_code IS NULL THEN NULL ELSE BTRIM\(invite_code\) END`).
		WillReturnResult(sqlmock.NewResult(0, 1))

	mock.ExpectQuery(`SELECT EXISTS \(\s*SELECT 1 FROM information_schema.columns`).
		WithArgs("v2_invite_campaign_record", "invite_code").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectExec(`UPDATE v2_invite_campaign_record SET invite_code = BTRIM\(invite_code\)`).
		WillReturnResult(sqlmock.NewResult(0, 3))

	if err := applyUpdateCompatFixes(context.Background(), db); err != nil {
		t.Fatalf("applyUpdateCompatFixes: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestApplyUpdateCompatFixesAddsV2nodeSendThroughColumnWhenMissing(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_v2_runtime_kv_expire_at ON v2_runtime_kv\(expire_at\)`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_v2_auth_session_user_id ON v2_auth_session\(user_id\)`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_v2_order_payment_callback ON v2_order\(payment_id, callback_no\) WHERE callback_no IS NOT NULL`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT EXISTS \(\s*SELECT 1 FROM information_schema.columns`).
		WithArgs("v2_server_v2node", "send_through").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectExec(`ALTER TABLE v2_server_v2node ADD COLUMN send_through varchar\(255\) DEFAULT NULL`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT EXISTS \(\s*SELECT 1 FROM information_schema.columns`).
		WithArgs("v2_server_v2node", "ddns_settings").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectExec(`ALTER TABLE v2_server_v2node ADD COLUMN ddns_settings text DEFAULT NULL`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	expectColumnDataType(mock, "v2_plan", "transfer_enable", "integer")
	mock.ExpectExec(`ALTER TABLE v2_plan ALTER COLUMN transfer_enable TYPE BIGINT USING transfer_enable::BIGINT`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	for _, item := range []struct {
		table  string
		column string
	}{
		{table: "v2_user", column: "expired_at"},
		{table: "v2_invite_code", column: "code"},
		{table: "v2_invite_campaign", column: "invite_code"},
		{table: "v2_invite_campaign_record", column: "invite_code"},
	} {
		mock.ExpectQuery(`SELECT EXISTS \(\s*SELECT 1 FROM information_schema.columns`).
			WithArgs(item.table, item.column).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	}

	if err := applyUpdateCompatFixes(context.Background(), db); err != nil {
		t.Fatalf("applyUpdateCompatFixes: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestApplyUpdateCompatFixesMigratesPlanTransferEnableToBigint(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_v2_runtime_kv_expire_at ON v2_runtime_kv\(expire_at\)`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_v2_auth_session_user_id ON v2_auth_session\(user_id\)`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CREATE INDEX IF NOT EXISTS idx_v2_order_payment_callback ON v2_order\(payment_id, callback_no\) WHERE callback_no IS NOT NULL`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT EXISTS \(\s*SELECT 1 FROM information_schema.columns`).
		WithArgs("v2_server_v2node", "send_through").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(`SELECT EXISTS \(\s*SELECT 1 FROM information_schema.columns`).
		WithArgs("v2_server_v2node", "ddns_settings").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	expectColumnDataType(mock, "v2_plan", "transfer_enable", "integer")
	mock.ExpectExec(`ALTER TABLE v2_plan ALTER COLUMN transfer_enable TYPE BIGINT USING transfer_enable::BIGINT`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	for _, item := range []struct {
		table  string
		column string
	}{
		{table: "v2_user", column: "expired_at"},
		{table: "v2_invite_code", column: "code"},
		{table: "v2_invite_campaign", column: "invite_code"},
		{table: "v2_invite_campaign_record", column: "invite_code"},
	} {
		mock.ExpectQuery(`SELECT EXISTS \(\s*SELECT 1 FROM information_schema.columns`).
			WithArgs(item.table, item.column).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	}

	if err := applyUpdateCompatFixes(context.Background(), db); err != nil {
		t.Fatalf("applyUpdateCompatFixes: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func expectColumnDataType(mock sqlmock.Sqlmock, table, column, dataType string) {
	mock.ExpectQuery(`SELECT data_type FROM information_schema.columns`).
		WithArgs(table, column).
		WillReturnRows(sqlmock.NewRows([]string{"data_type"}).AddRow(dataType))
}

func TestVerifyRequiredUpdateSchemaRejectsMissingCheckoutResult(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(`SELECT EXISTS \(\s*SELECT 1 FROM information_schema.columns`).
		WithArgs("v2_order", "checkout_result").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))

	err = verifyRequiredUpdateSchema(context.Background(), db)
	if err == nil || !strings.Contains(err.Error(), "v2_order.checkout_result is missing") {
		t.Fatalf("expected a required-schema error, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestVerifyRequiredUpdateSchemaRejectsMissingPaymentAttemptTable(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	for _, column := range []string{"checkout_result", "checkout_claim", "checkout_claim_expires_at", "checkout_fingerprint"} {
		mock.ExpectQuery(`SELECT EXISTS \(\s*SELECT 1 FROM information_schema.columns`).
			WithArgs("v2_order", column).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	}
	mock.ExpectQuery(`SELECT EXISTS \(\s*SELECT 1 FROM information_schema.columns`).
		WithArgs("v2_order_payment_attempt", "order_id").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))

	err = verifyRequiredUpdateSchema(context.Background(), db)
	if err == nil || !strings.Contains(err.Error(), "v2_order_payment_attempt.order_id is missing") {
		t.Fatalf("expected a required payment-attempt schema error, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestVerifyRequiredUpdateSchemaAcceptsCheckoutResult(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	for _, column := range []string{"checkout_result", "checkout_claim", "checkout_claim_expires_at", "checkout_fingerprint"} {
		mock.ExpectQuery(`SELECT EXISTS \(\s*SELECT 1 FROM information_schema.columns`).
			WithArgs("v2_order", column).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	}
	for _, column := range []string{"order_id", "payment_id", "handling_amount", "amount", "checkout_result", "checkout_claim", "checkout_claim_expires_at", "checkout_fingerprint", "callback_no", "status", "paid_at"} {
		mock.ExpectQuery(`SELECT EXISTS \(\s*SELECT 1 FROM information_schema.columns`).
			WithArgs("v2_order_payment_attempt", column).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	}

	if err := verifyRequiredUpdateSchema(context.Background(), db); err != nil {
		t.Fatalf("verify required schema: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}
