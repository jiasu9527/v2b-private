package aiops

import "testing"

func TestPanelRecoveryInvestigationReadOnly(t *testing.T) {
	for _, name := range []string{"entry_evidence.investigation", "entry_evidence.investigation_list", "entry_evidence.cases"} {
		if !panelRecoveryReadOnlyOperation(name) {
			t.Fatal("persisted read missing", name)
		}
	}
	for _, name := range []string{"entry_evidence.investigation_start", "entry_evidence.investigation_refresh", "entry_evidence.investigation_confirm", "entry_evidence.investigation_next", "entry_evidence.case"} {
		if panelRecoveryReadOnlyOperation(name) {
			t.Fatal("mutating refresh in recovery", name)
		}
	}
}
