package httpapi

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
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

	flag := clientSubscribeFlag(r)
	servers = prependLegacySubscribeInfoServers(cfg, flag, subscribe, servers)

	writeSubscribeMetadataHeaders(w, cfg, subscribe)

	if isClashLikeSubscribeFlag(flag) {
		writeSubscribeDownloadHeaders(w, cfg)
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

	if isShadowrocketSubscribeFlag(flag) {
		writePlainText(w, http.StatusOK, buildShadowrocketPayload(subscribe, servers))
		return true
	}

	writePlainText(w, http.StatusOK, buildGeneralSubscribePayload(subscribe.UUID, servers))
	return true
}

func handleServerV2Config(w http.ResponseWriter, r *http.Request, cfg config.Config, service nodeapi.Service) bool {
	server, ok := lookupNodeServerV2Legacy(w, r, cfg, service)
	if !ok {
		return true
	}

	routes, err := service.Routes(r.Context(), server.RouteIDs)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"status": "fail", "message": err.Error()})
		return true
	}

	return writeJSONWithETag(w, r, buildV2ServerConfig(cfg, server, routes))
}

func lookupNodeServerV2Legacy(w http.ResponseWriter, r *http.Request, cfg config.Config, service nodeapi.Service) (nodeapi.ServerRecord, bool) {
	if service == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "node service unavailable"})
		return nodeapi.ServerRecord{}, false
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"status": "fail", "message": err.Error()})
		return nodeapi.ServerRecord{}, false
	}

	token := strings.TrimSpace(inputs["token"])
	switch {
	case token == "":
		writeJSON(w, http.StatusOK, map[string]any{"status": "fail", "message": "token is null"})
		return nodeapi.ServerRecord{}, false
	case strings.TrimSpace(cfg.ServerToken) == "" || token != strings.TrimSpace(cfg.ServerToken):
		writeJSON(w, http.StatusOK, map[string]any{"status": "fail", "message": "token is error"})
		return nodeapi.ServerRecord{}, false
	}

	nodeID, err := strconv.ParseInt(strings.TrimSpace(inputs["node_id"]), 10, 64)
	if err != nil || nodeID <= 0 {
		writeJSON(w, http.StatusOK, map[string]any{"status": "fail", "message": "server is not exist"})
		return nodeapi.ServerRecord{}, false
	}

	server, err := service.LookupServer(r.Context(), nodeapi.ServerLookupRequest{
		NodeID:   nodeID,
		NodeType: "v2node",
	})
	if err != nil || server.ID <= 0 {
		writeJSON(w, http.StatusOK, map[string]any{"status": "fail", "message": "server is not exist"})
		return nodeapi.ServerRecord{}, false
	}

	return server, true
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

func writeSubscribeMetadataHeaders(w http.ResponseWriter, cfg config.Config, subscribe usersvc.Subscribe) {
	expire := int64(0)
	if subscribe.ExpiredAt != nil {
		expire = *subscribe.ExpiredAt
	}

	w.Header().Set("subscription-userinfo", fmt.Sprintf(
		"upload=%d; download=%d; total=%d; expire=%d",
		subscribe.U,
		subscribe.D,
		subscribe.TransferEnable,
		expire,
	))
	w.Header().Set("profile-update-interval", "24")
	appName := strings.TrimSpace(cfg.AppName)
	if appName == "" {
		appName = "Forest"
	}
	w.Header().Set("profile-title", "base64:"+base64.StdEncoding.EncodeToString([]byte(appName)))
	if strings.TrimSpace(cfg.AppURL) != "" {
		w.Header().Set("profile-web-page-url", strings.TrimSpace(cfg.AppURL))
	}
}

func writeSubscribeDownloadHeaders(w http.ResponseWriter, cfg config.Config) {
	appName := strings.TrimSpace(cfg.AppName)
	if appName == "" {
		appName = "Forest"
	}
	w.Header().Set("content-disposition", "attachment;filename*=UTF-8''"+url.PathEscape(appName))
}

func isClashLikeSubscribeFlag(flag string) bool {
	return strings.Contains(flag, "clash") ||
		strings.Contains(flag, "meta") ||
		strings.Contains(flag, "stash") ||
		strings.Contains(flag, "req-ios") ||
		strings.Contains(flag, "verge") ||
		strings.Contains(flag, "nyanpasu")
}

func isShadowrocketSubscribeFlag(flag string) bool {
	return strings.Contains(flag, "shadowrocket")
}

func prependLegacySubscribeInfoServers(cfg config.Config, flag string, subscribe usersvc.Subscribe, servers []map[string]any) []map[string]any {
	if !cfg.ShowInfoToServerEnable || strings.TrimSpace(flag) == "" || strings.Contains(flag, "sing") || len(servers) == 0 {
		return servers
	}

	template := copyMap(servers[0])
	infoServers := make([]map[string]any, 0, 3)

	remainingTraffic := subscribe.TransferEnable - subscribe.U - subscribe.D
	if remainingTraffic < 0 {
		remainingTraffic = 0
	}
	infoServers = append(infoServers, cloneSubscribeInfoServer(template, "剩余流量："+formatSubscribeTrafficBytes(remainingTraffic)))

	if subscribe.ResetDay != nil && *subscribe.ResetDay > 0 {
		infoServers = append(infoServers, cloneSubscribeInfoServer(template, fmt.Sprintf("距离下次重置剩余：%d 天", *subscribe.ResetDay)))
	}

	expiredDate := "长期有效"
	if subscribe.ExpiredAt != nil && *subscribe.ExpiredAt > 0 {
		expiredDate = formatSubscribeDate(*subscribe.ExpiredAt)
	}
	infoServers = append(infoServers, cloneSubscribeInfoServer(template, "套餐到期："+expiredDate))

	result := make([]map[string]any, 0, len(infoServers)+len(servers))
	result = append(result, infoServers...)
	result = append(result, servers...)
	return result
}

func cloneSubscribeInfoServer(template map[string]any, name string) map[string]any {
	item := copyMap(template)
	item["name"] = name
	return item
}

func buildV2ServerConfig(cfg config.Config, server nodeapi.ServerRecord, routes []map[string]any) map[string]any {
	payload := map[string]any{
		"listen_ip":               fieldString(server, "listen_ip"),
		"server_port":             fieldInt64(server, "server_port"),
		"network":                 fieldString(server, "network"),
		"network_settings":        fieldMap(server, "network_settings"),
		"protocol":                fieldString(server, "protocol"),
		"tls":                     fieldInt64(server, "tls"),
		"tls_settings":            fieldMap(server, "tls_settings"),
		"encryption":              fieldString(server, "encryption"),
		"encryption_settings":     fieldMap(server, "encryption_settings"),
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
