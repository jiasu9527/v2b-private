package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/session"
)

func handleAdminDNSPodConfig(w http.ResponseWriter, r *http.Request, sessions session.Service, service admin.Service) bool {
	if !requireHTTPMethod(w, r, http.MethodGet) {
		return true
	}
	if !requireDNSPodAdmin(w, r, sessions, service) {
		return true
	}
	status, err := service.GetDNSPodConfig(r.Context())
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": status})
	return true
}

func handleAdminDNSPodConfigSave(w http.ResponseWriter, r *http.Request, sessions session.Service, service admin.Service) bool {
	if !requireHTTPMethod(w, r, http.MethodPost) {
		return true
	}
	if !requireDNSPodAdmin(w, r, sessions, service) {
		return true
	}
	inputs, ok := readDNSPodInputs(w, r)
	if !ok {
		return true
	}
	status, err := service.SaveDNSPodConfig(r.Context(), admin.DNSPodConfigSaveRequest{
		SecretID: strings.TrimSpace(inputs["secret_id"]), SecretKey: strings.TrimSpace(inputs["secret_key"]),
		Verify: parseBoolish(inputs["verify"]), Clear: parseBoolish(inputs["clear"]),
	})
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": status})
	return true
}

func handleAdminDNSPodConfigTest(w http.ResponseWriter, r *http.Request, sessions session.Service, service admin.Service) bool {
	if !requireHTTPMethod(w, r, http.MethodPost) {
		return true
	}
	if !requireDNSPodAdmin(w, r, sessions, service) {
		return true
	}
	inputs, ok := readDNSPodInputs(w, r)
	if !ok {
		return true
	}
	if err := service.TestDNSPodConfig(r.Context(), inputs["secret_id"], inputs["secret_key"]); err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": true})
	return true
}

func handleAdminDNSPodDomainList(w http.ResponseWriter, r *http.Request, sessions session.Service, service admin.Service) bool {
	if !requireHTTPMethod(w, r, http.MethodGet) {
		return true
	}
	if !requireDNSPodAdmin(w, r, sessions, service) {
		return true
	}
	inputs, ok := readDNSPodInputs(w, r)
	if !ok {
		return true
	}
	current, pageSize := dnsPodPagination(inputs)
	result, err := service.ListDNSPodDomains(r.Context(), admin.DNSPodDomainListRequest{
		Current: current, PageSize: pageSize, Keyword: inputs["keyword"], Type: inputs["type"],
	})
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": result.Domains, "total": result.Total, "request_id": result.RequestID})
	return true
}

func handleAdminDNSPodRecordList(w http.ResponseWriter, r *http.Request, sessions session.Service, service admin.Service) bool {
	if !requireHTTPMethod(w, r, http.MethodGet) {
		return true
	}
	if !requireDNSPodAdmin(w, r, sessions, service) {
		return true
	}
	inputs, ok := readDNSPodInputs(w, r)
	if !ok {
		return true
	}
	if strings.TrimSpace(inputs["domain"]) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "请选择域名"})
		return true
	}
	current, pageSize := dnsPodPagination(inputs)
	result, err := service.ListDNSPodRecords(r.Context(), admin.DNSPodRecordListRequest{
		Domain: inputs["domain"], Current: current, PageSize: pageSize, Keyword: inputs["keyword"],
		Subdomain: inputs["subdomain"], RecordType: inputs["record_type"], RecordLine: inputs["record_line"],
	})
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": result.Records, "total": result.Total, "request_id": result.RequestID})
	return true
}

func handleAdminDNSPodRecordTypes(w http.ResponseWriter, r *http.Request, sessions session.Service, service admin.Service) bool {
	if !requireHTTPMethod(w, r, http.MethodGet) {
		return true
	}
	if !requireDNSPodAdmin(w, r, sessions, service) {
		return true
	}
	inputs, ok := readDNSPodInputs(w, r)
	if !ok {
		return true
	}
	result, err := service.ListDNSPodRecordTypes(r.Context(), inputs["domain_grade"])
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": result.Types, "request_id": result.RequestID})
	return true
}

func handleAdminDNSPodRecordLines(w http.ResponseWriter, r *http.Request, sessions session.Service, service admin.Service) bool {
	if !requireHTTPMethod(w, r, http.MethodGet) {
		return true
	}
	if !requireDNSPodAdmin(w, r, sessions, service) {
		return true
	}
	inputs, ok := readDNSPodInputs(w, r)
	if !ok {
		return true
	}
	if strings.TrimSpace(inputs["domain"]) == "" || strings.TrimSpace(inputs["record_type"]) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "域名和记录类型不能为空"})
		return true
	}
	result, err := service.ListDNSPodRecordLines(r.Context(), inputs["domain"], inputs["domain_grade"], inputs["record_type"])
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": result.Lines, "request_id": result.RequestID})
	return true
}

func handleAdminDNSPodRecordSave(w http.ResponseWriter, r *http.Request, sessions session.Service, service admin.Service) bool {
	if !requireHTTPMethod(w, r, http.MethodPost) {
		return true
	}
	if !requireDNSPodAdmin(w, r, sessions, service) {
		return true
	}
	inputs, ok := readDNSPodInputs(w, r)
	if !ok {
		return true
	}
	recordID, valid := optionalDNSPodInt(inputs["record_id"])
	if !valid {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "记录 ID 无效"})
		return true
	}
	ttl, valid := optionalDNSPodInt(inputs["ttl"])
	if !valid {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "TTL 无效"})
		return true
	}
	mx, valid := optionalDNSPodInt(inputs["mx"])
	if !valid {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "MX 优先级无效"})
		return true
	}
	var weight *int64
	if strings.TrimSpace(inputs["weight"]) != "" {
		value, valid := optionalDNSPodInt(inputs["weight"])
		if !valid {
			writeJSON(w, http.StatusBadRequest, map[string]any{"message": "权重无效"})
			return true
		}
		weight = &value
	}
	if strings.TrimSpace(inputs["domain"]) == "" || strings.TrimSpace(inputs["record_type"]) == "" || strings.TrimSpace(inputs["value"]) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "域名、记录类型和记录值不能为空"})
		return true
	}
	result, err := service.SaveDNSPodRecord(r.Context(), admin.DNSPodRecordSaveRequest{
		Domain: inputs["domain"], RecordID: recordID, SubDomain: inputs["sub_domain"], RecordType: inputs["record_type"],
		RecordLine: inputs["record_line"], RecordLineID: inputs["record_line_id"], Value: inputs["value"],
		TTL: ttl, MX: mx, Weight: weight,
	})
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": result})
	return true
}

func handleAdminDNSPodRecordDelete(w http.ResponseWriter, r *http.Request, sessions session.Service, service admin.Service) bool {
	if !requireHTTPMethod(w, r, http.MethodPost) {
		return true
	}
	if !requireDNSPodAdmin(w, r, sessions, service) {
		return true
	}
	inputs, ok := readDNSPodInputs(w, r)
	if !ok {
		return true
	}
	recordID, err := strconv.ParseInt(strings.TrimSpace(inputs["record_id"]), 10, 64)
	if err != nil || recordID <= 0 || strings.TrimSpace(inputs["domain"]) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "域名或记录 ID 无效"})
		return true
	}
	if err := service.DeleteDNSPodRecord(r.Context(), inputs["domain"], recordID); err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": true})
	return true
}

func handleAdminDNSPodRecordStatus(w http.ResponseWriter, r *http.Request, sessions session.Service, service admin.Service) bool {
	if !requireHTTPMethod(w, r, http.MethodPost) {
		return true
	}
	if !requireDNSPodAdmin(w, r, sessions, service) {
		return true
	}
	inputs, ok := readDNSPodInputs(w, r)
	if !ok {
		return true
	}
	recordID, err := strconv.ParseInt(strings.TrimSpace(inputs["record_id"]), 10, 64)
	status := strings.ToUpper(strings.TrimSpace(inputs["status"]))
	if err != nil || recordID <= 0 || strings.TrimSpace(inputs["domain"]) == "" || (status != "ENABLE" && status != "DISABLE") {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "域名、记录 ID 或状态无效"})
		return true
	}
	if err := service.SetDNSPodRecordStatus(r.Context(), inputs["domain"], recordID, status); err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": true})
	return true
}

func requireDNSPodAdmin(w http.ResponseWriter, r *http.Request, sessions session.Service, service admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessions, true); !ok {
		return false
	}
	if service == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return false
	}
	return true
}

func requireHTTPMethod(w http.ResponseWriter, r *http.Request, allowed string) bool {
	if r.Method == allowed {
		return true
	}
	w.Header().Set("Allow", allowed)
	writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "请求方式不支持"})
	return false
}

func readDNSPodInputs(w http.ResponseWriter, r *http.Request) (map[string]string, bool) {
	inputs, err := readInputs(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return nil, false
	}
	return inputs, true
}

func dnsPodPagination(inputs map[string]string) (int64, int64) {
	current := int64(1)
	if parsed, err := strconv.ParseInt(strings.TrimSpace(inputs["current"]), 10, 64); err == nil && parsed > 0 {
		current = parsed
	}
	pageSize := int64(20)
	if parsed, err := strconv.ParseInt(strings.TrimSpace(inputs["page_size"]), 10, 64); err == nil && parsed > 0 {
		pageSize = parsed
	}
	if pageSize > 100 {
		pageSize = 100
	}
	return current, pageSize
}

func optionalDNSPodInt(raw string) (int64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, true
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	return value, err == nil && value >= 0
}
