package probeagent

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"
)

type fakeResolver struct {
	addrs []netip.Addr
	err   error
}

func (r fakeResolver) LookupNetIP(_ context.Context, _, _ string) ([]netip.Addr, error) {
	return r.addrs, r.err
}

func TestTCPCheckerUsesAnyResolvedAddress(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		c, err := listener.Accept()
		if err == nil {
			_ = c.Close()
		}
	}()
	port := listener.Addr().(*net.TCPAddr).Port
	checker := TCPChecker{Resolver: fakeResolver{addrs: []netip.Addr{netip.MustParseAddr("127.0.0.2"), netip.MustParseAddr("127.0.0.1")}}}
	result := checker.Check(context.Background(), "probe.test", port, time.Second)
	if !result.Success || result.ResolvedIP != "127.0.0.1" || result.LatencyMS == nil {
		t.Fatalf("result = %#v", result)
	}
}

func TestTCPCheckerClassifiesTimeout(t *testing.T) {
	checker := TCPChecker{Resolver: fakeResolver{addrs: []netip.Addr{netip.MustParseAddr("203.0.113.1")}}}
	result := checker.Check(context.Background(), "probe.test", 9, time.Nanosecond)
	if result.Success || result.Error != "timeout" {
		t.Fatalf("result = %#v", result)
	}
}

func TestTCPCheckerClassifiesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	checker := TCPChecker{Resolver: fakeResolver{err: errors.New("resolver should not run")}}
	result := checker.Check(ctx, "probe.test", 443, time.Second)
	if result.Success || result.Error != "cancelled" {
		t.Fatalf("result = %#v", result)
	}
}
