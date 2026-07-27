package admin

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	mysqlcfg "github.com/go-sql-driver/mysql"
)

const xboardPreviewTTL = 30 * time.Minute

var xboardPlanMapping = map[int64]int64{1: 1, 2: 1, 3: 2, 4: 3, 5: 3, 6: 3, 7: 5, 8: 5, 9: 5}

type XBoardSourceConfig struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Database string `json:"database"`
	Username string `json:"username"`
	Password string `json:"password"`
}

type XBoardMigrationRequest struct {
	Source       XBoardSourceConfig `json:"source"`
	PreviewToken string             `json:"preview_token,omitempty"`
}

type XBoardPlanPreview struct {
	SourcePlanID int64  `json:"source_plan_id"`
	TargetPlanID int64  `json:"target_plan_id"`
	TargetName   string `json:"target_name"`
	Users        int64  `json:"users"`
}

type XBoardMigrationPreview struct {
	PreviewToken  string              `json:"preview_token"`
	ExpiresAt     int64               `json:"expires_at"`
	SourceUsers   int64               `json:"source_users"`
	Ready         int64               `json:"ready"`
	SkipAdmin     int64               `json:"skip_admin"`
	SkipNoPlan    int64               `json:"skip_no_plan"`
	SkipUnmapped  int64               `json:"skip_unmapped"`
	SkipConflict  int64               `json:"skip_conflict"`
	PlanBreakdown []XBoardPlanPreview `json:"plan_breakdown"`
}

type XBoardMigrationResult struct {
	BatchID      int64 `json:"batch_id"`
	Imported     int64 `json:"imported"`
	SkipAdmin    int64 `json:"skip_admin"`
	SkipNoPlan   int64 `json:"skip_no_plan"`
	SkipUnmapped int64 `json:"skip_unmapped"`
	SkipConflict int64 `json:"skip_conflict"`
	Failed       int64 `json:"failed"`
}

type xboardSourceUser struct {
	ID             int64
	Email          string
	Password       string
	PasswordAlgo   sql.NullString
	PasswordSalt   sql.NullString
	T              int64
	U              int64
	D              int64
	TransferEnable int64
	DeviceLimit    sql.NullInt64
	Banned         int64
	PlanID         sql.NullInt64
	SpeedLimit     sql.NullInt64
	RemindExpire   int64
	RemindTraffic  int64
	ExpiredAt      sql.NullInt64
	Remarks        sql.NullString
	CreatedAt      int64
	UpdatedAt      int64
	IsAdmin        int64
	IsStaff        int64
}

type xboardScan struct {
	Users       []xboardSourceUser
	TargetPlans map[int64]xboardTargetPlan
	Conflicts   map[string]struct{}
	Preview     XBoardMigrationPreview
	Fingerprint string
}

type xboardTargetPlan struct {
	ID          int64
	Name        string
	GroupID     sql.NullInt64
	DeviceLimit sql.NullInt64
}

func (s *DBService) PreviewXBoardMigration(ctx context.Context, req XBoardMigrationRequest) (XBoardMigrationPreview, error) {
	if s.db == nil {
		return XBoardMigrationPreview{}, ErrUnavailable
	}
	source, err := openXBoardSource(ctx, req.Source)
	if err != nil {
		return XBoardMigrationPreview{}, err
	}
	defer source.Close()
	scan, err := s.scanXBoardMigration(ctx, source)
	if err != nil {
		return XBoardMigrationPreview{}, err
	}
	if err := s.ensureXBoardMigrationSchema(ctx); err != nil {
		return XBoardMigrationPreview{}, err
	}
	token, err := randomHexString(24)
	if err != nil {
		return XBoardMigrationPreview{}, err
	}
	now := time.Now().Unix()
	expiresAt := now + int64(xboardPreviewTTL/time.Second)
	_, err = s.db.ExecContext(ctx, `INSERT INTO v2_xboard_migration_preview
(token, fingerprint, source_host, source_database, source_users, ready, skip_admin, skip_no_plan, skip_unmapped, skip_conflict, expires_at, created_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, token, scan.Fingerprint, strings.TrimSpace(req.Source.Host), strings.TrimSpace(req.Source.Database), scan.Preview.SourceUsers, scan.Preview.Ready, scan.Preview.SkipAdmin, scan.Preview.SkipNoPlan, scan.Preview.SkipUnmapped, scan.Preview.SkipConflict, expiresAt, now)
	if err != nil {
		return XBoardMigrationPreview{}, fmt.Errorf("save xboard migration preview: %w", err)
	}
	scan.Preview.PreviewToken = token
	scan.Preview.ExpiresAt = expiresAt
	return scan.Preview, nil
}

func (s *DBService) ExecuteXBoardMigration(ctx context.Context, req XBoardMigrationRequest) (XBoardMigrationResult, error) {
	if s.db == nil {
		return XBoardMigrationResult{}, ErrUnavailable
	}
	if err := s.ensureXBoardMigrationSchema(ctx); err != nil {
		return XBoardMigrationResult{}, err
	}
	token := strings.TrimSpace(req.PreviewToken)
	if token == "" {
		return XBoardMigrationResult{}, errors.New("请先预览并确认迁移")
	}
	var fingerprint, status string
	var expiresAt int64
	err := s.db.QueryRowContext(ctx, `SELECT fingerprint, status, expires_at FROM v2_xboard_migration_preview WHERE token=$1 LIMIT 1`, token).Scan(&fingerprint, &status, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) || status != "ready" || expiresAt < time.Now().Unix() {
		return XBoardMigrationResult{}, errors.New("预览已失效，请重新预览")
	}
	if err != nil {
		return XBoardMigrationResult{}, fmt.Errorf("load xboard migration preview: %w", err)
	}
	source, err := openXBoardSource(ctx, req.Source)
	if err != nil {
		return XBoardMigrationResult{}, err
	}
	defer source.Close()
	scan, err := s.scanXBoardMigration(ctx, source)
	if err != nil {
		return XBoardMigrationResult{}, err
	}
	if scan.Fingerprint != fingerprint {
		return XBoardMigrationResult{}, errors.New("源站或目标站数据已变化，请重新预览")
	}
	return s.importXBoardUsers(ctx, token, scan)
}

func openXBoardSource(ctx context.Context, value XBoardSourceConfig) (*sql.DB, error) {
	host := strings.TrimSpace(value.Host)
	database := strings.TrimSpace(value.Database)
	username := strings.TrimSpace(value.Username)
	port := value.Port
	if port == 0 {
		port = 3306
	}
	if host == "" || database == "" || username == "" || port < 1 || port > 65535 {
		return nil, errors.New("源 MySQL 连接参数不完整")
	}
	cfg := mysqlcfg.NewConfig()
	cfg.User, cfg.Passwd, cfg.Net = username, value.Password, "tcp"
	cfg.Addr, cfg.DBName = host+":"+strconv.Itoa(port), database
	cfg.ParseTime = true
	cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout = 10*time.Second, 30*time.Second, 30*time.Second
	cfg.Params = map[string]string{"charset": "utf8mb4"}
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return nil, errors.New("无法打开源 MySQL")
	}
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		db.Close()
		return nil, errors.New("无法连接源 MySQL，请检查地址、端口和账号")
	}
	return db, nil
}

func (s *DBService) scanXBoardMigration(ctx context.Context, source *sql.DB) (xboardScan, error) {
	users, err := loadXBoardUsers(ctx, source)
	if err != nil {
		return xboardScan{}, err
	}
	targetPlans, err := s.loadXBoardTargetPlans(ctx)
	if err != nil {
		return xboardScan{}, err
	}
	conflicts, err := s.loadXBoardEmailConflicts(ctx, users)
	if err != nil {
		return xboardScan{}, err
	}
	preview := XBoardMigrationPreview{SourceUsers: int64(len(users))}
	counts := map[int64]int64{}
	for _, user := range users {
		switch {
		case user.IsAdmin != 0 || user.IsStaff != 0:
			preview.SkipAdmin++
		case !user.PlanID.Valid || user.PlanID.Int64 <= 0:
			preview.SkipNoPlan++
		case xboardPlanMapping[user.PlanID.Int64] <= 0 || targetPlans[xboardPlanMapping[user.PlanID.Int64]].ID == 0:
			preview.SkipUnmapped++
		case hasEmailKey(conflicts, user.Email):
			preview.SkipConflict++
		default:
			preview.Ready++
			counts[user.PlanID.Int64]++
		}
	}
	ids := make([]int64, 0, len(counts))
	for id := range counts {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, sourceID := range ids {
		targetID := xboardPlanMapping[sourceID]
		preview.PlanBreakdown = append(preview.PlanBreakdown, XBoardPlanPreview{SourcePlanID: sourceID, TargetPlanID: targetID, TargetName: targetPlans[targetID].Name, Users: counts[sourceID]})
	}
	fingerprint := xboardFingerprint(users, conflicts, targetPlans)
	return xboardScan{Users: users, TargetPlans: targetPlans, Conflicts: conflicts, Preview: preview, Fingerprint: fingerprint}, nil
}

func loadXBoardUsers(ctx context.Context, source *sql.DB) ([]xboardSourceUser, error) {
	rows, err := source.QueryContext(ctx, `SELECT id,email,password,password_algo,password_salt,t,u,d,transfer_enable,device_limit,banned,plan_id,speed_limit,remind_expire,remind_traffic,expired_at,remarks,created_at,updated_at,is_admin,is_staff FROM v2_user ORDER BY id`)
	if err != nil {
		return nil, errors.New("读取源 XBoard 用户失败，请确认版本和表结构")
	}
	defer rows.Close()
	result := make([]xboardSourceUser, 0)
	for rows.Next() {
		var user xboardSourceUser
		if err := rows.Scan(&user.ID, &user.Email, &user.Password, &user.PasswordAlgo, &user.PasswordSalt, &user.T, &user.U, &user.D, &user.TransferEnable, &user.DeviceLimit, &user.Banned, &user.PlanID, &user.SpeedLimit, &user.RemindExpire, &user.RemindTraffic, &user.ExpiredAt, &user.Remarks, &user.CreatedAt, &user.UpdatedAt, &user.IsAdmin, &user.IsStaff); err != nil {
			return nil, errors.New("读取源 XBoard 用户失败")
		}
		user.Email = strings.ToLower(strings.TrimSpace(user.Email))
		if user.Email != "" {
			result = append(result, user)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, errors.New("遍历源 XBoard 用户失败")
	}
	return result, nil
}

func (s *DBService) loadXBoardTargetPlans(ctx context.Context) (map[int64]xboardTargetPlan, error) {
	ids := []int64{1, 2, 3, 5}
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,group_id,device_limit FROM v2_plan WHERE id IN ($1,$2,$3,$4)`, ids[0], ids[1], ids[2], ids[3])
	if err != nil {
		return nil, fmt.Errorf("query xboard target plans: %w", err)
	}
	defer rows.Close()
	result := map[int64]xboardTargetPlan{}
	for rows.Next() {
		var p xboardTargetPlan
		if err := rows.Scan(&p.ID, &p.Name, &p.GroupID, &p.DeviceLimit); err != nil {
			return nil, err
		}
		result[p.ID] = p
	}
	for _, id := range ids {
		if result[id].ID == 0 {
			return nil, fmt.Errorf("目标套餐 ID %d 不存在", id)
		}
	}
	return result, nil
}

func (s *DBService) loadXBoardEmailConflicts(ctx context.Context, users []xboardSourceUser) (map[string]struct{}, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT lower(trim(email)) FROM v2_user`)
	if err != nil {
		return nil, fmt.Errorf("query xboard target emails: %w", err)
	}
	defer rows.Close()
	result := map[string]struct{}{}
	for rows.Next() {
		var email string
		if err := rows.Scan(&email); err != nil {
			return nil, err
		}
		result[email] = struct{}{}
	}
	return result, nil
}

func hasEmailKey(values map[string]struct{}, email string) bool {
	_, ok := values[strings.ToLower(strings.TrimSpace(email))]
	return ok
}

func xboardFingerprint(users []xboardSourceUser, conflicts map[string]struct{}, plans map[int64]xboardTargetPlan) string {
	h := sha256.New()
	for _, user := range users {
		// Only fields that change preview eligibility belong in the fingerprint.
		// Traffic, password, expiry and other live account values can legitimately
		// change between preview and execution and are re-read at execution time.
		fmt.Fprintf(h, "%d\x00%s\x00%d:%t\x00%d\x00%d\n",
			user.ID, user.Email, user.PlanID.Int64, user.PlanID.Valid, user.IsAdmin, user.IsStaff)
		if hasEmailKey(conflicts, user.Email) {
			h.Write([]byte("conflict\n"))
		}
	}
	for _, id := range []int64{1, 2, 3, 5} {
		fmt.Fprintf(h, "plan:%d:%d\n", id, plans[id].ID)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (s *DBService) importXBoardUsers(ctx context.Context, token string, scan xboardScan) (XBoardMigrationResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return XBoardMigrationResult{}, errors.New("开始迁移事务失败")
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	claim, err := tx.ExecContext(ctx, `UPDATE v2_xboard_migration_preview SET status='running' WHERE token=$1 AND status='ready' AND expires_at >= $2`, token, now)
	if err != nil {
		return XBoardMigrationResult{}, fmt.Errorf("claim xboard migration preview: %w", err)
	}
	claimed, _ := claim.RowsAffected()
	if claimed != 1 {
		return XBoardMigrationResult{}, errors.New("预览已使用或失效，请重新预览")
	}
	var batchID int64
	err = tx.QueryRowContext(ctx, `INSERT INTO v2_xboard_migration_batch (preview_token,status,source_users,created_at,updated_at) VALUES ($1,'running',$2,$3,$3) RETURNING id`, token, scan.Preview.SourceUsers, now).Scan(&batchID)
	if err != nil {
		return XBoardMigrationResult{}, fmt.Errorf("create xboard migration batch: %w", err)
	}
	result := XBoardMigrationResult{BatchID: batchID, SkipAdmin: scan.Preview.SkipAdmin, SkipNoPlan: scan.Preview.SkipNoPlan, SkipUnmapped: scan.Preview.SkipUnmapped, SkipConflict: scan.Preview.SkipConflict}
	for _, user := range scan.Users {
		reason := ""
		switch {
		case user.IsAdmin != 0 || user.IsStaff != 0:
			reason = "admin"
		case !user.PlanID.Valid || user.PlanID.Int64 <= 0:
			reason = "no_plan"
		case xboardPlanMapping[user.PlanID.Int64] <= 0:
			reason = "unmapped_plan"
		case hasEmailKey(scan.Conflicts, user.Email):
			reason = "email_conflict"
		}
		if reason != "" {
			_, _ = tx.ExecContext(ctx, `INSERT INTO v2_xboard_migration_item (batch_id,source_user_id,email,status,reason,created_at) VALUES ($1,$2,$3,'skipped',$4,$5)`, batchID, user.ID, user.Email, reason, now)
			continue
		}
		targetPlan := scan.TargetPlans[xboardPlanMapping[user.PlanID.Int64]]
		uuidValue, uuidErr := randomUUIDString()
		tokenValue, tokenErr := randomHexString(16)
		if uuidErr != nil || tokenErr != nil {
			return XBoardMigrationResult{}, errors.New("生成迁移用户标识失败")
		}
		createdAt := user.CreatedAt
		if createdAt <= 0 {
			createdAt = now
		}
		updatedAt := user.UpdatedAt
		if updatedAt <= 0 {
			updatedAt = now
		}
		var targetID int64
		err := tx.QueryRowContext(ctx, `INSERT INTO v2_user (email,password,password_algo,password_salt,t,u,d,transfer_enable,device_limit,banned,plan_id,group_id,speed_limit,remind_expire,remind_traffic,expired_at,remarks,uuid,token,created_at,updated_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21) ON CONFLICT (email) DO NOTHING RETURNING id`, user.Email, user.Password, nullableSQLStringValue(user.PasswordAlgo), nullableSQLStringValue(user.PasswordSalt), user.T, user.U, user.D, user.TransferEnable, nullableSQLInt64Value(user.DeviceLimit), user.Banned, targetPlan.ID, nullableSQLInt64Value(targetPlan.GroupID), nullableSQLInt64Value(user.SpeedLimit), user.RemindExpire, user.RemindTraffic, nullableSQLInt64Value(user.ExpiredAt), nullableSQLStringValue(user.Remarks), uuidValue, tokenValue, createdAt, updatedAt).Scan(&targetID)
		if errors.Is(err, sql.ErrNoRows) {
			result.SkipConflict++
			_, _ = tx.ExecContext(ctx, `INSERT INTO v2_xboard_migration_item (batch_id,source_user_id,email,status,reason,created_at) VALUES ($1,$2,$3,'skipped','email_conflict',$4)`, batchID, user.ID, user.Email, now)
			continue
		}
		if err != nil {
			// PostgreSQL marks the whole transaction failed after a statement
			// error. Abort instead of pretending later rows can still import.
			return XBoardMigrationResult{}, fmt.Errorf("迁移用户 %s 失败，已回滚整个批次: %w", user.Email, err)
		}
		result.Imported++
		_, _ = tx.ExecContext(ctx, `INSERT INTO v2_xboard_migration_item (batch_id,source_user_id,email,target_user_id,status,reason,created_at) VALUES ($1,$2,$3,$4,'imported','',$5)`, batchID, user.ID, user.Email, targetID, now)
	}
	_, err = tx.ExecContext(ctx, `UPDATE v2_xboard_migration_batch SET status='completed',imported=$2,skip_admin=$3,skip_no_plan=$4,skip_unmapped=$5,skip_conflict=$6,failed=$7,updated_at=$8 WHERE id=$1`, batchID, result.Imported, result.SkipAdmin, result.SkipNoPlan, result.SkipUnmapped, result.SkipConflict, result.Failed, now)
	if err != nil {
		return XBoardMigrationResult{}, fmt.Errorf("finish xboard migration batch: %w", err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE v2_xboard_migration_preview SET status='used' WHERE token=$1`, token); err != nil {
		return XBoardMigrationResult{}, err
	}
	if err = tx.Commit(); err != nil {
		return XBoardMigrationResult{}, errors.New("提交迁移事务失败")
	}
	return result, nil
}

func nullableSQLStringValue(v sql.NullString) any {
	if v.Valid {
		return v.String
	}
	return nil
}
func nullableSQLInt64Value(v sql.NullInt64) any {
	if v.Valid {
		return v.Int64
	}
	return nil
}

func (s *DBService) ensureXBoardMigrationSchema(ctx context.Context) error {
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS v2_xboard_migration_preview (token varchar(64) PRIMARY KEY,fingerprint varchar(64) NOT NULL,source_host varchar(255) NOT NULL,source_database varchar(255) NOT NULL,source_users BIGINT NOT NULL DEFAULT 0,ready BIGINT NOT NULL DEFAULT 0,skip_admin BIGINT NOT NULL DEFAULT 0,skip_no_plan BIGINT NOT NULL DEFAULT 0,skip_unmapped BIGINT NOT NULL DEFAULT 0,skip_conflict BIGINT NOT NULL DEFAULT 0,status varchar(16) NOT NULL DEFAULT 'ready',expires_at BIGINT NOT NULL,created_at BIGINT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS v2_xboard_migration_batch (id BIGINT GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,preview_token varchar(64) NOT NULL,status varchar(16) NOT NULL,source_users BIGINT NOT NULL DEFAULT 0,imported BIGINT NOT NULL DEFAULT 0,skip_admin BIGINT NOT NULL DEFAULT 0,skip_no_plan BIGINT NOT NULL DEFAULT 0,skip_unmapped BIGINT NOT NULL DEFAULT 0,skip_conflict BIGINT NOT NULL DEFAULT 0,failed BIGINT NOT NULL DEFAULT 0,created_at BIGINT NOT NULL,updated_at BIGINT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS v2_xboard_migration_item (id BIGINT GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,batch_id BIGINT NOT NULL,source_user_id BIGINT NOT NULL,email varchar(255) NOT NULL,target_user_id BIGINT DEFAULT NULL,status varchar(16) NOT NULL,reason varchar(64) NOT NULL DEFAULT '',created_at BIGINT NOT NULL,CONSTRAINT uniq_v2_xboard_migration_item UNIQUE(batch_id,source_user_id))`,
	} {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("ensure xboard migration schema: %w", err)
		}
	}
	return nil
}
