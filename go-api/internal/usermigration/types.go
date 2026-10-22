// Package usermigration imports only ordinary users from a read-only Xboard
// snapshot. Connection credentials never leave the inspect/preview call.
package usermigration

import (
	"errors"
	"time"
)

var (
	ErrUnavailable             = errors.New("user migration unavailable")
	ErrInvalidParameter        = errors.New("invalid migration parameter")
	ErrSourceConnection        = errors.New("cannot connect to source MySQL; check connection details and network access")
	ErrSourceRead              = errors.New("cannot read source Xboard snapshot; check read-only permissions and schema")
	ErrSourceSchema            = errors.New("unsupported source Xboard schema")
	ErrSourceLimit             = errors.New("source exceeds migration limit of 200000 users")
	ErrTargetRead              = errors.New("cannot read migration target")
	ErrTargetWrite             = errors.New("migration database operation failed; query operation status before retrying")
	ErrBusy                    = errors.New("another migration operation is in progress; try again later")
	ErrPreviewExpired          = errors.New("migration preview not found or expired; create a new preview")
	ErrPreviewBlocked          = errors.New("migration preview contains blocking issues")
	ErrPreviewStale            = errors.New("target users or mapped plans changed; create a new preview")
	ErrReceiptNotFound         = errors.New("migration receipt not found")
	ErrMailTrackingUnavailable = errors.New("migration batch has no precise recipient tracking")
	ErrMailUnavailable         = errors.New("migration mail is not configured or cannot be prepared")
	ErrMailQueueUnavailable    = errors.New("migration mail queue is unavailable")
	ErrMailNoRecipients        = errors.New("migration batch has no eligible recipients")
	ErrMailConflict            = errors.New("migration mail request id was already used with different content")
	ErrMailBusy                = errors.New("this migration batch already has an active mail task")
	ErrCommitUnknown           = errors.New("migration commit outcome is unknown; query operation status before creating another task")
)

const (
	PreviewTTL       = 10 * time.Minute
	MaxSourceUsers   = 200000
	maxTargetUsers   = 2000000
	maxPreviews      = 4
	operationTimeout = 110 * time.Second
)

type SourceConfig struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Database string `json:"database"`
	Username string `json:"username"`
	Password string `json:"password"`
	TLS      bool   `json:"tls"`
}

type Plan struct {
	ID                 int64  `json:"id"`
	Name               string `json:"name"`
	Users              int    `json:"users"`
	TransferEnable     int64  `json:"transfer_enable"`
	GroupID            int64  `json:"group_id"`
	DeviceLimit        *int64 `json:"device_limit"`
	SpeedLimit         *int64 `json:"speed_limit"`
	ResetTrafficMethod *int64 `json:"reset_traffic_method"`
}

type Inspection struct {
	TotalUsers          int      `json:"total_users"`
	SourceAdmins        int      `json:"source_admins"`
	SourceStaff         int      `json:"source_staff"`
	LockingBalanceUsers int      `json:"locking_balance_users"`
	LockingBalanceTotal int64    `json:"locking_balance_total"`
	SourcePlans         []Plan   `json:"source_plans"`
	TargetPlans         []Plan   `json:"target_plans"`
	Warnings            []string `json:"warnings"`
}

type PreviewRequest struct {
	Source                SourceConfig     `json:"source"`
	PlanMapping           map[string]int64 `json:"plan_mapping"`
	LockingBalancePolicy  string           `json:"locking_balance_policy"`
	ConfirmLockingBalance bool             `json:"confirm_locking_balance"`
	RegenerateTokens      bool             `json:"regenerate_tokens"`
	RegenerateUUIDs       bool             `json:"regenerate_uuids"`
}

type Counts struct {
	Total                 int   `json:"total"`
	Importable            int   `json:"importable"`
	SkippedPrivileged     int   `json:"skipped_privileged"`
	SkippedExisting       int   `json:"skipped_existing"`
	SkippedDuplicateEmail int   `json:"skipped_duplicate_email"`
	SkippedLockedBalance  int   `json:"skipped_locked_balance"`
	SkippedPlan           int   `json:"skipped_plan"`
	InvalidEmail          int   `json:"invalid_email"`
	UnsupportedPassword   int   `json:"unsupported_password"`
	InvalidValues         int   `json:"invalid_values"`
	UnmappedPlan          int   `json:"unmapped_plan"`
	NoPlan                int   `json:"no_plan"`
	RegeneratedTokens     int   `json:"regenerated_tokens"`
	RegeneratedUUIDs      int   `json:"regenerated_uuids"`
	LockingBalanceUsers   int   `json:"locking_balance_users"`
	LockingBalanceTotal   int64 `json:"locking_balance_total"`
}

type PreviewResult struct {
	PreviewID            string   `json:"preview_id"`
	ExpiresAt            int64    `json:"expires_at"`
	Counts               Counts   `json:"counts"`
	Blockers             []string `json:"blockers"`
	Warnings             []string `json:"warnings"`
	CanApply             bool     `json:"can_apply"`
	SourceDigest         string   `json:"source_digest"`
	LockingBalancePolicy string   `json:"locking_balance_policy"`
	RegenerateTokens     bool     `json:"regenerate_tokens"`
	RegenerateUUIDs      bool     `json:"regenerate_uuids"`
}

type ApplyRequest struct {
	PreviewID string `json:"preview_id"`
	Confirm   bool   `json:"confirm"`
}

type Receipt struct {
	MailTrackingAvailable bool   `json:"mail_tracking_available"`
	PreviewID             string `json:"preview_id"`
	Status                string `json:"status"`
	Imported              int    `json:"imported"`
	Counts                Counts `json:"counts"`
	SourceDigest          string `json:"source_digest"`
	LockingBalancePolicy  string `json:"locking_balance_policy"`
	RegenerateTokens      bool   `json:"regenerate_tokens"`
	RegenerateUUIDs       bool   `json:"regenerate_uuids"`
	CreatedAt             int64  `json:"created_at"`
	CompletedAt           int64  `json:"completed_at"`
}

// sourceUser deliberately excludes source administrative grants, referral links,
// orders, payment credentials, sessions and node configuration from any writes.
type sourceUser struct {
	ID                                                                  int64
	Email, Password, PasswordAlgo, Token, UUID                          string
	Balance, CommissionBalance, LockingBalance, U, D, T, TransferEnable int64
	PlanID, ExpiredAt                                                   *int64
	Banned, IsAdmin, IsStaff                                            int64
	CreatedAt, UpdatedAt                                                int64
}

type snapshot struct {
	users []sourceUser
	plans []Plan
}

type insertUser struct {
	sourceUser
	TargetPlan *Plan
}

type targetState struct {
	plans                 map[int64]Plan
	emails, tokens, uuids map[string]bool
}

type storedPreview struct {
	actorID     int64
	result      PreviewResult
	users       []insertUser
	plans       map[int64]Plan
	resetMethod int64
	state       string
	expired     bool
	expiryTimer *time.Timer
}
