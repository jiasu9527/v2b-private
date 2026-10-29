package aiops

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"strings"

	"forest/go-api/internal/admin"
)

func nodeToolDefinitions() []ToolDefinition {
	str := func(description string, max int) map[string]any {
		return map[string]any{"type": "string", "maxLength": max, "description": description}
	}
	integer := func(min, max int) map[string]any {
		return map[string]any{"type": "integer", "minimum": min, "maximum": max}
	}
	nullableString := func(max int) map[string]any {
		return map[string]any{"type": []string{"string", "null"}, "maxLength": max}
	}
	props := map[string]any{
		"name": str("节点名称", 255), "host": str("单个域名或IP", 255), "port": integer(1, 65535), "server_port": integer(1, 65535), "group_id": map[string]any{"type": "array", "items": integer(1, 2147483647), "minItems": 1, "maxItems": 50, "uniqueItems": true}, "rate": map[string]any{"type": "number", "minimum": 0, "maximum": 100000, "description": "流量计费倍率"}, "show": integer(0, 1), "client_entry_only": integer(0, 1), "sort": map[string]any{"type": []string{"integer", "null"}, "minimum": 0, "maximum": 2147483647}, "tags": map[string]any{"type": "array", "items": str("标签", 64), "maxItems": 20},
		"tls": integer(0, 2), "network": str("tcp/ws/grpc/http/h2/kcp/quic/httpupgrade/xhttp/splithttp；以节点协议支持为准", 11), "flow": nullableString(64), "server_name": nullableString(255), "allow_insecure": integer(0, 1), "insecure": integer(0, 1), "disable_sni": integer(0, 1), "zero_rtt_handshake": integer(0, 1), "udp_relay_mode": nullableString(16), "congestion_control": nullableString(16), "cipher": str("加密方式", 64), "listen_ip": nullableString(45), "send_through": nullableString(45), "up_mbps": integer(0, 2147483647), "down_mbps": integer(0, 2147483647),
	}
	tls := map[string]any{"server_name": nullableString(255), "allow_insecure": integer(0, 1), "fingerprint": nullableString(255), "alpn": map[string]any{"type": "array", "items": str("ALPN", 128), "maxItems": 10}, "dest": nullableString(255), "server_port": integer(1, 65535), "xver": integer(0, 2), "reject_unknown_sni": integer(0, 1)}
	network := map[string]any{"path": nullableString(2048), "host": nullableString(2048), "serviceName": nullableString(2048), "mode": nullableString(2048), "headers": map[string]any{"type": "object", "properties": map[string]any{"Host": nullableString(255)}, "additionalProperties": false}}
	props["tls_settings"] = map[string]any{"type": "object", "properties": tls, "additionalProperties": false, "description": "只合并公开TLS字段，私钥及证书配置保留；VMess也统一使用本字段。REALITY必须已有密钥。"}
	props["network_settings"] = map[string]any{"type": "object", "properties": network, "additionalProperties": false, "description": "只合并传输字段，不替换整个配置；VMess也统一使用本字段。"}
	identity := func() map[string]any {
		return map[string]any{"node_type": map[string]any{"type": "string", "enum": []string{"shadowsocks", "vmess", "vless", "trojan", "tuic", "hysteria", "anytls", "v2node"}}, "node_id": integer(1, 2147483647)}
	}
	out := []ToolDefinition{}
	add := func(name, description string, p map[string]any, required []string) {
		raw, _ := json.Marshal(map[string]any{"type": "object", "properties": p, "required": required, "additionalProperties": false})
		out = append(out, ToolDefinition{Type: "function", Function: ToolFunctionDefinition{Name: name, Description: description, Parameters: raw}})
	}
	add("node_detail", "查询指定节点可编辑连接配置和editable_fields；不返回私钥、密码、安装密钥或任意嵌套配置。先nodes_list定位协议和ID。", identity(), []string{"node_type", "node_id"})
	p := identity()
	p["changes"] = map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	p["reason"] = str("原因、影响与回滚办法", 500)
	add("propose_node_update", "创建节点连接配置修改审批卡。先node_detail查看可编辑字段；支持地址/前后端端口、权限组、计费倍率、显示、TLS/SNI及传输参数；保留未指定配置和凭据，确认后执行。修改面板配置不等于远程部署或服务已重启。", p, []string{"node_type", "node_id", "changes", "reason"})
	return out
}

type nodeToolArgs struct {
	NodeType string         `json:"node_type"`
	NodeID   int64          `json:"node_id"`
	Changes  map[string]any `json:"changes"`
	Reason   string         `json:"reason"`
}

func (s *Service) callNodeTool(ctx context.Context, actor, sessionID int64, call ToolCall) (ToolOutcome, error) {
	var a nodeToolArgs
	decoder := json.NewDecoder(strings.NewReader(call.Function.Arguments))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&a) != nil || decoder.Decode(new(any)) != io.EOF || a.NodeID <= 0 || nodeTables[a.NodeType] == "" {
		return ToolOutcome{}, ErrValidation
	}
	switch call.Function.Name {
	case "node_detail":
		tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return ToolOutcome{}, err
		}
		defer tx.Rollback()
		row, _, err := loadNodeUpdateRow(ctx, tx, a.NodeType, a.NodeID, false)
		if err != nil {
			return ToolOutcome{}, err
		}
		if err = tx.Commit(); err != nil {
			return ToolOutcome{}, err
		}
		raw, _ := json.Marshal(map[string]any{"node_type": a.NodeType, "node_id": a.NodeID, "settings": admin.AIManagedNodePublic(a.NodeType, row), "editable_fields": admin.AIManagedNodeFields(a.NodeType), "credentials_omitted": true})
		if len(raw) > maxToolOutputBytes {
			return ToolOutcome{}, ErrUnavailable
		}
		return ToolOutcome{Content: string(raw)}, nil
	case "propose_node_update":
		if admin.ValidateAIManagedNodeChanges(a.NodeType, a.Changes) != nil {
			return ToolOutcome{}, ErrValidation
		}
		payload, _ := json.Marshal(nodeUpdatePayload{NodeType: a.NodeType, NodeID: a.NodeID, Changes: a.Changes})
		action, err := s.proposeAction(ctx, actor, sessionID, call.ID, actionSpec{Kind: "node_update", TargetID: a.NodeID, TargetType: a.NodeType, Payload: payload, Reason: a.Reason})
		if err != nil {
			return ToolOutcome{}, err
		}
		raw, _ := json.Marshal(actionToolSummary(action))
		return ToolOutcome{Content: string(raw), Pending: true}, nil
	default:
		return ToolOutcome{}, ErrValidation
	}
}
