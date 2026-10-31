package admin

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jackc/pgx/v5/pgconn"
	"testing"
)

type evidenceTestArrayConverter struct{}

func (evidenceTestArrayConverter) ConvertValue(v any) (driver.Value, error) {
	if ids, ok := v.([]int64); ok {
		return fmt.Sprint(ids), nil
	}
	return driver.DefaultParameterConverter.ConvertValue(v)
}
func evidenceCurrentPolicyRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id", "mode", "conditions", "action", "host", "sort", "type", "server_id"})
}
func evidenceCurrentUserRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id", "email", "plan_id", "created_at"})
}
func evidenceCurrentAssignmentRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"user_id", "policy_id", "group_id", "host", "sort"})
}
func TestEntryEvidenceCurrentCoverageBatchesTenThousandUsers(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.ValueConverterOption(evidenceTestArrayConverter{}))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectBegin()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	members := []AIEntryRuleMember{{ServerType: "vmess", ServerID: 1}, {ServerType: "trojan", ServerID: 2}}
	conditions := "[{\"field\":\"user_id\",\"operator\":\"between\",\"min\":1,\"max\":10000},{\"field\":\"ua\",\"operator\":\"contains_any\",\"values\":[\"Forest\"]}]"
	mock.ExpectQuery("SELECT p.id,p.mode,p.conditions").WillReturnRows(evidenceCurrentPolicyRows().AddRow(5, "standard", conditions, "override", "old.example", 100, "vmess", 1).AddRow(5, "standard", conditions, "override", "old.example", 100, "trojan", 2))
	rows := evidenceCurrentUserRows()
	items := make([]EntryEvidenceCandidate, 10000)
	for i := 1; i <= 10000; i++ {
		rows.AddRow(i, fmt.Sprintf("u%d@example.test", i), 1, 1)
		items[i-1] = EntryEvidenceCandidate{UserID: int64(i), UAs: []string{"Forest/1", "Forest/2"}, Nodes: members}
	}
	mock.ExpectQuery("SELECT id,email,COALESCE").WithArgs(sqlmock.AnyArg()).WillReturnRows(rows)
	mock.ExpectQuery("SELECT user_id FROM v2_support_entry_override").WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"user_id"}))
	mock.ExpectQuery("SELECT a.user_id,a.policy_id,g.id").WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg()).WillReturnRows(evidenceCurrentAssignmentRows())
	mock.ExpectRollback()
	if err := evidenceCurrentCoverage(context.Background(), tx, AIEntryRuleSnapshot{Kind: "policy", PolicyID: 5, TargetID: 5, Members: members}, items); err != nil {
		t.Fatal(err)
	}
	_ = tx.Rollback()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
func TestEntryEvidenceCurrentCoverageFirstMatchAndSplitUASemantics(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		kind                 string
		target, leaf         int64
		ua                   string
		otherBefore, missing bool
		wantErr              bool
	}{
		{name: "standard unchanged", kind: "policy", target: 5, ua: "Forest"},
		{name: "earlier hide blocks", kind: "policy", target: 5, ua: "Forest", otherBefore: true, wantErr: true},
		{name: "current UA no longer matches", kind: "policy", target: 5, ua: "Clash", wantErr: true},
		{name: "deleted user conflicts", kind: "policy", target: 5, ua: "Forest", missing: true, wantErr: true},
		{name: "split drops frozen ID keeps UA", kind: "split_group", target: 99, leaf: 99, ua: "Forest"},
		{name: "split UA mismatched", kind: "split_group", target: 99, leaf: 99, ua: "Clash", wantErr: true},
		{name: "different leaf conflicts", kind: "split_group", target: 99, leaf: 98, ua: "Forest", wantErr: true},
		{name: "unassigned split conflicts", kind: "split_group", target: 99, ua: "Forest", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, e := sqlmock.New(sqlmock.ValueConverterOption(evidenceTestArrayConverter{}))
			if e != nil {
				t.Fatal(e)
			}
			defer db.Close()
			mock.ExpectBegin()
			tx, e := db.Begin()
			if e != nil {
				t.Fatal(e)
			}
			mode := "standard"
			lo, hi := 1, 10
			if tc.kind == "split_group" {
				mode = "split"
				lo, hi = 1000, 2000
			}
			condition := fmt.Sprintf("[{\"field\":\"user_id\",\"operator\":\"between\",\"min\":%d,\"max\":%d},{\"field\":\"ua\",\"operator\":\"contains_any\",\"values\":[\"Forest\"]}]", lo, hi)
			rows := evidenceCurrentPolicyRows().AddRow(5, mode, condition, "override", "target.example", 100, "vmess", 1)
			if tc.otherBefore {
				rows.AddRow(6, "standard", "[]", "hide", "", 50, "vmess", 1)
			}
			mock.ExpectQuery("SELECT p.id,p.mode,p.conditions").WillReturnRows(rows)
			users := evidenceCurrentUserRows()
			if !tc.missing {
				users.AddRow(1, "user@example.test", 1, 1)
			}
			mock.ExpectQuery("SELECT id,email,COALESCE").WithArgs(sqlmock.AnyArg()).WillReturnRows(users)
			mock.ExpectQuery("SELECT user_id FROM v2_support_entry_override").WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"user_id"}))
			assignments := evidenceCurrentAssignmentRows()
			if tc.leaf > 0 {
				assignments.AddRow(1, 5, tc.leaf, "leaf.example", 75)
			}
			mock.ExpectQuery("SELECT a.user_id,a.policy_id,g.id").WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg()).WillReturnRows(assignments)
			mock.ExpectRollback()
			err := evidenceCurrentCoverage(context.Background(), tx, AIEntryRuleSnapshot{Kind: tc.kind, PolicyID: 5, TargetID: tc.target, Members: []AIEntryRuleMember{{ServerType: "vmess", ServerID: 1}}}, []EntryEvidenceCandidate{{UserID: 1, UAs: []string{tc.ua}}})
			if tc.wantErr && !errors.Is(err, ErrEntryEvidenceConflict) || !tc.wantErr && err != nil {
				t.Fatalf("got %v want conflict=%v", err, tc.wantErr)
			}
			_ = tx.Rollback()
			if e := mock.ExpectationsWereMet(); e != nil {
				t.Fatal(e)
			}
		})
	}
}

func TestEntryEvidenceCurrentCoverageRejectsSupportOverrideBeforeOverlay(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.ValueConverterOption(evidenceTestArrayConverter{}))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectBegin()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery("SELECT p.id,p.mode,p.conditions").WillReturnRows(evidenceCurrentPolicyRows())
	mock.ExpectQuery("SELECT id,email,COALESCE").WithArgs(sqlmock.AnyArg()).WillReturnRows(evidenceCurrentUserRows().AddRow(12, "u@example.test", 1, 1))
	mock.ExpectQuery("SELECT user_id FROM v2_support_entry_override").WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"user_id"}).AddRow(12))
	mock.ExpectRollback()
	err = evidenceCurrentCoverage(context.Background(), tx, AIEntryRuleSnapshot{}, []EntryEvidenceCandidate{{UserID: 12}})
	if !errors.Is(err, ErrEntryEvidenceConflict) {
		t.Fatalf("active support override was accepted: %v", err)
	}
	_ = tx.Rollback()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestEntryEvidenceUserLockConflictIsReported(t *testing.T) {
	err := evidenceUserLockError(&pgconn.PgError{Code: "55P03"})
	if !errors.Is(err, ErrEntryEvidenceConflict) {
		t.Fatal(err)
	}
	original := errors.New("unrelated failure")
	if evidenceUserLockError(original) != original {
		t.Fatal("unrelated error changed")
	}
}
