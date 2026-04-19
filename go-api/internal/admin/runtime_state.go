package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

func managedServerRuntimeKey(serverType, suffix string, serverID int64) string {
	return "SERVER_" + strings.ToUpper(strings.TrimSpace(serverType)) + "_" + suffix + "_" + strconv.FormatInt(serverID, 10)
}

func managedServerRuntimeTargetID(item map[string]any) int64 {
	if parentID := mapAnyInt64(item["parent_id"]); parentID > 0 {
		return parentID
	}
	return mapAnyInt64(item["id"])
}

func mergeManagedServerRuntimeFields(item map[string]any, online, lastCheckAt, lastPushAt *int64, now int64) {
	onlineValue := int64(0)
	lastCheckValue := int64(0)
	lastPushValue := int64(0)
	if online != nil {
		onlineValue = *online
	}
	if lastCheckAt != nil {
		lastCheckValue = *lastCheckAt
	}
	if lastPushAt != nil {
		lastPushValue = *lastPushAt
	}

	item["online"] = onlineValue
	item["last_check_at"] = lastCheckValue
	item["last_push_at"] = lastPushValue

	switch {
	case now-300 >= lastCheckValue:
		item["available_status"] = int64(0)
	case now-300 >= lastPushValue:
		item["available_status"] = int64(1)
	default:
		item["available_status"] = int64(2)
	}
}

func adminAliveIPSummary(raw string) (int64, string) {
	return adminAliveIPSummaryWithNodeNames(raw, nil)
}

func adminAliveIPSummaryWithNodeNames(raw string, nodeNames map[string]string) (int64, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, ""
	}

	var state map[string]any
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		return 0, ""
	}

	count := mapAnyInt64(state["alive_ip"])
	ips := make([]string, 0)
	seen := make(map[string]struct{})
	for nodeTypeID, payload := range state {
		if nodeTypeID == "alive_ip" {
			continue
		}
		entry, ok := payload.(map[string]any)
		if !ok {
			continue
		}
		for _, item := range runtimeAliveIPList(entry["aliveips"]) {
			ip := strings.TrimSpace(item)
			if ip == "" {
				continue
			}
			if index := strings.Index(ip, "_"); index >= 0 {
				ip = ip[:index]
			}
			if ip != "" {
				label := adminAliveIPNodeLabel(nodeTypeID, nodeNames)
				value := ip
				if label != "" {
					value = fmt.Sprintf("%s | %s", ip, label)
				}
				if _, ok := seen[value]; ok {
					continue
				}
				seen[value] = struct{}{}
				ips = append(ips, value)
			}
		}
	}
	sort.Strings(ips)
	return count, strings.Join(ips, ", ")
}

func adminAliveIPNodeLabel(nodeTypeID string, nodeNames map[string]string) string {
	nodeTypeID = strings.ToLower(strings.TrimSpace(nodeTypeID))
	if nodeTypeID == "" {
		return ""
	}
	if name := strings.TrimSpace(nodeNames[nodeTypeID]); name != "" {
		return name
	}
	if nodeType, nodeID, ok := parseManagedServerNodeKey(nodeTypeID); ok {
		normalizedKey := nodeType + strconv.FormatInt(nodeID, 10)
		if name := strings.TrimSpace(nodeNames[normalizedKey]); name != "" {
			return name
		}
		return normalizedKey
	}
	return nodeTypeID
}

func parseManagedServerNodeKey(nodeTypeID string) (string, int64, bool) {
	nodeTypeID = strings.ToLower(strings.TrimSpace(nodeTypeID))
	if nodeTypeID == "" {
		return "", 0, false
	}

	index := len(nodeTypeID)
	for index > 0 {
		ch := nodeTypeID[index-1]
		if ch < '0' || ch > '9' {
			break
		}
		index--
	}
	if index <= 0 || index >= len(nodeTypeID) {
		return "", 0, false
	}

	nodeType := normalizeManagedServerType(nodeTypeID[:index])
	if _, ok := managedServerDefinitions[nodeType]; !ok {
		return "", 0, false
	}

	nodeID, err := strconv.ParseInt(nodeTypeID[index:], 10, 64)
	if err != nil || nodeID <= 0 {
		return "", 0, false
	}

	return nodeType, nodeID, true
}

func normalizeManagedServerType(serverType string) string {
	switch strings.ToLower(strings.TrimSpace(serverType)) {
	case "v2ray":
		return "vmess"
	case "hysteria2":
		return "hysteria"
	default:
		return strings.ToLower(strings.TrimSpace(serverType))
	}
}

func (s *DBService) loadManagedServerNameMap(ctx context.Context) (map[string]string, error) {
	result := make(map[string]string)
	serverTypes := make([]string, 0, len(managedServerDefinitions))
	for serverType := range managedServerDefinitions {
		serverTypes = append(serverTypes, serverType)
	}
	sort.Strings(serverTypes)

	for _, serverType := range serverTypes {
		def := managedServerDefinitions[serverType]
		rows, err := s.db.QueryContext(ctx, `SELECT id, name FROM `+quoteIdentifier(def.table))
		if err != nil {
			return nil, fmt.Errorf("query managed server names for %s: %w", serverType, err)
		}

		for rows.Next() {
			var (
				id   int64
				name sql.NullString
			)
			if err := rows.Scan(&id, &name); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scan managed server name for %s: %w", serverType, err)
			}
			if id > 0 && strings.TrimSpace(name.String) != "" {
				result[serverType+strconv.FormatInt(id, 10)] = strings.TrimSpace(name.String)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("iterate managed server names for %s: %w", serverType, err)
		}
		rows.Close()
	}

	return result, nil
}

func runtimeAliveIPList(value any) []string {
	switch typed := value.(type) {
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
		return nil
	}
}
