package admin

// Investigation decisions supplement frozen evidence. They never identify a
// person as responsible and the maintenance worker never changes routing.
type EntryInvestigationStartRequest struct {
	CaseID            int64  `json:"case_id"`
	Revision          int64  `json:"revision"`
	BlockedAt         int64  `json:"blocked_at"`
	Note              string `json:"note"`
	MinObserveSeconds int64  `json:"min_observe_seconds,omitempty"`
	MaxRounds         int    `json:"max_rounds,omitempty"`
	StopUsers         int64  `json:"stop_users,omitempty"`
}
type EntryInvestigationConfirmRequest struct {
	CaseID        int64  `json:"case_id"`
	Revision      int64  `json:"revision"`
	Branch        string `json:"branch"`
	BlockedAt     int64  `json:"blocked_at"`
	Note          string `json:"note"`
	ControlStatus string `json:"control_status"`
}
type EntryInvestigationNextRequest struct {
	CaseID   int64 `json:"case_id"`
	Revision int64 `json:"revision"`
}
type EntryInvestigationFailure struct {
	Branch        string `json:"branch"`
	BlockedAt     int64  `json:"blocked_at"`
	ConfirmedAt   int64  `json:"confirmed_at"`
	ConfirmedBy   int64  `json:"confirmed_by"`
	Note          string `json:"note"`
	ControlStatus string `json:"control_status"`
}
type EntryInvestigationBranch struct {
	Branch          string `json:"branch"`
	GroupID         int64  `json:"group_id"`
	Host            string `json:"host"`
	AssignedUsers   int64  `json:"assigned_users"`
	AcquiredUsers   int64  `json:"acquired_users"`
	UnrecordedUsers int64  `json:"unrecorded_users"`
	PreFailureUsers int64  `json:"pre_failure_users"`
	Health          string `json:"health"`
	LastReportedAt  int64  `json:"last_reported_at"`
	BlockedAt       int64  `json:"blocked_at,omitempty"`
}
type EntryInvestigationReport struct {
	EvidenceRisks        []string                   `json:"evidence_risks"`
	State                string                     `json:"state"`
	Reason               string                     `json:"reason"`
	CheckedAt            int64                      `json:"checked_at"`
	ExecutedAt           int64                      `json:"executed_at,omitempty"`
	ObservedSeconds      int64                      `json:"observed_seconds"`
	Branches             []EntryInvestigationBranch `json:"branches"`
	Warnings             []string                   `json:"warnings"`
	Blockers             []string                   `json:"blockers"`
	NextQuery            *EntryEvidenceQueryRequest `json:"next_query,omitempty"`
	RequiresConfirmation bool                       `json:"requires_confirmation"`
}
type EntryInvestigation struct {
	CaseID     int64                          `json:"case_id"`
	RootCaseID int64                          `json:"root_case_id"`
	Round      int                            `json:"round"`
	Revision   int64                          `json:"revision"`
	CreatedBy  int64                          `json:"created_by"`
	CreatedAt  int64                          `json:"created_at"`
	UpdatedAt  int64                          `json:"updated_at"`
	Settings   EntryInvestigationStartRequest `json:"settings"`
	Failures   []EntryInvestigationFailure    `json:"failures"`
	Report     EntryInvestigationReport       `json:"report"`
	NextCaseID int64                          `json:"next_case_id,omitempty"`
}
type EntryInvestigationsPage struct {
	Items    []EntryInvestigation `json:"items"`
	Total    int64                `json:"total"`
	Current  int                  `json:"current"`
	PageSize int                  `json:"page_size"`
}
