package admin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// AI node edits use the same normalizer as the node editor, but are patches:
// fields not named by the operator (including stored secrets) stay untouched.
var aiNodeCommonFields = []string{"name", "host", "port", "server_port", "group_id", "rate", "show", "client_entry_only", "tags", "sort"}
var aiNodeProtocolFields = map[string][]string{
	"vmess":       {"tls", "network", "tls_settings", "network_settings"},
	"vless":       {"tls", "network", "flow", "tls_settings", "network_settings"},
	"trojan":      {"network", "network_settings", "server_name", "allow_insecure"},
	"shadowsocks": {"cipher"},
	"tuic":        {"server_name", "insecure", "disable_sni", "zero_rtt_handshake", "udp_relay_mode", "congestion_control"},
	"hysteria":    {"server_name", "insecure", "up_mbps", "down_mbps"},
	"anytls":      {"server_name", "insecure"},
	"v2node":      {"tls", "network", "flow", "tls_settings", "network_settings", "listen_ip", "send_through", "disable_sni", "zero_rtt_handshake", "udp_relay_mode", "congestion_control", "cipher", "up_mbps", "down_mbps"},
}
var aiTLSFields = map[string]bool{"server_name": true, "allow_insecure": true, "fingerprint": true, "alpn": true, "dest": true, "server_port": true, "xver": true, "reject_unknown_sni": true}
var aiNetworkFields = map[string]bool{"path": true, "host": true, "serviceName": true, "mode": true, "headers": true}

func AIManagedNodeFields(kind string) []string {
	fields, ok := aiNodeProtocolFields[kind]
	if !ok {
		return nil
	}
	out := append(append([]string{}, aiNodeCommonFields...), fields...)
	sort.Strings(out)
	return out
}
func aiNodeColumn(kind, key string) string {
	if kind == "vmess" {
		if key == "tls_settings" {
			return "tlsSettings"
		}
		if key == "network_settings" {
			return "networkSettings"
		}
	}
	return key
}
func aiNodeObject(value any) (map[string]any, error) {
	if value == nil || value == "" {
		return map[string]any{}, nil
	}
	if s, ok := value.(string); ok {
		var v map[string]any
		if json.Unmarshal([]byte(s), &v) != nil {
			return nil, errors.New("invalid settings")
		}
		if v == nil {
			v = map[string]any{}
		}
		return v, nil
	}
	if v, ok := value.(map[string]any); ok {
		raw, _ := json.Marshal(v)
		var out map[string]any
		_ = json.Unmarshal(raw, &out)
		return out, nil
	}
	return nil, errors.New("invalid settings")
}
func aiNodeNumber(v any, min, max float64) bool {
	n, ok := v.(float64)
	return ok && !math.IsNaN(n) && !math.IsInf(n, 0) && n >= min && n <= max && n == math.Trunc(n)
}
func aiNodeText(v any, max int, nullable bool) bool {
	if v == nil {
		return nullable
	}
	s, ok := v.(string)
	return ok && utf8.ValidString(s) && len(s) <= max && !strings.ContainsRune(s, 0)
}
func aiNodeValidSetting(key string, v any, tls bool) bool {
	if (tls && !aiTLSFields[key]) || (!tls && !aiNetworkFields[key]) {
		return false
	}
	if v == nil {
		return true
	}
	if tls {
		if !aiTLSFields[key] {
			return false
		}
		switch key {
		case "allow_insecure", "reject_unknown_sni":
			return aiNodeNumber(v, 0, 1)
		case "xver":
			return aiNodeNumber(v, 0, 2)
		case "server_port":
			return aiNodeNumber(v, 1, 65535)
		case "alpn":
			a, ok := v.([]any)
			if !ok || len(a) > 10 {
				return false
			}
			for _, item := range a {
				if !aiNodeText(item, 128, false) {
					return false
				}
			}
			return true
		default:
			return aiNodeText(v, 255, false)
		}
	}
	if !aiNetworkFields[key] {
		return false
	}
	if key == "headers" {
		m, ok := v.(map[string]any)
		if !ok || len(m) > 1 {
			return false
		}
		for k, value := range m {
			if k != "Host" || !aiNodeText(value, 255, true) {
				return false
			}
		}
		return true
	}
	return aiNodeText(v, 2048, false)
}

// ValidateAIManagedNodeChanges rejects wrong protocol fields and arbitrary
// nested configuration before either SQL or backend normalization runs.
func ValidateAIManagedNodeChanges(kind string, changes map[string]any) error {
	allowed := map[string]bool{}
	for _, f := range AIManagedNodeFields(kind) {
		allowed[f] = true
	}
	if len(allowed) == 0 || len(changes) == 0 || len(changes) > 32 {
		return errors.New("invalid node changes")
	}
	for k, v := range changes {
		if !allowed[k] {
			return errors.New("unsupported node field")
		}
		valid := false
		switch k {
		case "tls_settings", "network_settings":
			m, ok := v.(map[string]any)
			valid = ok && len(m) > 0 && len(m) <= 10
			if valid {
				for sub, value := range m {
					if !aiNodeValidSetting(sub, value, k == "tls_settings") {
						valid = false
					}
				}
			}
		case "name", "host":
			valid = aiNodeText(v, 255, false) && strings.TrimSpace(fmt.Sprint(v)) != ""
		case "port", "server_port":
			valid = aiNodeNumber(v, 1, 65535)
		case "show", "client_entry_only", "insecure", "disable_sni", "zero_rtt_handshake", "allow_insecure":
			valid = aiNodeNumber(v, 0, 1)
		case "tls":
			max := float64(2)
			if kind == "vmess" {
				max = 1
			}
			valid = aiNodeNumber(v, 0, max)
		case "sort":
			valid = v == nil || aiNodeNumber(v, 0, 2147483647)
		case "up_mbps", "down_mbps":
			valid = aiNodeNumber(v, 0, 2147483647)
		case "rate":
			if n, ok := v.(float64); ok {
				valid = n >= 0 && n <= 100000 && !math.IsInf(n, 0) && !math.IsNaN(n) && len(strconv.FormatFloat(n, 'f', -1, 64)) <= 11
			}
		case "group_id":
			a, ok := v.([]any)
			valid = ok && len(a) > 0 && len(a) <= 50
			seen := map[float64]bool{}
			for _, item := range a {
				if !aiNodeNumber(item, 1, 2147483647) {
					valid = false
					break
				}
				n := item.(float64)
				if seen[n] {
					valid = false
				}
				seen[n] = true
			}
			raw, _ := json.Marshal(v)
			valid = valid && len(raw) <= 255
		case "tags":
			a, ok := v.([]any)
			valid = ok && len(a) <= 20
			for _, item := range a {
				if !aiNodeText(item, 64, false) {
					valid = false
				}
			}
			raw, _ := json.Marshal(v)
			valid = valid && len(raw) <= 255
		case "network":
			valid = aiNodeText(v, 11, false) && containsString([]string{"tcp", "ws", "grpc", "http", "h2", "kcp", "quic", "httpupgrade", "xhttp", "splithttp"}, fmt.Sprint(v))
		case "flow":
			valid = v == nil || v == "" || v == "xtls-rprx-vision"
		case "listen_ip", "send_through":
			valid = v == nil || v == ""
			if s, ok := v.(string); ok && net.ParseIP(s) != nil {
				valid = true
			}
		case "server_name":
			max := 255
			if containsString([]string{"tuic", "hysteria", "anytls"}, kind) {
				max = 64
			}
			valid = aiNodeText(v, max, true)
		case "cipher":
			valid = aiNodeText(v, 64, false) && strings.TrimSpace(fmt.Sprint(v)) != ""
		case "udp_relay_mode":
			valid = v == nil || v == "native" || v == "quic"
		case "congestion_control":
			valid = v == nil || v == "bbr" || v == "cubic" || v == "new_reno"
		}
		if !valid {
			return errors.New("invalid node field value")
		}
	}
	return nil
}

func aiPublicNodeSettings(value any, tls bool) map[string]any {
	obj, err := aiNodeObject(value)
	if err != nil {
		return map[string]any{}
	}
	if tls {
		if _, ok := obj["server_name"]; !ok {
			obj["server_name"] = obj["serverName"]
		}
		if _, ok := obj["allow_insecure"]; !ok {
			obj["allow_insecure"] = obj["allowInsecure"]
		}
	}
	out := map[string]any{}
	for k, v := range obj {
		allowed := aiNetworkFields[k]
		if tls {
			allowed = aiTLSFields[k]
		}
		if !allowed {
			continue
		}
		if k == "headers" {
			headers, _ := aiNodeObject(v)
			if host, ok := headers["Host"]; ok {
				out[k] = map[string]any{"Host": host}
			}
			continue
		}
		out[k] = v
	}
	return out
}
func AIManagedNodePublic(kind string, row map[string]any) map[string]any {
	out := map[string]any{}
	for _, key := range AIManagedNodeFields(kind) {
		v := row[aiNodeColumn(kind, key)]
		switch key {
		case "tls_settings", "network_settings":
			v = aiPublicNodeSettings(v, key == "tls_settings")
		case "group_id", "tags":
			if s, ok := v.(string); ok {
				var parsed any
				if json.Unmarshal([]byte(s), &parsed) == nil {
					v = parsed
				}
			}
		}
		out[key] = v
	}
	return out
}

// Returned DB values may contain existing secrets, and must never be sent to
// the model or stored in the action payload. before/after are public diffs.
func PrepareAIManagedNodeUpdate(kind string, current, changes map[string]any) (values, before, after map[string]any, err error) {
	if err = ValidateAIManagedNodeChanges(kind, changes); err != nil {
		return
	}
	raw, _ := json.Marshal(current)
	merged := map[string]any{}
	if json.Unmarshal(raw, &merged) != nil {
		err = errors.New("invalid node")
		return
	}
	requested := map[string]bool{}
	for key, v := range changes {
		column := aiNodeColumn(kind, key)
		requested[column] = true
		if key == "tls_settings" || key == "network_settings" {
			var obj map[string]any
			obj, err = aiNodeObject(merged[column])
			if err != nil {
				return
			}
			for sub, value := range v.(map[string]any) {
				if key == "network_settings" && sub == "headers" {
					headers, e := aiNodeObject(obj["headers"])
					if e != nil {
						err = e
						return
					}
					if value == nil {
						delete(headers, "Host")
					} else {
						for h, hv := range value.(map[string]any) {
							if hv == nil {
								delete(headers, h)
							} else {
								headers[h] = hv
							}
						}
					}
					obj[sub] = headers
					continue
				}
				storageKey := sub
				if kind == "vmess" && key == "tls_settings" {
					if sub == "server_name" {
						storageKey = "serverName"
						delete(obj, sub)
					}
					if sub == "allow_insecure" {
						storageKey = "allowInsecure"
						delete(obj, sub)
					}
				}
				if value == nil {
					delete(obj, storageKey)
				} else {
					obj[storageKey] = value
				}
				if kind != "vmess" && key == "tls_settings" && sub == "server_name" {
					delete(obj, "serverName")
				}
				if kind != "vmess" && key == "tls_settings" && sub == "allow_insecure" {
					delete(obj, "allowInsecure")
				}
			}
			merged[column] = obj
		} else {
			if key == "listen_ip" && (v == nil || v == "") {
				v = "0.0.0.0"
			}
			merged[column] = v
		}
	}
	// Do not create hidden key material as a side effect of editing TLS settings.
	if (kind == "vless" || kind == "v2node") && (requested["tls"] || requested["tls_settings"]) {
		if aiNodeNumber(merged["tls"], 2, 2) {
			obj, e := aiNodeObject(merged["tls_settings"])
			if e != nil || !nonEmptyString(obj["private_key"]) || !nonEmptyString(obj["public_key"]) {
				err = errors.New("REALITY keys must be configured in node editor first")
				return
			}
		}
	}
	var normalized map[string]any
	_, normalized, err = normalizeManagedServerSavePayload(kind, merged)
	if err != nil {
		return
	}
	values = map[string]any{}
	for column := range requested {
		if column == "sort" && changes["sort"] == nil {
			values[column] = nil
			continue
		}
		v, ok := normalized[column]
		if !ok {
			err = errors.New("node field was not normalized")
			return
		}
		values[column] = v
	}
	for _, key := range []string{"tls_settings", "network_settings"} {
		column := aiNodeColumn(kind, key)
		if !requested[column] {
			continue
		}
		oldPrivate, e1 := aiNodeProtectedSettings(current[column], key == "tls_settings")
		newPrivate, e2 := aiNodeProtectedSettings(values[column], key == "tls_settings")
		if e1 != nil || e2 != nil || !reflect.DeepEqual(oldPrivate, newPrivate) {
			err = errors.New("node edit would alter stored credentials or unrelated settings")
			return
		}
	}
	// Backend transport rules can clear flow; show that coupled change as well.
	if (kind == "vless" || kind == "v2node") && requested["network"] {
		if v, ok := normalized["flow"]; ok {
			values["flow"] = v
		}
	}
	next := map[string]any{}
	for k, v := range current {
		next[k] = v
	}
	for k, v := range values {
		next[k] = v
	}
	pubBefore, pubAfter := AIManagedNodePublic(kind, current), AIManagedNodePublic(kind, next)
	before = map[string]any{}
	after = map[string]any{}
	for key := range pubBefore {
		oldJSON, _ := json.Marshal(pubBefore[key])
		newJSON, _ := json.Marshal(pubAfter[key])
		if !bytes.Equal(oldJSON, newJSON) {
			before[key] = pubBefore[key]
			after[key] = pubAfter[key]
		}
	}
	if len(after) == 0 {
		err = errors.New("no node changes")
		return
	}
	return
}

func aiNodeProtectedSettings(value any, tls bool) (map[string]any, error) {
	obj, err := aiNodeObject(value)
	if err != nil {
		return nil, err
	}
	allowed := aiNetworkFields
	if tls {
		allowed = aiTLSFields
		delete(obj, "serverName")
		delete(obj, "allowInsecure")
	}
	for key := range allowed {
		if key == "headers" {
			headers, e := aiNodeObject(obj[key])
			if e != nil {
				return nil, e
			}
			delete(headers, "Host")
			if len(headers) > 0 {
				obj[key] = headers
			} else {
				delete(obj, key)
			}
		} else {
			delete(obj, key)
		}
	}
	return obj, nil
}
