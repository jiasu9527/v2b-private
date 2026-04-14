package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"forest/go-api/internal/config"
	"forest/go-api/internal/nodeapi"
	usersvc "forest/go-api/internal/user"

	"gopkg.in/yaml.v3"
)

func buildGeneralSubscribePayload(userUUID string, servers []map[string]any) string {
	var builder strings.Builder
	for _, server := range servers {
		builder.WriteString(buildSubscribeURI(userUUID, server))
	}
	return base64.StdEncoding.EncodeToString([]byte(builder.String()))
}

func buildShadowrocketPayload(subscribe usersvc.Subscribe, servers []map[string]any) string {
	var builder strings.Builder
	builder.WriteString(buildShadowrocketStatusLine(subscribe))
	for _, server := range servers {
		serverType, normalized := normalizeSubscribeServer(server)
		if serverType == "vmess" {
			builder.WriteString(buildShadowrocketVmessURI(subscribe.UUID, normalized))
			continue
		}
		builder.WriteString(buildSubscribeURI(subscribe.UUID, normalized))
	}
	return base64.StdEncoding.EncodeToString([]byte(builder.String()))
}

func buildShadowrocketStatusLine(subscribe usersvc.Subscribe) string {
	expiredDate := "长期有效"
	if subscribe.ExpiredAt != nil && *subscribe.ExpiredAt > 0 {
		expiredDate = formatSubscribeDate(*subscribe.ExpiredAt)
	}

	return fmt.Sprintf(
		"STATUS=🚀↑:%sGB,↓:%sGB,TOT:%sGB💡Expires:%s\r\n",
		formatShadowrocketTrafficGB(subscribe.U),
		formatShadowrocketTrafficGB(subscribe.D),
		formatShadowrocketTrafficGB(subscribe.TransferEnable),
		expiredDate,
	)
}

func buildShadowrocketVmessURI(userUUID string, server map[string]any) string {
	userinfo := base64.StdEncoding.EncodeToString([]byte(
		"auto:" + userUUID + "@" + formatHost(serverString(server, "host")) + ":" + strconv.FormatInt(serverInt64(server, "port"), 10),
	))

	params := url.Values{}
	params.Set("tfo", "1")
	params.Set("remark", serverString(server, "name"))
	params.Set("alterId", "0")

	if serverBool(server, "tls") {
		params.Set("tls", "1")
		tlsSettings := firstNonEmptyMap(serverMap(server, "tls_settings"), serverMap(server, "tlsSettings"))
		if serverMapBool(tlsSettings, "allow_insecure", "allowInsecure") {
			params.Set("allowInsecure", "1")
		}
		if peer := firstNonEmptyString(
			stringValue(tlsSettings["server_name"]),
			stringValue(tlsSettings["serverName"]),
		); peer != "" {
			params.Set("peer", peer)
		}
	}

	network := serverString(server, "network")
	settings := firstNonEmptyMap(serverMap(server, "network_settings"), serverMap(server, "networkSettings"))
	switch network {
	case "tcp":
		header := nestedMap(settings, "header")
		if obfs := stringValue(header["type"]); obfs != "" {
			params.Set("obfs", obfs)
		}
		request := nestedMap(header, "request")
		if paths := nestedAnySlice(request, "path"); len(paths) > 0 {
			params.Set("path", stringValue(paths[0]))
		}
		if hosts := nestedAnySlice(nestedMap(request, "headers"), "Host"); len(hosts) > 0 {
			params.Set("obfsParam", stringValue(hosts[0]))
		}
	case "ws":
		params.Set("obfs", "websocket")
		if path := stringValue(settings["path"]); path != "" {
			params.Set("path", path)
		}
		if host := stringValue(nestedMap(settings, "headers")["Host"]); host != "" {
			params.Set("obfsParam", host)
		}
		if method := stringValue(settings["security"]); method != "" {
			params.Set("method", method)
		}
	case "grpc":
		params.Set("obfs", "grpc")
		if serviceName := stringValue(settings["serviceName"]); serviceName != "" {
			params.Set("path", serviceName)
		}
		if host := firstNonEmptyString(
			stringValue(firstNonEmptyMap(serverMap(server, "tls_settings"), serverMap(server, "tlsSettings"))["server_name"]),
			serverString(server, "host"),
		); host != "" {
			params.Set("host", host)
		}
	}

	return "vmess://" + userinfo + "?" + params.Encode() + "\r\n"
}

func buildClashStandardProfile(cfg config.Config, customFile, defaultFile, userUUID string, servers []map[string]any) (string, error) {
	template, err := loadRuleTemplate(cfg, customFile, defaultFile)
	if err != nil {
		return "", err
	}

	proxies := make([]any, 0)
	names := make([]string, 0)
	for _, server := range servers {
		proxy, ok := buildClashStandardProxy(userUUID, server)
		if !ok {
			continue
		}
		proxies = append(proxies, proxy)
		names = append(names, stringValue(proxy["name"]))
	}

	template["proxies"] = appendAnySlice(asAnySlice(template["proxies"]), proxies...)
	if err := mergeProxyGroups(template, names); err != nil {
		return "", err
	}

	raw, err := yaml.Marshal(template)
	if err != nil {
		return "", fmt.Errorf("marshal clash profile: %w", err)
	}

	appName := strings.TrimSpace(cfg.AppName)
	if appName == "" {
		appName = "Forest"
	}
	return strings.ReplaceAll(string(raw), "$app_name", appName), nil
}

func loadRuleTemplate(cfg config.Config, customFile, defaultFile string) (map[string]any, error) {
	candidates := make([]string, 0, 2)
	if strings.TrimSpace(customFile) != "" {
		candidates = append(candidates, filepath.Join(cfg.PublicDir, "..", "resources", "rules", customFile))
	}
	if strings.TrimSpace(defaultFile) != "" {
		candidates = append(candidates, filepath.Join(cfg.PublicDir, "..", "resources", "rules", defaultFile))
	}
	if _, file, _, ok := runtime.Caller(0); ok {
		repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
		if strings.TrimSpace(customFile) != "" {
			candidates = append(candidates, filepath.Join(repoRoot, "resources", "rules", customFile))
		}
		if strings.TrimSpace(defaultFile) != "" {
			candidates = append(candidates, filepath.Join(repoRoot, "resources", "rules", defaultFile))
		}
	}

	var raw []byte
	var err error
	for _, candidate := range candidates {
		raw, err = os.ReadFile(candidate)
		if err == nil {
			break
		}
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("read rule template: %w", err)
	}

	template := map[string]any{}
	if err := yaml.Unmarshal(raw, &template); err != nil {
		return nil, fmt.Errorf("decode rule template: %w", err)
	}
	return template, nil
}

func mergeProxyGroups(template map[string]any, proxyNames []string) error {
	groups := asAnySlice(template["proxy-groups"])
	for _, groupValue := range groups {
		group, ok := groupValue.(map[string]any)
		if !ok {
			continue
		}
		merged, hasMatcher, err := resolveProxyGroupMembers(group, proxyNames)
		if err != nil {
			return err
		}
		if !hasMatcher {
			for _, name := range proxyNames {
				if sliceContainsString(merged, name) {
					continue
				}
				merged = append(merged, name)
			}
		}
		group["proxies"] = merged
	}
	template["proxy-groups"] = groups
	return nil
}

func resolveProxyGroupMembers(group map[string]any, proxyNames []string) ([]any, bool, error) {
	existing := asAnySlice(group["proxies"])
	resolved := make([]any, 0, len(existing))
	hasMatcher := false
	groupName := firstNonEmptyString(stringValue(group["name"]), "unnamed")

	for _, value := range existing {
		raw := stringValue(value)
		if raw == "" {
			continue
		}
		matcher, ok, err := compileProxyGroupEntryMatcher(raw)
		if err != nil {
			return nil, false, fmt.Errorf("proxy group %q invalid regex %q: %w", groupName, raw, err)
		}
		if !ok {
			if !sliceContainsString(resolved, raw) {
				resolved = append(resolved, raw)
			}
			continue
		}
		hasMatcher = true
		resolved = appendMatchingProxyNames(resolved, proxyNames, matcher)
	}

	if serverBoolValue(group["include-all"]) || stringValue(group["filter"]) != "" || stringValue(group["exclude-filter"]) != "" {
		includeMatcher, err := compileOptionalProxyGroupMatcher(stringValue(group["filter"]))
		if err != nil {
			return nil, false, fmt.Errorf("proxy group %q invalid filter %q: %w", groupName, stringValue(group["filter"]), err)
		}
		excludeMatcher, err := compileOptionalProxyGroupMatcher(stringValue(group["exclude-filter"]))
		if err != nil {
			return nil, false, fmt.Errorf("proxy group %q invalid exclude-filter %q: %w", groupName, stringValue(group["exclude-filter"]), err)
		}
		hasMatcher = true
		for _, name := range proxyNames {
			if includeMatcher != nil && !includeMatcher.MatchString(name) {
				continue
			}
			if excludeMatcher != nil && excludeMatcher.MatchString(name) {
				continue
			}
			if !sliceContainsString(resolved, name) {
				resolved = append(resolved, name)
			}
		}
	}

	return resolved, hasMatcher, nil
}

func appendMatchingProxyNames(dst []any, proxyNames []string, matcher *regexp.Regexp) []any {
	for _, name := range proxyNames {
		if matcher.MatchString(name) && !sliceContainsString(dst, name) {
			dst = append(dst, name)
		}
	}
	return dst
}

func compileOptionalProxyGroupMatcher(raw string) (*regexp.Regexp, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if strings.HasPrefix(raw, "/") && strings.HasSuffix(raw, "/") && len(raw) >= 2 {
		raw = strings.TrimSuffix(strings.TrimPrefix(raw, "/"), "/")
	}
	return regexp.Compile(raw)
}

func compileProxyGroupEntryMatcher(raw string) (*regexp.Regexp, bool, error) {
	raw = strings.TrimSpace(raw)
	if len(raw) < 2 {
		return nil, false, nil
	}
	if strings.HasPrefix(raw, "/") && strings.HasSuffix(raw, "/") {
		pattern := strings.TrimSuffix(strings.TrimPrefix(raw, "/"), "/")
		matcher, err := regexp.Compile(pattern)
		if err != nil {
			return nil, true, err
		}
		return matcher, true, nil
	}
	return nil, false, nil
}

func buildClashStandardProxy(userUUID string, server map[string]any) (map[string]any, bool) {
	serverType, normalized := normalizeSubscribeServer(server)
	switch serverType {
	case "shadowsocks":
		cipher := serverString(normalized, "cipher")
		switch cipher {
		case "aes-128-gcm", "aes-192-gcm", "aes-256-gcm", "chacha20-ietf-poly1305":
			return buildClashShadowsocksProxy(userUUID, normalized), true
		default:
			return nil, false
		}
	case "vmess":
		return buildClashVmessProxy(userUUID, normalized), true
	case "vless":
		return buildClashVlessProxy(userUUID, normalized), true
	case "trojan":
		return buildClashTrojanProxy(userUUID, normalized), true
	case "tuic":
		return buildClashTUICProxy(userUUID, normalized), true
	case "hysteria":
		return buildClashHysteriaProxy(userUUID, normalized), true
	case "hysteria2":
		return buildClashHysteria2Proxy(userUUID, normalized), true
	case "anytls":
		return buildClashAnyTLSProxy(userUUID, normalized), true
	default:
		return nil, false
	}
}

func buildClashShadowsocksProxy(userUUID string, server map[string]any) map[string]any {
	proxy := map[string]any{
		"name":     serverString(server, "name"),
		"type":     "ss",
		"server":   serverString(server, "host"),
		"port":     serverInt64(server, "port"),
		"cipher":   serverString(server, "cipher"),
		"password": userUUID,
		"udp":      true,
	}

	if strings.EqualFold(serverString(server, "obfs"), "http") {
		proxy["plugin"] = "obfs"
		opts := map[string]any{"mode": "http"}
		if host := serverString(server, "obfs-host"); host != "" {
			opts["host"] = host
		}
		proxy["plugin-opts"] = opts
	} else if strings.EqualFold(serverString(server, "network"), "http") {
		networkSettings := serverMap(server, "network_settings")
		if host := stringValue(networkSettings["Host"]); host != "" {
			proxy["plugin"] = "obfs"
			proxy["plugin-opts"] = map[string]any{
				"mode": "http",
				"host": host,
			}
		}
	}

	return proxy
}

func buildClashVmessProxy(userUUID string, server map[string]any) map[string]any {
	proxy := map[string]any{
		"name":    serverString(server, "name"),
		"type":    "vmess",
		"server":  serverString(server, "host"),
		"port":    serverInt64(server, "port"),
		"uuid":    userUUID,
		"alterId": int64(0),
		"cipher":  "auto",
		"udp":     true,
	}

	if serverBool(server, "tls") {
		proxy["tls"] = true
		tlsSettings := firstNonEmptyMap(serverMap(server, "tls_settings"), serverMap(server, "tlsSettings"))
		proxy["skip-cert-verify"] = serverMapBool(tlsSettings, "allow_insecure", "allowInsecure")
		proxy["servername"] = firstNonEmptyString(
			stringValue(tlsSettings["server_name"]),
			stringValue(tlsSettings["serverName"]),
		)
	}

	network := serverString(server, "network")
	networkSettings := firstNonEmptyMap(serverMap(server, "network_settings"), serverMap(server, "networkSettings"))
	switch network {
	case "tcp":
		header := nestedMap(networkSettings, "header")
		if strings.EqualFold(stringValue(header["type"]), "http") {
			proxy["network"] = "http"
			request := nestedMap(header, "request")
			if hosts := nestedAnySlice(nestedMap(request, "headers"), "Host"); len(hosts) > 0 {
				proxy["http-opts"] = map[string]any{
					"headers": map[string]any{
						"Host": hosts,
					},
				}
			}
			if paths := nestedAnySlice(request, "path"); len(paths) > 0 {
				httpOpts := mapFromAny(proxy["http-opts"])
				httpOpts["path"] = paths
				proxy["http-opts"] = httpOpts
			}
		}
	case "ws":
		proxy["network"] = "ws"
		wsOpts := map[string]any{}
		if path := stringValue(networkSettings["path"]); path != "" {
			wsOpts["path"] = path
		}
		if host := stringValue(nestedMap(networkSettings, "headers")["Host"]); host != "" {
			wsOpts["headers"] = map[string]any{"Host": host}
		}
		if len(wsOpts) > 0 {
			proxy["ws-opts"] = wsOpts
		}
		if security := stringValue(networkSettings["security"]); security != "" {
			proxy["cipher"] = security
		}
	case "grpc":
		proxy["network"] = "grpc"
		if serviceName := stringValue(networkSettings["serviceName"]); serviceName != "" {
			proxy["grpc-opts"] = map[string]any{"grpc-service-name": serviceName}
		}
	}

	return proxy
}

func buildClashTrojanProxy(userUUID string, server map[string]any) map[string]any {
	proxy := map[string]any{
		"name":     serverString(server, "name"),
		"type":     "trojan",
		"server":   serverString(server, "host"),
		"port":     serverInt64(server, "port"),
		"password": userUUID,
		"udp":      true,
	}

	network := serverString(server, "network")
	networkSettings := serverMap(server, "network_settings")
	if network == "grpc" || network == "ws" {
		proxy["network"] = network
		if network == "grpc" {
			if serviceName := stringValue(networkSettings["serviceName"]); serviceName != "" {
				proxy["grpc-opts"] = map[string]any{"grpc-service-name": serviceName}
			}
		}
		if network == "ws" {
			wsOpts := map[string]any{}
			if path := stringValue(networkSettings["path"]); path != "" {
				wsOpts["path"] = path
			}
			if host := stringValue(nestedMap(networkSettings, "headers")["Host"]); host != "" {
				wsOpts["headers"] = map[string]any{"Host": host}
			}
			if len(wsOpts) > 0 {
				proxy["ws-opts"] = wsOpts
			}
		}
	}

	tlsSettings := serverMap(server, "tls_settings")
	proxy["sni"] = firstNonEmptyString(serverString(server, "server_name"), stringValue(tlsSettings["server_name"]))
	proxy["skip-cert-verify"] = serverBoolValue(server["allow_insecure"]) || serverMapBool(tlsSettings, "allow_insecure")
	return proxy
}

func buildClashVlessProxy(userUUID string, server map[string]any) map[string]any {
	proxy := map[string]any{
		"name":   serverString(server, "name"),
		"type":   "vless",
		"server": serverString(server, "host"),
		"port":   serverInt64(server, "port"),
		"uuid":   userUUID,
		"udp":    true,
	}

	if flow := serverString(server, "flow"); flow != "" {
		proxy["flow"] = flow
	}

	tlsSettings := firstNonEmptyMap(serverMap(server, "tls_settings"), serverMap(server, "tlsSettings"))
	if clientFingerprint := firstNonEmptyString(stringValue(tlsSettings["fingerprint"]), "chrome"); clientFingerprint != "" {
		proxy["client-fingerprint"] = clientFingerprint
	}

	encryption := buildClashVlessEncryption(server)
	if encryption != "" {
		proxy["encryption"] = encryption
	}

	if serverInt64(server, "tls") != 0 {
		proxy["tls"] = true
		proxy["servername"] = firstNonEmptyString(
			stringValue(tlsSettings["server_name"]),
			stringValue(tlsSettings["serverName"]),
			serverString(server, "host"),
		)
		proxy["skip-cert-verify"] = serverMapBool(tlsSettings, "allow_insecure", "allowInsecure")
		if alpn := clashALPNList(tlsSettings, false); len(alpn) > 0 {
			proxy["alpn"] = alpn
		}
		if serverInt64(server, "tls") == 2 {
			realityOpts := map[string]any{}
			if publicKey := stringValue(tlsSettings["public_key"]); publicKey != "" {
				realityOpts["public-key"] = publicKey
			}
			if shortID := stringValue(tlsSettings["short_id"]); shortID != "" {
				realityOpts["short-id"] = shortID
			}
			if len(realityOpts) > 0 {
				proxy["reality-opts"] = realityOpts
			}
		}
	}

	applyClashStreamOptions(proxy, server, true)
	return proxy
}

func buildClashTUICProxy(userUUID string, server map[string]any) map[string]any {
	tlsSettings := firstNonEmptyMap(serverMap(server, "tls_settings"), serverMap(server, "tlsSettings"))
	proxy := map[string]any{
		"name":     serverString(server, "name"),
		"type":     "tuic",
		"server":   serverString(server, "host"),
		"port":     serverInt64(server, "port"),
		"uuid":     userUUID,
		"password": userUUID,
	}

	if token := serverString(server, "token"); token != "" {
		proxy["token"] = token
	}
	if sni := firstNonEmptyString(serverString(server, "server_name"), stringValue(tlsSettings["server_name"]), serverString(server, "host")); sni != "" {
		proxy["sni"] = sni
	}
	if alpn := clashALPNList(tlsSettings, true); len(alpn) > 0 {
		proxy["alpn"] = alpn
	}
	if congestion := serverString(server, "congestion_control"); congestion != "" {
		proxy["congestion-controller"] = congestion
	}
	if relayMode := firstNonEmptyString(serverString(server, "udp_relay_mode"), "native"); relayMode != "" {
		proxy["udp-relay-mode"] = relayMode
	}
	if serverBool(server, "disable_sni") {
		proxy["disable-sni"] = true
	}
	if serverBoolValue(server["zero_rtt_handshake"]) || serverBool(server, "reduce_rtt") {
		proxy["reduce-rtt"] = true
	}
	if timeout := serverInt64(server, "request_timeout"); timeout > 0 {
		proxy["request-timeout"] = timeout
	}
	if packetSize := serverInt64(server, "max_udp_relay_packet_size"); packetSize > 0 {
		proxy["max-udp-relay-packet-size"] = packetSize
	}
	if maxOpenStreams := serverInt64(server, "max_open_streams"); maxOpenStreams > 0 {
		proxy["max-open-streams"] = maxOpenStreams
	}
	if heartbeat := serverInt64(server, "heartbeat_interval"); heartbeat > 0 {
		proxy["heartbeat-interval"] = heartbeat
	}
	proxy["skip-cert-verify"] = serverBoolValue(server["insecure"]) || serverMapBool(tlsSettings, "allow_insecure", "allowInsecure")
	return proxy
}

func buildClashHysteriaProxy(userUUID string, server map[string]any) map[string]any {
	tlsSettings := firstNonEmptyMap(serverMap(server, "tls_settings"), serverMap(server, "tlsSettings"))
	proxy := map[string]any{
		"name":     serverString(server, "name"),
		"type":     "hysteria",
		"server":   serverString(server, "host"),
		"port":     clashPrimaryPort(server),
		"auth-str": userUUID,
		"protocol": firstNonEmptyString(serverString(server, "protocol"), "udp"),
		"up":       clashMbps(serverInt64(server, "up_mbps")),
		"down":     clashMbps(serverInt64(server, "down_mbps")),
	}
	if ports := clashPortHopping(server); ports != "" {
		proxy["ports"] = ports
	}
	if obfs := serverString(server, "obfs"); obfs != "" {
		proxy["obfs"] = obfs
	}
	if sni := firstNonEmptyString(serverString(server, "server_name"), stringValue(tlsSettings["server_name"]), serverString(server, "host")); sni != "" {
		proxy["sni"] = sni
	}
	if alpn := clashALPNList(tlsSettings, true); len(alpn) > 0 {
		proxy["alpn"] = alpn
	}
	proxy["skip-cert-verify"] = serverBoolValue(server["insecure"]) || serverMapBool(tlsSettings, "allow_insecure", "allowInsecure")
	return proxy
}

func buildClashHysteria2Proxy(userUUID string, server map[string]any) map[string]any {
	tlsSettings := firstNonEmptyMap(serverMap(server, "tls_settings"), serverMap(server, "tlsSettings"))
	proxy := map[string]any{
		"name":     serverString(server, "name"),
		"type":     "hysteria2",
		"server":   serverString(server, "host"),
		"port":     clashPrimaryPort(server),
		"password": userUUID,
		"up":       clashMbps(serverInt64(server, "up_mbps")),
		"down":     clashMbps(serverInt64(server, "down_mbps")),
	}
	if ports := clashPortHopping(server); ports != "" {
		proxy["ports"] = ports
	}
	if hopInterval := serverInt64(server, "hop_interval"); hopInterval > 0 {
		proxy["hop-interval"] = hopInterval
	}
	if obfs := serverString(server, "obfs"); obfs != "" {
		proxy["obfs"] = obfs
	}
	if obfsPassword := serverString(server, "obfs_password"); obfsPassword != "" {
		proxy["obfs-password"] = obfsPassword
	}
	if sni := firstNonEmptyString(serverString(server, "server_name"), stringValue(tlsSettings["server_name"]), serverString(server, "host")); sni != "" {
		proxy["sni"] = sni
	}
	if alpn := clashALPNList(tlsSettings, true); len(alpn) > 0 {
		proxy["alpn"] = alpn
	}
	proxy["skip-cert-verify"] = serverBoolValue(server["insecure"]) || serverMapBool(tlsSettings, "allow_insecure", "allowInsecure")
	return proxy
}

func buildClashAnyTLSProxy(userUUID string, server map[string]any) map[string]any {
	tlsSettings := firstNonEmptyMap(serverMap(server, "tls_settings"), serverMap(server, "tlsSettings"))
	proxy := map[string]any{
		"name":               serverString(server, "name"),
		"type":               "anytls",
		"server":             serverString(server, "host"),
		"port":               serverInt64(server, "port"),
		"password":           userUUID,
		"client-fingerprint": firstNonEmptyString(stringValue(tlsSettings["fingerprint"]), "chrome"),
		"udp":                true,
	}
	if sni := firstNonEmptyString(serverString(server, "server_name"), stringValue(tlsSettings["server_name"]), serverString(server, "host")); sni != "" {
		proxy["sni"] = sni
	}
	if alpn := clashALPNList(tlsSettings, false); len(alpn) > 0 {
		proxy["alpn"] = alpn
	}
	if interval := serverInt64(server, "idle_session_check_interval"); interval > 0 {
		proxy["idle-session-check-interval"] = interval
	}
	if timeout := serverInt64(server, "idle_session_timeout"); timeout > 0 {
		proxy["idle-session-timeout"] = timeout
	}
	if minIdle := serverInt64(server, "min_idle_session"); minIdle > 0 {
		proxy["min-idle-session"] = minIdle
	}
	proxy["skip-cert-verify"] = serverBoolValue(server["insecure"]) || serverMapBool(tlsSettings, "allow_insecure", "allowInsecure")
	return proxy
}

func buildSubscribeURI(userUUID string, server map[string]any) string {
	serverType, normalized := normalizeSubscribeServer(server)
	switch serverType {
	case "shadowsocks":
		return buildShadowsocksURI(userUUID, normalized)
	case "vmess":
		return buildVmessURI(userUUID, normalized)
	case "vless":
		return buildVlessURI(userUUID, normalized)
	case "trojan":
		return buildTrojanURI(userUUID, normalized)
	case "tuic":
		return buildTuicURI(userUUID, normalized)
	case "hysteria":
		return buildHysteriaURI(userUUID, normalized)
	case "hysteria2":
		return buildHysteriaURI(userUUID, normalized)
	case "anytls":
		return buildAnyTLSURI(userUUID, normalized)
	default:
		return ""
	}
}

func normalizeSubscribeServer(server map[string]any) (string, map[string]any) {
	normalized := copyMap(server)
	serverType := canonicalSubscribeServerType(serverString(normalized, "type"))
	if serverType == "v2node" {
		serverType = canonicalSubscribeServerType(serverString(normalized, "protocol"))
		if serverType == "" {
			serverType = inferLegacyV2nodeProtocol(normalized)
		}
		normalized["type"] = serverType
	}
	if serverType == "hysteria" && serverInt64(normalized, "version") == 2 {
		return "hysteria2", normalized
	}
	return serverType, normalized
}

func canonicalSubscribeServerType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "<nil>", "null":
		return ""
	case "v2ray":
		return "vmess"
	case "ss":
		return "shadowsocks"
	case "hy", "hy1":
		return "hysteria"
	case "hy2":
		return "hysteria2"
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func buildClashVlessEncryption(server map[string]any) string {
	if !strings.EqualFold(serverString(server, "encryption"), "mlkem768x25519plus") {
		return ""
	}
	encryptionSettings := serverMap(server, "encryption_settings")
	parts := []string{
		"mlkem768x25519plus",
		firstNonEmptyString(stringValue(encryptionSettings["mode"]), "native"),
		firstNonEmptyString(stringValue(encryptionSettings["rtt"]), "1rtt"),
	}
	if padding := stringValue(encryptionSettings["client_padding"]); padding != "" {
		parts = append(parts, padding)
	}
	if password := stringValue(encryptionSettings["password"]); password != "" {
		parts = append(parts, password)
	}
	return strings.Join(parts, ".")
}

func applyClashStreamOptions(proxy map[string]any, server map[string]any, allowXHTTP bool) {
	network := serverString(server, "network")
	networkSettings := firstNonEmptyMap(serverMap(server, "network_settings"), serverMap(server, "networkSettings"))
	switch network {
	case "tcp":
		header := nestedMap(networkSettings, "header")
		if strings.EqualFold(stringValue(header["type"]), "http") {
			proxy["network"] = "http"
			httpOpts := map[string]any{}
			request := nestedMap(header, "request")
			if paths := nestedAnySlice(request, "path"); len(paths) > 0 {
				httpOpts["path"] = paths
			}
			headers := map[string]any{}
			if hosts := nestedAnySlice(nestedMap(request, "headers"), "Host"); len(hosts) > 0 {
				headers["Host"] = hosts
			}
			if len(headers) > 0 {
				httpOpts["headers"] = headers
			}
			if len(httpOpts) > 0 {
				proxy["http-opts"] = httpOpts
			}
		}
	case "ws":
		proxy["network"] = "ws"
		wsOpts := map[string]any{}
		if path := stringValue(networkSettings["path"]); path != "" {
			wsOpts["path"] = path
		}
		if host := stringValue(nestedMap(networkSettings, "headers")["Host"]); host != "" {
			wsOpts["headers"] = map[string]any{"Host": host}
		}
		if len(wsOpts) > 0 {
			proxy["ws-opts"] = wsOpts
		}
	case "grpc":
		proxy["network"] = "grpc"
		if serviceName := stringValue(networkSettings["serviceName"]); serviceName != "" {
			proxy["grpc-opts"] = map[string]any{"grpc-service-name": serviceName}
		}
	case "httpupgrade":
		proxy["network"] = "ws"
		wsOpts := map[string]any{
			"v2ray-http-upgrade": true,
		}
		if path := stringValue(networkSettings["path"]); path != "" {
			wsOpts["path"] = path
		}
		if host := stringValue(networkSettings["host"]); host != "" {
			wsOpts["headers"] = map[string]any{"Host": host}
		}
		proxy["ws-opts"] = wsOpts
	case "xhttp":
		if allowXHTTP {
			proxy["network"] = "xhttp"
			xhttpOpts := map[string]any{}
			if path := stringValue(networkSettings["path"]); path != "" {
				xhttpOpts["path"] = path
			}
			if host := stringValue(networkSettings["host"]); host != "" {
				xhttpOpts["host"] = host
			}
			if mode := firstNonEmptyString(stringValue(networkSettings["mode"]), "auto"); mode != "" {
				xhttpOpts["mode"] = mode
			}
			if headers := mapFromAny(networkSettings["headers"]); len(headers) > 0 {
				xhttpOpts["headers"] = headers
			}
			if len(xhttpOpts) > 0 {
				proxy["xhttp-opts"] = xhttpOpts
			}
		}
	}
}

func clashPrimaryPort(server map[string]any) int64 {
	if port, ok := clashPrimaryPortString(server); ok {
		parsed, _ := strconv.ParseInt(port, 10, 64)
		return parsed
	}
	return serverInt64(server, "port")
}

func clashPrimaryPortString(server map[string]any) (string, bool) {
	for _, raw := range []string{serverString(server, "mport"), serverString(server, "port")} {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		for _, segment := range strings.Split(raw, ",") {
			segment = strings.TrimSpace(segment)
			if segment == "" {
				continue
			}
			if strings.Contains(segment, "-") {
				parts := strings.SplitN(segment, "-", 2)
				return strings.TrimSpace(parts[0]), true
			}
			return segment, true
		}
	}
	return "", false
}

func clashPortHopping(server map[string]any) string {
	for _, raw := range []string{serverString(server, "mport"), serverString(server, "port")} {
		raw = strings.TrimSpace(raw)
		if strings.Contains(raw, "-") {
			return raw
		}
	}
	return ""
}

func clashMbps(value int64) string {
	if value <= 0 {
		return ""
	}
	return fmt.Sprintf("%d Mbps", value)
}

func clashALPNList(settings map[string]any, fallbackH3 bool) []string {
	values := serverStringSlice(settings, "alpn")
	if len(values) == 0 && fallbackH3 {
		return []string{"h3"}
	}
	return values
}

func serverStringSlice(server map[string]any, key string) []string {
	values := asAnySlice(server[key])
	result := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := stringValue(value)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	if len(result) > 0 {
		return result
	}
	if raw := stringValue(server[key]); raw != "" && raw != "[]" {
		for _, part := range strings.Split(raw, ",") {
			trimmed := strings.Trim(strings.TrimSpace(part), "[]\"")
			if trimmed != "" {
				result = append(result, trimmed)
			}
		}
	}
	return result
}

func inferLegacyV2nodeProtocol(server map[string]any) string {
	switch {
	case serverString(server, "flow") != "" || serverString(server, "encryption") != "" || len(serverMap(server, "encryption_settings")) > 0:
		return "vless"
	case serverString(server, "cipher") != "":
		return "shadowsocks"
	case server["padding_scheme"] != nil:
		return "anytls"
	case serverString(server, "udp_relay_mode") != "" || serverBool(server, "disable_sni"):
		return "tuic"
	case serverInt64(server, "up_mbps") > 0 || serverInt64(server, "down_mbps") > 0 || serverString(server, "obfs") != "" || serverString(server, "obfs_password") != "":
		if serverInt64(server, "tls") != 0 || len(serverMap(server, "tls_settings")) > 0 {
			return "hysteria2"
		}
		return "hysteria"
	case serverString(server, "server_name") != "" || serverBoolValue(server["allow_insecure"]):
		return "trojan"
	case len(serverMap(server, "tls_settings")) > 0 || len(serverMap(server, "network_settings")) > 0 || len(serverMap(server, "networkSettings")) > 0 || serverString(server, "network") != "":
		return "vmess"
	default:
		return ""
	}
}

func buildShadowsocksURI(userUUID string, server map[string]any) string {
	cipher := serverString(server, "cipher")
	password := userUUID
	if strings.Contains(cipher, "2022-blake3") {
		length := 32
		if cipher == "2022-blake3-aes-128-gcm" {
			length = 16
		}
		serverKey := nodeapi.ServerKey(serverInt64(server, "created_at"), length)
		userKey := base64.StdEncoding.EncodeToString([]byte(truncateString(userUUID, length)))
		password = serverKey + ":" + userKey
	}

	auth := base64.RawURLEncoding.EncodeToString([]byte(cipher + ":" + password))
	uri := fmt.Sprintf("ss://%s@%s:%d", auth, formatHost(serverString(server, "host")), serverInt64(server, "port"))
	if strings.EqualFold(serverString(server, "obfs"), "http") {
		uri += fmt.Sprintf(
			"?plugin=obfs-local;obfs=http;obfs-host=%s;path=%s",
			serverString(server, "obfs-host"),
			serverString(server, "obfs-path"),
		)
	} else if strings.EqualFold(serverString(server, "network"), "http") {
		settings := serverMap(server, "network_settings")
		if host := stringValue(settings["Host"]); host != "" {
			path := firstNonEmptyString(stringValue(settings["path"]), "/")
			uri += fmt.Sprintf("?plugin=obfs-local;obfs=tls;obfs-host=%s;path=%s", host, path)
		}
	}
	return uri + "#" + rawURLEncode(serverString(server, "name")) + "\r\n"
}

func buildVmessURI(userUUID string, server map[string]any) string {
	config := map[string]any{
		"v":    "2",
		"ps":   serverString(server, "name"),
		"add":  formatHost(serverString(server, "host")),
		"port": strconv.FormatInt(serverInt64(server, "port"), 10),
		"id":   userUUID,
		"aid":  "0",
		"scy":  "auto",
		"net":  serverString(server, "network"),
		"type": "none",
		"host": "",
		"path": "",
		"tls":  "",
		"fp":   "chrome",
	}

	if serverBool(server, "tls") {
		tlsSettings := firstNonEmptyMap(serverMap(server, "tls_settings"), serverMap(server, "tlsSettings"))
		config["tls"] = "tls"
		config["allowInsecure"] = boolToInt(serverMapBool(tlsSettings, "allow_insecure", "allowInsecure"))
		config["sni"] = firstNonEmptyString(
			stringValue(tlsSettings["server_name"]),
			stringValue(tlsSettings["serverName"]),
		)
	}

	networkSettings := firstNonEmptyMap(serverMap(server, "networkSettings"), serverMap(server, "network_settings"))
	switch serverString(server, "network") {
	case "tcp":
		header := nestedMap(networkSettings, "header")
		if strings.EqualFold(stringValue(header["type"]), "http") {
			config["type"] = "http"
			request := nestedMap(header, "request")
			if hosts := nestedAnySlice(nestedMap(request, "headers"), "Host"); len(hosts) > 0 {
				config["host"] = stringValue(hosts[0])
			}
			if paths := nestedAnySlice(request, "path"); len(paths) > 0 {
				config["path"] = stringValue(paths[0])
			}
		}
	case "ws":
		config["path"] = stringValue(networkSettings["path"])
		config["host"] = stringValue(nestedMap(networkSettings, "headers")["Host"])
		if security := stringValue(networkSettings["security"]); security != "" {
			config["scy"] = security
		}
	case "grpc":
		config["path"] = stringValue(networkSettings["serviceName"])
	case "kcp":
		if seed := stringValue(networkSettings["seed"]); seed != "" {
			config["path"] = seed
		}
		config["type"] = firstNonEmptyString(stringValue(nestedMap(networkSettings, "header")["type"]), "none")
	case "httpupgrade":
		config["path"] = stringValue(networkSettings["path"])
		config["host"] = stringValue(networkSettings["host"])
	case "xhttp":
		config["path"] = stringValue(networkSettings["path"])
		config["host"] = stringValue(networkSettings["host"])
		config["mode"] = firstNonEmptyString(stringValue(networkSettings["mode"]), "auto")
		if extra, ok := networkSettings["extra"]; ok && extra != nil {
			if raw, err := json.Marshal(extra); err == nil {
				config["extra"] = string(raw)
			}
		}
	}

	raw, _ := json.Marshal(config)
	return "vmess://" + base64.StdEncoding.EncodeToString(raw) + "\r\n"
}

func buildVlessURI(userUUID string, server map[string]any) string {
	tlsSettings := serverMap(server, "tls_settings")
	config := map[string]string{
		"type":         serverString(server, "network"),
		"encryption":   "none",
		"host":         "",
		"path":         "",
		"headerType":   "none",
		"quicSecurity": "none",
		"serviceName":  "",
		"security":     "",
		"flow":         serverString(server, "flow"),
		"fp":           firstNonEmptyString(stringValue(tlsSettings["fingerprint"]), "chrome"),
		"insecure":     strconv.Itoa(boolToInt(serverMapBool(tlsSettings, "allow_insecure"))),
	}

	if tls := serverInt64(server, "tls"); tls != 0 {
		if tls == 2 {
			config["security"] = "reality"
		} else {
			config["security"] = "tls"
		}
		config["sni"] = stringValue(tlsSettings["server_name"])
		if tls == 2 {
			config["pbk"] = stringValue(tlsSettings["public_key"])
			config["sid"] = stringValue(tlsSettings["short_id"])
		}
	}

	if strings.EqualFold(serverString(server, "encryption"), "mlkem768x25519plus") {
		encryptionSettings := serverMap(server, "encryption_settings")
		parts := []string{
			"mlkem768x25519plus",
			firstNonEmptyString(stringValue(encryptionSettings["mode"]), "native"),
			firstNonEmptyString(stringValue(encryptionSettings["rtt"]), "1rtt"),
		}
		if padding := stringValue(encryptionSettings["client_padding"]); padding != "" {
			parts = append(parts, padding)
		}
		parts = append(parts, stringValue(encryptionSettings["password"]))
		config["encryption"] = strings.Join(parts, ".")
	}

	configureVlessNetwork(server, config)
	return buildURIString("vless", userUUID, server, rawURLEncode(serverString(server, "name")), config)
}

func configureVlessNetwork(server map[string]any, config map[string]string) {
	settings := firstNonEmptyMap(serverMap(server, "network_settings"), serverMap(server, "networkSettings"))
	switch serverString(server, "network") {
	case "tcp":
		header := nestedMap(settings, "header")
		if strings.EqualFold(stringValue(header["type"]), "http") {
			config["headerType"] = "http"
			request := nestedMap(header, "request")
			if hosts := nestedAnySlice(nestedMap(request, "headers"), "Host"); len(hosts) > 0 {
				config["host"] = stringValue(hosts[0])
			}
			if paths := nestedAnySlice(request, "path"); len(paths) > 0 {
				config["path"] = stringValue(paths[0])
			}
		}
	case "ws":
		config["path"] = stringValue(settings["path"])
		config["host"] = stringValue(nestedMap(settings, "headers")["Host"])
	case "grpc":
		config["serviceName"] = stringValue(settings["serviceName"])
	case "kcp":
		config["headerType"] = firstNonEmptyString(stringValue(nestedMap(settings, "header")["type"]), "none")
		if seed := stringValue(settings["seed"]); seed != "" {
			config["seed"] = seed
		}
	case "httpupgrade":
		config["path"] = stringValue(settings["path"])
		config["host"] = stringValue(settings["host"])
	case "xhttp":
		config["path"] = stringValue(settings["path"])
		config["host"] = stringValue(settings["host"])
		config["mode"] = firstNonEmptyString(stringValue(settings["mode"]), "auto")
		if extra, ok := settings["extra"]; ok && extra != nil {
			if raw, err := json.Marshal(extra); err == nil {
				config["extra"] = string(raw)
			}
		}
	}
}

func buildTrojanURI(userUUID string, server map[string]any) string {
	tlsSettings := serverMap(server, "tls_settings")
	params := map[string]string{
		"allowInsecure": strconv.Itoa(boolToInt(serverBoolValue(server["allow_insecure"]) || serverMapBool(tlsSettings, "allow_insecure"))),
		"peer":          firstNonEmptyString(serverString(server, "server_name"), stringValue(tlsSettings["server_name"])),
		"sni":           firstNonEmptyString(serverString(server, "server_name"), stringValue(tlsSettings["server_name"])),
		"type":          serverString(server, "network"),
	}

	if network := serverString(server, "network"); network == "grpc" || network == "ws" {
		settings := serverMap(server, "network_settings")
		if network == "grpc" {
			params["serviceName"] = stringValue(settings["serviceName"])
		}
		if network == "ws" {
			params["path"] = stringValue(settings["path"])
			params["host"] = stringValue(nestedMap(settings, "headers")["Host"])
		}
	}
	return buildURIString("trojan", userUUID, server, rawURLEncode(serverString(server, "name")), params)
}

func buildHysteriaURI(userUUID string, server map[string]any) string {
	remote := formatHost(serverString(server, "host"))
	name := encodeURIComponent(serverString(server, "name"))
	portValue := serverString(server, "port")
	if portValue == "" {
		portValue = strconv.FormatInt(serverInt64(server, "port"), 10)
	}
	parts := strings.Split(portValue, ",")
	firstPort := strings.TrimSpace(parts[0])
	if strings.Contains(firstPort, "-") {
		firstPort = strings.TrimSpace(strings.SplitN(firstPort, "-", 2)[0])
	}

	if serverInt64(server, "version") == 2 || strings.EqualFold(serverString(server, "type"), "hysteria2") {
		tlsSettings := serverMap(server, "tls_settings")
		uri := fmt.Sprintf(
			"hysteria2://%s@%s:%s/?insecure=%d&sni=%s",
			userUUID,
			remote,
			firstPort,
			boolToInt(serverBoolValue(server["insecure"]) || serverMapBool(tlsSettings, "allow_insecure")),
			firstNonEmptyString(serverString(server, "server_name"), stringValue(tlsSettings["server_name"])),
		)
		if obfs := serverString(server, "obfs"); obfs != "" && serverString(server, "obfs_password") != "" {
			uri += "&obfs=" + url.QueryEscape(obfs) + "&obfs-password=" + url.QueryEscape(serverString(server, "obfs_password"))
		}
		if len(parts) != 1 || strings.Contains(parts[0], "-") {
			uri += "&mport=" + url.QueryEscape(portValue)
		}
		return uri + "#" + name + "\r\n"
	}

	uri := fmt.Sprintf(
		"hysteria://%s:%s/?protocol=udp&auth=%s&insecure=%d&peer=%s&upmbps=%d&downmbps=%d",
		remote,
		firstPort,
		url.QueryEscape(userUUID),
		boolToInt(serverBoolValue(server["insecure"])),
		url.QueryEscape(serverString(server, "server_name")),
		serverInt64(server, "down_mbps"),
		serverInt64(server, "up_mbps"),
	)
	if obfs := serverString(server, "obfs"); obfs != "" && serverString(server, "obfs_password") != "" {
		uri += "&obfs=" + url.QueryEscape(obfs) + "&obfsParam" + url.QueryEscape(serverString(server, "obfs_password"))
	}
	if len(parts) != 1 || strings.Contains(parts[0], "-") {
		uri += "&mport=" + url.QueryEscape(portValue)
	}
	return uri + "#" + name + "\r\n"
}

func buildTuicURI(userUUID string, server map[string]any) string {
	tlsSettings := serverMap(server, "tls_settings")
	params := map[string]string{
		"sni":                firstNonEmptyString(serverString(server, "server_name"), stringValue(tlsSettings["server_name"])),
		"alpn":               "h3",
		"congestion_control": serverString(server, "congestion_control"),
		"allow_insecure":     strconv.Itoa(boolToInt(serverBoolValue(server["insecure"]) || serverMapBool(tlsSettings, "allow_insecure"))),
		"disable_sni":        strconv.Itoa(boolToInt(serverBool(server, "disable_sni"))),
		"udp_relay_mode":     firstNonEmptyString(serverString(server, "udp_relay_mode"), "native"),
	}
	return fmt.Sprintf(
		"tuic://%s:%s@%s:%d?%s#%s\r\n",
		userUUID,
		userUUID,
		formatHost(serverString(server, "host")),
		serverInt64(server, "port"),
		encodeQuery(params),
		encodeURIComponent(serverString(server, "name")),
	)
}

func buildAnyTLSURI(userUUID string, server map[string]any) string {
	tlsSettings := serverMap(server, "tls_settings")
	params := map[string]string{
		"insecure": strconv.Itoa(boolToInt(serverBoolValue(server["insecure"]) || serverMapBool(tlsSettings, "allow_insecure"))),
	}
	if sni := firstNonEmptyString(serverString(server, "server_name"), stringValue(tlsSettings["server_name"])); sni != "" {
		params["sni"] = sni
	}
	return fmt.Sprintf(
		"anytls://%s@%s:%d/?%s#%s\r\n",
		userUUID,
		formatHost(serverString(server, "host")),
		serverInt64(server, "port"),
		encodeQuery(params),
		encodeURIComponent(serverString(server, "name")),
	)
}

func buildURIString(scheme, auth string, server map[string]any, name string, params map[string]string) string {
	return fmt.Sprintf(
		"%s://%s@%s:%d?%s#%s\r\n",
		scheme,
		auth,
		formatHost(serverString(server, "host")),
		serverInt64(server, "port"),
		encodeQuery(params),
		name,
	)
}

func encodeQuery(params map[string]string) string {
	values := url.Values{}
	for key, value := range params {
		if strings.TrimSpace(value) == "" {
			continue
		}
		values.Set(key, value)
	}
	return values.Encode()
}

func rawURLEncode(value string) string {
	return strings.ReplaceAll(url.PathEscape(value), "+", "%20")
}

func encodeURIComponent(value string) string {
	encoded := url.QueryEscape(value)
	encoded = strings.ReplaceAll(encoded, "+", "%20")
	replacer := strings.NewReplacer("%21", "!", "%2A", "*", "%27", "'", "%28", "(", "%29", ")")
	return replacer.Replace(encoded)
}

func formatHost(host string) string {
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") && !strings.HasSuffix(host, "]") {
		return "[" + host + "]"
	}
	return host
}

func truncateString(value string, length int) string {
	if length <= 0 || len(value) <= length {
		return value
	}
	return value[:length]
}

func formatSubscribeTrafficBytes(value int64) string {
	if value < 0 {
		value = 0
	}

	units := []string{"B", "KB", "MB", "GB", "TB", "PB"}
	size := float64(value)
	index := 0
	for size >= 1024 && index < len(units)-1 {
		size /= 1024
		index++
	}
	if index == 0 {
		return fmt.Sprintf("%d %s", value, units[index])
	}
	return fmt.Sprintf("%.2f %s", size, units[index])
}

func formatSubscribeDate(unix int64) string {
	return time.Unix(unix, 0).UTC().Format("2006-01-02")
}

func formatShadowrocketTrafficGB(value int64) string {
	gb := float64(value) / float64(1024*1024*1024)
	gb = math.Round(gb*100) / 100
	return strconv.FormatFloat(gb, 'f', -1, 64)
}

func asAnySlice(value any) []any {
	switch typed := value.(type) {
	case nil:
		return []any{}
	case []any:
		return append([]any(nil), typed...)
	case []string:
		result := make([]any, 0, len(typed))
		for _, item := range typed {
			result = append(result, item)
		}
		return result
	default:
		return []any{}
	}
}

func appendAnySlice(dst []any, src ...any) []any {
	return append(dst, src...)
}

func sliceContainsString(values []any, target string) bool {
	for _, value := range values {
		if stringValue(value) == target {
			return true
		}
	}
	return false
}

func copyMap(input map[string]any) map[string]any {
	result := make(map[string]any, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

func mapFromAny(value any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	if typed, ok := value.(map[string]any); ok {
		return typed
	}
	return map[string]any{}
}

func nestedMap(input map[string]any, key string) map[string]any {
	return mapFromAny(input[key])
}

func nestedAnySlice(input map[string]any, key string) []any {
	return asAnySlice(input[key])
}

func firstNonEmptyMap(values ...map[string]any) map[string]any {
	for _, value := range values {
		if len(value) > 0 {
			return value
		}
	}
	return map[string]any{}
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if normalized := sanitizeString(value); normalized != "" {
			return normalized
		}
	}
	return ""
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	return sanitizeString(fmt.Sprintf("%v", value))
}

func sanitizeString(value string) string {
	trimmed := strings.TrimSpace(value)
	switch strings.ToLower(trimmed) {
	case "", "<nil>", "null":
		return ""
	default:
		return trimmed
	}
}

func serverString(server map[string]any, key string) string {
	if server == nil {
		return ""
	}
	value, ok := server[key]
	if !ok || value == nil {
		return ""
	}
	return stringValue(value)
}

func serverInt64(server map[string]any, key string) int64 {
	if server == nil {
		return 0
	}
	return serverInt64Value(server[key])
}

func serverInt64Value(value any) int64 {
	switch typed := value.(type) {
	case int64:
		return typed
	case int32:
		return int64(typed)
	case int:
		return int64(typed)
	case float64:
		return int64(typed)
	case float32:
		return int64(typed)
	case json.Number:
		v, _ := typed.Int64()
		return v
	case string:
		parsed, _ := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		return parsed
	default:
		return 0
	}
}

func serverBool(server map[string]any, key string) bool {
	return serverBoolValue(server[key])
}

func serverBoolValue(value any) bool {
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
		case "1", "true", "yes", "on":
			return true
		default:
			return false
		}
	default:
		return false
	}
}

func serverMap(server map[string]any, key string) map[string]any {
	if server == nil {
		return map[string]any{}
	}
	if typed, ok := server[key].(map[string]any); ok {
		return typed
	}
	return map[string]any{}
}

func serverMapBool(server map[string]any, keys ...string) bool {
	for _, key := range keys {
		if key == "" {
			continue
		}
		if serverBoolValue(server[key]) {
			return true
		}
	}
	return false
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
