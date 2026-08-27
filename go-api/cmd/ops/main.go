package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	adminsvc "forest/go-api/internal/admin"
	"forest/go-api/internal/config"
	usersvc "forest/go-api/internal/user"

	_ "github.com/jackc/pgx/v5/stdlib"
	"golang.org/x/crypto/bcrypt"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "install":
		if err := runInstall(os.Args[2:]); err != nil {
			exitWithErr(err)
		}
	case "update":
		if err := runUpdate(os.Args[2:]); err != nil {
			exitWithErr(err)
		}
	case "create-admin":
		if err := runCreateAdmin(os.Args[2:]); err != nil {
			exitWithErr(err)
		}
	case "seed-demo":
		if err := runSeedDemo(os.Args[2:]); err != nil {
			exitWithErr(err)
		}
	case "reset-traffic":
		if err := runResetTraffic(os.Args[2:]); err != nil {
			exitWithErr(err)
		}
	case "migrate-config":
		if err := runMigrateConfig(os.Args[2:]); err != nil {
			exitWithErr(err)
		}
	case "migrate-mysql":
		if err := runMigrateMySQL(os.Args[2:]); err != nil {
			exitWithErr(err)
		}
	case "inspect-merge-mysql":
		if err := runInspectMergeMySQL(os.Args[2:]); err != nil {
			exitWithErr(err)
		}
	case "merge-mysql":
		if err := runMergeMySQL(os.Args[2:]); err != nil {
			exitWithErr(err)
		}
	case "gen-app-key":
		key, err := generateAppKey()
		if err != nil {
			exitWithErr(err)
		}
		fmt.Println(key)
	default:
		printUsage()
		os.Exit(1)
	}
}

func runInstall(args []string) error {
	flags := flag.NewFlagSet("install", flag.ContinueOnError)
	dsn := flags.String("dsn", "", "PostgreSQL DSN")
	sqlPath := flags.String("sql", defaultSQLPath("install.pgsql.sql"), "install SQL file path")
	adminEmail := flags.String("admin-email", strings.TrimSpace(os.Getenv("ADMIN_EMAIL")), "admin email")
	adminPassword := flags.String("admin-password", os.Getenv("ADMIN_PASSWORD"), "admin password (optional, auto-generate if empty)")
	if err := flags.Parse(args); err != nil {
		return err
	}

	resolvedDSN, err := resolveDSN(*dsn)
	if err != nil {
		return err
	}

	db, err := openDB(resolvedDSN)
	if err != nil {
		return err
	}
	defer db.Close()

	ok, failed, err := execSQLFile(context.Background(), db, *sqlPath)
	if err != nil {
		return err
	}
	fmt.Printf("schema install finished: success=%d failed=%d\n", ok, failed)

	if strings.TrimSpace(*adminEmail) == "" {
		return fmt.Errorf("admin email is required (use --admin-email or ADMIN_EMAIL)")
	}
	adminPass, err := upsertAdmin(context.Background(), db, *adminEmail, *adminPassword)
	if err != nil {
		return err
	}
	fmt.Printf("admin ready: email=%s password=%s\n", strings.TrimSpace(*adminEmail), adminPass)
	return nil
}

func runUpdate(args []string) error {
	flags := flag.NewFlagSet("update", flag.ContinueOnError)
	dsn := flags.String("dsn", "", "PostgreSQL DSN")
	sqlPath := flags.String("sql", defaultSQLPath("update.pgsql.sql"), "update SQL file path")
	if err := flags.Parse(args); err != nil {
		return err
	}

	resolvedDSN, err := resolveDSN(*dsn)
	if err != nil {
		return err
	}

	db, err := openDB(resolvedDSN)
	if err != nil {
		return err
	}
	defer db.Close()

	ok, failed, err := execSQLFile(context.Background(), db, *sqlPath)
	if err != nil {
		return err
	}
	if err := applyUpdateCompatFixes(context.Background(), db); err != nil {
		return err
	}
	if err := verifyRequiredUpdateSchema(context.Background(), db); err != nil {
		return err
	}
	fmt.Printf("schema update finished: success=%d failed=%d\n", ok, failed)
	return nil
}

func verifyRequiredUpdateSchema(ctx context.Context, db *sql.DB) error {
	for _, column := range []string{"checkout_result", "checkout_claim", "checkout_claim_expires_at", "checkout_fingerprint"} {
		exists, err := postgresColumnExists(ctx, db, "v2_order", column)
		if err != nil {
			return fmt.Errorf("verify required v2_order.%s column: %w", column, err)
		}
		if !exists {
			return fmt.Errorf("required database migration is incomplete: v2_order.%s is missing", column)
		}
	}
	for _, column := range []string{"order_id", "payment_id", "handling_amount", "amount", "checkout_result", "checkout_claim", "checkout_claim_expires_at", "checkout_fingerprint", "callback_no", "status", "paid_at"} {
		exists, err := postgresColumnExists(ctx, db, "v2_order_payment_attempt", column)
		if err != nil {
			return fmt.Errorf("verify required v2_order_payment_attempt.%s column: %w", column, err)
		}
		if !exists {
			return fmt.Errorf("required database migration is incomplete: v2_order_payment_attempt.%s is missing", column)
		}
	}
	return nil
}

func runCreateAdmin(args []string) error {
	flags := flag.NewFlagSet("create-admin", flag.ContinueOnError)
	dsn := flags.String("dsn", "", "PostgreSQL DSN")
	adminEmail := flags.String("admin-email", strings.TrimSpace(os.Getenv("ADMIN_EMAIL")), "admin email")
	adminPassword := flags.String("admin-password", os.Getenv("ADMIN_PASSWORD"), "admin password (optional, auto-generate if empty)")
	if err := flags.Parse(args); err != nil {
		return err
	}

	if strings.TrimSpace(*adminEmail) == "" {
		return fmt.Errorf("admin email is required (use --admin-email or ADMIN_EMAIL)")
	}

	resolvedDSN, err := resolveDSN(*dsn)
	if err != nil {
		return err
	}

	db, err := openDB(resolvedDSN)
	if err != nil {
		return err
	}
	defer db.Close()

	adminPass, err := upsertAdmin(context.Background(), db, *adminEmail, *adminPassword)
	if err != nil {
		return err
	}
	fmt.Printf("admin ready: email=%s password=%s\n", strings.TrimSpace(*adminEmail), adminPass)
	return nil
}

func runResetTraffic(args []string) error {
	flags := flag.NewFlagSet("reset-traffic", flag.ContinueOnError)
	dsn := flags.String("dsn", "", "PostgreSQL DSN")
	if err := flags.Parse(args); err != nil {
		return err
	}

	resolvedDSN, err := resolveDSN(*dsn)
	if err != nil {
		return err
	}

	db, err := openDB(resolvedDSN)
	if err != nil {
		return err
	}
	defer db.Close()

	svc := usersvc.NewDBService(config.Load(), db)
	result, err := svc.ResetAllTrafficUsage(context.Background())
	if err != nil {
		return err
	}
	fmt.Printf("traffic reset finished: scanned=%d reset=%d marked_only=%d skipped=%d\n", result.Scanned, result.Reset, result.MarkedOnly, result.Skipped)
	return nil
}

func runMigrateConfig(args []string) error {
	flags := flag.NewFlagSet("migrate-config", flag.ContinueOnError)
	sourceRoot := flags.String("legacy-root", "", "legacy config source root")
	targetRoot := flags.String("target-root", defaultProjectRoot(), "target project root")
	if err := flags.Parse(args); err != nil {
		return err
	}

	migratedConfig, err := adminsvc.MigrateLegacyConfig(*sourceRoot, *targetRoot)
	if err != nil {
		return err
	}
	fmt.Printf("config migration finished: admin=%d\n", migratedConfig)
	return nil
}

func openDB(dsn string) (*sql.DB, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return db, nil
}

func execSQLFile(ctx context.Context, db *sql.DB, path string) (int, int, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, fmt.Errorf("read sql file %s: %w", path, err)
	}

	stmts := splitSQLStatements(string(content))
	if len(stmts) == 0 {
		return 0, 0, fmt.Errorf("no executable SQL statements found in %s", path)
	}

	ok := 0
	failed := 0
	for idx, stmt := range stmts {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			failed++
			fmt.Fprintf(os.Stderr, "sql failed #%d: %v\n", idx+1, err)
			continue
		}
		ok++
	}

	if failed > 0 {
		return ok, failed, fmt.Errorf("%d of %d SQL statements failed for %s", failed, len(stmts), path)
	}
	return ok, failed, nil
}

func applyUpdateCompatFixes(ctx context.Context, db *sql.DB) error {
	if err := bestEffortEnsureUpdateIndex(ctx, db, `CREATE INDEX IF NOT EXISTS idx_v2_runtime_kv_expire_at ON v2_runtime_kv(expire_at)`); err != nil {
		return err
	}
	if err := bestEffortEnsureUpdateIndex(ctx, db, `CREATE INDEX IF NOT EXISTS idx_v2_auth_session_user_id ON v2_auth_session(user_id)`); err != nil {
		return err
	}
	if err := bestEffortEnsureUpdateIndex(ctx, db, `CREATE INDEX IF NOT EXISTS idx_v2_order_payment_callback ON v2_order(payment_id, callback_no) WHERE callback_no IS NOT NULL`); err != nil {
		return err
	}
	if err := bestEffortEnsureUpdateColumn(
		ctx,
		db,
		"v2_server_v2node",
		"send_through",
		`ALTER TABLE v2_server_v2node ADD COLUMN send_through varchar(255) DEFAULT NULL`,
	); err != nil {
		return err
	}
	if err := bestEffortEnsureUpdateColumn(
		ctx,
		db,
		"v2_server_v2node",
		"ddns_settings",
		`ALTER TABLE v2_server_v2node ADD COLUMN ddns_settings text DEFAULT NULL`,
	); err != nil {
		return err
	}
	if err := bestEffortEnsureUpdateColumnType(
		ctx,
		db,
		"v2_plan",
		"transfer_enable",
		"bigint",
		`ALTER TABLE v2_plan ALTER COLUMN transfer_enable TYPE BIGINT USING transfer_enable::BIGINT`,
	); err != nil {
		return err
	}

	repairs := []struct {
		table  string
		column string
		query  string
		label  string
	}{
		{
			table:  "v2_user",
			column: "expired_at",
			query:  `UPDATE v2_user SET expired_at = NULL WHERE expired_at IS NOT NULL AND expired_at <= 0`,
			label:  "normalize v2_user.expired_at",
		},
		{
			table:  "v2_invite_code",
			column: "code",
			query:  `UPDATE v2_invite_code SET code = BTRIM(code)`,
			label:  "trim v2_invite_code.code",
		},
		{
			table:  "v2_invite_campaign",
			column: "invite_code",
			query:  `UPDATE v2_invite_campaign SET invite_code = CASE WHEN invite_code IS NULL THEN NULL ELSE BTRIM(invite_code) END`,
			label:  "trim v2_invite_campaign.invite_code",
		},
		{
			table:  "v2_invite_campaign_record",
			column: "invite_code",
			query:  `UPDATE v2_invite_campaign_record SET invite_code = BTRIM(invite_code)`,
			label:  "trim v2_invite_campaign_record.invite_code",
		},
	}

	for _, repair := range repairs {
		exists, err := postgresColumnExists(ctx, db, repair.table, repair.column)
		if err != nil {
			return fmt.Errorf("check %s: %w", repair.label, err)
		}
		if !exists {
			continue
		}
		if _, err := db.ExecContext(ctx, repair.query); err != nil {
			return fmt.Errorf("%s: %w", repair.label, err)
		}
	}

	return nil
}

func bestEffortEnsureUpdateColumnType(ctx context.Context, db *sql.DB, tableName, columnName, wantDataType, stmt string) error {
	dataType, err := postgresColumnDataType(ctx, db, tableName, columnName)
	if err != nil {
		return fmt.Errorf("check column type %s.%s: %w", tableName, columnName, err)
	}
	if dataType == "" || strings.EqualFold(dataType, wantDataType) {
		return nil
	}
	if _, err := db.ExecContext(ctx, stmt); err != nil {
		if isIgnorableOwnerError(err) {
			return nil
		}
		return fmt.Errorf("ensure column type %s.%s: %w", tableName, columnName, err)
	}
	return nil
}

func bestEffortEnsureUpdateColumn(ctx context.Context, db *sql.DB, tableName, columnName, stmt string) error {
	exists, err := postgresColumnExists(ctx, db, tableName, columnName)
	if err != nil {
		return fmt.Errorf("check column %s.%s: %w", tableName, columnName, err)
	}
	if exists {
		return nil
	}
	if _, err := db.ExecContext(ctx, stmt); err != nil {
		if isIgnorableOwnerError(err) {
			return nil
		}
		return fmt.Errorf("ensure column %s.%s: %w", tableName, columnName, err)
	}
	return nil
}

func bestEffortEnsureUpdateIndex(ctx context.Context, db *sql.DB, stmt string) error {
	if _, err := db.ExecContext(ctx, stmt); err != nil {
		if isIgnorableOwnerError(err) {
			return nil
		}
		return fmt.Errorf("ensure update index: %w", err)
	}
	return nil
}

func postgresColumnDataType(ctx context.Context, db *sql.DB, tableName, columnName string) (string, error) {
	const query = `SELECT data_type FROM information_schema.columns
WHERE table_schema = CURRENT_SCHEMA() AND table_name = $1 AND column_name = $2`
	var dataType string
	if err := db.QueryRowContext(ctx, query, tableName, columnName).Scan(&dataType); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return dataType, nil
}

func postgresColumnExists(ctx context.Context, db *sql.DB, tableName, columnName string) (bool, error) {
	const query = `SELECT EXISTS (
SELECT 1 FROM information_schema.columns
WHERE table_schema = CURRENT_SCHEMA() AND table_name = $1 AND column_name = $2
)`
	var exists bool
	if err := db.QueryRowContext(ctx, query, tableName, columnName).Scan(&exists); err != nil {
		return false, err
	}
	return exists, nil
}

func isIgnorableOwnerError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "must be owner of table") ||
		strings.Contains(message, "must be owner of relation") ||
		strings.Contains(message, "permission denied for table") ||
		strings.Contains(message, "permission denied for relation")
}

func splitSQLStatements(raw string) []string {
	stmts := make([]string, 0)
	var builder strings.Builder
	var singleQuoted bool
	var singleQuoteBackslashEscapes bool
	var doubleQuoted bool
	var lineComment bool
	var blockCommentDepth int
	var dollarTag string

	appendStatement := func() {
		stmt := strings.TrimSpace(builder.String())
		if stmt != "" {
			stmts = append(stmts, stmt)
		}
		builder.Reset()
	}

	for i := 0; i < len(raw); {
		if lineComment {
			if raw[i] == '\n' {
				builder.WriteByte('\n')
				lineComment = false
			}
			i++
			continue
		}

		if blockCommentDepth > 0 {
			switch {
			case i+1 < len(raw) && raw[i:i+2] == "/*":
				blockCommentDepth++
				i += 2
			case i+1 < len(raw) && raw[i:i+2] == "*/":
				blockCommentDepth--
				i += 2
				if blockCommentDepth == 0 {
					builder.WriteByte(' ')
				}
			case raw[i] == '\n':
				builder.WriteByte('\n')
				i++
			default:
				i++
			}
			continue
		}

		if dollarTag != "" {
			if strings.HasPrefix(raw[i:], dollarTag) {
				builder.WriteString(dollarTag)
				i += len(dollarTag)
				dollarTag = ""
				continue
			}
			builder.WriteByte(raw[i])
			i++
			continue
		}

		if singleQuoted {
			builder.WriteByte(raw[i])
			if singleQuoteBackslashEscapes && raw[i] == '\\' && i+1 < len(raw) {
				builder.WriteByte(raw[i+1])
				i += 2
				continue
			}
			if raw[i] == '\'' {
				if i+1 < len(raw) && raw[i+1] == '\'' {
					builder.WriteByte(raw[i+1])
					i += 2
					continue
				}
				singleQuoted = false
				singleQuoteBackslashEscapes = false
			}
			i++
			continue
		}

		if doubleQuoted {
			builder.WriteByte(raw[i])
			if raw[i] == '"' {
				if i+1 < len(raw) && raw[i+1] == '"' {
					builder.WriteByte(raw[i+1])
					i += 2
					continue
				}
				doubleQuoted = false
			}
			i++
			continue
		}

		switch {
		case i+1 < len(raw) && raw[i:i+2] == "--":
			builder.WriteByte(' ')
			lineComment = true
			i += 2
		case i+1 < len(raw) && raw[i:i+2] == "/*":
			builder.WriteByte(' ')
			blockCommentDepth = 1
			i += 2
		case raw[i] == '\'':
			builder.WriteByte(raw[i])
			singleQuoted = true
			singleQuoteBackslashEscapes = postgresEscapeStringPrefix(raw, i)
			i++
		case raw[i] == '"':
			builder.WriteByte(raw[i])
			doubleQuoted = true
			i++
		case raw[i] == '$':
			tag, ok := postgresDollarQuoteTag(raw, i)
			if !ok {
				builder.WriteByte(raw[i])
				i++
				continue
			}
			builder.WriteString(tag)
			dollarTag = tag
			i += len(tag)
		case raw[i] == ';':
			builder.WriteByte(raw[i])
			i++
			appendStatement()
		default:
			builder.WriteByte(raw[i])
			i++
		}
	}

	appendStatement()
	return stmts
}

func postgresDollarQuoteTag(raw string, start int) (string, bool) {
	if start < 0 || start >= len(raw) || raw[start] != '$' || start+1 >= len(raw) {
		return "", false
	}
	if start > 0 && isPostgresIdentifierContinue(raw[start-1]) {
		return "", false
	}
	if raw[start+1] == '$' {
		return "$$", true
	}
	if !isPostgresDollarTagStart(raw[start+1]) {
		return "", false
	}

	for i := start + 2; i < len(raw); i++ {
		if raw[i] == '$' {
			return raw[start : i+1], true
		}
		if !isPostgresDollarTagContinue(raw[i]) {
			return "", false
		}
	}
	return "", false
}

func isPostgresDollarTagStart(value byte) bool {
	return value == '_' || value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z'
}

func isPostgresDollarTagContinue(value byte) bool {
	return isPostgresDollarTagStart(value) || value >= '0' && value <= '9'
}

func postgresEscapeStringPrefix(raw string, quote int) bool {
	if quote < 1 || raw[quote-1] != 'e' && raw[quote-1] != 'E' {
		return false
	}
	return quote == 1 || !isPostgresIdentifierContinue(raw[quote-2])
}

func isPostgresIdentifierContinue(value byte) bool {
	return isPostgresDollarTagContinue(value) || value == '$' || value >= 0x80
}

func upsertAdmin(ctx context.Context, db *sql.DB, email, password string) (string, error) {
	email = strings.TrimSpace(email)
	if !strings.Contains(email, "@") {
		return "", fmt.Errorf("invalid admin email")
	}

	pass := strings.TrimSpace(password)
	if pass == "" {
		generated, err := randomTokenHex(9)
		if err != nil {
			return "", fmt.Errorf("generate admin password: %w", err)
		}
		pass = generated
	}
	if len(pass) < 8 {
		return "", fmt.Errorf("admin password must be at least 8 characters")
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(pass), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("hash admin password: %w", err)
	}
	uuid, err := randomUUID()
	if err != nil {
		return "", err
	}
	token, err := randomTokenHex(16)
	if err != nil {
		return "", err
	}
	now := time.Now().Unix()

	const query = `INSERT INTO v2_user (email, password, is_admin, uuid, token, created_at, updated_at)
VALUES ($1, $2, 1, $3, $4, $5, $5)
ON CONFLICT (email) DO UPDATE
SET password = EXCLUDED.password, is_admin = 1, updated_at = EXCLUDED.updated_at`
	if _, err := db.ExecContext(ctx, query, email, string(hash), uuid, token, now); err != nil {
		return "", fmt.Errorf("upsert admin user: %w", err)
	}

	return pass, nil
}

func resolveDSN(explicit string) (string, error) {
	explicit = strings.TrimSpace(explicit)
	if explicit != "" {
		return explicit, nil
	}

	if envDSN := strings.TrimSpace(os.Getenv("POSTGRES_DSN")); envDSN != "" {
		return envDSN, nil
	}

	host := strings.TrimSpace(defaultValue(os.Getenv("DB_HOST"), "127.0.0.1"))
	port := strings.TrimSpace(defaultValue(os.Getenv("DB_PORT"), "5432"))
	database := strings.TrimSpace(os.Getenv("DB_DATABASE"))
	user := strings.TrimSpace(os.Getenv("DB_USERNAME"))
	password := os.Getenv("DB_PASSWORD")
	sslmode := strings.TrimSpace(defaultValue(os.Getenv("DB_SSLMODE"), "disable"))

	if database == "" || user == "" {
		return "", fmt.Errorf("missing database config: set POSTGRES_DSN or DB_DATABASE/DB_USERNAME")
	}

	parts := []string{
		connKV("host", host),
		connKV("port", port),
		connKV("user", user),
		connKV("dbname", database),
		connKV("sslmode", sslmode),
	}
	if strings.TrimSpace(password) != "" {
		parts = append(parts, connKV("password", password))
	}
	return strings.Join(parts, " "), nil
}

func connKV(key, value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	escaped := strings.ReplaceAll(value, "'", "\\'")
	if strings.ContainsAny(escaped, " \t") {
		return fmt.Sprintf("%s='%s'", key, escaped)
	}
	return fmt.Sprintf("%s=%s", key, escaped)
}

func defaultValue(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func generateAppKey() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate app key: %w", err)
	}
	return "base64:" + base64.StdEncoding.EncodeToString(buf), nil
}

func randomTokenHex(byteLength int) (string, error) {
	if byteLength <= 0 {
		return "", fmt.Errorf("invalid random length")
	}
	buf := make([]byte, byteLength)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate random token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func randomUUID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate uuid: %w", err)
	}
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	hexed := hex.EncodeToString(buf)
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		hexed[0:8],
		hexed[8:12],
		hexed[12:16],
		hexed[16:20],
		hexed[20:32],
	), nil
}

func defaultSQLPath(fileName string) string {
	cwd, err := os.Getwd()
	if err != nil {
		return filepath.Join("..", "database", fileName)
	}
	if strings.HasSuffix(cwd, string(filepath.Separator)+"go-api") {
		return filepath.Join("..", "database", fileName)
	}
	return filepath.Join("database", fileName)
}

func defaultProjectRoot() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ".."
	}
	if strings.HasSuffix(cwd, string(filepath.Separator)+"go-api") {
		return ".."
	}
	return "."
}

func printUsage() {
	fmt.Println(`Usage:
  go run ./cmd/ops install --admin-email=admin@example.com [--admin-password=xxx] [--dsn=...]
  go run ./cmd/ops update [--dsn=...]
  go run ./cmd/ops create-admin --admin-email=admin@example.com [--admin-password=xxx] [--dsn=...]
  go run ./cmd/ops seed-demo [--admin-email=admin@example.com] [--admin-password=xxx] [--dsn=...]
  go run ./cmd/ops reset-traffic [--dsn=...]
  go run ./cmd/ops migrate-config [--legacy-root=...] [--target-root=...]
  go run ./cmd/ops migrate-mysql --source-env=../.env [--target-dsn=...] [--install-sql=...]
  go run ./cmd/ops inspect-merge-mysql --source-host=127.0.0.1 --source-port=3306 --source-database=legacy --source-username=root --source-password=xxx [--target-dsn=...]
  go run ./cmd/ops merge-mysql --source-host=127.0.0.1 --source-port=3306 --source-database=legacy --source-username=root --source-password=xxx --plan-map=1:10,2:20 [--target-dsn=...]
  go run ./cmd/ops gen-app-key`)
}

func exitWithErr(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
