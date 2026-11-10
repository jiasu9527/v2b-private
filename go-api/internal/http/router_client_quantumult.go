package httpapi

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net"
	"strconv"
	"strings"
	"unicode"

	"forest/go-api/internal/entryevidence"
	"forest/go-api/internal/nodeapi"
	"forest/go-api/internal/subscribelink"
)

// Quantumult X server resources are plain UTF-8 node lines, not an encoded URI
// list or a complete config. Keep this format separate from Surge's grammar.
func buildQuantumultXPayload(userUUID string, servers []map[string]any, collectors ...*entryevidence.Collector) string {
	collector := entryCollector(collectors)
	var payload strings.Builder
	for _, server := range servers {
		line, ok := buildQuantumultXProxyLine(userUUID, server)
		if !ok {
			continue
		}
		payload.WriteString(line)
		payload.WriteByte('\n')
		emitEntryServer(collector, server, serverString(server, "host"), strconv.FormatInt(serverInt64(server, "port"), 10))
	}
	collector.Coverage("complete", "server_write_completed")
	return payload.String()
}

func buildQuantumultXProxyLine(userUUID string, server map[string]any) (string, bool) {
	kind, node := normalizeSubscribeServer(server)
	switch kind {
	case "shadowsocks", "vmess", "vless", "trojan", "anytls":
	default:
		return "", false
	}
	if !quantumultXRestoreExtraFields(kind, node) {
		return "", false
	}
	host := serverString(node, "host")
	port, err := strconv.ParseUint(fmt.Sprint(node["port"]), 10, 16)
	if err != nil || port == 0 || !quantumultXHostValid(host) {
		return "", false
	}
	password := subscribelink.Credential(userUUID, node)
	if !quantumultXValueValid(password) {
		return "", false
	}
	parts := []string{kind + "=" + formatHost(host) + ":" + strconv.FormatUint(port, 10)}
	settings := firstNonEmptyMap(serverMap(node, "network_settings"), serverMap(node, "networkSettings"))
	tlsSettings := firstNonEmptyMap(serverMap(node, "tls_settings"), serverMap(node, "tlsSettings"))
	tlsEnabled := serverBool(node, "tls") || serverInt64(node, "tls") == 2 || kind == "trojan" || kind == "anytls"
	valid := true
	add := func(key, value string) {
		if !quantumultXValueValid(value) {
			valid = false
			return
		}
		parts = append(parts, key+"="+value)
	}
	switch kind {
	case "shadowsocks":
		cipher := strings.ToLower(serverString(node, "cipher"))
		switch cipher {
		case "aes-128-gcm", "aes-192-gcm", "aes-256-gcm", "aes-128-cfb", "aes-192-cfb", "aes-256-cfb", "chacha20", "chacha20-ietf", "chacha20-ietf-poly1305", "rc4-md5", "2022-blake3-aes-128-gcm", "2022-blake3-aes-256-gcm":
		default:
			return "", false
		}
		if strings.HasPrefix(cipher, "2022-") && !subscribelink.IsExtra(node) {
			length := 32
			if cipher == "2022-blake3-aes-128-gcm" {
				length = 16
			}
			password = nodeapi.ServerKey(serverInt64(node, "created_at"), length) + ":" + base64.StdEncoding.EncodeToString([]byte(truncateString(userUUID, length)))
		}
		add("method", cipher)
	case "vmess":
		cipher := firstNonEmptyString(stringValue(settings["security"]), serverString(node, "security"), serverString(node, "cipher"), "auto")
		if cipher == "auto" || cipher == "chacha20-ietf-poly1305" {
			cipher = "chacha20-poly1305"
		}
		switch cipher {
		case "none", "aes-128-gcm", "chacha20-poly1305":
		default:
			return "", false
		}
		add("method", cipher)
		add("aead", strconv.FormatBool(serverInt64(node, "alter_id") == 0 && serverInt64(node, "alterId") == 0))
	case "vless":
		if encryption := serverString(node, "encryption"); encryption != "" && encryption != "none" {
			return "", false
		}
		add("method", "none")
		if flow := serverString(node, "flow"); flow != "" {
			if flow != "xtls-rprx-vision" || !tlsEnabled || (serverString(node, "network") != "" && serverString(node, "network") != "tcp") {
				return "", false
			}
			add("vless-flow", flow)
		}
	}
	add("password", password)
	transport, ok := quantumultXTransport(kind, node, settings, tlsSettings, tlsEnabled)
	if !ok {
		return "", false
	}
	// A fixed ordering keeps subscriptions and their regression tests stable.
	for _, field := range []string{"obfs", "over-tls", "obfs-host", "tls-host", "obfs-uri"} {
		if value := transport[field]; value != "" {
			add(field, value)
		}
	}
	if tlsEnabled {
		// QX has no documented ECH configuration equivalent. Do not silently
		// turn a configured ECH connection into ordinary TLS.
		for _, field := range []string{"ech", "ech_config"} {
			if value := stringValue(tlsSettings[field]); value != "" && value != "0" && value != "false" && value != "none" && value != "[]" {
				return "", false
			}
		}
		insecure := serverMapBool(tlsSettings, "allow_insecure", "allowInsecure") || serverMapBool(node, "allow_insecure", "allowInsecure", "insecure")
		add("tls-verification", strconv.FormatBool(!insecure))
		// Nested settings survive switching a node from Reality back to TLS.
		// The active mode, not a leftover key, determines the handshake.
		if serverInt64(node, "tls") == 2 {
			publicKey := firstNonEmptyString(stringValue(tlsSettings["public_key"]), stringValue(tlsSettings["publicKey"]))
			decoded, decodeErr := base64.RawURLEncoding.DecodeString(strings.TrimRight(publicKey, "="))
			if decodeErr != nil || len(decoded) != 32 {
				return "", false
			}
			add("reality-base64-pubkey", publicKey)
			if shortID := firstNonEmptyString(stringValue(tlsSettings["short_id"]), stringValue(tlsSettings["shortId"])); shortID != "" {
				if decoded, err := hex.DecodeString(shortID); err != nil || len(decoded) > 8 {
					return "", false
				}
				add("reality-hex-shortid", shortID)
			}
		} else if alpn := clashALPNList(tlsSettings, false); len(alpn) > 0 {
			var encoded []byte
			for _, protocol := range alpn {
				if len(protocol) == 0 || len(protocol) > 255 || !quantumultXValueValid(protocol) {
					return "", false
				}
				encoded = append(encoded, byte(len(protocol)))
				encoded = append(encoded, protocol...)
			}
			add("tls-alpn", hex.EncodeToString(encoded))
		}
	}
	add("fast-open", "false")
	add("udp-relay", "true")
	add("tag", quantumultXTag(firstNonEmptyString(serverString(node, "name"), host+":"+strconv.FormatUint(port, 10))))
	if !valid {
		return "", false
	}
	return strings.Join(parts, ", "), true
}

// obfs-host is BOTH the HTTP Host and TLS SNI for WSS. Never substitute one
// explicit value for a different other value: that would output a broken node.
func quantumultXTransport(kind string, node, settings, tlsSettings map[string]any, tlsEnabled bool) (map[string]string, bool) {
	result := map[string]string{}
	network := strings.ToLower(serverString(node, "network"))
	sni := firstNonEmptyString(serverString(node, "server_name"), stringValue(tlsSettings["server_name"]), stringValue(tlsSettings["serverName"]))
	obfs := strings.ToLower(serverString(node, "obfs"))
	if kind == "shadowsocks" && obfs != "" && obfs != "none" {
		if tlsEnabled || (network != "" && network != "tcp") || (obfs != "http" && obfs != "tls") {
			return nil, false
		}
		result["obfs"] = obfs
		result["obfs-host"] = serverString(node, "obfs-host")
		if obfs == "http" {
			result["obfs-uri"] = serverString(node, "obfs-path")
		}
		return result, true
	}
	switch network {
	case "", "tcp":
		header := nestedMap(settings, "header")
		headerType := strings.ToLower(stringValue(header["type"]))
		if headerType != "" && headerType != "none" {
			if headerType != "http" || tlsEnabled || kind == "trojan" || kind == "anytls" {
				return nil, false
			}
			result["obfs"] = "http"
			if kind == "shadowsocks" {
				result["obfs"] = "vmess-http"
			}
			request := nestedMap(header, "request")
			for key, values := range map[string][]string{"obfs-host": serverStringSlice(nestedMap(request, "headers"), "Host"), "obfs-uri": serverStringSlice(request, "path")} {
				if len(values) > 1 {
					return nil, false
				}
				if len(values) == 1 {
					result[key] = values[0]
				}
			}
		} else if tlsEnabled {
			if kind == "trojan" || kind == "anytls" {
				result["over-tls"], result["tls-host"] = "true", sni
			} else {
				result["obfs"], result["obfs-host"] = "over-tls", sni
			}
		}
	case "http":
		// Xray HTTP transport is not TCP's HTTP header obfuscation. Only the
		// explicit tcp/header.type=http branch above has a faithful QX mapping.
		return nil, false
	case "ws":
		if kind == "anytls" {
			return nil, false
		}
		result["obfs"] = "ws"
		result["obfs-uri"] = stringValue(settings["path"])
		for key, value := range nestedMap(settings, "headers") {
			if !strings.EqualFold(key, "Host") && stringValue(value) != "" {
				return nil, false // Arbitrary WebSocket headers cannot be expressed.
			}
			if strings.EqualFold(key, "Host") {
				result["obfs-host"] = stringValue(value)
			}
		}
		if tlsEnabled {
			result["obfs"] = "wss"
			if sni != "" && result["obfs-host"] != "" && !strings.EqualFold(sni, result["obfs-host"]) {
				return nil, false
			}
			result["obfs-host"] = firstNonEmptyString(result["obfs-host"], sni)
		}
	default:
		return nil, false // Do not turn unsupported transports into plain TCP.
	}
	return result, true
}

func quantumultXValueValid(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || strings.ContainsRune(value, ',') {
		return false
	}
	for _, char := range value {
		if unicode.IsControl(char) || char == '\u2028' || char == '\u2029' {
			return false
		}
	}
	return true
}

func quantumultXHostValid(host string) bool {
	if !quantumultXValueValid(host) || strings.ContainsAny(host, " /\\@?#;=") {
		return false
	}
	for _, char := range host {
		if unicode.IsSpace(char) {
			return false
		}
	}
	if strings.ContainsAny(host, "[]") {
		if !strings.HasPrefix(host, "[") || !strings.HasSuffix(host, "]") || strings.Count(host, "[") != 1 || strings.Count(host, "]") != 1 {
			return false
		}
		return strings.ContainsRune(host, ':') && net.ParseIP(host[1:len(host)-1]) != nil
	}
	if strings.ContainsRune(host, ':') {
		return net.ParseIP(host) != nil
	}
	return true
}

func quantumultXTag(value string) string {
	return strings.TrimSpace(strings.Map(func(char rune) rune {
		switch {
		case char == ',':
			return '，'
		case unicode.IsControl(char), char == '\u2028', char == '\u2029':
			return ' '
		default:
			return char
		}
	}, value))
}
