package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"forest/go-api/internal/config"
	"forest/go-api/internal/nodeapi"

	"gopkg.in/yaml.v3"
)

func buildGeneralSubscribePayload(userUUID string, servers []map[string]any) string {
	var builder strings.Builder
	for _, server := range servers {
		builder.WriteString(buildSubscribeURI(userUUID, server))
	}
	return base64.StdEncoding.EncodeToString([]byte(builder.String()))
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
		names = append(names, strings.TrimSpace(fmt.Sprint(proxy["name"])))
	}

	template["proxies"] = appendAnySlice(asAnySlice(template["proxies"]), proxies...)
	mergeProxyGroups(template, names)

	raw, err := yaml.Marshal(template)
	if err != nil {
		return "", fmt.Errorf("marshal clash profile: %w", err)
	}

	appName := strings.TrimSpace(cfg.AppName)
	if appName == "" {
		appName = "V2Board"
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

func mergeProxyGroups(template map[string]any, proxyNames []string) {
	if len(proxyNames) == 0 {
		return
	}

	groups := asAnySlice(template["proxy-groups"])
	for _, groupValue := range groups {
		group, ok := groupValue.(map[string]any)
		if !ok {
			continue
		}
		existing := asAnySlice(group["proxies"])
		for _, name := range proxyNames {
			if sliceContainsString(existing, name) {
				continue
			}
			existing = append(existing, name)
		}
		group["proxies"] = existing
	}
	template["proxy-groups"] = groups
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
	case "trojan":
		return buildClashTrojanProxy(userUUID, normalized), true
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
		if host := strings.TrimSpace(fmt.Sprint(networkSettings["Host"])); host != "" {
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
			strings.TrimSpace(fmt.Sprint(tlsSettings["server_name"])),
			strings.TrimSpace(fmt.Sprint(tlsSettings["serverName"])),
		)
	}

	network := serverString(server, "network")
	networkSettings := firstNonEmptyMap(serverMap(server, "network_settings"), serverMap(server, "networkSettings"))
	switch network {
	case "tcp":
		header := nestedMap(networkSettings, "header")
		if strings.EqualFold(strings.TrimSpace(fmt.Sprint(header["type"])), "http") {
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
		if path := strings.TrimSpace(fmt.Sprint(networkSettings["path"])); path != "" {
			wsOpts["path"] = path
		}
		if host := strings.TrimSpace(fmt.Sprint(nestedMap(networkSettings, "headers")["Host"])); host != "" {
			wsOpts["headers"] = map[string]any{"Host": host}
		}
		if len(wsOpts) > 0 {
			proxy["ws-opts"] = wsOpts
		}
		if security := strings.TrimSpace(fmt.Sprint(networkSettings["security"])); security != "" {
			proxy["cipher"] = security
		}
	case "grpc":
		proxy["network"] = "grpc"
		if serviceName := strings.TrimSpace(fmt.Sprint(networkSettings["serviceName"])); serviceName != "" {
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
			if serviceName := strings.TrimSpace(fmt.Sprint(networkSettings["serviceName"])); serviceName != "" {
				proxy["grpc-opts"] = map[string]any{"grpc-service-name": serviceName}
			}
		}
		if network == "ws" {
			wsOpts := map[string]any{}
			if path := strings.TrimSpace(fmt.Sprint(networkSettings["path"])); path != "" {
				wsOpts["path"] = path
			}
			if host := strings.TrimSpace(fmt.Sprint(nestedMap(networkSettings, "headers")["Host"])); host != "" {
				wsOpts["headers"] = map[string]any{"Host": host}
			}
			if len(wsOpts) > 0 {
				proxy["ws-opts"] = wsOpts
			}
		}
	}

	tlsSettings := serverMap(server, "tls_settings")
	proxy["sni"] = firstNonEmptyString(serverString(server, "server_name"), strings.TrimSpace(fmt.Sprint(tlsSettings["server_name"])))
	proxy["skip-cert-verify"] = serverBoolValue(server["allow_insecure"]) || serverMapBool(tlsSettings, "allow_insecure")
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
	serverType := strings.ToLower(strings.TrimSpace(fmt.Sprint(normalized["type"])))
	if serverType == "v2node" {
		serverType = strings.ToLower(strings.TrimSpace(fmt.Sprint(normalized["protocol"])))
		normalized["type"] = serverType
	}
	if serverType == "hysteria" && serverInt64(normalized, "version") == 2 {
		return "hysteria2", normalized
	}
	return serverType, normalized
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
		if host := strings.TrimSpace(fmt.Sprint(settings["Host"])); host != "" {
			path := firstNonEmptyString(strings.TrimSpace(fmt.Sprint(settings["path"])), "/")
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
			strings.TrimSpace(fmt.Sprint(tlsSettings["server_name"])),
			strings.TrimSpace(fmt.Sprint(tlsSettings["serverName"])),
		)
	}

	networkSettings := firstNonEmptyMap(serverMap(server, "networkSettings"), serverMap(server, "network_settings"))
	switch serverString(server, "network") {
	case "tcp":
		header := nestedMap(networkSettings, "header")
		if strings.EqualFold(strings.TrimSpace(fmt.Sprint(header["type"])), "http") {
			config["type"] = "http"
			request := nestedMap(header, "request")
			if hosts := nestedAnySlice(nestedMap(request, "headers"), "Host"); len(hosts) > 0 {
				config["host"] = fmt.Sprint(hosts[0])
			}
			if paths := nestedAnySlice(request, "path"); len(paths) > 0 {
				config["path"] = fmt.Sprint(paths[0])
			}
		}
	case "ws":
		config["path"] = strings.TrimSpace(fmt.Sprint(networkSettings["path"]))
		config["host"] = strings.TrimSpace(fmt.Sprint(nestedMap(networkSettings, "headers")["Host"]))
		if security := strings.TrimSpace(fmt.Sprint(networkSettings["security"])); security != "" {
			config["scy"] = security
		}
	case "grpc":
		config["path"] = strings.TrimSpace(fmt.Sprint(networkSettings["serviceName"]))
	case "kcp":
		if seed := strings.TrimSpace(fmt.Sprint(networkSettings["seed"])); seed != "" {
			config["path"] = seed
		}
		config["type"] = firstNonEmptyString(strings.TrimSpace(fmt.Sprint(nestedMap(networkSettings, "header")["type"])), "none")
	case "httpupgrade":
		config["path"] = strings.TrimSpace(fmt.Sprint(networkSettings["path"]))
		config["host"] = strings.TrimSpace(fmt.Sprint(networkSettings["host"]))
	case "xhttp":
		config["path"] = strings.TrimSpace(fmt.Sprint(networkSettings["path"]))
		config["host"] = strings.TrimSpace(fmt.Sprint(networkSettings["host"]))
		config["mode"] = firstNonEmptyString(strings.TrimSpace(fmt.Sprint(networkSettings["mode"])), "auto")
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
		"fp":           firstNonEmptyString(strings.TrimSpace(fmt.Sprint(tlsSettings["fingerprint"])), "chrome"),
		"insecure":     strconv.Itoa(boolToInt(serverMapBool(tlsSettings, "allow_insecure"))),
	}

	if tls := serverInt64(server, "tls"); tls != 0 {
		if tls == 2 {
			config["security"] = "reality"
		} else {
			config["security"] = "tls"
		}
		config["sni"] = strings.TrimSpace(fmt.Sprint(tlsSettings["server_name"]))
		if tls == 2 {
			config["pbk"] = strings.TrimSpace(fmt.Sprint(tlsSettings["public_key"]))
			config["sid"] = strings.TrimSpace(fmt.Sprint(tlsSettings["short_id"]))
		}
	}

	if strings.EqualFold(serverString(server, "encryption"), "mlkem768x25519plus") {
		encryptionSettings := serverMap(server, "encryption_settings")
		parts := []string{
			"mlkem768x25519plus",
			firstNonEmptyString(strings.TrimSpace(fmt.Sprint(encryptionSettings["mode"])), "native"),
			firstNonEmptyString(strings.TrimSpace(fmt.Sprint(encryptionSettings["rtt"])), "1rtt"),
		}
		if padding := strings.TrimSpace(fmt.Sprint(encryptionSettings["client_padding"])); padding != "" {
			parts = append(parts, padding)
		}
		parts = append(parts, strings.TrimSpace(fmt.Sprint(encryptionSettings["password"])))
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
		if strings.EqualFold(strings.TrimSpace(fmt.Sprint(header["type"])), "http") {
			config["headerType"] = "http"
			request := nestedMap(header, "request")
			if hosts := nestedAnySlice(nestedMap(request, "headers"), "Host"); len(hosts) > 0 {
				config["host"] = fmt.Sprint(hosts[0])
			}
			if paths := nestedAnySlice(request, "path"); len(paths) > 0 {
				config["path"] = fmt.Sprint(paths[0])
			}
		}
	case "ws":
		config["path"] = strings.TrimSpace(fmt.Sprint(settings["path"]))
		config["host"] = strings.TrimSpace(fmt.Sprint(nestedMap(settings, "headers")["Host"]))
	case "grpc":
		config["serviceName"] = strings.TrimSpace(fmt.Sprint(settings["serviceName"]))
	case "kcp":
		config["headerType"] = firstNonEmptyString(strings.TrimSpace(fmt.Sprint(nestedMap(settings, "header")["type"])), "none")
		if seed := strings.TrimSpace(fmt.Sprint(settings["seed"])); seed != "" {
			config["seed"] = seed
		}
	case "httpupgrade":
		config["path"] = strings.TrimSpace(fmt.Sprint(settings["path"]))
		config["host"] = strings.TrimSpace(fmt.Sprint(settings["host"]))
	case "xhttp":
		config["path"] = strings.TrimSpace(fmt.Sprint(settings["path"]))
		config["host"] = strings.TrimSpace(fmt.Sprint(settings["host"]))
		config["mode"] = firstNonEmptyString(strings.TrimSpace(fmt.Sprint(settings["mode"])), "auto")
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
		"peer":          firstNonEmptyString(serverString(server, "server_name"), strings.TrimSpace(fmt.Sprint(tlsSettings["server_name"]))),
		"sni":           firstNonEmptyString(serverString(server, "server_name"), strings.TrimSpace(fmt.Sprint(tlsSettings["server_name"]))),
		"type":          serverString(server, "network"),
	}

	if network := serverString(server, "network"); network == "grpc" || network == "ws" {
		settings := serverMap(server, "network_settings")
		if network == "grpc" {
			params["serviceName"] = strings.TrimSpace(fmt.Sprint(settings["serviceName"]))
		}
		if network == "ws" {
			params["path"] = strings.TrimSpace(fmt.Sprint(settings["path"]))
			params["host"] = strings.TrimSpace(fmt.Sprint(nestedMap(settings, "headers")["Host"]))
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
			firstNonEmptyString(serverString(server, "server_name"), strings.TrimSpace(fmt.Sprint(tlsSettings["server_name"]))),
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
		"sni":                firstNonEmptyString(serverString(server, "server_name"), strings.TrimSpace(fmt.Sprint(tlsSettings["server_name"]))),
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
	if sni := firstNonEmptyString(serverString(server, "server_name"), strings.TrimSpace(fmt.Sprint(tlsSettings["server_name"]))); sni != "" {
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
		if strings.TrimSpace(fmt.Sprint(value)) == target {
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
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func serverString(server map[string]any, key string) string {
	if server == nil {
		return ""
	}
	value, ok := server[key]
	if !ok || value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
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
