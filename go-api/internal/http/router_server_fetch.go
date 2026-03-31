package httpapi

import (
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"forest/go-api/internal/config"
	"forest/go-api/internal/nodeapi"

	"github.com/vmihailenco/msgpack/v5"
)

const deepbworkV2RayConfig = `{"log":{"loglevel":"debug","access":"access.log","error":"error.log"},"api":{"services":["HandlerService","StatsService"],"tag":"api"},"dns":{},"stats":{},"inbounds":[{"port":443,"protocol":"vmess","settings":{"clients":[]},"sniffing":{"enabled":true,"destOverride":["http","tls"]},"streamSettings":{"network":"tcp"},"tag":"proxy"},{"listen":"127.0.0.1","port":23333,"protocol":"dokodemo-door","settings":{"address":"0.0.0.0"},"tag":"api"}],"outbounds":[{"protocol":"freedom","settings":{}},{"protocol":"blackhole","settings":{},"tag":"block"}],"routing":{"rules":[{"type":"field","inboundTag":"api","outboundTag":"api"}]},"policy":{"levels":{"0":{"handshake":4,"connIdle":300,"uplinkOnly":5,"downlinkOnly":30,"statsUserUplink":true,"statsUserDownlink":true}}}}`

const trojanTidalabConfig = `{"run_type":"server","local_addr":"0.0.0.0","local_port":443,"remote_addr":"www.taobao.com","remote_port":80,"password":[],"ssl":{"cert":"server.crt","key":"server.key","sni":"domain.com"},"api":{"enabled":true,"api_addr":"127.0.0.1","api_port":10000}}`

func handleServerUniProxyUser(w http.ResponseWriter, r *http.Request, cfg config.Config, service nodeapi.Service) bool {
	server, ok := lookupNodeServer(w, r, cfg, service, "")
	if !ok {
		return true
	}
	if err := service.TouchLastCheck(r.Context(), server.NodeType, server.ID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": err.Error()})
		return true
	}

	users, err := service.AvailableUsers(r.Context(), server.GroupIDs)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": err.Error()})
		return true
	}

	payload := map[string]any{"users": users}
	if wantsMsgpack(r) {
		return writeMsgpackWithETag(w, r, payload)
	}
	return writeJSONWithETag(w, r, payload)
}

func handleServerUniProxyConfig(w http.ResponseWriter, r *http.Request, cfg config.Config, service nodeapi.Service) bool {
	server, ok := lookupNodeServer(w, r, cfg, service, "")
	if !ok {
		return true
	}

	routes, err := service.Routes(r.Context(), server.RouteIDs)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": err.Error()})
		return true
	}

	return writeJSONWithETag(w, r, buildUniProxyConfig(cfg, server, routes))
}

func handleServerUniProxyAliveList(w http.ResponseWriter, r *http.Request, cfg config.Config, service nodeapi.Service) bool {
	_, ok := lookupNodeServer(w, r, cfg, service, "")
	if !ok {
		return true
	}

	alive, err := service.AliveList(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": err.Error()})
		return true
	}
	writeJSON(w, http.StatusOK, map[string]any{"alive": alive})
	return true
}

func handleServerUniProxyAlive(w http.ResponseWriter, r *http.Request, cfg config.Config, service nodeapi.Service) bool {
	server, ok := lookupNodeServer(w, r, cfg, service, "")
	if !ok {
		return true
	}

	users, err := decodeAlivePayload(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}

	if err := service.ReportAlive(r.Context(), nodeapi.AliveReportRequest{
		NodeID:   server.ID,
		NodeType: server.NodeType,
		Users:    users,
	}); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": err.Error()})
		return true
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": true})
	return true
}

func handleServerDeepbworkUser(w http.ResponseWriter, r *http.Request, cfg config.Config, service nodeapi.Service) bool {
	server, ok := lookupNodeServer(w, r, cfg, service, "vmess")
	if !ok {
		return true
	}
	if err := service.TouchLastCheck(r.Context(), server.NodeType, server.ID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": err.Error()})
		return true
	}

	users, err := service.AvailableUsers(r.Context(), server.GroupIDs)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": err.Error()})
		return true
	}

	payload := map[string]any{"msg": "ok", "data": buildDeepbworkUsers(users)}
	return writeJSONWithETag(w, r, payload)
}

func handleServerShadowsocksUser(w http.ResponseWriter, r *http.Request, cfg config.Config, service nodeapi.Service) bool {
	server, ok := lookupNodeServer(w, r, cfg, service, "shadowsocks")
	if !ok {
		return true
	}
	if err := service.TouchLastCheck(r.Context(), server.NodeType, server.ID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": err.Error()})
		return true
	}

	users, err := service.AvailableUsers(r.Context(), server.GroupIDs)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": err.Error()})
		return true
	}

	payload := map[string]any{"data": buildShadowsocksUsers(server, users)}
	return writeJSONWithETag(w, r, payload)
}

func handleServerTrojanUser(w http.ResponseWriter, r *http.Request, cfg config.Config, service nodeapi.Service) bool {
	server, ok := lookupNodeServer(w, r, cfg, service, "trojan")
	if !ok {
		return true
	}
	if err := service.TouchLastCheck(r.Context(), server.NodeType, server.ID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": err.Error()})
		return true
	}

	users, err := service.AvailableUsers(r.Context(), server.GroupIDs)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": err.Error()})
		return true
	}

	payload := map[string]any{"msg": "ok", "data": buildTrojanUsers(users)}
	return writeJSONWithETag(w, r, payload)
}

func handleServerDeepbworkConfig(w http.ResponseWriter, r *http.Request, cfg config.Config, service nodeapi.Service) bool {
	server, ok := lookupNodeServer(w, r, cfg, service, "vmess")
	if !ok {
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	localPort, err := strconv.ParseInt(strings.TrimSpace(inputs["local_port"]), 10, 64)
	if err != nil || localPort <= 0 {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "参数错误"})
		return true
	}

	payload, err := buildDeepbworkConfig(cfg, server, localPort)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": err.Error()})
		return true
	}
	return writeJSONWithETag(w, r, payload)
}

func handleServerTrojanConfig(w http.ResponseWriter, r *http.Request, cfg config.Config, service nodeapi.Service) bool {
	server, ok := lookupNodeServer(w, r, cfg, service, "trojan")
	if !ok {
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	localPort, err := strconv.ParseInt(strings.TrimSpace(inputs["local_port"]), 10, 64)
	if err != nil || localPort <= 0 {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "参数错误"})
		return true
	}

	payload, err := buildTrojanConfig(server, localPort)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": err.Error()})
		return true
	}
	return writeJSONWithETag(w, r, payload)
}

func lookupNodeServer(w http.ResponseWriter, r *http.Request, cfg config.Config, service nodeapi.Service, defaultType string) (nodeapi.ServerRecord, bool) {
	if service == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "node service unavailable"})
		return nodeapi.ServerRecord{}, false
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return nodeapi.ServerRecord{}, false
	}
	if err := validateServerPushToken(cfg, inputs); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": err.Error()})
		return nodeapi.ServerRecord{}, false
	}

	nodeType := strings.TrimSpace(defaultType)
	if nodeType == "" {
		nodeType = strings.TrimSpace(inputs["node_type"])
	}
	nodeType = nodeapi.NormalizeNodeType(nodeType)

	nodeID, err := strconv.ParseInt(strings.TrimSpace(inputs["node_id"]), 10, 64)
	if err != nil || nodeID <= 0 {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "node_id is invalid"})
		return nodeapi.ServerRecord{}, false
	}

	server, err := service.LookupServer(r.Context(), nodeapi.ServerLookupRequest{
		NodeID:   nodeID,
		NodeType: nodeType,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": err.Error()})
		return nodeapi.ServerRecord{}, false
	}
	return server, true
}

func buildUniProxyConfig(cfg config.Config, server nodeapi.ServerRecord, routes []map[string]any) map[string]any {
	payload := map[string]any{}
	switch server.NodeType {
	case "shadowsocks":
		payload["server_port"] = fieldInt64(server, "server_port")
		payload["cipher"] = fieldString(server, "cipher")
		payload["obfs"] = fieldString(server, "obfs")
		payload["obfs_settings"] = fieldMap(server, "obfs_settings")
		cipher := fieldString(server, "cipher")
		switch cipher {
		case "2022-blake3-aes-128-gcm":
			payload["server_key"] = nodeapi.ServerKey(fieldInt64(server, "created_at"), 16)
		case "2022-blake3-aes-256-gcm":
			payload["server_key"] = nodeapi.ServerKey(fieldInt64(server, "created_at"), 32)
		}
	case "vmess":
		payload["server_port"] = fieldInt64(server, "server_port")
		payload["network"] = fieldString(server, "network")
		payload["networkSettings"] = fieldMapFirst(server, "networkSettings", "network_settings")
		payload["tls"] = fieldInt64(server, "tls")
	case "vless":
		payload["server_port"] = fieldInt64(server, "server_port")
		payload["network"] = fieldString(server, "network")
		payload["networkSettings"] = fieldMap(server, "network_settings")
		payload["tls"] = fieldInt64(server, "tls")
		payload["flow"] = fieldString(server, "flow")
		payload["tls_settings"] = fieldMap(server, "tls_settings")
		payload["encryption"] = fieldString(server, "encryption")
		payload["encryption_settings"] = fieldMap(server, "encryption_settings")
	case "trojan":
		payload["host"] = fieldString(server, "host")
		payload["network"] = fieldString(server, "network")
		payload["networkSettings"] = fieldMap(server, "network_settings")
		payload["server_port"] = fieldInt64(server, "server_port")
		payload["server_name"] = fieldString(server, "server_name")
	case "tuic":
		payload["server_port"] = fieldInt64(server, "server_port")
		payload["server_name"] = fieldString(server, "server_name")
		payload["congestion_control"] = fieldString(server, "congestion_control")
		payload["zero_rtt_handshake"] = fieldBool(server, "zero_rtt_handshake")
	case "hysteria":
		version := fieldInt64(server, "version")
		payload["version"] = version
		payload["host"] = fieldString(server, "host")
		payload["server_port"] = fieldInt64(server, "server_port")
		payload["server_name"] = fieldString(server, "server_name")
		payload["up_mbps"] = fieldInt64(server, "up_mbps")
		payload["down_mbps"] = fieldInt64(server, "down_mbps")
		if version == 1 {
			payload["obfs"] = nonEmptyStringOrNil(fieldString(server, "obfs_password"))
		} else if version == 2 {
			payload["ignore_client_bandwidth"] = fieldInt64(server, "up_mbps") == 0 && fieldInt64(server, "down_mbps") == 0
			payload["obfs"] = nonEmptyStringOrNil(fieldString(server, "obfs"))
			payload["obfs-password"] = nonEmptyStringOrNil(fieldString(server, "obfs_password"))
		}
	case "anytls":
		payload["server_port"] = fieldInt64(server, "server_port")
		payload["server_name"] = fieldString(server, "server_name")
		payload["padding_scheme"] = server.Fields["padding_scheme"]
	}

	pushInterval := cfg.ServerPushInterval
	if pushInterval <= 0 {
		pushInterval = 60
	}
	pullInterval := cfg.ServerPullInterval
	if pullInterval <= 0 {
		pullInterval = 60
	}
	payload["base_config"] = map[string]any{
		"push_interval": pushInterval,
		"pull_interval": pullInterval,
	}
	if len(routes) > 0 {
		payload["routes"] = routes
	}
	return payload
}

func buildDeepbworkUsers(users []nodeapi.AvailableUser) []map[string]any {
	result := make([]map[string]any, 0, len(users))
	for _, item := range users {
		record := map[string]any{
			"id": item.ID,
			"v2ray_user": map[string]any{
				"uuid":     item.UUID,
				"email":    item.UUID + "@forest.local",
				"alter_id": int64(0),
				"level":    int64(0),
			},
		}
		if item.SpeedLimit != nil {
			record["speed_limit"] = *item.SpeedLimit
		}
		if item.DeviceLimit != nil {
			record["device_limit"] = *item.DeviceLimit
		}
		result = append(result, record)
	}
	return result
}

func buildShadowsocksUsers(server nodeapi.ServerRecord, users []nodeapi.AvailableUser) []map[string]any {
	result := make([]map[string]any, 0, len(users))
	serverPort := fieldInt64(server, "server_port")
	cipher := fieldString(server, "cipher")
	for _, item := range users {
		result = append(result, map[string]any{
			"id":     item.ID,
			"port":   serverPort,
			"cipher": cipher,
			"secret": item.UUID,
		})
	}
	return result
}

func buildTrojanUsers(users []nodeapi.AvailableUser) []map[string]any {
	result := make([]map[string]any, 0, len(users))
	for _, item := range users {
		record := map[string]any{
			"id": item.ID,
			"trojan_user": map[string]any{
				"password": item.UUID,
			},
		}
		if item.SpeedLimit != nil {
			record["speed_limit"] = *item.SpeedLimit
		}
		if item.DeviceLimit != nil {
			record["device_limit"] = *item.DeviceLimit
		}
		result = append(result, record)
	}
	return result
}

func buildDeepbworkConfig(cfg config.Config, server nodeapi.ServerRecord, localPort int64) (map[string]any, error) {
	var payload map[string]any
	if err := json.Unmarshal([]byte(deepbworkV2RayConfig), &payload); err != nil {
		return nil, fmt.Errorf("decode deepbwork config template: %w", err)
	}

	logConfig := ensureMap(payload, "log")
	logConfig["loglevel"] = map[bool]string{true: "debug", false: "none"}[cfg.ServerLogEnable]

	inbounds := ensureSlice(payload["inbounds"])
	if len(inbounds) >= 2 {
		proxy := ensureAnyMap(inbounds[0])
		api := ensureAnyMap(inbounds[1])
		api["port"] = localPort
		proxy["port"] = fieldInt64(server, "server_port")
		streamSettings := ensureMap(proxy, "streamSettings")
		network := fieldString(server, "network")
		streamSettings["network"] = network
		applyDeepbworkNetwork(streamSettings, network, fieldMapFirst(server, "networkSettings", "network_settings"))
		applyDeepbworkTLS(streamSettings, fieldInt64(server, "tls"), fieldMapFirst(server, "tlsSettings", "tls_settings"))
		if len(inbounds) > 0 {
			inbounds[0] = proxy
			inbounds[1] = api
		}
		payload["inbounds"] = inbounds
	}

	applyDeepbworkDNS(payload, fieldMapFirst(server, "dnsSettings", "dns_settings"))
	applyDeepbworkRules(cfg, payload, fieldMapFirst(server, "ruleSettings", "rule_settings"))
	return payload, nil
}

func buildTrojanConfig(server nodeapi.ServerRecord, localPort int64) (map[string]any, error) {
	var payload map[string]any
	if err := json.Unmarshal([]byte(trojanTidalabConfig), &payload); err != nil {
		return nil, fmt.Errorf("decode trojan config template: %w", err)
	}
	payload["local_port"] = fieldInt64(server, "server_port")

	sslConfig := ensureMap(payload, "ssl")
	serverName := fieldString(server, "server_name")
	if serverName == "" {
		serverName = fieldString(server, "host")
	}
	sslConfig["sni"] = serverName
	sslConfig["cert"] = "/root/.cert/server.crt"
	sslConfig["key"] = "/root/.cert/server.key"

	apiConfig := ensureMap(payload, "api")
	apiConfig["api_port"] = localPort
	return payload, nil
}

func applyDeepbworkDNS(payload map[string]any, dnsSettings map[string]any) {
	if len(dnsSettings) == 0 {
		return
	}
	dnsCopy := cloneMap(dnsSettings)
	if servers, ok := dnsCopy["servers"]; ok {
		list := append(ensureSlice(servers), "1.1.1.1", "localhost")
		dnsCopy["servers"] = list
	}
	payload["dns"] = dnsCopy
	outbounds := ensureSlice(payload["outbounds"])
	if len(outbounds) > 0 {
		first := ensureAnyMap(outbounds[0])
		settings := ensureMap(first, "settings")
		settings["domainStrategy"] = "UseIP"
		outbounds[0] = first
		payload["outbounds"] = outbounds
	}
}

func applyDeepbworkNetwork(streamSettings map[string]any, network string, networkSettings map[string]any) {
	if len(networkSettings) == 0 {
		return
	}
	switch strings.TrimSpace(network) {
	case "tcp":
		streamSettings["tcpSettings"] = networkSettings
	case "kcp":
		streamSettings["kcpSettings"] = networkSettings
	case "ws":
		streamSettings["wsSettings"] = networkSettings
	case "http":
		streamSettings["httpSettings"] = networkSettings
	case "domainsocket":
		streamSettings["dsSettings"] = networkSettings
	case "quic":
		streamSettings["quicSettings"] = networkSettings
	case "grpc":
		streamSettings["grpcSettings"] = networkSettings
	}
}

func applyDeepbworkRules(cfg config.Config, payload map[string]any, ruleSettings map[string]any) {
	domainRules := splitConfigLines(cfg.ServerV2RayDomain)
	protocolRules := splitConfigLines(cfg.ServerV2RayProtocol)

	if len(ruleSettings) > 0 {
		if values, ok := ruleSettings["domain"]; ok {
			domainRules = append(domainRules, stringList(values)...)
		}
		if values, ok := ruleSettings["protocol"]; ok {
			protocolRules = append(protocolRules, stringList(values)...)
		}
	}

	inbounds := ensureSlice(payload["inbounds"])
	if len(domainRules) == 0 && len(protocolRules) == 0 {
		if len(inbounds) > 0 {
			first := ensureAnyMap(inbounds[0])
			sniffing := ensureMap(first, "sniffing")
			sniffing["enabled"] = false
			inbounds[0] = first
			payload["inbounds"] = inbounds
		}
		return
	}

	routing := ensureMap(payload, "routing")
	rules := ensureSlice(routing["rules"])
	if len(domainRules) > 0 {
		rules = append(rules, map[string]any{
			"type":        "field",
			"domain":      domainRules,
			"outboundTag": "block",
		})
	}
	if len(protocolRules) > 0 {
		rules = append(rules, map[string]any{
			"type":        "field",
			"protocol":    protocolRules,
			"outboundTag": "block",
		})
	}
	routing["rules"] = rules
}

func applyDeepbworkTLS(streamSettings map[string]any, tls int64, tlsSettings map[string]any) {
	if tls == 0 {
		return
	}
	streamSettings["security"] = "tls"
	payload := map[string]any{
		"certificates": []any{
			map[string]any{
				"certificateFile": "/root/.cert/server.crt",
				"keyFile":         "/root/.cert/server.key",
			},
		},
	}
	if serverName := nonEmptyStringOrNil(fieldMapValue(tlsSettings, "serverName")); serverName != nil {
		payload["serverName"] = serverName
	}
	if allowInsecure, ok := mapValueBool(tlsSettings["allowInsecure"]); ok {
		payload["allowInsecure"] = allowInsecure
	}
	streamSettings["tlsSettings"] = payload
}

func decodeAlivePayload(r *http.Request) (map[int64][]string, error) {
	rawBody, err := readRequestBody(r)
	if err != nil {
		return nil, err
	}

	var payload any
	if trimmed := strings.TrimSpace(string(rawBody)); trimmed != "" {
		if err := json.Unmarshal(rawBody, &payload); err != nil {
			inputs, inputErr := readInputs(r)
			if inputErr != nil {
				return nil, fmt.Errorf("decode alive payload: %w", err)
			}
			data := strings.TrimSpace(inputs["data"])
			if data == "" {
				return nil, fmt.Errorf("decode alive payload: %w", err)
			}
			if err := json.Unmarshal([]byte(data), &payload); err != nil {
				return nil, fmt.Errorf("decode alive payload: %w", err)
			}
		}
	}

	result := map[int64][]string{}
	entries, ok := payload.(map[string]any)
	if !ok {
		return result, nil
	}
	for rawUserID, value := range entries {
		userID, err := strconv.ParseInt(strings.TrimSpace(rawUserID), 10, 64)
		if err != nil || userID <= 0 {
			continue
		}
		result[userID] = stringList(value)
	}
	return result, nil
}

func wantsMsgpack(r *http.Request) bool {
	return strings.Contains(strings.ToLower(strings.TrimSpace(r.Header.Get("X-Response-Format"))), "msgpack")
}

func writeJSONWithETag(w http.ResponseWriter, r *http.Request, payload any) bool {
	raw, err := json.Marshal(payload)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": err.Error()})
		return true
	}
	return writeETagBody(w, r, "application/json", raw)
}

func writeMsgpackWithETag(w http.ResponseWriter, r *http.Request, payload any) bool {
	raw, err := msgpack.Marshal(payload)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": err.Error()})
		return true
	}
	return writeETagBody(w, r, "application/x-msgpack", raw)
}

func writeETagBody(w http.ResponseWriter, r *http.Request, contentType string, raw []byte) bool {
	sum := sha1.Sum(raw)
	etag := fmt.Sprintf("%x", sum[:])
	if strings.Contains(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return true
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("ETag", `"`+etag+`"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
	return true
}

func fieldInt64(server nodeapi.ServerRecord, key string) int64 {
	return mapValueInt64(server.Fields[key])
}

func fieldString(server nodeapi.ServerRecord, key string) string {
	value, ok := server.Fields[key]
	if !ok || value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func fieldBool(server nodeapi.ServerRecord, key string) bool {
	value, _ := mapValueBool(server.Fields[key])
	return value
}

func fieldMap(server nodeapi.ServerRecord, key string) map[string]any {
	value, ok := server.Fields[key].(map[string]any)
	if !ok {
		return map[string]any{}
	}
	return cloneMap(value)
}

func fieldMapFirst(server nodeapi.ServerRecord, keys ...string) map[string]any {
	for _, key := range keys {
		if value := fieldMap(server, key); len(value) > 0 {
			return value
		}
	}
	return map[string]any{}
}

func fieldMapValue(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	value, ok := values[key]
	if !ok || value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func mapValueInt64(value any) int64 {
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

func mapValueBool(value any) (bool, bool) {
	switch typed := value.(type) {
	case bool:
		return typed, true
	case int64:
		return typed != 0, true
	case int:
		return typed != 0, true
	case float64:
		return typed != 0, true
	case json.Number:
		next, err := typed.Int64()
		if err == nil {
			return next != 0, true
		}
	case string:
		trimmed := strings.ToLower(strings.TrimSpace(typed))
		switch trimmed {
		case "1", "true", "yes", "on":
			return true, true
		case "0", "false", "no", "off":
			return false, true
		}
	}
	return false, false
}

func ensureMap(target map[string]any, key string) map[string]any {
	if target == nil {
		return map[string]any{}
	}
	current, ok := target[key].(map[string]any)
	if !ok {
		current = map[string]any{}
		target[key] = current
	}
	return current
}

func ensureSlice(value any) []any {
	items, ok := value.([]any)
	if !ok {
		return []any{}
	}
	return items
}

func ensureAnyMap(value any) map[string]any {
	current, ok := value.(map[string]any)
	if !ok {
		return map[string]any{}
	}
	return current
}

func cloneMap(value map[string]any) map[string]any {
	if len(value) == 0 {
		return map[string]any{}
	}
	cloned := make(map[string]any, len(value))
	for key, item := range value {
		cloned[key] = item
	}
	return cloned
}

func mapKeys(value map[string]any) []string {
	if len(value) == 0 {
		return nil
	}
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	return keys
}

func stripPrivateKey(value map[string]any) map[string]any {
	if len(value) == 0 {
		return value
	}
	delete(value, "private_key")
	return value
}

func splitConfigLines(raw string) []string {
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	result := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			result = append(result, line)
		}
	}
	return result
}

func stringList(value any) []string {
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
		text := strings.TrimSpace(fmt.Sprint(value))
		if text == "" {
			return []string{}
		}
		return []string{text}
	}
}

func nonEmptyStringOrNil(value string) any {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return value
}
