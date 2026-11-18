package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func TestObservationPartitionCleanupUsesAttachedChildSchema(t *testing.T) {
	dsn := os.Getenv("FOREST_OBSERVATION_TEST_DSN")
	if dsn == "" {
		dsn = os.Getenv("OBSERVATION_TEST_DSN")
	}
	if dsn == "" {
		t.Skip("set FOREST_OBSERVATION_TEST_DSN to a disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	adminDB := stdlib.OpenDB(*config)
	schema := fmt.Sprintf("observation_partition_test_%d", time.Now().UnixNano())
	archive := schema + `_archive"quoted`
	for _, name := range []string{schema, archive} {
		if _, err := adminDB.ExecContext(ctx, `CREATE SCHEMA `+pgx.Identifier{name}.Sanitize()); err != nil {
			adminDB.Close()
			t.Fatal(err)
		}
	}
	config.RuntimeParams["search_path"] = schema
	db := stdlib.OpenDB(*config)
	t.Cleanup(func() {
		db.Close()
		for _, name := range []string{schema, archive} {
			if _, err := adminDB.Exec(`DROP SCHEMA ` + pgx.Identifier{name}.Sanitize() + ` CASCADE`); err != nil {
				t.Errorf("clean up owned test schema: %v", err)
			}
		}
		adminDB.Close()
	})
	if err := EnsureObservationSchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	day := observationUTCDay(time.Now()).AddDate(0, 0, -10)
	name := observationPartitionPrefix + day.Format("20060102")
	child := pgx.Identifier{archive, name}.Sanitize()
	shadow := pgx.Identifier{schema, name}.Sanitize()
	// PostgreSQL allows a child partition in a different namespace. An unrelated
	// table with the same name must not be resolved through search_path by DROP.
	for _, statement := range []string{
		fmt.Sprintf(`CREATE TABLE %s PARTITION OF v2_node_observation_compact FOR VALUES FROM (%d) TO (%d)`, child, day.Unix(), day.AddDate(0, 0, 1).Unix()),
		`CREATE TABLE ` + shadow + `(marker text)`,
		`INSERT INTO ` + shadow + ` VALUES('preserve unrelated table')`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := DeleteExpiredObservationPartitions(ctx, db, time.Now().Add(-7*24*time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	var relation sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT to_regclass($1)::text`, child).Scan(&relation); err != nil || relation.Valid {
		t.Fatalf("attached expired partition was not dropped: relation=%v err=%v", relation, err)
	}
	var marker string
	if err := db.QueryRowContext(ctx, `SELECT marker FROM `+shadow).Scan(&marker); err != nil || marker != "preserve unrelated table" {
		t.Fatalf("unrelated shadow table was changed: marker=%q err=%v", marker, err)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pg_inherits WHERE inhparent='v2_node_observation_compact'::regclass`).Scan(&count); err != nil || count != 9 {
		t.Fatalf("active partitions were removed: count=%d err=%v", count, err)
	}
}
