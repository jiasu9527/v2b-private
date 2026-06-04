package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
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
			normalizeManagedServerObjectFields(item)
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

func normalizeManagedServerObjectFields(item map[string]any) {
	for _, key := range []string{
		"network_settings",
		"tls_settings",
		"encryption_settings",
		"ddns_settings",
		"networkSettings",
		"tlsSettings",
		"ruleSettings",
		"dnsSettings",
	} {
		normalizeManagedServerObjectField(item, key)
	}
}

func normalizeManagedServerObjectField(item map[string]any, key string) {
	value, ok := item[key]
	if !ok || value == nil {
		return
	}
	switch typed := value.(type) {
	case map[string]any:
		item[key] = cloneMap(typed)
	case string:
		raw := strings.TrimSpace(typed)
		if raw == "" || raw == "null" {
			item[key] = nil
			return
		}
		var decoded map[string]any
		if err := json.Unmarshal([]byte(raw), &decoded); err == nil {
			item[key] = decoded
		}
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
	defaults := v2nodeDDNSDefaults{}
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
		defaults = v2nodeDDNSDefaultsFromConfig(cfg)
	}

	nodeID := managedServerIDValue(item["id"])
	ddnsArgs := renderV2nodeDDNSArgs(item, defaults)
	return renderNodeInstallCommand(commandTemplate, scriptURL, apiHost, nodeID, apiKey, ddnsArgs)
}

type v2nodeDDNSDefaults struct {
	CFToken        string
	CFZoneID       string
	RecordType     string
	TTL            string
	Proxied        string
	Interval       string
	BlockURL       string
	BlockKeyword   string
	BlockTimeout   string
	BlockThreshold string
	ChangeWait     string
	ChangeCooldown string
}

func v2nodeDDNSDefaultsFromConfig(cfg *phpConfigFile) v2nodeDDNSDefaults {
	return v2nodeDDNSDefaults{
		CFToken:        cfg.stringValue("server_cf_api_token", ""),
		CFZoneID:       cfg.stringValue("server_cf_zone_id", ""),
		RecordType:     cfg.stringValue("server_cf_record_type", "A"),
		TTL:            strconv.FormatInt(cfg.int64Value("server_cf_ttl", 1), 10),
		Proxied:        strconv.FormatInt(cfg.int64Value("server_cf_proxied", 0), 10),
		Interval:       strconv.FormatInt(cfg.int64Value("server_ddns_interval", 1), 10),
		BlockURL:       cfg.stringValue("server_block_check_url", "https://www.baidu.com/"),
		BlockKeyword:   cfg.stringValue("server_block_check_keyword", ""),
		BlockTimeout:   strconv.FormatInt(cfg.int64Value("server_block_check_timeout", 10), 10),
		BlockThreshold: strconv.FormatInt(cfg.int64Value("server_block_check_threshold", 3), 10),
		ChangeWait:     strconv.FormatInt(cfg.int64Value("server_change_ip_wait", 60), 10),
		ChangeCooldown: strconv.FormatInt(cfg.int64Value("server_change_ip_cooldown", 1800), 10),
	}
}

func renderV2nodeDDNSArgs(item map[string]any, defaults v2nodeDDNSDefaults) string {
	settings := v2nodeDDNSSettings(item["ddns_settings"])
	ddnsEnabled := truthySetting(settings["ddns_enabled"]) || truthySetting(settings["enabled"])
	blockEnabled := truthySetting(settings["block_check_enabled"])
	if !ddnsEnabled && !blockEnabled {
		return ""
	}

	cfToken := settingString(settings, "cf_token", defaults.CFToken)
	cfZoneID := settingString(settings, "cf_zone_id", defaults.CFZoneID)
	record := settingString(settings, "cf_record", strings.TrimSpace(fmt.Sprint(item["host"])))
	if ddnsEnabled && (cfToken == "" || record == "") {
		return ""
	}

	args := []string{"--ddns-interval", settingString(settings, "ddns_interval", defaults.Interval)}
	if ddnsEnabled {
		args = append(args,
			"--enable-ddns",
			"--cf-token", cfToken,
			"--cf-record", record,
			"--cf-record-type", strings.ToUpper(settingString(settings, "cf_record_type", defaults.RecordType)),
			"--cf-ttl", settingString(settings, "cf_ttl", defaults.TTL),
			"--cf-proxied", normalizeInstallBool(settingString(settings, "cf_proxied", defaults.Proxied)),
		)
		if cfZoneID != "" {
			args = append(args, "--cf-zone-id", cfZoneID)
		}
	}
	if blockEnabled {
		args = append(args, "--enable-block-check")
	}

	if blockEnabled {
		if value := settingString(settings, "block_check_url", defaults.BlockURL); value != "" {
			args = append(args, "--block-check-url", value)
		}
		if value := settingString(settings, "block_check_keyword", defaults.BlockKeyword); value != "" {
			args = append(args, "--block-check-keyword", value)
		}
		if value := settingString(settings, "block_check_timeout", defaults.BlockTimeout); value != "" {
			args = append(args, "--block-check-timeout", value)
		}
		if value := settingString(settings, "block_check_threshold", defaults.BlockThreshold); value != "" {
			args = append(args, "--block-check-threshold", value)
		}
		if value := settingString(settings, "change_ip_curl", ""); value != "" {
			args = append(args, "--change-ip-curl", value)
		}
		if value := settingString(settings, "change_ip_wait", defaults.ChangeWait); value != "" {
			args = append(args, "--change-ip-wait", value)
		}
		if value := settingString(settings, "change_ip_cooldown", defaults.ChangeCooldown); value != "" {
			args = append(args, "--change-ip-cooldown", value)
		}
	}

	parts := make([]string, 0, len(args))
	for _, arg := range args {
		parts = append(parts, shellQuote(arg))
	}
	return strings.Join(parts, " ")
}

func v2nodeDDNSSettings(value any) map[string]any {
	switch typed := value.(type) {
	case nil:
		return map[string]any{}
	case map[string]any:
		return cloneMap(typed)
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" || strings.EqualFold(trimmed, "null") {
			return map[string]any{}
		}
		var next map[string]any
		if err := json.Unmarshal([]byte(trimmed), &next); err == nil {
			return next
		}
	}
	return map[string]any{}
}

func settingString(settings map[string]any, key, fallback string) string {
	if value, ok := settings[key]; ok {
		trimmed := strings.TrimSpace(fmt.Sprint(value))
		if trimmed != "" {
			return trimmed
		}
	}
	return strings.TrimSpace(fallback)
}

func truthySetting(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case int:
		return typed != 0
	case int64:
		return typed != 0
	case float64:
		return typed != 0
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "1", "true", "yes", "y", "on", "启用", "开启":
			return true
		}
	}
	return false
}

func normalizeInstallBool(value string) string {
	if truthySetting(value) {
		return "true"
	}
	return "false"
}

func renderNodeInstallCommand(commandTemplate, scriptURL, apiHost string, nodeID int64, apiKey, extraArgs string) string {
	if commandTemplate != "" {
		replacer := strings.NewReplacer(
			"{{script_url}}", scriptURL,
			"{{api_host}}", apiHost,
			"{{node_id}}", strconv.FormatInt(nodeID, 10),
			"{{api_key}}", apiKey,
			"{{ddns_args}}", extraArgs,
		)
		return strings.TrimSpace(replacer.Replace(commandTemplate))
	}

	if scriptURL == "" {
		scriptURL = "https://raw.githubusercontent.com/jiasu9527/v2node/main/script/install.sh"
	}

	command := fmt.Sprintf(
		"wget -N %s && bash install.sh --api-host %s --node-id %d --api-key %s",
		shellQuote(scriptURL),
		shellQuote(apiHost),
		nodeID,
		shellQuote(apiKey),
	)
	if strings.TrimSpace(extraArgs) != "" {
		command += " " + strings.TrimSpace(extraArgs)
	}
	return command
}

var shellSafeArgPattern = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

func shellQuote(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "''"
	}
	if shellSafeArgPattern.MatchString(value) {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
