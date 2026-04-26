package addrutil

import (
	"net"
	"strings"
)

func IsIPOrHostname(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	if isIPAddress(raw) {
		return true
	}
	return isHostname(raw)
}

func isIPAddress(raw string) bool {
	return net.ParseIP(raw) != nil
}

func isHostname(raw string) bool {
	raw = strings.TrimSuffix(strings.TrimSpace(raw), ".")
	if raw == "" || len(raw) > 253 {
		return false
	}
	labels := strings.Split(raw, ".")
	for _, label := range labels {
		if !isHostnameLabel(label) {
			return false
		}
	}
	return true
}

func isHostnameLabel(label string) bool {
	if label == "" || len(label) > 63 {
		return false
	}
	for index, char := range label {
		switch {
		case char >= 'a' && char <= 'z':
		case char >= 'A' && char <= 'Z':
		case char >= '0' && char <= '9':
		case char == '-':
			if index == 0 || index == len(label)-1 {
				return false
			}
		default:
			return false
		}
	}
	return true
}
