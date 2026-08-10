package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"forest/go-api/internal/admin"
	"forest/go-api/internal/session"
)

func handleAdminClientEntryUserPolicySplitPreview(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}
	minutes := int64(60)
	if raw := strings.TrimSpace(r.URL.Query().Get("minutes")); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"message": "订阅时间范围无效"})
			return true
		}
		minutes = value
	}
	result, err := adminService.PreviewClientEntryUserPolicySplit(r.Context(), admin.ClientEntryUserPolicySplitPreviewRequest{Minutes: minutes})
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": result})
	return true
}

func handleAdminClientEntryUserPolicySplitCreate(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}
	var payload struct {
		Name             string       `json:"name"`
		Minutes          *json.Number `json:"minutes"`
		EntryHostA       string       `json:"entry_host_a"`
		EntryHostB       string       `json:"entry_host_b"`
		ResolveEntryHost *json.Number `json:"resolve_entry_host"`
		Enabled          *json.Number `json:"enabled"`
		Remarks          string       `json:"remarks"`
		Members          []struct {
			ServerType string       `json:"server_type"`
			ServerID   *json.Number `json:"server_id"`
			Sort       *json.Number `json:"sort"`
		} `json:"members"`
	}
	if err := decodeClientEntrySplitPayload(r, &payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	minutes, err := jsonNumberToInt64Pointer(payload.Minutes)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "订阅时间范围无效"})
		return true
	}
	minutesValue := int64(0)
	if minutes != nil {
		minutesValue = *minutes
	}
	resolveEntryHost, err := jsonNumberToInt64Pointer(payload.ResolveEntryHost)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "解析域名设置无效"})
		return true
	}
	enabled, err := jsonNumberToInt64Pointer(payload.Enabled)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "规则状态无效"})
		return true
	}
	members, err := clientEntrySplitMembers(payload.Members)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	result, err := adminService.CreateClientEntryUserPolicySplit(r.Context(), admin.ClientEntryUserPolicySplitCreateRequest{
		Name:             payload.Name,
		Minutes:          minutesValue,
		Members:          members,
		EntryHostA:       payload.EntryHostA,
		EntryHostB:       payload.EntryHostB,
		ResolveEntryHost: resolveEntryHost,
		Enabled:          enabled,
		Remarks:          payload.Remarks,
	})
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": result})
	return true
}

func handleAdminClientEntryUserPolicySplitConvert(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "请求方式不支持"})
		return true
	}
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}
	var payload struct {
		PolicyID   *json.Number `json:"policy_id"`
		EntryHostA string       `json:"entry_host_a"`
		EntryHostB string       `json:"entry_host_b"`
	}
	if err := readJSONBody(r, &payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	policyID, err := jsonNumberToInt64Pointer(payload.PolicyID)
	if err != nil || policyID == nil || *policyID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "规则不存在"})
		return true
	}
	result, err := adminService.ConvertClientEntryUserPolicyToSplit(r.Context(), admin.ClientEntryUserPolicySplitConvertRequest{
		PolicyID: *policyID, EntryHostA: payload.EntryHostA, EntryHostB: payload.EntryHostB,
	})
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": result})
	return true
}

func handleAdminClientEntryUserPolicySplitGroup(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}
	var payload struct {
		PolicyID   *json.Number `json:"policy_id"`
		GroupID    *json.Number `json:"group_id"`
		EntryHostA string       `json:"entry_host_a"`
		EntryHostB string       `json:"entry_host_b"`
	}
	if err := readJSONBody(r, &payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	policyID, groupID, ok := clientEntrySplitIDs(w, payload.PolicyID, payload.GroupID)
	if !ok {
		return true
	}
	result, err := adminService.SplitClientEntryUserPolicyGroup(r.Context(), admin.ClientEntryUserPolicyGroupSplitRequest{
		PolicyID: policyID, GroupID: groupID, EntryHostA: payload.EntryHostA, EntryHostB: payload.EntryHostB,
	})
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": result})
	return true
}

func handleAdminClientEntryUserPolicySplitGroupHost(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}
	var payload struct {
		PolicyID  *json.Number `json:"policy_id"`
		GroupID   *json.Number `json:"group_id"`
		EntryHost string       `json:"entry_host"`
	}
	if err := readJSONBody(r, &payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	policyID, groupID, ok := clientEntrySplitIDs(w, payload.PolicyID, payload.GroupID)
	if !ok {
		return true
	}
	result, err := adminService.UpdateClientEntryUserPolicySplitGroupHost(r.Context(), admin.ClientEntryUserPolicyGroupHostUpdateRequest{
		PolicyID: policyID, GroupID: groupID, EntryHost: payload.EntryHost,
	})
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": result})
	return true
}

func handleAdminClientEntryUserPolicyEnabled(w http.ResponseWriter, r *http.Request, sessionService session.Service, adminService admin.Service) bool {
	if _, ok := authenticateRequest(w, r, sessionService, true); !ok {
		return true
	}
	if adminService == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "admin service unavailable"})
		return true
	}
	var payload struct {
		ID      *json.Number `json:"id"`
		Enabled *json.Number `json:"enabled"`
	}
	if err := readJSONBody(r, &payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return true
	}
	id, err := jsonNumberToInt64Pointer(payload.ID)
	if err != nil || id == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "规则不存在"})
		return true
	}
	enabled, err := jsonNumberToInt64Pointer(payload.Enabled)
	if err != nil || enabled == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "规则状态无效"})
		return true
	}
	result, err := adminService.SetClientEntryUserPolicyEnabled(r.Context(), *id, *enabled)
	if err != nil {
		return handleAdminError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": result})
	return true
}

func decodeClientEntrySplitPayload(r *http.Request, payload any) error {
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		return readJSONBody(r, payload)
	}
	return json.NewDecoder(r.Body).Decode(payload)
}

func clientEntrySplitMembers(values []struct {
	ServerType string       `json:"server_type"`
	ServerID   *json.Number `json:"server_id"`
	Sort       *json.Number `json:"sort"`
}) ([]admin.ClientEntryGroupMemberSaveRequest, error) {
	result := make([]admin.ClientEntryGroupMemberSaveRequest, 0, len(values))
	for _, value := range values {
		serverID, err := jsonNumberToInt64Pointer(value.ServerID)
		if err != nil || serverID == nil {
			return nil, &clientEntrySplitInputError{message: "生效节点不能为空"}
		}
		var sortValue *int64
		if value.Sort != nil {
			sortValue, err = jsonNumberToInt64Pointer(value.Sort)
			if err != nil {
				return nil, &clientEntrySplitInputError{message: "节点顺序无效"}
			}
		}
		result = append(result, admin.ClientEntryGroupMemberSaveRequest{ServerType: value.ServerType, ServerID: *serverID, Sort: sortValue})
	}
	return result, nil
}

type clientEntrySplitInputError struct{ message string }

func (e *clientEntrySplitInputError) Error() string { return e.message }

func clientEntrySplitIDs(w http.ResponseWriter, rawPolicyID, rawGroupID *json.Number) (int64, int64, bool) {
	policyID, err := jsonNumberToInt64Pointer(rawPolicyID)
	if err != nil || policyID == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "二分规则不存在"})
		return 0, 0, false
	}
	groupID, err := jsonNumberToInt64Pointer(rawGroupID)
	if err != nil || groupID == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "二分组不存在"})
		return 0, 0, false
	}
	return *policyID, *groupID, true
}
