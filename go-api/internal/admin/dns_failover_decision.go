package admin

import (
	"sort"
	"time"
)

type dnsFailoverAction string

const (
	dnsFailoverActionNone     dnsFailoverAction = "none"
	dnsFailoverActionFailover dnsFailoverAction = "failover"
	dnsFailoverActionFailback dnsFailoverAction = "failback"
)

const (
	dnsFailoverReasonAllProbesOffline        = "all_probes_offline"
	dnsFailoverReasonCooldown                = "cooldown_active"
	dnsFailoverReasonCurrentFailed           = "current_target_failed"
	dnsFailoverReasonProbeDisagreement       = "probe_disagreement"
	dnsFailoverReasonNoHealthyTarget         = "no_healthy_target"
	dnsFailoverReasonCurrentHealthy          = "current_target_healthy"
	dnsFailoverReasonFailureThresholdPending = "failure_threshold_pending"
	dnsFailoverReasonNoProbeData             = "no_probe_data"
	dnsFailoverReasonHigherPriorityRecovered = "higher_priority_target_recovered"
	dnsFailoverReasonRuleDisabled            = "rule_disabled"
)

type dnsFailoverDecision struct {
	Action   dnsFailoverAction
	TargetID int64
	Reason   string
}

type dnsFailoverDecisionInput struct {
	Now                         time.Time
	CurrentTargetID             int64
	AutoFailback                bool
	LastSwitchAt                time.Time
	Cooldown                    time.Duration
	FailureThreshold            int
	SuccessThreshold            int
	SingleProbeFailureThreshold int
	SingleProbeSuccessThreshold int
	Targets                     []dnsFailoverTargetSnapshot
	Probes                      []dnsFailoverProbeSnapshot
	States                      []dnsFailoverProbeTargetSnapshot
}

type dnsFailoverTargetSnapshot struct {
	ID      int64
	Sort    int
	Enabled bool
}

type dnsFailoverProbeSnapshot struct {
	ID     int64
	Online bool
}

type dnsFailoverProbeTargetSnapshot struct {
	ProbeID       int64
	TargetID      int64
	SuccessStreak int
	FailureStreak int
}

func decideDNSFailover(input dnsFailoverDecisionInput) dnsFailoverDecision {
	heartbeatOnlineProbeIDs := make(map[int64]struct{}, len(input.Probes))
	for _, probe := range input.Probes {
		if probe.Online {
			heartbeatOnlineProbeIDs[probe.ID] = struct{}{}
		}
	}
	if len(heartbeatOnlineProbeIDs) == 0 {
		return dnsFailoverDecision{Action: dnsFailoverActionNone, Reason: dnsFailoverReasonAllProbesOffline}
	}

	if !input.LastSwitchAt.IsZero() && input.Cooldown > 0 && input.Now.Before(input.LastSwitchAt.Add(input.Cooldown)) {
		return dnsFailoverDecision{Action: dnsFailoverActionNone, Reason: dnsFailoverReasonCooldown}
	}

	availableProbeIDs := dnsFailoverDecisionAvailableProbeIDs(input.Probes, input.States, input.CurrentTargetID)
	if len(availableProbeIDs) == 0 {
		return dnsFailoverDecision{Action: dnsFailoverActionNone, Reason: dnsFailoverReasonNoProbeData}
	}

	failureThreshold := input.FailureThreshold
	successThreshold := input.SuccessThreshold
	if len(availableProbeIDs) == 1 {
		failureThreshold = input.SingleProbeFailureThreshold
		successThreshold = input.SingleProbeSuccessThreshold
	}

	type stateKey struct {
		probeID  int64
		targetID int64
	}
	stateByProbeTarget := make(map[stateKey]dnsFailoverProbeTargetSnapshot, len(input.States))
	for _, state := range input.States {
		stateByProbeTarget[stateKey{probeID: state.ProbeID, targetID: state.TargetID}] = state
	}

	allMeet := func(targetID int64, threshold int, success bool) bool {
		for probeID := range availableProbeIDs {
			state, ok := stateByProbeTarget[stateKey{probeID: probeID, targetID: targetID}]
			if !ok {
				return false
			}
			streak := state.FailureStreak
			if success {
				streak = state.SuccessStreak
			}
			if streak < threshold {
				return false
			}
		}
		return true
	}
	anyMeet := func(targetID int64, threshold int) bool {
		for probeID := range availableProbeIDs {
			if state, ok := stateByProbeTarget[stateKey{probeID: probeID, targetID: targetID}]; ok && state.FailureStreak >= threshold {
				return true
			}
		}
		return false
	}

	targets := append([]dnsFailoverTargetSnapshot(nil), input.Targets...)
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].Sort == targets[j].Sort {
			return targets[i].ID < targets[j].ID
		}
		return targets[i].Sort < targets[j].Sort
	})

	if allMeet(input.CurrentTargetID, failureThreshold, false) {
		for _, target := range targets {
			if !target.Enabled || target.ID == input.CurrentTargetID {
				continue
			}
			if allMeet(target.ID, successThreshold, true) {
				return dnsFailoverDecision{
					Action:   dnsFailoverActionFailover,
					TargetID: target.ID,
					Reason:   dnsFailoverReasonCurrentFailed,
				}
			}
		}
		return dnsFailoverDecision{Action: dnsFailoverActionNone, Reason: dnsFailoverReasonNoHealthyTarget}
	}

	if input.AutoFailback {
		var currentSort int
		var currentFound bool
		for _, target := range targets {
			if target.ID == input.CurrentTargetID {
				currentSort = target.Sort
				currentFound = true
				break
			}
		}
		if currentFound {
			for _, target := range targets {
				if target.Sort >= currentSort {
					break
				}
				if target.Enabled && allMeet(target.ID, successThreshold, true) {
					return dnsFailoverDecision{
						Action:   dnsFailoverActionFailback,
						TargetID: target.ID,
						Reason:   dnsFailoverReasonHigherPriorityRecovered,
					}
				}
			}
		}
	}

	if len(availableProbeIDs) > 1 && anyMeet(input.CurrentTargetID, failureThreshold) {
		return dnsFailoverDecision{Action: dnsFailoverActionNone, Reason: dnsFailoverReasonProbeDisagreement}
	}
	hasCurrentState := false
	hasCurrentFailure := false
	for probeID := range availableProbeIDs {
		if state, ok := stateByProbeTarget[stateKey{probeID: probeID, targetID: input.CurrentTargetID}]; ok {
			hasCurrentState = true
			hasCurrentFailure = hasCurrentFailure || state.FailureStreak > 0
		}
	}
	if !hasCurrentState {
		return dnsFailoverDecision{Action: dnsFailoverActionNone, Reason: dnsFailoverReasonNoProbeData}
	}
	if hasCurrentFailure {
		return dnsFailoverDecision{Action: dnsFailoverActionNone, Reason: dnsFailoverReasonFailureThresholdPending}
	}
	return dnsFailoverDecision{Action: dnsFailoverActionNone, Reason: dnsFailoverReasonCurrentHealthy}
}

func dnsFailoverDecisionAvailableProbeIDs(probes []dnsFailoverProbeSnapshot, states []dnsFailoverProbeTargetSnapshot, currentTargetID int64) map[int64]struct{} {
	probesWithFreshCurrentState := make(map[int64]struct{}, len(states))
	for _, state := range states {
		if state.TargetID == currentTargetID {
			probesWithFreshCurrentState[state.ProbeID] = struct{}{}
		}
	}
	available := make(map[int64]struct{}, len(probes))
	for _, probe := range probes {
		if !probe.Online {
			continue
		}
		if _, ok := probesWithFreshCurrentState[probe.ID]; ok {
			available[probe.ID] = struct{}{}
		}
	}
	return available
}

func dnsFailoverDecisionAvailableProbeIDList(probes []dnsFailoverProbeSnapshot, states []dnsFailoverProbeTargetSnapshot, currentTargetID int64) []int64 {
	available := dnsFailoverDecisionAvailableProbeIDs(probes, states, currentTargetID)
	ids := make([]int64, 0, len(available))
	for probeID := range available {
		ids = append(ids, probeID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}
