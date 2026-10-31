package httpapi

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"forest/go-api/internal/entryevidence"
	"forest/go-api/internal/subscribelink"
)

// beginEntryDelivery observes the bytes accepted by the server's writer, not
// client receipt/import/use. No response body or credential-bearing URL is kept.
func beginEntryDelivery(w http.ResponseWriter, r *http.Request, transport string) (http.ResponseWriter, *http.Request, func()) {
	store := entryevidence.StoreFromContext(r.Context())
	if store == nil {
		return w, r, func() {}
	}
	collector := entryevidence.NewCollector(transport, r.UserAgent())
	if collector == nil {
		return w, r, func() {}
	}
	observed := &entryDeliveryWriter{ResponseWriter: w}
	r = r.WithContext(entryevidence.WithCollector(r.Context(), collector))
	return observed, r, func() {
		if panicValue := recover(); panicValue != nil {
			observed.writeError = "handler_panicked"
			defer func() { panic(panicValue) }()
		}
		status := observed.status
		if status == 0 {
			status = http.StatusOK
		}
		// json.Encoder can fail before calling Write after headers have already
		// been sent. Generated metadata alone is not a completed response.
		if !observed.writeCalled && status >= 200 && status < 300 && observed.writeError == "" {
			observed.writeError = "body_not_written"
		}
		request, nodes := collector.Finish(status, observed.bytes, observed.writeError)
		if request.UserID <= 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = store.Record(ctx, request, nodes) // Fail open; store status exposes failures.
	}
}

type entryDeliveryWriter struct {
	http.ResponseWriter
	status      int
	bytes       int64
	writeError  string
	writeCalled bool
}

func (w *entryDeliveryWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *entryDeliveryWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	if status >= 100 && status < 200 {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
func (w *entryDeliveryWriter) Write(body []byte) (int, error) {
	w.writeCalled = true
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(body)
	w.bytes += int64(n)
	if err != nil {
		w.writeError = "write_failed"
	} else if n != len(body) {
		w.writeError = "short_write"
		err = io.ErrShortWrite
	}
	return n, err
}
func (w *entryDeliveryWriter) Flush() {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if err := http.NewResponseController(w.ResponseWriter).Flush(); err != nil {
		w.writeError = "flush_failed"
	}
}

func entryCollector(collectors []*entryevidence.Collector) *entryevidence.Collector {
	if len(collectors) == 0 {
		return nil
	}
	return collectors[0]
}
func emitEntryServer(c *entryevidence.Collector, server map[string]any, host, port string) {
	if c == nil || serverBool(server, "_entry_evidence_info") {
		return
	}
	c.Emit(serverString(server, "type"), serverInt64(server, "id"), host, port)
}
func emitEntryJSON(ctx context.Context, servers []map[string]any) {
	c := entryevidence.FromContext(ctx)
	for _, server := range servers {
		emitEntryServer(c, server, serverString(server, "host"), serverString(server, "port"))
	}
	c.Coverage("complete", "server_write_completed")
}
func emitEntryURI(c *entryevidence.Collector, server map[string]any) {
	// Raw extras are already parsed by subscribelink; no URI/credential is retained.
	port := serverString(server, "port")
	if kind, _ := normalizeSubscribeServer(server); kind == "hysteria" || kind == "hysteria2" {
		// buildHysteriaURI reads port (not mport); raw links can carry mport.
		if subscribelink.RawURI(server) != "" {
			port = firstNonEmptyString(serverString(server, "mport"), port)
		}
		port = strings.ReplaceAll(port, ":", "-")
	} else {
		port = strconv.FormatInt(serverInt64(server, "port"), 10)
	}
	emitEntryServer(c, server, serverString(server, "host"), port)
}
func emitEntrySurge(c *entryevidence.Collector, server map[string]any) {
	port := strconv.FormatInt(serverInt64(server, "port"), 10)
	if kind, normalized := normalizeSubscribeServer(server); kind == "hysteria2" {
		if value, ok := clashPrimaryPortString(normalized); ok {
			port = value
		}
	}
	emitEntryServer(c, server, serverString(server, "host"), port)
}
func entryProxyPort(proxy map[string]any, singular, plural string) string {
	if value := serverString(proxy, plural); value != "" && value != "<nil>" {
		if items, ok := proxy[plural].([]string); ok {
			value = strings.Join(items, ",")
		}
		return strings.ReplaceAll(value, ":", "-")
	}
	return serverString(proxy, singular)
}

func hasStaticSurgeProxies(template string) bool {
	inProxies := false
	for _, line := range strings.Split(template, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			inProxies = strings.EqualFold(line, "[Proxy]")
			continue
		}
		if !inProxies || line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") || line == "$proxies" {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			kind := strings.ToLower(strings.TrimSpace(strings.SplitN(parts[1], ",", 2)[0]))
			if kind != "direct" && kind != "reject" {
				return true
			}
		}
	}
	return false
}
