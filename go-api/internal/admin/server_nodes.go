package admin

import (
	"context"
	"crypto/ecdh"
	"crypto/md5"
	crand "crypto/rand"
	"crypto/sha1"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"forest/go-api/internal/cliententry"

	"golang.org/x/crypto/cryptobyte"
	"golang.org/x/crypto/curve25519"
)

type managedServerDefinition struct {
	table   string
	columns []string
}

type managedServerCommonOptions struct {
	portAsInt     bool
	defaultListen string
}

var managedServerDefinitions = map[string]managedServerDefinition{
	"vmess": {
		table: "v2_server_vmess",
		columns: []string{
			"group_id", "route_id", "name", "parent_id", "host", "port", "server_port",
			"tls", "tags", "rate", "network", "rules", "networkSettings", "tlsSettings",
			"ruleSettings", "dnsSettings", "show", "client_entry_only", "sort", "created_at", "updated_at",
		},
	},
	"trojan": {
		table: "v2_server_trojan",
		columns: []string{
			"group_id", "route_id", "parent_id", "tags", "name", "rate", "host", "port",
			"server_port", "network", "network_settings", "allow_insecure", "server_name",
			"show", "client_entry_only", "sort", "created_at", "updated_at",
		},
	},
	"shadowsocks": {
		table: "v2_server_shadowsocks",
		columns: []string{
			"group_id", "route_id", "parent_id", "tags", "name", "rate", "host", "port",
			"server_port", "cipher", "obfs", "obfs_settings", "show", "client_entry_only", "sort", "created_at",
			"updated_at",
		},
	},
	"tuic": {
		table: "v2_server_tuic",
		columns: []string{
			"group_id", "route_id", "name", "parent_id", "host", "port", "server_port",
			"tags", "rate", "show", "client_entry_only", "sort", "server_name", "insecure", "disable_sni",
			"udp_relay_mode", "zero_rtt_handshake", "congestion_control", "created_at",
			"updated_at",
		},
	},
	"hysteria": {
		table: "v2_server_hysteria",
		columns: []string{
			"version", "group_id", "route_id", "name", "parent_id", "host", "port",
			"server_port", "tags", "rate", "show", "client_entry_only", "sort", "up_mbps", "down_mbps",
			"obfs", "obfs_password", "server_name", "insecure", "created_at", "updated_at",
		},
	},
	"vless": {
		table: "v2_server_vless",
		columns: []string{
			"group_id", "route_id", "name", "parent_id", "host", "port", "server_port",
			"tls", "tls_settings", "flow", "network", "network_settings", "encryption",
			"encryption_settings", "tags", "rate", "show", "client_entry_only", "sort", "created_at", "updated_at",
		},
	},
	"anytls": {
		table: "v2_server_anytls",
		columns: []string{
			"group_id", "route_id", "name", "parent_id", "host", "port", "server_port",
			"tags", "rate", "show", "client_entry_only", "sort", "server_name", "insecure", "padding_scheme",
			"created_at", "updated_at",
		},
	},
	"v2node": {
		table: "v2_server_v2node",
		columns: []string{
			"group_id", "route_id", "name", "parent_id", "host", "listen_ip", "send_through", "port",
			"server_port", "tags", "rate", "show", "client_entry_only", "sort", "protocol", "tls",
			"tls_settings", "flow", "network", "network_settings", "encryption",
			"encryption_settings", "disable_sni", "udp_relay_mode", "zero_rtt_handshake",
			"congestion_control", "cipher", "up_mbps", "down_mbps", "obfs", "obfs_password",
			"padding_scheme", "ddns_settings", "created_at", "updated_at",
		},
	},
}

func (s *DBService) SaveManagedServer(ctx context.Context, serverType string, payload map[string]any) (bool, error) {
	if s.db == nil {
		return false, ErrUnavailable
	}
	if err := s.ensureClientEntrySchema(ctx); err != nil {
		return false, err
	}

	def, ok := managedServerDefinitions[strings.TrimSpace(serverType)]
	if !ok {
		return false, errors.New("保存失败")
	}

	id, values, err := normalizeManagedServerSavePayload(serverType, payload)
	if err != nil {
		return false, err
	}

	now := time.Now().Unix()
	if id == nil {
		values["created_at"] = now
		values["updated_at"] = now
		if err := s.insertManagedServer(ctx, def, values); err != nil {
			return false, errors.New("创建失败")
		}
		s.markClientEntryMonitorTargetsDirty()
		return true, nil
	}

	exists, err := s.managedServerExists(ctx, def.table, *id)
	if err != nil {
		return false, err
	}
	if !exists {
		return false, errors.New("服务器不存在")
	}

	values["updated_at"] = now
	if err := s.updateManagedServerRecord(ctx, def, *id, values); err != nil {
		return false, errors.New("保存失败")
	}
	s.markClientEntryMonitorTargetsDirty()
	return true, nil
}

func (s *DBService) DeleteManagedServer(ctx context.Context, serverType string, id int64) (bool, error) {
	if s.db == nil {
		return false, ErrUnavailable
	}
	if err := s.ensureClientEntrySchema(ctx); err != nil {
		return false, err
	}

	def, ok := managedServerDefinitions[strings.TrimSpace(serverType)]
	if !ok {
		return false, errors.New("删除失败")
	}

	exists, err := s.managedServerExists(ctx, def.table, id)
	if err != nil {
		return false, err
	}
	if !exists {
		return false, errors.New("节点ID不存在")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, errors.New("删除失败")
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM v2_client_entry_user_policy_member WHERE server_type = $1 AND server_id = $2`, strings.TrimSpace(serverType), id); err != nil {
		return false, errors.New("删除失败")
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM v2_client_entry_group_member WHERE server_type = $1 AND server_id = $2`, strings.TrimSpace(serverType), id); err != nil {
		return false, errors.New("删除失败")
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM `+quoteIdentifier(def.table)+` WHERE id = $1`, id)
	if err != nil {
		return false, errors.New("删除失败")
	}
	affected, err := result.RowsAffected()
	if err != nil || affected == 0 {
		return false, errors.New("删除失败")
	}
	if err := tx.Commit(); err != nil {
		return false, errors.New("删除失败")
	}
	s.markClientEntryMonitorTargetsDirty()
	return true, nil
}

func (s *DBService) UpdateManagedServer(ctx context.Context, serverType string, id int64, values map[string]any) (bool, error) {
	if s.db == nil {
		return false, ErrUnavailable
	}
	if err := s.ensureClientEntrySchema(ctx); err != nil {
		return false, err
	}

	def, ok := managedServerDefinitions[strings.TrimSpace(serverType)]
	if !ok {
		return false, errors.New("保存失败")
	}

	exists, err := s.managedServerExists(ctx, def.table, id)
	if err != nil {
		return false, err
	}
	if !exists {
		return false, errors.New("该服务器不存在")
	}

	normalized, err := normalizeManagedServerUpdatePayload(values)
	if err != nil {
		return false, err
	}

	now := time.Now().Unix()
	if entryGroupID, present := normalized["entry_group_id"]; present {
		if err := s.replaceManagedServerEntryGroup(ctx, strings.TrimSpace(serverType), id, entryGroupID, now); err != nil {
			return false, errors.New("保存失败")
		}
		delete(normalized, "entry_group_id")
	}
	if len(normalized) > 0 {
		normalized["updated_at"] = now
		if err := s.updateManagedServerRecord(ctx, def, id, normalized); err != nil {
			return false, errors.New("保存失败")
		}
	}
	s.markClientEntryMonitorTargetsDirty()
	return true, nil
}

func (s *DBService) CopyManagedServer(ctx context.Context, serverType string, id int64) (bool, error) {
	if s.db == nil {
		return false, ErrUnavailable
	}
	if err := s.ensureClientEntrySchema(ctx); err != nil {
		return false, err
	}

	def, ok := managedServerDefinitions[strings.TrimSpace(serverType)]
	if !ok {
		return false, errors.New("复制失败")
	}

	exists, err := s.managedServerExists(ctx, def.table, id)
	if err != nil {
		return false, err
	}
	if !exists {
		return false, errors.New("服务器不存在")
	}

	insertColumns := joinQuotedIdentifiers(def.columns)
	selectExprs := make([]string, 0, len(def.columns))
	for _, column := range def.columns {
		switch column {
		case "show":
			selectExprs = append(selectExprs, "0")
		case "created_at", "updated_at":
			selectExprs = append(selectExprs, "$2")
		default:
			selectExprs = append(selectExprs, quoteIdentifier(column))
		}
	}

	now := time.Now().Unix()
	result, err := s.db.ExecContext(
		ctx,
		`INSERT INTO `+quoteIdentifier(def.table)+` (`+insertColumns+`)
SELECT `+strings.Join(selectExprs, ", ")+`
FROM `+quoteIdentifier(def.table)+`
WHERE id = $1`,
		id,
		now,
	)
	if err != nil {
		return false, errors.New("复制失败")
	}
	affected, err := result.RowsAffected()
	if err != nil || affected == 0 {
		return false, errors.New("复制失败")
	}
	return true, nil
}

func normalizeManagedServerSavePayload(serverType string, payload map[string]any) (*int64, map[string]any, error) {
	id, err := managedServerPayloadID(payload)
	if err != nil {
		return nil, nil, errors.New("保存失败")
	}

	values := make(map[string]any)
	seed, err := normalizeManagedServerCommon(payload, values, id != nil, managedServerCommonOptions{
		portAsInt:     serverType == "vless",
		defaultListen: map[bool]string{true: "0.0.0.0", false: ""}[serverType == "v2node"],
	})
	if err != nil {
		return nil, nil, err
	}

	switch serverType {
	case "vmess":
		if err := normalizeVmessServer(values, payload, id != nil); err != nil {
			return nil, nil, err
		}
	case "trojan":
		if err := normalizeTrojanServer(values, payload, id != nil); err != nil {
			return nil, nil, err
		}
	case "shadowsocks":
		if err := normalizeShadowsocksServer(values, payload); err != nil {
			return nil, nil, err
		}
	case "tuic":
		if err := normalizeTuicServer(values, payload); err != nil {
			return nil, nil, err
		}
	case "hysteria":
		if err := normalizeHysteriaServer(values, payload, seed, id != nil); err != nil {
			return nil, nil, err
		}
	case "vless":
		if err := normalizeVlessServer(values, payload); err != nil {
			return nil, nil, err
		}
	case "anytls":
		if err := normalizeAnyTLSServer(values, payload); err != nil {
			return nil, nil, err
		}
	case "v2node":
		if err := normalizeV2nodeServer(values, payload, seed, id != nil); err != nil {
			return nil, nil, err
		}
	default:
		return nil, nil, errors.New("保存失败")
	}

	return id, values, nil
}

func normalizeManagedServerUpdatePayload(payload map[string]any) (map[string]any, error) {
	values := make(map[string]any)
	show, present, err := optionalAllowedIntField(payload, "show", []int64{0, 1}, "显示状态格式不正确")
	if err != nil {
		return nil, err
	}
	if present {
		values["show"] = show
	}
	if entryOnly, present, err := optionalAllowedIntField(payload, "client_entry_only", []int64{0, 1}, "入口分配可见性格式不正确"); err != nil {
		return nil, err
	} else if present {
		values["client_entry_only"] = entryOnly
	}
	if entryGroupID, present, err := optionalManagedServerEntryGroupField(payload, "entry_group_id", "入口组格式不正确"); err != nil {
		return nil, err
	} else if present {
		values["entry_group_id"] = entryGroupID
	}
	if len(values) == 0 {
		return nil, errors.New("保存失败")
	}
	return values, nil
}

func optionalManagedServerEntryGroupField(payload map[string]any, key, errMsg string) (any, bool, error) {
	value, present := payload[key]
	if !present {
		return nil, false, nil
	}
	if value == nil {
		return nil, true, nil
	}
	switch typed := value.(type) {
	case json.Number:
		next, err := typed.Int64()
		if err != nil || next <= 0 {
			return nil, false, errors.New(errMsg)
		}
		return next, true, nil
	case float64:
		next := int64(typed)
		if next <= 0 {
			return nil, false, errors.New(errMsg)
		}
		return next, true, nil
	case int64:
		if typed <= 0 {
			return nil, false, errors.New(errMsg)
		}
		return typed, true, nil
	case int:
		if typed <= 0 {
			return nil, false, errors.New(errMsg)
		}
		return int64(typed), true, nil
	case string:
		raw := strings.TrimSpace(typed)
		if raw == "" || raw == "null" {
			return nil, true, nil
		}
		next, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || next <= 0 {
			return nil, false, errors.New(errMsg)
		}
		return next, true, nil
	default:
		raw := strings.TrimSpace(fmt.Sprint(value))
		if raw == "" || raw == "null" {
			return nil, true, nil
		}
		next, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || next <= 0 {
			return nil, false, errors.New(errMsg)
		}
		return next, true, nil
	}
}

func (s *DBService) replaceManagedServerEntryGroup(ctx context.Context, serverType string, serverID int64, entryGroupID any, now int64) error {
	if err := s.ensureClientEntrySchema(ctx); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM v2_client_entry_group_member WHERE server_type = $1 AND server_id = $2`, serverType, serverID); err != nil {
		return err
	}

	if entryGroupID != nil {
		groupID, ok := entryGroupID.(int64)
		if !ok || groupID <= 0 {
			return errors.New("invalid entry group id")
		}
		var exists int64
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM v2_client_entry_group WHERE id = $1 LIMIT 1`, groupID).Scan(&exists); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO v2_client_entry_group_member (entry_group_id, server_type, server_id, sort, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6)`, groupID, serverType, serverID, nil, now, now); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func normalizeManagedServerCommon(payload map[string]any, values map[string]any, hasID bool, options managedServerCommonOptions) (string, error) {
	groupID, err := requiredIntArrayJSON(payload, "group_id", "权限组不能为空", "权限组格式不正确")
	if err != nil {
		return "", err
	}
	values["group_id"] = groupID

	if routeID, present, err := optionalIntArrayJSON(payload, "route_id", "路由组格式不正确"); err != nil {
		return "", err
	} else if present {
		values["route_id"] = routeID
	}

	name, err := requiredStringField(payload, "name", "节点名称不能为空")
	if err != nil {
		return "", err
	}
	values["name"] = name

	if parentID, present, err := optionalIntField(payload, "parent_id", "父节点格式不正确"); err != nil {
		return "", err
	} else if present {
		values["parent_id"] = parentID
	}

	host, err := requiredStringField(payload, "host", "节点地址不能为空")
	if err != nil {
		return "", err
	}
	host, err = cliententry.NormalizeHost(host)
	if err != nil {
		return "", errors.New("节点地址必须是单个域名或 IP；入口规则请在用户入口分配中维护")
	}
	values["host"] = host

	if options.defaultListen != "" {
		if listenIP, present := optionalNullableStringField(payload, "listen_ip"); present {
			values["listen_ip"] = listenIP
		} else if !hasID {
			values["listen_ip"] = options.defaultListen
		}
	}

	if options.portAsInt {
		port, err := requiredIntField(payload, "port", "连接端口不能为空", "连接端口格式不正确")
		if err != nil {
			return "", err
		}
		values["port"] = port
	} else {
		port, err := requiredPortString(payload, "port")
		if err != nil {
			return "", err
		}
		values["port"] = port
	}

	serverPort, err := requiredIntField(payload, "server_port", "后端服务端口不能为空", "后端服务端口格式不正确")
	if err != nil {
		return "", err
	}
	values["server_port"] = serverPort

	if tags, present, err := optionalStringArrayJSON(payload, "tags", "标签格式不正确"); err != nil {
		return "", err
	} else if present {
		values["tags"] = tags
	}

	rate, err := requiredNumericStringField(payload, "rate", "倍率不能为空", "倍率格式不正确")
	if err != nil {
		return "", err
	}
	values["rate"] = rate

	if show, present, err := optionalAllowedIntField(payload, "show", []int64{0, 1}, "显示状态格式不正确"); err != nil {
		return "", err
	} else if present {
		values["show"] = show
	} else if !hasID {
		values["show"] = int64(0)
	}
	if entryOnly, present, err := optionalAllowedIntField(payload, "client_entry_only", []int64{0, 1}, "入口分配可见性格式不正确"); err != nil {
		return "", err
	} else if present {
		values["client_entry_only"] = entryOnly
	} else if !hasID {
		values["client_entry_only"] = int64(0)
	}

	if sortValue, present, err := optionalIntField(payload, "sort", "排序格式不正确"); err != nil {
		return "", err
	} else if present {
		values["sort"] = sortValue
	}

	return payloadTimestampSeed(payload), nil
}

func normalizeVmessServer(values, payload map[string]any, hasID bool) error {
	tls, err := requiredIntField(payload, "tls", "TLS不能为空", "TLS格式不正确")
	if err != nil {
		return err
	}
	values["tls"] = tls

	network, err := requiredStringField(payload, "network", "传输协议不能为空")
	if err != nil {
		return err
	}
	values["network"] = network

	if rules, present, err := optionalJSONLikeField(payload, "rules"); err != nil {
		return errors.New("规则配置有误")
	} else if present {
		values["rules"] = rules
	}
	if networkSettings, present, err := optionalJSONLikeField(payload, "networkSettings"); err != nil {
		return errors.New("传输协议配置有误")
	} else if present {
		values["networkSettings"] = networkSettings
	}
	if tlsSettings, present, err := optionalJSONLikeField(payload, "tlsSettings"); err != nil {
		return errors.New("tls配置有误")
	} else if present {
		values["tlsSettings"] = tlsSettings
	}
	if ruleSettings, present, err := optionalJSONLikeField(payload, "ruleSettings"); err != nil {
		return errors.New("规则配置有误")
	} else if present {
		values["ruleSettings"] = ruleSettings
	}
	if dnsSettings, present, err := optionalJSONLikeField(payload, "dnsSettings"); err != nil {
		return errors.New("dns配置有误")
	} else if present {
		values["dnsSettings"] = dnsSettings
	}
	if !hasID {
		if _, exists := values["tls"]; !exists {
			values["tls"] = int64(0)
		}
	}
	return nil
}

func normalizeTrojanServer(values, payload map[string]any, hasID bool) error {
	network, err := requiredStringField(payload, "network", "传输协议不能为空")
	if err != nil {
		return err
	}
	values["network"] = network

	if networkSettings, present, err := optionalJSONLikeField(payload, "network_settings"); err != nil {
		return errors.New("传输协议配置有误")
	} else if present {
		values["network_settings"] = networkSettings
	}
	if allowInsecure, present, err := optionalAllowedIntField(payload, "allow_insecure", []int64{0, 1}, "允许不安全格式不正确"); err != nil {
		return err
	} else if present {
		values["allow_insecure"] = allowInsecure
	} else if !hasID {
		values["allow_insecure"] = int64(0)
	}
	if serverName, present := optionalNullableStringField(payload, "server_name"); present {
		values["server_name"] = serverName
	}
	return nil
}

func normalizeShadowsocksServer(values, payload map[string]any) error {
	cipher, err := requiredStringField(payload, "cipher", "加密方式不能为空")
	if err != nil {
		return err
	}
	values["cipher"] = cipher

	if obfs, present := optionalNullableStringField(payload, "obfs"); present {
		values["obfs"] = obfs
	}
	if obfsSettings, present, err := optionalJSONLikeField(payload, "obfs_settings"); err != nil {
		return errors.New("混淆设置格式不正确")
	} else if present {
		values["obfs_settings"] = obfsSettings
	}
	return nil
}

func normalizeTuicServer(values, payload map[string]any) error {
	insecure, err := requiredAllowedIntField(payload, "insecure", []int64{0, 1}, "insecure不能为空", "insecure格式不正确")
	if err != nil {
		return err
	}
	disableSNI, err := requiredAllowedIntField(payload, "disable_sni", []int64{0, 1}, "disable_sni不能为空", "disable_sni格式不正确")
	if err != nil {
		return err
	}
	zeroRTT, err := requiredAllowedIntField(payload, "zero_rtt_handshake", []int64{0, 1}, "zero_rtt_handshake不能为空", "zero_rtt_handshake格式不正确")
	if err != nil {
		return err
	}

	values["insecure"] = insecure
	values["disable_sni"] = disableSNI
	values["zero_rtt_handshake"] = zeroRTT

	if serverName, present := optionalNullableStringField(payload, "server_name"); present {
		values["server_name"] = serverName
	}
	if udpRelayMode, present := optionalNullableStringField(payload, "udp_relay_mode"); present {
		values["udp_relay_mode"] = udpRelayMode
	}
	if congestionControl, present := optionalNullableStringField(payload, "congestion_control"); present {
		values["congestion_control"] = congestionControl
	}
	return nil
}

func normalizeHysteriaServer(values, payload map[string]any, seed string, hasID bool) error {
	version, err := requiredAllowedIntField(payload, "version", []int64{1, 2}, "版本不能为空", "版本格式不正确")
	if err != nil {
		return err
	}
	values["version"] = version

	if upMbps, present, err := optionalIntField(payload, "up_mbps", "上行速度格式不正确"); err != nil {
		return err
	} else if present {
		values["up_mbps"] = upMbps
	} else if !hasID {
		values["up_mbps"] = int64(0)
	}
	if downMbps, present, err := optionalIntField(payload, "down_mbps", "下行速度格式不正确"); err != nil {
		return err
	} else if present {
		values["down_mbps"] = downMbps
	} else if !hasID {
		values["down_mbps"] = int64(0)
	}

	obfsValue, hasObfs := optionalNullableStringField(payload, "obfs")
	if hasObfs {
		values["obfs"] = obfsValue
	}
	if serverName, present := optionalNullableStringField(payload, "server_name"); present {
		values["server_name"] = serverName
	}
	insecure, err := requiredAllowedIntField(payload, "insecure", []int64{0, 1}, "insecure不能为空", "insecure格式不正确")
	if err != nil {
		return err
	}
	values["insecure"] = insecure

	if isNilValue(obfsValue) {
		values["obfs_password"] = nil
	} else if password, present := optionalNullableStringField(payload, "obfs_password"); present && !isNilValue(password) {
		values["obfs_password"] = password
	} else {
		values["obfs_password"] = phpServerKey(seed, 16)
	}
	return nil
}

func normalizeAnyTLSServer(values, payload map[string]any) error {
	insecure, err := requiredAllowedIntField(payload, "insecure", []int64{0, 1}, "insecure不能为空", "insecure格式不正确")
	if err != nil {
		return err
	}
	values["insecure"] = insecure

	if serverName, present := optionalNullableStringField(payload, "server_name"); present {
		values["server_name"] = serverName
	}
	if paddingScheme, present, err := optionalDecodedJSONField(payload, "padding_scheme"); err != nil {
		return errors.New("保存失败")
	} else if present {
		values["padding_scheme"] = paddingScheme
	}
	return nil
}

func normalizeVlessServer(values, payload map[string]any) error {
	tls, err := requiredAllowedIntField(payload, "tls", []int64{0, 1, 2}, "TLS不能为空", "TLS格式不正确")
	if err != nil {
		return err
	}

	network, err := requiredStringField(payload, "network", "传输协议不能为空")
	if err != nil {
		return err
	}

	tlsSettings, err := mapField(payload, "tls_settings")
	if err != nil {
		return errors.New("tls配置有误")
	}
	if tls == 2 {
		next, err := ensureRealitySettings(tlsSettings)
		if err != nil {
			return errors.New("保存失败")
		}
		tlsSettings = next
	}
	tlsSettings, err = normalizeECHSettings(tlsSettings)
	if err != nil {
		return errors.New("保存失败")
	}
	if tlsSettings != nil || hasPayloadKey(payload, "tls_settings") || tls == 2 {
		values["tls_settings"] = marshalJSONOrNil(tlsSettings)
	}

	if flow, present := optionalNullableStringField(payload, "flow"); present {
		values["flow"] = flow
	}
	if network != "tcp" {
		values["flow"] = nil
	}

	networkSettings, err := mapField(payload, "network_settings")
	if err != nil {
		return errors.New("传输协议配置有误")
	}
	if network == "xhttp" && networkSettings != nil {
		networkSettings = normalizeXHTTPNetworkSettings(networkSettings)
	}
	if networkSettings != nil || hasPayloadKey(payload, "network_settings") {
		values["network_settings"] = marshalJSONOrNil(networkSettings)
	}

	if encryption, present := optionalNullableStringField(payload, "encryption"); present {
		values["encryption"] = encryption
	}

	encryptionSettings, err := mapField(payload, "encryption_settings")
	if err != nil {
		return errors.New("保存失败")
	}
	if encryptionValue, _ := optionalNullableStringField(payload, "encryption"); !isNilValue(encryptionValue) && fmt.Sprint(encryptionValue) == "mlkem768x25519plus" {
		next, err := ensureMLKEMSettings(encryptionSettings, false)
		if err != nil {
			return errors.New("保存失败")
		}
		encryptionSettings = next
	}
	if encryptionSettings != nil || hasPayloadKey(payload, "encryption_settings") {
		values["encryption_settings"] = marshalJSONOrNil(encryptionSettings)
	}

	values["tls"] = tls
	values["network"] = network
	return nil
}

func normalizeV2nodeServer(values, payload map[string]any, seed string, hasID bool) error {
	protocol, err := requiredStringField(payload, "protocol", "协议不能为空")
	if err != nil {
		return err
	}
	if sendThrough, present := optionalNullableStringField(payload, "send_through"); present {
		values["send_through"] = sendThrough
	}
	tls, err := requiredAllowedIntField(payload, "tls", []int64{0, 1, 2}, "TLS不能为空", "TLS格式不正确")
	if err != nil {
		return err
	}
	if containsString([]string{"anytls", "hysteria2", "trojan", "tuic"}, protocol) {
		tls = 1
	}

	network, err := requiredStringField(payload, "network", "传输协议不能为空")
	if err != nil {
		return err
	}

	tlsSettings, err := mapField(payload, "tls_settings")
	if err != nil {
		return errors.New("tls配置有误")
	}
	if tls == 2 {
		next, err := ensureRealitySettings(tlsSettings)
		if err != nil {
			return errors.New("保存失败")
		}
		tlsSettings = next
	}
	tlsSettings, err = normalizeECHSettings(tlsSettings)
	if err != nil {
		return errors.New("保存失败")
	}
	if tlsSettings != nil || hasPayloadKey(payload, "tls_settings") || tls == 2 {
		values["tls_settings"] = marshalJSONOrNil(tlsSettings)
	}

	if flow, present := optionalNullableStringField(payload, "flow"); present {
		values["flow"] = flow
	}

	if encryption, present := optionalNullableStringField(payload, "encryption"); present {
		values["encryption"] = encryption
	}
	if network != "tcp" && fmt.Sprint(values["encryption"]) != "mlkem768x25519plus" {
		values["flow"] = nil
	}

	networkSettings, err := mapField(payload, "network_settings")
	if err != nil {
		return errors.New("传输协议配置有误")
	}
	if networkSettings != nil {
		networkSettings = normalizeV2nodeNetworkSettings(network, networkSettings)
	}
	if networkSettings != nil || hasPayloadKey(payload, "network_settings") {
		values["network_settings"] = marshalJSONOrNil(networkSettings)
	}

	encryptionSettings, err := mapField(payload, "encryption_settings")
	if err != nil {
		return errors.New("保存失败")
	}
	if fmt.Sprint(values["encryption"]) == "mlkem768x25519plus" {
		next, err := ensureMLKEMSettings(encryptionSettings, true)
		if err != nil {
			return errors.New("保存失败")
		}
		encryptionSettings = next
	}
	if encryptionSettings != nil || hasPayloadKey(payload, "encryption_settings") {
		values["encryption_settings"] = marshalJSONOrNil(encryptionSettings)
	}

	disableSNI, err := requiredAllowedIntField(payload, "disable_sni", []int64{0, 1}, "disable_sni不能为空", "disable_sni格式不正确")
	if err != nil {
		return err
	}
	zeroRTT, err := requiredAllowedIntField(payload, "zero_rtt_handshake", []int64{0, 1}, "zero_rtt_handshake不能为空", "zero_rtt_handshake格式不正确")
	if err != nil {
		return err
	}
	values["disable_sni"] = disableSNI
	values["zero_rtt_handshake"] = zeroRTT

	if udpRelayMode, present := optionalNullableStringField(payload, "udp_relay_mode"); present {
		values["udp_relay_mode"] = udpRelayMode
	}
	if congestionControl, present := optionalNullableStringField(payload, "congestion_control"); present {
		values["congestion_control"] = congestionControl
	}
	if cipher, present := optionalNullableStringField(payload, "cipher"); present {
		values["cipher"] = cipher
	} else if protocol == "shadowsocks" {
		values["cipher"] = "aes-128-gcm"
	}

	if upMbps, present, err := optionalIntField(payload, "up_mbps", "上行速度格式不正确"); err != nil {
		return err
	} else if present {
		values["up_mbps"] = upMbps
	} else if !hasID {
		values["up_mbps"] = int64(0)
	}
	if downMbps, present, err := optionalIntField(payload, "down_mbps", "下行速度格式不正确"); err != nil {
		return err
	} else if present {
		values["down_mbps"] = downMbps
	} else if !hasID {
		values["down_mbps"] = int64(0)
	}

	obfsValue, hasObfs := optionalNullableStringField(payload, "obfs")
	if hasObfs {
		values["obfs"] = obfsValue
	}
	if isNilValue(obfsValue) {
		values["obfs_password"] = nil
	} else if password, present := optionalNullableStringField(payload, "obfs_password"); present && !isNilValue(password) {
		values["obfs_password"] = password
	} else if !isNilValue(obfsValue) {
		values["obfs_password"] = phpServerKey(seed, 16)
	}

	if paddingScheme, present, err := optionalDecodedJSONField(payload, "padding_scheme"); err != nil {
		return errors.New("保存失败")
	} else if present {
		values["padding_scheme"] = paddingScheme
	}

	ddnsSettings, err := mapField(payload, "ddns_settings")
	if err != nil {
		return errors.New("DDNS配置有误")
	}
	if ddnsSettings != nil || hasPayloadKey(payload, "ddns_settings") {
		values["ddns_settings"] = marshalJSONOrNil(ddnsSettings)
	}

	values["protocol"] = protocol
	values["tls"] = tls
	values["network"] = network
	return nil
}

func (s *DBService) managedServerExists(ctx context.Context, table string, id int64) (bool, error) {
	var exists int64
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM `+quoteIdentifier(table)+` WHERE id = $1 LIMIT 1`, id).Scan(&exists)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return false, fmt.Errorf("query managed server existence: %w", err)
}

func (s *DBService) insertManagedServer(ctx context.Context, def managedServerDefinition, values map[string]any) error {
	columns := make([]string, 0, len(def.columns))
	args := make([]any, 0, len(def.columns))
	for _, column := range def.columns {
		value, ok := values[column]
		if !ok {
			continue
		}
		columns = append(columns, quoteIdentifier(column))
		args = append(args, value)
	}
	if len(columns) == 0 {
		return errors.New("missing values")
	}

	placeholders := make([]string, 0, len(columns))
	for index := range columns {
		placeholders = append(placeholders, fmt.Sprintf("$%d", index+1))
	}

	_, err := s.db.ExecContext(
		ctx,
		`INSERT INTO `+quoteIdentifier(def.table)+` (`+strings.Join(columns, ", ")+`)
VALUES (`+strings.Join(placeholders, ", ")+`)`,
		args...,
	)
	return err
}

func (s *DBService) updateManagedServerRecord(ctx context.Context, def managedServerDefinition, id int64, values map[string]any) error {
	sets := make([]string, 0, len(def.columns))
	args := make([]any, 0, len(def.columns)+1)
	args = append(args, id)

	for _, column := range def.columns {
		if column == "created_at" {
			continue
		}
		value, ok := values[column]
		if !ok {
			continue
		}
		args = append(args, value)
		sets = append(sets, fmt.Sprintf("%s = $%d", quoteIdentifier(column), len(args)))
	}

	if len(sets) == 0 {
		return errors.New("missing update values")
	}

	result, err := s.db.ExecContext(
		ctx,
		`UPDATE `+quoteIdentifier(def.table)+`
SET `+strings.Join(sets, ", ")+`
WHERE id = $1`,
		args...,
	)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil || affected == 0 {
		return errors.New("no rows updated")
	}
	return nil
}

func managedServerPayloadID(payload map[string]any) (*int64, error) {
	value, ok := payload["id"]
	if !ok || value == nil || strings.TrimSpace(fmt.Sprint(value)) == "" {
		return nil, nil
	}
	id, ok := anyToInt64Strict(value)
	if !ok || id <= 0 {
		return nil, errors.New("invalid id")
	}
	return &id, nil
}

func payloadTimestampSeed(payload map[string]any) string {
	if value, ok := payload["created_at"]; ok && value != nil {
		raw := strings.TrimSpace(fmt.Sprint(value))
		if raw != "" && raw != "<nil>" {
			return raw
		}
	}
	return strconv.FormatInt(time.Now().Unix(), 10)
}

func requiredStringField(payload map[string]any, key, requiredMessage string) (string, error) {
	value, ok := payload[key]
	if !ok || value == nil {
		return "", errors.New(requiredMessage)
	}
	raw := strings.TrimSpace(fmt.Sprint(value))
	if raw == "" || strings.EqualFold(raw, "null") {
		return "", errors.New(requiredMessage)
	}
	return raw, nil
}

func requiredPortString(payload map[string]any, key string) (string, error) {
	value, ok := payload[key]
	if !ok || value == nil {
		return "", errors.New("连接端口不能为空")
	}
	raw := strings.TrimSpace(fmt.Sprint(value))
	if raw == "" || strings.EqualFold(raw, "null") {
		return "", errors.New("连接端口不能为空")
	}
	return raw, nil
}

func requiredNumericStringField(payload map[string]any, key, requiredMessage, formatMessage string) (string, error) {
	value, ok := payload[key]
	if !ok || value == nil {
		return "", errors.New(requiredMessage)
	}
	raw, ok := anyToNumericString(value)
	if !ok || raw == "" {
		return "", errors.New(formatMessage)
	}
	return raw, nil
}

func requiredIntField(payload map[string]any, key, requiredMessage, formatMessage string) (int64, error) {
	value, ok := payload[key]
	if !ok || value == nil || strings.TrimSpace(fmt.Sprint(value)) == "" {
		return 0, errors.New(requiredMessage)
	}
	next, ok := anyToInt64Strict(value)
	if !ok {
		return 0, errors.New(formatMessage)
	}
	return next, nil
}

func requiredAllowedIntField(payload map[string]any, key string, allowed []int64, requiredMessage, formatMessage string) (int64, error) {
	value, err := requiredIntField(payload, key, requiredMessage, formatMessage)
	if err != nil {
		return 0, err
	}
	if !containsInt64(allowed, value) {
		return 0, errors.New(formatMessage)
	}
	return value, nil
}

func optionalAllowedIntField(payload map[string]any, key string, allowed []int64, formatMessage string) (int64, bool, error) {
	value, present, err := optionalIntField(payload, key, formatMessage)
	if err != nil || !present {
		return value, present, err
	}
	if !containsInt64(allowed, value) {
		return 0, true, errors.New(formatMessage)
	}
	return value, true, nil
}

func optionalIntField(payload map[string]any, key, formatMessage string) (int64, bool, error) {
	value, ok := payload[key]
	if !ok || value == nil {
		return 0, false, nil
	}
	raw := strings.TrimSpace(fmt.Sprint(value))
	if raw == "" || strings.EqualFold(raw, "null") {
		return 0, true, nil
	}
	next, ok := anyToInt64Strict(value)
	if !ok {
		return 0, true, errors.New(formatMessage)
	}
	return next, true, nil
}

func requiredIntArrayJSON(payload map[string]any, key, requiredMessage, formatMessage string) (string, error) {
	value, ok := payload[key]
	if !ok {
		return "", errors.New(requiredMessage)
	}
	items, err := anyToInt64Slice(value)
	if err != nil {
		return "", errors.New(formatMessage)
	}
	if len(items) == 0 {
		return "", errors.New(requiredMessage)
	}
	raw, err := json.Marshal(items)
	if err != nil {
		return "", errors.New(formatMessage)
	}
	return string(raw), nil
}

func optionalIntArrayJSON(payload map[string]any, key, formatMessage string) (string, bool, error) {
	value, ok := payload[key]
	if !ok {
		return "", false, nil
	}
	if value == nil {
		return "[]", true, nil
	}
	items, err := anyToInt64Slice(value)
	if err != nil {
		return "", true, errors.New(formatMessage)
	}
	raw, err := json.Marshal(items)
	if err != nil {
		return "", true, errors.New(formatMessage)
	}
	return string(raw), true, nil
}

func optionalStringArrayJSON(payload map[string]any, key, formatMessage string) (string, bool, error) {
	value, ok := payload[key]
	if !ok {
		return "", false, nil
	}
	if value == nil {
		return "[]", true, nil
	}
	items, err := anyToStringSlice(value)
	if err != nil {
		return "", true, errors.New(formatMessage)
	}
	raw, err := json.Marshal(items)
	if err != nil {
		return "", true, errors.New(formatMessage)
	}
	return string(raw), true, nil
}

func optionalNullableStringField(payload map[string]any, key string) (any, bool) {
	value, ok := payload[key]
	if !ok {
		return nil, false
	}
	if value == nil {
		return nil, true
	}
	raw := strings.TrimSpace(fmt.Sprint(value))
	if raw == "" || strings.EqualFold(raw, "null") {
		return nil, true
	}
	return raw, true
}

func optionalJSONLikeField(payload map[string]any, key string) (any, bool, error) {
	value, ok := payload[key]
	if !ok {
		return nil, false, nil
	}
	if value == nil {
		return nil, true, nil
	}
	switch typed := value.(type) {
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" || strings.EqualFold(trimmed, "null") {
			return nil, true, nil
		}
		if json.Valid([]byte(trimmed)) {
			return trimmed, true, nil
		}
		return trimmed, true, nil
	default:
		raw, err := json.Marshal(value)
		if err != nil {
			return nil, true, err
		}
		return string(raw), true, nil
	}
}

func optionalDecodedJSONField(payload map[string]any, key string) (any, bool, error) {
	value, ok := payload[key]
	if !ok {
		return nil, false, nil
	}
	if value == nil {
		return nil, true, nil
	}
	switch typed := value.(type) {
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" || strings.EqualFold(trimmed, "null") {
			return nil, true, nil
		}
		var decoded any
		if err := json.Unmarshal([]byte(trimmed), &decoded); err != nil {
			return nil, true, nil
		}
		raw, err := json.Marshal(decoded)
		if err != nil {
			return nil, true, err
		}
		return string(raw), true, nil
	default:
		raw, err := json.Marshal(value)
		if err != nil {
			return nil, true, err
		}
		return string(raw), true, nil
	}
}

func mapField(payload map[string]any, key string) (map[string]any, error) {
	value, ok := payload[key]
	if !ok || value == nil {
		return nil, nil
	}
	switch typed := value.(type) {
	case map[string]any:
		return cloneMap(typed), nil
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" || strings.EqualFold(trimmed, "null") {
			return nil, nil
		}
		var next map[string]any
		if err := json.Unmarshal([]byte(trimmed), &next); err != nil {
			return nil, err
		}
		return next, nil
	default:
		raw, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		var next map[string]any
		if err := json.Unmarshal(raw, &next); err != nil {
			return nil, err
		}
		return next, nil
	}
}

func normalizeXHTTPNetworkSettings(settings map[string]any) map[string]any {
	extra, ok := settings["extra"].(map[string]any)
	if !ok {
		return settings
	}

	if value, ok := boolFromAny(extra["noGRPCHeader"]); ok {
		extra["noGRPCHeader"] = value
	}
	if value, ok := boolFromAny(extra["noSSEHeader"]); ok {
		extra["noSSEHeader"] = value
	}
	if value, ok := intFromAny(extra["scMaxBufferedPosts"]); ok {
		extra["scMaxBufferedPosts"] = value
	}
	if xmux, ok := extra["xmux"].(map[string]any); ok {
		if value, ok := intFromAny(xmux["hKeepAlivePeriod"]); ok {
			xmux["hKeepAlivePeriod"] = value
		}
		extra["xmux"] = xmux
	}
	if downloadSettings, ok := extra["downloadSettings"].(map[string]any); ok {
		if value, ok := intFromAny(downloadSettings["port"]); ok {
			downloadSettings["port"] = value
		}
		extra["downloadSettings"] = downloadSettings
	}

	settings["extra"] = extra
	return settings
}

func normalizeV2nodeNetworkSettings(network string, settings map[string]any) map[string]any {
	if value, ok := boolFromAny(settings["acceptProxyProtocol"]); ok {
		settings["acceptProxyProtocol"] = value
	}
	if network == "xhttp" {
		settings = normalizeXHTTPNetworkSettings(settings)
	}
	return settings
}

func ensureRealitySettings(settings map[string]any) (map[string]any, error) {
	if settings == nil {
		settings = make(map[string]any)
	}
	if !nonEmptyString(settings["public_key"]) || !nonEmptyString(settings["private_key"]) {
		publicKey, privateKey, err := generateURLSafeKeyPair()
		if err != nil {
			return nil, err
		}
		if !nonEmptyString(settings["public_key"]) {
			settings["public_key"] = publicKey
		}
		if !nonEmptyString(settings["private_key"]) {
			settings["private_key"] = privateKey
		}
	}
	if !nonEmptyString(settings["short_id"]) {
		sum := sha1.Sum([]byte(fmt.Sprint(settings["private_key"])))
		settings["short_id"] = fmt.Sprintf("%x", sum)[:8]
	}
	if !nonEmptyString(settings["server_port"]) {
		settings["server_port"] = "443"
	}
	return settings, nil
}

func normalizeECHSettings(settings map[string]any) (map[string]any, error) {
	if settings == nil {
		return nil, nil
	}

	switch strings.TrimSpace(fmt.Sprint(settings["ech"])) {
	case "", "<nil>", "cloudflare":
		return settings, nil
	case "custom":
		outerSNI := strings.TrimSpace(fmt.Sprint(settings["ech_server_name"]))
		if outerSNI == "" || outerSNI == "<nil>" {
			settings["ech"] = ""
			return settings, nil
		}

		missingKey := !nonEmptyString(settings["ech_key"])
		missingConfig := !nonEmptyString(settings["ech_config"])
		if !missingKey && !missingConfig {
			return settings, nil
		}

		echKey, echConfig, err := generateECHMaterial(outerSNI)
		if err != nil {
			return nil, err
		}
		if missingKey {
			settings["ech_key"] = echKey
		}
		if missingConfig {
			settings["ech_config"] = echConfig
		}
		return settings, nil
	default:
		settings["ech"] = ""
		return settings, nil
	}
}

func generateECHMaterial(publicName string) (string, string, error) {
	configBytes, privateKey, err := generateECHConfig(publicName)
	if err != nil {
		return "", "", err
	}

	var keyBuffer cryptobyte.Builder
	keyBuffer.AddUint16(uint16(len(privateKey)))
	keyBuffer.AddBytes(privateKey)
	keyBuffer.AddUint16(uint16(len(configBytes)))
	keyBuffer.AddBytes(configBytes)
	keyBytes, err := keyBuffer.Bytes()
	if err != nil {
		return "", "", err
	}

	return base64.StdEncoding.EncodeToString(keyBytes), base64.StdEncoding.EncodeToString(configBytes), nil
}

func generateECHConfig(publicName string) ([]byte, []byte, error) {
	privateKey := make([]byte, 32)
	if _, err := io.ReadFull(crand.Reader, privateKey); err != nil {
		return nil, nil, err
	}

	privateKeyHandle, err := ecdh.X25519().NewPrivateKey(privateKey)
	if err != nil {
		return nil, nil, err
	}
	publicKey := privateKeyHandle.PublicKey().Bytes()

	var builder cryptobyte.Builder
	builder.AddUint16(0xfe0d)
	builder.AddUint16LengthPrefixed(func(child *cryptobyte.Builder) {
		child.AddUint8(0)
		child.AddUint16(0x0020)
		child.AddUint16(uint16(len(publicKey)))
		child.AddBytes(publicKey)
		child.AddUint16LengthPrefixed(func(suites *cryptobyte.Builder) {
			for _, suite := range [][2]uint16{
				{0x0001, 0x0001},
				{0x0001, 0x0002},
				{0x0001, 0x0003},
			} {
				suites.AddUint16(suite[0])
				suites.AddUint16(suite[1])
			}
		})
		child.AddUint8(0)
		child.AddUint8(uint8(len(publicName)))
		child.AddBytes([]byte(publicName))
		child.AddUint16(0)
	})

	configBytes, err := builder.Bytes()
	if err != nil {
		return nil, nil, err
	}
	return configBytes, privateKey, nil
}

func ensureMLKEMSettings(settings map[string]any, includeDefaults bool) (map[string]any, error) {
	if settings == nil {
		settings = make(map[string]any)
	}

	if includeDefaults {
		if !nonEmptyString(settings["mode"]) {
			settings["mode"] = "native"
		}
		if !nonEmptyString(settings["rtt"]) {
			settings["rtt"] = "0rtt"
			settings["ticket"] = "600s"
		}
	}

	if fmt.Sprint(settings["rtt"]) == "1rtt" {
		settings["ticket"] = "0s"
	}
	if !nonEmptyString(settings["private_key"]) || !nonEmptyString(settings["password"]) {
		publicKey, privateKey, err := generateURLSafeKeyPair()
		if err != nil {
			return nil, err
		}
		if !nonEmptyString(settings["private_key"]) {
			settings["private_key"] = privateKey
		}
		if !nonEmptyString(settings["password"]) {
			settings["password"] = publicKey
		}
	}
	return settings, nil
}

func generateURLSafeKeyPair() (string, string, error) {
	privateKey := make([]byte, 32)
	if _, err := crand.Read(privateKey); err != nil {
		return "", "", err
	}
	publicKey, err := curve25519.X25519(privateKey, curve25519.Basepoint)
	if err != nil {
		return "", "", err
	}
	return base64.RawURLEncoding.EncodeToString(publicKey), base64.RawURLEncoding.EncodeToString(privateKey), nil
}

func phpServerKey(seed string, length int) string {
	sum := md5.Sum([]byte(seed))
	hexValue := fmt.Sprintf("%x", sum)
	if length > len(hexValue) {
		length = len(hexValue)
	}
	return base64.StdEncoding.EncodeToString([]byte(hexValue[:length]))
}

func anyToNumericString(value any) (string, bool) {
	switch typed := value.(type) {
	case json.Number:
		if _, err := typed.Float64(); err != nil {
			return "", false
		}
		return typed.String(), true
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64), true
	case float32:
		return strconv.FormatFloat(float64(typed), 'f', -1, 64), true
	case int:
		return strconv.FormatInt(int64(typed), 10), true
	case int64:
		return strconv.FormatInt(typed, 10), true
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return "", false
		}
		if _, err := strconv.ParseFloat(trimmed, 64); err != nil {
			return "", false
		}
		return trimmed, true
	default:
		trimmed := strings.TrimSpace(fmt.Sprint(value))
		if trimmed == "" || trimmed == "<nil>" {
			return "", false
		}
		if _, err := strconv.ParseFloat(trimmed, 64); err != nil {
			return "", false
		}
		return trimmed, true
	}
}

func anyToInt64Slice(value any) ([]int64, error) {
	switch typed := value.(type) {
	case []any:
		result := make([]int64, 0, len(typed))
		for _, item := range typed {
			next, ok := anyToInt64Strict(item)
			if !ok {
				return nil, errors.New("invalid integer slice")
			}
			result = append(result, next)
		}
		return result, nil
	case []int64:
		return append([]int64(nil), typed...), nil
	case []string:
		result := make([]int64, 0, len(typed))
		for _, item := range typed {
			next, err := strconv.ParseInt(strings.TrimSpace(item), 10, 64)
			if err != nil {
				return nil, err
			}
			result = append(result, next)
		}
		return result, nil
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" || strings.EqualFold(trimmed, "null") {
			return []int64{}, nil
		}
		if strings.HasPrefix(trimmed, "[") {
			var generic []any
			if err := json.Unmarshal([]byte(trimmed), &generic); err != nil {
				return nil, err
			}
			return anyToInt64Slice(generic)
		}
		parts := strings.Split(trimmed, ",")
		result := make([]int64, 0, len(parts))
		for _, item := range parts {
			item = strings.TrimSpace(strings.Trim(item, `"'`))
			if item == "" {
				continue
			}
			next, err := strconv.ParseInt(item, 10, 64)
			if err != nil {
				return nil, err
			}
			result = append(result, next)
		}
		return result, nil
	default:
		next, ok := anyToInt64Strict(value)
		if !ok {
			return nil, errors.New("invalid integer")
		}
		return []int64{next}, nil
	}
}

func anyToStringSlice(value any) ([]string, error) {
	switch typed := value.(type) {
	case []any:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			next := strings.TrimSpace(fmt.Sprint(item))
			if next != "" && next != "<nil>" {
				result = append(result, next)
			}
		}
		return result, nil
	case []string:
		return normalizeStringSlice(typed), nil
	case string:
		return parseServerStringList(typed), nil
	default:
		next := strings.TrimSpace(fmt.Sprint(value))
		if next == "" || next == "<nil>" {
			return []string{}, nil
		}
		return []string{next}, nil
	}
}

func anyToInt64Strict(value any) (int64, bool) {
	switch typed := value.(type) {
	case json.Number:
		next, err := typed.Int64()
		if err == nil {
			return next, true
		}
		floatValue, err := typed.Float64()
		if err != nil {
			return 0, false
		}
		next = int64(floatValue)
		return next, float64(next) == floatValue
	case float64:
		next := int64(typed)
		return next, float64(next) == typed
	case float32:
		next := int64(typed)
		return next, float32(next) == typed
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	case int32:
		return int64(typed), true
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" || strings.EqualFold(trimmed, "null") {
			return 0, false
		}
		next, err := strconv.ParseInt(trimmed, 10, 64)
		return next, err == nil
	default:
		trimmed := strings.TrimSpace(fmt.Sprint(value))
		if trimmed == "" || trimmed == "<nil>" {
			return 0, false
		}
		next, err := strconv.ParseInt(trimmed, 10, 64)
		return next, err == nil
	}
}

func boolFromAny(value any) (bool, bool) {
	switch typed := value.(type) {
	case bool:
		return typed, true
	case string:
		trimmed := strings.TrimSpace(strings.ToLower(typed))
		switch trimmed {
		case "1", "true", "on", "yes":
			return true, true
		case "0", "false", "off", "no":
			return false, true
		default:
			return false, false
		}
	case json.Number:
		next, err := typed.Int64()
		return next != 0, err == nil
	case float64:
		return typed != 0, true
	case int:
		return typed != 0, true
	case int64:
		return typed != 0, true
	default:
		return false, false
	}
}

func intFromAny(value any) (int, bool) {
	next, ok := anyToInt64Strict(value)
	return int(next), ok
}

func hasPayloadKey(payload map[string]any, key string) bool {
	_, ok := payload[key]
	return ok
}

func isNilValue(value any) bool {
	if value == nil {
		return true
	}
	raw := strings.TrimSpace(fmt.Sprint(value))
	return raw == "" || raw == "<nil>" || strings.EqualFold(raw, "null")
}

func nonEmptyString(value any) bool {
	if value == nil {
		return false
	}
	raw := strings.TrimSpace(fmt.Sprint(value))
	return raw != "" && raw != "<nil>" && !strings.EqualFold(raw, "null")
}

func marshalJSONOrNil(value map[string]any) any {
	if value == nil {
		return nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return string(raw)
}

func cloneMap(values map[string]any) map[string]any {
	if values == nil {
		return nil
	}
	result := make(map[string]any, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

func quoteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func joinQuotedIdentifiers(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, quoteIdentifier(value))
	}
	return strings.Join(quoted, ", ")
}

func containsInt64(values []int64, target int64) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
