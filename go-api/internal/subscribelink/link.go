package subscribelink

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"unicode"
)

const (
	MarkerField     = "client_entry_extra_node"
	RawURIField     = "client_entry_extra_uri"
	CredentialField = "client_entry_extra_credential"
	UUIDField       = "client_entry_extra_uuid"
	PasswordField   = "client_entry_extra_password"

	maxExtraNodes = 200
	maxURILength  = 8192
)

func NormalizeList(values []string) ([]string, error) {
	if len(values) > maxExtraNodes {
		return nil, fmt.Errorf("额外节点不能超过 %d 个", maxExtraNodes)
	}
	result := make([]string, 0, len(values))
	for index, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, err := Parse(value); err != nil {
			return nil, fmt.Errorf("第 %d 个额外节点无效：%w", index+1, err)
		}
		result = append(result, value)
	}
	return result, nil
}

func EncodeList(values []string) (string, error) {
	values, err := NormalizeList(values)
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(values)
	if err != nil {
		return "", errors.New("额外节点格式无效")
	}
	return string(raw), nil
}

func DecodeList(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return []string{}, nil
	}
	var values []string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, errors.New("额外节点格式无效")
	}
	return NormalizeList(values)
}

func Parse(raw string) (map[string]any, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("分享链接不能为空")
	}
	if len(raw) > maxURILength {
		return nil, errors.New("分享链接过长")
	}
	for _, char := range raw {
		if unicode.IsControl(char) || unicode.IsSpace(char) {
			return nil, errors.New("分享链接不能包含空白或控制字符")
		}
	}
	lower := strings.ToLower(raw)
	if strings.HasPrefix(lower, "vmess://") {
		return parseVMess(raw)
	}
	if strings.HasPrefix(lower, "ss://") {
		return parseShadowsocks(raw)
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("无法解析分享链接")
	}
	scheme := strings.ToLower(strings.TrimSpace(parsed.Scheme))
	switch scheme {
	case "trojan", "vless", "hysteria2", "hy2", "anytls", "tuic":
		return parseURLNode(raw, parsed, scheme)
	default:
		return nil, fmt.Errorf("暂不支持 %q 协议", scheme)
	}
}

func IsExtra(server map[string]any) bool {
	if server == nil {
		return false
	}
	switch value := server[MarkerField].(type) {
	case bool:
		return value
	case int:
		return value != 0
	case int64:
		return value != 0
	case float64:
		return value != 0
	case string:
		value = strings.TrimSpace(strings.ToLower(value))
		return value == "1" || value == "true" || value == "yes"
	default:
		return false
	}
}

func RawURI(server map[string]any) string {
	if !IsExtra(server) {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(server[RawURIField]))
}

func Credential(fallback string, server map[string]any) string {
	if IsExtra(server) {
		if value := cleanMapString(server, CredentialField); value != "" {
			return value
		}
		if value := cleanMapString(server, PasswordField); value != "" {
			return value
		}
		if value := cleanMapString(server, UUIDField); value != "" {
			return value
		}
	}
	return fallback
}

func UUID(fallback string, server map[string]any) string {
	if IsExtra(server) {
		if value := cleanMapString(server, UUIDField); value != "" {
			return value
		}
	}
	return Credential(fallback, server)
}

func Password(fallback string, server map[string]any) string {
	if IsExtra(server) {
		if value := cleanMapString(server, PasswordField); value != "" {
			return value
		}
	}
	return Credential(fallback, server)
}

func parseURLNode(raw string, parsed *url.URL, scheme string) (map[string]any, error) {
	if parsed.User == nil {
		return nil, errors.New("分享链接缺少认证信息")
	}
	host := strings.TrimSpace(parsed.Hostname())
	if host == "" {
		return nil, errors.New("分享链接缺少节点地址")
	}
	port, err := parsePort(parsed.Port())
	if err != nil {
		return nil, err
	}
	query := parsed.Query()
	name := strings.TrimSpace(parsed.Fragment)
	if name == "" {
		name = host + ":" + strconv.FormatInt(port, 10)
	}
	username := parsed.User.Username()
	userinfoPassword, hasUserinfoPassword := parsed.User.Password()
	if username == "" {
		return nil, errors.New("分享链接缺少认证信息")
	}

	node := baseNode(raw, name, host, port)
	network := strings.ToLower(firstQuery(query, "type", "network"))
	if network == "tcp" {
		network = ""
	}
	if network != "" {
		node["network"] = network
	}
	if settings := parseNetworkSettings(network, query); len(settings) > 0 {
		node["network_settings"] = settings
	}

	insecure := queryBool(query, "allowInsecure", "allow_insecure", "insecure")
	serverName := firstQuery(query, "sni", "peer", "serverName", "server_name")
	tlsSettings := map[string]any{
		"allow_insecure": insecure,
	}
	if serverName != "" {
		tlsSettings["server_name"] = serverName
	}
	if fingerprint := firstQuery(query, "fp", "fingerprint"); fingerprint != "" {
		tlsSettings["fingerprint"] = fingerprint
	}
	if alpn := splitCommaList(firstQuery(query, "alpn")); len(alpn) > 0 {
		tlsSettings["alpn"] = alpn
	}
	if ech := firstQuery(query, "ech"); ech != "" {
		tlsSettings["ech_config"] = ech
	}

	switch scheme {
	case "trojan":
		node["type"] = "trojan"
		node["allow_insecure"] = boolInt(insecure)
		node["server_name"] = serverName
		node["tls_settings"] = tlsSettings
		setSingleCredential(node, username)
	case "vless":
		node["type"] = "vless"
		security := strings.ToLower(firstQuery(query, "security"))
		switch security {
		case "reality":
			node["tls"] = int64(2)
		case "tls":
			node["tls"] = int64(1)
		default:
			node["tls"] = int64(0)
		}
		if publicKey := firstQuery(query, "pbk", "publicKey", "public_key"); publicKey != "" {
			tlsSettings["public_key"] = publicKey
		}
		if shortID := firstQuery(query, "sid", "shortId", "short_id"); shortID != "" {
			tlsSettings["short_id"] = shortID
		}
		node["tls_settings"] = tlsSettings
		node["flow"] = firstQuery(query, "flow")
		node["encryption"] = firstNonEmpty(firstQuery(query, "encryption"), "none")
		setSingleCredential(node, username)
	case "hysteria2", "hy2":
		node["type"] = "hysteria2"
		node["version"] = int64(2)
		node["server_name"] = serverName
		node["insecure"] = boolInt(insecure)
		node["tls_settings"] = tlsSettings
		if mport := firstQuery(query, "mport", "ports"); mport != "" {
			node["mport"] = mport
		}
		if obfs := firstQuery(query, "obfs"); obfs != "" {
			node["obfs"] = obfs
			node["obfs_password"] = firstQuery(query, "obfs-password", "obfs_password")
		}
		setSingleCredential(node, username)
	case "anytls":
		node["type"] = "anytls"
		node["server_name"] = serverName
		node["insecure"] = boolInt(insecure)
		node["tls_settings"] = tlsSettings
		setSingleCredential(node, username)
	case "tuic":
		node["type"] = "tuic"
		node["server_name"] = serverName
		node["insecure"] = boolInt(insecure)
		node["tls_settings"] = tlsSettings
		node["congestion_control"] = firstQuery(query, "congestion_control", "congestion-controller")
		node["udp_relay_mode"] = firstQuery(query, "udp_relay_mode", "udp-relay-mode")
		node["zero_rtt_handshake"] = boolInt(queryBool(query, "reduce_rtt", "zero_rtt_handshake"))
		password := username
		if hasUserinfoPassword && userinfoPassword != "" {
			password = userinfoPassword
		}
		node[UUIDField] = username
		node[PasswordField] = password
		node[CredentialField] = username
	}
	return node, nil
}

func parseVMess(raw string) (map[string]any, error) {
	payload := strings.TrimSpace(raw[len("vmess://"):])
	decoded, err := decodeFlexibleBase64(payload)
	if err != nil {
		return nil, errors.New("VMess 分享链接编码无效")
	}
	var config map[string]any
	if err := json.Unmarshal(decoded, &config); err != nil {
		return nil, errors.New("VMess 分享链接内容无效")
	}
	host := cleanAnyString(config["add"])
	credential := cleanAnyString(config["id"])
	port, err := parsePort(cleanAnyString(config["port"]))
	if err != nil || host == "" || credential == "" {
		return nil, errors.New("VMess 分享链接缺少地址、端口或 UUID")
	}
	name := cleanAnyString(config["ps"])
	if name == "" {
		name = host + ":" + strconv.FormatInt(port, 10)
	}
	node := baseNode(raw, name, host, port)
	node["type"] = "vmess"
	network := strings.ToLower(cleanAnyString(config["net"]))
	if network != "" {
		node["network"] = network
	}
	settings := map[string]any{}
	switch network {
	case "ws":
		settings["path"] = cleanAnyString(config["path"])
		if headerHost := cleanAnyString(config["host"]); headerHost != "" {
			settings["headers"] = map[string]any{"Host": headerHost}
		}
	case "grpc":
		settings["serviceName"] = cleanAnyString(config["path"])
	case "tcp":
		if headerType := cleanAnyString(config["type"]); headerType != "" && headerType != "none" {
			settings["header"] = map[string]any{"type": headerType}
		}
	}
	if len(settings) > 0 {
		node["network_settings"] = settings
	}
	tlsMode := strings.ToLower(cleanAnyString(config["tls"]))
	if tlsMode != "" && tlsMode != "none" {
		node["tls"] = int64(1)
	}
	tlsSettings := map[string]any{}
	if serverName := firstNonEmpty(cleanAnyString(config["sni"]), cleanAnyString(config["host"])); serverName != "" {
		tlsSettings["server_name"] = serverName
	}
	if fingerprint := cleanAnyString(config["fp"]); fingerprint != "" {
		tlsSettings["fingerprint"] = fingerprint
	}
	if alpn := splitCommaList(cleanAnyString(config["alpn"])); len(alpn) > 0 {
		tlsSettings["alpn"] = alpn
	}
	if len(tlsSettings) > 0 {
		node["tls_settings"] = tlsSettings
	}
	setSingleCredential(node, credential)
	return node, nil
}

func parseShadowsocks(raw string) (map[string]any, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("Shadowsocks 分享链接格式无效")
	}

	if parsed.Hostname() == "" || parsed.User == nil {
		payload := strings.TrimPrefix(raw, "ss://")
		fragment := ""
		if index := strings.IndexByte(payload, '#'); index >= 0 {
			fragment = payload[index:]
			payload = payload[:index]
		}
		query := ""
		if index := strings.IndexByte(payload, '?'); index >= 0 {
			query = payload[index:]
			payload = payload[:index]
		}
		decoded, decodeErr := decodeFlexibleBase64(payload)
		if decodeErr != nil {
			return nil, errors.New("Shadowsocks 分享链接编码无效")
		}
		return parseShadowsocks("ss://" + string(decoded) + query + fragment)
	}

	host := strings.TrimSpace(parsed.Hostname())
	port, err := parsePort(parsed.Port())
	if err != nil || host == "" {
		return nil, errors.New("Shadowsocks 分享链接缺少地址或端口")
	}
	method := parsed.User.Username()
	password, hasPassword := parsed.User.Password()
	if !hasPassword {
		decoded, decodeErr := decodeFlexibleBase64(method)
		if decodeErr != nil {
			return nil, errors.New("Shadowsocks 分享链接认证信息无效")
		}
		parts := strings.SplitN(string(decoded), ":", 2)
		if len(parts) != 2 {
			return nil, errors.New("Shadowsocks 分享链接认证信息无效")
		}
		method, password = parts[0], parts[1]
	}
	method = strings.TrimSpace(method)
	if !supportedShadowsocksCipher(method) || password == "" {
		return nil, errors.New("Shadowsocks 分享链接使用了不支持的加密方式或空密码")
	}
	name := strings.TrimSpace(parsed.Fragment)
	if name == "" {
		name = host + ":" + strconv.FormatInt(port, 10)
	}
	node := baseNode(raw, name, host, port)
	node["type"] = "shadowsocks"
	node["cipher"] = method
	setSingleCredential(node, password)

	plugin := parsed.Query().Get("plugin")
	if plugin != "" {
		parts := strings.Split(plugin, ";")
		for _, part := range parts {
			keyValue := strings.SplitN(part, "=", 2)
			if len(keyValue) != 2 {
				continue
			}
			switch strings.ToLower(strings.TrimSpace(keyValue[0])) {
			case "obfs":
				node["obfs"] = strings.TrimSpace(keyValue[1])
			case "obfs-host":
				node["obfs-host"] = strings.TrimSpace(keyValue[1])
			case "obfs-uri", "obfs-path":
				node["obfs-path"] = strings.TrimSpace(keyValue[1])
			}
		}
	}
	return node, nil
}

func baseNode(raw, name, host string, port int64) map[string]any {
	sum := sha256.Sum256([]byte(raw))
	return map[string]any{
		"name":          name,
		"host":          host,
		"port":          port,
		MarkerField:     int64(1),
		RawURIField:     raw,
		"show":          int64(1),
		"is_online":     int64(1),
		"last_check_at": int64(0),
		"cache_key":     fmt.Sprintf("client-entry-extra-%x", sum[:12]),
	}
}

func setSingleCredential(node map[string]any, value string) {
	node[CredentialField] = value
	node[UUIDField] = value
	node[PasswordField] = value
}

func parseNetworkSettings(network string, query url.Values) map[string]any {
	settings := map[string]any{}
	switch network {
	case "ws":
		if path := firstQuery(query, "path"); path != "" {
			settings["path"] = path
		}
		if host := firstQuery(query, "host"); host != "" {
			settings["headers"] = map[string]any{"Host": host}
		}
	case "grpc":
		if serviceName := firstQuery(query, "serviceName", "service_name"); serviceName != "" {
			settings["serviceName"] = serviceName
		}
	case "httpupgrade", "xhttp":
		settings["path"] = firstQuery(query, "path")
		settings["host"] = firstQuery(query, "host")
		if network == "xhttp" {
			settings["mode"] = firstQuery(query, "mode")
		}
	}
	for key, value := range settings {
		if cleanAnyString(value) == "" {
			delete(settings, key)
		}
	}
	return settings
}

func parsePort(raw string) (int64, error) {
	port, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || port < 1 || port > 65535 {
		return 0, errors.New("分享链接端口无效")
	}
	return port, nil
}

func firstQuery(values url.Values, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(values.Get(key)); value != "" {
			return value
		}
		for actual, candidates := range values {
			if !strings.EqualFold(actual, key) || len(candidates) == 0 {
				continue
			}
			if value := strings.TrimSpace(candidates[0]); value != "" {
				return value
			}
		}
	}
	return ""
}

func queryBool(values url.Values, keys ...string) bool {
	value := strings.ToLower(firstQuery(values, keys...))
	switch value {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func boolInt(value bool) int64 {
	if value {
		return 1
	}
	return 0
}

func cleanMapString(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	return cleanAnyString(values[key])
}

func cleanAnyString(value any) string {
	if value == nil {
		return ""
	}
	text := strings.TrimSpace(fmt.Sprint(value))
	if text == "<nil>" || strings.EqualFold(text, "null") {
		return ""
	}
	return text
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func splitCommaList(raw string) []string {
	parts := strings.Split(raw, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			result = append(result, part)
		}
	}
	return result
}

func decodeFlexibleBase64(raw string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	for _, encoding := range []*base64.Encoding{
		base64.RawURLEncoding,
		base64.URLEncoding,
		base64.RawStdEncoding,
		base64.StdEncoding,
	} {
		if decoded, err := encoding.DecodeString(raw); err == nil {
			return decoded, nil
		}
	}
	return nil, errors.New("invalid base64")
}

func supportedShadowsocksCipher(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "aes-128-gcm", "aes-192-gcm", "aes-256-gcm", "chacha20-ietf-poly1305":
		return true
	default:
		return false
	}
}
