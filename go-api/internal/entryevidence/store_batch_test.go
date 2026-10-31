package entryevidence

import (
	"context"
	"errors"
	"github.com/DATA-DOG/go-sqlmock"
	"testing"
	"time"
)

func TestEntryEvidenceStoreBatchesNodesAndRollsBackAsUnit(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit", true: "rollback"}[fail], func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			store := NewStore(db)
			now := time.Now().Unix()
			req := Request{ID: "batch-test", UserID: 1, RequestedAt: now, CompletedAt: now, Transport: "subscription", Status: 200, ResponseComplete: true, Coverage: "complete"}
			nodes := make([]Node, 501)
			for i := range nodes {
				nodes[i] = Node{ServerType: "vmess", ServerID: int64(i + 1), Source: "default", SourceHost: "example.test", DeliveredHost: "example.test", Port: "443"}
			}
			mock.ExpectBegin()
			mock.ExpectExec("INSERT INTO v2_entry_delivery_request").WillReturnResult(sqlmock.NewResult(1, 1))
			mock.ExpectExec("INSERT INTO v2_entry_delivery_node.*\\$3500\\)").WillReturnResult(sqlmock.NewResult(0, 250))
			if fail {
				mock.ExpectExec("INSERT INTO v2_entry_delivery_node.*\\$3500\\)").WillReturnError(errors.New("failure"))
				mock.ExpectRollback()
			} else {
				mock.ExpectExec("INSERT INTO v2_entry_delivery_node.*\\$3500\\)").WillReturnResult(sqlmock.NewResult(0, 250))
				mock.ExpectExec("INSERT INTO v2_entry_delivery_node.*\\$14\\)").WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectCommit()
			}
			err = store.Record(context.Background(), req, nodes)
			if fail != (err != nil) {
				t.Fatalf("err=%v", err)
			}
			if fail && store.failures.Load() != 1 {
				t.Fatal("failure not counted")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestEntryEvidenceStoreRejectsInvalidEndpointFilters(t *testing.T) {
	s := NewStore(nil)
	for _, filter := range []Filter{{Host: "https://example.test?token=secret"}, {Port: "password=secret"}, {Host: " "}} {
		if _, err := s.List(context.Background(), filter); !errors.Is(err, ErrValidation) {
			t.Fatal(filter, err)
		}
	}
}
