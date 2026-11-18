package nodeapi

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestCompactObservationIDIsLosslessForEveryAcceptedFormat(t *testing.T) {
	cases := []struct {
		id       string
		format   byte
		wantSize int
	}{
		{"0123456789abcdef0123456789abcdef", 0, 17},
		{"00000000000000000000000000000000", 0, 17},
		{"0123456789ABCDEF0123456789ABCDEF", 1, 33},
		{"event_-1", 1, 9},
		{strings.Repeat("a", 64), 1, 65},
		{strings.Repeat("z", 32), 1, 33},
	}
	encoded := map[string]string{}
	for _, test := range cases {
		key := compactObservationID(test.id)
		if len(key) != test.wantSize || key[0] != test.format {
			t.Errorf("%q encoded as %x, want format=%d length=%d", test.id, key, test.format, test.wantSize)
		}
		var roundTrip string
		if key[0] == 0 {
			roundTrip = hex.EncodeToString(key[1:])
		} else {
			roundTrip = string(key[1:])
		}
		if roundTrip != test.id {
			t.Errorf("ID %q round-trip changed to %q", test.id, roundTrip)
		}
		if previous, exists := encoded[string(key)]; exists {
			t.Errorf("ID collision between %q and %q", previous, test.id)
		}
		encoded[string(key)] = test.id
		if !bytes.Equal(key, compactObservationID(test.id)) {
			t.Error("encoding is not deterministic")
		}
	}
}

func TestCompactObservationWritePreservesFieldsAndBothDedupeScopes(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectExec(`SET LOCAL lock_timeout='3s'`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`SELECT pg_advisory_xact_lock\(hashtextextended`).WithArgs(int64(7)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`WITH incoming[\s\S]*DISTINCT ON \(node_id,event_key\)[\s\S]*INSERT INTO v2_node_observation_compact[\s\S]*NOT EXISTS \(SELECT 1 FROM v2_node_observation_compact[\s\S]*NOT EXISTS \(SELECT 1 FROM v2_node_observation_event`).
		WithArgs(int64(7), int64(2), []byte{1, 'e', 'v', 'e', 'n', 't', '_', '1', '2', '3'}, int64(100), "192.0.2.1", "example.test", "2001:db8::1", 443, 1, int64(101), "event_123").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	service := observationTestService(db)
	count, err := service.insertCompactObservation(context.Background(), 7, []ObservationEvent{{EventID: "event_123", UserID: 2, OccurredAt: 100,
		ClientIP: "192.0.2.1", TargetHost: "example.test", TargetIP: "2001:db8::1", TargetPort: 443, Network: "udp", Result: "route_attempt"}}, 101)
	if err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCompactObservationWriteRollsBackOnInsertFailure(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	wantErr := errors.New("partition unavailable")
	mock.ExpectBegin()
	mock.ExpectExec(`SET LOCAL lock_timeout='3s'`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`SELECT pg_advisory_xact_lock\(hashtextextended`).WithArgs(int64(7)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`WITH incoming`).WillReturnError(wantErr)
	mock.ExpectRollback()
	service := observationTestService(db)
	count, err := service.insertCompactObservation(context.Background(), 7, []ObservationEvent{{EventID: "event_123", UserID: 2, OccurredAt: 100,
		TargetHost: "example.test", TargetPort: 443, Network: "tcp", Result: "route_attempt"}}, 101)
	if !errors.Is(err, wantErr) || count != 0 {
		t.Fatalf("failed insert returned count=%d err=%v", count, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
