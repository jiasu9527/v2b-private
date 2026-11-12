package entryevidence

import (
	"context"
	"database/sql"
	"os"
	"reflect"
	"testing"
)

func TestHistoryHostRejectsCredentialsAndURLs(t *testing.T) {
	for _, v := range []string{"", "https://example.test/?token=secret", "user:password@host.test", "192.0.2.1:443"} {
		if _, err := HistoryHost(v); err == nil {
			t.Fatal("invalid host accepted", v)
		}
	}
}

func TestHistorySelectionNormalization(t *testing.T) {
	got, err := NormalizeHistorySelection(HistorySelection{Host: " Entry.Example.TEST ", GroupIDs: []int64{8, 7, 8}, ExcludeHosts: []string{"Fault.Example.TEST", "192.0.2.8", "fault.example.test"}})
	want := HistorySelection{Host: "entry.example.test", GroupIDs: []int64{7, 8}, ExcludeHosts: []string{"192.0.2.8", "fault.example.test"}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal(got, err)
	}
	for _, invalid := range []HistorySelection{
		{}, {ExcludeHosts: []string{"192.0.2.7"}}, {GroupIDs: []int64{0}}, {GroupIDs: []int64{-1}},
		{GroupIDs: make([]int64, 101)}, {Host: "192.0.2.7", ExcludeHosts: make([]string, 21)},
		{Host: "https://private.test/?token=x"}, {GroupIDs: []int64{1}, ExcludeHosts: []string{"user:pass@host.test"}},
	} {
		if _, err := NormalizeHistorySelection(invalid); err == nil {
			t.Fatal("invalid selection accepted", invalid)
		}
	}
}
func TestHistoryRetentionPostgresDefaultsAndPreservesExplicitSettings(t *testing.T) {
	dsn := os.Getenv("FOREST_ENTRY_EVIDENCE_TEST_DSN")
	if dsn == "" {
		t.Skip("requires disposable FOREST_ENTRY_EVIDENCE_TEST_DSN")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	store := NewStore(db)
	if err = store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	old, err := store.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`UPDATE v2_entry_delivery_settings SET keep_days=$1,updated_at=$2 WHERE id=1`, old.KeepDays, old.UpdatedAt)
	if _, err = db.Exec(`UPDATE v2_entry_delivery_settings SET keep_days=30,updated_at=0 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if err = store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	v, err := store.Settings(ctx)
	if err != nil || v.KeepDays != 7 {
		t.Fatal("legacy default not migrated", v, err)
	}
	if _, err = store.SaveSettings(ctx, 30); err != nil {
		t.Fatal(err)
	}
	if err = store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	v, err = store.Settings(ctx)
	if err != nil || v.KeepDays != 30 {
		t.Fatal("explicit settings overwritten", v, err)
	}
}
