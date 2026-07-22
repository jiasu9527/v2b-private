package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"forest/go-api/internal/cliententry"
)

const (
	legacyEntryHostMigrationKey         = "legacy-server-host-dsl-to-entry-rules-v1"
	legacyEntryHostGroupingMigrationKey = "legacy-server-host-dsl-group-rules-v2"
	legacyEntryHostMigrationLockKey     = int64(0x464f524553544552)
	legacyEntryHostMigrationSortStep    = int64(10)
)

var (
	legacyEntryHostUAWithUserRangePattern = regexp.MustCompile(`^(.+?)\(U(.+?)(\d+)-(\d+)\)$`)
	legacyEntryHostUAPattern              = regexp.MustCompile(`^(.+?)\(U([^)]+)\)$`)
	legacyEntryHostPlanPattern            = regexp.MustCompile(`^(.+?)\(P(\d+)-(\d+)\)$`)
	legacyEntryHostDayRangePattern        = regexp.MustCompile(`^(.+?)\(D(\d+)-(\d+)\)$`)
	legacyEntryHostDayGTPattern           = regexp.MustCompile(`^(.+?)\(D>(\d+)\)$`)
	legacyEntryHostDayLEPattern           = regexp.MustCompile(`^(.+?)\(D<=(\d+)\)$`)
	legacyEntryHostDayShortPattern        = regexp.MustCompile(`^(.+?)\(D(\d+)\)$`)
	legacyEntryHostUserRangePattern       = regexp.MustCompile(`^(.+?)\((\d+)-(\d+)\)$`)
)

var legacyEntryHostServerSources = []legacyEntryHostServerSource{
	{ServerType: "shadowsocks", Table: "v2_server_shadowsocks"},
	{ServerType: "vmess", Table: "v2_server_vmess"},
	{ServerType: "trojan", Table: "v2_server_trojan"},
	{ServerType: "tuic", Table: "v2_server_tuic"},
	{ServerType: "hysteria", Table: "v2_server_hysteria"},
	{ServerType: "vless", Table: "v2_server_vless"},
	{ServerType: "anytls", Table: "v2_server_anytls"},
	{ServerType: "v2node", Table: "v2_server_v2node"},
}

type legacyEntryHostServerSource struct {
	ServerType string
	Table      string
}

// LegacyEntryRuleCondition is the JSON representation consumed by the
// structured client-entry rule evaluator. Values contains strings for UA
// rules and integers for numeric "in" rules.
type LegacyEntryRuleCondition struct {
	Field    string `json:"field"`
	Operator string `json:"operator"`
	Values   any    `json:"values,omitempty"`
	Min      *int64 `json:"min,omitempty"`
	Max      *int64 `json:"max,omitempty"`
	Value    any    `json:"value,omitempty"`
}

// LegacyEntryHostMigrationReport summarizes one committed migration. If
// AlreadyApplied is true, the values were loaded from the migration marker and
// no data was changed by this invocation.
type LegacyEntryHostMigrationReport struct {
	MigrationKey               string `json:"migration_key"`
	AppliedAt                  int64  `json:"applied_at"`
	AlreadyApplied             bool   `json:"-"`
	ServersScanned             int64  `json:"servers_scanned"`
	ServersRewritten           int64  `json:"servers_rewritten"`
	RulesCreated               int64  `json:"rules_created"`
	HideRulesCreated           int64  `json:"hide_rules_created"`
	LegacyEmailPoliciesUpdated int64  `json:"legacy_email_policies_updated"`
	LegacyEmailsMapped         int64  `json:"legacy_emails_mapped"`
	LegacyNoopPoliciesDisabled int64  `json:"legacy_noop_policies_disabled"`
	IgnoredFallbackHosts       int64  `json:"ignored_fallback_hosts"`
}

// LegacyEntryHostMigrationIssue identifies a legacy value that cannot be
// converted without changing its behavior. The migration aborts atomically if
// at least one issue is found.
type LegacyEntryHostMigrationIssue struct {
	Source     string `json:"source"`
	Table      string `json:"table,omitempty"`
	ServerType string `json:"server_type,omitempty"`
	ServerID   int64  `json:"server_id,omitempty"`
	PolicyID   int64  `json:"policy_id,omitempty"`
	Email      string `json:"email,omitempty"`
	Host       string `json:"host,omitempty"`
	Reason     string `json:"reason"`
}

type LegacyEntryHostMigrationError struct {
	Issues []LegacyEntryHostMigrationIssue
}

// LegacyEntryHostGroupingReport records the one-time compaction of rules
// created by the first version of this migration.  It only touches records
// carrying the automatic-migration remark, never administrator-created rules.
type LegacyEntryHostGroupingReport struct {
	MigrationKey        string `json:"migration_key"`
	AppliedAt           int64  `json:"applied_at"`
	AlreadyApplied      bool   `json:"-"`
	RulesMerged         int64  `json:"rules_merged"`
	MembersConsolidated int64  `json:"members_consolidated"`
}

func (e *LegacyEntryHostMigrationError) Error() string {
	if e == nil || len(e.Issues) == 0 {
		return "legacy entry host migration failed"
	}
	first := e.Issues[0]
	return fmt.Sprintf("legacy entry host migration blocked by %d issue(s): %s", len(e.Issues), first.Reason)
}

type legacyEntryHostRule struct {
	EntryHost  string
	Action     string
	Conditions []LegacyEntryRuleCondition
}

type legacyEntryHostParseResult struct {
	DefaultHost          string
	Rules                []legacyEntryHostRule
	NeedsMigration       bool
	HideWhenUnmatched    bool
	IgnoredFallbackHosts int
}

type legacyEntryHostNodePlan struct {
	Source       legacyEntryHostServerSource
	ServerID     int64
	ServerName   string
	OriginalHost string
	Parsed       legacyEntryHostParseResult
}

// legacyEntryHostRuleGroup keeps one structured rule for every identical
// legacy node-address rule chain.  Its members are all nodes that used that
// same chain, so the migration does not create a separate UI rule per node.
// The chain fingerprint is deliberately part of the key: two nodes can share
// an individual condition while using a different rule order, and merging
// those would change first-match behavior.
type legacyEntryHostRuleGroup struct {
	Plan     legacyEntryHostNodePlan
	Rule     legacyEntryHostRule
	Sequence int
	Members  []legacyEntryHostRuleMember
}

type legacyEntryHostRuleMember struct {
	ServerType string
	ServerID   int64
}

type legacyEntryEmailPolicyPlan struct {
	PolicyID  int64
	EntryHost string
	Emails    []string
	Disable   bool
}

// MigrateLegacyServerHostEntryRules performs the one-time conversion from the
// old comma/parenthesis host DSL into structured client-entry policies. It must
// be called after the structured policy columns (name, sort, action and
// conditions) have been added. PostgreSQL DDL, marker, policy inserts and node
// host rewrites all run in one serializable transaction.
func MigrateLegacyServerHostEntryRules(ctx context.Context, db *sql.DB) (LegacyEntryHostMigrationReport, error) {
	report := LegacyEntryHostMigrationReport{MigrationKey: legacyEntryHostMigrationKey}
	if db == nil {
		return report, errors.New("db is nil")
	}

	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return report, fmt.Errorf("begin legacy entry host migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, legacyEntryHostMigrationLockKey); err != nil {
		return report, fmt.Errorf("lock legacy entry host migration: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS v2_client_entry_rule_migration (
migration_key varchar(128) NOT NULL,
applied_at INTEGER NOT NULL,
report text NOT NULL DEFAULT '{}',
PRIMARY KEY (migration_key)
)`); err != nil {
		return report, fmt.Errorf("ensure legacy entry host migration marker: %w", err)
	}

	var storedReport string
	err = tx.QueryRowContext(ctx, `SELECT report FROM v2_client_entry_rule_migration WHERE migration_key = $1`, legacyEntryHostMigrationKey).Scan(&storedReport)
	switch {
	case err == nil:
		if err := json.Unmarshal([]byte(storedReport), &report); err != nil {
			return report, fmt.Errorf("decode legacy entry host migration marker: %w", err)
		}
		report.MigrationKey = legacyEntryHostMigrationKey
		report.AlreadyApplied = true
		if err := tx.Commit(); err != nil {
			return report, fmt.Errorf("commit legacy entry host migration marker read: %w", err)
		}
		return report, nil
	case !errors.Is(err, sql.ErrNoRows):
		return report, fmt.Errorf("read legacy entry host migration marker: %w", err)
	}

	emailPlans, emailIssues, err := loadLegacyEntryEmailPolicyPlans(ctx, tx)
	if err != nil {
		return report, err
	}
	nodePlans, nodeIssues, err := loadLegacyEntryHostNodePlans(ctx, tx, &report)
	if err != nil {
		return report, err
	}
	issues := append(emailIssues, nodeIssues...)
	if len(issues) > 0 {
		return report, &LegacyEntryHostMigrationError{Issues: issues}
	}
	// Very old deployments created a table-level uniqueness constraint with
	// this name.  It includes retired columns (notably email), so it rejects
	// more than one migrated node rule before those columns can be removed.
	// The new structured rule table deliberately has no equivalent constraint;
	// rule ordering and node memberships are independent.  Drop it inside the
	// same migration transaction so a failure rolls this schema change back.
	if _, err := tx.ExecContext(ctx, `ALTER TABLE v2_client_entry_user_policy DROP CONSTRAINT IF EXISTS uniq_v2_client_entry_user_policy`); err != nil {
		return report, fmt.Errorf("drop legacy client entry policy uniqueness constraint: %w", err)
	}

	now := time.Now().Unix()
	nextSort := legacyEntryHostMigrationSortStep
	for _, plan := range emailPlans {
		conditionsJSON, err := marshalLegacyEntryConditions([]LegacyEntryRuleCondition{{
			Field:    "email",
			Operator: "in",
			Values:   append([]string(nil), plan.Emails...),
		}})
		if err != nil {
			return report, fmt.Errorf("encode legacy policy %d emails: %w", plan.PolicyID, err)
		}
		if _, err := cliententry.DecodeConditions(conditionsJSON); err != nil {
			return report, fmt.Errorf("validate legacy policy %d emails: %w", plan.PolicyID, err)
		}
		name := fmt.Sprintf("迁移用户入口 #%d", plan.PolicyID)
		result, err := tx.ExecContext(ctx, `UPDATE v2_client_entry_user_policy
SET name = CASE WHEN trim(name) = '' THEN $1 ELSE name END,
    sort = $2, action = 'override', conditions = $3,
    enabled = CASE WHEN $5 THEN 0 ELSE enabled END,
    updated_at = $4
WHERE id = $6`, name, nextSort, conditionsJSON, now, plan.Disable, plan.PolicyID)
		if err != nil {
			return report, fmt.Errorf("convert legacy email policy %d: %w", plan.PolicyID, err)
		}
		if err := requireExactlyOneRow(result, fmt.Sprintf("legacy email policy %d", plan.PolicyID)); err != nil {
			return report, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM v2_client_entry_user_policy_user WHERE policy_id = $1`, plan.PolicyID); err != nil {
			return report, fmt.Errorf("delete converted policy %d emails: %w", plan.PolicyID, err)
		}
		report.LegacyEmailPoliciesUpdated++
		report.LegacyEmailsMapped += int64(len(plan.Emails))
		if plan.Disable {
			report.LegacyNoopPoliciesDisabled++
		}
		nextSort += legacyEntryHostMigrationSortStep
	}

	ruleGroups, err := groupLegacyEntryHostRules(nodePlans)
	if err != nil {
		return report, err
	}
	for _, group := range ruleGroups {
		if err := insertMigratedEntryRule(ctx, tx, group.Plan, group.Rule, nextSort, group.Sequence, group.Members, now); err != nil {
			return report, err
		}
		report.RulesCreated++
		if group.Rule.Action == "hide" {
			report.HideRulesCreated++
		}
		nextSort += legacyEntryHostMigrationSortStep
	}

	for _, plan := range nodePlans {

		query := fmt.Sprintf(`UPDATE %s SET host = $1, updated_at = $2 WHERE id = $3 AND host = $4`, plan.Source.Table)
		result, err := tx.ExecContext(ctx, query, plan.Parsed.DefaultHost, now, plan.ServerID, plan.OriginalHost)
		if err != nil {
			return report, fmt.Errorf("rewrite %s node %d host: %w", plan.Source.ServerType, plan.ServerID, err)
		}
		if err := requireExactlyOneRow(result, fmt.Sprintf("%s node %d", plan.Source.ServerType, plan.ServerID)); err != nil {
			return report, err
		}
		report.ServersRewritten++
		report.IgnoredFallbackHosts += int64(plan.Parsed.IgnoredFallbackHosts)
	}

	// The runtime no longer has a legacy parser or email-based policy branch.
	// Remove the retired storage only after every conversion has succeeded, in
	// the same transaction as the marker, so an interrupted upgrade remains
	// fully recoverable by simply running it again.
	if err := discardLegacyClientEntryStorage(ctx, tx); err != nil {
		return report, err
	}

	report.AppliedAt = now
	reportJSON, err := json.Marshal(report)
	if err != nil {
		return report, fmt.Errorf("encode legacy entry host migration report: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO v2_client_entry_rule_migration (migration_key, applied_at, report)
VALUES ($1, $2, $3)`, legacyEntryHostMigrationKey, now, string(reportJSON)); err != nil {
		return report, fmt.Errorf("mark legacy entry host migration complete: %w", err)
	}
	// No runtime path uses the retired email/single-node columns after this
	// point.  Drop them in the same transaction so the next schema ensure does
	// not recreate and backfill an unused email association table.
	if err := dropRetiredClientEntryRuleSchema(ctx, tx); err != nil {
		return report, err
	}
	if err := tx.Commit(); err != nil {
		return report, fmt.Errorf("commit legacy entry host migration: %w", err)
	}
	return report, nil
}

// ConsolidateLegacyServerHostEntryRules combines v1 auto-migrated rules that
// originated from the same full legacy host chain.  A rule may therefore have
// many node members, exactly as the rule-center UI expects.  The full source
// chain is used as part of the grouping key to preserve first-match order.
func ConsolidateLegacyServerHostEntryRules(ctx context.Context, db *sql.DB) (LegacyEntryHostGroupingReport, error) {
	report := LegacyEntryHostGroupingReport{MigrationKey: legacyEntryHostGroupingMigrationKey}
	if db == nil {
		return report, errors.New("db is nil")
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return report, fmt.Errorf("begin legacy entry rule grouping: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, legacyEntryHostMigrationLockKey); err != nil {
		return report, fmt.Errorf("lock legacy entry rule grouping: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS v2_client_entry_rule_migration (
migration_key varchar(128) NOT NULL,
applied_at INTEGER NOT NULL,
report text NOT NULL DEFAULT '{}',
PRIMARY KEY (migration_key)
)`); err != nil {
		return report, fmt.Errorf("ensure legacy entry rule grouping marker: %w", err)
	}
	var stored string
	err = tx.QueryRowContext(ctx, `SELECT report FROM v2_client_entry_rule_migration WHERE migration_key = $1`, legacyEntryHostGroupingMigrationKey).Scan(&stored)
	switch {
	case err == nil:
		if err := json.Unmarshal([]byte(stored), &report); err != nil {
			return report, fmt.Errorf("decode legacy entry rule grouping marker: %w", err)
		}
		report.MigrationKey = legacyEntryHostGroupingMigrationKey
		report.AlreadyApplied = true
		if err := tx.Commit(); err != nil {
			return report, fmt.Errorf("commit legacy entry rule grouping marker read: %w", err)
		}
		return report, nil
	case !errors.Is(err, sql.ErrNoRows):
		return report, fmt.Errorf("read legacy entry rule grouping marker: %w", err)
	}

	type policy struct {
		ID         int64
		Sort       int64
		Action     string
		Conditions string
		EntryHost  string
		Remarks    string
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, sort, action, conditions, entry_host, remarks
FROM v2_client_entry_user_policy
WHERE remarks LIKE $1
ORDER BY sort ASC NULLS LAST, id ASC`, "由旧节点地址规则自动迁移；原配置:%")
	if err != nil {
		return report, fmt.Errorf("query auto-migrated entry rules for grouping: %w", err)
	}
	groups := make(map[string][]policy)
	order := make([]string, 0)
	for rows.Next() {
		var value policy
		if err := rows.Scan(&value.ID, &value.Sort, &value.Action, &value.Conditions, &value.EntryHost, &value.Remarks); err != nil {
			_ = rows.Close()
			return report, fmt.Errorf("scan auto-migrated entry rule for grouping: %w", err)
		}
		// Equal condition/action/entry rules are one user-visible rule even if
		// their old node host strings had different fallback text.
		key := strings.Join([]string{value.Action, value.Conditions, value.EntryHost}, "\x00")
		if _, exists := groups[key]; !exists {
			order = append(order, key)
		}
		groups[key] = append(groups[key], value)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return report, fmt.Errorf("iterate auto-migrated entry rules for grouping: %w", err)
	}
	if err := rows.Close(); err != nil {
		return report, fmt.Errorf("close auto-migrated entry rules for grouping: %w", err)
	}

	now := time.Now().Unix()
	for _, key := range order {
		policies := groups[key]
		if len(policies) < 2 {
			continue
		}
		canonical := policies[0]
		for _, duplicate := range policies[1:] {
			memberRows, err := tx.QueryContext(ctx, `SELECT server_type, server_id FROM v2_client_entry_user_policy_member WHERE policy_id = $1`, duplicate.ID)
			if err != nil {
				return report, fmt.Errorf("query members of auto-migrated rule %d: %w", duplicate.ID, err)
			}
			members := make([]legacyEntryHostRuleMember, 0)
			for memberRows.Next() {
				var serverType string
				var serverID int64
				if err := memberRows.Scan(&serverType, &serverID); err != nil {
					_ = memberRows.Close()
					return report, fmt.Errorf("scan members of auto-migrated rule %d: %w", duplicate.ID, err)
				}
				members = append(members, legacyEntryHostRuleMember{ServerType: serverType, ServerID: serverID})
			}
			if err := memberRows.Err(); err != nil {
				_ = memberRows.Close()
				return report, fmt.Errorf("iterate members of auto-migrated rule %d: %w", duplicate.ID, err)
			}
			if err := memberRows.Close(); err != nil {
				return report, fmt.Errorf("close members of auto-migrated rule %d: %w", duplicate.ID, err)
			}
			for _, member := range members {
				if _, err := tx.ExecContext(ctx, `INSERT INTO v2_client_entry_user_policy_member
(policy_id, server_type, server_id, sort, created_at, updated_at)
VALUES ($1, $2, $3, NULL, $4, $4)
ON CONFLICT (policy_id, server_type, server_id) DO NOTHING`, canonical.ID, member.ServerType, member.ServerID, now); err != nil {
					return report, fmt.Errorf("merge member into auto-migrated rule %d: %w", canonical.ID, err)
				}
				report.MembersConsolidated++
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM v2_client_entry_user_policy_member WHERE policy_id = $1`, duplicate.ID); err != nil {
				return report, fmt.Errorf("delete duplicate auto-migrated rule %d members: %w", duplicate.ID, err)
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM v2_client_entry_user_policy WHERE id = $1`, duplicate.ID); err != nil {
				return report, fmt.Errorf("delete duplicate auto-migrated rule %d: %w", duplicate.ID, err)
			}
			report.RulesMerged++
		}
	}
	report.AppliedAt = now
	payload, err := json.Marshal(report)
	if err != nil {
		return report, fmt.Errorf("encode legacy entry rule grouping report: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO v2_client_entry_rule_migration (migration_key, applied_at, report) VALUES ($1, $2, $3)`, legacyEntryHostGroupingMigrationKey, now, string(payload)); err != nil {
		return report, fmt.Errorf("mark legacy entry rule grouping complete: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return report, fmt.Errorf("commit legacy entry rule grouping: %w", err)
	}
	return report, nil
}

// RestoreLegacyEmailPolicyConditions repairs the first structured-rule
// migration, which incorrectly converted legacy email selectors to user IDs.
// Email and user-ID are intentionally independent rule fields.  The old
// migrated policy name is the provenance marker; manually created ID rules
// are never touched.
func RestoreLegacyEmailPolicyConditions(ctx context.Context, db *sql.DB) (int64, error) {
	if db == nil {
		return 0, errors.New("db is nil")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin legacy email condition restore: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	type policy struct {
		ID         int64
		Conditions string
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, conditions FROM v2_client_entry_user_policy
WHERE name LIKE $1
ORDER BY id ASC`, "迁移用户入口 #%")
	if err != nil {
		return 0, fmt.Errorf("query migrated legacy email policies: %w", err)
	}
	policies := make([]policy, 0)
	for rows.Next() {
		var value policy
		if err := rows.Scan(&value.ID, &value.Conditions); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("scan migrated legacy email policy: %w", err)
		}
		policies = append(policies, value)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, fmt.Errorf("iterate migrated legacy email policies: %w", err)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("close migrated legacy email policies: %w", err)
	}

	updated := int64(0)
	now := time.Now().Unix()
	for _, policy := range policies {
		conditions, err := cliententry.DecodeConditions(policy.Conditions)
		if err != nil || len(conditions) != 1 || conditions[0].Field != "user_id" || conditions[0].Operator != "in" {
			continue
		}
		emails := make([]json.RawMessage, 0, len(conditions[0].Values))
		for _, rawID := range conditions[0].Values {
			id, err := strconv.ParseInt(strings.TrimSpace(string(rawID)), 10, 64)
			if err != nil || id <= 0 {
				emails = nil
				break
			}
			var email string
			if err := tx.QueryRowContext(ctx, `SELECT lower(trim(email)) FROM v2_user WHERE id = $1`, id).Scan(&email); err != nil || strings.TrimSpace(email) == "" {
				emails = nil
				break
			}
			encoded, _ := json.Marshal(strings.TrimSpace(email))
			emails = append(emails, encoded)
		}
		if len(emails) == 0 {
			continue
		}
		encoded, err := cliententry.EncodeConditions([]cliententry.Condition{{Field: "email", Operator: "in", Values: emails}})
		if err != nil {
			return updated, fmt.Errorf("encode restored legacy email policy %d: %w", policy.ID, err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE v2_client_entry_user_policy SET conditions = $2, updated_at = $3 WHERE id = $1`, policy.ID, encoded, now); err != nil {
			return updated, fmt.Errorf("restore legacy email policy %d: %w", policy.ID, err)
		}
		updated++
	}
	if err := tx.Commit(); err != nil {
		return updated, fmt.Errorf("commit legacy email condition restore: %w", err)
	}
	return updated, nil
}

func dropRetiredClientEntryRuleSchema(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS v2_client_entry_user_policy_user`); err != nil {
		return fmt.Errorf("drop retired client entry policy user map: %w", err)
	}
	for _, column := range []string{"email", "entry_group_id", "server_type", "server_id"} {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE v2_client_entry_user_policy DROP COLUMN IF EXISTS `+column); err != nil {
			return fmt.Errorf("drop retired client entry policy column %s: %w", column, err)
		}
	}
	return nil
}

func discardLegacyClientEntryStorage(ctx context.Context, tx *sql.Tx) error {
	for _, statement := range []string{
		`DROP TABLE IF EXISTS v2_client_entry_user_policy_user`,
		`ALTER TABLE v2_client_entry_user_policy DROP COLUMN IF EXISTS email`,
		`ALTER TABLE v2_client_entry_user_policy DROP COLUMN IF EXISTS entry_group_id`,
		`ALTER TABLE v2_client_entry_user_policy DROP COLUMN IF EXISTS server_type`,
		`ALTER TABLE v2_client_entry_user_policy DROP COLUMN IF EXISTS server_id`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("discard legacy client entry storage: %w", err)
		}
	}
	return nil
}

func loadLegacyEntryEmailPolicyPlans(ctx context.Context, tx *sql.Tx) ([]legacyEntryEmailPolicyPlan, []LegacyEntryHostMigrationIssue, error) {
	// Fresh installations intentionally do not create the retired email mapping
	// table.  Its absence means there is nothing to backfill; host DSL scanning
	// still needs to run and the migration still needs to write its marker.
	var legacyTableExists bool
	if err := tx.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, "v2_client_entry_user_policy_user").Scan(&legacyTableExists); err != nil {
		return nil, nil, fmt.Errorf("check legacy client entry policy email table: %w", err)
	}
	if !legacyTableExists {
		return nil, nil, nil
	}

	rows, err := tx.QueryContext(ctx, `SELECT pu.policy_id, p.entry_host, trim(pu.email)
FROM v2_client_entry_user_policy_user pu
JOIN v2_client_entry_user_policy p ON p.id = pu.policy_id
ORDER BY pu.policy_id ASC, lower(trim(pu.email)) ASC`)
	if err != nil {
		return nil, nil, fmt.Errorf("query legacy client entry policy emails: %w", err)
	}
	defer rows.Close()

	byPolicy := make(map[int64]*legacyEntryEmailPolicyPlan)
	order := make([]int64, 0)
	issues := make([]LegacyEntryHostMigrationIssue, 0)
	for rows.Next() {
		var policyID int64
		var entryHost sql.NullString
		var email string
		if err := rows.Scan(&policyID, &entryHost, &email); err != nil {
			return nil, nil, fmt.Errorf("scan legacy client entry policy email: %w", err)
		}
		plan := byPolicy[policyID]
		if plan == nil {
			plan = &legacyEntryEmailPolicyPlan{PolicyID: policyID, EntryHost: strings.TrimSpace(entryHost.String)}
			byPolicy[policyID] = plan
			order = append(order, policyID)
		}
		email = strings.ToLower(strings.TrimSpace(email))
		if email != "" && !containsFold(plan.Emails, email) {
			plan.Emails = append(plan.Emails, email)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("iterate legacy client entry policy emails: %w", err)
	}

	plans := make([]legacyEntryEmailPolicyPlan, 0, len(order))
	for _, policyID := range order {
		plan := byPolicy[policyID]
		if len(plan.Emails) == 0 {
			continue
		}
		if plan.EntryHost == "" {
			// The old runtime skipped a policy with no address even when it was
			// enabled.  Make that no-op explicit so the new unconditional rule
			// evaluator cannot accidentally publish a blank host.
			plan.Disable = true
		} else {
			parsed, err := parseLegacyEntryHost(plan.EntryHost)
			if err != nil {
				issues = append(issues, LegacyEntryHostMigrationIssue{
					Source:   "policy_entry_host",
					PolicyID: plan.PolicyID,
					Host:     plan.EntryHost,
					Reason:   fmt.Sprintf("旧用户入口策略地址无法迁移：%v", err),
				})
			} else if parsed.NeedsMigration {
				issues = append(issues, LegacyEntryHostMigrationIssue{
					Source:   "policy_entry_host",
					PolicyID: plan.PolicyID,
					Host:     plan.EntryHost,
					Reason:   "旧用户入口策略地址包含多地址或条件语法，请先拆分为独立规则",
				})
			}
		}
		plans = append(plans, *plan)
	}
	return plans, issues, nil
}

func loadLegacyEntryHostNodePlans(ctx context.Context, tx *sql.Tx, report *LegacyEntryHostMigrationReport) ([]legacyEntryHostNodePlan, []LegacyEntryHostMigrationIssue, error) {
	plans := make([]legacyEntryHostNodePlan, 0)
	issues := make([]LegacyEntryHostMigrationIssue, 0)
	for _, source := range legacyEntryHostServerSources {
		query := fmt.Sprintf(`SELECT id, name, host FROM %s ORDER BY id ASC`, source.Table)
		rows, err := tx.QueryContext(ctx, query)
		if err != nil {
			return nil, nil, fmt.Errorf("query %s legacy hosts: %w", source.ServerType, err)
		}
		for rows.Next() {
			var serverID int64
			var serverName sql.NullString
			var host sql.NullString
			if err := rows.Scan(&serverID, &serverName, &host); err != nil {
				_ = rows.Close()
				return nil, nil, fmt.Errorf("scan %s legacy host: %w", source.ServerType, err)
			}
			report.ServersScanned++
			parsed, err := parseLegacyEntryHost(host.String)
			if err != nil {
				issues = append(issues, LegacyEntryHostMigrationIssue{
					Source:     "server_host",
					Table:      source.Table,
					ServerType: source.ServerType,
					ServerID:   serverID,
					Host:       host.String,
					Reason:     err.Error(),
				})
				continue
			}
			if !parsed.NeedsMigration {
				continue
			}
			plans = append(plans, legacyEntryHostNodePlan{
				Source:       source,
				ServerID:     serverID,
				ServerName:   strings.TrimSpace(serverName.String),
				OriginalHost: host.String,
				Parsed:       parsed,
			})
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, nil, fmt.Errorf("iterate %s legacy hosts: %w", source.ServerType, err)
		}
		if err := rows.Close(); err != nil {
			return nil, nil, fmt.Errorf("close %s legacy host rows: %w", source.ServerType, err)
		}
	}
	return plans, issues, nil
}

func groupLegacyEntryHostRules(plans []legacyEntryHostNodePlan) ([]legacyEntryHostRuleGroup, error) {
	byKey := make(map[string]int)
	groups := make([]legacyEntryHostRuleGroup, 0)
	for _, plan := range plans {
		rules := append([]legacyEntryHostRule(nil), plan.Parsed.Rules...)
		if plan.Parsed.HideWhenUnmatched {
			rules = append(rules, legacyEntryHostRule{Action: "hide", Conditions: []LegacyEntryRuleCondition{}})
		}
		for index, rule := range rules {
			key, err := legacyEntryHostRuleGroupKey(plan, rule, index+1)
			if err != nil {
				return nil, err
			}
			member := legacyEntryHostRuleMember{ServerType: plan.Source.ServerType, ServerID: plan.ServerID}
			if existing, ok := byKey[key]; ok {
				groups[existing].Members = append(groups[existing].Members, member)
				continue
			}
			byKey[key] = len(groups)
			groups = append(groups, legacyEntryHostRuleGroup{Plan: plan, Rule: rule, Sequence: index + 1, Members: []legacyEntryHostRuleMember{member}})
		}
	}
	return groups, nil
}

func legacyEntryHostRuleGroupKey(plan legacyEntryHostNodePlan, rule legacyEntryHostRule, sequence int) (string, error) {
	conditions, err := marshalLegacyEntryConditions(rule.Conditions)
	if err != nil {
		return "", err
	}
	// A rule is shared by every node with the same effective condition and
	// result.  Rule order is configured in the dedicated UI after migration;
	// keeping source-host text here would unnecessarily create one rule/node.
	return strings.Join([]string{rule.Action, rule.EntryHost, conditions}, "\x00"), nil
}

func insertMigratedEntryRule(ctx context.Context, tx *sql.Tx, plan legacyEntryHostNodePlan, rule legacyEntryHostRule, sortValue int64, sequence int, members []legacyEntryHostRuleMember, now int64) error {
	conditionsJSON, err := marshalLegacyEntryConditions(rule.Conditions)
	if err != nil {
		return fmt.Errorf("encode %s node %d migrated rule: %w", plan.Source.ServerType, plan.ServerID, err)
	}
	nodeLabel := plan.ServerName
	if nodeLabel == "" {
		nodeLabel = fmt.Sprintf("%s #%d", plan.Source.ServerType, plan.ServerID)
	}
	if len(members) > 1 {
		nodeLabel = fmt.Sprintf("%s 等 %d 个节点", nodeLabel, len(members))
	}
	actionLabel := fmt.Sprintf("入口 %d", sequence)
	if rule.Action == "hide" {
		actionLabel = "未命中隐藏"
	}
	name := truncateRunes(fmt.Sprintf("迁移: %s · %s", nodeLabel, actionLabel), 255)
	remarks := truncateRunes("由旧节点地址规则自动迁移；原配置: "+plan.OriginalHost, 255)

	var policyID int64
	err = tx.QueryRowContext(ctx, `INSERT INTO v2_client_entry_user_policy
(name, sort, action, conditions, entry_host, enabled, remarks, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, 1, $6, $7, $7)
RETURNING id`, name, sortValue, rule.Action, conditionsJSON, rule.EntryHost, remarks, now).Scan(&policyID)
	if err != nil {
		return fmt.Errorf("insert %s node %d migrated rule: %w", plan.Source.ServerType, plan.ServerID, err)
	}
	for _, member := range members {
		if _, err := tx.ExecContext(ctx, `INSERT INTO v2_client_entry_user_policy_member
(policy_id, server_type, server_id, sort, created_at, updated_at)
VALUES ($1, $2, $3, NULL, $4, $4)`, policyID, member.ServerType, member.ServerID, now); err != nil {
			return fmt.Errorf("attach migrated rule %d to %s node %d: %w", policyID, member.ServerType, member.ServerID, err)
		}
	}
	return nil
}

func parseLegacyEntryHost(hostConfig string) (legacyEntryHostParseResult, error) {
	result := legacyEntryHostParseResult{}
	originalHostConfig := hostConfig
	hostConfig = strings.TrimSpace(hostConfig)
	if hostConfig == "" {
		return result, errors.New("节点地址为空，无法迁移为单个普通入口地址")
	}

	plainHosts := make([]string, 0)
	// The legacy evaluator always split on commas, including configurations
	// such as "entry.example.com,".  Mark every comma-delimited value for
	// rewrite so the new node host is a single ordinary address rather than
	// leaving a syntactically obsolete trailing/leading delimiter behind.
	if originalHostConfig != hostConfig || strings.Contains(hostConfig, ",") {
		result.NeedsMigration = true
	}
	for _, rawPart := range strings.Split(hostConfig, ",") {
		part := strings.TrimSpace(rawPart)
		if part == "" {
			continue
		}
		rules, recognized, err := parseLegacyEntryHostConditional(part)
		if err != nil {
			return result, err
		}
		if recognized {
			result.NeedsMigration = true
			for _, rule := range rules {
				if strings.TrimSpace(rule.EntryHost) == "" {
					return result, fmt.Errorf("旧地址条件 %q 缺少入口地址", part)
				}
			}
			result.Rules = append(result.Rules, rules...)
			continue
		}
		if strings.ContainsAny(part, "()") {
			return result, fmt.Errorf("存在无法识别的旧地址条件 %q", part)
		}
		plainHosts = append(plainHosts, part)
	}

	if !result.NeedsMigration {
		result.DefaultHost = hostConfig
		return finalizeLegacyEntryHostParseResult(result)
	}
	if len(plainHosts) > 0 {
		result.DefaultHost = plainHosts[len(plainHosts)-1]
		result.IgnoredFallbackHosts = len(plainHosts) - 1
		return finalizeLegacyEntryHostParseResult(result)
	}
	if len(result.Rules) == 0 {
		return legacyEntryHostParseResult{}, fmt.Errorf("旧地址列表没有可用的默认地址或条件入口")
	}
	// The base host is never exposed for unmatched users because the final hide
	// rule wins. A real conditional host is used so the node row remains valid.
	result.DefaultHost = result.Rules[0].EntryHost
	result.HideWhenUnmatched = true
	return finalizeLegacyEntryHostParseResult(result)
}

// finalizeLegacyEntryHostParseResult validates the generated values with the
// exact same contract used by new rule writes.  The migration writes directly
// to the database, so skipping this check could introduce a rule that makes
// every subsequent subscription request fail while decoding conditions.
func finalizeLegacyEntryHostParseResult(result legacyEntryHostParseResult) (legacyEntryHostParseResult, error) {
	if _, err := cliententry.NormalizeHost(result.DefaultHost); err != nil {
		return legacyEntryHostParseResult{}, fmt.Errorf("节点默认地址 %q 无法迁移：%w", result.DefaultHost, err)
	}
	for _, rule := range result.Rules {
		if rule.Action != "override" {
			return legacyEntryHostParseResult{}, fmt.Errorf("旧地址规则动作 %q 无法迁移", rule.Action)
		}
		if _, err := cliententry.NormalizeHost(rule.EntryHost); err != nil {
			return legacyEntryHostParseResult{}, fmt.Errorf("条件入口地址 %q 无法迁移：%w", rule.EntryHost, err)
		}
		encoded, err := marshalLegacyEntryConditions(rule.Conditions)
		if err != nil {
			return legacyEntryHostParseResult{}, fmt.Errorf("编码条件入口规则失败：%w", err)
		}
		if _, err := cliententry.DecodeConditions(encoded); err != nil {
			return legacyEntryHostParseResult{}, fmt.Errorf("条件入口规则无效：%w", err)
		}
	}
	return result, nil
}

func parseLegacyEntryHostConditional(part string) ([]legacyEntryHostRule, bool, error) {
	if matches := legacyEntryHostUAWithUserRangePattern.FindStringSubmatch(part); len(matches) == 5 {
		minID, maxID, err := parseLegacyRange(matches[3], matches[4])
		if err != nil {
			return nil, true, fmt.Errorf("解析 UA 用户 ID 范围 %q: %w", part, err)
		}
		branches, err := parseLegacyUAConditions(matches[2])
		if err != nil {
			return nil, true, fmt.Errorf("解析 UA 条件 %q: %w", part, err)
		}
		userCondition := LegacyEntryRuleCondition{Field: "user_id", Operator: "between", Min: &minID, Max: &maxID}
		rules := make([]legacyEntryHostRule, 0, len(branches))
		for _, branch := range branches {
			conditions := append([]LegacyEntryRuleCondition(nil), branch...)
			conditions = append(conditions, userCondition)
			rules = append(rules, legacyEntryHostRule{EntryHost: strings.TrimSpace(matches[1]), Action: "override", Conditions: conditions})
		}
		return rules, true, nil
	}
	if matches := legacyEntryHostUAPattern.FindStringSubmatch(part); len(matches) == 3 {
		branches, err := parseLegacyUAConditions(matches[2])
		if err != nil {
			return nil, true, fmt.Errorf("解析 UA 条件 %q: %w", part, err)
		}
		rules := make([]legacyEntryHostRule, 0, len(branches))
		for _, conditions := range branches {
			rules = append(rules, legacyEntryHostRule{EntryHost: strings.TrimSpace(matches[1]), Action: "override", Conditions: conditions})
		}
		return rules, true, nil
	}
	if matches := legacyEntryHostPlanPattern.FindStringSubmatch(part); len(matches) == 4 {
		minPlan, maxPlan, err := parseLegacyRange(matches[2], matches[3])
		if err != nil {
			return nil, true, fmt.Errorf("解析套餐范围 %q: %w", part, err)
		}
		return oneLegacyNumericRule(matches[1], "plan_id", "between", &minPlan, &maxPlan, nil), true, nil
	}
	if matches := legacyEntryHostDayRangePattern.FindStringSubmatch(part); len(matches) == 4 {
		minDays, maxDays, err := parseLegacyRange(matches[2], matches[3])
		if err != nil {
			return nil, true, fmt.Errorf("解析注册天数范围 %q: %w", part, err)
		}
		return oneLegacyNumericRule(matches[1], "registration_days", "between", &minDays, &maxDays, nil), true, nil
	}
	if matches := legacyEntryHostDayGTPattern.FindStringSubmatch(part); len(matches) == 3 {
		value, err := parseLegacyInteger(matches[2])
		if err != nil {
			return nil, true, fmt.Errorf("解析注册天数 %q: %w", part, err)
		}
		return oneLegacyNumericRule(matches[1], "registration_days", "gt", nil, nil, value), true, nil
	}
	if matches := legacyEntryHostDayLEPattern.FindStringSubmatch(part); len(matches) == 3 {
		value, err := parseLegacyInteger(matches[2])
		if err != nil {
			return nil, true, fmt.Errorf("解析注册天数 %q: %w", part, err)
		}
		return oneLegacyNumericRule(matches[1], "registration_days", "lte", nil, nil, value), true, nil
	}
	if matches := legacyEntryHostDayShortPattern.FindStringSubmatch(part); len(matches) == 3 {
		value, err := parseLegacyInteger(matches[2])
		if err != nil {
			return nil, true, fmt.Errorf("解析注册天数 %q: %w", part, err)
		}
		return oneLegacyNumericRule(matches[1], "registration_days", "lte", nil, nil, value), true, nil
	}
	if matches := legacyEntryHostUserRangePattern.FindStringSubmatch(part); len(matches) == 4 {
		minID, maxID, err := parseLegacyRange(matches[2], matches[3])
		if err != nil {
			return nil, true, fmt.Errorf("解析用户 ID 范围 %q: %w", part, err)
		}
		return oneLegacyNumericRule(matches[1], "user_id", "between", &minID, &maxID, nil), true, nil
	}
	return nil, false, nil
}

func parseLegacyUAConditions(raw string) ([][]LegacyEntryRuleCondition, error) {
	positives := make([]string, 0)
	negatives := make([]string, 0)
	wantsEmpty := false
	firstPositiveKind := ""
	for _, rawKeyword := range strings.Split(raw, "|") {
		keyword := strings.TrimSpace(rawKeyword)
		keyword = strings.TrimPrefix(strings.TrimPrefix(keyword, "U"), "u")
		if keyword == "" {
			continue
		}
		if normalized, negated := legacyNegatedUAKeyword(keyword); negated {
			if normalized == "" {
				return nil, fmt.Errorf("排除 UA 关键词不能为空")
			}
			negatives = appendUniqueFold(negatives, normalized)
			continue
		}
		if legacyMissingUAKeyword(keyword) {
			wantsEmpty = true
			if firstPositiveKind == "" {
				firstPositiveKind = "empty"
			}
			continue
		}
		positives = appendUniqueFold(positives, keyword)
		if firstPositiveKind == "" {
			firstPositiveKind = "contains"
		}
	}

	exclusion := func() []LegacyEntryRuleCondition {
		if len(negatives) == 0 {
			return nil
		}
		return []LegacyEntryRuleCondition{{Field: "ua", Operator: "excludes_any", Values: append([]string(nil), negatives...)}}
	}
	containsBranch := func() []LegacyEntryRuleCondition {
		if len(positives) == 0 {
			return nil
		}
		branch := []LegacyEntryRuleCondition{{Field: "ua", Operator: "contains_any", Values: append([]string(nil), positives...)}}
		return append(branch, exclusion()...)
	}
	emptyBranch := func() []LegacyEntryRuleCondition {
		if !wantsEmpty {
			return nil
		}
		branch := []LegacyEntryRuleCondition{{Field: "ua", Operator: "empty"}}
		return append(branch, exclusion()...)
	}

	branches := make([][]LegacyEntryRuleCondition, 0, 2)
	if firstPositiveKind == "empty" {
		if branch := emptyBranch(); branch != nil {
			branches = append(branches, branch)
		}
		if branch := containsBranch(); branch != nil {
			branches = append(branches, branch)
		}
	} else {
		if branch := containsBranch(); branch != nil {
			branches = append(branches, branch)
		}
		if branch := emptyBranch(); branch != nil {
			branches = append(branches, branch)
		}
	}
	if len(branches) == 0 {
		branches = append(branches, exclusion())
	}
	return branches, nil
}

func oneLegacyNumericRule(host, field, operator string, minValue, maxValue *int64, value any) []legacyEntryHostRule {
	condition := LegacyEntryRuleCondition{Field: field, Operator: operator, Min: minValue, Max: maxValue, Value: value}
	return []legacyEntryHostRule{{
		EntryHost:  strings.TrimSpace(host),
		Action:     "override",
		Conditions: []LegacyEntryRuleCondition{condition},
	}}
}

func parseLegacyRange(rawMin, rawMax string) (int64, int64, error) {
	minValue, err := parseLegacyInteger(rawMin)
	if err != nil {
		return 0, 0, err
	}
	maxValue, err := parseLegacyInteger(rawMax)
	if err != nil {
		return 0, 0, err
	}
	if minValue > maxValue {
		return 0, 0, fmt.Errorf("范围起始值 %d 不能大于结束值 %d", minValue, maxValue)
	}
	return minValue, maxValue, nil
}

func parseLegacyInteger(raw string) (int64, error) {
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return 0, err
	}
	return value, nil
}

func marshalLegacyEntryConditions(conditions []LegacyEntryRuleCondition) (string, error) {
	if conditions == nil {
		conditions = []LegacyEntryRuleCondition{}
	}
	encoded, err := json.Marshal(conditions)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func requireExactlyOneRow(result sql.Result, label string) error {
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read %s affected rows: %w", label, err)
	}
	if count != 1 {
		return fmt.Errorf("%s changed concurrently: expected 1 affected row, got %d", label, count)
	}
	return nil
}

func legacyMissingUAKeyword(keyword string) bool {
	switch strings.ToLower(strings.TrimSpace(keyword)) {
	case "none", "empty", "blank", "null", "missing", "noua", "no-ua", "no_ua":
		return true
	default:
		return false
	}
}

func legacyNegatedUAKeyword(keyword string) (string, bool) {
	trimmed := strings.TrimSpace(keyword)
	switch {
	case strings.HasPrefix(trimmed, "!"):
		return strings.TrimSpace(trimmed[1:]), true
	case strings.HasPrefix(trimmed, "-"):
		return strings.TrimSpace(trimmed[1:]), true
	default:
		return "", false
	}
}

func appendUniqueFold(values []string, next string) []string {
	if containsFold(values, next) {
		return values
	}
	return append(values, next)
}

func containsFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(target)) {
			return true
		}
	}
	return false
}

func containsInt64(values []int64, target int64) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit])
}
