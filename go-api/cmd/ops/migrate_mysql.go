package main

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	mysqlcfg "github.com/go-sql-driver/mysql"
)

type legacyMySQLConfig struct {
	Host     string
	Port     string
	Database string
	Username string
	Password string
	Charset  string
}

type tableColumnPlan struct {
	Name string
	Cast string
}

type tableCopyPlan struct {
	Table          string
	Columns        []tableColumnPlan
	IdentityColumn string
}

func runMigrateMySQL(args []string) error {
	flags := flag.NewFlagSet("migrate-mysql", flag.ContinueOnError)
	sourceEnv := flags.String("source-env", "", "legacy MySQL env file path")
	targetDSN := flags.String("target-dsn", "", "PostgreSQL DSN")
	installSQL := flags.String("install-sql", defaultSQLPath("install.pgsql.sql"), "PostgreSQL install SQL path")
	resetTarget := flags.Bool("reset-target", false, "reset PostgreSQL target before import")
	if err := flags.Parse(args); err != nil {
		return err
	}

	if strings.TrimSpace(*sourceEnv) == "" {
		return fmt.Errorf("source env is required")
	}

	resolvedTargetDSN, err := resolveDSN(*targetDSN)
	if err != nil {
		return err
	}

	targetDB, err := openDB(resolvedTargetDSN)
	if err != nil {
		return err
	}
	defer targetDB.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	hasRows, err := postgresTargetHasRows(ctx, targetDB)
	if err != nil {
		return err
	}
	if hasRows {
		if *resetTarget {
			if err := resetPostgresTarget(ctx, targetDB); err != nil {
				return err
			}
		} else {
			fmt.Println("migration_status=skipped")
			fmt.Println("migration_reason=target_not_empty")
			return nil
		}
	}

	envValues, err := parseSimpleEnvFile(*sourceEnv)
	if err != nil {
		return err
	}
	sourceCfg, err := legacyMySQLConfigFromEnv(envValues)
	if err != nil {
		return err
	}

	sourceDB, err := openMySQL(sourceCfg.DSN())
	if err != nil {
		return err
	}
	defer sourceDB.Close()

	ok, failed, err := execSQLFile(ctx, targetDB, *installSQL)
	if err != nil {
		return err
	}
	fmt.Printf("schema bootstrap finished: success=%d failed=%d\n", ok, failed)

	tablesCopied, rowsCopied, err := migrateMySQLIntoPostgres(ctx, sourceDB, targetDB)
	if err != nil {
		return err
	}

	fmt.Println("migration_status=applied")
	fmt.Printf("migration_tables=%d\n", tablesCopied)
	fmt.Printf("migration_rows=%d\n", rowsCopied)
	return nil
}

func resetPostgresTarget(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `DROP SCHEMA IF EXISTS public CASCADE`); err != nil {
		if !shouldFallbackToTableDrop(err) {
			return fmt.Errorf("drop public schema: %w", err)
		}
		schemaErr := err
		if fallbackErr := resetPostgresTables(ctx, db); fallbackErr != nil {
			return fmt.Errorf("drop public schema: %v; fallback table reset: %w", schemaErr, fallbackErr)
		}
		return nil
	}
	if _, err := db.ExecContext(ctx, `CREATE SCHEMA public`); err != nil {
		return fmt.Errorf("create public schema: %w", err)
	}
	if _, err := db.ExecContext(ctx, `GRANT ALL ON SCHEMA public TO CURRENT_USER`); err != nil {
		return fmt.Errorf("grant public schema to current user: %w", err)
	}
	return nil
}

func shouldFallbackToTableDrop(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "must be owner of schema public") || strings.Contains(msg, "sqlstate 42501")
}

func resetPostgresTables(ctx context.Context, db *sql.DB) error {
	tables, err := postgresTableNames(ctx, db)
	if err != nil {
		return err
	}
	for i := len(tables) - 1; i >= 0; i-- {
		query := fmt.Sprintf(`DROP TABLE IF EXISTS %s CASCADE`, quotePGIdent(tables[i]))
		if _, err := db.ExecContext(ctx, query); err != nil {
			return fmt.Errorf("drop table %s: %w", tables[i], err)
		}
	}
	return nil
}

func openMySQL(dsn string) (*sql.DB, error) {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("open mysql: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping mysql: %w", err)
	}
	return db, nil
}

func parseSimpleEnvFile(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open env file %s: %w", path, err)
	}
	defer file.Close()

	values := make(map[string]string)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || !strings.Contains(line, "=") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(strings.TrimSuffix(value, "\r"))
		if len(value) >= 2 {
			if (value[0] == '\'' && value[len(value)-1] == '\'') || (value[0] == '"' && value[len(value)-1] == '"') {
				value = value[1 : len(value)-1]
			}
		}
		values[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan env file %s: %w", path, err)
	}
	return values, nil
}

func legacyMySQLConfigFromEnv(values map[string]string) (legacyMySQLConfig, error) {
	connection := strings.TrimSpace(values["DB_CONNECTION"])
	if connection != "" && connection != "mysql" {
		return legacyMySQLConfig{}, fmt.Errorf("legacy env is not mysql: %s", connection)
	}

	cfg := legacyMySQLConfig{
		Host:     strings.TrimSpace(defaultValue(values["DB_HOST"], "127.0.0.1")),
		Port:     strings.TrimSpace(defaultValue(values["DB_PORT"], "3306")),
		Database: strings.TrimSpace(values["DB_DATABASE"]),
		Username: strings.TrimSpace(values["DB_USERNAME"]),
		Password: values["DB_PASSWORD"],
		Charset:  strings.TrimSpace(defaultValue(values["DB_CHARSET"], "utf8mb4")),
	}
	if cfg.Database == "" || cfg.Username == "" {
		return legacyMySQLConfig{}, fmt.Errorf("legacy mysql config missing DB_DATABASE or DB_USERNAME")
	}
	return cfg, nil
}

func (c legacyMySQLConfig) DSN() string {
	cfg := mysqlcfg.NewConfig()
	cfg.User = c.Username
	cfg.Passwd = c.Password
	cfg.Net = "tcp"
	cfg.Addr = c.Host + ":" + c.Port
	cfg.DBName = c.Database
	cfg.ParseTime = true
	cfg.MultiStatements = true
	cfg.Params = map[string]string{
		"charset": c.Charset,
	}
	return cfg.FormatDSN()
}

func postgresTargetHasRows(ctx context.Context, db *sql.DB) (bool, error) {
	tables, err := postgresTableNames(ctx, db)
	if err != nil {
		return false, err
	}
	for _, table := range tables {
		query := fmt.Sprintf(`SELECT 1 FROM %s LIMIT 1`, quotePGIdent(table))
		var marker int
		err := db.QueryRowContext(ctx, query).Scan(&marker)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("probe postgres table %s: %w", table, err)
		}
		return true, nil
	}
	return false, nil
}

func migrateMySQLIntoPostgres(ctx context.Context, sourceDB, targetDB *sql.DB) (int, int64, error) {
	sourceTables, err := mysqlTableNames(ctx, sourceDB)
	if err != nil {
		return 0, 0, err
	}
	targetTables, err := postgresTableNames(ctx, targetDB)
	if err != nil {
		return 0, 0, err
	}

	targetSet := make(map[string]struct{}, len(targetTables))
	for _, table := range targetTables {
		targetSet[table] = struct{}{}
	}

	copyTables := make([]string, 0, len(sourceTables))
	for _, table := range sourceTables {
		if _, ok := targetSet[table]; ok {
			copyTables = append(copyTables, table)
		}
	}
	sort.Strings(copyTables)

	tablesCopied := 0
	var rowsCopied int64
	for _, table := range copyTables {
		plan, ok, err := buildTableCopyPlan(ctx, sourceDB, targetDB, table)
		if err != nil {
			return tablesCopied, rowsCopied, err
		}
		if !ok {
			continue
		}
		count, err := copyTableRows(ctx, sourceDB, targetDB, plan)
		if err != nil {
			return tablesCopied, rowsCopied, err
		}
		tablesCopied++
		rowsCopied += count
	}
	return tablesCopied, rowsCopied, nil
}

func mysqlTableNames(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT table_name FROM information_schema.tables WHERE table_schema = DATABASE() AND table_type = 'BASE TABLE' ORDER BY table_name`)
	if err != nil {
		return nil, fmt.Errorf("list mysql tables: %w", err)
	}
	defer rows.Close()
	return scanSingleStringColumn(rows, "mysql tables")
}

func postgresTableNames(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT table_name FROM information_schema.tables WHERE table_schema = 'public' AND table_type = 'BASE TABLE' ORDER BY table_name`)
	if err != nil {
		return nil, fmt.Errorf("list postgres tables: %w", err)
	}
	defer rows.Close()
	return scanSingleStringColumn(rows, "postgres tables")
}

func scanSingleStringColumn(rows *sql.Rows, label string) ([]string, error) {
	items := make([]string, 0)
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, fmt.Errorf("scan %s: %w", label, err)
		}
		items = append(items, strings.TrimSpace(value))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate %s: %w", label, err)
	}
	return items, nil
}

func buildTableCopyPlan(ctx context.Context, sourceDB, targetDB *sql.DB, table string) (tableCopyPlan, bool, error) {
	sourceColumns, err := mysqlColumnSet(ctx, sourceDB, table)
	if err != nil {
		return tableCopyPlan{}, false, err
	}
	targetColumns, identityColumn, err := postgresColumnPlans(ctx, targetDB, table)
	if err != nil {
		return tableCopyPlan{}, false, err
	}

	plan := tableCopyPlan{Table: table, IdentityColumn: identityColumn}
	for _, item := range targetColumns {
		if _, ok := sourceColumns[item.Name]; ok {
			plan.Columns = append(plan.Columns, item)
		}
	}
	if len(plan.Columns) == 0 {
		return tableCopyPlan{}, false, nil
	}
	if !columnPlanContains(plan.Columns, plan.IdentityColumn) {
		plan.IdentityColumn = ""
	}
	return plan, true, nil
}

func mysqlColumnSet(ctx context.Context, db *sql.DB, table string) (map[string]struct{}, error) {
	rows, err := db.QueryContext(ctx, `SELECT column_name FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? ORDER BY ordinal_position`, table)
	if err != nil {
		return nil, fmt.Errorf("list mysql columns for %s: %w", table, err)
	}
	defer rows.Close()

	items, err := scanSingleStringColumn(rows, "mysql columns")
	if err != nil {
		return nil, err
	}
	set := make(map[string]struct{}, len(items))
	for _, item := range items {
		set[item] = struct{}{}
	}
	return set, nil
}

func postgresColumnPlans(ctx context.Context, db *sql.DB, table string) ([]tableColumnPlan, string, error) {
	rows, err := db.QueryContext(ctx, `SELECT column_name, data_type, udt_name, is_identity, column_default FROM information_schema.columns WHERE table_schema = 'public' AND table_name = $1 ORDER BY ordinal_position`, table)
	if err != nil {
		return nil, "", fmt.Errorf("list postgres columns for %s: %w", table, err)
	}
	defer rows.Close()

	plans := make([]tableColumnPlan, 0)
	identity := ""
	for rows.Next() {
		var name string
		var dataType string
		var udtName string
		var isIdentity string
		var columnDefault sql.NullString
		if err := rows.Scan(&name, &dataType, &udtName, &isIdentity, &columnDefault); err != nil {
			return nil, "", fmt.Errorf("scan postgres columns for %s: %w", table, err)
		}
		plans = append(plans, tableColumnPlan{
			Name: name,
			Cast: postgresCastType(dataType, udtName),
		})
		if identity == "" && (strings.EqualFold(strings.TrimSpace(isIdentity), "YES") || strings.Contains(strings.ToLower(columnDefault.String), "nextval(")) {
			identity = name
		}
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("iterate postgres columns for %s: %w", table, err)
	}
	return plans, identity, nil
}

func postgresCastType(dataType, udtName string) string {
	udt := strings.TrimSpace(strings.ToLower(udtName))
	switch udt {
	case "int2":
		return "smallint"
	case "int4":
		return "integer"
	case "int8":
		return "bigint"
	case "numeric":
		return "numeric"
	case "float4":
		return "real"
	case "float8":
		return "double precision"
	case "varchar", "text", "bpchar":
		return "text"
	case "bool":
		return "boolean"
	case "timestamp":
		return "timestamp"
	case "timestamptz":
		return "timestamptz"
	case "date":
		return "date"
	case "time":
		return "time"
	default:
		dataType = strings.TrimSpace(strings.ToLower(dataType))
		if dataType == "" {
			return "text"
		}
		return dataType
	}
}

func columnPlanContains(columns []tableColumnPlan, name string) bool {
	if strings.TrimSpace(name) == "" {
		return false
	}
	for _, item := range columns {
		if item.Name == name {
			return true
		}
	}
	return false
}

func copyTableRows(ctx context.Context, sourceDB, targetDB *sql.DB, plan tableCopyPlan) (int64, error) {
	sourceQuery := buildMySQLSelectSQL(plan)
	rows, err := sourceDB.QueryContext(ctx, sourceQuery)
	if err != nil {
		return 0, fmt.Errorf("query mysql table %s: %w", plan.Table, err)
	}
	defer rows.Close()

	tx, err := targetDB.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin postgres transaction for %s: %w", plan.Table, err)
	}

	insertSQL := buildPostgresInsertSQL(plan)
	stmt, err := tx.PrepareContext(ctx, insertSQL)
	if err != nil {
		_ = tx.Rollback()
		return 0, fmt.Errorf("prepare postgres insert for %s: %w", plan.Table, err)
	}
	defer stmt.Close()

	rawValues := make([]any, len(plan.Columns))
	scanTargets := make([]any, len(plan.Columns))
	for idx := range rawValues {
		scanTargets[idx] = &rawValues[idx]
	}

	var copied int64
	for rows.Next() {
		if err := rows.Scan(scanTargets...); err != nil {
			_ = tx.Rollback()
			return copied, fmt.Errorf("scan mysql row for %s: %w", plan.Table, err)
		}

		args := make([]any, len(plan.Columns))
		for idx, raw := range rawValues {
			normalized, err := normalizeMySQLValue(plan.Columns[idx].Cast, raw)
			if err != nil {
				_ = tx.Rollback()
				return copied, fmt.Errorf("normalize mysql value for %s.%s: %w", plan.Table, plan.Columns[idx].Name, err)
			}
			args[idx] = normalized
		}

		if _, err := stmt.ExecContext(ctx, args...); err != nil {
			_ = tx.Rollback()
			return copied, fmt.Errorf("insert postgres row for %s: %w", plan.Table, err)
		}
		copied++
	}
	if err := rows.Err(); err != nil {
		_ = tx.Rollback()
		return copied, fmt.Errorf("iterate mysql rows for %s: %w", plan.Table, err)
	}
	if err := tx.Commit(); err != nil {
		return copied, fmt.Errorf("commit postgres rows for %s: %w", plan.Table, err)
	}
	if plan.IdentityColumn != "" {
		if err := resetPostgresIdentity(ctx, targetDB, plan.Table, plan.IdentityColumn); err != nil {
			return copied, err
		}
	}
	return copied, nil
}

func buildMySQLSelectSQL(plan tableCopyPlan) string {
	columns := make([]string, 0, len(plan.Columns))
	for _, item := range plan.Columns {
		columns = append(columns, quoteMySQLIdent(item.Name))
	}
	return fmt.Sprintf(`SELECT %s FROM %s`, strings.Join(columns, ", "), quoteMySQLIdent(plan.Table))
}

func buildPostgresInsertSQL(plan tableCopyPlan) string {
	columns := make([]string, 0, len(plan.Columns))
	placeholders := make([]string, 0, len(plan.Columns))
	for idx, item := range plan.Columns {
		columns = append(columns, quotePGIdent(item.Name))
		placeholders = append(placeholders, fmt.Sprintf(`$%d::%s`, idx+1, item.Cast))
	}
	return fmt.Sprintf(`INSERT INTO %s (%s) VALUES (%s)`, quotePGIdent(plan.Table), strings.Join(columns, ", "), strings.Join(placeholders, ", "))
}

func normalizeMySQLValue(cast string, value any) (any, error) {
	if value == nil {
		return nil, nil
	}

	switch typed := value.(type) {
	case []byte:
		return normalizeMySQLStringValue(cast, string(typed))
	case string:
		return normalizeMySQLStringValue(cast, typed)
	case int64:
		return typed, nil
	case int32:
		return int64(typed), nil
	case int16:
		return int64(typed), nil
	case int8:
		return int64(typed), nil
	case int:
		return int64(typed), nil
	case uint64:
		return int64(typed), nil
	case uint32:
		return int64(typed), nil
	case uint16:
		return int64(typed), nil
	case uint8:
		return int64(typed), nil
	case uint:
		return int64(typed), nil
	case float64:
		return typed, nil
	case float32:
		return float64(typed), nil
	case bool:
		return typed, nil
	case time.Time:
		return typed, nil
	default:
		return typed, nil
	}
}

func normalizeMySQLStringValue(cast, raw string) (any, error) {
	trimmed := strings.TrimSpace(raw)
	switch strings.TrimSpace(strings.ToLower(cast)) {
	case "smallint", "integer", "bigint":
		if trimmed == "" {
			return nil, nil
		}
		parsed, err := strconv.ParseInt(trimmed, 10, 64)
		if err != nil {
			return nil, err
		}
		return parsed, nil
	case "numeric", "decimal", "real", "double precision":
		if trimmed == "" {
			return nil, nil
		}
		parsed, err := strconv.ParseFloat(trimmed, 64)
		if err != nil {
			return nil, err
		}
		return parsed, nil
	case "boolean":
		switch strings.ToLower(trimmed) {
		case "1", "true", "t", "yes":
			return true, nil
		case "0", "false", "f", "no", "":
			return false, nil
		default:
			return nil, fmt.Errorf("invalid boolean value %q", raw)
		}
	default:
		return raw, nil
	}
}

func resetPostgresIdentity(ctx context.Context, db *sql.DB, table, column string) error {
	query := fmt.Sprintf(`SELECT setval(pg_get_serial_sequence('public.%s', '%s'), COALESCE(MAX(%s), 1), COUNT(*) > 0) FROM %s`,
		table,
		column,
		quotePGIdent(column),
		quotePGIdent(table),
	)
	var nextValue int64
	if err := db.QueryRowContext(ctx, query).Scan(&nextValue); err != nil {
		return fmt.Errorf("reset postgres identity for %s.%s: %w", table, column, err)
	}
	return nil
}

func quotePGIdent(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func quoteMySQLIdent(value string) string {
	return "`" + strings.ReplaceAll(value, "`", "``") + "`"
}
