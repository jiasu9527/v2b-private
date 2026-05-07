package admin

import (
	"context"
	"crypto/hmac"
	crand "crypto/rand"
	"crypto/sha1"
	"database/sql"
	"encoding/base64"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/mail"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const gibibyte = int64(1073741824)

var adminInviteCodePattern = regexp.MustCompile(`^[A-Za-z0-9]{1,32}$`)

type UserFilter struct {
	Key       string
	Condition string
	Value     string
}

type UserFetchRequest struct {
	Current  int64
	PageSize int64
	Sort     string
	SortType string
	Filters  []UserFilter
}

type UserListResult struct {
	Data  []map[string]any `json:"data"`
	Total int64            `json:"total"`
}

type UserUpdateRequest struct {
	ID     int64
	Values map[string]string
}

type UserGenerateRequest struct {
	Values map[string]string
}

type UserMailRequest struct {
	Subject  string
	Content  string
	Sort     string
	SortType string
	Filters  []UserFilter
}

type userPlanSnapshot struct {
	ID             int64
	GroupID        int64
	TransferEnable int64
	DeviceLimit    sql.NullInt64
}

const adminUserJSONAlias = "user_row"

func adminUserListJSONQuery(whereClause, sortExpr, sortType string, limitArgIndex, offsetArgIndex int) string {
	return fmt.Sprintf(`SELECT COALESCE(json_agg(row_to_json(%s)), '[]'::json)
FROM (
	SELECT u.*, (u.u + u.d) AS total_used, p.name AS plan_name
	FROM v2_user u
	LEFT JOIN v2_plan p ON p.id = u.plan_id
	%s
	ORDER BY %s %s
	LIMIT $%d OFFSET $%d
) AS %s`, adminUserJSONAlias, whereClause, sortExpr, sortType, limitArgIndex, offsetArgIndex, adminUserJSONAlias)
}

func adminUserExportJSONQuery(whereClause string) string {
	return `SELECT COALESCE(json_agg(row_to_json(` + adminUserJSONAlias + `)), '[]'::json)
FROM (
	SELECT u.*, p.name AS plan_name
	FROM v2_user u
	LEFT JOIN v2_plan p ON p.id = u.plan_id
	` + whereClause + `
	ORDER BY u.id ASC
) AS ` + adminUserJSONAlias
}

func adminUserJSONMapQuery(innerQuery string) string {
	return `SELECT row_to_json(` + adminUserJSONAlias + `)
FROM (
	` + innerQuery + `
) AS ` + adminUserJSONAlias
}

func (s *DBService) ListUsers(ctx context.Context, req UserFetchRequest) (UserListResult, error) {
	if s.db == nil {
		return UserListResult{}, ErrUnavailable
	}

	current := req.Current
	if current <= 0 {
		current = 1
	}
	pageSize := req.PageSize
	if pageSize < 10 {
		pageSize = 10
	}
	sortExpr := sanitizeUserSort(req.Sort)
	sortType := sanitizeAdminSortType(req.SortType)

	whereClause, args, err := s.buildUserWhere(ctx, req.Filters)
	if err != nil {
		return UserListResult{}, err
	}

	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_user u`+whereClause, args...).Scan(&total); err != nil {
		return UserListResult{}, fmt.Errorf("count admin users: %w", err)
	}

	offset := (current - 1) * pageSize
	dataArgs := append(append([]any{}, args...), pageSize, offset)
	query := adminUserListJSONQuery(whereClause, sortExpr, sortType, len(dataArgs)-1, len(dataArgs))

	data, err := s.queryJSONList(ctx, query, dataArgs...)
	if err != nil {
		return UserListResult{}, fmt.Errorf("query admin users: %w", err)
	}

	nodeNames, err := s.loadManagedServerNameMap(ctx)
	if err != nil {
		return UserListResult{}, err
	}

	for _, item := range data {
		token := strings.TrimSpace(fmt.Sprint(item["token"]))
		userID := mapAnyInt64(item["id"])
		if raw, ok, err := s.getStringKV(ctx, adminAliveUserRuntimeKey(userID)); err != nil {
			return UserListResult{}, err
		} else if ok {
			item["alive_ip"], item["ips"] = adminAliveIPSummaryWithNodeNames(raw, nodeNames)
		} else {
			item["alive_ip"] = int64(0)
			item["ips"] = ""
		}
		subscribeURL, err := s.buildAdminUserSubscribeURL(ctx, userID, token)
		if err != nil {
			return UserListResult{}, err
		}
		item["subscribe_url"] = subscribeURL
	}

	return UserListResult{Data: data, Total: total}, nil
}

func (s *DBService) GetUserInfoByID(ctx context.Context, id int64) (map[string]any, error) {
	if s.db == nil {
		return nil, ErrUnavailable
	}
	if id <= 0 {
		return nil, errors.New("参数错误")
	}

	user, err := s.queryJSONMap(ctx, adminUserJSONMapQuery(`SELECT * FROM v2_user WHERE id = $1 LIMIT 1`), id)
	if err != nil {
		return nil, fmt.Errorf("query admin user detail: %w", err)
	}
	if user == nil {
		return deletedAdminUserPlaceholder(id), nil
	}

	if inviteID := mapNullableAnyInt64(user["invite_user_id"]); inviteID != nil && *inviteID > 0 {
		inviteUser, err := s.queryJSONMap(ctx, adminUserJSONMapQuery(`SELECT * FROM v2_user WHERE id = $1 LIMIT 1`), *inviteID)
		if err != nil {
			return nil, fmt.Errorf("query invite user detail: %w", err)
		}
		if inviteUser != nil {
			user["invite_user"] = inviteUser
		}
	}

	inviteCode, err := s.getPrimaryInviteCode(ctx, id)
	if err != nil {
		return nil, err
	}
	if inviteCode != nil {
		user["invite_code_id"] = inviteCode.ID
		user["invite_code"] = inviteCode.Code
	}

	return user, nil
}

func deletedAdminUserPlaceholder(id int64) map[string]any {
	return map[string]any{
		"id":                 id,
		"email":              fmt.Sprintf("已删除用户 #%d", id),
		"password":           "",
		"transfer_enable":    int64(0),
		"u":                  int64(0),
		"d":                  int64(0),
		"commission_balance": int64(0),
		"balance":            int64(0),
		"discount":           int64(0),
		"commission_type":    int64(0),
		"commission_rate":    int64(0),
		"is_admin":           int64(0),
		"is_staff":           int64(0),
		"banned":             int64(1),
		"group_id":           int64(0),
		"plan_id":            int64(0),
		"deleted_user":       int64(1),
	}
}

func (s *DBService) UpdateUser(ctx context.Context, req UserUpdateRequest) (bool, error) {
	if s.db == nil {
		return false, ErrUnavailable
	}
	if req.ID <= 0 {
		return false, errors.New("用户不存在")
	}

	values := cloneStringMap(req.Values)
	if len(values) == 1 {
		if _, ok := values["invite_user_email"]; ok {
			return s.updateInviteUserBinding(ctx, req.ID, strings.TrimSpace(values["invite_user_email"]))
		}
	}
	email := strings.TrimSpace(strings.ToLower(values["email"]))
	if email == "" {
		return false, errors.New("邮箱不能为空")
	}
	if !isValidEmail(email) {
		return false, errors.New("邮箱格式不正确")
	}

	var currentEmail string
	err := s.db.QueryRowContext(ctx, `SELECT email FROM v2_user WHERE id = $1 LIMIT 1`, req.ID).Scan(&currentEmail)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, errors.New("用户不存在")
		}
		return false, fmt.Errorf("query admin user update target: %w", err)
	}

	var duplicateID int64
	err = s.db.QueryRowContext(ctx, `SELECT id FROM v2_user WHERE LOWER(email) = $1 LIMIT 1`, email).Scan(&duplicateID)
	switch {
	case err == nil && duplicateID != req.ID:
		return false, errors.New("邮箱已被使用")
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return false, fmt.Errorf("query duplicate admin user email: %w", err)
	}

	setParts := make([]string, 0, 20)
	args := []any{req.ID}
	nextIndex := 2
	addSet := func(column string, value any) {
		setParts = append(setParts, fmt.Sprintf("%s = $%d", column, nextIndex))
		args = append(args, value)
		nextIndex++
	}

	addSet("email", email)

	bannedValue, err := parseRequiredBinaryInt(values["banned"], "是否封禁格式不正确")
	if err != nil {
		return false, err
	}
	addSet("banned", bannedValue)

	isAdminValue, err := parseRequiredBinaryInt(values["is_admin"], "是否管理员格式不正确")
	if err != nil {
		return false, err
	}
	addSet("is_admin", isAdminValue)

	isStaffValue, err := parseRequiredBinaryInt(values["is_staff"], "是否员工格式不正确")
	if err != nil {
		return false, err
	}
	addSet("is_staff", isStaffValue)

	if _, ok := values["transfer_enable"]; ok {
		transferEnable, err := strconv.ParseInt(strings.TrimSpace(values["transfer_enable"]), 10, 64)
		if err != nil {
			return false, errors.New("流量格式不正确")
		}
		addSet("transfer_enable", transferEnable)
	}
	if value, ok, err := parseNullableInt64Input(values, "device_limit"); err != nil {
		return false, errors.New("设备数限制格式不正确")
	} else if ok {
		addSet("device_limit", nullableInt64(value))
	}
	if value, ok, err := parseNullableInt64Input(values, "expired_at"); err != nil {
		return false, errors.New("到期时间格式不正确")
	} else if ok {
		addSet("expired_at", nullableInt64(value))
	}
	if value, ok, err := parseNullableInt64Input(values, "commission_rate"); err != nil {
		return false, errors.New("推荐返利比例格式不正确")
	} else if ok {
		if value != nil && (*value < 0 || *value > 100) {
			return false, errors.New("推荐返利比例格式不正确")
		}
		addSet("commission_rate", nullableInt64(value))
	}
	if value, ok, err := parseNullableInt64Input(values, "discount"); err != nil {
		return false, errors.New("专属折扣比例格式不正确")
	} else if ok {
		if value != nil && (*value < 0 || *value > 100) {
			return false, errors.New("专属折扣比例格式不正确")
		}
		addSet("discount", nullableInt64(value))
	}
	if value, ok, err := parseNullableInt64Input(values, "speed_limit"); err != nil {
		return false, errors.New("限速格式不正确")
	} else if ok {
		addSet("speed_limit", nullableInt64(value))
	}

	for _, field := range []struct {
		Key     string
		Column  string
		Message string
	}{
		{Key: "u", Column: "u", Message: "上行流量格式不正确"},
		{Key: "d", Column: "d", Message: "下行流量格式不正确"},
		{Key: "balance", Column: "balance", Message: "余额格式不正确"},
		{Key: "commission_type", Column: "commission_type", Message: "佣金格式不正确"},
		{Key: "commission_balance", Column: "commission_balance", Message: "佣金格式不正确"},
	} {
		raw, ok := values[field.Key]
		if !ok {
			continue
		}
		parsed, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil {
			return false, errors.New(field.Message)
		}
		addSet(field.Column, parsed)
	}

	if rawPlanID, ok := values["plan_id"]; ok {
		trimmedPlanID := strings.TrimSpace(rawPlanID)
		if trimmedPlanID == "" {
			addSet("plan_id", nil)
		} else {
			planID, err := strconv.ParseInt(trimmedPlanID, 10, 64)
			if err != nil {
				return false, errors.New("订阅计划格式不正确")
			}
			plan, err := s.loadUserPlan(ctx, planID)
			if err != nil {
				return false, err
			}
			addSet("plan_id", plan.ID)
			addSet("group_id", plan.GroupID)
		}
	}

	if rawPassword, ok := values["password"]; ok && strings.TrimSpace(rawPassword) != "" {
		password := strings.TrimSpace(rawPassword)
		if len(password) < 8 {
			return false, errors.New("密码长度最小8位")
		}
		hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			return false, fmt.Errorf("hash admin user password: %w", err)
		}
		addSet("password", string(hashedPassword))
		addSet("password_algo", nil)
	}

	if rawInviteEmail, ok := values["invite_user_email"]; ok {
		inviteEmail := strings.TrimSpace(strings.ToLower(rawInviteEmail))
		if inviteEmail == "" {
			addSet("invite_user_id", nil)
		} else {
			var inviteUserID int64
			err := s.db.QueryRowContext(ctx, `SELECT id FROM v2_user WHERE LOWER(email) = $1 LIMIT 1`, inviteEmail).Scan(&inviteUserID)
			switch {
			case err == nil:
				addSet("invite_user_id", inviteUserID)
			case errors.Is(err, sql.ErrNoRows):
			default:
				return false, fmt.Errorf("query invite user by email: %w", err)
			}
		}
	}

	if rawRemarks, ok := values["remarks"]; ok {
		remarks := strings.TrimSpace(rawRemarks)
		if remarks == "" {
			addSet("remarks", nil)
		} else {
			addSet("remarks", remarks)
		}
	}

	now := time.Now().Unix()
	addSet("updated_at", now)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin admin user update transaction: %w", err)
	}
	defer tx.Rollback()

	if rawInviteCode, ok := values["invite_code"]; ok {
		if err := s.savePrimaryInviteCodeTx(ctx, tx, req.ID, rawInviteCode, now); err != nil {
			return false, err
		}
	}

	if bannedValue == 1 {
		if _, err := tx.ExecContext(ctx, `DELETE FROM v2_auth_session WHERE user_id = $1`, req.ID); err != nil {
			return false, fmt.Errorf("clear user sessions: %w", err)
		}
	}

	query := fmt.Sprintf(`UPDATE v2_user SET %s WHERE id = $1`, strings.Join(setParts, ", "))
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return false, errors.New("保存失败")
	}
	affected, err := result.RowsAffected()
	if err != nil || affected == 0 {
		return false, errors.New("保存失败")
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit admin user update transaction: %w", err)
	}
	if bannedValue == 1 && s.authCache != nil {
		s.authCache.InvalidateUser(req.ID)
	}
	return true, nil
}

func (s *DBService) updateInviteUserBinding(ctx context.Context, userID int64, inviteEmail string) (bool, error) {
	var currentEmail string
	if err := s.db.QueryRowContext(ctx, `SELECT email FROM v2_user WHERE id = $1 LIMIT 1`, userID).Scan(&currentEmail); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, errors.New("用户不存在")
		}
		return false, fmt.Errorf("query admin user update target: %w", err)
	}

	var inviteUserID any
	if inviteEmail != "" {
		inviteEmail = strings.TrimSpace(strings.ToLower(inviteEmail))
		var parsedInviteUserID int64
		err := s.db.QueryRowContext(ctx, `SELECT id FROM v2_user WHERE LOWER(email) = $1 LIMIT 1`, inviteEmail).Scan(&parsedInviteUserID)
		switch {
		case err == nil:
			inviteUserID = parsedInviteUserID
		case errors.Is(err, sql.ErrNoRows):
		default:
			return false, fmt.Errorf("query invite user by email: %w", err)
		}
	}

	result, err := s.db.ExecContext(ctx, `UPDATE v2_user SET invite_user_id = $2, updated_at = $3 WHERE id = $1`, userID, inviteUserID, time.Now().Unix())
	if err != nil {
		return false, errors.New("保存失败")
	}
	affected, err := result.RowsAffected()
	if err != nil || affected == 0 {
		return false, errors.New("保存失败")
	}
	return true, nil
}

type adminPrimaryInviteCode struct {
	ID               int64
	Code             string
	InviteCampaignID sql.NullInt64
}

func (s *DBService) getPrimaryInviteCode(ctx context.Context, userID int64) (*adminPrimaryInviteCode, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, code FROM v2_invite_code WHERE user_id = $1 AND status = 0 ORDER BY id ASC LIMIT 1`, userID)
	result := &adminPrimaryInviteCode{}
	if err := row.Scan(&result.ID, &result.Code); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("query user invite code: %w", err)
	}
	result.Code = strings.TrimSpace(result.Code)
	return result, nil
}

func (s *DBService) savePrimaryInviteCodeTx(ctx context.Context, tx *sql.Tx, userID int64, rawCode string, now int64) error {
	code := strings.TrimSpace(rawCode)
	if code == "" {
		return nil
	}
	if !adminInviteCodePattern.MatchString(code) {
		return errors.New("邀请码格式不正确，仅支持1-32位字母和数字")
	}

	current, err := s.getPrimaryInviteCodeTx(ctx, tx, userID)
	if err != nil {
		return err
	}

	var duplicateID int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM v2_invite_code WHERE code = $1 AND status = 0 AND user_id <> $2 LIMIT 1`, code, userID).Scan(&duplicateID)
	switch {
	case err == nil:
		return errors.New("邀请码已被使用")
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("query duplicate invite code: %w", err)
	}

	if current != nil {
		if current.Code == code {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE v2_invite_code SET code = $2, updated_at = $3 WHERE id = $1`, current.ID, code, now); err != nil {
			return fmt.Errorf("update invite code: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE v2_invite_campaign SET invite_code = $2, updated_at = $3 WHERE invite_code_id = $1`, current.ID, code, now); err != nil {
			return fmt.Errorf("update invite campaign code: %w", err)
		}
		return nil
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO v2_invite_code (user_id, code, status, pv, created_at, updated_at)
VALUES ($1, $2, 0, 0, $3, $3)`, userID, code, now); err != nil {
		return fmt.Errorf("insert invite code: %w", err)
	}
	return nil
}

func (s *DBService) getPrimaryInviteCodeTx(ctx context.Context, tx *sql.Tx, userID int64) (*adminPrimaryInviteCode, error) {
	row := tx.QueryRowContext(ctx, `SELECT id, code, invite_campaign_id
FROM v2_invite_code
WHERE user_id = $1 AND status = 0
ORDER BY id ASC
LIMIT 1
FOR UPDATE`, userID)
	result := &adminPrimaryInviteCode{}
	if err := row.Scan(&result.ID, &result.Code, &result.InviteCampaignID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("query user invite code: %w", err)
	}
	result.Code = strings.TrimSpace(result.Code)
	return result, nil
}

func (s *DBService) GenerateUsers(ctx context.Context, req UserGenerateRequest) (string, bool, error) {
	if s.db == nil {
		return "", false, ErrUnavailable
	}

	values := cloneStringMap(req.Values)
	emailSuffix := strings.TrimSpace(strings.ToLower(values["email_suffix"]))
	if emailSuffix == "" {
		return "", false, errors.New("参数有误")
	}

	plan, err := s.optionalUserPlan(ctx, strings.TrimSpace(values["plan_id"]))
	if err != nil {
		return "", false, err
	}
	expiredAt, err := parseOptionalUserInt64(strings.TrimSpace(values["expired_at"]))
	if err != nil {
		return "", false, errors.New("参数有误")
	}

	if emailPrefix := strings.TrimSpace(strings.ToLower(values["email_prefix"])); emailPrefix != "" {
		email := emailPrefix + "@" + emailSuffix
		exists, err := s.userEmailExists(ctx, email)
		if err != nil {
			return "", false, err
		}
		if exists {
			return "", false, errors.New("邮箱已存在于系统中")
		}

		password := strings.TrimSpace(values["password"])
		if password == "" {
			password = email
		}
		hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			return "", false, fmt.Errorf("hash generated user password: %w", err)
		}
		token, err := randomHexString(16)
		if err != nil {
			return "", false, err
		}
		uuidValue, err := randomUUIDString()
		if err != nil {
			return "", false, err
		}

		now := time.Now().Unix()
		_, err = s.db.ExecContext(ctx, `INSERT INTO v2_user (
email, password, plan_id, group_id, transfer_enable, device_limit, expired_at, uuid, token, created_at, updated_at
) VALUES (
$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $10
)`,
			email,
			string(hashedPassword),
			nullableUserPlanID(plan),
			nullableUserPlanGroupID(plan),
			userPlanTransferEnable(plan),
			nullableUserPlanDeviceLimit(plan),
			nullableInt64(expiredAt),
			uuidValue,
			token,
			now,
		)
		if err != nil {
			return "", false, errors.New("生成失败")
		}
		return "", false, nil
	}

	generateCount, err := strconv.ParseInt(strings.TrimSpace(values["generate_count"]), 10, 64)
	if err != nil || generateCount <= 0 {
		return "", false, errors.New("生成数量必须为数字")
	}
	if generateCount > 500 {
		return "", false, errors.New("生成数量最大为500个")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", false, errors.New("生成失败")
	}
	defer tx.Rollback()

	type generatedUser struct {
		ID          int64
		Email       string
		Password    string
		ExpiredAt   *int64
		UUID        string
		Token       string
		CreatedAt   int64
		PlanID      *int64
		GroupID     *int64
		DeviceLimit *int64
	}

	users := make([]generatedUser, 0, generateCount)
	seenEmails := make(map[string]struct{}, generateCount)
	now := time.Now().Unix()
	for range generateCount {
		email, err := s.generateUniqueEmailTx(ctx, tx, emailSuffix, seenEmails)
		if err != nil {
			return "", false, errors.New("生成失败")
		}
		password := strings.TrimSpace(values["password"])
		if password == "" {
			password = email
		}
		hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			return "", false, errors.New("生成失败")
		}
		token, err := randomHexString(16)
		if err != nil {
			return "", false, errors.New("生成失败")
		}
		uuidValue, err := randomUUIDString()
		if err != nil {
			return "", false, errors.New("生成失败")
		}

		var userID int64
		if err := tx.QueryRowContext(ctx, `INSERT INTO v2_user (
email, password, plan_id, group_id, transfer_enable, device_limit, expired_at, uuid, token, created_at, updated_at
) VALUES (
$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $10
) RETURNING id`,
			email,
			string(hashedPassword),
			nullableUserPlanID(plan),
			nullableUserPlanGroupID(plan),
			userPlanTransferEnable(plan),
			nullableUserPlanDeviceLimit(plan),
			nullableInt64(expiredAt),
			uuidValue,
			token,
			now,
		).Scan(&userID); err != nil {
			return "", false, errors.New("生成失败")
		}

		users = append(users, generatedUser{
			ID:          userID,
			Email:       email,
			Password:    password,
			ExpiredAt:   expiredAt,
			UUID:        uuidValue,
			Token:       token,
			CreatedAt:   now,
			PlanID:      nullableUserPlanID(plan),
			GroupID:     nullableUserPlanGroupID(plan),
			DeviceLimit: nullableUserPlanDeviceLimit(plan),
		})
	}

	if err := tx.Commit(); err != nil {
		return "", false, errors.New("生成失败")
	}

	var builder strings.Builder
	writer := csv.NewWriter(&builder)
	writer.UseCRLF = true
	_ = writer.Write([]string{"账号", "密码", "过期时间", "UUID", "创建时间", "订阅地址"})
	for _, item := range users {
		subscribeURL, err := s.buildAdminUserSubscribeURL(ctx, item.ID, item.Token)
		if err != nil {
			return "", false, err
		}
		_ = writer.Write([]string{
			item.Email,
			item.Password,
			formatOptionalDateTime(item.ExpiredAt, "长期有效"),
			item.UUID,
			time.Unix(item.CreatedAt, 0).Format("2006-01-02 15:04:05"),
			subscribeURL,
		})
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return "", false, fmt.Errorf("write user generate csv: %w", err)
	}
	return builder.String(), true, nil
}

func (s *DBService) DumpUserCSV(ctx context.Context, filters []UserFilter) (string, error) {
	if s.db == nil {
		return "", ErrUnavailable
	}

	whereClause, args, err := s.buildUserWhere(ctx, filters)
	if err != nil {
		return "", err
	}
	query := adminUserExportJSONQuery(whereClause)
	users, err := s.queryJSONList(ctx, query, args...)
	if err != nil {
		return "", fmt.Errorf("query users for csv export: %w", err)
	}

	var builder strings.Builder
	builder.WriteString("\xEF\xBB\xBF")
	writer := csv.NewWriter(&builder)
	writer.UseCRLF = true
	_ = writer.Write([]string{"邮箱", "余额", "推广佣金", "总流量", "设备数限制", "剩余流量", "套餐到期时间", "订阅计划", "订阅地址"})
	for _, item := range users {
		token := strings.TrimSpace(fmt.Sprint(item["token"]))
		userID := mapAnyInt64(item["id"])
		subscribeURL, err := s.buildAdminUserSubscribeURL(ctx, userID, token)
		if err != nil {
			return "", err
		}
		expiredAt := mapNullableAnyInt64(item["expired_at"])
		if expiredAt != nil && *expiredAt <= 0 {
			expiredAt = nil
		}
		planName := strings.TrimSpace(fmt.Sprint(item["plan_name"]))
		if planName == "" || planName == "<nil>" {
			planName = "无订阅"
		}
		deviceLimit := ""
		if value := mapNullableAnyInt64(item["device_limit"]); value != nil {
			deviceLimit = strconv.FormatInt(*value, 10)
		}
		transferEnable := float64(mapAnyInt64(item["transfer_enable"])) / float64(gibibyte)
		notUseFlow := float64(mapAnyInt64(item["transfer_enable"])-mapAnyInt64(item["u"])-mapAnyInt64(item["d"])) / float64(gibibyte)
		_ = writer.Write([]string{
			strings.TrimSpace(fmt.Sprint(item["email"])),
			formatDecimalDiv100(mapAnyInt64(item["balance"])),
			formatDecimalDiv100(mapAnyInt64(item["commission_balance"])),
			formatFloatForCSV(transferEnable),
			deviceLimit,
			formatFloatForCSV(notUseFlow),
			formatOptionalDateTime(expiredAt, "长期有效"),
			planName,
			subscribeURL,
		})
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return "", fmt.Errorf("write user csv export: %w", err)
	}
	return builder.String(), nil
}

func (s *DBService) SendUserMail(ctx context.Context, req UserMailRequest) (bool, error) {
	if s.db == nil {
		return false, ErrUnavailable
	}

	req.Subject = strings.TrimSpace(req.Subject)
	req.Content = strings.TrimSpace(req.Content)
	if req.Subject == "" {
		return false, errors.New("主题不能为空")
	}
	if req.Content == "" {
		return false, errors.New("发送内容不能为空")
	}

	whereClause, args, err := s.buildUserWhere(ctx, req.Filters)
	if err != nil {
		return false, err
	}
	sortExpr := sanitizeUserSort(req.Sort)
	sortType := sanitizeAdminSortType(req.SortType)
	rows, err := s.db.QueryContext(ctx, `SELECT email FROM v2_user u`+whereClause+` ORDER BY `+sortExpr+` `+sortType, args...)
	if err != nil {
		return false, fmt.Errorf("query admin mail users: %w", err)
	}
	defer rows.Close()

	emails := make([]string, 0)
	for rows.Next() {
		var email string
		if err := rows.Scan(&email); err != nil {
			return false, fmt.Errorf("scan admin mail user: %w", err)
		}
		email = strings.TrimSpace(email)
		if email != "" {
			emails = append(emails, email)
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("iterate admin mail users: %w", err)
	}

	mailConfig, err := s.loadBulkMailConfig()
	if err != nil {
		return false, err
	}

	if err := s.dispatchUserMailJobs(ctx, emails, req.Subject, req.Content, mailConfig); err != nil {
		return false, err
	}
	return true, nil
}

func (s *DBService) BanUsers(ctx context.Context, filters []UserFilter) (bool, error) {
	if s.db == nil {
		return false, ErrUnavailable
	}

	ids, err := s.matchingUserIDs(ctx, filters)
	if err != nil {
		return false, err
	}
	if len(ids) == 0 {
		return true, nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin admin user ban transaction: %w", err)
	}
	defer tx.Rollback()

	if err := deleteAuthSessionsByUserIDs(ctx, tx, ids, "处理失败"); err != nil {
		return false, err
	}
	inClause, inArgs := buildInt64Placeholders(2, ids)
	query := `UPDATE v2_user SET banned = 1, updated_at = $1 WHERE id IN (` + inClause + `)`
	args := append([]any{time.Now().Unix()}, inArgs...)
	if _, err := tx.ExecContext(ctx, query, args...); err != nil {
		return false, errors.New("处理失败")
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit admin user ban transaction: %w", err)
	}
	if s.authCache != nil {
		for _, id := range ids {
			s.authCache.InvalidateUser(id)
		}
	}
	return true, nil
}

func (s *DBService) ResetUserSecret(ctx context.Context, id int64) (bool, error) {
	if s.db == nil {
		return false, ErrUnavailable
	}
	if id <= 0 {
		return false, errors.New("用户不存在")
	}

	token, err := randomHexString(16)
	if err != nil {
		return false, err
	}
	uuidValue, err := randomUUIDString()
	if err != nil {
		return false, err
	}

	result, err := s.db.ExecContext(ctx, `UPDATE v2_user
SET token = $2, uuid = $3, updated_at = $4
WHERE id = $1`, id, token, uuidValue, time.Now().Unix())
	if err != nil {
		return false, fmt.Errorf("reset admin user secret: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil || affected == 0 {
		return false, errors.New("用户不存在")
	}
	return true, nil
}

func (s *DBService) DeleteUser(ctx context.Context, id int64) (bool, error) {
	if s.db == nil {
		return false, ErrUnavailable
	}
	if id <= 0 {
		return false, errors.New("用户不存在")
	}

	var exists int64
	err := s.db.QueryRowContext(ctx, `SELECT id FROM v2_user WHERE id = $1 LIMIT 1`, id).Scan(&exists)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, errors.New("用户不存在")
		}
		return false, fmt.Errorf("query user delete target: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin admin user delete transaction: %w", err)
	}
	defer tx.Rollback()

	if err := deleteUsersByIDList(ctx, tx, []int64{id}, "删除用户失败"); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit admin user delete transaction: %w", err)
	}
	if s.authCache != nil {
		s.authCache.InvalidateUser(id)
	}
	return true, nil
}

func (s *DBService) DeleteUsers(ctx context.Context, filters []UserFilter) (bool, error) {
	if s.db == nil {
		return false, ErrUnavailable
	}

	ids, err := s.matchingUserIDs(ctx, filters)
	if err != nil {
		return false, err
	}
	if len(ids) == 0 {
		return true, nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin admin user bulk delete transaction: %w", err)
	}
	defer tx.Rollback()

	if err := deleteUsersByIDList(ctx, tx, ids, "批量删除用户信息失败"); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit admin user bulk delete transaction: %w", err)
	}
	if s.authCache != nil {
		for _, id := range ids {
			s.authCache.InvalidateUser(id)
		}
	}
	return true, nil
}

type bulkMailConfig struct {
	host                string
	port                int64
	username            string
	password            string
	encryption          string
	from                string
	fromName            string
	template            string
	appName             string
	appURL              string
	bulkIntervalSeconds int64
}

func (s *DBService) dispatchUserMailJobs(ctx context.Context, emails []string, subject, content string, cfg bulkMailConfig) error {
	emailList := make([]string, 0, len(emails))
	for _, email := range emails {
		email = strings.TrimSpace(email)
		if email != "" {
			emailList = append(emailList, email)
		}
	}
	if len(emailList) == 0 {
		return nil
	}
	if cfg.bulkIntervalSeconds < 0 {
		cfg.bulkIntervalSeconds = 0
	}

	runBatch := func(jobCtx context.Context) error {
		var batchErr error
		for idx, email := range emailList {
			var sendErr error
			if cfg.host == "" {
				sendErr = errors.New("邮件服务未配置")
			} else {
				renderedBody := renderAdminMailBody(cfg, "notify", content, nil)
				sendErr = s.adminMailSender()(cfg.host, int(cfg.port), cfg.encryption, cfg.username, cfg.password, cfg.from, cfg.fromName, email, subject, renderedBody)
			}
			_ = s.insertMailLog(jobCtx, email, subject, sendErr)
			if sendErr != nil && batchErr == nil {
				batchErr = sendErr
			}
			if idx >= len(emailList)-1 || cfg.bulkIntervalSeconds <= 0 {
				continue
			}
			if err := s.sleepContext(jobCtx, time.Duration(cfg.bulkIntervalSeconds)*time.Second); err != nil {
				if batchErr == nil {
					batchErr = err
				}
				break
			}
		}
		return batchErr
	}

	if s.jobs != nil {
		if err := s.jobs.Enqueue("send_email_mass", "batch:"+subject, runBatch); err == nil {
			return nil
		}
	}
	_ = runBatch(ctx)
	return nil
}

func (s *DBService) adminMailSender() func(host string, port int, encryption, username, password, from, fromName, to, subject, body string) error {
	if s.mailSender != nil {
		return s.mailSender
	}
	return sendMail
}

func (s *DBService) insertMailLog(ctx context.Context, email, subject string, sendErr error) error {
	if s.db == nil {
		return nil
	}
	now := time.Now().Unix()
	var errText any
	if sendErr != nil {
		errText = sendErr.Error()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO v2_mail_log (
email, subject, template_name, error, created_at, updated_at
) VALUES (
$1, $2, $3, $4, $5, $5
)`, email, subject, "notify", errText, now)
	return err
}

func (s *DBService) matchingUserIDs(ctx context.Context, filters []UserFilter) ([]int64, error) {
	whereClause, args, err := s.buildUserWhere(ctx, filters)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM v2_user u`+whereClause+` ORDER BY u.id ASC`, args...)
	if err != nil {
		return nil, fmt.Errorf("query matching admin user ids: %w", err)
	}
	defer rows.Close()

	ids := make([]int64, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan matching admin user id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate matching admin user ids: %w", err)
	}
	return ids, nil
}

func deleteUsersByIDList(ctx context.Context, tx *sql.Tx, ids []int64, message string) error {
	if err := deleteAuthSessionsByUserIDs(ctx, tx, ids, message); err != nil {
		return err
	}

	if query, args := buildInt64InQuery(`DELETE FROM v2_order WHERE user_id IN (%s)`, ids); len(args) > 0 {
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return errors.New(message)
		}
	}
	if inClause, inArgs := buildInt64Placeholders(2, ids); len(inArgs) > 0 {
		query := `UPDATE v2_order SET invite_user_id = NULL, updated_at = $1 WHERE invite_user_id IN (` + inClause + `)`
		args := append([]any{time.Now().Unix()}, inArgs...)
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return errors.New(message)
		}
	}
	if query, args := buildInt64InQuery(`DELETE FROM v2_invite_code WHERE user_id IN (%s)`, ids); len(args) > 0 {
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return errors.New(message)
		}
	}
	if query, args := buildInt64InQuery(`DELETE FROM v2_ticket_message WHERE ticket_id IN (SELECT id FROM v2_ticket WHERE user_id IN (%s))`, ids); len(args) > 0 {
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return errors.New(message)
		}
	}
	if query, args := buildInt64InQuery(`DELETE FROM v2_ticket WHERE user_id IN (%s)`, ids); len(args) > 0 {
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return errors.New(message)
		}
	}
	if inClause, inArgs := buildInt64Placeholders(2, ids); len(inArgs) > 0 {
		query := `UPDATE v2_user SET invite_user_id = NULL, updated_at = $1 WHERE invite_user_id IN (` + inClause + `)`
		args := append([]any{time.Now().Unix()}, inArgs...)
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return errors.New(message)
		}
	}
	if query, args := buildInt64InQuery(`DELETE FROM v2_user WHERE id IN (%s)`, ids); len(args) > 0 {
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return errors.New(message)
		}
	}
	return nil
}

func deleteAuthSessionsByUserIDs(ctx context.Context, tx *sql.Tx, ids []int64, message string) error {
	if len(ids) == 0 {
		return nil
	}
	query, args := buildInt64InQuery(`DELETE FROM v2_auth_session WHERE user_id IN (%s)`, ids)
	if _, err := tx.ExecContext(ctx, query, args...); err != nil {
		return errors.New(message)
	}
	return nil
}

func (s *DBService) buildUserWhere(ctx context.Context, filters []UserFilter) (string, []any, error) {
	if len(filters) == 0 {
		return "", nil, nil
	}

	parts := make([]string, 0, len(filters))
	args := make([]any, 0, len(filters))
	for _, filter := range filters {
		key := strings.TrimSpace(filter.Key)
		if key == "" {
			continue
		}
		condition, err := normalizeUserFilterCondition(filter.Condition)
		if err != nil {
			return "", nil, err
		}
		value := strings.TrimSpace(filter.Value)

		if key == "invite_by_email" {
			inviteUserID, err := s.findInviteUserIDByEmail(ctx, condition, value)
			if err != nil {
				return "", nil, err
			}
			args = append(args, inviteUserID)
			parts = append(parts, fmt.Sprintf("u.invite_user_id = $%d", len(args)))
			continue
		}

		if key == "invite_code" {
			operator := condition
			argument := any(value)
			if condition == "ILIKE" {
				argument = "%" + value + "%"
			}
			args = append(args, argument)
			parts = append(parts, fmt.Sprintf(`EXISTS (
SELECT 1
FROM v2_invite_code ic
WHERE ic.user_id = u.id
AND ic.code %s $%d
)`, operator, len(args)))
			continue
		}

		if key == "plan_id" && value == "null" {
			parts = append(parts, "u.plan_id IS NULL")
			continue
		}

		field, ok := userFilterField(key)
		if !ok {
			return "", nil, errors.New("过滤键参数有误")
		}

		operator := condition
		argument := any(value)
		if condition == "ILIKE" {
			field = userLikeFilterField(key)
			argument = "%" + value + "%"
		}
		if key == "d" || key == "transfer_enable" {
			argument = userTrafficBytesFromInput(value)
		}
		if condition != "ILIKE" && key != "remarks" && key != "email" && key != "uuid" && key != "token" {
			if parsedInt, err := strconv.ParseInt(strings.TrimSpace(fmt.Sprint(argument)), 10, 64); err == nil {
				argument = parsedInt
			}
		}

		args = append(args, argument)
		parts = append(parts, fmt.Sprintf("%s %s $%d", field, operator, len(args)))
	}

	if len(parts) == 0 {
		return "", nil, nil
	}
	return " WHERE " + strings.Join(parts, " AND "), args, nil
}

func normalizeUserFilterCondition(raw string) (string, error) {
	switch strings.TrimSpace(raw) {
	case ">", "<", "=", ">=", "<=":
		return strings.TrimSpace(raw), nil
	case "!=":
		return "<>", nil
	case "模糊":
		return "ILIKE", nil
	default:
		return "", errors.New("过滤条件参数有误")
	}
}

func userFilterField(key string) (string, bool) {
	switch key {
	case "id":
		return "u.id", true
	case "email":
		return "u.email", true
	case "transfer_enable":
		return "u.transfer_enable", true
	case "device_limit":
		return "u.device_limit", true
	case "d":
		return "u.d", true
	case "t", "last_login_at":
		return "u.t", true
	case "expired_at":
		return "u.expired_at", true
	case "uuid":
		return "u.uuid", true
	case "token":
		return "u.token", true
	case "invite_user_id":
		return "u.invite_user_id", true
	case "plan_id":
		return "u.plan_id", true
	case "banned":
		return "u.banned", true
	case "remarks":
		return "u.remarks", true
	case "is_admin":
		return "u.is_admin", true
	default:
		return "", false
	}
}

func userLikeFilterField(key string) string {
	switch key {
	case "id":
		return "CAST(u.id AS TEXT)"
	case "transfer_enable":
		return "CAST(u.transfer_enable AS TEXT)"
	case "device_limit":
		return "CAST(u.device_limit AS TEXT)"
	case "d":
		return "CAST(u.d AS TEXT)"
	case "t", "last_login_at":
		return "CAST(u.t AS TEXT)"
	case "expired_at":
		return "CAST(u.expired_at AS TEXT)"
	case "invite_user_id":
		return "CAST(u.invite_user_id AS TEXT)"
	case "plan_id":
		return "CAST(u.plan_id AS TEXT)"
	case "banned":
		return "CAST(u.banned AS TEXT)"
	case "is_admin":
		return "CAST(u.is_admin AS TEXT)"
	default:
		field, _ := userFilterField(key)
		return field
	}
}

func sanitizeUserSort(raw string) string {
	switch strings.TrimSpace(raw) {
	case "id":
		return "u.id"
	case "email":
		return "u.email"
	case "transfer_enable":
		return "u.transfer_enable"
	case "device_limit":
		return "u.device_limit"
	case "d":
		return "u.d"
	case "u":
		return "u.u"
	case "expired_at":
		return "u.expired_at"
	case "uuid":
		return "u.uuid"
	case "token":
		return "u.token"
	case "invite_user_id":
		return "u.invite_user_id"
	case "plan_id":
		return "u.plan_id"
	case "banned":
		return "u.banned"
	case "remarks":
		return "u.remarks"
	case "is_admin":
		return "u.is_admin"
	case "is_staff":
		return "u.is_staff"
	case "balance":
		return "u.balance"
	case "commission_balance":
		return "u.commission_balance"
	case "last_login_at":
		return "u.last_login_at"
	case "updated_at":
		return "u.updated_at"
	case "total_used":
		return "(u.u + u.d)"
	default:
		return "u.created_at"
	}
}

func (s *DBService) findInviteUserIDByEmail(ctx context.Context, operator, value string) (int64, error) {
	field := "email"
	argument := any(value)
	if operator == "ILIKE" {
		argument = "%" + value + "%"
	}
	var inviteUserID int64
	query := fmt.Sprintf(`SELECT id FROM v2_user WHERE %s %s $1 ORDER BY id ASC LIMIT 1`, field, operator)
	err := s.db.QueryRowContext(ctx, query, argument).Scan(&inviteUserID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("query invite user email filter: %w", err)
	}
	return inviteUserID, nil
}

func (s *DBService) buildAdminUserSubscribeURL(ctx context.Context, userID int64, token string) (string, error) {
	cfg := s.currentConfig()
	path := strings.TrimSpace(cfg.SubscribePath)
	if path == "" {
		path = "/api/v1/client/subscribe"
	}
	baseURL := strings.TrimSpace(cfg.SubscribeURL)

	switch cfg.ShowSubscribeMethod {
	case 1:
		newToken, err := s.adminOneTimeSubscribeToken(ctx, token)
		if err != nil {
			return "", err
		}
		return appendAdminTokenToURL(baseURL, path, newToken), nil
	case 2:
		ttl := cfg.ShowSubscribeExpire
		if ttl <= 0 {
			ttl = 5
		}
		counter := time.Now().Unix() / (ttl * 60)
		counterBytes := []byte{0, 0, 0, 0, 0, 0, 0, 0}
		counterBytes[4] = byte(counter >> 24)
		counterBytes[5] = byte(counter >> 16)
		counterBytes[6] = byte(counter >> 8)
		counterBytes[7] = byte(counter)
		mac := hmac.New(sha1.New, []byte(token))
		_, _ = mac.Write(counterBytes)
		hashed := hex.EncodeToString(mac.Sum(nil))
		clientToken := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%d:%s", userID, hashed)))
		return appendAdminTokenToURL(baseURL, path, clientToken), nil
	default:
		return appendAdminTokenToURL(baseURL, path, token), nil
	}
}

func (s *DBService) adminOneTimeSubscribeToken(ctx context.Context, token string) (string, error) {
	cacheKey := "otp_" + token
	if value, ok, err := s.getStringKV(ctx, cacheKey); err != nil {
		return "", err
	} else if ok && strings.TrimSpace(value) != "" {
		return value, nil
	}

	buf := make([]byte, 24)
	if _, err := crand.Read(buf); err != nil {
		return "", fmt.Errorf("generate admin one-time subscribe token: %w", err)
	}
	newToken := base64.RawURLEncoding.EncodeToString(buf)
	if err := s.setStringKV(ctx, cacheKey, newToken, 86400); err != nil {
		return "", err
	}
	if err := s.setStringKV(ctx, "otpn_"+newToken, token, 86400); err != nil {
		return "", err
	}
	return newToken, nil
}

func (s *DBService) getStringKV(ctx context.Context, key string) (string, bool, error) {
	var value string
	var expireAt int64
	err := s.db.QueryRowContext(ctx, `SELECT v, expire_at FROM v2_runtime_kv WHERE k = $1 LIMIT 1`, key).Scan(&value, &expireAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("query admin runtime kv: %w", err)
	}
	if expireAt > 0 && expireAt <= time.Now().Unix() {
		return "", false, nil
	}
	return value, true, nil
}

func adminAliveUserRuntimeKey(userID int64) string {
	return "ALIVE_IP_USER_" + strconv.FormatInt(userID, 10)
}

func (s *DBService) setStringKV(ctx context.Context, key, value string, ttlSeconds int64) error {
	now := time.Now().Unix()
	expireAt := int64(0)
	if ttlSeconds > 0 {
		expireAt = now + ttlSeconds
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO v2_runtime_kv (k, v, expire_at, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (k) DO UPDATE SET v = EXCLUDED.v, expire_at = EXCLUDED.expire_at, updated_at = EXCLUDED.updated_at`,
		key, value, expireAt, now, now)
	if err != nil {
		return fmt.Errorf("set admin runtime kv: %w", err)
	}
	return nil
}

func (s *DBService) loadUserPlan(ctx context.Context, id int64) (userPlanSnapshot, error) {
	var plan userPlanSnapshot
	err := s.db.QueryRowContext(ctx, `SELECT id, group_id, transfer_enable, device_limit
FROM v2_plan
WHERE id = $1
LIMIT 1`, id).Scan(&plan.ID, &plan.GroupID, &plan.TransferEnable, &plan.DeviceLimit)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return userPlanSnapshot{}, errors.New("订阅计划不存在")
		}
		return userPlanSnapshot{}, fmt.Errorf("query user plan: %w", err)
	}
	return plan, nil
}

func (s *DBService) optionalUserPlan(ctx context.Context, raw string) (*userPlanSnapshot, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return nil, errors.New("订阅计划不存在")
	}
	plan, err := s.loadUserPlan(ctx, id)
	if err != nil {
		return nil, err
	}
	return &plan, nil
}

func (s *DBService) userEmailExists(ctx context.Context, email string) (bool, error) {
	email = strings.TrimSpace(strings.ToLower(email))
	var exists bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM v2_user WHERE LOWER(email) = $1)`, email).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("query user email exists: %w", err)
	}
	return exists, nil
}

func (s *DBService) generateUniqueEmailTx(ctx context.Context, tx *sql.Tx, suffix string, seen map[string]struct{}) (string, error) {
	suffix = strings.TrimSpace(strings.ToLower(suffix))
	for range 32 {
		localPart, err := randomAlphaNumeric(6)
		if err != nil {
			return "", err
		}
		email := strings.ToLower(localPart + "@" + suffix)
		if _, ok := seen[email]; ok {
			continue
		}
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM v2_user WHERE LOWER(email) = $1)`, email).Scan(&exists); err != nil {
			return "", err
		}
		if exists {
			continue
		}
		seen[email] = struct{}{}
		return email, nil
	}
	return "", errors.New("failed to generate unique email")
}

func (s *DBService) queryJSONMap(ctx context.Context, query string, args ...any) (map[string]any, error) {
	var raw []byte
	err := s.db.QueryRowContext(ctx, query, args...).Scan(&raw)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("decode json map: %w", err)
	}
	return result, nil
}

func parseRequiredBinaryInt(raw string, message string) (int64, error) {
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || (value != 0 && value != 1) {
		return 0, errors.New(message)
	}
	return value, nil
}

func parseNullableInt64Input(values map[string]string, key string) (*int64, bool, error) {
	raw, ok := values[key]
	if !ok {
		return nil, false, nil
	}
	value, err := parseOptionalUserInt64(strings.TrimSpace(raw))
	return value, true, err
}

func parseOptionalUserInt64(raw string) (*int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return nil, err
	}
	return &value, nil
}

func userTrafficBytesFromInput(raw string) int64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0
	}
	return int64(math.Round(value * float64(gibibyte)))
}

func randomHexString(byteLen int) (string, error) {
	buf := make([]byte, byteLen)
	if _, err := crand.Read(buf); err != nil {
		return "", fmt.Errorf("generate random hex: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func randomUUIDString() (string, error) {
	buf := make([]byte, 16)
	if _, err := crand.Read(buf); err != nil {
		return "", fmt.Errorf("generate uuid: %w", err)
	}
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16]), nil
}

func appendAdminTokenToURL(baseURL, path, token string) string {
	if strings.TrimSpace(path) == "" {
		path = "/api/v1/client/subscribe"
	}
	if baseURL == "" {
		return path + "?token=" + url.QueryEscape(token)
	}
	return strings.TrimRight(baseURL, "/") + path + "?token=" + url.QueryEscape(token)
}

func formatOptionalDateTime(value *int64, fallback string) string {
	if value == nil || *value <= 0 {
		return fallback
	}
	return time.Unix(*value, 0).Format("2006-01-02 15:04:05")
}

func formatDecimalDiv100(value int64) string {
	return formatFloatForCSV(float64(value) / 100)
}

func formatFloatForCSV(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func buildInt64Placeholders(startAt int, values []int64) (string, []any) {
	parts := make([]string, 0, len(values))
	args := make([]any, 0, len(values))
	for index, value := range values {
		parts = append(parts, fmt.Sprintf("$%d", startAt+index))
		args = append(args, value)
	}
	return strings.Join(parts, ","), args
}

func isValidEmail(value string) bool {
	address, err := mail.ParseAddress(value)
	return err == nil && strings.EqualFold(strings.TrimSpace(address.Address), strings.TrimSpace(value))
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return map[string]string{}
	}
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func mapAnyInt64(value any) int64 {
	switch typed := value.(type) {
	case int64:
		return typed
	case int32:
		return int64(typed)
	case int:
		return int64(typed)
	case float64:
		return int64(typed)
	case json.Number:
		parsed, _ := typed.Int64()
		return parsed
	case string:
		parsed, _ := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		return parsed
	default:
		parsed, _ := strconv.ParseInt(strings.TrimSpace(fmt.Sprint(value)), 10, 64)
		return parsed
	}
}

func mapNullableAnyInt64(value any) *int64 {
	if value == nil {
		return nil
	}
	next := mapAnyInt64(value)
	return &next
}

func nullableUserPlanID(plan *userPlanSnapshot) *int64 {
	if plan == nil {
		return nil
	}
	return &plan.ID
}

func nullableUserPlanGroupID(plan *userPlanSnapshot) *int64 {
	if plan == nil {
		return nil
	}
	return &plan.GroupID
}

func nullableUserPlanDeviceLimit(plan *userPlanSnapshot) *int64 {
	if plan == nil || !plan.DeviceLimit.Valid {
		return nil
	}
	value := plan.DeviceLimit.Int64
	return &value
}

func userPlanTransferEnable(plan *userPlanSnapshot) int64 {
	if plan == nil {
		return 0
	}
	return planTransferEnableBytes(plan.TransferEnable)
}
