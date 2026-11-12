package postgres

import (
	"context"
	"database/sql"
	"fmt"
)

func EnsureEntryInvestigationSchema(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("db is nil")
	}
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS v2_entry_investigation (
 case_id BIGINT PRIMARY KEY REFERENCES v2_entry_evidence_case(id) ON DELETE RESTRICT,
 root_case_id BIGINT NOT NULL REFERENCES v2_entry_evidence_case(id) ON DELETE RESTRICT,
 round INTEGER NOT NULL CHECK(round BETWEEN 1 AND 30),
 revision BIGINT NOT NULL DEFAULT 1 CHECK(revision>0),
 created_by BIGINT NOT NULL, created_at BIGINT NOT NULL, updated_at BIGINT NOT NULL,
 settings JSONB NOT NULL, failures JSONB NOT NULL DEFAULT '[]',
 report JSONB NOT NULL DEFAULT '{}', checked_at BIGINT NOT NULL DEFAULT 0,
 next_case_id BIGINT UNIQUE REFERENCES v2_entry_evidence_case(id) ON DELETE RESTRICT
 )`,
		`CREATE INDEX IF NOT EXISTS idx_entry_investigation_refresh_all ON v2_entry_investigation(checked_at,case_id)`,
		`CREATE INDEX IF NOT EXISTS idx_entry_investigation_root ON v2_entry_investigation(root_case_id,round)`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("ensure entry investigation: %w", err)
		}
	}
	return nil
}
