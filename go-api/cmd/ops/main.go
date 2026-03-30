package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	adminsvc "forest/go-api/internal/admin"

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
	case "migrate-config":
		if err := runMigrateConfig(os.Args[2:]); err != nil {
			exitWithErr(err)
		}
	case "migrate-mysql":
		if err := runMigrateMySQL(os.Args[2:]); err != nil {
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
	fmt.Printf("schema update finished: success=%d failed=%d\n", ok, failed)
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

func runMigrateConfig(args []string) error {
	flags := flag.NewFlagSet("migrate-config", flag.ContinueOnError)
	sourceRoot := flags.String("legacy-root", "", "legacy config source root")
	targetRoot := flags.String("target-root", defaultProjectRoot(), "target project root")
	if err := flags.Parse(args); err != nil {
		return err
	}

	migratedConfig, migratedThemes, err := adminsvc.MigrateLegacyConfig(*sourceRoot, *targetRoot)
	if err != nil {
		return err
	}
	fmt.Printf("config migration finished: admin=%d themes=%d\n", migratedConfig, migratedThemes)
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

	if ok == 0 {
		return ok, failed, fmt.Errorf("all SQL statements failed for %s", path)
	}
	return ok, failed, nil
}

func splitSQLStatements(raw string) []string {
	lines := strings.Split(raw, "\n")
	stmts := make([]string, 0, len(lines))
	var builder strings.Builder

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "--") {
			continue
		}

		if builder.Len() > 0 {
			builder.WriteByte('\n')
		}
		builder.WriteString(line)

		if strings.HasSuffix(trimmed, ";") {
			stmt := strings.TrimSpace(builder.String())
			if stmt != "" {
				stmts = append(stmts, stmt)
			}
			builder.Reset()
		}
	}

	if builder.Len() > 0 {
		stmt := strings.TrimSpace(builder.String())
		if stmt != "" {
			stmts = append(stmts, stmt)
		}
	}

	return stmts
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
  go run ./cmd/ops migrate-config [--legacy-root=...] [--target-root=...]
  go run ./cmd/ops migrate-mysql --source-env=../.env [--target-dsn=...] [--install-sql=...]
  go run ./cmd/ops gen-app-key`)
}

func exitWithErr(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
