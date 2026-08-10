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
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"forest/go-api/internal/cliententry"
	"forest/go-api/internal/subscribelink"
)

type serverFetchUser struct {
	ID             int64
	Email          string
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
	userPolicies, err := s.loadClientEntryUserPolicies(ctx, userID)
	if err != nil {
		return nil, err
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

	servers = applyClientEntryUserPoliciesWithResolver(ctx, servers, cliententry.Subject{
		UserID:           userRow.ID,
		Email:            userRow.Email,
		RegistrationDays: serverFetchRegistrationDays(userRow.CreatedAt, now),
		PlanID:           userRow.PlanID,
		UA:               ua,
	}, userPolicies, s.clientEntryHostResolver)

	filtered := make([]map[string]any, 0, len(servers))
	for _, item := range servers {
		if rawPort, isRange := serverPortRange(item["port"]); isRange {
			item["mport"] = rawPort
		} else if port, ok := serverPortInt(item["port"]); ok {
			item["port"] = port
		}

		if subscribelink.IsExtra(item) {
			item["is_online"] = int64(1)
		} else {
			lastCheckAt := mapInt64(item["last_check_at"])
			isOnline := int64(0)
			if now-300 <= lastCheckAt {
				isOnline = 1
			}
			item["is_online"] = isOnline
			item["cache_key"] = fmt.Sprintf("%s-%d-%v-%d", item["type"], mapInt64(item["id"]), item["updated_at"], isOnline)
		}
		filtered = append(filtered, item)
	}

	s.recordClientEntrySubscribeActivity(ctx, userID, now)
	return filtered, nil
}

func serverFetchRegistrationDays(createdAt, now int64) int64 {
	if createdAt <= 0 || now < createdAt {
		return -1
	}
	return (now - createdAt) / 86400
}

func (s *DBService) loadServerFetchUser(ctx context.Context, userID int64) (serverFetchUser, error) {
	var row serverFetchUser
	var (
		groupID sql.NullInt64
		planID  sql.NullInt64
	)
	err := s.db.QueryRowContext(ctx, `SELECT id, email, group_id, plan_id, transfer_enable, banned, created_at, expired_at
FROM v2_user
WHERE id = $1
LIMIT 1`, userID).Scan(
		&row.ID,
		&row.Email,
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
	delete(item, "ddns_settings")
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
