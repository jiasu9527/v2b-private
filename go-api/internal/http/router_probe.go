package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"

	"forest/go-api/internal/admin"
)

const (
	maxDNSProbeBearerSecretBytes = 512
	maxDNSProbeHeartbeatBody     = 4 << 10
	maxDNSProbeResultsBody       = 1 << 20
	maxDNSProbeResultsBatch      = 500
)

func WithDNSProbeService(service admin.DNSProbeService) Option {
	return func(state *routerState) {
		state.dnsProbe = service
	}
}

func handleDNSProbeHeartbeat(w http.ResponseWriter, r *http.Request, service admin.DNSProbeService) {
	if !requireDNSProbeMethod(w, r, http.MethodPost) {
		return
	}
	identity, ok := authenticateDNSProbeRequest(w, r, service)
	if !ok {
		return
	}
	var request admin.DNSProbeHeartbeatRequest
	if err := decodeDNSProbeJSON(w, r, maxDNSProbeHeartbeatBody, &request); err != nil {
		writeDNSProbeBadRequest(w)
		return
	}
	request.PublicIP = normalizedRequestIP(r)
	if request.PublicIP == "" {
		writeDNSProbeBadRequest(w)
		return
	}
	result, err := service.HeartbeatDNSProbe(r.Context(), identity.ID, request)
	if err != nil {
		writeDNSProbeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": result})
}

func handleDNSProbeTasks(w http.ResponseWriter, r *http.Request, service admin.DNSProbeService) {
	if !requireDNSProbeMethod(w, r, http.MethodGet) {
		return
	}
	identity, ok := authenticateDNSProbeRequest(w, r, service)
	if !ok {
		return
	}
	tasks, err := service.ListDNSProbeTasks(r.Context(), identity.ID)
	if err != nil {
		writeDNSProbeServiceError(w, err)
		return
	}
	if tasks == nil {
		tasks = make([]admin.DNSProbeTask, 0)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": tasks})
}

func handleDNSProbeResults(w http.ResponseWriter, r *http.Request, service admin.DNSProbeService) {
	if !requireDNSProbeMethod(w, r, http.MethodPost) {
		return
	}
	identity, ok := authenticateDNSProbeRequest(w, r, service)
	if !ok {
		return
	}
	var request admin.DNSProbeResultsRequest
	if err := decodeDNSProbeJSON(w, r, maxDNSProbeResultsBody, &request); err != nil || request.Results == nil || len(request.Results) > maxDNSProbeResultsBatch {
		writeDNSProbeBadRequest(w)
		return
	}
	result, err := service.ReportDNSProbeResults(r.Context(), identity.ID, request)
	if err != nil {
		writeDNSProbeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": result})
}

func requireDNSProbeMethod(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method == method {
		return true
	}
	w.Header().Set("Allow", method)
	writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "method not allowed"})
	return false
}

func authenticateDNSProbeRequest(w http.ResponseWriter, r *http.Request, service admin.DNSProbeService) (admin.DNSProbeIdentity, bool) {
	if service == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "probe service unavailable"})
		return admin.DNSProbeIdentity{}, false
	}
	values := r.Header.Values("Authorization")
	if len(values) != 1 {
		writeDNSProbeUnauthorized(w)
		return admin.DNSProbeIdentity{}, false
	}
	value := values[0]
	if !strings.HasPrefix(value, "Bearer ") {
		writeDNSProbeUnauthorized(w)
		return admin.DNSProbeIdentity{}, false
	}
	secret := strings.TrimPrefix(value, "Bearer ")
	if secret == "" || len(secret) > maxDNSProbeBearerSecretBytes || strings.IndexFunc(secret, func(character rune) bool {
		return character <= ' ' || character == '\u007f'
	}) >= 0 {
		writeDNSProbeUnauthorized(w)
		return admin.DNSProbeIdentity{}, false
	}
	identity, err := service.AuthenticateDNSProbe(r.Context(), secret)
	if err != nil {
		if errors.Is(err, admin.ErrDNSProbeUnauthorized) {
			writeDNSProbeUnauthorized(w)
		} else {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "probe service unavailable"})
		}
		return admin.DNSProbeIdentity{}, false
	}
	return identity, true
}

func decodeDNSProbeJSON(w http.ResponseWriter, r *http.Request, maxBytes int64, target any) error {
	if r.Body == nil {
		return io.EOF
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}

func writeDNSProbeUnauthorized(w http.ResponseWriter) {
	writeJSON(w, http.StatusUnauthorized, map[string]any{"message": "unauthorized"})
}

func writeDNSProbeBadRequest(w http.ResponseWriter) {
	writeJSON(w, http.StatusBadRequest, map[string]any{"message": "invalid probe request"})
}

func writeDNSProbeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, admin.ErrDNSProbeUnauthorized):
		writeDNSProbeUnauthorized(w)
	case errors.Is(err, admin.ErrDNSProbeInvalidRequest):
		writeDNSProbeBadRequest(w)
	case errors.Is(err, admin.ErrUnavailable):
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "probe service unavailable"})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "probe request failed"})
	}
}

// normalizedRequestIP trusts the deployment's reverse-proxy headers in this
// order: Cloudflare, X-Forwarded-For, X-Real-IP, then the socket peer. Invalid
// header values are ignored instead of masking a valid lower-priority source.
func normalizedRequestIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	if ip := canonicalDNSProbeIP(r.Header.Get("CF-Connecting-IP")); ip != "" {
		return ip
	}
	for _, candidate := range strings.Split(r.Header.Get("X-Forwarded-For"), ",") {
		if ip := canonicalDNSProbeIP(candidate); ip != "" {
			return ip
		}
	}
	if ip := canonicalDNSProbeIP(r.Header.Get("X-Real-IP")); ip != "" {
		return ip
	}
	remote := strings.TrimSpace(r.RemoteAddr)
	if host, _, err := net.SplitHostPort(remote); err == nil {
		remote = host
	}
	return canonicalDNSProbeIP(remote)
}

func canonicalDNSProbeIP(value string) string {
	addr, err := netip.ParseAddr(strings.TrimSpace(value))
	if err != nil || addr.Zone() != "" {
		return ""
	}
	return addr.Unmap().String()
}
