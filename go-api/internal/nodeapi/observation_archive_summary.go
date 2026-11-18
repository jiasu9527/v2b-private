package nodeapi

import "sort"

type observationTargetSummary struct {
	events int64
	users  map[int64]bool
}
type observationSummaryAccumulator struct {
	events, known int64
	groups        int
	users         map[int64]bool
	domains, ips  map[string]*observationTargetSummary
}

func newObservationSummaryAccumulator() *observationSummaryAccumulator {
	return &observationSummaryAccumulator{users: make(map[int64]bool), domains: make(map[string]*observationTargetSummary), ips: make(map[string]*observationTargetSummary)}
}
func (a *observationSummaryAccumulator) add(user int64, host, ip string, count int64) error {
	a.groups++
	if a.groups > 2000000 {
		return ErrObservationQueryTimeout
	}
	a.events += count
	a.users[user] = true
	if host != "" {
		a.known += count
		a.target(a.domains, host, user, count)
	}
	if ip != "" {
		a.target(a.ips, ip, user, count)
	}
	return nil
}
func (a *observationSummaryAccumulator) target(targets map[string]*observationTargetSummary, key string, user, count int64) {
	x := targets[key]
	if x == nil {
		x = &observationTargetSummary{users: make(map[int64]bool)}
		targets[key] = x
	}
	x.events += count
	x.users[user] = true
}
func (a *observationSummaryAccumulator) result() map[string]any {
	top := func(targets map[string]*observationTargetSummary, field string) []map[string]any {
		keys := make([]string, 0, len(targets))
		for k := range targets {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			x, y := targets[keys[i]], targets[keys[j]]
			if len(x.users) != len(y.users) {
				return len(x.users) > len(y.users)
			}
			if x.events != y.events {
				return x.events > y.events
			}
			return keys[i] < keys[j]
		})
		if len(keys) > 30 {
			keys = keys[:30]
		}
		out := make([]map[string]any, 0, len(keys))
		for _, k := range keys {
			out = append(out, map[string]any{field: k, "users": int64(len(targets[k].users)), "events": targets[k].events})
		}
		return out
	}
	return map[string]any{"events": a.events, "users": int64(len(a.users)), "known_domain_events": a.known, "unknown_domain_events": a.events - a.known, "top_domains": top(a.domains, "domain"), "top_ips": top(a.ips, "ip")}
}
