package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"forest/go-api/internal/config"
	"forest/go-api/internal/nodeapi"
	usersvc "forest/go-api/internal/user"
)

func handleClientAppGetVersion(w http.ResponseWriter, r *http.Request, cfg config.Config) bool {
	ua := strings.ToLower(strings.TrimSpace(r.UserAgent()))
	if strings.Contains(ua, "tidalab/4.0.0") || strings.Contains(ua, "tunnelab/4.0.0") {
		data := map[string]any{
			"version":      cfg.MacOSVersion,
			"download_url": cfg.MacOSDownloadURL,
		}
		if strings.Contains(ua, "win64") {
			data["version"] = cfg.WindowsVersion
			data["download_url"] = cfg.WindowsDownloadURL
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": data})
		return true
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"data": map[string]any{
			"windows_version":      cfg.WindowsVersion,
			"windows_download_url": cfg.WindowsDownloadURL,
			"macos_version":        cfg.MacOSVersion,
			"macos_download_url":   cfg.MacOSDownloadURL,
			"android_version":      cfg.AndroidVersion,
			"android_download_url": cfg.AndroidDownloadURL,
		},
	})
	return true
}

func handleClientAppGetConfig(w http.ResponseWriter, r *http.Request, cfg config.Config, service usersvc.Service) bool {
	userID, ok := authenticateClientUser(w, r, service)
	if !ok {
		return true
	}

	subscribe, err := service.Subscribe(r.Context(), userID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": err.Error()})
		return true
	}

	servers, err := service.Servers(r.Context(), userID, strings.TrimSpace(r.UserAgent()))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": err.Error()})
		return true
	}

	body, err := buildClashStandardProfile(cfg, "custom.app.clash.yaml", "app.clash.yaml", subscribe.UUID, servers)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": err.Error()})
		return true
	}

	w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
	return true
}

func handleClientSubscribe(w http.ResponseWriter, r *http.Request, cfg config.Config, service usersvc.Service) bool {
	userID, ok := authenticateClientUser(w, r, service)
	if !ok {
		return true
	}

	subscribe, err := service.Subscribe(r.Context(), userID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": err.Error()})
		return true
	}

	servers, err := service.Servers(r.Context(), userID, strings.TrimSpace(r.UserAgent()))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": err.Error()})
		return true
	}

	writeSubscribeHeaders(w, cfg, subscribe)

	flag := clientSubscribeFlag(r)
	if strings.Contains(flag, "clash") || strings.Contains(flag, "meta") || strings.Contains(flag, "stash") {
		body, err := buildClashStandardProfile(cfg, "custom.clash.yaml", "default.clash.yaml", subscribe.UUID, servers)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"message": err.Error()})
			return true
		}
		w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
		return true
	}

	writePlainText(w, http.StatusOK, buildGeneralSubscribePayload(subscribe.UUID, servers))
	return true
}

func handleServerV2Config(w http.ResponseWriter, r *http.Request, cfg config.Config, service nodeapi.Service) bool {
	server, ok := lookupNodeServer(w, r, cfg, service, "v2node")
	if !ok {
		return true
	}

	routes, err := service.Routes(r.Context(), server.RouteIDs)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": err.Error()})
		return true
	}

	return writeJSONWithETag(w, r, buildV2ServerConfig(cfg, server, routes))
}

func authenticateClientUser(w http.ResponseWriter, r *http.Request, service usersvc.Service) (int64, bool) {
	if service == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "user service unavailable"})
		return 0, false
	}

	token, err := readInputValue(r, "token")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return 0, false
	}
	token = strings.TrimSpace(token)
	if token == "" {
		writeJSON(w, http.StatusForbidden, map[string]any{"message": "token is null"})
		return 0, false
	}

	userID, err := service.ResolveClientUserID(r.Context(), token)
	if err != nil {
		if errors.Is(err, usersvc.ErrClientTokenInvalid) || errors.Is(err, usersvc.ErrNotFound) {
			writeJSON(w, http.StatusForbidden, map[string]any{"message": "token is error"})
			return 0, false
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": err.Error()})
		return 0, false
	}
	if userID <= 0 {
		writeJSON(w, http.StatusForbidden, map[string]any{"message": "token is error"})
		return 0, false
	}
	return userID, true
}

func clientSubscribeFlag(r *http.Request) string {
	flag := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("flag")))
	if flag != "" {
		return flag
	}
	return strings.ToLower(strings.TrimSpace(r.UserAgent()))
}

func writeSubscribeHeaders(w http.ResponseWriter, cfg config.Config, subscribe usersvc.Subscribe) {
	expire := int64(0)
	if subscribe.ExpiredAt != nil {
		expire = *subscribe.ExpiredAt
	}

	appName := strings.TrimSpace(cfg.AppName)
	if appName == "" {
		appName = "V2Board"
	}

	w.Header().Set("subscription-userinfo", fmt.Sprintf(
		"upload=%d; download=%d; total=%d; expire=%d",
		subscribe.U,
		subscribe.D,
		subscribe.TransferEnable,
		expire,
	))
	w.Header().Set("profile-update-interval", "24")
	w.Header().Set("content-disposition", "attachment;filename*=UTF-8''"+url.PathEscape(appName))
	if strings.TrimSpace(cfg.AppURL) != "" {
		w.Header().Set("profile-web-page-url", strings.TrimSpace(cfg.AppURL))
	}
}

func buildV2ServerConfig(cfg config.Config, server nodeapi.ServerRecord, routes []map[string]any) map[string]any {
	payload := map[string]any{
		"listen_ip":               fieldString(server, "listen_ip"),
		"server_port":             fieldInt64(server, "server_port"),
		"network":                 fieldString(server, "network"),
		"network_settings":        fieldMap(server, "network_settings"),
		"protocol":                fieldString(server, "protocol"),
		"tls":                     fieldInt64(server, "tls"),
		"tls_settings":            stripPrivateKey(fieldMap(server, "tls_settings")),
		"encryption":              fieldString(server, "encryption"),
		"encryption_settings":     stripPrivateKey(fieldMap(server, "encryption_settings")),
		"flow":                    fieldString(server, "flow"),
		"cipher":                  fieldString(server, "cipher"),
		"congestion_control":      fieldString(server, "congestion_control"),
		"zero_rtt_handshake":      fieldBool(server, "zero_rtt_handshake"),
		"up_mbps":                 fieldInt64(server, "up_mbps"),
		"down_mbps":               fieldInt64(server, "down_mbps"),
		"obfs":                    fieldString(server, "obfs"),
		"obfs_password":           fieldString(server, "obfs_password"),
		"padding_scheme":          server.Fields["padding_scheme"],
		"ignore_client_bandwidth": fieldInt64(server, "up_mbps") == 0 && fieldInt64(server, "down_mbps") == 0,
		"base_config": map[string]any{
			"push_interval":             defaultInt64(cfg.ServerPushInterval, 60),
			"pull_interval":             defaultInt64(cfg.ServerPullInterval, 60),
			"node_report_min_traffic":   cfg.ServerNodeReportMinTraffic,
			"device_online_min_traffic": cfg.ServerDeviceOnlineMinTraffic,
		},
	}

	switch fieldString(server, "cipher") {
	case "2022-blake3-aes-128-gcm":
		payload["server_key"] = nodeapi.ServerKey(fieldInt64(server, "created_at"), 16)
	case "2022-blake3-aes-256-gcm":
		payload["server_key"] = nodeapi.ServerKey(fieldInt64(server, "created_at"), 32)
	}

	if len(routes) > 0 {
		payload["routes"] = routes
	}
	return payload
}

func defaultInt64(value, fallback int64) int64 {
	if value <= 0 {
		return fallback
	}
	return value
}
