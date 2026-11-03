package entryevidence

import (
	"context"
	"database/sql"
	"os"
	"testing"
)

func TestHistoryHostRejectsCredentialsAndURLs(t *testing.T) {
	for _, v := range []string{"", "https://example.test/?token=secret", "user:password@host.test", "192.0.2.1:443"} {
		if _, err := HistoryHost(v); err == nil {
			t.Fatal("invalid host accepted", v)
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
