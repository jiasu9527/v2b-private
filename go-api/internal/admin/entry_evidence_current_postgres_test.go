package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"forest/go-api/internal/cliententry"
	"os"
	"testing"
	"time"
)

func TestEntryEvidenceSupportOverrideGuardPostgres(t *testing.T) {
	dsn := os.Getenv("FOREST_ENTRY_EVIDENCE_TEST_DSN")
	if dsn == "" {
		t.Skip("isolated FOREST_ENTRY_EVIDENCE_TEST_DSN required")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	// A temporary shadow table verifies the SQL's NULL/expiry predicates without
	// touching persistent support records or needing a real panel identity.
	_, err = tx.Exec(`CREATE TEMP TABLE v2_support_entry_override(user_id BIGINT,source_policy_id BIGINT,active SMALLINT,expires_at BIGINT)`)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name         string
		active       int
		expiry       any
		selected     bool
		wantConflict bool
	}{
		{"no expiry", 1, nil, true, true}, {"zero expiry", 1, int64(0), true, true}, {"future expiry", 1, int64(101), true, true},
		{"expires at now", 1, int64(100), true, false}, {"already expired", 1, int64(99), true, false}, {"inactive", 0, nil, true, false}, {"unselected user", 1, nil, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tx.Exec(`TRUNCATE v2_support_entry_override`); err != nil {
				t.Fatal(err)
			}
			userID := int64(7)
			if !tc.selected {
				userID = 8
			}
			if _, err := tx.Exec(`INSERT INTO v2_support_entry_override VALUES($1,11,$2,$3)`, userID, tc.active, tc.expiry); err != nil {
				t.Fatal(err)
			}
			err := evidenceRejectSupportOverrides(context.Background(), tx, []int64{7}, 100)
			if tc.wantConflict != errors.Is(err, ErrEntryEvidenceConflict) {
				t.Fatalf("err=%v conflict=%v", err, tc.wantConflict)
			}
			if !tc.wantConflict && err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestEntryEvidenceConcurrentUserEditReturnsConflictPostgres(t *testing.T) {
	dsn := os.Getenv("FOREST_ENTRY_EVIDENCE_TEST_DSN")
	if dsn == "" {
		t.Skip("isolated FOREST_ENTRY_EVIDENCE_TEST_DSN required")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var userID int64
	marker := fmt.Sprint(time.Now().UnixNano())
	err = db.QueryRow(`INSERT INTO v2_user(email,password,uuid,token,created_at,updated_at)VALUES($1,'fixture','fixture',$2,1,1) RETURNING id`, "ev-locked-"+marker+"@example.test", marker).Scan(&userID)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM v2_user WHERE id=$1`, userID)
	editor, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer editor.Rollback()
	var locked int64
	if err = editor.QueryRow(`SELECT id FROM v2_user WHERE id=$1 FOR UPDATE`, userID).Scan(&locked); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"source list", "current coverage"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			rule := AIEntryRuleSnapshot{Kind: "policy", Conditions: []cliententry.Condition{{Field: "user_id", Operator: "between", Min: json.RawMessage(fmt.Sprint(userID)), Max: json.RawMessage(fmt.Sprint(userID))}}}
			if name == "source list" {
				_, err = evidenceSourceUsers(ctx, tx, rule)
			} else {
				err = evidenceCurrentCoverage(ctx, tx, rule, []EntryEvidenceCandidate{{UserID: userID, UAs: []string{"Forest"}}})
			}
			if !errors.Is(err, ErrEntryEvidenceConflict) {
				t.Fatalf("must promptly conflict rather than block: %v", err)
			}
		})
	}
}
