package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"forest/go-api/internal/cliententry"
	"github.com/DATA-DOG/go-sqlmock"
)

func guardSearchTestSnapshot() *SubscribeGuardSearchSnapshot {
	return &SubscribeGuardSearchSnapshot{Users: map[int64]SubscribeGuardSearchUser{1: {ID: 1, Email: "one@example.com", PlanID: 2, CreatedAt: sql.NullInt64{Int64: 100, Valid: true}}}, Assignments: map[int64]map[int64]int64{}, EvaluatedAt: 200000}
}
func guardSearchTestPolicy(id, rank int64, members ...int64) ClientEntryUserPolicyRecord {
	p := ClientEntryUserPolicyRecord{ID: id, Name: "policy", Sort: rank, Enabled: 1, Mode: ClientEntryUserPolicyModeStandard, Action: cliententry.ActionOverride, EntryHost: "entry.example.com"}
	for _, member := range members {
		p.Members = append(p.Members, ClientEntryGroupMemberRecord{ServerType: "vmess", ServerID: member})
	}
	return p
}
func guardSearchTestUA() cliententry.Condition {
	return cliententry.Condition{Field: "ua", Operator: "contains_any", Values: []json.RawMessage{json.RawMessage(`"Forest"`)}}
}
func TestSubscribeGuardSearchMatchPerNodePrecedence(t *testing.T) {
	snapshot := guardSearchTestSnapshot()
	hide := guardSearchTestPolicy(1, 10, 1)
	hide.Action = cliententry.ActionHide
	original := guardSearchTestPolicy(2, 20, 1, 2)
	original.Action = cliententry.ActionOriginal
	unreachable := guardSearchTestPolicy(3, 30, 1, 2)
	disabled := guardSearchTestPolicy(4, 0, 3)
	disabled.Enabled = 0
	snapshot.Policies = []ClientEntryUserPolicyRecord{unreachable, original, disabled, hide}
	matches, unknown := snapshot.Match(1, "Forest/1", true)
	ids := []int64{}
	for _, match := range matches {
		ids = append(ids, match.ID)
	}
	if unknown || !reflect.DeepEqual(ids, []int64{1, 2}) || matches[0].Action != "hide" || matches[1].Action != "original" {
		t.Fatalf("matches=%#v unknown=%v", matches, unknown)
	}
}
func TestSubscribeGuardSearchMatchFixedAssignmentAndGlobalSort(t *testing.T) {
	snapshot := guardSearchTestSnapshot()
	split := guardSearchTestPolicy(9, 100, 1)
	split.Mode = ClientEntryUserPolicyModeSplit
	priority := int64(1)
	split.SplitGroups = []ClientEntryUserPolicySplitGroupRecord{{ID: 71, PolicyID: 9, Name: "leaf", IsLeaf: true, EntryHost: "leaf.example.com", GlobalSort: &priority}}
	snapshot.Policies = []ClientEntryUserPolicyRecord{guardSearchTestPolicy(2, 20, 1), split}
	snapshot.Assignments[1] = map[int64]int64{9: 71}
	before := snapshot.Assignments[1][9]
	matches, _ := snapshot.Match(1, "Forest", true)
	if len(matches) != 1 || matches[0].ID != 9 || matches[0].Name != "policy" || matches[0].GroupID != 71 || matches[0].GroupName != "leaf" || matches[0].EntryHost != "leaf.example.com" || snapshot.Assignments[1][9] != before {
		t.Fatalf("matches=%#v", matches)
	}
	for _, invalid := range []ClientEntryUserPolicySplitGroupRecord{{ID: 71, IsLeaf: false, EntryHost: "leaf.example.com"}, {ID: 71, IsLeaf: true, EntryHost: ""}, {ID: 72, IsLeaf: true, EntryHost: "leaf.example.com"}} {
		snapshot.Policies[1].SplitGroups = []ClientEntryUserPolicySplitGroupRecord{invalid}
		matches, _ = snapshot.Match(1, "Forest", true)
		if len(matches) != 1 || matches[0].ID != 2 {
			t.Fatalf("invalid group should not win: %#v", matches)
		}
	}
	delete(snapshot.Assignments, 1)
	matches, _ = snapshot.Match(1, "Forest", true)
	if len(matches) != 1 || matches[0].ID != 2 || len(snapshot.Assignments) != 0 {
		t.Fatal("query invented an assignment")
	}
}
func TestSubscribeGuardSearchMissingUAIsUnknownNotEmpty(t *testing.T) {
	snapshot := guardSearchTestSnapshot()
	first := guardSearchTestPolicy(1, 10, 1)
	first.Conditions = []cliententry.Condition{guardSearchTestUA()}
	snapshot.Policies = []ClientEntryUserPolicyRecord{first, guardSearchTestPolicy(2, 20, 1, 2)}
	matches, unknown := snapshot.Match(1, "", false)
	if !unknown || len(matches) != 1 || matches[0].ID != 2 {
		t.Fatalf("second node's certain winner lost: %#v unknown %v", matches, unknown)
	}
	snapshot.Policies[1].Members = snapshot.Policies[1].Members[:1]
	matches, unknown = snapshot.Match(1, "", false)
	if !unknown || len(matches) != 0 {
		t.Fatalf("UA shadow should be unknown, got %#v", matches)
	}
	matches, unknown = snapshot.Match(1, "", true)
	if unknown || len(matches) != 1 || matches[0].ID != 2 {
		t.Fatal("actual empty UA should be evaluated normally")
	}
	matches, _ = snapshot.Match(1, "Forest/1", true)
	if len(matches) != 1 || matches[0].ID != 1 {
		t.Fatal("actual UA was ignored")
	}
}
func TestSubscribeGuardSearchNoUAConditionWithOtherConditionsFalseDoesNotShadow(t *testing.T) {
	snapshot := guardSearchTestSnapshot()
	first := guardSearchTestPolicy(1, 10, 1)
	first.Conditions = []cliententry.Condition{guardSearchTestUA(), {Field: "plan_id", Operator: "eq", Value: json.RawMessage(`99`)}}
	snapshot.Policies = []ClientEntryUserPolicyRecord{first, guardSearchTestPolicy(2, 20, 1)}
	matches, unknown := snapshot.Match(1, "", false)
	if unknown || len(matches) != 1 || matches[0].ID != 2 {
		t.Fatalf("nonmatching UA policy must not shadow %#v", matches)
	}
}
func TestSubscribeGuardSearchLoadIsReadOnlyAndTokenPrivate(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service := &DBService{db: db}
	// No readyClientEntrySchema call: an unexpected schema/assignment write fails.
	mock.ExpectQuery(`SELECT p.id, p.name, p.sort, p.mode`).WillReturnRows(sqlmock.NewRows([]string{"id", "name", "sort", "mode", "snapshot_from", "snapshot_to", "action", "conditions", "entry_host", "resolve_entry_host", "extra_nodes", "extra_nodes_position", "enabled", "remarks", "created_at", "updated_at"}))
	mock.ExpectQuery(`SELECT u.id, u.email, u.token.*STRPOS\(LOWER\(u.email\), \$1\) > 0 AND u.id IN \(\$2,\$3\)`).WithArgs("example", int64(1), int64(2)).WillReturnRows(sqlmock.NewRows([]string{"id", "email", "token", "plan_id", "created_at", "banned", "u", "d", "transfer_enable", "expired_at", "name"}).AddRow(1, "one@example.com", "secret-token", 2, 100, 0, 1, 2, 1000, nil, "plan"))
	result, err := service.LoadSubscribeGuardSearchSnapshot(context.Background(), SubscribeGuardSearchLoadRequest{Account: "example", UserIDs: []int64{1, 2}})
	if err != nil {
		t.Fatal(err)
	}
	if result.UserIDsByToken["secret-token"] != 1 || len(result.Users) != 1 {
		t.Fatalf("result=%#v", result)
	}
	raw, _ := json.Marshal(result.Users[1].Public)
	if strings.Contains(string(raw), "secret-token") {
		t.Fatal("public row leaks token")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
func TestSubscribeGuardSearchLoadLimitsAndContext(t *testing.T) {
	service := &DBService{}
	_, err := service.LoadSubscribeGuardSearchSnapshot(context.Background(), SubscribeGuardSearchLoadRequest{})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service.db = db
	_, err = service.LoadSubscribeGuardSearchSnapshot(context.Background(), SubscribeGuardSearchLoadRequest{UserIDs: make([]int64, SubscribeGuardSearchMaxUsers*2+1)})
	if !errors.Is(err, ErrSubscribeGuardSearchLimit) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = service.LoadSubscribeGuardSearchSnapshot(ctx, SubscribeGuardSearchLoadRequest{})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestSubscribeGuardSearchAccountSQLMatchesHTTPContract(t *testing.T) {
	for _, test := range []struct {
		account, mode, where string
		value                any
	}{{"123", "contains", `u.id = \$1`, int64(123)}, {"+123", "contains", `STRPOS\(LOWER\(u.email\), \$1\) > 0`, "+123"}, {"A_%@Example.com", "exact", `LOWER\(u.email\) = \$1`, "a_%@example.com"}} {
		t.Run(test.account, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			service := &DBService{db: db}
			mock.ExpectQuery(`SELECT u.id, u.email, u.token.* WHERE ` + test.where).WithArgs(test.value).WillReturnRows(sqlmock.NewRows([]string{"id", "email", "token", "plan_id", "created_at", "banned", "u", "d", "transfer_enable", "expired_at", "name"}))
			err = service.loadSubscribeGuardSearchUsers(context.Background(), SubscribeGuardSearchLoadRequest{Account: test.account, AccountMatch: test.mode}, nil, nil, guardSearchTestSnapshot())
			if err != nil {
				t.Fatal(err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSubscribeGuardSearchLoadBatchAssignmentsNoWrites(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service := &DBService{db: db}
	mock.ExpectQuery(`SELECT p.id, p.name, p.sort, p.mode`).WillReturnRows(sqlmock.NewRows([]string{"id", "name", "sort", "mode", "snapshot_from", "snapshot_to", "action", "conditions", "entry_host", "resolve_entry_host", "extra_nodes", "extra_nodes_position", "enabled", "remarks", "created_at", "updated_at"}).AddRow(9, "fixed", 10, "split", nil, nil, "override", `[]`, "", 0, `[]`, "after", 1, "", 1, 2))
	mock.ExpectQuery(`SELECT policy_id, server_type, server_id, sort`).WithArgs(int64(9)).WillReturnRows(sqlmock.NewRows([]string{"policy_id", "server_type", "server_id", "sort"}).AddRow(9, "vmess", 1, 1))
	mock.ExpectQuery(`(?s)SELECT split_group.id, split_group.policy_id.*WHERE split_group.policy_id IN \(\$1\)`).WithArgs(int64(9)).WillReturnRows(sqlmock.NewRows([]string{"id", "policy_id", "parent_id", "name", "path", "entry_host", "sort", "global_sort", "user_count", "is_leaf", "created_at", "updated_at", "resolve_entry_host"}).AddRow(71, 9, nil, "leaf", "leaf", "leaf.example.com", 1, nil, 1, true, 1, 2, nil))
	mock.ExpectQuery(`SELECT u.id, u.email, u.token.*u.id IN \(\$1,\$2\)`).WithArgs(int64(1), int64(2)).WillReturnRows(sqlmock.NewRows([]string{"id", "email", "token", "plan_id", "created_at", "banned", "u", "d", "transfer_enable", "expired_at", "name"}).AddRow(1, "one@example.com", "t1", nil, 1, 0, 0, 0, 1, nil, nil).AddRow(2, "two@example.com", "t2", nil, 1, 0, 0, 0, 1, nil, nil))
	mock.ExpectQuery(`SELECT user_id, policy_id, group_id FROM v2_client_entry_user_policy_split_assignment WHERE user_id IN \(\$1,\$2\)`).WithArgs(int64(1), int64(2)).WillReturnRows(sqlmock.NewRows([]string{"user_id", "policy_id", "group_id"}).AddRow(1, 9, 71))
	result, err := service.LoadSubscribeGuardSearchSnapshot(context.Background(), SubscribeGuardSearchLoadRequest{UserIDs: []int64{1, 2}})
	if err != nil {
		t.Fatal(err)
	}
	matches, _ := result.Match(1, "Forest", true)
	missing, _ := result.Match(2, "Forest", true)
	if len(matches) != 1 || matches[0].GroupID != 71 || len(missing) != 0 || len(result.Assignments) != 1 {
		t.Fatalf("assignment mismatch %v %v", matches, missing)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
