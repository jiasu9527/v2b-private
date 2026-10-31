package postgres

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forest/go-api/internal/entryevidence"
)

// This is a text contract only: it never connects to or mutates PostgreSQL.
func TestEntryEvidenceSQLFilesMatchRuntimeSchemas(t *testing.T) {
	t.Parallel()
	statements := append([]string{entryevidence.Schema}, entryEvidenceCasesStatements...)
	statements = append(statements, EntryEvidenceObservationSchema)
	for _, name := range []string{"install.pgsql.sql", "update.pgsql.sql"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "database", name))
			if err != nil {
				t.Fatal(err)
			}
			normalize := func(value string) string { return strings.Join(strings.Fields(value), " ") }
			schema := normalize(string(raw))
			for _, block := range statements {
				for _, statement := range strings.Split(block, ";") {
					statement = normalize(statement)
					if statement != "" && !strings.Contains(schema, statement+";") {
						t.Errorf("%s missing runtime schema statement: %s", name, statement)
					}
				}
			}
			if name == "update.pgsql.sql" && strings.Contains(string(raw), `DROP TABLE IF EXISTS "v2_entry_`) {
				t.Fatal("upgrade must not drop evidence tables")
			}
		})
	}
}

func TestEntryEvidenceInstallDropsChildrenBeforeParents(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "database", "install.pgsql.sql"))
	if err != nil {
		t.Fatal(err)
	}
	schema := string(raw)
	firstCreate := strings.Index(schema, "CREATE TABLE")
	positions := map[string]int{}
	for _, table := range []string{"v2_entry_evidence_probe_event", "v2_entry_evidence_probe_event_state", "v2_entry_evidence_exposure", "v2_entry_evidence_observation_state", "v2_entry_evidence_case_candidate", "v2_entry_evidence_case", "v2_entry_delivery_node", "v2_entry_delivery_request", "v2_entry_delivery_settings"} {
		position := strings.Index(schema, `DROP TABLE IF EXISTS "`+table+`";`)
		if position < 0 || position > firstCreate {
			t.Fatalf("%s must be dropped in the install preamble", table)
		}
		positions[table] = position
	}
	for _, pair := range [][2]string{{"v2_entry_evidence_probe_event", "v2_entry_evidence_case"}, {"v2_entry_evidence_probe_event_state", "v2_entry_evidence_case"}, {"v2_entry_evidence_exposure", "v2_entry_evidence_case"}, {"v2_entry_evidence_observation_state", "v2_entry_evidence_case"}, {"v2_entry_evidence_case_candidate", "v2_entry_evidence_case"}, {"v2_entry_delivery_node", "v2_entry_delivery_request"}} {
		if positions[pair[0]] >= positions[pair[1]] {
			t.Errorf("child %s must be dropped before parent %s", pair[0], pair[1])
		}
	}
}
