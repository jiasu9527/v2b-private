package admin

import "errors"

var (
	ErrClientEntryRetirementInvalid  = errors.New("入口组回收参数无效")
	ErrClientEntryRetirementConflict = errors.New("入口组、名单或配置已变化，请重新预览")
	ErrClientEntryRetirementBlocked  = errors.New("入口组暂不能回收")
	ErrClientEntryRetirementNotFound = errors.New("入口组回收预览不存在")
)

type ClientEntryRetirementPreviewRequest struct {
	GroupIDs            []int64 `json:"group_ids"`
	ReplacementGroupIDs []int64 `json:"replacement_group_ids,omitempty"`
	AllowFallback       bool    `json:"allow_fallback"`
	FallbackPolicyID    int64   `json:"fallback_policy_id,omitempty"`
	Reason              string  `json:"reason"`
}

type ClientEntryRetirementExecuteRequest struct {
	PreviewID      int64  `json:"preview_id"`
	PreviewHash    string `json:"preview_hash"`
	IdempotencyKey string `json:"idempotency_key"`
}

type ClientEntryRetirementGroup struct {
	GroupID       int64  `json:"group_id"`
	PolicyID      int64  `json:"policy_id"`
	Name          string `json:"name"`
	Host          string `json:"host"`
	AssignedUsers int64  `json:"assigned_users"`
}

type ClientEntryRetirementIP struct {
	Host                string  `json:"host"`
	PoolIDs             []int64 `json:"pool_ids"`
	RemainingReferences int64   `json:"remaining_references"`
	Releasable          bool    `json:"releasable"`
}

type ClientEntryRetirementPreview struct {
	PreviewID               int64                        `json:"preview_id"`
	PreviewHash             string                       `json:"preview_hash"`
	ExpiresAt               int64                        `json:"expires_at"`
	CanExecute              bool                         `json:"can_execute"`
	SelectedGroups          []ClientEntryRetirementGroup `json:"selected_groups"`
	RemovedAncestorIDs      []int64                      `json:"removed_ancestor_ids"`
	RemovedPolicyIDs        []int64                      `json:"removed_policy_ids"`
	AssignedUsers           int64                        `json:"assigned_users"`
	ExistingUsers           int64                        `json:"existing_users"`
	DeletedUsers            int64                        `json:"deleted_users"`
	ReplacementCoveredUsers int64                        `json:"replacement_covered_users"`
	FallbackUsers           int64                        `json:"fallback_users"`
	UncoveredUsers          int64                        `json:"uncovered_users"`
	RemovedAssignments      int64                        `json:"removed_assignments"`
	RemovedMonitorTargets   int64                        `json:"removed_monitor_targets"`
	IPs                     []ClientEntryRetirementIP    `json:"ips"`
	Blockers                []string                     `json:"blockers"`
	Warnings                []string                     `json:"warnings"`
}

type ClientEntryRetirementReceipt struct {
	PreviewID                   int64    `json:"preview_id"`
	OperationID                 int64    `json:"operation_id,omitempty"`
	Status                      string   `json:"status"`
	Committed                   bool     `json:"committed"`
	Replayed                    bool     `json:"replayed"`
	AppliedAt                   int64    `json:"applied_at,omitempty"`
	RemovedGroups               int64    `json:"removed_groups"`
	RemovedAncestors            int64    `json:"removed_ancestors"`
	RemovedPolicyIDs            []int64  `json:"removed_policy_ids"`
	RemovedAssignments          int64    `json:"removed_assignments"`
	RemovedMonitorTargets       int64    `json:"removed_monitor_targets"`
	CancelledPendingOperations  int64    `json:"cancelled_pending_operations"`
	ReleasedIPs                 []string `json:"released_ips"`
	RetainedIPs                 []string `json:"retained_ips"`
	MonitorRevision             int64    `json:"monitor_revision,omitempty"`
	PreservedOtherConfiguration bool     `json:"preserved_other_configuration"`
}
