package admin

import (
	"encoding/json"
	"errors"
	"forest/go-api/internal/cliententry"
)

var ErrEntryEvidenceInvalid = errors.New("入口排查参数无效")
var ErrEntryEvidenceConflict = errors.New("入口排查快照已变化，请重新预览并冻结名单")
var ErrEntryEvidenceForbidden = errors.New("无权操作入口排查证据")
var ErrEntryEvidenceNotFound = errors.New("入口排查档案不存在")

const entryEvidenceMaxCandidates = 10000

type EntryEvidenceQueryRequest struct {
	Kind     string `json:"kind"`
	TargetID int64  `json:"target_id"`
	Host     string `json:"host"`
	Port     int64  `json:"port"`
	From     int64  `json:"from"`
	To       int64  `json:"to"`
	UA       string `json:"ua,omitempty"`
	Account  string `json:"account,omitempty"`
	Current  int    `json:"current"`
	PageSize int    `json:"page_size"`
}
type EntryEvidenceReference struct {
	RequestID    string `json:"request_id"`
	CompletedAt  int64  `json:"completed_at"`
	UA           string `json:"ua"`
	RuleRevision string `json:"rule_revision"`
	ServerType   string `json:"server_type"`
	ServerID     int64  `json:"server_id"`
	Host         string `json:"host"`
	Port         string `json:"port"`
}
type EntryEvidenceCandidate struct {
	Evidence          []EntryEvidenceReference `json:"evidence"`
	EvidenceTotal     int64                    `json:"evidence_total"`
	EvidenceTruncated bool                     `json:"evidence_truncated"`
	UserID            int64                    `json:"user_id"`
	Email             string                   `json:"email"`
	FirstCompletedAt  int64                    `json:"first_completed_at"`
	LastCompletedAt   int64                    `json:"last_completed_at"`
	RequestCount      int64                    `json:"request_count"`
	Nodes             []AIEntryRuleMember      `json:"nodes"`
	UAs               []string                 `json:"uas"`
	Selected          bool                     `json:"selected"`
	Branch            string                   `json:"branch,omitempty"`
}
type EntryEvidenceCandidatePreview struct {
	Target            AIEntryRuleSnapshot       `json:"target"`
	Query             EntryEvidenceQueryRequest `json:"query"`
	Total             int64                     `json:"total"`
	Items             []EntryEvidenceCandidate  `json:"items"`
	Current           int                       `json:"current"`
	PageSize          int                       `json:"page_size"`
	Supported         bool                      `json:"supported"`
	FreezeAllowed     bool                      `json:"freeze_allowed"`
	UnsupportedReason string                    `json:"unsupported_reason,omitempty"`
	CandidatesHash    string                    `json:"candidates_hash"`
	SourceUserCount   int64                     `json:"source_user_count"`
}
type CreateEntryEvidenceCaseRequest struct {
	EntryEvidenceQueryRequest
	Revision               string  `json:"revision"`
	ExpectedCandidatesHash string  `json:"expected_candidates_hash"`
	SelectedUserIDs        []int64 `json:"selected_user_ids,omitempty"`
	Title                  string  `json:"title,omitempty"`
	ParentCaseID           int64   `json:"parent_case_id,omitempty"`
}
type EntryEvidenceSplitRequest struct {
	CaseID          int64  `json:"case_id"`
	Revision        int64  `json:"revision"`
	UseBackupIPPool bool   `json:"use_backup_ip_pool"`
	EntryHostA      string `json:"entry_host_a,omitempty"`
	EntryHostB      string `json:"entry_host_b,omitempty"`
}
type EntryEvidenceGroupPreview struct {
	Branch    string `json:"branch"`
	UserCount int64  `json:"user_count"`
	EntryHost string `json:"entry_host"`
	PolicyID  int64  `json:"policy_id,omitempty"`
	GroupID   int64  `json:"group_id,omitempty"`
	Unchanged bool   `json:"unchanged"`
}
type EntryEvidenceSplitPreview struct {
	CaseID            int64                       `json:"case_id"`
	Revision          int64                       `json:"revision"`
	Mode              string                      `json:"mode"`
	Groups            []EntryEvidenceGroupPreview `json:"groups"`
	Conditions        []cliententry.Condition     `json:"conditions"`
	Members           []AIEntryRuleMember         `json:"members"`
	Warnings          []string                    `json:"warnings"`
	Supported         bool                        `json:"supported"`
	UnsupportedReason string                      `json:"unsupported_reason,omitempty"`
}
type EntryEvidenceExecution struct {
	MonitoringStatus  string                    `json:"monitoring_status,omitempty"`
	MonitoringWarning string                    `json:"monitoring_warning,omitempty"`
	EntryHostA        string                    `json:"entry_host_a"`
	EntryHostB        string                    `json:"entry_host_b"`
	PolicyID          int64                     `json:"policy_id"`
	GroupAID          int64                     `json:"group_a_id"`
	GroupBID          int64                     `json:"group_b_id"`
	GroupRID          int64                     `json:"group_r_id,omitempty"`
	ExecutedAt        int64                     `json:"executed_at"`
	ExecutedBy        int64                     `json:"executed_by"`
	Request           EntryEvidenceSplitRequest `json:"request"`
}
type EntryEvidenceCase struct {
	ProbeEvents          []EntryEvidenceProbeEvent        `json:"probe_events,omitempty"`
	ProbeEventsTotal     int64                            `json:"probe_events_total"`
	ProbeEventsTruncated bool                             `json:"probe_events_truncated"`
	ProbeEventsGap       bool                             `json:"probe_events_gap"`
	Observations         []EntryEvidenceBranchObservation `json:"observations,omitempty"`
	ObservationWarning   string                           `json:"observation_warning,omitempty"`
	ID                   int64                            `json:"id"`
	Title                string                           `json:"title"`
	Status               string                           `json:"status"`
	Revision             int64                            `json:"revision"`
	CreatedBy            int64                            `json:"created_by"`
	CreatedAt            int64                            `json:"created_at"`
	UpdatedAt            int64                            `json:"updated_at"`
	ParentCaseID         int64                            `json:"parent_case_id,omitempty"`
	Target               AIEntryRuleSnapshot              `json:"target"`
	Query                EntryEvidenceQueryRequest        `json:"query"`
	CandidatesHash       string                           `json:"candidates_hash"`
	TotalCandidates      int64                            `json:"total_candidates"`
	SelectedCount        int64                            `json:"selected_count"`
	ResidualCount        int64                            `json:"residual_count"`
	SourceUserCount      int64                            `json:"source_user_count"`
	SourceUsersRevision  string                           `json:"source_users_revision"`
	TopologyRevision     string                           `json:"-"`
	Observation          string                           `json:"observation"`
	Conclusion           string                           `json:"conclusion"`
	Preview              EntryEvidenceSplitPreview        `json:"preview"`
	Execution            *EntryEvidenceExecution          `json:"execution,omitempty"`
	ProbeSnapshot        json.RawMessage                  `json:"probe_snapshot"`
}
type EntryEvidenceListRequest struct {
	Current  int `json:"current"`
	PageSize int `json:"page_size"`
}
type EntryEvidenceCasesPage struct {
	Items    []EntryEvidenceCase `json:"items"`
	Total    int64               `json:"total"`
	Current  int                 `json:"current"`
	PageSize int                 `json:"page_size"`
}
type EntryEvidenceCandidatesRequest struct {
	CaseID   int64 `json:"case_id"`
	Current  int   `json:"current"`
	PageSize int   `json:"page_size"`
	Selected *bool `json:"selected,omitempty"`
}
type EntryEvidenceCandidatesPage struct {
	Items    []EntryEvidenceCandidate `json:"items"`
	Total    int64                    `json:"total"`
	Current  int                      `json:"current"`
	PageSize int                      `json:"page_size"`
}
type EntryEvidenceObservationRequest struct {
	CaseID      int64  `json:"case_id"`
	Revision    int64  `json:"revision"`
	Observation string `json:"observation"`
	Conclusion  string `json:"conclusion"`
}
