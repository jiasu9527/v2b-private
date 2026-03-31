package admin

import (
	"context"
	crand "crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"

	"forest/go-api/internal/config"
	"forest/go-api/internal/payment"
	"forest/go-api/internal/queue"
	"forest/go-api/internal/session"
)

var ErrUnavailable = errors.New("admin service unavailable")

const scheduleLastCheckKey = "SCHEDULE_LAST_CHECK_AT_"

var monitoredQueueWorkloadNames = []string{
	"order_handle",
	"send_email",
	"send_email_mass",
	"send_telegram",
	"stat",
	"stat_refresh",
	"traffic_fetch",
}

type SystemStatus struct {
	Schedule            bool   `json:"schedule"`
	Horizon             bool   `json:"horizon"`
	ScheduleLastRuntime *int64 `json:"schedule_last_runtime"`
}

type QueueStats struct {
	FailedJobs             int64          `json:"failedJobs"`
	JobsPerMinute          int64          `json:"jobsPerMinute"`
	PausedMasters          int64          `json:"pausedMasters"`
	Periods                QueuePeriods   `json:"periods"`
	Processes              int64          `json:"processes"`
	QueueWithMaxRuntime    any            `json:"queueWithMaxRuntime"`
	QueueWithMaxThroughput any            `json:"queueWithMaxThroughput"`
	RecentJobs             int64          `json:"recentJobs"`
	Status                 bool           `json:"status"`
	Wait                   []QueueWaitJob `json:"wait"`
}

type QueuePeriods struct {
	FailedJobs int64 `json:"failedJobs"`
	RecentJobs int64 `json:"recentJobs"`
}

type QueueWaitJob struct {
	Name string `json:"name"`
	Time int64  `json:"time"`
}

type PaymentFormField = payment.FormField

type PaymentRecord struct {
	ID                 int64          `json:"id"`
	UUID               string         `json:"uuid"`
	Payment            string         `json:"payment"`
	Name               string         `json:"name"`
	Icon               *string        `json:"icon"`
	Config             map[string]any `json:"config"`
	NotifyDomain       *string        `json:"notify_domain"`
	HandlingFeeFixed   *int64         `json:"handling_fee_fixed"`
	HandlingFeePercent *float64       `json:"handling_fee_percent"`
	Enable             int64          `json:"enable"`
	Sort               *int64         `json:"sort"`
	CreatedAt          int64          `json:"created_at"`
	UpdatedAt          int64          `json:"updated_at"`
	NotifyURL          string         `json:"notify_url"`
}

type PaymentSaveRequest struct {
	ID                 *int64
	Name               string
	Icon               *string
	Payment            string
	Config             map[string]string
	NotifyDomain       *string
	HandlingFeeFixed   *int64
	HandlingFeePercent *float64
}

type TicketRecord struct {
	ID          int64  `json:"id"`
	UserID      int64  `json:"user_id"`
	Subject     string `json:"subject"`
	Level       int64  `json:"level"`
	Status      int64  `json:"status"`
	ReplyStatus int64  `json:"reply_status"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
}

type TicketMessageRecord struct {
	ID        int64  `json:"id"`
	UserID    int64  `json:"user_id"`
	TicketID  int64  `json:"ticket_id"`
	Message   string `json:"message"`
	IsMe      bool   `json:"is_me"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

type TicketDetail struct {
	TicketRecord
	Messages []TicketMessageRecord `json:"message"`
}

type TicketListRequest struct {
	Current     int64
	PageSize    int64
	Status      *int64
	ReplyStatus []int64
	Email       string
}

type TicketListResult struct {
	Data  []TicketRecord `json:"data"`
	Total int64          `json:"total"`
}

type TicketReplyRequest struct {
	ID      int64
	Message string
	AdminID int64
}

type ConfigMailTestLog map[string]any

type InviteCampaignFilter struct {
	Key       string
	Condition string
	Value     string
}

type InviteCampaignListRequest struct {
	Current  int64
	PageSize int64
	Filters  []InviteCampaignFilter
}

type InviteCampaignListResult struct {
	Data  []map[string]any `json:"data"`
	Total int64            `json:"total"`
}

type InviteCampaignRecordListRequest struct {
	CampaignID int64
	Current    int64
	PageSize   int64
}

type InviteCampaignRecordListResult struct {
	Data  []map[string]any `json:"data"`
	Total int64            `json:"total"`
}

type ServerGroupRecord struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	UserCount   int64  `json:"user_count,omitempty"`
	ServerCount int64  `json:"server_count,omitempty"`
	CreatedAt   int64  `json:"created_at,omitempty"`
	UpdatedAt   int64  `json:"updated_at,omitempty"`
}

type ServerGroupSaveRequest struct {
	ID   *int64
	Name string
}

type ServerRouteRecord struct {
	ID          int64    `json:"id"`
	Remarks     string   `json:"remarks"`
	Match       []string `json:"match"`
	Action      string   `json:"action"`
	ActionValue *string  `json:"action_value"`
	CreatedAt   int64    `json:"created_at,omitempty"`
	UpdatedAt   int64    `json:"updated_at,omitempty"`
}

type ServerRouteSaveRequest struct {
	ID          *int64
	Remarks     string
	Match       []string
	Action      string
	ActionValue *string
}

type ManagedServerHostUpdateResult struct {
	UpdatedTotal   int64            `json:"updated_total"`
	UpdatedByTable map[string]int64 `json:"updated_by_table"`
}

type Service interface {
	GetSystemStatus(ctx context.Context) (SystemStatus, error)
	GetQueueStats(ctx context.Context) (QueueStats, error)
	GetQueueWorkload(ctx context.Context) ([]map[string]any, error)
	ListSystemLogs(ctx context.Context, current, pageSize int64, level string) ([]map[string]any, int64, error)
	GetStat(ctx context.Context, startAt, endAt int64) (map[string]any, error)
	GetStatOverride(ctx context.Context) (map[string]any, error)
	GetStatOrder(ctx context.Context) ([]map[string]any, error)
	GetRanking(ctx context.Context, rankingType string, startAt, endAt, limit int64) ([]map[string]any, error)
	GetStatRecord(ctx context.Context, statType string, startAt, endAt int64) ([]map[string]any, error)
	GetServerLastRank(ctx context.Context) ([]map[string]any, error)
	GetServerTodayRank(ctx context.Context) ([]map[string]any, error)
	GetUserLastRank(ctx context.Context) ([]map[string]any, error)
	GetUserTodayRank(ctx context.Context) ([]map[string]any, error)
	GetStatUser(ctx context.Context, userID, current, pageSize int64) ([]map[string]any, int64, error)
	ListUsers(ctx context.Context, req UserFetchRequest) (UserListResult, error)
	GetUserInfoByID(ctx context.Context, id int64) (map[string]any, error)
	UpdateUser(ctx context.Context, req UserUpdateRequest) (bool, error)
	GenerateUsers(ctx context.Context, req UserGenerateRequest) (string, bool, error)
	DumpUserCSV(ctx context.Context, filters []UserFilter) (string, error)
	SendUserMail(ctx context.Context, req UserMailRequest) (bool, error)
	BanUsers(ctx context.Context, filters []UserFilter) (bool, error)
	ResetUserSecret(ctx context.Context, id int64) (bool, error)
	DeleteUser(ctx context.Context, id int64) (bool, error)
	DeleteUsers(ctx context.Context, filters []UserFilter) (bool, error)
	ListInviteCampaigns(ctx context.Context, req InviteCampaignListRequest) (InviteCampaignListResult, error)
	GetInviteCampaign(ctx context.Context, id int64) (map[string]any, error)
	ListInviteCampaignRecords(ctx context.Context, req InviteCampaignRecordListRequest) (InviteCampaignRecordListResult, error)
	ListServerGroups(ctx context.Context, groupID *int64) ([]ServerGroupRecord, error)
	SaveServerGroup(ctx context.Context, req ServerGroupSaveRequest) (bool, error)
	DeleteServerGroup(ctx context.Context, id int64) (bool, error)
	ListServerRoutes(ctx context.Context) ([]ServerRouteRecord, error)
	SaveServerRoute(ctx context.Context, req ServerRouteSaveRequest) (bool, error)
	DeleteServerRoute(ctx context.Context, id int64) (bool, error)
	ListManagedServers(ctx context.Context) ([]map[string]any, error)
	SortManagedServers(ctx context.Context, values map[string]map[int64]int64) (bool, error)
	UpdateManagedServerHost(ctx context.Context, oldHost, newHost string) (ManagedServerHostUpdateResult, error)
	SaveManagedServer(ctx context.Context, serverType string, payload map[string]any) (bool, error)
	DeleteManagedServer(ctx context.Context, serverType string, id int64) (bool, error)
	UpdateManagedServer(ctx context.Context, serverType string, id int64, values map[string]any) (bool, error)
	CopyManagedServer(ctx context.Context, serverType string, id int64) (bool, error)
	FetchConfig(ctx context.Context, key string) (map[string]any, error)
	SaveConfig(ctx context.Context, values map[string]any) (bool, error)
	ListThemes(ctx context.Context) (map[string]any, error)
	GetThemeConfig(ctx context.Context, name string) (map[string]any, error)
	SaveThemeConfig(ctx context.Context, name string, values map[string]any) (map[string]any, error)
	ListEmailTemplates(ctx context.Context) ([]string, error)
	ListThemeTemplates(ctx context.Context) ([]string, error)
	SetTelegramWebhook(ctx context.Context, token string) (bool, error)
	TestSendMail(ctx context.Context, email string) (ConfigMailTestLog, error)
	ListPlans(ctx context.Context) ([]PlanRecord, error)
	SavePlan(ctx context.Context, req PlanSaveRequest) (bool, error)
	DeletePlan(ctx context.Context, id int64) (bool, error)
	TogglePlan(ctx context.Context, req PlanToggleRequest) (bool, error)
	SortPlans(ctx context.Context, ids []int64) (bool, error)
	ListNotices(ctx context.Context) ([]NoticeRecord, error)
	SaveNotice(ctx context.Context, req NoticeSaveRequest) (bool, error)
	DeleteNotice(ctx context.Context, id int64) (bool, error)
	ToggleNotice(ctx context.Context, id int64) (bool, error)
	ListCoupons(ctx context.Context, req CouponListRequest) (CouponListResult, error)
	GenerateCoupon(ctx context.Context, req CouponGenerateRequest) (string, bool, error)
	DeleteCoupon(ctx context.Context, id int64) (bool, error)
	ToggleCoupon(ctx context.Context, id int64) (bool, error)
	ListGiftcards(ctx context.Context, req GiftcardListRequest) (GiftcardListResult, error)
	GenerateGiftcard(ctx context.Context, req GiftcardGenerateRequest) (string, bool, error)
	DeleteGiftcard(ctx context.Context, id int64) (bool, error)
	ListKnowledges(ctx context.Context) ([]KnowledgeRecord, error)
	GetKnowledge(ctx context.Context, id int64) (KnowledgeRecord, error)
	ListKnowledgeCategories(ctx context.Context) ([]string, error)
	SaveKnowledge(ctx context.Context, req KnowledgeSaveRequest) (bool, error)
	ToggleKnowledge(ctx context.Context, id int64) (bool, error)
	SortKnowledges(ctx context.Context, ids []int64) (bool, error)
	DeleteKnowledge(ctx context.Context, id int64) (bool, error)
	FetchOrders(ctx context.Context, req OrderFetchRequest) (OrderListResult, error)
	GetOrderDetail(ctx context.Context, id int64) (map[string]any, error)
	UpdateOrder(ctx context.Context, req OrderUpdateRequest) (bool, error)
	MarkOrderPaid(ctx context.Context, tradeNo string) (bool, error)
	CancelManagedOrder(ctx context.Context, tradeNo string) (bool, error)
	AssignOrder(ctx context.Context, req OrderAssignRequest) (string, error)
	ListPayments(ctx context.Context) ([]PaymentRecord, error)
	ListPaymentMethods(ctx context.Context) ([]string, error)
	GetPaymentForm(ctx context.Context, gateway string, id *int64) (map[string]PaymentFormField, error)
	SavePayment(ctx context.Context, req PaymentSaveRequest) (bool, error)
	DeletePayment(ctx context.Context, id int64) (bool, error)
	TogglePayment(ctx context.Context, id int64) (bool, error)
	SortPayments(ctx context.Context, ids []int64) (bool, error)
	ListTickets(ctx context.Context, req TicketListRequest) (TicketListResult, error)
	GetTicket(ctx context.Context, id int64) (TicketDetail, error)
	ReplyTicket(ctx context.Context, req TicketReplyRequest) (bool, error)
	CloseTicket(ctx context.Context, id int64) (bool, error)
}

type DBService struct {
	cfg        config.Config
	runtime    *config.RuntimeState
	db         *sql.DB
	orders     orderRuntime
	jobs       queue.Enqueuer
	authCache  *session.AuthCache
	mailSender func(host string, port int, encryption, username, password, from, fromName, to, subject, body string) error
	sleep      func(context.Context, time.Duration) error
}

type paymentRow struct {
	ID                 int64
	UUID               string
	Payment            string
	Name               string
	Icon               sql.NullString
	Config             string
	NotifyDomain       sql.NullString
	HandlingFeeFixed   sql.NullInt64
	HandlingFeePercent sql.NullFloat64
	Enable             int64
	Sort               sql.NullInt64
	CreatedAt          int64
	UpdatedAt          int64
}

func NewDBService(cfg config.Config, db *sql.DB, orders ...orderRuntime) *DBService {
	var runtime orderRuntime
	if len(orders) > 0 {
		runtime = orders[0]
	}
	return &DBService{cfg: cfg, db: db, orders: runtime}
}

func (s *DBService) WithQueueRuntime(jobs queue.Enqueuer) *DBService {
	s.jobs = jobs
	return s
}

func (s *DBService) WithRuntimeConfig(runtime *config.RuntimeState) *DBService {
	s.runtime = runtime
	return s
}

func (s *DBService) WithAuthCache(cache *session.AuthCache) *DBService {
	s.authCache = cache
	return s
}

func (s *DBService) sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	if s.sleep != nil {
		return s.sleep(ctx, d)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *DBService) GetSystemStatus(ctx context.Context) (SystemStatus, error) {
	if s.db == nil {
		return SystemStatus{}, ErrUnavailable
	}

	lastRuntime, err := s.getInt64KV(ctx, scheduleLastCheckKey)
	if err != nil {
		return SystemStatus{}, err
	}

	status := SystemStatus{
		ScheduleLastRuntime: lastRuntime,
		Horizon:             false,
		Schedule:            false,
	}
	if lastRuntime != nil && time.Now().Unix()-120 < *lastRuntime {
		status.Schedule = true
	}

	return status, nil
}

func (s *DBService) GetQueueStats(_ context.Context) (QueueStats, error) {
	if s.db == nil {
		return QueueStats{}, ErrUnavailable
	}
	snapshot := queue.Snapshot{}
	if s.jobs != nil {
		snapshot = s.jobs.Snapshot()
	}

	wait := make([]QueueWaitJob, 0, len(snapshot.Queues))
	for _, item := range snapshot.Queues {
		wait = append(wait, QueueWaitJob{Name: item.Name, Time: item.Wait})
	}

	var maxRuntime any
	if snapshot.MaxRuntimeQueue != "" {
		maxRuntime = snapshot.MaxRuntimeQueue
	}
	var maxThroughput any
	if snapshot.MaxThroughputQueue != "" {
		maxThroughput = snapshot.MaxThroughputQueue
	}

	return QueueStats{
		FailedJobs:    snapshot.FailedLast7Days,
		JobsPerMinute: snapshot.CurrentJobs,
		PausedMasters: 0,
		Periods: QueuePeriods{
			FailedJobs: snapshot.FailedLast7Days,
			RecentJobs: snapshot.ProcessedLastHour,
		},
		Processes:              snapshot.Workers,
		QueueWithMaxRuntime:    maxRuntime,
		QueueWithMaxThroughput: maxThroughput,
		RecentJobs:             snapshot.ProcessedLastHour,
		Status:                 snapshot.Running,
		Wait:                   wait,
	}, nil
}

func (s *DBService) GetQueueWorkload(_ context.Context) ([]map[string]any, error) {
	if s.db == nil {
		return nil, ErrUnavailable
	}

	snapshot := queue.Snapshot{}
	if s.jobs != nil {
		snapshot = s.jobs.Snapshot()
	}
	return queueWorkloadRows(snapshot), nil
}

func queueWorkloadRows(snapshot queue.Snapshot) []map[string]any {
	rowsByName := make(map[string]map[string]any, len(snapshot.Queues))
	for _, item := range snapshot.Queues {
		rowsByName[item.Name] = map[string]any{
			"name":      item.Name,
			"processes": item.Processes,
			"length":    item.Length,
			"wait":      item.Wait,
		}
	}

	result := make([]map[string]any, 0, len(monitoredQueueWorkloadNames)+len(rowsByName))
	seen := make(map[string]struct{}, len(monitoredQueueWorkloadNames))
	for _, name := range monitoredQueueWorkloadNames {
		row, ok := rowsByName[name]
		if !ok {
			row = map[string]any{
				"name":      name,
				"processes": int64(0),
				"length":    int64(0),
				"wait":      int64(0),
			}
		}
		result = append(result, row)
		seen[name] = struct{}{}
	}

	extraNames := make([]string, 0, len(rowsByName))
	for name := range rowsByName {
		if _, ok := seen[name]; ok {
			continue
		}
		extraNames = append(extraNames, name)
	}
	sort.Strings(extraNames)
	for _, name := range extraNames {
		result = append(result, rowsByName[name])
	}

	return result
}

func (s *DBService) TouchScheduleHeartbeat(ctx context.Context) error {
	if s.db == nil {
		return ErrUnavailable
	}

	now := time.Now().Unix()
	_, err := s.db.ExecContext(ctx, `INSERT INTO v2_runtime_kv (k, v, expire_at, created_at, updated_at)
VALUES ($1, $2, 0, $3, $3)
ON CONFLICT (k) DO UPDATE SET v = EXCLUDED.v, updated_at = EXCLUDED.updated_at`, scheduleLastCheckKey, strconv.FormatInt(now, 10), now)
	if err != nil {
		return fmt.Errorf("touch schedule heartbeat: %w", err)
	}
	return nil
}

type legacyStatWindow struct {
	recordAt int64
	startAt  int64
	endAt    int64
}

func legacyStatWindows(now time.Time) []legacyStatWindow {
	current := now
	if current.IsZero() {
		current = time.Now()
	}
	todayStart := time.Date(current.Year(), current.Month(), current.Day(), 0, 0, 0, 0, current.Location()).Unix()
	yesterdayStart := todayStart - 86400

	windows := []legacyStatWindow{{
		recordAt: yesterdayStart,
		startAt:  yesterdayStart,
		endAt:    todayStart,
	}}
	if current.Unix() > todayStart {
		windows = append(windows, legacyStatWindow{
			recordAt: todayStart,
			startAt:  todayStart,
			endAt:    current.Unix(),
		})
	}
	return windows
}

func (s *DBService) RefreshLegacyStats(ctx context.Context) error {
	if s.db == nil {
		return ErrUnavailable
	}

	for _, window := range legacyStatWindows(time.Now()) {
		summary, err := s.GetStat(ctx, window.startAt, window.endAt)
		if err != nil {
			return err
		}
		if err := s.upsertLegacyStat(ctx, window.recordAt, summary); err != nil {
			return err
		}
	}
	return nil
}

func (s *DBService) upsertLegacyStat(ctx context.Context, recordAt int64, summary map[string]any) error {
	now := time.Now().Unix()
	_, err := s.db.ExecContext(ctx, `INSERT INTO v2_stat (
record_at, record_type, order_count, order_total, commission_count, commission_total,
paid_count, paid_total, register_count, invite_count, transfer_used_total, created_at, updated_at
) VALUES (
$1, 'd', $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $11
)
ON CONFLICT (record_at) DO UPDATE SET
order_count = EXCLUDED.order_count,
order_total = EXCLUDED.order_total,
commission_count = EXCLUDED.commission_count,
commission_total = EXCLUDED.commission_total,
paid_count = EXCLUDED.paid_count,
paid_total = EXCLUDED.paid_total,
register_count = EXCLUDED.register_count,
invite_count = EXCLUDED.invite_count,
transfer_used_total = EXCLUDED.transfer_used_total,
updated_at = EXCLUDED.updated_at`,
		recordAt,
		int64FromSummary(summary["order_count"]),
		int64FromSummary(summary["order_total"]),
		int64FromSummary(summary["commission_count"]),
		int64FromSummary(summary["commission_total"]),
		int64FromSummary(summary["paid_count"]),
		int64FromSummary(summary["paid_total"]),
		int64FromSummary(summary["register_count"]),
		int64FromSummary(summary["invite_count"]),
		stringFromSummary(summary["transfer_used_total"]),
		now,
	)
	if err != nil {
		return fmt.Errorf("upsert legacy stat: %w", err)
	}
	return nil
}

func int64FromSummary(value any) int64 {
	switch typed := value.(type) {
	case int64:
		return typed
	case int32:
		return int64(typed)
	case int:
		return int64(typed)
	case float64:
		return int64(math.Round(typed))
	case string:
		parsed, _ := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		return parsed
	default:
		return 0
	}
}

func stringFromSummary(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case int64:
		return strconv.FormatInt(typed, 10)
	case int:
		return strconv.Itoa(typed)
	case float64:
		return strconv.FormatInt(int64(math.Round(typed)), 10)
	default:
		return "0"
	}
}

func (s *DBService) ListPayments(ctx context.Context) ([]PaymentRecord, error) {
	if s.db == nil {
		return nil, ErrUnavailable
	}

	rows, err := s.db.QueryContext(ctx, `SELECT id, uuid, payment, name, icon, config, notify_domain, handling_fee_fixed, handling_fee_percent::float8, enable, sort, created_at, updated_at
FROM v2_payment
ORDER BY sort ASC NULLS LAST, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("query payments: %w", err)
	}
	defer rows.Close()

	var result []PaymentRecord
	for rows.Next() {
		row, err := scanPaymentRow(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, s.paymentRecord(row))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate payments: %w", err)
	}
	return result, nil
}

func (s *DBService) ListPaymentMethods(_ context.Context) ([]string, error) {
	if s.db == nil {
		return nil, ErrUnavailable
	}
	return payment.SupportedGateways(), nil
}

func (s *DBService) GetPaymentForm(ctx context.Context, gateway string, id *int64) (map[string]PaymentFormField, error) {
	if s.db == nil {
		return nil, ErrUnavailable
	}

	gateway = strings.TrimSpace(gateway)
	form, ok := payment.GatewayForm(gateway)
	if !ok {
		return nil, errors.New("gate is not found")
	}

	if id == nil {
		return form, nil
	}

	row, ok, err := s.loadPaymentByID(ctx, *id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("支付方式不存在")
	}

	configValues := decodeConfigMap(row.Config)
	for key, field := range form {
		if value, ok := configValues[key]; ok && value != nil {
			field.Value = strings.TrimSpace(fmt.Sprint(value))
			form[key] = field
		}
	}
	return form, nil
}

func (s *DBService) SavePayment(ctx context.Context, req PaymentSaveRequest) (bool, error) {
	if s.db == nil {
		return false, ErrUnavailable
	}
	if strings.TrimSpace(s.cfg.AppURL) == "" {
		return false, errors.New("请在站点配置中配置站点地址")
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Payment = strings.TrimSpace(req.Payment)
	if req.Name == "" {
		return false, errors.New("显示名称不能为空")
	}
	if req.Payment == "" {
		return false, errors.New("网关参数不能为空")
	}
	if len(req.Config) == 0 {
		return false, errors.New("配置参数不能为空")
	}
	if req.NotifyDomain != nil {
		value := strings.TrimSpace(*req.NotifyDomain)
		if value == "" {
			req.NotifyDomain = nil
		} else {
			req.NotifyDomain = &value
		}
	}
	if req.Icon != nil {
		value := strings.TrimSpace(*req.Icon)
		if value == "" {
			req.Icon = nil
		} else {
			req.Icon = &value
		}
	}
	if req.HandlingFeePercent != nil {
		value := math.Round(*req.HandlingFeePercent*100) / 100
		req.HandlingFeePercent = &value
	}

	configJSON, err := json.Marshal(req.Config)
	if err != nil {
		return false, fmt.Errorf("encode payment config: %w", err)
	}

	now := time.Now().Unix()
	if req.ID != nil {
		if _, ok, err := s.loadPaymentByID(ctx, *req.ID); err != nil {
			return false, err
		} else if !ok {
			return false, errors.New("支付方式不存在")
		}

		if _, err := s.db.ExecContext(ctx, `UPDATE v2_payment
SET name = $2, icon = $3, payment = $4, config = $5, notify_domain = $6, handling_fee_fixed = $7, handling_fee_percent = $8, updated_at = $9
WHERE id = $1`,
			*req.ID,
			req.Name,
			nullableString(req.Icon),
			req.Payment,
			string(configJSON),
			nullableString(req.NotifyDomain),
			nullableInt64(req.HandlingFeeFixed),
			nullableFloat64(req.HandlingFeePercent),
			now,
		); err != nil {
			return false, err
		}
		return true, nil
	}

	uuid, err := randomAlphaNumeric(8)
	if err != nil {
		return false, err
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO v2_payment (
uuid, payment, name, icon, config, notify_domain, handling_fee_fixed, handling_fee_percent, enable, sort, created_at, updated_at
) VALUES (
$1, $2, $3, $4, $5, $6, $7, $8, 0, NULL, $9, $9
)`,
		uuid,
		req.Payment,
		req.Name,
		nullableString(req.Icon),
		string(configJSON),
		nullableString(req.NotifyDomain),
		nullableInt64(req.HandlingFeeFixed),
		nullableFloat64(req.HandlingFeePercent),
		now,
	); err != nil {
		return false, err
	}
	return true, nil
}

func (s *DBService) DeletePayment(ctx context.Context, id int64) (bool, error) {
	if s.db == nil {
		return false, ErrUnavailable
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM v2_payment WHERE id = $1`, id)
	if err != nil {
		return false, fmt.Errorf("delete payment: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("delete payment rows affected: %w", err)
	}
	if affected == 0 {
		return false, errors.New("支付方式不存在")
	}
	return true, nil
}

func (s *DBService) TogglePayment(ctx context.Context, id int64) (bool, error) {
	if s.db == nil {
		return false, ErrUnavailable
	}
	result, err := s.db.ExecContext(ctx, `UPDATE v2_payment
SET enable = CASE WHEN enable = 1 THEN 0 ELSE 1 END, updated_at = $2
WHERE id = $1`, id, time.Now().Unix())
	if err != nil {
		return false, fmt.Errorf("toggle payment: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("toggle payment rows affected: %w", err)
	}
	if affected == 0 {
		return false, errors.New("支付方式不存在")
	}
	return true, nil
}

func (s *DBService) SortPayments(ctx context.Context, ids []int64) (bool, error) {
	if s.db == nil {
		return false, ErrUnavailable
	}
	if len(ids) == 0 {
		return false, errors.New("参数有误")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin payment sort transaction: %w", err)
	}
	now := time.Now().Unix()
	for index, id := range ids {
		result, err := tx.ExecContext(ctx, `UPDATE v2_payment SET sort = $2, updated_at = $3 WHERE id = $1`, id, index+1, now)
		if err != nil {
			_ = tx.Rollback()
			return false, errors.New("保存失败")
		}
		affected, err := result.RowsAffected()
		if err != nil || affected == 0 {
			_ = tx.Rollback()
			return false, errors.New("保存失败")
		}
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit payment sort transaction: %w", err)
	}
	return true, nil
}

func (s *DBService) getInt64KV(ctx context.Context, key string) (*int64, error) {
	var raw string
	var expireAt int64
	err := s.db.QueryRowContext(ctx, `SELECT v, expire_at FROM v2_runtime_kv WHERE k = $1 LIMIT 1`, key).Scan(&raw, &expireAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("query runtime kv: %w", err)
	}

	now := time.Now().Unix()
	if expireAt > 0 && expireAt <= now {
		return nil, nil
	}

	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return nil, nil
	}
	return &value, nil
}

func (s *DBService) loadPaymentByID(ctx context.Context, id int64) (paymentRow, bool, error) {
	row, err := scanPaymentRow(s.db.QueryRowContext(ctx, `SELECT id, uuid, payment, name, icon, config, notify_domain, handling_fee_fixed, handling_fee_percent::float8, enable, sort, created_at, updated_at
FROM v2_payment
WHERE id = $1
LIMIT 1`, id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return paymentRow{}, false, nil
		}
		return paymentRow{}, false, err
	}
	return row, true, nil
}

func (s *DBService) paymentRecord(row paymentRow) PaymentRecord {
	record := PaymentRecord{
		ID:                 row.ID,
		UUID:               row.UUID,
		Payment:            row.Payment,
		Name:               row.Name,
		Icon:               nullStringPtr(row.Icon),
		Config:             decodeConfigMap(row.Config),
		NotifyDomain:       nullStringPtr(row.NotifyDomain),
		HandlingFeeFixed:   nullInt64Ptr(row.HandlingFeeFixed),
		HandlingFeePercent: nullFloat64Ptr(row.HandlingFeePercent),
		Enable:             row.Enable,
		Sort:               nullInt64Ptr(row.Sort),
		CreatedAt:          row.CreatedAt,
		UpdatedAt:          row.UpdatedAt,
		NotifyURL:          s.notifyURL(row.Payment, row.UUID, row.NotifyDomain),
	}
	return record
}

func (s *DBService) notifyURL(gateway, uuid string, notifyDomain sql.NullString) string {
	path := "/api/v1/guest/payment/notify/" + strings.TrimSpace(gateway) + "/" + strings.TrimSpace(uuid)
	if notifyDomain.Valid && strings.TrimSpace(notifyDomain.String) != "" {
		return strings.TrimRight(strings.TrimSpace(notifyDomain.String), "/") + path
	}
	if strings.TrimSpace(s.cfg.AppURL) == "" {
		return path
	}
	return strings.TrimRight(strings.TrimSpace(s.cfg.AppURL), "/") + path
}

func scanPaymentRow(scanner interface{ Scan(...any) error }) (paymentRow, error) {
	var row paymentRow
	if err := scanner.Scan(
		&row.ID,
		&row.UUID,
		&row.Payment,
		&row.Name,
		&row.Icon,
		&row.Config,
		&row.NotifyDomain,
		&row.HandlingFeeFixed,
		&row.HandlingFeePercent,
		&row.Enable,
		&row.Sort,
		&row.CreatedAt,
		&row.UpdatedAt,
	); err != nil {
		return paymentRow{}, err
	}
	return row, nil
}

func decodeConfigMap(raw string) map[string]any {
	if strings.TrimSpace(raw) == "" {
		return map[string]any{}
	}
	var values map[string]any
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return map[string]any{}
	}
	if values == nil {
		return map[string]any{}
	}
	return values
}

func nullStringPtr(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	text := strings.TrimSpace(value.String)
	return &text
}

func nullInt64Ptr(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	next := value.Int64
	return &next
}

func nullFloat64Ptr(value sql.NullFloat64) *float64 {
	if !value.Valid {
		return nil
	}
	next := value.Float64
	return &next
}

func nullableString(value *string) any {
	if value == nil {
		return nil
	}
	return strings.TrimSpace(*value)
}

func trimmedStringPtr(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func nullableInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableFloat64(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}

func randomAlphaNumeric(length int) (string, error) {
	if length <= 0 {
		return "", fmt.Errorf("invalid random length")
	}
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	var builder strings.Builder
	builder.Grow(length)
	limit := big.NewInt(int64(len(alphabet)))
	for range length {
		next, err := crand.Int(crand.Reader, limit)
		if err != nil {
			return "", fmt.Errorf("generate payment uuid: %w", err)
		}
		builder.WriteByte(alphabet[next.Int64()])
	}
	return builder.String(), nil
}
