package nodeapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"crypto/md5"
)

var serverRecordJSONKeys = []string{
	"obfs_settings",
	"network_settings",
	"tls_settings",
	"encryption_settings",
	"networkSettings",
	"tlsSettings",
	"dnsSettings",
	"ruleSettings",
	"padding_scheme",
}

func (s *DBService) LookupServer(ctx context.Context, req ServerLookupRequest) (ServerRecord, error) {
	if s == nil || s.db == nil {
		return ServerRecord{}, errors.New("node service unavailable")
	}

	req.NodeType = NormalizeNodeType(req.NodeType)
	if req.NodeID <= 0 || req.NodeType == "" {
		return ServerRecord{}, errors.New("invalid node target")
	}

	table, ok := nodeServerTables[req.NodeType]
	if !ok {
		return ServerRecord{}, errors.New("unsupported node type")
	}

	var raw []byte
	err := s.db.QueryRowContext(ctx, `SELECT row_to_json(t)
FROM (
	SELECT *
	FROM `+table+`
	WHERE id = $1
	LIMIT 1
) AS t`, req.NodeID).Scan(&raw)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ServerRecord{}, errors.New("server is not exist")
		}
		return ServerRecord{}, fmt.Errorf("query node server: %w", err)
	}

	fields, err := decodeJSONObject(raw)
	if err != nil {
		return ServerRecord{}, err
	}
	for _, key := range serverRecordJSONKeys {
		if value, ok := fields[key]; ok {
			fields[key] = decodeEmbeddedJSON(value)
		}
	}

	return ServerRecord{
		ID:       req.NodeID,
		NodeType: req.NodeType,
		GroupIDs: parseInt64List(fields["group_id"]),
		RouteIDs: parseInt64List(fields["route_id"]),
		Fields:   fields,
	}, nil
}

func (s *DBService) TouchLastCheck(ctx context.Context, nodeType string, nodeID int64) error {
	if s == nil || s.db == nil {
		return errors.New("node service unavailable")
	}

	nodeType = NormalizeNodeType(nodeType)
	if nodeID <= 0 || nodeType == "" {
		return errors.New("invalid node target")
	}

	return s.setRuntimeKV(ctx, serverRuntimeKey(nodeType, "LAST_CHECK_AT", nodeID), strconv.FormatInt(time.Now().Unix(), 10), 3600)
}

func (s *DBService) AvailableUsers(ctx context.Context, groupIDs []int64) ([]AvailableUser, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("node service unavailable")
	}
	if len(groupIDs) == 0 {
		return []AvailableUser{}, nil
	}

	inClause, args := buildInt64Placeholders(1, groupIDs)
	args = append(args, time.Now().Unix())
	rows, err := s.db.QueryContext(ctx, `SELECT id, uuid, speed_limit, device_limit
FROM v2_user
WHERE group_id IN (`+inClause+`)
  AND u + d < transfer_enable
  AND (expired_at >= $`+strconv.Itoa(len(args))+` OR expired_at IS NULL OR expired_at <= 0)
  AND banned = 0
ORDER BY id ASC`, args...)
	if err != nil {
		return nil, fmt.Errorf("query node users: %w", err)
	}
	defer rows.Close()

	result := make([]AvailableUser, 0)
	for rows.Next() {
		var (
			item        AvailableUser
			speedLimit  sql.NullInt64
			deviceLimit sql.NullInt64
		)
		if err := rows.Scan(&item.ID, &item.UUID, &speedLimit, &deviceLimit); err != nil {
			return nil, fmt.Errorf("scan node user: %w", err)
		}
		if speedLimit.Valid {
			item.SpeedLimit = &speedLimit.Int64
		}
		if deviceLimit.Valid {
			item.DeviceLimit = &deviceLimit.Int64
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate node users: %w", err)
	}
	return result, nil
}

func (s *DBService) Routes(ctx context.Context, routeIDs []int64) ([]map[string]any, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("node service unavailable")
	}
	if len(routeIDs) == 0 {
		return []map[string]any{}, nil
	}

	inClause, args := buildInt64Placeholders(1, routeIDs)
	rows, err := s.db.QueryContext(ctx, `SELECT id, match, action, action_value
FROM v2_server_route
WHERE id IN (`+inClause+`)
ORDER BY id ASC`, args...)
	if err != nil {
		return nil, fmt.Errorf("query node routes: %w", err)
	}
	defer rows.Close()

	result := make([]map[string]any, 0)
	byID := make(map[int64]map[string]any, len(routeIDs))
	for rows.Next() {
		var (
			id          int64
			matchRaw    string
			action      string
			actionValue sql.NullString
		)
		if err := rows.Scan(&id, &matchRaw, &action, &actionValue); err != nil {
			return nil, fmt.Errorf("scan node route: %w", err)
		}
		record := map[string]any{
			"id":     id,
			"match":  decodeEmbeddedJSON(matchRaw),
			"action": action,
		}
		if actionValue.Valid && strings.TrimSpace(actionValue.String) != "" {
			record["action_value"] = actionValue.String
		}
		byID[id] = record
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate node routes: %w", err)
	}
	for _, id := range routeIDs {
		record, ok := byID[id]
		if !ok {
			continue
		}
		result = append(result, record)
	}
	return result, nil
}

func (s *DBService) AliveList(ctx context.Context) (map[int64]int64, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("node service unavailable")
	}

	rows, err := s.db.QueryContext(ctx, `SELECT id
FROM v2_user
WHERE u + d < transfer_enable
  AND (expired_at >= $1 OR expired_at IS NULL OR expired_at <= 0)
  AND banned = 0
  AND device_limit > 0
ORDER BY id ASC`, time.Now().Unix())
	if err != nil {
		return nil, fmt.Errorf("query alive users: %w", err)
	}
	defer rows.Close()

	result := make(map[int64]int64)
	for rows.Next() {
		var userID int64
		if err := rows.Scan(&userID); err != nil {
			return nil, fmt.Errorf("scan alive user: %w", err)
		}
		state, ok, err := s.loadAliveState(ctx, aliveUserKey(userID))
		if err != nil || !ok {
			if err != nil {
				return nil, err
			}
			continue
		}
		count := mapAnyInt64(state["alive_ip"])
		if count > 0 {
			result[userID] = count
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate alive users: %w", err)
	}
	return result, nil
}

func (s *DBService) ReportAlive(ctx context.Context, req AliveReportRequest) error {
	if s == nil || s.db == nil {
		return errors.New("node service unavailable")
	}

	req.NodeType = NormalizeNodeType(req.NodeType)
	if req.NodeID <= 0 || req.NodeType == "" {
		return errors.New("invalid node target")
	}

	now := time.Now().Unix()
	nodeKey := req.NodeType + strconv.FormatInt(req.NodeID, 10)
	for userID, ips := range req.Users {
		if userID <= 0 {
			continue
		}

		state, _, err := s.loadAliveState(ctx, aliveUserKey(userID))
		if err != nil {
			return err
		}
		state[nodeKey] = map[string]any{
			"aliveips":     append([]string(nil), ips...),
			"lastupdateAt": now,
		}

		count := int64(0)
		ipmap := map[string]struct{}{}
		for key, value := range state {
			if key == "alive_ip" {
				continue
			}
			entry, ok := value.(map[string]any)
			if !ok {
				delete(state, key)
				continue
			}
			lastUpdate := mapAnyInt64(entry["lastupdateAt"])
			if now-lastUpdate > 100 {
				delete(state, key)
				continue
			}
			currentIPs := stringSliceFromAny(entry["aliveips"])
			if s.cfg.DeviceLimitMode == 1 {
				for _, item := range currentIPs {
					ip := strings.TrimSpace(item)
					if index := strings.Index(ip, "_"); index >= 0 {
						ip = ip[:index]
					}
					if ip != "" {
						ipmap[ip] = struct{}{}
					}
				}
				continue
			}
			count += int64(len(currentIPs))
		}
		if s.cfg.DeviceLimitMode == 1 {
			count = int64(len(ipmap))
		}
		state["alive_ip"] = count

		raw, err := json.Marshal(state)
		if err != nil {
			return fmt.Errorf("encode alive state: %w", err)
		}
		if err := s.setRuntimeKV(ctx, aliveUserKey(userID), string(raw), 120); err != nil {
			return err
		}
	}
	return nil
}

func (s *DBService) loadAliveState(ctx context.Context, key string) (map[string]any, bool, error) {
	raw, ok, err := s.kvGet(ctx, key)
	if err != nil || !ok || strings.TrimSpace(raw) == "" {
		return map[string]any{}, ok, err
	}
	state, err := decodeJSONObject([]byte(raw))
	if err != nil {
		return map[string]any{}, false, err
	}
	return state, true, nil
}

func (s *DBService) kvGet(ctx context.Context, key string) (string, bool, error) {
	var (
		value    string
		expireAt int64
	)
	err := s.db.QueryRowContext(ctx, `SELECT v, expire_at FROM v2_runtime_kv WHERE k = $1 LIMIT 1`, key).Scan(&value, &expireAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("get runtime kv: %w", err)
	}
	if expireAt > 0 && expireAt <= time.Now().Unix() {
		return "", false, nil
	}
	return value, true, nil
}

func aliveUserKey(userID int64) string {
	return "ALIVE_IP_USER_" + strconv.FormatInt(userID, 10)
}

func decodeJSONObject(raw []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode json object: %w", err)
	}
	return payload, nil
}

func decodeEmbeddedJSON(value any) any {
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

func parseInt64List(value any) []int64 {
	switch typed := value.(type) {
	case nil:
		return []int64{}
	case []any:
		result := make([]int64, 0, len(typed))
		for _, item := range typed {
			if next := mapAnyInt64(item); next > 0 {
				result = append(result, next)
			}
		}
		return result
	case []string:
		result := make([]int64, 0, len(typed))
		for _, item := range typed {
			if next, err := strconv.ParseInt(strings.TrimSpace(item), 10, 64); err == nil && next > 0 {
				result = append(result, next)
			}
		}
		return result
	case string:
		raw := strings.TrimSpace(typed)
		if raw == "" || strings.EqualFold(raw, "null") {
			return []int64{}
		}
		var generic []any
		if err := json.Unmarshal([]byte(raw), &generic); err == nil {
			return parseInt64List(generic)
		}
		parts := strings.Split(strings.Trim(raw, "[]"), ",")
		result := make([]int64, 0, len(parts))
		for _, part := range parts {
			if next, err := strconv.ParseInt(strings.TrimSpace(strings.Trim(part, `"'`)), 10, 64); err == nil && next > 0 {
				result = append(result, next)
			}
		}
		return result
	default:
		if next := mapAnyInt64(value); next > 0 {
			return []int64{next}
		}
		return []int64{}
	}
}

func stringSliceFromAny(value any) []string {
	switch typed := value.(type) {
	case nil:
		return []string{}
	case []string:
		return append([]string(nil), typed...)
	case []any:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			text := strings.TrimSpace(fmt.Sprint(item))
			if text != "" {
				result = append(result, text)
			}
		}
		return result
	default:
		return []string{}
	}
}

func mapAnyInt64(value any) int64 {
	switch typed := value.(type) {
	case nil:
		return 0
	case int64:
		return typed
	case int:
		return int64(typed)
	case float64:
		return int64(typed)
	case json.Number:
		next, err := typed.Int64()
		if err == nil {
			return next
		}
	case string:
		next, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		if err == nil {
			return next
		}
	}
	return 0
}

func buildInt64Placeholders(start int, values []int64) (string, []any) {
	parts := make([]string, 0, len(values))
	args := make([]any, 0, len(values))
	for index, value := range values {
		parts = append(parts, "$"+strconv.Itoa(start+index))
		args = append(args, value)
	}
	return strings.Join(parts, ", "), args
}

func ServerKey(createdAt int64, length int) string {
	if createdAt <= 0 || length <= 0 {
		return ""
	}
	sum := md5.Sum([]byte(strconv.FormatInt(createdAt, 10)))
	hexValue := hex.EncodeToString(sum[:])
	if length > len(hexValue) {
		length = len(hexValue)
	}
	return base64.StdEncoding.EncodeToString([]byte(hexValue[:length]))
}
