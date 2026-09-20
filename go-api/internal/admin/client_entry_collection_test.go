package admin

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func expectClientEntryCollectionOrderLock(mock sqlmock.Sqlmock) {
	mock.ExpectExec(`SELECT pg_advisory_xact_lock\(\$1\)`).WithArgs(clientEntryVisibleOrderLockKey).WillReturnResult(sqlmock.NewResult(0, 1))
}
func expectDetachClientEntryCollectionMembers(mock sqlmock.Sqlmock) {
	mock.ExpectExec(`(?s)WITH removed AS .*DELETE FROM v2_client_entry_collection_member.*UPDATE v2_client_entry_collection SET version=version\+1`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`DELETE FROM v2_client_entry_collection c WHERE NOT EXISTS`).WillReturnResult(sqlmock.NewResult(0, 0))
}

func TestNormalizeClientEntryCollectionSaveRequest(t *testing.T) {
	base := ClientEntryCollectionSaveRequest{Name: " test ", EntryHost: "ENTRY.Example.COM", Items: []ClientEntryCollectionItem{{Kind: "policy", ID: 1}, {Kind: "split_group", ID: 2}}}
	got, err := normalizeClientEntryCollectionSaveRequest(base)
	if err != nil || got.Name != "test" || got.EntryHost != "entry.example.com" {
		t.Fatalf("normalize: %#v %v", got, err)
	}
	for _, tc := range []struct {
		name string
		edit func(*ClientEntryCollectionSaveRequest)
	}{
		{"one member create", func(r *ClientEntryCollectionSaveRequest) { r.Items = r.Items[:1] }},
		{"bad resolve", func(r *ClientEntryCollectionSaveRequest) { r.ResolveEntryHost = 2 }},
		{"blank host", func(r *ClientEntryCollectionSaveRequest) { r.EntryHost = "" }},
		{"duplicate", func(r *ClientEntryCollectionSaveRequest) {
			r.Items = []ClientEntryCollectionItem{{Kind: "policy", ID: 1}, {Kind: "policy", ID: 1}}
		}},
		{"bad kind", func(r *ClientEntryCollectionSaveRequest) {
			r.Items = []ClientEntryCollectionItem{{Kind: "other", ID: 1}, {Kind: "policy", ID: 2}}
		}},
		{"version missing", func(r *ClientEntryCollectionSaveRequest) { r.ID = 1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := base
			tc.edit(&r)
			if _, err := normalizeClientEntryCollectionSaveRequest(r); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
	base.ID = 1
	base.Version = 1
	base.Items = base.Items[:1]
	if _, err := normalizeClientEntryCollectionSaveRequest(base); err != nil {
		t.Fatal(err)
	}
}

func TestSaveClientEntryCollectionSharesHostWithoutReordering(t *testing.T) {
	db, mock, _ := sqlmock.New()
	defer db.Close()
	s := &DBService{db: db}
	readyClientEntrySchemaForPolicyTest(s)
	mock.ExpectBegin()
	expectClientEntryCollectionOrderLock(mock)
	mock.ExpectQuery(`(?s)SELECT m.collection_id FROM v2_client_entry_user_policy p.*p.mode='standard' AND p.action='override'.*FOR UPDATE OF p`).WithArgs(int64(1)).WillReturnRows(sqlmock.NewRows([]string{"collection_id"}).AddRow(nil))
	mock.ExpectQuery(`(?s)SELECT m.collection_id FROM v2_client_entry_user_policy_split_group g.*NOT EXISTS.*FOR UPDATE OF g`).WithArgs(int64(2)).WillReturnRows(sqlmock.NewRows([]string{"collection_id"}).AddRow(nil))
	mock.ExpectQuery(`INSERT INTO v2_client_entry_collection`).WithArgs("Shared", "entry.example.com", int64(0), sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(4))
	mock.ExpectExec(`DELETE FROM v2_client_entry_collection_member WHERE collection_id=\$1`).WithArgs(int64(4)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`INSERT INTO v2_client_entry_collection_member`).WithArgs(int64(4), int64(1), nil).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE v2_client_entry_user_policy SET entry_host=\$2,resolve_entry_host=\$3,updated_at=\$4 WHERE id=\$1`).WithArgs(int64(1), "entry.example.com", int64(0), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO v2_client_entry_collection_member`).WithArgs(int64(4), nil, int64(2)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE v2_client_entry_user_policy_split_group SET entry_host=\$2,resolve_entry_host=\$3,updated_at=\$4 WHERE id=\$1`).WithArgs(int64(2), "entry.example.com", int64(0), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	got, err := s.SaveClientEntryCollection(context.Background(), ClientEntryCollectionSaveRequest{Name: "Shared", EntryHost: "entry.example.com", Items: []ClientEntryCollectionItem{{Kind: "policy", ID: 1}, {Kind: "split_group", ID: 2}}})
	if err != nil || got.ID != 4 || got.Version != 1 || len(got.Items) != 2 {
		t.Fatalf("save: %#v %v", got, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSaveClientEntryCollectionRejectsStaleVersionAndOwnedMember(t *testing.T) {
	for _, tc := range []struct {
		name  string
		stale bool
	}{{"stale", true}, {"owned", false}} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, _ := sqlmock.New()
			defer db.Close()
			s := &DBService{db: db}
			readyClientEntrySchemaForPolicyTest(s)
			mock.ExpectBegin()
			expectClientEntryCollectionOrderLock(mock)
			req := ClientEntryCollectionSaveRequest{Name: "Shared", EntryHost: "entry.example.com", Items: []ClientEntryCollectionItem{{Kind: "policy", ID: 1}, {Kind: "policy", ID: 2}}}
			if tc.stale {
				req.ID = 4
				req.Version = 1
				mock.ExpectQuery(`SELECT version FROM v2_client_entry_collection`).WithArgs(int64(4)).WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow(2))
			} else {
				mock.ExpectQuery(`SELECT m.collection_id`).WithArgs(int64(1)).WillReturnRows(sqlmock.NewRows([]string{"collection_id"}).AddRow(8))
			}
			mock.ExpectRollback()
			_, err := s.SaveClientEntryCollection(context.Background(), req)
			if err == nil {
				t.Fatal("expected rejection")
			}
			if tc.stale && !errors.Is(err, errClientEntryCollectionChanged) {
				t.Fatal(err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSaveClientEntryCollectionRejectsNonLeaf(t *testing.T) {
	db, mock, _ := sqlmock.New()
	defer db.Close()
	s := &DBService{db: db}
	readyClientEntrySchemaForPolicyTest(s)
	mock.ExpectBegin()
	expectClientEntryCollectionOrderLock(mock)
	mock.ExpectQuery(`SELECT m.collection_id`).WithArgs(int64(1)).WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()
	_, err := s.SaveClientEntryCollection(context.Background(), ClientEntryCollectionSaveRequest{Name: "Shared", EntryHost: "entry.example.com", Items: []ClientEntryCollectionItem{{Kind: "split_group", ID: 1}, {Kind: "split_group", ID: 2}}})
	if err == nil || !strings.Contains(err.Error(), "当前固定名单") {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteClientEntryCollectionLeavesUnderlyingRulesUntouched(t *testing.T) {
	db, mock, _ := sqlmock.New()
	defer db.Close()
	s := &DBService{db: db}
	readyClientEntrySchemaForPolicyTest(s)
	mock.ExpectBegin()
	expectClientEntryCollectionOrderLock(mock)
	mock.ExpectQuery(`SELECT version FROM v2_client_entry_collection`).WithArgs(int64(4)).WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow(2))
	mock.ExpectExec(`DELETE FROM v2_client_entry_collection WHERE id=\$1`).WithArgs(int64(4)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	ok, err := s.DeleteClientEntryCollection(context.Background(), 4, 2)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestListClientEntryCollectionsReturnsMixedMembers(t *testing.T) {
	db, mock, _ := sqlmock.New()
	defer db.Close()
	s := &DBService{db: db}
	readyClientEntrySchemaForPolicyTest(s)
	mock.ExpectQuery(`(?s)SELECT c.id, c.name, c.entry_host, c.resolve_entry_host, c.version, m.policy_id, m.split_group_id.*LEFT JOIN`).WillReturnRows(sqlmock.NewRows([]string{"id", "name", "entry_host", "resolve_entry_host", "version", "policy_id", "split_group_id"}).AddRow(4, "Shared", "entry.example.com", 1, 2, 1, nil).AddRow(4, "Shared", "entry.example.com", 1, 2, nil, 3))
	got, err := s.ListClientEntryCollections(context.Background())
	if err != nil || len(got) != 1 || len(got[0].Items) != 2 || got[0].ResolveEntryHost == nil || *got[0].ResolveEntryHost != 1 {
		t.Fatalf("%#v %v", got, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSaveClientEntryCollectionRollsBackPartialHostUpdate(t *testing.T) {
	db, mock, _ := sqlmock.New()
	defer db.Close()
	s := &DBService{db: db}
	readyClientEntrySchemaForPolicyTest(s)
	mock.ExpectBegin()
	expectClientEntryCollectionOrderLock(mock)
	mock.ExpectQuery(`SELECT m.collection_id`).WithArgs(int64(1)).WillReturnRows(sqlmock.NewRows([]string{"collection_id"}).AddRow(nil))
	mock.ExpectQuery(`SELECT m.collection_id`).WithArgs(int64(2)).WillReturnRows(sqlmock.NewRows([]string{"collection_id"}).AddRow(nil))
	mock.ExpectQuery(`INSERT INTO v2_client_entry_collection`).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(4))
	mock.ExpectExec(`DELETE FROM v2_client_entry_collection_member`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`INSERT INTO v2_client_entry_collection_member`).WithArgs(int64(4), int64(1), nil).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE v2_client_entry_user_policy SET entry_host`).WithArgs(int64(1), "entry.example.com", int64(0), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO v2_client_entry_collection_member`).WithArgs(int64(4), nil, int64(2)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE v2_client_entry_user_policy_split_group SET entry_host`).WithArgs(int64(2), "entry.example.com", int64(0), sqlmock.AnyArg()).WillReturnError(errors.New("simulated write failure"))
	mock.ExpectRollback()
	_, err := s.SaveClientEntryCollection(context.Background(), ClientEntryCollectionSaveRequest{Name: "Shared", EntryHost: "entry.example.com", Items: []ClientEntryCollectionItem{{Kind: "policy", ID: 1}, {Kind: "split_group", ID: 2}}})
	if err == nil || !strings.Contains(err.Error(), "simulated write failure") {
		t.Fatalf("expected failure: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
