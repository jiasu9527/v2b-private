package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"

	"forest/go-api/internal/subscribelink"
)

// General subscriptions preserve extra nodes' original URI. Native conversion
// must also preserve fields absent from the shared, intentionally minimal parser.
// node is the renderer's copy, not the stored server map.
func quantumultXRestoreExtraFields(kind string, node map[string]any) bool {
	if !subscribelink.IsExtra(node) {
		return true
	}
	raw := subscribelink.RawURI(node)
	if raw == "" || raw == "<nil>" {
		return true // Structured extras can already carry all connection fields.
	}
	switch kind {
	case "shadowsocks":
		parsed, err := url.Parse(raw)
		if err != nil {
			return false
		}
		query, err := url.ParseQuery(parsed.RawQuery)
		if err != nil {
			return false
		}
		if plugin := query.Get("plugin"); plugin != "" {
			parts := strings.Split(plugin, ";")
			if parts[0] != "obfs-local" && parts[0] != "simple-obfs" {
				return false // In particular, never downgrade v2ray-plugin to plain SS.
			}
			for _, part := range parts[1:] {
				pair := strings.SplitN(part, "=", 2)
				if len(pair) != 2 {
					return false
				}
				switch pair[0] {
				case "obfs", "obfs-host", "obfs-uri", "obfs-path":
				default:
					return false
				}
			}
			if obfs := serverString(node, "obfs"); obfs != "http" && obfs != "tls" {
				return false
			}
		}
	case "vmess":
		if !strings.HasPrefix(strings.ToLower(raw), "vmess://") {
			return false
		}
		encoded := strings.TrimRight(raw[len("vmess://"):], "=")
		body, err := base64.RawStdEncoding.DecodeString(encoded)
		if err != nil {
			body, err = base64.RawURLEncoding.DecodeString(encoded)
		}
		var fields map[string]any
		if err != nil || json.Unmarshal(body, &fields) != nil {
			return false
		}
		if mode := strings.ToLower(stringValue(fields["tls"])); mode != "" && mode != "none" && mode != "tls" {
			return false
		}
		if cipher := stringValue(fields["scy"]); cipher != "" {
			node["security"] = cipher
		}
		if aid := stringValue(fields["aid"]); aid != "" {
			value, err := strconv.ParseUint(aid, 10, 16)
			if err != nil {
				return false
			}
			node["alter_id"] = int64(value)
		}
		if serverString(node, "network") == "tcp" && strings.EqualFold(stringValue(fields["type"]), "http") {
			request := map[string]any{}
			if host := stringValue(fields["host"]); host != "" {
				request["headers"] = map[string]any{"Host": []string{host}}
			}
			if path := stringValue(fields["path"]); path != "" {
				request["path"] = []string{path}
			}
			settings := copyMap(serverMap(node, "network_settings"))
			settings["header"] = map[string]any{"type": "http", "request": request}
			node["network_settings"] = settings
		}
	}
	return true
}
