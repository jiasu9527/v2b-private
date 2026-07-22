package postgres

import (
	"context"
	"strings"
	"testing"

	"forest/go-api/internal/cliententry"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestMigrateLegacyServerHostEntryRulesReturnsStoredReport(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock\(\$1\)`).
		WithArgs(legacyEntryHostMigrationLockKey).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CREATE TABLE IF NOT EXISTS v2_client_entry_rule_migration`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT report FROM v2_client_entry_rule_migration WHERE migration_key = \$1`).
		WithArgs(legacyEntryHostMigrationKey).
		WillReturnRows(sqlmock.NewRows([]string{"report"}).AddRow(`{"migration_key":"legacy-server-host-dsl-to-entry-rules-v1","applied_at":123,"rules_created":4}`))
	mock.ExpectCommit()

	report, err := MigrateLegacyServerHostEntryRules(context.Background(), db)
	if err != nil {
		t.Fatalf("read stored migration marker: %v", err)
	}
	if !report.AlreadyApplied || report.AppliedAt != 123 || report.RulesCreated != 4 {
		t.Fatalf("unexpected stored report: %#v", report)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestLoadLegacyEntryEmailPolicyPlansSkipsMissingRetiredTable(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}
	mock.ExpectQuery(`SELECT to_regclass\(\$1\) IS NOT NULL`).
		WithArgs("v2_client_entry_user_policy_user").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))

	plans, issues, err := loadLegacyEntryEmailPolicyPlans(context.Background(), tx)
	if err != nil {
		t.Fatalf("skip missing legacy email table: %v", err)
	}
	if len(plans) != 0 || len(issues) != 0 {
		t.Fatalf("expected no plans/issues without retired table, got %#v / %#v", plans, issues)
	}
	mock.ExpectRollback()
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestLoadLegacyEntryEmailPolicyPlansReportsNestedLegacyDSL(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}
	mock.ExpectQuery(`SELECT to_regclass\(\$1\) IS NOT NULL`).
		WithArgs("v2_client_entry_user_policy_user").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(`SELECT pu.policy_id, p.entry_host, trim\(pu.email\)`).
		WillReturnRows(sqlmock.NewRows([]string{"policy_id", "entry_host", "email"}).
			AddRow(int64(7), "vip.example.com", "VIP@example.com").
			AddRow(int64(8), "new.example.com(D<=30)", "new@example.com"))

	plans, issues, err := loadLegacyEntryEmailPolicyPlans(context.Background(), tx)
	if err != nil {
		t.Fatalf("load legacy email policies: %v", err)
	}
	if len(plans) != 2 || plans[0].PolicyID != 7 || len(plans[0].Emails) != 1 || plans[0].Emails[0] != "vip@example.com" {
		t.Fatalf("unexpected converted email plans: %#v", plans)
	}
	if len(issues) != 1 || issues[0].PolicyID != 8 || issues[0].Source != "policy_entry_host" {
		t.Fatalf("expected nested legacy DSL report, got %#v", issues)
	}
	mock.ExpectRollback()
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestParseLegacyEntryHostPreservesConditionalOrder(t *testing.T) {
	parsed, err := parseLegacyEntryHost("default.example.com,clash.example.com(UClash|UMihomo),new.example.com(D<=30),vip.example.com(1000-2000)")
	if err != nil {
		t.Fatalf("parse legacy host: %v", err)
	}
	if !parsed.NeedsMigration {
		t.Fatal("expected comma/condition host to require migration")
	}
	if parsed.DefaultHost != "default.example.com" {
		t.Fatalf("unexpected fallback host %q", parsed.DefaultHost)
	}
	if len(parsed.Rules) != 3 {
		t.Fatalf("expected 3 converted rules, got %#v", parsed.Rules)
	}

	cases := []struct {
		name string
		subj cliententry.Subject
		want string
	}{
		{
			name: "ua condition wins before subsequent conditions",
			subj: cliententry.Subject{UserID: 42, RegistrationDays: 90, PlanID: 1, UA: "ClashMeta/1.0"},
			want: "clash.example.com",
		},
		{
			name: "registration-day condition",
			subj: cliententry.Subject{UserID: 42, RegistrationDays: 30, PlanID: 1, UA: "Mozilla/5.0"},
			want: "new.example.com",
		},
		{
			name: "user id condition",
			subj: cliententry.Subject{UserID: 1500, RegistrationDays: 90, PlanID: 1, UA: "Mozilla/5.0"},
			want: "vip.example.com",
		},
		{
			name: "ordinary fallback",
			subj: cliententry.Subject{UserID: 42, RegistrationDays: 90, PlanID: 1, UA: "Mozilla/5.0"},
			want: "default.example.com",
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got, visible := resolvedParsedLegacyHost(t, parsed, tt.subj)
			if !visible || got != tt.want {
				t.Fatalf("resolved host = (%q, %v), want (%q, true)", got, visible, tt.want)
			}
		})
	}
}

func TestParseLegacyEntryHostConvertsNoFallbackToHideRule(t *testing.T) {
	parsed, err := parseLegacyEntryHost("locked.example.com(P2-3)")
	if err != nil {
		t.Fatalf("parse legacy host: %v", err)
	}
	if !parsed.HideWhenUnmatched {
		t.Fatal("expected an explicit unmatched hide rule")
	}
	if parsed.DefaultHost != "locked.example.com" {
		t.Fatalf("unexpected retained node host %q", parsed.DefaultHost)
	}

	got, visible := resolvedParsedLegacyHost(t, parsed, cliententry.Subject{UserID: 1, PlanID: 2})
	if !visible || got != "locked.example.com" {
		t.Fatalf("matching plan resolved host = (%q, %v)", got, visible)
	}
	got, visible = resolvedParsedLegacyHost(t, parsed, cliententry.Subject{UserID: 1, PlanID: 1})
	if visible || got != "" {
		t.Fatalf("non-matching plan must be hidden, got (%q, %v)", got, visible)
	}
}

func TestParseLegacyEntryHostSplitsMixedEmptyAndContainsUA(t *testing.T) {
	parsed, err := parseLegacyEntryHost("fallback.example.com,target.example.com(UNoUA|UClash|U!Bad)")
	if err != nil {
		t.Fatalf("parse legacy host: %v", err)
	}
	if len(parsed.Rules) != 2 {
		t.Fatalf("expected empty-UA and contains-UA branches, got %#v", parsed.Rules)
	}

	cases := []struct {
		name string
		ua   string
		want string
	}{
		{name: "empty ua", ua: "", want: "target.example.com"},
		{name: "matching ua", ua: "ClashMeta/1.0", want: "target.example.com"},
		{name: "excluded ua vetoes target", ua: "Bad ClashMeta/1.0", want: "fallback.example.com"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got, visible := resolvedParsedLegacyHost(t, parsed, cliententry.Subject{UserID: 1, UA: tt.ua})
			if !visible || got != tt.want {
				t.Fatalf("resolved host = (%q, %v), want (%q, true)", got, visible, tt.want)
			}
		})
	}
}

func TestParseLegacyEntryHostRewritesTrailingDelimiter(t *testing.T) {
	parsed, err := parseLegacyEntryHost("entry.example.com,")
	if err != nil {
		t.Fatalf("parse legacy host: %v", err)
	}
	if !parsed.NeedsMigration {
		t.Fatal("expected trailing comma to be normalized during migration")
	}
	if parsed.DefaultHost != "entry.example.com" || len(parsed.Rules) != 0 || parsed.HideWhenUnmatched {
		t.Fatalf("unexpected trailing-comma result: %#v", parsed)
	}
}

func TestParseLegacyEntryHostRewritesLegacyWhitespace(t *testing.T) {
	parsed, err := parseLegacyEntryHost("  entry.example.com\t")
	if err != nil {
		t.Fatalf("parse legacy host: %v", err)
	}
	if !parsed.NeedsMigration || parsed.DefaultHost != "entry.example.com" {
		t.Fatalf("expected whitespace normalization, got %#v", parsed)
	}
}

func TestParseLegacyEntryHostRejectsInvalidRange(t *testing.T) {
	_, err := parseLegacyEntryHost("invalid.example.com(P5-2)")
	if err == nil || !strings.Contains(err.Error(), "起始值") {
		t.Fatalf("expected an invalid range error, got %v", err)
	}
}

func TestParseLegacyEntryHostRejectsValuesInvalidUnderNewRuleContract(t *testing.T) {
	cases := []struct {
		name string
		host string
	}{
		{name: "scheme is not a host", host: "https://entry.example.com(P1-2)"},
		{name: "zero cannot be a user id", host: "range.example.com(0-10)"},
		{name: "empty node host", host: "  \t"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseLegacyEntryHost(tt.host)
			if err == nil {
				t.Fatalf("expected migration validation error for %q", tt.host)
			}
		})
	}
}

func TestGroupLegacyEntryHostRulesCombinesIdenticalRulesAcrossNodes(t *testing.T) {
	clash := legacyEntryHostRule{Action: "override", EntryHost: "clash.example.com", Conditions: []LegacyEntryRuleCondition{{Field: "ua", Operator: "contains_any", Values: []string{"Clash"}}}}
	newUser := legacyEntryHostRule{Action: "override", EntryHost: "new.example.com", Conditions: []LegacyEntryRuleCondition{{Field: "registration_days", Operator: "lte", Value: int64(30)}}}
	plans := []legacyEntryHostNodePlan{
		{Source: legacyEntryHostServerSource{ServerType: "vmess"}, ServerID: 1, OriginalHost: "default.example.com,clash.example.com(UClash),new.example.com(D<=30)", Parsed: legacyEntryHostParseResult{Rules: []legacyEntryHostRule{clash, newUser}}},
		{Source: legacyEntryHostServerSource{ServerType: "trojan"}, ServerID: 2, OriginalHost: "default.example.com,clash.example.com(UClash),new.example.com(D<=30)", Parsed: legacyEntryHostParseResult{Rules: []legacyEntryHostRule{clash, newUser}}},
		{Source: legacyEntryHostServerSource{ServerType: "vless"}, ServerID: 3, OriginalHost: "fallback.example.com,clash.example.com(UClash)", Parsed: legacyEntryHostParseResult{Rules: []legacyEntryHostRule{clash}}},
	}

	groups, err := groupLegacyEntryHostRules(plans)
	if err != nil {
		t.Fatalf("group rules: %v", err)
	}
	if len(groups) != 2 {
		t.Fatalf("expected 2 groups, got %#v", groups)
	}
	if len(groups[0].Members) != 3 || len(groups[1].Members) != 2 {
		t.Fatalf("identical rules must combine all selected nodes: %#v", groups)
	}
	if groups[0].Sequence != 1 || groups[1].Sequence != 2 {
		t.Fatalf("rule order must remain stable: %#v", groups)
	}
}

func resolvedParsedLegacyHost(t *testing.T, parsed legacyEntryHostParseResult, subject cliententry.Subject) (string, bool) {
	t.Helper()
	for _, rule := range parsed.Rules {
		encoded, err := marshalLegacyEntryConditions(rule.Conditions)
		if err != nil {
			t.Fatalf("encode migration conditions: %v", err)
		}
		conditions, err := cliententry.DecodeConditions(encoded)
		if err != nil {
			t.Fatalf("decode migration conditions %s: %v", encoded, err)
		}
		if !cliententry.MatchAll(conditions, subject) {
			continue
		}
		if rule.Action == "hide" {
			return "", false
		}
		return rule.EntryHost, true
	}
	if parsed.HideWhenUnmatched {
		return "", false
	}
	return parsed.DefaultHost, strings.TrimSpace(parsed.DefaultHost) != ""
}
