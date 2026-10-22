package usermigration

import (
	"context"
	"database/sql"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/go-sql-driver/mysql"
)

func normalizedSource(c SourceConfig) (SourceConfig, error) {
	c.Host, c.Database, c.Username = strings.TrimSpace(c.Host), strings.TrimSpace(c.Database), strings.TrimSpace(c.Username)
	if c.Port == 0 {
		c.Port = 3306
	}
	if c.Host == "" || len(c.Host) > 253 || c.Port < 1 || c.Port > 65535 || c.Database == "" || len(c.Database) > 64 || c.Username == "" || len(c.Username) > 128 || len(c.Password) > 4096 {
		return SourceConfig{}, ErrInvalidParameter
	}
	for _, value := range []string{c.Host, c.Database, c.Username} {
		if strings.IndexFunc(value, unicode.IsControl) >= 0 {
			return SourceConfig{}, ErrInvalidParameter
		}
	}
	if strings.ContainsAny(c.Host, "/\\@?#[]() ") || strings.Contains(c.Host, ":") && net.ParseIP(c.Host) == nil {
		return SourceConfig{}, ErrInvalidParameter
	}
	return c, nil
}

func openSource(c SourceConfig) (*sql.DB, error) {
	cfg := mysql.NewConfig()
	// The driver may log raw server/authentication errors independently of our
	// sanitized API errors. Keep this one-off source connection silent as well.
	cfg.Logger = &mysql.NopLogger{}
	cfg.Net = "tcp"
	cfg.Addr = net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	cfg.DBName, cfg.User, cfg.Passwd = c.Database, c.Username, c.Password
	cfg.Timeout = 10 * time.Second
	cfg.ReadTimeout = 60 * time.Second
	cfg.WriteTimeout = 10 * time.Second
	cfg.Params = map[string]string{"charset": "utf8mb4"}
	cfg.MultiStatements = false
	cfg.AllowAllFiles = false
	cfg.AllowCleartextPasswords = false
	cfg.InterpolateParams = false
	if c.TLS {
		cfg.TLSConfig = "true"
	}
	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		return nil, ErrSourceConnection
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(0)
	db.SetConnMaxLifetime(2 * time.Minute)
	return db, nil
}

// Every source query runs in the same READ ONLY / REPEATABLE READ transaction.
// Only the two fixed tables and a fixed allowlist of columns are queried. Never
// accept a DSN, SQL fragment, table prefix or filename from the HTTP caller.
func (s *DBService) sourceSnapshot(ctx context.Context, c SourceConfig) (snapshot, error) {
	c, err := normalizedSource(c)
	if err != nil {
		return snapshot{}, err
	}
	db, err := s.openSource(c)
	c.Password = ""
	if err != nil {
		return snapshot{}, ErrSourceConnection
	}
	defer db.Close()
	if err = db.PingContext(ctx); err != nil {
		return snapshot{}, ErrSourceConnection
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return snapshot{}, ErrSourceRead
	}
	defer tx.Rollback()
	if err := verifySourceEngines(ctx, tx); err != nil {
		return snapshot{}, err
	}
	columns, err := sourceColumns(ctx, tx)
	if err != nil {
		return snapshot{}, err
	}
	for _, required := range []string{"id", "email", "password", "balance", "commission_balance", "u", "d", "transfer_enable", "is_admin", "is_staff", "banned", "plan_id", "expired_at", "token", "uuid"} {
		if !columns[required] {
			return snapshot{}, ErrSourceSchema
		}
	}
	required := func(name string) string { return "`" + name + "`" }
	optional := func(name, fallback string) string {
		if columns[name] {
			return required(name)
		}
		return fallback
	}
	num := func(name string) string { return "COALESCE(" + optional(name, "0") + ",0)" }
	str := func(name string, limit int) string {
		return "LEFT(COALESCE(" + optional(name, "''") + ",'')," + strconv.Itoa(limit) + ")"
	}
	fields := []string{required("id"), str("email", 256), str("password", 256), str("password_algo", 32), str("token", 128), str("uuid", 128), num("balance"), num("commission_balance"), num("locking_balance"), num("u"), num("d"), num("t"), num("transfer_enable"), required("plan_id"), required("expired_at"), num("banned"), num("is_admin"), num("is_staff"), num("created_at"), num("updated_at")}
	rows, err := tx.QueryContext(ctx, "SELECT "+strings.Join(fields, ",")+" FROM `v2_user` ORDER BY `id` LIMIT ?", MaxSourceUsers+1)
	if err != nil {
		return snapshot{}, ErrSourceRead
	}
	result := snapshot{}
	for rows.Next() {
		var u sourceUser
		if err = rows.Scan(&u.ID, &u.Email, &u.Password, &u.PasswordAlgo, &u.Token, &u.UUID, &u.Balance, &u.CommissionBalance, &u.LockingBalance, &u.U, &u.D, &u.T, &u.TransferEnable, &u.PlanID, &u.ExpiredAt, &u.Banned, &u.IsAdmin, &u.IsStaff, &u.CreatedAt, &u.UpdatedAt); err != nil {
			rows.Close()
			return snapshot{}, ErrSourceRead
		}
		result.users = append(result.users, u)
		if len(result.users) > MaxSourceUsers {
			rows.Close()
			return snapshot{}, ErrSourceLimit
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return snapshot{}, ErrSourceRead
	}
	rows, err = tx.QueryContext(ctx, "SELECT `id`, LEFT(`name`,255), COALESCE(`transfer_enable`,0) FROM `v2_plan` ORDER BY `id` LIMIT 10001")
	if err != nil {
		return snapshot{}, ErrSourceRead
	}
	planCounts := map[int64]int{}
	for _, u := range result.users {
		if u.PlanID != nil && *u.PlanID > 0 {
			planCounts[*u.PlanID]++
		}
	}
	for rows.Next() {
		var p Plan
		if err = rows.Scan(&p.ID, &p.Name, &p.TransferEnable); err != nil {
			rows.Close()
			return snapshot{}, ErrSourceRead
		}
		p.Users = planCounts[p.ID]
		result.plans = append(result.plans, p)
		if len(result.plans) > 10000 {
			rows.Close()
			return snapshot{}, ErrSourceSchema
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return snapshot{}, ErrSourceRead
	}
	present := map[int64]bool{}
	for _, p := range result.plans {
		present[p.ID] = true
	}
	for id, count := range planCounts {
		if !present[id] {
			result.plans = append(result.plans, Plan{ID: id, Name: "源套餐已删除（ID " + strconv.FormatInt(id, 10) + "）", Users: count})
		}
	}
	sort.Slice(result.plans, func(i, j int) bool { return result.plans[i].ID < result.plans[j].ID })
	if err = tx.Commit(); err != nil {
		return snapshot{}, ErrSourceRead
	}
	return result, nil
}

func sourceColumns(ctx context.Context, tx *sql.Tx) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, "SELECT COLUMN_NAME FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='v2_user'")
	if err != nil {
		return nil, ErrSourceRead
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var name string
		if rows.Scan(&name) != nil {
			return nil, ErrSourceRead
		}
		out[name] = true
	}
	if rows.Err() != nil {
		return nil, ErrSourceRead
	}
	return out, nil
}

func verifySourceEngines(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, "SELECT TABLE_NAME,ENGINE FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME IN ('v2_user','v2_plan')")
	if err != nil {
		return ErrSourceRead
	}
	defer rows.Close()
	found := map[string]bool{}
	for rows.Next() {
		var name, engine string
		if rows.Scan(&name, &engine) != nil {
			return ErrSourceRead
		}
		if !strings.EqualFold(engine, "InnoDB") {
			return ErrSourceSchema
		}
		found[name] = true
	}
	if rows.Err() != nil {
		return ErrSourceRead
	}
	if !found["v2_user"] || !found["v2_plan"] {
		return ErrSourceSchema
	}
	return nil
}
