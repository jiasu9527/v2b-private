package main

import (
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"flag"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

type mergeSourceUser struct {
	ID                int64
	Email             string
	EmailKey          string
	InviteUserID      *int64
	TelegramID        *int64
	Password          string
	PasswordAlgo      *string
	PasswordSalt      *string
	Balance           int64
	Discount          *int64
	CommissionType    int64
	CommissionRate    *int64
	CommissionBalance int64
	T                 int64
	U                 int64
	D                 int64
	TransferEnable    int64
	DeviceLimit       *int64
	Banned            int64
	IsAdmin           int64
	LastLoginAt       *int64
	IsStaff           int64
	LastLoginIP       *int64
	UUID              string
	GroupID           *int64
	PlanID            *int64
	SpeedLimit        *int64
	AutoRenewal       *int64
	RemindExpire      *int64
	RemindTraffic     *int64
	Token             string
	ExpiredAt         *int64
	Remarks           *string
	CreatedAt         int64
	UpdatedAt         int64
}

type mergeTargetUser struct {
	ID             int64
	Email          string
	EmailKey       string
	InviteUserID   *int64
	Token          string
	UUID           string
	GroupID        *int64
	PlanID         *int64
	TransferEnable int64
	DeviceLimit    *int64
	SpeedLimit     *int64
	ExpiredAt      *int64
}

type mergeTargetPlanInfo struct {
	ID             int64
	Name           string
	Users          int64
	GroupID        int64
	TransferEnable int64
	DeviceLimit    *int64
	SpeedLimit     *int64
}

type mergePlanSummary struct {
	ID    int64
	Name  string
	Users int64
}

type mergeSourceInviteCode struct {
	ID               int64
	UserID           int64
	Code             string
	Status           int64
	InviteCampaignID *int64
	PV               int64
	CreatedAt        int64
	UpdatedAt        int64
}

type mergeSourceInviteCampaign struct {
	ID            int64
	UserID        int64
	PlanID        int64
	Period        string
	InviteCodeID  *int64
	InviteCode    *string
	RewardAmount  int64
	TargetAmount  int64
	CurrentAmount int64
	InviteCount   int64
	Status        int64
	StartedAt     int64
	ExpiredAt     int64
	CompletedAt   *int64
	AbandonedAt   *int64
	UsedAt        *int64
	CreatedAt     int64
	UpdatedAt     int64
}

type mergeSourceInviteCampaignRecord struct {
	ID            int64
	CampaignID    int64
	InviteeUserID int64
	InviteCode    string
	RewardAmount  int64
	CreatedAt     int64
	UpdatedAt     int64
}

type mergeInsertUser struct {
	Email             string
	Password          string
	PasswordAlgo      *string
	PasswordSalt      *string
	TelegramID        *int64
	Balance           int64
	Discount          *int64
	CommissionType    int64
	CommissionRate    *int64
	CommissionBalance int64
	T                 int64
	U                 int64
	D                 int64
	TransferEnable    int64
	DeviceLimit       *int64
	Banned            int64
	IsAdmin           int64
	LastLoginAt       *int64
	IsStaff           int64
	LastLoginIP       *int64
	UUID              string
	GroupID           *int64
	PlanID            *int64
	SpeedLimit        *int64
	AutoRenewal       *int64
	RemindExpire      *int64
	RemindTraffic     *int64
	Token             string
	ExpiredAt         *int64
	Remarks           *string
	CreatedAt         int64
	UpdatedAt         int64
}

type mergeResult struct {
	UsersInserted            int
	UsersMatchedExisting     int
	UsersSkippedUnmappedPlan int
	UsersSkippedInvalidEmail int
	UsersSkippedNoPassword   int
	InviteRelationsUpdated   int
	InviteCodesInserted      int
	InviteCodesReused        int
	InviteCodesRenamed       int
	InviteCampaignsInserted  int
	InviteCampaignsReused    int
	InviteCampaignsSkipped   int
	InviteRecordsInserted    int
	InviteRecordsReused      int
	InviteRecordsSkipped     int
}

type queryRower interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

type queryExecer interface {
	queryRower
	execer
}

func runInspectMergeMySQL(args []string) error {
	flags := flag.NewFlagSet("inspect-merge-mysql", flag.ContinueOnError)
	sourceHost := flags.String("source-host", "127.0.0.1", "源 MySQL 主机")
	sourcePort := flags.String("source-port", "3306", "源 MySQL 端口")
	sourceDatabase := flags.String("source-database", "", "源 MySQL 数据库名")
	sourceUsername := flags.String("source-username", "", "源 MySQL 用户名")
	sourcePassword := flags.String("source-password", "", "源 MySQL 密码")
	sourceCharset := flags.String("source-charset", "utf8mb4", "源 MySQL 字符集")
	targetDSN := flags.String("target-dsn", "", "PostgreSQL DSN")
	if err := flags.Parse(args); err != nil {
		return err
	}

	sourceCfg, err := legacyMySQLConfigFromFlags(*sourceHost, *sourcePort, *sourceDatabase, *sourceUsername, *sourcePassword, *sourceCharset)
	if err != nil {
		return err
	}
	resolvedTargetDSN, err := resolveDSN(*targetDSN)
	if err != nil {
		return err
	}

	sourceDB, err := openMySQL(sourceCfg.DSN())
	if err != nil {
		return err
	}
	defer sourceDB.Close()

	targetDB, err := openDB(resolvedTargetDSN)
	if err != nil {
		return err
	}
	defer targetDB.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	sourcePlans, sourceUsersTotal, sourceUsersWithoutPlan, err := inspectSourceMergePlans(ctx, sourceDB)
	if err != nil {
		return err
	}
	targetPlans, err := loadMergeTargetPlans(ctx, targetDB)
	if err != nil {
		return err
	}

	fmt.Printf("source_users_total=%d\n", sourceUsersTotal)
	fmt.Printf("source_users_without_plan=%d\n", sourceUsersWithoutPlan)
	for _, item := range sourcePlans {
		fmt.Printf("source_plan\t%d\t%s\t%d\n", item.ID, escapeInspectField(item.Name), item.Users)
	}
	for _, item := range targetPlans {
		fmt.Printf("target_plan\t%d\t%s\t%d\n", item.ID, escapeInspectField(item.Name), item.Users)
	}
	return nil
}

func runMergeMySQL(args []string) error {
	flags := flag.NewFlagSet("merge-mysql", flag.ContinueOnError)
	sourceHost := flags.String("source-host", "127.0.0.1", "源 MySQL 主机")
	sourcePort := flags.String("source-port", "3306", "源 MySQL 端口")
	sourceDatabase := flags.String("source-database", "", "源 MySQL 数据库名")
	sourceUsername := flags.String("source-username", "", "源 MySQL 用户名")
	sourcePassword := flags.String("source-password", "", "源 MySQL 密码")
	sourceCharset := flags.String("source-charset", "utf8mb4", "源 MySQL 字符集")
	targetDSN := flags.String("target-dsn", "", "PostgreSQL DSN")
	planMapRaw := flags.String("plan-map", "", "旧套餐到新套餐映射，格式 1:10,2:20")
	if err := flags.Parse(args); err != nil {
		return err
	}

	sourceCfg, err := legacyMySQLConfigFromFlags(*sourceHost, *sourcePort, *sourceDatabase, *sourceUsername, *sourcePassword, *sourceCharset)
	if err != nil {
		return err
	}
	resolvedTargetDSN, err := resolveDSN(*targetDSN)
	if err != nil {
		return err
	}
	planMap, err := parseMergeMySQLPlanMap(*planMapRaw)
	if err != nil {
		return err
	}

	sourceDB, err := openMySQL(sourceCfg.DSN())
	if err != nil {
		return err
	}
	defer sourceDB.Close()

	targetDB, err := openDB(resolvedTargetDSN)
	if err != nil {
		return err
	}
	defer targetDB.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	result, err := mergeMySQLUsersIntoPostgres(ctx, sourceDB, targetDB, planMap)
	if err != nil {
		return err
	}

	fmt.Println("merge_status=applied")
	fmt.Printf("users_inserted=%d\n", result.UsersInserted)
	fmt.Printf("users_matched_existing=%d\n", result.UsersMatchedExisting)
	fmt.Printf("users_skipped_unmapped_plan=%d\n", result.UsersSkippedUnmappedPlan)
	fmt.Printf("users_skipped_invalid_email=%d\n", result.UsersSkippedInvalidEmail)
	fmt.Printf("users_skipped_no_password=%d\n", result.UsersSkippedNoPassword)
	fmt.Printf("invite_relations_updated=%d\n", result.InviteRelationsUpdated)
	fmt.Printf("invite_codes_inserted=%d\n", result.InviteCodesInserted)
	fmt.Printf("invite_codes_reused=%d\n", result.InviteCodesReused)
	fmt.Printf("invite_codes_renamed=%d\n", result.InviteCodesRenamed)
	fmt.Printf("invite_campaigns_inserted=%d\n", result.InviteCampaignsInserted)
	fmt.Printf("invite_campaigns_reused=%d\n", result.InviteCampaignsReused)
	fmt.Printf("invite_campaigns_skipped=%d\n", result.InviteCampaignsSkipped)
	fmt.Printf("invite_records_inserted=%d\n", result.InviteRecordsInserted)
	fmt.Printf("invite_records_reused=%d\n", result.InviteRecordsReused)
	fmt.Printf("invite_records_skipped=%d\n", result.InviteRecordsSkipped)
	return nil
}

func legacyMySQLConfigFromFlags(host, port, database, username, password, charset string) (legacyMySQLConfig, error) {
	cfg := legacyMySQLConfig{
		Host:     strings.TrimSpace(defaultValue(host, "127.0.0.1")),
		Port:     strings.TrimSpace(defaultValue(port, "3306")),
		Database: strings.TrimSpace(database),
		Username: strings.TrimSpace(username),
		Password: password,
		Charset:  strings.TrimSpace(defaultValue(charset, "utf8mb4")),
	}
	if cfg.Database == "" || cfg.Username == "" {
		return legacyMySQLConfig{}, fmt.Errorf("源 MySQL 配置缺少数据库名或用户名")
	}
	return cfg, nil
}

func parseMergeMySQLPlanMap(raw string) (map[int64]int64, error) {
	planMap := make(map[int64]int64)
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return planMap, nil
	}

	tokens := strings.FieldsFunc(trimmed, func(r rune) bool {
		switch r {
		case ',', '\n', '\r', '\t', ' ':
			return true
		default:
			return false
		}
	})
	for _, token := range tokens {
		if strings.TrimSpace(token) == "" {
			continue
		}
		parts := strings.Split(token, ":")
		if len(parts) != 2 {
			return nil, fmt.Errorf("无效的套餐映射：%s", token)
		}
		sourceID, err := strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64)
		if err != nil || sourceID <= 0 {
			return nil, fmt.Errorf("无效的旧套餐 ID：%s", strings.TrimSpace(parts[0]))
		}
		targetID, err := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
		if err != nil || targetID <= 0 {
			return nil, fmt.Errorf("无效的新套餐 ID：%s", strings.TrimSpace(parts[1]))
		}
		planMap[sourceID] = targetID
	}
	return planMap, nil
}

func ensureUniqueMergeInviteCode(base, email string, used map[string]struct{}) string {
	if used == nil {
		used = map[string]struct{}{}
	}
	for _, candidate := range mergeInviteCodeCandidates(base, email) {
		if _, exists := used[candidate]; exists {
			continue
		}
		used[candidate] = struct{}{}
		return candidate
	}
	return ""
}

func mergeInviteCodeCandidates(base, email string) []string {
	sanitized := sanitizeMergeInviteCode(base)
	if sanitized == "" {
		sanitized = "INVITE"
	}
	if len(sanitized) > 32 {
		sanitized = sanitized[:32]
	}

	candidates := []string{sanitized}
	digest := sha1.Sum([]byte(strings.ToLower(strings.TrimSpace(email)) + "|" + sanitized))
	hexDigest := strings.ToUpper(hex.EncodeToString(digest[:]))
	suffixes := []string{
		hexDigest[:6],
		hexDigest[:8],
		hexDigest[:10],
		hexDigest[:12],
	}
	for idx, suffix := range suffixes {
		counter := strings.ToUpper(strconv.FormatInt(int64(idx+1), 16))
		if idx == 0 {
			counter = ""
		}
		fullSuffix := suffix + counter
		prefixLen := 32 - len(fullSuffix)
		if prefixLen < 1 {
			prefixLen = 1
			fullSuffix = fullSuffix[:31]
		}
		prefix := sanitized
		if len(prefix) > prefixLen {
			prefix = prefix[:prefixLen]
		}
		candidate := prefix + fullSuffix
		if candidate != sanitized {
			candidates = append(candidates, candidate)
		}
	}
	return candidates
}

func sanitizeMergeInviteCode(raw string) string {
	var builder strings.Builder
	for _, r := range strings.TrimSpace(raw) {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

func inspectSourceMergePlans(ctx context.Context, sourceDB *sql.DB) ([]mergePlanSummary, int64, int64, error) {
	sourceTables, err := mysqlTableNames(ctx, sourceDB)
	if err != nil {
		return nil, 0, 0, err
	}
	sourceSet := tableNameSet(sourceTables)
	if _, ok := sourceSet["v2_user"]; !ok {
		return nil, 0, 0, fmt.Errorf("源 MySQL 缺少 v2_user 表")
	}

	planCounts := make(map[int64]int64)
	var usersTotal int64
	var usersWithoutPlan int64
	rows, err := sourceDB.QueryContext(ctx, `SELECT plan_id, COUNT(*) FROM v2_user GROUP BY plan_id ORDER BY plan_id`)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("读取源库套餐统计失败: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var planID sql.NullInt64
		var count int64
		if err := rows.Scan(&planID, &count); err != nil {
			return nil, 0, 0, fmt.Errorf("扫描源库套餐统计失败: %w", err)
		}
		usersTotal += count
		if !planID.Valid || planID.Int64 <= 0 {
			usersWithoutPlan += count
			continue
		}
		planCounts[planID.Int64] = count
	}
	if err := rows.Err(); err != nil {
		return nil, 0, 0, fmt.Errorf("读取源库套餐统计失败: %w", err)
	}

	planNames := make(map[int64]string)
	if _, ok := sourceSet["v2_plan"]; ok {
		nameRows, err := sourceDB.QueryContext(ctx, `SELECT id, name FROM v2_plan ORDER BY id`)
		if err != nil {
			return nil, 0, 0, fmt.Errorf("读取源库套餐列表失败: %w", err)
		}
		defer nameRows.Close()
		for nameRows.Next() {
			var id int64
			var name sql.NullString
			if err := nameRows.Scan(&id, &name); err != nil {
				return nil, 0, 0, fmt.Errorf("扫描源库套餐列表失败: %w", err)
			}
			planNames[id] = strings.TrimSpace(name.String)
		}
		if err := nameRows.Err(); err != nil {
			return nil, 0, 0, fmt.Errorf("读取源库套餐列表失败: %w", err)
		}
	}

	ids := make([]int64, 0, len(planCounts))
	for id := range planCounts {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	plans := make([]mergePlanSummary, 0, len(ids))
	for _, id := range ids {
		plans = append(plans, mergePlanSummary{
			ID:    id,
			Name:  strings.TrimSpace(planNames[id]),
			Users: planCounts[id],
		})
	}
	return plans, usersTotal, usersWithoutPlan, nil
}

func loadMergeTargetPlans(ctx context.Context, db queryRower) ([]mergeTargetPlanInfo, error) {
	rows, err := db.QueryContext(ctx, `SELECT p.id, p.name, p.group_id, p.transfer_enable, p.device_limit, p.speed_limit, COALESCE(COUNT(u.id), 0)
FROM v2_plan p
LEFT JOIN v2_user u ON u.plan_id = p.id
GROUP BY p.id, p.name, p.group_id, p.transfer_enable, p.device_limit, p.speed_limit
ORDER BY p.id`)
	if err != nil {
		return nil, fmt.Errorf("读取目标站套餐列表失败: %w", err)
	}
	defer rows.Close()

	plans := make([]mergeTargetPlanInfo, 0)
	for rows.Next() {
		var item mergeTargetPlanInfo
		var deviceLimit sql.NullInt64
		var speedLimit sql.NullInt64
		if err := rows.Scan(&item.ID, &item.Name, &item.GroupID, &item.TransferEnable, &deviceLimit, &speedLimit, &item.Users); err != nil {
			return nil, fmt.Errorf("扫描目标站套餐列表失败: %w", err)
		}
		item.Name = strings.TrimSpace(item.Name)
		item.DeviceLimit = nullInt64Ptr(deviceLimit)
		item.SpeedLimit = nullInt64Ptr(speedLimit)
		plans = append(plans, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取目标站套餐列表失败: %w", err)
	}
	return plans, nil
}

func mergeMySQLUsersIntoPostgres(ctx context.Context, sourceDB, targetDB *sql.DB, planMap map[int64]int64) (mergeResult, error) {
	var result mergeResult

	sourceTables, err := mysqlTableNames(ctx, sourceDB)
	if err != nil {
		return result, err
	}
	sourceSet := tableNameSet(sourceTables)
	if _, ok := sourceSet["v2_user"]; !ok {
		return result, fmt.Errorf("源 MySQL 缺少 v2_user 表")
	}

	sourceUsers, err := loadMergeSourceUsers(ctx, sourceDB)
	if err != nil {
		return result, err
	}
	sourceUsersByID := make(map[int64]mergeSourceUser, len(sourceUsers))
	for _, item := range sourceUsers {
		sourceUsersByID[item.ID] = item
	}

	targetPlans, err := loadMergeTargetPlans(ctx, targetDB)
	if err != nil {
		return result, err
	}
	targetPlanByID := make(map[int64]mergeTargetPlanInfo, len(targetPlans))
	for _, item := range targetPlans {
		targetPlanByID[item.ID] = item
	}

	tx, err := targetDB.BeginTx(ctx, nil)
	if err != nil {
		return result, fmt.Errorf("开启目标库事务失败: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	targetUsersByEmail, usedTokens, usedUUIDs, err := loadMergeTargetUsers(ctx, tx)
	if err != nil {
		return result, err
	}
	userIDMap := make(map[int64]int64, len(sourceUsers))

	for _, sourceUser := range sourceUsers {
		if sourceUser.EmailKey == "" || !strings.Contains(sourceUser.Email, "@") {
			result.UsersSkippedInvalidEmail++
			continue
		}
		if existing, ok := targetUsersByEmail[sourceUser.EmailKey]; ok {
			updatedExisting, err := mergeTargetUserSubscriptionIfNeeded(ctx, tx, existing, sourceUser, targetPlanByID, planMap)
			if err != nil {
				return result, err
			}
			targetUsersByEmail[sourceUser.EmailKey] = updatedExisting
			userIDMap[sourceUser.ID] = existing.ID
			result.UsersMatchedExisting++
			continue
		}
		if strings.TrimSpace(sourceUser.Password) == "" {
			result.UsersSkippedNoPassword++
			continue
		}

		insertUser, mapped, err := buildMergeInsertUser(sourceUser, targetPlanByID, planMap, usedTokens, usedUUIDs)
		if err != nil {
			return result, err
		}
		if !mapped {
			result.UsersSkippedUnmappedPlan++
			continue
		}
		insertedID, err := insertMergeUser(ctx, tx, insertUser)
		if err != nil {
			return result, err
		}
		userIDMap[sourceUser.ID] = insertedID
		targetUsersByEmail[sourceUser.EmailKey] = mergeTargetUser{
			ID:             insertedID,
			Email:          sourceUser.Email,
			EmailKey:       sourceUser.EmailKey,
			Token:          insertUser.Token,
			UUID:           insertUser.UUID,
			GroupID:        insertUser.GroupID,
			PlanID:         insertUser.PlanID,
			TransferEnable: insertUser.TransferEnable,
			DeviceLimit:    insertUser.DeviceLimit,
			SpeedLimit:     insertUser.SpeedLimit,
			ExpiredAt:      cloneInt64Ptr(insertUser.ExpiredAt),
		}
		usedTokens[insertUser.Token] = struct{}{}
		usedUUIDs[insertUser.UUID] = struct{}{}
		result.UsersInserted++
	}

	for _, sourceUser := range sourceUsers {
		targetUserID, ok := userIDMap[sourceUser.ID]
		if !ok || targetUserID <= 0 || sourceUser.InviteUserID == nil {
			continue
		}
		inviteUserID, ok := userIDMap[*sourceUser.InviteUserID]
		if !ok || inviteUserID <= 0 || inviteUserID == targetUserID {
			continue
		}
		updated, err := updateMergeInviteRelation(ctx, tx, targetUsersByEmail, sourceUser.EmailKey, targetUserID, inviteUserID)
		if err != nil {
			return result, err
		}
		if updated {
			result.InviteRelationsUpdated++
		}
	}

	targetInviteCodeByUserCode, targetInviteCodeOwner, err := loadMergeTargetInviteCodes(ctx, tx)
	if err != nil {
		return result, err
	}
	targetCampaignBySignature, err := loadMergeTargetInviteCampaigns(ctx, tx)
	if err != nil {
		return result, err
	}
	targetCampaignRecordKeys, err := loadMergeTargetInviteCampaignRecords(ctx, tx)
	if err != nil {
		return result, err
	}

	sourceInviteCodes, err := loadMergeSourceInviteCodes(ctx, sourceDB, sourceSet)
	if err != nil {
		return result, err
	}
	codeIDMap := make(map[int64]int64, len(sourceInviteCodes))
	sourceCodeCampaignMap := make(map[int64]int64, len(sourceInviteCodes))
	for _, sourceCode := range sourceInviteCodes {
		targetUserID, ok := userIDMap[sourceCode.UserID]
		if !ok || targetUserID <= 0 {
			continue
		}
		sourceOwner, ok := sourceUsersByID[sourceCode.UserID]
		if !ok {
			continue
		}
		resolvedCode, existingCodeID, renamed := resolveMergeInviteCodeForUser(targetUserID, sourceCode.Code, sourceOwner.Email, targetInviteCodeByUserCode, targetInviteCodeOwner)
		if existingCodeID > 0 {
			codeIDMap[sourceCode.ID] = existingCodeID
			result.InviteCodesReused++
		} else {
			insertedCodeID, err := insertMergeInviteCode(ctx, tx, targetUserID, resolvedCode, sourceCode.Status, sourceCode.PV, sourceCode.CreatedAt, sourceCode.UpdatedAt)
			if err != nil {
				return result, err
			}
			if _, ok := targetInviteCodeByUserCode[targetUserID]; !ok {
				targetInviteCodeByUserCode[targetUserID] = make(map[string]int64)
			}
			targetInviteCodeByUserCode[targetUserID][resolvedCode] = insertedCodeID
			targetInviteCodeOwner[resolvedCode] = targetUserID
			codeIDMap[sourceCode.ID] = insertedCodeID
			result.InviteCodesInserted++
		}
		if renamed {
			result.InviteCodesRenamed++
		}
		if sourceCode.InviteCampaignID != nil && *sourceCode.InviteCampaignID > 0 {
			sourceCodeCampaignMap[sourceCode.ID] = *sourceCode.InviteCampaignID
		}
	}

	sourceCampaigns, err := loadMergeSourceInviteCampaigns(ctx, sourceDB, sourceSet)
	if err != nil {
		return result, err
	}
	campaignIDMap := make(map[int64]int64, len(sourceCampaigns))
	campaignFinalCodeBySourceID := make(map[int64]string, len(sourceCampaigns))
	for _, sourceCampaign := range sourceCampaigns {
		targetUserID, ok := userIDMap[sourceCampaign.UserID]
		if !ok || targetUserID <= 0 {
			result.InviteCampaignsSkipped++
			continue
		}
		targetPlanID, ok := planMap[sourceCampaign.PlanID]
		if !ok || targetPlanID <= 0 {
			result.InviteCampaignsSkipped++
			continue
		}
		if _, ok := targetPlanByID[targetPlanID]; !ok {
			result.InviteCampaignsSkipped++
			continue
		}

		var targetInviteCodeID *int64
		var targetInviteCode *string
		if sourceCampaign.InviteCodeID != nil {
			if mappedCodeID, ok := codeIDMap[*sourceCampaign.InviteCodeID]; ok && mappedCodeID > 0 {
				targetInviteCodeID = int64Ptr(mappedCodeID)
				resolvedCode := lookupMergeInviteCodeByID(mappedCodeID, targetInviteCodeByUserCode)
				if resolvedCode != "" {
					targetInviteCode = stringPtr(resolvedCode)
				}
			}
		}
		if targetInviteCode == nil && sourceCampaign.InviteCode != nil {
			trimmed := strings.TrimSpace(*sourceCampaign.InviteCode)
			if trimmed != "" {
				targetInviteCode = stringPtr(trimmed)
			}
		}

		signature := mergeInviteCampaignSignature(targetUserID, targetPlanID, sourceCampaign.Period, derefString(targetInviteCode), sourceCampaign.StartedAt, sourceCampaign.ExpiredAt, sourceCampaign.CreatedAt)
		if existingCampaignID, ok := targetCampaignBySignature[signature]; ok {
			campaignIDMap[sourceCampaign.ID] = existingCampaignID
			campaignFinalCodeBySourceID[sourceCampaign.ID] = derefString(targetInviteCode)
			result.InviteCampaignsReused++
			continue
		}

		insertedCampaignID, err := insertMergeInviteCampaign(ctx, tx, targetUserID, targetPlanID, targetInviteCodeID, targetInviteCode, sourceCampaign)
		if err != nil {
			return result, err
		}
		campaignIDMap[sourceCampaign.ID] = insertedCampaignID
		campaignFinalCodeBySourceID[sourceCampaign.ID] = derefString(targetInviteCode)
		targetCampaignBySignature[signature] = insertedCampaignID
		result.InviteCampaignsInserted++
	}

	for sourceCodeID, sourceCampaignID := range sourceCodeCampaignMap {
		targetCodeID, ok := codeIDMap[sourceCodeID]
		if !ok || targetCodeID <= 0 {
			continue
		}
		targetCampaignID, ok := campaignIDMap[sourceCampaignID]
		if !ok || targetCampaignID <= 0 {
			continue
		}
		if err := attachMergeInviteCodeCampaign(ctx, tx, targetCodeID, targetCampaignID); err != nil {
			return result, err
		}
	}

	sourceRecords, err := loadMergeSourceInviteCampaignRecords(ctx, sourceDB, sourceSet)
	if err != nil {
		return result, err
	}
	for _, sourceRecord := range sourceRecords {
		targetCampaignID, ok := campaignIDMap[sourceRecord.CampaignID]
		if !ok || targetCampaignID <= 0 {
			result.InviteRecordsSkipped++
			continue
		}
		targetInviteeUserID, ok := userIDMap[sourceRecord.InviteeUserID]
		if !ok || targetInviteeUserID <= 0 {
			result.InviteRecordsSkipped++
			continue
		}
		recordKey := mergeInviteCampaignRecordSignature(targetCampaignID, targetInviteeUserID)
		if _, ok := targetCampaignRecordKeys[recordKey]; ok {
			result.InviteRecordsReused++
			continue
		}
		inviteCode := campaignFinalCodeBySourceID[sourceRecord.CampaignID]
		if inviteCode == "" {
			inviteCode = strings.TrimSpace(sourceRecord.InviteCode)
		}
		if err := insertMergeInviteCampaignRecord(ctx, tx, targetCampaignID, targetInviteeUserID, inviteCode, sourceRecord); err != nil {
			return result, err
		}
		targetCampaignRecordKeys[recordKey] = struct{}{}
		result.InviteRecordsInserted++
	}

	if err := tx.Commit(); err != nil {
		return result, fmt.Errorf("提交目标库事务失败: %w", err)
	}
	return result, nil
}

func loadMergeSourceUsers(ctx context.Context, sourceDB *sql.DB) ([]mergeSourceUser, error) {
	rows, err := queryRowsAsMaps(ctx, sourceDB, `SELECT * FROM v2_user ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("读取源库用户失败: %w", err)
	}
	users := make([]mergeSourceUser, 0, len(rows))
	for _, row := range rows {
		user, err := mergeSourceUserFromRow(row)
		if err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	return users, nil
}

func mergeSourceUserFromRow(row map[string]any) (mergeSourceUser, error) {
	id, ok := rowInt64(row, "id")
	if !ok || id <= 0 {
		return mergeSourceUser{}, fmt.Errorf("源库用户存在无效的 id")
	}
	email := strings.TrimSpace(rowString(row, "email"))
	createdAt := rowInt64Default(row, time.Now().Unix(), "created_at")
	updatedAt := rowInt64Default(row, createdAt, "updated_at")
	banned, hasBanned := rowInt64(row, "banned")
	if !hasBanned {
		if enable, ok := rowInt64(row, "enable"); ok {
			if enable > 0 {
				banned = 0
			} else {
				banned = 1
			}
		}
	}
	planID := positiveInt64Ptr(rowInt64Ptr(row, "plan_id"))
	groupID := positiveInt64Ptr(rowInt64Ptr(row, "group_id"))
	inviteUserID := positiveInt64Ptr(rowInt64Ptr(row, "invite_user_id"))

	user := mergeSourceUser{
		ID:                id,
		Email:             email,
		EmailKey:          normalizeMergeEmailKey(email),
		InviteUserID:      inviteUserID,
		TelegramID:        positiveInt64Ptr(rowInt64Ptr(row, "telegram_id")),
		Password:          rowString(row, "password"),
		PasswordAlgo:      trimmedStringPtr(rowStringPtr(row, "password_algo")),
		PasswordSalt:      trimmedStringPtr(rowStringPtr(row, "password_salt")),
		Balance:           rowInt64Default(row, 0, "balance"),
		Discount:          rowInt64Ptr(row, "discount"),
		CommissionType:    rowInt64Default(row, 0, "commission_type"),
		CommissionRate:    rowInt64Ptr(row, "commission_rate"),
		CommissionBalance: rowInt64Default(row, 0, "commission_balance"),
		T:                 rowInt64Default(row, 0, "t"),
		U:                 rowInt64Default(row, 0, "u"),
		D:                 rowInt64Default(row, 0, "d"),
		TransferEnable:    rowInt64Default(row, 0, "transfer_enable"),
		DeviceLimit:       rowInt64Ptr(row, "device_limit"),
		Banned:            banned,
		IsAdmin:           rowInt64Default(row, 0, "is_admin"),
		LastLoginAt:       positiveInt64Ptr(rowInt64Ptr(row, "last_login_at")),
		IsStaff:           rowInt64Default(row, 0, "is_staff"),
		LastLoginIP:       positiveInt64Ptr(rowInt64Ptr(row, "last_login_ip")),
		UUID:              strings.TrimSpace(rowString(row, "uuid", "v2ray_uuid")),
		GroupID:           groupID,
		PlanID:            planID,
		SpeedLimit:        rowInt64Ptr(row, "speed_limit"),
		AutoRenewal:       rowInt64Ptr(row, "auto_renewal"),
		RemindExpire:      rowInt64Ptr(row, "remind_expire"),
		RemindTraffic:     rowInt64Ptr(row, "remind_traffic"),
		Token:             strings.TrimSpace(rowString(row, "token")),
		ExpiredAt:         normalizeMergeExpiredAt(rowInt64Ptr(row, "expired_at")),
		Remarks:           trimmedStringPtr(rowStringPtr(row, "remarks")),
		CreatedAt:         createdAt,
		UpdatedAt:         updatedAt,
	}
	return user, nil
}

func buildMergeInsertUser(sourceUser mergeSourceUser, targetPlanByID map[int64]mergeTargetPlanInfo, planMap map[int64]int64, usedTokens, usedUUIDs map[string]struct{}) (mergeInsertUser, bool, error) {
	insertUser := mergeInsertUser{
		Email:             sourceUser.Email,
		Password:          sourceUser.Password,
		PasswordAlgo:      sourceUser.PasswordAlgo,
		PasswordSalt:      sourceUser.PasswordSalt,
		TelegramID:        sourceUser.TelegramID,
		Balance:           sourceUser.Balance,
		Discount:          sourceUser.Discount,
		CommissionType:    sourceUser.CommissionType,
		CommissionRate:    sourceUser.CommissionRate,
		CommissionBalance: sourceUser.CommissionBalance,
		T:                 sourceUser.T,
		U:                 sourceUser.U,
		D:                 sourceUser.D,
		TransferEnable:    sourceUser.TransferEnable,
		DeviceLimit:       sourceUser.DeviceLimit,
		Banned:            sourceUser.Banned,
		IsAdmin:           sourceUser.IsAdmin,
		LastLoginAt:       sourceUser.LastLoginAt,
		IsStaff:           sourceUser.IsStaff,
		LastLoginIP:       sourceUser.LastLoginIP,
		GroupID:           sourceUser.GroupID,
		PlanID:            nil,
		SpeedLimit:        sourceUser.SpeedLimit,
		AutoRenewal:       sourceUser.AutoRenewal,
		RemindExpire:      sourceUser.RemindExpire,
		RemindTraffic:     sourceUser.RemindTraffic,
		ExpiredAt:         normalizeMergeExpiredAt(sourceUser.ExpiredAt),
		Remarks:           sourceUser.Remarks,
		CreatedAt:         sourceUser.CreatedAt,
		UpdatedAt:         sourceUser.UpdatedAt,
	}

	if sourceUser.PlanID != nil && *sourceUser.PlanID > 0 {
		targetPlanID, ok := planMap[*sourceUser.PlanID]
		if !ok || targetPlanID <= 0 {
			return mergeInsertUser{}, false, nil
		}
		targetPlan, ok := targetPlanByID[targetPlanID]
		if !ok {
			return mergeInsertUser{}, false, nil
		}
		insertUser.PlanID = int64Ptr(targetPlanID)
		insertUser.GroupID = int64Ptr(targetPlan.GroupID)
		insertUser.TransferEnable = maxMergeTransferEnableBytes(
			sourceUser.TransferEnable,
			planTransferGBToUserBytes(targetPlan.TransferEnable),
		)
		insertUser.DeviceLimit = targetPlan.DeviceLimit
		insertUser.SpeedLimit = targetPlan.SpeedLimit
	}

	uuid := strings.TrimSpace(sourceUser.UUID)
	if uuid == "" {
		generatedUUID, err := randomUUID()
		if err != nil {
			return mergeInsertUser{}, false, err
		}
		uuid = generatedUUID
	}
	for {
		if _, exists := usedUUIDs[uuid]; !exists {
			break
		}
		generatedUUID, err := randomUUID()
		if err != nil {
			return mergeInsertUser{}, false, err
		}
		uuid = generatedUUID
	}
	insertUser.UUID = uuid

	token := strings.TrimSpace(sourceUser.Token)
	if token == "" {
		generatedToken, err := randomTokenHex(16)
		if err != nil {
			return mergeInsertUser{}, false, err
		}
		token = generatedToken
	}
	for {
		if _, exists := usedTokens[token]; !exists {
			break
		}
		generatedToken, err := randomTokenHex(16)
		if err != nil {
			return mergeInsertUser{}, false, err
		}
		token = generatedToken
	}
	insertUser.Token = token

	return insertUser, true, nil
}

func mergeTargetUserSubscriptionIfNeeded(ctx context.Context, tx *sql.Tx, existing mergeTargetUser, sourceUser mergeSourceUser, targetPlanByID map[int64]mergeTargetPlanInfo, planMap map[int64]int64) (mergeTargetUser, error) {
	sourcePlan, mapped := resolveMergeMappedTargetPlan(sourceUser, targetPlanByID, planMap)
	if !mapped {
		return existing, nil
	}
	desired := buildMergedTargetUserSubscription(existing, sourceUser, sourcePlan, targetPlanByID)
	if !mergeTargetUserSubscriptionChanged(existing, desired) {
		return existing, nil
	}
	if err := updateMergeTargetUserSubscription(ctx, tx, existing.ID, desired); err != nil {
		return existing, err
	}
	return desired, nil
}

func resolveMergeMappedTargetPlan(sourceUser mergeSourceUser, targetPlanByID map[int64]mergeTargetPlanInfo, planMap map[int64]int64) (mergeTargetPlanInfo, bool) {
	if sourceUser.PlanID == nil || *sourceUser.PlanID <= 0 {
		return mergeTargetPlanInfo{}, false
	}
	targetPlanID, ok := planMap[*sourceUser.PlanID]
	if !ok || targetPlanID <= 0 {
		return mergeTargetPlanInfo{}, false
	}
	targetPlan, ok := targetPlanByID[targetPlanID]
	if !ok {
		return mergeTargetPlanInfo{}, false
	}
	return targetPlan, true
}

func normalizeMergeTransferGB(value int64) int64 {
	if value <= 0 {
		return 0
	}
	const gib = int64(1024) * 1024 * 1024
	if value >= gib {
		return value / gib
	}
	return value
}

func planTransferGBToUserBytes(value int64) int64 {
	if value <= 0 {
		return 0
	}
	const gib = int64(1024) * 1024 * 1024
	return value * gib
}

func buildMergedTargetUserSubscription(existing mergeTargetUser, sourceUser mergeSourceUser, sourcePlan mergeTargetPlanInfo, targetPlanByID map[int64]mergeTargetPlanInfo) mergeTargetUser {
	desired := existing
	targetPlan := preferredMergeTargetPlan(existing, sourcePlan, targetPlanByID)
	desired.GroupID = int64Ptr(targetPlan.GroupID)
	desired.PlanID = int64Ptr(targetPlan.ID)
	desired.TransferEnable = maxMergeTransferEnableBytes(
		existing.TransferEnable,
		sourceUser.TransferEnable,
		planTransferGBToUserBytes(targetPlan.TransferEnable),
	)
	desired.DeviceLimit = cloneInt64Ptr(targetPlan.DeviceLimit)
	desired.SpeedLimit = cloneInt64Ptr(targetPlan.SpeedLimit)
	desired.ExpiredAt = mergePreferredExpiredAt(existing.ExpiredAt, sourceUser.ExpiredAt)
	return desired
}

func maxMergeTransferEnableBytes(values ...int64) int64 {
	var maxValue int64
	for _, value := range values {
		if value > maxValue {
			maxValue = value
		}
	}
	return maxValue
}

func preferredMergeTargetPlan(existing mergeTargetUser, sourcePlan mergeTargetPlanInfo, targetPlanByID map[int64]mergeTargetPlanInfo) mergeTargetPlanInfo {
	currentPlan, ok := currentMergeTargetPlan(existing, targetPlanByID)
	if !ok {
		return sourcePlan
	}
	if sourcePlan.TransferEnable > currentPlan.TransferEnable {
		return sourcePlan
	}
	return currentPlan
}

func currentMergeTargetPlan(existing mergeTargetUser, targetPlanByID map[int64]mergeTargetPlanInfo) (mergeTargetPlanInfo, bool) {
	if existing.PlanID != nil && *existing.PlanID > 0 {
		if currentPlan, ok := targetPlanByID[*existing.PlanID]; ok {
			return currentPlan, true
		}
		return mergeTargetPlanInfo{
			ID:             *existing.PlanID,
			GroupID:        derefInt64(existing.GroupID),
			TransferEnable: normalizeMergeTransferGB(existing.TransferEnable),
			DeviceLimit:    cloneInt64Ptr(existing.DeviceLimit),
			SpeedLimit:     cloneInt64Ptr(existing.SpeedLimit),
		}, true
	}
	return mergeTargetPlanInfo{}, false
}

func mergePreferredExpiredAt(currentRaw, sourceRaw *int64) *int64 {
	current := normalizeMergeExpiredAt(currentRaw)
	source := normalizeMergeExpiredAt(sourceRaw)
	if source == nil {
		return nil
	}
	if current == nil {
		return nil
	}
	if *source > *current {
		return cloneInt64Ptr(source)
	}
	return cloneInt64Ptr(current)
}

func normalizeMergeExpiredAt(value *int64) *int64 {
	if value == nil || *value <= 0 {
		return nil
	}
	return cloneInt64Ptr(value)
}

func mergeTargetUserSubscriptionChanged(current, desired mergeTargetUser) bool {
	if !equalInt64Ptr(current.GroupID, desired.GroupID) {
		return true
	}
	if !equalInt64Ptr(current.PlanID, desired.PlanID) {
		return true
	}
	if current.TransferEnable != desired.TransferEnable {
		return true
	}
	if !equalInt64Ptr(current.DeviceLimit, desired.DeviceLimit) {
		return true
	}
	if !equalInt64Ptr(current.SpeedLimit, desired.SpeedLimit) {
		return true
	}
	if !equalExpiredAtStorage(current.ExpiredAt, desired.ExpiredAt) {
		return true
	}
	return false
}

func equalExpiredAtStorage(currentRaw, desired *int64) bool {
	if desired == nil {
		return currentRaw == nil
	}
	if currentRaw == nil {
		return false
	}
	return *currentRaw == *desired
}

func equalInt64Ptr(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func updateMergeTargetUserSubscription(ctx context.Context, tx *sql.Tx, userID int64, targetUser mergeTargetUser) error {
	if _, err := tx.ExecContext(ctx, `UPDATE v2_user SET group_id = $1, plan_id = $2, transfer_enable = $3, device_limit = $4, speed_limit = $5, expired_at = $6, updated_at = $7 WHERE id = $8`,
		nullableInt64Value(targetUser.GroupID),
		nullableInt64Value(targetUser.PlanID),
		targetUser.TransferEnable,
		nullableInt64Value(targetUser.DeviceLimit),
		nullableInt64Value(targetUser.SpeedLimit),
		nullableInt64Value(targetUser.ExpiredAt),
		time.Now().Unix(),
		userID,
	); err != nil {
		return fmt.Errorf("更新目标站已存在用户订阅失败（用户 %d）: %w", userID, err)
	}
	return nil
}

func insertMergeUser(ctx context.Context, tx *sql.Tx, user mergeInsertUser) (int64, error) {
	const query = `INSERT INTO v2_user (
invite_user_id, telegram_id, email, password, password_algo, password_salt,
balance, discount, commission_type, commission_rate, commission_balance,
t, u, d, transfer_enable, device_limit, banned, is_admin, last_login_at,
is_staff, last_login_ip, uuid, group_id, plan_id, speed_limit, auto_renewal,
remind_expire, remind_traffic, token, expired_at, remarks, created_at, updated_at
) VALUES (
NULL, $1, $2, $3, $4, $5,
$6, $7, $8, $9, $10,
$11, $12, $13, $14, $15, $16, $17, $18,
$19, $20, $21, $22, $23, $24, $25,
$26, $27, $28, $29, $30, $31, $32
) RETURNING id`
	var id int64
	if err := tx.QueryRowContext(ctx, query,
		user.TelegramID,
		user.Email,
		user.Password,
		user.PasswordAlgo,
		user.PasswordSalt,
		user.Balance,
		user.Discount,
		user.CommissionType,
		user.CommissionRate,
		user.CommissionBalance,
		user.T,
		user.U,
		user.D,
		user.TransferEnable,
		user.DeviceLimit,
		user.Banned,
		user.IsAdmin,
		user.LastLoginAt,
		user.IsStaff,
		user.LastLoginIP,
		user.UUID,
		user.GroupID,
		user.PlanID,
		user.SpeedLimit,
		user.AutoRenewal,
		defaultInt64Ptr(user.RemindExpire, 1),
		defaultInt64Ptr(user.RemindTraffic, 1),
		user.Token,
		nullableInt64Value(user.ExpiredAt),
		user.Remarks,
		user.CreatedAt,
		user.UpdatedAt,
	).Scan(&id); err != nil {
		return 0, fmt.Errorf("写入目标站用户失败（%s）: %w", user.Email, err)
	}
	return id, nil
}

func updateMergeInviteRelation(ctx context.Context, tx *sql.Tx, targetUsersByEmail map[string]mergeTargetUser, emailKey string, targetUserID, inviteUserID int64) (bool, error) {
	targetUser := targetUsersByEmail[emailKey]
	if targetUser.InviteUserID != nil && *targetUser.InviteUserID > 0 {
		return false, nil
	}
	result, err := tx.ExecContext(ctx, `UPDATE v2_user SET invite_user_id = $1, updated_at = $3 WHERE id = $2 AND (invite_user_id IS NULL OR invite_user_id = 0)`, inviteUserID, targetUserID, time.Now().Unix())
	if err != nil {
		return false, fmt.Errorf("更新邀请关系失败（用户 %d）: %w", targetUserID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("读取邀请关系更新结果失败（用户 %d）: %w", targetUserID, err)
	}
	if affected > 0 {
		targetUser.InviteUserID = int64Ptr(inviteUserID)
		targetUsersByEmail[emailKey] = targetUser
		return true, nil
	}
	return false, nil
}

func loadMergeTargetUsers(ctx context.Context, db queryRower) (map[string]mergeTargetUser, map[string]struct{}, map[string]struct{}, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, email, invite_user_id, token, uuid, group_id, plan_id, transfer_enable, device_limit, speed_limit, expired_at FROM v2_user ORDER BY id`)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("读取目标站用户失败: %w", err)
	}
	defer rows.Close()

	usersByEmail := make(map[string]mergeTargetUser)
	usedTokens := make(map[string]struct{})
	usedUUIDs := make(map[string]struct{})
	for rows.Next() {
		var item mergeTargetUser
		var inviteUserID sql.NullInt64
		var groupID sql.NullInt64
		var planID sql.NullInt64
		var transferEnable sql.NullInt64
		var deviceLimit sql.NullInt64
		var speedLimit sql.NullInt64
		var expiredAt sql.NullInt64
		if err := rows.Scan(&item.ID, &item.Email, &inviteUserID, &item.Token, &item.UUID, &groupID, &planID, &transferEnable, &deviceLimit, &speedLimit, &expiredAt); err != nil {
			return nil, nil, nil, fmt.Errorf("扫描目标站用户失败: %w", err)
		}
		item.Email = strings.TrimSpace(item.Email)
		item.EmailKey = normalizeMergeEmailKey(item.Email)
		item.InviteUserID = nullInt64Ptr(inviteUserID)
		item.GroupID = nullInt64Ptr(groupID)
		item.PlanID = nullInt64Ptr(planID)
		item.TransferEnable = transferEnable.Int64
		item.DeviceLimit = nullInt64Ptr(deviceLimit)
		item.SpeedLimit = nullInt64Ptr(speedLimit)
		item.ExpiredAt = nullInt64Ptr(expiredAt)
		if item.EmailKey != "" {
			if _, ok := usersByEmail[item.EmailKey]; !ok {
				usersByEmail[item.EmailKey] = item
			}
		}
		if strings.TrimSpace(item.Token) != "" {
			usedTokens[strings.TrimSpace(item.Token)] = struct{}{}
		}
		if strings.TrimSpace(item.UUID) != "" {
			usedUUIDs[strings.TrimSpace(item.UUID)] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, nil, fmt.Errorf("读取目标站用户失败: %w", err)
	}
	return usersByEmail, usedTokens, usedUUIDs, nil
}

func cloneInt64Ptr(value *int64) *int64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func derefInt64(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func nullableInt64Value(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func loadMergeTargetInviteCodes(ctx context.Context, db queryRower) (map[int64]map[string]int64, map[string]int64, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, user_id, code FROM v2_invite_code ORDER BY id`)
	if err != nil {
		return nil, nil, fmt.Errorf("读取目标站邀请码失败: %w", err)
	}
	defer rows.Close()

	byUserCode := make(map[int64]map[string]int64)
	ownerByCode := make(map[string]int64)
	for rows.Next() {
		var id int64
		var userID int64
		var code string
		if err := rows.Scan(&id, &userID, &code); err != nil {
			return nil, nil, fmt.Errorf("扫描目标站邀请码失败: %w", err)
		}
		code = strings.TrimSpace(code)
		if code == "" {
			continue
		}
		if _, ok := byUserCode[userID]; !ok {
			byUserCode[userID] = make(map[string]int64)
		}
		if _, ok := byUserCode[userID][code]; !ok {
			byUserCode[userID][code] = id
		}
		if _, ok := ownerByCode[code]; !ok {
			ownerByCode[code] = userID
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("读取目标站邀请码失败: %w", err)
	}
	return byUserCode, ownerByCode, nil
}

func resolveMergeInviteCodeForUser(targetUserID int64, sourceCode, email string, byUserCode map[int64]map[string]int64, ownerByCode map[string]int64) (string, int64, bool) {
	for _, candidate := range mergeInviteCodeCandidates(sourceCode, email) {
		if existingID := lookupMergeInviteCodeForUser(byUserCode, targetUserID, candidate); existingID > 0 {
			return candidate, existingID, candidate != sanitizeMergeInviteCode(sourceCode)
		}
		owner, occupied := ownerByCode[candidate]
		if !occupied || owner == targetUserID {
			return candidate, 0, candidate != sanitizeMergeInviteCode(sourceCode)
		}
	}
	used := make(map[string]struct{}, len(ownerByCode))
	for code := range ownerByCode {
		used[code] = struct{}{}
	}
	resolved := ensureUniqueMergeInviteCode(sourceCode, email, used)
	return resolved, 0, resolved != sanitizeMergeInviteCode(sourceCode)
}

func lookupMergeInviteCodeForUser(byUserCode map[int64]map[string]int64, userID int64, code string) int64 {
	codes, ok := byUserCode[userID]
	if !ok {
		return 0
	}
	return codes[code]
}

func lookupMergeInviteCodeByID(codeID int64, byUserCode map[int64]map[string]int64) string {
	for _, codes := range byUserCode {
		for code, id := range codes {
			if id == codeID {
				return code
			}
		}
	}
	return ""
}

func loadMergeSourceInviteCodes(ctx context.Context, sourceDB *sql.DB, sourceSet map[string]struct{}) ([]mergeSourceInviteCode, error) {
	if _, ok := sourceSet["v2_invite_code"]; !ok {
		return nil, nil
	}
	rows, err := queryRowsAsMaps(ctx, sourceDB, `SELECT * FROM v2_invite_code ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("读取源库邀请码失败: %w", err)
	}
	codes := make([]mergeSourceInviteCode, 0, len(rows))
	for _, row := range rows {
		id, ok := rowInt64(row, "id")
		if !ok || id <= 0 {
			return nil, fmt.Errorf("源库邀请码存在无效的 id")
		}
		userID, ok := rowInt64(row, "user_id")
		if !ok || userID <= 0 {
			continue
		}
		codes = append(codes, mergeSourceInviteCode{
			ID:               id,
			UserID:           userID,
			Code:             strings.TrimSpace(rowString(row, "code")),
			Status:           rowInt64Default(row, 0, "status"),
			InviteCampaignID: positiveInt64Ptr(rowInt64Ptr(row, "invite_campaign_id")),
			PV:               rowInt64Default(row, 0, "pv"),
			CreatedAt:        rowInt64Default(row, time.Now().Unix(), "created_at"),
			UpdatedAt:        rowInt64Default(row, time.Now().Unix(), "updated_at"),
		})
	}
	return codes, nil
}

func insertMergeInviteCode(ctx context.Context, tx *sql.Tx, userID int64, code string, status, pv, createdAt, updatedAt int64) (int64, error) {
	var id int64
	if err := tx.QueryRowContext(ctx, `INSERT INTO v2_invite_code (user_id, code, status, invite_campaign_id, pv, created_at, updated_at)
VALUES ($1, $2, $3, NULL, $4, $5, $6) RETURNING id`, userID, code, status, pv, createdAt, updatedAt).Scan(&id); err != nil {
		return 0, fmt.Errorf("写入邀请码失败（用户 %d, code %s）: %w", userID, code, err)
	}
	return id, nil
}

func loadMergeSourceInviteCampaigns(ctx context.Context, sourceDB *sql.DB, sourceSet map[string]struct{}) ([]mergeSourceInviteCampaign, error) {
	if _, ok := sourceSet["v2_invite_campaign"]; !ok {
		return nil, nil
	}
	rows, err := queryRowsAsMaps(ctx, sourceDB, `SELECT * FROM v2_invite_campaign ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("读取源库活动任务失败: %w", err)
	}
	campaigns := make([]mergeSourceInviteCampaign, 0, len(rows))
	for _, row := range rows {
		id, ok := rowInt64(row, "id")
		if !ok || id <= 0 {
			return nil, fmt.Errorf("源库活动任务存在无效的 id")
		}
		userID, ok := rowInt64(row, "user_id")
		if !ok || userID <= 0 {
			continue
		}
		planID, ok := rowInt64(row, "plan_id")
		if !ok || planID <= 0 {
			continue
		}
		campaigns = append(campaigns, mergeSourceInviteCampaign{
			ID:            id,
			UserID:        userID,
			PlanID:        planID,
			Period:        strings.TrimSpace(rowString(row, "period")),
			InviteCodeID:  positiveInt64Ptr(rowInt64Ptr(row, "invite_code_id")),
			InviteCode:    trimmedStringPtr(rowStringPtr(row, "invite_code")),
			RewardAmount:  rowInt64Default(row, 0, "reward_amount"),
			TargetAmount:  rowInt64Default(row, 0, "target_amount"),
			CurrentAmount: rowInt64Default(row, 0, "current_amount"),
			InviteCount:   rowInt64Default(row, 0, "invite_count"),
			Status:        rowInt64Default(row, 0, "status"),
			StartedAt:     rowInt64Default(row, 0, "started_at"),
			ExpiredAt:     rowInt64Default(row, 0, "expired_at"),
			CompletedAt:   rowInt64Ptr(row, "completed_at"),
			AbandonedAt:   rowInt64Ptr(row, "abandoned_at"),
			UsedAt:        rowInt64Ptr(row, "used_at"),
			CreatedAt:     rowInt64Default(row, time.Now().Unix(), "created_at"),
			UpdatedAt:     rowInt64Default(row, time.Now().Unix(), "updated_at"),
		})
	}
	return campaigns, nil
}

func loadMergeTargetInviteCampaigns(ctx context.Context, db queryRower) (map[string]int64, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, user_id, plan_id, period, invite_code, started_at, expired_at, created_at FROM v2_invite_campaign ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("读取目标站活动任务失败: %w", err)
	}
	defer rows.Close()

	campaigns := make(map[string]int64)
	for rows.Next() {
		var id int64
		var userID int64
		var planID int64
		var period string
		var inviteCode sql.NullString
		var startedAt int64
		var expiredAt int64
		var createdAt int64
		if err := rows.Scan(&id, &userID, &planID, &period, &inviteCode, &startedAt, &expiredAt, &createdAt); err != nil {
			return nil, fmt.Errorf("扫描目标站活动任务失败: %w", err)
		}
		signature := mergeInviteCampaignSignature(userID, planID, strings.TrimSpace(period), strings.TrimSpace(inviteCode.String), startedAt, expiredAt, createdAt)
		if _, ok := campaigns[signature]; !ok {
			campaigns[signature] = id
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取目标站活动任务失败: %w", err)
	}
	return campaigns, nil
}

func mergeInviteCampaignSignature(userID, planID int64, period, inviteCode string, startedAt, expiredAt, createdAt int64) string {
	return fmt.Sprintf("%d|%d|%s|%s|%d|%d|%d", userID, planID, strings.TrimSpace(period), strings.TrimSpace(inviteCode), startedAt, expiredAt, createdAt)
}

func insertMergeInviteCampaign(ctx context.Context, tx *sql.Tx, targetUserID, targetPlanID int64, targetInviteCodeID *int64, targetInviteCode *string, sourceCampaign mergeSourceInviteCampaign) (int64, error) {
	var id int64
	if err := tx.QueryRowContext(ctx, `INSERT INTO v2_invite_campaign (
user_id, plan_id, period, invite_code_id, invite_code, reward_amount, target_amount,
current_amount, invite_count, status, started_at, expired_at, completed_at,
abandoned_at, used_at, created_at, updated_at
) VALUES (
$1, $2, $3, $4, $5, $6, $7,
$8, $9, $10, $11, $12, $13,
$14, $15, $16, $17
) RETURNING id`,
		targetUserID,
		targetPlanID,
		sourceCampaign.Period,
		targetInviteCodeID,
		targetInviteCode,
		sourceCampaign.RewardAmount,
		sourceCampaign.TargetAmount,
		sourceCampaign.CurrentAmount,
		sourceCampaign.InviteCount,
		sourceCampaign.Status,
		sourceCampaign.StartedAt,
		sourceCampaign.ExpiredAt,
		sourceCampaign.CompletedAt,
		sourceCampaign.AbandonedAt,
		sourceCampaign.UsedAt,
		sourceCampaign.CreatedAt,
		sourceCampaign.UpdatedAt,
	).Scan(&id); err != nil {
		return 0, fmt.Errorf("写入活动任务失败（用户 %d, 套餐 %d）: %w", targetUserID, targetPlanID, err)
	}
	return id, nil
}

func attachMergeInviteCodeCampaign(ctx context.Context, tx *sql.Tx, targetCodeID, targetCampaignID int64) error {
	if _, err := tx.ExecContext(ctx, `UPDATE v2_invite_code SET invite_campaign_id = $2, updated_at = $3 WHERE id = $1 AND (invite_campaign_id IS NULL OR invite_campaign_id = 0)`, targetCodeID, targetCampaignID, time.Now().Unix()); err != nil {
		return fmt.Errorf("回填邀请码活动任务关联失败（邀请码 %d）: %w", targetCodeID, err)
	}
	return nil
}

func loadMergeSourceInviteCampaignRecords(ctx context.Context, sourceDB *sql.DB, sourceSet map[string]struct{}) ([]mergeSourceInviteCampaignRecord, error) {
	if _, ok := sourceSet["v2_invite_campaign_record"]; !ok {
		return nil, nil
	}
	rows, err := queryRowsAsMaps(ctx, sourceDB, `SELECT * FROM v2_invite_campaign_record ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("读取源库活动任务记录失败: %w", err)
	}
	records := make([]mergeSourceInviteCampaignRecord, 0, len(rows))
	for _, row := range rows {
		id, ok := rowInt64(row, "id")
		if !ok || id <= 0 {
			return nil, fmt.Errorf("源库活动任务记录存在无效的 id")
		}
		campaignID, ok := rowInt64(row, "campaign_id")
		if !ok || campaignID <= 0 {
			continue
		}
		inviteeUserID, ok := rowInt64(row, "invitee_user_id")
		if !ok || inviteeUserID <= 0 {
			continue
		}
		records = append(records, mergeSourceInviteCampaignRecord{
			ID:            id,
			CampaignID:    campaignID,
			InviteeUserID: inviteeUserID,
			InviteCode:    strings.TrimSpace(rowString(row, "invite_code")),
			RewardAmount:  rowInt64Default(row, 0, "reward_amount"),
			CreatedAt:     rowInt64Default(row, time.Now().Unix(), "created_at"),
			UpdatedAt:     rowInt64Default(row, time.Now().Unix(), "updated_at"),
		})
	}
	return records, nil
}

func loadMergeTargetInviteCampaignRecords(ctx context.Context, db queryRower) (map[string]struct{}, error) {
	rows, err := db.QueryContext(ctx, `SELECT campaign_id, invitee_user_id FROM v2_invite_campaign_record`)
	if err != nil {
		return nil, fmt.Errorf("读取目标站活动任务记录失败: %w", err)
	}
	defer rows.Close()

	records := make(map[string]struct{})
	for rows.Next() {
		var campaignID int64
		var inviteeUserID int64
		if err := rows.Scan(&campaignID, &inviteeUserID); err != nil {
			return nil, fmt.Errorf("扫描目标站活动任务记录失败: %w", err)
		}
		records[mergeInviteCampaignRecordSignature(campaignID, inviteeUserID)] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取目标站活动任务记录失败: %w", err)
	}
	return records, nil
}

func mergeInviteCampaignRecordSignature(campaignID, inviteeUserID int64) string {
	return fmt.Sprintf("%d|%d", campaignID, inviteeUserID)
}

func insertMergeInviteCampaignRecord(ctx context.Context, tx *sql.Tx, targetCampaignID, targetInviteeUserID int64, inviteCode string, sourceRecord mergeSourceInviteCampaignRecord) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO v2_invite_campaign_record (
campaign_id, invitee_user_id, invite_code, reward_amount, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6)`, targetCampaignID, targetInviteeUserID, inviteCode, sourceRecord.RewardAmount, sourceRecord.CreatedAt, sourceRecord.UpdatedAt); err != nil {
		return fmt.Errorf("写入活动任务记录失败（campaign %d, invitee %d）: %w", targetCampaignID, targetInviteeUserID, err)
	}
	return nil
}

func queryRowsAsMaps(ctx context.Context, db queryRower, query string, args ...any) ([]map[string]any, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	result := make([]map[string]any, 0)
	for rows.Next() {
		values := make([]any, len(columns))
		scanArgs := make([]any, len(columns))
		for idx := range values {
			scanArgs[idx] = &values[idx]
		}
		if err := rows.Scan(scanArgs...); err != nil {
			return nil, err
		}
		row := make(map[string]any, len(columns))
		for idx, name := range columns {
			row[strings.ToLower(name)] = normalizeMergeSQLValue(values[idx])
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func normalizeMergeSQLValue(value any) any {
	switch item := value.(type) {
	case []byte:
		return string(item)
	default:
		return item
	}
}

func rowLookup(row map[string]any, keys ...string) (any, bool) {
	for _, key := range keys {
		if value, ok := row[strings.ToLower(key)]; ok {
			return value, true
		}
	}
	return nil, false
}

func rowString(row map[string]any, keys ...string) string {
	value, ok := rowLookup(row, keys...)
	if !ok || value == nil {
		return ""
	}
	switch item := value.(type) {
	case string:
		return item
	case []byte:
		return string(item)
	default:
		return fmt.Sprint(item)
	}
}

func rowStringPtr(row map[string]any, keys ...string) *string {
	value, ok := rowLookup(row, keys...)
	if !ok || value == nil {
		return nil
	}
	result := rowString(row, keys...)
	return &result
}

func rowInt64(row map[string]any, keys ...string) (int64, bool) {
	value, ok := rowLookup(row, keys...)
	if !ok || value == nil {
		return 0, false
	}
	switch item := value.(type) {
	case int64:
		return item, true
	case int32:
		return int64(item), true
	case int16:
		return int64(item), true
	case int8:
		return int64(item), true
	case int:
		return int64(item), true
	case uint64:
		return int64(item), true
	case uint32:
		return int64(item), true
	case uint16:
		return int64(item), true
	case uint8:
		return int64(item), true
	case float64:
		return int64(item), true
	case float32:
		return int64(item), true
	case bool:
		if item {
			return 1, true
		}
		return 0, true
	case string:
		trimmed := strings.TrimSpace(item)
		if trimmed == "" {
			return 0, false
		}
		if value, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
			return value, true
		}
		if value, err := strconv.ParseFloat(trimmed, 64); err == nil {
			return int64(value), true
		}
	case time.Time:
		return item.Unix(), true
	}
	return 0, false
}

func rowInt64Ptr(row map[string]any, keys ...string) *int64 {
	value, ok := rowInt64(row, keys...)
	if !ok {
		return nil
	}
	return &value
}

func rowInt64Default(row map[string]any, fallback int64, keys ...string) int64 {
	value, ok := rowInt64(row, keys...)
	if !ok {
		return fallback
	}
	return value
}

func tableNameSet(names []string) map[string]struct{} {
	set := make(map[string]struct{}, len(names))
	for _, name := range names {
		set[name] = struct{}{}
	}
	return set
}

func normalizeMergeEmailKey(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func trimmedStringPtr(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func positiveInt64Ptr(value *int64) *int64 {
	if value == nil {
		return nil
	}
	if *value <= 0 {
		return nil
	}
	return value
}

func defaultInt64Ptr(value *int64, fallback int64) int64 {
	if value == nil {
		return fallback
	}
	return *value
}

func nullInt64Ptr(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	result := value.Int64
	return &result
}

func int64Ptr(value int64) *int64 {
	return &value
}

func stringPtr(value string) *string {
	return &value
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func escapeInspectField(value string) string {
	value = strings.ReplaceAll(value, "\t", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	return strings.TrimSpace(value)
}
