package probeagent

import (
	"context"
	"net"
	"net/netip"
	"strconv"
	"time"
)

type CheckResult struct {
	Success    bool
	LatencyMS  *int64
	Error      string
	ResolvedIP string
}

type IPResolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

type TCPChecker struct {
	Resolver IPResolver
	Dialer   interface {
		DialContext(context.Context, string, string) (net.Conn, error)
	}
}

func (c TCPChecker) Check(ctx context.Context, host string, port int, timeout time.Duration) CheckResult {
	if err := ctx.Err(); err != nil {
		return failedCheck(err, "")
	}
	checkCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var addrs []netip.Addr
	if ip := net.ParseIP(host); ip != nil {
		addrs = []netip.Addr{netip.MustParseAddr(ip.String())}
	} else {
		resolver := c.Resolver
		if resolver == nil {
			resolver = net.DefaultResolver
		}
		var err error
		addrs, err = resolver.LookupNetIP(checkCtx, "ip", host)
		if err != nil {
			return failedCheck(checkCtx.Err(), "")
		}
	}
	dialer := c.Dialer
	if dialer == nil {
		dialer = &net.Dialer{}
	}
	lastIP := ""
	for index, addr := range addrs {
		if err := checkCtx.Err(); err != nil {
			return failedCheck(err, lastIP)
		}
		ip := addr.String()
		lastIP = ip
		remaining := time.Until(deadline(checkCtx))
		attemptCtx, attemptCancel := context.WithTimeout(checkCtx, remaining/time.Duration(len(addrs)-index))
		started := time.Now()
		conn, err := dialer.DialContext(attemptCtx, "tcp", net.JoinHostPort(ip, strconv.Itoa(port)))
		attemptCancel()
		if err == nil {
			_ = conn.Close()
			latency := time.Since(started).Milliseconds()
			return CheckResult{Success: true, LatencyMS: &latency, ResolvedIP: ip}
		}
	}
	return failedCheck(checkCtx.Err(), lastIP)
}

func deadline(ctx context.Context) time.Time {
	deadline, _ := ctx.Deadline()
	return deadline
}

func failedCheck(err error, ip string) CheckResult {
	kind := "connect_error"
	if err == context.Canceled {
		kind = "cancelled"
	}
	if err == context.DeadlineExceeded {
		kind = "timeout"
	}
	return CheckResult{Error: kind, ResolvedIP: ip}
}
