package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

var managedServerTypeTable = map[string]string{
	"shadowsocks": "v2_server_shadowsocks",
	"vmess":       "v2_server_vmess",
	"vless":       "v2_server_vless",
	"trojan":      "v2_server_trojan",
	"tuic":        "v2_server_tuic",
	"hysteria":    "v2_server_hysteria",
	"anytls":      "v2_server_anytls",
	"v2node":      "v2_server_v2node",
}

func (s *DBService) ListManagedServers(ctx context.Context) ([]map[string]any, error) {
	if s.db == nil {
		return nil, ErrUnavailable
	}

	result := make([]map[string]any, 0)
	for serverType, table := range managedServerTypeTable {
		rows, err := s.db.QueryContext(ctx, `SELECT row_to_json(t)
FROM (
	SELECT *
	FROM `+table+`
	ORDER BY sort ASC NULLS LAST, id ASC
) AS t`)
		if err != nil {
			return nil, fmt.Errorf("query managed servers from %s: %w", table, err)
		}

		for rows.Next() {
			var raw []byte
			if err := rows.Scan(&raw); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scan managed server from %s: %w", table, err)
			}
			var item map[string]any
			if err := json.Unmarshal(raw, &item); err != nil {
				rows.Close()
				return nil, fmt.Errorf("decode managed server from %s: %w", table, err)
			}
			item["type"] = serverType
			item["group_id"] = managedServerStringList(item["group_id"])
			item["route_id"] = managedServerStringList(item["route_id"])
			item["tags"] = managedServerStringList(item["tags"])
			targetID := managedServerRuntimeTargetID(item)
			online, err := s.getInt64KV(ctx, managedServerRuntimeKey(serverType, "ONLINE_USER", targetID))
			if err != nil {
				rows.Close()
				return nil, fmt.Errorf("load managed server online runtime from %s: %w", table, err)
			}
			lastCheckAt, err := s.getInt64KV(ctx, managedServerRuntimeKey(serverType, "LAST_CHECK_AT", targetID))
			if err != nil {
				rows.Close()
				return nil, fmt.Errorf("load managed server check runtime from %s: %w", table, err)
			}
			lastPushAt, err := s.getInt64KV(ctx, managedServerRuntimeKey(serverType, "LAST_PUSH_AT", targetID))
			if err != nil {
				rows.Close()
				return nil, fmt.Errorf("load managed server push runtime from %s: %w", table, err)
			}
			mergeManagedServerRuntimeFields(item, online, lastCheckAt, lastPushAt, time.Now().Unix())
			if serverType == "v2node" {
				item["install_command"] = s.v2nodeInstallCommand(item)
			}
			result = append(result, item)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("iterate managed servers from %s: %w", table, err)
		}
		rows.Close()
	}

	sort.SliceStable(result, func(i, j int) bool {
		leftSort, leftOK := managedServerSortValue(result[i]["sort"])
		rightSort, rightOK := managedServerSortValue(result[j]["sort"])
		switch {
		case leftOK && rightOK && leftSort != rightSort:
			return leftSort < rightSort
		case leftOK != rightOK:
			return leftOK
		}
		return managedServerIDValue(result[i]["id"]) < managedServerIDValue(result[j]["id"])
	})

	return result, nil
}

func (s *DBService) SortManagedServers(ctx context.Context, values map[string]map[int64]int64) (bool, error) {
	if s.db == nil {
		return false, ErrUnavailable
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, errors.New("保存失败")
	}
	defer tx.Rollback()

	now := time.Now().Unix()
	for serverType, items := range values {
		table, ok := managedServerTypeTable[strings.TrimSpace(serverType)]
		if !ok {
			continue
		}
		for id, position := range items {
			result, err := tx.ExecContext(ctx, `UPDATE `+table+` SET sort = $2, updated_at = $3 WHERE id = $1`, id, position, now)
			if err != nil {
				return false, errors.New("保存失败")
			}
			affected, err := result.RowsAffected()
			if err != nil || affected == 0 {
				return false, errors.New("保存失败")
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return false, errors.New("保存失败")
	}
	return true, nil
}

func (s *DBService) UpdateManagedServerHost(ctx context.Context, oldHost, newHost string) (ManagedServerHostUpdateResult, error) {
	if s.db == nil {
		return ManagedServerHostUpdateResult{}, ErrUnavailable
	}

	oldHost = strings.TrimSpace(oldHost)
	newHost = strings.TrimSpace(newHost)
	if oldHost == "" || newHost == "" {
		return ManagedServerHostUpdateResult{}, errors.New("地址不能为空")
	}
	if oldHost == newHost {
		return ManagedServerHostUpdateResult{}, errors.New("原地址和新地址不能相同")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ManagedServerHostUpdateResult{}, errors.New("批量修改地址失败")
	}
	defer tx.Rollback()

	result := ManagedServerHostUpdateResult{
		UpdatedByTable: make(map[string]int64),
	}
	for _, table := range []string{
		"v2_server_v2node",
		"v2_server_anytls",
		"v2_server_hysteria",
		"v2_server_shadowsocks",
		"v2_server_trojan",
		"v2_server_tuic",
		"v2_server_vless",
		"v2_server_vmess",
	} {
		execResult, err := tx.ExecContext(ctx, `UPDATE `+table+` SET host = $2 WHERE host = $1`, oldHost, newHost)
		if err != nil {
			return ManagedServerHostUpdateResult{}, errors.New("批量修改地址失败")
		}
		affected, err := execResult.RowsAffected()
		if err != nil {
			return ManagedServerHostUpdateResult{}, errors.New("批量修改地址失败")
		}
		if affected > 0 {
			result.UpdatedByTable[table] = affected
			result.UpdatedTotal += affected
		}
	}

	if err := tx.Commit(); err != nil {
		return ManagedServerHostUpdateResult{}, errors.New("批量修改地址失败")
	}
	return result, nil
}

func managedServerStringList(value any) []string {
	switch typed := value.(type) {
	case nil:
		return []string{}
	case []string:
		return normalizeStringSlice(typed)
	case []any:
		return normalizeStringSlice(stringsFromAny(typed))
	case string:
		return parseServerStringList(typed)
	default:
		return parseServerStringList(fmt.Sprint(value))
	}
}

func managedServerSortValue(value any) (int64, bool) {
	return managedServerInt64Value(value)
}

func managedServerIDValue(value any) int64 {
	id, _ := managedServerInt64Value(value)
	return id
}

func managedServerInt64Value(value any) (int64, bool) {
	switch typed := value.(type) {
	case nil:
		return 0, false
	case float64:
		return int64(typed), true
	case int64:
		return typed, true
	case int:
		return int64(typed), true
	case json.Number:
		next, err := typed.Int64()
		return next, err == nil
	case string:
		next, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		return next, err == nil
	default:
		next, err := strconv.ParseInt(strings.TrimSpace(fmt.Sprint(value)), 10, 64)
		return next, err == nil
	}
}

func (s *DBService) v2nodeInstallCommand(item map[string]any) string {
	cfg, err := loadAdminConfigStore(adminConfigPath())
	apiHost := strings.TrimSpace(s.currentConfig().AppURL)
	apiKey := ""
	scriptURL := ""
	commandTemplate := ""
	if err == nil {
		if value, ok := cfg.nullableStringValue("server_api_url").(string); ok {
			apiHost = strings.TrimSpace(value)
		}
		if value, ok := cfg.nullableStringValue("server_token").(string); ok {
			apiKey = strings.TrimSpace(value)
		}
		if value, ok := cfg.nullableStringValue("server_node_install_script_url").(string); ok {
			scriptURL = strings.TrimSpace(value)
		}
		if value, ok := cfg.nullableStringValue("server_node_install_command_template").(string); ok {
			commandTemplate = strings.TrimSpace(value)
		}
	}

	nodeID := managedServerIDValue(item["id"])
	return renderNodeInstallCommand(commandTemplate, scriptURL, apiHost, nodeID, apiKey)
}

func renderNodeInstallCommand(commandTemplate, scriptURL, apiHost string, nodeID int64, apiKey string) string {
	if commandTemplate != "" {
		replacer := strings.NewReplacer(
			"{{script_url}}", scriptURL,
			"{{api_host}}", apiHost,
			"{{node_id}}", strconv.FormatInt(nodeID, 10),
			"{{api_key}}", apiKey,
		)
		return replacer.Replace(commandTemplate)
	}

	if scriptURL == "" {
		scriptURL = "https://your-node-installer.example/install.sh"
	}

	return fmt.Sprintf(
		"NODE_INSTALL_URL=%q && if command -v curl >/dev/null 2>&1; then curl -fsSL \"$NODE_INSTALL_URL\" | bash -s -- --api-host %s --node-id %d --api-key %s; else wget -qO- \"$NODE_INSTALL_URL\" | bash -s -- --api-host %s --node-id %d --api-key %s; fi",
		scriptURL,
		apiHost,
		nodeID,
		apiKey,
		apiHost,
		nodeID,
		apiKey,
	)
}
