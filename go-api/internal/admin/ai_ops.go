package admin

// InvalidateAIEntryTargets marks derived monitor targets stale after an AI
// action has already passed human approval and committed its narrow SQL update.
// It does not trigger a probe or expose any broad mutation capability.
func (s *DBService) InvalidateAIEntryTargets() { s.markClientEntryMonitorTargetsDirty() }
