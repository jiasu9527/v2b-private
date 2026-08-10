package postgres

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClientEntrySplitSQLSchemasContainSnapshotTreeAndAssignments(t *testing.T) {
	t.Parallel()

	databaseDir := filepath.Join("..", "..", "..", "..", "database")
	for _, filename := range []string{"install.pgsql.sql", "update.pgsql.sql"} {
		filename := filename
		t.Run(filename, func(t *testing.T) {
			t.Parallel()

			content, err := os.ReadFile(filepath.Join(databaseDir, filename))
			if err != nil {
				t.Fatalf("read %s: %v", filename, err)
			}
			schema := string(content)
			for _, required := range []string{
				`"mode" varchar(16) NOT NULL DEFAULT 'standard'`,
				`"snapshot_from" BIGINT DEFAULT NULL`,
				`"snapshot_to" BIGINT DEFAULT NULL`,
				`"v2_user_subscribe_activity"`,
				`"last_subscribe_at" BIGINT NOT NULL`,
				`"v2_client_entry_user_policy_split_group"`,
				`"parent_id" BIGINT DEFAULT NULL`,
				`"path" varchar(255) NOT NULL DEFAULT ''`,
				`"entry_host" varchar(255) NOT NULL DEFAULT ''`,
				`"v2_client_entry_user_policy_split_assignment"`,
				`UNIQUE ("policy_id", "user_id")`,
				`ON DELETE CASCADE`,
				`ON DELETE CASCADE`,
				`"idx_v2_user_subscribe_activity_last_subscribe_at"`,
				`"idx_v2_client_entry_user_policy_split_group_policy"`,
				`"idx_v2_client_entry_user_policy_split_assignment_user"`,
			} {
				if !strings.Contains(schema, required) {
					t.Errorf("%s missing %q", filename, required)
				}
			}
		})
	}
}

func TestClientEntrySplitInstallDropsChildrenBeforeParents(t *testing.T) {
	t.Parallel()

	path := filepath.Join("..", "..", "..", "..", "database", "install.pgsql.sql")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read install schema: %v", err)
	}
	schema := string(content)

	assignment := strings.Index(schema, `DROP TABLE IF EXISTS "v2_client_entry_user_policy_split_assignment"`)
	group := strings.Index(schema, `DROP TABLE IF EXISTS "v2_client_entry_user_policy_split_group"`)
	policy := strings.Index(schema, `DROP TABLE IF EXISTS "v2_client_entry_user_policy"`)
	activity := strings.Index(schema, `DROP TABLE IF EXISTS "v2_user_subscribe_activity"`)
	user := strings.Index(schema, `DROP TABLE IF EXISTS "v2_user"`)
	if assignment < 0 || group < 0 || policy < 0 || activity < 0 || user < 0 {
		t.Fatalf("install schema is missing a split/activity DROP statement")
	}
	if !(assignment < group && group < policy) {
		t.Fatalf("split DROP order must be assignment -> group -> policy")
	}
	if activity > user {
		t.Fatalf("activity table must be dropped before the user table")
	}
}
