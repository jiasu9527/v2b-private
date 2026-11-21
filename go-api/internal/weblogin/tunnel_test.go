package weblogin

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sync/atomic"
	"testing"
	"time"
)

func testTarget(t *testing.T) *url.URL {
	t.Helper()
	addr, err := ParseTarget("https://admin.example.com/login")
	if err != nil {
		t.Fatal(err)
	}
	return addr
}

func TestTunnelRejectsMixedAndNonPublicAnswersBeforeListening(t *testing.T) {
	for _, tt := range []struct {
		ips  []netip.Addr
		want error
	}{
		{[]netip.Addr{netip.MustParseAddr("127.0.0.1")}, ErrBlockedTarget},
		{[]netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("fd00::1")}, ErrBlockedTarget},
		{[]netip.Addr{netip.MustParseAddr("2606:4700:4700::1111"), netip.MustParseAddr("100.100.100.200")}, ErrBlockedTarget},
		{[]netip.Addr{netip.MustParseAddr("2606:4700:4700::1111")}, ErrUnavailable},
		{nil, ErrUnavailable},
	} {
		tun, err := openTunnel(context.Background(), testTarget(t), func(context.Context, string, string) ([]netip.Addr, error) {
			return tt.ips, nil
		}, func(context.Context, string, string) (net.Conn, error) {
			t.Error("attempted a network connection before URL/DNS screening")
			return nil, ErrUnavailable
		})
		if tun != nil {
			tun.Close()
			t.Errorf("unexpectedly opened tunnel for %v", tt.ips)
		}
		if !errors.Is(err, tt.want) {
			t.Errorf("openTunnel for %v: got %v want %v", tt.ips, err, tt.want)
		}
	}
}

func TestTunnelPinsAddressAndCancelsHijackedConnections(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var lookups, dials atomic.Int32
	tun, err := openTunnel(ctx, testTarget(t), func(_ context.Context, network, host string) ([]netip.Addr, error) {
		lookups.Add(1)
		if network != "ip" || host != "admin.example.com." {
			t.Errorf("wrong resolver input: %s %s", network, host)
		}
		return []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("2606:4700:4700::1111")}, nil
	}, func(_ context.Context, network, address string) (net.Conn, error) {
		dials.Add(1)
		if network != "tcp4" || address != "8.8.8.8:443" {
			t.Errorf("wrong pinned dial: %s %s", network, address)
		}
		client, remote := net.Pipe()
		go func() {
			defer remote.Close()
			_, _ = io.Copy(remote, remote)
		}()
		return client, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer tun.Close()

	for _, tt := range []struct {
		method, authority, headers string
	}{
		{"GET", "https://admin.example.com", ""},
		{"CONNECT", "admin.example.com:80", ""},
		{"CONNECT", "other.example.com:443", ""},
		{"CONNECT", "127.0.0.1:443", ""},
		{"CONNECT", "admin.example.com:443", "Content-Length: 5\r\n"},
	} {
		c, _, code := proxyRequest(t, tun, tt.method, tt.authority, tt.headers, "")
		_ = c.Close()
		if code != http.StatusForbidden {
			t.Errorf("%s %s: got code %d", tt.method, tt.authority, code)
		}
	}
	if dials.Load() != 0 {
		t.Fatal("proxy contacted a destination for a rejected request")
	}

	c, reader, code := proxyRequest(t, tun, "CONNECT", "admin.example.com:443", "", "ping")
	defer c.Close()
	if code != http.StatusOK {
		t.Fatalf("approved CONNECT failed with %d", code)
	}
	got := make([]byte, 4)
	if _, err = io.ReadFull(reader, got); err != nil || string(got) != "ping" {
		t.Fatalf("CONNECT did not forward already-buffered bytes: %v %q", err, got)
	}
	if lookups.Load() != 1 || dials.Load() != 1 {
		t.Fatalf("lookup/dial counts: got %d/%d", lookups.Load(), dials.Load())
	}
	cancel()
	select {
	case <-tun.done:
	case <-time.After(time.Second):
		t.Fatal("tunnel did not close on context cancellation")
	}
	_, err = reader.ReadByte()
	if !errors.Is(err, io.EOF) {
		t.Fatalf("hijacked browser connection not closed: %v", err)
	}
}

func proxyRequest(t *testing.T, tun *Tunnel, method, authority, extraHeaders, payload string) (net.Conn, *bufio.Reader, int) {
	t.Helper()
	u, err := url.Parse(tun.URL())
	if err != nil {
		t.Fatal(err)
	}
	client, err := net.DialTimeout("tcp4", u.Host, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = client.SetDeadline(time.Now().Add(2 * time.Second))
	host := authority
	if method != http.MethodConnect {
		u, err := url.Parse(authority)
		if err != nil {
			t.Fatal(err)
		}
		host = u.Host
	}
	_, err = fmt.Fprintf(client, "%s %s HTTP/1.1\r\nHost: %s\r\n%s\r\n%s", method, authority, host, extraHeaders, payload)
	if err != nil {
		_ = client.Close()
		t.Fatal(err)
	}
	reader := bufio.NewReader(client)
	response, err := http.ReadResponse(reader, &http.Request{Method: method})
	if err != nil {
		_ = client.Close()
		t.Fatal(err)
	}
	return client, reader, response.StatusCode
}
