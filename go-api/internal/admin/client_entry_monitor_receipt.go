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

// Upserts replace the supplied policies only; omitted monitors and their
// targets, states and automatic-split settings remain untouched.
type ClientEntryMonitorUpsertService interface {
	UpsertClientEntryMonitorReceipt(context.Context, ClientEntryMonitorSaveRequest) (ClientEntryMonitorSaveReceipt, error)
}

// ClientEntryMonitorValidationError is produced only by controlled validation
// before monitor configuration writes. SQL failures and uncertain commits must
// never be converted to this type.
type ClientEntryMonitorValidationError struct{ message string }

func (e *ClientEntryMonitorValidationError) Error() string { return e.message }

func clientEntryMonitorValidation(message string) error {
	return &ClientEntryMonitorValidationError{message: message}
}
