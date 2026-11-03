package admin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"forest/go-api/internal/cliententry"
	"github.com/DATA-DOG/go-sqlmock"
)

func TestClientEntryMatchAllConfirmationSemantics(t *testing.T) {
	limited := []cliententry.Condition{{Field: "ua", Operator: "contains_any", Values: []json.RawMessage{json.RawMessage(`"Forest"`)}}}
	denyAll := []cliententry.Condition{{Field: "email", Operator: "in", Values: []json.RawMessage{}}}
	for _, tc := range []struct {
		name             string
		isNew, confirmed bool
		before, after    []cliententry.Condition
		wantErr          bool
	}{
		{"new_empty", true, false, nil, nil, true},
		{"new_empty_confirmed", true, true, nil, nil, false},
		{"clear_conditions", false, false, limited, nil, true},
		{"clear_conditions_confirmed", false, true, limited, nil, false},
		{"existing_empty", false, false, nil, nil, false},
		{"new_empty_email_is_deny_all", true, false, nil, denyAll, false},
		{"clear_empty_email_is_expansion", false, false, denyAll, nil, true},
		{"limited", true, false, nil, limited, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := requireClientEntryMatchAllConfirmation(tc.isNew, tc.before, tc.after, tc.confirmed)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

func TestDBServiceSaveClientEntryPolicyMatchAllConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name               string
		id                 *int64
		before             string
		confirmed, wantErr bool
	}{
		{"new_requires_confirmation", nil, "", false, true},
		{"new_confirmed", nil, "", true, false},
		{"clearing_requires_confirmation", ptrInt64ForClientEntryPolicyTest(9), `[{"field":"ua","operator":"contains_any","values":["Forest"]}]`, false, true},
		{"clearing_confirmed", ptrInt64ForClientEntryPolicyTest(9), "", true, false},
		{"existing_empty_compatible", ptrInt64ForClientEntryPolicyTest(9), `[]`, false, false},
		{"deny_all_must_not_become_match_all", ptrInt64ForClientEntryPolicyTest(9), `[{"field":"email","operator":"in","values":[]}]`, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			s := &DBService{db: db}
			readyClientEntrySchemaForPolicyTest(s)
			if tc.id != nil || tc.confirmed {
				mock.ExpectBegin()
				mock.ExpectQuery(`SELECT EXISTS \(SELECT 1 FROM "v2_server_vmess" WHERE id = \$1\)`).WithArgs(int64(11)).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
				if tc.id != nil {
					expectClientEntryVisibleOrderLock(mock)
					if !tc.confirmed {
						mock.ExpectQuery(`SELECT conditions FROM v2_client_entry_user_policy WHERE id=\$1 AND mode='standard' FOR UPDATE`).WithArgs(int64(9)).WillReturnRows(sqlmock.NewRows([]string{"conditions"}).AddRow(tc.before))
					}
					if !tc.wantErr {
						mock.ExpectExec(`UPDATE v2_client_entry_user_policy`).WillReturnResult(sqlmock.NewResult(0, 1))
					}
				} else {
					expectNextClientEntryVisibleSort(mock, 0)
					mock.ExpectQuery(`INSERT INTO v2_client_entry_user_policy`).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(9)))
				}
				if tc.wantErr {
					mock.ExpectRollback()
				} else {
					mock.ExpectExec(`DELETE FROM v2_client_entry_user_policy_member`).WillReturnResult(sqlmock.NewResult(0, 0))
					mock.ExpectExec(`INSERT INTO v2_client_entry_user_policy_member`).WillReturnResult(sqlmock.NewResult(1, 1))
					mock.ExpectCommit()
				}
			}
			ok, err := s.SaveClientEntryUserPolicy(context.Background(), ClientEntryUserPolicySaveRequest{ID: tc.id, Name: "fixture", Action: "original", Members: []ClientEntryGroupMemberSaveRequest{{ServerType: "vmess", ServerID: 11}}, ConfirmMatchAll: tc.confirmed})
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "confirm_match_all") || ok {
					t.Fatalf("ok=%v err=%v", ok, err)
				}
			} else if err != nil || !ok {
				t.Fatalf("ok=%v err=%v", ok, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAIEntryRuleMatchAllConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name, patch          string
		emptyBefore, wantErr bool
	}{
		{"clear_requires_confirmation", `{"conditions":[]}`, false, true},
		{"false_is_not_confirmation", `{"conditions":[],"confirm_match_all":false}`, false, true},
		{"explicit_confirmation", `{"conditions":[],"confirm_match_all":true}`, false, false},
		{"existing_empty_compatible", `{"conditions":[]}`, true, false},
		{"keep_empty_email_condition", `{"conditions":[{"field":"email","operator":"in","values":[]}]}`, false, false},
		{"confirmation_alone_rejected", `{"confirm_match_all":true}`, true, true},
		{"non_boolean_rejected", `{"conditions":[],"confirm_match_all":1}`, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			mock.ExpectBegin()
			tx, err := db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			before := AIEntryRuleSnapshot{Kind: "policy", Name: "fixture", Action: "original", Enabled: 1, Members: []AIEntryRuleMember{{ServerType: "vmess", ServerID: 11}}}
			if !tc.emptyBefore {
				before.Conditions = []cliententry.Condition{{Field: "email", Operator: "in", Values: []json.RawMessage{json.RawMessage(`"test@example.test"`)}}}
			}
			if !tc.wantErr {
				mock.ExpectQuery(`SELECT EXISTS \(SELECT 1 FROM "v2_server_vmess" WHERE id = \$1\)`).WithArgs(int64(11)).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
			}
			var changes map[string]json.RawMessage
			if err := json.Unmarshal([]byte(tc.patch), &changes); err != nil {
				t.Fatal(err)
			}
			effective, err := PrepareAIEntryRuleChanges(context.Background(), tx, before, changes)
			if tc.wantErr {
				if !errors.Is(err, ErrAIEntryRuleInvalid) {
					t.Fatalf("err=%v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if _, ok := effective["confirm_match_all"]; ok {
					t.Fatal("confirmation must not become a stored business field")
				}
				if len(effective) != 1 || effective["conditions"] == nil {
					t.Fatalf("effective=%v", effective)
				}
			}
			mock.ExpectRollback()
			tx.Rollback()
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
