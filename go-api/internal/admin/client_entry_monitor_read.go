package admin

import "context"

// ReadClientEntryMonitors reads persisted targets and observations without the
// schema initialization or target synchronization done by the admin page's
// ListClientEntryMonitors. A missing/stale target must remain visible as such to
// a diagnostic caller; a read must not repair it or start a probe implicitly.
// Probes contain only enabled probe identities and heartbeat times, never keys.
func (s *DBService) ReadClientEntryMonitors(ctx context.Context) (ClientEntryMonitorOverview, error) {
	if s == nil || s.db == nil {
		return ClientEntryMonitorOverview{}, ErrUnavailable
	}
	revision, err := s.loadClientEntryMonitorRevision(ctx)
	if err != nil {
		return ClientEntryMonitorOverview{}, err
	}
	policies, err := s.listClientEntryUserPolicies(ctx, false)
	if err != nil {
		return ClientEntryMonitorOverview{}, err
	}
	nodes, err := s.loadClientEntryMonitorNodes(ctx, policies)
	if err != nil {
		return ClientEntryMonitorOverview{}, err
	}
	items, err := s.loadClientEntryMonitorRecords(ctx)
	if err != nil {
		return ClientEntryMonitorOverview{}, err
	}
	identities, err := s.loadSupportMonitorProbes(ctx)
	if err != nil {
		return ClientEntryMonitorOverview{}, err
	}
	probes := make([]DNSProbeRecord, 0, len(identities))
	for _, identity := range identities {
		probe := DNSProbeRecord{ID: identity.ID, Name: identity.Name, Enabled: true}
		if identity.LastHeartbeatAt.Valid {
			timestamp := identity.LastHeartbeatAt.Int64
			probe.LastHeartbeatAt = &timestamp
		}
		probes = append(probes, probe)
	}
	current, err := s.loadClientEntryMonitorRevision(ctx)
	if err != nil {
		return ClientEntryMonitorOverview{}, err
	}
	if current != revision {
		return ClientEntryMonitorOverview{}, ErrClientEntryMonitorRevisionConflict
	}
	return ClientEntryMonitorOverview{Revision: revision, Items: items, Policies: buildClientEntryMonitorPolicies(policies, nodes), Probes: probes}, nil
}
