package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"forest/go-api/internal/cliententry"
)

type EntryEvidenceProbeEvent struct {
	EventID    int64  `json:"event_id"`
	Branch     string `json:"branch"`
	TargetID   int64  `json:"target_id"`
	ProbeID    int64  `json:"probe_id"`
	EventType  string `json:"event_type"`
	HappenedAt int64  `json:"happened_at"`
	Host       string `json:"host"`
	Port       int64  `json:"port"`
	ResolvedIP string `json:"resolved_ip,omitempty"`
}

// Never retain raw monitor details or error/command output. Parse and validate
// only typed address metadata; malformed unrelated fields are ignored.
func evidenceProbeAddress(raw string) (host string, port int64, resolved string, ok bool) {
	var details struct {
		Host       string `json:"host"`
		Port       int64  `json:"port"`
		ResolvedIP string `json:"resolved_ip"`
	}
	if len(raw) > 1<<20 || json.Unmarshal([]byte(raw), &details) != nil || details.Port < 1 || details.Port > 65535 {
		return "", 0, "", false
	}
	normalized, err := cliententry.NormalizeHost(details.Host)
	if err != nil {
		return "", 0, "", false
	}
	if ip := net.ParseIP(details.ResolvedIP); ip != nil {
		resolved = ip.String()
	}
	return strings.ToLower(normalized), details.Port, resolved, true
}

func evidenceProbeSnapshotEvents(ctx context.Context, tx *sql.Tx, rule AIEntryRuleSnapshot, source string) ([]EntryEvidenceProbeEvent, error) {
	if rule.Kind == "policy" {
		source = ""
	}
	rows, err := tx.QueryContext(ctx, `SELECT e.id,COALESCE(e.target_id,0),COALESCE(e.probe_id,0),e.event_type,e.created_at,e.details
 FROM v2_client_entry_monitor_event e JOIN v2_client_entry_monitor m ON m.id=e.monitor_id
 LEFT JOIN v2_client_entry_monitor_target t ON t.id=e.target_id
 WHERE m.policy_id=$1 AND ($2='' OR t.source_key=$2) AND e.event_type IN ('down','recovered')
 ORDER BY e.id DESC LIMIT 50`, rule.PolicyID, source)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EntryEvidenceProbeEvent{}
	for rows.Next() {
		var event EntryEvidenceProbeEvent
		var raw string
		if err = rows.Scan(&event.EventID, &event.TargetID, &event.ProbeID, &event.EventType, &event.HappenedAt, &raw); err != nil {
			return nil, err
		}
		host, port, resolved, ok := evidenceProbeAddress(raw)
		if !ok {
			continue
		}
		event.Host, event.Port, event.ResolvedIP = host, port, resolved
		out = append(out, event)
	}
	return out, rows.Err()
}

// Called with the case row lock already held by observation refresh. Events are
// accepted only for the execution's exact source key AND event-time address.
// Joining the current target alone would wrongly relabel pre-edit events.
func refreshEntryEvidenceProbeEvents(ctx context.Context, tx *sql.Tx, c *EntryEvidenceCase) error {
	if c.Execution == nil {
		return nil
	}
	now := time.Now().Unix()
	var last int64
	var priorGap bool
	err := tx.QueryRowContext(ctx, `SELECT last_refreshed_at,coverage_gap FROM v2_entry_evidence_probe_event_state WHERE case_id=$1`, c.ID).Scan(&last, &priorGap)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if last == 0 {
		last = c.Execution.ExecutedAt
	}
	c.ProbeEventsGap = priorGap || last < now-86400
	aSource := fmt.Sprintf("policy:%d:split-group:%d", c.Execution.PolicyID, c.Execution.GroupAID)
	bSource := fmt.Sprintf("policy:%d:split-group:%d", c.Execution.PolicyID, c.Execution.GroupBID)
	aHost, _, _, aOK := evidenceProbeAddress(fmt.Sprintf(`{"host":%q,"port":%d}`, c.Execution.EntryHostA, c.Query.Port))
	bHost, _, _, bOK := evidenceProbeAddress(fmt.Sprintf(`{"host":%q,"port":%d}`, c.Execution.EntryHostB, c.Query.Port))
	if !aOK || !bOK {
		return ErrEntryEvidenceInvalid
	}
	// A keyset is local to this refresh only; late-committing old events are
	// reconsidered next time. Persisted event IDs avoid repeat payload parsing.
	afterID := int64(0)
	for {
		rows, queryErr := tx.QueryContext(ctx, `SELECT e.id,e.target_id,COALESCE(e.probe_id,0),e.event_type,e.created_at,t.source_key,e.details
 FROM v2_client_entry_monitor_event e
 JOIN v2_client_entry_monitor_target t ON t.id=e.target_id AND t.monitor_id=e.monitor_id
 JOIN v2_client_entry_monitor m ON m.id=e.monitor_id
 WHERE m.policy_id=$1 AND t.source_key IN ($2,$3) AND e.created_at>=$4 AND e.created_at<=$5
 AND e.event_type IN ('down','recovered') AND e.id>$6
 AND NOT EXISTS(SELECT 1 FROM v2_entry_evidence_probe_event saved WHERE saved.case_id=$7 AND saved.event_id=e.id)
 ORDER BY e.id LIMIT 1000`, c.Execution.PolicyID, aSource, bSource, c.Execution.ExecutedAt, now, afterID, c.ID)
		if queryErr != nil {
			return queryErr
		}
		batch := []EntryEvidenceProbeEvent{}
		seen := 0
		for rows.Next() {
			var event EntryEvidenceProbeEvent
			var source, raw string
			if err = rows.Scan(&event.EventID, &event.TargetID, &event.ProbeID, &event.EventType, &event.HappenedAt, &source, &raw); err != nil {
				rows.Close()
				return err
			}
			afterID = event.EventID
			seen++
			host, port, resolved, ok := evidenceProbeAddress(raw)
			if !ok || port != c.Query.Port {
				continue
			}
			if source == aSource && host == aHost {
				event.Branch = "A"
			} else if source == bSource && host == bHost {
				event.Branch = "B"
			} else {
				continue
			}
			event.Host, event.Port, event.ResolvedIP = host, port, resolved
			batch = append(batch, event)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(batch) > 0 {
			raw, err := json.Marshal(batch)
			if err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO v2_entry_evidence_probe_event(case_id,event_id,branch,target_id,probe_id,event_type,happened_at,host,port,resolved_ip)
 SELECT $1,event_id,branch,target_id,probe_id,event_type,happened_at,host,port,COALESCE(resolved_ip,'')
 FROM jsonb_to_recordset($2::JSONB) AS e(event_id BIGINT,branch TEXT,target_id BIGINT,probe_id BIGINT,event_type TEXT,happened_at BIGINT,host TEXT,port INTEGER,resolved_ip TEXT)
 ON CONFLICT(case_id,event_id) DO NOTHING`, c.ID, string(raw)); err != nil {
				return err
			}
		}
		if seen < 1000 {
			break
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO v2_entry_evidence_probe_event_state(case_id,last_refreshed_at,coverage_gap) VALUES($1,$2,$3) ON CONFLICT(case_id) DO UPDATE SET last_refreshed_at=EXCLUDED.last_refreshed_at,coverage_gap=EXCLUDED.coverage_gap`, c.ID, now, c.ProbeEventsGap); err != nil {
		return err
	}
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_entry_evidence_probe_event WHERE case_id=$1`, c.ID).Scan(&c.ProbeEventsTotal); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT event_id,branch,target_id,probe_id,event_type,happened_at,host,port,resolved_ip FROM v2_entry_evidence_probe_event WHERE case_id=$1 ORDER BY happened_at DESC,event_id DESC LIMIT 100`, c.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	c.ProbeEvents = []EntryEvidenceProbeEvent{}
	for rows.Next() {
		var event EntryEvidenceProbeEvent
		if err = rows.Scan(&event.EventID, &event.Branch, &event.TargetID, &event.ProbeID, &event.EventType, &event.HappenedAt, &event.Host, &event.Port, &event.ResolvedIP); err != nil {
			return err
		}
		c.ProbeEvents = append(c.ProbeEvents, event)
	}
	c.ProbeEventsTruncated = c.ProbeEventsTotal > int64(len(c.ProbeEvents))
	return rows.Err()
}
