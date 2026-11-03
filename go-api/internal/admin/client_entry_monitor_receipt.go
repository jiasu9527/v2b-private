package admin

import "context"

// ClientEntryMonitorSaveReceipt is an acknowledgement of a committed complete
// configuration save, not a current-state snapshot. Counts describe the saved
// configuration (including unchanged items), not a field-level diff.
type ClientEntryMonitorSaveReceipt struct {
	Committed          bool  `json:"committed"`
	Revision           int64 `json:"revision"`
	SavedPolicyCount   int64 `json:"saved_policy_count"`
	SavedTargetCount   int64 `json:"saved_target_count"`
	DeletedPolicyCount int64 `json:"deleted_policy_count"`
}

// Optional to keep existing administrator integrations and overview consumers
// compatible. The compact HTTP endpoint must never fall back to the overview
// save: that could report a read failure after the configuration was committed.
type ClientEntryMonitorReceiptService interface {
	SaveClientEntryMonitorReceipt(context.Context, ClientEntryMonitorSaveRequest) (ClientEntryMonitorSaveReceipt, error)
}
