package httpapi

import (
	"errors"
	"net/http"

	"forest/go-api/internal/admin"
)

func clientEntryBackupIPService(w http.ResponseWriter, service admin.Service) (admin.ClientEntryBackupIPAdminService, bool) {
	result, ok := any(service).(admin.ClientEntryBackupIPAdminService)
	if !ok || result == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "备用 IP 池服务暂不可用"})
		return nil, false
	}
	return result, true
}

func handleClientEntryBackupIPs(w http.ResponseWriter, r *http.Request, service admin.Service) bool {
	backupService, ok := clientEntryBackupIPService(w, service)
	if !ok {
		return true
	}
	switch r.Method {
	case http.MethodGet:
		result, err := backupService.ListClientEntryBackupIPs(r.Context())
		if err != nil {
			return writeClientEntryBackupIPError(w, err)
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": result})
	case http.MethodPost:
		var request admin.ClientEntryBackupIPCreateRequest
		if !decodeStrictDNSFailoverJSON(w, r, &request) {
			return true
		}
		if request.Items != nil {
			result, err := backupService.CreateClientEntryBackupIPs(r.Context(), request.Items)
			if err != nil {
				return writeClientEntryBackupIPError(w, err)
			}
			writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"items": result}})
			return true
		}
		result, err := backupService.CreateClientEntryBackupIP(r.Context(), request.ClientEntryBackupIPSaveRequest)
		if err != nil {
			return writeClientEntryBackupIPError(w, err)
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": result})
	default:
		return dnsFailoverMethodNotAllowed(w, r, http.MethodGet+", "+http.MethodPost)
	}
	return true
}

func handleClientEntryBackupIP(w http.ResponseWriter, r *http.Request, service admin.Service, rawID string) bool {
	id, ok := dnsFailoverPositiveID(w, rawID, "备用 IP ID")
	if !ok {
		return true
	}
	backupService, ok := clientEntryBackupIPService(w, service)
	if !ok {
		return true
	}
	switch r.Method {
	case http.MethodPut:
		var request admin.ClientEntryBackupIPSaveRequest
		if !decodeStrictDNSFailoverJSON(w, r, &request) {
			return true
		}
		result, err := backupService.UpdateClientEntryBackupIP(r.Context(), id, request)
		if err != nil {
			return writeClientEntryBackupIPError(w, err)
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": result})
	case http.MethodDelete:
		deleted, err := backupService.DeleteClientEntryBackupIP(r.Context(), id)
		if err != nil {
			return writeClientEntryBackupIPError(w, err)
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"deleted": deleted}})
	default:
		return dnsFailoverMethodNotAllowed(w, r, http.MethodPut+", "+http.MethodDelete)
	}
	return true
}

func handleClientEntryBackupIPRefresh(w http.ResponseWriter, r *http.Request, service admin.Service) bool {
	if r.Method != http.MethodPost {
		return dnsFailoverMethodNotAllowed(w, r, http.MethodPost)
	}
	backupService, ok := clientEntryBackupIPService(w, service)
	if !ok {
		return true
	}
	var request struct {
		IDs []int64 `json:"ids"`
	}
	if !decodeStrictDNSFailoverJSON(w, r, &request) {
		return true
	}
	result, err := backupService.RefreshClientEntryBackupIPs(r.Context(), request.IDs)
	if err != nil {
		return writeClientEntryBackupIPError(w, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": result})
	return true
}

func writeClientEntryBackupIPError(w http.ResponseWriter, err error) bool {
	status := http.StatusBadRequest
	if errors.Is(err, admin.ErrUnavailable) {
		status = http.StatusServiceUnavailable
	} else if errors.Is(err, admin.ErrClientEntryBackupIPNotFound) {
		status = http.StatusNotFound
	} else if errors.Is(err, admin.ErrClientEntryBackupIPConflict) || errors.Is(err, admin.ErrClientEntryBackupIPInUse) {
		status = http.StatusConflict
	}
	writeJSON(w, status, map[string]any{"message": err.Error()})
	return true
}
