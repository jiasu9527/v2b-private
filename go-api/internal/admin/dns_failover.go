package admin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const dnsProbeAPIURLKey = "dns_probe_api_url"

const (
	dnsFailoverEventDefaultPageSize int64 = 20
	dnsFailoverEventMaxPageSize     int64 = 100
	dnsFailoverEventMaxCurrent      int64 = 10_000
	maxPostgresInteger              int64 = 1<<31 - 1
)

type DNSFailoverSettings struct {
	ProbeAPIURL string `json:"dns_probe_api_url"`
}

type DNSFailoverSettingsSaveRequest struct {
	ProbeAPIURL string
}

type DNSProbeRecord struct {
	ID              int64  `json:"id"`
	Name            string `json:"name"`
	Secret          string `json:"secret"`
	Enabled         bool   `json:"enabled"`
	Version         string `json:"version"`
	Arch            string `json:"arch"`
	PublicIP        string `json:"public_ip"`
	LastHeartbeatAt *int64 `json:"last_heartbeat_at"`
	PrewarmCount    int64  `json:"prewarm_count"`
	CreatedAt       int64  `json:"created_at"`
	UpdatedAt       int64  `json:"updated_at"`
}

type DNSProbeCreateRequest struct {
	Name string
}

type DNSProbeCreateResult struct {
	Probe  DNSProbeRecord `json:"probe"`
	Secret string         `json:"secret"`
}

type DNSFailoverTargetRecord struct {
	ID        int64  `json:"id"`
	GroupID   int64  `json:"group_id"`
	Sort      int64  `json:"sort"`
	Name      string `json:"name"`
	DNSType   string `json:"dns_type"`
	DNSValue  string `json:"dns_value"`
	CheckHost string `json:"check_host"`
	CheckPort int64  `json:"check_port"`
	Enabled   bool   `json:"enabled"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

type DNSFailoverTargetSaveRequest struct {
	ID        int64
	Sort      int64
	Name      string
	DNSType   string
	DNSValue  string
	CheckHost string
	CheckPort int64
	Enabled   bool
}

type DNSFailoverRuleRecord struct {
	ID                          int64                     `json:"id"`
	Name                        string                    `json:"name"`
	DomainID                    int64                     `json:"domain_id"`
	Domain                      string                    `json:"domain"`
	RecordID                    int64                     `json:"record_id"`
	Subdomain                   string                    `json:"subdomain"`
	RecordLineID                string                    `json:"record_line_id"`
	RecordLineName              string                    `json:"record_line_name"`
	TTL                         int64                     `json:"ttl"`
	MX                          int64                     `json:"mx"`
	Weight                      *int64                    `json:"weight"`
	CurrentTargetID             *int64                    `json:"current_target_id"`
	Enabled                     bool                      `json:"enabled"`
	AutoFailback                bool                      `json:"auto_failback"`
	CheckIntervalSec            int64                     `json:"check_interval_sec"`
	TCPTimeoutMS                int64                     `json:"tcp_timeout_ms"`
	FailureThreshold            int64                     `json:"failure_threshold"`
	SuccessThreshold            int64                     `json:"success_threshold"`
	SingleProbeFailureThreshold int64                     `json:"single_probe_failure_threshold"`
	SingleProbeSuccessThreshold int64                     `json:"single_probe_success_threshold"`
	ProbeOfflineSec             int64                     `json:"probe_offline_sec"`
	CooldownSec                 int64                     `json:"cooldown_sec"`
	LastSwitchAt                *int64                    `json:"last_switch_at"`
	LastSwitchReason            string                    `json:"last_switch_reason"`
	Targets                     []DNSFailoverTargetRecord `json:"targets"`
	ProbeIDs                    []int64                   `json:"probe_ids"`
	CreatedAt                   int64                     `json:"created_at"`
	UpdatedAt                   int64                     `json:"updated_at"`
}

type DNSFailoverRuleSaveRequest struct {
	ID                          *int64
	Name                        string
	DomainID                    int64
	Domain                      string
	RecordID                    int64
	Subdomain                   string
	RecordLineID                string
	RecordLineName              string
	TTL                         int64
	MX                          int64
	Weight                      *int64
	Enabled                     bool
	AutoFailback                bool
	CheckIntervalSec            int64
	TCPTimeoutMS                int64
	FailureThreshold            int64
	SuccessThreshold            int64
	SingleProbeFailureThreshold int64
	SingleProbeSuccessThreshold int64
	ProbeOfflineSec             int64
	CooldownSec                 int64
	Targets                     []DNSFailoverTargetSaveRequest
	ProbeIDs                    []int64
}

type DNSFailoverEventRecord struct {
	ID         int64  `json:"id"`
	GroupID    int64  `json:"group_id"`
	ProbeID    *int64 `json:"probe_id"`
	TargetID   *int64 `json:"target_id"`
	EventType  string `json:"event_type"`
	Message    string `json:"message"`
	Details    string `json:"details"`
	DedupeKey  string `json:"dedupe_key"`
	NotifiedAt *int64 `json:"notified_at"`
	CreatedAt  int64  `json:"created_at"`
}

type DNSFailoverEventListRequest struct {
	GroupID   *int64
	EventType string
	Current   int64
	PageSize  int64
}

type DNSFailoverEventListResult struct {
	Data     []DNSFailoverEventRecord `json:"data"`
	Total    int64                    `json:"total"`
	Current  int64                    `json:"current"`
	PageSize int64                    `json:"page_size"`
}

func (s *DBService) GetDNSFailoverSettings(_ context.Context) (DNSFailoverSettings, error) {
	cfg, err := loadAdminConfigStore(adminConfigPath())
	if err != nil {
		return DNSFailoverSettings{}, fmt.Errorf("读取 DNS 故障转移设置失败: %w", err)
	}
	return DNSFailoverSettings{ProbeAPIURL: strings.TrimRight(strings.TrimSpace(cfg.stringValue(dnsProbeAPIURLKey, "")), "/")}, nil
}

func (s *DBService) SaveDNSFailoverSettings(ctx context.Context, request DNSFailoverSettingsSaveRequest) (DNSFailoverSettings, error) {
	probeAPIURL, err := normalizeDNSProbeAPIURL(request.ProbeAPIURL)
	if err != nil {
		return DNSFailoverSettings{}, err
	}
	if err := updateAdminConfigStore(adminConfigPath(), func(cfg *phpConfigFile) error {
		cfg.values[dnsProbeAPIURLKey] = phpConfigValue{kind: phpConfigScalar, scalar: probeAPIURL}
		cfg.order = appendMissingConfigKeys(cfg.order, []string{dnsProbeAPIURLKey}, cfg.values)
		return nil
	}); err != nil {
		return DNSFailoverSettings{}, fmt.Errorf("保存 DNS 故障转移设置失败: %w", err)
	}
	return DNSFailoverSettings{ProbeAPIURL: probeAPIURL}, nil
}

func normalizeDNSProbeAPIURL(value string) (string, error) {
	if err := validateDNSFailoverIdentifierText("探针接入地址", value); err != nil {
		return "", err
	}
	value = strings.TrimRight(strings.TrimSpace(value), "/")
	if value == "" {
		return "", nil
	}
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("探针接入地址必须是有效的 HTTPS 地址")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme == "https" {
		return value, nil
	}
	if scheme != "http" || !isLocalDNSProbeHost(parsed.Hostname()) {
		return "", errors.New("探针接入地址必须使用 HTTPS；仅 localhost 或 127.0.0.1 测试地址可使用 HTTP")
	}
	return value, nil
}

func isLocalDNSProbeHost(host string) bool {
	if strings.EqualFold(strings.TrimSpace(host), "localhost") {
		return true
	}
	ip := net.ParseIP(strings.TrimSpace(host))
	return ip != nil && ip.Equal(net.ParseIP("127.0.0.1"))
}

func (s *DBService) ListDNSProbes(ctx context.Context) ([]DNSProbeRecord, error) {
	if err := s.ensureDNSFailoverSchema(ctx); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, token_plaintext, enabled, version, arch, public_ip, last_heartbeat_at, prewarm_count, created_at, updated_at
FROM v2_dns_probe
ORDER BY id ASC`)
	if err != nil {
		return nil, fmt.Errorf("读取探针列表失败: %w", err)
	}
	defer rows.Close()

	result := make([]DNSProbeRecord, 0)
	for rows.Next() {
		var (
			probe         DNSProbeRecord
			enabled       int64
			lastHeartbeat sql.NullInt64
		)
		if err := rows.Scan(
			&probe.ID,
			&probe.Name,
			&probe.Secret,
			&enabled,
			&probe.Version,
			&probe.Arch,
			&probe.PublicIP,
			&lastHeartbeat,
			&probe.PrewarmCount,
			&probe.CreatedAt,
			&probe.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("读取探针列表失败: %w", err)
		}
		probe.Enabled = enabled != 0
		if lastHeartbeat.Valid {
			value := lastHeartbeat.Int64
			probe.LastHeartbeatAt = &value
		}
		result = append(result, probe)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取探针列表失败: %w", err)
	}
	return result, nil
}

func (s *DBService) CreateDNSProbe(ctx context.Context, request DNSProbeCreateRequest) (DNSProbeCreateResult, error) {
	if err := s.ensureDNSFailoverSchema(ctx); err != nil {
		return DNSProbeCreateResult{}, err
	}
	if err := validateDNSFailoverIdentifierText("探针名称", request.Name); err != nil {
		return DNSProbeCreateResult{}, err
	}
	name := strings.TrimSpace(request.Name)
	if name == "" {
		return DNSProbeCreateResult{}, errors.New("探针名称不能为空")
	}
	if len([]rune(name)) > 255 {
		return DNSProbeCreateResult{}, errors.New("探针名称不能超过 255 个字符")
	}

	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return DNSProbeCreateResult{}, fmt.Errorf("生成探针密钥失败: %w", err)
	}
	secret := base64.RawURLEncoding.EncodeToString(random)
	hash := sha256.Sum256([]byte(secret))
	hashText := hex.EncodeToString(hash[:])
	now := time.Now().Unix()
	probe := DNSProbeRecord{Name: name, Enabled: true}
	if err := s.db.QueryRowContext(ctx, `INSERT INTO v2_dns_probe (name, token_hash, token_plaintext, enabled, created_at, updated_at)
VALUES ($1, $2, $3, 1, $4, $5)
RETURNING id, created_at, updated_at`, name, hashText, secret, now, now).Scan(&probe.ID, &probe.CreatedAt, &probe.UpdatedAt); err != nil {
		return DNSProbeCreateResult{}, fmt.Errorf("创建探针失败: %w", err)
	}
	return DNSProbeCreateResult{Probe: probe, Secret: secret}, nil
}

func (s *DBService) DeleteDNSProbe(ctx context.Context, id int64) (bool, error) {
	if err := s.ensureDNSFailoverSchema(ctx); err != nil {
		return false, err
	}
	if id <= 0 {
		return false, errors.New("探针 ID 无效")
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM v2_dns_probe WHERE id = $1`, id)
	if err != nil {
		return false, fmt.Errorf("删除探针失败: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("读取探针删除结果失败: %w", err)
	}
	if affected == 0 {
		return false, errors.New("探针不存在")
	}
	return true, nil
}

func (s *DBService) SetDNSProbeEnabled(ctx context.Context, id int64, enabled bool) (bool, error) {
	if err := s.ensureDNSFailoverSchema(ctx); err != nil {
		return false, err
	}
	if id <= 0 {
		return false, errors.New("探针 ID 无效")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE v2_dns_probe SET enabled = $2, updated_at = $3 WHERE id = $1`, id, boolToInt64(enabled), time.Now().Unix())
	if err != nil {
		return false, fmt.Errorf("更新探针状态失败: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("读取探针状态更新结果失败: %w", err)
	}
	if affected == 0 {
		return false, errors.New("探针不存在")
	}
	return true, nil
}

func (s *DBService) ListDNSFailoverRules(ctx context.Context) ([]DNSFailoverRuleRecord, error) {
	if err := s.ensureDNSFailoverSchema(ctx); err != nil {
		return nil, err
	}
	return s.readDNSFailoverRulesSnapshot(ctx, nil)
}

func (s *DBService) GetDNSFailoverRule(ctx context.Context, id int64) (DNSFailoverRuleRecord, error) {
	if err := s.ensureDNSFailoverSchema(ctx); err != nil {
		return DNSFailoverRuleRecord{}, err
	}
	if id <= 0 {
		return DNSFailoverRuleRecord{}, errors.New("故障转移规则 ID 无效")
	}
	rules, err := s.readDNSFailoverRulesSnapshot(ctx, &id)
	if err != nil {
		return DNSFailoverRuleRecord{}, err
	}
	if len(rules) == 0 {
		return DNSFailoverRuleRecord{}, errors.New("故障转移规则不存在")
	}
	return rules[0], nil
}

type dnsFailoverQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func dnsFailoverReadTxOptions() *sql.TxOptions {
	return &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}
}

func (s *DBService) readDNSFailoverRulesSnapshot(ctx context.Context, id *int64) ([]DNSFailoverRuleRecord, error) {
	tx, err := s.db.BeginTx(ctx, dnsFailoverReadTxOptions())
	if err != nil {
		return nil, fmt.Errorf("开始读取故障转移规则失败: %w", err)
	}
	defer tx.Rollback()
	rules, err := s.listDNSFailoverRules(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("完成读取故障转移规则失败: %w", err)
	}
	return rules, nil
}

func (s *DBService) listDNSFailoverRules(ctx context.Context, queryer dnsFailoverQueryer, id *int64) ([]DNSFailoverRuleRecord, error) {
	query := `SELECT id, name, domain_id, domain, record_id, subdomain, record_line_id, record_line_name, ttl, mx, weight, current_target_id,
enabled, auto_failback, check_interval_sec, tcp_timeout_ms, failure_threshold, success_threshold, single_probe_failure_threshold,
single_probe_success_threshold, probe_offline_sec, cooldown_sec, last_switch_at, last_switch_reason, created_at, updated_at
FROM v2_dns_failover_group`
	args := make([]any, 0, 1)
	if id != nil {
		query += ` WHERE id = $1`
		args = append(args, *id)
	}
	query += ` ORDER BY id ASC`

	rows, err := queryer.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("读取故障转移规则失败: %w", err)
	}
	defer rows.Close()

	result := make([]DNSFailoverRuleRecord, 0)
	groupIDs := make([]int64, 0)
	for rows.Next() {
		var (
			rule          DNSFailoverRuleRecord
			weight        sql.NullInt64
			currentTarget sql.NullInt64
			enabled       int64
			autoFailback  int64
			lastSwitch    sql.NullInt64
		)
		if err := rows.Scan(
			&rule.ID,
			&rule.Name,
			&rule.DomainID,
			&rule.Domain,
			&rule.RecordID,
			&rule.Subdomain,
			&rule.RecordLineID,
			&rule.RecordLineName,
			&rule.TTL,
			&rule.MX,
			&weight,
			&currentTarget,
			&enabled,
			&autoFailback,
			&rule.CheckIntervalSec,
			&rule.TCPTimeoutMS,
			&rule.FailureThreshold,
			&rule.SuccessThreshold,
			&rule.SingleProbeFailureThreshold,
			&rule.SingleProbeSuccessThreshold,
			&rule.ProbeOfflineSec,
			&rule.CooldownSec,
			&lastSwitch,
			&rule.LastSwitchReason,
			&rule.CreatedAt,
			&rule.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("读取故障转移规则失败: %w", err)
		}
		rule.Enabled = enabled != 0
		rule.AutoFailback = autoFailback != 0
		rule.Weight = dnsNullInt64Pointer(weight)
		rule.CurrentTargetID = dnsNullInt64Pointer(currentTarget)
		rule.LastSwitchAt = dnsNullInt64Pointer(lastSwitch)
		rule.Targets = []DNSFailoverTargetRecord{}
		rule.ProbeIDs = []int64{}
		result = append(result, rule)
		groupIDs = append(groupIDs, rule.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取故障转移规则失败: %w", err)
	}
	if len(groupIDs) == 0 {
		return result, nil
	}

	targets, err := s.loadDNSFailoverTargets(ctx, queryer, groupIDs)
	if err != nil {
		return nil, err
	}
	probeIDs, err := s.loadDNSFailoverProbeBindings(ctx, queryer, groupIDs)
	if err != nil {
		return nil, err
	}
	for index := range result {
		if values, ok := targets[result[index].ID]; ok {
			result[index].Targets = values
		}
		if values, ok := probeIDs[result[index].ID]; ok {
			result[index].ProbeIDs = values
		}
	}
	return result, nil
}

func (s *DBService) loadDNSFailoverTargets(ctx context.Context, queryer dnsFailoverQueryer, groupIDs []int64) (map[int64][]DNSFailoverTargetRecord, error) {
	result := make(map[int64][]DNSFailoverTargetRecord, len(groupIDs))
	placeholders, args := dnsFailoverPlaceholders(1, groupIDs)
	rows, err := queryer.QueryContext(ctx, `SELECT id, group_id, sort, name, dns_type, dns_value, check_host, check_port, enabled, created_at, updated_at
FROM v2_dns_failover_target
WHERE group_id IN (`+placeholders+`)
	ORDER BY group_id ASC, sort ASC, id ASC`, args...)
	if err != nil {
		return nil, fmt.Errorf("读取故障转移目标失败: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var target DNSFailoverTargetRecord
		var enabled int64
		if err := rows.Scan(
			&target.ID,
			&target.GroupID,
			&target.Sort,
			&target.Name,
			&target.DNSType,
			&target.DNSValue,
			&target.CheckHost,
			&target.CheckPort,
			&enabled,
			&target.CreatedAt,
			&target.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("读取故障转移目标失败: %w", err)
		}
		target.Enabled = enabled != 0
		result[target.GroupID] = append(result[target.GroupID], target)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取故障转移目标失败: %w", err)
	}
	return result, nil
}

func (s *DBService) loadDNSFailoverProbeBindings(ctx context.Context, queryer dnsFailoverQueryer, groupIDs []int64) (map[int64][]int64, error) {
	result := make(map[int64][]int64, len(groupIDs))
	placeholders, args := dnsFailoverPlaceholders(1, groupIDs)
	rows, err := queryer.QueryContext(ctx, `SELECT group_id, probe_id
FROM v2_dns_failover_group_probe
WHERE group_id IN (`+placeholders+`)
	ORDER BY group_id ASC, probe_id ASC`, args...)
	if err != nil {
		return nil, fmt.Errorf("读取规则探针绑定失败: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var groupID, probeID int64
		if err := rows.Scan(&groupID, &probeID); err != nil {
			return nil, fmt.Errorf("读取规则探针绑定失败: %w", err)
		}
		result[groupID] = append(result[groupID], probeID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取规则探针绑定失败: %w", err)
	}
	return result, nil
}

func (s *DBService) SaveDNSFailoverRule(ctx context.Context, request DNSFailoverRuleSaveRequest) (DNSFailoverRuleRecord, error) {
	if err := s.ensureDNSFailoverSchema(ctx); err != nil {
		return DNSFailoverRuleRecord{}, err
	}
	if err := normalizeDNSFailoverRuleSaveRequest(&request); err != nil {
		return DNSFailoverRuleRecord{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DNSFailoverRuleRecord{}, fmt.Errorf("保存故障转移规则失败: %w", err)
	}
	defer tx.Rollback()
	if err := validateDNSFailoverProbeBindings(ctx, tx, request.ProbeIDs); err != nil {
		return DNSFailoverRuleRecord{}, err
	}

	var rule DNSFailoverRuleRecord
	if request.ID == nil {
		rule, err = createDNSFailoverRule(ctx, tx, request)
	} else {
		rule, err = updateDNSFailoverRule(ctx, tx, request)
	}
	if err != nil {
		return DNSFailoverRuleRecord{}, err
	}
	if err := replaceDNSFailoverProbeBindings(ctx, tx, rule.ID, request.ProbeIDs, request.ID != nil); err != nil {
		return DNSFailoverRuleRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return DNSFailoverRuleRecord{}, fmt.Errorf("保存故障转移规则失败: %w", err)
	}
	rule.ProbeIDs = append([]int64(nil), request.ProbeIDs...)
	return rule, nil
}

func rejectUnfinishedDNSFailoverOperation(ctx context.Context, tx *sql.Tx, groupID int64) error {
	var exists bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS (
SELECT 1 FROM v2_dns_failover_saga WHERE group_id = $1
UNION ALL
SELECT 1 FROM v2_dns_failover_eval_outbox WHERE group_id = $1 AND operation <> 'evaluate'
)`, groupID).Scan(&exists)
	if err != nil {
		return fmt.Errorf("检查 DNS 故障转移恢复状态失败: %w", err)
	}
	if exists {
		return errors.New("DNS 状态正在切换或等待恢复，请等待恢复完成后再修改规则")
	}
	return nil
}

func createDNSFailoverRule(ctx context.Context, tx *sql.Tx, request DNSFailoverRuleSaveRequest) (DNSFailoverRuleRecord, error) {
	now := time.Now().Unix()
	rule := dnsFailoverRuleFromSaveRequest(request)
	if err := tx.QueryRowContext(ctx, `INSERT INTO v2_dns_failover_group (
name, domain_id, domain, record_id, subdomain, record_line_id, record_line_name, ttl, mx, weight, current_target_id,
enabled, auto_failback, check_interval_sec, tcp_timeout_ms, failure_threshold, success_threshold, single_probe_failure_threshold,
single_probe_success_threshold, probe_offline_sec, cooldown_sec, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, NULL, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22)
RETURNING id, created_at, updated_at`,
		request.Name,
		request.DomainID,
		request.Domain,
		request.RecordID,
		request.Subdomain,
		request.RecordLineID,
		request.RecordLineName,
		request.TTL,
		request.MX,
		dnsInt64PointerValue(request.Weight),
		boolToInt64(request.Enabled),
		boolToInt64(request.AutoFailback),
		request.CheckIntervalSec,
		request.TCPTimeoutMS,
		request.FailureThreshold,
		request.SuccessThreshold,
		request.SingleProbeFailureThreshold,
		request.SingleProbeSuccessThreshold,
		request.ProbeOfflineSec,
		request.CooldownSec,
		now,
		now,
	).Scan(&rule.ID, &rule.CreatedAt, &rule.UpdatedAt); err != nil {
		return DNSFailoverRuleRecord{}, fmt.Errorf("创建故障转移规则失败: %w", err)
	}

	rule.Targets = make([]DNSFailoverTargetRecord, 0, len(request.Targets))
	var firstEnabledID int64
	for _, item := range request.Targets {
		target, err := insertDNSFailoverTarget(ctx, tx, rule.ID, item, now)
		if err != nil {
			return DNSFailoverRuleRecord{}, err
		}
		rule.Targets = append(rule.Targets, target)
		if firstEnabledID == 0 && target.Enabled {
			firstEnabledID = target.ID
		}
	}
	if firstEnabledID == 0 {
		return DNSFailoverRuleRecord{}, errors.New("至少需要一个启用目标")
	}
	if err := setDNSFailoverCurrentTarget(ctx, tx, rule.ID, firstEnabledID, now); err != nil {
		return DNSFailoverRuleRecord{}, err
	}
	rule.CurrentTargetID = int64Pointer(firstEnabledID)
	rule.UpdatedAt = now
	return rule, nil
}

func updateDNSFailoverRule(ctx context.Context, tx *sql.Tx, request DNSFailoverRuleSaveRequest) (DNSFailoverRuleRecord, error) {
	groupID := *request.ID
	var (
		currentTarget    sql.NullInt64
		lastSwitchAt     sql.NullInt64
		lastSwitchReason string
		groupCreatedAt   int64
	)
	if err := tx.QueryRowContext(ctx, `SELECT current_target_id, last_switch_at, last_switch_reason, created_at FROM v2_dns_failover_group WHERE id = $1 FOR UPDATE`, groupID).Scan(&currentTarget, &lastSwitchAt, &lastSwitchReason, &groupCreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return DNSFailoverRuleRecord{}, errors.New("故障转移规则不存在")
		}
		return DNSFailoverRuleRecord{}, fmt.Errorf("读取故障转移规则失败: %w", err)
	}
	if err := rejectUnfinishedDNSFailoverOperation(ctx, tx, groupID); err != nil {
		return DNSFailoverRuleRecord{}, err
	}

	existingTargets, maxExistingSort, err := lockDNSFailoverTargets(ctx, tx, groupID)
	if err != nil {
		return DNSFailoverRuleRecord{}, err
	}
	for _, target := range request.Targets {
		if target.ID > 0 {
			if _, ok := existingTargets[target.ID]; !ok {
				return DNSFailoverRuleRecord{}, fmt.Errorf("目标 ID %d 不属于当前规则", target.ID)
			}
		}
	}
	if err := validateDNSFailoverCurrentTargetMutation(currentTarget, existingTargets, request.Targets); err != nil {
		return DNSFailoverRuleRecord{}, err
	}

	now := time.Now().Unix()
	result, err := tx.ExecContext(ctx, `UPDATE v2_dns_failover_group SET name = $2, domain_id = $3, domain = $4, record_id = $5,
subdomain = $6, record_line_id = $7, record_line_name = $8, ttl = $9, mx = $10, weight = $11, enabled = $12,
auto_failback = $13, check_interval_sec = $14, tcp_timeout_ms = $15, failure_threshold = $16, success_threshold = $17,
single_probe_failure_threshold = $18, single_probe_success_threshold = $19, probe_offline_sec = $20, cooldown_sec = $21, updated_at = $22
WHERE id = $1`,
		groupID,
		request.Name,
		request.DomainID,
		request.Domain,
		request.RecordID,
		request.Subdomain,
		request.RecordLineID,
		request.RecordLineName,
		request.TTL,
		request.MX,
		dnsInt64PointerValue(request.Weight),
		boolToInt64(request.Enabled),
		boolToInt64(request.AutoFailback),
		request.CheckIntervalSec,
		request.TCPTimeoutMS,
		request.FailureThreshold,
		request.SuccessThreshold,
		request.SingleProbeFailureThreshold,
		request.SingleProbeSuccessThreshold,
		request.ProbeOfflineSec,
		request.CooldownSec,
		now,
	)
	if err != nil {
		return DNSFailoverRuleRecord{}, fmt.Errorf("更新故障转移规则失败: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return DNSFailoverRuleRecord{}, fmt.Errorf("读取故障转移规则更新结果失败: %w", err)
	}
	if affected == 0 {
		return DNSFailoverRuleRecord{}, errors.New("故障转移规则不存在")
	}

	maxRequestedSort := request.Targets[len(request.Targets)-1].Sort
	if len(existingTargets) > 0 {
		offset := maxExistingSort + maxRequestedSort + int64(len(existingTargets)) + 1
		if offset <= 0 || maxExistingSort+offset > 2147483647 {
			return DNSFailoverRuleRecord{}, errors.New("目标排序值过大，无法安全更新")
		}
		if _, err := tx.ExecContext(ctx, `UPDATE v2_dns_failover_target SET sort = sort + $2, updated_at = $3 WHERE group_id = $1`, groupID, offset, now); err != nil {
			return DNSFailoverRuleRecord{}, fmt.Errorf("更新目标排序失败: %w", err)
		}
	}

	rule := dnsFailoverRuleFromSaveRequest(request)
	rule.ID = groupID
	rule.LastSwitchAt = dnsNullInt64Pointer(lastSwitchAt)
	rule.LastSwitchReason = lastSwitchReason
	rule.Targets = make([]DNSFailoverTargetRecord, 0, len(request.Targets))
	keptIDs := make(map[int64]struct{}, len(request.Targets))
	var firstEnabledID int64
	currentStillEnabled := false
	for _, item := range request.Targets {
		var target DNSFailoverTargetRecord
		if item.ID == 0 {
			target, err = insertDNSFailoverTarget(ctx, tx, groupID, item, now)
		} else if item.ID != currentTarget.Int64 && dnsFailoverTargetCriticalFieldsChanged(existingTargets[item.ID], item) {
			replacement := item
			replacement.ID = 0
			target, err = insertDNSFailoverTarget(ctx, tx, groupID, replacement, now)
		} else {
			target, err = updateDNSFailoverTarget(ctx, tx, groupID, item, existingTargets[item.ID], now)
			keptIDs[item.ID] = struct{}{}
		}
		if err != nil {
			return DNSFailoverRuleRecord{}, err
		}
		rule.Targets = append(rule.Targets, target)
		if firstEnabledID == 0 && target.Enabled {
			firstEnabledID = target.ID
		}
		if currentTarget.Valid && target.ID == currentTarget.Int64 && target.Enabled {
			currentStillEnabled = true
		}
	}
	if firstEnabledID == 0 {
		return DNSFailoverRuleRecord{}, errors.New("至少需要一个启用目标")
	}

	nextCurrentID := firstEnabledID
	if currentStillEnabled {
		nextCurrentID = currentTarget.Int64
	}
	if !currentTarget.Valid || currentTarget.Int64 != nextCurrentID {
		if err := setDNSFailoverCurrentTarget(ctx, tx, groupID, nextCurrentID, now); err != nil {
			return DNSFailoverRuleRecord{}, err
		}
	}
	rule.CurrentTargetID = int64Pointer(nextCurrentID)

	removedIDs := make([]int64, 0)
	for targetID := range existingTargets {
		if _, kept := keptIDs[targetID]; !kept {
			removedIDs = append(removedIDs, targetID)
		}
	}
	sort.Slice(removedIDs, func(i, j int) bool { return removedIDs[i] < removedIDs[j] })
	if len(removedIDs) > 0 {
		placeholders, args := dnsFailoverPlaceholders(2, removedIDs)
		args = append([]any{groupID}, args...)
		if _, err := tx.ExecContext(ctx, `DELETE FROM v2_dns_failover_target WHERE group_id = $1 AND id IN (`+placeholders+`)`, args...); err != nil {
			return DNSFailoverRuleRecord{}, fmt.Errorf("删除已移除的故障转移目标失败: %w", err)
		}
	}
	rule.CreatedAt = groupCreatedAt
	rule.UpdatedAt = now
	return rule, nil
}

type dnsFailoverExistingTarget struct {
	Sort      int64
	DNSType   string
	DNSValue  string
	CheckHost string
	CheckPort int64
	Enabled   bool
	CreatedAt int64
}

func dnsFailoverTargetCriticalFieldsChanged(existing dnsFailoverExistingTarget, requested DNSFailoverTargetSaveRequest) bool {
	return existing.DNSType != requested.DNSType ||
		existing.DNSValue != requested.DNSValue ||
		existing.CheckHost != requested.CheckHost ||
		existing.CheckPort != requested.CheckPort
}

func lockDNSFailoverTargets(ctx context.Context, tx *sql.Tx, groupID int64) (map[int64]dnsFailoverExistingTarget, int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, sort, dns_type, dns_value, check_host, check_port, enabled, created_at FROM v2_dns_failover_target WHERE group_id = $1 ORDER BY id ASC FOR UPDATE`, groupID)
	if err != nil {
		return nil, 0, fmt.Errorf("读取故障转移目标失败: %w", err)
	}
	defer rows.Close()
	result := make(map[int64]dnsFailoverExistingTarget)
	var maxSort int64
	for rows.Next() {
		var (
			id      int64
			target  dnsFailoverExistingTarget
			enabled int64
		)
		if err := rows.Scan(&id, &target.Sort, &target.DNSType, &target.DNSValue, &target.CheckHost, &target.CheckPort, &enabled, &target.CreatedAt); err != nil {
			return nil, 0, fmt.Errorf("读取故障转移目标失败: %w", err)
		}
		target.Enabled = enabled != 0
		result[id] = target
		if target.Sort > maxSort {
			maxSort = target.Sort
		}
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("读取故障转移目标失败: %w", err)
	}
	return result, maxSort, nil
}

func validateDNSFailoverCurrentTargetMutation(currentTarget sql.NullInt64, existingTargets map[int64]dnsFailoverExistingTarget, requestedTargets []DNSFailoverTargetSaveRequest) error {
	if !currentTarget.Valid {
		return nil
	}
	existing, ok := existingTargets[currentTarget.Int64]
	if !ok {
		return errors.New("当前目标状态异常，请先修复规则状态")
	}
	for _, target := range requestedTargets {
		if target.ID != currentTarget.Int64 {
			continue
		}
		if !target.Enabled {
			return errors.New("不能停用当前目标，请先手动切换到其他目标")
		}
		if target.DNSType != existing.DNSType || target.DNSValue != existing.DNSValue || target.CheckHost != existing.CheckHost || target.CheckPort != existing.CheckPort {
			return errors.New("不能修改当前目标的 DNS 或检测关键字段，请先手动切换到其他目标")
		}
		return nil
	}
	return errors.New("不能删除当前目标，请先手动切换到其他目标")
}

func insertDNSFailoverTarget(ctx context.Context, tx *sql.Tx, groupID int64, request DNSFailoverTargetSaveRequest, now int64) (DNSFailoverTargetRecord, error) {
	target := dnsFailoverTargetFromSaveRequest(groupID, request)
	if err := tx.QueryRowContext(ctx, `INSERT INTO v2_dns_failover_target (group_id, sort, name, dns_type, dns_value, check_host, check_port, enabled, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING id, created_at, updated_at`,
		groupID,
		request.Sort,
		request.Name,
		request.DNSType,
		request.DNSValue,
		request.CheckHost,
		request.CheckPort,
		boolToInt64(request.Enabled),
		now,
		now,
	).Scan(&target.ID, &target.CreatedAt, &target.UpdatedAt); err != nil {
		return DNSFailoverTargetRecord{}, fmt.Errorf("创建故障转移目标失败: %w", err)
	}
	return target, nil
}

func updateDNSFailoverTarget(ctx context.Context, tx *sql.Tx, groupID int64, request DNSFailoverTargetSaveRequest, existing dnsFailoverExistingTarget, now int64) (DNSFailoverTargetRecord, error) {
	result, err := tx.ExecContext(ctx, `UPDATE v2_dns_failover_target SET sort = $3, name = $4, dns_type = $5, dns_value = $6, check_host = $7, check_port = $8, enabled = $9, updated_at = $10 WHERE group_id = $1 AND id = $2`,
		groupID,
		request.ID,
		request.Sort,
		request.Name,
		request.DNSType,
		request.DNSValue,
		request.CheckHost,
		request.CheckPort,
		boolToInt64(request.Enabled),
		now,
	)
	if err != nil {
		return DNSFailoverTargetRecord{}, fmt.Errorf("更新故障转移目标失败: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return DNSFailoverTargetRecord{}, fmt.Errorf("读取故障转移目标更新结果失败: %w", err)
	}
	if affected == 0 {
		return DNSFailoverTargetRecord{}, errors.New("故障转移目标不存在")
	}
	target := dnsFailoverTargetFromSaveRequest(groupID, request)
	target.ID = request.ID
	target.CreatedAt = existing.CreatedAt
	target.UpdatedAt = now
	return target, nil
}

func setDNSFailoverCurrentTarget(ctx context.Context, tx *sql.Tx, groupID, targetID, now int64) error {
	result, err := tx.ExecContext(ctx, `UPDATE v2_dns_failover_group SET current_target_id = $2, updated_at = $3 WHERE id = $1`, groupID, targetID, now)
	if err != nil {
		return fmt.Errorf("更新当前故障转移目标失败: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("读取当前故障转移目标更新结果失败: %w", err)
	}
	if affected == 0 {
		return errors.New("故障转移规则不存在")
	}
	return nil
}

func validateDNSFailoverProbeBindings(ctx context.Context, tx *sql.Tx, probeIDs []int64) error {
	placeholders, args := dnsFailoverPlaceholders(1, probeIDs)
	rows, err := tx.QueryContext(ctx, `SELECT id FROM v2_dns_probe WHERE enabled = 1 AND id IN (`+placeholders+`) ORDER BY id ASC FOR SHARE`, args...)
	if err != nil {
		return fmt.Errorf("校验绑定探针失败: %w", err)
	}
	defer rows.Close()
	found := make([]int64, 0, len(probeIDs))
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return fmt.Errorf("校验绑定探针失败: %w", err)
		}
		found = append(found, id)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("校验绑定探针失败: %w", err)
	}
	if len(found) != len(probeIDs) {
		return errors.New("绑定的探针不存在或已被吊销，请重新选择")
	}
	for index := range found {
		if found[index] != probeIDs[index] {
			return errors.New("绑定的探针不存在或已被吊销，请重新选择")
		}
	}
	return nil
}

func replaceDNSFailoverProbeBindings(ctx context.Context, tx *sql.Tx, groupID int64, probeIDs []int64, clearExisting bool) error {
	if clearExisting {
		if _, err := tx.ExecContext(ctx, `DELETE FROM v2_dns_failover_group_probe WHERE group_id = $1`, groupID); err != nil {
			return fmt.Errorf("更新规则探针绑定失败: %w", err)
		}
	}
	now := time.Now().Unix()
	for _, probeID := range probeIDs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO v2_dns_failover_group_probe (group_id, probe_id, created_at, updated_at) VALUES ($1, $2, $3, $4)`, groupID, probeID, now, now); err != nil {
			return fmt.Errorf("更新规则探针绑定失败: %w", err)
		}
	}
	return nil
}

func (s *DBService) DeleteDNSFailoverRule(ctx context.Context, id int64) (bool, error) {
	if err := s.ensureDNSFailoverSchema(ctx); err != nil {
		return false, err
	}
	if id <= 0 {
		return false, errors.New("故障转移规则 ID 无效")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("删除故障转移规则失败: %w", err)
	}
	defer tx.Rollback()
	var enabled int64
	if err := tx.QueryRowContext(ctx, `SELECT enabled FROM v2_dns_failover_group WHERE id = $1 FOR UPDATE`, id).Scan(&enabled); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, errors.New("故障转移规则不存在")
		}
		return false, fmt.Errorf("读取故障转移规则状态失败: %w", err)
	}
	if err := rejectUnfinishedDNSFailoverOperation(ctx, tx, id); err != nil {
		return false, err
	}
	if enabled != 0 {
		return false, errors.New("规则仍处于启用状态，请先停用后再删除")
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM v2_dns_failover_group WHERE id = $1`, id)
	if err != nil {
		return false, fmt.Errorf("删除故障转移规则失败: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("读取故障转移规则删除结果失败: %w", err)
	}
	if affected == 0 {
		return false, errors.New("故障转移规则不存在")
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("删除故障转移规则失败: %w", err)
	}
	return true, nil
}

func (s *DBService) SetDNSFailoverRuleEnabled(ctx context.Context, id int64, enabled bool) (bool, error) {
	if err := s.ensureDNSFailoverSchema(ctx); err != nil {
		return false, err
	}
	if id <= 0 {
		return false, errors.New("故障转移规则 ID 无效")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE v2_dns_failover_group SET enabled = $2, updated_at = $3 WHERE id = $1`, id, boolToInt64(enabled), time.Now().Unix())
	if err != nil {
		return false, fmt.Errorf("更新故障转移规则状态失败: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("读取故障转移规则状态更新结果失败: %w", err)
	}
	if affected == 0 {
		return false, errors.New("故障转移规则不存在")
	}
	return true, nil
}

func (s *DBService) ListDNSFailoverEvents(ctx context.Context, request DNSFailoverEventListRequest) (DNSFailoverEventListResult, error) {
	if err := s.ensureDNSFailoverSchema(ctx); err != nil {
		return DNSFailoverEventListResult{}, err
	}
	if request.GroupID != nil && *request.GroupID <= 0 {
		return DNSFailoverEventListResult{}, errors.New("故障转移规则 ID 无效")
	}

	current, pageSize := normalizeDNSFailoverEventPage(request.Current, request.PageSize)
	if err := validateDNSFailoverIdentifierText("事件类型", request.EventType); err != nil {
		return DNSFailoverEventListResult{}, err
	}
	eventType := strings.TrimSpace(request.EventType)
	conditions := make([]string, 0, 2)
	filterArgs := make([]any, 0, 2)
	if request.GroupID != nil {
		filterArgs = append(filterArgs, *request.GroupID)
		conditions = append(conditions, fmt.Sprintf("group_id = $%d", len(filterArgs)))
	}
	if eventType != "" {
		filterArgs = append(filterArgs, eventType)
		conditions = append(conditions, fmt.Sprintf("event_type = $%d", len(filterArgs)))
	}
	whereClause := ""
	if len(conditions) > 0 {
		whereClause = " WHERE " + strings.Join(conditions, " AND ")
	}

	result := DNSFailoverEventListResult{
		Data:     []DNSFailoverEventRecord{},
		Current:  current,
		PageSize: pageSize,
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM v2_dns_failover_event`+whereClause, filterArgs...).Scan(&result.Total); err != nil {
		return DNSFailoverEventListResult{}, fmt.Errorf("读取故障转移事件总数失败: %w", err)
	}

	queryArgs := append([]any(nil), filterArgs...)
	limitPlaceholder := len(queryArgs) + 1
	queryArgs = append(queryArgs, pageSize)
	offsetPlaceholder := len(queryArgs) + 1
	queryArgs = append(queryArgs, (current-1)*pageSize)
	query := `SELECT id, group_id, probe_id, target_id, event_type, message, details, dedupe_key, notified_at, created_at
FROM v2_dns_failover_event` + whereClause + `
ORDER BY created_at DESC, id DESC
LIMIT $` + fmt.Sprint(limitPlaceholder) + ` OFFSET $` + fmt.Sprint(offsetPlaceholder)
	rows, err := s.db.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return DNSFailoverEventListResult{}, fmt.Errorf("读取故障转移事件失败: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			event      DNSFailoverEventRecord
			probeID    sql.NullInt64
			targetID   sql.NullInt64
			notifiedAt sql.NullInt64
		)
		if err := rows.Scan(
			&event.ID,
			&event.GroupID,
			&probeID,
			&targetID,
			&event.EventType,
			&event.Message,
			&event.Details,
			&event.DedupeKey,
			&notifiedAt,
			&event.CreatedAt,
		); err != nil {
			return DNSFailoverEventListResult{}, fmt.Errorf("读取故障转移事件失败: %w", err)
		}
		event.ProbeID = dnsNullInt64Pointer(probeID)
		event.TargetID = dnsNullInt64Pointer(targetID)
		event.NotifiedAt = dnsNullInt64Pointer(notifiedAt)
		result.Data = append(result.Data, event)
	}
	if err := rows.Err(); err != nil {
		return DNSFailoverEventListResult{}, fmt.Errorf("读取故障转移事件失败: %w", err)
	}
	return result, nil
}

func normalizeDNSFailoverEventPage(current, pageSize int64) (int64, int64) {
	if current < 1 {
		current = 1
	}
	if current > dnsFailoverEventMaxCurrent {
		current = dnsFailoverEventMaxCurrent
	}
	if pageSize < 1 {
		pageSize = dnsFailoverEventDefaultPageSize
	}
	if pageSize > dnsFailoverEventMaxPageSize {
		pageSize = dnsFailoverEventMaxPageSize
	}
	return current, pageSize
}

func validateDNSFailoverText(field, value string) error {
	if !utf8.ValidString(value) || strings.IndexByte(value, 0) >= 0 {
		return fmt.Errorf("%s包含无效字符", field)
	}
	return nil
}

func validateDNSFailoverIdentifierText(field, value string) error {
	if err := validateDNSFailoverText(field, value); err != nil {
		return err
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return fmt.Errorf("%s包含无效字符", field)
		}
	}
	return nil
}

func normalizeDNSFailoverRuleSaveRequest(request *DNSFailoverRuleSaveRequest) error {
	if request == nil {
		return errors.New("故障转移规则参数不能为空")
	}
	if request.ID != nil && *request.ID <= 0 {
		return errors.New("故障转移规则 ID 无效")
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "规则名称", value: request.Name},
		{name: "DNSPod 域名", value: request.Domain},
		{name: "DNSPod 主机记录", value: request.Subdomain},
		{name: "DNSPod 线路 ID", value: request.RecordLineID},
		{name: "DNSPod 线路名称", value: request.RecordLineName},
	} {
		if err := validateDNSFailoverIdentifierText(field.name, field.value); err != nil {
			return err
		}
	}
	request.Name = strings.TrimSpace(request.Name)
	if request.Name == "" {
		return errors.New("规则名称不能为空")
	}
	if len([]rune(request.Name)) > 255 {
		return errors.New("规则名称不能超过 255 个字符")
	}
	if request.DomainID <= 0 || request.RecordID <= 0 {
		return errors.New("请选择有效的 DNSPod 域名和记录")
	}
	domain, err := normalizeDNSFailoverHostname(request.Domain)
	if err != nil {
		return errors.New("DNSPod 域名格式不正确")
	}
	request.Domain = domain
	request.Subdomain = strings.TrimSpace(request.Subdomain)
	request.RecordLineID = strings.TrimSpace(request.RecordLineID)
	request.RecordLineName = strings.TrimSpace(request.RecordLineName)
	if request.Subdomain == "" {
		return errors.New("DNSPod 主机记录不能为空")
	}
	if len([]rune(request.Subdomain)) > 255 {
		return errors.New("DNSPod 主机记录不能超过 255 个字符")
	}
	if len([]rune(request.RecordLineID)) > 255 {
		return errors.New("DNSPod 线路 ID 不能超过 255 个字符")
	}
	if len([]rune(request.RecordLineName)) > 255 {
		return errors.New("DNSPod 线路名称不能超过 255 个字符")
	}
	if request.TTL < 0 || request.MX < 0 || (request.Weight != nil && *request.Weight < 0) {
		return errors.New("TTL、MX 和权重不能小于 0")
	}
	integerValues := []struct {
		name  string
		value int64
	}{
		{name: "TTL", value: request.TTL},
		{name: "MX", value: request.MX},
		{name: "检测间隔", value: request.CheckIntervalSec},
		{name: "TCP 超时", value: request.TCPTimeoutMS},
		{name: "失败阈值", value: request.FailureThreshold},
		{name: "成功阈值", value: request.SuccessThreshold},
		{name: "单探针失败阈值", value: request.SingleProbeFailureThreshold},
		{name: "单探针成功阈值", value: request.SingleProbeSuccessThreshold},
		{name: "探针离线时间", value: request.ProbeOfflineSec},
		{name: "冷却时间", value: request.CooldownSec},
	}
	if request.Weight != nil {
		integerValues = append(integerValues, struct {
			name  string
			value int64
		}{name: "权重", value: *request.Weight})
	}
	for _, item := range integerValues {
		if item.value > maxPostgresInteger {
			return fmt.Errorf("%s不能超过 PostgreSQL INTEGER 上限 %d", item.name, maxPostgresInteger)
		}
	}
	if request.CheckIntervalSec <= 0 || request.TCPTimeoutMS <= 0 || request.ProbeOfflineSec <= 0 || request.CooldownSec < 0 {
		return errors.New("检测间隔、TCP 超时和探针离线时间必须大于 0，冷却时间不能小于 0")
	}
	if request.FailureThreshold <= 0 || request.SuccessThreshold <= 0 {
		return errors.New("失败和成功阈值必须大于 0")
	}
	if request.SingleProbeFailureThreshold <= request.FailureThreshold || request.SingleProbeSuccessThreshold <= request.SuccessThreshold {
		return errors.New("单探针失败/成功阈值必须分别大于多探针阈值")
	}
	if len(request.Targets) == 0 {
		return errors.New("至少需要一个故障转移目标")
	}

	seenIDs := make(map[int64]struct{}, len(request.Targets))
	seenSort := make(map[int64]struct{}, len(request.Targets))
	enabledTargets := 0
	for index := range request.Targets {
		target := &request.Targets[index]
		for _, field := range []struct {
			name  string
			value string
		}{
			{name: "目标名称", value: target.Name},
			{name: "目标 DNS 类型", value: target.DNSType},
			{name: "目标 DNS 值", value: target.DNSValue},
			{name: "目标检测主机", value: target.CheckHost},
		} {
			if err := validateDNSFailoverIdentifierText(field.name, field.value); err != nil {
				return err
			}
		}
		if target.ID < 0 {
			return errors.New("目标 ID 无效")
		}
		if target.ID > 0 {
			if _, exists := seenIDs[target.ID]; exists {
				return fmt.Errorf("目标 ID %d 重复", target.ID)
			}
			seenIDs[target.ID] = struct{}{}
		}
		if target.Sort < 0 || target.Sort > maxPostgresInteger {
			return fmt.Errorf("目标排序必须在 0 到 %d 之间", maxPostgresInteger)
		}
		if _, exists := seenSort[target.Sort]; exists {
			return fmt.Errorf("目标排序 %d 重复", target.Sort)
		}
		seenSort[target.Sort] = struct{}{}
		target.Name = strings.TrimSpace(target.Name)
		if target.Name == "" {
			return fmt.Errorf("排序 %d 的目标名称不能为空", target.Sort)
		}
		if len([]rune(target.Name)) > 255 {
			return fmt.Errorf("目标 %q 的名称不能超过 255 个字符", target.Name)
		}
		target.DNSType = strings.ToUpper(strings.TrimSpace(target.DNSType))
		target.DNSValue, err = normalizeDNSFailoverTargetValue(target.DNSType, target.DNSValue)
		if err != nil {
			return fmt.Errorf("目标 %q: %w", target.Name, err)
		}
		target.CheckHost, err = normalizeDNSFailoverCheckHost(target.CheckHost)
		if err != nil {
			return fmt.Errorf("目标 %q 的检测主机格式不正确", target.Name)
		}
		if target.CheckPort < 1 || target.CheckPort > 65535 {
			return fmt.Errorf("目标 %q 的检测端口必须在 1 到 65535 之间", target.Name)
		}
		if target.Enabled {
			enabledTargets++
		}
	}
	if enabledTargets == 0 {
		return errors.New("至少需要一个启用目标")
	}
	sort.SliceStable(request.Targets, func(i, j int) bool {
		return request.Targets[i].Sort < request.Targets[j].Sort
	})

	probeIDs := make([]int64, 0, len(request.ProbeIDs))
	seenProbeIDs := make(map[int64]struct{}, len(request.ProbeIDs))
	for _, probeID := range request.ProbeIDs {
		if probeID <= 0 {
			return errors.New("绑定的探针 ID 无效")
		}
		if _, exists := seenProbeIDs[probeID]; exists {
			continue
		}
		seenProbeIDs[probeID] = struct{}{}
		probeIDs = append(probeIDs, probeID)
	}
	if len(probeIDs) == 0 {
		return errors.New("请至少绑定一个已启用探针")
	}
	sort.Slice(probeIDs, func(i, j int) bool { return probeIDs[i] < probeIDs[j] })
	request.ProbeIDs = probeIDs
	return nil
}

func normalizeDNSFailoverTargetValue(dnsType, value string) (string, error) {
	value = strings.TrimSpace(value)
	address, addressErr := netip.ParseAddr(value)
	switch dnsType {
	case "A":
		if addressErr != nil || !address.Is4() || address.Zone() != "" {
			return "", errors.New("A 记录值必须是有效的 IPv4 地址")
		}
		return address.String(), nil
	case "AAAA":
		if addressErr != nil || !address.Is6() || address.Is4In6() || address.Zone() != "" {
			return "", errors.New("AAAA 记录值必须是有效的 IPv6 地址")
		}
		return address.String(), nil
	case "CNAME":
		name, err := normalizeDNSFailoverHostname(value)
		if err != nil || net.ParseIP(name) != nil {
			return "", errors.New("CNAME 记录值必须是无 scheme、端口和路径的有效域名")
		}
		return name, nil
	default:
		return "", errors.New("DNS 类型仅支持 A、AAAA 或 CNAME")
	}
}

func normalizeDNSFailoverCheckHost(value string) (string, error) {
	value = strings.TrimSpace(value)
	if ip := net.ParseIP(value); ip != nil {
		return ip.String(), nil
	}
	return normalizeDNSFailoverHostname(value)
}

func normalizeDNSFailoverHostname(value string) (string, error) {
	value = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(value), "."))
	if value == "" || len(value) > 253 || strings.ContainsAny(value, ":/?#[]@") {
		return "", errors.New("invalid hostname")
	}
	labels := strings.Split(value, ".")
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", errors.New("invalid hostname")
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
				return "", errors.New("invalid hostname")
			}
		}
	}
	return value, nil
}

func dnsFailoverRuleFromSaveRequest(request DNSFailoverRuleSaveRequest) DNSFailoverRuleRecord {
	return DNSFailoverRuleRecord{
		Name:                        request.Name,
		DomainID:                    request.DomainID,
		Domain:                      request.Domain,
		RecordID:                    request.RecordID,
		Subdomain:                   request.Subdomain,
		RecordLineID:                request.RecordLineID,
		RecordLineName:              request.RecordLineName,
		TTL:                         request.TTL,
		MX:                          request.MX,
		Weight:                      cloneInt64Pointer(request.Weight),
		Enabled:                     request.Enabled,
		AutoFailback:                request.AutoFailback,
		CheckIntervalSec:            request.CheckIntervalSec,
		TCPTimeoutMS:                request.TCPTimeoutMS,
		FailureThreshold:            request.FailureThreshold,
		SuccessThreshold:            request.SuccessThreshold,
		SingleProbeFailureThreshold: request.SingleProbeFailureThreshold,
		SingleProbeSuccessThreshold: request.SingleProbeSuccessThreshold,
		ProbeOfflineSec:             request.ProbeOfflineSec,
		CooldownSec:                 request.CooldownSec,
		ProbeIDs:                    append([]int64(nil), request.ProbeIDs...),
	}
}

func dnsFailoverTargetFromSaveRequest(groupID int64, request DNSFailoverTargetSaveRequest) DNSFailoverTargetRecord {
	return DNSFailoverTargetRecord{
		ID:        request.ID,
		GroupID:   groupID,
		Sort:      request.Sort,
		Name:      request.Name,
		DNSType:   request.DNSType,
		DNSValue:  request.DNSValue,
		CheckHost: request.CheckHost,
		CheckPort: request.CheckPort,
		Enabled:   request.Enabled,
	}
}

func dnsFailoverPlaceholders(startAt int, values []int64) (string, []any) {
	parts := make([]string, 0, len(values))
	args := make([]any, 0, len(values))
	for index, value := range values {
		parts = append(parts, fmt.Sprintf("$%d", startAt+index))
		args = append(args, value)
	}
	return strings.Join(parts, ", "), args
}

func dnsNullInt64Pointer(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	return int64Pointer(value.Int64)
}

func int64Pointer(value int64) *int64 {
	return &value
}

func cloneInt64Pointer(value *int64) *int64 {
	if value == nil {
		return nil
	}
	return int64Pointer(*value)
}

func dnsInt64PointerValue(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}
