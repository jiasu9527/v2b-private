package postgres

import (
	"context"
	"database/sql"
)

const EntryEvidenceObservationSchema = `
CREATE TABLE IF NOT EXISTS v2_entry_evidence_exposure (
 case_id BIGINT NOT NULL REFERENCES v2_entry_evidence_case(id) ON DELETE CASCADE,
 branch TEXT NOT NULL CHECK(branch IN ('A','B')), user_id BIGINT NOT NULL,
 first_at BIGINT NOT NULL, last_at BIGINT NOT NULL, first_request_id TEXT NOT NULL,
 last_request_id TEXT NOT NULL, PRIMARY KEY(case_id,branch,user_id)
);
CREATE TABLE IF NOT EXISTS v2_entry_evidence_observation_state (
 case_id BIGINT PRIMARY KEY REFERENCES v2_entry_evidence_case(id) ON DELETE CASCADE,
 last_refreshed_at BIGINT NOT NULL, coverage_gap BOOLEAN NOT NULL DEFAULT FALSE
);
` + EntryEvidenceProbeEventSchema

func EnsureEntryEvidenceObservationSchema(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, EntryEvidenceObservationSchema)
	return err
}
