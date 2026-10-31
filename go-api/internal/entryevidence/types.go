package entryevidence

import "errors"

var ErrValidation = errors.New("invalid entry evidence parameters")

type Node struct {
	ServerType    string `json:"server_type"`
	ServerID      int64  `json:"server_id"`
	PolicyID      int64  `json:"policy_id"`
	GroupID       int64  `json:"group_id"`
	RuleRevision  string `json:"rule_revision"`
	SourceHost    string `json:"source_host"`
	DeliveredHost string `json:"delivered_host"`
	Port          string `json:"port"`
	Resolved      bool   `json:"resolved"`
	ProviderID    int64  `json:"provider_id"`
	CollectionID  int64  `json:"collection_id"`
	Source        string `json:"source"`
}

type Request struct {
	ID               string `json:"id"`
	UserID           int64  `json:"user_id"`
	EmailAtEvent     string `json:"email_at_event"`
	UA               string `json:"ua"`
	RequestedAt      int64  `json:"requested_at"`
	CompletedAt      int64  `json:"completed_at"`
	Transport        string `json:"transport"`
	Status           int    `json:"status"`
	ResponseComplete bool   `json:"response_complete"`
	Coverage         string `json:"coverage"`
	CoverageReason   string `json:"coverage_reason"`
	NodeCount        int    `json:"node_count"`
	BytesWritten     int64  `json:"bytes_written"`
	WriteError       string `json:"write_error"`
}

type Record struct {
	Request
	Nodes []Node `json:"nodes"`
}
type Filter struct {
	UserID       int64  `json:"user_id"`
	Email        string `json:"email"`
	UA           string `json:"ua"`
	From         int64  `json:"from"`
	To           int64  `json:"to"`
	PolicyID     int64  `json:"policy_id"`
	GroupID      int64  `json:"group_id"`
	Host         string `json:"host"`
	Port         string `json:"port"`
	CompleteOnly bool   `json:"complete_only"`
	Limit        int    `json:"limit"`
	Offset       int    `json:"offset"`
}
type Page struct {
	Items  []Record `json:"items"`
	Total  int64    `json:"total"`
	Limit  int      `json:"limit"`
	Offset int      `json:"offset"`
}
type Settings struct {
	KeepDays  int   `json:"keep_days"`
	UpdatedAt int64 `json:"updated_at"`
}
type Status struct {
	Requests           int64  `json:"requests"`
	Completed          int64  `json:"completed"`
	Partial            int64  `json:"partial"`
	Failed             int64  `json:"failed"`
	FirstAt            int64  `json:"first_at"`
	LastAt             int64  `json:"last_at"`
	WriteFailures      uint64 `json:"write_failures"`
	LastWriteFailureAt int64  `json:"last_write_failure_at"`
	KeepDays           int    `json:"keep_days"`
}
