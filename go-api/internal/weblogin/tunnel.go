package weblogin

import (
	"context"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sync"
	"time"
)

const (
	lookupTimeout     = 5 * time.Second
	dialTimeout       = 5 * time.Second
	localSetupTimeout = 4 * time.Second
	maxTunnelLifetime = 90 * time.Second
	maxAnswers        = 32
	maxActive         = 12
	maxAttempts       = 96
	maxAccepted       = 128
)

type resolutionFunc func(context.Context, string, string) ([]netip.Addr, error)
type connectionFunc func(context.Context, string, string) (net.Conn, error)

// Bound all accepted clients, not just valid CONNECT requests. Otherwise
// incomplete headers could occupy an unbounded number of proxy goroutines.
type admissionListener struct {
	net.Listener
	slots    chan struct{}
	accepted int // Accept is called only by this tunnel's HTTP serve loop.
}

func (l *admissionListener) Accept() (net.Conn, error) {
	for {
		if l.accepted >= maxAccepted {
			return nil, net.ErrClosed
		}
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		l.accepted++
		select {
		case l.slots <- struct{}{}:
			return &admittedConn{Conn: conn, release: func() { <-l.slots }}, nil
		default:
			_ = conn.Close()
		}
	}
}

type admittedConn struct {
	net.Conn
	release func()
	once    sync.Once
}

func (c *admittedConn) Close() error {
	defer c.once.Do(c.release)
	return c.Conn.Close()
}

// Tunnel is a loopback CONNECT server pinned to validated public DNS answers.
// The one permitted authority is fixed at construction time, never taken from
// browser inputs. It does not decrypt TLS, extract credentials, or log traffic.
type Tunnel struct {
	authority string
	listener  net.Listener
	server    *http.Server
	ctx       context.Context
	cancel    context.CancelFunc
	dial      connectionFunc
	addresses []netip.Addr
	done      chan struct{}
	closeOnce sync.Once
	mu        sync.Mutex
	stopped   bool
	attempts  int
	streams   map[net.Conn]net.Conn
	slots     chan struct{}
}

// OpenTunnel resolves once before constructing the server. All answers must be
// safe; a public A record cannot hide a private AAAA answer or the reverse.
// For the first release only public IPv4 is dialed so translation or split
// tunnel IPv6 addresses cannot reach the host network.
func OpenTunnel(ctx context.Context, target *url.URL) (*Tunnel, error) {
	dialer := &net.Dialer{}
	return openTunnel(ctx, target, net.DefaultResolver.LookupNetIP, dialer.DialContext)
}

func openTunnel(ctx context.Context, target *url.URL, resolve resolutionFunc, dial connectionFunc) (*Tunnel, error) {
	if target == nil {
		return nil, ErrInvalidTarget
	}
	target, err := ParseTarget(target.String())
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, maxTunnelLifetime)
	lookupCtx, stopLookup := context.WithTimeout(ctx, lookupTimeout)
	// A terminating dot prevents resolver search-list expansion.
	resolved, err := resolve(lookupCtx, "ip", target.Hostname()+".")
	stopLookup()
	if err != nil || len(resolved) == 0 {
		cancel()
		return nil, ErrUnavailable
	}
	if len(resolved) > maxAnswers {
		cancel()
		return nil, ErrBlockedTarget
	}
	seen := make(map[netip.Addr]bool)
	addresses := make([]netip.Addr, 0, len(resolved))
	for _, address := range resolved {
		if !isPublicAnswer(address) {
			cancel()
			return nil, ErrBlockedTarget
		}
		ip := address.Unmap()
		if ip.Is4() && !seen[ip] {
			seen[ip] = true
			addresses = append(addresses, ip)
		}
	}
	if len(addresses) == 0 || ctx.Err() != nil {
		cancel()
		return nil, ErrUnavailable
	}
	rawListener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		cancel()
		return nil, ErrUnavailable
	}
	listener := &admissionListener{Listener: rawListener, slots: make(chan struct{}, 2*maxActive)}
	t := &Tunnel{
		authority: net.JoinHostPort(target.Hostname(), "443"),
		listener:  listener, ctx: ctx, cancel: cancel, dial: dial, addresses: addresses,
		done: make(chan struct{}), streams: make(map[net.Conn]net.Conn), slots: make(chan struct{}, maxActive),
	}
	t.server = &http.Server{
		Handler:           http.HandlerFunc(t.handle),
		ReadHeaderTimeout: localSetupTimeout,
		WriteTimeout:      localSetupTimeout + dialTimeout,
		IdleTimeout:       localSetupTimeout,
		MaxHeaderBytes:    4 << 10,
		ErrorLog:          log.New(io.Discard, "", 0),
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	t.server.SetKeepAlivesEnabled(false)
	go func() {
		_ = t.server.Serve(listener)
		t.Close()
	}()
	go func() {
		select {
		case <-ctx.Done():
			t.Close()
		case <-t.done:
		}
	}()
	return t, nil
}

func (t *Tunnel) URL() string { return "http://" + t.listener.Addr().String() }

// Close ends both pending HTTP requests and established CONNECT streams,
// including hijacked connections that net/http.Server.Close does not track.
func (t *Tunnel) Close() {
	t.closeOnce.Do(func() {
		t.mu.Lock()
		t.stopped = true
		for client, upstream := range t.streams {
			_ = client.Close()
			_ = upstream.Close()
		}
		t.mu.Unlock()
		t.cancel()
		_ = t.listener.Close()
		_ = t.server.Close()
		close(t.done)
	})
}

func (t *Tunnel) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodConnect || r.RequestURI != t.authority || r.Host != t.authority ||
		r.ContentLength > 0 || len(r.TransferEncoding) != 0 {
		http.Error(w, "CONNECT target not allowed", http.StatusForbidden)
		return
	}
	select {
	case t.slots <- struct{}{}:
		defer func() { <-t.slots }()
	default:
		http.Error(w, "connection limit reached", http.StatusTooManyRequests)
		return
	}
	t.mu.Lock()
	allowed := !t.stopped && t.attempts < maxAttempts
	if allowed {
		t.attempts++
	}
	t.mu.Unlock()
	if !allowed {
		http.Error(w, "connection limit reached", http.StatusTooManyRequests)
		return
	}

	upstream, err := t.dialPinned(r.Context())
	if err != nil {
		http.Error(w, "website connection unavailable", http.StatusBadGateway)
		return
	}
	defer upstream.Close()
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "website connection unavailable", http.StatusBadGateway)
		return
	}
	client, rw, err := hijacker.Hijack()
	if err != nil {
		return
	}
	defer client.Close()
	t.mu.Lock()
	if t.stopped {
		t.mu.Unlock()
		return
	}
	t.streams[client] = upstream
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		delete(t.streams, client)
		t.mu.Unlock()
	}()

	_ = client.SetDeadline(time.Now().Add(localSetupTimeout))
	if _, err = rw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	if err = rw.Flush(); err != nil {
		return
	}
	_ = client.SetDeadline(time.Time{})

	// Preserve bytes coalesced with the CONNECT headers. Cancel the other
	// copy direction as soon as either side closes the tunnel.
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(upstream, rw.Reader)
		_ = upstream.Close()
		_ = client.Close()
		close(done)
	}()
	_, _ = io.Copy(client, upstream)
	_ = client.Close()
	_ = upstream.Close()
	<-done
}

func (t *Tunnel) dialPinned(ctx context.Context) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	for i, ip := range t.addresses {
		if ctx.Err() != nil {
			break
		}
		// Give additional pinned answers a chance if the first address stalls.
		deadline, _ := ctx.Deadline()
		reqCtx, stop := context.WithTimeout(ctx, time.Until(deadline)/time.Duration(len(t.addresses)-i))
		conn, err := t.dial(reqCtx, "tcp4", net.JoinHostPort(ip.String(), "443"))
		stop()
		if err == nil {
			return conn, nil
		}
	}
	return nil, ErrUnavailable
}
