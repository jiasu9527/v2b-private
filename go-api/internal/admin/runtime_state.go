package admin

import (
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
				ips = append(ips, fmt.Sprintf("%s_%s", ip, nodeTypeID))
			}
		}
	}
	sort.Strings(ips)
	return count, strings.Join(ips, ", ")
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
