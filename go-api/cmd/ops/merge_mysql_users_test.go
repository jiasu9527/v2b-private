package main

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestParseMergeMySQLPlanMap(t *testing.T) {
	planMap, err := parseMergeMySQLPlanMap("1:10, 2:20\n3:30")
	if err != nil {
		t.Fatalf("parseMergeMySQLPlanMap: %v", err)
	}

	if len(planMap) != 3 {
		t.Fatalf("expected 3 mappings, got %#v", planMap)
	}
	if planMap[1] != 10 || planMap[2] != 20 || planMap[3] != 30 {
		t.Fatalf("unexpected mappings: %#v", planMap)
	}
}

func TestParseMergeMySQLPlanMapRejectsInvalidToken(t *testing.T) {
	if _, err := parseMergeMySQLPlanMap("1:10,broken"); err == nil {
		t.Fatal("expected invalid mapping to fail")
	}
}

func TestEnsureUniqueMergeInviteCodeKeepsAvailableCode(t *testing.T) {
	used := map[string]struct{}{
		"OTHER999": {},
	}

	got := ensureUniqueMergeInviteCode("ABC888", "user@example.com", used)
	if got != "ABC888" {
		t.Fatalf("expected original code kept, got %q", got)
	}
}

func TestEnsureUniqueMergeInviteCodeRenamesConflictingCode(t *testing.T) {
	used := map[string]struct{}{
		"ABC888":       {},
		"ABC8889A4B2C": {},
	}

	got := ensureUniqueMergeInviteCode("ABC888", "user@example.com", used)
	if got == "ABC888" {
		t.Fatalf("expected conflicting code to be renamed, got %q", got)
	}
	if len(got) > 32 {
		t.Fatalf("expected invite code length <= 32, got %d (%q)", len(got), got)
	}
	if !regexp.MustCompile(`^[A-Za-z0-9]+$`).MatchString(got) {
		t.Fatalf("expected invite code to stay alphanumeric, got %q", got)
	}
}

func TestBuildMergeInsertUserUsesTargetPlanTrafficBytes(t *testing.T) {
	sourcePlanID := int64(23)
	sourceUser := mergeSourceUser{
		Email:          "merge@example.com",
		Password:       "hashed-password",
		PlanID:         &sourcePlanID,
		TransferEnable: 123,
	}

	insertUser, mapped, err := buildMergeInsertUser(
		sourceUser,
		map[int64]mergeTargetPlanInfo{
			3: {
				ID:             3,
				GroupID:        1,
				TransferEnable: 2000,
			},
		},
		map[int64]int64{23: 3},
		map[string]struct{}{},
		map[string]struct{}{},
	)
	if err != nil {
		t.Fatalf("buildMergeInsertUser: %v", err)
	}
	if !mapped {
		t.Fatal("expected source user plan to map to target plan")
	}

	const expectedBytes = int64(2000) * 1024 * 1024 * 1024
	if insertUser.TransferEnable != expectedBytes {
		t.Fatalf("expected transfer_enable=%d bytes, got %d", expectedBytes, insertUser.TransferEnable)
	}
}

func TestBuildMergeInsertUserKeepsHigherSourceTrafficBytes(t *testing.T) {
	sourcePlanID := int64(23)
	const sourceTrafficBytes = int64(3000) * 1024 * 1024 * 1024
	sourceUser := mergeSourceUser{
		Email:          "merge@example.com",
		Password:       "hashed-password",
		PlanID:         &sourcePlanID,
		TransferEnable: sourceTrafficBytes,
	}

	insertUser, mapped, err := buildMergeInsertUser(
		sourceUser,
		map[int64]mergeTargetPlanInfo{
			3: {
				ID:             3,
				GroupID:        1,
				TransferEnable: 2000,
			},
		},
		map[int64]int64{23: 3},
		map[string]struct{}{},
		map[string]struct{}{},
	)
	if err != nil {
		t.Fatalf("buildMergeInsertUser: %v", err)
	}
	if !mapped {
		t.Fatal("expected source user plan to map to target plan")
	}
	if insertUser.TransferEnable != sourceTrafficBytes {
		t.Fatalf("expected higher source transfer_enable=%d bytes to be preserved, got %d", sourceTrafficBytes, insertUser.TransferEnable)
	}
}

func TestBuildMergeInsertUserNormalizesUnlimitedExpiredAt(t *testing.T) {
	sourcePlanID := int64(23)
	zeroExpiredAt := int64(0)
	sourceUser := mergeSourceUser{
		Email:     "merge@example.com",
		Password:  "hashed-password",
		PlanID:    &sourcePlanID,
		ExpiredAt: &zeroExpiredAt,
	}

	insertUser, mapped, err := buildMergeInsertUser(
		sourceUser,
		map[int64]mergeTargetPlanInfo{
			3: {
				ID:             3,
				GroupID:        1,
				TransferEnable: 2000,
			},
		},
		map[int64]int64{23: 3},
		map[string]struct{}{},
		map[string]struct{}{},
	)
	if err != nil {
		t.Fatalf("buildMergeInsertUser: %v", err)
	}
	if !mapped {
		t.Fatal("expected source user plan to map to target plan")
	}
	if insertUser.ExpiredAt != nil {
		t.Fatalf("expected unlimited expired_at to become nil, got %#v", insertUser.ExpiredAt)
	}
}

func TestBuildMergeInsertUserDefaultsAutoRenewalToZero(t *testing.T) {
	sourcePlanID := int64(23)
	sourceUser := mergeSourceUser{
		Email:    "merge@example.com",
		Password: "hashed-password",
		PlanID:   &sourcePlanID,
	}

	insertUser, mapped, err := buildMergeInsertUser(
		sourceUser,
		map[int64]mergeTargetPlanInfo{
			3: {
				ID:             3,
				GroupID:        1,
				TransferEnable: 2000,
			},
		},
		map[int64]int64{23: 3},
		map[string]struct{}{},
		map[string]struct{}{},
	)
	if err != nil {
		t.Fatalf("buildMergeInsertUser: %v", err)
	}
	if !mapped {
		t.Fatal("expected source user plan to map to target plan")
	}
	if insertUser.AutoRenewal == nil || *insertUser.AutoRenewal != 0 {
		t.Fatalf("expected auto_renewal default 0, got %#v", insertUser.AutoRenewal)
	}
}

func TestBuildMergedTargetUserSubscriptionKeepsHigherSourceTrafficBytes(t *testing.T) {
	existingPlanID := int64(1)
	existingGroupID := int64(1)
	existingTrafficBytes := int64(200) * 1024 * 1024 * 1024
	sourcePlanID := int64(23)
	sourceGroupID := int64(1)
	sourceTrafficBytes := int64(1000) * 1024 * 1024 * 1024

	desired := buildMergedTargetUserSubscription(
		mergeTargetUser{
			ID:             9,
			Email:          "merge@example.com",
			GroupID:        &existingGroupID,
			PlanID:         &existingPlanID,
			TransferEnable: existingTrafficBytes,
		},
		mergeSourceUser{
			Email:          "merge@example.com",
			GroupID:        &sourceGroupID,
			PlanID:         &sourcePlanID,
			TransferEnable: sourceTrafficBytes,
		},
		mergeTargetPlanInfo{
			ID:             5,
			GroupID:        1,
			TransferEnable: 400,
		},
		map[int64]mergeTargetPlanInfo{
			1: {
				ID:             1,
				GroupID:        1,
				TransferEnable: 200,
			},
			5: {
				ID:             5,
				GroupID:        1,
				TransferEnable: 400,
			},
		},
	)

	if desired.TransferEnable != sourceTrafficBytes {
		t.Fatalf("expected higher source transfer_enable=%d bytes to be preserved, got %d", sourceTrafficBytes, desired.TransferEnable)
	}
	if desired.PlanID == nil || *desired.PlanID != 5 {
		t.Fatalf("expected mapped target plan to remain 5, got %#v", desired.PlanID)
	}
}

func TestMergeMySQLUsersIntoPostgresUpgradesExistingMatchedUserPlan(t *testing.T) {
	ctx := context.Background()

	sourceDB, sourceMock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("new source mock: %v", err)
	}
	defer sourceDB.Close()

	targetDB, targetMock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("new target mock: %v", err)
	}
	defer targetDB.Close()

	sourceMock.ExpectQuery(regexp.QuoteMeta(`SELECT table_name FROM information_schema.tables WHERE table_schema = DATABASE() AND table_type = 'BASE TABLE' ORDER BY table_name`)).
		WillReturnRows(sqlmock.NewRows([]string{"table_name"}).AddRow("v2_user"))
	sourceMock.ExpectQuery(regexp.QuoteMeta(`SELECT * FROM v2_user ORDER BY id`)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "email", "password", "plan_id", "expired_at"}).
			AddRow(int64(101), "dup@example.com", "hashed-password", int64(23), nil))

	targetMock.ExpectQuery(regexp.QuoteMeta(`SELECT p.id, p.name, p.group_id, p.transfer_enable, p.device_limit, p.speed_limit, COALESCE(COUNT(u.id), 0)
FROM v2_plan p
LEFT JOIN v2_user u ON u.plan_id = p.id
GROUP BY p.id, p.name, p.group_id, p.transfer_enable, p.device_limit, p.speed_limit
ORDER BY p.id`)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "group_id", "transfer_enable", "device_limit", "speed_limit", "users"}).
			AddRow(int64(2), "高级", int64(1), int64(500), int64(2), int64(20), int64(10)).
			AddRow(int64(3), "旗舰", int64(1), int64(2000), int64(5), int64(50), int64(20)))

	targetMock.ExpectBegin()
	targetMock.ExpectQuery(regexp.QuoteMeta(`SELECT id, email, invite_user_id, token, uuid, group_id, plan_id, transfer_enable, device_limit, speed_limit, expired_at FROM v2_user ORDER BY id`)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "email", "invite_user_id", "token", "uuid", "group_id", "plan_id", "transfer_enable", "device_limit", "speed_limit", "expired_at"}).
			AddRow(int64(7), "dup@example.com", nil, "existing-token", "existing-uuid", int64(1), int64(2), int64(500)*1024*1024*1024, int64(2), int64(20), int64(1893456000)))
	targetMock.ExpectExec(regexp.QuoteMeta(`UPDATE v2_user SET group_id = $1, plan_id = $2, transfer_enable = $3, device_limit = $4, speed_limit = $5, expired_at = $6, updated_at = $7 WHERE id = $8`)).
		WithArgs(int64(1), int64(3), int64(2000)*1024*1024*1024, int64(5), int64(50), nil, sqlmock.AnyArg(), int64(7)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	targetMock.ExpectQuery(regexp.QuoteMeta(`SELECT id, user_id, code FROM v2_invite_code ORDER BY id`)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "user_id", "code"}))
	targetMock.ExpectQuery(regexp.QuoteMeta(`SELECT id, user_id, plan_id, period, invite_code, started_at, expired_at, created_at FROM v2_invite_campaign ORDER BY id`)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "user_id", "plan_id", "period", "invite_code", "started_at", "expired_at", "created_at"}))
	targetMock.ExpectQuery(regexp.QuoteMeta(`SELECT campaign_id, invitee_user_id FROM v2_invite_campaign_record`)).
		WillReturnRows(sqlmock.NewRows([]string{"campaign_id", "invitee_user_id"}))
	targetMock.ExpectCommit()

	result, err := mergeMySQLUsersIntoPostgres(ctx, sourceDB, targetDB, map[int64]int64{23: 3})
	if err != nil {
		t.Fatalf("mergeMySQLUsersIntoPostgres: %v", err)
	}
	if result.UsersMatchedExisting != 1 {
		t.Fatalf("expected matched existing users=1, got %d", result.UsersMatchedExisting)
	}

	if err := sourceMock.ExpectationsWereMet(); err != nil {
		t.Fatalf("source expectations: %v", err)
	}
	if err := targetMock.ExpectationsWereMet(); err != nil {
		t.Fatalf("target expectations: %v", err)
	}
}

func TestMergeMySQLUsersIntoPostgresUpdatesUnlimitedExpiryWithoutPlanUpgrade(t *testing.T) {
	ctx := context.Background()

	sourceDB, sourceMock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("new source mock: %v", err)
	}
	defer sourceDB.Close()

	targetDB, targetMock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("new target mock: %v", err)
	}
	defer targetDB.Close()

	sourceMock.ExpectQuery(regexp.QuoteMeta(`SELECT table_name FROM information_schema.tables WHERE table_schema = DATABASE() AND table_type = 'BASE TABLE' ORDER BY table_name`)).
		WillReturnRows(sqlmock.NewRows([]string{"table_name"}).AddRow("v2_user"))
	sourceMock.ExpectQuery(regexp.QuoteMeta(`SELECT * FROM v2_user ORDER BY id`)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "email", "password", "plan_id", "expired_at"}).
			AddRow(int64(101), "sameplan@example.com", "hashed-password", int64(19), nil))

	targetMock.ExpectQuery(regexp.QuoteMeta(`SELECT p.id, p.name, p.group_id, p.transfer_enable, p.device_limit, p.speed_limit, COALESCE(COUNT(u.id), 0)
FROM v2_plan p
LEFT JOIN v2_user u ON u.plan_id = p.id
GROUP BY p.id, p.name, p.group_id, p.transfer_enable, p.device_limit, p.speed_limit
ORDER BY p.id`)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "group_id", "transfer_enable", "device_limit", "speed_limit", "users"}).
			AddRow(int64(1), "轻量", int64(1), int64(200), int64(2), int64(20), int64(10)))

	targetMock.ExpectBegin()
	targetMock.ExpectQuery(regexp.QuoteMeta(`SELECT id, email, invite_user_id, token, uuid, group_id, plan_id, transfer_enable, device_limit, speed_limit, expired_at FROM v2_user ORDER BY id`)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "email", "invite_user_id", "token", "uuid", "group_id", "plan_id", "transfer_enable", "device_limit", "speed_limit", "expired_at"}).
			AddRow(int64(8), "sameplan@example.com", nil, "existing-token", "existing-uuid", int64(1), int64(1), int64(200)*1024*1024*1024, int64(2), int64(20), int64(1893456000)))
	targetMock.ExpectExec(regexp.QuoteMeta(`UPDATE v2_user SET group_id = $1, plan_id = $2, transfer_enable = $3, device_limit = $4, speed_limit = $5, expired_at = $6, updated_at = $7 WHERE id = $8`)).
		WithArgs(int64(1), int64(1), int64(200)*1024*1024*1024, int64(2), int64(20), nil, sqlmock.AnyArg(), int64(8)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	targetMock.ExpectQuery(regexp.QuoteMeta(`SELECT id, user_id, code FROM v2_invite_code ORDER BY id`)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "user_id", "code"}))
	targetMock.ExpectQuery(regexp.QuoteMeta(`SELECT id, user_id, plan_id, period, invite_code, started_at, expired_at, created_at FROM v2_invite_campaign ORDER BY id`)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "user_id", "plan_id", "period", "invite_code", "started_at", "expired_at", "created_at"}))
	targetMock.ExpectQuery(regexp.QuoteMeta(`SELECT campaign_id, invitee_user_id FROM v2_invite_campaign_record`)).
		WillReturnRows(sqlmock.NewRows([]string{"campaign_id", "invitee_user_id"}))
	targetMock.ExpectCommit()

	result, err := mergeMySQLUsersIntoPostgres(ctx, sourceDB, targetDB, map[int64]int64{19: 1})
	if err != nil {
		t.Fatalf("mergeMySQLUsersIntoPostgres: %v", err)
	}
	if result.UsersMatchedExisting != 1 {
		t.Fatalf("expected matched existing users=1, got %d", result.UsersMatchedExisting)
	}

	if err := sourceMock.ExpectationsWereMet(); err != nil {
		t.Fatalf("source expectations: %v", err)
	}
	if err := targetMock.ExpectationsWereMet(); err != nil {
		t.Fatalf("target expectations: %v", err)
	}
}
