package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/cliententry"
	"forest/go-api/internal/config"
	"forest/go-api/internal/platform/addrutil"
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

func handleUserForestRuntimeProfile(w http.ResponseWriter, r *http.Request, cfg config.Config, sessionService session.Service, userService usersvc.Service, resolver clientEntryRemoteResolver) bool {
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
		"entry_groups": buildForestRuntimeEntryGroups(r.Context(), r, cfg, subscribe.Token, servers, groups, resolver),
	}
	if capability := strings.TrimSpace(r.URL.Query().Get("cap")); capability != "" {
		payload["capability"] = capability
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": payload})
	return true
}

func handleClientForestEntryProvider(w http.ResponseWriter, r *http.Request, cfg config.Config, userService usersvc.Service, resolver clientEntryRemoteResolver) bool {
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
	proxies := buildForestEntryProviderProxies(r.Context(), subscribe.UUID, memberServers, group, resolver)

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

func handleAdminClientEntryGroupFetch(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service, resolver clientEntryRemoteResolver) bool {
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
		payload = append(payload, buildAdminClientEntryGroupPayload(r.Context(), group, resolver, false))
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": payload})
	return true
}

func handleAdminClientEntryGroupResolve(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service, resolver clientEntryRemoteResolver) bool {
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
	rawID := strings.TrimSpace(inputs["id"])
	id, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "入口组不存在"})
		return true
	}

	data, err := adminService.ListClientEntryGroups(r.Context(), &id)
	if err != nil {
		return handleAdminError(w, err)
	}
	if len(data) == 0 {
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "入口组不存在"})
		return true
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"data": buildAdminClientEntryGroupPayload(r.Context(), data[0], resolver, true),
	})
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
		ID                 *json.Number `json:"id"`
		Code               string       `json:"code"`
		Name               string       `json:"name"`
		DisplayName        string       `json:"display_name"`
		Remarks            string       `json:"remarks"`
		Strategy           string       `json:"strategy"`
		Action             string       `json:"action"`
		ActionValue        *string      `json:"action_value"`
		HideMemberNodes    *bool        `json:"hide_member_nodes"`
		Show               *json.Number `json:"show"`
		RemoteEnabled      *bool        `json:"remote_enabled"`
		RemoteHost         string       `json:"remote_host"`
		RemoteSSHPort      *json.Number `json:"remote_ssh_port"`
		RemoteSSHUser      string       `json:"remote_ssh_user"`
		RemoteSSHPassword  string       `json:"remote_ssh_password"`
		RemoteGroupRef     string       `json:"remote_group_ref"`
		RemoteExcludeNames []string     `json:"remote_exclude_names"`
		RemoteRefreshSec   *json.Number `json:"remote_refresh_sec"`
		Match              []string     `json:"match"`
		Members            []struct {
			ServerType string       `json:"server_type"`
			ServerID   *json.Number `json:"server_id"`
			Sort       *json.Number `json:"sort"`
		} `json:"members"`
		IPs []struct {
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
	if payload.RemoteEnabled == nil {
		if raw, ok := inputs["remote_enabled"]; ok && strings.TrimSpace(raw) != "" {
			value := parseBoolish(raw)
			payload.RemoteEnabled = &value
		}
	}
	if strings.TrimSpace(payload.RemoteHost) == "" {
		payload.RemoteHost = strings.TrimSpace(inputs["remote_host"])
	}
	if payload.RemoteSSHPort == nil {
		if raw := strings.TrimSpace(inputs["remote_ssh_port"]); raw != "" {
			value := json.Number(raw)
			payload.RemoteSSHPort = &value
		}
	}
	if strings.TrimSpace(payload.RemoteSSHUser) == "" {
		payload.RemoteSSHUser = strings.TrimSpace(inputs["remote_ssh_user"])
	}
	if strings.TrimSpace(payload.RemoteSSHPassword) == "" {
		payload.RemoteSSHPassword = strings.TrimSpace(inputs["remote_ssh_password"])
	}
	if strings.TrimSpace(payload.RemoteGroupRef) == "" {
		payload.RemoteGroupRef = strings.TrimSpace(inputs["remote_group_ref"])
	}
	if len(payload.RemoteExcludeNames) == 0 {
		payload.RemoteExcludeNames = splitClientEntryTextList(strings.TrimSpace(inputs["remote_exclude_names"]))
		if len(payload.RemoteExcludeNames) == 0 {
			payload.RemoteExcludeNames = indexedStrings(inputs, "remote_exclude_names")
		}
	}
	if payload.RemoteRefreshSec == nil {
		if raw := strings.TrimSpace(inputs["remote_refresh_sec"]); raw != "" {
			value := json.Number(raw)
			payload.RemoteRefreshSec = &value
		}
	}
	if len(payload.Match) == 0 {
		payload.Match = indexedStrings(inputs, "match")
	}
	if len(payload.Members) == 0 {
		for _, entry := range indexedNestedFieldMap(inputs, "members") {
			serverType := strings.TrimSpace(entry["server_type"])
			serverIDRaw := strings.TrimSpace(entry["server_id"])
			if serverType == "" || serverIDRaw == "" {
				continue
			}
			serverID := json.Number(serverIDRaw)
			var sortValue *json.Number
			if raw := strings.TrimSpace(entry["sort"]); raw != "" {
				value := json.Number(raw)
				sortValue = &value
			}
			payload.Members = append(payload.Members, struct {
				ServerType string       `json:"server_type"`
				ServerID   *json.Number `json:"server_id"`
				Sort       *json.Number `json:"sort"`
			}{
				ServerType: serverType,
				ServerID:   &serverID,
				Sort:       sortValue,
			})
		}
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
	var remoteSSHPort int64
	if payload.RemoteSSHPort != nil {
		value, err := jsonNumberToInt64Pointer(payload.RemoteSSHPort)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"message": "保存失败"})
			return true
		}
		if value != nil {
			remoteSSHPort = *value
		}
	}
	var remoteRefreshSec int64
	if payload.RemoteRefreshSec != nil {
		value, err := jsonNumberToInt64Pointer(payload.RemoteRefreshSec)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"message": "保存失败"})
			return true
		}
		if value != nil {
			remoteRefreshSec = *value
		}
	}

	members := make([]admin.ClientEntryGroupMemberSaveRequest, 0, len(payload.Members))
	for _, item := range payload.Members {
		serverID, err := jsonNumberToInt64Pointer(item.ServerID)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"message": "保存失败"})
			return true
		}
		if serverID == nil || *serverID <= 0 {
			continue
		}
		sortValue, err := jsonNumberToInt64Pointer(item.Sort)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"message": "保存失败"})
			return true
		}
		members = append(members, admin.ClientEntryGroupMemberSaveRequest{
			ServerType: strings.TrimSpace(item.ServerType),
			ServerID:   *serverID,
			Sort:       sortValue,
		})
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
		ID:                 id,
		Code:               code,
		Name:               name,
		DisplayName:        displayName,
		Strategy:           strategy,
		HideMemberNodes:    payload.HideMemberNodes != nil && *payload.HideMemberNodes,
		Show:               show,
		RemoteEnabled:      payload.RemoteEnabled != nil && *payload.RemoteEnabled,
		RemoteHost:         payload.RemoteHost,
		RemoteSSHPort:      remoteSSHPort,
		RemoteSSHUser:      payload.RemoteSSHUser,
		RemoteSSHPassword:  payload.RemoteSSHPassword,
		RemoteGroupRef:     payload.RemoteGroupRef,
		RemoteExcludeNames: payload.RemoteExcludeNames,
		RemoteRefreshSec:   remoteRefreshSec,
		Members:            members,
		IPs:                ips,
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

func buildForestRuntimeEntryGroups(ctx context.Context, r *http.Request, cfg config.Config, clientToken string, servers []map[string]any, groups []usersvc.ClientEntryGroup, resolver clientEntryRemoteResolver) []map[string]any {
	result := make([]map[string]any, 0, len(groups))
	for _, group := range groups {
		memberServers, memberNames := matchForestEntryGroupServers(servers, group)
		effectiveIPs := effectiveClientEntryGroupIPs(ctx, group, resolver)
		if len(memberServers) == 0 || len(effectiveIPs) == 0 || strings.TrimSpace(clientToken) == "" {
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
		"id":                   group.ID,
		"code":                 group.Code,
		"name":                 group.Name,
		"display_name":         group.DisplayName,
		"strategy":             strategy,
		"hide_member_nodes":    group.HideMemberNodes,
		"show":                 group.Show,
		"remote_enabled":       group.RemoteEnabled,
		"remote_host":          group.RemoteHost,
		"remote_ssh_port":      group.RemoteSSHPort,
		"remote_ssh_user":      group.RemoteSSHUser,
		"remote_ssh_password":  group.RemoteSSHPassword,
		"remote_group_ref":     group.RemoteGroupRef,
		"remote_exclude_names": group.RemoteExcludeNames,
		"remote_refresh_sec":   group.RemoteRefreshSec,
		"members":              group.Members,
		"ips":                  group.IPs,
		"ip_count":             len(group.IPs),
		"created_at":           group.CreatedAt,
		"updated_at":           group.UpdatedAt,
		"remarks":              group.DisplayName,
		"match":                match,
		"action":               strategy,
		"action_value":         group.Code,
	}
}

func buildAdminClientEntryGroupPayload(ctx context.Context, group admin.ClientEntryGroupRecord, resolver clientEntryRemoteResolver, forceRefresh bool) map[string]any {
	payload := decorateClientEntryGroupForAdminPage(group)
	staticEntries := sanitizeClientEntryPreviewValues(payload["match"])
	remoteEntries := make([]string, 0)
	remoteError := ""

	if group.RemoteEnabled && resolver != nil {
		values, err := resolveClientEntryGroupPreviewRemoteIPs(ctx, resolver, adminClientEntryGroupRecordToUserGroup(group), forceRefresh)
		if err != nil {
			remoteError = err.Error()
		} else {
			remoteEntries = sanitizeClientEntryPreviewValues(values)
		}
	}

	effectiveEntries := mergeClientEntryPreviewValues(staticEntries, remoteEntries)
	payload["static_entries"] = staticEntries
	payload["static_entry_count"] = len(staticEntries)
	payload["remote_resolved_ips"] = remoteEntries
	payload["remote_resolved_count"] = len(remoteEntries)
	payload["remote_resolve_error"] = remoteError
	payload["effective_entries"] = effectiveEntries
	payload["effective_entry_count"] = len(effectiveEntries)
	return payload
}

func resolveClientEntryGroupPreviewRemoteIPs(ctx context.Context, resolver clientEntryRemoteResolver, group usersvc.ClientEntryGroup, forceRefresh bool) ([]string, error) {
	if resolver == nil {
		return nil, nil
	}
	if forceRefresh {
		if refresher, ok := resolver.(clientEntryRemoteRefresher); ok {
			return refresher.RefreshRemoteIPs(ctx, group)
		}
	}
	return resolver.ResolveRemoteIPs(ctx, group)
}

func adminClientEntryGroupRecordToUserGroup(group admin.ClientEntryGroupRecord) usersvc.ClientEntryGroup {
	result := usersvc.ClientEntryGroup{
		ID:                 group.ID,
		Code:               strings.TrimSpace(group.Code),
		Name:               strings.TrimSpace(group.Name),
		DisplayName:        strings.TrimSpace(group.DisplayName),
		Strategy:           normalizeClientEntryStrategy(group.Strategy),
		HideMemberNodes:    group.HideMemberNodes,
		RemoteEnabled:      group.RemoteEnabled,
		RemoteHost:         strings.TrimSpace(group.RemoteHost),
		RemoteSSHPort:      group.RemoteSSHPort,
		RemoteSSHUser:      strings.TrimSpace(group.RemoteSSHUser),
		RemoteSSHPassword:  strings.TrimSpace(group.RemoteSSHPassword),
		RemoteGroupRef:     strings.TrimSpace(group.RemoteGroupRef),
		RemoteExcludeNames: append([]string(nil), group.RemoteExcludeNames...),
		RemoteRefreshSec:   group.RemoteRefreshSec,
		Members:            make([]usersvc.ClientEntryGroupMember, 0, len(group.Members)),
		IPs:                make([]usersvc.ClientEntryGroupIP, 0, len(group.IPs)),
	}
	for _, member := range group.Members {
		result.Members = append(result.Members, usersvc.ClientEntryGroupMember{
			ServerType: strings.TrimSpace(member.ServerType),
			ServerID:   member.ServerID,
			Sort:       member.Sort,
		})
	}
	for _, item := range group.IPs {
		result.IPs = append(result.IPs, usersvc.ClientEntryGroupIP{
			IP:   strings.TrimSpace(item.IP),
			Sort: item.Sort,
		})
	}
	return result
}

func sanitizeClientEntryPreviewValues(raw any) []string {
	seen := make(map[string]struct{})
	result := make([]string, 0)
	appendValue := func(value string) {
		value = strings.TrimSpace(value)
		if !addrutil.IsIPOrHostname(value) {
			return
		}
		if _, ok := seen[value]; ok {
			return
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	switch values := raw.(type) {
	case []string:
		for _, value := range values {
			appendValue(value)
		}
	case []any:
		for _, value := range values {
			appendValue(fmt.Sprint(value))
		}
	}
	return result
}

func mergeClientEntryPreviewValues(staticEntries, remoteEntries []string) []string {
	result := make([]string, 0, len(staticEntries)+len(remoteEntries))
	seen := make(map[string]struct{}, len(staticEntries)+len(remoteEntries))
	appendValue := func(value string) {
		value = strings.TrimSpace(value)
		if !addrutil.IsIPOrHostname(value) {
			return
		}
		if _, ok := seen[value]; ok {
			return
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	for _, value := range staticEntries {
		appendValue(value)
	}
	for _, value := range remoteEntries {
		appendValue(value)
	}
	return result
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

func buildForestEntryProviderProxies(ctx context.Context, userUUID string, servers []map[string]any, group usersvc.ClientEntryGroup, resolver clientEntryRemoteResolver) []any {
	effectiveIPs := effectiveClientEntryGroupIPs(ctx, group, resolver)
	proxies := make([]any, 0, len(servers)*len(effectiveIPs))
	for _, server := range servers {
		for _, item := range effectiveIPs {
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

func effectiveClientEntryGroupIPs(ctx context.Context, group usersvc.ClientEntryGroup, resolver clientEntryRemoteResolver) []usersvc.ClientEntryGroupIP {
	result := make([]usersvc.ClientEntryGroupIP, 0, len(group.IPs)+2)
	seen := make(map[string]struct{}, len(group.IPs)+2)
	appendIP := func(ip string, sort *int64) {
		ip = strings.TrimSpace(ip)
		if !addrutil.IsIPOrHostname(ip) {
			return
		}
		if _, exists := seen[ip]; exists {
			return
		}
		seen[ip] = struct{}{}
		result = append(result, usersvc.ClientEntryGroupIP{IP: ip, Sort: sort})
	}
	for _, item := range group.IPs {
		appendIP(item.IP, item.Sort)
	}
	if !group.RemoteEnabled || resolver == nil {
		return result
	}
	remoteIPs, err := resolver.ResolveRemoteIPs(ctx, group)
	if err != nil {
		return result
	}
	for _, ip := range remoteIPs {
		appendIP(ip, nil)
	}
	return result
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

func splitClientEntryTextList(raw string) []string {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.ReplaceAll(raw, ",", "\n")
	parts := strings.Split(raw, "\n")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		result = append(result, part)
	}
	return result
}

func splitClientEntryLines(raw string) []string {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	parts := strings.Split(raw, "\n")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			result = append(result, part)
		}
	}
	return result
}

func handleAdminClientEntryUserPolicyFetch(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}
	data, err := adminService.ListClientEntryUserPolicies(r.Context())
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
	return true
}

func handleAdminClientEntryUserPolicySave(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}
	var payload struct {
		ID                 *json.Number            `json:"id"`
		Name               string                  `json:"name"`
		Action             string                  `json:"action"`
		Conditions         []cliententry.Condition `json:"conditions"`
		EntryHost          string                  `json:"entry_host"`
		ResolveEntryHost   *json.Number            `json:"resolve_entry_host"`
		ExtraNodes         []string                `json:"extra_nodes"`
		ExtraNodesPosition string                  `json:"extra_nodes_position"`
		Members            []struct {
			ServerType string       `json:"server_type"`
			ServerID   *json.Number `json:"server_id"`
			Sort       *json.Number `json:"sort"`
		} `json:"members"`
		Enabled *json.Number `json:"enabled"`
		Remarks string       `json:"remarks"`
	}
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		if err := readJSONBody(r, &payload); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
			return true
		}
	} else {
		inputs, err := readInputs(r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
			return true
		}
		if raw := strings.TrimSpace(inputs["id"]); raw != "" {
			v := json.Number(raw)
			payload.ID = &v
		}
		payload.Name = strings.TrimSpace(inputs["name"])
		payload.Action = strings.TrimSpace(inputs["action"])
		payload.EntryHost = strings.TrimSpace(inputs["entry_host"])
		if raw := strings.TrimSpace(inputs["resolve_entry_host"]); raw != "" {
			v := json.Number(raw)
			payload.ResolveEntryHost = &v
		}
		payload.ExtraNodes = splitClientEntryLines(inputs["extra_nodes"])
		payload.ExtraNodesPosition = strings.TrimSpace(inputs["extra_nodes_position"])
		if raw := strings.TrimSpace(inputs["conditions"]); raw != "" {
			if err := json.Unmarshal([]byte(raw), &payload.Conditions); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"message": "匹配条件格式无效"})
				return true
			}
		}
		if len(payload.Members) == 0 {
			for _, entry := range indexedNestedFieldMap(inputs, "members") {
				serverType := strings.TrimSpace(entry["server_type"])
				serverIDRaw := strings.TrimSpace(entry["server_id"])
				if serverType == "" || serverIDRaw == "" {
					continue
				}
				serverID := json.Number(serverIDRaw)
				var sortValue *json.Number
				if raw := strings.TrimSpace(entry["sort"]); raw != "" {
					value := json.Number(raw)
					sortValue = &value
				}
				payload.Members = append(payload.Members, struct {
					ServerType string       `json:"server_type"`
					ServerID   *json.Number `json:"server_id"`
					Sort       *json.Number `json:"sort"`
				}{ServerType: serverType, ServerID: &serverID, Sort: sortValue})
			}
		}
		if raw := strings.TrimSpace(inputs["enabled"]); raw != "" {
			v := json.Number(raw)
			payload.Enabled = &v
		}
		payload.Remarks = strings.TrimSpace(inputs["remarks"])
	}
	id, err := jsonNumberToInt64Pointer(payload.ID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "保存失败"})
		return true
	}
	enabled, err := jsonNumberToInt64Pointer(payload.Enabled)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "保存失败"})
		return true
	}
	resolveEntryHost, err := jsonNumberToInt64Pointer(payload.ResolveEntryHost)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "保存失败"})
		return true
	}
	members := make([]admin.ClientEntryGroupMemberSaveRequest, 0, len(payload.Members))
	for _, member := range payload.Members {
		serverID, err := jsonNumberToInt64Pointer(member.ServerID)
		if err != nil || serverID == nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"message": "生效节点不能为空"})
			return true
		}
		var sortValue *int64
		if member.Sort != nil {
			sortValue, err = jsonNumberToInt64Pointer(member.Sort)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"message": "保存失败"})
				return true
			}
		}
		members = append(members, admin.ClientEntryGroupMemberSaveRequest{ServerType: member.ServerType, ServerID: *serverID, Sort: sortValue})
	}
	saved, err := adminService.SaveClientEntryUserPolicy(r.Context(), admin.ClientEntryUserPolicySaveRequest{
		ID: id, Name: payload.Name, Action: payload.Action, Conditions: payload.Conditions, EntryHost: payload.EntryHost, ResolveEntryHost: resolveEntryHost, ExtraNodes: payload.ExtraNodes, ExtraNodesPosition: payload.ExtraNodesPosition, Members: members, Enabled: enabled, Remarks: payload.Remarks,
	})
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": saved})
	return true
}

func handleAdminClientEntryUserPolicySort(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}
	var payload struct {
		IDs []json.Number `json:"ids"`
	}
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		if err := readJSONBody(r, &payload); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
			return true
		}
	} else {
		inputs, err := readInputs(r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
			return true
		}
		for _, raw := range strings.FieldsFunc(inputs["ids"], func(r rune) bool { return r == ',' || r == ';' || r == '\n' || r == '\r' || r == ' ' || r == '\t' }) {
			payload.IDs = append(payload.IDs, json.Number(raw))
		}
	}
	ids := make([]int64, 0, len(payload.IDs))
	for _, raw := range payload.IDs {
		id, err := strconv.ParseInt(strings.TrimSpace(raw.String()), 10, 64)
		if err != nil || id <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"message": "规则顺序无效"})
			return true
		}
		ids = append(ids, id)
	}
	sorted, err := adminService.SortClientEntryUserPolicies(r.Context(), ids)
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": sorted})
	return true
}

func handleAdminClientEntryUserPolicyDrop(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "分配不存在"})
		return true
	}
	deleted, err := adminService.DeleteClientEntryUserPolicy(r.Context(), id)
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": deleted})
	return true
}
