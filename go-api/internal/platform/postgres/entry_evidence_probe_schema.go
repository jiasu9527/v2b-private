package postgres

// Archived transition evidence is independent of the 24-hour monitor log and
// mutable targets/probes. Only deletion of its case removes the frozen copy.
const EntryEvidenceProbeEventSchema = `
CREATE TABLE IF NOT EXISTS v2_entry_evidence_probe_event (
 case_id BIGINT NOT NULL REFERENCES v2_entry_evidence_case(id) ON DELETE CASCADE,
 event_id BIGINT NOT NULL, branch TEXT NOT NULL CHECK(branch IN ('A','B')),
 target_id BIGINT NOT NULL, probe_id BIGINT NOT NULL DEFAULT 0,
 event_type TEXT NOT NULL CHECK(event_type IN ('down','recovered')),
 happened_at BIGINT NOT NULL, host TEXT NOT NULL,
 port INTEGER NOT NULL CHECK(port BETWEEN 1 AND 65535),
 resolved_ip TEXT NOT NULL DEFAULT '', PRIMARY KEY(case_id,event_id)
);
CREATE INDEX IF NOT EXISTS idx_entry_evidence_probe_event_time
 ON v2_entry_evidence_probe_event(case_id,happened_at DESC,event_id DESC);
CREATE TABLE IF NOT EXISTS v2_entry_evidence_probe_event_state (
 case_id BIGINT PRIMARY KEY REFERENCES v2_entry_evidence_case(id) ON DELETE CASCADE,
 last_refreshed_at BIGINT NOT NULL, coverage_gap BOOLEAN NOT NULL DEFAULT FALSE
);
`
