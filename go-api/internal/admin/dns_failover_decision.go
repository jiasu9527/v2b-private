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
	dnsFailoverReasonHigherPriorityRecovered = "higher_priority_target_recovered"
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
	onlineProbeIDs := make(map[int64]struct{}, len(input.Probes))
	for _, probe := range input.Probes {
		if probe.Online {
			onlineProbeIDs[probe.ID] = struct{}{}
		}
	}
	if len(onlineProbeIDs) == 0 {
		return dnsFailoverDecision{Action: dnsFailoverActionNone, Reason: dnsFailoverReasonAllProbesOffline}
	}

	if !input.LastSwitchAt.IsZero() && input.Cooldown > 0 && input.Now.Before(input.LastSwitchAt.Add(input.Cooldown)) {
		return dnsFailoverDecision{Action: dnsFailoverActionNone, Reason: dnsFailoverReasonCooldown}
	}

	failureThreshold := input.FailureThreshold
	successThreshold := input.SuccessThreshold
	if len(onlineProbeIDs) == 1 {
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
		for probeID := range onlineProbeIDs {
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
		for probeID := range onlineProbeIDs {
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

	if len(onlineProbeIDs) > 1 && anyMeet(input.CurrentTargetID, failureThreshold) {
		return dnsFailoverDecision{Action: dnsFailoverActionNone, Reason: dnsFailoverReasonProbeDisagreement}
	}
	return dnsFailoverDecision{Action: dnsFailoverActionNone, Reason: dnsFailoverReasonCurrentHealthy}
}
