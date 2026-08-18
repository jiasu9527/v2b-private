package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"time"
)

const (
	defaultClientEntryBackupIPIntervalSec = defaultClientEntryMonitorIntervalSec
	defaultClientEntryBackupIPTimeoutMS   = defaultClientEntryMonitorTimeoutMS
	clientEntryBackupIPSuccessThreshold   = int64(2)
	clientEntryBackupIPMaxBatch           = 500
)

var (
	ErrClientEntryBackupIPNotFound     = errors.New("备用 IP 不存在")
	ErrClientEntryBackupIPConflict     = errors.New("相同的备用 IP 已存在")
	ErrClientEntryBackupIPInUse        = errors.New("备用 IP 正被入口规则使用，请先更换对应入口后再删除")
	ErrClientEntryBackupIPInsufficient = errors.New("健康且未使用的备用 IP 不足")
)

// ClientEntryBackupIPUsage identifies an entry rule that currently consumes
// an address from the pool. Pool health and rule ownership deliberately remain
// separate: deleting a pool row never mutates a client-entry rule.
type ClientEntryBackupIPUsage struct {
	Kind           string `json:"kind"`
	PolicyID       int64  `json:"policy_id"`
	PolicyName     string `json:"policy_name"`
	SplitGroupID   int64  `json:"split_group_id,omitempty"`
	SplitGroupName string `json:"split_group_name,omitempty"`
	SplitGroupPath string `json:"split_group_path,omitempty"`
}

type ClientEntryBackupIPState struct {
	ProbeID            int64  `json:"probe_id"`
	ProbeName          string `json:"probe_name"`
	ProbeOnline        bool   `json:"probe_online"`
	LastSuccess        *bool  `json:"last_success"`
	LastLatencyMS      *int64 `json:"last_latency_ms"`
	LastError          string `json:"last_error"`
	ConsecutiveSuccess int64  `json:"consecutive_success"`
	ConsecutiveFailure int64  `json:"consecutive_failure"`
	LastReportedAt     *int64 `json:"last_reported_at"`
	Stale              bool   `json:"stale"`
}

type ClientEntryBackupIPRecord struct {
	ID                int64                      `json:"id"`
	Name              string                     `json:"name"`
	IP                string                     `json:"ip"`
	Port              int64                      `json:"port"`
	Enabled           bool                       `json:"enabled"`
	CheckIntervalSec  int64                      `json:"check_interval_sec"`
	TCPTimeoutMS      int64                      `json:"tcp_timeout_ms"`
	Generation        int64                      `json:"generation"`
	Sort              int64                      `json:"sort"`
	QuarantineUntil   int64                      `json:"quarantine_until"`
	Status            string                     `json:"status"`
	Available         bool                       `json:"available"`
	Used              bool                       `json:"used"`
	EnabledProbeCount int64                      `json:"enabled_probe_count"`
	OnlineProbeCount  int64                      `json:"online_probe_count"`
	HealthyProbeCount int64                      `json:"healthy_probe_count"`
	Usages            []ClientEntryBackupIPUsage `json:"usages"`
	States            []ClientEntryBackupIPState `json:"states"`
	CreatedAt         int64                      `json:"created_at"`
	UpdatedAt         int64                      `json:"updated_at"`
}

type ClientEntryBackupIPList struct {
	Items []ClientEntryBackupIPRecord `json:"items"`
}

// Pointer fields keep create/update defaults explicit without accepting
// silently disabled rows when an older UI omits optional properties.
type ClientEntryBackupIPSaveRequest struct {
	Name             string `json:"name"`
	IP               string `json:"ip"`
	Port             int64  `json:"port"`
	Enabled          *bool  `json:"enabled,omitempty"`
	CheckIntervalSec int64  `json:"check_interval_sec,omitempty"`
	TCPTimeoutMS     int64  `json:"tcp_timeout_ms,omitempty"`
	Sort             int64  `json:"sort,omitempty"`
}

// ClientEntryBackupIPCreateRequest accepts either one resource in the top-level
// fields or an atomic batch in items. New UIs should use items; the single form
// keeps the endpoint convenient for scripts.
type ClientEntryBackupIPCreateRequest struct {
	ClientEntryBackupIPSaveRequest
	Items []ClientEntryBackupIPSaveRequest `json:"items,omitempty"`
}

type ClientEntryBackupIPRefreshResult struct {
	Updated int64 `json:"updated"`
}

type ClientEntryBackupIPAdminService interface {
	ListClientEntryBackupIPs(context.Context) (ClientEntryBackupIPList, error)
	CreateClientEntryBackupIP(context.Context, ClientEntryBackupIPSaveRequest) (ClientEntryBackupIPRecord, error)
	CreateClientEntryBackupIPs(context.Context, []ClientEntryBackupIPSaveRequest) ([]ClientEntryBackupIPRecord, error)
	UpdateClientEntryBackupIP(context.Context, int64, ClientEntryBackupIPSaveRequest) (ClientEntryBackupIPRecord, error)
	DeleteClientEntryBackupIP(context.Context, int64) (bool, error)
	RefreshClientEntryBackupIPs(context.Context, []int64) (ClientEntryBackupIPRefreshResult, error)
}

type clientEntryBackupIPProbe struct {
	ID              int64
	Name            string
	LastHeartbeatAt sql.NullInt64
}

func (s *DBService) ListClientEntryBackupIPs(ctx context.Context) (ClientEntryBackupIPList, error) {
	if s == nil || s.db == nil {
		return ClientEntryBackupIPList{}, ErrUnavailable
	}
	if err := s.ensureDNSFailoverSchema(ctx); err != nil {
		return ClientEntryBackupIPList{}, err
	}

	rows, err := s.db.QueryContext(ctx, `SELECT id, name, ip, port, enabled, check_interval_sec,
tcp_timeout_ms, generation, sort, quarantine_until, created_at, updated_at
FROM v2_client_entry_backup_ip
ORDER BY sort ASC, id ASC`)
	if err != nil {
		return ClientEntryBackupIPList{}, fmt.Errorf("查询备用 IP 池失败: %w", err)
	}
	items := make([]ClientEntryBackupIPRecord, 0)
	locations := make(map[int64]int)
	for rows.Next() {
		var item ClientEntryBackupIPRecord
		var enabled int64
		if err := rows.Scan(&item.ID, &item.Name, &item.IP, &item.Port, &enabled,
			&item.CheckIntervalSec, &item.TCPTimeoutMS, &item.Generation, &item.Sort,
			&item.QuarantineUntil, &item.CreatedAt, &item.UpdatedAt); err != nil {
			_ = rows.Close()
			return ClientEntryBackupIPList{}, fmt.Errorf("读取备用 IP 失败: %w", err)
		}
		item.Enabled = enabled == 1
		item.Usages = make([]ClientEntryBackupIPUsage, 0)
		item.States = make([]ClientEntryBackupIPState, 0)
		locations[item.ID] = len(items)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return ClientEntryBackupIPList{}, fmt.Errorf("遍历备用 IP 失败: %w", err)
	}
	_ = rows.Close()
	if len(items) == 0 {
		return ClientEntryBackupIPList{Items: items}, nil
	}

	probes, err := loadClientEntryBackupIPProbes(ctx, s.db)
	if err != nil {
		return ClientEntryBackupIPList{}, err
	}
	now := time.Now().Unix()
	probeByID := make(map[int64]clientEntryBackupIPProbe, len(probes))
	for _, probe := range probes {
		probeByID[probe.ID] = probe
	}
	for index := range items {
		items[index].EnabledProbeCount = int64(len(probes))
		for _, probe := range probes {
			online := dnsProbeHeartbeatFresh(probe.LastHeartbeatAt, now, defaultProbeOfflineSec)
			if online {
				items[index].OnlineProbeCount++
			}
			items[index].States = append(items[index].States, ClientEntryBackupIPState{
				ProbeID: probe.ID, ProbeName: probe.Name, ProbeOnline: online, Stale: true,
			})
		}
	}

	stateRows, err := s.db.QueryContext(ctx, `SELECT state.backup_ip_id, state.probe_id, state.last_success,
state.last_latency_ms, state.last_error, state.consecutive_success, state.consecutive_failure,
state.last_reported_at
FROM v2_client_entry_backup_ip_state state
JOIN v2_dns_probe probe ON probe.id = state.probe_id AND probe.enabled = 1
ORDER BY state.backup_ip_id, state.probe_id`)
	if err != nil {
		return ClientEntryBackupIPList{}, fmt.Errorf("查询备用 IP 测活状态失败: %w", err)
	}
	stateLocations := make(map[int64]map[int64]int, len(items))
	for index := range items {
		stateLocations[items[index].ID] = make(map[int64]int, len(items[index].States))
		for stateIndex := range items[index].States {
			stateLocations[items[index].ID][items[index].States[stateIndex].ProbeID] = stateIndex
		}
	}
	for stateRows.Next() {
		var backupIPID, probeID int64
		var success, latency, reported sql.NullInt64
		var lastError string
		var successStreak, failureStreak int64
		if err := stateRows.Scan(&backupIPID, &probeID, &success, &latency, &lastError,
			&successStreak, &failureStreak, &reported); err != nil {
			_ = stateRows.Close()
			return ClientEntryBackupIPList{}, fmt.Errorf("读取备用 IP 测活状态失败: %w", err)
		}
		itemIndex, exists := locations[backupIPID]
		if !exists {
			continue
		}
		stateIndex, exists := stateLocations[backupIPID][probeID]
		probe, probeExists := probeByID[probeID]
		if !exists || !probeExists {
			continue
		}
		state := ClientEntryBackupIPState{
			ProbeID: probeID, ProbeName: probe.Name,
			ProbeOnline: dnsProbeHeartbeatFresh(probe.LastHeartbeatAt, now, defaultProbeOfflineSec),
			LastError:   lastError, ConsecutiveSuccess: successStreak,
			ConsecutiveFailure: failureStreak,
			Stale: !dnsFailoverProbeStateFresh(reported, now, items[itemIndex].CheckIntervalSec,
				items[itemIndex].TCPTimeoutMS, defaultProbeOfflineSec),
		}
		if success.Valid {
			value := success.Int64 == 1
			state.LastSuccess = &value
		}
		if latency.Valid {
			value := latency.Int64
			state.LastLatencyMS = &value
		}
		if reported.Valid {
			value := reported.Int64
			state.LastReportedAt = &value
		}
		items[itemIndex].States[stateIndex] = state
	}
	if err := stateRows.Err(); err != nil {
		_ = stateRows.Close()
		return ClientEntryBackupIPList{}, fmt.Errorf("遍历备用 IP 测活状态失败: %w", err)
	}
	_ = stateRows.Close()

	usageByIP, err := loadClientEntryBackupIPUsages(ctx, s.db)
	if err != nil {
		return ClientEntryBackupIPList{}, err
	}
	for index := range items {
		items[index].Usages = append(items[index].Usages, usageByIP[items[index].IP]...)
		items[index].Used = len(items[index].Usages) > 0
		for _, state := range items[index].States {
			if state.ProbeOnline && !state.Stale && state.LastSuccess != nil && *state.LastSuccess &&
				state.ConsecutiveSuccess >= clientEntryBackupIPSuccessThreshold {
				items[index].HealthyProbeCount++
			}
		}
		items[index].Status, items[index].Available = classifyClientEntryBackupIP(items[index], now)
	}
	return ClientEntryBackupIPList{Items: items}, nil
}

func loadClientEntryBackupIPProbes(ctx context.Context, queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}) ([]clientEntryBackupIPProbe, error) {
	rows, err := queryer.QueryContext(ctx, `SELECT id, name, last_heartbeat_at
FROM v2_dns_probe WHERE enabled = 1 ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("查询备用 IP 探针失败: %w", err)
	}
	defer rows.Close()
	result := make([]clientEntryBackupIPProbe, 0)
	for rows.Next() {
		var probe clientEntryBackupIPProbe
		if err := rows.Scan(&probe.ID, &probe.Name, &probe.LastHeartbeatAt); err != nil {
			return nil, fmt.Errorf("读取备用 IP 探针失败: %w", err)
		}
		result = append(result, probe)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历备用 IP 探针失败: %w", err)
	}
	return result, nil
}

func classifyClientEntryBackupIP(item ClientEntryBackupIPRecord, now int64) (string, bool) {
	if !item.Enabled {
		return "disabled", false
	}
	if item.Used {
		return "in_use", false
	}
	if item.QuarantineUntil > now {
		return "quarantined", false
	}
	if item.EnabledProbeCount == 0 {
		return "no_probe", false
	}
	if item.OnlineProbeCount == 0 {
		return "probe_offline", false
	}
	if item.HealthyProbeCount == item.OnlineProbeCount {
		return "available", true
	}
	for _, state := range item.States {
		if state.ProbeOnline && !state.Stale && state.LastSuccess != nil && !*state.LastSuccess {
			return "unhealthy", false
		}
	}
	return "checking", false
}

func (s *DBService) CreateClientEntryBackupIP(ctx context.Context, request ClientEntryBackupIPSaveRequest) (ClientEntryBackupIPRecord, error) {
	items, err := s.CreateClientEntryBackupIPs(ctx, []ClientEntryBackupIPSaveRequest{request})
	if err != nil {
		return ClientEntryBackupIPRecord{}, err
	}
	return items[0], nil
}

func (s *DBService) CreateClientEntryBackupIPs(ctx context.Context, requests []ClientEntryBackupIPSaveRequest) ([]ClientEntryBackupIPRecord, error) {
	if s == nil || s.db == nil {
		return nil, ErrUnavailable
	}
	if err := s.ensureDNSFailoverSchema(ctx); err != nil {
		return nil, err
	}
	if len(requests) == 0 {
		return nil, errors.New("备用 IP 列表不能为空")
	}
	if len(requests) > clientEntryBackupIPMaxBatch {
		return nil, fmt.Errorf("一次最多添加 %d 个备用 IP", clientEntryBackupIPMaxBatch)
	}
	prepared := make([]ClientEntryBackupIPSaveRequest, len(requests))
	seen := make(map[string]struct{}, len(requests))
	for index, request := range requests {
		item, err := normalizeClientEntryBackupIPSaveRequest(request, true)
		if err != nil {
			return nil, fmt.Errorf("第 %d 个备用 IP 无效：%w", index+1, err)
		}
		key := item.IP
		if _, exists := seen[key]; exists {
			return nil, fmt.Errorf("第 %d 个备用 IP 与本次列表中的其他项目重复", index+1)
		}
		seen[key] = struct{}{}
		prepared[index] = item
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("创建备用 IP 失败: %w", err)
	}
	defer tx.Rollback()
	for _, item := range prepared {
		if err := rejectDuplicateClientEntryBackupIP(ctx, tx, 0, item.IP); err != nil {
			return nil, err
		}
	}
	now := time.Now().Unix()
	result := make([]ClientEntryBackupIPRecord, 0, len(prepared))
	for _, item := range prepared {
		var id int64
		if err := tx.QueryRowContext(ctx, `INSERT INTO v2_client_entry_backup_ip
(name, ip, port, enabled, check_interval_sec, tcp_timeout_ms, generation, sort,
quarantine_until, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, 1, $7, 0, $8, $8)
	RETURNING id`, item.Name, item.IP, item.Port, boolToInt64(*item.Enabled),
			item.CheckIntervalSec, item.TCPTimeoutMS, item.Sort, now).Scan(&id); err != nil {
			if isClientEntryBackupIPUniqueError(err) {
				return nil, ErrClientEntryBackupIPConflict
			}
			return nil, fmt.Errorf("创建备用 IP 失败: %w", err)
		}
		status := "checking"
		if !*item.Enabled {
			status = "disabled"
		}
		result = append(result, ClientEntryBackupIPRecord{
			ID: id, Name: item.Name, IP: item.IP, Port: item.Port, Enabled: *item.Enabled,
			CheckIntervalSec: item.CheckIntervalSec, TCPTimeoutMS: item.TCPTimeoutMS,
			Generation: 1, Sort: item.Sort, Status: status,
			Usages: []ClientEntryBackupIPUsage{}, States: []ClientEntryBackupIPState{},
			CreatedAt: now, UpdatedAt: now,
		})
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("提交备用 IP 失败: %w", err)
	}
	return result, nil
}

func (s *DBService) UpdateClientEntryBackupIP(ctx context.Context, id int64, request ClientEntryBackupIPSaveRequest) (ClientEntryBackupIPRecord, error) {
	if s == nil || s.db == nil {
		return ClientEntryBackupIPRecord{}, ErrUnavailable
	}
	if id <= 0 {
		return ClientEntryBackupIPRecord{}, ErrClientEntryBackupIPNotFound
	}
	if err := s.ensureDNSFailoverSchema(ctx); err != nil {
		return ClientEntryBackupIPRecord{}, err
	}
	prepared, err := normalizeClientEntryBackupIPSaveRequest(request, false)
	if err != nil {
		return ClientEntryBackupIPRecord{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ClientEntryBackupIPRecord{}, fmt.Errorf("更新备用 IP 失败: %w", err)
	}
	defer tx.Rollback()
	var oldIP string
	var oldPort, oldEnabled, oldInterval, oldTimeout int64
	if err := tx.QueryRowContext(ctx, `SELECT ip, port, enabled, check_interval_sec, tcp_timeout_ms
FROM v2_client_entry_backup_ip WHERE id = $1 FOR UPDATE`, id).Scan(
		&oldIP, &oldPort, &oldEnabled, &oldInterval, &oldTimeout,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ClientEntryBackupIPRecord{}, ErrClientEntryBackupIPNotFound
		}
		return ClientEntryBackupIPRecord{}, fmt.Errorf("读取备用 IP 失败: %w", err)
	}
	if err := rejectDuplicateClientEntryBackupIP(ctx, tx, id, prepared.IP); err != nil {
		return ClientEntryBackupIPRecord{}, err
	}
	if prepared.IP != oldIP {
		used, err := clientEntryBackupIPHostInUse(ctx, tx, oldIP)
		if err != nil {
			return ClientEntryBackupIPRecord{}, err
		}
		if used {
			return ClientEntryBackupIPRecord{}, ErrClientEntryBackupIPInUse
		}
	}
	now := time.Now().Unix()
	taskChanged := oldIP != prepared.IP || oldPort != prepared.Port || oldEnabled != boolToInt64(*prepared.Enabled) ||
		oldInterval != prepared.CheckIntervalSec || oldTimeout != prepared.TCPTimeoutMS
	var generation, quarantineUntil, createdAt, updatedAt int64
	if err := tx.QueryRowContext(ctx, `UPDATE v2_client_entry_backup_ip SET
name = $2, ip = $3, port = $4, enabled = $5, check_interval_sec = $6,
tcp_timeout_ms = $7, sort = $8,
generation = CASE WHEN ip IS DISTINCT FROM $3 OR port IS DISTINCT FROM $4
  OR enabled IS DISTINCT FROM $5 OR check_interval_sec IS DISTINCT FROM $6
  OR tcp_timeout_ms IS DISTINCT FROM $7 THEN generation + 1 ELSE generation END,
updated_at = $9
WHERE id = $1
RETURNING generation, quarantine_until, created_at, updated_at`, id, prepared.Name, prepared.IP,
		prepared.Port, boolToInt64(*prepared.Enabled), prepared.CheckIntervalSec, prepared.TCPTimeoutMS,
		prepared.Sort, now).Scan(&generation, &quarantineUntil, &createdAt, &updatedAt); err != nil {
		if isClientEntryBackupIPUniqueError(err) {
			return ClientEntryBackupIPRecord{}, ErrClientEntryBackupIPConflict
		}
		return ClientEntryBackupIPRecord{}, fmt.Errorf("更新备用 IP 失败: %w", err)
	}
	if taskChanged {
		if _, err := tx.ExecContext(ctx, `DELETE FROM v2_client_entry_backup_ip_state WHERE backup_ip_id = $1`, id); err != nil {
			return ClientEntryBackupIPRecord{}, fmt.Errorf("重置备用 IP 测活状态失败: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return ClientEntryBackupIPRecord{}, fmt.Errorf("提交备用 IP 更新失败: %w", err)
	}
	status := "checking"
	if !*prepared.Enabled {
		status = "disabled"
	}
	return ClientEntryBackupIPRecord{
		ID: id, Name: prepared.Name, IP: prepared.IP, Port: prepared.Port, Enabled: *prepared.Enabled,
		CheckIntervalSec: prepared.CheckIntervalSec, TCPTimeoutMS: prepared.TCPTimeoutMS,
		Generation: generation, Sort: prepared.Sort, QuarantineUntil: quarantineUntil, Status: status,
		Usages: []ClientEntryBackupIPUsage{}, States: []ClientEntryBackupIPState{},
		CreatedAt: createdAt, UpdatedAt: updatedAt,
	}, nil
}

func (s *DBService) DeleteClientEntryBackupIP(ctx context.Context, id int64) (bool, error) {
	if s == nil || s.db == nil {
		return false, ErrUnavailable
	}
	if id <= 0 {
		return false, ErrClientEntryBackupIPNotFound
	}
	if err := s.ensureDNSFailoverSchema(ctx); err != nil {
		return false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("删除备用 IP 失败: %w", err)
	}
	defer tx.Rollback()
	var ip string
	if err := tx.QueryRowContext(ctx, `SELECT ip FROM v2_client_entry_backup_ip WHERE id = $1 FOR UPDATE`, id).Scan(&ip); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, ErrClientEntryBackupIPNotFound
		}
		return false, fmt.Errorf("读取备用 IP 失败: %w", err)
	}
	used, err := clientEntryBackupIPHostInUse(ctx, tx, ip)
	if err != nil {
		return false, err
	}
	if used {
		return false, ErrClientEntryBackupIPInUse
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM v2_client_entry_backup_ip WHERE id = $1`, id)
	if err != nil {
		return false, fmt.Errorf("删除备用 IP 失败: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("确认备用 IP 删除结果失败: %w", err)
	}
	if affected == 0 {
		return false, ErrClientEntryBackupIPNotFound
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("提交备用 IP 删除失败: %w", err)
	}
	return true, nil
}

func clientEntryBackupIPHostInUse(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, ip string) (bool, error) {
	var used bool
	if err := queryer.QueryRowContext(ctx, `SELECT EXISTS (
	SELECT 1 FROM v2_client_entry_user_policy policy WHERE btrim(policy.entry_host) = $1
	UNION ALL
	SELECT 1 FROM v2_client_entry_user_policy_split_group split_group WHERE btrim(split_group.entry_host) = $1
)`, ip).Scan(&used); err != nil {
		return false, fmt.Errorf("检查备用 IP 占用失败: %w", err)
	}
	return used, nil
}

func (s *DBService) RefreshClientEntryBackupIPs(ctx context.Context, ids []int64) (ClientEntryBackupIPRefreshResult, error) {
	if s == nil || s.db == nil {
		return ClientEntryBackupIPRefreshResult{}, ErrUnavailable
	}
	if err := s.ensureDNSFailoverSchema(ctx); err != nil {
		return ClientEntryBackupIPRefreshResult{}, err
	}
	ids, err := normalizeClientEntryBackupIPIDs(ids)
	if err != nil {
		return ClientEntryBackupIPRefreshResult{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ClientEntryBackupIPRefreshResult{}, fmt.Errorf("刷新备用 IP 测活失败: %w", err)
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	var result sql.Result
	if len(ids) == 0 {
		result, err = tx.ExecContext(ctx, `UPDATE v2_client_entry_backup_ip
SET generation = generation + 1, updated_at = $1 WHERE enabled = 1`, now)
		if err == nil {
			_, err = tx.ExecContext(ctx, `DELETE FROM v2_client_entry_backup_ip_state state
USING v2_client_entry_backup_ip backup WHERE backup.id = state.backup_ip_id AND backup.enabled = 1`)
		}
	} else {
		placeholders := make([]string, len(ids))
		args := make([]any, 0, len(ids)+1)
		args = append(args, now)
		for index, id := range ids {
			placeholders[index] = fmt.Sprintf("$%d", index+2)
			args = append(args, id)
		}
		clause := strings.Join(placeholders, ",")
		result, err = tx.ExecContext(ctx, `UPDATE v2_client_entry_backup_ip
SET generation = generation + 1, updated_at = $1
WHERE enabled = 1 AND id IN (`+clause+`)`, args...)
		if err == nil {
			deletePlaceholders := make([]string, len(ids))
			deleteArgs := make([]any, len(ids))
			for index, id := range ids {
				deletePlaceholders[index] = fmt.Sprintf("$%d", index+1)
				deleteArgs[index] = id
			}
			_, err = tx.ExecContext(ctx, `DELETE FROM v2_client_entry_backup_ip_state
WHERE backup_ip_id IN (`+strings.Join(deletePlaceholders, ",")+`)`, deleteArgs...)
		}
	}
	if err != nil {
		return ClientEntryBackupIPRefreshResult{}, fmt.Errorf("刷新备用 IP 测活失败: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return ClientEntryBackupIPRefreshResult{}, fmt.Errorf("确认备用 IP 刷新结果失败: %w", err)
	}
	if len(ids) > 0 && affected != int64(len(ids)) {
		return ClientEntryBackupIPRefreshResult{}, errors.New("部分备用 IP 不存在或已停用，请刷新后重试")
	}
	if err := tx.Commit(); err != nil {
		return ClientEntryBackupIPRefreshResult{}, fmt.Errorf("提交备用 IP 刷新失败: %w", err)
	}
	return ClientEntryBackupIPRefreshResult{Updated: affected}, nil
}

func normalizeClientEntryBackupIPSaveRequest(request ClientEntryBackupIPSaveRequest, create bool) (ClientEntryBackupIPSaveRequest, error) {
	request.Name = strings.TrimSpace(request.Name)
	if len([]rune(request.Name)) > 255 {
		return request, errors.New("备用 IP 名称不能超过 255 个字符")
	}
	address, err := netip.ParseAddr(strings.TrimSpace(request.IP))
	if err != nil || address.Zone() != "" {
		return request, errors.New("备用 IP 必须是有效的 IPv4 或 IPv6 地址")
	}
	address = address.Unmap()
	if address.IsUnspecified() {
		return request, errors.New("备用 IP 必须是有效的 IPv4 或 IPv6 地址")
	}
	request.IP = address.String()
	if request.Port <= 0 || request.Port > 65535 {
		return request, errors.New("备用 IP TCP 端口必须在 1 到 65535 之间")
	}
	if request.CheckIntervalSec == 0 {
		request.CheckIntervalSec = defaultClientEntryBackupIPIntervalSec
	}
	if request.CheckIntervalSec < 5 || request.CheckIntervalSec > 3600 {
		return request, errors.New("备用 IP 检测间隔必须在 5 到 3600 秒之间")
	}
	if request.TCPTimeoutMS == 0 {
		request.TCPTimeoutMS = defaultClientEntryBackupIPTimeoutMS
	}
	if request.TCPTimeoutMS < 100 || request.TCPTimeoutMS > 60_000 {
		return request, errors.New("备用 IP TCP 超时必须在 100 到 60000 毫秒之间")
	}
	if request.Sort < 0 {
		return request, errors.New("备用 IP 排序不能小于 0")
	}
	if request.Enabled == nil {
		value := true
		request.Enabled = &value
	}
	_ = create // kept in the signature for future create-only defaults.
	return request, nil
}

func normalizeClientEntryBackupIPIDs(ids []int64) ([]int64, error) {
	if len(ids) > clientEntryBackupIPMaxBatch {
		return nil, fmt.Errorf("一次最多刷新 %d 个备用 IP", clientEntryBackupIPMaxBatch)
	}
	seen := make(map[int64]struct{}, len(ids))
	result := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return nil, errors.New("备用 IP ID 无效")
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result, nil
}

func rejectDuplicateClientEntryBackupIP(ctx context.Context, tx *sql.Tx, excludeID int64, ip string) error {
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM v2_client_entry_backup_ip
WHERE ip = $1 AND id <> $2
LIMIT 1 FOR SHARE`, ip, excludeID).Scan(&id)
	if err == nil {
		return ErrClientEntryBackupIPConflict
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return fmt.Errorf("检查备用 IP 重复失败: %w", err)
}

func isClientEntryBackupIPUniqueError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "uniq_v2_client_entry_backup_ip_value") ||
		(strings.Contains(message, "duplicate key") && strings.Contains(message, "v2_client_entry_backup_ip"))
}

func loadClientEntryBackupIPUsages(ctx context.Context, queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}) (map[string][]ClientEntryBackupIPUsage, error) {
	result := make(map[string][]ClientEntryBackupIPUsage)
	rows, err := queryer.QueryContext(ctx, `SELECT policy.id, policy.name, policy.entry_host
FROM v2_client_entry_user_policy policy
WHERE btrim(policy.entry_host) <> ''
ORDER BY policy.id`)
	if err != nil {
		return nil, fmt.Errorf("查询备用 IP 普通规则占用失败: %w", err)
	}
	for rows.Next() {
		var usage ClientEntryBackupIPUsage
		var host string
		if err := rows.Scan(&usage.PolicyID, &usage.PolicyName, &host); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("读取备用 IP 普通规则占用失败: %w", err)
		}
		ip, ok := canonicalClientEntryBackupIPHost(host)
		if !ok {
			continue
		}
		usage.Kind = "policy"
		result[ip] = append(result[ip], usage)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("遍历备用 IP 普通规则占用失败: %w", err)
	}
	_ = rows.Close()

	rows, err = queryer.QueryContext(ctx, `SELECT policy.id, policy.name, split_group.id,
split_group.name, split_group.path, split_group.entry_host
FROM v2_client_entry_user_policy_split_group split_group
JOIN v2_client_entry_user_policy policy ON policy.id = split_group.policy_id
WHERE btrim(split_group.entry_host) <> ''
ORDER BY policy.id, split_group.id`)
	if err != nil {
		return nil, fmt.Errorf("查询备用 IP 二分组占用失败: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var usage ClientEntryBackupIPUsage
		var host string
		if err := rows.Scan(&usage.PolicyID, &usage.PolicyName, &usage.SplitGroupID,
			&usage.SplitGroupName, &usage.SplitGroupPath, &host); err != nil {
			return nil, fmt.Errorf("读取备用 IP 二分组占用失败: %w", err)
		}
		ip, ok := canonicalClientEntryBackupIPHost(host)
		if !ok {
			continue
		}
		usage.Kind = "split_group"
		result[ip] = append(result[ip], usage)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历备用 IP 二分组占用失败: %w", err)
	}
	return result, nil
}

func canonicalClientEntryBackupIPHost(host string) (string, bool) {
	address, err := netip.ParseAddr(strings.TrimSpace(host))
	if err != nil || address.Zone() != "" {
		return "", false
	}
	return address.Unmap().String(), true
}

// claimHealthyClientEntryBackupIPs must be called inside the same transaction
// that writes the destination split groups. FOR UPDATE SKIP LOCKED prevents two
// concurrent incidents from selecting the same pool rows; the caller's rule
// writes make the ownership durable before commit.
func claimHealthyClientEntryBackupIPs(ctx context.Context, tx *sql.Tx, count, now int64) ([]ClientEntryBackupIPRecord, error) {
	if tx == nil || count <= 0 || count > clientEntryBackupIPMaxBatch {
		return nil, errors.New("备用 IP 领取参数无效")
	}
	rows, err := tx.QueryContext(ctx, `SELECT backup.id, backup.name, backup.ip, backup.port,
backup.enabled, backup.check_interval_sec, backup.tcp_timeout_ms, backup.generation,
backup.sort, backup.quarantine_until, backup.created_at, backup.updated_at
FROM v2_client_entry_backup_ip backup
WHERE backup.enabled = 1
  AND backup.quarantine_until <= $1
  AND NOT EXISTS (
	SELECT 1 FROM v2_client_entry_user_policy policy
	WHERE btrim(policy.entry_host) = backup.ip
  )
  AND NOT EXISTS (
	SELECT 1 FROM v2_client_entry_user_policy_split_group split_group
	WHERE btrim(split_group.entry_host) = backup.ip
  )
  AND EXISTS (
	SELECT 1 FROM v2_dns_probe probe
	WHERE probe.enabled = 1
	  AND probe.last_heartbeat_at IS NOT NULL
	  AND probe.last_heartbeat_at BETWEEN $1 - $2 AND $1
  )
  AND NOT EXISTS (
	SELECT 1 FROM v2_dns_probe probe
	WHERE probe.enabled = 1
	  AND probe.last_heartbeat_at IS NOT NULL
	  AND probe.last_heartbeat_at BETWEEN $1 - $2 AND $1
	  AND NOT EXISTS (
		SELECT 1 FROM v2_client_entry_backup_ip_state state
		WHERE state.backup_ip_id = backup.id AND state.probe_id = probe.id
		  AND state.last_success = 1
		  AND state.consecutive_success >= $3
		  AND state.last_reported_at IS NOT NULL
		  AND state.last_reported_at >= $1 - LEAST($2,
		      2 * (backup.check_interval_sec::BIGINT + (backup.tcp_timeout_ms::BIGINT + 999) / 1000))
	  )
  )
ORDER BY backup.sort ASC, backup.id ASC
LIMIT $4
FOR UPDATE OF backup SKIP LOCKED`, now, defaultProbeOfflineSec, clientEntryBackupIPSuccessThreshold, count)
	if err != nil {
		return nil, fmt.Errorf("领取健康备用 IP 失败: %w", err)
	}
	defer rows.Close()
	result := make([]ClientEntryBackupIPRecord, 0, count)
	for rows.Next() {
		var item ClientEntryBackupIPRecord
		var enabled int64
		if err := rows.Scan(&item.ID, &item.Name, &item.IP, &item.Port, &enabled,
			&item.CheckIntervalSec, &item.TCPTimeoutMS, &item.Generation, &item.Sort,
			&item.QuarantineUntil, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, fmt.Errorf("读取健康备用 IP 失败: %w", err)
		}
		item.Enabled = enabled == 1
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历健康备用 IP 失败: %w", err)
	}
	if int64(len(result)) != count {
		return nil, fmt.Errorf("%w，需要 %d 个，当前可分配 %d 个", ErrClientEntryBackupIPInsufficient, count, len(result))
	}
	return result, nil
}

func quarantineClientEntryBackupIPByHost(ctx context.Context, tx *sql.Tx, host string, until, now int64) error {
	if tx == nil {
		return errors.New("备用 IP 隔离事务无效")
	}
	ip, ok := canonicalClientEntryBackupIPHost(host)
	if !ok {
		return nil
	}
	if until < 0 {
		until = 0
	}
	_, err := tx.ExecContext(ctx, `UPDATE v2_client_entry_backup_ip
SET quarantine_until = GREATEST(quarantine_until, $2), updated_at = $3
WHERE ip = $1`, ip, until, now)
	if err != nil {
		return fmt.Errorf("隔离故障备用 IP 失败: %w", err)
	}
	return nil
}
