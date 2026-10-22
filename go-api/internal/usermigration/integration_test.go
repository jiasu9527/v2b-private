package usermigration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"forest/go-api/internal/config"
	"forest/go-api/internal/passport"
	"forest/go-api/internal/session"
	usersvc "forest/go-api/internal/user"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// These tests are explicitly opt-in and create/drop only randomly named test
// schemas/databases. Use isolated disposable servers, never a production DSN.
// Example:
// MIGRATION_TEST_POSTGRES_DSN='postgresql://user@127.0.0.1:55479/postgres?sslmode=disable' \
// MIGRATION_TEST_MYSQL_PORT=53379 go test ./internal/usermigration -run Integration -count=1
type migrationIntegrationFixture struct {
	pg, source *sql.DB
	config     SourceConfig
	service    *DBService
	now        time.Time
}

func newMigrationIntegrationFixture(t *testing.T) *migrationIntegrationFixture {
	t.Helper()
	pgDSN, mysqlPort := os.Getenv("MIGRATION_TEST_POSTGRES_DSN"), os.Getenv("MIGRATION_TEST_MYSQL_PORT")
	if pgDSN == "" || mysqlPort == "" {
		t.Skip("set MIGRATION_TEST_POSTGRES_DSN and MIGRATION_TEST_MYSQL_PORT to disposable local databases")
	}
	port, err := strconv.Atoi(mysqlPort)
	if err != nil || port < 1 || port > 65535 {
		t.Fatal("invalid MIGRATION_TEST_MYSQL_PORT")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pgConfig, err := pgx.ParseConfig(pgDSN)
	if err != nil {
		t.Fatal("invalid MIGRATION_TEST_POSTGRES_DSN")
	}
	if pgConfig.Host != "127.0.0.1" && pgConfig.Host != "localhost" && pgConfig.Host != "::1" {
		t.Fatal("integration fixture only accepts a loopback PostgreSQL server")
	}
	adminPG := stdlib.OpenDB(*pgConfig)
	t.Cleanup(func() { _ = adminPG.Close() })
	if err = adminPG.PingContext(ctx); err != nil {
		t.Fatalf("connect to disposable PostgreSQL: %v", err)
	}
	suffix, err := randomHex(10)
	if err != nil {
		t.Fatal(err)
	}
	name := "migration_it_" + suffix
	if _, err = adminPG.ExecContext(ctx, `CREATE SCHEMA `+name); err != nil {
		t.Fatalf("create isolated PostgreSQL schema: %v", err)
	}
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		if _, err := adminPG.ExecContext(cleanup, `DROP SCHEMA `+name+` CASCADE`); err != nil {
			t.Errorf("drop isolated PostgreSQL schema: %v", err)
		}
	})
	pgConfig.RuntimeParams["search_path"] = name
	pg := stdlib.OpenDB(*pgConfig)
	pg.SetMaxOpenConns(4) // The traffic sweeper reads rows while looking up markers.
	t.Cleanup(func() { _ = pg.Close() })

	// Execute only the three current production table definitions, not the full
	// installer (which also drops tables and installs unrelated application data).
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate PostgreSQL install schema")
	}
	install, err := os.ReadFile(filepath.Join(filepath.Dir(filename), "../../../database/install.pgsql.sql"))
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"v2_user", "v2_plan", "v2_order"} {
		found := false
		for _, line := range strings.Split(string(install), "\n") {
			if strings.HasPrefix(line, `CREATE TABLE "`+table+`" (`) {
				if _, err = pg.ExecContext(ctx, line); err != nil {
					t.Fatalf("create production-shape %s: %v", table, err)
				}
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("production schema definition missing: %s", table)
		}
	}

	mysqlConfig := mysql.NewConfig()
	mysqlConfig.Net, mysqlConfig.Addr, mysqlConfig.User = "tcp", fmt.Sprintf("127.0.0.1:%d", port), "root"
	mysqlConfig.Timeout, mysqlConfig.ReadTimeout, mysqlConfig.WriteTimeout = 5*time.Second, 15*time.Second, 15*time.Second
	connector, err := mysql.NewConnector(mysqlConfig)
	if err != nil {
		t.Fatal(err)
	}
	adminSource := sql.OpenDB(connector)
	t.Cleanup(func() { _ = adminSource.Close() })
	if _, err = adminSource.ExecContext(ctx, `CREATE DATABASE `+name+` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci`); err != nil {
		t.Fatalf("create isolated MySQL database: %v", err)
	}
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		if _, err := adminSource.ExecContext(cleanup, `DROP DATABASE `+name); err != nil {
			t.Errorf("drop isolated MySQL database: %v", err)
		}
	})
	sourceConfig := SourceConfig{Host: "127.0.0.1", Port: port, Database: name, Username: "root"}
	source, err := openSource(sourceConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	for _, ddl := range []string{
		`CREATE TABLE v2_user (
 id BIGINT PRIMARY KEY, email VARCHAR(255) NOT NULL, password VARCHAR(255) NOT NULL,
 password_algo VARCHAR(32) NULL, token VARCHAR(128) NOT NULL, uuid VARCHAR(128) NOT NULL,
 balance BIGINT NOT NULL, commission_balance BIGINT NOT NULL, locking_balance BIGINT NOT NULL,
 u BIGINT NOT NULL, d BIGINT NOT NULL, t BIGINT NOT NULL, transfer_enable BIGINT NOT NULL,
 plan_id BIGINT NULL, expired_at BIGINT NULL, banned BIGINT NOT NULL, is_admin BIGINT NOT NULL,
 is_staff BIGINT NOT NULL, created_at BIGINT NOT NULL, updated_at BIGINT NOT NULL
) ENGINE=InnoDB`,
		`CREATE TABLE v2_plan (id BIGINT PRIMARY KEY, name VARCHAR(255) NOT NULL, transfer_enable BIGINT NOT NULL) ENGINE=InnoDB`,
		`INSERT INTO v2_plan (id,name,transfer_enable) VALUES (4,'Source package',300)`,
	} {
		if _, err = source.ExecContext(ctx, ddl); err != nil {
			t.Fatalf("create isolated source tables: %v", err)
		}
	}
	now := time.Now()
	if _, err = pg.ExecContext(ctx, `INSERT INTO v2_plan (id,name,group_id,transfer_enable,device_limit,speed_limit,reset_traffic_method,created_at,updated_at) VALUES (9,'Target package',3,700,4,100,0,$1,$1)`, now.Unix()); err != nil {
		t.Fatal(err)
	}
	service := NewDBService(config.Config{ResetTrafficMethod: 0}, pg)
	service.now = func() time.Time { return now }
	return &migrationIntegrationFixture{pg: pg, source: source, config: sourceConfig, service: service, now: now}
}

func (f *migrationIntegrationFixture) users(t *testing.T, count int) []sourceUser {
	t.Helper()
	users := make([]sourceUser, count)
	for i := range users {
		users[i] = sampleUser(int64(i + 1))
		users[i].ExpiredAt = i64(f.now.Add(90 * 24 * time.Hour).Unix())
	}
	return users
}

func (f *migrationIntegrationFixture) seedSource(t *testing.T, users []sourceUser) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	tx, err := f.source.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO v2_user (id,email,password,password_algo,token,uuid,balance,commission_balance,locking_balance,u,d,t,transfer_enable,plan_id,expired_at,banned,is_admin,is_staff,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		t.Fatal(err)
	}
	defer stmt.Close()
	for _, u := range users {
		if _, err = stmt.ExecContext(ctx, u.ID, u.Email, u.Password, u.PasswordAlgo, u.Token, u.UUID, u.Balance, u.CommissionBalance, u.LockingBalance, u.U, u.D, u.T, u.TransferEnable, u.PlanID, u.ExpiredAt, u.Banned, u.IsAdmin, u.IsStaff, u.CreatedAt, u.UpdatedAt); err != nil {
			t.Fatalf("seed source fixture row %d: %v", u.ID, err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func (f *migrationIntegrationFixture) preview(t *testing.T) PreviewResult {
	t.Helper()
	p, err := f.service.Preview(context.Background(), 7, PreviewRequest{Source: f.config, PlanMapping: map[string]int64{"4": 9}, LockingBalancePolicy: "discard", ConfirmLockingBalance: true})
	if err != nil || !p.CanApply {
		t.Fatalf("preview failed: result=%+v err=%v", p, err)
	}
	return p
}

func integrationCount(t *testing.T, db *sql.DB, query string, args ...any) int64 {
	t.Helper()
	var count int64
	if err := db.QueryRowContext(context.Background(), query, args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestMigrationIntegrationMySQLToPostgresWholeFlow(t *testing.T) {
	f := newMigrationIntegrationFixture(t)
	ctx := context.Background()
	users := f.users(t, 501)
	admin, staff, existing, locked, planless := sampleUser(600), sampleUser(601), sampleUser(602), sampleUser(603), sampleUser(604)
	admin.IsAdmin, staff.IsStaff = 1, 1
	locked.LockingBalance = 98765
	// PHP password_hash() emits the $2y$ variant used by Xboard.
	locked.Password = "$2y$" + testHash[4:]
	locked.ExpiredAt = i64(f.now.Add(90 * 24 * time.Hour).Unix())
	planless.PlanID, planless.ExpiredAt = nil, nil
	planless.TransferEnable, planless.U, planless.D = 987654321, 111, 222
	users = append(users, admin, staff, existing, locked, planless)
	f.seedSource(t, users)
	if _, err := f.pg.ExecContext(ctx, `INSERT INTO v2_user (email,password,uuid,token,balance,commission_balance,transfer_enable,expired_at,is_admin,created_at,updated_at) VALUES ($1,'unchanged-target-password','target-existing-uuid','ffffffffffffffffffffffffffffffff',4444,5555,8888,1999999999,1,1700000000,1700000001)`, existing.Email); err != nil {
		t.Fatal(err)
	}
	var beforeExisting, afterExisting string
	if err := f.pg.QueryRowContext(ctx, `SELECT row_to_json(target_row)::text FROM v2_user target_row WHERE email=$1`, existing.Email).Scan(&beforeExisting); err != nil {
		t.Fatal(err)
	}
	inspection, err := f.service.Inspect(ctx, 7, f.config)
	if err != nil || inspection.TotalUsers != 506 || inspection.SourceAdmins != 1 || inspection.SourceStaff != 1 || inspection.LockingBalanceUsers != 1 || inspection.LockingBalanceTotal != 98765 || len(inspection.SourcePlans) != 1 || len(inspection.TargetPlans) != 1 {
		t.Fatalf("unexpected real inspection: %+v err=%v", inspection, err)
	}
	p := f.preview(t)
	if p.Counts.Importable != 503 || p.Counts.SkippedPrivileged != 2 || p.Counts.SkippedExisting != 1 || p.Counts.NoPlan != 1 || p.Counts.LockingBalanceUsers != 1 || p.Counts.LockingBalanceTotal != 98765 {
		t.Fatalf("unexpected real preview counts: %+v", p.Counts)
	}
	if _, err = f.service.Apply(ctx, 8, ApplyRequest{PreviewID: p.PreviewID, Confirm: true}); !errors.Is(err, ErrPreviewExpired) {
		t.Fatalf("foreign actor may apply preview: %v", err)
	}
	if _, err = f.service.Receipt(ctx, 8, p.PreviewID); !errors.Is(err, ErrReceiptNotFound) {
		t.Fatalf("foreign actor may inspect preview: %v", err)
	}
	r, err := f.service.Apply(ctx, 7, ApplyRequest{PreviewID: p.PreviewID, Confirm: true})
	if err != nil || r.Status != "completed" || r.Imported != 503 || r.Counts != p.Counts || r.LockingBalancePolicy != "discard" {
		t.Fatalf("real multi-batch apply failed: %+v err=%v", r, err)
	}
	for _, service := range []*DBService{f.service, NewDBService(config.Config{}, f.pg)} {
		got, err := service.Receipt(ctx, 7, p.PreviewID)
		if err != nil || got != r {
			t.Fatalf("durable receipt mismatch: %+v err=%v", got, err)
		}
		again, err := service.Apply(ctx, 7, ApplyRequest{PreviewID: p.PreviewID, Confirm: true})
		if err != nil || again != r {
			t.Fatalf("repeat apply was not idempotent: %+v err=%v", again, err)
		}
		if _, err = service.Receipt(ctx, 8, p.PreviewID); !errors.Is(err, ErrReceiptNotFound) {
			t.Fatalf("foreign actor may read durable receipt: %v", err)
		}
	}
	if got := integrationCount(t, f.pg, `SELECT count(*) FROM v2_user`); got != 504 {
		t.Fatalf("duplicate import or missing rows: %d", got)
	}
	if got := integrationCount(t, f.pg, `SELECT count(*) FROM v2_user_migration_receipt`); got != 1 {
		t.Fatalf("idempotency produced %d receipts", got)
	}
	if got := integrationCount(t, f.pg, `SELECT count(*) FROM v2_user WHERE email IN ($1,$2)`, admin.Email, staff.Email); got != 0 {
		t.Fatal("source privileged users were imported")
	}
	if err = f.pg.QueryRowContext(ctx, `SELECT row_to_json(target_row)::text FROM v2_user target_row WHERE email=$1`, existing.Email).Scan(&afterExisting); err != nil || beforeExisting != afterExisting {
		t.Fatalf("existing target user changed: err=%v", err)
	}
	var balance, commission, planID, groupID, quota, expiry, upload, download, device, speed, isAdmin, isStaff int64
	var password, token, uuid string
	if err = f.pg.QueryRowContext(ctx, `SELECT balance,commission_balance,plan_id,group_id,transfer_enable,expired_at,u,d,device_limit,speed_limit,password,token,uuid,is_admin,is_staff FROM v2_user WHERE email=$1`, locked.Email).Scan(&balance, &commission, &planID, &groupID, &quota, &expiry, &upload, &download, &device, &speed, &password, &token, &uuid, &isAdmin, &isStaff); err != nil {
		t.Fatal(err)
	}
	if balance != locked.Balance || commission != locked.CommissionBalance || planID != 9 || groupID != 3 || quota != locked.TransferEnable || expiry != *locked.ExpiredAt || upload != locked.U || download != locked.D || device != 4 || speed != 100 || password != locked.Password || token != locked.Token || uuid != locked.UUID || isAdmin != 0 || isStaff != 0 {
		t.Fatal("mapped user lost source assets/identity or retained locked balance/privileges")
	}
	var noPlan, noGroup, noExpiry, noDevice, noSpeed sql.NullInt64
	if err = f.pg.QueryRowContext(ctx, `SELECT plan_id,group_id,expired_at,device_limit,speed_limit,transfer_enable,u,d FROM v2_user WHERE email=$1`, planless.Email).Scan(&noPlan, &noGroup, &noExpiry, &noDevice, &noSpeed, &quota, &upload, &download); err != nil {
		t.Fatal(err)
	}
	if noPlan.Valid || noGroup.Valid || noExpiry.Valid || noDevice.Valid || noSpeed.Valid || quota != planless.TransferEnable || upload != planless.U || download != planless.D {
		t.Fatal("planless user was granted plan/group or lost exact NULL expiry/traffic")
	}
	if got := integrationCount(t, f.pg, `SELECT count(*) FROM v2_runtime_kv WHERE k LIKE 'TRAFFIC_RESET_CYCLE_USER_%'`); got != 502 {
		t.Fatalf("traffic markers missing across batch boundary: %d", got)
	}
	var beforeUsage, afterUsage string
	if err = f.pg.QueryRowContext(ctx, `SELECT json_agg(row(id,u,d) ORDER BY id)::text FROM v2_user`).Scan(&beforeUsage); err != nil {
		t.Fatal(err)
	}
	sweep, err := usersvc.NewDBService(config.Config{ResetTrafficMethod: 0}, f.pg).SweepTrafficResets(ctx)
	if err != nil || sweep.Scanned != 502 || sweep.Reset != 0 || sweep.Skipped != 502 {
		t.Fatalf("migration markers did not protect usage: %+v err=%v", sweep, err)
	}
	if err = f.pg.QueryRowContext(ctx, `SELECT json_agg(row(id,u,d) ORDER BY id)::text FROM v2_user`).Scan(&afterUsage); err != nil || beforeUsage != afterUsage {
		t.Fatalf("first traffic sweep changed usage: %v", err)
	}
	// Use the actual application's login and fresh-session code, not just a
	// bcrypt comparison, to prove compatibility of the imported hash/algorithm.
	loginConfig := config.Config{AppKey: "integration-test-signing-key-not-a-real-deployment"}
	login, err := passport.NewDBServiceWithConfig(loginConfig, f.pg).Login(ctx, passport.LoginRequest{Email: strings.ToUpper(locked.Email), Password: "test-password-not-a-real-account", IP: "127.0.0.1", UserAgent: "migration-integration-test"})
	if err != nil || login.AuthData == "" {
		t.Fatalf("real login rejected imported bcrypt account: %v", err)
	}
	identity, err := session.NewDBService(loginConfig, f.pg).AuthenticateFresh(ctx, login.AuthData, false)
	if err != nil || identity == nil || identity.Email != locked.Email || identity.IsAdmin != 0 || identity.IsStaff != 0 {
		t.Fatalf("imported user's real session is invalid: %v", err)
	}
	if got := integrationCount(t, f.source, `SELECT locking_balance FROM v2_user WHERE id=?`, locked.ID); got != locked.LockingBalance {
		t.Fatal("source locked commission was modified")
	}
	if got := integrationCount(t, f.source, `SELECT count(*) FROM v2_user`); got != 506 {
		t.Fatal("source user rows were modified")
	}
}

func TestMigrationIntegrationSecondBatchFailureRollsBackEverything(t *testing.T) {
	f := newMigrationIntegrationFixture(t)
	f.seedSource(t, f.users(t, 501))
	p := f.preview(t)
	ctx := context.Background()
	// Sequence increments survive rollback, proving the failure really occurs
	// after 500 inserts in batch one rather than before any write took place.
	for _, statement := range []string{
		`CREATE SEQUENCE integration_insert_attempts`,
		`CREATE FUNCTION integration_reject_second_batch() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM nextval('integration_insert_attempts'); IF NEW.email = 'person501@example.test' THEN RAISE EXCEPTION 'intentional integration second-batch failure'; END IF; RETURN NEW; END $$`,
		`CREATE TRIGGER integration_second_batch_failure BEFORE INSERT ON v2_user FOR EACH ROW EXECUTE FUNCTION integration_reject_second_batch()`,
	} {
		if _, err := f.pg.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.service.Apply(ctx, 7, ApplyRequest{PreviewID: p.PreviewID, Confirm: true}); !errors.Is(err, ErrTargetWrite) {
		t.Fatalf("expected definite target write rollback: %v", err)
	}
	if got := integrationCount(t, f.pg, `SELECT last_value FROM integration_insert_attempts`); got != 501 {
		t.Fatalf("failure was not in second batch: %d attempted inserts", got)
	}
	for _, table := range []string{"v2_user", "v2_runtime_kv", "v2_user_migration_receipt"} {
		if got := integrationCount(t, f.pg, `SELECT count(*) FROM `+table); got != 0 {
			t.Fatalf("partial transaction left %d rows in %s", got, table)
		}
	}
	if _, err := f.service.Receipt(ctx, 7, p.PreviewID); !errors.Is(err, ErrReceiptNotFound) {
		t.Fatalf("failed import unexpectedly has completion receipt: %v", err)
	}
	if _, err := f.pg.ExecContext(ctx, `DROP TRIGGER integration_second_batch_failure ON v2_user`); err != nil {
		t.Fatal(err)
	}
	r, err := f.service.Apply(ctx, 7, ApplyRequest{PreviewID: p.PreviewID, Confirm: true})
	if err != nil || r.Imported != 501 || integrationCount(t, f.pg, `SELECT count(*) FROM v2_user`) != 501 {
		t.Fatalf("definitely rolled-back preview cannot safely retry: imported=%d err=%v", r.Imported, err)
	}
}

func TestMigrationIntegrationStalePlanBlocksBeforeUserWrites(t *testing.T) {
	f := newMigrationIntegrationFixture(t)
	f.seedSource(t, f.users(t, 1))
	p := f.preview(t)
	ctx := context.Background()
	if _, err := f.pg.ExecContext(ctx, `UPDATE v2_plan SET group_id=33 WHERE id=9`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Apply(ctx, 7, ApplyRequest{PreviewID: p.PreviewID, Confirm: true}); !errors.Is(err, ErrPreviewStale) {
		t.Fatalf("changed target plan was silently imported: %v", err)
	}
	for _, table := range []string{"v2_user", "v2_runtime_kv", "v2_user_migration_receipt"} {
		if got := integrationCount(t, f.pg, `SELECT count(*) FROM `+table); got != 0 {
			t.Fatalf("stale preview wrote %d rows to %s", got, table)
		}
	}
}

func TestMigrationIntegrationSourceAccountNeedsOnlyTableSelect(t *testing.T) {
	f := newMigrationIntegrationFixture(t)
	f.seedSource(t, f.users(t, 2))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	suffix, err := randomHex(8)
	if err != nil {
		t.Fatal(err)
	}
	password, err := randomHex(24)
	if err != nil {
		t.Fatal(err)
	}
	username := "migration_ro_" + suffix
	// All interpolated names/passwords here are generated lowercase hex or a
	// fixed prefix. No caller-controlled identifier or DSN enters these DDLs.
	account := "'" + username + "'@'127.0.0.1'"
	if _, err = f.source.ExecContext(ctx, `CREATE USER `+account+` IDENTIFIED BY '`+password+`'`); err != nil {
		t.Fatalf("create isolated read-only fixture account: %v", err)
	}
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		if _, err := f.source.ExecContext(cleanup, `DROP USER `+account); err != nil {
			t.Errorf("drop isolated read-only fixture account: %v", err)
		}
	})
	for _, table := range []string{"v2_user", "v2_plan"} {
		if _, err = f.source.ExecContext(ctx, `GRANT SELECT ON `+f.config.Database+`.`+table+` TO `+account); err != nil {
			t.Fatalf("grant only SELECT on %s: %v", table, err)
		}
	}
	f.config.Username, f.config.Password = username, password
	reader, err := openSource(f.config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	var currentUser string
	if err = reader.QueryRowContext(ctx, `SELECT CURRENT_USER()`).Scan(&currentUser); err != nil || currentUser != username+"@127.0.0.1" {
		t.Fatalf("not connected with the exact SELECT-only account: err=%v", err)
	}
	before, err := f.service.sourceSnapshot(ctx, f.config)
	if err != nil {
		t.Fatalf("read-only account cannot inspect information_schema and source rows: %v", err)
	}
	_, err = reader.ExecContext(ctx, `UPDATE v2_user SET balance=balance+1 WHERE id=1`)
	var denied *mysql.MySQLError
	if !errors.As(err, &denied) || denied.Number != 1142 {
		t.Fatalf("source UPDATE was not rejected by table privilege enforcement: %v", err)
	}
	inspection, err := f.service.Inspect(ctx, 7, f.config)
	if err != nil || inspection.TotalUsers != 2 || len(inspection.SourcePlans) != 1 || inspection.SourcePlans[0].ID != 4 {
		t.Fatalf("SELECT-only inspect failed: %+v err=%v", inspection, err)
	}
	p := f.preview(t)
	r, err := f.service.Apply(ctx, 7, ApplyRequest{PreviewID: p.PreviewID, Confirm: true})
	if err != nil || r.Status != "completed" || r.Imported != 2 || integrationCount(t, f.pg, `SELECT count(*) FROM v2_user`) != 2 {
		t.Fatalf("SELECT-only source could not complete migration: imported=%d err=%v", r.Imported, err)
	}
	after, err := f.service.sourceSnapshot(ctx, f.config)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("read-only migration modified source user/plan rows: %v", err)
	}
}

func TestMigrationIntegrationCredentialRegenerationPersistsAndIsIdempotent(t *testing.T) {
	for _, tc := range []struct {
		name          string
		tokens, uuids bool
	}{
		{"preserve", false, false},
		{"tokens", true, false},
		{"uuids", false, true},
		{"both", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newMigrationIntegrationFixture(t)
			ctx := context.Background()
			users := f.users(t, 3)
			admin, staff, existing, skipped := sampleUser(10), sampleUser(11), sampleUser(12), sampleUser(13)
			admin.IsAdmin, staff.IsStaff = 1, 1
			skipped.PlanID = i64(5)
			users = append(users, admin, staff, existing, skipped)
			f.seedSource(t, users)
			// The first source user's token and UUID already belong to the target
			// account: disabled flags must still resolve identity collisions.
			if _, err := f.pg.ExecContext(ctx, `INSERT INTO v2_user (email,password,uuid,token,created_at,updated_at) VALUES ($1,'unchanged-target-password',$2,$3,1700000000,1700000001)`, existing.Email, users[0].UUID, users[0].Token); err != nil {
				t.Fatal(err)
			}
			var beforeExisting, afterExisting string
			if err := f.pg.QueryRowContext(ctx, `SELECT row_to_json(target_row)::text FROM v2_user target_row WHERE email=$1`, existing.Email).Scan(&beforeExisting); err != nil {
				t.Fatal(err)
			}
			beforeSource, err := f.service.sourceSnapshot(ctx, f.config)
			if err != nil {
				t.Fatal(err)
			}
			p, err := f.service.Preview(ctx, 7, PreviewRequest{
				Source: f.config, PlanMapping: map[string]int64{"4": 9, "5": 0},
				LockingBalancePolicy: "discard", ConfirmLockingBalance: true,
				RegenerateTokens: tc.tokens, RegenerateUUIDs: tc.uuids,
			})
			if err != nil || !p.CanApply || p.RegenerateTokens != tc.tokens || p.RegenerateUUIDs != tc.uuids {
				t.Fatalf("regeneration preview failed: %+v err=%v", p, err)
			}
			wantTokens, wantUUIDs := 1, 1
			if tc.tokens {
				wantTokens = 3
			}
			if tc.uuids {
				wantUUIDs = 3
			}
			if p.Counts.Importable != 3 || p.Counts.SkippedPrivileged != 2 || p.Counts.SkippedExisting != 1 || p.Counts.SkippedPlan != 1 || p.Counts.RegeneratedTokens != wantTokens || p.Counts.RegeneratedUUIDs != wantUUIDs {
				t.Fatalf("regeneration preview counts mismatch: %+v", p.Counts)
			}
			// Capture the generated in-memory identities before Apply clears the
			// sensitive snapshot: Apply must write these exact values, not reroll.
			expected := make(map[string]insertUser)
			for _, u := range f.service.previews[p.PreviewID].users {
				expected[u.Email] = u
			}
			r, err := f.service.Apply(ctx, 7, ApplyRequest{PreviewID: p.PreviewID, Confirm: true})
			if err != nil || r.Status != "completed" || r.Imported != 3 || r.Counts != p.Counts || r.RegenerateTokens != tc.tokens || r.RegenerateUUIDs != tc.uuids || r.SourceDigest != p.SourceDigest {
				t.Fatalf("regeneration apply lost settings: %+v err=%v", r, err)
			}
			for _, service := range []*DBService{f.service, NewDBService(config.Config{}, f.pg)} {
				got, err := service.Receipt(ctx, 7, p.PreviewID)
				if err != nil || got != r {
					t.Fatalf("durable regeneration receipt mismatch: %+v err=%v", got, err)
				}
				again, err := service.Apply(ctx, 7, ApplyRequest{PreviewID: p.PreviewID, Confirm: true})
				if err != nil || again != r {
					t.Fatalf("idempotent regeneration receipt mismatch: %+v err=%v", again, err)
				}
			}
			if integrationCount(t, f.pg, `SELECT count(*) FROM v2_user`) != 4 || integrationCount(t, f.pg, `SELECT count(*) FROM v2_user_migration_receipt`) != 1 {
				t.Fatal("repeat Apply inserted duplicate users or receipts")
			}
			for i, original := range users[:3] {
				var token, uuid, password string
				if err := f.pg.QueryRowContext(ctx, `SELECT token,uuid,password FROM v2_user WHERE email=$1`, original.Email).Scan(&token, &uuid, &password); err != nil {
					t.Fatal(err)
				}
				if token != expected[original.Email].Token || uuid != expected[original.Email].UUID || password != original.Password {
					t.Fatal("Apply or repeated Apply changed the preview's generated identities/password")
				}
				if tc.tokens || i == 0 {
					if !tokenPattern.MatchString(token) {
						t.Fatal("regenerated token has invalid format")
					}
					for _, old := range users {
						if token == old.Token {
							t.Fatal("target token reused a source credential")
						}
					}
				} else if token != original.Token {
					t.Fatal("disabled token option did not preserve compatible token")
				}
				if tc.uuids || i == 0 {
					if !uuidPattern.MatchString(uuid) {
						t.Fatal("regenerated UUID has invalid format")
					}
					for _, old := range users {
						if uuid == old.UUID {
							t.Fatal("target UUID reused a source credential")
						}
					}
				} else if uuid != original.UUID {
					t.Fatal("disabled UUID option did not preserve compatible UUID")
				}
			}
			if err := f.pg.QueryRowContext(ctx, `SELECT row_to_json(target_row)::text FROM v2_user target_row WHERE email=$1`, existing.Email).Scan(&afterExisting); err != nil || beforeExisting != afterExisting {
				t.Fatalf("regeneration changed an existing target user: %v", err)
			}
			afterSource, err := f.service.sourceSnapshot(ctx, f.config)
			if err != nil || !reflect.DeepEqual(beforeSource, afterSource) {
				t.Fatalf("credential regeneration modified the source database: %v", err)
			}
		})
	}
}

func TestMigrationIntegrationRegenerationSettingsAreBoundToPreviewDigest(t *testing.T) {
	f := newMigrationIntegrationFixture(t)
	f.seedSource(t, f.users(t, 1))
	seen := make(map[string]bool)
	for _, options := range []regenerationOptions{{}, {tokens: true}, {uuids: true}, {tokens: true, uuids: true}} {
		p, err := f.service.Preview(context.Background(), 7, PreviewRequest{
			Source: f.config, PlanMapping: map[string]int64{"4": 9},
			RegenerateTokens: options.tokens, RegenerateUUIDs: options.uuids,
		})
		if err != nil || !p.CanApply || p.SourceDigest == "" {
			t.Fatalf("preview failed: %+v err=%v", p, err)
		}
		if seen[p.SourceDigest] {
			t.Fatal("different regeneration settings have the same preview digest")
		}
		seen[p.SourceDigest] = true
		f.service.expirePreview(p.PreviewID)
	}
}
