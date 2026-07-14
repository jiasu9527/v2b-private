package admin

import (
	"regexp"
	"sort"
)

var (
	dnsFailoverAuthorizationPattern = regexp.MustCompile(`(?i)\bauthorization\s*[:=]\s*(?:bearer\s+)?[^\s,;&]+`)
	dnsFailoverCredentialPattern    = regexp.MustCompile(`(?i)\b(secret(?:id|key)?|api[_-]?token|api[_-]?key|token)\s*[:=]\s*([^\s;&]+)`)
)

func dnsFailoverSafeDiagnosticError(err error) string {
	if err == nil {
		return ""
	}
	return dnsFailoverSafeDiagnosticText(err.Error())
}

func dnsFailoverSafeDiagnosticText(value string) string {
	redacted := dnsFailoverAuthorizationPattern.ReplaceAllString(value, `Authorization=[REDACTED]`)
	redacted = dnsFailoverCredentialPattern.ReplaceAllString(redacted, `${1}=[REDACTED]`)
	return truncateDNSFailoverErrorText(redacted)
}

func truncateDNSFailoverErrorText(value string) string {
	return truncateDNSFailoverError(dnsFailoverTextError(value))
}

type dnsFailoverTextError string

func (err dnsFailoverTextError) Error() string { return string(err) }

func dnsFailoverOutboxTargetID(outbox dnsFailoverOutboxRow) *int64 {
	if !outbox.TargetID.Valid {
		return nil
	}
	targetID := outbox.TargetID.Int64
	return &targetID
}

func dnsFailoverOutboxTrace(outbox dnsFailoverOutboxRow, outcome, level, message string, now int64, extra map[string]any) dnsFailoverLogEntry {
	details := map[string]any{
		"outbox_id": outbox.ID, "operation": outbox.Operation, "requested_at": outbox.RequestedAt,
		"attempt": outbox.Attempts, "last_error": dnsFailoverSafeDiagnosticText(outbox.LastError),
	}
	for key, value := range extra {
		details[key] = value
	}
	return dnsFailoverLogEntry{
		GroupID: outbox.GroupID, TargetID: dnsFailoverOutboxTargetID(outbox), Stage: "outbox",
		Level: level, Outcome: outcome, Message: message, Details: details, CreatedAt: now,
	}
}

func dnsFailoverEvaluationTraceEntries(snapshot dnsFailoverWorkerSnapshot, outbox dnsFailoverOutboxRow, decision dnsFailoverDecision, now int64) []dnsFailoverLogEntry {
	currentTargetID := int64(0)
	if snapshot.Rule.CurrentTargetID != nil {
		currentTargetID = *snapshot.Rule.CurrentTargetID
	}
	candidateTargetIDs := make([]int64, 0, len(snapshot.Targets))
	enabledTargetIDs := make(map[int64]struct{}, len(snapshot.Targets))
	for _, target := range snapshot.Targets {
		if !target.Enabled {
			continue
		}
		enabledTargetIDs[target.ID] = struct{}{}
		if target.ID != currentTargetID {
			candidateTargetIDs = append(candidateTargetIDs, target.ID)
		}
	}
	sort.Slice(candidateTargetIDs, func(i, j int) bool { return candidateTargetIDs[i] < candidateTargetIDs[j] })
	onlineProbeIDs := onlineDNSFailoverProbeIDs(snapshot.Probes)
	decisionProbeIDs := dnsFailoverDecisionAvailableProbeIDList(snapshot.Probes, snapshot.States, currentTargetID)
	onlineProbes := make(map[int64]struct{}, len(onlineProbeIDs))
	for _, probeID := range onlineProbeIDs {
		onlineProbes[probeID] = struct{}{}
	}
	freshStates, staleStates, observedStates := 0, 0, 0
	for _, fact := range snapshot.StateFacts {
		probeID, probeOK := dnsFailoverTraceInt64(fact["probe_id"])
		targetID, targetOK := dnsFailoverTraceInt64(fact["target_id"])
		if !probeOK || !targetOK {
			continue
		}
		if _, ok := onlineProbes[probeID]; !ok {
			continue
		}
		if _, ok := enabledTargetIDs[targetID]; !ok {
			continue
		}
		observedStates++
		if stale, _ := fact["stale"].(bool); stale {
			staleStates++
		} else {
			freshStates++
		}
	}
	expectedStates := len(onlineProbeIDs) * len(enabledTargetIDs)
	missingStates := expectedStates - observedStates
	if missingStates < 0 {
		missingStates = 0
	}
	thresholds := map[string]any{
		"failure": snapshot.Rule.FailureThreshold, "success": snapshot.Rule.SuccessThreshold,
		"single_probe_failure": snapshot.Rule.SingleProbeFailureThreshold,
		"single_probe_success": snapshot.Rule.SingleProbeSuccessThreshold,
	}
	var effectiveFailureThreshold, effectiveSuccessThreshold any
	if len(decisionProbeIDs) == 1 {
		effectiveFailureThreshold = snapshot.Rule.SingleProbeFailureThreshold
		effectiveSuccessThreshold = snapshot.Rule.SingleProbeSuccessThreshold
	} else if len(decisionProbeIDs) > 1 {
		effectiveFailureThreshold = snapshot.Rule.FailureThreshold
		effectiveSuccessThreshold = snapshot.Rule.SuccessThreshold
	}
	freshness := map[string]any{
		"configured_probe_count": len(snapshot.Probes), "online_probe_count": len(onlineProbeIDs),
		"enabled_target_count": len(enabledTargetIDs), "expected_state_count": expectedStates,
		"observed_state_count": observedStates, "fresh_state_count": freshStates,
		"stale_state_count": staleStates, "missing_state_count": missingStates,
	}
	baseDetails := map[string]any{
		"outbox_id": outbox.ID, "operation": outbox.Operation, "requested_at": outbox.RequestedAt,
		"current_target_id": currentTargetID, "candidate_target_ids": candidateTargetIDs,
		"online_probe_ids": onlineProbeIDs, "decision_available_probe_ids": decisionProbeIDs,
		"thresholds": thresholds, "effective_failure_threshold": effectiveFailureThreshold,
		"effective_success_threshold": effectiveSuccessThreshold, "freshness": freshness,
		"states": snapshot.StateFacts,
	}
	snapshotDetails := cloneDNSFailoverTraceDetails(baseDetails)
	decisionDetails := cloneDNSFailoverTraceDetails(baseDetails)
	decisionDetails["action"] = string(decision.Action)
	decisionDetails["reason"] = decision.Reason
	decisionDetails["target_id"] = decision.TargetID
	outcome := "no_switch"
	level := "info"
	switch {
	case decision.Action != dnsFailoverActionNone:
		outcome = "switch"
		level = "warning"
	case decision.Reason == dnsFailoverReasonFailureThresholdPending:
		outcome = "threshold_pending"
		level = "warning"
	case decision.Reason == dnsFailoverReasonNoProbeData:
		outcome = "no_data"
		level = "warning"
	}
	targetID := currentTargetID
	if decision.TargetID > 0 {
		targetID = decision.TargetID
	}
	var targetIDPointer *int64
	if targetID > 0 {
		targetIDPointer = &targetID
	}
	return []dnsFailoverLogEntry{
		{
			GroupID: snapshot.Rule.ID, TargetID: targetIDPointer, Stage: "evaluation", Level: "info", Outcome: "snapshot",
			Message: "evaluation snapshot loaded", Details: snapshotDetails, CreatedAt: now,
		},
		{
			GroupID: snapshot.Rule.ID, TargetID: targetIDPointer, Stage: "evaluation", Level: level, Outcome: outcome,
			Message: "evaluation decision computed", Details: decisionDetails, CreatedAt: now,
		},
	}
}

func cloneDNSFailoverTraceDetails(source map[string]any) map[string]any {
	clone := make(map[string]any, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func dnsFailoverTraceInt64(value any) (int64, bool) {
	switch typed := value.(type) {
	case int64:
		return typed, true
	case int:
		return int64(typed), true
	default:
		return 0, false
	}
}
