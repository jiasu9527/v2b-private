package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/config"
	"forest/go-api/internal/session"
	usersvc "forest/go-api/internal/user"

	"gopkg.in/yaml.v3"
)

const (
	forestEntryProviderIntervalSec    = 180
	forestEntryHealthcheckURL         = "https://cp.cloudflare.com/generate_204"
	forestEntryHealthcheckIntervalSec = 180
	forestEntryHealthcheckTimeoutMS   = 3000
	fixedClientEntryStrategy          = "ordered-fallback"
)

func handleUserForestRuntimeProfile(w http.ResponseWriter, r *http.Request, cfg config.Config, sessionService session.Service, userService usersvc.Service) bool {
	identity, ok := authenticateRequest(w, r, sessionService, false)
	if !ok {
		return true
	}
	if userService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "user service unavailable"})
		return true
	}

	subscribe, err := userService.Subscribe(r.Context(), identity.ID)
	if err != nil {
		return handleUserError(w, err)
	}
	servers, err := userService.Servers(r.Context(), identity.ID, strings.TrimSpace(r.UserAgent()))
	if err != nil {
		return handleUserError(w, err)
	}
	groups, err := userService.ClientEntryGroups(r.Context(), identity.ID)
	if err != nil {
		return handleUserError(w, err)
	}

	payload := map[string]any{
		"entry_groups": buildForestRuntimeEntryGroups(r, cfg, subscribe.Token, servers, groups),
	}
	if capability := strings.TrimSpace(r.URL.Query().Get("cap")); capability != "" {
		payload["capability"] = capability
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": payload})
	return true
}

func handleClientForestEntryProvider(w http.ResponseWriter, r *http.Request, cfg config.Config, userService usersvc.Service) bool {
	userID, ok := authenticateClientUser(w, r, userService)
	if !ok {
		return true
	}
	if userService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "user service unavailable"})
		return true
	}

	code, err := readInputValue(r, "code")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	code = strings.TrimSpace(code)
	if code == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "code is null"})
		return true
	}

	subscribe, err := userService.Subscribe(r.Context(), userID)
	if err != nil {
		return handleUserError(w, err)
	}
	servers, err := userService.Servers(r.Context(), userID, strings.TrimSpace(r.UserAgent()))
	if err != nil {
		return handleUserError(w, err)
	}
	groups, err := userService.ClientEntryGroups(r.Context(), userID)
	if err != nil {
		return handleUserError(w, err)
	}

	group, ok := findForestEntryGroup(groups, code)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "entry group not found"})
		return true
	}

	memberServers, _ := matchForestEntryGroupServers(servers, group)
	proxies := buildForestEntryProviderProxies(subscribe.UUID, memberServers, group)

	raw, err := yaml.Marshal(map[string]any{"proxies": proxies})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": err.Error()})
		return true
	}
	w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
	return true
}

func handleAdminClientEntryGroupFetch(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	var id *int64
	if raw := strings.TrimSpace(inputs["id"]); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"message": "入口组不存在"})
			return true
		}
		id = &value
	}

	data, err := adminService.ListClientEntryGroups(r.Context(), id)
	if err != nil {
		return handleAdminError(w, err)
	}
	payload := make([]map[string]any, 0, len(data))
	for _, group := range data {
		payload = append(payload, decorateClientEntryGroupForAdminPage(group))
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": payload})
	return true
}

func handleAdminClientEntryGroupSave(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	var payload struct {
		ID              *json.Number `json:"id"`
		Code            string       `json:"code"`
		Name            string       `json:"name"`
		DisplayName     string       `json:"display_name"`
		Remarks         string       `json:"remarks"`
		Strategy        string       `json:"strategy"`
		Action          string       `json:"action"`
		ActionValue     *string      `json:"action_value"`
		HideMemberNodes *bool        `json:"hide_member_nodes"`
		Show            *json.Number `json:"show"`
		Match           []string     `json:"match"`
		IPs             []struct {
			IP   string       `json:"ip"`
			Sort *json.Number `json:"sort"`
		} `json:"ips"`
	}
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		if err := readJSONBody(r, &payload); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
			return true
		}
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	if payload.ID == nil {
		if raw := strings.TrimSpace(inputs["id"]); raw != "" {
			value := json.Number(raw)
			payload.ID = &value
		}
	}
	if strings.TrimSpace(payload.Code) == "" {
		payload.Code = strings.TrimSpace(inputs["code"])
	}
	if strings.TrimSpace(payload.Name) == "" {
		payload.Name = strings.TrimSpace(inputs["name"])
	}
	if strings.TrimSpace(payload.DisplayName) == "" {
		payload.DisplayName = strings.TrimSpace(inputs["display_name"])
	}
	if strings.TrimSpace(payload.Remarks) == "" {
		payload.Remarks = strings.TrimSpace(inputs["remarks"])
	}
	if strings.TrimSpace(payload.Strategy) == "" {
		payload.Strategy = strings.TrimSpace(inputs["strategy"])
	}
	if strings.TrimSpace(payload.Action) == "" {
		payload.Action = strings.TrimSpace(inputs["action"])
	}
	if payload.ActionValue == nil {
		if raw := strings.TrimSpace(inputs["action_value"]); raw != "" {
			payload.ActionValue = &raw
		}
	}
	if payload.HideMemberNodes == nil {
		if raw, ok := inputs["hide_member_nodes"]; ok && strings.TrimSpace(raw) != "" {
			value := parseBoolish(raw)
			payload.HideMemberNodes = &value
		}
	}
	if payload.Show == nil {
		if raw := strings.TrimSpace(inputs["show"]); raw != "" {
			value := json.Number(raw)
			payload.Show = &value
		}
	}
	if len(payload.Match) == 0 {
		payload.Match = indexedStrings(inputs, "match")
	}
	if len(payload.IPs) == 0 {
		for _, entry := range indexedNestedFieldMap(inputs, "ips") {
			if strings.TrimSpace(entry["ip"]) == "" {
				continue
			}
			var sortValue *json.Number
			if raw := strings.TrimSpace(entry["sort"]); raw != "" {
				value := json.Number(raw)
				sortValue = &value
			}
			payload.IPs = append(payload.IPs, struct {
				IP   string       `json:"ip"`
				Sort *json.Number `json:"sort"`
			}{
				IP:   strings.TrimSpace(entry["ip"]),
				Sort: sortValue,
			})
		}
	}

	id, err := jsonNumberToInt64Pointer(payload.ID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "保存失败"})
		return true
	}

	var show *int64
	if payload.Show != nil {
		value, err := jsonNumberToInt64Pointer(payload.Show)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"message": "保存失败"})
			return true
		}
		show = value
	}

	ips := make([]admin.ClientEntryGroupIPSaveRequest, 0, len(payload.IPs))
	for _, item := range payload.IPs {
		sortValue, err := jsonNumberToInt64Pointer(item.Sort)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"message": "保存失败"})
			return true
		}
		ips = append(ips, admin.ClientEntryGroupIPSaveRequest{
			IP:   item.IP,
			Sort: sortValue,
		})
	}
	if len(ips) == 0 {
		for _, raw := range payload.Match {
			ips = append(ips, admin.ClientEntryGroupIPSaveRequest{
				IP: raw,
			})
		}
	}

	code := strings.TrimSpace(payload.Code)
	if code == "" && payload.ActionValue != nil {
		code = strings.TrimSpace(*payload.ActionValue)
	}
	name := strings.TrimSpace(payload.Name)
	if name == "" {
		name = strings.TrimSpace(payload.DisplayName)
	}
	if name == "" {
		name = strings.TrimSpace(payload.Remarks)
	}
	displayName := strings.TrimSpace(payload.DisplayName)
	if displayName == "" {
		displayName = strings.TrimSpace(payload.Remarks)
	}
	if displayName == "" {
		displayName = name
	}
	strategy := strings.TrimSpace(payload.Strategy)
	if strategy == "" {
		strategy = strings.TrimSpace(payload.Action)
	}
	strategy = normalizeClientEntryStrategy(strategy)
	if code == "" {
		code = normalizeClientEntryCode(displayName)
	}

	saved, err := adminService.SaveClientEntryGroup(r.Context(), admin.ClientEntryGroupSaveRequest{
		ID:              id,
		Code:            code,
		Name:            name,
		DisplayName:     displayName,
		Strategy:        strategy,
		HideMemberNodes: payload.HideMemberNodes != nil && *payload.HideMemberNodes,
		Show:            show,
		IPs:             ips,
	})
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": saved})
	return true
}

func handleAdminClientEntryGroupDrop(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}

	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	id, err := strconv.ParseInt(strings.TrimSpace(inputs["id"]), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "入口组不存在"})
		return true
	}

	deleted, err := adminService.DeleteClientEntryGroup(r.Context(), id)
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": deleted})
	return true
}

func buildForestRuntimeEntryGroups(r *http.Request, cfg config.Config, clientToken string, servers []map[string]any, groups []usersvc.ClientEntryGroup) []map[string]any {
	result := make([]map[string]any, 0, len(groups))
	for _, group := range groups {
		memberServers, memberNames := matchForestEntryGroupServers(servers, group)
		if len(memberServers) == 0 || len(group.IPs) == 0 || strings.TrimSpace(clientToken) == "" {
			continue
		}

		name := strings.TrimSpace(group.DisplayName)
		if name == "" {
			name = strings.TrimSpace(group.Name)
		}
		if name == "" || strings.TrimSpace(group.Code) == "" {
			continue
		}

		result = append(result, map[string]any{
			"code":                     strings.TrimSpace(group.Code),
			"name":                     strings.TrimSpace(group.Name),
			"display_name":             name,
			"strategy":                 normalizeClientEntryStrategy(group.Strategy),
			"member_names":             memberNames,
			"hide_member_nodes":        group.HideMemberNodes,
			"provider_url":             buildForestEntryProviderURL(r, cfg, clientToken, group.Code),
			"provider_interval_sec":    forestEntryProviderIntervalSec,
			"healthcheck_url":          forestEntryHealthcheckURL,
			"healthcheck_interval_sec": forestEntryHealthcheckIntervalSec,
			"healthcheck_timeout_ms":   forestEntryHealthcheckTimeoutMS,
			"healthcheck_lazy":         true,
		})
	}
	return result
}

func matchForestEntryGroupServers(servers []map[string]any, group usersvc.ClientEntryGroup) ([]map[string]any, []string) {
	serverIndex := make(map[string]map[string]any, len(servers))
	for _, server := range servers {
		serverType := strings.TrimSpace(fmt.Sprint(server["type"]))
		serverID := int64FromAny(server["id"])
		if serverType == "" || serverID <= 0 {
			continue
		}
		serverIndex[forestEntryServerKey(serverType, serverID)] = server
	}

	result := make([]map[string]any, 0, len(group.Members))
	memberNames := make([]string, 0, len(group.Members))
	seenNames := make(map[string]struct{})
	for _, member := range group.Members {
		server, ok := serverIndex[forestEntryServerKey(member.ServerType, member.ServerID)]
		if !ok {
			continue
		}
		result = append(result, server)
		name := strings.TrimSpace(fmt.Sprint(server["name"]))
		if name == "" {
			continue
		}
		if _, exists := seenNames[name]; exists {
			continue
		}
		seenNames[name] = struct{}{}
		memberNames = append(memberNames, name)
	}
	return result, memberNames
}

func findForestEntryGroup(groups []usersvc.ClientEntryGroup, code string) (usersvc.ClientEntryGroup, bool) {
	target := strings.TrimSpace(code)
	for _, group := range groups {
		if strings.TrimSpace(group.Code) == target {
			return group, true
		}
	}
	return usersvc.ClientEntryGroup{}, false
}

func buildForestEntryProviderURL(r *http.Request, cfg config.Config, clientToken, code string) string {
	rawQuery := "token=" + url.QueryEscape(strings.TrimSpace(clientToken)) + "&code=" + url.QueryEscape(strings.TrimSpace(code))
	return resolveAbsoluteURL(r, cfg, "/api/v1/client/forest/entry-provider", rawQuery)
}

func resolveAbsoluteURL(r *http.Request, cfg config.Config, path, rawQuery string) string {
	baseURL := firstConfiguredSubscribeBaseURL(cfg.SubscribeURL)
	if baseURL == "" {
		baseURL = strings.TrimSpace(cfg.AppURL)
	}
	if baseURL == "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); forwarded != "" {
			scheme = strings.TrimSpace(strings.Split(forwarded, ",")[0])
		}
		host := strings.TrimSpace(r.Header.Get("X-Forwarded-Host"))
		if host == "" {
			host = strings.TrimSpace(r.Host)
		}
		if host == "" {
			host = strings.TrimSpace(r.URL.Host)
		}
		if host != "" {
			baseURL = scheme + "://" + host
		}
	}

	reference := &url.URL{Path: path, RawQuery: rawQuery}
	if baseURL == "" {
		return reference.String()
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return reference.String()
	}
	return parsed.ResolveReference(reference).String()
}

func forestEntryServerKey(serverType string, serverID int64) string {
	return strings.TrimSpace(serverType) + ":" + strconv.FormatInt(serverID, 10)
}

func int64FromAny(value any) int64 {
	switch typed := value.(type) {
	case int64:
		return typed
	case int32:
		return int64(typed)
	case int:
		return int64(typed)
	case float64:
		return int64(typed)
	case json.Number:
		parsed, _ := typed.Int64()
		return parsed
	default:
		parsed, _ := strconv.ParseInt(strings.TrimSpace(fmt.Sprint(value)), 10, 64)
		return parsed
	}
}

func normalizeClientEntryStrategy(value string) string {
	switch strings.TrimSpace(value) {
	case "latency-first", "sticky-low-latency", "ordered-fallback", "":
		return fixedClientEntryStrategy
	default:
		return fixedClientEntryStrategy
	}
}

func decorateClientEntryGroupForAdminPage(group admin.ClientEntryGroupRecord) map[string]any {
	match := make([]string, 0, len(group.IPs))
	for _, item := range group.IPs {
		match = append(match, strings.TrimSpace(item.IP))
	}
	strategy := normalizeClientEntryStrategy(group.Strategy)

	return map[string]any{
		"id":                group.ID,
		"code":              group.Code,
		"name":              group.Name,
		"display_name":      group.DisplayName,
		"strategy":          strategy,
		"hide_member_nodes": group.HideMemberNodes,
		"show":              group.Show,
		"members":           group.Members,
		"ips":               group.IPs,
		"ip_count":          len(group.IPs),
		"created_at":        group.CreatedAt,
		"updated_at":        group.UpdatedAt,
		"remarks":           group.DisplayName,
		"match":             match,
		"action":            strategy,
		"action_value":      group.Code,
	}
}

func parseClientEntryMatchItem(raw string) (string, int64, bool) {
	parts := strings.SplitN(strings.TrimSpace(raw), ":", 2)
	if len(parts) != 2 {
		return "", 0, false
	}
	serverType := strings.TrimSpace(parts[0])
	serverID, err := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
	if serverType == "" || err != nil || serverID <= 0 {
		return "", 0, false
	}
	return serverType, serverID, true
}

func buildForestEntryProviderProxies(userUUID string, servers []map[string]any, group usersvc.ClientEntryGroup) []any {
	proxies := make([]any, 0, len(servers)*len(group.IPs))
	for _, server := range servers {
		for _, item := range group.IPs {
			ip := strings.TrimSpace(item.IP)
			if ip == "" {
				continue
			}
			serverCopy := copyMap(server)
			serverCopy["host"] = ip
			serverCopy["name"] = buildForestEntryVariantName(fmt.Sprint(server["name"]), ip)
			proxy, ok := buildClashStandardProxy(userUUID, serverCopy)
			if !ok {
				continue
			}
			proxies = append(proxies, proxy)
		}
	}
	return proxies
}

func buildForestEntryVariantName(name, ip string) string {
	base := strings.TrimSpace(name)
	if base == "" {
		base = "entry"
	}
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return base
	}
	return base + " @" + ip
}

func normalizeClientEntryCode(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "client-entry"
	}

	var builder strings.Builder
	lastDash := false
	for _, char := range value {
		switch {
		case char >= 'a' && char <= 'z':
			builder.WriteRune(char)
			lastDash = false
		case char >= '0' && char <= '9':
			builder.WriteRune(char)
			lastDash = false
		default:
			if lastDash || builder.Len() == 0 {
				continue
			}
			builder.WriteByte('-')
			lastDash = true
		}
	}
	result := strings.Trim(builder.String(), "-")
	if result == "" {
		return "client-entry"
	}
	return result
}

func enrichManagedServersWithClientEntryGroups(servers []map[string]any, groups []admin.ClientEntryGroupRecord) []map[string]any {
	if len(servers) == 0 || len(groups) == 0 {
		return servers
	}

	byServer := make(map[string][]string)
	byServerID := make(map[string]int64)
	options := make([]map[string]any, 0, len(groups))
	for _, group := range groups {
		label := strings.TrimSpace(group.DisplayName)
		if label == "" {
			label = strings.TrimSpace(group.Name)
		}
		if label == "" {
			continue
		}
		options = append(options, map[string]any{
			"id":   group.ID,
			"name": label,
		})
		for _, member := range group.Members {
			key := forestEntryServerKey(member.ServerType, member.ServerID)
			if stringSliceContains(byServer[key], label) {
				continue
			}
			byServer[key] = append(byServer[key], label)
			if _, exists := byServerID[key]; !exists {
				byServerID[key] = group.ID
			}
		}
	}

	result := make([]map[string]any, 0, len(servers))
	for _, server := range servers {
		serverCopy := copyMap(server)
		key := forestEntryServerKey(fmt.Sprint(serverCopy["type"]), int64FromAny(serverCopy["id"]))
		serverCopy["entry_group_options"] = options
		if names := byServer[key]; len(names) > 0 {
			serverCopy["entry_group_names"] = names
			serverCopy["entry_group_id"] = byServerID[key]
		} else {
			serverCopy["entry_group_names"] = []string{}
			serverCopy["entry_group_id"] = nil
		}
		result = append(result, serverCopy)
	}
	return result
}

func stringSliceContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
