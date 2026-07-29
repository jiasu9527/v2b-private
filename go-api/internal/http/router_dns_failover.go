package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/session"
)

const maxDNSFailoverJSONBody = 1 << 20

func handleAdminDNSFailover(w http.ResponseWriter, r *http.Request, sessions session.Service, service admin.Service, path string) bool {
	if _, ok := authenticateRequest(w, r, sessions, true); !ok {
		return true
	}
	if service == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "管理服务暂不可用"})
		return true
	}
	parts := splitDNSFailoverPath(path)
	if len(parts) == 1 && parts[0] == "settings" {
		return handleDNSFailoverSettings(w, r, service)
	}
	if len(parts) == 1 && parts[0] == "probes" {
		return handleDNSFailoverProbes(w, r, service)
	}
	if len(parts) == 2 && parts[0] == "probes" {
		return handleDNSFailoverProbeDelete(w, r, service, parts[1])
	}
	if len(parts) == 3 && parts[0] == "probes" && (parts[2] == "enabled" || parts[2] == "revoke") {
		return handleDNSFailoverProbeMutation(w, r, service, parts[1], parts[2])
	}
	if len(parts) == 1 && parts[0] == "rules" {
		return handleDNSFailoverRules(w, r, service)
	}
	if len(parts) == 2 && parts[0] == "rules" {
		return handleDNSFailoverRule(w, r, service, parts[1])
	}
	if len(parts) == 3 && parts[0] == "rules" && parts[2] == "status" {
		return handleDNSFailoverRuleStatus(w, r, service, parts[1])
	}
	if len(parts) == 3 && parts[0] == "rules" && (parts[2] == "enabled" || parts[2] == "manual-switch") {
		return handleDNSFailoverRuleMutation(w, r, service, parts[1], parts[2])
	}
	if len(parts) == 1 && parts[0] == "events" {
		return handleDNSFailoverEvents(w, r, service)
	}
	if len(parts) == 1 && parts[0] == "logs" {
		return handleDNSFailoverLogs(w, r, service)
	}
	if len(parts) == 1 && parts[0] == "entry-monitors" {
		return handleClientEntryMonitors(w, r, service)
	}
	if len(parts) == 2 && parts[0] == "entry-monitors" && parts[1] == "run" {
		return handleClientEntryMonitorRun(w, r, service)
	}
	if len(parts) == 2 && parts[0] == "entry-monitors" && parts[1] == "runs" {
		return handleClientEntryMonitorRuns(w, r, service)
	}
	writeJSON(w, http.StatusNotFound, map[string]any{"message": "DNS 故障转移接口不存在"})
	return true
}

func handleDNSFailoverRuleStatus(w http.ResponseWriter, r *http.Request, service admin.Service, rawID string) bool {
	if r.Method != http.MethodGet {
		return dnsFailoverMethodNotAllowed(w, r, http.MethodGet)
	}
	id, ok := dnsFailoverPositiveID(w, rawID, "规则 ID")
	if !ok {
		return true
	}
	status, err := service.GetDNSFailoverStatus(r.Context(), id)
	if err != nil {
		return writeDNSFailoverError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": status})
	return true
}

func splitDNSFailoverPath(path string) []string {
	return strings.FieldsFunc(strings.Trim(path, "/"), func(r rune) bool { return r == '/' })
}

func handleDNSFailoverSettings(w http.ResponseWriter, r *http.Request, service admin.Service) bool {
	switch r.Method {
	case http.MethodGet:
		result, err := service.GetDNSFailoverSettings(r.Context())
		if err != nil {
			return writeDNSFailoverError(w, err)
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": result})
	case http.MethodPut:
		var input struct {
			ProbeAPIURL string `json:"dns_probe_api_url"`
		}
		if !decodeStrictDNSFailoverJSON(w, r, &input) {
			return true
		}
		result, err := service.SaveDNSFailoverSettings(r.Context(), admin.DNSFailoverSettingsSaveRequest{ProbeAPIURL: input.ProbeAPIURL})
		if err != nil {
			return writeDNSFailoverError(w, err)
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": result})
	default:
		return dnsFailoverMethodNotAllowed(w, r, http.MethodGet+", "+http.MethodPut)
	}
	return true
}

func handleDNSFailoverProbes(w http.ResponseWriter, r *http.Request, service admin.Service) bool {
	switch r.Method {
	case http.MethodGet:
		probes, err := service.ListDNSProbes(r.Context())
		if err != nil {
			return writeDNSFailoverError(w, err)
		}
		settings, err := service.GetDNSFailoverSettings(r.Context())
		if err != nil {
			return writeDNSFailoverError(w, err)
		}
		data := make([]map[string]any, 0, len(probes))
		for _, probe := range probes {
			raw, _ := json.Marshal(probe)
			item := map[string]any{}
			_ = json.Unmarshal(raw, &item)
			item["install_command"] = dnsFailoverInstallCommand(settings.ProbeAPIURL, probe.Secret, probe.Name)
			data = append(data, item)
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": data})
	case http.MethodPost:
		var input struct {
			Name string `json:"name"`
		}
		if !decodeStrictDNSFailoverJSON(w, r, &input) {
			return true
		}
		if strings.TrimSpace(input.Name) == "" {
			writeJSON(w, 400, map[string]any{"message": "探针名称不能为空"})
			return true
		}
		created, err := service.CreateDNSProbe(r.Context(), admin.DNSProbeCreateRequest{Name: input.Name})
		if err != nil {
			return writeDNSFailoverError(w, err)
		}
		settings, err := service.GetDNSFailoverSettings(r.Context())
		if err != nil {
			return writeDNSFailoverError(w, err)
		}
		data := map[string]any{"probe": created.Probe}
		if created.Secret != "" {
			data["secret"] = created.Secret
			data["install_command"] = dnsFailoverInstallCommand(settings.ProbeAPIURL, created.Secret, created.Probe.Name)
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": data})
	default:
		return dnsFailoverMethodNotAllowed(w, r, http.MethodGet+", "+http.MethodPost)
	}
	return true
}

func handleDNSFailoverProbeDelete(w http.ResponseWriter, r *http.Request, service admin.Service, rawID string) bool {
	if r.Method != http.MethodDelete {
		return dnsFailoverMethodNotAllowed(w, r, http.MethodDelete)
	}
	id, ok := dnsFailoverPositiveID(w, rawID, "探针 ID")
	if !ok {
		return true
	}
	if _, err := service.DeleteDNSProbe(r.Context(), id); err != nil {
		return writeDNSFailoverError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": true})
	return true
}

func handleDNSFailoverProbeMutation(w http.ResponseWriter, r *http.Request, service admin.Service, rawID, action string) bool {
	if r.Method != http.MethodPatch {
		return dnsFailoverMethodNotAllowed(w, r, http.MethodPatch)
	}
	id, ok := dnsFailoverPositiveID(w, rawID, "探针 ID")
	if !ok {
		return true
	}
	input := struct {
		Enabled *bool `json:"enabled"`
	}{}
	if action == "enabled" && !decodeStrictDNSFailoverJSON(w, r, &input) {
		return true
	}
	enabled := false
	if action == "enabled" {
		if input.Enabled == nil {
			writeJSON(w, 400, map[string]any{"message": "enabled 参数不能为空"})
			return true
		}
		enabled = *input.Enabled
	}
	result, err := service.SetDNSProbeEnabled(r.Context(), id, enabled)
	if err != nil {
		return writeDNSFailoverError(w, err)
	}
	writeJSON(w, 200, map[string]any{"data": result})
	return true
}

func handleDNSFailoverRules(w http.ResponseWriter, r *http.Request, service admin.Service) bool {
	switch r.Method {
	case http.MethodGet:
		result, err := service.ListDNSFailoverRules(r.Context())
		if err != nil {
			return writeDNSFailoverError(w, err)
		}
		writeJSON(w, 200, map[string]any{"data": result})
	case http.MethodPost:
		return saveDNSFailoverRule(w, r, service, nil)
	default:
		return dnsFailoverMethodNotAllowed(w, r, http.MethodGet+", "+http.MethodPost)
	}
	return true
}
func handleDNSFailoverRule(w http.ResponseWriter, r *http.Request, service admin.Service, rawID string) bool {
	id, ok := dnsFailoverPositiveID(w, rawID, "规则 ID")
	if !ok {
		return true
	}
	switch r.Method {
	case http.MethodGet:
		result, err := service.GetDNSFailoverRule(r.Context(), id)
		if err != nil {
			return writeDNSFailoverError(w, err)
		}
		writeJSON(w, 200, map[string]any{"data": result})
	case http.MethodPut:
		return saveDNSFailoverRule(w, r, service, &id)
	case http.MethodDelete:
		result, err := service.DeleteDNSFailoverRule(r.Context(), id)
		if err != nil {
			return writeDNSFailoverError(w, err)
		}
		writeJSON(w, 200, map[string]any{"data": result})
	default:
		return dnsFailoverMethodNotAllowed(w, r, http.MethodGet+", "+http.MethodPut+", "+http.MethodDelete)
	}
	return true
}

type dnsFailoverTargetInput struct {
	ID        int64  `json:"id"`
	Sort      int64  `json:"sort"`
	Name      string `json:"name"`
	DNSType   string `json:"dns_type"`
	DNSValue  string `json:"dns_value"`
	CheckHost string `json:"check_host"`
	CheckPort int64  `json:"check_port"`
	Enabled   bool   `json:"enabled"`
}

type dnsFailoverRuleInput struct {
	Name                        string                   `json:"name"`
	DomainID                    int64                    `json:"domain_id"`
	Domain                      string                   `json:"domain"`
	RecordID                    int64                    `json:"record_id"`
	Subdomain                   string                   `json:"subdomain"`
	RecordLineID                string                   `json:"record_line_id"`
	RecordLineName              string                   `json:"record_line_name"`
	TTL                         int64                    `json:"ttl"`
	MX                          int64                    `json:"mx"`
	Weight                      *int64                   `json:"weight"`
	Enabled                     bool                     `json:"enabled"`
	AutoFailback                bool                     `json:"auto_failback"`
	CheckIntervalSec            int64                    `json:"check_interval_sec"`
	TCPTimeoutMS                int64                    `json:"tcp_timeout_ms"`
	FailureThreshold            int64                    `json:"failure_threshold"`
	SuccessThreshold            int64                    `json:"success_threshold"`
	SingleProbeFailureThreshold int64                    `json:"single_probe_failure_threshold"`
	SingleProbeSuccessThreshold int64                    `json:"single_probe_success_threshold"`
	ProbeOfflineSec             int64                    `json:"probe_offline_sec"`
	CooldownSec                 int64                    `json:"cooldown_sec"`
	Targets                     []dnsFailoverTargetInput `json:"targets"`
	ProbeIDs                    []int64                  `json:"probe_ids"`
}

func saveDNSFailoverRule(w http.ResponseWriter, r *http.Request, service admin.Service, id *int64) bool {
	var input dnsFailoverRuleInput
	if !decodeStrictDNSFailoverJSON(w, r, &input) {
		return true
	}
	if input.DomainID <= 0 || input.RecordID <= 0 || strings.TrimSpace(input.Domain) == "" || len(input.Targets) == 0 {
		writeJSON(w, 400, map[string]any{"message": "域名、记录 ID 和 targets 不能为空"})
		return true
	}
	targets := make([]admin.DNSFailoverTargetSaveRequest, len(input.Targets))
	for i, target := range input.Targets {
		targets[i] = admin.DNSFailoverTargetSaveRequest{ID: target.ID, Sort: target.Sort, Name: target.Name, DNSType: target.DNSType, DNSValue: target.DNSValue, CheckHost: target.CheckHost, CheckPort: target.CheckPort, Enabled: target.Enabled}
	}
	request := admin.DNSFailoverRuleSaveRequest{ID: id, Name: input.Name, DomainID: input.DomainID, Domain: input.Domain, RecordID: input.RecordID, Subdomain: input.Subdomain, RecordLineID: input.RecordLineID, RecordLineName: input.RecordLineName, TTL: input.TTL, MX: input.MX, Weight: input.Weight, Enabled: input.Enabled, AutoFailback: input.AutoFailback, CheckIntervalSec: input.CheckIntervalSec, TCPTimeoutMS: input.TCPTimeoutMS, FailureThreshold: input.FailureThreshold, SuccessThreshold: input.SuccessThreshold, SingleProbeFailureThreshold: input.SingleProbeFailureThreshold, SingleProbeSuccessThreshold: input.SingleProbeSuccessThreshold, ProbeOfflineSec: input.ProbeOfflineSec, CooldownSec: input.CooldownSec, Targets: targets, ProbeIDs: input.ProbeIDs}
	result, err := service.SaveDNSFailoverRule(r.Context(), request)
	if err != nil {
		return writeDNSFailoverError(w, err)
	}
	writeJSON(w, 200, map[string]any{"data": result})
	return true
}

func handleDNSFailoverRuleMutation(w http.ResponseWriter, r *http.Request, service admin.Service, rawID, action string) bool {
	if r.Method != http.MethodPatch && !(action == "manual-switch" && r.Method == http.MethodPost) {
		return dnsFailoverMethodNotAllowed(w, r, map[string]string{"enabled": http.MethodPatch, "manual-switch": http.MethodPost}[action])
	}
	id, ok := dnsFailoverPositiveID(w, rawID, "规则 ID")
	if !ok {
		return true
	}
	if action == "enabled" {
		var input struct {
			Enabled *bool `json:"enabled"`
		}
		if !decodeStrictDNSFailoverJSON(w, r, &input) {
			return true
		}
		if input.Enabled == nil {
			writeJSON(w, 400, map[string]any{"message": "enabled 参数不能为空"})
			return true
		}
		result, err := service.SetDNSFailoverRuleEnabled(r.Context(), id, *input.Enabled)
		if err != nil {
			return writeDNSFailoverError(w, err)
		}
		writeJSON(w, 200, map[string]any{"data": result})
		return true
	}
	var input struct {
		TargetID int64 `json:"target_id"`
	}
	if !decodeStrictDNSFailoverJSON(w, r, &input) {
		return true
	}
	if input.TargetID <= 0 {
		writeJSON(w, 400, map[string]any{"message": "target_id 参数无效"})
		return true
	}
	if err := service.ManualSwitchDNSFailoverTarget(r.Context(), id, input.TargetID); err != nil {
		return writeDNSFailoverError(w, err)
	}
	writeJSON(w, 200, map[string]any{"data": true})
	return true
}

func handleDNSFailoverEvents(w http.ResponseWriter, r *http.Request, service admin.Service) bool {
	if r.Method != http.MethodGet {
		return dnsFailoverMethodNotAllowed(w, r, http.MethodGet)
	}
	q := r.URL.Query()
	request := admin.DNSFailoverEventListRequest{EventType: strings.TrimSpace(q.Get("event_type")), Current: 1, PageSize: 20}
	var err error
	if q.Get("group") != "" {
		v, ok := dnsFailoverPositiveID(w, q.Get("group"), "group 参数")
		if !ok {
			return true
		}
		request.GroupID = &v
	}
	for _, entry := range []struct {
		raw, name string
		target    *int64
	}{{q.Get("current"), "current 参数", &request.Current}, {q.Get("page_size"), "page_size 参数", &request.PageSize}} {
		if entry.raw != "" {
			*entry.target, err = strconv.ParseInt(entry.raw, 10, 64)
			if err != nil || *entry.target <= 0 {
				writeJSON(w, 400, map[string]any{"message": entry.name + "无效"})
				return true
			}
		}
	}
	result, err := service.ListDNSFailoverEvents(r.Context(), request)
	if err != nil {
		return writeDNSFailoverError(w, err)
	}
	writeJSON(w, 200, map[string]any{"data": result.Data, "total": result.Total, "current": result.Current, "page_size": result.PageSize})
	return true
}

func handleDNSFailoverLogs(w http.ResponseWriter, r *http.Request, service admin.Service) bool {
	if r.Method != http.MethodGet {
		return dnsFailoverMethodNotAllowed(w, r, http.MethodGet)
	}
	q := r.URL.Query()
	request := admin.DNSFailoverLogListRequest{Stage: q.Get("stage"), Level: q.Get("level"), Outcome: q.Get("outcome"), Current: 1, PageSize: 20}
	for _, entry := range []struct {
		key, name string
		target    **int64
	}{{"group", "group 参数", &request.GroupID}, {"probe_id", "probe_id 参数", &request.ProbeID}, {"target_id", "target_id 参数", &request.TargetID}} {
		if q.Get(entry.key) == "" {
			continue
		}
		value, ok := dnsFailoverPositiveID(w, q.Get(entry.key), entry.name)
		if !ok {
			return true
		}
		*entry.target = &value
	}
	for _, entry := range []struct {
		key, name string
		target    *int64
	}{{"current", "current 参数", &request.Current}, {"page_size", "page_size 参数", &request.PageSize}} {
		if q.Get(entry.key) == "" {
			continue
		}
		value, err := strconv.ParseInt(q.Get(entry.key), 10, 64)
		if err != nil || value <= 0 {
			writeJSON(w, 400, map[string]any{"message": entry.name + "无效"})
			return true
		}
		*entry.target = value
	}
	result, err := service.ListDNSFailoverLogs(r.Context(), request)
	if err != nil {
		return writeDNSFailoverError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": result.Data, "total": result.Total, "current": result.Current, "page_size": result.PageSize})
	return true
}

func decodeStrictDNSFailoverJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		writeJSON(w, 415, map[string]any{"message": "请求必须使用 application/json"})
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxDNSFailoverJSONBody)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeJSON(w, 400, map[string]any{"message": "JSON 请求体无效：" + err.Error()})
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeJSON(w, 400, map[string]any{"message": "JSON 请求体只能包含一个对象"})
		return false
	}
	return true
}
func dnsFailoverPositiveID(w http.ResponseWriter, raw, name string) (int64, bool) {
	id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, 400, map[string]any{"message": name + "无效"})
		return 0, false
	}
	return id, true
}
func dnsFailoverMethodNotAllowed(w http.ResponseWriter, r *http.Request, allow string) bool {
	w.Header().Set("Allow", allow)
	writeJSON(w, 405, map[string]any{"message": "请求方式不支持"})
	return true
}
func writeDNSFailoverError(w http.ResponseWriter, err error) bool {
	message := err.Error()
	status := http.StatusBadRequest
	if errors.Is(err, admin.ErrClientEntryMonitorRevisionConflict) {
		writeJSON(w, http.StatusConflict, map[string]any{"message": message})
		return true
	}
	lower := strings.ToLower(message)
	if strings.Contains(lower, "not found") || strings.Contains(message, "不存在") {
		status = 404
		message = "请求的 DNS 故障转移资源不存在"
	}
	if strings.Contains(message, "用户入口检测正在进行") {
		status = http.StatusConflict
		writeJSON(w, status, map[string]any{"message": message})
		return true
	}
	if strings.Contains(lower, "busy") || strings.Contains(lower, "conflict") || strings.Contains(message, "进行中") {
		status = 409
		message = "DNS 故障转移切换正在进行中，请稍后重试"
	}
	writeJSON(w, status, map[string]any{"message": message})
	return true
}
func dnsFailoverInstallCommand(apiURL, secret, name string) string {
	base := strings.TrimRight(strings.TrimSpace(apiURL), "/")
	if base == "" {
		return ""
	}
	query := url.Values{"api_url": {base}, "token": {secret}}
	installURL := base + "/api/v1/probe/install.sh?" + query.Encode()
	return fmt.Sprintf("curl -fsSL %s | sudo bash", dnsFailoverShellQuote(installURL))
}
func dnsFailoverShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\\"'\\\"'") + "'"
}
