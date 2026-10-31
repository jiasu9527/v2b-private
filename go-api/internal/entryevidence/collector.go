package entryevidence

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/netip"
	"regexp"
	"strings"
	"sync"
	"time"

	"forest/go-api/internal/platform/addrutil"
)

type contextKey int

const (
	storeKey contextKey = iota
	collectorKey
)

func WithStore(ctx context.Context, s *Store) context.Context {
	return context.WithValue(ctx, storeKey, s)
}
func StoreFromContext(ctx context.Context) *Store { s, _ := ctx.Value(storeKey).(*Store); return s }
func WithCollector(ctx context.Context, c *Collector) context.Context {
	return context.WithValue(ctx, collectorKey, c)
}
func FromContext(ctx context.Context) *Collector {
	c, _ := ctx.Value(collectorKey).(*Collector)
	return c
}

type Collector struct {
	mu       sync.Mutex
	request  Request
	observed map[string]Node
	emitted  []Node
}

func NewCollector(transport, ua string) *Collector {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil
	}
	return &Collector{request: Request{ID: hex.EncodeToString(id[:]), Transport: transport, UA: bounded(ua, 1024), RequestedAt: time.Now().Unix(), Coverage: "unavailable", CoverageReason: "not_generated"}, observed: map[string]Node{}, emitted: []Node{}}
}
func (c *Collector) SetUser(id int64, email string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.request.UserID = id
	if email != "" {
		c.request.EmailAtEvent = bounded(email, 254)
	}
}
func (c *Collector) Observe(node Node) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	node = safeNode(node)
	c.observed[nodeKey(node.ServerType, node.ServerID)] = node
}
func (c *Collector) Override(node Node) { c.Observe(node) }
func (c *Collector) Observed(serverType string, id int64) (Node, bool) {
	if c == nil {
		return Node{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	n, ok := c.observed[nodeKey(serverType, id)]
	return n, ok
}
func (c *Collector) Emit(serverType string, id int64, host, port string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	n, ok := c.observed[nodeKey(serverType, id)]
	if !ok {
		n = Node{ServerType: serverType, ServerID: id, Source: "unavailable"}
		c.request.Coverage = "partial"
		c.request.CoverageReason = "runtime_provenance_missing"
	}
	n.DeliveredHost = host
	n.Port = port
	n = safeNode(n)
	if n.DeliveredHost == "" || n.Port == "" || n.ServerType == "unknown" {
		c.request.Coverage = "partial"
		c.request.CoverageReason = "endpoint_metadata_incomplete"
	}
	if len(c.emitted) >= 2000 {
		c.request.Coverage = "partial"
		c.request.CoverageReason = "node_limit"
		return
	}
	c.emitted = append(c.emitted, n)
}
func (c *Collector) Coverage(coverage, reason string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.request.Coverage == "partial" && coverage == "complete" {
		return
	}
	c.request.Coverage = coverage
	c.request.CoverageReason = reason
}
func (c *Collector) Finish(status int, bytes int64, writeError string) (Request, []Node) {
	if c == nil {
		return Request{}, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	r := c.request
	r.CompletedAt = time.Now().Unix()
	r.Status = status
	r.BytesWritten = bytes
	r.WriteError = writeError
	r.ResponseComplete = status >= 200 && status < 300 && writeError == "" && (r.Coverage == "complete" || r.Coverage == "partial")
	if status == 304 {
		r.Coverage = "not_emitted"
		r.CoverageReason = "not_modified"
		r.ResponseComplete = false
		c.emitted = nil
	} else if status < 200 || status >= 300 || writeError != "" {
		r.ResponseComplete = false
		if r.Coverage == "unavailable" {
			r.CoverageReason = "request_failed"
		}
	}
	r.NodeCount = len(c.emitted)
	return r, append([]Node(nil), c.emitted...)
}

// Revision is an observed delivery snapshot hash, not the admin editor's
// optimistic-lock revision. Only the digest is retained, never raw input.
func Revision(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func nodeKey(kind string, id int64) string { return fmt.Sprintf("%s:%d", kind, id) }
func bounded(value string, limit int) string {
	r := []rune(strings.TrimSpace(value))
	if len(r) > limit {
		r = r[:limit]
	}
	return string(r)
}
func safeHost(host string) string {
	host = strings.TrimSpace(host)
	if !addrutil.IsIPOrHostname(host) {
		return ""
	}
	return bounded(host, 255)
}

var safePort = regexp.MustCompile(`^[0-9,\-]{1,64}$`)
var safeRevision = regexp.MustCompile(`^[a-f0-9]{64}$`)

func safeNode(n Node) Node {
	switch n.ServerType {
	case "shadowsocks", "vmess", "vless", "trojan", "tuic", "hysteria", "hysteria2", "anytls", "v2node", "juicity", "mieru", "socks", "http":
	default:
		n.ServerType = "unknown"
	}
	n.SourceHost = safeHost(n.SourceHost)
	n.DeliveredHost = safeHost(n.DeliveredHost)
	if !safePort.MatchString(n.Port) {
		n.Port = ""
	}
	if !safeRevision.MatchString(n.RuleRevision) {
		n.RuleRevision = ""
	}
	switch n.Source {
	case "default", "policy", "split", "support_override", "provider", "extra", "unavailable":
	default:
		n.Source = "unavailable"
	}
	if n.Resolved {
		_, err := netip.ParseAddr(n.DeliveredHost)
		n.Resolved = err == nil && n.SourceHost != n.DeliveredHost
	}
	return n
}
