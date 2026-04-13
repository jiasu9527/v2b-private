package user

import (
	"context"
	"crypto/md5"
	crand "crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

type serverFetchUser struct {
	ID             int64
	GroupID        int64
	PlanID         int64
	TransferEnable int64
	Banned         int64
	CreatedAt      int64
	ExpiredAt      sql.NullInt64
}

type serverFetchTable struct {
	serverType         string
	table              string
	lastCheckKeyPrefix string
	randomizePort      bool
	inheritCreatedAt   bool
}

var (
	serverFetchTables = []serverFetchTable{
		{serverType: "shadowsocks", table: "v2_server_shadowsocks", lastCheckKeyPrefix: "SERVER_SHADOWSOCKS_LAST_CHECK_AT_", randomizePort: true, inheritCreatedAt: true},
		{serverType: "vmess", table: "v2_server_vmess", lastCheckKeyPrefix: "SERVER_VMESS_LAST_CHECK_AT_", randomizePort: true},
		{serverType: "trojan", table: "v2_server_trojan", lastCheckKeyPrefix: "SERVER_TROJAN_LAST_CHECK_AT_", randomizePort: true},
		{serverType: "tuic", table: "v2_server_tuic", lastCheckKeyPrefix: "SERVER_TUIC_LAST_CHECK_AT_", inheritCreatedAt: true},
		{serverType: "hysteria", table: "v2_server_hysteria", lastCheckKeyPrefix: "SERVER_HYSTERIA_LAST_CHECK_AT_", inheritCreatedAt: true},
		{serverType: "vless", table: "v2_server_vless", lastCheckKeyPrefix: "SERVER_VLESS_LAST_CHECK_AT_", randomizePort: true},
		{serverType: "anytls", table: "v2_server_anytls", lastCheckKeyPrefix: "SERVER_ANYTLS_LAST_CHECK_AT_", randomizePort: true, inheritCreatedAt: true},
		{serverType: "v2node", table: "v2_server_v2node", lastCheckKeyPrefix: "SERVER_V2NODE_LAST_CHECK_AT_", inheritCreatedAt: true},
	}

	serverFetchUAWithRangePattern = regexp.MustCompile(`^(.+?)\(U(.+?)(\d+)-(\d+)\)$`)
	serverFetchUAPattern          = regexp.MustCompile(`^(.+?)\(U([^)]+)\)$`)
	serverFetchPlanPattern        = regexp.MustCompile(`^(.+?)\(P(\d+)-(\d+)\)$`)
	serverFetchDayRangePattern    = regexp.MustCompile(`^(.+?)\(D(\d+)-(\d+)\)$`)
	serverFetchDayGTPathern       = regexp.MustCompile(`^(.+?)\(D>(\d+)\)$`)
	serverFetchDayLEPattern       = regexp.MustCompile(`^(.+?)\(D<=(\d+)\)$`)
	serverFetchDayShortPattern    = regexp.MustCompile(`^(.+?)\(D(\d+)\)$`)
	serverFetchUserRangePattern   = regexp.MustCompile(`^(.+?)\((\d+)-(\d+)\)$`)
)

func (s *DBService) Servers(ctx context.Context, userID int64, ua string) ([]map[string]any, error) {
	if s.db == nil {
		return nil, ErrUnavailable
	}

	userRow, err := s.loadServerFetchUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	if !knowledgeUserAvailable(userRow.Banned, userRow.TransferEnable, userRow.ExpiredAt, now) {
		return []map[string]any{}, nil
	}

	servers := make([]map[string]any, 0)
	for _, table := range serverFetchTables {
		rows, err := s.queryServerFetchRows(ctx, table)
		if err != nil {
			return nil, err
		}

		byID := make(map[int64]map[string]any, len(rows))
		for _, row := range rows {
			byID[mapInt64(row["id"])] = row
		}

		for _, row := range rows {
			if !serverGroupAllowed(parseIDString(fmt.Sprint(row["group_id"])), userRow.GroupID) {
				continue
			}
			item, err := s.normalizeServerFetchRow(ctx, table, row, byID)
			if err != nil {
				return nil, err
			}
			servers = append(servers, item)
		}
	}

	sort.SliceStable(servers, func(i, j int) bool {
		leftSort, leftOK := serverSortValue(servers[i]["sort"])
		rightSort, rightOK := serverSortValue(servers[j]["sort"])
		switch {
		case leftOK && rightOK && leftSort != rightSort:
			return leftSort < rightSort
		case leftOK != rightOK:
			return leftOK
		}
		return mapInt64(servers[i]["id"]) < mapInt64(servers[j]["id"])
	})

	filtered := make([]map[string]any, 0, len(servers))
	for _, item := range servers {
		if hostValue, ok := item["host"].(string); ok {
			host, hostOK := parseServerHostByCondition(hostValue, userRow, ua, now)
			if !hostOK {
				continue
			}
			item["host"] = host
		}

		if rawPort, isRange := serverPortRange(item["port"]); isRange {
			item["mport"] = rawPort
		} else if port, ok := serverPortInt(item["port"]); ok {
			item["port"] = port
		}

		lastCheckAt := mapInt64(item["last_check_at"])
		isOnline := int64(0)
		if now-300 <= lastCheckAt {
			isOnline = 1
		}
		item["is_online"] = isOnline
		item["cache_key"] = fmt.Sprintf("%s-%d-%v-%d", item["type"], mapInt64(item["id"]), item["updated_at"], isOnline)
		filtered = append(filtered, item)
	}

	return filtered, nil
}

func (s *DBService) loadServerFetchUser(ctx context.Context, userID int64) (serverFetchUser, error) {
	var row serverFetchUser
	var (
		groupID sql.NullInt64
		planID  sql.NullInt64
	)
	err := s.db.QueryRowContext(ctx, `SELECT id, group_id, plan_id, transfer_enable, banned, created_at, expired_at
FROM v2_user
WHERE id = $1
LIMIT 1`, userID).Scan(
		&row.ID,
		&groupID,
		&planID,
		&row.TransferEnable,
		&row.Banned,
		&row.CreatedAt,
		&row.ExpiredAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return serverFetchUser{}, ErrNotFound
		}
		return serverFetchUser{}, fmt.Errorf("query server fetch user: %w", err)
	}
	row.ExpiredAt = normalizeNullableExpiry(row.ExpiredAt)
	if planID.Valid {
		row.PlanID = planID.Int64
	}
	if groupID.Valid {
		row.GroupID = groupID.Int64
	}
	return row, nil
}

func (s *DBService) queryServerFetchRows(ctx context.Context, table serverFetchTable) ([]map[string]any, error) {
	rows, err := s.queryRowsAsMaps(ctx, `SELECT *
FROM `+table.table+`
WHERE "show" = 1
ORDER BY sort ASC NULLS LAST, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("query %s servers: %w", table.serverType, err)
	}
	return rows, nil
}

func (s *DBService) normalizeServerFetchRow(ctx context.Context, table serverFetchTable, row map[string]any, byID map[int64]map[string]any) (map[string]any, error) {
	item := copyStringAnyMap(row)
	item["type"] = table.serverType
	item["group_id"] = parseIDString(fmt.Sprint(item["group_id"]))
	item["route_id"] = parseIDString(fmt.Sprint(item["route_id"]))
	tags := parseServerStringList(fmt.Sprint(item["tags"]))
	if len(tags) > 0 {
		item["tags"] = tags
	} else {
		delete(item, "tags")
	}

	for _, key := range []string{
		"obfs_settings",
		"network_settings",
		"tls_settings",
		"encryption_settings",
		"networkSettings",
		"tlsSettings",
		"dnsSettings",
		"ruleSettings",
		"padding_scheme",
	} {
		if value, ok := item[key]; ok {
			item[key] = decodeServerJSONValue(value)
		}
	}

	parentID := mapInt64(item["parent_id"])
	lastCheckID := mapInt64(item["id"])
	if parentID > 0 {
		lastCheckID = parentID
		if table.inheritCreatedAt {
			if parent, ok := byID[parentID]; ok {
				item["created_at"] = parent["created_at"]
			}
		}
	}

	lastCheckAt, err := s.serverFetchLastCheckAt(ctx, table.lastCheckKeyPrefix, lastCheckID)
	if err != nil {
		return nil, err
	}
	item["last_check_at"] = lastCheckAt

	if table.randomizePort {
		if port, ok := serverPortRange(item["port"]); ok {
			if randomPort, randomOK := randomPortInRange(port); randomOK {
				item["port"] = randomPort
			}
		}
	}

	switch table.serverType {
	case "shadowsocks":
		if strings.EqualFold(strings.TrimSpace(fmt.Sprint(item["obfs"])), "http") {
			if settings, ok := item["obfs_settings"].(map[string]any); ok {
				if host, ok := settings["host"]; ok {
					item["obfs-host"] = host
				}
				if path, ok := settings["path"]; ok {
					item["obfs-path"] = path
				}
			}
		}
	case "hysteria":
		item["server_key"] = serverFetchServerKey(mapInt64(item["created_at"]), 16)
	case "vless", "v2node":
		item["tls_settings"] = stripPrivateKey(item["tls_settings"])
		item["encryption_settings"] = stripPrivateKey(item["encryption_settings"])
	}

	return item, nil
}

func (s *DBService) serverFetchLastCheckAt(ctx context.Context, prefix string, serverID int64) (int64, error) {
	if prefix == "" || serverID <= 0 {
		return 0, nil
	}
	raw, ok, err := s.kvGet(ctx, prefix+strconv.FormatInt(serverID, 10))
	if err != nil {
		return 0, fmt.Errorf("query %s last_check_at: %w", prefix, err)
	}
	if !ok {
		return 0, nil
	}
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return 0, nil
	}
	return value, nil
}

func parseServerHostByCondition(hostConfig string, user serverFetchUser, ua string, now int64) (string, bool) {
	hostConfig = strings.TrimSpace(hostConfig)
	if hostConfig == "" {
		return "", false
	}

	hosts := strings.Split(hostConfig, ",")
	defaultHost := ""
	hasConditional := false
	registrationDays := int64(-1)
	if user.CreatedAt > 0 && now > user.CreatedAt {
		registrationDays = (now - user.CreatedAt) / 86400
	}

	for _, rawHost := range hosts {
		host := strings.TrimSpace(rawHost)
		if host == "" {
			continue
		}

		if matches := serverFetchUAWithRangePattern.FindStringSubmatch(host); len(matches) == 5 {
			hasConditional = true
			if serverFetchUAHit(matches[2], ua) {
				minID, _ := strconv.ParseInt(matches[3], 10, 64)
				maxID, _ := strconv.ParseInt(matches[4], 10, 64)
				if user.ID >= minID && user.ID <= maxID {
					return strings.TrimSpace(matches[1]), true
				}
			}
			continue
		}

		if matches := serverFetchUAPattern.FindStringSubmatch(host); len(matches) == 3 {
			hasConditional = true
			if serverFetchUAHit(matches[2], ua) {
				return strings.TrimSpace(matches[1]), true
			}
			continue
		}

		if matches := serverFetchPlanPattern.FindStringSubmatch(host); len(matches) == 4 {
			hasConditional = true
			minPlan, _ := strconv.ParseInt(matches[2], 10, 64)
			maxPlan, _ := strconv.ParseInt(matches[3], 10, 64)
			if user.PlanID >= minPlan && user.PlanID <= maxPlan {
				return strings.TrimSpace(matches[1]), true
			}
			continue
		}

		if matches := serverFetchDayRangePattern.FindStringSubmatch(host); len(matches) == 4 {
			hasConditional = true
			if registrationDays >= 0 {
				minDays, _ := strconv.ParseInt(matches[2], 10, 64)
				maxDays, _ := strconv.ParseInt(matches[3], 10, 64)
				if registrationDays >= minDays && registrationDays <= maxDays {
					return strings.TrimSpace(matches[1]), true
				}
			}
			continue
		}

		if matches := serverFetchDayGTPathern.FindStringSubmatch(host); len(matches) == 3 {
			hasConditional = true
			if registrationDays >= 0 {
				minDays, _ := strconv.ParseInt(matches[2], 10, 64)
				if registrationDays > minDays {
					return strings.TrimSpace(matches[1]), true
				}
			}
			continue
		}

		if matches := serverFetchDayLEPattern.FindStringSubmatch(host); len(matches) == 3 {
			hasConditional = true
			if registrationDays >= 0 {
				maxDays, _ := strconv.ParseInt(matches[2], 10, 64)
				if registrationDays <= maxDays {
					return strings.TrimSpace(matches[1]), true
				}
			}
			continue
		}

		if matches := serverFetchDayShortPattern.FindStringSubmatch(host); len(matches) == 3 {
			hasConditional = true
			if registrationDays >= 0 {
				maxDays, _ := strconv.ParseInt(matches[2], 10, 64)
				if registrationDays <= maxDays {
					return strings.TrimSpace(matches[1]), true
				}
			}
			continue
		}

		if matches := serverFetchUserRangePattern.FindStringSubmatch(host); len(matches) == 4 {
			hasConditional = true
			startID, _ := strconv.ParseInt(matches[2], 10, 64)
			endID, _ := strconv.ParseInt(matches[3], 10, 64)
			if user.ID >= startID && user.ID <= endID {
				return strings.TrimSpace(matches[1]), true
			}
			continue
		}

		defaultHost = host
	}

	if hasConditional && strings.TrimSpace(defaultHost) == "" {
		return "", false
	}
	defaultHost = strings.TrimSpace(defaultHost)
	if defaultHost == "" {
		return "", false
	}
	return defaultHost, true
}

func serverFetchUAHit(rawKeywords, ua string) bool {
	if strings.TrimSpace(ua) == "" {
		return false
	}
	for _, keyword := range strings.Split(rawKeywords, "|") {
		keyword = strings.TrimSpace(keyword)
		keyword = strings.TrimPrefix(strings.TrimPrefix(keyword, "U"), "u")
		if keyword != "" && strings.Contains(strings.ToLower(ua), strings.ToLower(keyword)) {
			return true
		}
	}
	return false
}

func serverGroupAllowed(groupIDs []int64, userGroupID int64) bool {
	return slices.Contains(groupIDs, userGroupID)
}

func serverSortValue(value any) (int64, bool) {
	switch typed := value.(type) {
	case nil:
		return 0, false
	case int64:
		return typed, true
	case int32:
		return int64(typed), true
	case int:
		return int64(typed), true
	case float64:
		return int64(typed), true
	case string:
		next, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		return next, err == nil
	default:
		return 0, false
	}
}

func serverPortRange(value any) (string, bool) {
	raw := strings.TrimSpace(fmt.Sprint(value))
	if strings.Contains(raw, "-") {
		return raw, true
	}
	return "", false
}

func serverPortInt(value any) (int64, bool) {
	switch typed := value.(type) {
	case int64:
		return typed, true
	case int32:
		return int64(typed), true
	case int:
		return int64(typed), true
	case float64:
		return int64(typed), true
	case string:
		next, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		return next, err == nil
	default:
		return 0, false
	}
}

func randomPortInRange(raw string) (int64, bool) {
	parts := strings.SplitN(raw, "-", 2)
	if len(parts) != 2 {
		return 0, false
	}
	start, err := strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64)
	if err != nil {
		return 0, false
	}
	end, err := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
	if err != nil {
		return 0, false
	}
	if end < start {
		start, end = end, start
	}
	if start == end {
		return start, true
	}
	delta := end - start + 1
	n, err := crand.Int(crand.Reader, big.NewInt(delta))
	if err != nil {
		return start, true
	}
	return start + n.Int64(), true
}

func serverFetchServerKey(timestamp int64, length int) string {
	if timestamp <= 0 || length <= 0 {
		return ""
	}
	sum := md5.Sum([]byte(strconv.FormatInt(timestamp, 10)))
	hexValue := hex.EncodeToString(sum[:])
	if length > len(hexValue) {
		length = len(hexValue)
	}
	return base64.StdEncoding.EncodeToString([]byte(hexValue[:length]))
}

func stripPrivateKey(value any) any {
	settings, ok := value.(map[string]any)
	if !ok {
		return value
	}
	delete(settings, "private_key")
	return settings
}

func decodeServerJSONValue(value any) any {
	raw := strings.TrimSpace(fmt.Sprint(value))
	if raw == "" || strings.EqualFold(raw, "null") {
		return nil
	}

	var object map[string]any
	if err := json.Unmarshal([]byte(raw), &object); err == nil {
		return object
	}

	var list []any
	if err := json.Unmarshal([]byte(raw), &list); err == nil {
		return list
	}

	return value
}

func parseServerStringList(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.EqualFold(raw, "null") || raw == "<nil>" {
		return []string{}
	}

	var values []string
	if err := json.Unmarshal([]byte(raw), &values); err == nil {
		return normalizeServerStringList(values)
	}

	var generic []any
	if err := json.Unmarshal([]byte(raw), &generic); err == nil {
		values = make([]string, 0, len(generic))
		for _, item := range generic {
			text := strings.TrimSpace(fmt.Sprint(item))
			if text != "" && text != "<nil>" {
				values = append(values, text)
			}
		}
		return values
	}

	raw = strings.Trim(raw, "[]")
	parts := strings.Split(raw, ",")
	values = make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(strings.Trim(part, `"'`))
		if part != "" && part != "<nil>" {
			values = append(values, part)
		}
	}
	return values
}

func normalizeServerStringList(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			result = append(result, value)
		}
	}
	return result
}

func copyStringAnyMap(value map[string]any) map[string]any {
	cloned := make(map[string]any, len(value))
	for key, item := range value {
		cloned[key] = item
	}
	return cloned
}
