package aiops

type KnowledgeEntry struct {
	ID              int64  `json:"id"`
	Title           string `json:"title"`
	Summary         string `json:"summary"`
	Content         string `json:"content,omitempty"`
	Category        string `json:"category"`
	Status          string `json:"status"`
	Pinned          bool   `json:"pinned"`
	Version         int64  `json:"version"`
	SourceKind      string `json:"source_kind"`
	SourceNote      string `json:"source_note"`
	SourceSessionID int64  `json:"source_session_id"`
	SourceTurnID    int64  `json:"source_turn_id"`
	CreatedBy       int64  `json:"created_by"`
	UpdatedBy       int64  `json:"updated_by"`
	CreatedAt       int64  `json:"created_at"`
	UpdatedAt       int64  `json:"updated_at"`
}

type KnowledgeListRequest struct {
	Query    string `json:"query"`
	Status   string `json:"status"`
	Category string `json:"category"`
	Current  int    `json:"current"`
	PageSize int    `json:"page_size"`
}

type KnowledgeListResult struct {
	Data     []KnowledgeEntry `json:"data"`
	Total    int64            `json:"total"`
	Current  int              `json:"current"`
	PageSize int              `json:"page_size"`
}

type KnowledgeSaveRequest struct {
	ID              int64  `json:"id"`
	Title           string `json:"title"`
	Summary         string `json:"summary"`
	Content         string `json:"content"`
	Category        string `json:"category"`
	Status          string `json:"status"`
	Pinned          bool   `json:"pinned"`
	ExpectedVersion *int64 `json:"expected_version,omitempty"`
}
