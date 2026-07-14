package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type DNSFailoverProbeStatus struct {
	ID                int64  `json:"id"`
	Name              string `json:"name"`
	Enabled           bool   `json:"enabled"`
	Online            bool   `json:"online"`
	DecisionAvailable bool   `json:"decision_available"`
	LastHeartbeatAt   *int64 `json:"last_heartbeat_at"`
	PrewarmCount      int64  `json:"prewarm_count"`
}

type DNSFailoverTargetStateStatus struct {
	ProbeID        int64  `json:"probe_id"`
	TargetID       int64  `json:"target_id"`
	LastSuccess    *bool  `json:"last_success"`
	LatencyMS      *int64 `json:"last_latency_ms"`
	LastError      string `json:"last_error"`
	ResolvedIP     string `json:"last_resolved_ip"`
	SuccessStreak  int64  `json:"consecutive_success"`
	FailureStreak  int64  `json:"consecutive_failure"`
	LastReportedAt *int64 `json:"last_reported_at"`
	WarmedUp       bool   `json:"warmed_up"`
	Stale          bool   `json:"stale"`
}

type DNSFailoverDecisionStatus struct {
	Action   string `json:"action"`
	Reason   string `json:"reason"`
	TargetID int64  `json:"target_id"`
}

type DNSFailoverOperationStatus struct {
	Operation     string `json:"operation"`
	Phase         string `json:"phase"`
	Attempts      int64  `json:"attempts"`
	NextAttemptAt int64  `json:"next_attempt_at"`
	LastError     string `json:"last_error"`
}

type DNSFailoverStatus struct {
	ServerTime             int64                          `json:"server_time"`
	Rule                   DNSFailoverRuleRecord          `json:"rule"`
	LastEvaluatedAt        *int64                         `json:"last_evaluated_at"`
	ActiveIncidentType     string                         `json:"active_incident_type"`
	ActiveIncidentSince    *int64                         `json:"active_incident_since"`
	ActiveDNSIncidentType  string                         `json:"active_dns_incident_type"`
	ActiveDNSIncidentSince *int64                         `json:"active_dns_incident_since"`
	Decision               DNSFailoverDecisionStatus      `json:"decision"`
	Operation              *DNSFailoverOperationStatus    `json:"operation"`
	Probes                 []DNSFailoverProbeStatus       `json:"probes"`
	States                 []DNSFailoverTargetStateStatus `json:"states"`
}

func (s *DBService) GetDNSFailoverStatus(ctx context.Context, id int64) (DNSFailoverStatus, error) {
	rule, err := s.GetDNSFailoverRule(ctx, id)
	if err != nil {
		return DNSFailoverStatus{}, err
	}
	now := time.Now().Unix()
	status := DNSFailoverStatus{ServerTime: now, Rule: rule, Probes: []DNSFailoverProbeStatus{}, States: []DNSFailoverTargetStateStatus{}}
	var lastEvaluated, incidentSince, dnsIncidentSince sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT last_evaluated_at, active_incident_type, active_incident_since, active_dns_incident_type, active_dns_incident_since
FROM v2_dns_failover_group WHERE id = $1`, id).Scan(&lastEvaluated, &status.ActiveIncidentType, &incidentSince, &status.ActiveDNSIncidentType, &dnsIncidentSince); err != nil {
		return DNSFailoverStatus{}, fmt.Errorf("读取故障转移运行状态失败: %w", err)
	}
	status.LastEvaluatedAt = dnsNullInt64Pointer(lastEvaluated)
	status.ActiveIncidentSince = dnsNullInt64Pointer(incidentSince)
	status.ActiveDNSIncidentSince = dnsNullInt64Pointer(dnsIncidentSince)

	rows, err := s.db.QueryContext(ctx, `SELECT p.id, p.name, p.enabled, p.last_heartbeat_at, p.prewarm_count
FROM v2_dns_failover_group_probe gp JOIN v2_dns_probe p ON p.id = gp.probe_id
WHERE gp.group_id = $1 ORDER BY p.id`, id)
	if err != nil {
		return DNSFailoverStatus{}, fmt.Errorf("读取故障转移探针状态失败: %w", err)
	}
	for rows.Next() {
		var probe DNSFailoverProbeStatus
		var enabled int64
		var heartbeat sql.NullInt64
		if err := rows.Scan(&probe.ID, &probe.Name, &enabled, &heartbeat, &probe.PrewarmCount); err != nil {
			rows.Close()
			return DNSFailoverStatus{}, err
		}
		probe.Enabled = enabled != 0
		probe.LastHeartbeatAt = dnsNullInt64Pointer(heartbeat)
		probe.Online = probe.Enabled && dnsFailoverProbeOnline(heartbeat, probe.PrewarmCount, now, rule.ProbeOfflineSec)
		status.Probes = append(status.Probes, probe)
	}
	if err := rows.Close(); err != nil {
		return DNSFailoverStatus{}, err
	}
	if err := rows.Err(); err != nil {
		return DNSFailoverStatus{}, err
	}

	rows, err = s.db.QueryContext(ctx, `SELECT s.probe_id, s.target_id, s.last_success, s.last_latency_ms, s.last_error,
s.last_resolved_ip, s.consecutive_success, s.consecutive_failure, s.last_reported_at, s.warmed_up
FROM v2_dns_probe_target_state s JOIN v2_dns_failover_target t ON t.id = s.target_id
WHERE t.group_id = $1 ORDER BY s.probe_id, s.target_id`, id)
	if err != nil {
		return DNSFailoverStatus{}, fmt.Errorf("读取故障转移测活状态失败: %w", err)
	}
	for rows.Next() {
		var state DNSFailoverTargetStateStatus
		var success, warmed sql.NullInt64
		var latency, reported sql.NullInt64
		if err := rows.Scan(&state.ProbeID, &state.TargetID, &success, &latency, &state.LastError, &state.ResolvedIP, &state.SuccessStreak, &state.FailureStreak, &reported, &warmed); err != nil {
			rows.Close()
			return DNSFailoverStatus{}, err
		}
		if success.Valid {
			value := success.Int64 != 0
			state.LastSuccess = &value
		}
		if latency.Valid {
			value := latency.Int64
			state.LatencyMS = &value
		}
		state.LastReportedAt = dnsNullInt64Pointer(reported)
		state.WarmedUp = warmed.Valid && warmed.Int64 != 0
		state.Stale = !dnsFailoverProbeStateFresh(reported, now, rule.CheckIntervalSec, rule.TCPTimeoutMS, rule.ProbeOfflineSec)
		status.States = append(status.States, state)
	}
	if err := rows.Close(); err != nil {
		return DNSFailoverStatus{}, err
	}
	if err := rows.Err(); err != nil {
		return DNSFailoverStatus{}, err
	}

	var operation DNSFailoverOperationStatus
	err = s.db.QueryRowContext(ctx, `SELECT operation, attempts, next_attempt_at, last_error FROM v2_dns_failover_eval_outbox WHERE group_id = $1`, id).
		Scan(&operation.Operation, &operation.Attempts, &operation.NextAttemptAt, &operation.LastError)
	if err == nil {
		status.Operation = &operation
	} else if !errors.Is(err, sql.ErrNoRows) {
		return DNSFailoverStatus{}, err
	}
	var saga DNSFailoverOperationStatus
	err = s.db.QueryRowContext(ctx, `SELECT phase, attempts, next_attempt_at, last_error FROM v2_dns_failover_saga WHERE group_id = $1`, id).
		Scan(&saga.Phase, &saga.Attempts, &saga.NextAttemptAt, &saga.LastError)
	if err == nil {
		saga.Operation = "saga"
		status.Operation = &saga
	} else if !errors.Is(err, sql.ErrNoRows) {
		return DNSFailoverStatus{}, err
	}

	probes := make([]dnsFailoverProbeSnapshot, 0, len(status.Probes))
	for _, probe := range status.Probes {
		probes = append(probes, dnsFailoverProbeSnapshot{ID: probe.ID, Online: probe.Online})
	}
	states := make([]dnsFailoverProbeTargetSnapshot, 0, len(status.States))
	for _, state := range status.States {
		if state.WarmedUp && !state.Stale {
			states = append(states, dnsFailoverProbeTargetSnapshot{ProbeID: state.ProbeID, TargetID: state.TargetID, SuccessStreak: safeInt(state.SuccessStreak), FailureStreak: safeInt(state.FailureStreak)})
		}
	}
	targets := make([]dnsFailoverTargetSnapshot, 0, len(rule.Targets))
	for _, target := range rule.Targets {
		targets = append(targets, dnsFailoverTargetSnapshot{ID: target.ID, Sort: safeInt(target.Sort), Enabled: target.Enabled})
	}
	current := int64(0)
	if rule.CurrentTargetID != nil {
		current = *rule.CurrentTargetID
	}
	var lastSwitch time.Time
	if rule.LastSwitchAt != nil {
		lastSwitch = time.Unix(*rule.LastSwitchAt, 0)
	}
	availableProbeIDs := dnsFailoverDecisionAvailableProbeIDs(probes, states, current)
	for index := range status.Probes {
		_, status.Probes[index].DecisionAvailable = availableProbeIDs[status.Probes[index].ID]
	}
	decision := dnsFailoverDecision{Action: dnsFailoverActionNone, Reason: dnsFailoverReasonRuleDisabled}
	if rule.Enabled {
		decision = decideDNSFailover(dnsFailoverDecisionInput{Now: time.Unix(now, 0), CurrentTargetID: current, AutoFailback: rule.AutoFailback, LastSwitchAt: lastSwitch, Cooldown: safeSecondsDuration(rule.CooldownSec), FailureThreshold: safeInt(rule.FailureThreshold), SuccessThreshold: safeInt(rule.SuccessThreshold), SingleProbeFailureThreshold: safeInt(rule.SingleProbeFailureThreshold), SingleProbeSuccessThreshold: safeInt(rule.SingleProbeSuccessThreshold), Targets: targets, Probes: probes, States: states})
	}
	status.Decision = DNSFailoverDecisionStatus{Action: string(decision.Action), Reason: decision.Reason, TargetID: decision.TargetID}
	return status, nil
}
